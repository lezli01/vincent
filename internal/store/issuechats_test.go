package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
)

func openIssueChat(t *testing.T, s *Store, projectID, issueID int64, branch string) (*Chat, error) {
	t.Helper()
	c := &Chat{
		ProjectID: projectID, Title: "work it", Agent: "claude", PermissionMode: "full_auto",
		BaseBranch: "main", IssueID: &issueID,
	}
	err := s.CreateIssueChat(t.Context(), c, func(int64) string { return branch })
	return c, err
}

func laneOfIssue(t *testing.T, s *Store, id int64) (issuestate.Lane, bool) {
	t.Helper()
	iss, err := s.GetIssue(t.Context(), id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	return iss.Lane, iss.Attention
}

// TestIssueChatHoldsTheLineOfMainWork is task 140 decisions 2, 3 and 5 in
// the store: the chat cuts the issue's main branch, a main task created
// behind it joins that branch and waits (IssueOccupied) without the chat's
// directory counting as a foreign claim, closing the chat lets the task in
// and hands it the directory; the lane is in_progress throughout, attention
// rises while the chat awaits input, a later ready delivery makes it
// hand_off, and a chat opened after that makes it in_progress again.
func TestIssueChatHoldsTheLineOfMainWork(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	chat, err := openIssueChat(t, s, p.ID, is.ID, "vincent/issue-chat")
	if err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if _, err := s.ClaimChatWorktree(ctx, chat.ID, "/wt/chat", "base0", nil); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	if mw := mainWorktree(t, s, is.ID); mw.Branch != "vincent/issue-chat" || mw.OccupantChatID == nil ||
		*mw.OccupantChatID != chat.ID || mw.OccupantTaskID != nil {
		t.Errorf("main worktree = %+v, want the chat's branch held by chat %d", mw, chat.ID)
	}
	if lane, _ := laneOfIssue(t, s, is.ID); lane != issuestate.LaneInProgress {
		t.Errorf("lane with an idle chat = %s, want in_progress", lane)
	}
	if _, err := openIssueChat(t, s, p.ID, is.ID, "other"); err == nil {
		t.Error("a second live chat on the issue was accepted")
	} else if _, ok := AsIssueHasLiveChat(err); !ok {
		t.Errorf("second chat err = %v, want *IssueHasLiveChatError", err)
	}
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.Running); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.AwaitingInput); err != nil {
		t.Fatal(err)
	}
	if lane, att := laneOfIssue(t, s, is.ID); lane != issuestate.LaneInProgress || !att {
		t.Errorf("awaiting chat: lane %s attention %v, want in_progress with attention", lane, att)
	}
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
		t.Fatal(err)
	}

	task := newMainTask(p.ID, is.ID, "follow on")
	mustCreate(t, s, task)
	if task.BranchName != "vincent/issue-chat" {
		t.Errorf("main task branch = %q, want the chat's vincent/issue-chat", task.BranchName)
	}
	if c := candidateOf(t, s, task.ID); !c.IssueOccupied || c.DirClaimants != 0 {
		t.Errorf("behind a live chat: IssueOccupied %v DirClaimants %d, want true and 0", c.IssueOccupied, c.DirClaimants)
	}

	if _, err := s.CloseChat(ctx, chat.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	if c := candidateOf(t, s, task.ID); c.IssueOccupied || c.DirClaimants != 0 {
		t.Errorf("after close: IssueOccupied %v DirClaimants %d, want false and 0", c.IssueOccupied, c.DirClaimants)
	}
	if lane, _ := laneOfIssue(t, s, is.ID); lane != issuestate.LaneInProgress {
		t.Errorf("lane with a closed chat = %s, want in_progress", lane)
	}
	holder, err := s.IssueMainWorktreeChatHolder(ctx, is.ID)
	if err != nil || holder == nil || holder.ID != chat.ID {
		t.Fatalf("chat holder = %v, %v; want chat %d", holder, err, chat.ID)
	}
	if err := s.TransferIssueWorktreeFromChat(ctx, chat.ID, task.ID, "/wt/chat", "tip1"); err != nil {
		t.Fatalf("TransferIssueWorktreeFromChat: %v", err)
	}
	gotChat, err := s.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotTask, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotChat.WorktreePath != "" || gotChat.EndSHA != "tip1" || gotTask.WorktreePath != "/wt/chat" || gotTask.BaseSHA != "tip1" {
		t.Errorf("after transfer: chat (%q, %q) task (%q, %q)",
			gotChat.WorktreePath, gotChat.EndSHA, gotTask.WorktreePath, gotTask.BaseSHA)
	}

	moveTask(t, s, task, TaskRunning)
	if _, err := openIssueChat(t, s, p.ID, is.ID, "x"); err == nil {
		t.Error("a chat opened while a main task runs in the worktree")
	} else if occ, ok := AsIssueWorktreeOccupied(err); !ok || occ.TaskID != task.ID {
		t.Errorf("chat beside a running main task err = %v, want occupied by task %d", err, task.ID)
	}
	moveTask(t, s, task, TaskDone)
	if lane, _ := laneOfIssue(t, s, is.ID); lane != issuestate.LaneHandOff {
		t.Errorf("lane after a ready main task finished after the chat = %s, want hand_off", lane)
	}
	again, err := openIssueChat(t, s, p.ID, is.ID, "unused")
	if err != nil {
		t.Fatalf("chat after hand_off: %v", err)
	}
	if again.Branch != "vincent/issue-chat" {
		t.Errorf("later chat branch = %q, want the issue's main branch", again.Branch)
	}
	if lane, _ := laneOfIssue(t, s, is.ID); lane != issuestate.LaneInProgress {
		t.Errorf("lane after a chat reopened the work = %s, want in_progress", lane)
	}
	if err := s.DeleteIssue(ctx, is.ID, issuestate.Human); err == nil {
		t.Error("issue deleted under a live chat")
	} else if _, ok := AsIssueHasLiveChat(err); !ok {
		t.Errorf("delete err = %v, want *IssueHasLiveChatError", err)
	}
}

// TestLaneOfChatFact holds issuestate.LaneOf's fourth fact (task 140
// decision 5): a chat that is the latest work keeps the lane in_progress
// over a ready delivery; closed still wins.
func TestLaneOfChatFact(t *testing.T) {
	if got := issuestate.LaneOf(issuestate.Open, false, true, true, true); got != issuestate.LaneInProgress {
		t.Errorf("chat after a ready delivery = %s, want in_progress", got)
	}
	if got := issuestate.LaneOf(issuestate.Open, false, false, false, true); got != issuestate.LaneInProgress {
		t.Errorf("chat on a fresh issue = %s, want in_progress", got)
	}
	if got := issuestate.LaneOf(issuestate.Closed, false, false, false, true); got != issuestate.LaneDone {
		t.Errorf("chat on a closed issue = %s, want done", got)
	}
}

// TestDeleteReleasedIssueChatKeepsTheIssuesWork is review F1 (task 140
// decisions 3 and 4): a released issue chat's row is the claim on the
// directory it left for the next main task and may be the only record of the
// issue's main branch, so a permanent delete is refused while it is either —
// and allowed once a main task carries the branch and the directory is gone.
func TestDeleteReleasedIssueChatKeepsTheIssuesWork(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	chat, err := openIssueChat(t, s, p.ID, is.ID, "vincent/issue-chat")
	if err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if _, err := s.ClaimChatWorktree(ctx, chat.ID, "/wt/chat", "base0", nil); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	if _, err := s.CloseChat(ctx, chat.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	wantRefused := func(reason string) {
		t.Helper()
		err := s.DeleteChatCascade(ctx, chat.ID)
		var refused *DeleteRefusedError
		if !errors.As(err, &refused) || refused.Reason != reason {
			t.Fatalf("DeleteChatCascade = %v, want refused %s", err, reason)
		}
		if mw := mainWorktree(t, s, is.ID); mw.Branch != "vincent/issue-chat" {
			t.Fatalf("after the refusal the main branch reads %q", mw.Branch)
		}
	}
	wantRefused(DeleteRefusedIssueChatWorktree)

	// The directory goes (an archive with nothing waiting does this); the
	// row is now the only record of the main branch.
	if _, err := s.SetChatWorktree(ctx, chat.ID, "", ""); err != nil {
		t.Fatalf("SetChatWorktree: %v", err)
	}
	wantRefused(DeleteRefusedIssueChatBranch)

	task := newMainTask(p.ID, is.ID, "follow on")
	mustCreate(t, s, task)
	if task.BranchName != "vincent/issue-chat" {
		t.Fatalf("main task branch = %q, want the chat's", task.BranchName)
	}
	if err := s.DeleteChatCascade(ctx, chat.ID); err != nil {
		t.Fatalf("delete once a main task carries the branch: %v", err)
	}
	if mw := mainWorktree(t, s, is.ID); mw.Branch != "vincent/issue-chat" {
		t.Errorf("after the delete the main branch reads %q", mw.Branch)
	}
}

// TestDiscardUnplacedIssueChat is the create rollback (review F1): an issue
// chat whose placement failed goes whatever its branch, but one that came to
// name a directory is kept.
func TestDiscardUnplacedIssueChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	chat, err := openIssueChat(t, s, p.ID, is.ID, "vincent/issue-chat")
	if err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if err := s.DiscardUnplacedIssueChat(ctx, chat.ID); err != nil {
		t.Fatalf("DiscardUnplacedIssueChat: %v", err)
	}
	if _, err := s.GetChat(ctx, chat.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetChat after discard = %v, want ErrNotFound", err)
	}
	placed, err := openIssueChat(t, s, p.ID, is.ID, "vincent/issue-chat")
	if err != nil {
		t.Fatalf("CreateIssueChat again: %v", err)
	}
	if _, err := s.ClaimChatWorktree(ctx, placed.ID, "/wt/chat", "base0", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardUnplacedIssueChat(ctx, placed.ID); err == nil {
		t.Error("a chat naming a directory was discarded")
	}
}

// TestAdmissionRechecksTheIssueChat is review F2: the scheduler reads
// occupancy before it marks a task running, and a chat may be opened on the
// issue in between. The admission's own transaction re-checks, so whichever
// of the two commits first wins and the other is refused cleanly.
func TestAdmissionRechecksTheIssueChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	is := testIssue(t, s, p.ID, "chat first")
	task := newMainTask(p.ID, is.ID, "queued")
	mustCreate(t, s, task)
	if c := candidateOf(t, s, task.ID); c.IssueOccupied {
		t.Fatal("a queued main task saw its issue occupied before any chat")
	}
	chat, err := openIssueChat(t, s, p.ID, is.ID, "unused")
	if err != nil {
		t.Fatalf("CreateIssueChat beside a queued main task: %v", err)
	}
	_, _, err = s.TransitionTask(ctx, task.ID, TaskQueued, TaskRunning, TaskChange{IssueMainFree: true})
	if occ, ok := AsIssueWorktreeOccupied(err); !ok || occ.ChatID != chat.ID {
		t.Fatalf("admission behind a chat created after the read = %v, want occupied by chat %d", err, chat.ID)
	}
	if got, err := s.GetTask(ctx, task.ID); err != nil || got.State != TaskQueued {
		t.Fatalf("task after the refused admission = %v, %v; want queued", got, err)
	}

	other := testIssue(t, s, p.ID, "admission first")
	first := newMainTask(p.ID, other.ID, "admitted")
	mustCreate(t, s, first)
	if _, _, err := s.TransitionTask(ctx, first.ID, TaskQueued, TaskRunning, TaskChange{IssueMainFree: true}); err != nil {
		t.Fatalf("admission with no chat: %v", err)
	}
	if _, err := openIssueChat(t, s, p.ID, other.ID, "unused2"); err == nil {
		t.Error("a chat opened beside a just-admitted main task")
	} else if occ, ok := AsIssueWorktreeOccupied(err); !ok || occ.TaskID != first.ID {
		t.Errorf("chat after admission = %v, want occupied by task %d", err, first.ID)
	}
}

// TestIssueChatDeletesAnnounceLaneMoves is review F8: removing an issue
// chat's row moves the issue's lane, and both ways of removing it — the
// create rollback and a permanent delete — announce that move after the
// chat.deleted, as every other lane-moving write does.
func TestIssueChatDeletesAnnounceLaneMoves(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	is := testIssue(t, s, p.ID, "rolled back")
	chat, err := openIssueChat(t, s, p.ID, is.ID, "vincent/rolled-back")
	if err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	w := watchEvents(s)
	if err := s.DiscardUnplacedIssueChat(ctx, chat.ID); err != nil {
		t.Fatalf("DiscardUnplacedIssueChat: %v", err)
	}
	wantLaneMove(t, s, w.take(), EventChatDeleted, is.ID, issuestate.LaneInProgress, issuestate.LaneOpen, 0, issuestate.Human)

	done := testIssue(t, s, p.ID, "deleted")
	task := newMainTask(p.ID, done.ID, "delivered")
	mustCreate(t, s, task)
	moveTask(t, s, task, TaskRunning, TaskDone)
	later, err := openIssueChat(t, s, p.ID, done.ID, "unused")
	if err != nil {
		t.Fatalf("CreateIssueChat after the delivery: %v", err)
	}
	if _, err := s.SetChatState(ctx, later.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	if lane, _ := laneOfIssue(t, s, done.ID); lane != issuestate.LaneInProgress {
		t.Fatalf("lane with an archived chat after the delivery = %s, want in_progress", lane)
	}
	w.take()
	if err := s.DeleteChatCascade(ctx, later.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}
	wantLaneMove(t, s, w.take(), EventChatDeleted, done.ID, issuestate.LaneInProgress, issuestate.LaneHandOff, 0, issuestate.Human)
}

// TestIssueChatEventsNameTheIssue is task 140.9: every chat.* event of a
// chat opened on an issue carries issue_id, so a client showing the issue
// re-reads it when the chat's state moves its occupant or attention. A free
// chat's events carry none.
func TestIssueChatEventsNameTheIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	w := watchEvents(s)
	chat, err := openIssueChat(t, s, p.ID, is.ID, "vincent/issue-chat")
	if err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.Running); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseChat(ctx, chat.ID); err == nil {
		t.Fatal("closed a running chat")
	}
	if _, err := s.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseChat(ctx, chat.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range w.take() {
		if !strings.HasPrefix(e.Type, "chat.") {
			continue
		}
		var body struct {
			IssueID *int64 `json:"issue_id"`
		}
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatalf("%s payload: %v", e.Type, err)
		}
		if body.IssueID == nil || *body.IssueID != is.ID {
			t.Errorf("%s payload %s: want issue_id %d", e.Type, e.Payload, is.ID)
		}
		seen[e.Type] = true
	}
	for _, want := range []string{EventChatCreated, EventChatClosed} {
		if !seen[want] {
			t.Errorf("no %s event among %v", want, seen)
		}
	}

	free := &Chat{ProjectID: p.ID, Title: "free", Agent: "claude", PermissionMode: "full_auto", BaseBranch: "main"}
	if err := s.CreateChat(ctx, free); err != nil {
		t.Fatal(err)
	}
	for _, e := range w.take() {
		if e.Type == EventChatCreated && strings.Contains(string(e.Payload), "issue_id") {
			t.Errorf("a free chat's %s names an issue: %s", e.Type, e.Payload)
		}
	}
}
