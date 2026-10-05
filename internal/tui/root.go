package tui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
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
	// helpScroll is the help sheet's first visible line (task 129.17),
	// zeroed each time the sheet opens and clamped to the text on every use.
	helpScroll int
	// palette is the §15 command palette, open when non-nil. It lives on
	// the root because it must overlay every screen, takeovers included —
	// while disconnected it is how the daemon view stays reachable.
	palette *palette
	// reader is the task 076 copy picker, open when non-nil. It lives beside
	// the palette and is routed exactly like it: a short-lived popup that
	// owns the keyboard while it is up. The two are never open together —
	// each closes on the key that would open the other.
	reader *readerPicker
	// readerResolve re-reads a picked row's document from the view that
	// offered it, and is nil while no picker is up.
	readerResolve func(seq int64) (string, bool)
	// linkPick is the task 112 link picker, open when non-nil, and routed
	// exactly like reader: the three popups are never open together, because
	// whichever is up owns every key that would raise another.
	linkPick *linkPicker
	// linkPickResolve is readerResolve for the link picker.
	linkPickResolve func(seq int64) (string, bool)
	// projPick is the task 132.4 project picker, open when non-nil, and
	// routed exactly like the three above. projPickSeq numbers its fetches
	// so an answer for a picker since closed or refetched is dropped, and
	// projPickPending is its event debounce, armed at most once.
	projPick        *projectPicker
	projPickSeq     int
	projPickPending bool
	// headerHit is the header's `◆` segment as last rendered, [x0, x1) on
	// row 0: a left click there opens the project picker (task 132
	// decision 23).
	headerHit [2]int
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
	// keysNotice is the one-time line a lenient keymap build raises (task 128
	// decision 4): a tui.keys binding displaced a default or a fixed key, or
	// named a retired operation. Cleared by the next key; keysNoticed is the
	// warning set it was raised for, so the same set is not raised twice.
	keysNotice  string
	keysNoticed string
	// selectedTask is the task the board last opened; PR J's detail view
	// reads it from the same message that sets it.
	selectedTask int64
	// lastScoped is the last projectScoped view that was active, recorded by
	// switchTo as it is left: where enter on a project overview row returns
	// to (task 132 decision 42). Zero is the board, the answer with no
	// history.
	lastScoped viewID

	// github is the §13.2 capability probe for every registered project,
	// yes and no alike, refreshed as the connection comes up and again on
	// reconnect. It lives here rather than in the pull-requests view because
	// the *nav row that reaches that view* is gated on it, and the nav rows
	// are global (task 052.6).
	//
	// The gate follows the selected project (task 132.11): while its answer
	// is unavailable — including while the probes are still in flight — the
	// row is withheld everywhere: the palette, the ? overlay and the footer.
	github []githubProject
	// probeSeq numbers each fan-out the root issues and probeApplied is the
	// newest one applied. Probes overlap — connect, reconnect and every
	// project.* event start one — and each makes a ProjectGitHub call per
	// project, so an older one can land last; applied as it arrived, a
	// probe that listed projects before one was registered would erase that
	// project's answer (review F4 on PR #720).
	probeSeq, probeApplied uint64

	// sel is the TUI's one selected project (task 132, §15): client-side,
	// owned here, and handed to every project-bearing view by selectProject
	// rather than broadcast. selWhy is the rule that chose it.
	sel    projectSel
	selWhy string
	// startup is the task 132.3 resolution chain's state (startproject.go),
	// and selNotice the one line it raises (decision 30): a pick by the
	// working directory, or a rule that fell through. Cleared by the next
	// key, like keysNotice; selNoticeWarn renders it as a warning.
	startup       startupState
	selNotice     string
	selNoticeWarn bool
	// defaultProject is `tui.default_project` as of the latest config answer.
	// Startup reads only the first one; a deleted selection is replaced by
	// this one when it names a registered project (task 132 decision 46).
	defaultProject string
	// projects is the registered-project list the selection is checked
	// against, refreshed on connect, on reconnect and on every project.*
	// event. projectsSeq numbers the fetches so an older answer landing
	// after a newer one is dropped rather than rolling the list back.
	projects    []apiclient.Project
	projectsSeq int
	// pending is a project switch held on the active view's draft (task 132
	// decision 36): drawn as the status line's y/n question, and applied or
	// dropped — with any open waiting on it — by the answer.
	pending *pendingSwitch

	// links is the session's `tui.hyperlinks` (task 111). The root fills it
	// because the config arrives in three messages bound for three different
	// views — the board's fetch, the daemon view's fetch and the config
	// editor's save — and both output panes read it.
	links *hyperlinkHolder
	// level is the session's output verbosity, shared by both output panes.
	// The root holds it for links' reason: `tui.output.level` arrives on the
	// same three messages (task 129.11).
	level *levelHolder

	width  int
	height int
}

// newRoot builds the shell. ctx bounds background work (the SSE stream);
// Run passes the program's lifetime. dataDir is resolved by the caller and
// not here so the first-run notice and the connector cannot disagree about
// which directory this is; it is empty when resolution failed, which the
// notice treats as "show it".
func newRoot(ctx context.Context, cn connector, dataDir string) *root {
	links := newHyperlinkHolder()
	level := newLevelHolder()
	m := &root{
		cn:      cn,
		ctx:     ctx,
		phase:   phaseProbing,
		dataDir: dataDir,
		views:   newViews(ctx, links, level),
		links:   links,
		level:   level,
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
	case selectTaskMsg, openTaskMsg, openIssueMsg, openChatMsg, taskCreatedMsg:
		// Every open follows its object into its project first (task 132.6):
		// openObject switches when it must, then routes.
		return m, m.openObject(msg)
	case followFetchedMsg:
		return m, m.updateFollowFetched(msg)
	case selectViewMsg:
		return m, m.switchTo(msg.id)
	case overviewPickMsg:
		return m, m.openFromOverview(msg)
	case taskChatOpenedMsg:
		// `T` landing (task 119). The bars that said "opening a chat…" hear
		// the outcome either way, and each refetches its task — the lock is
		// on it now; a chat that exists is then opened the way enter on the
		// chats board opens one.
		cmds := []tea.Cmd{m.broadcast(msg)}
		if msg.err == nil && msg.chatID != 0 {
			if v, ok := m.views[viewChat].(*chatView); ok {
				cmds = append(cmds, v.open(msg.chatID), m.switchTo(viewChat))
			}
		}
		return m, tea.Batch(cmds...)
	case openConfigKeyMsg:
		// The triggers view's global-off banner (task 096.6) names a switch
		// it must not flip itself: the daemon view's editor is where a config
		// key is changed, and it is the one that asks first. Delivered before
		// the switch for the reason openChatMsg is.
		return m, tea.Batch(m.deliver(viewDaemon, msg), m.switchTo(viewDaemon))
	case githubProbeMsg:
		// The listing failing leaves the previous answer standing: a probe
		// that could not be made is not an integration that stopped working,
		// and dropping the nav row under a human mid-session on a transient
		// error would be worse than a row that briefly outlives its project.
		//
		// A fan-out older than one already applied is dropped whole, for
		// every view: it describes the project list as it was before.
		if msg.seq != 0 && msg.seq <= m.probeApplied {
			return m, nil
		}
		if msg.err == nil {
			m.github = msg.projects
			m.probeApplied = max(m.probeApplied, msg.seq)
		}
		return m, m.broadcast(msg)
	case boardConfigMsg, daemonConfigMsg, configSavedMsg:
		m.applyHyperlinks(msg)
		m.applyOutputLevel(msg)
		m.applyKeymap(msg)
		m.noteDefaultProject(msg)
		return m, tea.Batch(m.noteStartupConfig(msg), m.broadcast(msg))
	case startupWorktreesMsg:
		return m, m.resolveStartup(msg)
	case newTaskFromPullMsg:
		return m.updateNewTaskFromPull(msg)
	case newTaskFromIssueMsg:
		return m.updateNewTaskFromIssue(msg)
	case newTaskFromChatMsg:
		return m.updateNewTaskFromChat(msg)
	case connectedMsg:
		return m.updateConnected(msg)
	case projectListMsg:
		return m, m.updateProjectList(msg)
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
		if m.notice.active || m.popupOpen() || m.help {
			return m, nil
		}
		msg.Y-- // the body starts under the header line
		return m, m.deliver(m.active, msg)
	case openCopyPickerMsg:
		m.reader = newReaderPicker(msg.items)
		m.readerResolve = msg.resolve
		return m, nil
	case openLinkPickerMsg:
		m.linkPick = newLinkPicker(msg.items)
		m.linkPickResolve = msg.resolve
		return m, nil
	case projectPickerMsg:
		// An answer for a picker that has closed, or that has refetched
		// since, is dropped.
		if m.projPick != nil && msg.seq == m.projPickSeq {
			m.projPick.land(msg)
		}
		return m, nil
	case openProjectPickerMsg:
		return m, m.openProjectPicker()
	case projectPickerRefreshMsg:
		m.projPickPending = false
		if m.projPick == nil {
			return m, nil
		}
		return m, m.fetchProjectPicker()
	case linkOpenedMsg:
		// Like a copy's outcome, to the surface the human pressed the key on
		// and nowhere else (task 112 decision 5).
		return m, m.deliver(m.active, msg)
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
	if text == "" || m.notice.active || m.help || m.pending != nil {
		return nil
	}
	if m.reader != nil {
		return m.reader.paste(text)
	}
	if m.linkPick != nil {
		return m.linkPick.paste(text)
	}
	if m.projPick != nil {
		return m.projPick.paste(text)
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

// updateMouseClick is §15's click scope: a footer hint fires its key, the
// header's `◆` segment opens the project picker (task 132.4), and everything
// else lands in the active screen's body. Popups stay keyboard; right-clicks
// and the rest are out of scope.
func (m *root) updateMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.notice.active || m.popupOpen() || m.help {
		return m, nil
	}
	if msg.Y == 0 && msg.X >= m.headerHit[0] && msg.X < m.headerHit[1] {
		return m, m.openProjectPicker()
	}
	if m.height > 0 && msg.Y == m.height-1 {
		for _, h := range m.footerHits {
			if msg.X >= h.x0 && msg.X < h.x1 {
				return m.replayKey(h.key, h.global)
			}
		}
		return m, nil
	}
	msg.Y -= m.chromeTop() // the body starts under the header lines
	return m, m.deliver(m.active, msg)
}

func (m *root) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.notice.active {
		return m.updateNoticeKey(msg)
	}
	// The keymap and startup-project notices are read by the time a key is
	// pressed; the key itself still does what it does.
	m.keysNotice = ""
	m.selNotice = ""
	// A switch waiting on its draft confirmation owns every key but ctrl+c
	// (task 132 decision 36): it is the question on screen.
	if m.pending != nil && msg.String() != "ctrl+c" {
		return m, m.updatePendingSwitchKey(msg)
	}
	// The help overlay owns every key but ctrl+c, the palette's rule (task
	// 114 decision 2). It sits above the input-capture gate: over a chat, a
	// key that fell through would type into a draft the sheet is hiding, and
	// esc would leave the chat instead of closing the sheet. Mouse and paste
	// already stand down while it is open.
	if m.help {
		return m.updateHelpKey(msg)
	}
	// ctrl+v is the explicit paste, for terminals that hand the key to the
	// app rather than pasting for you. It is caught ahead of every layer so
	// the clipboard is read in one place; the text comes back as a
	// tea.PasteMsg and takes the same route bracketed paste does. Nothing
	// capturing text means nothing to paste into — don't shell out to read a
	// clipboard whose contents would be dropped.
	if msg.String() == "ctrl+v" && (m.popupOpen() || m.activeCapturesInput()) {
		return m, readClipboardCmd()
	}
	// An open popup owns every key but ctrl+c — it is the top of the §15 esc
	// stack.
	if m.reader != nil && msg.String() != "ctrl+c" {
		return m.updateReaderKey(msg)
	}
	if m.linkPick != nil && msg.String() != "ctrl+c" {
		return m.updateLinksKey(msg)
	}
	if m.projPick != nil && msg.String() != "ctrl+c" {
		return m.updateProjectPickerKey(msg)
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
	//
	// f1 is `?` for the same surfaces, hoisted for the same reason (task 114
	// decision 1): help had been unreachable from a chat, a filter or a form
	// by its key, and a text field that took `?` as a key would lose it as a
	// character.
	if k := msg.String(); k == opKey(keymap.PaletteAlt) || k == opKey(keymap.HelpAlt) {
		cmd, _ := m.globalKey(msg)
		return m, cmd
	}
	// A view that is capturing text (the board's filter) owns every key but
	// ctrl+c: typing "q" into a filter must not quit the TUI.
	if msg.String() != "ctrl+c" && m.activeCapturesInput() {
		v, cmd := m.views[m.active].update(msg)
		m.views[m.active] = v
		return m, cmd
	}
	if m.shadowedHere(msg.String()) {
		// A fixed key a user binding took on a lenient load (task 128
		// decision 3). The root's own operations were matched by opKey above
		// the fixed literals, so what is left is the fixed meaning: it yields
		// on this surface unless the operation that owns the key now is
		// answered here too.
		if cmd, ok := m.globalKey(msg); ok {
			return m, cmd
		}
		if !keyOwnerAnsweredOn(msg.String(), m.activeContext()) {
			return m, nil
		}
	}
	if cmd, ok := m.globalKey(msg); ok {
		return m, cmd
	}
	// The rest of the §15 esc stack — a takeover screen's own layers, ending
	// in leave-to-home, and the board's filter layer — belongs to the views,
	// and the bottom is a no-op: esc never quits. The top layer, the help
	// overlay, closed in updateHelpKey before the key got here.
	return m.delegate(msg)
}

// updateHelpKey is the open help overlay's keyboard: the three keys that
// close it, the six that scroll it (task 129.17), ctrl+c because the TUI must
// always be killable, and nothing else. It is what makes helpFooter's promise
// true — the keys of the surface under the sheet do nothing until it closes.
// The scroll keys are the overlay's own, like a popup's, rather than registry
// rows: they mean the same on every surface's sheet.
func (m *root) updateHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	lines, visible := m.helpWindow()
	last := max(len(lines)-visible, 0)
	scroll := min(m.helpScroll, last)
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case opKey(keymap.Help), "esc", opKey(keymap.HelpAlt):
		m.help = false
	case "up":
		scroll--
	case "down":
		scroll++
	case "pgup":
		scroll -= max(visible-1, 1)
	case "pgdown":
		scroll += max(visible-1, 1)
	case "home":
		scroll = 0
	case "end":
		scroll = last
	}
	m.helpScroll = max(min(scroll, last), 0)
	return m, nil
}

// helpWindow is the help sheet as lines, and how many of them the frame
// shows at once — one fewer than it holds when they overflow, since the last
// row is then the position cue.
func (m *root) helpWindow() (lines []string, visible int) {
	lines = strings.Split(strings.TrimRight(m.helpSheet(), "\n"), "\n")
	visible = max(m.bodyHeight()-2, 1)
	if len(lines) > visible {
		visible = max(visible-1, 1)
	}
	return lines, visible
}

// helpSheet renders the sheet for the surface under it, against the same
// target the palette would be built from.
func (m *root) helpSheet() string {
	st := m.surfaceTarget()
	return helpText(m.activeContext(), m.githubAvailable(),
		helpState{target: st.target, editable: st.editable, tabs: st.tabs})
}

// globalKey runs the root's own single-key bindings, and reports whether key
// was one of them in the state the root is in. A key it does not consume —
// `!` while disconnected, `n` on the new-task form — is the caller's to route.
func (m *root) globalKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch key := msg.String(); key {
	case opKey(keymap.Quit), "ctrl+c":
		return tea.Quit, true
	case opKey(keymap.Palette), opKey(keymap.PaletteAlt):
		m.openPalette()
		return nil, true
	case opKey(keymap.Help), opKey(keymap.HelpAlt):
		m.help = !m.help
		m.helpScroll = 0
		return nil, true
	case opKey(keymap.Mouse):
		m.mouseOn = !m.mouseOn
		return nil, true
	case opKey(keymap.Project):
		// While reconnecting too: the picker opens on the cached list.
		if m.phase == phaseConnected || m.phase == phaseReconnecting {
			return m.openProjectPicker(), true
		}
	case opKey(keymap.NextAttention):
		// Jump to the next task needing a human — global, so it also pulls
		// a takeover screen back to the board it acts on.
		if m.phase == phaseConnected {
			if cmd, ok := m.crossAttention(); ok {
				return cmd, true
			}
			return tea.Batch(m.switchTo(viewHome), m.deliver(viewHome, jumpAttentionMsg{})), true
		}
	case opKey(keymap.New):
		// Not while the form is already up: there, n is "no" to the discard
		// prompt, and re-opening would throw away the draft it is asking
		// about. And not when the active surface declares n as a row of its
		// own — §15 makes n the one key whose meaning depends on where you
		// are, so on the chats board it falls through to delegate and makes a
		// chat. The yield is scoped to this arm rather than sitting ahead of
		// the switch because n is the only global key that both collides with
		// a panel row and consumes the key unconditionally.
		if m.phase == phaseConnected && m.active != viewNewTask && !m.panelOwnsKey(key) {
			return m.openNewTask(), true
		}
	case "r":
		if m.phase == phaseFailed || m.phase == phaseReconnecting {
			return m.restartConnect(), true
		}
	}
	return nil, false
}

// replayKey runs a key the human chose rather than pressed: a palette entry
// or a footer span, both of which fire the key they show (the one-execution-
// path rule). A global row replays into the root's own bindings, past the
// input-capture gate (task 114 decision 3): picking "toggle this help" or
// clicking `q quit` in a chat must do what it says, not type `?` or `q` into
// the draft. A global key the root does not consume in its current state is
// dropped rather than delegated where a text field has the keyboard, for the
// same reason. Everything else — panel rows, task actions — takes the route
// a keypress takes.
func (m *root) replayKey(key string, global bool) (tea.Model, tea.Cmd) {
	msg := synthKey(key)
	if !global {
		return m.updateKey(msg)
	}
	if cmd, ok := m.globalKey(msg); ok {
		return m, cmd
	}
	if m.activeCapturesInput() {
		return m, nil
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
	if run.action != nil {
		return m, run.action
	}
	if run.unbound || run.key == "" {
		return m, cmd
	}
	return m.replayKey(run.key, run.global)
}

// updateReaderKey routes keys into the open copy picker and puts whatever it
// picks on the clipboard. A row is a reference to a document, resolved here
// against the view's records as they stand now (#291): a document that grew
// while the popup was up copies whole, and one whose records were pruned
// copies the text captured when the popup was built.
func (m *root) updateReaderKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	run, done, cmd := m.reader.update(msg)
	resolve := m.readerResolve
	if done {
		m.reader, m.readerResolve = nil, nil
	}
	if run == nil {
		return m, cmd
	}
	return m, writeClipboardCmd(run.label, pickText(*run, resolve))
}

// updateLinksKey routes keys into the open link picker and carries out what
// it picks: enter opens the destination, ctrl+y copies it (task 112). Both go
// through the existing chokepoints — openURLCmd's scheme refusal and
// writeClipboardCmd's sanitizing — so a refused row reaches no opener, and its
// notice names why.
func (m *root) updateLinksKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	run, act, done, cmd := m.linkPick.update(msg)
	resolve := m.linkPickResolve
	if done {
		m.linkPick, m.linkPickResolve = nil, nil
	}
	if run == nil {
		return m, cmd
	}
	dest := pickLink(*run, resolve)
	if act == linkCopy {
		return m, writeClipboardCmd(linkLabel(*run), dest)
	}
	return m, openLinkCmd(dest)
}

// popupOpen reports whether one of the root's own popups is up. Each owns the
// keyboard while it is, so at most one ever is.
func (m *root) popupOpen() bool {
	return m.palette != nil || m.reader != nil || m.linkPick != nil || m.projPick != nil || m.pending != nil
}

// openProjectPicker raises the project picker over whatever is on screen,
// seeded with the root's own name list so it is never empty while its first
// answer is in flight, and makes its one list call. Not before the first
// connect, when there is no list to pick from. While reconnecting it opens on
// the cached list, marked stale (task 132.7): a switch made then issues loads
// that fail, and the reconnect reloads exactly those views (decision 48).
func (m *root) openProjectPicker() tea.Cmd {
	if m.client == nil || m.phase != phaseConnected && m.phase != phaseReconnecting || m.popupOpen() {
		return nil
	}
	m.help = false
	m.projPick = newProjectPicker(m.projects, m.sel.id)
	m.projPick.offline = m.phase != phaseConnected
	return m.fetchProjectPicker()
}

// fetchProjectPicker is one refresh of the open picker's rows.
func (m *root) fetchProjectPicker() tea.Cmd {
	if m.client == nil {
		return nil
	}
	m.projPickSeq++
	return fetchProjectPicker(m.client, m.projPickSeq)
}

// scheduleProjectPicker coalesces a burst of events into one refetch, the
// projects view's debounce (board.go's refreshDebounce). Closed, it fetches
// nothing: the picker's figures are only worth a request while they are on
// screen.
func (m *root) scheduleProjectPicker() tea.Cmd {
	if m.projPick == nil || m.projPickPending {
		return nil
	}
	m.projPickPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return projectPickerRefreshMsg{} })
}

// projectPickerEvent reports whether an event can move a picker figure: a
// task's state, an issue's, a chat's, or the project list itself.
func projectPickerEvent(typ string) bool {
	for _, prefix := range []string{"task.", "issue.", "chat.", "project."} {
		if strings.HasPrefix(typ, prefix) {
			return true
		}
	}
	return false
}

// updateProjectPickerKey routes keys into the open project picker and makes
// its pick the selection — by selectProject, the one path every switch takes.
func (m *root) updateProjectPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pick, done, cmd := m.projPick.update(msg)
	if done {
		m.projPick = nil
	}
	if pick == nil {
		return m, cmd
	}
	if pick.ID == m.sel.id && pick.Name == m.sel.name {
		return m, cmd
	}
	return m, tea.Batch(cmd, m.selectProject(*pick, "picked in the project picker"))
}

// surface is what the palette and the help sheet both need to know about the
// active surface beyond its binding context. One helper builds it for both,
// so the sheet's "actions now" and the palette's action group cannot drift
// (task 129.17).
type surface struct {
	target   taskActions
	editable bool
	live     func([]binding) []binding
	// tabs is the task workspace's strip as drawn; nil elsewhere.
	tabs []taskViewTab
	// extras are the surface's key-less palette rows (task 130.13).
	extras []paletteEntry
}

// surfaceTarget reads the active surface. Nothing can act on a task the
// daemon cannot see, so off a live connection the target is empty.
func (m *root) surfaceTarget() surface {
	var s surface
	if sh, ok := m.views[m.active].(*shell); ok {
		s.target = sh.board.target()
		s.editable = sh.detail.stepEditable()
		s.live = sh.liveBindings
	} else if t, ok := m.views[m.active].(*taskView); ok {
		s.target = t.target()
		s.editable = t.detail.stepEditable()
		s.live = t.liveBindings
		s.tabs = t.tabs()
		s.extras = t.paletteExtras()
	} else if lb, ok := m.views[m.active].(liveBinder); ok {
		s.live = lb.liveBindings
	}
	if m.phase != phaseConnected {
		s.target = taskActions{}
	}
	return s
}

// openPalette builds the palette for the active surface.
func (m *root) openPalette() {
	s := m.surfaceTarget()
	entries := paletteEntries(
		m.activeContext(), s.target, s.editable, m.phase == phaseConnected, m.githubAvailable(), s.live, s.tabs)
	m.palette = newPalette(append(entries, s.extras...))
}

// liveBinder is a surface whose registry rows depend on its state: it drops
// the ones whose keys do nothing right now, so neither the footer nor the
// palette offers them (issue #372).
type liveBinder interface {
	liveBindings(rows []binding) []binding
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
		if c, ok := m.views[viewNewTask].(contextual); ok {
			return c.bindingContext()
		}
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
	case viewArchived:
		return ctxArchived
	case viewArchivedChats:
		return ctxArchivedChats
	case viewChats:
		return m.views[viewChats].(*chatsView).bindingContext()
	case viewChat:
		return m.views[viewChat].(*chatView).bindingContext()
	case viewTriggers:
		return m.views[viewTriggers].(*triggersView).bindingContext()
	case viewIssues:
		return m.views[viewIssues].(*issuesView).bindingContext()
	case viewIssue:
		return m.views[viewIssue].(*issueView).bindingContext()
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
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	}
	// A `tui.keys` override may bind an operation to any key keymap.ParseKey
	// accepts (task 118), and the palette replays whatever the registry says:
	// alt+ and the navigation names have to round-trip too.
	if rest, ok := strings.CutPrefix(key, "alt+"); ok {
		msg := synthKey(rest)
		msg.Mod |= tea.ModAlt
		if rest != "space" && len([]rune(rest)) == 1 {
			msg.Text = ""
		}
		return msg
	}
	if code, ok := namedKeyCodes[key]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	// Any ctrl+<letter>, rather than a case per key. The named list above had
	// grown three of them and was already one behind the registry: ctrl+r and
	// ctrl+g are ctxChat rows the palette lists, and replaying either through
	// the default arm below synthesized `c` with the whole label as its text
	// (task 074). A rule the registry cannot outrun is the fix.
	if mod, ok := strings.CutPrefix(key, "ctrl+"); ok && len([]rune(mod)) == 1 {
		return tea.KeyPressMsg{Code: []rune(mod)[0], Mod: tea.ModCtrl}
	}
	// Any function key, by the same reasoning. The default arm would read f1
	// as the letter f carrying the text "f1" — a press no terminal sends, and
	// one a Code match would take for `f` (task 114).
	if n, ok := strings.CutPrefix(key, "f"); ok {
		if i, err := strconv.Atoi(n); err == nil && i >= 1 && i <= 63 {
			return tea.KeyPressMsg{Code: tea.KeyF1 + rune(i-1)}
		}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}

// namedKeyCodes are the navigation keys synthKey rebuilds by name.
var namedKeyCodes = map[string]rune{
	"left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd,
	"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "backspace": tea.KeyBackspace,
	"delete": tea.KeyDelete, "insert": tea.KeyInsert,
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

// openNewTask opens the §15 new-task form on the selected project, from
// every view: the form's project row is the selection, read-only (task
// 132.13, decision 9), so there is nothing for a view to hint any more. The
// form must be told to open before it is shown: opening is what resets the
// draft and fetches the catalogs.
func (m *root) openNewTask() tea.Cmd {
	cmd := m.deliver(viewNewTask, newTaskMsg{projectID: m.sel.id})
	return tea.Batch(cmd, m.switchTo(viewNewTask))
}

// updateNewTaskFromPull opens the new-task form seeded with a pull request
// (task 064). It goes through the root for the reason openNewTask does: the
// form has to be told to open before it is shown, because opening is what
// resets the draft and fetches the catalogs.
//
// It goes through openObject as well (task 132.13): the seed names its
// project, and the form's project row is locked to the selection, so a seed
// of another project switches first and opens second (decision 38). Every
// seed today comes from a view already on its project, so this is the guard
// that keeps a locked field from ever disagreeing with the header.
func (m *root) updateNewTaskFromPull(msg newTaskFromPullMsg) (tea.Model, tea.Cmd) {
	return m, m.openObject(msg)
}

// updateNewTaskFromIssue opens the new-task form seeded with a vincent issue
// (task 130.13), through the root for updateNewTaskFromPull's reasons.
func (m *root) updateNewTaskFromIssue(msg newTaskFromIssueMsg) (tea.Model, tea.Cmd) {
	return m, m.openObject(msg)
}

// updateNewTaskFromChat opens the form as a chat's handoff form (task 074),
// through the root for updateNewTaskFromPull's reasons.
func (m *root) updateNewTaskFromChat(msg newTaskFromChatMsg) (tea.Model, tea.Cmd) {
	return m, m.openObject(msg)
}

// routeOpen performs one open once its project is settled: openObject and a
// confirmed pending switch both end here.
func (m *root) routeOpen(open tea.Msg) tea.Cmd {
	switch msg := open.(type) {
	case selectTaskMsg:
		// The board keeps the row selected while the dedicated task workspace
		// loads the authoritative detail snapshot.
		m.selectedTask = msg.id
		return tea.Batch(
			m.deliver(viewHome, msg),
			m.deliver(viewTask, msg),
			m.switchTo(viewTask),
		)
	case openTaskMsg:
		// A jump made inside the task workspace (#316): the lane or parent
		// opens by exactly the path every other task opens by, with the task
		// it was reached from pushed onto the workspace's back stack first.
		// Pushing here rather than inside the message is what keeps
		// selectTaskMsg — the board's, and shared with the palette and the
		// pull-request takeover — unchanged. A jump that followed its task
		// into another project arrives with from cleared: a switch empties
		// the stack (task 132.6).
		if v, ok := m.views[viewTask].(*taskView); ok {
			v.pushTask(msg.from)
			if msg.failure {
				v.pendingFailure = msg.id
			}
		}
		m.selectedTask = msg.id
		sel := selectTaskMsg{id: msg.id, state: msg.state, projectID: msg.projectID}
		return tea.Batch(
			m.deliver(viewHome, sel),
			m.deliver(viewTask, sel),
			m.switchTo(viewTask),
		)
	case openIssueMsg:
		// The detail is pointed at the issue before it becomes active, for
		// openChatMsg's reason: an inactive view receives nothing.
		if v, ok := m.views[viewIssue].(*issueView); ok {
			v.back = msg.back
			return tea.Batch(v.open(msg.id), m.switchTo(viewIssue))
		}
	case openChatMsg:
		// The chat workspace is not the active view yet, and an inactive
		// view receives nothing — so the root points it at the chat first
		// and switches second, the same order every takeover that carries an
		// argument uses.
		if v, ok := m.views[viewChat].(*chatView); ok {
			return tea.Batch(v.open(msg.id), m.switchTo(viewChat))
		}
	case newTaskFromPullMsg, newTaskFromIssueMsg, newTaskFromChatMsg:
		// A seeded form, once its project is the selection.
		return tea.Batch(m.deliver(viewNewTask, msg), m.switchTo(viewNewTask))
	case taskCreatedMsg:
		// Landing on the task that was just created: creating a task is the
		// beginning of watching it, and the 201's warnings ride along so an
		// advisory finding is not lost on a board row.
		m.selectedTask = msg.task.ID
		sel := selectTaskMsg{id: msg.task.ID, state: msg.task.State, projectID: msg.task.ProjectID}
		return tea.Batch(
			m.deliver(viewHome, sel),
			m.deliver(viewHome, msg),
			m.deliver(viewTask, sel),
			m.deliver(viewTask, msg),
			m.switchTo(viewTask),
		)
	}
	return nil
}

// openProjectID is the project an open says its object belongs to.
func openProjectID(open tea.Msg) int64 {
	switch msg := open.(type) {
	case selectTaskMsg:
		return msg.projectID
	case openTaskMsg:
		return msg.projectID
	case openIssueMsg:
		return msg.projectID
	case openChatMsg:
		return msg.projectID
	case taskCreatedMsg:
		return msg.task.ProjectID
	case newTaskFromPullMsg:
		return msg.projectID
	case newTaskFromIssueMsg:
		return msg.projectID
	case newTaskFromChatMsg:
		return msg.chat.ProjectID
	}
	return 0
}

// withProjectID is open with its project filled in, as a fetch learned it.
func withProjectID(open tea.Msg, id int64) tea.Msg {
	switch msg := open.(type) {
	case selectTaskMsg:
		msg.projectID = id
		return msg
	case openTaskMsg:
		msg.projectID = id
		return msg
	case openIssueMsg:
		msg.projectID = id
		return msg
	case openChatMsg:
		msg.projectID = id
		return msg
	}
	return open
}

// openObject follows an open's object into its project (task 132.6,
// decision 38): an object of another known project switches the selection
// first, by selectProject's rules, and routes second, so a detail never
// draws under the wrong header. An open that does not say, or names a
// project the cached list does not hold, has its object fetched first.
func (m *root) openObject(open tea.Msg) tea.Cmd {
	id := openProjectID(open)
	if id != 0 && id == m.sel.id {
		return m.routeOpen(open)
	}
	if p, ok := m.knownProject(id); ok {
		return m.followTo(p, open, "")
	}
	if m.client == nil {
		// Nothing to ask: the open is routed as it stands.
		return m.routeOpen(open)
	}
	return fetchOpenProject(m.client, open)
}

// knownProject finds id in the cached project list.
func (m *root) knownProject(id int64) (apiclient.Project, bool) {
	if id == 0 {
		return apiclient.Project{}, false
	}
	for _, p := range m.projects {
		if p.ID == id {
			return p, true
		}
	}
	return apiclient.Project{}, false
}

// followFetchedMsg answers fetchOpenProject: the open, and the project its
// object turned out to belong to.
type followFetchedMsg struct {
	open      tea.Msg
	projectID int64
	err       error
}

// fetchOpenProject GETs an open's object to learn its project.
func fetchOpenProject(client *apiclient.Client, open tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var id int64
		var err error
		switch msg := open.(type) {
		case selectTaskMsg:
			var t apiclient.TaskDetail
			t, err = client.GetTask(ctx, msg.id)
			id = t.ProjectID
		case openTaskMsg:
			var t apiclient.TaskDetail
			t, err = client.GetTask(ctx, msg.id)
			id = t.ProjectID
		case openChatMsg:
			var c *apiclient.Chat
			c, _, err = client.GetChat(ctx, msg.id)
			if c != nil {
				id = c.ProjectID
			}
		case openIssueMsg:
			var iss apiclient.Issue
			iss, err = client.GetIssue(ctx, msg.id, "")
			id = iss.ProjectID
		}
		return followFetchedMsg{open: open, projectID: id, err: err}
	}
}

// updateFollowFetched resumes an open once its object's project is known. A
// fetch that failed, or a project still unknown, routes the open as it
// stands: the detail then shows whatever error its own load meets.
func (m *root) updateFollowFetched(msg followFetchedMsg) tea.Cmd {
	if msg.err != nil || msg.projectID == 0 || msg.projectID == m.sel.id {
		return m.routeOpen(msg.open)
	}
	p, ok := m.knownProject(msg.projectID)
	if !ok {
		return m.routeOpen(msg.open)
	}
	return m.followTo(p, withProjectID(msg.open, msg.projectID), "")
}

// crossAttention is `!` leaving the selected project (task 132 decision 52):
// once the selection's attention tasks are exhausted — or it has none — the
// press goes to the next project by name that has one, wrapping, and opens
// its first attention task in board order. The open follows the object
// (decision 38), so a dirty draft asks first (decision 36), and raises a
// notice naming the switch. ok is false when no other project has an
// attention task: the press then stays in the selection, which is the wrap
// back to its first.
func (m *root) crossAttention() (tea.Cmd, bool) {
	s, ok := m.views[viewHome].(*shell)
	if !ok {
		return nil, false
	}
	if _, wrapped, _ := s.nextAttention(); !wrapped {
		return nil, false
	}
	for _, p := range projectsAfter(m.projects, m.sel.id) {
		targets := s.board.attentionTargets(p.ID)
		if len(targets) == 0 {
			continue
		}
		t := targets[0]
		open := selectTaskMsg{id: t.ID, state: t.State, projectID: p.ID, attention: true}
		return m.followTo(p, open, attentionNotice(p.Name, t.ID)), true
	}
	return nil, false
}

// projectsAfter is every project but the selected one, by name (the
// picker's reading order), starting after the selection and wrapping.
func projectsAfter(projects []apiclient.Project, sel int64) []apiclient.Project {
	sorted := slices.Clone(projects)
	slices.SortStableFunc(sorted, func(a, b apiclient.Project) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	at := slices.IndexFunc(sorted, func(p apiclient.Project) bool { return p.ID == sel })
	if at < 0 {
		return sorted
	}
	return append(sorted[at+1:], sorted[:at]...)
}

// attentionNotice is the line a `!` that crossed projects raises.
func attentionNotice(name string, task int64) string {
	return fmt.Sprintf("%s — switched to `%s` (task #%d needs you)", attentionBadge, name, task)
}

// followTo switches to p and then routes open, unless the active view holds
// a draft: then the switch waits on the confirmation, and the open with it.
// A created task is never guarded: its draft is the one just sent, so there
// is nothing left to discard, and asking would strand the form on
// "creating…" with the task already made (review F1).
// notice, when set, replaces the default followedNotice.
func (m *root) followTo(p apiclient.Project, open tea.Msg, notice string) tea.Cmd {
	_, created := open.(taskCreatedMsg)
	if draft, dirty := m.activeDraft(); dirty && !created && m.sel.id != 0 {
		m.pending = &pendingSwitch{project: p, why: whyFollowed, draft: draft, open: open, notice: notice}
		return nil
	}
	return m.applyFollow(p, open, notice)
}

// applyFollow is a follow's switch, its open and its notice. A workspace
// jump loses the task it came from: the stack is the old project's.
func (m *root) applyFollow(p apiclient.Project, open tea.Msg, notice string) tea.Cmd {
	if o, ok := open.(openTaskMsg); ok {
		o.from = 0
		open = o
	}
	cmd := m.applySwitch(p, whyFollowed)
	if notice == "" {
		notice = followedNotice(p.Name)
	}
	m.selNotice, m.selNoticeWarn = notice, false
	return tea.Batch(cmd, m.routeOpen(open))
}

// updatePendingSwitchKey answers the draft confirmation: y discards the draft
// and applies the switch (and the open waiting on it), n or esc keeps both
// the draft and the selection, and every other key is swallowed.
func (m *root) updatePendingSwitchKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.pending
	switch msg.String() {
	case "y":
		m.pending = nil
		if p.deleted != "" {
			return m.applyDeletedSwitch(p.project, p.why, p.notice)
		}
		if p.open != nil {
			return m.applyFollow(p.project, p.open, p.notice)
		}
		return m.applySwitch(p.project, p.why)
	case "n", "esc":
		// A deleted selection has nowhere to stay (decision 47): the
		// question waits for y.
		if p.deleted == "" {
			m.pending = nil
		}
	}
	return nil
}

// activeDraft is the active view's switchGuard answer.
func (m *root) activeDraft() (string, bool) {
	if g, ok := m.views[m.active].(switchGuard); ok {
		return g.switchDraft()
	}
	return "", false
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

// probeGitHub starts a GitHub probe fan-out stamped with the next probeSeq.
func (m *root) probeGitHub() tea.Cmd {
	m.probeSeq++
	return probeGitHubCmd(m.client, m.probeSeq)
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
	cmds := []tea.Cmd{waitNote(m.notes), m.probeGitHub()}
	for i := range m.views {
		if ca, ok := m.views[i].(clientAware); ok {
			if cmd := ca.setClient(m.client); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	// After setClient, so a view connected late still learns the selection
	// before it acts on its new client's answers (task 132.2).
	cmds = append(cmds, m.scopeViews(), m.refreshProjects())
	return m, tea.Batch(cmds...)
}

// projectListMsg is one answer to refreshProjects.
type projectListMsg struct {
	seq      int
	projects []apiclient.Project
	err      error
}

// refreshProjects refetches the project list the selection is checked
// against. No stats: the list is only names and ids here.
func (m *root) refreshProjects() tea.Cmd {
	client := m.client
	if client == nil {
		return nil
	}
	m.projectsSeq++
	seq := m.projectsSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx)
		return projectListMsg{seq: seq, projects: projects, err: err}
	}
}

// updateProjectList adopts a fresh project list. The first one feeds the
// task 132.3 startup chain, which picks the selection once the first config
// answer is in as well. After that, with nothing selected, the first project
// by name is selected (task 132 decision 10: a new project only auto-selects
// when nothing is). A selected project that is still listed has its name
// refreshed, so a rename reaches the header and tui.json. One that has
// vanished was deleted (task 132.7): the list is the one source of truth, so
// a delete made by another client, or during an outage whose event the
// stream never replayed, is caught here as well as a live one (decision 49).
// A failed fetch leaves the previous list standing, for githubProbeMsg's
// reason.
func (m *root) updateProjectList(msg projectListMsg) tea.Cmd {
	if msg.err != nil || msg.seq != m.projectsSeq {
		return nil
	}
	m.projects = msg.projects
	for i := range m.views {
		if pa, ok := m.views[i].(projectListAware); ok {
			pa.setProjects(m.projects)
		}
	}
	if !m.startup.done {
		m.startup.listed = true
		return m.maybeResolveStartup()
	}
	if m.sel.id == 0 {
		first, ok := firstProjectByName(m.projects)
		if !ok {
			return nil
		}
		return m.selectProject(first, whyFirstName)
	}
	for _, p := range m.projects {
		if p.ID == m.sel.id {
			m.revalidatePending()
			if p.Name == m.sel.name {
				return nil
			}
			m.sel.name = p.Name
			return tea.Batch(m.scopeViews(), m.saveSelection())
		}
	}
	return m.selectionDeleted()
}

// selectionDeleted replaces a selection the project list no longer carries:
// tui.default_project when it names a registered project, else the first by
// name, else nothing (task 132 decision 46). A draft on the active view still
// asks first (decision 47), with no way to stay: the project is gone.
func (m *root) selectionDeleted() tea.Cmd {
	gone := m.sel.name
	pick := reselectAfterDelete(m.projects, m.defaultProject, gone)
	if draft, dirty := m.activeDraft(); dirty {
		m.pending = &pendingSwitch{
			project: pick.project, why: pick.why, draft: draft,
			deleted: gone, notice: pick.notice,
		}
		return nil
	}
	return m.applyDeletedSwitch(pick.project, pick.why, pick.notice)
}

// revalidatePending checks a pending switch's target against the fresh list
// while the selection itself is still listed (review F2). A target deleted
// while the question was open is dropped, with the draft and the selection
// kept and a notice saying why: answering y would otherwise select a project
// that no longer exists, and the event that said so has been consumed. A
// renamed target takes its new name, so the prompt and the switch carry it.
// A deleted selection's own question is not this one's: selectionDeleted
// recomputes it on every relist.
func (m *root) revalidatePending() {
	p := m.pending
	if p == nil || p.deleted != "" {
		return
	}
	for _, q := range m.projects {
		if q.ID == p.project.ID {
			p.project = q
			return
		}
	}
	m.pending = nil
	m.selNotice = fmt.Sprintf("project `%s` was deleted — staying on `%s`", p.project.Name, m.sel.name)
	m.selNoticeWarn = true
}

// applyDeletedSwitch is selectionDeleted past its guard. A zero p is "no
// project": applySwitch still counts it as a switch away from the deleted
// one, so a detail view falls back to its list and a form is re-aimed.
func (m *root) applyDeletedSwitch(p apiclient.Project, why, notice string) tea.Cmd {
	cmd := m.applySwitch(p, why)
	m.selNotice, m.selNoticeWarn = notice, true
	return cmd
}

// reloadFailedViews reloads every projectScoped view whose last load failed,
// walked directly in view order as scopeCmds does (task 132 decision 48).
func (m *root) reloadFailedViews() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.views {
		if fr, ok := m.views[i].(failedReloader); ok {
			if cmd := fr.reloadIfFailed(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return tea.Batch(cmds...)
}

// selectProject makes p the selection, tells every project-bearing view, and
// records it as the last used project (task 132 decision 31) — the one place
// the selection changes, so every change is persisted, the startup chain's
// own pick included. why is the rule that chose it.
//
// A switch to another project keeps the active view (task 132.6): a view
// holding a draft first asks, through the pending switch, and nothing changes
// until the answer is y.
func (m *root) selectProject(p apiclient.Project, why string) tea.Cmd {
	if m.sel.id != 0 && p.ID != m.sel.id {
		if draft, dirty := m.activeDraft(); dirty {
			m.pending = &pendingSwitch{project: p, why: why, draft: draft}
			return nil
		}
	}
	return m.applySwitch(p, why)
}

// applySwitch is selectProject past its guard.
func (m *root) applySwitch(p apiclient.Project, why string) tea.Cmd {
	// Coming from no selection at all is not a switch away from anything:
	// nothing on screen belongs to another project, so the view is left be.
	switched := m.sel.id != 0 && p.ID != m.sel.id
	m.sel = projectSel{id: p.ID, name: p.Name}
	m.selWhy = why
	cmds := append(m.scopeCmds(), m.saveSelection())
	if switched {
		cmds = append(cmds, m.keepViewAcrossSwitch())
	}
	return tea.Batch(cmds...)
}

// keepViewAcrossSwitch applies a switch to the active view (task 132.6, spec
// §15): a detail screen falls back to its list, a form is re-aimed at the new
// selection, and every other view stays — the lists have re-scoped through
// setProject already, and the daemon view and the projects overview are not
// project-bearing.
func (m *root) keepViewAcrossSwitch() tea.Cmd {
	v := m.views[m.active]
	if to, ok := switchFallback[m.active]; ok {
		if l, ok := v.(switchLeaving); ok {
			l.leaveForSwitch()
		}
		return m.switchTo(to)
	}
	if r, ok := v.(switchRetargeting); ok {
		return r.retarget(m.sel)
	}
	return nil
}

// scopeViews hands the selection to every projectScoped view, directly and
// in view order, and batches what they return.
func (m *root) scopeViews() tea.Cmd { return tea.Batch(m.scopeCmds()...) }

func (m *root) scopeCmds() []tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.views {
		if ps, ok := m.views[i].(projectScoped); ok {
			if cmd := ps.setProject(m.sel); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

func (m *root) updateNote(n apiclient.Note) (tea.Model, tea.Cmd) {
	var reprobe, relist, repick, reload tea.Cmd
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
			reprobe = m.probeGitHub()
			relist = m.refreshProjects()
			// Only the views whose last load failed — a switch made
			// offline among them — refetch. The selection is kept and
			// is checked against the relisted projects when they land.
			reload = m.reloadFailedViews()
		}
		// Distinct from phaseConnected, which only means the health probe
		// answered: this is the event stream itself being established, and
		// until it is, a committed event will not reach us (a stream with no
		// Last-Event-ID starts live at the *next* event, §13.3).
		m.streamLive = true
		m.setConnected(true)
		if m.projPick != nil {
			m.projPick.offline = false
		}
	case apiclient.DisconnectedNote:
		m.phase = phaseReconnecting
		m.streamLive = false
		m.connErr = n.Err
		m.retryIn = n.RetryIn
		m.setConnected(false)
		if m.projPick != nil {
			m.projPick.offline = true
		}
	case apiclient.EventNote:
		// Observed, not consumed: the views that list projects hear the
		// note through the broadcast below as before.
		if strings.HasPrefix(n.Event.Type, "project.") {
			relist = m.refreshProjects()
			// The pull-requests gate follows the selected project's probe
			// (task 132.11), so a project registered or re-pointed
			// mid-session needs an answer of its own before it is selected.
			if reprobe == nil {
				reprobe = m.probeGitHub()
			}
		}
		if projectPickerEvent(n.Event.Type) {
			repick = m.scheduleProjectPicker()
		}
	}
	// Every view sees the note, not just the visible one.
	return m, tea.Batch(m.broadcast(noteMsg{note: n}), waitNote(m.notes), reprobe, relist, repick, reload)
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
	if _, ok := m.views[prev].(projectScoped); ok {
		m.lastScoped = prev
	}
	return tea.Batch(
		m.deliver(prev, viewDeactivatedMsg{id: prev}),
		m.deliver(id, viewActivatedMsg{id: id}),
	)
}

// openFromOverview is enter on the project overview (task 132 decision 42).
// A project row selects the project and returns to the last project-scoped
// view; a "needs you" row selects the task's project and opens the task, with
// esc coming back to the overview. Both select through selectProject, the
// one path every switch takes.
func (m *root) openFromOverview(msg overviewPickMsg) tea.Cmd {
	same := msg.project.ID == m.sel.id && msg.project.Name == m.sel.name
	if msg.task != nil {
		var sel tea.Cmd
		if !same {
			sel = m.selectProject(msg.project, "picked in the project overview")
		}
		open := selectTaskMsg{
			id: msg.task.ID, state: msg.task.State, back: viewProjects,
			projectID: msg.task.ProjectID,
		}
		return tea.Batch(sel, func() tea.Msg { return open })
	}
	// The return view is made active before the switch, so the switch
	// applies task 132.6's rules to it rather than to the overview: a record
	// gives way to its list, a form is re-aimed, and a draft asks first
	// (decisions 36 and 37).
	back := m.switchTo(m.lastScoped)
	if same {
		return back
	}
	return tea.Batch(back, m.selectProject(msg.project, "picked in the project overview"))
}

// applyOutputLevel adopts `tui.output.level` from whichever config answer
// arrived (task 129.11): the first successful one sets the level, and later
// ones only when the configured value changed, so the refetch every
// reconnect makes never undoes a `v` press while an edit to the key — by
// file, the config editor or `vincent config` — applies live. A failed fetch
// or a refused save changes nothing, for applyHyperlinks' reason.
func (m *root) applyOutputLevel(msg tea.Msg) {
	if m.level == nil {
		return
	}
	switch msg := msg.(type) {
	case boardConfigMsg:
		if msg.err == nil {
			m.level.adopt(msg.outputLevel)
		}
	case daemonConfigMsg:
		if msg.err == nil {
			m.level.adopt(msg.config.TUI.Output.Level)
		}
	case configSavedMsg:
		if msg.err == nil {
			m.level.adopt(msg.cfg.TUI.Output.Level)
		}
	}
}

// applyHyperlinks adopts `tui.hyperlinks` from whichever config answer
// arrived (task 111). A failed fetch or a refused save changes nothing, for
// the reason board.applyConfig gives: a request that failed is not a
// statement about the setting.
func (m *root) applyHyperlinks(msg tea.Msg) {
	if m.links == nil {
		return
	}
	switch msg := msg.(type) {
	case boardConfigMsg:
		if msg.err == nil {
			m.links.set(msg.hyperlinks)
		}
	case daemonConfigMsg:
		if msg.err == nil {
			m.links.set(msg.config.TUI.Hyperlinks)
		}
	case configSavedMsg:
		if msg.err == nil {
			m.links.set(msg.cfg.TUI.Hyperlinks)
		}
	}
}

// applyKeymap installs `tui.keys` wherever the TUI already reads `tui:` (task
// 118 decision 9): the board's fetch on every connect and reconnect, the
// daemon view's, and the answer to the config editor's own PATCH — so a
// binding changed from the editor works on the next press, with no reconnect.
// A failed fetch or a refused save changes nothing, for applyHyperlinks'
// reason.
func (m *root) applyKeymap(msg tea.Msg) {
	switch msg := msg.(type) {
	case boardConfigMsg:
		if msg.err == nil {
			m.noticeKeys(applyKeys(msg.keys))
		}
	case daemonConfigMsg:
		if msg.err == nil {
			m.noticeKeys(applyKeys(msg.config.TUI.Keys))
		}
	case configSavedMsg:
		if msg.err == nil {
			m.noticeKeys(applyKeys(msg.cfg.TUI.Keys))
		}
	}
}

// noticeKeys raises the one-time keymap line for a warning set it has not
// raised before (task 128 decision 4). Every config fetch re-applies the
// keymap, so without the memory a reconnect would raise it again.
func (m *root) noticeKeys(warnings []string) {
	joined := strings.Join(warnings, "\n")
	if joined == m.keysNoticed {
		return
	}
	m.keysNoticed = joined
	if len(warnings) == 0 {
		m.keysNotice = ""
		return
	}
	more := ""
	if len(warnings) > 1 {
		more = fmt.Sprintf(" (+%d more — `vincent doctor` lists them)", len(warnings)-1)
	}
	m.keysNotice = " ⚠ tui.keys: " + warnings[0] + more
}

// statusLine is the line under the header, when there is one: the full-auto
// notice's failed acknowledgment, else a pending switch's confirmation, else
// the one-time keymap notice, else the startup-project notice — which a
// follow's `switched to` line reuses.
func (m *root) statusLine() (string, bool) {
	if line, ok := m.notice.statusLine(); ok {
		return line, true
	}
	if m.pending != nil {
		return styleWarn.Render(" " + m.pending.prompt()), true
	}
	if m.keysNotice != "" {
		return styleWarn.Render(m.keysNotice), true
	}
	if m.selNotice != "" {
		if m.selNoticeWarn {
			return styleWarn.Render(" ⚠ " + m.selNotice), true
		}
		return styleDim.Render(" " + m.selNotice), true
	}
	return "", false
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
	if line, ok := m.statusLine(); ok {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if line, ok := m.nowLine(); ok {
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

// headerTagger is a view that names itself in the app header with more than
// a bracketed title — the task workspace's breadcrumb (task 129.9). Such a
// view's frame is drawn untitled, since the header already says what it is.
type headerTagger interface {
	headerTag(width int) string
}

// nowLiner is a view with a live line to show under the app header while its
// subject is running (task 129.9).
type nowLiner interface {
	nowLine(width int) (string, bool)
}

func (m *root) headerLine() string {
	width := m.width
	if width <= 0 {
		width = 1 << 16
	}
	// Connected is the normal case and says nothing a working screen does
	// not (task 129.15): the badge is drawn only while it is news, and being
	// news it is never shed.
	badge := ""
	if m.phase != phaseConnected {
		badge = m.connBadge() + "  "
	}
	lead := func(name string) string { return " " + styleTitle.Render(name) + "  " + badge }
	full := "vincent"
	if m.version != "" {
		full += " " + m.version
	}
	project := "no project"
	if m.sel.id != 0 {
		project = m.sel.name
	}
	segment := func(name string) string { return styleTitle.Render(headerProjectGlyph + " " + name) }
	elsewhere := m.elsewhereBadge()

	// Shedding order (task 132.2, extended by 132.14): the view tag
	// truncates down to a floor and is then dropped; then the version goes;
	// then the elsewhere badge; last, the project name truncates behind an
	// ellipsis.
	// hit records where the segment landed, for the header click. It never
	// covers the badge: a click there is not a click on the project.
	hit := func(l, seg string) {
		x0 := ansi.StringWidth(l)
		m.headerHit = [2]int{x0, x0 + ansi.StringWidth(seg)}
	}
	head := lead(full) + segment(project) + elsewhere
	if room := width - ansi.StringWidth(head) - 2; room >= headerTagFloor || room >= ansi.StringWidth(m.headerTagFull()) {
		hit(lead(full), segment(project))
		return head + "  " + styleDim.Render(m.headerTag(room))
	}
	if ansi.StringWidth(head) <= width {
		hit(lead(full), segment(project))
		return head
	}
	if short := lead("vincent") + segment(project) + elsewhere; elsewhere != "" && ansi.StringWidth(short) <= width {
		hit(lead("vincent"), segment(project))
		return short
	}
	l := lead(full)
	if ansi.StringWidth(l+segment(project)) > width {
		l = lead("vincent")
	}
	room := width - ansi.StringWidth(l+segment(""))
	seg := segment(ansi.Truncate(project, max(room, 1), "…"))
	hit(l, seg)
	return l + seg
}

// elsewhereBadge is the app header's `(! N elsewhere)` (task 132.14,
// decision 2): how many tasks in projects other than the selected one need
// a human, from the board's global live listing (decision 17), so it costs
// no fetch of its own. Drawn on every view, omitted at zero.
func (m *root) elsewhereBadge() string {
	s, ok := m.views[viewHome].(*shell)
	if !ok {
		return ""
	}
	n := s.board.attentionTally().elsewhere
	if n <= 0 {
		return ""
	}
	return " " + styleAsk.Render(fmt.Sprintf("(%s %d elsewhere)", attentionBadge, n))
}

// headerProjectGlyph marks the selected-project segment of the app header. A
// glyph rather than a colour, so the segment reads the same uncoloured.
const headerProjectGlyph = "◆"

// headerTagFloor is the narrowest the view tag is truncated to before the
// header drops it altogether: below it a tag is an ellipsis, not a name.
const headerTagFloor = 8

// headerTagFull is the active view's tag at unbounded width.
func (m *root) headerTagFull() string { return m.headerTag(1 << 16) }

// headerTag is the active view's tag fitted to width: a headerTagger's own
// breadcrumb, or the bracketed title truncated.
func (m *root) headerTag(width int) string {
	if t, ok := m.views[m.active].(headerTagger); ok {
		return t.headerTag(max(width, 1))
	}
	return ansi.Truncate("["+m.views[m.active].title()+"]", max(width, 1), "…")
}

// nowLine is the active view's live line, when it has one to show. It is
// chrome under the header rather than part of the view's body, so the root
// accounts for its row in bodyHeight and in click routing.
func (m *root) nowLine() (string, bool) {
	if m.phase != phaseConnected {
		return "", false
	}
	n, ok := m.views[m.active].(nowLiner)
	if !ok {
		return "", false
	}
	return n.nowLine(max(m.width, 1))
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
		// It scrolls (task 129.17): the frame shows a window of it from
		// helpScroll, with a cue on the last row when there is more.
		ctx := m.activeContext()
		h := m.bodyHeight()
		if m.width < 4 || h < 3 {
			return m.helpSheet()
		}
		lines, visible := m.helpWindow()
		from := min(m.helpScroll, max(len(lines)-visible, 0))
		shown := lines[from:min(from+visible, len(lines))]
		if len(lines) > visible {
			shown = append(shown[:len(shown):len(shown)], helpScrollCue(from, visible, len(lines)))
		}
		return frame(helpTitle(ctx), strings.Join(shown, "\n"), m.width, h, true)
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
		// r is retry-connecting, fixed; the palette and quit are rebindable
		// (task 118 decision 8).
		return fmt.Sprintf(
			"\n  %s\n\n  log: %s\n\n  press r to retry, %s for the daemon view and its log, %s to quit\n",
			styleBad.Render("daemon unreachable: "+errString(m.connErr)), m.logPath,
			opKey(keymap.Palette), opKey(keymap.Quit))
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
	title := m.views[id].title()
	if _, ok := m.views[id].(headerTagger); ok {
		title = ""
	}
	return frame(title, content, m.width, h, true)
}

// quitReminder is §15's exit line: the daemon keeps working after the TUI
// closes, and the running count is what makes that worth saying. It is a
// line printed after teardown rather than a prompt before it — quitting is
// non-destructive by construction, and a confirmation on a harmless action
// is friction.
//
// The figure is the board's, which is the daemon's §11 slot count and not a
// walk of the listed rows (board.slotsUsed, issue #324): a walk tells the
// person leaving that nothing is running while six fan-out lanes are.
//
// Nothing is printed at zero, and nothing when the board never loaded: "0
// tasks running" from a TUI that never reached the daemon is a false
// statement, not a reassuring one.
func (m *root) quitReminder() (string, bool) {
	s, ok := m.views[viewHome].(*shell)
	if !ok || !s.board.loaded {
		return "", false
	}
	n := s.board.slotsUsed()
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

// githubAvailable reports whether the selected project answered the §13.2
// probe with available: true (task 132.11). It is what withholds the
// pull-requests nav row and the workspace's pull-request keys, so all of
// them follow a project switch.
func (m *root) githubAvailable() bool {
	st, ok := githubStatusFor(m.github, m.sel.id)
	return ok && st.Available
}

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
		attention attentionTally
	)
	if s, ok := m.views[viewHome].(*shell); ok {
		attention = s.board.attentionTally()
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
	if lb, ok := m.views[m.active].(liveBinder); ok {
		rows = lb.liveBindings(rows)
	}
	retry := m.phase == phaseFailed || m.phase == phaseReconnecting
	line, hits := buildFooter(m.width, rows, bar, target, attention, retry, m.activeCapturesInput())
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
	chrome := m.chromeTop() + 1
	if m.height <= chrome {
		return 0
	}
	return m.height - chrome
}

// chromeTop is the rows above the body: the header, the status line when
// one is up, and the now-line while it is shown.
func (m *root) chromeTop() int {
	n := 1
	if _, ok := m.statusLine(); ok {
		n++
	}
	if _, ok := m.nowLine(); ok {
		n++
	}
	return n
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
	case m.linkPick != nil:
		return m.linkPick.render, true
	case m.palette != nil:
		return m.palette.render, true
	case m.projPick != nil:
		return m.projPick.render, true
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
