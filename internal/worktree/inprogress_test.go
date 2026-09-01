package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// TestInProgressOp is the table decision 4 rests on, exercised **inside a
// linked worktree** rather than in the repository itself: that is where every
// vincent worktree lives, and the markers are in `.git/worktrees/{name}`, not
// in the repository's `.git`. A test that probed the repository would pass
// while the real path read the wrong directory.
//
// The markers are created rather than provoked with real conflicts on purpose:
// what is under test is the reading, and hand-built markers are the one form
// that behaves identically on all three platforms.
func TestInProgressOp(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()
	path, err := m.Create(ctx, repo, TaskOwner(3), "vincent/3-probe", "main", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gitDir, err := m.gitDir(ctx, path)
	if err != nil {
		t.Fatalf("gitDir: %v", err)
	}
	// The whole point of the fixture: a linked worktree's git dir is not the
	// repository's, so an implementation that read `{repo}/.git` would find
	// nothing here and report every worktree clean.
	if gitDir == filepath.Join(repo, ".git") {
		t.Fatalf("git dir resolved to the repository's own %s", gitDir)
	}

	if op, err := m.InProgressOp(ctx, path); err != nil || op != "" {
		t.Fatalf("a fresh worktree reports %q (%v), want none", op, err)
	}
	// Ordinary dirty state is not an operation: it is what a handoff keeps.
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if op, err := m.InProgressOp(ctx, path); err != nil || op != "" {
		t.Fatalf("a dirty worktree reports %q (%v), want none", op, err)
	}

	for _, tc := range []struct {
		marker string
		dir    bool
		want   string
	}{
		{"MERGE_HEAD", false, "merge"},
		{"rebase-merge", true, "rebase"},
		{"rebase-apply", true, "rebase"},
		{"CHERRY_PICK_HEAD", false, "cherry-pick"},
		{"REVERT_HEAD", false, "revert"},
		{"BISECT_LOG", false, "bisect"},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			p := filepath.Join(gitDir, tc.marker)
			if tc.dir {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte("marker\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(p) })
			op, err := m.InProgressOp(ctx, path)
			if err != nil {
				t.Fatalf("InProgressOp: %v", err)
			}
			if op != tc.want {
				t.Fatalf("InProgressOp with %s = %q, want %q", tc.marker, op, tc.want)
			}
		})
	}
}
