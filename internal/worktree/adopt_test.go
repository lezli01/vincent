package worktree

import (
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The adopt creation mode (§10, task 125). Everything here is a git-side fact
// the API, the scheduler and the engine take on faith, so it is proved against
// real repositories rather than a fake — the shape pull_test.go set.

// adoptTrack makes branch in the project repo, pushes it to the remote and
// configures it to track that remote, leaving the local branch where the
// remote is. It is the ordinary state of a branch a colleague shares.
func adoptTrack(t *testing.T, remote, local, branch string) {
	t.Helper()
	testrepo.Run(t, local, "checkout", "-q", "-b", branch)
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", "shared work")
	testrepo.Run(t, local, "push", "-q", remote, branch+":refs/heads/"+branch)
	testrepo.Run(t, local, "config", "--local", "branch."+branch+".remote", remote)
	testrepo.Run(t, local, "config", "--local", "branch."+branch+".merge", "refs/heads/"+branch)
	testrepo.Run(t, local, "checkout", "-q", "master")
}

// advanceRemote adds a commit to the remote's copy of branch without the
// project repo learning about it, which is what "behind its upstream" means.
func advanceRemote(t *testing.T, remote, branch, message string) string {
	t.Helper()
	clone := t.TempDir()
	testrepo.Run(t, clone, "init", "-q", "-b", "tmp", ".")
	testrepo.Run(t, clone, "fetch", "-q", remote, "refs/heads/"+branch)
	testrepo.Run(t, clone, "checkout", "-q", "-B", branch, "FETCH_HEAD")
	testrepo.Run(t, clone, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "-q", "--allow-empty", "-m", message)
	sha := testrepo.Run(t, clone, "rev-parse", "HEAD")
	testrepo.Run(t, clone, "push", "-q", remote, branch+":refs/heads/"+branch)
	return sha
}

func branchSHA(t *testing.T, repo, branch string) string {
	t.Helper()
	return testrepo.Run(t, repo, "rev-parse", "--verify", "refs/heads/"+branch)
}

func TestAdoptFastForwardsABranchBehindItsUpstream(t *testing.T) {
	m, remote, local := pullRepos(t)
	adoptTrack(t, remote, local, "shared/feature")
	want := advanceRemote(t, remote, "shared/feature", "what the colleague pushed")

	c, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(3), "shared/feature", nil)
	if err != nil {
		t.Fatalf("CreateAdoptAndClaim: %v", err)
	}
	if got := branchSHA(t, local, "shared/feature"); got != want {
		t.Errorf("branch tip = %s, want the fetched %s", got, want)
	}
	if c.BaseSHA != want {
		t.Errorf("BaseSHA = %s, want the branch tip at admission %s", c.BaseSHA, want)
	}
	if c.Path != filepath.Join(m.Root(), "3") {
		t.Errorf("worktree path = %s, want the owner's directory", c.Path)
	}
	if head := testrepo.Run(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"); head != "shared/feature" {
		t.Errorf("worktree HEAD = %q, want the adopted branch", head)
	}
}

func TestAdoptLeavesABranchAheadOfItsUpstreamAlone(t *testing.T) {
	m, remote, local := pullRepos(t)
	adoptTrack(t, remote, local, "shared/ahead")
	want := commitOn(t, local, "shared/ahead", "unpushed local work")

	c, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(4), "shared/ahead", nil)
	if err != nil {
		t.Fatalf("CreateAdoptAndClaim: %v", err)
	}
	if got := branchSHA(t, local, "shared/ahead"); got != want {
		t.Errorf("branch tip = %s, want the unpushed local commit %s left where it was", got, want)
	}
	if c.BaseSHA != want {
		t.Errorf("BaseSHA = %s, want %s", c.BaseSHA, want)
	}
}

func TestAdoptRefusesADivergedBranchAndMovesNoRef(t *testing.T) {
	m, remote, local := pullRepos(t)
	adoptTrack(t, remote, local, "shared/diverged")
	before := commitOn(t, local, "shared/diverged", "the local commit nobody pushed")
	advanceRemote(t, remote, "shared/diverged", "the remote commit nobody has")

	_, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(5), "shared/diverged", nil)
	if got := ReasonOf(err); got != ReasonAdoptBranchDiverged {
		t.Fatalf("reason = %q, want %q (err = %v)", got, ReasonAdoptBranchDiverged, err)
	}
	// Asserted on the SHA, not on the message: the point of the refusal is
	// that no commit can be lost, and only the ref says whether one was.
	if got := branchSHA(t, local, "shared/diverged"); got != before {
		t.Errorf("branch tip = %s, want it untouched at %s", got, before)
	}
}

func TestAdoptOfALocalOnlyBranchFetchesNothingAndSetsNoUpstream(t *testing.T) {
	m, remote, local := pullRepos(t)
	// A branch that never left the machine, in a repository that does have a
	// remote: the fallback-to-origin this mode refuses to make (decision 5)
	// would have had somewhere to go.
	_ = remote
	testrepo.Run(t, local, "branch", "local-only", "master")
	want := branchSHA(t, local, "local-only")

	c, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(6), "local-only", nil)
	if err != nil {
		t.Fatalf("CreateAdoptAndClaim: %v", err)
	}
	if c.Fetch.Result != FetchNoUpstream {
		t.Errorf("fetch result = %q, want %q", c.Fetch.Result, FetchNoUpstream)
	}
	if c.BaseSHA != want {
		t.Errorf("BaseSHA = %s, want the local tip %s", c.BaseSHA, want)
	}
	for _, key := range []string{"branch.local-only.remote", "branch.local-only.merge"} {
		if err := gitErr(local, "config", "--get", key); err == nil {
			t.Errorf("%s was written for a branch the user kept local", key)
		}
	}
}

func TestAdoptRunsInTheMainCheckoutWhenTheBranchIsCheckedOutThere(t *testing.T) {
	m, _, local := pullRepos(t)
	testrepo.Run(t, local, "checkout", "-q", "-b", "in-my-checkout")
	want := branchSHA(t, local, "in-my-checkout")

	c, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(8), "in-my-checkout", nil)
	if err != nil {
		t.Fatalf("CreateAdoptAndClaim: %v", err)
	}
	if !SameDir(c.Path, local) {
		t.Fatalf("worktree path = %s, want the project path %s", c.Path, local)
	}
	if c.BaseSHA != want {
		t.Errorf("BaseSHA = %s, want the tip the human left it at %s", c.BaseSHA, want)
	}
	// Nothing was created under the worktree root: the main checkout is not a
	// worktree vincent made.
	if entries, err := filepath.Glob(filepath.Join(m.Root(), "*")); err == nil && len(entries) > 0 {
		t.Errorf("worktree root holds %v, want nothing", entries)
	}
}

func TestAdoptRefusesABranchHeldByAnotherWorktree(t *testing.T) {
	m, _, local := pullRepos(t)
	testrepo.Run(t, local, "branch", "taken", "master")
	if _, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(9), "taken", nil); err != nil {
		t.Fatalf("first adopt: %v", err)
	}
	_, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(10), "taken", nil)
	if got := ReasonOf(err); got != ReasonAdoptBranchCheckedOut {
		t.Fatalf("reason = %q, want %q (err = %v)", got, ReasonAdoptBranchCheckedOut, err)
	}
}

func TestAdoptRefusesAMissingBranch(t *testing.T) {
	m, _, local := pullRepos(t)
	_, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(11), "never-existed", nil)
	if got := ReasonOf(err); got != ReasonAdoptBranchMissing {
		t.Fatalf("reason = %q, want %q (err = %v)", got, ReasonAdoptBranchMissing, err)
	}
}

// Archive's side of the main-checkout mode: nothing is removed, and the call
// succeeds rather than failing safe on removeDirect's containment check.
func TestRemoveOfTheProjectPathDeletesNothing(t *testing.T) {
	m, _, local := pullRepos(t)
	testrepo.Run(t, local, "checkout", "-q", "-b", "mine")
	if err := m.Remove(t.Context(), local, local, false); err != nil {
		t.Fatalf("Remove(projectPath, projectPath): %v", err)
	}
	if err := gitErr(local, "rev-parse", "--verify", "refs/heads/mine"); err != nil {
		t.Errorf("the branch is gone: %v", err)
	}
	if _, err := filepath.Glob(filepath.Join(local, ".git")); err != nil {
		t.Errorf("the repository is gone: %v", err)
	}
}

// The archive rule of decision 6: vincent deletes only branches it cut, and an
// adopted branch is the case DeleteEmptyBranch would otherwise fire on — it
// carries no commits past the SHA the task started at.
func TestDeleteEmptyBranchReportsNotOursForAnAdoptedBranch(t *testing.T) {
	m, _, local := pullRepos(t)
	testrepo.Run(t, local, "branch", "somebody-elses", "master")
	sha := branchSHA(t, local, "somebody-elses")

	ours := false
	out, err := m.DeleteEmptyBranch(t.Context(), local, "master", sha, "somebody-elses", false, &ours)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchNotOurs {
		t.Fatalf("result = %q, want %q", out.Result, BranchNotOurs)
	}
	if err := gitErr(local, "rev-parse", "--verify", "refs/heads/somebody-elses"); err != nil {
		t.Errorf("the adopted branch was deleted: %v", err)
	}
}

func TestListBranchesReportsWhereEachIsCheckedOut(t *testing.T) {
	m, _, local := pullRepos(t)
	testrepo.Run(t, local, "branch", "free", "master")
	testrepo.Run(t, local, "branch", "in-a-worktree", "master")
	if _, err := m.CreateAdoptAndClaim(t.Context(), local, TaskOwner(12), "in-a-worktree", nil); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	branches, err := m.ListBranches(t.Context(), local)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	got := map[string]Branch{}
	for _, b := range branches {
		got[b.Name] = b
	}
	for _, name := range []string{"master", "free", "in-a-worktree"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("branch %q missing from %v", name, branches)
		}
	}
	if got["free"].CheckedOutIn != "" {
		t.Errorf("free is reported as checked out in %q", got["free"].CheckedOutIn)
	}
	if !SameDir(got["master"].CheckedOutIn, local) {
		t.Errorf("master checked out in %q, want the project path", got["master"].CheckedOutIn)
	}
	if !got["master"].Current {
		t.Error("master is not reported as the current branch")
	}
	// Compared with SameDir rather than by prefix: git prints the resolved
	// path (/private/var on macOS) and the manager's root is the temporary
	// directory's spelling of it.
	if w := got["in-a-worktree"].CheckedOutIn; w == "" || !SameDir(w, filepath.Join(m.Root(), "12")) {
		t.Errorf("in-a-worktree checked out in %q, want the owner's worktree under %s", w, m.Root())
	}
}

func TestSameDirComparesCleanedPaths(t *testing.T) {
	dir := t.TempDir()
	if !SameDir(dir, filepath.Join(dir, "sub", "..")) {
		t.Error("a path and its uncleaned spelling are not the same directory")
	}
	if SameDir(dir, filepath.Join(dir, "sub")) {
		t.Error("a directory and its child are the same directory")
	}
}
