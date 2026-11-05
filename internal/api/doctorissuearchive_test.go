package api

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// iaFixture stages closed issues and their tasks for the issue_archive row
// (task 143.4) through the store's own writes, against a real repo, so the
// dirty check runs git against real worktrees.
type iaFixture struct {
	*doctorHarness
	projectID int64
	repo      string
	n         int
}

func newIAFixture(t *testing.T, wire func(*Deps)) *iaFixture {
	t.Helper()
	h := newDoctorHarnessWith(t, config.Dirs{Config: t.TempDir(), Data: t.TempDir()}, wire)
	projectID, repo := h.seedProject(t)
	return &iaFixture{doctorHarness: h, projectID: projectID, repo: repo}
}

func (f *iaFixture) issue(t *testing.T) int64 {
	t.Helper()
	is, err := f.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: f.projectID, Title: "an issue"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return is.ID
}

func (f *iaFixture) closeIssue(t *testing.T, issueID int64) {
	t.Helper()
	if _, err := f.store.TransitionIssue(t.Context(), issueID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close issue %d: %v", issueID, err)
	}
}

// task creates a task on issueID (0 for none) with role and parent, and
// walks it to state through running.
func (f *iaFixture) task(t *testing.T, issueID int64, role string, parent *store.Task, state store.TaskState) *store.Task {
	t.Helper()
	f.n++
	task := &store.Task{
		ProjectID: f.projectID, Title: "task " + strconv.Itoa(f.n), WorkflowName: "adhoc",
		WorkflowSnapshot: "steps: []", BaseBranch: "main",
		BranchName: "vincent/ia-" + strconv.Itoa(f.n), State: store.TaskQueued, IssueWorktree: role,
	}
	if issueID != 0 {
		task.IssueID = &issueID
	}
	if role == store.IssueWorktreeSide {
		task.MergeOnConflict = store.MergeOnConflictBlock
	}
	if parent != nil {
		task.ParentTaskID = &parent.ID
	}
	if err := f.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	f.walk(t, task, state)
	return task
}

func (f *iaFixture) walk(t *testing.T, task *store.Task, state store.TaskState) {
	t.Helper()
	if state == store.TaskQueued {
		return
	}
	path := []store.TaskState{store.TaskRunning}
	switch state {
	case store.TaskRunning:
	case store.TaskArchived:
		path = append(path, store.TaskDone, store.TaskArchived)
	default:
		path = append(path, state)
	}
	for _, to := range path {
		cur, err := f.store.GetTask(t.Context(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.store.TransitionTask(t.Context(), task.ID, cur.State, to, store.TaskChange{}); err != nil {
			t.Fatalf("task %d → %s: %v", task.ID, to, err)
		}
	}
}

// worktree gives task a real worktree, dirtied with an untracked file when
// dirty is set.
func (f *iaFixture) worktree(t *testing.T, task *store.Task, dirty bool) string {
	t.Helper()
	path, err := f.wt.Create(t.Context(), f.repo, worktree.TaskOwner(task.ID), task.BranchName, "main", false)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	f.claimWorktree(t, task, path)
	if dirty {
		if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("half done"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// held is the report's held entry for task, or nil.
func held(rep doctor.Report, taskID int64) *doctor.HeldTask {
	for i := range rep.IssueArchive.Held {
		if rep.IssueArchive.Held[i].TaskID == taskID {
			return &rep.IssueArchive.Held[i]
		}
	}
	return nil
}

func wantReasons(t *testing.T, rep doctor.Report, taskID int64, want ...string) {
	t.Helper()
	h := held(rep, taskID)
	if h == nil {
		t.Fatalf("task %d not in held %+v", taskID, rep.IssueArchive.Held)
	}
	if !slices.Equal(h.Reasons, want) {
		t.Errorf("task %d reasons = %v, want %v", taskID, h.Reasons, want)
	}
}

// wantNoIssueArchiveProblem is task 143 decision 16: a held task is never in
// problems[], so `vincent doctor` still exits 0 for it.
func wantNoIssueArchiveProblem(t *testing.T, rep doctor.Report) {
	t.Helper()
	for _, p := range rep.Problems {
		if p.Group != doctor.GroupDaemon && p.Group != doctor.GroupPaths && p.Group != doctor.GroupStorage &&
			p.Group != doctor.GroupDatabase && p.Group != doctor.GroupTasks && p.Group != doctor.GroupBackup {
			t.Errorf("unexpected problem %+v", p)
		}
	}
	if !rep.Healthy() {
		t.Errorf("held tasks made the report unhealthy: %+v", rep.Problems)
	}
}

func TestDoctorIssueArchiveAwaitingRetry(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	main := f.task(t, issue, store.IssueWorktreeMain, nil, store.TaskDone)
	f.closeIssue(t, issue)

	rep := f.report(t)
	if ia := rep.IssueArchive; !ia.Known || !ia.Enabled || ia.Error != "" {
		t.Fatalf("issue_archive = %+v, want known, enabled, no error", ia)
	}
	wantReasons(t, rep, main.ID, doctor.HoldAwaitingRetry)
	if h := held(rep, main.ID); h.IssueID != issue || h.Role != store.IssueWorktreeMain || h.State != "done" {
		t.Errorf("held = %+v", h)
	}
	wantNoIssueArchiveProblem(t, rep)
}

func TestDoctorIssueArchiveDirtyRootAndLane(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	root := f.task(t, issue, "", nil, store.TaskDone)
	f.worktree(t, root, true)
	fan := f.task(t, issue, "", nil, store.TaskDone)
	f.worktree(t, fan, false)
	lane := f.task(t, 0, "", fan, store.TaskDone)
	f.worktree(t, lane, true)
	clean := f.task(t, issue, "", nil, store.TaskAborted)
	f.worktree(t, clean, false)
	gone := f.task(t, issue, "", nil, store.TaskDone)
	f.claimWorktree(t, gone, filepath.Join(f.dirs.Data, "worktrees", "never-made"))
	f.closeIssue(t, issue)

	rep := f.report(t)
	wantReasons(t, rep, root.ID, doctor.HoldDirty)
	wantReasons(t, rep, fan.ID, doctor.HoldDirty)
	wantReasons(t, rep, clean.ID, doctor.HoldAwaitingRetry)
	wantReasons(t, rep, gone.ID, doctor.HoldAwaitingRetry)
	if held(rep, lane.ID) != nil {
		t.Errorf("a lane is listed: %+v", rep.IssueArchive.Held)
	}
	wantNoIssueArchiveProblem(t, rep)
}

func TestDoctorIssueArchiveLockedByChat(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	task := f.task(t, issue, "", nil, store.TaskDone)
	f.claimWorktree(t, task, filepath.Join(f.dirs.Data, "worktrees", "linked"))
	c := &store.Chat{Title: "about it", Agent: "claude", PermissionMode: "full_auto"}
	if err := f.store.OpenLinkedChat(t.Context(), task.ID, store.TaskDone, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	f.closeIssue(t, issue)
	wantReasons(t, f.report(t), task.ID, doctor.HoldLockedByChat)
}

// TestDoctorIssueArchiveLockedLane is review F1: the cascade archives each
// lane through the ordinary Archive, which a lane's own linked chat refuses,
// so a settled root with a locked lane is locked_by_chat, not awaiting_retry.
func TestDoctorIssueArchiveLockedLane(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	root := f.task(t, issue, "", nil, store.TaskDone)
	lane := f.task(t, 0, "", root, store.TaskDone)
	f.claimWorktree(t, lane, filepath.Join(f.dirs.Data, "worktrees", "lane"))
	c := &store.Chat{Title: "about the lane", Agent: "claude", PermissionMode: "full_auto"}
	if err := f.store.OpenLinkedChat(t.Context(), lane.ID, store.TaskDone, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	f.closeIssue(t, issue)
	wantReasons(t, f.report(t), root.ID, doctor.HoldLockedByChat)
}

func TestDoctorIssueArchiveUnfinishedLane(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	root := f.task(t, issue, "", nil, store.TaskDone)
	f.task(t, 0, "", root, store.TaskRunning)
	f.closeIssue(t, issue)
	wantReasons(t, f.report(t), root.ID, doctor.HoldUnfinishedLane)
}

func TestDoctorIssueArchiveNotQuiescent(t *testing.T) {
	f := newIAFixture(t, nil)

	liveRoot := f.issue(t)
	done := f.task(t, liveRoot, "", nil, store.TaskDone)
	f.closeIssue(t, liveRoot)
	// A root created after the close holds the issue, and is not listed.
	later := f.task(t, liveRoot, "", nil, store.TaskQueued)

	liveChat := f.issue(t)
	other := f.task(t, liveChat, "", nil, store.TaskDone)
	c := &store.Chat{
		ProjectID: f.projectID, Title: "still talking", Agent: "claude", PermissionMode: "full_auto",
		BaseBranch: "main", IssueID: &liveChat,
	}
	if err := f.store.CreateIssueChat(t.Context(), c, func(id int64) string { return "vincent/chat-" + strconv.FormatInt(id, 10) }); err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	f.closeIssue(t, liveChat)

	rep := f.report(t)
	wantReasons(t, rep, done.ID, doctor.HoldIssueNotQuiescent)
	wantReasons(t, rep, other.ID, doctor.HoldIssueNotQuiescent)
	if held(rep, later.ID) != nil {
		t.Errorf("a root created after the close is listed: %+v", rep.IssueArchive.Held)
	}
}

func TestDoctorIssueArchiveHandoffClaimed(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	main := f.task(t, issue, store.IssueWorktreeMain, nil, store.TaskDone)
	ht := &store.Task{
		ProjectID: f.projectID, Title: "integrate", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/integrate", State: store.TaskQueued,
	}
	if err := f.store.CreateHandoff(t.Context(), &store.Handoff{CreatedBy: store.HandoffByHuman}, []int64{issue}, ht, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	f.closeIssue(t, issue)
	wantReasons(t, f.report(t), main.ID, doctor.HoldHandoffClaimed)
}

// TestDoctorIssueArchiveHeldForSideWork is review F3 as doctor reports it:
// the main task waits on its issue's held side task, and lists why.
func TestDoctorIssueArchiveHeldForSideWork(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	main := f.task(t, issue, store.IssueWorktreeMain, nil, store.TaskDone)
	side := f.task(t, issue, store.IssueWorktreeSide, nil, store.TaskDone)
	f.worktree(t, side, true)
	f.closeIssue(t, issue)

	rep := f.report(t)
	wantReasons(t, rep, side.ID, doctor.HoldDirty)
	wantReasons(t, rep, main.ID, doctor.HoldHeldForSideWork)
}

// TestDoctorIssueArchiveListsEveryReason: one task's reasons are listed
// together, so fixing one never reveals another.
func TestDoctorIssueArchiveListsEveryReason(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	task := f.task(t, issue, "", nil, store.TaskDone)
	f.worktree(t, task, true)
	c := &store.Chat{Title: "about it", Agent: "claude", PermissionMode: "full_auto"}
	if err := f.store.OpenLinkedChat(t.Context(), task.ID, store.TaskDone, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	f.task(t, 0, "", task, store.TaskRunning)
	f.closeIssue(t, issue)
	f.task(t, issue, "", nil, store.TaskQueued)

	rep := f.report(t)
	wantReasons(t, rep, task.ID,
		doctor.HoldDirty, doctor.HoldLockedByChat, doctor.HoldUnfinishedLane, doctor.HoldIssueNotQuiescent)
	wantNoIssueArchiveProblem(t, rep)
}

// TestDoctorIssueArchiveLeftoverChatDir is task 143 decision 15: a released
// issue chat's directory on a closed issue is reported while it exists.
func TestDoctorIssueArchiveLeftoverChatDir(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	// Outside the data roots, so gc's orphan scan has nothing to say.
	dir := filepath.Join(t.TempDir(), "chat-left")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	c := &store.Chat{
		ProjectID: f.projectID, Title: "done talking", Agent: "claude", PermissionMode: "full_auto",
		BaseBranch: "main", IssueID: &issue,
	}
	if err := f.store.CreateIssueChat(t.Context(), c, func(id int64) string { return "vincent/chat-" + strconv.FormatInt(id, 10) }); err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if _, err := f.store.SetChatWorktree(t.Context(), c.ID, dir, ""); err != nil {
		t.Fatalf("SetChatWorktree: %v", err)
	}
	if _, err := f.store.SetChatState(t.Context(), c.ID, chatstate.Closed); err != nil {
		t.Fatalf("close chat: %v", err)
	}
	f.closeIssue(t, issue)

	rep := f.report(t)
	want := []doctor.LeftoverChatDir{{IssueID: issue, ChatID: c.ID, Path: dir}}
	if !slices.Equal(rep.IssueArchive.ChatDirs, want) {
		t.Errorf("chat_dirs = %+v, want %+v", rep.IssueArchive.ChatDirs, want)
	}
	wantNoIssueArchiveProblem(t, rep)

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if got := f.report(t).IssueArchive.ChatDirs; len(got) != 0 {
		t.Errorf("chat_dirs after the directory went = %+v, want none", got)
	}
}

// TestDoctorIssueArchiveEmptyWhenOff: with archive_tasks_on_issue_close
// false nothing is held by a feature that is switched off.
func TestDoctorIssueArchiveEmptyWhenOff(t *testing.T) {
	f := newIAFixture(t, func(d *Deps) {
		d.Config = func() config.Config {
			c := config.Default()
			c.ArchiveTasksOnIssueClose = false
			return c
		}
	})
	issue := f.issue(t)
	f.task(t, issue, store.IssueWorktreeMain, nil, store.TaskDone)
	f.closeIssue(t, issue)

	ia := f.report(t).IssueArchive
	if !ia.Known || ia.Enabled || len(ia.Held) != 0 || len(ia.ChatDirs) != 0 {
		t.Errorf("issue_archive with the key off = %+v, want known, disabled and empty", ia)
	}
}

// TestDoctorIssueArchiveEmptyWhenNothingHeld: open issues and archived
// tasks are not candidates.
func TestDoctorIssueArchiveEmptyWhenNothingHeld(t *testing.T) {
	f := newIAFixture(t, nil)
	open := f.issue(t)
	f.task(t, open, store.IssueWorktreeMain, nil, store.TaskDone)
	closed := f.issue(t)
	f.task(t, closed, "", nil, store.TaskArchived)
	f.closeIssue(t, closed)

	ia := f.report(t).IssueArchive
	if !ia.Known || !ia.Enabled || len(ia.Held) != 0 || len(ia.ChatDirs) != 0 || ia.Error != "" {
		t.Errorf("issue_archive with nothing held = %+v, want known, enabled and empty", ia)
	}
}

// TestDoctorFixLeavesHeldTasksAlone: --fix offers nothing for the row and
// archives nothing.
func TestDoctorFixLeavesHeldTasksAlone(t *testing.T) {
	f := newIAFixture(t, nil)
	issue := f.issue(t)
	task := f.task(t, issue, "", nil, store.TaskDone)
	path := f.worktree(t, task, true)
	f.closeIssue(t, issue)

	res := f.fix(t, true)
	got, err := f.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.TaskDone || got.WorktreePath != path {
		t.Errorf("after --fix --force task = %s at %q, want done at %q", got.State, got.WorktreePath, path)
	}
	if _, err := os.Stat(filepath.Join(path, "wip.txt")); err != nil {
		t.Errorf("--fix touched the held worktree: %v", err)
	}
	wantReasons(t, *res.Report, task.ID, doctor.HoldDirty)
}
