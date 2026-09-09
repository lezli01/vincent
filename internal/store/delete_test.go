package store

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// archiveTask puts a task into `archived` through the real transition path, so
// the `archived_at` these tests order and bound by is the one the engine
// writes rather than one a test invented.
func archiveTask(t *testing.T, s *Store, id int64, from TaskState) {
	t.Helper()
	if _, _, err := s.TransitionTask(t.Context(), id, from, TaskArchived, TaskChange{}); err != nil {
		t.Fatalf("archive task %d: %v", id, err)
	}
}

// backdateArchivedAt moves a task's archived_at, which nothing in the API can
// do. Ageing rows is the only way to test a date bound without sleeping.
func backdateArchivedAt(t *testing.T, s *Store, id int64, at time.Time) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE tasks SET archived_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
		t.Fatalf("backdate task %d: %v", id, err)
	}
}

func newArchivedTask(t *testing.T, s *Store, projectID int64, title string) *Task {
	t.Helper()
	task := newTask(projectID, title, TaskDone)
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", title, err)
	}
	archiveTask(t, s, task.ID, TaskDone)
	return task
}

func TestArchivedTaskListingIsNewestArchivedFirst(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	// Created oldest-first, archived in the reverse order, so `id DESC` and
	// `archived_at DESC` disagree — which is the whole point of the ordering.
	a := newArchivedTask(t, s, p.ID, "a")
	b := newArchivedTask(t, s, p.ID, "b")
	c := newArchivedTask(t, s, p.ID, "c")
	now := time.Now()
	backdateArchivedAt(t, s, a.ID, now.Add(-time.Hour))
	backdateArchivedAt(t, s, b.ID, now.Add(-3*time.Hour))
	backdateArchivedAt(t, s, c.ID, now.Add(-2*time.Hour))

	got, err := s.ListTasks(t.Context(), TaskFilter{Archived: ArchivedOnly})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	want := []int64{a.ID, c.ID, b.ID}
	if len(got) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d: got task %d, want %d", i, got[i].ID, id)
		}
	}
}

func TestArchivedTaskDateBoundsSelectOnArchivedAt(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	old := newArchivedTask(t, s, p.ID, "old")
	recent := newArchivedTask(t, s, p.ID, "recent")
	live := newTask(p.ID, "live", TaskDone)
	if err := s.CreateTask(t.Context(), live, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	now := time.Now()
	backdateArchivedAt(t, s, old.ID, now.AddDate(0, 0, -40))
	backdateArchivedAt(t, s, recent.ID, now.AddDate(0, 0, -2))
	cutoff := now.AddDate(0, 0, -7)

	before, err := s.ListTasks(t.Context(), TaskFilter{Archived: ArchivedOnly, ArchivedBefore: cutoff})
	if err != nil {
		t.Fatalf("ListTasks before: %v", err)
	}
	if len(before) != 1 || before[0].ID != old.ID {
		t.Fatalf("archived_before: got %v, want just task %d", ids(before), old.ID)
	}
	since, err := s.ListTasks(t.Context(), TaskFilter{Archived: ArchivedOnly, ArchivedSince: cutoff})
	if err != nil {
		t.Fatalf("ListTasks since: %v", err)
	}
	if len(since) != 1 || since[0].ID != recent.ID {
		t.Fatalf("archived_since: got %v, want just task %d", ids(since), recent.ID)
	}
	// A live task has a NULL archived_at, which fails both comparisons: a
	// bound narrows to the archive on its own.
	all, err := s.ListTasks(t.Context(), TaskFilter{Archived: ArchivedAll, ArchivedSince: cutoff})
	if err != nil {
		t.Fatalf("ListTasks all: %v", err)
	}
	if len(all) != 1 || all[0].ID != recent.ID {
		t.Fatalf("archived_since over ArchivedAll: got %v, want just task %d", ids(all), recent.ID)
	}
}

func TestArchivedTaskPagingIsStableAcrossPages(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	now := time.Now()
	var all []int64
	for i := range 6 {
		task := newArchivedTask(t, s, p.ID, string(rune('a'+i)))
		backdateArchivedAt(t, s, task.ID, now.Add(-time.Duration(i)*time.Hour))
		all = append(all, task.ID)
	}
	var seen []int64
	for offset := 0; offset < 6; offset += 2 {
		page, err := s.ListTasks(t.Context(), TaskFilter{
			Archived: ArchivedOnly, Limit: 2, Offset: offset,
		})
		if err != nil {
			t.Fatalf("ListTasks offset %d: %v", offset, err)
		}
		seen = append(seen, ids(page)...)
	}
	for i, id := range all {
		if seen[i] != id {
			t.Fatalf("paged position %d: got task %d, want %d (pages %v)", i, seen[i], id, seen)
		}
	}
}

func ids(tasks []Task) []int64 {
	out := make([]int64, 0, len(tasks))
	for i := range tasks {
		out = append(out, tasks[i].ID)
	}
	return out
}

func TestDeleteTaskCascadeRemovesStepRunsAndKeepsEvents(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newArchivedTask(t, s, p.ID, "gone")
	run := &StepRun{TaskID: task.ID, StepIndex: 0, StepID: "s1", StepType: "command", Attempt: 1, State: StepSucceeded}
	if err := s.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	before, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	if err := s.DeleteTaskCascade(ctx, task.ID); err != nil {
		t.Fatalf("DeleteTaskCascade: %v", err)
	}
	if _, err := s.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after delete: %v, want ErrNotFound", err)
	}
	runs, err := s.ListStepRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("got %d step runs after delete, want 0", len(runs))
	}
	// The historical events survive: `id` is the SSE Last-Event-ID cursor
	// every subscriber is holding, and one archived row going is not a
	// project's whole history going.
	after, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if len(after) < len(before)+1 {
		t.Fatalf("got %d events after delete, want at least %d plus the task.deleted", len(after), len(before))
	}
	var found bool
	for i := range after {
		if after[i].Type == EventTaskDeleted {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event was appended", EventTaskDeleted)
	}
}

func TestDeleteTaskCascadeNullsCreatedByAndClearsIdempotency(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	parent := newArchivedTask(t, s, p.ID, "parent")
	child := newTask(p.ID, "child", TaskDone)
	child.CreatedByTaskID = &parent.ID
	if err := s.CreateTask(ctx, child, nil); err != nil {
		t.Fatalf("CreateTask(child): %v", err)
	}

	if err := s.DeleteTaskCascade(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteTaskCascade: %v", err)
	}
	// created_by_task_id is ON DELETE SET NULL: mcp.max_depth's walk simply
	// stops one link early, which is why this needs no refusal.
	got, err := s.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetTask(child): %v", err)
	}
	if got.CreatedByTaskID != nil {
		t.Fatalf("child still points at task %d", *got.CreatedByTaskID)
	}
}

func TestDeleteTaskRefusesALiveTask(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "live", TaskRunning)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	err := s.DeleteTaskCascade(ctx, task.ID)
	assertRefused(t, err, DeleteRefusedNotArchived)
	if _, err := s.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("the task was deleted anyway: %v", err)
	}
}

func TestDeleteTaskRefusesAParentWithLanes(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	parent := newArchivedTask(t, s, p.ID, "parent")
	lane := newTask(p.ID, "lane", TaskDone)
	lane.ParentTaskID = &parent.ID
	if err := s.CreateTask(ctx, lane, nil); err != nil {
		t.Fatalf("CreateTask(lane): %v", err)
	}

	// Without the explicit guard this is a driver error rather than a
	// refusal: parent_task_id is a plain REFERENCES with no ON DELETE clause
	// and PRAGMA foreign_keys is on.
	err := s.DeleteTaskCascade(ctx, parent.ID)
	assertRefused(t, err, DeleteRefusedHasLanes)
	if _, err := s.GetTask(ctx, parent.ID); err != nil {
		t.Fatalf("the parent was deleted anyway: %v", err)
	}
}

func TestDeleteTaskRefusesAHandoffTarget(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	task := handOff(t, s, c, p.ID)
	archiveTask(t, s, task.ID, TaskQueued)

	err := s.DeleteTaskCascade(ctx, task.ID)
	assertRefused(t, err, DeleteRefusedHandoffTarget)
	if _, err := s.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("the task was deleted anyway: %v", err)
	}
}

func TestDeleteChatCascadeRemovesTheChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	if _, err := s.CreateChatTurn(ctx, c.ID, "hello"); err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	if _, err := s.SetChatState(ctx, c.ID, chatstate.Archived); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}

	if err := s.DeleteChatCascade(ctx, c.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}
	if _, err := s.GetChat(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetChat after delete: %v, want ErrNotFound", err)
	}
	// The turns go with the row through the schema's own cascade.
	turns, err := s.ListChatTurns(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListChatTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got %d turns after delete, want 0", len(turns))
	}
}

func TestDeleteChatRefusesAHandedOffChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	handOff(t, s, c, p.ID)

	err := s.DeleteChatCascade(ctx, c.ID)
	assertRefused(t, err, DeleteRefusedHandedOff)
	if _, err := s.GetChat(ctx, c.ID); err != nil {
		t.Fatalf("the chat was deleted anyway: %v", err)
	}
}

func TestDeleteChatRefusesALiveChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)

	err := s.DeleteChatCascade(ctx, c.ID)
	assertRefused(t, err, DeleteRefusedNotArchived)
	if _, err := s.GetChat(ctx, c.ID); err != nil {
		t.Fatalf("the chat was deleted anyway: %v", err)
	}
}

func TestChatDateBoundsAndPagingUseUpdatedAt(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	var made []int64
	for i := range 4 {
		c := &Chat{
			ProjectID: p.ID, Title: string(rune('a' + i)), Agent: "claude",
			BaseBranch: "main", Branch: "vincent/chat", PermissionMode: "full_auto",
		}
		if err := s.CreateChat(ctx, c); err != nil {
			t.Fatalf("CreateChat: %v", err)
		}
		if _, err := s.SetChatState(ctx, c.ID, chatstate.Archived); err != nil {
			t.Fatalf("SetChatState: %v", err)
		}
		made = append(made, c.ID)
	}
	now := time.Now()
	// `updated_at` is when a terminal chat ended, which is what these bounds
	// measure — there is deliberately no archived_at column (task 074/079).
	for i, id := range made {
		if _, err := s.db.ExecContext(ctx, `UPDATE chats SET updated_at = ? WHERE id = ?`,
			formatTime(now.Add(-time.Duration(i)*24*time.Hour)), id); err != nil {
			t.Fatalf("backdate chat %d: %v", id, err)
		}
	}

	got, err := s.ListChats(ctx, ChatFilter{Archived: ArchivedOnly, ArchivedBefore: now.Add(-36 * time.Hour)})
	if err != nil {
		t.Fatalf("ListChats before: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("archived_before: got %d chats, want 2", len(got))
	}
	// Newest-ended first, so the two returned are the third and fourth made.
	if got[0].ID != made[2] || got[1].ID != made[3] {
		t.Fatalf("archived_before order: got %d,%d want %d,%d", got[0].ID, got[1].ID, made[2], made[3])
	}
	page, err := s.ListChats(ctx, ChatFilter{Archived: ArchivedOnly, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListChats paged: %v", err)
	}
	if len(page) != 2 || page[0].ID != made[1] || page[1].ID != made[2] {
		t.Fatalf("paged: got %v, want %d,%d", page, made[1], made[2])
	}
}

// handOff runs a real handoff, so the `handed_off` state and the
// handoff_task_id link the two refusals turn on are the ones the feature
// writes rather than ones a test poked in.
func handOff(t *testing.T, s *Store, c *Chat, projectID int64) *Task {
	t.Helper()
	task := newTask(projectID, "carry on", TaskQueued)
	task.BranchName, task.BaseBranch, task.BaseSHA = c.Branch, c.BaseBranch, c.BaseSHA
	task.WorktreePath = c.WorktreePath
	if _, err := s.HandoffChat(t.Context(), c.ID, task); err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}
	return task
}

func assertRefused(t *testing.T, err error, reason string) {
	t.Helper()
	var refused *DeleteRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("got %v, want a *DeleteRefusedError", err)
	}
	if refused.Reason != reason {
		t.Fatalf("reason %q, want %q (message: %s)", refused.Reason, reason, refused.Message)
	}
	if refused.Message == "" {
		t.Fatal("the refusal names nothing")
	}
}
