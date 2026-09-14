package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/scheduler"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// followUpRequiredFieldYAML declares a required field with no default, so a
// task that never carried it cannot satisfy the contract.
const followUpRequiredFieldYAML = `name: release-notes
description: Needs a ticket.
fields:
  - name: ticket
    required: true
steps:
  - {id: gate, type: manual, instructions: review}
`

// followUpEnumFieldYAML declares a required enum with a default, and its one
// step exits 0 only when the value the round renders is that default.
const followUpEnumFieldYAML = `name: deploy-check
description: Exits 0 only when the environment is staging.
fields:
  - name: environment
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
steps:
  - id: check
    type: command
    max_retries: 0
    run: 'exit {{ if eq (index .Task.Fields "environment") "staging" }}0{{ else }}3{{ end }}'
`

// newFollowUpFieldsHarness is newTaskHarness with a workflow registry that
// has a global directory, which the plain harness lacks: a follow-up that
// names a workflow declaring fields needs one to be found.
func newFollowUpFieldsHarness(t *testing.T, withRunner bool) *taskHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	dataDir := t.TempDir()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return "/nonexistent/codex-not-here" }),
	)
	cfg := config.Default
	globalDir := filepath.Join(t.TempDir(), "workflows")
	writeWorkflowFile(t, globalDir, "release-notes", followUpRequiredFieldYAML)
	writeWorkflowFile(t, globalDir, "deploy-check", followUpEnumFieldYAML)
	workflows := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: []string{"claude", "codex"}}, nil)
	workflows.ReloadGlobal()
	for _, name := range []string{"release-notes", "deploy-check"} {
		if entry, ok := workflows.Lookup(0, name); !ok || !entry.Valid() {
			t.Fatalf("test workflow %q did not load: %+v", name, entry)
		}
	}

	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    cfg,
		Worktrees: wt,
		Agents:    reg,
		DataDir:   dataDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	sched := scheduler.New(scheduler.Deps{
		Store:    st,
		Config:   cfg,
		Admitter: runner,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	st.SetEventHook(func(e *store.Event) {
		if scheduler.WakeOn(e) {
			sched.Wake()
		}
	})
	if withRunner {
		runner.Start(t.Context())
		sched.Start(t.Context())
		t.Cleanup(sched.Stop)
		t.Cleanup(runner.Stop)
	}
	s := New(Deps{
		Token:       testToken,
		Config:      cfg,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
		Runner:      runner,
		WakeRunner:  sched.Wake,
		Workflows:   workflows,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h := &taskHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		runner:         runner,
		sched:          sched,
	}
	h.repo = testrepo.Init(t, "main")
	project := h.mustCreate(t, map[string]any{"path": h.repo})
	h.projectID = int64(project["id"].(float64))
	return h
}

// doneTaskWithFields creates a task on the default workflow with the given
// open field map and walks it straight to `done`, without a runner.
func doneTaskWithFields(t *testing.T, h *taskHarness, fields map[string]string) taskResponse {
	t.Helper()
	task := h.createTask(t, map[string]any{"title": "follow-up fields", "fields": fields})
	setState(t, h, task.ID, store.TaskRunning)
	setState(t, h, task.ID, store.TaskDone)
	return task
}

// wantFollowUpRefused asserts a refused follow-up left the task where it was.
func wantFollowUpRefused(t *testing.T, h *taskHarness, id int64, resp *http.Response, body []byte, mention string) {
	t.Helper()
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if msg := decodeError(t, body).Message; !strings.Contains(msg, mention) {
		t.Errorf("message = %q, want it to mention %q", msg, mention)
	}
	stored, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.State != store.TaskDone || stored.PendingFollowUp != nil {
		t.Errorf("a refused follow-up moved the task: %s, pending %+v", stored.State, stored.PendingFollowUp)
	}
}

// TestFollowUpWorkflowRefusesAMissingRequiredField is issue #369: a follow-up
// naming a workflow is held to that workflow's declared fields exactly as
// POST /v1/tasks holds a new task to them (§8.1.2). The task never carried
// `ticket` and the declaration has no default, so the run could only render
// an empty `.Task.Fields.ticket` — a 400 in front of the person asking.
func TestFollowUpWorkflowRefusesAMissingRequiredField(t *testing.T) {
	h := newFollowUpFieldsHarness(t, false)
	task := doneTaskWithFields(t, h, map[string]string{"owner": "alice"})

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"workflow": "release-notes"})
	wantFollowUpRefused(t, h, task.ID, resp, body, `field "ticket" is required`)
}

// TestFollowUpWorkflowRefusesAnEnumViolation is the enum leg of issue #369.
// `environment: qa` was legal on the task (the map is open and the default
// workflow declares nothing), but the follow-up's workflow declares it an
// enum without that member (§8.1.2, task 058).
func TestFollowUpWorkflowRefusesAnEnumViolation(t *testing.T) {
	h := newFollowUpFieldsHarness(t, false)
	task := doneTaskWithFields(t, h, map[string]string{"environment": "qa"})

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"workflow": "deploy-check"})
	wantFollowUpRefused(t, h, task.ID, resp, body, "environment")
}

// TestFollowUpWorkflowFillsARequiredDefault is the defaults leg of issue #369,
// driven through the real scheduler so what is proved is what the round
// renders, not where the value is stored. The task never carried
// `environment`; the follow-up workflow declares it required with default
// `staging`, so its step must see `staging` and exit 0, returning the
// aborted-origin task to `aborted`. Without the default it renders "" and
// exits 3, blocking the task.
func TestFollowUpWorkflowFillsARequiredDefault(t *testing.T) {
	runDeployCheckFollowUp(t, map[string]string{"owner": "alice"})
}

// TestFollowUpWorkflowKeepsAStoredField is the control for the test above: a
// value the task already carries is what the round renders, and a default
// never replaces a present key (§8.1.2). It passes on its own, which is what
// shows the step's template is sound and the defaults leg fails for the
// missing default alone.
func TestFollowUpWorkflowKeepsAStoredField(t *testing.T) {
	runDeployCheckFollowUp(t, map[string]string{"environment": "staging"})
}

// runDeployCheckFollowUp runs deploy-check as a follow-up on an aborted task
// carrying fields and requires it to return the task to `aborted`.
func runDeployCheckFollowUp(t *testing.T, fields map[string]string) {
	t.Helper()
	h := newFollowUpFieldsHarness(t, true)
	// Created paused and cancelled, so it is `aborted` without ever running.
	task := h.createTask(t, map[string]any{
		"title": "follow-up default", "paused": true, "fields": fields,
	})
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/cancel", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d %s", resp.StatusCode, body)
	}

	resp, body = h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"workflow": "deploy-check"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow_up: %d %s", resp.StatusCode, body)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		got := h.getTask(t, task.ID)
		if got.State == string(store.TaskAborted) {
			break
		}
		if got.State == string(store.TaskBlocked) {
			t.Fatalf("follow-up blocked (%s): its step did not render the required default `staging` for .Task.Fields.environment",
				sval(got.BlockReason))
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %d stuck in %q", task.ID, got.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.PendingFollowUp != nil {
		t.Errorf("the follow-up request survived its Restore: %+v", stored.PendingFollowUp)
	}
}
