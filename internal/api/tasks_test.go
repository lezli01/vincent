package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/lezli01/vincent/internal/worktree"
)

// taskHarness is a full M1 spine: API server + store + worktrees + runner +
// the claude adapter pointed at the fakeagent binary via the config knob.
type taskHarness struct {
	*projectHarness
	runner    *taskrun.Runner
	sched     *scheduler.Scheduler
	repo      string
	projectID int64
}

// newTaskHarness builds the spine. agentTimeout 0 keeps the default; the
// runner and scheduler are started unless withRunner is false.
func newTaskHarness(t *testing.T, agentTimeout time.Duration, withRunner bool) *taskHarness {
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
	cfg := func() config.Config {
		c := config.Default()
		if agentTimeout > 0 {
			c.Defaults.AgentTimeout = config.Duration(agentTimeout)
		}
		return c
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
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h := &taskHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		runner:         runner,
		sched:          sched,
	}

	h.repo = testrepo.Init(t, "main")
	resp, body := h.doJSON(t, http.MethodPost, "/v1/projects", map[string]any{"path": h.repo})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register project: %d %s", resp.StatusCode, body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("project body: %v", err)
	}
	h.projectID = p.ID
	return h
}

func (h *taskHarness) createTask(t *testing.T, req map[string]any) taskResponse {
	t.Helper()
	if _, ok := req["project_id"]; !ok {
		req["project_id"] = h.projectID
	}
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: %d %s", resp.StatusCode, body)
	}
	var tr taskResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("task body: %v", err)
	}
	return tr
}

// sval renders an optional string field for assertions.
func sval(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// waitForState polls the task until it reaches want or the deadline hits.
func (h *taskHarness) waitForState(t *testing.T, id int64, want string) taskResponse {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get task: %d %s", resp.StatusCode, body)
		}
		var tr taskResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			t.Fatalf("task body: %v", err)
		}
		if tr.State == want {
			return tr
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %d stuck in %q (block_reason %s), want %q", id, tr.State, sval(tr.BlockReason), want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTaskEndToEnd(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	h := newTaskHarness(t, 0, true)

	created := h.createTask(t, map[string]any{
		"title":       "Add login page",
		"description": "Build the login page.",
		"model":       "sonnet",
		"effort":      "high",
	})
	if created.State != "queued" && created.State != "running" {
		t.Errorf("created state = %q", created.State)
	}
	wantBranch := fmt.Sprintf("vincent/%d-add-login-page", created.ID)
	if created.BranchName != wantBranch {
		t.Errorf("branch = %q, want %q", created.BranchName, wantBranch)
	}
	if created.ModelOverride == nil || *created.ModelOverride != "sonnet" ||
		created.EffortOverride == nil || *created.EffortOverride != "high" {
		t.Errorf("overrides not echoed: %+v", created)
	}

	done := h.waitForState(t, created.ID, "done")
	if done.WorktreePath == nil || done.StartedAt == nil || done.FinishedAt == nil {
		t.Errorf("done task missing worktree/timestamps: %+v", done)
	}
	if len(done.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(done.Steps))
	}
	step := done.Steps[0]
	if step.State != "succeeded" || step.Attempt != 1 || step.StepType != "agent" {
		t.Errorf("step = %+v", step)
	}
	if step.Agent == nil || *step.Agent != "claude" ||
		step.Model == nil || *step.Model != "sonnet" ||
		step.Effort == nil || *step.Effort != "high" {
		t.Errorf("override did not round-trip into the StepRun: %+v", step)
	}
	if step.ExitCode == nil || *step.ExitCode != 0 ||
		step.InputTokens == nil || *step.InputTokens != 100 ||
		step.OutputTokens == nil || *step.OutputTokens != 42 ||
		step.CostUSD == nil || *step.CostUSD != 0.0123 {
		t.Errorf("usage/exit not recorded: %+v", step)
	}
	if !strings.Contains(step.ResultSummary, "Add login page") {
		t.Errorf("result summary %q does not echo the prompt", step.ResultSummary)
	}

	// Branch was created in the registered repo and survives (§10).
	testrepo.Run(t, h.repo, "rev-parse", "--verify", "refs/heads/"+wantBranch)

	// Transcript: verbatim agent lines + namespaced vincent lines, ranged.
	resp, body := h.doJSON(t, http.MethodGet,
		fmt.Sprintf("/v1/tasks/%d/steps/%d/transcript", created.ID, step.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transcript: %d %s", resp.StatusCode, body)
	}
	transcript := string(body)
	for _, want := range []string{"vincent.step_started", "vincent.step_finished", `"type":"result"`, "fake_marker"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing %q", want)
		}
	}
	next := resp.Header.Get("X-Next-Offset")
	if next != fmt.Sprint(len(body)) {
		t.Errorf("X-Next-Offset = %s, want %d", next, len(body))
	}
	resp, body = h.doJSON(t, http.MethodGet,
		fmt.Sprintf("/v1/tasks/%d/steps/%d/transcript?offset=%s", created.ID, step.ID, next), nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Errorf("ranged read from the end: %d, %d bytes; want 200 with empty body", resp.StatusCode, len(body))
	}
	if step.TranscriptPath == nil {
		t.Fatal("transcript_path not set")
	}
	if _, err := os.Stat(*step.TranscriptPath); err != nil {
		t.Errorf("transcript file: %v", err)
	}

	// Diff: the fakeagent edit to a tracked file shows against merge-base.
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "fakeagent was here") || !strings.Contains(string(body), "README.md") {
		t.Errorf("diff missing the fakeagent edit:\n%s", body)
	}

	// Steps endpoint mirrors the detail view.
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/steps", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("steps: %d %s", resp.StatusCode, body)
	}
	var steps []stepRunResponse
	if err := json.Unmarshal(body, &steps); err != nil || len(steps) != 1 {
		t.Errorf("steps endpoint: %v, %d rows", err, len(steps))
	}
}

func TestTaskBlockedOnAgentError(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "error-event")
	h := newTaskHarness(t, 0, true)
	created := h.createTask(t, map[string]any{"title": "doomed"})
	blocked := h.waitForState(t, created.ID, "blocked")
	if sval(blocked.BlockReason) != "agent_error" {
		t.Errorf("block_reason = %s, want agent_error", sval(blocked.BlockReason))
	}
	if len(blocked.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(blocked.Steps))
	}
	step := blocked.Steps[0]
	if step.State != "failed" || sval(step.FailureReason) != "agent_error" {
		t.Errorf("step state=%s reason=%s, want failed/agent_error", step.State, sval(step.FailureReason))
	}
	if !strings.Contains(step.ResultSummary, "fake agent failed on purpose") {
		t.Errorf("summary %q missing the error text", step.ResultSummary)
	}
}

func TestTaskBlockedOnNonzeroExit(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newTaskHarness(t, 0, true)
	created := h.createTask(t, map[string]any{"title": "crasher"})
	blocked := h.waitForState(t, created.ID, "blocked")
	if sval(blocked.BlockReason) != "nonzero_exit" {
		t.Errorf("block_reason = %s, want nonzero_exit", sval(blocked.BlockReason))
	}
	step := blocked.Steps[0]
	if step.ExitCode == nil || *step.ExitCode != 3 {
		t.Errorf("exit_code = %v, want 3", step.ExitCode)
	}
}

func TestTaskTimeout(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newTaskHarness(t, 500*time.Millisecond, true)
	created := h.createTask(t, map[string]any{"title": "sleeper"})
	blocked := h.waitForState(t, created.ID, "blocked")
	if sval(blocked.BlockReason) != "timeout" {
		t.Errorf("block_reason = %s, want timeout (defaults.agent_timeout enforced in M1)", sval(blocked.BlockReason))
	}
	step := blocked.Steps[0]
	if sval(step.FailureReason) != "timeout" {
		t.Errorf("step failure_reason = %s, want timeout", sval(step.FailureReason))
	}
}

func TestRunnerStopInterrupts(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newTaskHarness(t, 0, true)
	created := h.createTask(t, map[string]any{"title": "interrupted"})
	// Wait until the agent process is live (step run has a pid) so the stop
	// deterministically interrupts a running step, not worktree creation.
	deadline := time.Now().Add(60 * time.Second)
	for {
		runs, err := h.store.ListStepRuns(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("ListStepRuns: %v", err)
		}
		if len(runs) == 1 && runs[0].PID != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent process never came up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.sched.Stop()
	h.runner.Stop()
	got, err := h.store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// An interruption is not a failure (§7.2, §12.4): the task returns to
	// queued and the attempt re-runs on the next admission.
	if got.State != store.TaskQueued || got.BlockReason != "" {
		t.Errorf("after stop: %s/%q, want queued with no block reason", got.State, got.BlockReason)
	}
	runs, err := h.store.ListStepRuns(t.Context(), created.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListStepRuns: %v, %d rows", err, len(runs))
	}
	if runs[0].State != store.StepInterrupted {
		t.Errorf("step run = %s, want interrupted", runs[0].State)
	}
}

func TestTaskCreateValidation(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	tests := []struct {
		name string
		req  map[string]any
		want string // substring of the error message
	}{
		{"missing title", map[string]any{}, "title is required"},
		{"unknown project", map[string]any{"project_id": int64(9999), "title": "x"}, "project 9999 not found"},
		{"unknown workflow", map[string]any{"title": "x", "workflow": "feature-pr"}, "not found for project"},
		{"unknown agent", map[string]any{"title": "x", "agent": "gemini"}, "available: claude"},
		{"missing base branch", map[string]any{"title": "x", "base_branch": "nope"}, "does not resolve"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			if _, ok := req["project_id"]; !ok {
				req["project_id"] = h.projectID
			}
			resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", req)
			wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("message %s missing %q", body, tt.want)
			}
		})
	}
}

// TestTaskCreateCatalogValidation drives the §8.2 cross-catalog check with
// the task-level override applied to the adhoc workflow's claude step:
// a value from another adapter's catalog is a 400, an unknown value creates
// the task with a warning on the 201 body (T2.11).
func TestTaskCreateCatalogValidation(t *testing.T) {
	h := newTaskHarness(t, 0, false)

	// minimal is a codex-only effort; the adhoc step resolves to claude.
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "title": "bad effort", "effort": "minimal",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "codex") {
		t.Errorf("message %s must name the owning adapter", body)
	}

	// A model no catalog knows passes with a warning.
	created := h.createTask(t, map[string]any{"title": "warned", "model": "made-up-model-x"})
	if len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], "made-up-model-x") {
		t.Errorf("warnings = %v, want one naming the unknown model", created.Warnings)
	}

	// A curated value is silent.
	created = h.createTask(t, map[string]any{"title": "clean", "model": "opus", "effort": "max"})
	if len(created.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for curated values", created.Warnings)
	}
}

func TestTaskListFilters(t *testing.T) {
	h := newTaskHarness(t, 0, false) // no runner: tasks stay queued
	first := h.createTask(t, map[string]any{"title": "one"})
	h.createTask(t, map[string]any{"title": "two"})

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks?project_id=%d&state=queued", h.projectID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var tasks []taskResponse
	if err := json.Unmarshal(body, &tasks); err != nil || len(tasks) != 2 {
		t.Fatalf("list = %v, %d rows, want 2", err, len(tasks))
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks?limit=1&offset=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list paged: %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &tasks); err != nil || len(tasks) != 1 || tasks[0].ID != first.ID {
		t.Errorf("paged list = %d rows (first id %v), want the older task", len(tasks), tasks)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks?limit=x", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

// TestTaskDetailCarriesProjectName: the list endpoint has always carried
// project_name, and the detail endpoint silently did not — so every client's
// TaskDetail.ProjectName was empty and `vincent task show` printed a blank
// project. Found by walking the README quickstart (T4.4); asserted on both
// endpoints so the two cannot drift apart again.
func TestTaskDetailCarriesProjectName(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	created := h.createTask(t, map[string]any{"title": "named"})

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d %s", resp.StatusCode, body)
	}
	var detail taskResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("detail body: %v", err)
	}
	if detail.ProjectName == "" {
		t.Error("detail project_name is empty; clients cannot name the project without a second request")
	}

	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var list []listTaskResponse
	if err := json.Unmarshal(body, &list); err != nil || len(list) == 0 {
		t.Fatalf("list body: %v (%d rows)", err, len(list))
	}
	if list[0].ProjectName != detail.ProjectName {
		t.Errorf("list says %q, detail says %q; the two endpoints disagree",
			list[0].ProjectName, detail.ProjectName)
	}
}

func TestTaskDiffWithoutWorktree(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	created := h.createTask(t, map[string]any{"title": "not started"})
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", created.ID), nil)
	wantError(t, resp, body, http.StatusConflict, CodeInvalidState)
}

func TestTranscriptNotFound(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	created := h.createTask(t, map[string]any{"title": "no steps"})
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/steps/42/transcript", created.ID), nil)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)

	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks/9999", nil)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}

func TestInfoReportsAgents(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/info", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
	var info struct {
		Agents []AgentStatus `json:"agents"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("info body: %v", err)
	}
	if len(info.Agents) != 2 || info.Agents[0].Name != "claude" || !info.Agents[0].Available || info.Agents[1].Available {
		t.Errorf("agents = %+v, want claude available + codex not", info.Agents)
	}
}

// advanceRemoteBase pushes a commit to the remote's copy of branch without
// moving the local ref, which is what a merged pull request looks like from a
// daemon whose human has not pulled since (task 056).
func advanceRemoteBase(t *testing.T, repo, branch string) {
	t.Helper()
	remote := testrepo.InitBare(t)
	testrepo.Run(t, repo, "remote", "add", "origin", remote)
	testrepo.Run(t, repo, "push", "-q", "-u", "origin", branch)
	testrepo.Run(t, repo, "checkout", "-q", "-b", "upstream-work")
	testrepo.WriteFile(t, repo, "upstream.txt", "somebody else's merged work\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "upstream commit")
	testrepo.Run(t, repo, "push", "-q", "origin", "upstream-work:refs/heads/"+branch)
	testrepo.Run(t, repo, "checkout", "-q", branch)
	testrepo.Run(t, repo, "branch", "-q", "-D", "upstream-work")
}

// TestTaskDiffUsesTheRecordedBaseSHA: once a task branch starts at a fetched
// upstream tip, merge-base against `base_branch` resolves to the *stale* local
// commit, and the reviewer reads every upstream change the fetch brought in as
// the task's own work. Both halves are asserted — the recorded SHA gives the
// task's own diff, and clearing it reproduces the fault the column fixes.
func TestTaskDiffUsesTheRecordedBaseSHA(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	advanceRemoteBase(t, h.repo, stored.BaseBranch)

	created, err := h.wt.CreateAndClaim(t.Context(), h.repo, task.ID,
		stored.BranchName, stored.BaseBranch, true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if created.BaseSHA == "" {
		t.Fatal("fixture is wrong: no base SHA was recorded")
	}
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	testrepo.WriteFile(t, created.Path, "task-work.txt", "what the agent wrote\n")
	testrepo.Run(t, created.Path, "add", ".")
	testrepo.Run(t, created.Path, "commit", "-q", "-m", "the task's own commit")

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "task-work.txt") {
		t.Errorf("diff is missing the task's own file:\n%s", body)
	}
	if strings.Contains(string(body), "upstream.txt") {
		t.Errorf("diff presents upstream work as the task's:\n%s", body)
	}

	// The pre-056 row shape: no recorded base, so the branch name is the fork
	// point again — and the reviewer sees the upstream commit.
	var none string
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, nil, &none); err != nil {
		t.Fatalf("clear base_sha: %v", err)
	}
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "upstream.txt") {
		t.Errorf("without a base SHA the stale merge-base should reappear:\n%s", body)
	}
}
