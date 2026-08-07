package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/testrepo"
)

func TestSlug(t *testing.T) {
	tests := []struct{ title, want string }{
		{"Fix the login bug", "fix-the-login-bug"},
		{"  Leading & trailing!  ", "leading-trailing"},
		{"UPPER case 123", "upper-case-123"},
		{"---", ""},
		{"", ""},
		{"héllo wörld", "h-llo-w-rld"},
		{strings.Repeat("a", 50), strings.Repeat("a", 40)},
		{strings.Repeat("ab-", 14), strings.TrimRight(strings.Repeat("ab-", 14)[:40], "-")},
	}
	for _, tt := range tests {
		if got := Slug(tt.title); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	if got := BranchName(12, "Fix login"); got != "vincent/12-fix-login" {
		t.Errorf("BranchName = %q", got)
	}
	// Title sanitizing to empty drops the dash entirely (phase 1 decision).
	if got := BranchName(12, "!!!"); got != "vincent/12" {
		t.Errorf("BranchName empty-slug = %q", got)
	}
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(gitx.New(), t.TempDir())
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s error, got nil", reason)
	}
	if got := ReasonOf(err); got != reason {
		t.Fatalf("reason = %q (%v), want %q", got, err, reason)
	}
}

func TestCreateRemoveLifecycle(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()

	path, err := m.Create(ctx, repo, 7, "vincent/7-test", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if path != m.Path(7) {
		t.Errorf("path = %q, want %q", path, m.Path(7))
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Errorf("worktree not checked out: %v", err)
	}
	testrepo.Run(t, repo, "rev-parse", "--verify", "refs/heads/vincent/7-test")

	if err := m.Remove(ctx, repo, path, false); err != nil {
		t.Fatalf("Remove clean: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree dir still exists after remove")
	}
	// The branch survives removal (spec §10).
	testrepo.Run(t, repo, "rev-parse", "--verify", "refs/heads/vincent/7-test")
	// Idempotent: removing an already-removed worktree succeeds.
	if err := m.Remove(ctx, repo, path, false); err != nil {
		t.Fatalf("Remove again: %v", err)
	}
}

func TestCreateErrors(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()

	_, err := m.Create(ctx, filepath.Join(t.TempDir(), "gone"), 1, "vincent/1", "main")
	wantReason(t, err, ReasonProjectPathMissing)

	_, err = m.Create(ctx, repo, 2, "vincent/2", "nope")
	wantReason(t, err, ReasonBaseBranchMissing)

	testrepo.Run(t, repo, "branch", "vincent/3-taken")
	_, err = m.Create(ctx, repo, 3, "vincent/3-taken", "main")
	wantReason(t, err, ReasonBranchExists)

	// Pre-existing non-empty target dir: prune-then-fail decision.
	target := m.Path(4)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = m.Create(ctx, repo, 4, "vincent/4", "main")
	wantReason(t, err, ReasonWorktreePathOccupied)
}

func TestRemoveDirty(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()

	path, err := m.Create(ctx, repo, 5, "vincent/5-dirty", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Untracked files count as dirty (grill decision).
	testrepo.WriteFile(t, path, "untracked.txt", "work\n")
	if dirty, err := m.IsDirty(ctx, path); err != nil || !dirty {
		t.Fatalf("IsDirty = %v, %v; want true", dirty, err)
	}
	wantReason(t, m.Remove(ctx, repo, path, false), ReasonWorktreeDirty)
	if err := m.Remove(ctx, repo, path, true); err != nil {
		t.Fatalf("Remove force: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("worktree dir still exists after forced remove")
	}
}

func TestRemoveDirtyTracked(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()

	path, err := m.Create(ctx, repo, 6, "vincent/6", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	testrepo.WriteFile(t, path, "README.md", "modified\n")
	wantReason(t, m.Remove(ctx, repo, path, false), ReasonWorktreeDirty)
}

func TestRemoveProjectGone(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := newManager(t)
	ctx := context.Background()

	path, err := m.Create(ctx, repo, 8, "vincent/8", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("remove repo: %v", err)
	}
	wantReason(t, m.Remove(ctx, repo, path, false), ReasonProjectPathMissing)
	// force falls back to direct deletion inside the manager root.
	if err := m.Remove(ctx, repo, path, true); err != nil {
		t.Fatalf("Remove force with project gone: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("worktree dir still exists")
	}
	// Both gone: a no-op.
	if err := m.Remove(ctx, repo, path, true); err != nil {
		t.Fatalf("Remove with everything gone: %v", err)
	}
}

func TestRemoveDirectRefusesOutsideRoot(t *testing.T) {
	m := newManager(t)
	outside := t.TempDir()
	err := m.removeDirect(outside)
	wantReason(t, err, ReasonGitError)
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Error("directory outside the root was deleted")
	}
}

func TestReasonOf(t *testing.T) {
	if got := ReasonOf(errors.New("plain")); got != "" {
		t.Errorf("ReasonOf(plain) = %q, want empty", got)
	}
}
