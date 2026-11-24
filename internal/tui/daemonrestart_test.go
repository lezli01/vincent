package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var requestLog = []string{
	`level=INFO msg=started`,
	`level=INFO msg="http request" method=GET path=/v1/tasks status=200 duration_ms=1`,
	`level=INFO msg="http request" method=POST path=/v1/tasks/4/retry status=409 duration_ms=2`,
	`level=WARN msg="step failed" task=4`,
}

func requestDaemon() *daemonView {
	d := newTestDaemonView(nil, nil)
	loadedDaemon(d)
	d.update(daemonLogMsg{lines: requestLog})
	return d
}

func init() {
	panelKeyProbes[ctxDaemon]["h"] = func(t *testing.T) {
		d := requestDaemon()
		d.update(registryKey(t, "h"))
		if out := renderDaemon(d); !strings.Contains(out, "path=/v1/tasks status=200") {
			t.Fatalf("h did not show the request lines:\n%s", out)
		}
	}
	panelKeyProbes[ctxDaemon]["/"] = func(t *testing.T) {
		d := requestDaemon()
		d.update(registryKey(t, "/"))
		for _, r := range "step" {
			d.update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		out := renderDaemon(d)
		if !strings.Contains(out, "step failed") || strings.Contains(out, "msg=started") {
			t.Fatalf("the search did not narrow the log to \"step\":\n%s", out)
		}
	}
	panelKeyProbes[ctxDaemon]["r"] = func(t *testing.T) {
		d := requestDaemon()
		d.update(registryKey(t, "r"))
		if !strings.Contains(renderDaemon(d), "restart the daemon?") {
			t.Fatal("r did not ask before restarting")
		}
		_, cmd := d.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
		if _, ok := drain(cmd).(restartDaemonMsg); !ok {
			t.Fatalf("y produced %#v, want a restart", drain(cmd))
		}
	}
}

// #1081: successful requests are hidden and counted; a failed one stays.
func TestDaemonLogHidesTheTUIsOwnRequests(t *testing.T) {
	out := renderDaemon(requestDaemon())
	if strings.Contains(out, "status=200") {
		t.Errorf("a successful request line is shown:\n%s", out)
	}
	if !strings.Contains(out, "status=409") {
		t.Errorf("a failed request line is hidden:\n%s", out)
	}
	if !strings.Contains(out, "1 request lines hidden · h shows them") {
		t.Errorf("the pane does not say what it hides:\n%s", out)
	}
}

// #1081: anything but y keeps the daemon; a stop that fails says so.
func TestDaemonRestartAsksAndReportsAFailure(t *testing.T) {
	d := requestDaemon()
	d.update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if _, cmd := d.update(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil {
		t.Fatal("n restarted the daemon")
	}
	if d.restartAsk {
		t.Fatal("the question stayed open after n")
	}
	d.update(daemonStoppedMsg{err: errors.New("connection refused")})
	if out := renderDaemon(d); !strings.Contains(out, "could not restart: connection refused") {
		t.Errorf("a failed stop is not reported:\n%s", out)
	}
}

// Review finding 5: the question and a failed restart are drawn over an
// empty or unread log too, and the question keeps its key at 80 columns.
func TestDaemonRestartIsVisibleOverAnEmptyLog(t *testing.T) {
	for name, log := range map[string]tea.Msg{
		"empty":      daemonLogMsg{lines: nil},
		"unread":     nil,
		"unreadable": daemonLogMsg{err: errors.New("permission denied")},
	} {
		d := newTestDaemonView(nil, nil)
		loadedDaemon(d)
		if log != nil {
			d.update(log)
		}
		d.update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		if out := renderDaemon(d); !strings.Contains(out, "restart the daemon? y restart") {
			t.Errorf("%s log: the question is not on screen:\n%s", name, out)
		}
		d.update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		d.update(daemonStoppedMsg{err: errors.New("connection refused")})
		if out := renderDaemon(d); !strings.Contains(out, "could not restart") {
			t.Errorf("%s log: a failed restart is not reported:\n%s", name, out)
		}
	}
}

// #1081: a stopped daemon reruns the connect flow, which starts a new one;
// a failed stop leaves the connection alone.
func TestRootRestartReconnectsOnceTheDaemonStopped(t *testing.T) {
	m := connectedRoot(t)
	m.Update(daemonStoppedMsg{err: errors.New("refused")})
	if m.phase != phaseConnected {
		t.Fatalf("a failed stop changed the phase to %v", m.phase)
	}
	m.Update(daemonStoppedMsg{})
	if m.phase != phaseProbing {
		t.Fatalf("phase = %v after the stop, want probing for a new daemon", m.phase)
	}
}
