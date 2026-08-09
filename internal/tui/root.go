package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// connPhase is the daemon-connection state machine the shell renders. The
// failed and reconnecting screens share the retry affordance (PR H decision:
// the auto-start screen doubles as the reconnect screen).
type connPhase int

const (
	phaseProbing connPhase = iota
	phaseStarting
	phaseConnected
	phaseReconnecting
	phaseFailed
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true)
	styleOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleBad   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleDim   = lipgloss.NewStyle().Faint(true)
	// styleKey marks the letter to press, so an action bar reads as keys with
	// labels rather than a sentence.
	styleKey = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// noteMsg wraps one stream Note for Update.
type noteMsg struct{ note apiclient.Note }

// streamDoneMsg reports the note channel closed (stream context ended).
type streamDoneMsg struct{}

// root is the shell model: connection lifecycle, view routing, global keys,
// help overlay (§15). Views own everything else.
type root struct {
	cn  connector
	ctx context.Context

	phase       connPhase
	client      *apiclient.Client
	version     string
	dataDir     string
	autoStarted bool
	connErr     error
	logPath     string
	retryIn     time.Duration

	notes      <-chan apiclient.Note
	stopStream context.CancelFunc
	lastEvent  *apiclient.Event
	// streamLive reports that the SSE subscription is established, as
	// opposed to the daemon merely answering health checks.
	streamLive bool

	active viewID
	views  [viewCount]view
	help   bool
	// selectedTask is the task the board last opened; PR J's detail view
	// reads it from the same message that sets it.
	selectedTask int64

	width  int
	height int
}

// newRoot builds the shell. ctx bounds background work (the SSE stream);
// Run passes the program's lifetime.
func newRoot(ctx context.Context, cn connector) *root {
	return &root{
		cn:    cn,
		ctx:   ctx,
		phase: phaseProbing,
		views: newViews(ctx),
	}
}

// Init starts the connect flow immediately: probe first, auto-start on miss.
func (m *root) Init() tea.Cmd { return m.cn.probeCmd() }

// Update implements tea.Model.
func (m *root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, m.broadcast(msg)
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case selectTaskMsg:
		// A view asked to open a task. Routing is the shell's job, so the
		// board never reaches into the view table itself.
		m.selectedTask = msg.id
		// The detail view must have the task before it is told it is on
		// screen: activation is what opens its per-task subscription.
		cmd := m.deliver(viewDetail, msg)
		return m, tea.Batch(cmd, m.switchTo(viewDetail))
	case selectViewMsg:
		return m, m.switchTo(msg.id)
	case connectedMsg:
		return m.updateConnected(msg)
	case probeFailedMsg:
		m.phase = phaseStarting
		m.dataDir = msg.dataDir
		return m, m.cn.startCmd(msg.dataDir)
	case connectFailedMsg:
		m.phase = phaseFailed
		m.connErr = msg.err
		m.logPath = msg.logPath
		return m, nil
	case noteMsg:
		return m.updateNote(msg.note)
	case streamDoneMsg:
		return m, nil
	}
	return m.delegate(msg)
}

func (m *root) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// A view that is capturing text (the board's filter) owns every key but
	// ctrl+c: typing "q" into a filter must not quit the TUI.
	if msg.String() != "ctrl+c" && m.activeCapturesInput() {
		v, cmd := m.views[m.active].update(msg)
		m.views[m.active] = v
		return m, cmd
	}
	switch key := msg.String(); key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = !m.help
		return m, nil
	case "esc":
		m.help = false
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		return m, m.switchTo(viewID(key[0] - '1'))
	case "r":
		if m.phase == phaseFailed || m.phase == phaseReconnecting {
			return m, m.restartConnect()
		}
	}
	return m.delegate(msg)
}

// restartConnect tears down any live stream and reruns the full connect
// flow, auto-start included — the retry key on the failure screen.
func (m *root) restartConnect() tea.Cmd {
	if m.stopStream != nil {
		m.stopStream()
		m.stopStream = nil
		m.notes = nil
	}
	m.phase = phaseProbing
	m.connErr = nil
	return m.cn.probeCmd()
}

func (m *root) updateConnected(msg connectedMsg) (tea.Model, tea.Cmd) {
	m.phase = phaseConnected
	m.client = msg.client
	m.version = msg.health.Version
	m.dataDir = msg.dataDir
	m.autoStarted = msg.autoStarted
	m.connErr = nil
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.stopStream = cancel
	m.notes = m.client.StreamEvents(streamCtx, apiclient.StreamOptions{})
	cmds := []tea.Cmd{waitNote(m.notes)}
	for i := range m.views {
		if ca, ok := m.views[i].(clientAware); ok {
			if cmd := ca.setClient(m.client); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *root) updateNote(n apiclient.Note) (tea.Model, tea.Cmd) {
	if m.notes == nil {
		// A stale note from a stream torn down by retry; never re-arm on a
		// nil channel — that receive would block forever.
		return m, nil
	}
	switch n := n.(type) {
	case apiclient.EventNote:
		ev := n.Event
		m.lastEvent = &ev
	case apiclient.ConnectedNote:
		m.phase = phaseConnected
		m.connErr = nil
		// Distinct from phaseConnected, which only means the health probe
		// answered: this is the event stream itself being established, and
		// until it is, a committed event will not reach us (a stream with no
		// Last-Event-ID starts live at the *next* event, §13.3).
		m.streamLive = true
	case apiclient.DisconnectedNote:
		m.phase = phaseReconnecting
		m.streamLive = false
		m.connErr = n.Err
		m.retryIn = n.RetryIn
	}
	// Every view sees the note, not just the visible one.
	return m, tea.Batch(m.broadcast(noteMsg{note: n}), waitNote(m.notes))
}

// delegate routes a message to the active view.
func (m *root) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, m.deliver(m.active, msg)
}

// deliver routes a message to one specific view.
func (m *root) deliver(id viewID, msg tea.Msg) tea.Cmd {
	v, cmd := m.views[id].update(msg)
	m.views[id] = v
	return cmd
}

// switchTo changes the visible view and tells both sides. A view that owns a
// live subscription — the detail view's per-task stream — needs to know when
// it is no longer being watched, and re-entering one is when it refreshes.
func (m *root) switchTo(id viewID) tea.Cmd {
	m.help = false
	if id == m.active {
		return nil
	}
	prev := m.active
	m.active = id
	return tea.Batch(
		m.deliver(prev, viewDeactivatedMsg{id: prev}),
		m.deliver(id, viewActivatedMsg{id: id}),
	)
}

// broadcast routes a message to every view, not just the visible one.
// Connection lifecycle, window size and stream events all have to reach a
// view that is currently off-screen: the board must keep its rows current
// and must ring the bell for a task that starts waiting while the user is
// looking at another view.
func (m *root) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, 0, viewCount)
	for i := range m.views {
		v, cmd := m.views[i].update(msg)
		m.views[i] = v
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// activeCapturesInput reports whether the visible view is consuming raw
// keystrokes, in which case the global single-key bindings stand down.
func (m *root) activeCapturesInput() bool {
	c, ok := m.views[m.active].(inputCapturing)
	return ok && c.capturesInput()
}

// waitNote receives the next stream note as a message; Update re-arms it.
func waitNote(ch <-chan apiclient.Note) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		return noteMsg{note: n}
	}
}

// View implements tea.Model.
func (m *root) View() tea.View {
	var b strings.Builder
	b.WriteString(m.headerLine())
	b.WriteString("\n")
	b.WriteString(m.eventLine())
	b.WriteString("\n")
	b.WriteString(m.body())
	b.WriteString("\n")
	b.WriteString(m.footerLine())
	return tea.NewView(b.String())
}

func (m *root) headerLine() string {
	name := "vincent"
	if m.version != "" {
		name += " " + m.version
	}
	return fmt.Sprintf(" %s  %s  %s",
		styleTitle.Render(name), m.connBadge(),
		styleDim.Render("["+m.views[m.active].title()+"]"))
}

func (m *root) connBadge() string {
	switch m.phase {
	case phaseProbing:
		return styleWarn.Render("◌ connecting…")
	case phaseStarting:
		return styleWarn.Render("⧗ starting daemon…")
	case phaseConnected:
		return styleOK.Render("● connected")
	case phaseReconnecting:
		return styleWarn.Render("⟳ reconnecting…")
	default:
		return styleBad.Render("✗ disconnected")
	}
}

// eventLine renders the last durable event received — the foundation's
// visible proof that external state changes reach the TUI live (T3.1).
func (m *root) eventLine() string {
	if m.lastEvent == nil {
		return styleDim.Render(" no events yet")
	}
	ev := m.lastEvent
	line := fmt.Sprintf(" last event: %s #%d", ev.Type, ev.ID)
	if ev.TaskID != nil {
		line += fmt.Sprintf(" · task %d", *ev.TaskID)
	}
	line += " · " + ev.TS.Local().Format("15:04:05")
	return styleDim.Render(line)
}

func (m *root) body() string {
	if m.help {
		return helpText()
	}
	switch m.phase {
	case phaseProbing:
		return "\n  connecting to daemon…\n"
	case phaseStarting:
		return "\n  starting daemon…\n"
	case phaseFailed:
		return fmt.Sprintf("\n  %s\n\n  log: %s\n\n  press r to retry, q to quit\n",
			styleBad.Render("daemon unreachable: "+errString(m.connErr)), m.logPath)
	case phaseReconnecting:
		return fmt.Sprintf("\n  %s\n\n  retrying in %s — press r to restart the daemon if it stays down\n",
			styleWarn.Render("connection lost: "+errString(m.connErr)), m.retryIn)
	default:
		return m.views[m.active].render(m.width, m.bodyHeight())
	}
}

func (m *root) footerLine() string {
	hints := " 1-6 views · ? help · q quit"
	if m.phase == phaseFailed || m.phase == phaseReconnecting {
		hints += " · r retry"
	}
	return styleDim.Render(hints)
}

// bodyHeight is the space left for the active view: total minus header,
// event line, and footer.
func (m *root) bodyHeight() int {
	const chrome = 3
	if m.height <= chrome {
		return 0
	}
	return m.height - chrome
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
