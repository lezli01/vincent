package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func ptr(s string) *string { return &s }

// newStepRun inserts a bare attempt to record input against.
func newStepRun(t *testing.T, s *Store) *StepRun {
	t.Helper()
	task := testTask(t, s)
	r := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: StepRunning,
	}
	if err := s.CreateStepRun(t.Context(), r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return r
}

// The several render sites of one attempt each write their own field: an
// agent step records its prompt at one moment, a command step its script at
// another and its check command later still, and all three are the same row.
// A later call must not null what an earlier one wrote (migration 0027).
func TestRecordStepRunInputIsAdditive(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	r := newStepRun(t, s)

	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{
		Prompt:      ptr("fix the parser"),
		AgentSource: "task", ModelSource: "workflow", EffortSource: "adapter",
		PermissionMode: "full-auto", TimeoutMS: 600_000,
	}); err != nil {
		t.Fatalf("RecordStepRunInput (prompt): %v", err)
	}
	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{
		Check: ptr("go test ./..."), CheckTimeoutMS: 120_000,
		Shell: "/bin/sh", WorkDir: "/tmp/wt",
	}); err != nil {
		t.Fatalf("RecordStepRunInput (check): %v", err)
	}

	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.RenderedPrompt == nil || *got.RenderedPrompt != "fix the parser" {
		t.Errorf("RenderedPrompt = %v, want the first call's text — the second call cleared it",
			got.RenderedPrompt)
	}
	if got.RenderedCheck == nil || *got.RenderedCheck != "go test ./..." {
		t.Errorf("RenderedCheck = %v, want the second call's text", got.RenderedCheck)
	}
	if got.RenderedRun != nil || got.RenderedIf != nil || got.RenderedForEach != nil {
		t.Errorf("fields no call set are not nil: run=%v if=%v for_each=%v",
			got.RenderedRun, got.RenderedIf, got.RenderedForEach)
	}
	if got.AgentSource != "task" || got.ModelSource != "workflow" || got.EffortSource != "adapter" {
		t.Errorf("sources = %s/%s/%s, want task/workflow/adapter",
			got.AgentSource, got.ModelSource, got.EffortSource)
	}
	if got.PermissionMode != "full-auto" || got.TimeoutMS != 600_000 || got.CheckTimeoutMS != 120_000 {
		t.Errorf("resolution = %s %d/%d, want full-auto 600000/120000",
			got.PermissionMode, got.TimeoutMS, got.CheckTimeoutMS)
	}
	if got.Shell != "/bin/sh" || got.WorkDir != "/tmp/wt" {
		t.Errorf("shell/work_dir = %q/%q, want /bin/sh and /tmp/wt", got.Shell, got.WorkDir)
	}
	if got.InputTruncated {
		t.Error("InputTruncated on a record that lost nothing")
	}

	// Every read path selects the same column list, so proving one row through
	// a list reader is what catches a column missing from the others.
	runs, err := s.ListStepRuns(ctx, r.TaskID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RenderedPrompt == nil || *runs[0].RenderedPrompt != "fix the parser" {
		t.Errorf("ListStepRuns lost the record: %+v", runs)
	}
}

// The regression that would make the whole feature silently useless: the
// input is on the row while the attempt is still `running`, so the actor's
// own later UPDATE — carrying a struct read before the render — must not be
// able to carry these columns at all.
func TestUpdateStepRunDoesNotClearRecordedInput(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	r := newStepRun(t, s)

	// The actor's copy, read before the render: its new fields are all zero.
	stale, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{
		Prompt: ptr("the prompt this attempt got"), AgentSource: "step", TimeoutMS: 900_000,
	}); err != nil {
		t.Fatalf("RecordStepRunInput: %v", err)
	}

	finished := time.Now()
	stale.State, stale.FinishedAt = StepSucceeded, &finished
	if err := s.UpdateStepRun(ctx, stale); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}

	after, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun after update: %v", err)
	}
	if after.RenderedPrompt == nil || *after.RenderedPrompt != "the prompt this attempt got" {
		t.Errorf("RenderedPrompt = %v after UpdateStepRun; a stale struct erased the record",
			after.RenderedPrompt)
	}
	if after.AgentSource != "step" || after.TimeoutMS != 900_000 {
		t.Errorf("resolution = %q/%d after UpdateStepRun, want step/900000",
			after.AgentSource, after.TimeoutMS)
	}
	if after.State != StepSucceeded {
		t.Errorf("state = %s, want the update's succeeded", after.State)
	}
}

// nil and empty are different facts, and the pointer is what carries the
// difference: "no input was recorded" is what a pre-0027 row and a step type
// with no such input read, while an empty string is a render that produced
// nothing.
func TestRecordStepRunInputKeepsEmptyApartFromUnrecorded(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	r := newStepRun(t, s)

	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{Prompt: ptr("")}); err != nil {
		t.Fatalf("RecordStepRunInput: %v", err)
	}
	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.RenderedPrompt == nil {
		t.Fatal("an empty render read back as nil; that is 'nothing was recorded'")
	}
	if *got.RenderedPrompt != "" {
		t.Errorf("RenderedPrompt = %q, want the empty render", *got.RenderedPrompt)
	}
	if got.RenderedRun != nil {
		t.Errorf("RenderedRun = %v, want nil — no call recorded one", got.RenderedRun)
	}
}

// A field over the ceiling is cut to it on a rune boundary — never mid-rune,
// which would leave the record invalid UTF-8 forever — and the cut is
// recorded. A later short write must not clear that flag: truncation is a
// fact about the row, not about the last call.
func TestRecordStepRunInputTruncatesOnARuneBoundary(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	r := newStepRun(t, s)

	// A three-byte rune straddles the limit: 64 KiB is not a multiple of 3,
	// so a naive byte cut would land inside one.
	const rune3 = "→"
	if len(rune3) != 3 || StepInputLimit%len(rune3) == 0 {
		t.Fatalf("fixture no longer straddles the boundary: %d-byte rune, limit %d",
			len(rune3), StepInputLimit)
	}
	long := strings.Repeat(rune3, StepInputLimit/len(rune3)+10)
	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{Prompt: &long}); err != nil {
		t.Fatalf("RecordStepRunInput: %v", err)
	}

	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.RenderedPrompt == nil {
		t.Fatal("RenderedPrompt is nil after a long write")
	}
	cut := *got.RenderedPrompt
	if len(cut) > StepInputLimit {
		t.Errorf("stored %d bytes, want at most StepInputLimit (%d)", len(cut), StepInputLimit)
	}
	if !utf8.ValidString(cut) {
		t.Error("the record is not valid UTF-8; the cut landed mid-rune")
	}
	if !strings.HasPrefix(long, cut) {
		t.Error("the record is not a prefix of what was rendered")
	}
	if want := StepInputLimit - StepInputLimit%len(rune3); len(cut) != want {
		t.Errorf("cut to %d bytes, want %d — the last whole rune below the limit", len(cut), want)
	}
	if !got.InputTruncated {
		t.Error("InputTruncated is false after a cut")
	}

	// A later, short write on another field leaves the flag standing.
	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{Check: ptr("go vet ./...")}); err != nil {
		t.Fatalf("RecordStepRunInput (check): %v", err)
	}
	after, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun after the short write: %v", err)
	}
	if !after.InputTruncated {
		t.Error("a short write cleared InputTruncated; it is OR-ed, never cleared")
	}
}

// The insert is the second write path — a decision row's rendered guard and a
// loop body row's resolved `for_each` list are known at insert, the rule
// loop_item and loop_total already follow — and takes the same cut.
func TestCreateStepRunRecordsAndCutsInput(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	long := strings.Repeat("→", StepInputLimit/3+10)
	in := &StepRun{
		TaskID: task.ID, StepIndex: 2, StepID: "gate", StepType: "condition",
		Attempt: 1, State: StepRunning,
		RenderedIf: ptr("true"), RenderedForEach: &long,
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if !in.InputTruncated {
		t.Error("the insert did not report the cut back onto the struct")
	}

	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.RenderedIf == nil || *got.RenderedIf != "true" {
		t.Errorf("RenderedIf = %v, want the guard's render", got.RenderedIf)
	}
	if got.RenderedForEach == nil || len(*got.RenderedForEach) > StepInputLimit {
		t.Errorf("RenderedForEach was not bounded at insert: %v bytes", got.RenderedForEach)
	}
	if !got.InputTruncated {
		t.Error("InputTruncated is false on a row cut at insert")
	}
}

// A record against a row that is not there is ErrNotFound, and a call that
// carries nothing is a no-op: an empty `UPDATE step_runs SET WHERE id = ?` is
// not a statement.
func TestRecordStepRunInputNotFoundAndEmpty(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	r := newStepRun(t, s)

	if err := s.RecordStepRunInput(ctx, r.ID+999, StepRunInput{Prompt: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("RecordStepRunInput on a missing row = %v, want ErrNotFound", err)
	}
	// Empty is a no-op wherever it is aimed — it never reaches the row, so a
	// missing one is not an error either.
	if err := s.RecordStepRunInput(ctx, r.ID, StepRunInput{}); err != nil {
		t.Errorf("empty RecordStepRunInput = %v, want nil", err)
	}
	if err := s.RecordStepRunInput(ctx, r.ID+999, StepRunInput{}); err != nil {
		t.Errorf("empty RecordStepRunInput on a missing row = %v, want nil", err)
	}
	got, err := s.GetStepRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.RenderedPrompt != nil || got.InputTruncated {
		t.Errorf("the no-op wrote something: prompt=%v truncated=%v",
			got.RenderedPrompt, got.InputTruncated)
	}
}

// A row written before migration 0027 reads back as "no input recorded" —
// nils, false and zeros — rather than as an attempt that was given an empty
// prompt. The two are different answers and the pane says them differently.
func TestMigrateLeavesStepInputUnrecordedOnPreexistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 26)

	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0026: %v", err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatalf("disable foreign keys: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO step_runs
			(task_id, step_index, step_id, step_type, attempt, iteration, state, result_summary,
			started_at)
			VALUES (1, 0, 'implement', 'agent', 1, 0, 'succeeded', 'done', ?)`,
			formatTime(time.Now())); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('step_runs') WHERE name = 'rendered_prompt'`,
		).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if n != 0 {
			t.Fatalf("rendered_prompt exists at schema 26; the fixture is not a pre-0027 database")
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 26 -> %d): %v", latestSchemaVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	runs, err := s.ListStepRuns(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want the legacy row", len(runs))
	}
	got := runs[0]
	if got.RenderedPrompt != nil || got.RenderedRun != nil || got.RenderedCheck != nil ||
		got.RenderedIf != nil || got.RenderedForEach != nil {
		t.Errorf("a legacy row claims a recorded input: %+v", got)
	}
	if got.InputTruncated {
		t.Error("a legacy row claims a truncated input")
	}
	if got.AgentSource != "" || got.ModelSource != "" || got.EffortSource != "" ||
		got.PermissionMode != "" || got.Shell != "" || got.WorkDir != "" {
		t.Errorf("a legacy row claims a resolution: %+v", got)
	}
	if got.TimeoutMS != 0 || got.CheckTimeoutMS != 0 {
		t.Errorf("legacy timeouts = %d/%d, want 0/0", got.TimeoutMS, got.CheckTimeoutMS)
	}
	if got.ResultSummary != "done" {
		t.Errorf("legacy ResultSummary = %q, want done — the added columns shifted the scan",
			got.ResultSummary)
	}
}
