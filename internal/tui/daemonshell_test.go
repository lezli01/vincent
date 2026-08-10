package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// disconnectedShell is a root that never reached a daemon, with the daemon
// view holding a readable log — the state the gate exception exists for.
func disconnectedShell(t *testing.T, logLines []string) *root {
	t.Helper()
	dir := ackedDir(t)
	m := newRoot(testCtx(t), fakeConnector(), dir)
	m.width, m.height = 120, 40
	m.Update(connectFailedMsg{
		err:     errors.New("connection refused"),
		logPath: filepath.Join(dir, "logs", "daemon.log"),
	})
	d := m.views[viewDaemon].(*daemonView)
	d.tail = func(string, int) ([]string, error) { return logLines, nil }
	d.update(daemonLogMsg{lines: logLines})
	return m
}

// The gate exception: the daemon view renders while the daemon is down,
// because its log comes off the filesystem and that is when it is worth
// reading. Views 1-5 have nothing without the daemon and stay gated.
func TestDaemonViewIsReachableWhileDisconnected(t *testing.T) {
	m := disconnectedShell(t, []string{"level=ERROR msg=\"bind: address in use\""})

	// The §15 route: `:` still reaches the daemon view while disconnected.
	m.Update(key(":"))
	for _, r := range "daemon" {
		m.Update(key(string(r)))
	}
	m.Update(namedKey("enter"))
	if m.active != viewDaemon {
		t.Fatalf("active = %v, want the daemon view via the palette", m.active)
	}
	out := content(m)
	if !strings.Contains(out, "bind: address in use") {
		t.Errorf("the log is not readable on the disconnected screen:\n%s", out)
	}
	if strings.Contains(out, "press r to retry") {
		t.Error("the failure screen is still covering the daemon view")
	}

	for _, id := range []viewID{viewHome, viewNewTask, viewProjects, viewWorkflows} {
		m.Update(selectViewMsg{id: id})
		if body := content(m); !strings.Contains(body, "daemon unreachable") {
			t.Errorf("view %v rendered without a daemon:\n%s", id, body)
		}
	}
}

// And the failure screen has to say so, or the exception is undiscoverable.
func TestFailureScreenPointsAtTheDaemonView(t *testing.T) {
	m := disconnectedShell(t, nil)
	if out := content(m); !strings.Contains(out, ": for the daemon view") {
		t.Errorf("the failure screen does not point at the palette route:\n%s", out)
	}
}

// §15's exit line. It is only worth printing when something is actually
// running, and only when the board knows.
func TestQuitReminderCountsRunningTasks(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	b := m.views[viewHome].(*shell).board

	t.Run("board never loaded", func(t *testing.T) {
		b.loaded = false
		if line, ok := m.quitReminder(); ok {
			t.Errorf("reminder = %q, want none from a board that never loaded", line)
		}
	})

	t.Run("nothing running", func(t *testing.T) {
		b.loaded = true
		b.tasks = []apiclient.Task{task(1, stateQueued), task(2, stateDone)}
		if line, ok := m.quitReminder(); ok {
			t.Errorf("reminder = %q, want none at zero running", line)
		}
	})

	t.Run("one running", func(t *testing.T) {
		b.loaded = true
		b.tasks = []apiclient.Task{task(1, stateRunning), task(2, stateQueued)}
		line, ok := m.quitReminder()
		if !ok {
			t.Fatal("no reminder with a task running")
		}
		if !strings.Contains(line, "1 task is still running") {
			t.Errorf("reminder = %q, want the singular", line)
		}
		if !strings.Contains(line, "daemon keeps working") {
			t.Errorf("reminder = %q, want it to say the work continues", line)
		}
	})

	t.Run("several running", func(t *testing.T) {
		b.loaded = true
		b.tasks = []apiclient.Task{
			task(1, stateRunning), task(2, stateRunning), task(3, stateAwaitingInput),
		}
		line, ok := m.quitReminder()
		if !ok {
			t.Fatal("no reminder with tasks running")
		}
		// awaiting_input is waiting on the person who is leaving, and the
		// bell already covered it; only running work is reported here.
		if !strings.Contains(line, "2 tasks are still running") {
			t.Errorf("reminder = %q, want 2", line)
		}
	})
}

// The shell hands the daemon view the one resolved data dir rather than
// letting it resolve its own, so the notice and the log agree.
func TestShellHandsTheDataDirToTheDaemonView(t *testing.T) {
	dir := ackedDir(t)
	m := newRoot(testCtx(t), fakeConnector(), dir)
	if got := m.views[viewDaemon].(*daemonView).dataDir; got != dir {
		t.Errorf("daemon view dataDir = %q, want %q", got, dir)
	}
}
