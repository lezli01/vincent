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
	// streamLive reports that the SSE subscription is established, as
	// opposed to the daemon merely answering health checks.
	streamLive bool

	active viewID
	views  [viewCount]panel
	help   bool
	// palette is the §15 command palette, open when non-nil. It lives on
	// the root because it must overlay every screen, takeovers included —
	// while disconnected it is how the daemon view stays reachable.
	palette *palette
	// reader is the task 076 copy picker, open when non-nil. It lives beside
	// the palette and is routed exactly like it: a short-lived popup that
	// owns the keyboard while it is up. The two are never open together —
	// each closes on the key that would open the other.
	reader *readerPicker
	// mouseOn drives tea.View's mouse mode: on by default, M toggles (§15
	// Mouse). Off restores native click-drag text selection.
	mouseOn bool
	// footerHits are the clickable spans of the last-rendered footer.
	footerHits []footerHit
	// notice is §16's full-auto warning. It owns the whole screen until it
	// is dismissed, ahead of and independent of the connect flow: the
	// warning is about what the daemon will do, so it must not wait on the
	// daemon being reachable.
	notice firstRunNotice
	// selectedTask is the task the board last opened; PR J's detail view
	// reads it from the same message that sets it.
	selectedTask int64

	// github is the §13.2 capability probe per registered project, refreshed
	// as the connection comes up and again on reconnect. It lives here rather
	// than in the pull-requests view because the *nav row that reaches that
	// view* is gated on it, and the nav rows are global (task 052.6).
	//
	// While every answer is unavailable — including while the probes are
	// still in flight — the row is withheld everywhere: the palette, the ?
	// overlay and the footer.
	github []githubProject

	width  int
	height int
}

// newRoot builds the shell. ctx bounds background work (the SSE stream);
// Run passes the program's lifetime. dataDir is resolved by the caller and
// not here so the first-run notice and the connector cannot disagree about
// which directory this is; it is empty when resolution failed, which the
// notice treats as "show it".
func newRoot(ctx context.Context, cn connector, dataDir string) *root {
	m := &root{
		cn:      cn,
		ctx:     ctx,
		phase:   phaseProbing,
		dataDir: dataDir,
		views:   newViews(ctx),
		mouseOn: true,
		notice:  firstRunNotice{active: !noticeAcknowledged(dataDir)},
	}
	m.setDataDir(dataDir)
	return m
}

// setDataDir hands the resolved directory to the views that read from it.
func (m *root) setDataDir(dir string) {
	m.dataDir = dir
	for i := range m.views {
		if da, ok := m.views[i].(dataDirAware); ok {
			da.setDataDir(dir)
		}
	}
}

// setConnected tells the views that render while the daemon is gone.
func (m *root) setConnected(ok bool) {
	for i := range m.views {
		if ca, ok2 := m.views[i].(connectionAware); ok2 {
			ca.setConnected(ok)
		}
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
		// The board keeps the row selected while the dedicated task workspace
		// loads the authoritative detail snapshot.
		m.selectedTask = msg.id
		return m, tea.Batch(
			m.deliver(viewHome, msg),
			m.deliver(viewTask, msg),
			m.switchTo(viewTask),
		)
	case selectViewMsg:
		return m, m.switchTo(msg.id)
	case openChatMsg:
		// The chat workspace is not the active view yet, and an inactive
		// view receives nothing — so the root points it at the chat first
		// and switches second, the same order every takeover that carries an
		// argument uses.
		if v, ok := m.views[viewChat].(*chatView); ok {
			return m, tea.Batch(v.open(msg.id), m.switchTo(viewChat))
		}
		return m, nil
	case githubProbeMsg:
		// The listing failing leaves the previous answer standing: a probe
		// that could not be made is not an integration that stopped working,
		// and dropping the nav row under a human mid-session on a transient
		// error would be worse than a row that briefly outlives its project.
		if msg.err == nil {
			m.github = msg.available()
		}
		return m, m.broadcast(msg)
	case newTaskFromPullMsg:
		return m.updateNewTaskFromPull(msg)
	case newTaskFromChatMsg:
		return m.updateNewTaskFromChat(msg)
	case taskCreatedMsg:
		return m.updateTaskCreated(msg)
	case connectedMsg:
		return m.updateConnected(msg)
	case probeFailedMsg:
		m.phase = phaseStarting
		m.setDataDir(msg.dataDir)
		return m, m.cn.startCmd(msg.dataDir)
	case connectFailedMsg:
		m.phase = phaseFailed
		m.connErr = msg.err
		m.logPath = msg.logPath
		m.setConnected(false)
		return m, nil
	case tea.PasteMsg:
		return m, m.updatePaste(msg.Content)
	case tea.MouseClickMsg:
		return m.updateMouseClick(msg)
	case tea.MouseWheelMsg:
		if m.notice.active || m.palette != nil || m.reader != nil || m.help {
			return m, nil
		}
		msg.Y-- // the body starts under the header line
		return m, m.deliver(m.active, msg)
	case openCopyPickerMsg:
		m.reader = newReaderPicker(msg.items)
		return m, nil
	case clipboardResultMsg:
		return m, m.updateClipboardResult(msg)
	case noteMsg:
		return m.updateNote(msg.note)
	case streamDoneMsg:
		return m, nil
	}
	// Input is routed; everything else is broadcast. Keys and mouse events
	// belong to the surface in front of the human and are handled above;
	// what reaches here is background work — a debounce firing, a fetch
	// landing, a ticker re-arming — and it belongs to the view that started
	// it, which is not necessarily the visible one. Delivering these to the
	// active view only was a wedge: the board's refresh debounce fired while
	// the new-task form was up, the form dropped it, and `refreshPending`
	// stayed true forever, so the board ignored every later task event until
	// the TUI was restarted (T3.8 finding). A tea.Tick that is not re-armed
	// is gone for good, so the same routing killed the elapsed ticker.
	return m, m.broadcast(msg)
}

// updatePaste delivers pasted text to whatever field has the keyboard. It
// follows the key routing rather than the broadcast one — paste is input —
// so it lands on the layer a keystroke would: the palette, then the active
// view, and nowhere at all when nothing is capturing text. A paste with no
// field to receive it is dropped rather than treated as keystrokes: replaying
// a pasted path as single keys on the board would fire its action letters.
func (m *root) updatePaste(text string) tea.Cmd {
	if text == "" || m.notice.active || m.help {
		return nil
	}
	if m.reader != nil {
		return m.reader.paste(text)
	}
	if m.palette != nil {
		return m.palette.paste(text)
	}
	if !m.activeCapturesInput() {
		return nil
	}
	p, ok := m.views[m.active].(pasteReceiving)
	if !ok {
		return nil
	}
	return p.paste(text)
}

// updateMouseClick is §15's click scope: a footer hint fires its key, and
// everything else lands in the active screen's body. Popups stay keyboard;
// right-clicks and the rest are out of scope.
func (m *root) updateMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.notice.active || m.palette != nil || m.reader != nil || m.help {
		return m, nil
	}
	if m.height > 0 && msg.Y == m.height-1 {
		for _, h := range m.footerHits {
			if msg.X >= h.x0 && msg.X < h.x1 {
				return m.updateKey(synthKey(h.key))
			}
		}
		return m, nil
	}
	msg.Y-- // the body starts under the header line
	return m, m.deliver(m.active, msg)
}

func (m *root) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.notice.active {
		return m.updateNoticeKey(msg)
	}
	// ctrl+v is the explicit paste, for terminals that hand the key to the
	// app rather than pasting for you. It is caught ahead of every layer so
	// the clipboard is read in one place; the text comes back as a
	// tea.PasteMsg and takes the same route bracketed paste does. Nothing
	// capturing text means nothing to paste into — don't shell out to read a
	// clipboard whose contents would be dropped.
	if msg.String() == "ctrl+v" && (m.palette != nil || m.reader != nil || m.activeCapturesInput()) {
		return m, readClipboardCmd()
	}
	// An open popup owns every key but ctrl+c — it is the top of the §15 esc
	// stack.
	if m.reader != nil && msg.String() != "ctrl+c" {
		return m.updateReaderKey(msg)
	}
	if m.palette != nil && msg.String() != "ctrl+c" {
		return m.updatePaletteKey(msg)
	}
	// ctrl+p is `:` for a surface that is capturing text. It is hoisted above
	// the input-capture gate the way ctrl+v is, and for the same reason: a
	// chat's composer takes every printable key, so `:` there types a colon
	// into the draft and opens nothing (task 076 decision 7). The palette is
	// §15's "what can be done right now" surface, and it had been unreachable
	// from a chat since the workspace landed.
	if msg.String() == paletteAltKey {
		m.openPalette()
		return m, nil
	}
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
	case ":":
		m.openPalette()
		return m, nil
	case "?":
		m.help = !m.help
		return m, nil
	case "esc":
		// The top layers of the §15 esc stack that the root owns: the help
		// overlay, then a takeover screen's own layers (the views handle
		// those, ending in leave-to-home). The board owns the filter
		// layer, and the bottom is a no-op — esc never quits.
		if m.help {
			m.help = false
			return m, nil
		}
	case "M":
		m.mouseOn = !m.mouseOn
		return m, nil
	case "!":
		// Jump to the next task needing a human — global, so it also pulls
		// a takeover screen back to the board it acts on.
		if m.phase == phaseConnected {
			return m, tea.Batch(m.switchTo(viewHome), m.deliver(viewHome, jumpAttentionMsg{}))
		}
	case "n":
		// Not while the form is already up: there, n is "no" to the discard
		// prompt, and re-opening would throw away the draft it is asking
		// about. And not when the active surface declares n as a row of its
		// own — §15 makes n the one key whose meaning depends on where you
		// are, so on the chats board it falls through to delegate and makes a
		// chat. The yield is scoped to this arm rather than sitting ahead of
		// the switch because n is the only global key that both collides with
		// a panel row and consumes the key unconditionally: esc is declared by
		// ctxChat and ctxNewChat too, and must still close the help overlay
		// first.
		if m.phase == phaseConnected && m.active != viewNewTask && !m.panelOwnsKey(key) {
			return m, m.openNewTask()
		}
	case "r":
		if m.phase == phaseFailed || m.phase == phaseReconnecting {
			return m, m.restartConnect()
		}
	}
	return m.delegate(msg)
}

// updatePaletteKey routes keys into the open palette, and runs whatever it
// picks: a keyed entry replays its direct keypress through the normal
// routing — one execution path, so the palette cannot diverge from the
// shortcut it teaches — and a keyless navigation entry switches screens.
func (m *root) updatePaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	run, done, cmd := m.palette.update(msg)
	if done {
		m.palette = nil
	}
	if run == nil {
		return m, cmd
	}
	// A keyed nav entry replays its key — except where the active surface owns
	// that key as a row of its own, since the replay would then run the
	// panel's command instead of the navigation the entry names. On the chats
	// board that is "new task", whose key n now makes a chat.
	if run.nav && (run.key == "" || m.panelOwnsKey(run.key)) {
		return m, m.switchTo(run.navTarget)
	}
	return m.updateKey(synthKey(run.key))
}

// updateReaderKey routes keys into the open copy picker and puts whatever it
// picks on the clipboard. The payload was captured when the popup was built,
// so what is copied is what was on screen then — records that arrived since,
// and a resize, change nothing.
func (m *root) updateReaderKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	run, done, cmd := m.reader.update(msg)
	if done {
		m.reader = nil
	}
	if run == nil {
		return m, cmd
	}
	return m, writeClipboardCmd(run.label, run.text)
}

// openPalette builds the palette for the active surface.
func (m *root) openPalette() {
	ctx := m.activeContext()
	target := taskActions{}
	editable := false
	if s, ok := m.views[m.active].(*shell); ok {
		target = s.board.target()
		editable = s.detail.stepEditable()
	} else if t, ok := m.views[m.active].(*taskView); ok {
		target = t.target()
		editable = t.detail.stepEditable()
	}
	m.palette = newPalette(paletteEntries(
		ctx, target, editable, m.phase == phaseConnected, m.githubAvailable()))
}

// panelOwnsKey reports whether the active surface declares key as one of its
// own panel rows. It is what a global single-key binding consults before
// consuming a key the view underneath it also claims, so the registry answers
// the collision rather than a list of view special cases in updateKey.
func (m *root) panelOwnsKey(key string) bool {
	for _, b := range bindingsFor(m.activeContext()) {
		if b.key == key {
			return true
		}
	}
	return false
}

// activeContext names the active surface for the binding registry.
func (m *root) activeContext() bindingContext {
	switch m.active {
	case viewTask:
		return m.views[viewTask].(*taskView).bindingContext()
	case viewNewTask:
		return ctxNewTask
	case viewProjects:
		return ctxProjects
	case viewWorkflows:
		if c, ok := m.views[viewWorkflows].(contextual); ok {
			return c.bindingContext()
		}
		return ctxWorkflows
	case viewDaemon:
		return ctxDaemon
	case viewPullRequests:
		return ctxPullRequests
	case viewChats:
		return m.views[viewChats].(*chatsView).bindingContext()
	case viewChat:
		return m.views[viewChat].(*chatView).bindingContext()
	default:
		s := m.views[viewHome].(*shell)
		return s.focusedContext()
	}
}

// synthKey rebuilds the key message a terminal would deliver for one
// registry key.
func synthKey(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}

// updateNoticeKey is the §16 overlay's key handling: it swallows everything
// except the two keys that mean something. Deliberately not esc and not q —
// those are what people press to make a box go away without reading it, and
// this is the one screen where that is the failure. ctrl+c still quits,
// because the TUI must always be killable, and it leaves the flag unwritten
// so the notice comes back.
func (m *root) updateNoticeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.notice.acknowledge(m.dataDir)
	}
	return m, nil
}

// openNewTask opens the §15 new-task form, seeded with the project the
// current view is looking at. The form must be told to open before it is
// shown: opening is what resets the draft and fetches the catalogs.
func (m *root) openNewTask() tea.Cmd {
	hint := int64(0)
	if h, ok := m.views[m.active].(projectHinting); ok {
		hint = h.hintedProject()
	}
	cmd := m.deliver(viewNewTask, newTaskMsg{projectID: hint})
	return tea.Batch(cmd, m.switchTo(viewNewTask))
}

// updateNewTaskFromPull opens the new-task form seeded with a pull request
// (task 064). It goes through the root for the reason openNewTask does: the
// form has to be told to open before it is shown, because opening is what
// resets the draft and fetches the catalogs.
func (m *root) updateNewTaskFromPull(msg newTaskFromPullMsg) (tea.Model, tea.Cmd) {
	cmd := m.deliver(viewNewTask, msg)
	return m, tea.Batch(cmd, m.switchTo(viewNewTask))
}

// updateNewTaskFromChat opens the form as a chat's handoff form (task 074),
// through the root for the reason the two above go through it: the form must
// be told to open before it is shown, because opening is what resets the draft
// and fetches the catalogs.
func (m *root) updateNewTaskFromChat(msg newTaskFromChatMsg) (tea.Model, tea.Cmd) {
	cmd := m.deliver(viewNewTask, msg)
	return m, tea.Batch(cmd, m.switchTo(viewNewTask))
}

// updateTaskCreated lands on the task that was just created. Creating a task
// is the beginning of watching it, and the 201's warnings ride along so an
// advisory finding is not lost on a board row.
func (m *root) updateTaskCreated(msg taskCreatedMsg) (tea.Model, tea.Cmd) {
	m.selectedTask = msg.task.ID
	selectMsg := selectTaskMsg{id: msg.task.ID, state: msg.task.State}
	cmds := []tea.Cmd{
		m.deliver(viewHome, selectMsg),
		m.deliver(viewHome, msg),
		m.deliver(viewTask, selectMsg),
		m.deliver(viewTask, msg),
		m.switchTo(viewTask),
	}
	return m, tea.Batch(cmds...)
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
	m.setDataDir(msg.dataDir)
	m.setConnected(true)
	m.autoStarted = msg.autoStarted
	m.connErr = nil
	streamCtx, cancel := context.WithCancel(m.ctx)
	m.stopStream = cancel
	m.notes = m.client.StreamEvents(streamCtx, apiclient.StreamOptions{})
	cmds := []tea.Cmd{waitNote(m.notes), probeGitHubCmd(m.client)}
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
	var reprobe tea.Cmd
	if m.notes == nil {
		// A stale note from a stream torn down by retry; never re-arm on a
		// nil channel — that receive would block forever.
		return m, nil
	}
	switch n := n.(type) {
	case apiclient.ConnectedNote:
		m.phase = phaseConnected
		m.connErr = nil
		if !m.streamLive {
			// A reconnect: a project may have been registered, or a token may
			// have expired, while the stream was down. The daemon's short
			// cache absorbs the repeat cost.
			reprobe = probeGitHubCmd(m.client)
		}
		// Distinct from phaseConnected, which only means the health probe
		// answered: this is the event stream itself being established, and
		// until it is, a committed event will not reach us (a stream with no
		// Last-Event-ID starts live at the *next* event, §13.3).
		m.streamLive = true
		m.setConnected(true)
	case apiclient.DisconnectedNote:
		m.phase = phaseReconnecting
		m.streamLive = false
		m.connErr = n.Err
		m.retryIn = n.RetryIn
		m.setConnected(false)
	}
	// Every view sees the note, not just the visible one.
	return m, tea.Batch(m.broadcast(noteMsg{note: n}), waitNote(m.notes), reprobe)
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
	if m.notice.active {
		// The overlay takes the whole screen, chrome included: a warning
		// framed by a working app reads as decoration.
		return tea.NewView(m.notice.render())
	}
	var b strings.Builder
	b.WriteString(m.headerLine())
	b.WriteString("\n")
	if line, ok := m.notice.statusLine(); ok {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(m.body())
	b.WriteString("\n")
	b.WriteString(m.footerLine())
	frame := b.String()
	if popup, ok := m.popupRender(); ok {
		pw := min(m.width-8, 64)
		if pw < 24 {
			pw = max(m.width-2, 10)
		}
		ph := min(18, max(m.height-4, 6))
		frame = overlay(frame, popup(pw, ph), max((m.width-pw)/2, 0), 2)
	}
	v := tea.NewView(frame)
	if m.mouseOn && !m.notice.active {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
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

func (m *root) body() string {
	if m.help {
		// The sheet describes the surface it was opened over, framed like
		// every other surface (T3.8 findings).
		ctx := m.activeContext()
		h := m.bodyHeight()
		text := helpText(ctx, m.githubAvailable())
		if m.width < 4 || h < 3 {
			return text
		}
		return frame(helpTitle(ctx), text, m.width, h, true)
	}
	// The daemon view is the exception to the connection gate (§15): its log
	// tail comes off the filesystem, and a daemon that is down is exactly
	// when that log is worth reading.
	if m.active == viewDaemon && m.phase != phaseConnected {
		return m.framedView(viewDaemon)
	}
	// The board and an already-loaded task stay on screen while the daemon is
	// unreachable, marked stale (§15 Disconnected) — provided there was
	// ever anything to show; before the first connection a stale empty board
	// would be a lie, not information (PR P decision). The takeovers are
	// forms against a live daemon and stay behind the screens below.
	if (m.active == viewHome && m.homeLoaded() || m.active == viewTask && m.taskLoaded()) &&
		(m.phase == phaseReconnecting || m.phase == phaseFailed) {
		if m.active == viewHome {
			return m.views[viewHome].render(m.width, m.bodyHeight())
		}
		return m.framedView(viewTask)
	}
	switch m.phase {
	case phaseProbing:
		return "\n  connecting to daemon…\n"
	case phaseStarting:
		return "\n  starting daemon…\n"
	case phaseFailed:
		return fmt.Sprintf(
			"\n  %s\n\n  log: %s\n\n  press r to retry, : for the daemon view and its log, q to quit\n",
			styleBad.Render("daemon unreachable: "+errString(m.connErr)), m.logPath)
	case phaseReconnecting:
		return fmt.Sprintf("\n  %s\n\n  retrying in %s — press r to restart the daemon if it stays down\n",
			styleWarn.Render("connection lost: "+errString(m.connErr)), m.retryIn)
	default:
		if m.active == viewHome {
			return m.views[viewHome].render(m.width, m.bodyHeight())
		}
		return m.framedView(m.active)
	}
}

// framedView wraps a takeover screen in the same bordered chrome the home
// panels wear (T3.8 finding: the six surfaces should read as one program).
// A takeover is the only surface on screen, so its frame is always focused.
func (m *root) framedView(id viewID) string {
	h := m.bodyHeight()
	if m.width < 4 || h < 3 {
		return m.views[id].render(m.width, h)
	}
	content := m.views[id].render(m.width-2, h-2)
	return frame(m.views[id].title(), content, m.width, h, true)
}

// quitReminder is §15's exit line: the daemon keeps working after the TUI
// closes, and the running count is what makes that worth saying. It is a
// line printed after teardown rather than a prompt before it — quitting is
// non-destructive by construction, and a confirmation on a harmless action
// is friction.
//
// Nothing is printed at zero, and nothing when the board never loaded: "0
// tasks running" from a TUI that never reached the daemon is a false
// statement, not a reassuring one.
func (m *root) quitReminder() (string, bool) {
	s, ok := m.views[viewHome].(*shell)
	if !ok || !s.board.loaded {
		return "", false
	}
	n := countRunning(s.board.tasks)
	if n == 0 {
		return "", false
	}
	noun := "tasks are"
	if n == 1 {
		noun = "task is"
	}
	return fmt.Sprintf("%d %s still running — the daemon keeps working. Run vincent to come back.",
		n, noun), true
}

// homeLoaded reports whether the board ever loaded a task list — the
// difference between "stale panels are information" and "there is nothing
// to mark stale".
func (m *root) homeLoaded() bool {
	s, ok := m.views[viewHome].(*shell)
	return ok && s.board.loaded
}

// githubAvailable reports whether any registered project answered the §13.2
// probe with available: true. It is what withholds the pull-requests nav row
// and the workspace's two pull-request keys.
func (m *root) githubAvailable() bool { return len(m.github) > 0 }

func (m *root) taskLoaded() bool {
	t, ok := m.views[viewTask].(*taskView)
	return ok && t.detail.loaded
}

// footerLine is the §15 contextual footer, rendered from the registry and
// the shell's action bar.
func (m *root) footerLine() string {
	if m.help {
		// The overlay owns the keyboard, so it owns the key row: the keys
		// underneath do nothing until it closes.
		return helpFooter(m.width)
	}
	ctx := m.activeContext()
	var (
		bar       *actionBar
		target    taskActions
		attention int
	)
	if s, ok := m.views[viewHome].(*shell); ok {
		attention = countAttention(s.board.tasks)
	}
	if s, ok := m.views[m.active].(*shell); ok {
		bar = s.bar
		if m.phase == phaseConnected {
			target = s.board.target()
			// Answer is opened from the task workspace. On the board, enter
			// crosses that screen boundary; advertising it as an immediate
			// answer would describe the second press rather than this one.
			target.actions = withoutAction(target.actions, apiclient.ActionAnswer)
		}
	} else if t, ok := m.views[m.active].(*taskView); ok {
		bar = t.detail.actions
		if m.phase == phaseConnected {
			target = t.target()
		}
	}
	rows := withoutGitHub(bindingsFor(ctx), m.githubAvailable())
	if s, ok := m.views[m.active].(*shell); ok {
		rows = s.liveBindings(rows)
	}
	retry := m.phase == phaseFailed || m.phase == phaseReconnecting
	line, hits := buildFooter(m.width, rows, bar, target, attention, retry)
	m.footerHits = hits
	return line
}

func withoutAction(actions []string, drop string) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		if action != drop {
			out = append(out, action)
		}
	}
	return out
}

// bodyHeight is the space left for the active view: total minus header,
// event line, and footer.
func (m *root) bodyHeight() int {
	chrome := 2
	if _, ok := m.notice.statusLine(); ok {
		chrome++
	}
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

// popupRender names the popup the root itself owns and is drawing, if any.
// They are mutually exclusive and share one box, so the geometry is decided
// once rather than copied per popup.
func (m *root) popupRender() (func(w, h int) string, bool) {
	switch {
	case m.reader != nil:
		return m.reader.render, true
	case m.palette != nil:
		return m.palette.render, true
	default:
		return nil, false
	}
}

// updateClipboardResult turns a copy's outcome into the active surface's own
// notice (§15). It goes to the active view rather than being broadcast: the
// human pressed the key on one screen and is looking at it.
func (m *root) updateClipboardResult(msg clipboardResultMsg) tea.Cmd {
	cmds := []tea.Cmd{m.deliver(m.active, msg)}
	if msg.osc != "" {
		// The system clipboard refused; ask the terminal itself (OSC 52).
		cmds = append(cmds, tea.SetClipboard(msg.osc))
	}
	return tea.Batch(cmds...)
}
