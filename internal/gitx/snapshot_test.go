package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// snapRepo is a repo with one commit holding a.txt, gone.txt and a
// .gitignore that ignores *.log, and core.autocrlf off so file bytes compare
// exactly on Windows too.
func snapRepo(t *testing.T) (*Git, string) {
	t.Helper()
	requireGit(t)
	g := New()
	dir := t.TempDir()
	ctx := context.Background()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := g.Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init", "-q")
	mustGit("config", "core.autocrlf", "false")
	mustGit("config", "user.name", "t")
	mustGit("config", "user.email", "t@example.com")
	writeFile(t, dir, "a.txt", "one\n")
	writeFile(t, dir, "gone.txt", "doomed\n")
	writeFile(t, dir, ".gitignore", "*.log\n")
	mustGit("add", "-A")
	mustGit("commit", "-q", "-m", "init")
	return g, dir
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

// TestSnapshotLeavesIndexAndHeadAlone proves a capture touches neither the
// real index nor HEAD, and holds every kind of change but ignored files.
func TestSnapshotLeavesIndexAndHeadAlone(t *testing.T) {
	g, dir := snapRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "a.txt", "two\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "new.txt", "fresh\n")
	writeFile(t, dir, "deep/er/nested.txt", "nested\n")
	writeFile(t, dir, "debug.log", "ignored\n")
	statusBefore, _ := g.Run(ctx, dir, "status", "--porcelain")
	headBefore, _ := g.Run(ctx, dir, "rev-parse", "HEAD")

	snap, err := g.Snapshot(ctx, dir, t.TempDir(), "checkpoint")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Head != headBefore {
		t.Errorf("Head = %s, want %s", snap.Head, headBefore)
	}
	if parent, _ := g.Run(ctx, dir, "rev-parse", snap.Commit+"^1"); parent != headBefore {
		t.Errorf("parent = %s, want %s", parent, headBefore)
	}
	if after, _ := g.Run(ctx, dir, "status", "--porcelain"); after != statusBefore {
		t.Errorf("status changed:\nbefore %q\nafter  %q", statusBefore, after)
	}
	files, err := g.Run(ctx, dir, "ls-tree", "-r", "--name-only", snap.Commit)
	if err != nil {
		t.Fatal(err)
	}
	want := ".gitignore\na.txt\ndeep/er/nested.txt\nnew.txt"
	if files != want {
		t.Errorf("snapshot tree =\n%s\nwant\n%s", files, want)
	}
}

// TestSnapshotUnchangedReusesTree is the disk-cost property: a capture of an
// unchanged worktree adds no tree, only a commit.
func TestSnapshotUnchangedReusesTree(t *testing.T) {
	g, dir := snapRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "new.txt", "x\n")
	first, err := g.Snapshot(ctx, dir, t.TempDir(), "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Snapshot(ctx, dir, t.TempDir(), "two")
	if err != nil {
		t.Fatal(err)
	}
	t1, _ := g.Run(ctx, dir, "rev-parse", first.Commit+"^{tree}")
	t2, _ := g.Run(ctx, dir, "rev-parse", second.Commit+"^{tree}")
	if t1 == "" || t1 != t2 {
		t.Errorf("trees differ: %q vs %q", t1, t2)
	}
}

// TestRestoreRoundTrip captures a dirty state, commits and scribbles over it,
// and restores: the branch is back on the captured HEAD, every file is back
// byte for byte as an unstaged change, and the ignored file is untouched.
func TestRestoreRoundTrip(t *testing.T) {
	g, dir := snapRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "a.txt", "two\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "new.txt", "fresh\n")
	writeFile(t, dir, "deep/er/nested.txt", "nested\n")
	snap, err := g.Snapshot(ctx, dir, t.TempDir(), "checkpoint")
	if err != nil {
		t.Fatal(err)
	}

	// Later work: a commit, then more dirt, then an ignored file.
	if _, err := g.Run(ctx, dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, dir, "commit", "-q", "-m", "later"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "three\n")
	writeFile(t, dir, "stray.txt", "stray\n")
	writeFile(t, dir, "stray/dir/f.txt", "stray\n")
	writeFile(t, dir, "keep.log", "ignored\n")

	if err := g.Restore(ctx, dir, snap.Commit); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if head, _ := g.Run(ctx, dir, "rev-parse", "HEAD"); head != snap.Head {
		t.Errorf("HEAD = %s, want %s", head, snap.Head)
	}
	for rel, want := range map[string]string{
		"a.txt": "two\n", "new.txt": "fresh\n", "deep/er/nested.txt": "nested\n", "keep.log": "ignored\n",
	} {
		if got, ok := readFile(t, dir, rel); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", rel, got, ok, want)
		}
	}
	for _, rel := range []string{"gone.txt", "stray.txt", "stray/dir/f.txt"} {
		if _, ok := readFile(t, dir, rel); ok {
			t.Errorf("%s survived the restore", rel)
		}
	}
	// Nothing staged: the snapshot's changes are unstaged.
	if staged, _ := g.Run(ctx, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged after restore: %q", staged)
	}
	// And a fresh capture of the restored state has the snapshot's tree.
	again, err := g.Snapshot(ctx, dir, t.TempDir(), "again")
	if err != nil {
		t.Fatal(err)
	}
	t1, _ := g.Run(ctx, dir, "rev-parse", snap.Commit+"^{tree}")
	t2, _ := g.Run(ctx, dir, "rev-parse", again.Commit+"^{tree}")
	if t1 != t2 {
		t.Errorf("restored tree %s != snapshot tree %s", t2, t1)
	}
}

func TestUpdateAndDeleteRefs(t *testing.T) {
	g, dir := snapRepo(t)
	ctx := context.Background()
	snap, err := g.Snapshot(ctx, dir, t.TempDir(), "c")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/vincent/chats/7/turn-1", "refs/vincent/chats/7/turn-2", "refs/vincent/chats/8/turn-1"} {
		if err := g.UpdateRef(ctx, dir, ref, snap.Commit); err != nil {
			t.Fatalf("UpdateRef %s: %v", ref, err)
		}
	}
	n, err := g.DeleteRefs(ctx, dir, "refs/vincent/chats/7/")
	if err != nil || n != 2 {
		t.Fatalf("DeleteRefs = %d, %v; want 2", n, err)
	}
	left, _ := g.Run(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/vincent/")
	if left != "refs/vincent/chats/8/turn-1" {
		t.Errorf("refs left = %q", left)
	}
	if _, err := g.DeleteRefs(ctx, dir, "heads/"); err == nil {
		t.Error("DeleteRefs accepted a prefix outside refs/")
	}
}
