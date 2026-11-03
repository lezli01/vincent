//go:build unix

package main

import (
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/jiratest"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// TestSignalsStopCleanly: on POSIX, SIGTERM and os.Interrupt stop the server
// the way the shutdown route does. Windows has no graceful signal for a
// console-less child, so this leg is POSIX-only (decision 1).
func TestSignalsStopCleanly(t *testing.T) {
	for name, sig := range map[string]os.Signal{"SIGTERM": syscall.SIGTERM, "Interrupt": os.Interrupt} {
		t.Run(name, func(t *testing.T) {
			srv := jiratest.StartFakeJira(t, jiratest.Options{})
			if got := get(t, srv, "/rest/api/3/serverInfo"); got != http.StatusOK {
				t.Fatalf("serverInfo: %d", got)
			}
			if err := srv.Cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if !srv.WaitExit(wait.Timeout(10 * time.Second)) {
				t.Fatalf("fakejira did not exit on %s", name)
			}
			if err := srv.ExitErr(); err != nil {
				t.Fatalf("exit on %s: %v\n%s", name, err, srv.Stderr)
			}
		})
	}
}
