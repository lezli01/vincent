package taskrun

import (
	"os/exec"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestDeletingAnIssueMainSuccessorKeepsThePredecessorsCommits is #961: a
// successor's base_sha is the tip it received, so judging "empty" against it
// reads a branch carrying a predecessor's unmerged commit as empty. Archive
// judges an issue-main task against the issue's base branch instead
// (deleteArchivedBranch); permanent delete must give the same answer, and keep
// the branch.
func TestDeletingAnIssueMainSuccessorKeepsThePredecessorsCommits(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", committingSnapshot)
	second := h.settledMainTask(t, iss, "second", quickSnapshot)
	tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+first.BranchName)
	if second.BaseSHA != tip {
		t.Fatalf("successor base_sha = %q, want the tip %s it received", second.BaseSHA, tip)
	}
	if _, _, err := h.runner.Archive(t.Context(), first.ID, ArchiveOptions{}); err != nil {
		t.Fatalf("Archive(first): %v", err)
	}
	if _, branch, err := h.runner.Archive(t.Context(), second.ID, ArchiveOptions{}); err != nil {
		t.Fatalf("Archive(second): %v", err)
	} else if branch.Result != worktree.BranchHasCommits {
		t.Fatalf("archive outcome = %+v, want %q", branch, worktree.BranchHasCommits)
	}

	out, err := h.runner.Delete(t.Context(), second.ID, true)
	if err != nil {
		t.Fatalf("Delete(second): %v", err)
	}
	if out.Result != worktree.BranchHasCommits {
		t.Errorf("delete outcome = %+v, want %q: the predecessor's commit is past the issue's base",
			out, worktree.BranchHasCommits)
	}
	if err := exec.Command("git", "-C", h.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+first.BranchName).Run(); err != nil {
		t.Errorf("deleting the successor deleted the issue's branch carrying %s: %v", tip, err)
	}
}
