//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The config directory and config.yaml may hold user-supplied secrets:
// environment.set takes literal values (§12.3), which is where an API token or
// a license key ends up. Both are created for the owner only, like every other
// file the daemon owns — {data_dir}/token, transcripts, logs (§12.2, §16).
//
// The umask is pinned to the common 022 so the assertion measures the modes
// requested by EnsureDefaultFile rather than the ones the developer's shell
// happened to allow. No test in this package runs in parallel, so the
// process-global umask is safe to move and restore here.
func TestEnsureDefaultFilePermissions(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := filepath.Join(t.TempDir(), "cfg") // exercise dir creation too
	created, err := EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile: %v", err)
	}
	if !created {
		t.Fatal("created = false on first call, want true")
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config dir mode = %#o, want no group/other access (0700)", perm)
	}

	path := filepath.Join(dir, FileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("%s mode = %#o, want no group/other access (0600)", FileName, perm)
	}
}

// An installation created before the modes were tightened keeps a 0755 dir and
// a 0644 config.yaml forever: EnsureDefaultFile returns early on an existing
// file. The daemon re-tightens what it owns on every start, the way
// daemon.EnsureToken does for {data_dir}/token — and the file's contents stay
// exactly as the user left them.
func TestEnsureDefaultFileTightensExistingModes(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	const custom = "max_parallel_tasks: 42\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile: %v", err)
	}
	if created {
		t.Error("created = true for an existing file, want false")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != custom {
		t.Errorf("existing config rewritten: %q, want %q", raw, custom)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("existing config dir left at %#o, want group/other access dropped", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("existing %s left at %#o, want group/other access dropped", FileName, perm)
	}
}
