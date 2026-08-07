package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchReloadsValidAndDropsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("max_parallel_tasks: 3\n")

	reloads := make(chan Config, 16)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Watch(t.Context(), log, dir, func(c Config) { reloads <- c }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	next := func() Config {
		t.Helper()
		select {
		case c := <-reloads:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for a config reload")
			return Config{}
		}
	}

	// A valid edit is delivered.
	write("max_parallel_tasks: 7\n")
	if got := next(); got.MaxParallelTasks != 7 {
		t.Fatalf("reloaded MaxParallelTasks = %d, want 7", got.MaxParallelTasks)
	}

	// An invalid edit is dropped (no callback); the next valid edit is
	// delivered. The sleep lets the invalid write's debounce window fire
	// before the valid write happens, so the two cannot coalesce.
	write("max_parallel_tasks: 0\n")
	time.Sleep(4 * debounce)
	write("max_parallel_tasks: 9\n")
	got := next()
	if got.MaxParallelTasks == 0 {
		t.Fatal("watcher delivered an invalid config")
	}
	if got.MaxParallelTasks != 9 {
		t.Fatalf("reloaded MaxParallelTasks = %d, want 9", got.MaxParallelTasks)
	}
}
