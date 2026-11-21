package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Moving an issue between lanes (task 145). A lane is still derived, never
// stored (task 134 decision 1, kept by task 145 decision 1): `m` picks the
// lane a card should go to and runs the action that lane is derived from —
// a new task, ready, not ready, a return, a close, a reopen or cancelling
// the issue's running tasks. A move nothing can make is listed, refused, and
// says why; the TUI never pretends a card moved.
//
// planLaneMove is the one table of what reaches which lane. It reads only
// what the daemon served — the lane, the handoff marker, available_actions
// and the active task ids — so it decides nothing the daemon has not said.

// issueMoveKey is `m` on both issue screens. Not a rebindable operation:
// like `X` and `i`, it is a surface-local fixed key (task 145 decision 4).
const issueMoveKey = "m"

// issueMoveLabel is `m`'s row on both issue screens.
const issueMoveLabel = "move the issue to another lane — runs the action that lane comes from (start a task, ready, not ready, return, cancel its tasks, close, reopen), and says why a move cannot be made"

// issueActCancel is a move's action that cancels the issue's unsettled root
// tasks, beside close, reopen and the handoff actions.
const issueActCancel = "cancel"

// laneMoveKind is what a move does.
type laneMoveKind int

const (
	moveRefused laneMoveKind = iota
	moveStartTask
	moveReady
	moveUnready
	moveReturn
	moveClose
	moveReopen
	moveCancel
)

// laneMove is one target lane's answer: what picking it does, and the line
// the picker shows beside it — the effect, or why it is refused.
type laneMove struct {
	kind laneMoveKind
	note string
}

// issueLaneWord is a lane's section label, or the wire value for a lane this
// TUI does not know.
func issueLaneWord(lane string) string {
	for _, s := range issueSections {
		if s.lane == lane {
			return s.label
		}
	}
	return lane
}

func hasAction(iss apiclient.Issue, action string) bool {
	return slices.Contains(iss.AvailableActions, action)
}

// planLaneMove is what moving iss, read in full (GetIssue), to lane does.
func planLaneMove(iss apiclient.Issue, to string) laneMove {
	from := iss.Lane
	if from == to {
		return laneMove{kind: moveRefused, note: "it is already here"}
	}
	if to == issueLaneDone {
		if hasAction(iss, issueActClose) {
			return laneMove{kind: moveClose, note: "closes it — you pick the reason"}
		}
		return laneMove{kind: moveRefused, note: "the daemon offers no close for it"}
	}
	if from == issueLaneDone {
		// A reopened issue lands where its tasks put it, which may not be the
		// lane picked: the note says so before, and the result says where.
		if !hasAction(iss, issueActReopen) {
			return laneMove{kind: moveRefused, note: "the daemon offers no reopen for it"}
		}
		return laneMove{kind: moveReopen, note: "reopens it — its tasks decide the lane it lands in"}
	}
	running := len(iss.Tasks.ActiveIDs)
	switch to {
	case "open":
		switch {
		case from == "hand_off" || issueMarker(iss) == markerIdle:
			return laneMove{kind: moveRefused, note: "it has finished work, and open means no work was done"}
		case handoffClaimed(iss):
			return laneMove{kind: moveRefused, note: inHandoffNote(iss)}
		case running == 0:
			return laneMove{kind: moveRefused, note: "no task of its own is running — a chat on it holds it in progress"}
		}
		return laneMove{kind: moveCancel, note: "cancels " + taskRefs(iss.Tasks.ActiveIDs) + " — asks first"}
	case "in_progress":
		switch {
		case from == "open":
			return laneMove{kind: moveStartTask, note: "starts a task from it, in the new-task form"}
		case handoffPending(iss):
			return laneMove{kind: moveUnready, note: "marks it not ready"}
		case handoffClaimed(iss):
			return laneMove{kind: moveReturn, note: "returns it from its handoff — asks first"}
		}
		return laneMove{kind: moveRefused, note: "its handoff has delivered; start a follow-up task with a"}
	case "hand_off":
		switch {
		case from == "open" && issueMarker(iss) == markerDelivered:
			// A handoff delivered part of it and left it open (#1042):
			// that delivery is spent, and the rest needs new work.
			return laneMove{kind: moveRefused, note: "a handoff delivered part of it; start a task for the rest first"}
		case from == "open":
			return laneMove{kind: moveRefused, note: "nothing is finished to hand off — start a task first"}
		case readyApplies(iss):
			return laneMove{kind: moveReady, note: "marks it ready"}
		case running > 0:
			return laneMove{kind: moveRefused, note: taskRefs(iss.Tasks.ActiveIDs) + " is still running; it moves when it finishes"}
		}
		return laneMove{kind: moveRefused, note: "it is not idle, so ready does not apply"}
	}
	return laneMove{kind: moveRefused, note: "this TUI does not know the lane " + strconv.Quote(to)}
}

// taskRefs spells ids as "task #1" or "tasks #1, #2".
func taskRefs(ids []int64) string {
	refs := make([]string, len(ids))
	for i, id := range ids {
		refs[i] = "#" + strconv.FormatInt(id, 10)
	}
	if len(refs) == 1 {
		return "task " + refs[0]
	}
	return "tasks " + strings.Join(refs, ", ")
}

// issueMoveTargetMsg is the full issue `m` on the list read: a list row
// carries neither available_actions nor the active task ids.
type issueMoveTargetMsg struct {
	issue apiclient.Issue
	err   error
}

func issueMoveFor(client *apiclient.Client, id int64) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, "")
		return issueMoveTargetMsg{issue: cleanIssue(iss), err: err}
	}
}

// newIssueMove opens `m`'s lane picker over iss, every lane but its own,
// the refused ones disabled with their reason. The done lane is listed
// whether or not the list shows it: closing is a move like any other.
func newIssueMove(client *apiclient.Client, iss apiclient.Issue) *issueAction {
	opts := make([]pickerOption, 0, len(issueSections)-1)
	for _, s := range issueSections {
		if s.lane == iss.Lane {
			continue
		}
		mv := planLaneMove(iss, s.lane)
		opts = append(opts, pickerOption{
			value: s.lane, label: s.label, note: mv.note, disabled: mv.kind == moveRefused,
		})
	}
	cur := ""
	for _, o := range opts {
		if !o.disabled {
			cur = o.value
			break
		}
	}
	a := &issueAction{client: client, issue: iss, stage: iaMove}
	a.pick = newPicker(0, "move from "+issueLaneWord(iss.Lane)+" to", opts, false, cur)
	return a
}

// move runs the picked lane's action. done is true when the prompt hands
// off to another screen — the new-task form — and closes.
func (a *issueAction) move(to string) (cmd tea.Cmd, done bool) {
	switch mv := planLaneMove(a.issue, to); mv.kind {
	case moveStartTask:
		if handoffClaimed(a.issue) {
			a.note = inHandoffNote(a.issue)
			return nil, false
		}
		return newTaskFromIssueCmd(a.issue), true
	case moveClose:
		return a.begin(issueActClose), false
	case moveReopen:
		return a.begin(issueActReopen), false
	case moveReady:
		a.action, a.pick = issueActReady, nil
		return a.run(), false
	case moveUnready:
		a.action, a.pick = issueActUnready, nil
		return a.run(), false
	case moveReturn:
		a.action, a.stage, a.pick = issueActReturn, iaConfirm, nil
		return nil, false
	case moveCancel:
		a.action, a.stage, a.pick = issueActCancel, iaConfirm, nil
		return nil, false
	default:
		a.note = mv.note
		return nil, false
	}
}

// runCancel cancels every unsettled root task of iss, then re-reads it so
// the note can say which lane it landed in.
func runCancel(ctx context.Context, client *apiclient.Client, iss apiclient.Issue) (apiclient.Issue, error) {
	for _, id := range iss.Tasks.ActiveIDs {
		if _, err := client.Cancel(ctx, id); err != nil {
			return apiclient.Issue{}, err
		}
	}
	return client.GetIssue(ctx, iss.ID, "")
}

// cancelConfirmText is the cancel move's prompt.
func cancelConfirmText(iss apiclient.Issue) []string {
	return []string{
		"Cancel " + taskRefs(iss.Tasks.ActiveIDs) + " of issue #" + strconv.FormatInt(iss.ID, 10) + "?",
		"Their work stops; the issue goes back to open once none of its tasks finished.",
	}
}

// landedNote appends where a write left the issue, when the write answered
// with it.
func landedNote(note string, iss apiclient.Issue) string {
	if iss.Lane == "" {
		return note
	}
	return note + " — it is in the " + issueLaneWord(iss.Lane) + " lane"
}
