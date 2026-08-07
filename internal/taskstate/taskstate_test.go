package taskstate

import (
	"slices"
	"testing"
)

// allActions is every action the table may contain, human and engine.
var allActions = []Action{
	Cancel, Pause, Resume, Retry, Skip, Answer, Approve, Reject, Archive,
	Admit, Gate, RequestInput, Complete, Fail, Interrupt, Park,
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
		Complete:     Done,
		Fail:         Blocked,
		Interrupt:    Queued,
		Park:         Paused,
	},
	AwaitingGate: {
		Cancel:  Aborted,
		Skip:    Queued,
		Approve: Queued,
		Reject:  Blocked,
	},
	AwaitingInput: {
		Cancel:    Aborted,
		Answer:    Running,
		Fail:      Blocked,
		Interrupt: Queued,
	},
	Blocked: {
		Cancel: Aborted,
		Retry:  Queued,
		Skip:   Queued,
	},
	Paused: {
		Cancel: Aborted,
		Resume: Queued,
	},
	Done: {
		Archive: Archived,
	},
	Aborted: {
		Archive: Archived,
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
		Queued:        {Cancel, Pause},
		Running:       {Cancel, Pause},
		AwaitingGate:  {Approve, Cancel, Reject, Skip},
		AwaitingInput: {Answer, Cancel},
		Blocked:       {Cancel, Retry, Skip},
		Paused:        {Cancel, Resume},
		Done:          {Archive},
		Aborted:       {Archive},
		Archived:      nil,
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
	human := []Action{Cancel, Pause, Resume, Retry, Skip, Answer, Approve, Reject, Archive}
	engine := []Action{Admit, Gate, RequestInput, Complete, Fail, Interrupt, Park}
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
