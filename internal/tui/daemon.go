package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/daemon"
)

const (
	// logPollInterval paces the log re-read. The log is the only thing on
	// this view that changes while you watch it, so it gets a timer while
	// identity and config wait for activation or R.
	logPollInterval = 2 * time.Second
	// logTailLines bounds the buffer the pane scrolls through. The read
	// behind it is bounded separately, by bytes.
	logTailLines = 500
)

// Daemon view messages.
type (
	// daemonInfoMsg and daemonConfigMsg land separately: one endpoint failing
	// must not blank the block the other one fills.
	daemonInfoMsg struct {
		info apiclient.Info
		err  error
	}
	daemonConfigMsg struct {
		config apiclient.Config
		err    error
	}
	// daemonDoctorMsg carries GET /v1/doctor?probe=false — the row counts and
	// the retention span behind the database block (task 029). It lands
	// separately for the same reason the other two do.
	daemonDoctorMsg struct {
		report *apiclient.DoctorReport
		err    error
	}
	// daemonLogMsg carries one tail read. err is the log being absent or
	// unreadable, which is a different fact from a log with nothing in it.
	daemonLogMsg struct {
		lines []string
		err   error
	}
	// daemonTickMsg re-reads the log and re-renders the uptime.
	daemonTickMsg struct{}
)

// daemonView is §15's view 6. It is the only view that renders while the
// daemon is unreachable: the log is worth reading exactly then, and it comes
// off the filesystem rather than the API for that reason.
type daemonView struct {
	client  *apiclient.Client
	dataDir string
	now     func() time.Time
	tail    func(path string, n int) ([]string, error)

	info      apiclient.Info
	infoOK    bool
	infoErr   error
	infoAt    time.Time
	config    apiclient.Config
	configOK  bool
	configErr error
	configAt  time.Time
	doctor    *apiclient.DoctorReport
	doctorOK  bool
	doctorErr error
	doctorAt  time.Time

	logLines []string
	logErr   error
	logOK    bool
	// logDirty defers rebuilding the viewport's content until render, which
	// is the only place the pane's width and height are known.
	logDirty bool

	// keys is the editable config surface, and cursor is the row under the
	// selection. focusConfig moves j/k off the log pane and onto that list —
	// the view has two scrollable things now, and tab is what says which one
	// the arrows mean.
	keys        []configKey
	cursor      int
	focusConfig bool
	// form is the open editor, nil when none is. While it is non-nil the view
	// captures input: every single-key global would otherwise land in the
	// text field.
	form *configForm
	// saved names the key the last successful patch changed, so the block can
	// say what moved rather than silently re-rendering.
	saved string

	vp        viewport.Model
	following bool
	// visible gates the ticker: a view that is off-screen re-reads nothing,
	// but it also does not need to, since it refetches on activation.
	visible bool
	// connected mirrors the shell's phase so the blocks can say "stale"
	// rather than pretending a dead daemon's last figures are current.
	connected bool

	width, height int
}

func newDaemonView() *daemonView {
	return &daemonView{
		now:       time.Now,
		tail:      daemon.TailFile,
		keys:      configKeys(),
		vp:        viewport.New(),
		following: true,
	}
}

func (d *daemonView) title() string { return "Daemon" }

func (d *daemonView) setClient(c *apiclient.Client) tea.Cmd {
	d.client = c
	d.connected = c != nil
	if !d.visible {
		return nil
	}
	return tea.Batch(d.infoCmd(), d.configCmd(), d.doctorCmd())
}

// setDataDir hands the view the resolved data dir. It arrives from the shell
// rather than being resolved here so the whole TUI agrees on one answer.
func (d *daemonView) setDataDir(dir string) { d.dataDir = dir }

// setConnected tells the view the daemon went away or came back. Losing the
// daemon does not clear the blocks — the last figures are still the truth
// about the process that was there — but it does change what they claim.
func (d *daemonView) setConnected(ok bool) {
	d.connected = ok
	if !ok {
		d.client = nil
	}
}

func (d *daemonView) infoCmd() tea.Cmd {
	client := d.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		info, err := client.Info(ctx)
		return daemonInfoMsg{info: info, err: err}
	}
}

func (d *daemonView) configCmd() tea.Cmd {
	client := d.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.Config(ctx)
		return daemonConfigMsg{config: cfg, err: err}
	}
}

// doctorCmd fetches the diagnostic report for its database group.
//
// probe=false is the point: the default forces a re-probe of every adapter,
// which is right for `vincent doctor` — a command a human ran, in the loop
// task 005 decision 2 is about — and wrong for a panel that opens on a
// keypress. This view already has adapter availability from /v1/info; what it
// wants here is the row counts and the span (task 029 decision 4).
func (d *daemonView) doctorCmd() tea.Cmd {
	client := d.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		rep, err := client.Doctor(ctx, false)
		return daemonDoctorMsg{report: rep, err: err}
	}
}

// logCmd reads the tail off disk. It needs no client, which is the point:
// this is the one pane that still works when the daemon does not.
func (d *daemonView) logCmd() tea.Cmd {
	dir := d.dataDir
	tail := d.tail
	if dir == "" || tail == nil {
		return nil
	}
	return func() tea.Msg {
		lines, err := tail(daemon.LogPath(dir), logTailLines)
		return daemonLogMsg{lines: lines, err: err}
	}
}

func (d *daemonView) tickCmd() tea.Cmd {
	return tea.Tick(logPollInterval, func(time.Time) tea.Msg { return daemonTickMsg{} })
}

// refreshCmd re-reads every source. It is what R does, and what activation
// does.
func (d *daemonView) refreshCmd() tea.Cmd {
	return tea.Batch(d.infoCmd(), d.configCmd(), d.doctorCmd(), d.logCmd())
}

func (d *daemonView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil
	case viewActivatedMsg:
		if msg.id != viewDaemon {
			return d, nil
		}
		d.visible = true
		return d, tea.Batch(d.refreshCmd(), d.tickCmd())
	case viewDeactivatedMsg:
		if msg.id == viewDaemon {
			d.visible = false
		}
		return d, nil
	case daemonTickMsg:
		if !d.visible {
			// The ticker is not cancellable, so it stops by not re-arming.
			return d, nil
		}
		return d, tea.Batch(d.logCmd(), d.tickCmd())
	case daemonInfoMsg:
		d.applyInfo(msg)
		return d, nil
	case daemonConfigMsg:
		d.applyConfig(msg)
		return d, nil
	case configSavedMsg:
		d.applySaved(msg)
		return d, nil
	case daemonDoctorMsg:
		d.applyDoctor(msg)
		return d, nil
	case daemonLogMsg:
		d.applyLog(msg)
		return d, nil
	case tea.KeyPressMsg:
		return d.updateKey(msg)
	}
	return d, nil
}

// applyInfo keeps the last-good identity behind the error. A refresh that
// failed is not a daemon with no version.
func (d *daemonView) applyInfo(msg daemonInfoMsg) {
	if msg.err != nil {
		d.infoErr = msg.err
		return
	}
	d.infoErr = nil
	d.infoOK = true
	d.info = msg.info
	d.infoAt = d.now()
}

func (d *daemonView) applyConfig(msg daemonConfigMsg) {
	if msg.err != nil {
		d.configErr = msg.err
		return
	}
	d.configErr = nil
	d.configOK = true
	d.config = msg.config
	d.configAt = d.now()
}

// applyDoctor keeps the last-good report behind a failed refresh, the way the
// other two blocks do: figures that were true about a database a minute ago
// are more use than a blank panel, as long as the block says so.
func (d *daemonView) applyDoctor(msg daemonDoctorMsg) {
	if msg.err != nil {
		d.doctorErr = msg.err
		return
	}
	d.doctorErr = nil
	d.doctorOK = true
	d.doctor = msg.report
	d.doctorAt = d.now()
}

// applyLog replaces the buffer wholesale — the read is a tail, not a delta —
// and keeps the pane pinned to the end while it is following.
func (d *daemonView) applyLog(msg daemonLogMsg) {
	if msg.err != nil {
		d.logErr = msg.err
		d.logLines = nil
		d.logOK = false
		return
	}
	d.logErr = nil
	d.logOK = true
	d.logLines = msg.lines
	d.logDirty = true
}

// applySaved lands the answer to a PATCH. A refusal keeps the editor open
// with the message against the field — that is the whole reason the form
// stays up — and a success closes it and adopts the configuration the daemon
// reported, which is what is in force rather than what was asked for.
func (d *daemonView) applySaved(msg configSavedMsg) {
	if d.form == nil {
		return
	}
	d.form.saving = false
	if msg.err != nil {
		d.form.err = errString(msg.err)
		return
	}
	d.form = nil
	d.saved = msg.path
	d.config = msg.cfg
	d.configOK = true
	d.configErr = nil
	d.configAt = d.now()
}

func (d *daemonView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if d.form != nil {
		cmd, done := d.form.update(msg, d.client)
		if done {
			d.form = nil
		}
		return d, cmd
	}
	switch msg.String() {
	case "esc":
		// The takeover layer of the §15 esc stack: back to the home screen.
		return d, func() tea.Msg { return selectViewMsg{id: viewHome} }
	case "R":
		return d, d.refreshCmd()
	case "tab":
		// Two scrollable things on one view: tab says which one j/k mean.
		d.focusConfig = !d.focusConfig
		return d, nil
	case "enter", "e":
		if k, ok := d.selectedKey(); ok && d.configOK {
			d.focusConfig = true
			d.form = newConfigForm(k, d.config)
		}
		return d, nil
	case "f", "G", "end":
		d.setFollowing(true)
		return d, nil
	case "j", "down":
		if d.focusConfig {
			d.moveCursor(1)
			return d, nil
		}
		d.vp.ScrollDown(1)
	case "k", "up":
		if d.focusConfig {
			d.moveCursor(-1)
			return d, nil
		}
		d.vp.ScrollUp(1)
	default:
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(msg)
		d.syncFollowToViewport()
		return d, cmd
	}
	d.syncFollowToViewport()
	return d, nil
}

// selectedKey is the config row under the cursor.
func (d *daemonView) selectedKey() (configKey, bool) {
	if d.cursor < 0 || d.cursor >= len(d.keys) {
		return configKey{}, false
	}
	return d.keys[d.cursor], true
}

// moveCursor walks the config list, clamped rather than wrapped: a list this
// long is read top to bottom and wrapping past the end reads as a jump.
func (d *daemonView) moveCursor(delta int) {
	if len(d.keys) == 0 {
		return
	}
	d.cursor = min(max(d.cursor+delta, 0), len(d.keys)-1)
}

func (d *daemonView) setFollowing(on bool) {
	d.following = on
	if on {
		d.vp.GotoBottom()
	}
}

// syncFollowToViewport keeps follow honest after a manual scroll: the pane
// is following exactly when it is showing the end of the log. Same rule as
// the detail view's output pane, so f and G mean one thing in both.
func (d *daemonView) syncFollowToViewport() {
	d.following = d.vp.AtBottom()
}

// capturesInput is true exactly while the config editor is open. It was
// permanently false before this view could edit anything; leaving it that way
// would send every single-key global — R, f, g, q — into the text field on the
// first keystroke.
func (d *daemonView) capturesInput() bool { return d.form != nil }
