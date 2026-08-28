package store

import (
	"errors"
	"testing"
	"time"
)

// TestCreateTaskWithKeyRecordsBoth: the row and the key are one write, so the
// key names a task that exists.
func TestCreateTaskWithKeyRecordsBoth(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	task := newTask(p.ID, "keyed", TaskQueued)
	key := &IdempotencyKey{
		Method: "POST", Path: "/v1/tasks", Key: "abc", RequestSHA: "sha-1",
	}
	if err := s.CreateTaskWithKey(ctx, task, nil, key); err != nil {
		t.Fatalf("CreateTaskWithKey: %v", err)
	}
	got, err := s.GetIdempotencyKey(ctx, "POST", "/v1/tasks", "abc")
	if err != nil {
		t.Fatalf("GetIdempotencyKey: %v", err)
	}
	if got.TaskID != task.ID {
		t.Errorf("key names task %d, want %d", got.TaskID, task.ID)
	}
	if got.RequestSHA != "sha-1" {
		t.Errorf("request_sha = %q, want %q", got.RequestSHA, "sha-1")
	}
	if time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("created_at = %s, want roughly now", got.CreatedAt)
	}
	// The scope is (method, path, key): the same key on another route is a
	// different row, which is what lets a later route join the table.
	if _, err := s.GetIdempotencyKey(ctx, "POST", "/v1/projects", "abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup on another route: %v, want ErrNotFound", err)
	}
}

// TestCreateTaskWithKeyIsAtomic: a key that is already recorded fails the
// *whole* transaction, so the task the losing request was inserting is rolled
// back with it. Without that, a duplicate would leave an orphan task behind
// the key that rejected it.
func TestCreateTaskWithKeyIsAtomic(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	first := newTask(p.ID, "winner", TaskQueued)
	if err := s.CreateTaskWithKey(ctx, first, nil,
		&IdempotencyKey{Method: "POST", Path: "/v1/tasks", Key: "dup", RequestSHA: "sha"}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	second := newTask(p.ID, "loser", TaskQueued)
	second.BranchName = "vincent/0-loser"
	err := s.CreateTaskWithKey(ctx, second, nil,
		&IdempotencyKey{Method: "POST", Path: "/v1/tasks", Key: "dup", RequestSHA: "sha"})
	if !errors.Is(err, ErrIdempotencyKeyExists) {
		t.Fatalf("second create: %v, want ErrIdempotencyKeyExists", err)
	}
	rows, err := s.TableRows(ctx)
	if err != nil {
		t.Fatalf("TableRows: %v", err)
	}
	if rows["tasks"] != 1 {
		t.Errorf("tasks = %d after a rejected key, want only the first", rows["tasks"])
	}
	if rows["idempotency_keys"] != 1 {
		t.Errorf("idempotency_keys = %d, want 1", rows["idempotency_keys"])
	}
}

// TestCreateTasksCarriesNoKey: the fan-out path spawns lanes the engine chose,
// not a request a client could replay, so it writes no key at all.
func TestCreateTasksCarriesNoKey(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	lanes := []*Task{newTask(p.ID, "lane-a", TaskQueued), newTask(p.ID, "lane-b", TaskQueued)}
	lanes[1].BranchName = "vincent/0-lane-b"
	if err := s.CreateTasks(ctx, lanes, nil); err != nil {
		t.Fatalf("CreateTasks: %v", err)
	}
	rows, err := s.TableRows(ctx)
	if err != nil {
		t.Fatalf("TableRows: %v", err)
	}
	if rows["idempotency_keys"] != 0 {
		t.Errorf("idempotency_keys = %d after a fan-out spawn, want 0", rows["idempotency_keys"])
	}
}

// TestPruneIdempotencyKeys: rows past the window go, rows inside it stay, and
// a second pass removes nothing — the ticker runs this forever.
func TestPruneIdempotencyKeys(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	now := time.Now()
	old := newTask(p.ID, "old", TaskQueued)
	if err := s.CreateTaskWithKey(ctx, old, nil, &IdempotencyKey{
		Method: "POST", Path: "/v1/tasks", Key: "old", RequestSHA: "sha",
		CreatedAt: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("create the aged task: %v", err)
	}
	fresh := newTask(p.ID, "fresh", TaskQueued)
	fresh.BranchName = "vincent/0-fresh"
	if err := s.CreateTaskWithKey(ctx, fresh, nil, &IdempotencyKey{
		Method: "POST", Path: "/v1/tasks", Key: "fresh", RequestSHA: "sha",
		CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create the fresh task: %v", err)
	}

	n, err := s.PruneIdempotencyKeys(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("first pass: removed %d, err %v; want 1, nil", n, err)
	}
	if _, err := s.GetIdempotencyKey(ctx, "POST", "/v1/tasks", "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the aged key survived: %v", err)
	}
	if _, err := s.GetIdempotencyKey(ctx, "POST", "/v1/tasks", "fresh"); err != nil {
		t.Errorf("a key inside the window was pruned: %v", err)
	}
	// Pruning never touches the tasks themselves: the key expires, the work
	// it created does not.
	if _, err := s.GetTask(ctx, old.ID); err != nil {
		t.Errorf("pruning a key deleted its task: %v", err)
	}
	n, err = s.PruneIdempotencyKeys(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("second pass: removed %d, err %v; want 0, nil", n, err)
	}
}

// TestIdempotencyKeyCascades: force-deleting a project deletes its tasks, and
// a key whose task is gone has nothing left to replay, so it goes too
// (task 040 decision 6).
func TestIdempotencyKeyCascades(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	task := newTask(p.ID, "doomed", TaskQueued)
	if err := s.CreateTaskWithKey(ctx, task, nil, &IdempotencyKey{
		Method: "POST", Path: "/v1/tasks", Key: "cascade", RequestSHA: "sha",
	}); err != nil {
		t.Fatalf("CreateTaskWithKey: %v", err)
	}
	if err := s.DeleteProjectCascade(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}
	if _, err := s.GetIdempotencyKey(ctx, "POST", "/v1/tasks", "cascade"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the key outlived its task: %v", err)
	}
}
