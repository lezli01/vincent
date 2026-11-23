package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
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
	// parent re-admitted (task 090). Like branch it is a consequence the
	// task's own row does not show — the parent comes back unchanged, still
	// `awaiting_children` — and no other action sets it.
	retried int
	// warnings is what a force delete's cleanup left behind (task 153); no
	// other action sets it.
	warnings []string
	err      error
}

// actionForceDelete is the bar's one pending question that is not a §6
// action (task 153): offered only after an archive failed on something a
// retry will not fix, it removes the task from any state that holds no live
// process. It is never in available_actions and has no key of its own — the
// failure that offers it is the way in, as the dirty re-ask's is.
const actionForceDelete = "force delete"

// forceCauseWidth is how much of the failure a force-delete question quotes.
// The footer is one line cut from the right, so the answer keys come first
// and the cause is cut short enough to leave its consequence readable.
const forceCauseWidth = 60

// actionOps binds the §6 actions the bar dispatches to the operations whose
// keys they answer (task 118). Pause and resume share one operation, which is
// why `p` resolves to whichever the daemon offers; the rest are one to one.
// `x` rejects a gate — §15 gave approve `a` and retry `r`, and left reject
// unnamed. The order is how the hint line reads, so the bar does not reshuffle
// under a cursor as states change.
var actionOps = []struct {
	action string
	op     keymap.Op
}{
	{apiclient.ActionPause, keymap.Pause},
	{apiclient.ActionResume, keymap.Pause},
	{apiclient.ActionApprove, keymap.Approve},
	{apiclient.ActionReject, keymap.Reject},
	{apiclient.ActionRetry, keymap.Retry},
	{apiclient.ActionSkip, keymap.Skip},
	{apiclient.ActionCancel, keymap.Cancel},
	{apiclient.ActionArchive, keymap.Archive},
}

// confirmed actions are the ones a human cannot take back by pressing a key
// again: a live process tree, a worktree, and — since #1055 — a step skipped
// past or a gate rejected, each a decision the task carries on from.
func needsConfirm(action string) bool {
	switch action {
	case apiclient.ActionCancel, apiclient.ActionArchive, apiclient.ActionSkip, apiclient.ActionReject:
		return true
	}
	return false
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
	// openChatID is the task's `open_chat_id` (task 119): the chat that
	// locks it. While it is set the daemon offers no `chat`, and `T` opens
	// this one instead of asking for a second.
	openChatID int64
	// projectID is the task's project, which its chat shares: `T` opening
	// that chat follows it there (task 132.6).
	projectID int64
	// editRetry is whether `E` works here (#1059): a step with a prompt or
	// command to edit, as the detail sub-model holding the task sees it — the
	// workspace's, or the board's detail panes when they show the selected
	// task. A target with no such detail leaves it false.
	editRetry bool
}

// offersChat reports whether `T` does anything for this target: the daemon
// offers `chat`, or a chat is already open on the task and the key reopens
// it. Never under a bulk selection — a conversation is about one task, the
// reason `F` is not a bulk action either.
func (t taskActions) offersChat() bool {
	if t.bulk() || t.id == 0 {
		return false
	}
	return t.openChatID != 0 || t.has(apiclient.ActionChat)
}

// offers is has for a registry row's action, with `chat` answered by
// offersChat: the palette and the footer gate the `T` row on the lock as well
// as on the action, and every other row on the action alone.
func (t taskActions) offers(action string) bool {
	if action == apiclient.ActionChat {
		return t.offersChat()
	}
	if action == actionDiagnose {
		return t.offersDiagnose()
	}
	return t.has(action)
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
	// cause is why a force delete is being offered: the failure it is the
	// way out of, said in the question so `y` is answered knowing it.
	cause string
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
			a.pending, a.force, a.pendingIDs, a.cause = "", false, nil, ""
			if len(ids) > 0 {
				return a.dispatchBulk(client, ids, action, force), true
			}
			return a.dispatch(client, t.id, action, force), true
		case "n", "esc":
			a.pending, a.force, a.pendingIDs, a.cause = "", false, nil, ""
			a.setStatus("cancelled", false)
			return nil, true
		}
		// A confirmation is a question: anything else waits for its answer
		// rather than starting a second action underneath it.
		return nil, true
	}
	action, ok := resolveAction(key, t)
	if !ok {
		// An action key the target does not offer says so rather than
		// doing nothing (#1059). No other meaning shares these keys on the
		// surfaces that call here, so nothing falls through that would have
		// done something.
		if name, isAction := keyAction(key, t); isAction {
			a.setStatus(unavailableText(name, t), false)
			return nil, true
		}
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
	for _, o := range actionOps {
		if opKey(o.op) == key && t.has(o.action) {
			return o.action, true
		}
	}
	return "", false
}

// keyAction is the §6 action an action key names, whether or not the target
// offers it: `p` is resume on a paused task and pause otherwise.
func keyAction(key string, t taskActions) (string, bool) {
	if key == "" {
		return "", false
	}
	for _, o := range actionOps {
		if opKey(o.op) != key {
			continue
		}
		if o.op == keymap.Pause && t.state == statePaused {
			return apiclient.ActionResume, true
		}
		return o.action, true
	}
	return "", false
}

// unavailableText is why an action key did nothing: no task under the
// cursor, a selection none of which offers it, a chat holding the task, or
// the state the task is in — "retry: not available while running".
func unavailableText(action string, t taskActions) string {
	name := strings.ReplaceAll(action, "_", " ")
	switch {
	case t.bulk():
		return name + ": none of the selected tasks offers it"
	case t.id == 0:
		return name + ": select a task — it acts on one task, not a group"
	case t.openChatID != 0:
		return fmt.Sprintf("%s: not available while chat #%d holds the task — close the chat first", name, t.openChatID)
	case t.state == "":
		return name + ": not available"
	}
	return name + ": not available while " + stateWords(t.state)
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
	if action == actionForceDelete {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			warnings, err := client.ForceDeleteTask(ctx, id)
			return actionResultMsg{taskID: id, action: action, warnings: warnings, err: err}
		}
	}
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
	task, branch, retried, err := callActionRaw(ctx, client, id, action, force)
	return cleanTask(task), branch, retried, err
}

// callActionRaw is callAction before the answer's titles are cleaned.
func callActionRaw(ctx context.Context, client *apiclient.Client, id int64, action string, force bool) (apiclient.Task, apiclient.BranchOutcome, int, error) {
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
		// From a parked fan_out parent this is the cascade (task 090); the
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
	case actionForceDelete:
		// The bulk path's force delete; the single one keeps the cleanup's
		// warnings (dispatch), a batch reports counts only.
		_, err := client.ForceDeleteTask(ctx, id)
		return apiclient.Task{ID: id}, apiclient.BranchOutcome{}, 0, err
	default:
		return apiclient.Task{}, apiclient.BranchOutcome{}, 0, fmt.Errorf("unknown action %q", action)
	}
}

// applyResult records what the daemon answered. A dirty worktree comes back
// as the second confirmation rather than an error, because `force` *is* the
// dirty confirmation (§6) and re-asking is what a human expects to be asked.
func (a *actionBar) applyResult(msg actionResultMsg) {
	if msg.action == actionForceDelete && msg.err == nil {
		status := fmt.Sprintf("#%d force-deleted", msg.taskID)
		if n := len(msg.warnings); n > 0 {
			status += " · left behind: " + msg.warnings[0]
			if n > 1 {
				status += fmt.Sprintf(" (and %d more)", n-1)
			}
		}
		a.setStatus(status, len(msg.warnings) > 0)
		return
	}
	if msg.err == nil {
		status := msg.action + " · now " + stateWords(msg.task.State)
		// A cascade leaves the parent exactly where it was, so "now
		// awaiting_children" is all the task row can say about it; the lanes
		// it re-admitted are the part a human acted for (task 090).
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
	if isStuckArchive(msg.action, msg.err) {
		// An archive no retry will fix (task 153): offer the way out
		// instead of leaving the task on the board for good.
		a.pending, a.force = actionForceDelete, false
		a.pendingIDs, a.bulkTotal = nil, 0
		a.cause, a.status = actionErrString(msg.err), ""
		return
	}
	var apiErr *apiclient.Error
	if errors.As(msg.err, &apiErr) {
		if apiErr.Status == http.StatusConflict {
			if state := apiErr.Details["state"]; state != "" {
				a.setStatus(fmt.Sprintf("%s: the task is %s", msg.action, stateWords(state)), true)
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
	a.pending, a.force, a.cause = "", false, ""
	a.status, a.statusBad = "", false
}

// confirmPrompt is the question a pending confirmation asks. It names the
// consequence, not the action: "archive?" is not what a human needs to know.
func (a *actionBar) confirmPrompt(t taskActions) string {
	if n := len(a.pendingIDs); n > 0 {
		return a.bulkPrompt(n)
	}
	switch {
	case a.pending == actionForceDelete:
		return fmt.Sprintf("force delete #%d? (y/n) · archive failed: %s · the task goes anyway, its branch stays",
			t.id, ansi.Truncate(a.cause, forceCauseWidth, "…"))
	case a.pending == apiclient.ActionArchive && a.force:
		return fmt.Sprintf("#%d has uncommitted changes — archive anyway and lose them? (y/n)", t.id)
	case a.pending == apiclient.ActionArchive:
		return fmt.Sprintf("archive #%d? its worktree is removed (y/n)", t.id)
	case a.pending == apiclient.ActionCancel:
		return fmt.Sprintf("cancel #%d? a running step is killed (y/n)", t.id)
	case a.pending == apiclient.ActionSkip:
		return fmt.Sprintf("skip #%d's current step? it is marked skipped and the task goes on without it (y/n)", t.id)
	case a.pending == apiclient.ActionReject:
		return fmt.Sprintf("reject #%d's gate? the task is blocked there until you retry, skip or cancel (y/n)", t.id)
	default:
		return fmt.Sprintf("%s #%d? (y/n)", a.pending, t.id)
	}
}

// bulkPrompt is the same question asked of a selection. The force re-ask names
// the subset it is about — "2 of 5 selected tasks" — because by then the other
// three are already archived and re-asking about all five would be a lie.
func (a *actionBar) bulkPrompt(n int) string {
	switch {
	case a.pending == actionForceDelete:
		subject := selectedNoun(n)
		if a.bulkTotal > n {
			subject = fmt.Sprintf("%d of %s", n, selectedNoun(a.bulkTotal))
		}
		return fmt.Sprintf("force delete %s? (y/n) · their archive failed: %s · branches stay",
			subject, ansi.Truncate(a.cause, forceCauseWidth, "…"))
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
	case a.pending == apiclient.ActionSkip:
		return fmt.Sprintf("skip the current step of %s? each goes on without it (y/n)", selectedNoun(n))
	case a.pending == apiclient.ActionReject:
		return fmt.Sprintf("reject the gates of %s? each is blocked there (y/n)", selectedNoun(n))
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
	parts := make([]string, 0, len(actionOps)+2)
	for _, o := range actionOps {
		if t.has(o.action) {
			parts = append(parts, styleKey.Render(opKey(o.op))+" "+actionLabel(t, o.action))
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
