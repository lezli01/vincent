//go:build !windows

package trigger

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lezli01/vincent/internal/workflow"
)

// Every trigger write is 0600, new file or existing (decision 20): a trigger's
// argv may carry a secret, and no repository owns the file. The umask is
// pinned to 022 so the assertion measures the requested mode; no test in this
// package runs in parallel, so the process-global umask is safe to move.
func TestWritesAreOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := filepath.Join(t.TempDir(), "triggers")
	w := NewWriter(dir)
	if _, err := w.Create("t1", Starter(StarterSpec{ID: "t1", Project: 1})); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertMode(t, filepath.Join(dir, "t1.yaml"), 0o600)
	assertMode(t, dir, 0o700)

	// A hand-created 0644 file is tightened by the first write through the
	// daemon, not kept as workflows' existing files are.
	path := writeFile(t, dir, "t2.yaml", string(Starter(StarterSpec{ID: "t2", Project: 1})), 0o644)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := workflow.Version(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Patch("t2", v, []workflow.Op{{Kind: workflow.OpSet, Path: "source.poll_interval", Value: "10m"}}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}
