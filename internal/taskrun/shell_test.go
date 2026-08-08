package taskrun

import (
	"io"
	"log/slog"
	"testing"
)

func testShells() *Shells {
	return NewShells(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestShellsReprobe: a config reload re-resolves the platform default, so a
// shell installed mid-flight is picked up without a daemon restart (§8.3).
func TestShellsReprobe(t *testing.T) {
	s := testShells()
	s.probeFn = func() (Shell, error) {
		return Shell{Name: "sh", Path: "/bin/sh", Args: []string{"-c"}}, nil
	}
	if got, err := s.Default(); err != nil || got.Name != "sh" {
		t.Fatalf("Default = %+v/%v, want the probed sh", got, err)
	}

	// The result is cached until a reprobe is asked for.
	s.probeFn = func() (Shell, error) {
		return Shell{Name: "pwsh", Path: "/usr/bin/pwsh", Args: []string{"-NoProfile", "-Command"}}, nil
	}
	if got, _ := s.Default(); got.Name != "sh" {
		t.Fatalf("Default re-probed without being asked: %+v", got)
	}

	s.Reprobe()
	if got, _ := s.Default(); got.Name != "pwsh" {
		t.Fatalf("Default after Reprobe = %+v, want pwsh", got)
	}
}

// TestShellsReprobeBeforeUseStaysLazy: a reload before any command step ran
// must not force a probe — first use probes and logs, as at startup.
func TestShellsReprobeBeforeUseStaysLazy(t *testing.T) {
	s := testShells()
	probes := 0
	s.probeFn = func() (Shell, error) {
		probes++
		return Shell{Name: "sh", Path: "/bin/sh", Args: []string{"-c"}}, nil
	}

	s.Reprobe()
	if probes != 0 {
		t.Fatalf("Reprobe before first use probed %d times, want 0", probes)
	}
	if _, err := s.Default(); err != nil {
		t.Fatalf("Default: %v", err)
	}
	if probes != 1 {
		t.Fatalf("probes after first use = %d, want 1", probes)
	}
}
