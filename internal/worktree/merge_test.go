package worktree

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// divergedRepo is a repository on main with a branch `side`, each side having
// committed its own README.md (conflicting) or its own file (clean).
func divergedRepo(t *testing.T, conflicting bool) string {
	t.Helper()
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "checkout", "-q", "-b", "side")
	name := "side.txt"
	if conflicting {
		name = "README.md"
	}
	testrepo.WriteFile(t, repo, name, "side\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "side")
	testrepo.Run(t, repo, "checkout", "-q", "main")
	testrepo.WriteFile(t, repo, "README.md", "main\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "main")
	return repo
}

// The lane message is the contract `diff?by=lane` parses (§7.6), so the
// test pins its exact spelling.
func TestMergeLaneMessage(t *testing.T) {
	repo := divergedRepo(t, false)
	m := newManager(t)
	got, err := m.MergeLane(context.Background(), repo, "side", "api", 42)
	if err != nil || got != MergeOK {
		t.Fatalf("MergeLane = %v, %v; want MergeOK", got, err)
	}
	if subj := testrepo.Run(t, repo, "log", "-1", "--format=%s"); subj != "Merge lane 'api' of task 42" {
		t.Fatalf("merge subject = %q", subj)
	}
	if _, err := m.MergeLane(context.Background(), repo, "no-such-branch", "api", 42); err == nil ||
		!strings.Contains(err.Error(), `merge lane "api"`) {
		t.Fatalf("failure text = %v, want it to name the lane", err)
	}
}

func TestMergeBranch(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	t.Run("clean", func(t *testing.T) {
		repo := divergedRepo(t, false)
		got, err := m.MergeBranch(ctx, repo, "side", "Merge task 7 back")
		if err != nil || got != MergeOK {
			t.Fatalf("MergeBranch = %v, %v; want MergeOK", got, err)
		}
		if subj := testrepo.Run(t, repo, "log", "-1", "--format=%s"); subj != "Merge task 7 back" {
			t.Fatalf("merge subject = %q", subj)
		}
		// --no-ff: a merge commit, two parents.
		if parents := strings.Fields(testrepo.Run(t, repo, "log", "-1", "--format=%P")); len(parents) != 2 {
			t.Fatalf("parents = %v, want a merge commit", parents)
		}
	})

	t.Run("conflicted", func(t *testing.T) {
		repo := divergedRepo(t, true)
		head := testrepo.Run(t, repo, "rev-parse", "HEAD")
		got, err := m.MergeBranch(ctx, repo, "side", "Merge task 7 back")
		if err != nil || got != MergeConflicted {
			t.Fatalf("MergeBranch = %v, %v; want MergeConflicted", got, err)
		}
		if inMerge, err := m.InMerge(ctx, repo); err != nil || !inMerge {
			t.Fatalf("InMerge = %v, %v; want a merge left in progress", inMerge, err)
		}
		if now := testrepo.Run(t, repo, "rev-parse", "HEAD"); now != head {
			t.Fatalf("HEAD moved to %s on a conflict", now)
		}
		paths, err := m.ConflictedPaths(ctx, repo)
		if err != nil || !slices.Equal(paths, []string{"README.md"}) {
			t.Fatalf("ConflictedPaths = %v, %v", paths, err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		repo := divergedRepo(t, false)
		_, err := m.MergeBranch(ctx, repo, "no-such-branch", "Merge task 7 back")
		wantReason(t, err, ReasonGitError)
		if !strings.Contains(err.Error(), "merge no-such-branch") || strings.Contains(err.Error(), "lane") {
			t.Fatalf("failure text = %v, want a neutral one naming the branch", err)
		}
	})
}

func TestConflictMarkers(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"marked.txt":   "a\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> side\n",
		"crlf.txt":     "a\r\n<<<<<<< HEAD\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> side\r\n",
		"onlysep.txt":  "a\r\n=======\r\nb\r\n",
		"clean.txt":    "resolved\n",
		"inline.txt":   "a ======= b\n========\n  =======\n<<<<<<<no-space\n",
		"sub/deep.txt": ">>>>>>> side\n",
	}
	for name, content := range files {
		testrepo.WriteFile(t, dir, name, content)
	}
	paths := []string{"marked.txt", "crlf.txt", "onlysep.txt", "clean.txt", "inline.txt", "sub/deep.txt", "deleted.txt"}
	got, err := newManager(t).ConflictMarkers(context.Background(), dir, paths)
	if err != nil {
		t.Fatalf("ConflictMarkers: %v", err)
	}
	want := []string{"marked.txt", "crlf.txt", "onlysep.txt", "sub/deep.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("ConflictMarkers = %v, want %v", got, want)
	}
}

// A real conflicted merge: the markers git wrote are found, and staging the
// file — which empties the index's unmerged list — does not hide them.
func TestConflictMarkersSurviveStaging(t *testing.T) {
	ctx := context.Background()
	repo := divergedRepo(t, true)
	m := newManager(t)
	if got, err := m.MergeBranch(ctx, repo, "side", "Merge"); err != nil || got != MergeConflicted {
		t.Fatalf("MergeBranch = %v, %v", got, err)
	}
	paths, err := m.ConflictedPaths(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StageAll(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if conflicted, err := m.IndexConflicted(ctx, repo); err != nil || conflicted {
		t.Fatalf("IndexConflicted after staging = %v, %v; the premise of #756 is that it is false", conflicted, err)
	}
	if got, err := m.ConflictMarkers(ctx, repo, paths); err != nil || !slices.Equal(got, []string{"README.md"}) {
		t.Fatalf("ConflictMarkers = %v, %v; want README.md", got, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("both\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := m.ConflictMarkers(ctx, repo, paths); err != nil || len(got) != 0 {
		t.Fatalf("ConflictMarkers after resolving = %v, %v; want none", got, err)
	}
}
