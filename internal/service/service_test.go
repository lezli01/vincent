package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// TestResolveCapturesTheDirsInEffect is the decision that makes an installed
// service use the same database as the CLI that installed it. A service does
// not inherit the shell that installed it, so directory overrides have to be
// captured at install time or the service silently runs against different
// state.
func TestResolveCapturesTheDirsInEffect(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvDataDir, dataDir)

	got, err := Options{}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Dirs.Config != cfgDir {
		t.Errorf("config dir = %q, want the override %q", got.Dirs.Config, cfgDir)
	}
	if got.Dirs.Data != dataDir {
		t.Errorf("data dir = %q, want the override %q", got.Dirs.Data, dataDir)
	}
	if got.Exe == "" || !filepath.IsAbs(got.Exe) {
		t.Errorf("exe = %q, want the absolute path of the running binary", got.Exe)
	}
}

// TestResolveKeepsExplicitValues: a caller that supplies paths gets them
// back untouched, which is what makes the installer testable and scriptable.
func TestResolveKeepsExplicitValues(t *testing.T) {
	want := Options{
		Exe:  filepath.Join(t.TempDir(), "vincent"),
		Dirs: config.Dirs{Config: "/etc/vincent", Data: "/var/lib/vincent"},
	}
	got, err := want.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != want {
		t.Errorf("resolve = %+v, want it unchanged (%+v)", got, want)
	}
}

// TestResolveDefaultsToTheRunningBinary: `service install` must install *this*
// binary, not whatever happens to be on PATH when the service later starts.
func TestResolveDefaultsToTheRunningBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	got, err := Options{Dirs: config.Dirs{Config: "c", Data: "d"}}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Exe != self {
		t.Errorf("exe = %q, want the running binary %q", got.Exe, self)
	}
}

// TestLabelsAreStable pins the identifiers the OS stores. Changing one turns
// an installed service into an orphan the next uninstall cannot find, and on
// Windows it must also match the name the SCM handshake registers
// (internal/daemon.serviceName).
func TestLabelsAreStable(t *testing.T) {
	if Label != "vincent" {
		t.Errorf("Label = %q; it must match internal/daemon.serviceName", Label)
	}
	if LaunchdName != "dev.lezli01.vincent" {
		t.Errorf("LaunchdName = %q, want the reverse-DNS launchd label", LaunchdName)
	}
}
