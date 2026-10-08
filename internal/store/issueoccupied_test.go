package store

import (
	"testing"
	"time"
)

// candidateOf finds taskID among ListAdmissible's rows.
func candidateOf(t *testing.T, s *Store, taskID int64) Candidate {
	t.Helper()
	cands, err := s.ListAdmissible(t.Context())
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	for _, c := range cands {
		if c.Task.ID == taskID {
			return c
		}
	}
	t.Fatalf("task %d is not an admission candidate", taskID)
	return Candidate{}
}

// linkChat opens a chat on task, which must be in state want.
func linkChat(t *testing.T, s *Store, task *Task, want TaskState) *Chat {
	t.Helper()
	c := &Chat{Title: "look", Agent: "claude", PermissionMode: "full_auto"}
	if err := s.OpenLinkedChat(t.Context(), task.ID, want, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return c
}

// TestIssueOccupiedFollowsTheOccupantDefinition is decision 8 as amended
// 2026-10-07, read through ListAdmissible: a main candidate is held by
// another main task of its issue that was admitted and has not settled, or
// that settled with its worktree kept open by a linked chat — and by nothing
// else. GetIssueMainWorktree, which serves the creation hint and the issue
// row, names the same occupant, so the hint and the scheduler agree.
func TestIssueOccupiedFollowsTheOccupantDefinition(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name     string
		state    TaskState
		admitted bool
		worktree bool
		// chat: "" none, "open", "closed".
		chat     string
		archived bool
		occupies bool
	}{
		{name: "running", state: TaskRunning, admitted: true, occupies: true},
		{name: "blocked", state: TaskBlocked, admitted: true, occupies: true},
		{name: "awaiting_gate", state: TaskAwaitingGate, admitted: true, occupies: true},
		{name: "paused", state: TaskPaused, admitted: true, occupies: true},
		{name: "awaiting_children", state: TaskAwaitingChildren, admitted: true, occupies: true},
		{name: "re-queued after an interrupt", state: TaskQueued, admitted: true, occupies: true},
		{name: "done with an open chat", state: TaskDone, admitted: true, worktree: true, chat: "open", occupies: true},
		{name: "aborted with an open chat", state: TaskAborted, admitted: true, worktree: true, chat: "open", occupies: true},
		{name: "queued, never started", state: TaskQueued},
		{name: "done", state: TaskDone, admitted: true, worktree: true},
		{name: "aborted", state: TaskAborted, admitted: true, worktree: true},
		{name: "done, chat closed", state: TaskDone, admitted: true, worktree: true, chat: "closed"},
		{name: "archived with an open chat", state: TaskDone, admitted: true, worktree: true, chat: "open", archived: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			p := testProject(t, s, "p1")
			is := testIssue(t, s, p.ID, "the issue")

			occ := newMainTask(p.ID, is.ID, "occupant")
			occ.State = tc.state
			if tc.admitted {
				occ.StartedAt = &started
			}
			if tc.worktree {
				occ.WorktreePath = "/wt/occupant"
			}
			mustCreate(t, s, occ)
			if tc.chat != "" {
				c := linkChat(t, s, occ, tc.state)
				if tc.chat == "closed" {
					if _, err := s.CloseChat(t.Context(), c.ID); err != nil {
						t.Fatalf("CloseChat: %v", err)
					}
				}
			}
			if tc.archived {
				// The archive path closes linked chats itself; set the column
				// directly so the clause that must reject it is what is tested.
				if _, err := s.db.ExecContext(t.Context(), `UPDATE tasks SET archived_at = ? WHERE id = ?`,
					formatTime(time.Now()), occ.ID); err != nil {
					t.Fatalf("archive: %v", err)
				}
			}
			waiter := newMainTask(p.ID, is.ID, "waiter")
			mustCreate(t, s, waiter)

			if got := candidateOf(t, s, waiter.ID).IssueOccupied; got != tc.occupies {
				t.Errorf("IssueOccupied = %v, want %v", got, tc.occupies)
			}
			mw := mainWorktree(t, s, is.ID)
			holds := mw.OccupantTaskID != nil && *mw.OccupantTaskID == occ.ID
			if holds != tc.occupies {
				t.Errorf("GetIssueMainWorktree occupant = %v, want occupant %v: %v", mw.OccupantTaskID, occ.ID, tc.occupies)
			}
			iss, err := s.GetIssue(t.Context(), is.ID)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if (iss.MainWorktree.OccupantTaskID != nil) != (mw.OccupantTaskID != nil) {
				t.Errorf("issue row occupant = %v, want %v", iss.MainWorktree.OccupantTaskID, mw.OccupantTaskID)
			}
			// The row's occupant state (134.16 decision 1) is the occupant's
			// own, and "" when nothing holds the worktree.
			wantState := TaskState("")
			if tc.occupies {
				wantState = tc.state
			}
			if iss.MainWorktree.OccupantState != wantState {
				t.Errorf("issue row occupant state = %q, want %q", iss.MainWorktree.OccupantState, wantState)
			}
			listed, err := s.ListIssues(t.Context(), IssueFilter{IDs: []int64{is.ID}})
			if err != nil || len(listed) != 1 {
				t.Fatalf("ListIssues = %v, %v", listed, err)
			}
			if lw := listed[0].MainWorktree; lw.Branch != iss.MainWorktree.Branch ||
				lw.OccupantState != iss.MainWorktree.OccupantState ||
				(lw.OccupantTaskID != nil) != (iss.MainWorktree.OccupantTaskID != nil) {
				t.Errorf("listed main worktree = %+v, detail %+v", listed[0].MainWorktree, iss.MainWorktree)
			}
		})
	}
}

// TestIssueOccupiedIsOnlyForAnotherMainTask: a candidate never occupies its
// own issue — an interrupted occupant must be re-admitted, not held behind
// itself — and only main-role candidates of the occupied issue are held.
func TestIssueOccupiedIsOnlyForAnotherMainTask(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	other := testIssue(t, s, p.ID, "another issue")
	started := time.Now().Add(-time.Minute)

	occ := newMainTask(p.ID, is.ID, "occupant")
	occ.StartedAt = &started // interrupted and re-queued: still queued
	mustCreate(t, s, occ)
	if candidateOf(t, s, occ.ID).IssueOccupied {
		t.Error("the occupant is held by itself")
	}
	if mw := mainWorktree(t, s, is.ID); mw.OccupantTaskID == nil || *mw.OccupantTaskID != occ.ID {
		t.Errorf("occupant = %v, want %d", mw.OccupantTaskID, occ.ID)
	}

	side := newTask(p.ID, "side", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictAgent
	mustCreate(t, s, side)
	roleless := newTask(p.ID, "roleless", TaskQueued)
	roleless.IssueID = &is.ID
	mustCreate(t, s, roleless)
	elsewhere := newMainTask(p.ID, other.ID, "elsewhere")
	mustCreate(t, s, elsewhere)

	for _, task := range []*Task{side, roleless, elsewhere} {
		if candidateOf(t, s, task.ID).IssueOccupied {
			t.Errorf("%s: IssueOccupied = true, want false", task.Title)
		}
	}
}
