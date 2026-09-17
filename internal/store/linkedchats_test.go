package store

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// lockedFixture is a blocked task with a worktree and an open chat on it.
func lockedFixture(t *testing.T, s *Store) (*Task, *Chat) {
	t.Helper()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "stuck", TaskBlocked)
	task.WorktreePath = "/wt/task-1"
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &Chat{Title: "why", Agent: "claude", PermissionMode: "full_auto", OpeningContext: "ctx"}
	if err := s.OpenLinkedChat(t.Context(), task.ID, TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return task, c
}

// TestTheLockIsInsideTheSwap: a §6 transition out of a lockable state is
// refused by the store itself while a linked chat is open — whatever path the
// caller took to get there — and the task is left exactly as it was.
func TestTheLockIsInsideTheSwap(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task, c := lockedFixture(t, s)
	if c.WorktreePath != "" || c.Branch != task.BranchName || c.LinkedTaskID == nil {
		t.Errorf("linked chat = %+v, want the task's branch and no worktree of its own", c)
	}

	_, _, err := s.TransitionTask(ctx, task.ID, TaskBlocked, TaskQueued, TaskChange{})
	locked, ok := AsTaskLocked(err)
	if !ok || locked.ChatID != c.ID {
		t.Fatalf("TransitionTask while locked = %v, want a TaskLockedError naming chat %d", err, c.ID)
	}
	if got, _ := s.GetTask(ctx, task.ID); got.State != TaskBlocked {
		t.Errorf("state = %s after a refused swap", got.State)
	}
	// A second chat is the same refusal.
	second := &Chat{Title: "again", Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(ctx, task.ID, TaskBlocked, second); !errors.As(err, &locked) {
		t.Errorf("second open = %v, want TaskLockedError", err)
	}

	// Cancel closes the chat and aborts the task in the same commit.
	aborted, _, err := s.TransitionTask(ctx, task.ID, TaskBlocked, TaskAborted, TaskChange{CloseLinkedChats: true})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if aborted.State != TaskAborted {
		t.Errorf("task = %s, want aborted", aborted.State)
	}
	after, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != chatstate.Closed {
		t.Errorf("chat = %s, want closed", after.State)
	}
	if id, err := s.OpenLinkedChatID(ctx, task.ID); err != nil || id != 0 {
		t.Errorf("task still locked by %d (%v)", id, err)
	}
}

// TestOpenLosesARaceToAMove: the open's own transaction proves the state the
// context was assembled for, so an open racing a retry loses exactly one side.
func TestOpenLosesARaceToAMove(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "stuck", TaskBlocked)
	task.WorktreePath = "/wt/task-1"
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	// The retry wins the swap first.
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskBlocked, TaskQueued, TaskChange{}); err != nil {
		t.Fatal(err)
	}
	c := &Chat{Title: "late", Agent: "claude", PermissionMode: "full_auto"}
	err := s.OpenLinkedChat(ctx, task.ID, TaskBlocked, c)
	if _, ok := AsStateConflict(err); !ok {
		t.Fatalf("open after the task moved = %v, want a StateConflictError", err)
	}
	if id, _ := s.OpenLinkedChatID(ctx, task.ID); id != 0 {
		t.Errorf("the losing open still wrote chat %d", id)
	}

	noTree := newTask(p.ID, "no tree", TaskAborted)
	if err := s.CreateTask(ctx, noTree, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenLinkedChat(ctx, noTree.ID, TaskAborted, &Chat{Title: "x", Agent: "claude"}); !errors.Is(err, ErrTaskHasNoWorktree) {
		t.Errorf("open with no worktree = %v, want ErrTaskHasNoWorktree", err)
	}
}

// TestClosedIsTerminalEverywhere: closing is the linked table's alone, a
// closed chat stays closed against a late turn ending, and it is hidden,
// retained and deletable like every other terminal chat.
func TestClosedIsTerminalEverywhere(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task, c := lockedFixture(t, s)
	free := testChat(t, s, task.ProjectID)
	if _, err := s.CloseChat(ctx, free.ID); !errors.Is(err, ErrInvalidChatAction) {
		t.Errorf("closing a free chat = %v, want ErrInvalidChatAction", err)
	}
	closed, err := s.CloseChat(ctx, c.ID)
	if err != nil || closed.State != chatstate.Closed {
		t.Fatalf("CloseChat = %v, %v", closed, err)
	}
	if after, _ := s.SetChatState(ctx, c.ID, chatstate.Idle); after.State != chatstate.Closed {
		t.Errorf("a late idle write reopened the chat: %s", after.State)
	}
	taskID := task.ID
	open, err := s.ListChats(ctx, ChatFilter{TaskID: &taskID})
	if err != nil || len(open) != 0 {
		t.Errorf("default listing = %d chats (%v), want the closed one hidden", len(open), err)
	}
	all, err := s.ListChats(ctx, ChatFilter{TaskID: &taskID, Archived: ArchivedAll})
	if err != nil || len(all) != 1 {
		t.Errorf("archived=all listing = %d chats (%v), want 1", len(all), err)
	}
	ids, err := s.TerminalChatIDsBefore(ctx, time.Now().Add(time.Hour))
	if err != nil || len(ids) != 1 || ids[0] != c.ID {
		t.Errorf("retention = %v (%v), want the closed chat", ids, err)
	}
	if err := s.DeleteChatCascade(ctx, c.ID); err != nil {
		t.Errorf("deleting a closed chat: %v", err)
	}
}
