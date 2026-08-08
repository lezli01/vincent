// Package taskstate is the task lifecycle state machine of spec §6. It is
// pure: no persistence, no I/O. Both the API (which rejects an invalid
// action with 409) and the engine (which drives a task through its steps)
// consult it, so there is exactly one definition of what may happen next.
package taskstate

import "sort"

// State is a task lifecycle state (spec §6).
type State string

// Task lifecycle states (spec §6).
const (
	Queued        State = "queued"
	Running       State = "running"
	AwaitingGate  State = "awaiting_gate"
	AwaitingInput State = "awaiting_input"
	Blocked       State = "blocked"
	Paused        State = "paused"
	Done          State = "done"
	Aborted       State = "aborted"
	Archived      State = "archived"
)

// All lists every state, in the order §6 documents them.
var All = []State{
	Queued, Running, AwaitingGate, AwaitingInput,
	Blocked, Paused, Done, Aborted, Archived,
}

// Valid reports whether s is a known state.
func Valid(s State) bool {
	for _, v := range All {
		if v == s {
			return true
		}
	}
	return false
}

// HoldsSlot reports whether a task in this state occupies a concurrency slot
// (spec §6, §11). `awaiting_input` does: its agent process is alive, idle on
// its stdin (§7.4).
func HoldsSlot(s State) bool { return s == Running || s == AwaitingInput }

// Terminal reports whether no further transition is possible.
func Terminal(s State) bool { return s == Archived }

// Action is something that moves a task between states: either a human
// action (§6) or an engine event.
type Action string

// Human actions (spec §6). These are the actions the API exposes and the
// TUI offers; every one is rejected with 409 outside its valid states.
const (
	Cancel  Action = "cancel"
	Pause   Action = "pause"
	Resume  Action = "resume"
	Retry   Action = "retry"
	Skip    Action = "skip"
	Answer  Action = "answer"
	Approve Action = "approve"
	Reject  Action = "reject"
	Archive Action = "archive"
)

// Engine events. They are transitions the daemon performs while running a
// task, and go through the same table so no state change bypasses §6.
const (
	// Admit is the scheduler starting a queued task (§11).
	Admit Action = "admit"
	// Gate is the engine reaching a manual step (§7.1).
	Gate Action = "gate"
	// RequestInput is an agent asking for input mid-step (§7.4).
	RequestInput Action = "request_input"
	// Complete is the last step succeeding (§6).
	Complete Action = "complete"
	// Fail is a step failing with retries exhausted (§7.2).
	Fail Action = "fail"
	// Interrupt is a crash or graceful stop mid-step: the attempt does not
	// consume a retry and the task is re-queued (§12.4).
	Interrupt Action = "interrupt"
	// InputClosed is a pending input request ending without an answer while
	// execution continues — input_timeout expiry or agent process death with
	// retries remaining, or the agent withdrawing its request (§7.4; PR F).
	// Exhausted retries use Fail instead.
	InputClosed Action = "input_closed"
	// Park is a requested pause taking effect at the step boundary (§6).
	Park Action = "park"
)

// humanActions is the set of actions a client may invoke, in the order §6
// lists them.
var humanActions = []Action{Cancel, Pause, Resume, Retry, Skip, Answer, Approve, Reject, Archive}

// Human reports whether a is a human action rather than an engine event.
func Human(a Action) bool {
	for _, h := range humanActions {
		if h == a {
			return true
		}
	}
	return false
}

// Transition is the outcome of applying an action.
type Transition struct {
	// To is the resulting state.
	To State
	// Deferred marks an action accepted now that takes effect later:
	// pausing a running task lets the current step finish, and the engine
	// applies Park at the step boundary (§6).
	Deferred bool
}

// table is the §6 transition table: action → valid from-state → outcome.
// Every allowed transition in vincent appears here exactly once.
var table = map[Action]map[State]Transition{
	Cancel: {
		Queued:        {To: Aborted},
		Running:       {To: Aborted},
		AwaitingInput: {To: Aborted},
		AwaitingGate:  {To: Aborted},
		Blocked:       {To: Aborted},
		Paused:        {To: Aborted},
	},
	Pause: {
		Queued: {To: Paused},
		// A running task finishes its current step first; Park completes it.
		Running: {To: Running, Deferred: true},
	},
	Resume:  {Paused: {To: Queued}},
	Retry:   {Blocked: {To: Queued}},
	Skip:    {Blocked: {To: Queued}, AwaitingGate: {To: Queued}},
	Answer:  {AwaitingInput: {To: Running}},
	Approve: {AwaitingGate: {To: Queued}},
	Reject:  {AwaitingGate: {To: Blocked}},
	Archive: {Done: {To: Archived}, Aborted: {To: Archived}},

	Admit:        {Queued: {To: Running}},
	Gate:         {Running: {To: AwaitingGate}},
	RequestInput: {Running: {To: AwaitingInput}},
	Complete:     {Running: {To: Done}},
	Fail:         {Running: {To: Blocked}, AwaitingInput: {To: Blocked}},
	Interrupt:    {Running: {To: Queued}, AwaitingInput: {To: Queued}},
	InputClosed:  {AwaitingInput: {To: Running}},
	Park:         {Running: {To: Paused}},
}

// Next returns the transition for applying a to a task in state from. ok is
// false when the action is not valid from that state — the caller answers
// 409 with the current state (§13.1).
func Next(from State, a Action) (Transition, bool) {
	t, ok := table[a][from]
	return t, ok
}

// Can reports whether action a is valid from state from.
func Can(from State, a Action) bool {
	_, ok := Next(from, a)
	return ok
}

// CanSetPriority reports whether priority may be changed in this state
// (§6: queued and paused only). Priority is not a transition — it reorders
// scheduler admission without changing state.
func CanSetPriority(s State) bool { return s == Queued || s == Paused }

// HumanActionsFrom lists the human actions valid from a state, sorted so
// clients render a stable action bar (§15).
func HumanActionsFrom(s State) []Action {
	var out []Action
	for _, a := range humanActions {
		if Can(s, a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
