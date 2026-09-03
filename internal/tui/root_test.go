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
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))

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
	m := newRoot(testCtx(t), cn, ackedDir(t))

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
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	// The shell sizes its panels; below the floor it renders the size line
	// instead, so routing tests need a real terminal size first.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// T3.11 retired the digits: they are no-ops, not navigation.
	m.Update(key("2"))
	if m.active != viewHome {
		t.Fatalf("active = %v after 2, want the digit to be dead", m.active)
	}
	m.Update(key("6"))
	if m.active != viewHome {
		t.Fatalf("active = %v after 6, want the digit to be dead", m.active)
	}

	// The palette is the route: search for the workflows entry and run it.
	m.Update(key(":"))
	if m.palette == nil {
		t.Fatal(": did not open the palette")
	}
	for _, r := range "workflows" {
		m.Update(key(string(r)))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette != nil {
		t.Fatal("running an entry did not close the palette")
	}
	if m.active != viewWorkflows {
		t.Fatalf("active = %v, want the workflows view via the palette", m.active)
	}
	// esc on the takeover asks to leave; the command it returns is the
	// message the runtime would feed back.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc on the takeover produced no command")
	}
	m.Update(cmd())
	if m.active != viewHome {
		t.Fatalf("active = %v, want esc to close the takeover", m.active)
	}

	m.Update(key("?"))
	help := content(m)
	if !strings.Contains(help, "GLOBAL KEYS") {
		t.Errorf("help overlay missing after ?: %q", help)
	}
	// The sheet is contextual (T3.8): on the home screen it carries the
	// focused panel's keys, and its own key row replaces the footer's.
	for _, k := range []string{"open the selected task", "filter by id", "esc"} {
		if !strings.Contains(help, k) {
			t.Errorf("help overlay lacks %q: %q", k, help)
		}
	}
	if strings.Contains(help, "register a repository") {
		t.Errorf("help overlay carries another surface's keys: %q", help)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(content(m), "toggle this help") {
		t.Errorf("help overlay still visible after esc: %q", content(m))
	}
}

// TestTakeoversShareThePanelChrome pins a T3.8 walkthrough finding: the
// four takeover screens wear the same bordered frame as the home panels,
// and the key guidance appears once — in the registry footer — not again
// as a second, differently-styled row inside the view.
func TestTakeoversShareThePanelChrome(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	views := map[viewID]string{
		viewNewTask:   "New task",
		viewProjects:  "Projects",
		viewWorkflows: "Workflows",
		viewDaemon:    "Daemon",
	}
	for id, title := range views {
		m.Update(selectViewMsg{id: id})
		body := content(m)
		if !strings.Contains(body, focusGlyph+" "+title) {
			t.Errorf("%s: no framed title in the body", title)
		}
		if !strings.Contains(body, "┌─") || !strings.Contains(body, "└") {
			t.Errorf("%s: no frame borders", title)
		}
		// The retired duplicate rows must be gone; the registry footer is
		// the one key surface.
		for _, stale := range []string{"new task here", "follow the log", "re-read", "re-probe adapters · esc back"} {
			if strings.Contains(body, stale) {
				t.Errorf("%s: the old in-view key row is back: %q", title, stale)
			}
		}
		if got := strings.Count(body, "commands"); got != 1 {
			t.Errorf("%s: %d key rows mention the palette, want exactly the footer's 1", title, got)
		}
	}
}

// TestBackgroundMessagesReachTheirOwnView is the T3.8 finding: background
// work belongs to the view that started it, not to whichever screen the
// human is looking at. The board's refresh debounce fired while the
// new-task form was up, the form swallowed it, and the board never
// refetched again — "0/3 running" with tasks in the daemon until restart.
func TestBackgroundMessagesReachTheirOwnView(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	b := m.views[viewHome].(*shell).board
	b.client = apiclient.New("http://127.0.0.1:1", "token")

	// A task event opens the debounce window, then the human opens the form.
	if cmd := b.scheduleRefresh(); cmd == nil {
		t.Fatal("scheduleRefresh returned no tick")
	}
	m.Update(selectViewMsg{id: viewNewTask})
	if m.active != viewNewTask {
		t.Fatalf("active = %v, want the takeover", m.active)
	}

	// The tick lands while the form is on screen. It must reach the board.
	_, cmd := m.Update(boardRefreshMsg{})
	if b.refreshPending {
		t.Fatal("the board's debounce is still pending; the refresh was swallowed")
	}
	if cmd == nil {
		t.Fatal("the debounce fired without issuing a refetch")
	}
	// And the window reopens, so later events are not ignored either.
	if next := b.scheduleRefresh(); next == nil {
		t.Fatal("the board cannot schedule another refresh; it is wedged")
	}

	// The elapsed ticker survives a takeover for the same reason: an
	// unre-armed tea.Tick never comes back.
	if _, cmd = m.Update(boardTickMsg(testNow)); cmd == nil {
		t.Fatal("the elapsed ticker died while a takeover was on screen")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		key("q"),
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%q returned no command", k.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%q command is not tea.Quit", k.String())
		}
	}
}

func TestReconnectingState(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
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

// TestRootJumpPushesTheTaskItLeaves is the root's half of #316's back stack:
// a jump made inside the workspace opens the target by the ordinary
// selectTaskMsg path — so the board, the palette and the takeovers keep the
// route they have always had — and the task it came from is pushed first.
func TestRootJumpPushesTheTaskItLeaves(t *testing.T) {
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := m.views[viewTask].(*taskView)

	m.Update(openTaskMsg{id: 42, state: stateBlocked, from: 7})
	if m.active != viewTask {
		t.Fatalf("a jump left the active view at %v, want the task workspace", m.active)
	}
	if m.selectedTask != 42 {
		t.Fatalf("the board's selection is %d, want 42", m.selectedTask)
	}
	if got := view.stack; len(got) != 1 || got[0] != 7 {
		t.Fatalf("stack after the jump = %v, want [7]", got)
	}
	if view.detail.taskID != 42 {
		t.Fatalf("the workspace opened task %d, want 42", view.detail.taskID)
	}

	// The board's own way of opening a task still clears the chain: there is
	// nothing behind a task opened from the board but the board.
	m.Update(selectTaskMsg{id: 43, state: stateRunning})
	if len(view.stack) != 0 {
		t.Fatalf("stack after a board open = %v, want empty", view.stack)
	}
}
