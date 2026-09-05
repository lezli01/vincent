package taskrun

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for the injection connection
)

func recoverStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "recover.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return st, p.ID
}

func recoverTask(t *testing.T, st *store.Store, projectID int64, state store.TaskState) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: projectID, Title: "t-" + string(state), WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", State: state,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, task.Title), nil }
	if err := st.CreateTask(context.Background(), task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// journalRun writes a `running` step run the way a pre-0013 daemon left one:
// created, then PID + proc start time journaled, with no native identity. It
// is the legacy shape the ±5 s tolerance still covers.
func journalRun(t *testing.T, st *store.Store, taskID int64, pid *int, startedAt *time.Time) *store.StepRun {
	t.Helper()
	return journalRunWithIdentity(t, st, taskID, pid, startedAt, nil)
}

// journalRunWithIdentity writes a `running` step run as the engine writes one
// today: PID, spawn stamp and the platform-native identity, all in one update
// (§12.4, issue #149).
func journalRunWithIdentity(
	t *testing.T, st *store.Store, taskID int64, pid *int, startedAt *time.Time, identity *string,
) *store.StepRun {
	t.Helper()
	run := &store.StepRun{
		TaskID: taskID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := st.CreateStepRun(context.Background(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	run.PID, run.ProcStartedAt, run.ProcIdentity = pid, startedAt, identity
	if err := st.UpdateStepRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	return run
}

// liveIdentity reads the identity of a process the test just started, the way
// the engine reads it at spawn.
func liveIdentity(t *testing.T, pid int) string {
	t.Helper()
	id, err := procx.Identity(pid)
	if err != nil {
		t.Fatalf("procx.Identity(%d): %v", pid, err)
	}
	return id
}

func recoverSleeper(t *testing.T) (*exec.Cmd, *procx.Proc) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 300")
	} else {
		cmd = exec.Command("sleep", "300")
	}
	proc, err := procx.Start(cmd)
	if err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	return cmd, proc
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecoverRequeuesThroughTheFSM(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	running := recoverTask(t, st, projectID, store.TaskRunning)
	waiting := recoverTask(t, st, projectID, store.TaskAwaitingInput)
	queued := recoverTask(t, st, projectID, store.TaskQueued)
	run := journalRun(t, st, running.ID, nil, nil) // crashed before the PID write

	n, err := Recover(ctx, st, discardLog())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 2 {
		t.Errorf("requeued = %d, want 2 (running + awaiting_input)", n)
	}
	for _, id := range []int64{running.ID, waiting.ID} {
		if got, _ := st.GetTask(ctx, id); got.State != store.TaskQueued {
			t.Errorf("task %d = %s, want queued", id, got.State)
		}
	}
	if got, _ := st.GetTask(ctx, queued.ID); got.State != store.TaskQueued {
		t.Errorf("queued task disturbed: %s", got.State)
	}

	got, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepInterrupted || got.FailureReason != ReasonInterrupted ||
		got.FinishedAt == nil || got.PID != nil {
		t.Errorf("step run = %+v, want interrupted/interrupted, finished, pid cleared", got)
	}

	// The re-queues went through TransitionTask: durable events exist for
	// SSE clients that reconnect after the crash (§12.4, §13.3).
	evs, err := st.ListEvents(ctx, store.EventFilter{Types: []string{store.EventTaskStateChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("state_changed events = %d, want 2", len(evs))
	}

	// An interruption consumes no retry (§7.2).
	attempts, err := st.CountStepAttempts(ctx, store.StepRef{TaskID: running.ID, StepID: "s"}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 0 || attempts.Last != 1 {
		t.Errorf("attempts = %+v, want Last=1 Failed=0", attempts)
	}
}

// The legacy path: a row with no journaled identity is still judged by the
// ±5 s wall-clock tolerance, and still killed inside it. Rows written before
// migration 0013 — and by any spawn whose identity read failed — must behave
// exactly as they did before issue #149.
func TestRecoverKillsOrphanWhoseStartTimeMatches(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer proc.Release()
	pid := cmd.Process.Pid
	now := time.Now()
	journalRun(t, st, task.ID, &pid, &now)

	if _, err := Recover(context.Background(), st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done: // recovery killed it
	case <-time.After(10 * time.Second):
		_ = proc.Kill()
		t.Fatal("orphan with a matching start time survived recovery")
	}
}

// The other half of the legacy path: outside the tolerance, an identity-less
// row still spares the PID.
func TestRecoverSparesReusedPID(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()
	pid := cmd.Process.Pid
	// The journal claims the process started an hour ago: this PID now
	// belongs to somebody else (the reuse guard's whole reason to exist).
	old := time.Now().Add(-time.Hour)
	run := journalRun(t, st, task.ID, &pid, &old)

	if _, err := Recover(context.Background(), st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if _, err := procx.StartTime(pid); err != nil {
		t.Errorf("innocent process is gone (%v); the mismatched start time must spare it", err)
	}
	// The row is still finalized and the task still re-queued — only the
	// kill is withheld.
	got, _ := st.GetStepRun(context.Background(), run.ID)
	if got.State != store.StepInterrupted {
		t.Errorf("step run = %s, want interrupted", got.State)
	}
	if tk, _ := st.GetTask(context.Background(), task.ID); tk.State != store.TaskQueued {
		t.Errorf("task = %s, want queued", tk.State)
	}
}

func TestRecoverToleratesDeadPID(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	pid := cmd.Process.Pid
	spawned := time.Now()
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	proc.Release()
	journalRun(t, st, task.ID, &pid, &spawned)

	n, err := Recover(context.Background(), st, discardLog())
	if err != nil {
		t.Fatalf("Recover with a dead pid: %v", err)
	}
	if n != 1 {
		t.Errorf("requeued = %d, want 1", n)
	}
	if tk, _ := st.GetTask(context.Background(), task.ID); tk.State != store.TaskQueued {
		t.Errorf("task = %s, want queued", tk.State)
	}
}

// failStepRunFinalize installs a trigger that aborts exactly the write
// recovery uses to terminalize a `running` step run. It goes in over a second
// connection so the store's own single connection (phase 1 decision) is
// untouched, and it is the hermetic stand-in for the storage failure §12.4
// does not discuss: the row cannot be moved out of `running`.
func failStepRunFinalize(t *testing.T, st *store.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", st.Path())
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER vincent_test_block_interrupt
		BEFORE UPDATE OF state ON step_runs
		WHEN NEW.state = 'interrupted'
		BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

// A step run that could not be finalized leaves its task recoverable — it
// must not be re-queued on top of a row that is still durably `running`.
// §12.4 orders the two: the run is finalized as `interrupted`, *then* the
// owning task returns to `queued`. Re-queueing anyway lets the scheduler
// admit a second attempt while the first is still open in the database,
// which breaks the one-active-attempt invariant precisely during a storage
// failure.
func TestRecoverDoesNotRequeuePastAFinalizeFailure(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	task := recoverTask(t, st, projectID, store.TaskRunning)
	run := journalRun(t, st, task.ID, nil, nil) // crashed before the PID write

	failStepRunFinalize(t, st)

	_, _ = Recover(ctx, st, discardLog())

	got, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepRunning {
		t.Fatalf("injection did not hold: step run = %s, want it stuck at running", got.State)
	}
	tk, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if tk.State == store.TaskQueued {
		t.Errorf("task %d re-queued while step run %d is still durably running: "+
			"recovery must not requeue a task whose running step run could not be terminalized (§12.4)",
			task.ID, run.ID)
	}
}

// Not re-queueing is only half of fail-closed. The other half is that the
// daemon hears about it: startup aborts on a recovery it could not complete,
// rather than starting the scheduler over rows it knows are contradictory
// (§12.4, issue #142).
func TestRecoverReturnsTheFinalizeFailure(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	task := recoverTask(t, st, projectID, store.TaskRunning)
	journalRun(t, st, task.ID, nil, nil)

	failStepRunFinalize(t, st)

	n, err := Recover(ctx, st, discardLog())
	if err == nil {
		t.Fatal("Recover returned nil; the daemon would start over unreconciled rows")
	}
	if n != 0 {
		t.Errorf("requeued = %d, want 0 — nothing was reconciled", n)
	}
}

// An `awaiting_input` task's process is alive with its step run open (§7.4).
// Recovery closes the run and re-queues the task in the same commit, exactly
// as it does for `running` — the two travel the same path.
func TestRecoverInterruptsAwaitingInputTogetherWithItsRun(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	task := recoverTask(t, st, projectID, store.TaskAwaitingInput)
	run := journalRun(t, st, task.ID, nil, nil)

	if _, err := Recover(ctx, st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepInterrupted {
		t.Errorf("step run = %s, want interrupted", got.State)
	}
	tk, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if tk.State != store.TaskQueued {
		t.Errorf("task = %s, want queued", tk.State)
	}
	// Leaving awaiting_input discards the pending request with the process
	// that would have answered it (§7.4).
	if tk.PendingInputJSON != "" {
		t.Errorf("pending input survived recovery: %q", tk.PendingInputJSON)
	}
}

// A fan-out lane is a task like any other and is recovered like one — the
// filter default that hides lanes from the board must not hide them here
// (task 014, §7.6). The parent is left alone: `awaiting_children` re-queues
// through ChildrenSettled, not through Interrupt.
// TestRecoverInterruptsTheFanOutParkRow: a parent parked in
// `awaiting_children` owns the `running` row its park opened (§7.6, issue
// #322). No Interrupt transition re-queues that state — the scheduler wakes
// the parent when its subtree settles — so the row falls to the sweep that
// finalizes the leftovers of every other owner, exactly like `awaiting_gate`'s
// manual row. Nothing is killed: a park journals no pid and no container id,
// because there is no process behind it.
//
// After the sweep the round has no open row, so the next merge admission
// inserts a fresh one rather than adopting. That is the gate's precedent and
// costs the round one extra row on the timeline.
func TestRecoverInterruptsTheFanOutParkRow(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	parent := recoverTask(t, st, projectID, store.TaskAwaitingChildren)
	run := &store.StepRun{
		TaskID: parent.ID, StepIndex: 0, StepID: "build", StepType: workflow.StepFanOut,
		Attempt: 1, Iteration: 0, State: store.StepRunning,
	}
	if err := st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	requeued, err := Recover(ctx, st, discardLog())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if requeued != 0 {
		t.Errorf("Recover re-queued %d tasks; a parked parent is woken by its children", requeued)
	}
	got, err := st.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != store.TaskAwaitingChildren {
		t.Errorf("parent state = %s, want %s — recovery moves no parked parent",
			got.State, store.TaskAwaitingChildren)
	}
	after, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if after.State != store.StepInterrupted {
		t.Errorf("park row state = %s, want %s", after.State, store.StepInterrupted)
	}
	if after.PID != nil || after.ContainerID != nil {
		t.Errorf("park row journaled something killable: pid=%v container=%v", after.PID, after.ContainerID)
	}
	ref := store.StepRef{TaskID: parent.ID, StepIndex: 0, StepID: "build", Iteration: 0}
	open, err := st.OpenStepRun(ctx, ref)
	if err != nil {
		t.Fatalf("OpenStepRun: %v", err)
	}
	if open != nil {
		t.Errorf("round 0 still has an open row (%d) after recovery", open.ID)
	}
}

func TestRecoverReconcilesFanOutLanes(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	parent := recoverTask(t, st, projectID, store.TaskAwaitingChildren)
	lane := recoverTask(t, st, projectID, store.TaskRunning)
	stepIndex := 0
	lane.ParentTaskID, lane.ParentStepIndex = &parent.ID, &stepIndex
	lane.LaneID, lane.LaneOrder = "lane-a", 0
	if err := st.UpdateTask(ctx, lane); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	run := journalRun(t, st, lane.ID, nil, nil)

	if _, err := Recover(ctx, st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepInterrupted {
		t.Errorf("lane step run = %s, want interrupted", got.State)
	}
	tk, err := st.GetTask(ctx, lane.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if tk.State != store.TaskQueued {
		t.Errorf("lane = %s, want queued", tk.State)
	}
	if pt, _ := st.GetTask(ctx, parent.ID); pt.State != store.TaskAwaitingChildren {
		t.Errorf("parent = %s, want it left awaiting_children", pt.State)
	}
}

// The identity path's good case: the PID still holds the very process the row
// journaled, so the orphan is tree-killed (§12.4, issue #149).
func TestRecoverKillsOrphanWhoseIdentityMatches(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer proc.Release()
	pid := cmd.Process.Pid
	now := time.Now()
	identity := liveIdentity(t, pid)
	journalRunWithIdentity(t, st, task.ID, &pid, &now, &identity)

	if _, err := Recover(context.Background(), st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done: // recovery killed it
	case <-time.After(10 * time.Second):
		_ = proc.Kill()
		t.Fatal("orphan with a matching identity survived recovery")
	}
}

// The case the ±5 s tolerance could not see and issue #149 exists for: the
// journaled PID is live and its wall-clock spawn stamp is seconds old, but the
// process holding it is not the one the row spawned. Real PID reuse is not
// reproducible on demand; a doctored token is the same comparison with the
// same answer.
func TestRecoverSparesSamePIDWithADifferentIdentity(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()
	pid := cmd.Process.Pid
	// Inside the legacy tolerance in every respect — only the identity says
	// this is somebody else.
	now := time.Now()
	doctored := liveIdentity(t, pid) + "0"
	run := journalRunWithIdentity(t, st, task.ID, &pid, &now, &doctored)

	if _, err := Recover(context.Background(), st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if _, err := procx.Identity(pid); err != nil {
		t.Errorf("innocent process is gone (%v); a mismatched identity must spare it", err)
	}
	// The row is still finalized and the task still re-queued — only the
	// kill is withheld.
	got, _ := st.GetStepRun(context.Background(), run.ID)
	if got.State != store.StepInterrupted {
		t.Errorf("step run = %s, want interrupted", got.State)
	}
	if tk, _ := st.GetTask(context.Background(), task.ID); tk.State != store.TaskQueued {
		t.Errorf("task = %s, want queued", tk.State)
	}
}

// A token journaled in a previous boot. On Linux it is literally the boot-id
// component that differs; on macOS and Windows the constant simply cannot
// equal a live token. Either way a machine that rebooted between the crash and
// the restart kills nothing, which is the reboot half of the acceptance
// criteria.
func TestRecoverSparesPIDWhoseIdentityIsFromAnotherBoot(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()
	pid := cmd.Process.Pid
	now := time.Now()
	foreign := "linux1:00000000-0000-0000-0000-000000000000:1"
	journalRunWithIdentity(t, st, task.ID, &pid, &now, &foreign)

	if _, err := Recover(context.Background(), st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if _, err := procx.Identity(pid); err != nil {
		t.Errorf("innocent process is gone (%v); a token from another boot must spare it", err)
	}
}

// An identity journaled for a PID that has since exited is the quiet good
// case: nothing to kill, no warning worth raising, and the task re-queues.
func TestRecoverToleratesDeadPIDWithAnIdentity(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	pid := cmd.Process.Pid
	spawned := time.Now()
	identity := liveIdentity(t, pid)
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	proc.Release()
	journalRunWithIdentity(t, st, task.ID, &pid, &spawned, &identity)

	n, err := Recover(context.Background(), st, discardLog())
	if err != nil {
		t.Fatalf("Recover with a dead pid: %v", err)
	}
	if n != 1 {
		t.Errorf("requeued = %d, want 1", n)
	}
	if tk, _ := st.GetTask(context.Background(), task.ID); tk.State != store.TaskQueued {
		t.Errorf("task = %s, want queued", tk.State)
	}
}

// "Cannot prove, do not kill" matters most where the proof itself is
// unavailable: an identity was journaled, so the tolerance is not consulted,
// and the read fails. The PID lives, and the daemon says why.
func TestRecoverDoesNotKillWhenIdentityIsUnreadable(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	cmd, proc := recoverSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()
	pid := cmd.Process.Pid
	now := time.Now()
	identity := liveIdentity(t, pid)
	journalRunWithIdentity(t, st, task.ID, &pid, &now, &identity)

	// The identity the row carries is the live one, so only the unreadable
	// read can spare this process.
	restore := identityOf
	identityOf = func(int) (string, error) { return "", errors.New("permission denied") }
	t.Cleanup(func() { identityOf = restore })

	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	if _, err := Recover(context.Background(), st, log); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if _, err := procx.Identity(pid); err != nil {
		t.Errorf("process was killed on an unreadable identity (%v); §12.4 forbids it", err)
	}
	if !strings.Contains(logged.String(), "process identity unavailable") {
		t.Errorf("recovery left the PID alone without saying why; log was:\n%s", logged.String())
	}
}
