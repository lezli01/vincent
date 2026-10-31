package store

import (
	"errors"
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
