package taskrun

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

func TestNormalizeStatusMessage(t *testing.T) {
	long := strings.Repeat("a", StatusMessageLimit+50)
	// A run of multi-byte runes straddling the cap: the cut has to land on a
	// boundary, or the message ends in a replacement character that reads as
	// corruption rather than as brevity.
	wide := strings.Repeat("é", StatusMessageLimit)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "3 tests red in internal/store", "3 tests red in internal/store"},
		{"multiline", "rebasing\nonto master", "rebasing onto master"},
		{"crlf", "packing\r\nthe artifact", "packing the artifact"},
		{"tabs and runs", "  two\t\tspaces   here  ", "two spaces here"},
		{"control characters", "clear\x1b[2Jscreen\x00", "clear[2Jscreen"},
		{"truncated", long, strings.Repeat("a", StatusMessageLimit)},
		{"empty", "", ""},
		{"only whitespace", " \n\t ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeStatusMessage(c.in); got != c.want {
				t.Errorf("NormalizeStatusMessage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	got := NormalizeStatusMessage(wide)
	if len(got) > StatusMessageLimit {
		t.Errorf("truncated length = %d bytes, want at most %d", len(got), StatusMessageLimit)
	}
	if strings.ContainsRune(got, '�') {
		t.Error("truncation cut a rune in half")
	}
	if got != strings.Repeat("é", StatusMessageLimit/2) {
		t.Errorf("multi-byte truncation = %q, want a whole number of runes", got)
	}
}

// Writes faster than the floor are coalesced, never rejected: the first goes
// through, the burst behind it does not reach the database, and the latest
// value lands once when the floor expires (§13.3).
func TestStatusThrottleCoalesces(t *testing.T) {
	var (
		mu      sync.Mutex
		written []string
	)
	th := newStatusThrottle(func(_ int64, message string) {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, message)
	})
	th.interval = 60 * time.Millisecond

	if !th.admit(1, "first") {
		t.Fatal("the first write was not admitted; a quiet step must never be delayed")
	}
	th.accept(1, "first")

	for _, msg := range []string{"second", "third", "fourth"} {
		if th.admit(1, msg) {
			t.Fatalf("%q was admitted inside the floor", msg)
		}
	}
	// A different run is a different slot: one chatty step must not silence
	// another.
	if !th.admit(2, "sibling") {
		t.Error("a second step run was throttled by the first's traffic")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(written)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(written) != 1 {
		t.Fatalf("coalesced writes = %v, want exactly one flush", written)
	}
	if written[0] != "fourth" {
		t.Errorf("flushed %q, want the latest value", written[0])
	}
}

// After the floor has passed, a write goes straight through again.
func TestStatusThrottleAdmitsAfterTheFloor(t *testing.T) {
	th := newStatusThrottle(func(int64, string) {})
	th.interval = 20 * time.Millisecond
	th.accept(1, "first")
	if th.admit(1, "second") {
		t.Fatal("admitted inside the floor")
	}
	// Well past the floor, and past the flush that the coalesced write above
	// armed — which restarts it. A margin rather than the floor exactly: this
	// asserts that the throttle reopens, not how precisely a timer fires.
	time.Sleep(250 * time.Millisecond)
	if !th.admit(1, "third") {
		t.Error("still throttled after the floor expired")
	}
}

// The engine path end to end, against a real store and a real agent step: a
// status set while the step is running is readable on the running row, and
// the last value set survives onto the finished one.
func TestSetStepStatusLiveAndTerminal(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	task := h.createTask(t, "name: s\nsteps:\n  - id: implement\n    type: agent\n    prompt: work\n")
	h.start(t)

	run := waitForRunningRun(t, h, task.ID, "implement")
	ctx := t.Context()
	if err := h.runner.SetStepStatus(ctx, task.ID, "implement", "scaffolding the migration"); err != nil {
		t.Fatalf("SetStepStatus: %v", err)
	}
	got, err := h.store.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepRunning {
		t.Fatalf("row state = %s, want the status readable while running", got.State)
	}
	if got.StatusMessage != "scaffolding the migration" {
		t.Fatalf("live status = %q", got.StatusMessage)
	}

	// A cancel ends the step as `interrupted`/`canceled`, which is the §12.4
	// shape: the terminalizing write must leave the status alone.
	if _, err := h.runner.Cancel(ctx, task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.waitForState(t, task.ID, store.TaskAborted)
	// The task is aborted durably before the actor reaps the process and
	// closes the row, so the row is what to wait on here.
	after := waitForFinishedRun(t, h, run.ID)
	if after.StatusMessage != "scaffolding the migration" {
		t.Errorf("terminal status = %q, want the last live value to survive", after.StatusMessage)
	}

	// And the endpoint refuses a write against the finished attempt rather
	// than applying it silently.
	err = h.runner.SetStepStatus(ctx, task.ID, "implement", "too late")
	if !errors.Is(err, store.ErrStepNotRunning) {
		t.Errorf("write against a finished step = %v, want ErrStepNotRunning", err)
	}
}

// The failing leg: a status set before the step fails is what stays on the
// `failed` row, beside — never instead of — the daemon's failure_reason.
func TestSetStepStatusSurvivesAFailure(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	task := h.createTask(t,
		"name: s\nsteps:\n  - id: implement\n    type: agent\n    prompt: work\n    timeout: 3s\n")
	h.start(t)

	run := waitForRunningRun(t, h, task.ID, "implement")
	if err := h.runner.SetStepStatus(t.Context(), task.ID, "implement", "stuck retrying the same test"); err != nil {
		t.Fatalf("SetStepStatus: %v", err)
	}
	h.waitForState(t, task.ID, store.TaskBlocked)

	got, err := h.store.GetStepRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepFailed || got.FailureReason != ReasonTimeout {
		t.Fatalf("row = %s/%s, want failed/timeout", got.State, got.FailureReason)
	}
	if got.StatusMessage != "stuck retrying the same test" {
		t.Errorf("status on the failed row = %q, want the step's own last words", got.StatusMessage)
	}
}

// A step that says two things in quick succession has the second coalesced —
// and the coalesced value still lands, even though the step finished inside
// the floor. Losing it would lose exactly the message the terminal reading is
// about: the last thing a step said before it exited.
func TestSetStepStatusCoalescedValueSurvivesTheStep(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	task := h.createTask(t, "name: s\nsteps:\n  - id: implement\n    type: agent\n    prompt: work\n")
	h.start(t)

	run := waitForRunningRun(t, h, task.ID, "implement")
	// Short enough that the whole burst plus the cancel fit inside one floor,
	// which is the case the flush has to survive.
	h.runner.status.interval = 300 * time.Millisecond
	ctx := t.Context()
	for _, msg := range []string{"first", "second", "third"} {
		if err := h.runner.SetStepStatus(ctx, task.ID, "implement", msg); err != nil {
			t.Fatalf("SetStepStatus(%q): %v", msg, err)
		}
	}
	if _, err := h.runner.Cancel(ctx, task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.waitForState(t, task.ID, store.TaskAborted)
	waitForFinishedRun(t, h, run.ID)

	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		got, err := h.store.GetStepRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetStepRun: %v", err)
		}
		last = got.StatusMessage
		if last == "third" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("status after the burst = %q, want the coalesced latest value", last)
}

// An unknown task is not-found, which the API answers 404 to; only a step
// that is not running is the 409.
func TestSetStepStatusUnknownTask(t *testing.T) {
	h := newEngineHarness(t)
	err := h.runner.SetStepStatus(t.Context(), 9999, "implement", "hello")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetStepStatus on an unknown task = %v, want ErrNotFound", err)
	}
}

// §12.4 recovery finalizes a `running` row left by a crashed daemon as
// `interrupted`. The step's last words are the most useful thing on that row
// — "what was it doing when the machine went down" — so the terminalizing
// write must not take them with it.
func TestRecoverKeepsTheStatusMessage(t *testing.T) {
	st, projectID := recoverStore(t)
	ctx := context.Background()
	task := recoverTask(t, st, projectID, store.TaskRunning)
	run := journalRun(t, st, task.ID, nil, nil)

	if _, _, err := st.SetStepRunStatus(ctx, task.ID, run.StepID, "migrating the schema"); err != nil {
		t.Fatalf("SetStepRunStatus: %v", err)
	}
	if _, err := Recover(ctx, st, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, err := st.GetStepRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != store.StepInterrupted {
		t.Fatalf("row = %s, want interrupted", got.State)
	}
	if got.StatusMessage != "migrating the schema" {
		t.Errorf("status after recovery = %q, want it kept", got.StatusMessage)
	}
}

// waitForFinishedRun polls until a step run carries a finish stamp.
func waitForFinishedRun(t *testing.T, h *engineHarness, runID int64) *store.StepRun {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last *store.StepRun
	for time.Now().Before(deadline) {
		run, err := h.store.GetStepRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("GetStepRun: %v", err)
		}
		last = run
		if run.FinishedAt != nil {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("step run %d never finished: %+v", runID, last)
	return nil
}

// waitForRunningRun polls until the named step has a `running` row.
func waitForRunningRun(t *testing.T, h *engineHarness, taskID int64, stepID string) *store.StepRun {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListStepRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListStepRuns: %v", err)
		}
		for i := range runs {
			if runs[i].StepID == stepID && runs[i].State == store.StepRunning {
				return &runs[i]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("step %q of task %d never reached running", stepID, taskID)
	return nil
}
