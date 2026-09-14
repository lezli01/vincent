package worktree

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The fast-forward of the local base that follows a successful base fetch
// (§10, issue #430). Every fixture starts from remoteAhead: `main` checked out
// clean in the project repository, one commit behind its upstream, which adds
// upstream.txt. What is asserted is the human's checkout — ref, HEAD, index and
// files — because that is the thing this step may move or must leave alone.

// checkoutState is a byte-exact picture of a checkout: the base ref, HEAD, the
// index, git's own view of the working tree, and every file outside `.git`.
// Two equal pictures mean nothing the human can see has moved.
func checkoutState(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, args := range [][]string{
		{"rev-parse", "refs/heads/main"},
		{"rev-parse", "HEAD"},
		{"ls-files", "--stage"},
		{"status", "--porcelain", "--untracked-files=all"},
	} {
		fmt.Fprintf(&b, "$ git %s\n%s\n", strings.Join(args, " "), testrepo.Run(t, dir, args...))
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(&b, "--- %s\n%s\n", filepath.ToSlash(rel), data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return b.String()
}

// wantSameDir compares directories by identity rather than by spelling: git
// reports a checkout by its real path, which on macOS is /private/var for
// t.TempDir's /var and on Windows may differ in slashes and short names.
func wantSameDir(t *testing.T, got, want string) {
	t.Helper()
	gi, err := os.Stat(got)
	if err != nil {
		t.Fatalf("worktree %q: %v", got, err)
	}
	wi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gi, wi) {
		t.Errorf("worktree = %q, want %q", got, want)
	}
}

func wantFastForward(t *testing.T, got FastForwardOutcome, result, reason string) {
	t.Helper()
	if got.Result != result || got.Reason != reason {
		t.Fatalf("fast-forward = %+v, want result %q reason %q", got, result, reason)
	}
	if (reason == SkipError) != (got.Error != "") {
		t.Errorf("fast-forward error = %q with reason %q", got.Error, reason)
	}
	if got.Skipped() != (result == FastForwardSkipped) {
		t.Errorf("Skipped() = %v for result %q", got.Skipped(), result)
	}
}

func createFetched(t *testing.T, m *Manager, repo string) Created {
	t.Helper()
	c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-fresh", "main", true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if c.Fetch.Result != FetchDone {
		t.Fatalf("fetch outcome = %+v, want %q", c.Fetch, FetchDone)
	}
	return c
}

// TestCreateFastForwardsCleanCheckout is the point of the change: the human's
// clean `main` ends up where the task starts, ref and files together.
func TestCreateFastForwardsCleanCheckout(t *testing.T) {
	repo, _, remoteTip := remoteAhead(t)
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardAdvanced, "")
	wantSameDir(t, c.FastForward.Worktree, repo)
	wantTip(t, repo, "refs/heads/main", remoteTip, "local base")
	wantTip(t, repo, "HEAD", remoteTip, "checkout HEAD")
	if got := testrepo.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Errorf("checkout disagrees with its HEAD after the fast-forward:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "upstream.txt")); err != nil {
		t.Errorf("the upstream's file is not in the checkout: %v", err)
	}
	if got := testrepo.Run(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("checked out branch = %q, want main", got)
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreateLeavesDirtyCheckoutAlone: the user's uncommitted work is not
// vincent's to carry across a tree switch. Tracked and untracked-only both
// count, on the T1.5/T1.6 rule. The task still starts at the fetched commit.
func TestCreateLeavesDirtyCheckoutAlone(t *testing.T) {
	for name, dirty := range map[string]func(t *testing.T, repo string){
		"tracked edit": func(t *testing.T, repo string) {
			testrepo.WriteFile(t, repo, "README.md", "edited, not committed\n")
		},
		"untracked only": func(t *testing.T, repo string) {
			testrepo.WriteFile(t, repo, "wip.txt", "half-finished\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, _, remoteTip := remoteAhead(t)
			dirty(t, repo)
			before := checkoutState(t, repo)
			m := newManager(t)
			c := createFetched(t, m, repo)

			wantFastForward(t, c.FastForward, FastForwardSkipped, SkipCheckoutDirty)
			if c.FastForward.Worktree != "" {
				t.Errorf("worktree = %q for a skip", c.FastForward.Worktree)
			}
			if after := checkoutState(t, repo); after != before {
				t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
			}
			wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
		})
	}
}

// TestCreateLeavesBusyCheckoutAlone: a merge the human stopped before
// committing is theirs to finish, and switching the tree under it would lose
// the merge's staged result.
func TestCreateLeavesBusyCheckoutAlone(t *testing.T) {
	repo, _, remoteTip := remoteAhead(t)
	testrepo.Run(t, repo, "checkout", "-q", "-b", "side")
	testrepo.WriteFile(t, repo, "side.txt", "side work\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "side work")
	testrepo.Run(t, repo, "checkout", "-q", "main")
	testrepo.Run(t, repo, "merge", "-q", "--no-ff", "--no-commit", "side")
	before := checkoutState(t, repo)
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardSkipped, SkipCheckoutBusy)
	if after := checkoutState(t, repo); after != before {
		t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
	}
	if err := gitErr(repo, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
		t.Errorf("the stopped merge is gone: %v", err)
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreateFastForwardsBaseNotCheckedOut: with no checkout holding the base,
// there is no working tree to keep in step, so the ref alone moves.
func TestCreateFastForwardsBaseNotCheckedOut(t *testing.T) {
	for name, leave := range map[string][]string{
		"detached":       {"checkout", "-q", "--detach"},
		"another branch": {"checkout", "-q", "-b", "elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			repo, localTip, remoteTip := remoteAhead(t)
			testrepo.Run(t, repo, leave...)
			beforeHead := testrepo.Run(t, repo, "rev-parse", "HEAD")
			m := newManager(t)
			c := createFetched(t, m, repo)

			wantFastForward(t, c.FastForward, FastForwardAdvanced, "")
			if c.FastForward.Worktree != "" {
				t.Errorf("worktree = %q; no working tree moved", c.FastForward.Worktree)
			}
			wantTip(t, repo, "refs/heads/main", remoteTip, "local base")
			wantTip(t, repo, "HEAD", beforeHead, "checkout HEAD")
			if beforeHead != localTip {
				t.Fatalf("fixture is wrong: HEAD %s is not the old base %s", beforeHead, localTip)
			}
			if got := testrepo.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
				t.Errorf("working tree changed:\n%s", got)
			}
			wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
		})
	}
}

// TestCreateSkipsDivergedBase: a local commit nobody pushed, and an upstream
// commit the local branch lacks. Only the human can reconcile those.
func TestCreateSkipsDivergedBase(t *testing.T) {
	repo, _, remoteTip := remoteAhead(t)
	testrepo.Run(t, repo, "commit", "-q", "--allow-empty", "-m", "mine, never pushed")
	before := checkoutState(t, repo)
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardSkipped, SkipDiverged)
	if after := checkoutState(t, repo); after != before {
		t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreateSkipsBaseAheadOfUpstream: the local base already contains the
// fetched commit; moving it "forward" would drop the human's unpushed one.
func TestCreateSkipsBaseAheadOfUpstream(t *testing.T) {
	repo, _, remoteTip := remoteAhead(t)
	testrepo.Run(t, repo, "reset", "-q", "--hard", remoteTip)
	testrepo.Run(t, repo, "commit", "-q", "--allow-empty", "-m", "ahead, not pushed yet")
	before := checkoutState(t, repo)
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardSkipped, SkipLocalAhead)
	if after := checkoutState(t, repo); after != before {
		t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

func TestCreateReportsBaseUpToDate(t *testing.T) {
	repo, _, remoteTip := remoteAhead(t)
	testrepo.Run(t, repo, "reset", "-q", "--hard", remoteTip)
	before := checkoutState(t, repo)
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardUpToDate, "")
	if after := checkoutState(t, repo); after != before {
		t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
	}
}

// TestCreateFastForwardKeepsTaskBranchUntracked: moving the base must not
// reopen task 056's hazard. With git at its most eager to record an upstream,
// the task branch still carries none after an advanced fast-forward.
func TestCreateFastForwardKeepsTaskBranchUntracked(t *testing.T) {
	repo, _, _ := remoteAhead(t)
	testrepo.Run(t, repo, "config", "branch.autoSetupMerge", "always")
	m := newManager(t)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardAdvanced, "")
	const branch = "vincent/1-fresh"
	for _, key := range []string{"branch." + branch + ".remote", "branch." + branch + ".merge"} {
		if v, ok := m.gitConfig(t.Context(), repo, key); ok {
			t.Errorf("%s = %q; a task branch must carry no upstream", key, v)
		}
	}
}

// TestCreateDoesNotFastForwardWithoutAFetchedCommit: every path on which
// fetchBase resolved nothing leaves the base where it was, and says it never
// tried rather than that it skipped.
func TestCreateDoesNotFastForwardWithoutAFetchedCommit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fetch bool
		setup func(t *testing.T, repo string)
		want  string
	}{
		{"disabled", false, func(*testing.T, string) {}, FetchDisabled},
		{"no upstream", true, func(t *testing.T, repo string) {
			testrepo.Run(t, repo, "branch", "--unset-upstream", "main")
		}, FetchNoUpstream},
		{"fetch failed", true, func(t *testing.T, repo string) {
			testrepo.Run(t, repo, "remote", "set-url", "origin",
				testrepo.Run(t, repo, "rev-parse", "--show-toplevel")+"-gone.git")
		}, FetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, localTip, _ := remoteAhead(t)
			tc.setup(t, repo)
			before := checkoutState(t, repo)
			m := newManager(t)
			c, err := m.CreateAndClaim(t.Context(), repo, TaskOwner(1), "vincent/1-stale", "main", tc.fetch, nil)
			if err != nil {
				t.Fatalf("CreateAndClaim: %v", err)
			}
			if c.Fetch.Result != tc.want {
				t.Errorf("fetch outcome = %+v, want %q", c.Fetch, tc.want)
			}
			if c.FastForward != (FastForwardOutcome{Result: FastForwardNotAttempted}) {
				t.Errorf("fast-forward = %+v, want only %q", c.FastForward, FastForwardNotAttempted)
			}
			if after := checkoutState(t, repo); after != before {
				t.Errorf("checkout changed:\n%s\nwant:\n%s", after, before)
			}
			wantTip(t, c.Path, "HEAD", localTip, "task branch tip")
		})
	}
}

// raceBase arms the manager's seam to move `main` just before the
// compare-and-swap, the way a human's commit landing in that window would. The
// racing commit keeps the old base's tree, so a checkout rolled back to the old
// tree agrees with it exactly — which is what makes "HEAD and the working tree
// never disagree" checkable.
func raceBase(t *testing.T, m *Manager, repo, localTip string) *string {
	t.Helper()
	moved := new(string)
	m.beforeBaseRefUpdate = func() {
		*moved = testrepo.Run(t, repo, "commit-tree", localTip+"^{tree}", "-p", localTip, "-m", "landed mid-update")
		testrepo.Run(t, repo, "update-ref", "refs/heads/main", *moved)
	}
	return moved
}

// TestCreateFastForwardLosesRaceCleanly: the ref moved between the read and
// the update. The compare-and-swap refuses, the checkout is switched back, and
// the task is created regardless.
func TestCreateFastForwardLosesRaceCleanly(t *testing.T) {
	repo, localTip, remoteTip := remoteAhead(t)
	m := newManager(t)
	moved := raceBase(t, m, repo, localTip)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardSkipped, SkipError)
	if c.FastForward.Worktree != "" {
		t.Errorf("worktree = %q for a skip", c.FastForward.Worktree)
	}
	if *moved == "" {
		t.Fatal("the seam never ran: the checked-out path did not reach update-ref")
	}
	wantTip(t, repo, "refs/heads/main", *moved, "local base")
	wantTip(t, repo, "HEAD", *moved, "checkout HEAD")
	if got := testrepo.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Errorf("checkout HEAD and working tree disagree after the lost race:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "upstream.txt")); err == nil {
		t.Error("the upstream's file survived the rollback")
	}
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreateFastForwardLosesRaceNotCheckedOut: the same refusal with no
// working tree to roll back.
func TestCreateFastForwardLosesRaceNotCheckedOut(t *testing.T) {
	repo, localTip, remoteTip := remoteAhead(t)
	testrepo.Run(t, repo, "checkout", "-q", "--detach")
	m := newManager(t)
	moved := raceBase(t, m, repo, localTip)
	c := createFetched(t, m, repo)

	wantFastForward(t, c.FastForward, FastForwardSkipped, SkipError)
	wantTip(t, repo, "refs/heads/main", *moved, "local base")
	wantTip(t, c.Path, "HEAD", remoteTip, "task branch tip")
}

// TestCreatePullRecordsNoBaseFastForward: the pull-request mode fetches a
// head, never the base, and must not claim anything happened to it.
func TestCreatePullRecordsNoBaseFastForward(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "feature/pr", "the contributor's commit")
	c, err := m.CreatePullAndClaim(t.Context(), local, TaskOwner(7), "master",
		PullSpec{Number: 1, Branch: "feature/pr", Ref: "refs/heads/feature/pr"}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.FastForward != (FastForwardOutcome{}) {
		t.Errorf("fast-forward = %+v, want the zero value", c.FastForward)
	}
}
