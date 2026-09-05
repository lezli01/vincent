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
	// branch is what an archive did to the task's branch (§10, task 008); its
	// zero value means the branch step did not run, and no other action sets
	// it. It rides the result rather than an event because `archived` is
	// terminal and this is the one place a human is waiting for the answer.
	branch apiclient.BranchOutcome
	// retried is how many blocked descendants a retry on a parked fan_out
	// parent re-admitted (task 088). Like branch it is a consequence the
	// task's own row does not show — the parent comes back unchanged, still
	// `awaiting_children` — and no other action sets it.
	retried int
	err     error
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
	// marked is the board's bulk selection (task 011). When it is non-empty
	// the bar acts on every marked task that offers the action, and `id` names
	// only the row under the cursor — so a key can never act on a task the
	// count beside it did not include.
	marked []markedTask
}

// markedTask is one member of a bulk selection: which task, and what the
// daemon says can be done to it.
type markedTask struct {
	id      int64
	actions []string
}

// has reports the action is on offer. Under a bulk selection that means *some*
// marked task offers it (task 011 decision): requiring all of them would make
// `archive` vanish from a sweep of finished work because one task in it is
// still running, with nothing on screen to explain the absence. The count
// beside the key is what keeps that honest — `A archive (7)` on nine marked
// rows says both that the key works and that two rows will not move.
func (t taskActions) has(action string) bool {
	if t.bulk() {
		return len(t.targets(action)) > 0
	}
	for _, a := range t.actions {
		if a == action {
			return true
		}
	}
	return false
}

// bulk reports that a selection, rather than the cursor row, is what the bar
// acts on.
func (t taskActions) bulk() bool { return len(t.marked) > 0 }

// targets is the ids one action would be sent to: the marked tasks offering
// it, or nil when nothing is marked — the single-task path uses t.id, and a
// nil here is what distinguishes the two.
func (t taskActions) targets(action string) []int64 {
	out := make([]int64, 0, len(t.marked))
	for _, m := range t.marked {
		for _, a := range m.actions {
			if a == action {
				out = append(out, m.id)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// actionBar turns key presses into daemon calls and reports what happened.
// It is shared: the detail view renders it as a bar, the board as a footer
// hint, and both gate on the same available_actions.
type actionBar struct {
	// pending is an action waiting on y/n. force marks the second ask, the
	// one that carries `force` after a dirty worktree refused the first.
	pending string
	force   bool
	// pendingIDs pins the tasks a confirmation is for, when they are not
	// simply "the row under the cursor": a bulk selection, and — on the force
	// re-ask — the subset of it whose worktrees were dirty. nil means the
	// single-task path. bulkTotal is how many the question started out about,
	// so the re-ask can say "2 of 5".
	pendingIDs []int64
	bulkTotal  int

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
			action, force, ids := a.pending, a.force, a.pendingIDs
			a.pending, a.force, a.pendingIDs = "", false, nil
			if len(ids) > 0 {
				return a.dispatchBulk(client, ids, action, force), true
			}
			return a.dispatch(client, t.id, action, force), true
		case "n", "esc":
			a.pending, a.force, a.pendingIDs = "", false, nil
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
	// A selection retargets the key at every marked task offering the action;
	// with nothing marked, targets is nil and this is the path it always was.
	ids := t.targets(action)
	if needsConfirm(action) {
		a.pending, a.force = action, false
		a.pendingIDs, a.bulkTotal = ids, len(ids)
		return nil, true
	}
	if len(ids) > 0 {
		return a.dispatchBulk(client, ids, action, false), true
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
		task, branch, retried, err := callActionWithTimeout(client, id, action, force)
		return actionResultMsg{
			taskID: id, action: action, task: task,
			branch: branch, retried: retried, err: err,
		}
	}
}

// callAction is the one place a §6 action becomes an HTTP call, shared by the
// single-task path and the bulk one so the two cannot drift into meaning
// different things by the same key.
func callAction(ctx context.Context, client *apiclient.Client, id int64, action string, force bool) (apiclient.Task, apiclient.BranchOutcome, int, error) {
	switch action {
	case apiclient.ActionCancel:
		task, err := client.Cancel(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionPause:
		task, err := client.Pause(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionResume:
		task, err := client.Resume(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionRetry:
		// From a parked fan_out parent this is the cascade (task 088); the
		// key is the same one, because available_actions is what says the
		// action is on offer and the daemon is what decides it means.
		task, retried, err := client.Retry(ctx, id, apiclient.Override{})
		return task, apiclient.BranchOutcome{}, retried, err
	case apiclient.ActionSkip:
		task, err := client.Skip(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionApprove:
		task, err := client.Approve(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionReject:
		task, err := client.Reject(ctx, id)
		return task, apiclient.BranchOutcome{}, 0, err
	case apiclient.ActionArchive:
		task, branch, err := client.Archive(ctx, id, force)
		return task, branch, 0, err
	default:
		return apiclient.Task{}, apiclient.BranchOutcome{}, 0, fmt.Errorf("unknown action %q", action)
	}
}

// applyResult records what the daemon answered. A dirty worktree comes back
// as the second confirmation rather than an error, because `force` *is* the
// dirty confirmation (§6) and re-asking is what a human expects to be asked.
func (a *actionBar) applyResult(msg actionResultMsg) {
	if msg.err == nil {
		status := msg.action + " · now " + msg.task.State
		// A cascade leaves the parent exactly where it was, so "now
		// awaiting_children" is all the task row can say about it; the lanes
		// it re-admitted are the part a human acted for (task 088).
		if msg.retried > 0 {
			status += " · " + plural(msg.retried, "lane", "lanes") + " re-admitted"
		}
		// The branch is the one consequence of an archive that is invisible
		// afterwards — the task row no longer names a worktree to go look in —
		// so it is said here or nowhere (§10, task 008).
		if line := msg.branch.Summary(); line != "" {
			status += " · " + line
		}
		a.setStatus(status, false)
		return
	}
	if isDirtyWorktree(msg.action, msg.err) {
		a.pending, a.force = apiclient.ActionArchive, true
		a.pendingIDs, a.bulkTotal = nil, 0
		a.status = ""
		return
	}
	var apiErr *apiclient.Error
	if errors.As(msg.err, &apiErr) {
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
//
// A pending *bulk* question survives it, because it is a question about the
// selection rather than about the row under the cursor — and the cursor moves
// on its own exactly when one is on screen: a bulk archive that met a dirty
// worktree has already archived the clean tasks, the refetch drops their rows,
// and the cursor lands somewhere else. Clearing there would take the force
// re-ask away between the two halves of one action (task 011).
func (a *actionBar) clear() {
	if len(a.pendingIDs) > 0 {
		return
	}
	a.pending, a.force = "", false
	a.status, a.statusBad = "", false
}

// confirmPrompt is the question a pending confirmation asks. It names the
// consequence, not the action: "archive?" is not what a human needs to know.
func (a *actionBar) confirmPrompt(t taskActions) string {
	if n := len(a.pendingIDs); n > 0 {
		return a.bulkPrompt(n)
	}
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

// bulkPrompt is the same question asked of a selection. The force re-ask names
// the subset it is about — "2 of 5 selected tasks" — because by then the other
// three are already archived and re-asking about all five would be a lie.
func (a *actionBar) bulkPrompt(n int) string {
	switch {
	case a.pending == apiclient.ActionArchive && a.force:
		subject := selectedNoun(n)
		if a.bulkTotal > n {
			subject = fmt.Sprintf("%d of %s", n, selectedNoun(a.bulkTotal))
		}
		return fmt.Sprintf("%s have uncommitted changes — archive anyway and lose them? (y/n)", subject)
	case a.pending == apiclient.ActionArchive:
		return fmt.Sprintf("archive %s? their worktrees are removed (y/n)", selectedNoun(n))
	case a.pending == apiclient.ActionCancel:
		return fmt.Sprintf("cancel %s? their running steps are killed (y/n)", selectedNoun(n))
	default:
		return fmt.Sprintf("%s %s? (y/n)", a.pending, selectedNoun(n))
	}
}

// actionLabel names an action beside its key. Under a bulk selection it
// carries how many of the marked tasks the key would actually move: an action
// on offer for seven of nine says so, rather than leaving the reader to guess
// whether the other two come along (task 011).
func actionLabel(t taskActions, action string) string {
	if !t.bulk() {
		return action
	}
	return fmt.Sprintf("%s (%d)", action, len(t.targets(action)))
}

// render draws the bar: the pending question if there is one, otherwise the
// keys this task accepts — or, under a selection, the keys the marked tasks
// accept — plus whatever the last action reported.
func (a *actionBar) render(t taskActions) string {
	if a.pending != "" {
		return styleWarn.Render(" " + a.confirmPrompt(t))
	}
	parts := make([]string, 0, len(actionOrder)+2)
	for _, o := range actionOrder {
		if t.has(o.action) {
			parts = append(parts, styleKey.Render(o.key)+" "+actionLabel(t, o.action))
		}
	}
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
