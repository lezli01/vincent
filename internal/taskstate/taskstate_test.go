package taskstate

import (
	"slices"
	"testing"
)

// allActions is every action the table may contain, human and engine.
var allActions = []Action{
	Cancel, Pause, Resume, Retry, Repair, Skip, Answer, Approve, Reject, Archive, FollowUp, Chat,
	Admit, Gate, RequestInput, Complete, Fail, Interrupt, InputClosed, Park,
	FanOut, ChildrenSettled, Restore,
}

// wantTransitions is the §6 transition table restated independently of the
// implementation: every (state, action) pair that must be allowed, and the
// state it must produce. Every pair not listed here must be rejected.
var wantTransitions = map[State]map[Action]State{
	Queued: {
		Cancel: Aborted,
		Pause:  Paused,
		Admit:  Running,
	},
	Running: {
		Cancel:       Aborted,
		Pause:        Running, // deferred to the step boundary
		Gate:         AwaitingGate,
		RequestInput: AwaitingInput,
		FanOut:       AwaitingChildren,
		Complete:     Done,
		Fail:         Blocked,
		Interrupt:    Queued,
		Park:         Paused,
		// The aborted-origin end of a follow-up run (task 027 decision 7).
		// Its done-origin twin is Complete, one line above.
		Restore: Aborted,
	},
	AwaitingGate: {
		Cancel:  Aborted,
		Skip:    Queued,
		Approve: Queued,
		Reject:  Blocked,
		Chat:    AwaitingGate, // a linked chat moves nothing (task 115)
	},
	AwaitingInput: {
		Cancel:      Aborted,
		Answer:      Running,
		Fail:        Blocked,
		Interrupt:   Queued,
		InputClosed: Running, // unanswered wait, retries remaining (§7.4)
	},
	// A parked fan-out parent: cancel is the only human action, and the
	// scheduler's ChildrenSettled is the only other way out (task 014).
	// Notably absent is Pause — a parent owning nothing running has nothing
	// to pause — and the approve/reject/skip trio, which is why this is not
	// awaiting_gate.
	// Retry's self-transition is the legality marker of task 090: it makes
	// the action legal here so it can cascade to blocked descendants, and
	// resolves to the state the parent is already in, because nothing on the
	// parent's own row changes.
	AwaitingChildren: {
		Cancel:          Aborted,
		Retry:           AwaitingChildren,
		ChildrenSettled: Queued,
	},
	// Blocked is the only state `repair` is valid from (task 025): it is a
	// second producer of blocked → queued, beside retry and skip.
	Blocked: {
		Cancel: Aborted,
		Retry:  Queued,
		Repair: Queued,
		Skip:   Queued,
		Chat:   Blocked,
	},
	Paused: {
		Cancel: Aborted,
		Resume: Queued,
	},
	// The two unarchived non-terminal states, and the only two a follow-up
	// may be asked for from (task 027). Both re-queue; neither gains any
	// other action.
	Done: {
		Archive:  Archived,
		FollowUp: Queued,
		Chat:     Done,
	},
	Aborted: {
		Archive:  Archived,
		FollowUp: Queued,
		Chat:     Aborted,
	},
	Archived: {},
}

// TestTransitionTable walks every state × every action, so neither an added
// transition nor a removed one can slip through unnoticed.
func TestTransitionTable(t *testing.T) {
	for _, from := range All {
		for _, a := range allActions {
			tr, ok := Next(from, a)
			want, wantOK := wantTransitions[from][a]
			switch {
			case wantOK && !ok:
				t.Errorf("Next(%s, %s) rejected; want %s", from, a, want)
			case !wantOK && ok:
				t.Errorf("Next(%s, %s) = %s; want rejected", from, a, tr.To)
			case wantOK && tr.To != want:
				t.Errorf("Next(%s, %s) = %s; want %s", from, a, tr.To, want)
			}
			if ok != Can(from, a) {
				t.Errorf("Can(%s, %s) disagrees with Next", from, a)
			}
		}
	}
}

// TestEveryStateCoveredByTable guards against a state added to All but
// forgotten in the expectations above.
func TestEveryStateCoveredByTable(t *testing.T) {
	for _, s := range All {
		if _, ok := wantTransitions[s]; !ok {
			t.Errorf("state %s has no expectations", s)
		}
	}
	if len(wantTransitions) != len(All) {
		t.Errorf("expectations cover %d states, All has %d", len(wantTransitions), len(All))
	}
}

func TestPauseOfRunningIsDeferred(t *testing.T) {
	tr, ok := Next(Running, Pause)
	if !ok || !tr.Deferred || tr.To != Running {
		t.Fatalf("Next(running, pause) = %+v, %v; want a deferred no-op transition", tr, ok)
	}
	// Every other transition takes effect immediately.
	for _, from := range All {
		for _, a := range allActions {
			if from == Running && a == Pause {
				continue
			}
			if tr, ok := Next(from, a); ok && tr.Deferred {
				t.Errorf("Next(%s, %s) is deferred; only pausing a running task is", from, a)
			}
		}
	}
}

func TestTerminalAndSlots(t *testing.T) {
	for _, s := range All {
		if Terminal(s) != (s == Archived) {
			t.Errorf("Terminal(%s) = %v", s, Terminal(s))
		}
		wantSlot := s == Running || s == AwaitingInput
		if HoldsSlot(s) != wantSlot {
			t.Errorf("HoldsSlot(%s) = %v, want %v", s, HoldsSlot(s), wantSlot)
		}
	}
	// A terminal state accepts nothing.
	for _, a := range allActions {
		if Can(Archived, a) {
			t.Errorf("Can(archived, %s) = true; archived is terminal", a)
		}
	}
	// Every state that holds a slot must have a way to give it back.
	for _, s := range All {
		if !HoldsSlot(s) {
			continue
		}
		if !Can(s, Interrupt) || !Can(s, Cancel) {
			t.Errorf("state %s holds a slot but cannot be interrupted or canceled", s)
		}
	}
}

func TestHumanActionsFrom(t *testing.T) {
	tests := map[State][]Action{
		Queued:           {Cancel, Pause},
		Running:          {Cancel, Pause},
		AwaitingGate:     {Approve, Cancel, Chat, Reject, Skip},
		AwaitingInput:    {Answer, Cancel},
		AwaitingChildren: {Cancel, Retry},
		Blocked:          {Cancel, Chat, Repair, Retry, Skip},
		Paused:           {Cancel, Resume},
		Done:             {Archive, Chat, FollowUp},
		Aborted:          {Archive, Chat, FollowUp},
		Archived:         nil,
	}
	for state, want := range tests {
		got := HumanActionsFrom(state)
		if !slices.Equal(got, want) {
			t.Errorf("HumanActionsFrom(%s) = %v, want %v", state, got, want)
		}
		if !slices.IsSorted(got) {
			t.Errorf("HumanActionsFrom(%s) = %v, want sorted output", state, got)
		}
	}
}

// TestHumanClassification pins which actions clients may invoke: an engine
// event must never be reachable through the API.
func TestHumanClassification(t *testing.T) {
	human := []Action{
		Cancel, Pause, Resume, Retry, Repair, Skip, Answer, Approve, Reject, Archive, FollowUp, Chat,
	}
	engine := []Action{
		Admit, Gate, RequestInput, Complete, Fail, Interrupt, InputClosed, Park,
		FanOut, ChildrenSettled, Restore,
	}
	for _, a := range human {
		if !Human(a) {
			t.Errorf("Human(%s) = false", a)
		}
	}
	for _, a := range engine {
		if Human(a) {
			t.Errorf("Human(%s) = true; engine events are not client actions", a)
		}
	}
	if len(human)+len(engine) != len(allActions) {
		t.Errorf("action classification covers %d actions, allActions has %d",
			len(human)+len(engine), len(allActions))
	}
}

// TestSettledIsNotTerminal pins task 014 decision 20: the join's wait
// condition is its own predicate, and widening Terminal to mean it would lie
// to every other caller — a done task can still be archived.
func TestSettledIsNotTerminal(t *testing.T) {
	for _, s := range []State{Done, Aborted, Archived} {
		if !Settled(s) {
			t.Errorf("Settled(%s) = false, want true", s)
		}
	}
	// The three that hold no slot but still need a human: a join must wait
	// for them rather than merge around them.
	for _, s := range []State{Blocked, AwaitingGate, Paused, Queued, Running, AwaitingChildren, AwaitingInput} {
		if Settled(s) {
			t.Errorf("Settled(%s) = true; a join would merge over unfinished work", s)
		}
	}
	if Terminal(Done) || Terminal(Aborted) {
		t.Error("Terminal widened to mean Settled; it must stay 'no further transition possible'")
	}
}

// TestAwaitingChildrenHoldsNoSlot is what makes fan-out deadlock-free at any
// depth: a parent releases its slot *before* its children need one, so there
// is no hold-and-wait anywhere in the chain, under any cap.
func TestAwaitingChildrenHoldsNoSlot(t *testing.T) {
	if HoldsSlot(AwaitingChildren) {
		t.Error("awaiting_children holds a slot; a deep fan-out would deadlock under the caps")
	}
}

// TestAwaitingChildrenOffersCancelAndRetry is the reason it is not a reuse of
// awaiting_gate: approve, reject and skip would be actions the API accepts
// and the TUI renders while they mean nothing. `retry` does mean something
// (task 090) — it re-admits the blocked lanes holding the join open — and
// `cancel`, which ends them, is the only other one.
func TestAwaitingChildrenOffersCancelAndRetry(t *testing.T) {
	got := HumanActionsFrom(AwaitingChildren)
	if !slices.Equal(got, []Action{Cancel, Retry}) {
		t.Errorf("HumanActionsFrom(awaiting_children) = %v, want [cancel retry]", got)
	}
}

// TestRetryIsLegalOnlyFromBlockedAndParked pins the whole from-set of the one
// action task 090 widened: `blocked`, where it re-runs the failed step, and
// `awaiting_children`, where it cascades to the blocked lanes. Nowhere else —
// a retry from `aborted` in particular is what keeps a cascade from
// resurrecting a lane a human ended (task 014 decision 22).
func TestRetryIsLegalOnlyFromBlockedAndParked(t *testing.T) {
	for _, s := range All {
		want := s == Blocked || s == AwaitingChildren
		if got := Can(s, Retry); got != want {
			t.Errorf("Can(%s, retry) = %v, want %v", s, got, want)
		}
	}
}

// wantHeld is the held table of task 096 decision C, restated independently:
// the (state, action) pairs a caller may ask to land in `paused` instead of
// `queued`. Every pair not listed must be rejected by NextHeld.
var wantHeld = map[State]map[Action]State{
	Blocked: {Retry: Paused},
	Done:    {FollowUp: Paused},
	Aborted: {FollowUp: Paused},
}

// TestHeldTable walks every state × every action through NextHeld, so a held
// form cannot be added or dropped unnoticed.
func TestHeldTable(t *testing.T) {
	for _, from := range All {
		for _, a := range allActions {
			tr, ok := NextHeld(from, a)
			want, wantOK := wantHeld[from][a]
			switch {
			case wantOK && !ok:
				t.Errorf("NextHeld(%s, %s) rejected; want %s", from, a, want)
			case !wantOK && ok:
				t.Errorf("NextHeld(%s, %s) = %s; want rejected", from, a, tr.To)
			case wantOK && tr.To != want:
				t.Errorf("NextHeld(%s, %s) = %s; want %s", from, a, tr.To, want)
			}
			if ok != CanHold(from, a) {
				t.Errorf("CanHold(%s, %s) disagrees with NextHeld", from, a)
			}
		}
	}
}

// TestHeldOnlySubstitutesPausedForQueued pins what makes the held table safe
// to share with the API's 409 rule: every held pair is a human action the
// plain table allows from the same state, whose plain target is `queued`, and
// the held form is immediate. A held action can therefore never reach a state
// its plain form could not, other than waiting in `paused` first.
func TestHeldOnlySubstitutesPausedForQueued(t *testing.T) {
	for _, from := range All {
		for _, a := range allActions {
			held, ok := NextHeld(from, a)
			if !ok {
				continue
			}
			if !Human(a) {
				t.Errorf("NextHeld(%s, %s) allowed on an engine event", from, a)
			}
			plain, plainOK := Next(from, a)
			if !plainOK || plain.To != Queued {
				t.Errorf("NextHeld(%s, %s) allowed, but Next = %+v, %v; want a plain re-queue",
					from, a, plain, plainOK)
			}
			if held.To != Paused || held.Deferred {
				t.Errorf("NextHeld(%s, %s) = %+v; want an immediate paused", from, a, held)
			}
		}
	}
}

// TestRetryFromParkedParentCannotHold is the 400 of decision C: the retry
// legal from `awaiting_children` re-admits the blocked lanes rather than
// taking the parent through `queued`, so it has no held form.
func TestRetryFromParkedParentCannotHold(t *testing.T) {
	if !Can(AwaitingChildren, Retry) {
		t.Fatal("Can(awaiting_children, retry) = false; the cascade is task 090's")
	}
	if CanHold(AwaitingChildren, Retry) {
		t.Error("CanHold(awaiting_children, retry) = true; the cascade never goes through queued")
	}
}

func TestCanSetPriority(t *testing.T) {
	for _, s := range All {
		want := s == Queued || s == Paused
		if CanSetPriority(s) != want {
			t.Errorf("CanSetPriority(%s) = %v, want %v", s, CanSetPriority(s), want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, s := range All {
		if !Valid(s) {
			t.Errorf("Valid(%s) = false", s)
		}
	}
	if Valid("nonsense") {
		t.Error(`Valid("nonsense") = true`)
	}
}

// TestChatIsOfferedFromExactlyTheFourStoppedStates pins task 115: a chat is
// opened on a task that has stopped with its worktree still there, every row
// is a self-loop, and the lock it places leaves `cancel` alone where cancel is
// legal and nothing anywhere else.
func TestChatIsOfferedFromExactlyTheFourStoppedStates(t *testing.T) {
	want := map[State]bool{Blocked: true, AwaitingGate: true, Done: true, Aborted: true}
	for _, s := range All {
		if got := Can(s, Chat); got != want[s] {
			t.Errorf("Can(%s, chat) = %v, want %v", s, got, want[s])
		}
		if got := Lockable(s); got != want[s] {
			t.Errorf("Lockable(%s) = %v, want %v", s, got, want[s])
		}
		if tr, ok := Next(s, Chat); ok && tr.To != s {
			t.Errorf("Next(%s, chat) = %s, want a self-loop", s, tr.To)
		}
	}
	locked := map[State][]Action{
		Blocked: {Cancel}, AwaitingGate: {Cancel}, Done: nil, Aborted: nil,
	}
	for s, wantActions := range locked {
		if got := LockedActionsFrom(s); !slices.Equal(got, wantActions) {
			t.Errorf("LockedActionsFrom(%s) = %v, want %v", s, got, wantActions)
		}
	}
}
