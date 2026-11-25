//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// stubRun records every systemctl/loginctl invocation instead of running it,
// and points HOME at an empty directory so unitPath never sees the
// developer's real user units.
func stubRun(t *testing.T, err error) (home string, calls *[][]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	var got [][]string
	orig := run
	run = func(_ context.Context, name string, args ...string) (string, error) {
		got = append(got, append([]string{name}, args...))
		return "", err
	}
	t.Cleanup(func() { run = orig })
	return home, &got
}

func writeUnit(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, Label+".service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #1160: a restart is systemd's own, so the new daemon is the unit's process
// rather than one started beside it.
func TestRestartRestartsTheUnit(t *testing.T) {
	home, calls := stubRun(t, nil)
	writeUnit(t, home)

	if err := Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	want := [][]string{{"systemctl", "--user", "restart", Label + ".service"}}
	if !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

func TestRestartWithNothingInstalled(t *testing.T) {
	_, calls := stubRun(t, nil)
	if err := Restart(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Restart = %v, want ErrNotInstalled", err)
	}
	if len(*calls) != 0 {
		t.Errorf("systemctl ran with nothing installed: %q", *calls)
	}
}

func TestRestartReportsSystemctlsFailure(t *testing.T) {
	boom := errors.New("Job for vincent.service failed")
	home, _ := stubRun(t, boom)
	writeUnit(t, home)
	if err := Restart(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Restart = %v, want systemctl's error", err)
	}
}
