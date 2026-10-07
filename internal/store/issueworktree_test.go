package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newMainTask is a queued main-role task of issue, carrying the name §5.3's
// chain would have produced for it.
func newMainTask(projectID, issueID int64, title string) *Task {
	task := newTask(projectID, title, TaskQueued)
	task.IssueID = &issueID
	task.IssueWorktree = IssueWorktreeMain
	return task
}

func mustCreate(t *testing.T, s *Store, task *Task) {
	t.Helper()
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", task.Title, err)
	}
}

func mainWorktree(t *testing.T, s *Store, issueID int64) IssueMainWorktree {
	t.Helper()
	mw, err := s.GetIssueMainWorktree(t.Context(), issueID)
	if err != nil {
		t.Fatalf("GetIssueMainWorktree: %v", err)
	}
	return mw
}

// TestFirstMainTaskBindsTheIssueMainBranch: the first main task's own name
// becomes the issue's main branch, and a later main task is bound to it
// inside the create transaction rather than keeping the name it resolved
// (task 134 decisions 2, 3).
func TestFirstMainTaskBindsTheIssueMainBranch(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	if got := mainWorktree(t, s, is.ID); got.Branch != "" {
		t.Fatalf("main branch before any task = %q, want none", got.Branch)
	}
	first := newMainTask(p.ID, is.ID, "first")
	mustCreate(t, s, first)
	if got := mainWorktree(t, s, is.ID); got.Branch != first.BranchName {
		t.Fatalf("main branch = %q, want the first main task's %q", got.Branch, first.BranchName)
	}

	second := newMainTask(p.ID, is.ID, "second")
	mustCreate(t, s, second)
	if second.BranchName != first.BranchName {
		t.Errorf("second main task's BranchName = %q, want %q", second.BranchName, first.BranchName)
	}
	got, err := s.GetTask(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.BranchName != first.BranchName || got.IssueWorktree != IssueWorktreeMain {
		t.Errorf("stored second = (%q, %q), want (%q, main)", got.BranchName, got.IssueWorktree, first.BranchName)
	}
	// The first cut the branch; the second adopts it, so admission puts it
	// behind the branch's working directory rather than refusing a second
	// cut with branch_exists (review F1 of #768).
	if first.AdoptedBranch || !second.AdoptedBranch || !got.AdoptedBranch {
		t.Errorf("adopted: first %v, second %v (stored %v), want false, true, true",
			first.AdoptedBranch, second.AdoptedBranch, got.AdoptedBranch)
	}
	// While the first has not cut it, the second waits on it.
	cands, err := s.ListAdmissible(t.Context())
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	for _, c := range cands {
		if c.Task.ID == second.ID && c.DirClaimants == 0 {
			t.Errorf("second's DirClaimants = 0 while the first is queued to cut its branch")
		}
	}
	if shared, err := s.BranchSharedByOther(t.Context(), p.ID, first.BranchName, first.ID); err != nil || !shared {
		t.Errorf("BranchSharedByOther(first) = %v, %v; want true", shared, err)
	}

	// A resolveBranch name that needed the id is overridden the same way.
	third := newMainTask(p.ID, is.ID, "third")
	if err := s.CreateTask(t.Context(), third, func(int64) (string, error) { return "vincent/x-third", nil }); err != nil {
		t.Fatalf("CreateTask(third): %v", err)
	}
	if third.BranchName != first.BranchName {
		t.Errorf("id-bearing main task's BranchName = %q, want %q", third.BranchName, first.BranchName)
	}
}

// TestConcurrentFirstMainTasksShareOneBranch: two "first main" creates for
// one issue racing each other end on one branch, because the binding runs
// in the create transaction and SQLite has one writer.
func TestConcurrentFirstMainTasksShareOneBranch(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	tasks := []*Task{newMainTask(p.ID, is.ID, "a"), newMainTask(p.ID, is.ID, "b")}
	var wg sync.WaitGroup
	errs := make([]error, len(tasks))
	for i, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.CreateTask(t.Context(), task, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("CreateTask(%d): %v", i, err)
		}
	}
	if tasks[0].BranchName != tasks[1].BranchName {
		t.Fatalf("racing first main tasks got %q and %q, want one branch",
			tasks[0].BranchName, tasks[1].BranchName)
	}
	if got := mainWorktree(t, s, is.ID); got.Branch != tasks[0].BranchName {
		t.Errorf("main branch = %q, want %q", got.Branch, tasks[0].BranchName)
	}
}

// TestMainBranchClaimStillCollidesOutsideTheIssue: the claim's exemption
// covers main tasks of one issue only. A task with no issue, a main task of
// another issue and a side task all still collide with the main branch.
func TestMainBranchClaimStillCollidesOutsideTheIssue(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	other := testIssue(t, s, p.ID, "another issue")
	first := newMainTask(p.ID, is.ID, "first")
	mustCreate(t, s, first)

	plain := newTask(p.ID, "plain", TaskQueued)
	plain.BranchName = first.BranchName
	otherMain := newMainTask(p.ID, other.ID, "other")
	otherMain.BranchName = first.BranchName
	side := newTask(p.ID, "side", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictBlock
	side.BranchName = first.BranchName

	for _, task := range []*Task{plain, otherMain, side} {
		var claimed *BranchClaimedError
		err := s.CreateTask(t.Context(), task, nil)
		if !errors.As(err, &claimed) || claimed.TaskID != first.ID {
			t.Errorf("%s on the main branch: err = %v, want BranchClaimedError naming task %d",
				task.Title, err, first.ID)
		}
	}
}

// TestExplicitBranchOnAMainTaskMustBeTheMainBranch: a main task that named a
// branch of its own is refused once the issue's main branch is a different
// one, and accepted when it names the main branch (task 134 decision 5).
func TestExplicitBranchOnAMainTaskMustBeTheMainBranch(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	first := newMainTask(p.ID, is.ID, "first")
	first.BranchName, first.BranchExplicit = "issue/typed", true
	mustCreate(t, s, first)
	if got := mainWorktree(t, s, is.ID); got.Branch != "issue/typed" {
		t.Fatalf("main branch = %q, want the typed name", got.Branch)
	}

	wrong := newMainTask(p.ID, is.ID, "wrong")
	wrong.BranchName, wrong.BranchExplicit = "issue/other", true
	var mismatch *MainBranchMismatchError
	if err := s.CreateTask(t.Context(), wrong, nil); !errors.As(err, &mismatch) {
		t.Fatalf("different explicit branch: err = %v, want MainBranchMismatchError", err)
	}
	if mismatch.MainBranch != "issue/typed" || mismatch.Branch != "issue/other" {
		t.Errorf("mismatch = %+v", mismatch)
	}

	same := newMainTask(p.ID, is.ID, "same")
	same.BranchName, same.BranchExplicit = "issue/typed", true
	mustCreate(t, s, same)
	// Naming the main branch is joining it, so it adopts the branch the
	// first task cut exactly as an unnamed join does (review F1 of #768).
	if !same.AdoptedBranch {
		t.Error("a main task naming the existing main branch did not adopt it")
	}
}

// TestSideTaskNeedsAMainBranch: a side task is refused in the transaction
// while the issue has no main branch, and accepted once it has one, keeping
// its own name and its on_conflict (task 134 decision 6).
func TestSideTaskNeedsAMainBranch(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	newSide := func(title string) *Task {
		task := newTask(p.ID, title, TaskQueued)
		task.IssueID, task.IssueWorktree, task.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictAgent
		return task
	}
	var noMain *NoMainBranchError
	if err := s.CreateTask(t.Context(), newSide("early"), nil); !errors.As(err, &noMain) {
		t.Fatalf("side task before a main branch: err = %v, want NoMainBranchError", err)
	}
	mustCreate(t, s, newMainTask(p.ID, is.ID, "main"))
	side := newSide("side")
	mustCreate(t, s, side)
	got, err := s.GetTask(t.Context(), side.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.IssueWorktree != IssueWorktreeSide || got.MergeOnConflict != MergeOnConflictAgent ||
		got.BranchName != "vincent/0-side" {
		t.Errorf("side task = (%q, %q, %q)", got.IssueWorktree, got.MergeOnConflict, got.BranchName)
	}
}

// TestArchivedMainTasksReleaseTheMainBranch: once every main task is
// archived the issue has no main branch, and the next main task keeps its
// own fresh name — the known gap task 134 records.
func TestArchivedMainTasksReleaseTheMainBranch(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	first := newMainTask(p.ID, is.ID, "first")
	first.State = TaskDone
	mustCreate(t, s, first)
	archiveTask(t, s, first.ID, TaskDone)
	if got := mainWorktree(t, s, is.ID); got.Branch != "" {
		t.Fatalf("main branch after archive = %q, want none", got.Branch)
	}
	next := newMainTask(p.ID, is.ID, "next")
	mustCreate(t, s, next)
	if next.BranchName != "vincent/0-next" {
		t.Errorf("next main task's BranchName = %q, want its own fresh name", next.BranchName)
	}
}

// TestRenamingASharedMainBranchIsRefused is review F2 of #768: a retry's
// branch_override must not move one main task of an issue off the main
// branch while another still carries it, which would leave the issue two
// main branches. A sole main task may be renamed, and the issue's main
// branch moves with it.
func TestRenamingASharedMainBranchIsRefused(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	first := newMainTask(p.ID, is.ID, "first")
	first.State = TaskBlocked
	mustCreate(t, s, first)
	second := newMainTask(p.ID, is.ID, "second")
	second.State = TaskDone
	mustCreate(t, s, second)

	for _, task := range []*Task{first, second} {
		var shared *SharedMainBranchError
		err := s.SetTaskBranchName(t.Context(), task.ID, p.ID, "issue/elsewhere")
		if !errors.As(err, &shared) {
			t.Fatalf("rename %s: err = %v, want SharedMainBranchError", task.Title, err)
		}
		if shared.Branch != first.BranchName || shared.IssueID != is.ID {
			t.Errorf("rename %s: %+v", task.Title, shared)
		}
	}
	if got := mainWorktree(t, s, is.ID); got.Branch != first.BranchName {
		t.Fatalf("main branch after refused renames = %q, want %q", got.Branch, first.BranchName)
	}

	archiveTask(t, s, second.ID, TaskDone)
	if err := s.SetTaskBranchName(t.Context(), first.ID, p.ID, "issue/elsewhere"); err != nil {
		t.Fatalf("rename the sole main task: %v", err)
	}
	if got := mainWorktree(t, s, is.ID); got.Branch != "issue/elsewhere" {
		t.Errorf("main branch after renaming the sole main task = %q, want issue/elsewhere", got.Branch)
	}
}

// TestRoleIsOnlyForRootTasksWithAnIssue: a role on a lane or on a task with
// no issue is a caller's bug, refused rather than stored (task 130 decision
// 5), and a task created without one reads back with none.
func TestRoleIsOnlyForRootTasksWithAnIssue(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	parent := newMainTask(p.ID, is.ID, "parent")
	mustCreate(t, s, parent)

	noIssue := newTask(p.ID, "no-issue", TaskQueued)
	noIssue.IssueWorktree = IssueWorktreeMain
	lane := newMainTask(p.ID, is.ID, "lane")
	lane.ParentTaskID, lane.LaneID = &parent.ID, "a"
	for _, task := range []*Task{noIssue, lane} {
		if err := s.CreateTask(t.Context(), task, nil); err == nil {
			t.Errorf("%s with a role was created", task.Title)
		}
	}

	// The lane the engine actually builds carries the issue and no role.
	lane = newTask(p.ID, "lane-ok", TaskQueued)
	lane.IssueID, lane.ParentTaskID, lane.LaneID = &is.ID, &parent.ID, "a"
	mustCreate(t, s, lane)
	got, err := s.GetTask(t.Context(), lane.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.IssueWorktree != "" || got.MergeOnConflict != "" || got.EndSHA != "" {
		t.Errorf("lane role = (%q, %q, %q), want none", got.IssueWorktree, got.MergeOnConflict, got.EndSHA)
	}
}

// TestIssueMainWorktreeOccupant: the occupant is a main task that has been
// admitted and has not settled (task 134 decision 8) — queued is not one,
// running, blocked, paused and awaiting_gate after admission each are, and a
// settled task is not. The issue row carries the same answer.
func TestIssueMainWorktreeOccupant(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		state    TaskState
		admitted bool
		occupies bool
	}{
		{TaskQueued, false, false},
		{TaskPaused, false, false},
		{TaskRunning, true, true},
		{TaskBlocked, true, true},
		{TaskPaused, true, true},
		{TaskAwaitingGate, true, true},
		{TaskDone, true, false},
		{TaskAborted, true, false},
	} {
		s := openTest(t)
		p := testProject(t, s, "p1")
		is := testIssue(t, s, p.ID, "the issue")
		task := newMainTask(p.ID, is.ID, "m")
		task.State = tc.state
		if tc.admitted {
			task.StartedAt = &started
		}
		mustCreate(t, s, task)

		mw := mainWorktree(t, s, is.ID)
		if mw.Branch != task.BranchName {
			t.Errorf("%s: branch = %q, want %q", tc.state, mw.Branch, task.BranchName)
		}
		occupied := mw.OccupantTaskID != nil && *mw.OccupantTaskID == task.ID
		if occupied != tc.occupies || (!tc.occupies && mw.OccupantTaskID != nil) {
			t.Errorf("%s admitted=%v: occupant = %v, want occupied=%v", tc.state, tc.admitted, mw.OccupantTaskID, tc.occupies)
		}

		iss, err := s.GetIssue(t.Context(), is.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if iss.MainWorktree.Branch != mw.Branch ||
			(iss.MainWorktree.OccupantTaskID == nil) != (mw.OccupantTaskID == nil) {
			t.Errorf("%s: issue row main worktree = %+v, want %+v", tc.state, iss.MainWorktree, mw)
		}
	}
}

// TestMigration0043LeavesExistingRowsWithoutARole: a task written at schema
// 0042, issue or not, reads back with no role after the migration — it
// behaves exactly as it did.
func TestMigration0043LeavesExistingRowsWithoutARole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 42)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open at 0042: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (1, 'p1', '/p1', 'main', '` + backfillAt(0) + `', '` + backfillAt(0) + `')`,
		`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot, base_branch,
			branch_name, state, created_at, updated_at)
			VALUES (1, 1, 't', 'adhoc', 'steps: []', 'main', 'b1', 'queued', '` + backfillAt(1) + `', '` + backfillAt(1) + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 42 -> 43): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.GetTask(t.Context(), 1)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.IssueWorktree != "" || got.EndSHA != "" || got.MergeOnConflict != "" {
		t.Errorf("pre-0043 row = (%q, %q, %q), want no role", got.IssueWorktree, got.EndSHA, got.MergeOnConflict)
	}
}
