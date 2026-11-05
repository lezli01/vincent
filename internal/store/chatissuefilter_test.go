package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// taskOfIssue is a blocked task of an issue with a worktree of its own,
// which is what a linked chat needs to open on it (task 119).
func taskOfIssue(t *testing.T, s *Store, projectID, issueID int64, title string) *Task {
	t.Helper()
	task := newTask(projectID, title, TaskBlocked)
	task.IssueID = &issueID
	task.WorktreePath = "/wt/" + title
	mustCreate(t, s, task)
	return task
}

func linkedChatOn(t *testing.T, s *Store, task *Task, title string) *Chat {
	t.Helper()
	c := &Chat{Title: title, Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(t.Context(), task.ID, task.State, c); err != nil {
		t.Fatalf("OpenLinkedChat(%s): %v", title, err)
	}
	return c
}

func freeChat(t *testing.T, s *Store, projectID int64, title string) *Chat {
	t.Helper()
	c := &Chat{
		ProjectID: projectID, Title: title, Agent: "claude", PermissionMode: "full_auto",
		Branch: "vincent/chat-" + title, BaseBranch: "main", BaseSHA: "abc123",
		WorktreePath: "/wt/chat-" + title,
	}
	if err := s.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat(%s): %v", title, err)
	}
	return c
}

func chatIDs(cs []Chat) []int64 {
	out := []int64{}
	for i := range cs {
		out = append(out, cs[i].ID)
	}
	return out
}

// issueChatFixture is every way a chat can concern issue N, beside chats
// that concern another issue M or none (newIssueChatFixture builds it).
type issueChatFixture struct {
	s                        *Store
	projectID, other         int64
	issueID                  int64
	rootTaskID               int64
	issueArchived, issueLive *Chat // opened on N (task 140)
	onRoot, onLane           *Chat // linked to a task of N, and to a lane of one
	closedOnRoot             *Chat // linked, then closed
	handedOff                *Chat // free chat handed off into a task of N
}

func newIssueChatFixture(t *testing.T) issueChatFixture {
	t.Helper()
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	q := testProject(t, s, "p2")
	n := testIssue(t, s, p.ID, "issue n")
	m := testIssue(t, s, p.ID, "issue m")
	f := issueChatFixture{s: s, projectID: p.ID, other: q.ID, issueID: n.ID}

	// Opened on N, then archived, so a second one may open on it.
	var err error
	if f.issueArchived, err = openIssueChat(t, s, p.ID, n.ID, "vincent/n-chat-1"); err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if _, err := s.SetChatState(ctx, f.issueArchived.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}

	root := taskOfIssue(t, s, p.ID, n.ID, "root-n")
	f.rootTaskID = root.ID
	f.closedOnRoot = linkedChatOn(t, s, root, "closed on root")
	if _, err := s.CloseChat(ctx, f.closedOnRoot.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	f.onRoot = linkedChatOn(t, s, root, "on root")

	// A fan-out lane of a task of N carries N too (task 130 decision 5),
	// and the chat filter includes it where the task filter does not.
	parent := taskOfIssue(t, s, p.ID, n.ID, "parent-n")
	l := newTask(p.ID, "lane-n", TaskBlocked)
	l.ParentTaskID = &parent.ID
	idx := 0
	l.ParentStepIndex = &idx
	l.LaneID = "a"
	l.IssueID = &n.ID
	l.WorktreePath = "/wt/lane-n"
	mustCreate(t, s, l)
	f.onLane = linkedChatOn(t, s, l, "on lane")

	// A free chat handed off into a task of N (task 074).
	f.handedOff = freeChat(t, s, p.ID, "handoff")
	into := newTask(p.ID, "into-n", TaskQueued)
	into.IssueID = &n.ID
	into.BranchName, into.BaseBranch, into.BaseSHA = f.handedOff.Branch, f.handedOff.BaseBranch, f.handedOff.BaseSHA
	into.WorktreePath = f.handedOff.WorktreePath
	if _, err := s.HandoffChat(ctx, f.handedOff.ID, into); err != nil {
		t.Fatalf("HandoffChat: %v", err)
	}

	// Noise: a free chat, a chat opened on M, a chat linked to a task of M,
	// and a free chat in another project.
	freeChat(t, s, p.ID, "free")
	if _, err := openIssueChat(t, s, p.ID, m.ID, "vincent/m-chat"); err != nil {
		t.Fatalf("CreateIssueChat(m): %v", err)
	}
	linkedChatOn(t, s, taskOfIssue(t, s, p.ID, m.ID, "root-m"), "on m")
	freeChat(t, s, q.ID, "elsewhere")

	f.issueLive, err = openIssueChat(t, s, p.ID, n.ID, "vincent/n-chat-2")
	if err != nil {
		t.Fatalf("CreateIssueChat(second): %v", err)
	}
	return f
}

// TestListChatsByIssue is #931's filter: the chats that concern an issue are
// the ones opened on it, linked to any of its tasks or lanes, or handed off
// into one — and nothing on another issue or on none.
func TestListChatsByIssue(t *testing.T) {
	f := newIssueChatFixture(t)
	ctx := t.Context()
	list := func(cf ChatFilter) []int64 {
		t.Helper()
		cf.IssueID = f.issueID
		got, err := f.s.ListChats(ctx, cf)
		if err != nil {
			t.Fatalf("ListChats(%+v): %v", cf, err)
		}
		return chatIDs(got)
	}

	all := []int64{f.issueLive.ID, f.handedOff.ID, f.onLane.ID, f.onRoot.ID, f.closedOnRoot.ID, f.issueArchived.ID}
	if got := list(ChatFilter{Archived: ArchivedAll}); !reflect.DeepEqual(got, all) {
		t.Errorf("archived=all = %v, want %v newest first", got, all)
	}
	live := []int64{f.issueLive.ID, f.onLane.ID, f.onRoot.ID}
	if got := list(ChatFilter{}); !reflect.DeepEqual(got, live) {
		t.Errorf("default = %v, want the live ones %v", got, live)
	}
	terminal := map[int64]bool{f.handedOff.ID: true, f.closedOnRoot.ID: true, f.issueArchived.ID: true}
	gotTerminal := list(ChatFilter{Archived: ArchivedOnly})
	if len(gotTerminal) != len(terminal) {
		t.Errorf("archived=only = %v, want %d terminal chats", gotTerminal, len(terminal))
	}
	for _, id := range gotTerminal {
		if !terminal[id] {
			t.Errorf("archived=only listed live chat %d", id)
		}
	}
	// An explicit state wins over the archived default.
	if got := list(ChatFilter{States: []chatstate.State{chatstate.Closed}}); !reflect.DeepEqual(got, []int64{f.closedOnRoot.ID}) {
		t.Errorf("state=closed = %v, want [%d]", got, f.closedOnRoot.ID)
	}

	// It is an AND with the other filters.
	if got := list(ChatFilter{ProjectID: &f.other, Archived: ArchivedAll}); len(got) != 0 {
		t.Errorf("issue and another project = %v, want none", got)
	}
	if got := list(ChatFilter{ProjectID: &f.projectID, Archived: ArchivedAll}); !reflect.DeepEqual(got, all) {
		t.Errorf("issue and its project = %v, want %v", got, all)
	}
	root := f.rootTaskID
	if got := list(ChatFilter{TaskID: &root, Archived: ArchivedAll}); !reflect.DeepEqual(got, []int64{f.onRoot.ID, f.closedOnRoot.ID}) {
		t.Errorf("issue and task = %v, want the root's two chats", got)
	}
	// Paging applies after the filter.
	if got := list(ChatFilter{Archived: ArchivedAll, Limit: 2, Offset: 1}); !reflect.DeepEqual(got, all[1:3]) {
		t.Errorf("page = %v, want %v", got, all[1:3])
	}
	// An issue nothing names lists nothing.
	got, err := f.s.ListChats(ctx, ChatFilter{IssueID: 9999, Archived: ArchivedAll})
	if err != nil || len(got) != 0 {
		t.Errorf("unknown issue = %v (%v), want none", got, err)
	}
}

// TestLinkedChatEventsCarryTheTask pins what #931's event half relies on and
// task 119 already built: every event of a linked chat names the task in
// `linked_task_id`, and a free chat's events name none.
func TestLinkedChatEventsCarryTheTask(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "stuck", TaskBlocked)
	task.WorktreePath = "/wt/task-1"
	mustCreate(t, s, task)

	w := watchEvents(s)
	c := linkedChatOn(t, s, task, "why")
	if _, err := s.CloseChat(ctx, c.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range w.take() {
		if !strings.HasPrefix(e.Type, "chat.") {
			continue
		}
		var body struct {
			LinkedTaskID *int64 `json:"linked_task_id"`
		}
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatalf("%s payload: %v", e.Type, err)
		}
		if body.LinkedTaskID == nil || *body.LinkedTaskID != task.ID {
			t.Errorf("%s payload %s: want linked_task_id %d", e.Type, e.Payload, task.ID)
		}
		seen[e.Type] = true
	}
	for _, want := range []string{EventChatCreated, EventChatClosed} {
		if !seen[want] {
			t.Errorf("no %s event among %v", want, seen)
		}
	}

	free := freeChat(t, s, p.ID, "free")
	if _, err := s.SetChatState(ctx, free.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	for _, e := range w.take() {
		if strings.HasPrefix(e.Type, "chat.") && strings.Contains(string(e.Payload), "linked_task_id") {
			t.Errorf("a free chat's %s names a task: %s", e.Type, e.Payload)
		}
	}
}
