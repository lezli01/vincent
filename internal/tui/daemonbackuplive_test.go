package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/backupsched"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// TestDaemonViewRendersTheBackupRowFromTheRealDoctor is task 115's TUI live
// test: the backup row is composed by the daemon from config.yaml on disk and
// the timer's status, serialized by the real /v1/doctor handler, decoded by
// the real client and rendered by the daemon view. A failed last attempt has
// to reach the screen as its error, and a later success has to clear it —
// the same two facts `vincent doctor` turns into an exit code.
func TestDaemonViewRendersTheBackupRowFromTheRealDoctor(t *testing.T) {
	const token = "backup-live-token"
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	if err := os.WriteFile(filepath.Join(dirs.Config, config.FileName),
		[]byte("backup:\n  interval: 24h\n  keep: 3\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	st, err := store.Open(filepath.Join(dirs.Data, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	success := testNow.Add(-25 * time.Hour)
	attempt := testNow.Add(-time.Hour)
	// Guarded rather than a plain variable: the handler reads it on the
	// server's goroutine and the test rewrites it between fetches.
	var mu sync.Mutex
	status := backupsched.Status{
		Dir:           filepath.Join(dirs.Data, config.BackupDirName),
		LastSuccessAt: success,
		LastAttemptAt: attempt,
		LastError:     "write archive: disk full",
		NextDueAt:     testNow,
		LastBytes:     5 << 20,
		Retained:      3,
	}
	srv := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        dirs,
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		BackupStatus: func() backupsched.Status {
			mu.Lock()
			defer mu.Unlock()
			return status
		},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	d := newTestDaemonView(nil, nil)
	d.client = apiclient.New(ts.URL, token)
	fetch := func() string {
		t.Helper()
		msg, ok := d.doctorCmd()().(daemonDoctorMsg)
		if !ok {
			t.Fatal("doctorCmd did not produce a daemonDoctorMsg")
		}
		if msg.err != nil {
			t.Fatalf("GET /v1/doctor: %v", msg.err)
		}
		d.update(msg)
		return renderDaemon(d)
	}

	out := fetch()
	for _, want := range []string{
		"backups", "every 24h0m0s", "keep 3", filepath.Join(dirs.Data, config.BackupDirName),
		"last backup", success.Local().Format("2006-01-02 15:04"), "5.0MB", "3 kept",
		"next " + testNow.Local().Format("2006-01-02 15:04"),
		"last backup failed: write archive: disk full",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the daemon view does not show %q:\n%s", want, out)
		}
	}

	// The next attempt succeeded: the error is gone from the row, not left
	// behind by the last-good report.
	mu.Lock()
	status.LastError = ""
	status.LastSuccessAt = testNow
	status.NextDueAt = testNow.Add(24 * time.Hour)
	mu.Unlock()
	out = fetch()
	if strings.Contains(out, "disk full") || strings.Contains(out, "last backup failed") {
		t.Errorf("a succeeding backup still renders the old failure:\n%s", out)
	}
	line := lineWith(out, "last backup")
	if !strings.Contains(line, testNow.Local().Format("2006-01-02 15:04")) ||
		strings.Contains(line, success.Local().Format("2006-01-02 15:04")) {
		t.Errorf("the row did not adopt the new last success: %q", line)
	}
}

// lineWith is the first rendered line containing sub, or "".
func lineWith(out, sub string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}
