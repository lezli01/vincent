package worktree

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The pull-request creation mode (§10, task 064 decision 2). Every case here
// is a git-side fact the API and the engine take on faith, so it is proved
// against real repositories rather than a fake: a bare "remote", a clone that
// stands in for the project, and a head branch that only exists on the remote.

// pullRepos returns a project repository and the bare remote its `origin`
// points at, plus the manager under test. Real repositories throughout: every
// claim below is a git-side fact the API and the engine take on faith.
func pullRepos(t *testing.T) (m *Manager, remote, local string) {
	t.Helper()
	local = testrepo.Init(t, "master")
	remote = testrepo.InitBare(t)
	testrepo.Run(t, local, "remote", "add", "origin", remote)
	testrepo.Run(t, local, "push", "-q", "origin", "master")
	return NewManager(gitx.New(), t.TempDir()), remote, local
}

// pushHead makes a commit on branch in the project repo, pushes it, and
// removes the local branch again — leaving the head existing only on the
// remote, which is the state a pull request from somebody else is in.
func pushHead(t *testing.T, remote, local, branch, message string) string {
	t.Helper()
	// From the remote's own tip when it already has this branch, so a second
	// call adds to the head rather than replacing it from master.
	start := "HEAD"
	if err := gitErr(local, "fetch", "-q", remote, "refs/heads/"+branch); err == nil {
		start = "FETCH_HEAD"
	}
	testrepo.Run(t, local, "checkout", "-q", "-B", branch, start)
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", message)
	sha := testrepo.Run(t, local, "rev-parse", "HEAD")
	testrepo.Run(t, local, "push", "-q", remote, branch+":refs/heads/"+branch)
	testrepo.Run(t, local, "checkout", "-q", "master")
	testrepo.Run(t, local, "branch", "-q", "-D", branch)
	return sha
}

// commitOn adds an unpushed commit to an existing local branch.
func commitOn(t *testing.T, local, branch, message string) string {
	t.Helper()
	testrepo.Run(t, local, "checkout", "-q", branch)
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", message)
	sha := testrepo.Run(t, local, "rev-parse", "HEAD")
	testrepo.Run(t, local, "checkout", "-q", "master")
	return sha
}

func TestCreatePullChecksOutTheHeadBranch(t *testing.T) {
	m, remote, local := pullRepos(t)
	head := pushHead(t, remote, local, "feature/from-a-pull-request", "the contributor's commit")

	spec := PullSpec{
		Number: 412,
		Branch: "feature/from-a-pull-request",
		Ref:    "refs/heads/feature/from-a-pull-request",
	}
	c, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(7), "master", spec, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Path != filepath.Join(m.Root(), "7") {
		t.Errorf("path = %q, want the task's worktree", c.Path)
	}
	// base_sha is the head commit as it stood at admission (decision 6), so
	// the diff tab answers "what did this task change".
	if c.BaseSHA != head {
		t.Errorf("base sha = %q, want the fetched head %q", c.BaseSHA, head)
	}
	if got := testrepo.Run(t, c.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/from-a-pull-request" {
		t.Errorf("worktree is on %q, want the pull request's head branch", got)
	}
	// The upstream is the deliverable: without it a workflow's push has
	// nowhere to go.
	if got := testrepo.Run(t, local, "config", "--get", "branch.feature/from-a-pull-request.remote"); got != "origin" {
		t.Errorf("upstream remote = %q, want origin", got)
	}
	if got := testrepo.Run(t, local, "config", "--get", "branch.feature/from-a-pull-request.merge"); got != "refs/heads/feature/from-a-pull-request" {
		t.Errorf("upstream ref = %q, want the head branch", got)
	}
}

func TestCreatePullFastForwardsAStaleLocalBranch(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "feature/stale", "first")
	// The human already has the branch, one commit behind.
	testrepo.Run(t, local, "fetch", "origin", "refs/heads/feature/stale")
	stale := testrepo.Run(t, local, "rev-parse", "FETCH_HEAD")
	testrepo.Run(t, local, "branch", "feature/stale", stale)
	head := pushHead(t, remote, local, "feature/stale", "second")

	if _, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(8), "master",
		PullSpec{Number: 9, Branch: "feature/stale", Ref: "refs/heads/feature/stale"}, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := testrepo.Run(t, local, "rev-parse", "refs/heads/feature/stale"); got != head {
		t.Errorf("branch is at %q, want it fast-forwarded to %q", got, head)
	}
}

func TestCreatePullBlocksOnADivergedLocalBranch(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "feature/diverged", "shared")
	// Their commit, pushed from a scratch branch so the local head is left
	// alone: this stands in for a contributor pushing to the pull request.
	testrepo.Run(t, local, "fetch", "-q", "origin", "refs/heads/feature/diverged")
	base := testrepo.Run(t, local, "rev-parse", "FETCH_HEAD")
	testrepo.Run(t, local, "checkout", "-q", "-B", "scratch", base)
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", "theirs")
	testrepo.Run(t, local, "push", "-q", "-f", remote, "scratch:refs/heads/feature/diverged")
	testrepo.Run(t, local, "checkout", "-q", "master")
	testrepo.Run(t, local, "branch", "-q", "-D", "scratch")
	// And the human's own copy, one unpushed commit off the shared base.
	testrepo.Run(t, local, "branch", "feature/diverged", base)
	mine := commitOn(t, local, "feature/diverged", "mine, never pushed")

	_, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(9), "master",
		PullSpec{Number: 9, Branch: "feature/diverged", Ref: "refs/heads/feature/diverged"}, nil)
	if ReasonOf(err) != ReasonPullBranchDiverged {
		t.Fatalf("reason = %q (%v), want %q", ReasonOf(err), err, ReasonPullBranchDiverged)
	}
	// And the commit nobody pushed is still there: the whole reason this
	// blocks rather than resetting.
	if got := testrepo.Run(t, local, "rev-parse", "refs/heads/feature/diverged"); got != mine {
		t.Errorf("branch moved to %q; the unpushed commit %q must survive", got, mine)
	}
}

func TestCreatePullBlocksWhenTheBranchIsCheckedOutElsewhere(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "feature/busy", "work")
	testrepo.Run(t, local, "fetch", "origin", "refs/heads/feature/busy")
	testrepo.Run(t, local, "checkout", "-B", "feature/busy", "FETCH_HEAD")

	_, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(10), "master",
		PullSpec{Number: 9, Branch: "feature/busy", Ref: "refs/heads/feature/busy"}, nil)
	if ReasonOf(err) != ReasonPullBranchCheckedOut {
		t.Fatalf("reason = %q (%v), want %q", ReasonOf(err), err, ReasonPullBranchCheckedOut)
	}
}

func TestCreatePullBlocksWhenTheFetchFails(t *testing.T) {
	m, _, local := pullRepos(t)
	_, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(11), "master",
		PullSpec{Number: 9, Branch: "nope", Ref: "refs/heads/nope"}, nil)
	if ReasonOf(err) != ReasonPullFetchFailed {
		t.Fatalf("reason = %q (%v), want %q", ReasonOf(err), err, ReasonPullFetchFailed)
	}
}

// A fork's head is read through `refs/pull/{n}/head` and its branch carries no
// upstream: the task runs, and nothing can push back (decision 5). The ref is
// GitHub's own, so it is simulated here by putting it on the bare remote.
func TestCreatePullForkGetsNoUpstream(t *testing.T) {
	m, remote, local := pullRepos(t)
	head := pushHead(t, remote, local, "contributor-typo-fix", "one character")
	testrepo.Run(t, remote, "update-ref", "refs/pull/355/head", head)
	testrepo.Run(t, remote, "branch", "-D", "contributor-typo-fix")

	spec := PullSpec{
		Number: 355,
		Branch: "contributor-typo-fix",
		Ref:    "refs/pull/355/head",
		Fork:   true,
	}
	if _, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(12), "master", spec, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	// `config --get` exits 1 on a missing key, which is the answer wanted
	// here — so it is run directly rather than through the fail-on-error
	// helper.
	if err := gitErr(local, "config", "--get", "branch.contributor-typo-fix.remote"); err == nil {
		t.Error("fork branch has an upstream remote; nothing can be pushed back to a fork")
	}
	// And no remote was added for the fork.
	remotes := testrepo.Run(t, local, "remote")
	for _, name := range strings.Fields(remotes) {
		if name != "origin" {
			t.Errorf("remote %q was added; §10 says nothing local is mutated", name)
		}
	}
}

// Decision 3: archive never touches a branch vincent did not cut — with the
// merged pull request's "no commits past its base" being exactly the case
// delete_empty_branch_on_archive fires on.
func TestDeleteEmptyBranchSkipsAPullRequestBranch(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "feature/theirs", "their work")
	testrepo.Run(t, local, "fetch", "origin", "refs/heads/feature/theirs")
	testrepo.Run(t, local, "branch", "feature/theirs", "FETCH_HEAD")
	testrepo.Run(t, local, "config", "branch.feature/theirs.remote", "origin")
	testrepo.Run(t, local, "config", "branch.feature/theirs.merge", "refs/heads/feature/theirs")

	notOurs := false
	out, err := m.DeleteEmptyBranch(t.Context(), local, "master", "", "feature/theirs", true, &notOurs)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if out.Result != BranchNotOurs {
		t.Errorf("result = %q, want %q", out.Result, BranchNotOurs)
	}
	if out.Remote != nil {
		t.Errorf("the remote leg ran (%+v); it must not touch a contributor's branch", out.Remote)
	}
	if !localBranch(t, local, "feature/theirs") {
		t.Error("the local branch was deleted; vincent only deletes branches it cut")
	}
	if !localBranch(t, remote, "feature/theirs") {
		t.Error("the remote branch was deleted; that is a contributor's head branch on the forge")
	}
}

func localBranch(t *testing.T, repo, name string) bool {
	t.Helper()
	return gitErr(repo, "rev-parse", "--verify", "refs/heads/"+name) == nil
}

// gitErr runs git for its exit code. Several checks here *want* a failure —
// a missing config key, an absent branch — which testrepo.Run cannot express.
func gitErr(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}
