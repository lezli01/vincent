package issuestate

import "fmt"

// State is an issue lifecycle state (spec §5.6).
type State string

// Issue lifecycle states (spec §5.6, task 130 decision 3). The stored column
// carries no CHECK constraint, so a later state needs no table rebuild — it
// needs a row here.
const (
	// Open is the state an issue is created in and the one a reopen
	// returns it to.
	Open State = "open"
	// Closed carries a Reason; a duplicate also names the issue it
	// duplicates.
	Closed State = "closed"
)

// States lists every state, in the order §5.6 documents them.
var States = []State{Open, Closed}

// Normalize maps a stored state onto the vocabulary: an unknown value — a
// row written by a newer daemon, or hand-edited — reads as Open, the state
// from which every human action is still offered.
func Normalize(s State) State {
	for _, v := range States {
		if v == s {
			return s
		}
	}
	return Open
}

// Action moves an issue between states.
type Action string

// Issue actions (spec §5.6). Close and Reopen are what a human or an agent
// asks for; RemoteClosed and RemoteReopened are sync reporting what the
// remote now says (task 130 decision 9).
const (
	Close          Action = "close"
	Reopen         Action = "reopen"
	RemoteClosed   Action = "remote_closed"
	RemoteReopened Action = "remote_reopened"
)

// Actions lists every action.
var Actions = []Action{Close, Reopen, RemoteClosed, RemoteReopened}

// Reason is why a closed issue was closed (decision 3). It is GitHub's
// vocabulary, so sync can carry it both ways without a mapping.
type Reason string

// Close reasons.
const (
	Completed  Reason = "completed"
	NotPlanned Reason = "not_planned"
	Duplicate  Reason = "duplicate"
)

// Reasons lists every close reason.
var Reasons = []Reason{Completed, NotPlanned, Duplicate}

// ValidReason reports whether r is a known close reason. The empty reason
// is not one: ResolveReason is where "" acquires its default.
func ValidReason(r Reason) bool {
	for _, v := range Reasons {
		if v == r {
			return true
		}
	}
	return false
}

// Actor is who asked for a write. It decides which actions are legal and
// whether asking for the current state is an error or a no-op.
type Actor string

// Actors.
const (
	Human Actor = "human"
	Agent Actor = "agent"
	Sync  Actor = "sync"
)

// Actors lists every actor.
var Actors = []Actor{Human, Agent, Sync}

// ValidActor reports whether a is a known actor.
func ValidActor(a Actor) bool {
	for _, v := range Actors {
		if v == a {
			return true
		}
	}
	return false
}

// ResolveReason returns the reason a transition records. A closing action
// defaults an empty reason to Completed — GitHub's own default — and refuses
// an unknown one; an opening action carries no reason, and refuses one, since
// a reopened issue has none to keep.
func ResolveReason(a Action, r Reason) (Reason, error) {
	switch a {
	case Close, RemoteClosed:
		if r == "" {
			return Completed, nil
		}
		if !ValidReason(r) {
			return "", fmt.Errorf("unknown close reason %q", r)
		}
		return r, nil
	case Reopen, RemoteReopened:
		if r != "" {
			return "", fmt.Errorf("%s takes no reason, got %q", a, r)
		}
		return "", nil
	default:
		return "", fmt.Errorf("unknown issue action %q", a)
	}
}

// target is the state each action leads to.
func target(a Action) (State, bool) {
	switch a {
	case Close, RemoteClosed:
		return Closed, true
	case Reopen, RemoteReopened:
		return Open, true
	default:
		return "", false
	}
}

// Next reports the state a (state, action, actor) triple reaches. ok is
// false when the triple is illegal; noop is true when it is legal but
// changes nothing — sync reporting the state the issue is already in, after
// which the caller writes nothing and announces nothing. s is normalized
// first.
func Next(s State, a Action, by Actor) (next State, noop, ok bool) {
	s = Normalize(s)
	to, known := target(a)
	if !known || !ValidActor(by) {
		return "", false, false
	}
	remote := a == RemoteClosed || a == RemoteReopened
	if remote && by != Sync {
		return "", false, false
	}
	if s == to {
		if by == Sync {
			return s, true, true
		}
		return "", false, false
	}
	return to, false, true
}

// Allowed reports whether Next would accept the triple.
func Allowed(s State, a Action, by Actor) bool {
	_, _, ok := Next(s, a, by)
	return ok
}

// HumanActionsFrom lists what a human may ask for in state s — the
// affordance a client renders directly.
func HumanActionsFrom(s State) []Action {
	var out []Action
	for _, a := range []Action{Close, Reopen} {
		if Allowed(s, a, Human) {
			out = append(out, a)
		}
	}
	return out
}
