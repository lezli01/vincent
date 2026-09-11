package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
	if err := Watch(t.Context(), log, dir, new(sync.Mutex), func(c Config) { reloads <- c }); err != nil {
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

// announcingLocker reports each Lock call before it blocks, so a test can
// hold the mutex and know a reload is waiting on it.
type announcingLocker struct {
	sync.Mutex
	asked chan struct{}
}

func (l *announcingLocker) Lock() {
	select {
	case l.asked <- struct{}{}:
	default:
	}
	l.Mutex.Lock()
}

// A reload reads config.yaml only once it holds the lock. A writer that
// changes the file and applies it under that lock — PATCH /v1/config — is
// otherwise overtaken by a fire that read the bytes it replaced: m11 saw the
// patch answer 200 and the next GET read the old log_level on macOS.
func TestWatchReadsTheFileUnderTheLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("max_parallel_tasks: 3\n")

	mu := &announcingLocker{asked: make(chan struct{}, 1)}
	reloads := make(chan Config, 16)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Watch(t.Context(), log, dir, mu, func(c Config) { reloads <- c }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// The writer's critical section: an edit fires a reload, which must wait
	// for the lock; a second edit lands while it waits; the lock is released.
	mu.Mutex.Lock()
	write("max_parallel_tasks: 5\n")
	select {
	case <-mu.asked:
	case <-time.After(10 * time.Second):
		mu.Unlock()
		t.Fatal("timed out waiting for a reload to ask for the lock")
	}
	write("max_parallel_tasks: 6\n")
	mu.Unlock()

	select {
	case got := <-reloads:
		if got.MaxParallelTasks != 6 {
			t.Fatalf("first reload MaxParallelTasks = %d, want 6: the reload read the file before it held the lock", got.MaxParallelTasks)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a config reload")
	}
}
