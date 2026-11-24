package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The global pause (task 149) in the TUI. The root holds the daemon's last
// answer and draws it on every screen: a header badge that is never shed and
// a status line under it, both only while the pause is news — the same rule
// the connection badge follows (task 129.15). `active` is drawn on the
// daemon view, where the question "is this daemon working?" is asked.

type (
	// globalPauseMsg is one reading of GET /v1/daemon/pause, or the answer
	// to a pause or continue the human asked for (acted names which).
	globalPauseMsg struct {
		pause apiclient.GlobalPause
		err   error
		acted string
	}
	// globalPauseStateMsg hands the root's reading to the views that render
	// it (the daemon view), the way the config is relayed.
	globalPauseStateMsg struct {
		pause apiclient.GlobalPause
		ok    bool
	}
)

// pauseActed values: what a globalPauseMsg answers.
const (
	pauseActedPause    = "pause"
	pauseActedContinue = "continue"
)

// globalPauseState is the root's view of the pause.
type globalPauseState struct {
	pause apiclient.GlobalPause
	// ok is whether a reading has landed; before one, nothing is drawn.
	ok bool
	// asking is the y/n question `Z` raises, on the status line.
	asking bool
	// busy is a pause or continue in flight, so a second `Z` does not race it.
	busy bool
	// asked is whether the session's opening read has been sent.
	asked bool
}

// shown reports whether the pause is news worth chrome.
func (g *globalPauseState) shown() bool { return g.ok && !g.pause.Active() }

// fetchGlobalPause reads the pause.
func fetchGlobalPause(c *apiclient.Client) tea.Cmd {
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		p, err := c.GlobalPause(ctx)
		return globalPauseMsg{pause: p, err: err}
	}
}

// actGlobalPause takes or lifts the pause.
func actGlobalPause(c *apiclient.Client, acted string) tea.Cmd {
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var (
			p   apiclient.GlobalPause
			err error
		)
		if acted == pauseActedContinue {
			p, err = c.ContinueAll(ctx)
		} else {
			p, err = c.PauseAll(ctx, "")
		}
		return globalPauseMsg{pause: p, err: err, acted: acted}
	}
}

// refetchOn reports whether an event may have changed what the pause
// shows: the pause itself, or — while one is in effect — any transition,
// since a task parking is what turns `pausing` into `paused` and the counts
// move with every state change.
func (g *globalPauseState) refetchOn(evType string) bool {
	if evType == "daemon.pause_changed" {
		return true
	}
	return g.shown() && evType == "task.state_changed"
}

// updateGlobalPause applies a reading or an action's answer.
func (m *root) updateGlobalPause(msg globalPauseMsg) tea.Cmd {
	g := &m.globalPause
	if msg.acted != "" {
		g.busy = false
	}
	if msg.err != nil {
		if msg.acted != "" {
			m.selNotice, m.selNoticeWarn = msg.acted+" failed: "+errString(msg.err), true
		}
		// A failed read keeps the last answer: a blip is not a lifted pause.
		return nil
	}
	g.pause, g.ok = msg.pause, true
	switch msg.acted {
	case pauseActedPause:
		m.selNotice, m.selNoticeWarn = "all work paused — "+opKey(keymap.PauseAll)+" continues it", false
	case pauseActedContinue:
		n := 0
		if msg.pause.Resumed != nil {
			n = *msg.pause.Resumed
		}
		m.selNotice, m.selNoticeWarn = fmt.Sprintf("work continues — resumed %s", countNoun(n, "task")), false
	}
	return m.broadcast(globalPauseStateMsg{pause: g.pause, ok: true})
}

// pressPauseAll is `Z`: raise the question for the action the state calls
// for. It is answered on the status line, like a project switch's.
func (m *root) pressPauseAll() tea.Cmd {
	g := &m.globalPause
	if g.busy || !g.ok {
		if !g.ok {
			return fetchGlobalPause(m.client)
		}
		return nil
	}
	g.asking = true
	return nil
}

// updatePauseAskKey answers the question: y acts, n or esc drops it, and
// every other key is ignored while it is on screen.
func (m *root) updatePauseAskKey(msg tea.KeyPressMsg) tea.Cmd {
	g := &m.globalPause
	yes, no := confirmAnswer(msg.String())
	switch {
	case yes:
		g.asking = false
		g.busy = true
		acted := pauseActedPause
		if !g.pause.Active() {
			acted = pauseActedContinue
		}
		return actGlobalPause(m.client, acted)
	case no:
		g.asking = false
	}
	return nil
}

// pauseQuestion is the y/n line `Z` raises.
func (g *globalPauseState) question() string {
	if g.pause.Active() {
		return "Pause all work? Nothing new starts; running tasks finish their step, then park. y/n"
	}
	return fmt.Sprintf("Continue all work? Queued tasks start again and %s parked by the pause resume%s. y/n",
		countNoun(g.pause.Held, "task"), pickWord(g.pause.Held == 1, "s", ""))
}

// pauseBadge is the header's badge: drawn while the pause is news, and
// never shed, like the connection badge beside it.
func (g *globalPauseState) badge() string {
	if !g.shown() {
		return ""
	}
	if g.pause.State == apiclient.PausePausing {
		return styleWarn.Render(fmt.Sprintf("⏸ pausing (%d finishing)", g.pause.Running))
	}
	return styleBad.Render("⏸ PAUSED")
}

// statusLine is the line under the header while the pause is in effect.
func (g *globalPauseState) statusLine() string {
	var b strings.Builder
	if g.pause.State == apiclient.PausePausing {
		fmt.Fprintf(&b, " ⏸ Pausing all work — %s still finishing %s step",
			countNoun(g.pause.Running, "task"), pickWord(g.pause.Running == 1, "its", "their"))
	} else {
		b.WriteString(" ⏸ All work is paused")
	}
	if g.pause.PausedAt != nil {
		b.WriteString(" since " + g.pause.PausedAt.Local().Format("15:04"))
	}
	if g.pause.Reason != "" {
		b.WriteString(" — " + g.pause.Reason)
	}
	fmt.Fprintf(&b, " · %d held · %d queued", g.pause.Held, g.pause.Queued)
	if g.pause.AwaitingInput > 0 {
		fmt.Fprintf(&b, " · %d awaiting input", g.pause.AwaitingInput)
	}
	b.WriteString(" · " + opKey(keymap.PauseAll) + " continue")
	return b.String()
}

// pauseStateLabel is the daemon view's one-word state, active included.
func pauseStateLabel(p apiclient.GlobalPause) string {
	switch p.State {
	case apiclient.PausePausing:
		return styleWarn.Render(fmt.Sprintf("⏸ pausing — %s finishing", countNoun(p.Running, "task")))
	case apiclient.PausePaused:
		return styleBad.Render("⏸ paused")
	default:
		return styleOK.Render("▶ active")
	}
}

// pickWord is a ternary for a word.
func pickWord(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
