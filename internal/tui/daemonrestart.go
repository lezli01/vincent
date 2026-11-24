package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/procx"
)

// The daemon screen's restart and its log filter (#1081). Restart existed
// only as `r` on the reconnecting screen, which retries a connection rather
// than restarting anything; and the log was mostly this TUI's own polling.

// restartStopTimeout bounds the wait for the old daemon to exit — the CLI's
// `daemon stop` budget.
const restartStopTimeout = 30 * time.Second

// restartDaemonMsg is a confirmed restart, from the daemon screen to the
// root, which owns the connection it tears down.
type restartDaemonMsg struct{}

// daemonStoppedMsg says the old daemon is gone, or why the stop failed.
type daemonStoppedMsg struct{ err error }

// stopDaemonCmd asks the daemon to stop the way `vincent daemon stop` does
// and waits for its process to exit; the connect flow then starts a new one.
func stopDaemonCmd(dataDir string) tea.Cmd {
	return func() tea.Msg {
		ri, err := daemon.ReadRuntimeInfo(dataDir)
		if err != nil {
			return daemonStoppedMsg{err: fmt.Errorf("read daemon.json: %w", err)}
		}
		// Named before the stop, so the wait can tell its exit from a later
		// process reusing the PID.
		ident, _ := procx.Identity(ri.PID)
		token, err := daemon.ReadToken(dataDir)
		if err != nil {
			return daemonStoppedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := daemon.RequestStop(ctx, ri.Port, token); err != nil {
			return daemonStoppedMsg{err: err}
		}
		deadline := time.Now().Add(restartStopTimeout)
		for time.Now().Before(deadline) {
			if running, err := daemon.ProbeRunning(dataDir); err == nil && !running {
				if ident == "" {
					return daemonStoppedMsg{}
				}
				if gone, err := procx.Exited(ri.PID, ident); err != nil || gone {
					return daemonStoppedMsg{}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return daemonStoppedMsg{err: fmt.Errorf("the daemon did not exit within %s", restartStopTimeout)}
	}
}

// requestStatus reads the status of one `http request` log line; false when
// the line is not one.
var requestStatusRe = regexp.MustCompile(`\bstatus=(\d{3})\b`)

func requestStatus(line string) (int, bool) {
	if !strings.Contains(line, "http request") {
		return 0, false
	}
	m := requestStatusRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// shownLogLines is the log as the pane shows it: successful request lines
// hidden unless asked for — a failed request stays, it is news — and the
// search applied.
func (d *daemonView) shownLogLines() (shown []string, hidden int) {
	q := strings.ToLower(strings.TrimSpace(d.logSearch.Value()))
	for _, l := range d.logLines {
		if status, ok := requestStatus(l); ok && status < 400 && !d.showRequests {
			hidden++
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l), q) {
			continue
		}
		shown = append(shown, l)
	}
	return shown, hidden
}

// logHeader is the pane's title with what it is leaving out.
func (d *daemonView) logHeader() string {
	_, hidden := d.shownLogLines()
	var parts []string
	switch {
	case d.showRequests:
		parts = append(parts, "request lines shown · h hides them")
	case hidden > 0:
		parts = append(parts, fmt.Sprintf("%d request lines hidden · h shows them", hidden))
	}
	if q := strings.TrimSpace(d.logSearch.Value()); q != "" || d.searching {
		parts = append(parts, "matching "+d.logSearch.View())
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + strings.Join(parts, " · ")
}

// updateRestartAsk answers the restart question by the shared rule (#1162):
// y restarts, n or esc keeps the daemon, and any other key leaves the
// question up.
func (d *daemonView) updateRestartAsk(msg tea.KeyPressMsg) tea.Cmd {
	yes, no := confirmAnswer(msg.String())
	if yes || no {
		d.restartAsk = false
	}
	if yes {
		d.restarting = true
		return func() tea.Msg { return restartDaemonMsg{} }
	}
	return nil
}

// updateSearch edits the log search: enter keeps it, esc clears it.
func (d *daemonView) updateSearch(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		d.searching = false
		d.logSearch.SetValue("")
		d.logSearch.Blur()
		d.logDirty = true
		return nil
	case "enter":
		d.searching = false
		d.logSearch.Blur()
		return nil
	}
	var cmd tea.Cmd
	d.logSearch, cmd = d.logSearch.Update(msg)
	d.logDirty = true
	return cmd
}

// newLogSearch is the log pane's search line.
func newLogSearch() textField {
	f := newTextField()
	f.SetPlaceholder("search the log")
	return f
}

// The daemon screen's own keys (#1081).
const (
	daemonRestartKey  = "r"
	daemonRequestsKey = "h"
)
