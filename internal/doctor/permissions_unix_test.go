//go:build !windows

package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// A config directory or config.yaml other local accounts can read is worth a
// row of a user's attention: environment.set values are literal, so that file
// is where an API token ends up (§12.3). The row carries the exact chmod, and
// it is a warning rather than a Problem — the closed set that sets the exit
// code is unchanged (task 005 decision 7).
func TestConfigPermissionsWarn(t *testing.T) {
	d := dirs(t)
	path := filepath.Join(d.Config, config.FileName)
	write(t, path, "max_parallel_tasks: 2\n")
	if err := os.Chmod(d.Config, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := Compose(t.Context(), Options{Dirs: d, Agents: []Agent{}})

	want := map[string]PermissionWarning{
		d.Config: {Path: d.Config, Mode: "0755", ExpectedMode: "0700", Remediation: "chmod 0700 " + d.Config},
		path:     {Path: path, Mode: "0644", ExpectedMode: "0600", Remediation: "chmod 0600 " + path},
	}
	if len(rep.Paths.ConfigPermissions) != len(want) {
		t.Fatalf("ConfigPermissions = %+v, want one row per broad path", rep.Paths.ConfigPermissions)
	}
	for _, got := range rep.Paths.ConfigPermissions {
		if got != want[got.Path] {
			t.Errorf("warning for %s = %+v, want %+v", got.Path, got, want[got.Path])
		}
	}
	if hasProblem(rep, GroupPaths) {
		t.Errorf("a broad mode set a problem: %v", rep.Problems)
	}
}

// The same report on an owner-only installation says nothing — which is what
// every installation looks like once a daemon has started on it, since
// config.EnsureDefaultFile tightens the modes it finds.
func TestOwnerOnlyConfigWarnsAboutNothing(t *testing.T) {
	d := dirs(t)
	write(t, filepath.Join(d.Config, config.FileName), "max_parallel_tasks: 2\n")
	// t.TempDir subdirectories are 0755 under the usual umask, so the owner-only
	// state this test is about has to be asked for.
	if err := os.Chmod(d.Config, 0o700); err != nil {
		t.Fatal(err)
	}

	rep := Compose(t.Context(), Options{Dirs: d, Agents: []Agent{}})

	if len(rep.Paths.ConfigPermissions) != 0 {
		t.Errorf("ConfigPermissions = %+v, want none", rep.Paths.ConfigPermissions)
	}
}
