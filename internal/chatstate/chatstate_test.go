package chatstate

import (
	"reflect"
	"testing"
)

// TestTransitionTable walks every (state, action) pair — the whole product,
// not just the legal half — so a transition added to the table without a
// decision behind it shows up as a failing case here rather than as a chat
// that quietly does something §5.5 does not describe.
func TestTransitionTable(t *testing.T) {
	allActions := []Action{
		Send, Answer, Cancel, Archive,
		TurnStarted, InputRequested, InputClosed, TurnFinished, TurnInterrupted,
	}
	// want[state][action] is the state the pair reaches; a missing entry
	// means the pair is illegal and must be a 409.
	want := map[State]map[Action]State{
		Idle: {
			Send:    Running,
			Archive: Archived,
		},
		Running: {
			TurnStarted:     Running,
			InputRequested:  AwaitingInput,
			TurnFinished:    Idle,
			TurnInterrupted: Idle,
			Cancel:          Idle,
		},
		AwaitingInput: {
			Answer:          Running,
			InputClosed:     Running,
			TurnFinished:    Idle,
			TurnInterrupted: Idle,
			Cancel:          Idle,
		},
		Archived: {},
	}
	for _, s := range All {
		for _, a := range allActions {
			next, ok := Next(s, a)
			wantNext, wantOK := want[s][a]
			if ok != wantOK {
				t.Errorf("Next(%s, %s) legal = %v, want %v", s, a, ok, wantOK)
				continue
			}
			if ok && next != wantNext {
				t.Errorf("Next(%s, %s) = %s, want %s", s, a, next, wantNext)
			}
			if got := Allowed(s, a); got != wantOK {
				t.Errorf("Allowed(%s, %s) = %v, want %v", s, a, got, wantOK)
			}
		}
	}
}

// TestArchivedIsTheOnlyTerminal pins the shape of the lifecycle: a chat is
// never parked. A failed turn leaves the chat idle and usable, which is the
// difference from §6 that made a separate FSM worth having.
func TestArchivedIsTheOnlyTerminal(t *testing.T) {
	for _, s := range All {
		if got, want := Terminal(s), s == Archived; got != want {
			t.Errorf("Terminal(%s) = %v, want %v", s, got, want)
		}
		if len(Actions(s)) == 0 && s != Archived {
			t.Errorf("state %s is a dead end but is not archived", s)
		}
	}
}

// TestHoldsProcess is the cap's definition (§11, decision 1). awaiting_input
// counts because the process is alive on its stdin — a cap that did not count
// it would be a cap on nothing.
func TestHoldsProcess(t *testing.T) {
	for _, tc := range []struct {
		state State
		want  bool
	}{
		{Idle, false},
		{Running, true},
		{AwaitingInput, true},
		{Archived, false},
	} {
		if got := HoldsProcess(tc.state); got != tc.want {
			t.Errorf("HoldsProcess(%s) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, s := range All {
		if !Valid(s) {
			t.Errorf("Valid(%s) = false", s)
		}
	}
	for _, s := range []State{"", "queued", "blocked", "completed", "IDLE"} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

// TestActionsSorted keeps the affordance list stable, since a client renders
// it directly.
func TestActionsSorted(t *testing.T) {
	if got, want := Actions(Idle), []Action{Archive, Send}; !reflect.DeepEqual(got, want) {
		t.Errorf("Actions(idle) = %v, want %v", got, want)
	}
	if got := Actions(Archived); len(got) != 0 {
		t.Errorf("Actions(archived) = %v, want none", got)
	}
}

// TestTurnStates: a turn is running, then it is one of three endings, forever.
func TestTurnStates(t *testing.T) {
	if TurnTerminal(TurnRunning) {
		t.Error("a running turn is terminal")
	}
	for _, s := range []TurnState{TurnDone, TurnFailed, TurnInterruptedState} {
		if !TurnTerminal(s) {
			t.Errorf("TurnTerminal(%s) = false", s)
		}
	}
}

// TestNoStateCollidesWithATaskState is the point of decision 6 and of §6
// staying unchanged: the two vocabularies are separate on purpose, so a reader
// who sees `blocked` knows it is a task and one who sees `idle` knows it is a
// chat. Only the words are checked — taskstate is deliberately not imported,
// because chatstate is a leaf and importing it would be the coupling this
// package exists to avoid.
func TestNoStateCollidesWithATaskState(t *testing.T) {
	taskStates := map[State]bool{
		"queued": true, "running": true, "blocked": true, "paused": true,
		"completed": true, "failed": true, "canceled": true, "archived": true,
	}
	var shared []State
	for _, s := range All {
		if taskStates[s] {
			shared = append(shared, s)
		}
	}
	// `running` and `archived` are shared words by design — they mean the
	// same thing in both. Anything else would be a collision worth arguing.
	want := []State{Running, Archived}
	if !reflect.DeepEqual(shared, want) {
		t.Errorf("states shared with §6 = %v, want exactly %v", shared, want)
	}
}
