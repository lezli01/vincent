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
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/scheduler"
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
	sched     *scheduler.Scheduler
	repo      string
	dataDir   string
	projectID int64
}

func newEngineHarness(t *testing.T) *engineHarness { return newEngineHarnessWith(t, nil) }

// newEngineHarnessWith is newEngineHarness with the daemon config adjusted —
// the transcript cap has to be shrunk to be testable, and a test-only
// override hatch was exactly what the PR V decision rejected in favour of a
// real config field.
func newEngineHarnessWith(t *testing.T, mutate func(*config.Config)) *engineHarness {
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
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	runner := New(Deps{
		Store:     st,
		Config:    func() config.Config { return cfg },
		Worktrees: worktree.NewManager(git, dataDir),
		Agents: agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return fake }),
			cursor.New(func() string { return fake }),
		),
		DataDir: dataDir,
		Logger:  log,
	})
	return &engineHarness{store: st, runner: runner, repo: repo, dataDir: dataDir, projectID: project.ID}
}

// start runs the runner and a real scheduler for the test's lifetime, so
// tasks reach the engine the same way they do in the daemon.
func (h *engineHarness) start(t *testing.T) {
	t.Helper()
	sched := scheduler.New(scheduler.Deps{
		Store:    h.store,
		Config:   config.Default,
		Admitter: h.runner,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.store.SetEventHook(func(e *store.Event) {
		if scheduler.WakeOn(e) {
			sched.Wake()
		}
	})
	h.runner.Start(t.Context())
	sched.Start(t.Context())
	h.sched = sched
	t.Cleanup(h.runner.Stop)
	t.Cleanup(sched.Stop)
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
		if _, _, err := workflow.Parse([]byte(src), workflow.Options{}); err != nil {
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

// waitForWorktree polls until the task's worktree path is recorded.
func (h *engineHarness) waitForWorktree(t *testing.T, id int64) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if task.WorktreePath != "" {
			return task.WorktreePath
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("worktree never created")
	return ""
}

// waitForFile polls until path exists — the sign a step's process is really
// executing.
func (h *engineHarness) waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
}

// waitForActorExit polls until the runner no longer holds a live run.
func (h *engineHarness) waitForActorExit(t *testing.T, id int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !h.runner.Running(id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("actor still live after cancel")
}

// TestEngineCancelOfLiveStepInterrupts covers §6's cancel against a live
// process: the terminated attempt records interrupted/canceled — not a
// failure the retry loop would re-run, spawning fresh processes for a task
// that is already aborted.
func TestEngineCancelOfLiveStepInterrupts(t *testing.T) {
	h := newEngineHarness(t)
	long := script(
		"echo started > cancel-marker.txt\nsleep 30",
		`"started" | Out-File -Encoding ascii cancel-marker.txt`+"\nStart-Sleep -Seconds 30",
	)
	after := script(`echo after > after.txt`, `"after" | Out-File -Encoding ascii after.txt`)
	snapshot := "name: cancelable\nsteps:\n" +
		commandStep("long", long, "max_retries: 3") + commandStep("after", after)
	task := h.createTask(t, snapshot)
	h.start(t)

	h.waitForState(t, task.ID, store.TaskRunning)
	wt := h.waitForWorktree(t, task.ID)
	h.waitForFile(t, filepath.Join(wt, "cancel-marker.txt"))

	canceled, err := h.runner.Cancel(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.State != store.TaskAborted {
		t.Fatalf("state after cancel = %s, want aborted", canceled.State)
	}
	h.waitForActorExit(t, task.ID)

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1 — a canceled step must not retry or reach the next step", len(runs))
	}
	if runs[0].State != store.StepInterrupted || runs[0].FailureReason != ReasonCanceled {
		t.Fatalf("attempt = %s/%q, want interrupted/canceled", runs[0].State, runs[0].FailureReason)
	}
	if _, err := os.Stat(filepath.Join(wt, "after.txt")); err == nil {
		t.Error("the step after the canceled one ran")
	}
}

// TestEngineHumanRetryResetsBudget: a human retry grants the full budget
// again (§6) while attempt numbers keep climbing, so transcript files never
// collide.
func TestEngineHumanRetryResetsBudget(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: flaky\nsteps:\n" + commandStep("flaky", "exit 3", "max_retries: 1")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 2 {
		t.Fatalf("attempts before retry = %d, want 2", len(runs))
	}

	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	reblocked := h.waitForState(t, task.ID, store.TaskBlocked)
	if reblocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("block_reason = %q, want nonzero_exit", reblocked.BlockReason)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 4 {
		t.Fatalf("attempts after retry = %d, want 4 — the retry grants a fresh budget", len(runs))
	}
	paths := map[string]bool{}
	for i, run := range runs {
		if run.Attempt != i+1 {
			t.Errorf("attempt %d numbered %d, want %d — numbering must stay monotonic", i, run.Attempt, i+1)
		}
		paths[run.TranscriptPath] = true
	}
	if len(paths) != len(runs) {
		t.Errorf("transcript paths = %d unique, want %d — colliding names truncate history", len(paths), len(runs))
	}
}

// TestEngineFailureBlockSurvivesReadmission: the §8.4 failure block is
// rebuilt from the step's failed row when a fresh actor resumes the step —
// the human-retry path the block exists for.
func TestEngineFailureBlockSurvivesReadmission(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newEngineHarness(t)
	snapshot := `name: retried
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: "Do the work"
`
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}
	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	h.waitForState(t, task.ID, store.TaskBlocked)

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2", len(runs))
	}
	if !strings.Contains(runs[1].ResultSummary, "previous-attempt-failure") {
		t.Errorf("re-admitted retry prompt %q carries no failure block", runs[1].ResultSummary)
	}
	if !strings.Contains(runs[1].ResultSummary, ReasonNonzeroExit) {
		t.Errorf("re-admitted retry prompt %q names no failure reason", runs[1].ResultSummary)
	}
}

// TestEngineRunsCodexStep drives a codex step through the real engine: the
// registry hands the run to the codex adapter, the fake binary answers in
// the codex dialect (argv-sniffed on `exec`), and the step row records the
// resolved codex triple with nil cost (§9.3; T2.9/T2.11).
func TestEngineRunsCodexStep(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, `name: codex-run
steps:
  - id: build
    type: agent
    agent: codex
    model: gpt-5.6-sol
    effort: xhigh
    max_retries: 0
    prompt: |
      Implement {{.Task.Title}} with codex
`)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1: %+v", len(runs), runs)
	}
	run := runs[0]
	if run.State != store.StepSucceeded {
		t.Fatalf("step = %s (%s), want succeeded", run.State, run.FailureReason)
	}
	if run.Agent != "codex" || run.Model != "gpt-5.6-sol" || run.Effort != "xhigh" {
		t.Errorf("resolved triple = %s/%s/%s, want codex/gpt-5.6-sol/xhigh", run.Agent, run.Model, run.Effort)
	}
	// The rendered prompt reached codex via stdin (the dialect echoes it).
	if !strings.Contains(run.ResultSummary, "Implement engine test with codex") {
		t.Errorf("result summary %q does not contain the rendered prompt", run.ResultSummary)
	}
	if run.InputTokens == nil || *run.InputTokens != 100 {
		t.Errorf("input tokens = %v, want 100 from turn.completed", run.InputTokens)
	}
	if run.CostUSD != nil {
		t.Errorf("cost = %v, want nil (codex reports no cost)", *run.CostUSD)
	}
}

// TestEngineRunsCursorStep is the third adapter's end-to-end through the real
// engine: rendered prompt over stdin, cursor's own stream normalized, the
// resolved triple recorded. Effort is deliberately absent from the workflow —
// cursor has none (§9.7) — and must stay empty on the recorded run.
func TestEngineRunsCursorStep(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, `name: cursor-run
steps:
  - id: build
    type: agent
    agent: cursor
    model: claude-sonnet-5-thinking-high
    max_retries: 0
    prompt: |
      Implement {{.Task.Title}} with cursor
`)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1: %+v", len(runs), runs)
	}
	run := runs[0]
	if run.State != store.StepSucceeded {
		t.Fatalf("step = %s (%s), want succeeded", run.State, run.FailureReason)
	}
	if run.Agent != "cursor" || run.Model != "claude-sonnet-5-thinking-high" || run.Effort != "" {
		t.Errorf("resolved triple = %s/%s/%s, want cursor/claude-sonnet-5-thinking-high/(none)",
			run.Agent, run.Model, run.Effort)
	}
	if !strings.Contains(run.ResultSummary, "Implement engine test with cursor") {
		t.Errorf("result summary %q does not contain the rendered prompt", run.ResultSummary)
	}
	if run.InputTokens == nil || *run.InputTokens != 100 {
		t.Errorf("input tokens = %v, want 100 from the camelCase usage keys", run.InputTokens)
	}
	if run.CostUSD != nil {
		t.Errorf("cost = %v, want nil (cursor reports no cost)", *run.CostUSD)
	}
}

// TestEngineTranscriptLimitFailsTheStep is the T4.3 done-when for the cap, at
// a shrunk threshold: an agent that will not stop talking is killed and the
// attempt fails `transcript_limit` rather than filling the disk (§12.3, §18).
func TestEngineTranscriptLimitFailsTheStep(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.TranscriptMaxBytes = 8 << 10 // 8KB — the flood passes it in moments
	})
	t.Setenv("FAKEAGENT_SCENARIO", "flood")
	task := h.createTask(t, `name: floody
steps:
  - id: talk
    type: agent
    max_retries: 0
    prompt: say too much
`)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked {
		t.Fatalf("task = %s (%s), want blocked", final.State, final.BlockReason)
	}
	if final.BlockReason != ReasonTranscriptLimit {
		t.Errorf("block reason = %q, want %q", final.BlockReason, ReasonTranscriptLimit)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].FailureReason != ReasonTranscriptLimit {
		t.Fatalf("step runs = %+v, want one failed %s", runs, ReasonTranscriptLimit)
	}

	// The partial transcript is kept — the lines that got there are exactly
	// what explains the runaway — and it ends with the annotation saying why
	// it stops.
	body, err := os.ReadFile(runs[0].TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(body), "vincent.transcript_limit") {
		t.Error("the transcript does not record why it stopped")
	}
	// Bounded by roughly one line past the cap, not by the flood's appetite.
	if len(body) > 64<<10 {
		t.Errorf("transcript is %d bytes with an 8KB cap; the cap did not bound it", len(body))
	}
}

// TestEngineRestrictedUnsupportedIsItsOwnReason: a restricted step whose
// adapter cannot restrict on this platform must not be reported as a missing
// adapter, or the user reinstalls a CLI that is present and working (§9.4).
//
// The capability is forced rather than waited for, so the classification is
// proven on every OS CI runs and not only on Windows.
func TestEngineRestrictedUnsupportedIsItsOwnReason(t *testing.T) {
	t.Cleanup(cursor.SetSandboxAvailable(false))
	h := newEngineHarness(t)
	task := h.createTask(t, `name: cursor-restricted
steps:
  - id: build
    type: agent
    agent: cursor
    permission_mode: restricted
    max_retries: 0
    prompt: do the thing
`)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked — a restricted step must never run unrestricted", final.State)
	}
	if final.BlockReason != ReasonRestrictedUnsupported {
		t.Errorf("block reason = %q, want %q", final.BlockReason, ReasonRestrictedUnsupported)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].FailureReason != ReasonRestrictedUnsupported {
		t.Errorf("step runs = %+v, want one failed %s", runs, ReasonRestrictedUnsupported)
	}
}
