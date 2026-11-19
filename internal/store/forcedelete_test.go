package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// TestForceDeleteTaskTreeFromAnyState: a force delete takes a task that is
// nowhere near archived — the case task 092's archived-only rule cannot
// reach — with its lanes, deepest first, and the chats tied to it.
func TestForceDeleteTaskTreeFromAnyState(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	parent := newTask(p.ID, "parent", TaskBlocked)
	parent.WorktreePath = "/wt/parent"
	mustCreate(t, s, parent)
	lane := newTask(p.ID, "lane", TaskDone)
	lane.ParentTaskID = &parent.ID
	mustCreate(t, s, lane)
	grand := newTask(p.ID, "grand", TaskAborted)
	grand.ParentTaskID = &lane.ID
	mustCreate(t, s, grand)
	linked := linkedChatOn(t, s, parent, "linked")

	gone, err := s.ForceDeleteTaskTree(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ForceDeleteTaskTree: %v", err)
	}
	var ids []int64
	for _, task := range gone.Tasks {
		ids = append(ids, task.ID)
	}
	if want := []int64{grand.ID, lane.ID, parent.ID}; !slices.Equal(ids, want) {
		t.Fatalf("removed %v, want deepest first %v", ids, want)
	}
	if gone.Tasks[2].WorktreePath != "/wt/parent" {
		t.Errorf("the removed row lost the worktree its cleanup needs: %+v", gone.Tasks[2])
	}
	if !slices.Contains(gone.ChatIDs, linked.ID) {
		t.Errorf("chat ids %v do not name the linked chat %d", gone.ChatIDs, linked.ID)
	}
	for _, id := range ids {
		if _, err := s.GetTask(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("task %d after force delete: %v, want ErrNotFound", id, err)
		}
	}
	if _, err := s.GetChat(ctx, linked.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("linked chat after force delete: %v, want ErrNotFound", err)
	}
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskDeleted}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("got %d task.deleted events, want one per removed task", len(events))
	}
}

// TestForceDeleteTaskTreeTakesTheChatHandedOffToIt: the chat that would be
// left pointing at nothing goes with its task (task 153).
func TestForceDeleteTaskTreeTakesTheChatHandedOffToIt(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	task := handOff(t, s, c, p.ID)

	gone, err := s.ForceDeleteTaskTree(ctx, task.ID)
	if err != nil {
		t.Fatalf("ForceDeleteTaskTree: %v", err)
	}
	if !slices.Contains(gone.ChatIDs, c.ID) {
		t.Errorf("chat ids %v do not name the handed-off chat %d", gone.ChatIDs, c.ID)
	}
	if _, err := s.GetChat(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("handed-off chat after force delete: %v, want ErrNotFound", err)
	}
}

// TestForceDeleteTaskTreeRefusesALiveProcess: a slot-holding task anywhere
// in the subtree, or a chat with a turn in flight on one, refuses the whole
// delete and removes nothing.
func TestForceDeleteTaskTreeRefusesALiveProcess(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	parent := newTask(p.ID, "parent", TaskAwaitingChildren)
	mustCreate(t, s, parent)
	lane := newTask(p.ID, "lane", TaskRunning)
	lane.ParentTaskID = &parent.ID
	mustCreate(t, s, lane)

	_, err := s.ForceDeleteTaskTree(ctx, parent.ID)
	assertRefused(t, err, DeleteRefusedLive)
	if _, err := s.GetTask(ctx, parent.ID); err != nil {
		t.Fatalf("the parent was deleted anyway: %v", err)
	}

	done := newTask(p.ID, "done", TaskDone)
	done.WorktreePath = "/wt/done"
	mustCreate(t, s, done)
	chat := linkedChatOn(t, s, done, "talking")
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	_, err = s.ForceDeleteTaskTree(ctx, done.ID)
	assertRefused(t, err, DeleteRefusedLive)
	if _, err := s.GetTask(ctx, done.ID); err != nil {
		t.Fatalf("the task was deleted under a live chat: %v", err)
	}
}

func TestForceDeleteTaskTreeIs404ForAnUnknownID(t *testing.T) {
	s := openTest(t)
	if _, err := s.ForceDeleteTaskTree(t.Context(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ForceDeleteTaskTree(9999): %v, want ErrNotFound", err)
	}
}

// TestForceDeleteChatFromAnyStateButALiveTurn: idle and handed-off chats go;
// a running one is refused.
func TestForceDeleteChatFromAnyStateButALiveTurn(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	idle := testChat(t, s, p.ID)
	got, err := s.ForceDeleteChat(ctx, idle.ID)
	if err != nil {
		t.Fatalf("ForceDeleteChat(idle): %v", err)
	}
	if got.WorktreePath != idle.WorktreePath {
		t.Errorf("returned worktree %q, want %q", got.WorktreePath, idle.WorktreePath)
	}
	if _, err := s.GetChat(ctx, idle.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetChat after force delete: %v, want ErrNotFound", err)
	}

	handed := testChat(t, s, p.ID)
	task := handOff(t, s, handed, p.ID)
	if _, err := s.ForceDeleteChat(ctx, handed.ID); err != nil {
		t.Fatalf("ForceDeleteChat(handed_off): %v", err)
	}
	if _, err := s.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("the task a chat was handed to went with it: %v", err)
	}

	live := testChat(t, s, p.ID)
	if _, err := s.SetChatState(ctx, live.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	_, err = s.ForceDeleteChat(ctx, live.ID)
	assertRefused(t, err, DeleteRefusedLive)
	if _, err := s.GetChat(ctx, live.ID); err != nil {
		t.Fatalf("the running chat was deleted anyway: %v", err)
	}
}

// TestIssueDeleteHoldersAndTheirBlocker: the holders are the unsettled main
// and side root tasks and the open issue chats; a live one among them is
// the blocker an issue's force delete asks about first.
func TestIssueDeleteHoldersAndTheirBlocker(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	held := newMainTask(p.ID, is.ID, "held")
	mustCreate(t, s, held)
	moveTask(t, s, held, TaskRunning, TaskBlocked)
	settled := newMainTask(p.ID, is.ID, "settled")
	mustCreate(t, s, settled)
	moveTask(t, s, settled, TaskRunning, TaskDone)

	tasks, chats, err := s.IssueDeleteHolders(ctx, is.ID)
	if err != nil {
		t.Fatalf("IssueDeleteHolders: %v", err)
	}
	if !slices.Equal(tasks, []int64{held.ID}) || len(chats) != 0 {
		t.Fatalf("holders = %v, %v; want [%d], []", tasks, chats, held.ID)
	}
	if err := s.ForceDeleteBlocker(ctx, is.ID, tasks, chats); err != nil {
		t.Fatalf("a blocked holder blocked the force delete: %v", err)
	}
	moveTask(t, s, held, TaskQueued, TaskRunning)
	err = s.ForceDeleteBlocker(ctx, is.ID, tasks, chats)
	assertRefused(t, err, DeleteRefusedLive)
}
