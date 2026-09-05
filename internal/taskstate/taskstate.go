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
	// AwaitingChildren is a `fan_out` step's parent waiting for its lanes
	// (§7.6, task 014). It holds **no** slot: the actor invariant says a
	// gate, a block or a pause releases the slot and ends the goroutine, and
	// a parent that kept one for hours while its children worked is the exact
	// starvation awaiting_gate exists to avoid.
	//
	// It is a state of its own rather than a reuse of awaiting_gate because
	// the two differ in what a human can do about them, which is the only
	// thing §6 is for: awaiting_gate offers approve/reject/skip, and none of
	// the three mean anything while children are still running.
	AwaitingChildren State = "awaiting_children"
	Blocked          State = "blocked"
	Paused           State = "paused"
	Done             State = "done"
	Aborted          State = "aborted"
	Archived         State = "archived"
)

// All lists every state, in the order §6 documents them.
var All = []State{
	Queued, Running, AwaitingGate, AwaitingInput, AwaitingChildren,
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

// Settled reports whether a task has finished in the sense a fan-out join
// needs: it is done, or it ended without finishing (§6, task 014 decision
// 20). A parent resumes when every descendant is settled.
//
// This is deliberately **not** Terminal, which means "no further transition
// is possible" and is true of `archived` alone — a `done` task can still be
// archived. Nor is it "holds no slot": `blocked`, `awaiting_gate` and
// `paused` hold none, and a join that proceeded over a blocked lane would
// merge a branch missing that lane's work with nothing in the result saying
// so. Those three hold the join open until a human resolves them, which is
// what the §13.2 children rollup exists to surface.
//
// `archived` counts as settled: it is reachable only from done or aborted.
func Settled(s State) bool { return s == Done || s == Aborted || s == Archived }

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
	// Repair is an ad-hoc repair agent a human launches from `blocked`
	// (task 025). It is a second producer of `blocked → queued`, and the
	// admission it produces runs one agent in the task's existing worktree
	// and returns the task to `blocked` at the same step with the same
	// reason — the repair decides nothing about the blocked step.
	//
	// It is a human action rather than a lifecycle state of its own: a
	// `repairing` state would cost an FSM row, a board legend, slot rules
	// and a recovery path (what task 014 paid for `awaiting_children`) to
	// buy one nicer `cancel`, which keeps its present meaning throughout.
	Repair Action = "repair"
	// FollowUp is a run a human launches from `done` or `aborted`, before the
	// task is archived (task 027). It is the third producer of `→ queued`
	// after retry and repair, and the admission it produces runs an agent
	// prompt, a shell command or a whole registry workflow in the task's
	// *existing* worktree and branch, then returns the task to the state it
	// came from — `done → done`, `aborted → aborted`.
	//
	// It is scoped to exactly the pair `archive` is scoped to: the two states
	// where the work still exists and is still reachable. A follow-up decides
	// nothing about the task's verdict (decision 5), which is why there is no
	// `aborted → done` edge behind it.
	FollowUp Action = "follow_up"
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
	// Interrupt returns a running task to the queue (§12.4). A crash or a
	// graceful stop mid-step is the original caller; a usage-limit hold
	// (task 003) and a paced retry (`retry_backoff`, task 028) are the
	// others, each adding an admission hold to the same edge.
	//
	// Whether the attempt consumed a retry is **not** decided here. The
	// attempt's own row says so: `interrupted` consumes none, `failed`
	// consumes one. finishStepRun writes the row and this moves the task, and
	// the two are independent — which is what lets a paced retry keep a
	// `failed` attempt while the task goes back to `queued`.
	Interrupt Action = "interrupt"
	// InputClosed is a pending input request ending without an answer while
	// execution continues — input_timeout expiry or agent process death with
	// retries remaining, or the agent withdrawing its request (§7.4; PR F).
	// Exhausted retries use Fail instead.
	InputClosed Action = "input_closed"
	// Park is a requested pause taking effect at the step boundary (§6).
	Park Action = "park"
	// FanOut is a fan_out step parking its parent after spawning its lanes
	// (§7.6, task 014).
	FanOut Action = "fan_out"
	// ChildrenSettled is every descendant of a parked parent having settled,
	// which returns it to the queue to run its join.
	ChildrenSettled Action = "children_settled"
	// Restore returns a task whose follow-up run has ended to the state the
	// follow-up was launched from (task 027 decision 7). It exists for the
	// `aborted` origin alone: a done-origin follow-up ends with Complete,
	// which already means "the run finished", while returning an aborted task
	// had no edge behind it at all. `cancel` would have been the only
	// candidate and it is a human action — using it here would report a
	// human decision that nobody made.
	Restore Action = "restore"
)

// humanActions is the set of actions a client may invoke, in the order §6
// lists them.
var humanActions = []Action{
	Cancel, Pause, Resume, Retry, Repair, Skip, Answer, Approve, Reject, Archive, FollowUp,
}

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
		Queued:           {To: Aborted},
		Running:          {To: Aborted},
		AwaitingInput:    {To: Aborted},
		AwaitingGate:     {To: Aborted},
		AwaitingChildren: {To: Aborted},
		Blocked:          {To: Aborted},
		Paused:           {To: Aborted},
	},
	Pause: {
		Queued: {To: Paused},
		// A running task finishes its current step first; Park completes it.
		Running: {To: Running, Deferred: true},
	},
	Resume: {Paused: {To: Queued}},
	// Retry from `awaiting_children` is a **legality marker**, not a swap any
	// caller performs (§6, task 090). It exists so that
	// `Can(AwaitingChildren, Retry)` is true — the API answers 200 instead of
	// 409, and HumanActionsFrom lists `retry` — while Runner.Retry writes
	// nothing at all to the parked parent's row: it only cascades the retry to
	// every blocked descendant. A parent that has retried nothing of its own
	// must not be stamped as if it had.
	Retry:   {Blocked: {To: Queued}, AwaitingChildren: {To: AwaitingChildren}},
	Repair:  {Blocked: {To: Queued}},
	Skip:    {Blocked: {To: Queued}, AwaitingGate: {To: Queued}},
	Answer:  {AwaitingInput: {To: Running}},
	Approve: {AwaitingGate: {To: Queued}},
	Reject:  {AwaitingGate: {To: Blocked}},
	Archive: {Done: {To: Archived}, Aborted: {To: Archived}},
	// A follow-up is offered from exactly the states archive is (task 027):
	// the two where the task is finished but its worktree and branch are
	// still there. Both re-queue, so internal/scheduler stays the only
	// producer of `queued → running` and both §11 caps apply.
	FollowUp: {Done: {To: Queued}, Aborted: {To: Queued}},

	Admit:        {Queued: {To: Running}},
	Gate:         {Running: {To: AwaitingGate}},
	RequestInput: {Running: {To: AwaitingInput}},
	Complete:     {Running: {To: Done}},
	Fail:         {Running: {To: Blocked}, AwaitingInput: {To: Blocked}},
	Interrupt:    {Running: {To: Queued}, AwaitingInput: {To: Queued}},
	InputClosed:  {AwaitingInput: {To: Running}},
	Park:         {Running: {To: Paused}},

	// Fan-out (task 014). A parent parks in awaiting_children when its
	// fan_out step has spawned its lanes, and the scheduler — the one place
	// admission decisions are unraced — re-queues it once every descendant
	// has Settled.
	//
	// There is deliberately no Pause row: pause is valid from queued and
	// running, and a parent that owns nothing running has nothing to pause.
	// Its children are ordinary tasks and pause individually.
	FanOut:          {Running: {To: AwaitingChildren}},
	ChildrenSettled: {AwaitingChildren: {To: Queued}},

	// Follow-up (task 027). A done-origin follow-up ends through Complete,
	// which this row is the aborted-origin mirror of. There is no other user
	// of Restore, and deliberately no `Aborted → Done` anywhere: a follow-up
	// never changes a task's verdict (decision 5).
	Restore: {Running: {To: Aborted}},
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
