package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// fileRepo is testrepo.Init with one extra isolation step: testrepo does not
// isolate the developer's global git config, and `--exclude-standard` reads
// core.excludesFile from it. A machine-global rule — `*.txt`, say, or a
// personal `.gitignore_global` listing `tmp/` — would silently remove a row
// these tests expect, on that machine only. Pointing the key at an empty file
// in the repository's own config takes the global one out of the answer.
func fileRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	excludes := filepath.Join(t.TempDir(), "empty-excludes")
	if err := os.WriteFile(excludes, nil, 0o644); err != nil {
		t.Fatalf("write empty excludes file: %v", err)
	}
	testrepo.Run(t, dir, "config", "core.excludesFile", excludes)
	return dir
}

// listFiles runs the enumeration and fails the test on an error, which is what
// every case below but the missing-directory one wants.
func listFiles(t *testing.T, dir string) ([]string, int) {
	t.Helper()
	paths, dropped, err := newManager(t).ListFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListFiles(%s): %v", dir, err)
	}
	return paths, dropped
}

func wantListed(t *testing.T, paths []string, path string) {
	t.Helper()
	if !slices.Contains(paths, path) {
		t.Errorf("%q is not listed; got %q", path, paths)
	}
}

func wantNotListed(t *testing.T, paths []string, path string) {
	t.Helper()
	if slices.Contains(paths, path) {
		t.Errorf("%q is listed and should not be; got %q", path, paths)
	}
}

func TestListFilesTrackedUntrackedAndIgnored(t *testing.T) {
	dir := fileRepo(t)
	testrepo.WriteFile(t, dir, "tracked.txt", "tracked\n")
	testrepo.WriteFile(t, dir, ".gitignore", "ignored/\n")
	testrepo.Run(t, dir, "add", ".")
	testrepo.Run(t, dir, "commit", "-q", "-m", "tracked")
	testrepo.WriteFile(t, dir, "untracked.txt", "untracked\n")
	testrepo.WriteFile(t, dir, "ignored/secret.txt", "ignored\n")
	testrepo.WriteFile(t, dir, "nested/deep/file.txt", "nested\n")

	paths, dropped := listFiles(t, dir)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	wantListed(t, paths, "tracked.txt")
	wantListed(t, paths, "untracked.txt")
	wantListed(t, paths, ".gitignore")
	// A nested path is reported with forward slashes on every platform, which
	// is the form git prints and the form the agent CLIs take.
	wantListed(t, paths, "nested/deep/file.txt")
	wantNotListed(t, paths, "ignored/secret.txt")
	for _, p := range paths {
		if strings.Contains(p, `\`) {
			t.Errorf("path %q carries a backslash; git prints forward slashes", p)
		}
	}
}

// TestListFilesConflictedPathListedOnce is the regression test for deduping in
// Go instead of with `--deduplicate`: a worktree mid-merge prints a conflicted
// path once per index stage, and the picker must be offered one row.
func TestListFilesConflictedPathListedOnce(t *testing.T) {
	dir := fileRepo(t)
	testrepo.WriteFile(t, dir, "conflict.txt", "base\n")
	testrepo.Run(t, dir, "add", ".")
	testrepo.Run(t, dir, "commit", "-q", "-m", "base")
	testrepo.Run(t, dir, "checkout", "-q", "-b", "other")
	testrepo.WriteFile(t, dir, "conflict.txt", "other\n")
	testrepo.Run(t, dir, "commit", "-q", "-am", "other")
	testrepo.Run(t, dir, "checkout", "-q", "main")
	testrepo.WriteFile(t, dir, "conflict.txt", "mine\n")
	testrepo.Run(t, dir, "commit", "-q", "-am", "mine")

	// The merge is expected to fail, so it cannot go through testrepo.Run.
	merge := exec.Command("git", "merge", "other")
	merge.Dir = dir
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("git merge unexpectedly succeeded, wanted a conflict:\n%s", out)
	}
	stages := strings.Count(testrepo.Run(t, dir, "ls-files", "--stage", "conflict.txt"), "\n") + 1
	if stages < 2 {
		t.Fatalf("index holds %d stage(s) for conflict.txt, wanted a conflicted index", stages)
	}

	paths, _ := listFiles(t, dir)
	var n int
	for _, p := range paths {
		if p == "conflict.txt" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("conflict.txt listed %d times across %d stages, want 1; got %q", n, stages, paths)
	}
}

// TestListFilesPreservesSurroundingSpace is the reason RunRaw exists. Both
// names are legal on every platform vincent supports — Windows strips a
// *trailing* space from a filename but permits a leading one and an interior
// one — and both survive byte for byte.
func TestListFilesPreservesSurroundingSpace(t *testing.T) {
	dir := fileRepo(t)
	testrepo.WriteFile(t, dir, " leading.txt", "leading\n")
	testrepo.WriteFile(t, dir, "trailing .txt", "trailing\n")

	paths, dropped := listFiles(t, dir)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0; whitespace is not hostile", dropped)
	}
	wantListed(t, paths, " leading.txt")
	wantListed(t, paths, "trailing .txt")

	// The other half of the guard: the same call through the trimming runner
	// renames the file, which is why the enumeration does not use it.
	trimmed, err := newManager(t).git.Run(context.Background(), dir,
		"ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		t.Fatalf("Run(ls-files): %v", err)
	}
	if strings.HasPrefix(trimmed, " leading.txt") {
		t.Error("Run preserved the leading space; RunRaw would then be unnecessary")
	}
}

// TestListFilesSymlinks proves a symlink is one row and nothing more: not
// followed, not recursed into, and not read. Windows is skipped because
// os.Symlink there needs a privilege the CI runner may not hold.
func TestListFilesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink needs SeCreateSymbolicLinkPrivilege, which the runner may not hold")
	}
	dir := fileRepo(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("out\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	links := map[string]string{
		"outlink": outside,     // a directory outside the worktree
		"pw.txt":  absSystem(), // an absolute system path
		"loop":    "loop",      // self-referential
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s -> %s: %v", name, target, err)
		}
	}

	paths, dropped := listFiles(t, dir)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	for name := range links {
		wantListed(t, paths, name)
	}
	// Nothing was followed: the directory the symlink points at contributed
	// no rows of its own, under any name.
	for _, p := range paths {
		if strings.HasPrefix(p, "outlink/") || strings.Contains(p, "outside.txt") {
			t.Errorf("listing recursed through a symlink: %q", p)
		}
	}
}

// absSystem is an absolute path outside the repository that exists on the
// platform running the test. The symlink is never followed, so the target only
// has to be absolute — but pointing it at a real file is what makes "never
// followed" a claim worth testing.
func absSystem() string {
	if runtime.GOOS == "windows" {
		return `C:\Windows\win.ini`
	}
	return "/etc/hosts"
}

// TestListFilesDropsHostilePaths covers the rows that cannot cross a JSON wire
// honestly. Which of them can exist depends on the filesystem, so each fixture
// is attempted and the expected count follows what was actually created:
// Linux hosts both (count 2), macOS rejects an invalid-UTF-8 filename outright
// (APFS returns EILSEQ, count 1), and Windows hosts neither, so the test skips
// there rather than asserting nothing.
func TestListFilesDropsHostilePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot carry a control character or invalid UTF-8")
	}
	dir := fileRepo(t)
	testrepo.WriteFile(t, dir, "ordinary.txt", "fine\n")

	var hostile []string
	for _, name := range []string{"new\nline.txt", "bad\xff.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Logf("this filesystem refuses %q (%v); not asserting on it", name, err)
			continue
		}
		hostile = append(hostile, name)
	}
	if len(hostile) == 0 {
		t.Skip("this filesystem hosts neither a control character nor invalid UTF-8 in a filename")
	}

	paths, dropped := listFiles(t, dir)
	if dropped != len(hostile) {
		t.Errorf("dropped = %d, want %d for %q", dropped, len(hostile), hostile)
	}
	wantListed(t, paths, "ordinary.txt")
	for _, name := range hostile {
		wantNotListed(t, paths, name)
	}
}

// TestListFilesMissingDirectory is what the route serving this list keys its
// 400 on. The reason has to come from the os.Stat pre-check: git never runs,
// because gitx sets cmd.Dir and Go's fork/exec fails the chdir first, so there
// is no exit 128 and no stderr to classify.
func TestListFilesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	_, _, err := newManager(t).ListFiles(context.Background(), missing)
	wantReason(t, err, ReasonWorkspacePathMissing)
}
