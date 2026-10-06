package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/lezli01/vincent/internal/daemon"
)

// TestDaemonStopWaitsForTheProcessToExit holds `vincent daemon stop` to what
// spec §13.2's route table says it does with POST /v1/daemon/stop: it "calls
// this and waits for exit" — the daemon's *process*, not merely its lock
// (issue #732).
//
// The two come apart: Run releases daemon.lock in a defer and the process
// lives on past it — the log closes, Run returns, the runtime tears down. On
// Windows a handle the dying process still holds keeps its files undeletable,
// so a caller that removes the data dir the moment stop returns — every e2e
// cleanup in this package, `t.TempDir`'s RemoveAll — fails with "being used
// by another process".
//
// The window is microseconds on a real daemon, so the daemon here is a
// stand-in that widens it to a second: the test holds the lock itself, a
// long-lived child supplies the PID daemon.json names, and the stop route
// releases the lock at once but kills the child only a second later. A stop
// that waits for exit returns after the child is gone; one that waits for the
// lock returns while it is still running.
func TestDaemonStopWaitsForTheProcessToExit(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()

	// The "daemon process": git blocks reading a batch request from stdin,
	// which is never written, until it is killed. git is already a hard
	// requirement of this suite, and it runs the same on every platform.
	child := exec.Command("git", "cat-file", "--batch")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := child.Start(); err != nil {
		t.Skipf("cannot start a stand-in process: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = stdin.Close()
		<-exited
	})

	lock := flock.New(daemon.LockPath(dataDir))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("take the daemon lock: locked=%v err=%v", locked, err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })

	const token = "stop-test-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/daemon/stop" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// What Run's deferred Unlock does, ahead of the process exiting.
		_ = lock.Unlock()
		go func() {
			time.Sleep(time.Second)
			_ = child.Process.Kill()
		}()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("server address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("server port: %v", err)
	}

	if err := os.WriteFile(daemon.TokenPath(dataDir), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		PID: child.Process.Pid, Port: port, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}

	out, code := runVincent(t, dataDir, cfgDir, "daemon", "stop")
	if code != 0 || !strings.Contains(out, "daemon stopped") {
		t.Fatalf("stop: code %d, out %q; want 0, 'daemon stopped'", code, out)
	}
	// The grace is only for this test's own Wait goroutine to observe an exit
	// that stop already saw; it is far short of the second the child outlives
	// the lock by.
	select {
	case <-exited:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("daemon stop returned %q while pid %d was still running: it waited for the lock, not for exit",
			strings.TrimSpace(out), child.Process.Pid)
	}
}
