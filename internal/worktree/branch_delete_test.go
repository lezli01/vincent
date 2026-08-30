package worktree

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// branchExists asks the repository directly rather than through the Manager,
// so a test can never pass because the code under test and the assertion share
// a bug.
func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	out := testrepo.Run(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+branch)
	return out != ""
}

// commitInRepo advances whatever the main checkout has checked out.
func commitInRepo(t *testing.T, repo, file, content string) {
	t.Helper()
	testrepo.WriteFile(t, repo, file, content)
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "commit "+file)
}

// TestDeleteEmptyBranch walks the whole rule in one table: a branch is deleted
// only when its tip is an ancestor of its recorded base, and every other answer
// — including every answer git refuses to give — keeps it (§10, task 008).
func TestDeleteEmptyBranch(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the base and branch names to judge, having arranged
		// the repository however the case needs.
		setup      func(t *testing.T, m *Manager, repo string) (base, branch string)
		wantResult string
		wantErr    bool
		wantGone   bool
		// wantAlive are branches that must still exist afterwards, whatever
		// happened to the one under test.
		wantAlive []string
	}{
		{
			name: "no commits past base is deleted",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				if _, err := m.Create(t.Context(), repo, TaskOwner(1), "vincent/1-empty", "main", false); err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := m.Remove(t.Context(), repo, m.Path(TaskOwner(1)), false); err != nil {
					t.Fatalf("remove: %v", err)
				}
				return "main", "vincent/1-empty"
			},
			wantResult: BranchDeleted,
			wantGone:   true,
		},
		{
			name: "one commit past base survives",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				path, err := m.Create(t.Context(), repo, TaskOwner(2), "vincent/2-work", "main", false)
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				testrepo.WriteFile(t, path, "work.txt", "real work\n")
				testrepo.Run(t, path, "add", ".")
				testrepo.Run(t, path, "commit", "-q", "-m", "the work")
				if err := m.Remove(t.Context(), repo, path, false); err != nil {
					t.Fatalf("remove: %v", err)
				}
				return "main", "vincent/2-work"
			},
			wantResult: BranchHasCommits,
		},
		{
			name: "base moving forward afterwards still reads as empty",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				if _, err := m.Create(t.Context(), repo, TaskOwner(3), "vincent/3-empty", "main", false); err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := m.Remove(t.Context(), repo, m.Path(TaskOwner(3)), false); err != nil {
					t.Fatalf("remove: %v", err)
				}
				// main gains work the task never saw. The tip is still an
				// ancestor of it, which is the whole point of testing against
				// the base rather than against the fork point.
				commitInRepo(t, repo, "later.txt", "moved on\n")
				return "main", "vincent/3-empty"
			},
			wantResult: BranchDeleted,
			wantGone:   true,
		},
		{
			name: "base branch that no longer resolves cannot be judged",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				if _, err := m.Create(t.Context(), repo, TaskOwner(4), "vincent/4-empty", "main", false); err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := m.Remove(t.Context(), repo, m.Path(TaskOwner(4)), false); err != nil {
					t.Fatalf("remove: %v", err)
				}
				return "gone-forever", "vincent/4-empty"
			},
			wantResult: BranchUnknown,
			wantErr:    true,
		},
		{
			name: "a branch checked out in another worktree is refused",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				// The worktree is deliberately *not* removed: `git branch -d`
				// is the belt that covers this, and it must refuse.
				if _, err := m.Create(t.Context(), repo, TaskOwner(5), "vincent/5-live", "main", false); err != nil {
					t.Fatalf("create: %v", err)
				}
				return "main", "vincent/5-live"
			},
			wantResult: BranchDeleteFailed,
			wantErr:    true,
		},
		{
			// git stores refs as a path hierarchy, which is why creating
			// `feat/foo` beside `feat/foo/bar` is impossible in the first
			// place (§10, task 001). What *is* reachable is two branches
			// sharing a ref directory, and deleting one must not take the
			// directory — and its neighbour — with it.
			name: "a ref hierarchy neighbour is not collaterally removed",
			setup: func(t *testing.T, m *Manager, repo string) (string, string) {
				for id, name := range map[int64]string{6: "feat/foo/bar", 7: "feat/foo/baz"} {
					if _, err := m.Create(t.Context(), repo, TaskOwner(id), name, "main", false); err != nil {
						t.Fatalf("create %s: %v", name, err)
					}
					if err := m.Remove(t.Context(), repo, m.Path(TaskOwner(id)), false); err != nil {
						t.Fatalf("remove %s: %v", name, err)
					}
				}
				return "main", "feat/foo/bar"
			},
			wantResult: BranchDeleted,
			wantGone:   true,
			wantAlive:  []string{"feat/foo/baz"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testrepo.Init(t, "main")
			m := newManager(t)
			base, branch := tt.setup(t, m, repo)

			out, err := m.DeleteEmptyBranch(t.Context(), repo, base, "", branch, false, nil)
			if tt.wantErr && err == nil {
				t.Fatalf("DeleteEmptyBranch(%s..%s) returned no error", base, branch)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("DeleteEmptyBranch(%s..%s): %v", base, branch, err)
			}
			if out.Result != tt.wantResult {
				t.Errorf("result = %q, want %q (error %v)", out.Result, tt.wantResult, err)
			}
			if out.Branch != branch {
				t.Errorf("outcome names branch %q, want %q", out.Branch, branch)
			}
			if tt.wantErr && out.Error == "" {
				t.Error("an outcome that failed carries no error text")
			}
			if got := branchExists(t, repo, branch); got == tt.wantGone {
				t.Errorf("branch %s exists = %v, want %v", branch, got, !tt.wantGone)
			}
			for _, alive := range tt.wantAlive {
				if !branchExists(t, repo, alive) {
					t.Errorf("branch %s was removed alongside %s", alive, branch)
				}
			}
			if out.Remote != nil {
				t.Errorf("remote leg ran without being asked: %+v", out.Remote)
			}
		})
	}
}

// TestDeleteEmptyBranchKeepsTheCommitObject: a branch with work survives *and*
// its commit stays reachable. "The branch is still listed" would pass even if
// the objects had been pruned out from under it.
func TestDeleteEmptyBranchKeepsTheCommitObject(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	path, err := m.Create(t.Context(), repo, TaskOwner(1), "vincent/1-work", "main", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testrepo.WriteFile(t, path, "work.txt", "real work\n")
	testrepo.Run(t, path, "add", ".")
	testrepo.Run(t, path, "commit", "-q", "-m", "the work")
	sha := testrepo.Run(t, path, "rev-parse", "HEAD")
	if err := m.Remove(t.Context(), repo, path, false); err != nil {
		t.Fatalf("remove: %v", err)
	}

	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", "vincent/1-work", false, nil)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchHasCommits {
		t.Fatalf("result = %q, want %q", out.Result, BranchHasCommits)
	}
	if got := testrepo.Run(t, repo, "rev-parse", "refs/heads/vincent/1-work"); got != sha {
		t.Errorf("branch tip = %s, want the commit %s", got, sha)
	}
	if got := testrepo.Run(t, repo, "cat-file", "-t", sha); got != "commit" {
		t.Errorf("commit %s is no longer a reachable commit object (%s)", sha, got)
	}
}

// TestDeleteEmptyBranchMissingProjectPath: the repository is gone, so nothing
// can be judged and nothing is attempted.
func TestDeleteEmptyBranchMissingProjectPath(t *testing.T) {
	m := newManager(t)
	out, err := m.DeleteEmptyBranch(
		t.Context(), filepath.Join(t.TempDir(), "not-here"), "main", "", "vincent/1-x", false, nil)
	wantReason(t, err, ReasonProjectPathMissing)
	if out.Result != BranchUnknown {
		t.Errorf("result = %q, want %q", out.Result, BranchUnknown)
	}
}

// TestDeleteEmptyBranchNoBranch: a task with no branch name is not a check that
// fails, it is a check that never happens.
func TestDeleteEmptyBranchNoBranch(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", "", false, nil)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Checked() {
		t.Errorf("outcome = %+v, want the zero value", out)
	}
}

// pushedRepo is a repo whose task branch has been pushed to a bare remote with
// an upstream set, which is the only state in which the remote leg does
// anything. vincent never pushes; a user's workflow step does.
func pushedRepo(t *testing.T, m *Manager, branch string) (repo, remote string) {
	t.Helper()
	repo = testrepo.Init(t, "main")
	remote = testrepo.InitBare(t)
	testrepo.Run(t, repo, "remote", "add", "origin", remote)
	testrepo.Run(t, repo, "push", "-q", "-u", "origin", "main")
	path, err := m.Create(t.Context(), repo, TaskOwner(1), branch, "main", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testrepo.Run(t, path, "push", "-q", "-u", "origin", branch)
	if err := m.Remove(t.Context(), repo, path, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	return repo, remote
}

func TestDeleteEmptyBranchRemote(t *testing.T) {
	const branch = "vincent/1-pushed"
	m := newManager(t)
	repo, remote := pushedRepo(t, m, branch)
	if !branchExists(t, remote, branch) {
		t.Fatalf("fixture is wrong: %s never reached the remote", branch)
	}

	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", branch, true, nil)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchDeleted {
		t.Fatalf("result = %q, want %q", out.Result, BranchDeleted)
	}
	if out.Remote == nil || out.Remote.Result != RemoteBranchDeleted {
		t.Fatalf("remote outcome = %+v, want %q", out.Remote, RemoteBranchDeleted)
	}
	if out.Remote.Remote != "origin" || out.Remote.Ref != "refs/heads/"+branch {
		t.Errorf("remote outcome names %s %s, want origin refs/heads/%s",
			out.Remote.Remote, out.Remote.Ref, branch)
	}
	if branchExists(t, remote, branch) {
		t.Errorf("branch %s survived on the remote", branch)
	}
}

// TestDeleteEmptyBranchRemoteNoUpstream: with nothing recorded as pushed, no
// push is attempted at all — vincent does not guess a remote name.
func TestDeleteEmptyBranchRemoteNoUpstream(t *testing.T) {
	repo := testrepo.Init(t, "main")
	remote := testrepo.InitBare(t)
	testrepo.Run(t, repo, "remote", "add", "origin", remote)
	testrepo.Run(t, repo, "push", "-q", "origin", "main")
	m := newManager(t)
	if _, err := m.Create(t.Context(), repo, TaskOwner(1), "vincent/1-local", "main", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.Remove(t.Context(), repo, m.Path(TaskOwner(1)), false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// A branch with the same name on the remote, put there by something other
	// than this task. Nothing may touch it, because nothing recorded it as
	// this branch's upstream.
	testrepo.Run(t, remote, "branch", "vincent/1-local", "main")

	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", "vincent/1-local", true, nil)
	if err != nil {
		t.Fatalf("DeleteEmptyBranch: %v", err)
	}
	if out.Result != BranchDeleted {
		t.Fatalf("result = %q, want %q", out.Result, BranchDeleted)
	}
	if out.Remote == nil || out.Remote.Result != RemoteBranchNoUpstream {
		t.Fatalf("remote outcome = %+v, want %q", out.Remote, RemoteBranchNoUpstream)
	}
	if !branchExists(t, remote, "vincent/1-local") {
		t.Error("a remote branch vincent never pushed was deleted anyway")
	}
}

// TestDeleteEmptyBranchRemoteRejected: a push that cannot happen leaves the
// local branch deleted and reports the failure. The archive above it succeeds
// either way — that is asserted in internal/taskrun.
func TestDeleteEmptyBranchRemoteRejected(t *testing.T) {
	const branch = "vincent/1-pushed"
	m := newManager(t)
	repo, remote := pushedRepo(t, m, branch)
	// Point origin at nothing. Deleting the bare repo is the cheapest
	// unreachable remote there is, and needs no network.
	testrepo.Run(t, repo, "remote", "set-url", "origin", filepath.Join(remote, "..", "vanished.git"))

	out, err := m.DeleteEmptyBranch(t.Context(), repo, "main", "", branch, true, nil)
	if err == nil {
		t.Fatal("a failed push reported no error")
	}
	if out.Result != BranchDeleted {
		t.Errorf("result = %q, want the local branch still deleted (%q)", out.Result, BranchDeleted)
	}
	if branchExists(t, repo, branch) {
		t.Error("the local branch survived a remote failure")
	}
	if out.Remote == nil || out.Remote.Result != RemoteBranchFailed {
		t.Fatalf("remote outcome = %+v, want %q", out.Remote, RemoteBranchFailed)
	}
	if !strings.Contains(out.Remote.Error, "push") {
		t.Errorf("remote error %q does not name the failing command", out.Remote.Error)
	}
}
