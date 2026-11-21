package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// diffRepo is a testrepo with core.autocrlf off — so file bytes compare
// exactly on Windows too — and one commit holding a.txt, gone.txt, a
// twenty-line many.txt and a .gitignore that ignores *.log. It returns the
// repo and that commit.
func diffRepo(t *testing.T) (g *Git, dir, base string) {
	t.Helper()
	dir = testrepo.InitEmpty(t, "main")
	testrepo.Run(t, dir, "config", "core.autocrlf", "false")
	writeFile(t, dir, "a.txt", "one\ntwo\nthree\n")
	writeFile(t, dir, "gone.txt", "doomed\n")
	writeFile(t, dir, "many.txt", numbered(nil))
	writeFile(t, dir, ".gitignore", "*.log\n")
	testrepo.Run(t, dir, "add", "-A")
	testrepo.Run(t, dir, "commit", "-q", "-m", "init")
	return New(), dir, testrepo.Run(t, dir, "rev-parse", "HEAD")
}

// numbered is twenty lines "line 1" … "line 20", each ending in "\n", with
// the lines in edit replaced.
func numbered(edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		if s, ok := edit[i]; ok {
			b.WriteString(s)
		} else {
			fmt.Fprintf(&b, "line %d", i)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// repoState is everything a diff or a rejection must leave alone: HEAD, the
// index entry by entry, the staged diff, and the staging column of `status`.
// The staging column alone, because a rejection legitimately changes the
// worktree column; untracked rows ("??") have no staging column at all.
func repoState(t *testing.T, g *Git, dir string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"symbolic-ref", "HEAD"},
		{"ls-files", "-s", "-z"},
		{"diff", "--cached", "--binary"},
	} {
		out, err := g.RunRaw(ctx, dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		fmt.Fprintf(&b, "%v\n%s\n", args, out)
	}
	out, err := g.RunRaw(ctx, dir, "status", "--porcelain", "-z")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range strings.Split(string(out), "\x00") {
		if len(row) > 3 && row[0] != ' ' && row[0] != '?' {
			fmt.Fprintf(&b, "staged %c %s\n", row[0], row[3:])
		}
	}
	return b.String()
}

// snapDiff snapshots dir's working tree and diffs base against it.
func snapDiff(t *testing.T, g *Git, dir, base string) []FileDiff {
	t.Helper()
	ctx := context.Background()
	tree, err := g.SnapshotTree(ctx, dir, t.TempDir())
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	files, err := g.DiffTrees(ctx, dir, base, tree)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	return files
}

func fileNamed(t *testing.T, files []FileDiff, path string) FileDiff {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no %s in diff: %v", path, paths(files))
	return FileDiff{}
}

func paths(files []FileDiff) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestSnapshotTreeAndDiffTrees proves the chat diff's content (task 169
// decision 1): a modified, a deleted, a new untracked and a nested untracked
// file are all there, an ignored one is not, and neither call touches the
// index or HEAD.
func TestSnapshotTreeAndDiffTrees(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "a.txt", "one\nTWO\nthree\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "new.txt", "fresh\n")
	writeFile(t, dir, "deep/er/nested.txt", "nested\n")
	writeFile(t, dir, "debug.log", "ignored\n")
	// A staged change too, so "the index is untouched" is not vacuous.
	writeFile(t, dir, "staged.txt", "staged\n")
	testrepo.Run(t, dir, "add", "staged.txt")
	before := repoState(t, g, dir)
	statusBefore := testrepo.Run(t, dir, "status", "--porcelain")

	scratch := t.TempDir()
	tree, err := g.SnapshotTree(ctx, dir, scratch)
	if err != nil {
		t.Fatalf("SnapshotTree: %v", err)
	}
	if typ := testrepo.Run(t, dir, "cat-file", "-t", tree); typ != "tree" {
		t.Fatalf("SnapshotTree returned a %s, want a tree", typ)
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("scratch not cleaned: %v", left)
	}
	files, err := g.DiffTrees(ctx, dir, base, tree)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if after := testrepo.Run(t, dir, "status", "--porcelain"); after != statusBefore {
		t.Errorf("status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, after)
	}
	if refs := testrepo.Run(t, dir, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Errorf("refs = %q, want only refs/heads/main", refs)
	}

	want := []string{"a.txt", "deep/er/nested.txt", "gone.txt", "new.txt", "staged.txt"}
	if got := paths(files); !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	a := fileNamed(t, files, "a.txt")
	if a.Status != StatusModified || a.Added != 1 || a.Removed != 1 || len(a.Hunks) != 1 || !a.Rejectable() {
		t.Errorf("a.txt = %+v", a)
	}
	if a.Header[0] != "diff --git a/a.txt b/a.txt" || !slices.Contains(a.Header, "--- a/a.txt") || !slices.Contains(a.Header, "+++ b/a.txt") {
		t.Errorf("a.txt header = %q", a.Header)
	}
	h := a.Hunks[0]
	if !slices.Equal(h.Lines, []string{" one", "-two", "+TWO", " three"}) || h.Header != "@@ -1,3 +1,3 @@" || h.Added != 1 || h.Removed != 1 {
		t.Errorf("a.txt hunk = %+v", h)
	}
	sum := sha256.Sum256([]byte("a.txt\x00 one\n-two\n+TWO\n three"))
	if h.Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("hash = %s, want sha256 of path NUL lines", h.Hash)
	}
	for path, status := range map[string]string{
		"gone.txt": StatusDeleted, "new.txt": StatusAdded, "deep/er/nested.txt": StatusAdded, "staged.txt": StatusAdded,
	} {
		if f := fileNamed(t, files, path); f.Status != status || len(f.Hunks) != 1 || f.OldPath != "" {
			t.Errorf("%s = %+v, want %s with one hunk", path, f, status)
		}
	}

	n, added, removed, err := g.DiffTreesStat(ctx, dir, base, tree)
	if err != nil {
		t.Fatalf("DiffTreesStat: %v", err)
	}
	if n != 5 || added != 4 || removed != 2 {
		t.Errorf("stat = %d files +%d -%d, want 5 +4 -2", n, added, removed)
	}

	// An unchanged worktree is an empty diff, from a commit or from a tree.
	if same, err := g.DiffTrees(ctx, dir, tree, tree); err != nil || len(same) != 0 {
		t.Errorf("tree against itself = %v, %v", same, err)
	}
}

// TestDiffTreesIgnoresUserDiffConfig proves the output is the same whatever
// the repository's diff settings say.
func TestDiffTreesIgnoresUserDiffConfig(t *testing.T) {
	g, dir, _ := diffRepo(t)
	// A hunk with blank context lines: diff.suppressBlankEmpty would write
	// each as an empty line rather than a lone space, which reads as a hunk
	// cut short (review F3).
	writeFile(t, dir, "blank.txt", "a\n\nb\n\nc\n")
	testrepo.Run(t, dir, "add", "-A")
	testrepo.Run(t, dir, "commit", "-q", "-m", "blank lines")
	base := testrepo.Run(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "blank.txt", "a\n\nB\n\nc\n")
	writeFile(t, dir, "a.txt", "one\nTWO\nthree\n")
	writeFile(t, dir, "ünï cödé.txt", "x\n")
	plain := snapDiff(t, g, dir, base)
	if f := fileNamed(t, plain, "blank.txt"); len(f.Hunks) != 1 || !slices.Contains(f.Hunks[0].Lines, " ") {
		t.Fatalf("blank.txt has no blank context line: %+v", f)
	}
	for k, v := range map[string]string{
		"diff.noprefix": "true", "diff.mnemonicPrefix": "true", "color.ui": "always", "color.diff": "always",
		"diff.renames": "false", "diff.context": "0", "diff.external": "false", "core.quotepath": "true",
		"diff.suppressBlankEmpty": "true",
	} {
		testrepo.Run(t, dir, "config", k, v)
	}
	configured := snapDiff(t, g, dir, base)
	if !reflect.DeepEqual(plain, configured) {
		t.Errorf("diff changed with user config:\n%+v\n%+v", plain, configured)
	}
	if f := fileNamed(t, plain, "ünï cödé.txt"); f.Status != StatusAdded {
		t.Errorf("unicode path = %+v", f)
	}
}

// TestReverseHunkOneOfSeveral proves a rejection reverts exactly its hunk:
// the other hunks, the other files, the index and HEAD stay as they were,
// hashes are stable across calls, and the hunk below a rejected one keeps its
// hash and still applies although its line numbers moved.
func TestReverseHunkOneOfSeveral(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	// Hunk 1 adds two lines, so everything below it is shifted by two.
	edited := numbered(map[int]string{2: "line 2\nextra a\nextra b", 10: "TEN", 18: "EIGHTEEN"})
	writeFile(t, dir, "many.txt", edited)
	writeFile(t, dir, "a.txt", "one\nTWO\nthree\n")
	before := repoState(t, g, dir)

	f := fileNamed(t, snapDiff(t, g, dir, base), "many.txt")
	if len(f.Hunks) != 3 {
		t.Fatalf("hunks = %d, want 3", len(f.Hunks))
	}
	again := fileNamed(t, snapDiff(t, g, dir, base), "many.txt")
	for i := range f.Hunks {
		if f.Hunks[i].Hash != again.Hunks[i].Hash {
			t.Errorf("hunk %d hash differs between two calls", i)
		}
	}

	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[1].Hash); err != nil {
		t.Fatalf("ReverseHunk: %v", err)
	}
	want := numbered(map[int]string{2: "line 2\nextra a\nextra b", 18: "EIGHTEEN"})
	if got, _ := readFile(t, dir, "many.txt"); got != want {
		t.Errorf("many.txt after rejecting hunk 2:\n%s", got)
	}
	if got, _ := readFile(t, dir, "a.txt"); got != "one\nTWO\nthree\n" {
		t.Errorf("a.txt changed: %q", got)
	}

	// Reject the first hunk next, then the last one from the *original* diff:
	// its @@ numbers are now two lines off, and its hash is what the fresh
	// diff reports too.
	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash); err != nil {
		t.Fatalf("ReverseHunk first: %v", err)
	}
	fresh := fileNamed(t, snapDiff(t, g, dir, base), "many.txt")
	if len(fresh.Hunks) != 1 || fresh.Hunks[0].Hash != f.Hunks[2].Hash {
		t.Fatalf("remaining hunk does not keep its hash: %+v", fresh.Hunks)
	}
	if fresh.Hunks[0].Header == f.Hunks[2].Header {
		t.Fatalf("the hunk did not move; the test proves nothing: %s", fresh.Hunks[0].Header)
	}
	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[2].Hash); err != nil {
		t.Fatalf("ReverseHunk shifted: %v", err)
	}
	if got, _ := readFile(t, dir, "many.txt"); got != numbered(nil) {
		t.Errorf("many.txt after rejecting every hunk:\n%s", got)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestReverseHunkAddedAndDeletedFiles: rejecting a new untracked file's hunk
// deletes the file, a nested one's too, and a deleted file's restores it.
func TestReverseHunkAddedAndDeletedFiles(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "new.txt", "fresh\n")
	writeFile(t, dir, "deep/er/nested.txt", "nested")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	before := repoState(t, g, dir)
	files := snapDiff(t, g, dir, base)

	nested := fileNamed(t, files, "deep/er/nested.txt")
	if last := nested.Hunks[0].Lines[len(nested.Hunks[0].Lines)-1]; last != `\ No newline at end of file` {
		t.Errorf("the no-newline marker is not in its hunk: %q", nested.Hunks[0].Lines)
	}
	if nested.Added != 1 {
		t.Errorf("the marker was counted as a line: +%d", nested.Added)
	}
	for _, path := range []string{"new.txt", "deep/er/nested.txt"} {
		f := fileNamed(t, files, path)
		if err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash); err != nil {
			t.Fatalf("ReverseHunk %s: %v", path, err)
		}
		if _, ok := readFile(t, dir, path); ok {
			t.Errorf("%s still exists", path)
		}
	}
	gone := fileNamed(t, files, "gone.txt")
	if err := g.ReverseHunk(ctx, dir, gone, gone.Hunks[0].Hash); err != nil {
		t.Fatalf("ReverseHunk gone.txt: %v", err)
	}
	if got, _ := readFile(t, dir, "gone.txt"); got != "doomed\n" {
		t.Errorf("gone.txt = %q, want it restored", got)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if left := snapDiff(t, g, dir, base); len(left) != 0 {
		t.Errorf("diff after rejecting everything: %v", paths(left))
	}
}

// TestReverseHunkInCommittedChange: a hunk the agent already committed is
// reverted in the working tree only, the reversal left uncommitted (task 169
// decision 6).
func TestReverseHunkInCommittedChange(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "a.txt", "one\nTWO\nthree\n")
	testrepo.Run(t, dir, "commit", "-q", "-am", "agent commit")
	before := repoState(t, g, dir)

	f := fileNamed(t, snapDiff(t, g, dir, base), "a.txt")
	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash); err != nil {
		t.Fatalf("ReverseHunk: %v", err)
	}
	if got, _ := readFile(t, dir, "a.txt"); got != "one\ntwo\nthree\n" {
		t.Errorf("a.txt = %q", got)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if st := testrepo.Run(t, dir, "status", "--porcelain"); st != "M a.txt" {
		t.Errorf("status = %q, want the reversal as one unstaged change", st)
	}
	if left := snapDiff(t, g, dir, base); len(left) != 0 {
		t.Errorf("diff against the start after the rejection: %v", paths(left))
	}
}

// TestReverseHunkStaleAndConflict: a hash no hunk carries is ErrHunkNotFound;
// a hunk whose lines were since edited is ErrHunkConflict with git's message,
// and the tree is left untouched.
func TestReverseHunkStaleAndConflict(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "many.txt", numbered(map[int]string{3: "THREE", 15: "FIFTEEN"}))
	f := fileNamed(t, snapDiff(t, g, dir, base), "many.txt")

	if err := g.ReverseHunk(ctx, dir, f, strings.Repeat("0", 64)); !errors.Is(err, ErrHunkNotFound) {
		t.Errorf("unknown hash: %v, want ErrHunkNotFound", err)
	}

	edited := numbered(map[int]string{3: "THREE again", 15: "FIFTEEN"})
	writeFile(t, dir, "many.txt", edited)
	before := repoState(t, g, dir)
	err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash)
	if !errors.Is(err, ErrHunkConflict) {
		t.Fatalf("edited hunk: %v, want ErrHunkConflict", err)
	}
	if !strings.Contains(err.Error(), "many.txt") {
		t.Errorf("conflict does not carry git's message: %v", err)
	}
	if got, _ := readFile(t, dir, "many.txt"); got != edited {
		t.Errorf("a conflict changed the file:\n%s", got)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The edit changed the content, so the fresh diff no longer has the hash
	// (task 169 decision 2) — while the untouched hunk keeps its own.
	fresh := fileNamed(t, snapDiff(t, g, dir, base), "many.txt")
	if fresh.Hunks[0].Hash == f.Hunks[0].Hash || fresh.Hunks[1].Hash != f.Hunks[1].Hash {
		t.Errorf("hashes after an edit: %s %s", fresh.Hunks[0].Hash, fresh.Hunks[1].Hash)
	}
	if err := g.ReverseHunk(ctx, dir, fresh, f.Hunks[0].Hash); !errors.Is(err, ErrHunkNotFound) {
		t.Errorf("stale hash: %v, want ErrHunkNotFound", err)
	}
}

// TestHunkHashesUniqueForDuplicateContent: two hunks of one file with the
// same lines get different hashes, each rejects its own occurrence, and the
// same lines in another file hash differently again.
func TestHunkHashesUniqueForDuplicateContent(t *testing.T) {
	g, dir, _ := diffRepo(t)
	ctx := context.Background()
	block := "a\nb\nc\nold\nd\ne\nf\n"
	filler := "1\n2\n3\n4\n5\n6\n7\n8\n"
	writeFile(t, dir, "dup.txt", block+filler+block)
	writeFile(t, dir, "dup2.txt", block)
	testrepo.Run(t, dir, "add", "-A")
	testrepo.Run(t, dir, "commit", "-q", "-m", "dup")
	base := testrepo.Run(t, dir, "rev-parse", "HEAD")
	newBlock := strings.Replace(block, "old", "new", 1)
	writeFile(t, dir, "dup.txt", newBlock+filler+newBlock)
	writeFile(t, dir, "dup2.txt", newBlock)

	files := snapDiff(t, g, dir, base)
	f := fileNamed(t, files, "dup.txt")
	if len(f.Hunks) != 2 || !slices.Equal(f.Hunks[0].Lines, f.Hunks[1].Lines) {
		t.Fatalf("want two hunks with equal lines, got %+v", f.Hunks)
	}
	other := fileNamed(t, files, "dup2.txt").Hunks[0].Hash
	if f.Hunks[0].Hash == f.Hunks[1].Hash || f.Hunks[0].Hash == other || f.Hunks[1].Hash == other {
		t.Fatalf("hashes not unique: %s %s %s", f.Hunks[0].Hash, f.Hunks[1].Hash, other)
	}
	sum := sha256.Sum256([]byte("dup.txt\x00" + strings.Join(f.Hunks[1].Lines, "\n") + "\x002"))
	if f.Hunks[1].Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("second occurrence is not hashed with its ordinal")
	}
	again := fileNamed(t, snapDiff(t, g, dir, base), "dup.txt")
	if again.Hunks[0].Hash != f.Hunks[0].Hash || again.Hunks[1].Hash != f.Hunks[1].Hash {
		t.Errorf("hashes differ between two calls")
	}
	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[1].Hash); err != nil {
		t.Fatalf("ReverseHunk: %v", err)
	}
	if got, _ := readFile(t, dir, "dup.txt"); got != newBlock+filler+block {
		t.Errorf("rejecting the second occurrence changed the wrong one:\n%s", got)
	}
}

// TestDiffTreesNotRejectable: a binary file, a rename and a mode-only change
// are listed, and ReverseHunk refuses each. The three are committed and the
// commits diffed, which is how a mode change exists on Windows at all.
func TestDiffTreesNotRejectable(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "blob.bin", "\x00\x01\x02binary\x00")
	testrepo.Run(t, dir, "mv", "many.txt", "moved.txt")
	testrepo.Run(t, dir, "add", "-A")
	testrepo.Run(t, dir, "update-index", "--chmod=+x", "a.txt")
	testrepo.Run(t, dir, "commit", "-q", "-m", "three kinds")
	before := repoState(t, g, dir)

	files, err := g.DiffTrees(ctx, dir, base, "HEAD^{tree}")
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if got, want := paths(files), []string{"a.txt", "blob.bin", "moved.txt"}; !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	mode := fileNamed(t, files, "a.txt")
	if mode.Status != StatusModified || !mode.ModeOnly || mode.Binary || len(mode.Hunks) != 0 {
		t.Errorf("mode-only = %+v", mode)
	}
	bin := fileNamed(t, files, "blob.bin")
	if bin.Status != StatusAdded || !bin.Binary || bin.ModeOnly || len(bin.Hunks) != 0 {
		t.Errorf("binary = %+v", bin)
	}
	moved := fileNamed(t, files, "moved.txt")
	if moved.Status != StatusRenamed || moved.OldPath != "many.txt" || moved.Binary || moved.ModeOnly {
		t.Errorf("rename = %+v", moved)
	}
	for _, f := range files {
		if f.Rejectable() {
			t.Errorf("%s is rejectable", f.Path)
		}
		if err := g.ReverseHunk(ctx, dir, f, "anything"); !errors.Is(err, ErrHunkNotRejectable) {
			t.Errorf("%s: %v, want ErrHunkNotRejectable", f.Path, err)
		}
	}
	n, added, removed, err := g.DiffTreesStat(ctx, dir, base, "HEAD^{tree}")
	if err != nil || n != 3 || added != 0 || removed != 0 {
		t.Errorf("stat = %d +%d -%d, %v; want 3 +0 -0", n, added, removed, err)
	}
	if after := repoState(t, g, dir); after != before {
		t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestReverseHunkLeavesAModeChangeAlone: a file whose mode and content both
// changed is rejectable, and rejecting its hunk reverts the lines without
// applying the header's "old mode" — git reports no mode change afterwards
// that it did not report before.
func TestReverseHunkLeavesAModeChangeAlone(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "a.txt", "one\nTWO\nthree\n")
	// The worktree file is made executable too where the OS has the bit, so
	// that putting the old mode back would show up below.
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	testrepo.Run(t, dir, "add", "a.txt")
	testrepo.Run(t, dir, "update-index", "--chmod=+x", "a.txt")
	testrepo.Run(t, dir, "commit", "-q", "-m", "mode and content")
	f, err := g.DiffTrees(ctx, dir, base, "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	a := fileNamed(t, f, "a.txt")
	if a.ModeOnly || !a.Rejectable() || !slices.Contains(a.Header, "old mode 100644") {
		t.Fatalf("a.txt = %+v", a)
	}
	modeBefore := testrepo.Run(t, dir, "diff", "--summary")
	if err := g.ReverseHunk(ctx, dir, a, a.Hunks[0].Hash); err != nil {
		t.Fatalf("ReverseHunk: %v", err)
	}
	if got, _ := readFile(t, dir, "a.txt"); got != "one\ntwo\nthree\n" {
		t.Errorf("a.txt = %q", got)
	}
	if modeAfter := testrepo.Run(t, dir, "diff", "--summary"); modeAfter != modeBefore {
		t.Errorf("the rejection changed the mode: %q, was %q", modeAfter, modeBefore)
	}
}

// TestReverseHunkCRLF: CRLF content is parsed byte-faithfully and round-trips
// through a rejection, both where the blob holds CRLF (core.autocrlf off) and
// where it holds LF and the worktree CRLF (core.autocrlf on, Git for
// Windows' default), and whatever apply.whitespace the user configured.
func TestReverseHunkCRLF(t *testing.T) {
	for _, autocrlf := range []string{"false", "true"} {
		t.Run("autocrlf="+autocrlf, func(t *testing.T) {
			g, dir, _ := diffRepo(t)
			ctx := context.Background()
			testrepo.Run(t, dir, "config", "core.autocrlf", autocrlf)
			testrepo.Run(t, dir, "config", "apply.whitespace", "fix")
			orig := strings.ReplaceAll(numbered(nil), "\n", "\r\n")
			writeFile(t, dir, "crlf.txt", orig)
			testrepo.Run(t, dir, "add", "-A")
			testrepo.Run(t, dir, "commit", "-q", "-m", "crlf")
			base := testrepo.Run(t, dir, "rev-parse", "HEAD")

			edited := strings.ReplaceAll(numbered(map[int]string{3: "THREE", 15: "FIFTEEN  "}), "\n", "\r\n")
			writeFile(t, dir, "crlf.txt", edited)
			writeFile(t, dir, "newcrlf.txt", "x\r\ny\r\n")
			before := repoState(t, g, dir)
			files := snapDiff(t, g, dir, base)
			f := fileNamed(t, files, "crlf.txt")
			if len(f.Hunks) != 2 {
				t.Fatalf("hunks = %d, want 2", len(f.Hunks))
			}
			// The blob holds CRLF only with autocrlf off; then every line of
			// the hunk must still carry its "\r".
			for _, line := range f.Hunks[0].Lines {
				if got := strings.HasSuffix(line, "\r"); got != (autocrlf == "false") {
					t.Fatalf("line %q: CR kept = %v under autocrlf=%s", line, got, autocrlf)
				}
			}
			if err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash); err != nil {
				t.Fatalf("ReverseHunk: %v", err)
			}
			want := strings.ReplaceAll(numbered(map[int]string{15: "FIFTEEN  "}), "\n", "\r\n")
			if got, _ := readFile(t, dir, "crlf.txt"); got != want {
				t.Errorf("crlf.txt after rejecting the first hunk = %q", got)
			}
			if err := g.ReverseHunk(ctx, dir, f, f.Hunks[1].Hash); err != nil {
				t.Fatalf("ReverseHunk second: %v", err)
			}
			if got, _ := readFile(t, dir, "crlf.txt"); got != orig {
				t.Errorf("crlf.txt after rejecting both = %q", got)
			}
			n := fileNamed(t, files, "newcrlf.txt")
			if err := g.ReverseHunk(ctx, dir, n, n.Hunks[0].Hash); err != nil {
				t.Fatalf("ReverseHunk new CRLF file: %v", err)
			}
			if _, ok := readFile(t, dir, "newcrlf.txt"); ok {
				t.Errorf("newcrlf.txt still exists")
			}
			if after := repoState(t, g, dir); after != before {
				t.Errorf("index or HEAD changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestParsePatchIsByteFaithful covers what a real diff rarely shows together:
// hunk lines that look like structure, omitted counts, and a marker line.
func TestParsePatchIsByteFaithful(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/x b/x",
		"index 111..222 100644",
		"--- a/x",
		"+++ b/x",
		"@@ -1,2 +1,2 @@ func",
		"--- a/not-a-header",
		"+diff --git a/y b/y",
		" keep\r",
		"@@ -9 +9 @@",
		"-@@ -1 +1 @@",
		`\ No newline at end of file`,
		"+tail",
		`\ No newline at end of file`,
		"diff --git a/b.bin b/b.bin",
		"index 111..222 100644",
		"Binary files a/b.bin and b/b.bin differ",
		"",
	}, "\n")
	files, err := parsePatch(patch)
	if err != nil {
		t.Fatalf("parsePatch: %v", err)
	}
	if len(files) != 2 || len(files[0].Hunks) != 2 || len(files[0].Header) != 4 {
		t.Fatalf("files = %+v", files)
	}
	if got := files[0].Hunks[0].Lines; !slices.Equal(got, []string{"--- a/not-a-header", "+diff --git a/y b/y", " keep\r"}) {
		t.Errorf("hunk 1 lines = %q", got)
	}
	if got := files[0].Hunks[1].Lines; len(got) != 4 || got[3] != `\ No newline at end of file` {
		t.Errorf("hunk 2 lines = %q", got)
	}
	if files[0].Added != 2 || files[0].Removed != 2 {
		t.Errorf("counts = +%d -%d", files[0].Added, files[0].Removed)
	}
	if !files[1].Binary || len(files[1].Hunks) != 0 {
		t.Errorf("binary = %+v", files[1])
	}
	if _, err := parsePatch("diff --git a/x b/x\n@@ -1,2 +1,2 @@\n one\n"); err == nil {
		t.Error("a hunk cut short parsed without error")
	}
}

// TestDiffTreesTypeChangeAndEmptyFile: a type change is the one entry git
// renders as two patches, and both come back on its path; an empty new file
// is listed with no hunk. The symlink is written straight into the index, so
// no OS has to be able to create one.
func TestDiffTreesTypeChangeAndEmptyFile(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "empty.txt", "")
	testrepo.Run(t, dir, "add", "empty.txt")
	blob := testrepo.Run(t, dir, "rev-parse", "HEAD:gone.txt")
	testrepo.Run(t, dir, "update-index", "--cacheinfo", "120000,"+blob+",a.txt")
	testrepo.Run(t, dir, "commit", "-q", "-m", "typechange")

	files, err := g.DiffTrees(ctx, dir, base, "HEAD^{tree}")
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if got, want := paths(files), []string{"a.txt", "a.txt", "empty.txt"}; !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if files[0].Status != StatusDeleted || files[1].Status != StatusAdded || files[0].Hunks[0].Hash == files[1].Hunks[0].Hash {
		t.Errorf("type change = %+v, %+v", files[0], files[1])
	}
	if e := files[2]; e.Status != StatusAdded || len(e.Hunks) != 0 || e.Binary || e.ModeOnly {
		t.Errorf("empty file = %+v", e)
	}
}

// TestDiffTreesGitlinkNotRejectable is review F4: a directory holding its own
// .git — an agent ran `git clone` or `git init` in the worktree — is recorded
// by `add -A` as a gitlink and listed as an added file with one "Subproject
// commit" hunk. `git apply -R` on that patch removes nothing and exits 0, so
// the file must refuse a reject rather than report one that did not happen.
func TestDiffTreesGitlinkNotRejectable(t *testing.T) {
	g, dir, base := diffRepo(t)
	ctx := context.Background()
	nested := filepath.Join(dir, "vendor", "lib")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	testrepo.Run(t, nested, "init", "-q")
	testrepo.Run(t, nested, "config", "user.name", "t")
	testrepo.Run(t, nested, "config", "user.email", "t@example.invalid")
	testrepo.Run(t, nested, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "vendor/lib/lib.go", "package lib\n")
	testrepo.Run(t, nested, "add", "-A")
	testrepo.Run(t, nested, "commit", "-q", "-m", "nested")

	f := fileNamed(t, snapDiff(t, g, dir, base), "vendor/lib")
	if f.Status != StatusAdded || !f.Gitlink || len(f.Hunks) != 1 {
		t.Fatalf("nested repository = %+v", f)
	}
	if f.Rejectable() || f.NotRejectable() != NotRejectableGitlink {
		t.Errorf("rejectable = %v, reason %q; want a gitlink refused", f.Rejectable(), f.NotRejectable())
	}
	if err := g.ReverseHunk(ctx, dir, f, f.Hunks[0].Hash); !errors.Is(err, ErrHunkNotRejectable) {
		t.Errorf("ReverseHunk = %v, want ErrHunkNotRejectable", err)
	}
	if got, ok := readFile(t, dir, "vendor/lib/lib.go"); !ok || got != "package lib\n" {
		t.Errorf("the nested repository's file changed: %q, %v", got, ok)
	}
}
