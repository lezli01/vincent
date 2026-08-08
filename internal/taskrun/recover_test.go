package taskrun

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
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
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "b", State: state,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// journalRun writes a `running` step run as the engine would have before a
// crash: created, then PID + proc start time journaled.
func journalRun(t *testing.T, st *store.Store, taskID int64, pid *int, startedAt *time.Time) *store.StepRun {
	t.Helper()
	run := &store.StepRun{
		TaskID: taskID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := st.CreateStepRun(context.Background(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	run.PID, run.ProcStartedAt = pid, startedAt
	if err := st.UpdateStepRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	return run
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
	attempts, err := st.CountStepAttempts(ctx, running.ID, 0, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 0 || attempts.Last != 1 {
		t.Errorf("attempts = %+v, want Last=1 Failed=0", attempts)
	}
}

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
