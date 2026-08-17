package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// newInputHarness is a workflow harness whose registry and catalog are both
// live, which is what the task-013 gate needs: §8.2 reads the catalogs and
// POST /v1/tasks reads the probed verdict. claudePath decides what the probe
// finds — the fake agent (input supported) or nothing at all (unknown).
func newInputHarness(t *testing.T, claudePath string) *workflowHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "workflows")
	reg := agent.NewRegistry(
		claude.New(func() string { return claudePath }),
		codex.New(func() string { return "/nonexistent/codex-not-here" }),
	)
	cache := agent.NewCatalogCache(reg)
	wfreg := workflow.NewRegistry(globalDir, workflow.Options{
		KnownAgents: reg.Names(),
		Catalogs:    cache.Catalogs,
	}, nil)
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Workflows:   wfreg,
		Agents:      reg,
		Catalog:     cache,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &workflowHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		reg:            wfreg,
		globalDir:      globalDir,
	}
}

// requiringWorkflow leaves its requiring step's agent to the task, which is
// what makes the task-level choice the thing being gated (decision 6).
const requiringWorkflow = "name: interactive\n" +
	"steps:\n" +
	"  - {id: ask, type: agent, on_input: require, prompt: what should I build?}\n"

// TestWorkflowListReportsRequiresInput is the reporting half: the daemon
// derives the flag, the client reads it (§13.2, task 013).
func TestWorkflowListReportsRequiresInput(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "interactive", requiringWorkflow)
	writeWorkflowFile(t, h.globalDir, "pinned",
		"name: pinned\nsteps:\n"+
			"  - {id: ask, type: agent, agent: claude, on_input: require, prompt: hi}\n")
	h.reg.ReloadGlobal()

	list := decodeWorkflowList(t, mustGet(t, h, "/v1/workflows"))
	if !findWorkflow(t, list, "interactive").RequiresInput {
		t.Error("a workflow with an unpinned requiring step does not report requires_input")
	}
	// Its requiring step pins a capable agent, so the task's choice is
	// unconstrained and the flag must stay off.
	if findWorkflow(t, list, "pinned").RequiresInput {
		t.Error("a workflow whose requiring step pins its own agent constrains the picker")
	}
	if findWorkflow(t, list, "adhoc").RequiresInput {
		t.Error("the built-in adhoc workflow reports requires_input")
	}
}

// TestTaskCreateRefusesIncapableAgent is the enforcement half: the workflow
// is listed and valid, and only the agent override makes it unrunnable.
func TestTaskCreateRefusesIncapableAgent(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "interactive", requiringWorkflow)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "no", "workflow": "interactive", "agent": "codex",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	for _, want := range []string{"ask", "codex", "mid-run input"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("message %s missing %q", body, want)
		}
	}

	// The same workflow on the capable agent creates, so the gate refuses one
	// selection rather than the workflow.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "yes", "workflow": "interactive", "agent": "claude",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claude task: %d %s", resp.StatusCode, body)
	}
}

// TestTaskCreateAllowsUnknownVerdict is decision 5: a probe that cannot
// answer is not evidence, and §9.6's degrade-never-block rule outranks the
// gate. claude resolves to nothing here, so its support is unknowable.
func TestTaskCreateAllowsUnknownVerdict(t *testing.T) {
	h := newInputHarness(t, "/nonexistent/claude-not-here")
	writeWorkflowFile(t, h.globalDir, "interactive", requiringWorkflow)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "maybe", "workflow": "interactive", "agent": "claude",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("uninstalled agent refused at creation: %d %s", resp.StatusCode, body)
	}
}

// A workflow pinning an adapter that can never take input fails §8.2, so it
// never reaches the creation gate at all — it is listed with its error.
func TestRequireOnIncapableAgentFailsValidation(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "broken",
		"name: broken\nsteps:\n"+
			"  - {id: ask, type: agent, agent: codex, on_input: require, prompt: hi}\n")
	h.reg.ReloadGlobal()

	entry := findWorkflow(t, decodeWorkflowList(t, mustGet(t, h, "/v1/workflows")), "broken")
	if entry.Error == nil {
		t.Fatal("require on codex validated clean")
	}
	if !strings.Contains(*entry.Error, "mid-run input") {
		t.Errorf("error %q does not explain the capability", *entry.Error)
	}
}

// TestRetryRefusesIncapableAgent is the repair-path half (task 013): a task
// blocked with input_unsupported is refused a retry that would only reproduce
// the block, and the message names what to fix. The environment is the repair
// — install or downgrade the agent — so the verdict is re-taken per retry.
func TestRetryRefusesIncapableAgent(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	task := &store.Task{
		ProjectID:        h.mustProjectID(t),
		Title:            "blocked",
		WorkflowName:     "interactive",
		WorkflowSnapshot: requiringWorkflow,
		AgentOverride:    "codex",
		BaseBranch:       "main",
		BranchName:       "vincent/1-blocked",
		State:            store.TaskBlocked,
		BlockReason:      "input_unsupported",
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	resp, body := h.doJSON(t, http.MethodPost,
		"/v1/tasks/"+strconv.FormatInt(task.ID, 10)+"/retry", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "codex") {
		t.Errorf("message %s does not name the agent to change", body)
	}
}

// mustProjectID registers a throwaway repo and returns its project id.
func (h *workflowHarness) mustProjectID(t *testing.T) int64 {
	t.Helper()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	return int64(p["id"].(float64))
}
