package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// actionTimeout bounds one action call. Cancel kills a process tree and
// archive removes a worktree, so this is longer than a plain fetch.
const actionTimeout = 30 * time.Second

// actionResultMsg carries the outcome of one §6 human action.
type actionResultMsg struct {
	taskID int64
	action string
	task   apiclient.Task
	err    error
}

// actionKeys binds §15's keys to the actions they can mean. `p` is pause or
// resume depending on which the daemon offers, which is why a key maps to a
// list; the rest are one to one. `x` rejects a gate — §15 gave approve `a`
// and retry `r`, and left reject unnamed.
var actionKeys = map[string][]string{
	"p": {apiclient.ActionPause, apiclient.ActionResume},
	"c": {apiclient.ActionCancel},
	"a": {apiclient.ActionApprove},
	"x": {apiclient.ActionReject},
	"r": {apiclient.ActionRetry},
	"s": {apiclient.ActionSkip},
	"A": {apiclient.ActionArchive},
}

// actionOrder fixes how the hint line reads, so the bar does not reshuffle
// under a cursor as states change.
var actionOrder = []struct{ action, key string }{
	{apiclient.ActionPause, "p"},
	{apiclient.ActionResume, "p"},
	{apiclient.ActionApprove, "a"},
	{apiclient.ActionReject, "x"},
	{apiclient.ActionRetry, "r"},
	{apiclient.ActionSkip, "s"},
	{apiclient.ActionCancel, "c"},
	{apiclient.ActionArchive, "A"},
}

// confirmed actions are the ones that destroy something a human cannot get
// back by pressing the key again: a live process tree, and a worktree.
func needsConfirm(action string) bool {
	return action == apiclient.ActionCancel || action == apiclient.ActionArchive
}

// taskActions is the slice of a task an action bar needs: what it is, and
// what the daemon says can be done to it right now (§6). The bar never
// decides validity itself — that is what available_actions is for.
type taskActions struct {
	id      int64
	state   string
	actions []string
}

func (t taskActions) has(action string) bool {
	for _, a := range t.actions {
		if a == action {
			return true
		}
	}
	return false
}

// actionBar turns key presses into daemon calls and reports what happened.
// It is shared: the detail view renders it as a bar, the board as a footer
// hint, and both gate on the same available_actions.
type actionBar struct {
	// pending is an action waiting on y/n. force marks the second ask, the
	// one that carries `force` after a dirty worktree refused the first.
	pending string
	force   bool

	status    string
	statusBad bool
}

// capturing reports that a confirmation owns the keyboard, so the shell stops
// treating `q` as quit while a y/n is on screen.
func (a *actionBar) capturing() bool { return a.pending != "" }

// handleKey maps one key onto an action for this task. handled=false means
// the key was not an action the daemon offers, and the view should treat it
// as its own.
func (a *actionBar) handleKey(key string, client *apiclient.Client, t taskActions) (cmd tea.Cmd, handled bool) {
	if a.pending != "" {
		switch key {
		case "y":
			action, force := a.pending, a.force
			a.pending, a.force = "", false
			return a.dispatch(client, t.id, action, force), true
		case "n", "esc":
			a.pending, a.force = "", false
			a.setStatus("cancelled", false)
			return nil, true
		}
		// A confirmation is a question: anything else waits for its answer
		// rather than starting a second action underneath it.
		return nil, true
	}
	action, ok := resolveAction(key, t)
	if !ok {
		return nil, false
	}
	if needsConfirm(action) {
		a.pending, a.force = action, false
		return nil, true
	}
	return a.dispatch(client, t.id, action, false), true
}

// resolveAction reports which action a key means for this task, if any. `p`
// is pause or resume depending on which the daemon offers.
func resolveAction(key string, t taskActions) (string, bool) {
	for _, action := range actionKeys[key] {
		if t.has(action) {
			return action, true
		}
	}
	return "", false
}

// dispatch calls the daemon. Nothing is predicted locally: the response
// carries the task as the daemon now sees it (§6 — the TUI holds no state the
// daemon does not have).
func (a *actionBar) dispatch(client *apiclient.Client, id int64, action string, force bool) tea.Cmd {
	if client == nil {
		a.setStatus("not connected", true)
		return nil
	}
	a.setStatus(action+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		var (
			task apiclient.Task
			err  error
		)
		switch action {
		case apiclient.ActionCancel:
			task, err = client.Cancel(ctx, id)
		case apiclient.ActionPause:
			task, err = client.Pause(ctx, id)
		case apiclient.ActionResume:
			task, err = client.Resume(ctx, id)
		case apiclient.ActionRetry:
			task, err = client.Retry(ctx, id, apiclient.Override{})
		case apiclient.ActionSkip:
			task, err = client.Skip(ctx, id)
		case apiclient.ActionApprove:
			task, err = client.Approve(ctx, id)
		case apiclient.ActionReject:
			task, err = client.Reject(ctx, id)
		case apiclient.ActionArchive:
			task, err = client.Archive(ctx, id, force)
		default:
			err = fmt.Errorf("unknown action %q", action)
		}
		return actionResultMsg{taskID: id, action: action, task: task, err: err}
	}
}

// applyResult records what the daemon answered. A dirty worktree comes back
// as the second confirmation rather than an error, because `force` *is* the
// dirty confirmation (§6) and re-asking is what a human expects to be asked.
func (a *actionBar) applyResult(msg actionResultMsg) {
	if msg.err == nil {
		a.setStatus(msg.action+" · now "+msg.task.State, false)
		return
	}
	var apiErr *apiclient.Error
	if errors.As(msg.err, &apiErr) {
		if msg.action == apiclient.ActionArchive && apiErr.Details["reason"] == "worktree_dirty" {
			a.pending, a.force = apiclient.ActionArchive, true
			a.status = ""
			return
		}
		if apiErr.Status == http.StatusConflict {
			if state := apiErr.Details["state"]; state != "" {
				a.setStatus(fmt.Sprintf("%s: the task is %s", msg.action, state), true)
				return
			}
		}
		a.setStatus(msg.action+": "+apiErr.Message, true)
		return
	}
	a.setStatus(msg.action+": "+errString(msg.err), true)
}

func (a *actionBar) setStatus(text string, bad bool) {
	a.status, a.statusBad = text, bad
}

// clear drops transient state when the bar moves to another task.
func (a *actionBar) clear() {
	a.pending, a.force = "", false
	a.status, a.statusBad = "", false
}

// confirmPrompt is the question a pending confirmation asks. It names the
// consequence, not the action: "archive?" is not what a human needs to know.
func (a *actionBar) confirmPrompt(t taskActions) string {
	switch {
	case a.pending == apiclient.ActionArchive && a.force:
		return fmt.Sprintf("#%d has uncommitted changes — archive anyway and lose them? (y/n)", t.id)
	case a.pending == apiclient.ActionArchive:
		return fmt.Sprintf("archive #%d? its worktree is removed (y/n)", t.id)
	case a.pending == apiclient.ActionCancel:
		return fmt.Sprintf("cancel #%d? a running step is killed (y/n)", t.id)
	default:
		return fmt.Sprintf("%s #%d? (y/n)", a.pending, t.id)
	}
}

// render draws the bar: the pending question if there is one, otherwise the
// keys this task actually accepts plus whatever the last action reported.
func (a *actionBar) render(t taskActions, extra ...string) string {
	if a.pending != "" {
		return styleWarn.Render(" " + a.confirmPrompt(t))
	}
	parts := make([]string, 0, len(actionOrder)+2)
	for _, o := range actionOrder {
		if t.has(o.action) {
			parts = append(parts, styleKey.Render(o.key)+" "+o.action)
		}
	}
	// `answer` has no key: it is the form, and where the form lives differs
	// per view, so each view supplies that hint itself.
	parts = append(parts, extra...)
	line := " " + strings.Join(parts, styleDim.Render(" · "))
	if len(parts) == 0 {
		line = styleDim.Render(" no actions available")
	}
	if a.status != "" {
		style := styleDim
		if a.statusBad {
			style = styleBad
		}
		line += style.Render("   " + a.status)
	}
	return line
}
