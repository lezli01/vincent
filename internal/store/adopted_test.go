package store

import "testing"

// The working-directory claim of the adopt mode (§10, task 125 decision 2):
// at most one unarchived owner — task or chat — works in one directory, and
// the branch is what identifies it, because git cannot put one branch in two
// working trees.

func adoptedTask(t *testing.T, s *Store, projectID int64, branch string) *Task {
	t.Helper()
	task := newTask(projectID, "adopted", TaskQueued)
	task.BranchName = branch
	task.AdoptedBranch = true
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// Two tasks on one existing branch are both *created*: the second waits in the
// queue rather than being refused, which is the whole of decision 2. The
// ordinary mode's refusal is unchanged — see TestCreateTaskRefusesAClaimedBranch
// and the collision check in internal/api.
func TestAdoptedTasksMayShareABranchAtCreation(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	first := adoptedTask(t, s, p.ID, "shared/branch")
	second := adoptedTask(t, s, p.ID, "shared/branch")
	if first.ID == second.ID {
		t.Fatal("the two tasks are one row")
	}
	got, err := s.GetTask(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !got.AdoptedBranch {
		t.Error("adopted_branch did not round-trip")
	}
}

func TestWorkingDirClaimSpansTasksAndChats(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	// Nothing holds the branch yet.
	claim, err := s.WorkingDirClaim(ctx, p.ID, "shared/branch", 0)
	if err != nil {
		t.Fatalf("WorkingDirClaim: %v", err)
	}
	if claim != "" {
		t.Fatalf("claim = %q on a free branch, want none", claim)
	}

	// A task that has been admitted holds it: the claim is the directory, and
	// the directory is what worktree_path names.
	held := adoptedTask(t, s, p.ID, "shared/branch")
	if claim, err = s.WorkingDirClaim(ctx, p.ID, "shared/branch", 0); err != nil || claim != "" {
		t.Fatalf("claim = %q, %v before the worktree exists; want none", claim, err)
	}
	if err := s.ClaimTaskWorktree(ctx, held.ID, "/wt/1", "abc", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	if claim, err = s.WorkingDirClaim(ctx, p.ID, "shared/branch", 0); err != nil {
		t.Fatalf("WorkingDirClaim: %v", err)
	} else if claim == "" {
		t.Error("an admitted task does not claim its branch's directory")
	}
	// The task itself is not its own claimant.
	if claim, err = s.WorkingDirClaim(ctx, p.ID, "shared/branch", held.ID); err != nil || claim != "" {
		t.Errorf("claim = %q, %v against the holder itself; want none", claim, err)
	}

	// A chat on the same branch is a claimant too: before task 125 a chat's
	// branch was outside the claim set entirely.
	chat := testChat(t, s, p.ID)
	if err := s.SetChatBranch(ctx, chat.ID, "chat/branch"); err != nil {
		t.Fatalf("SetChatBranch: %v", err)
	}
	if _, err := s.ClaimChatWorktree(ctx, chat.ID, "/wt/chat-1", "abc", nil); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	if claim, err = s.WorkingDirClaim(ctx, p.ID, "chat/branch", 0); err != nil {
		t.Fatalf("WorkingDirClaim: %v", err)
	} else if claim == "" {
		t.Error("a chat does not claim its branch's directory")
	}
}

// The scheduler reads DirClaimants, so the query has to carry it. A queued
// adopted task whose branch is already held counts one claimant; the same task
// with the branch free counts none.
func TestListAdmissibleCountsDirClaimants(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	holder := adoptedTask(t, s, p.ID, "shared/branch")
	if err := s.ClaimTaskWorktree(ctx, holder.ID, "/wt/1", "abc", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	waiting := adoptedTask(t, s, p.ID, "shared/branch")
	free := adoptedTask(t, s, p.ID, "another/branch")

	cands, err := s.ListAdmissible(ctx)
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	got := map[int64]int{}
	for _, c := range cands {
		got[c.Task.ID] = c.DirClaimants
	}
	if got[waiting.ID] != 1 {
		t.Errorf("waiting task DirClaimants = %d, want 1", got[waiting.ID])
	}
	if got[free.ID] != 0 {
		t.Errorf("free task DirClaimants = %d, want 0", got[free.ID])
	}
}
