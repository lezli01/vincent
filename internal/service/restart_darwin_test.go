//go:build darwin

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// stubLaunchctl records every launchctl invocation instead of running it, and
// points HOME at an empty directory so plistPath never sees the developer's
// real LaunchAgents.
func stubLaunchctl(t *testing.T, err error) (home string, calls *[][]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	var got [][]string
	orig := launchctl
	launchctl = func(_ context.Context, args ...string) (string, error) {
		got = append(got, args)
		return "", err
	}
	t.Cleanup(func() { launchctl = orig })
	return home, &got
}

func writePlist(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LaunchdName+".plist"), []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #1160: a restart is launchd's kickstart -k, so the new daemon is the
// agent's own process rather than one started beside it.
func TestRestartKickstartsTheAgent(t *testing.T) {
	home, calls := stubLaunchctl(t, nil)
	writePlist(t, home)

	if err := Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	want := [][]string{{"kickstart", "-k", "gui/" + strconv.Itoa(os.Getuid()) + "/" + LaunchdName}}
	if !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Errorf("launchctl calls = %q, want %q", *calls, want)
	}
}

func TestRestartWithNothingInstalled(t *testing.T) {
	_, calls := stubLaunchctl(t, nil)
	if err := Restart(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Restart = %v, want ErrNotInstalled", err)
	}
	if len(*calls) != 0 {
		t.Errorf("launchctl ran with nothing installed: %q", *calls)
	}
}

func TestRestartReportsLaunchctlsFailure(t *testing.T) {
	boom := errors.New("Could not find service")
	home, _ := stubLaunchctl(t, boom)
	writePlist(t, home)
	if err := Restart(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Restart = %v, want launchctl's error", err)
	}
}
