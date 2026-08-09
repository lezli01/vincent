package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/daemon"
)

// key builds a printable-key press ("q", "?", "1", "r").
func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// content renders the model to its plain frame string.
func content(m *root) string { return m.View().Content }

// runCmd executes one tea.Cmd with a deadline, returning its message.
func runCmd(t *testing.T, cmd tea.Cmd, timeout time.Duration) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(timeout):
		t.Fatal("command did not return in time")
		return nil
	}
}

// pump drives the model the way the Bubble Tea runtime does: it runs every
// queued command, flattens tea.BatchMsg, feeds each resulting message into
// Update and queues whatever comes back.
//
// The shell returns batches now that connection lifecycle and stream notes
// fan out to every view, so a test running one command at a time would stop
// at the first fork. The queue lives across calls to until() because a test
// often has to reach one state, act on the world, then keep pumping — and
// dropping the pending commands in between would lose the SSE subscription.
type pump struct {
	t    *testing.T
	m    *root
	msgs chan tea.Msg
	done chan struct{}
}

func newPump(t *testing.T, m *root, cmd tea.Cmd) *pump {
	t.Helper()
	p := &pump{
		t:    t,
		m:    m,
		msgs: make(chan tea.Msg, 256),
		done: make(chan struct{}),
	}
	t.Cleanup(func() { close(p.done) })
	p.push(cmd)
	return p
}

// push runs a command the way the runtime does: on its own goroutine, with
// its message delivered asynchronously. Commands must not be serialized —
// a subscription command blocks until its next note arrives, and running one
// at a time would starve every command queued behind it.
func (p *pump) push(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if msg == nil {
			return
		}
		select {
		case p.msgs <- msg:
		case <-p.done:
		}
	}()
}

// until pumps until cond holds, failing the test on timeout.
func (p *pump) until(timeout time.Duration, what string, cond func() bool) {
	p.t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case msg := <-p.msgs:
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					p.push(c)
				}
				continue
			}
			_, cmd := p.m.Update(msg)
			p.push(cmd)
		case <-deadline:
			p.t.Fatalf("timed out waiting for %s; view: %q", what, content(p.m))
		}
	}
}

// testCtx is a context canceled at test end so stream goroutines die with
// the test.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// fakeConnector simulates the §12.1 flow: no daemon until startDetached,
// healthy daemon after.
func fakeConnector() connector {
	started := false
	return connector{
		resolveDataDir: func() (string, error) { return "/data", nil },
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			if !started {
				return daemon.RuntimeInfo{}, errors.New("no daemon.json")
			}
			return daemon.RuntimeInfo{PID: 42, Port: 9}, nil
		},
		checkHealth: func(context.Context, int) (daemon.HealthInfo, error) {
			return daemon.HealthInfo{Status: "ok", Version: "test-v"}, nil
		},
		newClient: func(string) (*apiclient.Client, error) {
			return apiclient.New("http://127.0.0.1:9", "t"), nil
		},
		startDetached: func() (int, error) { started = true; return 42, nil },
		startTimeout:  time.Second,
		pollInterval:  time.Millisecond,
	}
}

// TestAutoStartFlow drives probe-miss → auto-start → connected through the
// root state machine (T3.1: auto-starts a stopped daemon).
func TestAutoStartFlow(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector())

	msg := runCmd(t, m.Init(), 5*time.Second)
	if _, ok := msg.(probeFailedMsg); !ok {
		t.Fatalf("probe with no daemon = %T, want probeFailedMsg", msg)
	}
	_, cmd := m.Update(msg)
	if m.phase != phaseStarting {
		t.Fatalf("phase = %v, want phaseStarting", m.phase)
	}
	if !strings.Contains(content(m), "starting daemon") {
		t.Errorf("starting view lacks 'starting daemon': %q", content(m))
	}

	msg = runCmd(t, cmd, 5*time.Second)
	conn, ok := msg.(connectedMsg)
	if !ok {
		t.Fatalf("start result = %T, want connectedMsg", msg)
	}
	if !conn.autoStarted {
		t.Error("connectedMsg.autoStarted = false, want true")
	}
	if _, cmd = m.Update(msg); cmd == nil {
		t.Error("connected update returned no stream command")
	}
	if m.phase != phaseConnected {
		t.Fatalf("phase = %v, want phaseConnected", m.phase)
	}
	got := content(m)
	if !strings.Contains(got, "connected") || !strings.Contains(got, "test-v") {
		t.Errorf("connected view lacks badge/version: %q", got)
	}
}

func TestConnectFailureAndRetry(t *testing.T) {
	cn := fakeConnector()
	cn.startDetached = func() (int, error) { return 0, errors.New("spawn refused") }
	m := newRoot(testCtx(t), cn)

	msg := runCmd(t, m.Init(), 5*time.Second)
	_, cmd := m.Update(msg) // probeFailedMsg → starting
	msg = runCmd(t, cmd, 5*time.Second)
	if _, ok := msg.(connectFailedMsg); !ok {
		t.Fatalf("start result = %T, want connectFailedMsg", msg)
	}
	m.Update(msg)
	if m.phase != phaseFailed {
		t.Fatalf("phase = %v, want phaseFailed", m.phase)
	}
	got := content(m)
	for _, want := range []string{"spawn refused", "daemon.log", "r to retry"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure view lacks %q: %q", want, got)
		}
	}

	// r reruns the whole connect flow.
	_, cmd = m.Update(key("r"))
	if m.phase != phaseProbing || cmd == nil {
		t.Fatalf("retry: phase = %v, cmd nil = %v; want probing with a command", m.phase, cmd == nil)
	}
}

func TestViewRoutingAndHelp(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector())
	m.phase = phaseConnected

	m.Update(key("2"))
	if m.active != viewDetail {
		t.Fatalf("active = %v, want viewDetail", m.active)
	}
	if !strings.Contains(content(m), "Task detail") {
		t.Errorf("view 2 lacks 'Task detail': %q", content(m))
	}

	m.Update(key("?"))
	help := content(m)
	if !strings.Contains(help, "Global keys") {
		t.Errorf("help overlay missing after ?: %q", help)
	}
	// The board's keys are documented as they land (T3.2).
	for _, k := range []string{"open the selected task", "filter by id"} {
		if !strings.Contains(help, k) {
			t.Errorf("help overlay lacks %q: %q", k, help)
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(content(m), "toggle this help") {
		t.Errorf("help overlay still visible after esc: %q", content(m))
	}

	m.Update(key("6"))
	if m.active != viewDaemon {
		t.Fatalf("active = %v, want viewDaemon", m.active)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		key("q"),
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		m := newRoot(testCtx(t), fakeConnector())
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%q returned no command", k.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%q command is not tea.Quit", k.String())
		}
	}
}

// TestLiveEventLine renders a delivered event on the shell's event line —
// the foundation's visible re-render on external change.
func TestLiveEventLine(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector())
	m.phase = phaseConnected
	m.notes = make(chan apiclient.Note)

	if !strings.Contains(content(m), "no events yet") {
		t.Errorf("initial view lacks 'no events yet': %q", content(m))
	}

	taskID := int64(3)
	m.updateNote(apiclient.EventNote{Event: apiclient.Event{
		ID: 7, Type: "task.state_changed", TaskID: &taskID, TS: time.Now(),
	}})
	got := content(m)
	if !strings.Contains(got, "task.state_changed #7") || !strings.Contains(got, "task 3") {
		t.Errorf("event line missing details: %q", got)
	}
}

func TestReconnectingState(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector())
	m.phase = phaseConnected
	m.notes = make(chan apiclient.Note)

	m.updateNote(apiclient.DisconnectedNote{Err: errors.New("conn reset"), RetryIn: time.Second})
	if m.phase != phaseReconnecting {
		t.Fatalf("phase = %v, want phaseReconnecting", m.phase)
	}
	got := content(m)
	if !strings.Contains(got, "connection lost") || !strings.Contains(got, "conn reset") {
		t.Errorf("reconnect view lacks error: %q", got)
	}

	m.updateNote(apiclient.ConnectedNote{})
	if m.phase != phaseConnected {
		t.Fatalf("phase after reconnect = %v, want phaseConnected", m.phase)
	}
}
