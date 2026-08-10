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

	logLines []string
	logErr   error
	logOK    bool
	// logDirty defers rebuilding the viewport's content until render, which
	// is the only place the pane's width and height are known.
	logDirty bool

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
	return tea.Batch(d.infoCmd(), d.configCmd())
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

// refreshCmd re-reads all three sources. It is what R does, and what
// activation does.
func (d *daemonView) refreshCmd() tea.Cmd {
	return tea.Batch(d.infoCmd(), d.configCmd(), d.logCmd())
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

func (d *daemonView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "R":
		return d, d.refreshCmd()
	case "f", "G", "end":
		d.setFollowing(true)
		return d, nil
	case "j", "down":
		d.vp.ScrollDown(1)
	case "k", "up":
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

// capturesInput is always false: this view has no text entry.
func (d *daemonView) capturesInput() bool { return false }
