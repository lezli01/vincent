package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerRotates is the T4.3 log-rotation leg. Rotation has shipped since
// phase 1 and was never asserted, which is a fragile place to be: lumberjack
// rotates on write, so a wiring mistake — a plain os.File, a MaxSize of zero,
// a logger built before the directory exists — produces one file that grows
// forever and nothing complains until a disk does.
//
// It drives the daemon's real newLogger rather than a lumberjack.Logger built
// for the test, because the wiring is the part that can break.
func TestLoggerRotates(t *testing.T) {
	logsDir := t.TempDir()
	log, _, closeLog := newLogger(logsDir, false)
	t.Cleanup(func() { _ = closeLog() })

	// MaxSize is 10 MB; write comfortably past it. Each record is ~1 KB, so
	// this is ~12k writes — fast, and it exercises the real handler chain.
	payload := strings.Repeat("x", 1024)
	for range 12_000 {
		log.Info("rotation test", "payload", payload)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("read logs dir: %v", err)
	}
	var current, backups int
	var currentSize int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if e.Name() == "daemon.log" {
			current++
			currentSize = info.Size()
			continue
		}
		if strings.HasPrefix(e.Name(), "daemon-") && strings.HasSuffix(e.Name(), ".log") {
			backups++
		}
	}
	if current != 1 {
		t.Errorf("found %d daemon.log files, want exactly 1", current)
	}
	if backups == 0 {
		t.Errorf("no rotated backups in %v; the log grew unbounded", names(entries))
	}
	// The live file must be back under the cap after rotating, which is the
	// property that actually bounds disk use.
	if currentSize > 11<<20 {
		t.Errorf("daemon.log is %d bytes after rotation, want under the 10MB cap", currentSize)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, filepath.Base(e.Name()))
	}
	return out
}
