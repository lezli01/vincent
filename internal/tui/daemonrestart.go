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
	"github.com/lezli01/vincent/internal/service"
)

// The daemon screen's restart and its log filter (#1081). Restart existed
// only as `r` on the reconnecting screen, which retries a connection rather
// than restarting anything; and the log was mostly this TUI's own polling.

// restartStopTimeout bounds the wait for the old daemon to exit — the CLI's
// `daemon stop` budget.
const restartStopTimeout = 30 * time.Second

// serviceRestartTimeout bounds a restart through the service manager, from
// asking it to the new daemon answering /v1/health (#1160). It covers
// Windows' graceful stop, which waits up to `daemon stop`'s 30 s on its own.
const serviceRestartTimeout = 60 * time.Second

// restartDaemonMsg is a confirmed restart, from the daemon screen to the
// root, which owns the connection it tears down. viaService is the path the
// question named (#1160): through the service manager, or a stop and the
// connect flow's auto-start.
type restartDaemonMsg struct{ viaService bool }

// daemonStoppedMsg ends the first half of a restart: on the plain path the old
// daemon is gone, on the service path the service's new one is serving — or
// err says why not. Either way a nil err means "reconnect now".
type daemonStoppedMsg struct{ err error }

// restartEndedMsg tells the daemon screen a restart is over: reconnected, or
// failed with err. Until it lands a second restart is ignored (#1160).
type restartEndedMsg struct{ err error }

// serviceStateMsg is the service manager's answer to "is a vincent service
// installed and running?", read when `r` opens the question (#1160).
type serviceStateMsg struct {
	st  service.Status
	err error
}

// serviceStateCmd reads the service state off the UI goroutine: every
// backend shells out, and Windows' answer comes from PowerShell.
func serviceStateCmd(query func(context.Context) (service.Status, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st, err := query(ctx)
		return serviceStateMsg{st: st, err: err}
	}
}

// serviceRestartCmd restarts a service-managed daemon through its service
// manager and waits for the new one (#1160). A stop and the connect flow's
// auto-start would leave the new daemon outside the service, because every
// definition restarts on failure only and the stop exits 0.
//
// It waits here, for a daemon.json naming a PID other than the old one and a
// healthy answer from it, rather than handing straight to the connect flow:
// a probe that ran before the service had the new daemon up would fail and
// auto-start a detached one beside it. A daemon that does not come back is an
// error, and the connection is left to the stream's own reconnect.
func (cn connector) serviceRestartCmd(dataDir string) tea.Cmd {
	return func() tea.Msg {
		old := 0
		if ri, err := cn.readRuntime(dataDir); err == nil {
			old = ri.PID
		}
		ctx, cancel := context.WithTimeout(context.Background(), cn.serviceTimeout)
		defer cancel()
		if err := cn.restartService(ctx); err != nil {
			return daemonStoppedMsg{err: fmt.Errorf("restart the service: %w", err)}
		}
		for ctx.Err() == nil {
			if ri, err := cn.readRuntime(dataDir); err == nil && ri.PID != old {
				hctx, hcancel := context.WithTimeout(ctx, probeTimeout)
				_, err := cn.checkHealth(hctx, ri.Port)
				hcancel()
				if err == nil {
					return daemonStoppedMsg{}
				}
			}
			time.Sleep(cn.pollInterval)
		}
		return daemonStoppedMsg{err: fmt.Errorf("the service did not bring the daemon back within %s; "+
			"`vincent service status` and the log say why", cn.serviceTimeout)}
	}
}

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
// question up. Until the service reading lands y waits too (#1160): the
// question has not yet said which restart it is asking about.
func (d *daemonView) updateRestartAsk(msg tea.KeyPressMsg) tea.Cmd {
	yes, no := confirmAnswer(msg.String())
	if yes && !d.svcKnown {
		return nil
	}
	if yes || no {
		d.restartAsk = false
	}
	if yes {
		d.restarting, d.restartIgnored = true, false
		d.restartViaService = d.viaService()
		via := d.restartViaService
		return func() tea.Msg { return restartDaemonMsg{viaService: via} }
	}
	return nil
}

// startRestart is `r`: the question, and the service reading it waits on. A
// restart already under way ignores it and says so (#1160) — `r y r y` used
// to start a second one while the first was still reconnecting.
func (d *daemonView) startRestart() tea.Cmd {
	if d.restarting {
		d.restartIgnored = true
		return nil
	}
	d.restartAsk, d.restartErr = true, nil
	d.svcKnown, d.svc, d.svcErr = false, service.Status{}, nil
	return serviceStateCmd(d.serviceQuery)
}

// viaService is whether the restart goes through the service manager: a
// vincent service is installed and its daemon is running (#1160). A failed
// reading, or a service that is installed but stopped, restarts the daemon
// the way it did before — whatever is running is not the service's.
func (d *daemonView) viaService() bool {
	return d.svcKnown && d.svcErr == nil && d.svc.Installed && d.svc.Running
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
