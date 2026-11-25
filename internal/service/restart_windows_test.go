//go:build windows

package service

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// stubSchtasks records every schtasks invocation instead of running it. fail
// names the verb (its first argument) that should fail; /Query failing is how
// a task reads as not installed.
func stubSchtasks(t *testing.T, fail map[string]error) (calls *[][]string, stopped *int) {
	t.Helper()
	var got [][]string
	var stops int
	origS, origStop := schtasks, stopForRestart
	schtasks = func(_ context.Context, args ...string) (string, error) {
		got = append(got, args)
		return "", fail[args[0]]
	}
	stopForRestart = func(context.Context) {
		got = append(got, []string{"<graceful stop>"})
		stops++
	}
	t.Cleanup(func() { schtasks, stopForRestart = origS, origStop })
	return &got, &stops
}

// #1160: a restart asks the daemon to stop gracefully — /End alone is a hard
// terminate — then ends and runs the task, so the new daemon is the task's
// own process.
func TestRestartEndsAndRunsTheTask(t *testing.T) {
	calls, _ := stubSchtasks(t, nil)

	if err := Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	want := [][]string{
		{"/Query", "/TN", Label},
		{"<graceful stop>"},
		{"/End", "/TN", Label},
		{"/Run", "/TN", Label},
	}
	if !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

// /End fails when nothing is running, which is fine: /Run still starts it.
func TestRestartRunsTheTaskWhenNothingWasRunning(t *testing.T) {
	calls, _ := stubSchtasks(t, map[string]error{"/End": errors.New("no running instance")})
	if err := Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if last := (*calls)[len(*calls)-1]; !slices.Equal(last, []string{"/Run", "/TN", Label}) {
		t.Errorf("last call = %q, want /Run", last)
	}
}

func TestRestartWithNothingInstalled(t *testing.T) {
	if legacyServiceInstalled(context.Background()) {
		t.Skip("this host has a legacy vincent Windows Service")
	}
	calls, stops := stubSchtasks(t, map[string]error{"/Query": errors.New("cannot find the file")})
	if err := Restart(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Restart = %v, want ErrNotInstalled", err)
	}
	if len(*calls) != 1 || *stops != 0 {
		t.Errorf("calls = %q (stops %d), want only the /Query", *calls, *stops)
	}
}

func TestRestartReportsRunsFailure(t *testing.T) {
	boom := errors.New("ERROR: The task is disabled")
	stubSchtasks(t, map[string]error{"/Run": boom})
	if err := Restart(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Restart = %v, want schtasks' error", err)
	}
}
