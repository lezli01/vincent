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

	active viewID
	views  [viewCount]view
	help   bool

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
		views: newViews(),
	}
}

// Init starts the connect flow immediately: probe first, auto-start on miss.
func (m *root) Init() tea.Cmd { return m.cn.probeCmd() }

// Update implements tea.Model.
func (m *root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
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
		m.active = viewID(key[0] - '1')
		m.help = false
		return m, nil
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
	return m, waitNote(m.notes)
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
	case apiclient.DisconnectedNote:
		m.phase = phaseReconnecting
		m.connErr = n.Err
		m.retryIn = n.RetryIn
	}
	return m, waitNote(m.notes)
}

// delegate routes a message to the active view.
func (m *root) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	v, cmd := m.views[m.active].update(msg)
	m.views[m.active] = v
	return m, cmd
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
