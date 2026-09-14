//go:build !windows

package trigger

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRegistryUnreadableKeepsSet: neither a file nor a directory that cannot
// be read is a removal — the previous entries stand, OnChange does not run,
// and the failure is logged (decision 16). Mode 000 is the POSIX way to make
// one unreadable; Windows has no equivalent a test can set and undo.
func TestRegistryUnreadableKeepsSet(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	dir := t.TempDir()
	writeFile(t, dir, "a.yaml", registryDoc("a", "a"))
	writeFile(t, dir, "b.yaml", registryDoc("b", "b"))
	var logs syncBuffer
	reg := NewRegistry(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	var changes changeLog
	reg.OnChange(changes.record)
	reg.Reload()
	if got := registryIDs(reg); !slices.Equal(got, []string{"a", "b"}) || len(changes) != 1 {
		t.Fatalf("ids = %v, changes %v", got, changes)
	}

	b := filepath.Join(dir, "b.yaml")
	if err := os.Chmod(b, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(b, 0o600) })
	reg.Reload()
	if e, ok := reg.Get("b"); !ok || !e.Valid() || len(changes) != 1 {
		t.Errorf("unreadable file: b = %+v (present %v), changes %v", e, ok, changes)
	}
	if !strings.Contains(logs.String(), "trigger file unreadable") {
		t.Errorf("unreadable file not logged: %s", logs.String())
	}
	if err := os.Chmod(b, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	reg.Reload()
	if got := registryIDs(reg); !slices.Equal(got, []string{"a", "b"}) || len(changes) != 1 {
		t.Errorf("unreadable directory: ids = %v, changes %v; want the previous set", got, changes)
	}
	if !strings.Contains(logs.String(), "triggers directory unreadable") {
		t.Errorf("unreadable directory not logged: %s", logs.String())
	}
}
