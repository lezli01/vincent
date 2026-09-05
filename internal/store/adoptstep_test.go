package store

import (
	"testing"
	"time"
)

// A `fan_out` step runs in two admissions and is one row (§7.6, task 080
// decision 3): the park that spawns a round opens the row so the step is on
// the timeline while its lanes work (issue #322), and the merge admission
// that ends the round adopts that same row. These cover the pair of
// primitives that shape carries — the lookup the park uses to stay idempotent
// and the adoption the merge uses instead of a second insert.

// parkRow is the row a fan-out park opens: no pid, no container id, no
// summary, `running` at the round's iteration.
func parkRow(t *testing.T, s *Store, taskID int64, iteration int, startedAt time.Time) *StepRun {
	t.Helper()
	run := &StepRun{
		TaskID: taskID, StepIndex: 0, StepID: "build", StepType: "fan_out",
		Attempt: 1, Iteration: iteration, State: StepRunning, StartedAt: startedAt,
	}
	if err := s.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return run
}

func TestOpenStepRunFindsOnlyTheOpenRowAtItsRef(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	ref := StepRef{TaskID: task.ID, StepIndex: 0, StepID: "build", Iteration: 0}

	got, err := s.OpenStepRun(ctx, ref)
	if err != nil {
		t.Fatalf("OpenStepRun: %v", err)
	}
	if got != nil {
		t.Fatalf("OpenStepRun found %+v before any row was written", got)
	}

	open := parkRow(t, s, task.ID, 0, time.Now())
	got, err = s.OpenStepRun(ctx, ref)
	if err != nil {
		t.Fatalf("OpenStepRun: %v", err)
	}
	if got == nil || got.ID != open.ID {
		t.Fatalf("OpenStepRun = %+v, want the row at %+v", got, ref)
	}

	// Iteration is the round, so the next round's ref must not see this one's
	// row — that is what keeps one round to one row.
	next, err := s.OpenStepRun(ctx, StepRef{TaskID: task.ID, StepIndex: 0, StepID: "build", Iteration: 1})
	if err != nil {
		t.Fatalf("OpenStepRun: %v", err)
	}
	if next != nil {
		t.Errorf("round 1 adopted round 0's row %+v", next)
	}

	// A finalized round has no open row: the merge admission after a restart
	// that interrupted the park writes a fresh one, which is the gate's
	// precedent (§12.4).
	open.State = StepSucceeded
	if err := s.UpdateStepRun(ctx, open); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	got, err = s.OpenStepRun(ctx, ref)
	if err != nil {
		t.Fatalf("OpenStepRun: %v", err)
	}
	if got != nil {
		t.Errorf("OpenStepRun found the finalized row %+v", got)
	}
}

func TestAdoptOpenStepRunTakesTheParkRowAndItsOverride(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	// Nothing open: the caller inserts as usual.
	run := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "build", StepType: "fan_out",
		Attempt: 1, Iteration: 0, State: StepRunning,
	}
	adopted, err := s.AdoptOpenStepRun(ctx, run)
	if err != nil {
		t.Fatalf("AdoptOpenStepRun: %v", err)
	}
	if adopted {
		t.Fatal("AdoptOpenStepRun adopted a row that does not exist")
	}
	if run.ID != 0 {
		t.Errorf("AdoptOpenStepRun assigned id %d without adopting", run.ID)
	}

	started := time.Now().Add(-3 * time.Minute)
	open := parkRow(t, s, task.ID, 0, started)
	// The join is exactly a step a human retries after editing, so the
	// pending edit+retry override has to drain onto the adopted row in the
	// same transaction as the write (phase 2 decision).
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET pending_override_json = ? WHERE id = ?`,
		`{"prompt":"merge them by hand"}`, task.ID); err != nil {
		t.Fatalf("seed pending override: %v", err)
	}

	merge := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "build", StepType: "fan_out",
		// The number the merge admission would have taken had it inserted;
		// adoption must overwrite it with the park's.
		Attempt: 7, Iteration: 0, State: StepRunning,
		Agent: "claude", Model: "sonnet",
		TranscriptPath: "/data/transcripts/1/0-1.jsonl",
		StartedAt:      time.Now(),
	}
	adopted, err = s.AdoptOpenStepRun(ctx, merge)
	if err != nil {
		t.Fatalf("AdoptOpenStepRun: %v", err)
	}
	if !adopted {
		t.Fatal("AdoptOpenStepRun did not adopt the open park row")
	}
	if merge.ID != open.ID {
		t.Errorf("adopted id = %d, want the park row %d", merge.ID, open.ID)
	}
	if merge.Attempt != open.Attempt {
		t.Errorf("adopted attempt = %d, want the park's %d — §12.2 named the transcript after it",
			merge.Attempt, open.Attempt)
	}
	if merge.PromptOverride != "merge them by hand" {
		t.Errorf("adopted prompt override = %q, want the human's edit", merge.PromptOverride)
	}

	got, err := s.GetStepRun(ctx, open.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.TranscriptPath != merge.TranscriptPath || got.Agent != "claude" {
		t.Errorf("adopted row = transcript %q agent %q, want the attempt's columns",
			got.TranscriptPath, got.Agent)
	}
	if got.PromptOverride != "merge them by hand" {
		t.Errorf("adopted row prompt override = %q, want the human's edit", got.PromptOverride)
	}
	// The step started when the fan-out began, not when its last lane settled.
	if !got.StartedAt.Equal(open.StartedAt) {
		t.Errorf("adopted started_at = %s, want the park's %s", got.StartedAt, open.StartedAt)
	}
	if got.PID != nil || got.ContainerID != nil {
		t.Errorf("adoption journaled something killable: pid=%v container=%v", got.PID, got.ContainerID)
	}

	// One round, one row.
	rows, err := s.ListStepRunsAt(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("ListStepRunsAt: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the round has %d rows after the merge adopted, want 1: %+v", len(rows), rows)
	}

	after, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.PendingOverride != nil {
		t.Errorf("pending override survived the adoption: %+v", after.PendingOverride)
	}
}

// TestQueuedFanOutParkRowIsNotUnreconciled: the park row is `running` while
// the parent sits in `queued` between the two halves of one round — the
// scheduler wakes it when its subtree settles (§7.6). That combination means
// "an attempt was never finalized" for every other step type, and both
// readings of it have to skip a `fan_out` row: the admission guard, which
// would otherwise refuse to admit the merge and deadlock every fan-out, and
// `GET /v1/doctor`'s view of the same §12.4 invariant.
func TestQueuedFanOutParkRowIsNotUnreconciled(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "parent", TaskQueued)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	parkRow(t, s, task.ID, 0, time.Now())

	candidates, err := s.ListAdmissible(ctx)
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("ListAdmissible returned %d candidates, want 1", len(candidates))
	}
	if candidates[0].OpenStepRuns != 0 {
		t.Errorf("the park row counted as %d unreconciled runs; the scheduler would refuse the merge",
			candidates[0].OpenStepRuns)
	}
	stuck, err := s.UnreconciledTasks(ctx)
	if err != nil {
		t.Fatalf("UnreconciledTasks: %v", err)
	}
	if len(stuck) != 0 {
		t.Errorf("doctor reported normal fan-out operation as unreconciled: %+v", stuck)
	}

	// A row of any other type in the same place is still the contradiction it
	// always was — the carve-out is the step type, not the task state.
	agentRun := &StepRun{
		TaskID: task.ID, StepIndex: 1, StepID: "implement", StepType: "agent",
		Attempt: 1, State: StepRunning,
	}
	if err := s.CreateStepRun(ctx, agentRun); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	candidates, err = s.ListAdmissible(ctx)
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	if len(candidates) != 1 || candidates[0].OpenStepRuns != 1 {
		t.Errorf("open agent run counted as %+v, want exactly one unreconciled run", candidates)
	}
	stuck, err = s.UnreconciledTasks(ctx)
	if err != nil {
		t.Fatalf("UnreconciledTasks: %v", err)
	}
	if len(stuck) != 1 || stuck[0].OpenStepRuns != 1 {
		t.Errorf("doctor reported %+v, want the one open agent run", stuck)
	}
}
