package store

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// moveTask walks task through the given states with TransitionTask.
func moveTask(t *testing.T, s *Store, task *Task, states ...TaskState) {
	t.Helper()
	from := task.State
	for _, to := range states {
		got, _, err := s.TransitionTask(t.Context(), task.ID, from, to, TaskChange{})
		if err != nil {
			t.Fatalf("transition task %d %s → %s: %v", task.ID, from, to, err)
		}
		*task = *got
		from = to
	}
}

// TestTransferIssueWorktreeMovesTheDirectory: the predecessor lets go of the
// directory and keeps its tip as end_sha; the successor names the directory
// and starts from that tip, with no base refresh recorded (task 134.12). One
// row names the directory before and after.
func TestTransferIssueWorktreeMovesTheDirectory(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	pred := newMainTask(p.ID, is.ID, "pred")
	mustCreate(t, s, pred)
	refresh := &BaseRefresh{}
	if err := s.ClaimTaskWorktree(ctx, pred.ID, "/wt/pred", "base0", refresh); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	moveTask(t, s, pred, TaskRunning, TaskDone)
	succ := newMainTask(p.ID, is.ID, "succ")
	mustCreate(t, s, succ)

	if err := s.TransferIssueWorktree(ctx, pred.ID, succ.ID, "/wt/pred", "tip1"); err != nil {
		t.Fatalf("TransferIssueWorktree: %v", err)
	}
	gotPred, err := s.GetTask(ctx, pred.ID)
	if err != nil {
		t.Fatalf("GetTask(pred): %v", err)
	}
	if gotPred.WorktreePath != "" || gotPred.EndSHA != "tip1" {
		t.Errorf("pred = (path %q, end_sha %q), want (\"\", tip1)", gotPred.WorktreePath, gotPred.EndSHA)
	}
	gotSucc, err := s.GetTask(ctx, succ.ID)
	if err != nil {
		t.Fatalf("GetTask(succ): %v", err)
	}
	if gotSucc.WorktreePath != "/wt/pred" || gotSucc.BaseSHA != "tip1" || gotSucc.BaseRefresh != nil {
		t.Errorf("succ = (path %q, base_sha %q, base_refresh %v), want (/wt/pred, tip1, nil)",
			gotSucc.WorktreePath, gotSucc.BaseSHA, gotSucc.BaseRefresh)
	}

	claims, err := s.ListWorktreeClaims(ctx)
	if err != nil {
		t.Fatalf("ListWorktreeClaims: %v", err)
	}
	var owners []int64
	for _, c := range claims {
		if c.Path == "/wt/pred" {
			owners = append(owners, c.TaskID)
		}
	}
	if len(owners) != 1 || owners[0] != succ.ID {
		t.Errorf("claims on /wt/pred = %v, want [%d]", owners, succ.ID)
	}
}

// TestTransferIssueWorktreeFailsClosed: a predecessor that no longer names
// the directory, or a successor that already names one, refuses the transfer
// with ErrIssueWorktreeNotHeld and writes nothing to either row.
func TestTransferIssueWorktreeFailsClosed(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	pred := newMainTask(p.ID, is.ID, "pred")
	mustCreate(t, s, pred)
	if err := s.ClaimTaskWorktree(ctx, pred.ID, "/wt/pred", "base0", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	moveTask(t, s, pred, TaskRunning, TaskDone)
	succ := newMainTask(p.ID, is.ID, "succ")
	mustCreate(t, s, succ)

	unchanged := func(label string) {
		t.Helper()
		gotPred, err := s.GetTask(ctx, pred.ID)
		if err != nil {
			t.Fatalf("GetTask(pred): %v", err)
		}
		gotSucc, err := s.GetTask(ctx, succ.ID)
		if err != nil {
			t.Fatalf("GetTask(succ): %v", err)
		}
		if gotPred.WorktreePath != "/wt/pred" || gotPred.EndSHA != "" ||
			gotSucc.WorktreePath != succ.WorktreePath || gotSucc.BaseSHA != succ.BaseSHA {
			t.Errorf("%s: rows written: pred (%q, %q), succ (%q, %q)", label,
				gotPred.WorktreePath, gotPred.EndSHA, gotSucc.WorktreePath, gotSucc.BaseSHA)
		}
	}

	err := s.TransferIssueWorktree(ctx, pred.ID, succ.ID, "/wt/elsewhere", "tip1")
	if !errors.Is(err, ErrIssueWorktreeNotHeld) {
		t.Fatalf("wrong path: err = %v, want ErrIssueWorktreeNotHeld", err)
	}
	unchanged("wrong path")

	if err := s.ClaimTaskWorktree(ctx, succ.ID, "/wt/succ", "base1", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(succ): %v", err)
	}
	succ.WorktreePath, succ.BaseSHA = "/wt/succ", "base1"
	err = s.TransferIssueWorktree(ctx, pred.ID, succ.ID, "/wt/pred", "tip1")
	if !errors.Is(err, ErrIssueWorktreeNotHeld) {
		t.Fatalf("successor already holding: err = %v, want ErrIssueWorktreeNotHeld", err)
	}
	unchanged("successor already holding")
}

// TestIssueMainWorktreeHolder finds the main task naming the issue's
// directory, and ignores the excluded task, archived rows and side tasks.
func TestIssueMainWorktreeHolder(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	holder := func(exclude int64) *Task {
		t.Helper()
		got, err := s.IssueMainWorktreeHolder(ctx, is.ID, exclude)
		if err != nil {
			t.Fatalf("IssueMainWorktreeHolder: %v", err)
		}
		return got
	}

	first := newMainTask(p.ID, is.ID, "first")
	mustCreate(t, s, first)
	if got := holder(0); got != nil {
		t.Fatalf("holder before any worktree = task %d, want none", got.ID)
	}
	side := newTask(p.ID, "side", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictBlock
	mustCreate(t, s, side)
	if err := s.ClaimTaskWorktree(ctx, side.ID, "/wt/side", "base", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(side): %v", err)
	}
	if got := holder(0); got != nil {
		t.Fatalf("holder with only a side worktree = task %d, want none", got.ID)
	}

	if err := s.ClaimTaskWorktree(ctx, first.ID, "/wt/main", "base", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(first): %v", err)
	}
	if got := holder(0); got == nil || got.ID != first.ID || got.WorktreePath != "/wt/main" {
		t.Fatalf("holder = %v, want task %d on /wt/main", got, first.ID)
	}
	if got := holder(first.ID); got != nil {
		t.Fatalf("holder excluding the holder = task %d, want none", got.ID)
	}

	// An archived row that still names its directory — an archive
	// interrupted before the release — is not the issue's holder.
	moveTask(t, s, first, TaskRunning, TaskDone, TaskArchived)
	if got := holder(0); got != nil {
		t.Fatalf("holder after archive = task %d, want none", got.ID)
	}
}

// TestEndSHAPersistsThroughArchive: the archive stamps end_sha in the same
// write as the → archived transition (task 134.12).
func TestEndSHAPersistsThroughArchive(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	task := newMainTask(p.ID, is.ID, "main")
	mustCreate(t, s, task)
	moveTask(t, s, task, TaskRunning, TaskDone)

	tip, empty := "tip9", ""
	got, _, err := s.TransitionTask(ctx, task.ID, TaskDone, TaskArchived,
		TaskChange{EndSHA: &tip, WorktreePath: &empty})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got.EndSHA != tip {
		t.Errorf("returned EndSHA = %q, want %q", got.EndSHA, tip)
	}
	stored, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.EndSHA != tip || stored.State != TaskArchived {
		t.Errorf("stored = (%s, end_sha %q), want (archived, %q)", stored.State, stored.EndSHA, tip)
	}
}

// TestDeleteIssueRefusedWhileAMainTaskIsLive: an unsettled main task holds
// the issue's delete, naming the lowest such task; settled, archived and
// side tasks do not.
func TestDeleteIssueRefusedWhileAMainTaskIsLive(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	done := newMainTask(p.ID, is.ID, "done")
	mustCreate(t, s, done)
	moveTask(t, s, done, TaskRunning, TaskDone)
	archived := newMainTask(p.ID, is.ID, "archived")
	mustCreate(t, s, archived)
	moveTask(t, s, archived, TaskRunning, TaskDone, TaskArchived)
	side := newTask(p.ID, "side", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictBlock
	mustCreate(t, s, side)

	live := newMainTask(p.ID, is.ID, "live")
	mustCreate(t, s, live)
	later := newMainTask(p.ID, is.ID, "later")
	mustCreate(t, s, later)

	for _, states := range [][]TaskState{nil, {TaskRunning}, {TaskBlocked}} {
		moveTask(t, s, live, states...)
		err := s.DeleteIssue(ctx, is.ID, issuestate.Human)
		e, ok := AsIssueHasLiveMainTask(err)
		if !ok || e.IssueID != is.ID || e.TaskID != live.ID {
			t.Fatalf("delete with main task %s: err = %v, want IssueHasLiveMainTaskError naming task %d",
				live.State, err, live.ID)
		}
		if _, err := s.GetIssue(ctx, is.ID); err != nil {
			t.Fatalf("issue gone after a refused delete: %v", err)
		}
	}

	moveTask(t, s, live, TaskAborted)
	moveTask(t, s, later, TaskAborted)
	if err := s.DeleteIssue(ctx, is.ID, issuestate.Human); err != nil {
		t.Fatalf("delete with every main task settled: %v", err)
	}
}

// TestReceivingAWorktreeClearsEndSHA is review F4 of #770: a main task that
// handed its directory on and is later given one again — by transfer, or by
// a fresh claim — is working once more, so the end_sha an earlier hand-over
// stamped no longer bounds its range.
func TestReceivingAWorktreeClearsEndSHA(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	a := newMainTask(p.ID, is.ID, "a")
	mustCreate(t, s, a)
	if err := s.ClaimTaskWorktree(ctx, a.ID, "/wt/a", "", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(a): %v", err)
	}
	moveTask(t, s, a, TaskRunning, TaskDone)
	b := newMainTask(p.ID, is.ID, "b")
	mustCreate(t, s, b)
	if err := s.TransferIssueWorktree(ctx, a.ID, b.ID, "/wt/a", "tip1"); err != nil {
		t.Fatalf("TransferIssueWorktree(a → b): %v", err)
	}
	moveTask(t, s, b, TaskRunning, TaskDone)

	// Back to a, by transfer.
	if err := s.TransferIssueWorktree(ctx, b.ID, a.ID, "/wt/a", "tip2"); err != nil {
		t.Fatalf("TransferIssueWorktree(b → a): %v", err)
	}
	if got, err := s.GetTask(ctx, a.ID); err != nil || got.EndSHA != "" || got.BaseSHA != "tip2" {
		t.Errorf("a after receiving = (end_sha %q, base_sha %q, %v), want no end_sha and base tip2",
			got.EndSHA, got.BaseSHA, err)
	}

	// b, now handed on with end_sha tip2, receives a fresh worktree.
	if err := s.ClaimTaskWorktree(ctx, b.ID, "/wt/b", "tip3", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree(b): %v", err)
	}
	if got, err := s.GetTask(ctx, b.ID); err != nil || got.EndSHA != "" || got.WorktreePath != "/wt/b" {
		t.Errorf("b after claiming = (end_sha %q, path %q, %v), want no end_sha in /wt/b",
			got.EndSHA, got.WorktreePath, err)
	}
}

// TestTransferIssueWorktreeRefusedWhileAChatIsOpen is review F5 of #770: a
// chat opened on the predecessor after the scheduler admitted the successor
// is working in the directory, so the transfer fails closed — naming the
// chat — and writes nothing. Once the chat closes the transfer goes through,
// and from then on no chat can open on the predecessor, which names no
// directory.
func TestTransferIssueWorktreeRefusedWhileAChatIsOpen(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	pred := newMainTask(p.ID, is.ID, "pred")
	mustCreate(t, s, pred)
	if err := s.ClaimTaskWorktree(ctx, pred.ID, "/wt/pred", "", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	moveTask(t, s, pred, TaskRunning, TaskDone)
	succ := newMainTask(p.ID, is.ID, "succ")
	mustCreate(t, s, succ)
	chat := linkChat(t, s, pred, TaskDone)

	err := s.TransferIssueWorktree(ctx, pred.ID, succ.ID, "/wt/pred", "tip1")
	if locked, ok := AsTaskLocked(err); !ok || locked.ChatID != chat.ID {
		t.Fatalf("TransferIssueWorktree with chat %d open = %v, want a TaskLockedError naming it", chat.ID, err)
	}
	if got, err := s.GetTask(ctx, pred.ID); err != nil || got.WorktreePath != "/wt/pred" || got.EndSHA != "" {
		t.Errorf("pred = (path %q, end_sha %q, %v), want it untouched", got.WorktreePath, got.EndSHA, err)
	}
	if got, err := s.GetTask(ctx, succ.ID); err != nil || got.WorktreePath != "" {
		t.Errorf("succ = (path %q, %v), want nothing", got.WorktreePath, err)
	}

	if _, err := s.CloseChat(ctx, chat.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	if err := s.TransferIssueWorktree(ctx, pred.ID, succ.ID, "/wt/pred", "tip1"); err != nil {
		t.Fatalf("TransferIssueWorktree after the chat closed: %v", err)
	}
	late := &Chat{Title: "late", Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(ctx, pred.ID, TaskDone, late); !errors.Is(err, ErrTaskHasNoWorktree) {
		t.Errorf("OpenLinkedChat on the handed-on predecessor = %v, want ErrTaskHasNoWorktree", err)
	}
}
