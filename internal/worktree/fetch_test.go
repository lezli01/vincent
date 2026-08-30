package worktree

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/testrepo"
)

// remoteAhead builds the situation task 056 exists for: a local base branch
// tracking a remote that has moved on since the human last pulled. The
// upstream commit is pushed from a throwaway branch in the same repository, so
// `refs/heads/main` stays exactly where it was — which is the whole point,
// and what "the local base is stale" means.
func remoteAhead(t *testing.T) (repo, localTip, remoteTip string) {
	t.Helper()
	repo = testrepo.Init(t, "main")
	testrepo.Run(t, repo, "remote", "add", "origin", testrepo.InitBare(t))
	testrepo.Run(t, repo, "push", "-q", "-u", "origin", "main")
	localTip = testrepo.Run(t, repo, "rev-parse", "main")

	testrepo.Run(t, repo, "checkout", "-q", "-b", "upstream-work")
	testrepo.WriteFile(t, repo, "upstream.txt", "landed while the daemon was up\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "a pull request somebody else merged")
	remoteTip = testrepo.Run(t, repo, "rev-parse", "HEAD")
	testrepo.Run(t, repo, "push", "-q", "origin", "upstream-work:refs/heads/main")
	testrepo.Run(t, repo, "checkout", "-q", "main")
	testrepo.Run(t, repo, "branch", "-q", "-D", "upstream-work")
	return repo, localTip, remoteTip
}

func wantTip(t *testing.T, dir, rev, want, what string) {
	t.Helper()
	if got := testrepo.Run(t, dir, "rev-parse", rev); got != want {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

// TestCreateFetchesTheBaseBranch is the acceptance criterion: with the local
// base behind its remote, the task branch starts at the *remote* tip.
func TestCreateFetchesTheBaseBranch(t *testing.T) {
	repo, localTip, remoteTip := remoteAhead(t)
	if localTip == remoteTip {
		t.Fatal("fixture is wrong: the remote is not ahead")
	}
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-fresh", "main", true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if c.Fetch.Result != FetchDone {
		t.Fatalf("fetch outcome = %+v, want %q", c.Fetch, FetchDone)
	}
	if c.Fetch.Remote != "origin" || c.Fetch.Ref != "refs/heads/main" {
		t.Errorf("fetch named %s %s, want origin refs/heads/main", c.Fetch.Remote, c.Fetch.Ref)
	}
	if c.BaseSHA != remoteTip {
		t.Errorf("recorded base_sha = %s, want the remote tip %s", c.BaseSHA, remoteTip)
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreateFetchLeavesTheLocalBaseAlone: the user's checkout is not vincent's
// to move. Same SHA, same working-tree state — including the uncommitted file
// that makes a fast-forward unsafe in the first place — still checked out.
func TestCreateFetchLeavesTheLocalBaseAlone(t *testing.T) {
	repo, localTip, _ := remoteAhead(t)
	testrepo.WriteFile(t, repo, "wip.txt", "half-finished\n")
	before := testrepo.Run(t, repo, "status", "--porcelain")
	if before == "" {
		t.Fatal("fixture is wrong: the working tree should be dirty")
	}
	m := newManager(t)
	if _, err := m.Create(t.Context(), repo, TaskOwner(1), "vincent/1-fresh", "main", true); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantTip(t, repo, "refs/heads/main", localTip, "local base after the fetch")
	if got := testrepo.Run(t, repo, "status", "--porcelain"); got != before {
		t.Errorf("working tree changed:\n%s\nwant:\n%s", got, before)
	}
	if got := testrepo.Run(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("checked out branch = %q, want main", got)
	}
}

// TestCreateSetsNoUpstreamOnTheTaskBranch is the guard between this feature
// and a deleted `master` on somebody's forge. Under `branch.autoSetupMerge`
// git copies the start point's upstream onto the new branch; archive's remote
// leg then reads `branch.{task}.remote` + `.merge` and runs
// `git push --delete origin refs/heads/main` (§10, task 008). It must not be
// possible, with or without a fetch.
func TestCreateSetsNoUpstreamOnTheTaskBranch(t *testing.T) {
	for _, fetch := range []bool{true, false} {
		name := "fetch"
		if !fetch {
			name = "no-fetch"
		}
		t.Run(name, func(t *testing.T) {
			repo, _, _ := remoteAhead(t)
			// The setting that makes git most eager to record one.
			testrepo.Run(t, repo, "config", "branch.autoSetupMerge", "always")
			m := newManager(t)
			const branch = "vincent/1-fresh"
			if _, err := m.Create(t.Context(), repo, TaskOwner(1), branch, "main", fetch); err != nil {
				t.Fatalf("Create: %v", err)
			}
			for _, key := range []string{"branch." + branch + ".remote", "branch." + branch + ".merge"} {
				if v, ok := m.gitConfig(t.Context(), repo, key); ok {
					t.Errorf("%s = %q; a task branch must carry no upstream", key, v)
				}
			}
			up, ok := m.branchUpstream(t.Context(), repo, branch)
			if ok {
				t.Errorf("branchUpstream resolved %+v; archive would push --delete it", up)
			}
		})
	}
}

// TestCreateFetchWithNoRemote: a repository with no remote at all is a
// supported local-first configuration. It creates the task exactly as before,
// with no block.
func TestCreateFetchWithNoRemote(t *testing.T) {
	repo := testrepo.Init(t, "main")
	localTip := testrepo.Run(t, repo, "rev-parse", "main")
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-local", "main", true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if c.Fetch.Result != FetchNoUpstream || c.Fetch.Degraded() {
		t.Errorf("fetch outcome = %+v, want %q and not degraded", c.Fetch, FetchNoUpstream)
	}
	if c.BaseSHA != "" {
		t.Errorf("base_sha = %q, want none recorded", c.BaseSHA)
	}
	wantTip(t, c.Path, "HEAD", localTip, "task branch tip")
}

// TestCreateFetchWithBaseThatHasNoUpstream is the same answer for a different
// reason, and the one a fan_out lane takes: the repository has a remote, but
// the base branch is not on it. A vincent/{id}-{slug} branch — which is what
// §7.6 makes a child's base — is exactly this shape.
func TestCreateFetchWithBaseThatHasNoUpstream(t *testing.T) {
	repo, _, _ := remoteAhead(t)
	m := newManager(t)
	// The parent lane's branch, cut from a fetched main.
	parent, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-parent", "main", true, nil)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	testrepo.WriteFile(t, parent.Path, "parent.txt", "parent work\n")
	testrepo.Run(t, parent.Path, "add", ".")
	testrepo.Run(t, parent.Path, "commit", "-q", "-m", "parent work")
	parentTip := testrepo.Run(t, parent.Path, "rev-parse", "HEAD")

	child, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(2), "vincent/2-lane", "vincent/1-parent", true, nil)
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	if child.Fetch.Result != FetchNoUpstream {
		t.Errorf("lane fetch outcome = %+v, want %q", child.Fetch, FetchNoUpstream)
	}
	wantTip(t, child.Path, "HEAD", parentTip, "lane tip")
}

// TestCreateFetchFailureFallsBack: an unreachable remote is a warning, never a
// block, and it cannot park an admission behind gitx.RemoteTimeout when git
// itself gives up sooner.
func TestCreateFetchFailureFallsBack(t *testing.T) {
	repo, localTip, _ := remoteAhead(t)
	testrepo.Run(t, repo, "remote", "set-url", "origin",
		testrepo.Run(t, repo, "rev-parse", "--show-toplevel")+"-gone.git")
	m := newManager(t)
	start := time.Now()
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-fresh", "main", true, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("an unreachable remote must not fail the creation: %v", err)
	}
	if c.Fetch.Result != FetchFailed || !c.Fetch.Degraded() {
		t.Fatalf("fetch outcome = %+v, want %q", c.Fetch, FetchFailed)
	}
	if c.Fetch.Error == "" {
		t.Error("a failed fetch reports no error to log")
	}
	if c.BaseSHA != "" {
		t.Errorf("base_sha = %q; nothing was resolved to record", c.BaseSHA)
	}
	wantTip(t, c.Path, "HEAD", localTip, "task branch tip")
	if elapsed >= gitx.RemoteTimeout {
		t.Errorf("creation took %s, past RemoteTimeout %s", elapsed, gitx.RemoteTimeout)
	}
}

// TestCreateFetchDisabled: `fetch_base_branch: false` is the pre-056 behaviour
// exactly — the local ref, and no base SHA recorded, so both consumers keep
// reading `base_branch` as the fork point.
func TestCreateFetchDisabled(t *testing.T) {
	repo, localTip, remoteTip := remoteAhead(t)
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-stale", "main", false, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if c.Fetch.Result != FetchDisabled {
		t.Errorf("fetch outcome = %+v, want %q", c.Fetch, FetchDisabled)
	}
	if c.BaseSHA != "" {
		t.Errorf("base_sha = %q, want none recorded", c.BaseSHA)
	}
	wantTip(t, c.Path, "HEAD", localTip, "task branch tip")
	if testrepo.Run(t, c.Path, "rev-parse", "HEAD") == remoteTip {
		t.Error("the fetch ran with fetch_base_branch off")
	}
}

// TestDeleteEmptyBranchUsesTheRecordedBaseSHA is task 008's regression test.
// A task that wrote nothing but started at the fetched upstream tip is ahead
// of the local base branch, so reading `base_branch` as the fork point answers
// has_commits and delete_empty_branch_on_archive silently stops firing. Both
// halves are asserted: with the SHA it still deletes, and without one it does
// not — which is why the column exists.
func TestDeleteEmptyBranchUsesTheRecordedBaseSHA(t *testing.T) {
	const branch = "vincent/1-nothing"
	repo, _, _ := remoteAhead(t)
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), branch, "main", true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if c.BaseSHA == "" {
		t.Fatal("fixture is wrong: no base SHA was recorded")
	}
	if err := m.Remove(t.Context(), repo, c.Path, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// The branch name alone, which is what a pre-056 row carries.
	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", branch, false)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchHasCommits {
		t.Fatalf("without the SHA result = %q, want %q — the premise of the column",
			out.Result, BranchHasCommits)
	}
	out, err = m.DeleteEmptyBranch(t.Context(), repo, "main", c.BaseSHA, branch, false)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch with the recorded base: %v", err)
	}
	if out.Result != BranchDeleted {
		t.Fatalf("result = %q, want %q", out.Result, BranchDeleted)
	}
}

// TestDeleteEmptyBranchFallsBackToTheBranchName: a row with no base SHA — every
// task that predates the migration, and every task created with the fetch off
// — is judged exactly as it was before.
func TestDeleteEmptyBranchFallsBackToTheBranchName(t *testing.T) {
	const branch = "vincent/1-nothing"
	repo, _, _ := remoteAhead(t)
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), branch, "main", false, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if err := m.Remove(t.Context(), repo, c.Path, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", branch, false)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchDeleted {
		t.Fatalf("result = %q, want %q", out.Result, BranchDeleted)
	}
}

// TestDeleteEmptyBranchRefusesACheckedOutBranch pins what the `-D` above does
// not give up: git refuses to delete a branch checked out in a worktree with
// either flag, so the working-tree corruption case is still covered.
func TestDeleteEmptyBranchRefusesACheckedOutBranch(t *testing.T) {
	const branch = "vincent/1-live"
	repo, _, _ := remoteAhead(t)
	m := newManager(t)
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), branch, "main", true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", c.BaseSHA, branch, false)
	if err == nil {
		t.Fatal("deleted a branch that is checked out in a live worktree")
	}
	if out.Result != BranchDeleteFailed {
		t.Errorf("result = %q, want %q", out.Result, BranchDeleteFailed)
	}
	if !m.localBranchExists(t.Context(), repo, branch) {
		t.Error("the branch is gone")
	}
}
