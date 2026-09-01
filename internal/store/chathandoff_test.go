package store

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

func testChat(t *testing.T, s *Store, projectID int64) *Chat {
	t.Helper()
	c := &Chat{
		ProjectID: projectID, Title: "a talk", Agent: "claude",
		Branch: "vincent/1-a-talk", BaseBranch: "main", BaseSHA: "abc123",
		WorktreePath: "/wt/chat-1", PermissionMode: "full_auto",
	}
	if err := s.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// TestHandoffChatIsOneTransaction is the atomicity the whole feature rests on:
// either the task, the link, the transition and both events all happened, or
// none of them did.
func TestHandoffChatIsOneTransaction(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)

	task := newTask(p.ID, "carry on", TaskQueued)
	task.BranchName, task.BaseBranch, task.BaseSHA = c.Branch, c.BaseBranch, c.BaseSHA
	task.WorktreePath = c.WorktreePath
	after, err := s.HandoffChat(ctx, c.ID, task)
	if err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}
	if after.State != chatstate.HandedOff {
		t.Errorf("state = %s, want handed_off", after.State)
	}
	if after.HandoffTaskID == nil || *after.HandoffTaskID != task.ID {
		t.Errorf("handoff_task_id = %v, want %d", after.HandoffTaskID, task.ID)
	}
	// The claim moved: the chat stops naming the directory in the same write
	// that gives it to the task.
	if after.WorktreePath != "" {
		t.Errorf("chat still claims %q", after.WorktreePath)
	}
	// The branch is kept as history — it is not a claim, and reading it back
	// is how a terminal chat still says what it worked on.
	if after.Branch != c.Branch || after.BaseBranch != c.BaseBranch || after.BaseSHA != c.BaseSHA {
		t.Errorf("the chat lost its history: %+v", after)
	}
	stored, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.WorktreePath != c.WorktreePath || stored.BranchName != c.Branch {
		t.Errorf("task workspace = %q/%q, want %q/%q",
			stored.WorktreePath, stored.BranchName, c.WorktreePath, c.Branch)
	}
	// Both events, durable, behind one commit.
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskCreated, EventChatHandedOff}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want task.created and chat.handed_off", len(events))
	}
	// And the reverse lookup, which is the only direction that is not stored.
	sources, err := s.SourceChatIDs(ctx)
	if err != nil {
		t.Fatalf("SourceChatIDs: %v", err)
	}
	if sources[task.ID] != c.ID {
		t.Errorf("SourceChatIDs[%d] = %d, want %d", task.ID, sources[task.ID], c.ID)
	}
}

// TestHandoffChatRollsBackOnABranchClash: the insert fails inside the
// transaction, so the chat is untouched and no task row survives.
func TestHandoffChatRollsBackOnABranchClash(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)

	holder := newTask(p.ID, "holder", TaskQueued)
	holder.BranchName = c.Branch
	if err := s.CreateTask(ctx, holder, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := newTask(p.ID, "carry on", TaskQueued)
	task.BranchName, task.WorktreePath = c.Branch, c.WorktreePath
	var claimed *BranchClaimedError
	if _, err := s.HandoffChat(ctx, c.ID, task); !errors.As(err, &claimed) {
		t.Fatalf("HandoffChat over a claimed branch = %v, want BranchClaimedError", err)
	}
	got, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != chatstate.Idle || got.WorktreePath != c.WorktreePath || got.HandoffTaskID != nil {
		t.Fatalf("a rolled-back handoff changed the chat: %+v", got)
	}
	tasks, err := s.ListTasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want only the holder", len(tasks))
	}
}

// TestHandoffChatLosesToAConcurrentSend is the compare-and-set. The store has
// one writer, so the two transactions serialize; whichever runs second must
// see the first and refuse, and the refusal is the same ErrInvalidChatAction
// any illegal §5.5 action produces.
func TestHandoffChatLosesToAConcurrentSend(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)

	if _, err := s.CreateChatTurn(ctx, c.ID, "hello"); err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	task := newTask(p.ID, "carry on", TaskQueued)
	task.BranchName, task.WorktreePath = c.Branch, c.WorktreePath
	if _, err := s.HandoffChat(ctx, c.ID, task); !errors.Is(err, ErrInvalidChatAction) {
		t.Fatalf("HandoffChat on a running chat = %v, want ErrInvalidChatAction", err)
	}
	// And the other order: a send after a handoff is refused just as flatly.
	if _, err := s.SetChatState(ctx, c.ID, chatstate.Idle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HandoffChat(ctx, c.ID, task); err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}
	if _, err := s.CreateChatTurn(ctx, c.ID, "still there?"); !errors.Is(err, ErrInvalidChatAction) {
		t.Fatalf("send after a handoff = %v, want ErrInvalidChatAction", err)
	}
}

// TestTerminalChatIDsBeforeCountsBothEndings is decision 6: retention treats
// handed_off exactly as it treats archived, measured from the same column.
func TestTerminalChatIDsBeforeCountsBothEndings(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	idle := testChat(t, s, p.ID)
	archived := testChat(t, s, p.ID)
	handed := testChat(t, s, p.ID)
	if _, err := s.SetChatState(ctx, archived.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	task := newTask(p.ID, "carry on", TaskQueued)
	task.BranchName = "vincent/9-carry-on"
	task.WorktreePath = handed.WorktreePath
	if _, err := s.HandoffChat(ctx, handed.ID, task); err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}
	ids, err := s.TerminalChatIDsBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("TerminalChatIDsBefore: %v", err)
	}
	want := map[int64]bool{archived.ID: true, handed.ID: true}
	if len(ids) != len(want) {
		t.Fatalf("terminal chats = %v, want %v", ids, want)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("chat %d is not terminal but was listed for pruning", id)
		}
		if id == idle.ID {
			t.Error("an idle chat was listed for pruning")
		}
	}
}
