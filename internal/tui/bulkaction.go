package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Bulk actions (§6, task 011): one human action sent to every task in the
// board's selection.
//
// There is no bulk endpoint and there is not going to be one: §6 lives in the
// API, and the TUI is one of three clients — a second definition of what
// `archive` means is a second thing to keep in step. So the client makes one
// call per task, and the daemon stays the only place a transition happens.

// bulkResultMsg carries the outcome of one bulk action. It is deliberately not
// a stream of actionResultMsg: a batch is one thing a human did and gets one
// answer, and thirty status lines racing each other is not an answer.
type bulkResultMsg struct {
	action string
	force  bool
	// total is how many tasks the batch was sent to, so the report can say
	// "5 of 7" rather than a bare count.
	total int
	// done are the ids the daemon accepted; dirty are the archive targets it
	// refused for an unclean worktree (§6), which become the force re-ask;
	// failed carries everything else, one entry per task.
	done   []int64
	dirty  []int64
	failed []bulkFailure
	// branches counts archives that deleted the task's branch (§10, task 008)
	// — the one consequence of an archive that is invisible afterwards.
	branches int
	// lanes counts the blocked descendants a cascading retry re-admitted
	// across the whole batch (task 090), for the same reason: a parked parent
	// comes back in the state it went in, so its row says nothing about it.
	lanes int
}

type bulkFailure struct {
	id  int64
	err error
}

// dispatchBulk sends one action to every id, one at a time, in the order the
// board handed them over.
//
// Sequential rather than concurrent: N parallel archives are N worktree
// removals racing the scheduler for the slots they free, and the line a human
// reads afterwards has to be built from outcomes that actually happened. Each
// call carries the same timeout a single action does; the batch as a whole is
// not bounded, because a batch cut off halfway leaves no account of which half.
func (a *actionBar) dispatchBulk(client *apiclient.Client, ids []int64, action string, force bool) tea.Cmd {
	if client == nil {
		a.setStatus("not connected", true)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	a.setStatus(fmt.Sprintf("%s %s…", action, selectedNoun(len(ids))), false)
	ids = slices.Clone(ids) // the selection may change while the batch runs
	return func() tea.Msg {
		msg := bulkResultMsg{action: action, force: force, total: len(ids)}
		for _, id := range ids {
			_, branch, retried, err := callActionWithTimeout(client, id, action, force)
			switch {
			case err == nil:
				msg.done = append(msg.done, id)
				if branch.Result == apiclient.BranchDeleted {
					msg.branches++
				}
				msg.lanes += retried
			case isDirtyWorktree(action, err):
				msg.dirty = append(msg.dirty, id)
			default:
				msg.failed = append(msg.failed, bulkFailure{id: id, err: err})
			}
		}
		return msg
	}
}

func callActionWithTimeout(client *apiclient.Client, id int64, action string, force bool) (apiclient.Task, apiclient.BranchOutcome, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	return callAction(ctx, client, id, action, force)
}

// applyBulkResult records what a batch did. A dirty worktree comes back as the
// second confirmation rather than as an error, exactly as it does for a single
// task — `force` *is* the dirty confirmation (§6) — except that the re-ask is
// about the refusals only: the clean ones are already archived.
func (a *actionBar) applyBulkResult(msg bulkResultMsg) {
	a.setStatus(bulkSummary(msg), len(msg.failed) > 0)
	if len(msg.dirty) > 0 {
		a.pending, a.force = apiclient.ActionArchive, true
		a.pendingIDs = msg.dirty
		a.bulkTotal = msg.total
	}
}

// bulkSummary is the one line a batch reports: what was accepted, what the
// branch cleanup did, and the *first* refusal. A list of forty errors is not a
// status line — the board's own rows are the durable account of which tasks
// moved.
func bulkSummary(msg bulkResultMsg) string {
	parts := []string{fmt.Sprintf("%s · %d of %d", msg.action, len(msg.done), msg.total)}
	if msg.branches > 0 {
		parts = append(parts, fmt.Sprintf("%s deleted", plural(msg.branches, "branch", "branches")))
	}
	if msg.lanes > 0 {
		parts = append(parts, plural(msg.lanes, "lane", "lanes")+" re-admitted")
	}
	if n := len(msg.dirty); n > 0 {
		parts = append(parts, fmt.Sprintf("%d need force", n))
	}
	if n := len(msg.failed); n > 0 {
		first := msg.failed[0]
		parts = append(parts, fmt.Sprintf("#%d: %s", first.id, actionErrString(first.err)))
		if n > 1 {
			parts = append(parts, fmt.Sprintf("and %d more failed", n-1))
		}
	}
	return strings.Join(parts, " · ")
}

// actionErrString renders one failure the way the single-task bar does: a 409
// is the task having moved on, which is worth saying as a state rather than as
// an HTTP code.
func actionErrString(err error) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) {
		if state := apiErr.Details["state"]; state != "" {
			return "the task is " + state
		}
		return apiErr.Message
	}
	return errString(err)
}

// isDirtyWorktree reports the daemon refused an archive because the worktree
// has uncommitted changes (§6) — the one refusal that is a question, not a
// failure.
func isDirtyWorktree(action string, err error) bool {
	if action != apiclient.ActionArchive {
		return false
	}
	var apiErr *apiclient.Error
	return errors.As(err, &apiErr) && apiErr.Details["reason"] == "worktree_dirty"
}

// selectedNoun names a count of marked tasks for a prompt or a status line.
func selectedNoun(n int) string {
	if n == 1 {
		return "1 selected task"
	}
	return fmt.Sprintf("%d selected tasks", n)
}

// plural is the count plus the right noun, for the few lines that carry one.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
