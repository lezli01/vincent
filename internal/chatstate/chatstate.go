// Package chatstate is the chat lifecycle state machine of spec §5.5. It is
// pure: no persistence, no I/O. Both the API (which rejects an invalid action
// with 409) and internal/chatrun consult it, so there is exactly one
// definition of what may happen next — the arrangement internal/taskstate has
// for §6's task FSM.
//
// It is a second, deliberately separate vocabulary rather than a reuse of
// taskstate (task 063, §6). A chat has no steps, no gates and no verdict: it
// never "completes", a failed turn is rendered against that turn rather than
// parking the whole chat, and the only end is `archived`. Folding those four
// states into §6 would mean every existing task query and every board legend
// deciding whether it means chats too — the same objection that kept a `kind`
// column off the tasks table.
package chatstate

import "sort"

// State is a chat lifecycle state (spec §5.5).
type State string

// Chat lifecycle states (spec §5.5).
const (
	// Idle is a chat with no live turn: the one a human can send to. It is
	// the state a chat is created in and the one every finished turn —
	// succeeded, failed or interrupted — returns it to.
	Idle State = "idle"
	// Running is a live turn: an agent process is up, owned by the chat's
	// runner goroutine.
	Running State = "running"
	// AwaitingInput is a turn holding its process while the agent waits on
	// a §7.4 mid-run request. As in §6 it holds its slot, because the
	// process is alive and idle on its stdin (task 063 decision 8).
	AwaitingInput State = "awaiting_input"
	// Archived is a terminal state. Its worktree is gone and its branch may
	// have been deleted, so nothing further can run in it.
	Archived State = "archived"
	// HandedOff is the other terminal state (task 074, §5.5 amended
	// 2026-09-01): the chat's worktree and branch now belong to the task it
	// created, and the chat is kept only as the linked history of how that
	// task's workspace came to exist.
	//
	// It is not `archived` because archiving *removes* the worktree and may
	// delete the branch, which is exactly the state a handoff transfers. A
	// second terminal state makes "archiving a handed-off chat must never
	// remove task-owned workspace state" true by construction rather than by
	// a guard: `archive` is simply not legal from here.
	HandedOff State = "handed_off"
)

// All lists every state, in the order §5.5 documents them.
var All = []State{Idle, Running, AwaitingInput, Archived, HandedOff}

// Valid reports whether s is a known state.
func Valid(s State) bool {
	for _, v := range All {
		if v == s {
			return true
		}
	}
	return false
}

// HoldsProcess reports whether a chat in this state owns a live agent process,
// and so counts against `max_parallel_chats` (§11, task 063 decision 1).
//
// This is the chat analogue of taskstate.HoldsSlot and counts the same two
// states for the same reason: `awaiting_input` has a process alive on its
// stdin, and a cap that did not count it would be a cap on nothing.
func HoldsProcess(s State) bool { return s == Running || s == AwaitingInput }

// Terminal reports whether no further transition is possible. There are two
// such states, not one: a chat ends either by being archived or by being
// handed off to a task (task 074).
func Terminal(s State) bool { return s == Archived || s == HandedOff }

// Action is something that moves a chat between states: a human action or a
// runner event.
type Action string

// Human actions (spec §5.5). Every one is rejected with 409 outside its valid
// states, exactly as §6's are.
const (
	// Send is a human message, and the only thing that starts a turn. It is
	// refused rather than queued when `max_parallel_chats` is reached
	// (decision 1) — a refusal the FSM does not model, because the cap is
	// about how many chats are running, not about what this chat may do.
	Send Action = "send"
	// Answer answers a pending §7.4 input request, through the same adapter
	// Respond the task path uses (decision 8).
	Answer Action = "answer"
	// Cancel stops a live turn and kills its process tree. There is no
	// pause: a chat is a foreground conversation, and a paused one is just
	// an idle one nobody has sent to.
	Cancel Action = "cancel"
	// Archive removes the worktree and may delete the empty branch, the way
	// task 008 archives a task (§10).
	Archive Action = "archive"
	// HandOff creates a task that adopts this chat's worktree, branch, base
	// branch and base SHA verbatim, and leaves the chat terminal (task 074).
	// Nothing is copied, renamed or committed: the transfer *is* the task row
	// naming the same directory the chat named.
	HandOff Action = "hand_off"
)

// Runner events. They are transitions the daemon performs while running a
// turn, and go through the same table so no state change bypasses §5.5.
const (
	// TurnStarted is the runner picking up an accepted send.
	TurnStarted Action = "turn_started"
	// InputRequested is the agent asking mid-run (§7.4).
	InputRequested Action = "input_requested"
	// InputClosed is the agent withdrawing its request (§7.4).
	InputClosed Action = "input_closed"
	// TurnFinished ends a turn, whatever its outcome. A chat has no
	// `blocked`: a failed turn is a property of the turn, and the chat is
	// idle and usable the instant the process is gone (task 063).
	TurnFinished Action = "turn_finished"
	// TurnInterrupted is recovery finalizing a turn whose daemon died under
	// it (decision 5). It is a separate action from TurnFinished only so a
	// reader of this table can see that the case exists; both land on Idle.
	TurnInterrupted Action = "turn_interrupted"
)

// transitions is the whole of §5.5: state → action → next state. Anything not
// in it is invalid, which is what the API turns into a 409.
var transitions = map[State]map[Action]State{
	Idle: {
		Send:    Running,
		Archive: Archived,
		HandOff: HandedOff,
	},
	Running: {
		InputRequested:  AwaitingInput,
		TurnFinished:    Idle,
		TurnInterrupted: Idle,
		Cancel:          Idle,
		TurnStarted:     Running,
	},
	AwaitingInput: {
		Answer:          Running,
		InputClosed:     Running,
		TurnFinished:    Idle,
		TurnInterrupted: Idle,
		Cancel:          Idle,
	},
	Archived:  {},
	HandedOff: {},
}

// Next returns the state a reaches from s, and whether the pair is legal.
func Next(s State, a Action) (State, bool) {
	next, ok := transitions[s][a]
	return next, ok
}

// Allowed reports whether a is legal from s.
func Allowed(s State, a Action) bool {
	_, ok := transitions[s][a]
	return ok
}

// Actions lists the actions legal from s, sorted, so a client can render the
// affordances without a second copy of the table.
func Actions(s State) []Action {
	out := make([]Action, 0, len(transitions[s]))
	for a := range transitions[s] {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TurnState is the outcome recorded against one turn (spec §5.5, §14). Unlike
// State it is not a lifecycle: a turn is running, then it is one of the three
// endings, forever.
type TurnState string

// Turn outcomes.
const (
	TurnRunning TurnState = "running"
	TurnDone    TurnState = "done"
	TurnFailed  TurnState = "failed"
	// TurnInterruptedState is a turn whose daemon restarted under it
	// (decision 5). It is never re-run: re-running would re-send the
	// human's message, and the session it was resuming died with the
	// process, so §12.4's auto-resume rule does not carry over.
	TurnInterruptedState TurnState = "interrupted"
)

// TurnTerminal reports whether a turn in this state has finished.
func TurnTerminal(s TurnState) bool { return s != TurnRunning }
