package pathx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainsIsByComponent(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	webapp := filepath.Join(root, "webapp")
	for _, d := range []string{filepath.Join(web, "src"), webapp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		dir, child string
		want       bool
	}{
		{web, web, true},
		{web, filepath.Join(web, "src"), true},
		{web, filepath.Join(web, "src") + string(filepath.Separator), true},
		{web, webapp, false},
		{web, root, false},
		{filepath.Join(web, "src"), web, false},
		// Lexical: neither side has to exist.
		{filepath.Join(root, "gone"), filepath.Join(root, "gone", "deeper"), true},
	}
	for _, c := range cases {
		if got := Contains(c.dir, c.child); got != c.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", c.dir, c.child, got, c.want)
		}
	}
}

func TestContainsThroughASymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !Contains(target, filepath.Join(link, "sub")) {
		t.Error("a child spelled through a symlink is not contained by the target directory")
	}
	if !Contains(link, filepath.Join(target, "sub")) {
		t.Error("a child of the target directory is not contained by the symlink to it")
	}
	if !SameDir(link, target) {
		t.Error("a symlink and its target are not the same directory")
	}
}

func TestCaseFolding(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(root, "repo")
	if !CaseInsensitive() {
		if SameDir(dir, lower) || Contains(dir, filepath.Join(lower, "x")) {
			t.Error("a case-sensitive platform folded case")
		}
		return
	}
	if !SameDir(dir, lower) {
		t.Error("SameDir did not fold case")
	}
	if !Contains(strings.ToUpper(dir), filepath.Join(lower, "x")) {
		t.Error("Contains did not fold case")
	}
}

func TestSameDir(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if !SameDir(a, a+string(filepath.Separator)+".") {
		t.Error("Clean did not reconcile two spellings")
	}
	if !SameDir(filepath.Join(root, "missing"), filepath.Join(root, "x", "..", "missing")) {
		t.Error("two spellings of a missing path should compare lexically")
	}
	if SameDir(a, root) {
		t.Error("a directory is not its parent")
	}
}

// TestIsALeaf: the TUI imports this package precisely so it need not import
// internal/worktree, so it may depend on the standard library alone.
func TestIsALeaf(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/lezli01/vincent/") && strings.Contains(dep, "/internal/") &&
			!strings.HasSuffix(dep, "/internal/pathx") {
			t.Errorf("internal/pathx depends on %s; it must stay a leaf", dep)
		}
	}
}
