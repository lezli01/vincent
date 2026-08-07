package taskrun

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// engineHarness runs the real engine against a real store, a real git repo,
// and the fake agent binary.
type engineHarness struct {
	store     *store.Store
	runner    *Runner
	repo      string
	dataDir   string
	projectID int64
}

func newEngineHarness(t *testing.T) *engineHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	git := gitx.New()
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := New(Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: worktree.NewManager(git, dataDir),
		Agents:    agent.NewRegistry(claude.New(func() string { return fake })),
		DataDir:   dataDir,
		Logger:    log,
	})
	return &engineHarness{store: st, runner: runner, repo: repo, dataDir: dataDir, projectID: project.ID}
}

// start runs the admission loop for the test's lifetime.
func (h *engineHarness) start(t *testing.T) {
	t.Helper()
	h.runner.Start(t.Context())
	t.Cleanup(h.runner.Stop)
}

// createTask inserts a queued task carrying the given workflow snapshot.
func (h *engineHarness) createTask(t *testing.T, snapshot string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID:        h.projectID,
		Title:            "engine test",
		Description:      "a task",
		WorkflowName:     "test",
		WorkflowSnapshot: snapshot,
		BaseBranch:       "main",
		State:            store.TaskQueued,
	}
	if err := h.store.CreateTask(t.Context(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task.BranchName = worktree.BranchName(task.ID, task.Title)
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	return task
}

// waitForState polls until the task reaches one of want, or the test fails.
func (h *engineHarness) waitForState(t *testing.T, id int64, want ...store.TaskState) *store.Task {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last *store.Task
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		last = task
		for _, w := range want {
			if task.State == w {
				return task
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d stuck in %s (block_reason %q), want one of %v", id, last.State, last.BlockReason, want)
	return nil
}

func (h *engineHarness) stepRuns(t *testing.T, id int64) []store.StepRun {
	t.Helper()
	runs, err := h.store.ListStepRuns(t.Context(), id)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	return runs
}

func (h *engineHarness) eventTypes(t *testing.T, id int64) []string {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out []string
	for _, e := range events {
		if e.TaskID != nil && *e.TaskID == id {
			out = append(out, e.Type)
		}
	}
	return out
}

func hasEvent(types []string, want string) bool {
	for _, tp := range types {
		if tp == want {
			return true
		}
	}
	return false
}

// script picks the shell syntax for the platform the test runs on; command
// steps are the workflow author's responsibility to port (§8.3), and these
// tests are their own authors.
func script(posix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return posix
}

// commandStep renders a command step as YAML. The command goes in a literal
// block scalar: a PowerShell line often starts with a quote, which as a
// plain `run:` value would be parsed as a quoted scalar and make the rest of
// the line a YAML error.
func commandStep(id, cmd string, extra ...string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  - id: %s\n    type: command\n", id)
	for _, line := range extra {
		fmt.Fprintf(&sb, "    %s\n", line)
	}
	sb.WriteString("    run: |\n")
	for _, line := range strings.Split(cmd, "\n") {
		fmt.Fprintf(&sb, "      %s\n", line)
	}
	return sb.String()
}

// multiStepSnapshot is an agent step followed by a command step writing a
// marker file from the §8.5 environment.
func multiStepSnapshot(cmd string) string {
	return `name: multi
steps:
  - id: implement
    type: agent
    prompt: |
      Implement {{.Task.Title}} on {{.Task.BranchName}}
` + commandStep("publish", cmd)
}

// chainedSnapshot is two command steps, the second reading the first's
// result through `.Steps` (§8.4).
func chainedSnapshot(first, second string) string {
	return "name: chained\nsteps:\n" + commandStep("first", first) + commandStep("second", second)
}

// TestSnapshotsParseOnEveryPlatform parses both platform variants of every
// snapshot these tests build, so a YAML mistake in the Windows branch fails
// on POSIX too instead of only in CI.
func TestSnapshotsParseOnEveryPlatform(t *testing.T) {
	snapshots := map[string]string{
		"multi/posix":   multiStepSnapshot(markerCmdPosix),
		"multi/windows": multiStepSnapshot(markerCmdWindows),
		"chained/posix": chainedSnapshot(echoCmdPosix, relayCmdPosix),
		"chained/win":   chainedSnapshot(echoCmdWindows, relayCmdWindows),
	}
	for name, src := range snapshots {
		if _, err := workflow.Parse([]byte(src), workflow.Options{}); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, src)
		}
	}
}

// Command bodies used by the engine tests, per platform.
const (
	markerCmdPosix   = `echo "task $VINCENT_TASK_ID step $VINCENT_STEP_ID" > marker.txt`
	markerCmdWindows = `"task $env:VINCENT_TASK_ID step $env:VINCENT_STEP_ID" | Out-File -Encoding ascii marker.txt`

	echoCmdPosix   = `echo first-step-output`
	echoCmdWindows = `Write-Output first-step-output`

	relayCmdPosix   = `echo "{{ (index .Steps "first").Result }}" > relay.txt`
	relayCmdWindows = `"{{ (index .Steps "first").Result }}" | Out-File -Encoding ascii relay.txt`
)

func TestEngineRunsMultiStepWorkflow(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, multiStepSnapshot(script(markerCmdPosix, markerCmdWindows)))
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	if done.CurrentStep != 2 {
		t.Errorf("current_step = %d, want 2", done.CurrentStep)
	}
	if done.FinishedAt == nil {
		t.Error("finished_at not stamped")
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("step runs = %d, want 2: %+v", len(runs), runs)
	}
	for _, run := range runs {
		if run.State != store.StepSucceeded {
			t.Errorf("step %s = %s, want succeeded (%s)", run.StepID, run.State, run.FailureReason)
		}
		if run.Attempt != 1 {
			t.Errorf("step %s attempt = %d, want 1", run.StepID, run.Attempt)
		}
		if run.TranscriptPath == "" {
			t.Errorf("step %s has no transcript", run.StepID)
		} else if _, err := os.Stat(run.TranscriptPath); err != nil {
			t.Errorf("step %s transcript missing: %v", run.StepID, err)
		}
	}
	// The agent step records its resolved selection and usage.
	if runs[0].Agent != DefaultAgent && runs[0].Agent != "claude" {
		t.Errorf("agent = %q, want claude", runs[0].Agent)
	}
	if runs[0].InputTokens == nil || runs[0].CostUSD == nil {
		t.Errorf("usage not recorded: tokens=%v cost=%v", runs[0].InputTokens, runs[0].CostUSD)
	}
	// The rendered prompt reached the agent (fakeagent echoes it back).
	if !strings.Contains(runs[0].ResultSummary, "Implement engine test on vincent/") {
		t.Errorf("result summary %q does not contain the rendered prompt", runs[0].ResultSummary)
	}

	// The command step ran in the worktree with the §8.5 environment.
	marker, err := os.ReadFile(filepath.Join(done.WorktreePath, "marker.txt"))
	if err != nil {
		t.Fatalf("command step did not write its file: %v", err)
	}
	if !strings.Contains(string(marker), "step publish") {
		t.Errorf("marker = %q, want the step id from the environment", marker)
	}

	types := h.eventTypes(t, task.ID)
	for _, want := range []string{store.EventTaskStateChanged, eventStepStarted, eventStepFinished} {
		if !hasEvent(types, want) {
			t.Errorf("events %v missing %s", types, want)
		}
	}
}

func TestEngineStopsAtManualGate(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: gated
steps:
  - id: gate
    type: manual
    instructions: Inspect task {{.Task.ID}} before publishing.
` + commandStep("after", "echo never")
	task := h.createTask(t, snapshot)
	h.start(t)

	gated := h.waitForState(t, task.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if gated.State != store.TaskAwaitingGate {
		t.Fatalf("task = %s (%s), want awaiting_gate", gated.State, gated.BlockReason)
	}
	if gated.CurrentStep != 0 {
		t.Errorf("current_step = %d, want 0 (the gate has not been approved)", gated.CurrentStep)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want the gate row only", len(runs))
	}
	if runs[0].StepType != "manual" || runs[0].State != store.StepRunning {
		t.Errorf("gate row = %+v, want a running manual row", runs[0])
	}
	if !strings.Contains(runs[0].ResultSummary, "Inspect task") {
		t.Errorf("gate instructions = %q, want the rendered text", runs[0].ResultSummary)
	}
	if !hasEvent(h.eventTypes(t, task.ID), eventGateWaiting) {
		t.Error("no gate.waiting event")
	}
	// The actor exited: a parked task holds no slot (§11).
	deadline := time.Now().Add(5 * time.Second)
	for h.runner.Running(task.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.runner.Running(task.ID) {
		t.Error("actor still live while the task waits at a gate")
	}
}

func TestEngineRetriesThenBlocks(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: flaky\nsteps:\n" + commandStep("flaky", "exit 3", "max_retries: 1")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", len(runs))
	}
	for i, run := range runs {
		if run.Attempt != i+1 {
			t.Errorf("attempt %d numbered %d, want %d", i, run.Attempt, i+1)
		}
		if run.State != store.StepFailed {
			t.Errorf("attempt %d = %s, want failed", run.Attempt, run.State)
		}
		if run.ExitCode == nil || *run.ExitCode != 3 {
			t.Errorf("attempt %d exit code = %v, want 3", run.Attempt, run.ExitCode)
		}
	}
	if !hasEvent(h.eventTypes(t, task.ID), eventStepRetrying) {
		t.Error("no step.retrying event")
	}
	// Both attempts kept their own transcript (append-only history).
	if runs[0].TranscriptPath == runs[1].TranscriptPath {
		t.Error("both attempts share one transcript file")
	}
}

func TestEngineCheckFailureFailsStep(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: checked\nsteps:\n" + commandStep("build",
		script("echo built", "Write-Output built"), "max_retries: 0", "check: exit 7")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonCheckFailed {
		t.Fatalf("task = %s/%q, want blocked/check_failed", blocked.State, blocked.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1 (max_retries 0)", len(runs))
	}
	if runs[0].CheckExitCode == nil || *runs[0].CheckExitCode != 7 {
		t.Errorf("check_exit_code = %v, want 7", runs[0].CheckExitCode)
	}
	if runs[0].ExitCode == nil || *runs[0].ExitCode != 0 {
		t.Errorf("step exit code = %v, want 0 — the body succeeded", runs[0].ExitCode)
	}
}

func TestEngineTemplateErrorFailsBeforeSpawn(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: typo
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: "Work on {{.Task.Titel}}"
`
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonTemplateError {
		t.Fatalf("task = %s/%q, want blocked/template_error", blocked.State, blocked.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1", len(runs))
	}
	if runs[0].PID != nil {
		t.Error("a process was spawned despite the render failure")
	}
}

func TestEngineCommandTimeoutKills(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: slow\nsteps:\n" + commandStep("slow",
		script("sleep 30", "Start-Sleep -Seconds 30"), "max_retries: 0", "timeout: 1s")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonTimeout {
		t.Fatalf("task = %s/%q, want blocked/timeout", blocked.State, blocked.BlockReason)
	}
}

// TestEngineStepResultsFlowIntoTemplates covers §8.4's `.Steps`: a later
// step reads an earlier step's result.
func TestEngineStepResultsFlowIntoTemplates(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, chainedSnapshot(
		script(echoCmdPosix, echoCmdWindows),
		script(relayCmdPosix, relayCmdWindows),
	))
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	relay, err := os.ReadFile(filepath.Join(done.WorktreePath, "relay.txt"))
	if err != nil {
		t.Fatalf("second step did not write its file: %v", err)
	}
	if !strings.Contains(string(relay), "first-step-output") {
		t.Errorf("relay = %q, want the first step's result", relay)
	}
}

func TestEnginePinnedShellUnavailable(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: pinned\nsteps:\n" + commandStep("run", "echo hi", "max_retries: 0", "shell: cmd")
	if _, err := lookupShell("cmd"); err == nil {
		t.Skip("cmd is available on this platform; the unavailable-shell path needs a missing shell")
	}
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonShellUnavailable {
		t.Fatalf("task = %s/%q, want blocked/shell_unavailable", blocked.State, blocked.BlockReason)
	}
}

// TestEngineRetryPromptCarriesFailureBlock proves §8.4's failure block
// reaches the agent on a retry: the fake agent echoes its prompt back.
func TestEngineRetryPromptCarriesFailureBlock(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newEngineHarness(t)
	snapshot := `name: retrying
steps:
  - id: implement
    type: agent
    max_retries: 1
    prompt: "Do the work"
`
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2", len(runs))
	}
	if strings.Contains(runs[0].ResultSummary, "previous-attempt-failure") {
		t.Error("the first attempt's prompt carried a failure block")
	}
	if !strings.Contains(runs[1].ResultSummary, "previous-attempt-failure") {
		t.Errorf("retry prompt %q carries no failure block", runs[1].ResultSummary)
	}
	if !strings.Contains(runs[1].ResultSummary, ReasonNonzeroExit) {
		t.Errorf("retry prompt %q does not name the previous failure reason", runs[1].ResultSummary)
	}
}
