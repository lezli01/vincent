package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/service"
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
		_, cmd := d.update(registryKey(t, "r"))
		d.update(drain(cmd)) // the service reading
		if !strings.Contains(renderDaemon(d), "restart the daemon?") {
			t.Fatal("r did not ask before restarting")
		}
		_, cmd = d.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
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
	d.update(restartEndedMsg{err: errors.New("connection refused")})
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
		pressRestart(d)
		if out := renderDaemon(d); !strings.Contains(out, "restart the daemon? y restart") {
			t.Errorf("%s log: the question is not on screen:\n%s", name, out)
		}
		d.update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		d.update(restartEndedMsg{err: errors.New("connection refused")})
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

// pressRestart opens the restart question and lands the service reading the
// way the runtime would.
func pressRestart(d *daemonView) {
	_, cmd := d.update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	d.update(drain(cmd))
}

func serviceDaemon(st service.Status, err error) *daemonView {
	d := requestDaemon()
	d.serviceQuery = func(context.Context) (service.Status, error) { return st, err }
	return d
}

// #1160: the question says which restart it is asking about, and y asks the
// root for that one. A running service restarts through its manager; no
// service, a stopped one, or an unreadable one restarts as before.
func TestDaemonRestartSaysWhichPathItTakes(t *testing.T) {
	running := service.Status{Installed: true, Running: true, Name: "dev.lezli01.vincent"}
	for name, tc := range map[string]struct {
		st      service.Status
		err     error
		via     bool
		wording string
	}{
		"service running": {running, nil, true, "restart the daemon through its service (dev.lezli01.vincent)? y restart"},
		"no service":      {service.Status{}, nil, false, "restart the daemon? y restart"},
		"service stopped": {service.Status{Installed: true, Name: "x"}, nil, false, "restart the daemon? y restart"},
		"unsupported":     {service.Status{}, service.ErrUnsupported, false, "restart the daemon? y restart"},
	} {
		d := serviceDaemon(tc.st, tc.err)
		pressRestart(d)
		if out := renderDaemon(d); !strings.Contains(out, tc.wording) {
			t.Errorf("%s: the question does not read %q:\n%s", name, tc.wording, out)
		}
		_, cmd := d.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
		msg, ok := drain(cmd).(restartDaemonMsg)
		if !ok || msg.viaService != tc.via {
			t.Errorf("%s: y produced %#v, want restartDaemonMsg{viaService: %v}", name, drain(cmd), tc.via)
		}
	}
}

// #1160: until the reading lands the question cannot say which restart it
// is, so y waits rather than guessing.
func TestDaemonRestartWaitsForTheServiceReading(t *testing.T) {
	d := requestDaemon()
	d.update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if out := renderDaemon(d); !strings.Contains(out, "checking whether a service runs it") {
		t.Errorf("the question does not say it is checking:\n%s", out)
	}
	if _, cmd := d.update(tea.KeyPressMsg{Code: 'y', Text: "y"}); cmd != nil {
		t.Fatalf("y restarted before the service reading: %#v", drain(cmd))
	}
	if !d.restartAsk {
		t.Fatal("y closed the question before the reading")
	}
}

// #1160: `r y r y` restarted twice. A restart under way ignores `r` and says
// so, until the root reports it ended.
func TestDaemonSecondRestartIsIgnored(t *testing.T) {
	d := requestDaemon()
	pressRestart(d)
	d.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if _, cmd := d.update(tea.KeyPressMsg{Code: 'r', Text: "r"}); cmd != nil {
		t.Fatalf("a second r did something: %#v", drain(cmd))
	}
	if d.restartAsk {
		t.Fatal("a second r opened the question")
	}
	if out := renderDaemon(d); !strings.Contains(out, "already under way, so r was ignored") {
		t.Errorf("the ignored r is not noted:\n%s", out)
	}
	if _, cmd := d.update(tea.KeyPressMsg{Code: 'y', Text: "y"}); cmd != nil {
		t.Fatalf("the second y restarted again: %#v", drain(cmd))
	}

	d.update(restartEndedMsg{})
	pressRestart(d)
	if !d.restartAsk {
		t.Fatal("r after the restart ended does not ask")
	}
}

// #1160: the root holds one restart at a time, on either path, and ends it on
// the reconnect or the failure.
func TestRootIgnoresASecondRestart(t *testing.T) {
	for _, via := range []bool{false, true} {
		m := connectedRoot(t)
		if _, cmd := m.Update(restartDaemonMsg{viaService: via}); cmd == nil {
			t.Fatalf("via service %v: the first restart did nothing", via)
		}
		if _, cmd := m.Update(restartDaemonMsg{viaService: via}); cmd != nil {
			t.Fatalf("via service %v: a second restart was started", via)
		}
		m.Update(connectFailedMsg{err: errors.New("gone")})
		if m.restarting {
			t.Fatalf("via service %v: a failed reconnect did not end the restart", via)
		}
		if _, cmd := m.Update(restartDaemonMsg{viaService: via}); cmd == nil {
			t.Fatalf("via service %v: a restart after one ended did nothing", via)
		}
	}
}

// #1160: through the service, the root never stops the daemon itself — it
// asks the service manager.
func TestRootRestartsThroughTheService(t *testing.T) {
	m := connectedRoot(t)
	called := 0
	m.cn.restartService = func(context.Context) error { called++; return errors.New("refused") }
	m.cn.serviceTimeout = time.Second
	_, cmd := m.Update(restartDaemonMsg{viaService: true})
	msg, ok := drain(cmd).(daemonStoppedMsg)
	if !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "refused") {
		t.Fatalf("the restart produced %#v, want the service's failure", msg)
	}
	if called != 1 {
		t.Errorf("service restart called %d times, want once", called)
	}
	m.Update(msg)
	if m.restarting || m.phase != phaseConnected {
		t.Errorf("a failed service restart: restarting %v, phase %v", m.restarting, m.phase)
	}
	if d := m.views[viewDaemon].(*daemonView); d.restartErr == nil {
		t.Error("the daemon screen was not told the restart failed")
	}
}

// #1160: the service path waits for a daemon with a new PID to answer before
// reconnecting, so the connect flow never auto-starts one beside the
// service's; and it gives up, bounded, with an error that says so.
func TestServiceRestartWaitsForTheNewDaemon(t *testing.T) {
	reads := 0
	cn := connector{
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			reads++
			if reads < 4 { // the old daemon.json, still there for a while
				return daemon.RuntimeInfo{PID: 42, Port: 9}, nil
			}
			return daemon.RuntimeInfo{PID: 43, Port: 10}, nil
		},
		checkHealth: func(_ context.Context, port int) (daemon.HealthInfo, error) {
			if port != 10 {
				return daemon.HealthInfo{}, errors.New("not the new one")
			}
			return daemon.HealthInfo{Status: "ok"}, nil
		},
		startDetached:  func() (int, error) { t.Fatal("the service path auto-started a daemon"); return 0, nil },
		restartService: func(context.Context) error { return nil },
		serviceTimeout: 5 * time.Second,
		pollInterval:   time.Millisecond,
	}
	if msg := cn.serviceRestartCmd("/data")().(daemonStoppedMsg); msg.err != nil {
		t.Fatalf("restart: %v", msg.err)
	}
	if reads < 4 {
		t.Errorf("returned after %d reads, before the new daemon was up", reads)
	}

	cn.readRuntime = func(string) (daemon.RuntimeInfo, error) { return daemon.RuntimeInfo{PID: 42, Port: 9}, nil }
	cn.serviceTimeout = 50 * time.Millisecond
	msg := cn.serviceRestartCmd("/data")().(daemonStoppedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "did not bring the daemon back") {
		t.Fatalf("a daemon that never came back: %v", msg.err)
	}
}
