package worktree

import (
	"context"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

func TestValidateBranchName(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	for _, name := range []string{
		"feat/ok", "vincent/12-fix-login", "a", "a/b/c", "release-2026.08",
		"feat/OPS-123_thing", // uppercase and underscore are legal; the slug rules are not git's rules
		// Both of these are surprising, and both are pinned deliberately: git
		// accepts them, and the task 001 decision makes git the authority rather
		// than a Go matcher expressing what someone believed git's rules to be.
		// `refs/heads/x` as a *branch* would create refs/heads/refs/heads/x, and
		// git's own docs say a refname "cannot be the single character @" — yet
		// `check-ref-format` accepts both. If a future git disagrees, this test
		// is where that shows up.
		"refs/heads/x",
		"@",
	} {
		if err := m.ValidateBranchName(ctx, name); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v, want nil", name, err)
		}
	}

	// The forms a Go regex would have to remember, and the reason this delegates.
	for _, name := range []string{
		"a..b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`,
		"a.lock", "a.", "a//b", "a@{b}", "HEAD", "-a", "a b", "a\tb",
		"/a", "a/", "", "a/./b",
	} {
		err := m.ValidateBranchName(ctx, name)
		if err == nil {
			t.Errorf("ValidateBranchName(%q) = nil, want a rejection", name)
			continue
		}
		wantReason(t, err, ReasonBranchNameInvalid)
	}
}

// The finding that widened this check: an exact-match probe reports the name as
// free in both directions of a directory/file conflict, and `git worktree add`
// then fails with a message that maps to no named reason.
func TestBranchConflictCatchesDirectoryFileConflicts(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	t.Run("a ref under the name blocks it", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "branch", "feat/foo/bar")

		// Precisely the trap: the exact-match check used before task 001 says free.
		if m.localBranchExists(ctx, repo, "feat/foo") {
			t.Fatal("localBranchExists found feat/foo; the premise of this test is gone")
		}
		got, err := m.BranchConflict(ctx, repo, "feat/foo")
		if err != nil {
			t.Fatalf("BranchConflict: %v", err)
		}
		if got != "feat/foo/bar" {
			t.Fatalf("BranchConflict = %q, want feat/foo/bar", got)
		}
	})

	t.Run("a prefix that is a ref blocks the name", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "branch", "feat/foo")

		got, err := m.BranchConflict(ctx, repo, "feat/foo/bar")
		if err != nil {
			t.Fatalf("BranchConflict: %v", err)
		}
		if got != "feat/foo" {
			t.Fatalf("BranchConflict = %q, want feat/foo", got)
		}
	})

	t.Run("exact match is still reported", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "branch", "feat/foo")

		got, err := m.BranchConflict(ctx, repo, "feat/foo")
		if err != nil {
			t.Fatalf("BranchConflict: %v", err)
		}
		if got != "feat/foo" {
			t.Fatalf("BranchConflict = %q, want feat/foo", got)
		}
	})

	t.Run("a free name is free", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "branch", "feat/foo/bar")

		for _, name := range []string{"feat/other", "feat/foo/baz", "unrelated", "feat/foobar"} {
			got, err := m.BranchConflict(ctx, repo, name)
			if err != nil {
				t.Fatalf("BranchConflict(%q): %v", name, err)
			}
			if got != "" {
				t.Errorf("BranchConflict(%q) = %q, want no conflict", name, got)
			}
		}
	})
}

// What the probe protects: every name it reports as conflicting is one git would
// have refused, and the free ones really do create. Proving both halves keeps the
// check from drifting into either false alarms or a false all-clear.
func TestBranchConflictAgreesWithGit(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "branch", "feat/foo/bar")
	testrepo.Run(t, repo, "branch", "solo")

	for _, name := range []string{"feat/foo", "feat/foo/bar", "solo", "solo/deeper", "brand/new"} {
		conflict, err := m.BranchConflict(ctx, repo, name)
		if err != nil {
			t.Fatalf("BranchConflict(%q): %v", name, err)
		}
		_, createErr := m.git.Run(ctx, repo, "branch", name)
		switch {
		case conflict != "" && createErr == nil:
			t.Errorf("BranchConflict(%q) reported %q but git created it happily", name, conflict)
		case conflict == "" && createErr != nil:
			t.Errorf("BranchConflict(%q) reported free but git refused: %v", name, createErr)
		}
	}
}
