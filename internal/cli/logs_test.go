package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

// `vincent daemon logs` is the one data command that needs no daemon: it
// reads the log off disk, which is the point rather than a shortcut, so
// everything here runs against a data dir and nothing else.

// followPoll paces the follow loop in tests. The command polls every two
// seconds, which is right for a human and pointless to wait for here.
const followPoll = 20 * time.Millisecond

// runCLI executes the real command tree in-process, returning what each
// stream got and the exit code Execute would have returned.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	if err != nil {
		// Execute prints a plain error itself; a command returning exitError
		// has already written its own report.
		var ee exitError
		if !errors.As(err, &ee) {
			errOut.WriteString("Error: " + err.Error() + "\n")
		}
	}
	return out.String(), errOut.String(), asExitCode(err)
}

// seedLog writes a data dir holding a daemon log with the given lines, and
// points the CLI's directory resolution at it.
func seedLog(t *testing.T, lines ...string) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	path := daemon.LogPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	var body string
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

func appendLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append log: %v", err)
	}
}

func TestDaemonLogsPrintsTheTail(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, "line "+string(rune('0'+i)))
	}
	seedLog(t, lines...)

	out, _, code := runCLI(t, "daemon", "logs")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got := strings.Join(lines, "\n") + "\n"; out != got {
		t.Errorf("output = %q, want the whole log %q", out, got)
	}

	out, _, code = runCLI(t, "daemon", "logs", "-n", "3")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := strings.Join(lines[7:], "\n") + "\n"; out != want {
		t.Errorf("-n 3 output = %q, want %q", out, want)
	}
}

// A log that is not there and a log with nothing in it are different facts,
// and the command reports them differently: the first names the path and
// fails, the second succeeds silently.
func TestDaemonLogsMissingAndEmpty(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	out, errOut, code := runCLI(t, "daemon", "logs")
	if code != 1 {
		t.Errorf("exit on a missing log = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, daemon.LogPath(dataDir)) {
		t.Errorf("error %q does not name the log path %q", errOut, daemon.LogPath(dataDir))
	}

	seedLog(t)
	out, _, code = runCLI(t, "daemon", "logs")
	if code != 0 || out != "" {
		t.Errorf("empty log: exit %d, stdout %q; want 0 and nothing", code, out)
	}
}

// Following prints what was appended and only what was appended: the window
// it opened on is never printed a second time, and no line arrives twice.
func TestDaemonLogsFollowPrintsOnlyNewLines(t *testing.T) {
	path := seedLog(t, "old one", "old two")
	var buf lockedBuffer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- followLog(ctx, &buf, path, "old two", followPoll) }()

	appendLog(t, path, "fresh one")
	waitFor(t, func() bool { return strings.Contains(buf.String(), "fresh one") },
		"the first appended line")
	appendLog(t, path, "fresh two")
	waitFor(t, func() bool { return strings.Contains(buf.String(), "fresh two") },
		"the second appended line")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("followLog: %v", err)
	}
	if got := buf.String(); got != "fresh one\nfresh two\n" {
		t.Errorf("followed output = %q, want the two appended lines exactly once each", got)
	}
}

// Rotation is what the open-read-close contract exists for: lumberjack
// rotates by renaming the live file, and on Windows renaming a file another
// process holds open fails. A follower must survive it — including the moment
// where no daemon.log exists at all — and pick the new file up.
func TestDaemonLogsFollowSurvivesRotation(t *testing.T) {
	path := seedLog(t, "before rotation")
	var buf lockedBuffer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- followLog(ctx, &buf, path, "before rotation", followPoll) }()

	appendLog(t, path, "still the old file")
	waitFor(t, func() bool { return strings.Contains(buf.String(), "still the old file") },
		"a line from the pre-rotation file")

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rotate (a held handle is exactly what this proves absent): %v", err)
	}
	// Give the follower a poll or two with no log there at all: that gap is
	// waited out, not reported as the missing-file error a first read is.
	time.Sleep(3 * followPoll)
	if err := os.WriteFile(path, []byte("after rotation\n"), 0o600); err != nil {
		t.Fatalf("write rotated log: %v", err)
	}
	waitFor(t, func() bool { return strings.Contains(buf.String(), "after rotation") },
		"a line from the fresh file")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("followLog did not survive rotation: %v", err)
	}
	if strings.Count(buf.String(), "after rotation") != 1 {
		t.Errorf("output = %q, want the fresh file's line exactly once", buf.String())
	}
}

// waitFor polls a condition rather than sleeping for a fixed span, so a slow
// CI runner costs nothing when the condition is already true.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(followPoll / 2)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lockedBuffer is a buffer a follow goroutine writes while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
