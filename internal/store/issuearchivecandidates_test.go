package store

import (
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// candidatesOf is ClosedIssueArchiveCandidates narrowed to one issue.
func candidatesOf(t *testing.T, s *Store, issueID int64) []IssueArchiveCandidate {
	t.Helper()
	all, err := s.ClosedIssueArchiveCandidates(t.Context())
	if err != nil {
		t.Fatalf("ClosedIssueArchiveCandidates: %v", err)
	}
	var out []IssueArchiveCandidate
	for _, c := range all {
		if c.IssueID == issueID {
			out = append(out, c)
		}
	}
	return out
}

func candidateIDs(cs []IssueArchiveCandidate) []int64 {
	var ids []int64
	for _, c := range cs {
		ids = append(ids, c.TaskID)
	}
	return ids
}

// TestClosedIssueArchiveCandidatesParity (#929): over every case the
// archivable query is tested on, a quiescent, unclaimed issue's candidates
// are exactly ArchivableIssueTasks' answer, in its order; an issue that is
// not quiescent has nothing archivable and every candidate says why.
func TestClosedIssueArchiveCandidatesParity(t *testing.T) {
	for _, tc := range archiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			issueID, _ := seedArchiveCase(t, s, tc)
			cands := candidatesOf(t, s, issueID)
			archivable, err := s.ArchivableIssueTasks(t.Context(), issueID)
			if err != nil {
				t.Fatalf("ArchivableIssueTasks: %v", err)
			}
			quiescent := true
			for _, c := range cands {
				if c.HandoffClaimed {
					t.Errorf("candidate %d claimed with no handoff", c.TaskID)
				}
				if !c.Quiescent {
					quiescent = false
				}
				if c.Quiescent != cands[0].Quiescent {
					t.Errorf("candidates of one issue disagree on quiescence: %+v", cands)
				}
			}
			if quiescent {
				if !slices.Equal(candidateIDs(cands), archivable) {
					t.Errorf("candidates %v, archivable %v", candidateIDs(cands), archivable)
				}
				return
			}
			if len(archivable) != 0 {
				t.Errorf("issue not quiescent but archivable = %v", archivable)
			}
		})
	}
}

// TestClosedIssueArchiveCandidatesCoverHeldIssues: issue-level quiescence and
// a handoff claim never filter a candidate — they are reasons, reported on
// it — while the task-level rule still excludes open issues, archived and
// post-close roots, unsettled roots and lanes.
func TestClosedIssueArchiveCandidatesCoverHeldIssues(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	closeIssue := func(id int64) {
		execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
			closedAt.Format(TimeFormat), id)
	}

	open := testIssue(t, s, p.ID, "open")
	seedArchiveTasks(t, s, p.ID, open.ID, closedAt, []closeSeed{{role: "main", state: TaskDone, at: preClose}})

	busy := testIssue(t, s, p.ID, "busy")
	closeIssue(busy.ID)
	busyIDs := seedArchiveTasks(t, s, p.ID, busy.ID, closedAt, []closeSeed{
		{role: "main", state: TaskDone, at: preClose},
		{role: "side", state: TaskRunning, at: preClose},
		{role: "main", state: TaskDone, at: postClose},
		{role: "side", state: TaskArchived, at: preClose},
		{role: "", state: TaskDone, at: preClose, parent: 1},
	})

	claimed := testIssue(t, s, p.ID, "claimed")
	claimedIDs := seedArchiveTasks(t, s, p.ID, claimed.ID, closedAt, []closeSeed{{role: "main", state: TaskDone, at: preClose}})
	h := &Handoff{CreatedBy: HandoffByHuman}
	ht := newTask(p.ID, "integrate", TaskQueued)
	if err := s.CreateHandoff(t.Context(), h, []int64{claimed.ID}, ht, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	closeIssue(claimed.ID)

	if got := candidatesOf(t, s, open.ID); len(got) != 0 {
		t.Errorf("open issue candidates = %+v, want none", got)
	}
	got := candidatesOf(t, s, busy.ID)
	if !slices.Equal(candidateIDs(got), []int64{busyIDs[0]}) {
		t.Fatalf("busy candidates = %v, want [%d]", candidateIDs(got), busyIDs[0])
	}
	if c := got[0]; c.Quiescent || c.HandoffClaimed || c.Role != IssueWorktreeMain || c.State != TaskDone {
		t.Errorf("busy candidate = %+v, want a done main task, not quiescent, unclaimed", c)
	}
	got = candidatesOf(t, s, claimed.ID)
	if !slices.Equal(candidateIDs(got), claimedIDs) {
		t.Fatalf("claimed candidates = %v, want %v", candidateIDs(got), claimedIDs)
	}
	if c := got[0]; !c.Quiescent || !c.HandoffClaimed {
		t.Errorf("claimed candidate = %+v, want quiescent and claimed", c)
	}
}

// TestUnarchivedLaneWorktrees: every unarchived descendant that names a
// directory, at any depth; archived lanes and lanes with none are left out.
func TestUnarchivedLaneWorktrees(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "fan")
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ids := seedArchiveTasks(t, s, p.ID, is.ID, closedAt, []closeSeed{
		{state: TaskDone, at: preClose},
		{state: TaskDone, at: preClose, parent: 1},
		{state: TaskArchived, at: preClose, parent: 1},
		{state: TaskDone, at: preClose, parent: 2},
		{state: TaskDone, at: preClose, parent: 1},
	})
	for i, path := range []string{"/wt/root", "/wt/lane", "/wt/gone", "/wt/deep", ""} {
		execT(t, s, `UPDATE tasks SET worktree_path = ? WHERE id = ?`, path, ids[i])
	}
	got, err := s.UnarchivedLaneWorktrees(t.Context(), ids[0])
	if err != nil {
		t.Fatalf("UnarchivedLaneWorktrees: %v", err)
	}
	if want := []string{"/wt/lane", "/wt/deep"}; !slices.Equal(got, want) {
		t.Errorf("UnarchivedLaneWorktrees = %v, want %v", got, want)
	}
}

// TestClosedIssueIDs lists closed issues only, by id.
func TestClosedIssueIDs(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	a := testIssue(t, s, p.ID, "a")
	testIssue(t, s, p.ID, "b")
	c := testIssue(t, s, p.ID, "c")
	for _, id := range []int64{c.ID, a.ID} {
		execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
			time.Now().UTC().Format(TimeFormat), id)
	}
	got, err := s.ClosedIssueIDs(t.Context())
	if err != nil {
		t.Fatalf("ClosedIssueIDs: %v", err)
	}
	if want := []int64{a.ID, c.ID}; !slices.Equal(got, want) {
		t.Errorf("ClosedIssueIDs = %v, want %v", got, want)
	}
}

// TestSubtreeOpenLinkedChatID finds an open chat on the root or on a lane at
// any depth (review F1), and ignores a closed one.
func TestSubtreeOpenLinkedChatID(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "fan")
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ids := seedArchiveTasks(t, s, p.ID, is.ID, closedAt, []closeSeed{
		{state: TaskDone, at: preClose},
		{state: TaskDone, at: preClose, parent: 1},
		{state: TaskDone, at: preClose, parent: 2},
		{state: TaskDone, at: preClose},
	})
	for i, id := range ids {
		execT(t, s, `UPDATE tasks SET worktree_path = ? WHERE id = ?`, "/wt/"+string(rune('a'+i)), id)
	}
	subtree := func(id int64) int64 {
		t.Helper()
		got, err := s.SubtreeOpenLinkedChatID(t.Context(), id)
		if err != nil {
			t.Fatalf("SubtreeOpenLinkedChatID(%d): %v", id, err)
		}
		return got
	}
	if got := subtree(ids[0]); got != 0 {
		t.Fatalf("unlocked subtree = %d, want 0", got)
	}
	deep := &Chat{Title: "deep", Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(t.Context(), ids[2], TaskDone, deep); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	if got := subtree(ids[0]); got != deep.ID {
		t.Errorf("root with a locked deep lane = %d, want %d", got, deep.ID)
	}
	if got := subtree(ids[3]); got != 0 {
		t.Errorf("an unrelated root = %d, want 0", got)
	}
	if _, err := s.SetChatState(t.Context(), deep.ID, chatstate.Closed); err != nil {
		t.Fatalf("close chat: %v", err)
	}
	if got := subtree(ids[0]); got != 0 {
		t.Errorf("after the chat closed = %d, want 0", got)
	}
	own := &Chat{Title: "own", Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(t.Context(), ids[3], TaskDone, own); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	if got := subtree(ids[3]); got != own.ID {
		t.Errorf("a locked root = %d, want %d", got, own.ID)
	}
}
