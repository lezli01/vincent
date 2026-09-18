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
		Send, Answer, Cancel, Archive, HandOff, Close,
		TurnStarted, InputRequested, InputClosed, TurnFinished, TurnInterrupted,
	}
	// want[state][action] is the state the pair reaches; a missing entry
	// means the pair is illegal and must be a 409.
	want := map[State]map[Action]State{
		Idle: {
			Send:    Running,
			Archive: Archived,
			HandOff: HandedOff,
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
		Archived:  {},
		HandedOff: {},
		Closed:    {},
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

// TestLinkedTransitionTable walks every (state, action) pair under the table
// for a chat linked to a task (task 119 decision 2). It differs from the free
// table in `idle` alone: `close` is the way out, and `archive` and `hand_off`
// are not in it at all, because the worktree they would remove or transfer is
// the task's.
func TestLinkedTransitionTable(t *testing.T) {
	allActions := []Action{
		Send, Answer, Cancel, Archive, HandOff, Close,
		TurnStarted, InputRequested, InputClosed, TurnFinished, TurnInterrupted,
	}
	want := map[State]map[Action]State{
		Idle: {
			Send:  Running,
			Close: Closed,
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
		Archived:  {},
		HandedOff: {},
		Closed:    {},
	}
	for _, s := range All {
		for _, a := range allActions {
			next, ok := NextFor(true, s, a)
			wantNext, wantOK := want[s][a]
			if ok != wantOK {
				t.Errorf("NextFor(linked, %s, %s) legal = %v, want %v", s, a, ok, wantOK)
				continue
			}
			if ok && next != wantNext {
				t.Errorf("NextFor(linked, %s, %s) = %s, want %s", s, a, next, wantNext)
			}
			if got := AllowedFor(true, s, a); got != wantOK {
				t.Errorf("AllowedFor(linked, %s, %s) = %v, want %v", s, a, got, wantOK)
			}
		}
	}
	if got, want := ActionsFor(true, Idle), []Action{Close, Send}; !reflect.DeepEqual(got, want) {
		t.Errorf("ActionsFor(linked, idle) = %v, want %v", got, want)
	}
}

// TestTheThreeTerminalStates pins the shape of the lifecycle: a chat is never
// parked — a failed turn leaves it idle and usable, which is the difference
// from §6 that made a separate FSM worth having — and it ends in exactly one
// of three ways.
//
// This was TestTheTwoTerminalStates, and before that
// TestArchivedIsTheOnlyTerminal; each is amended rather than patched. §5.5's
// "archived is the only terminal state" was amended 2026-09-01 by task 074,
// because handing a chat off transfers the worktree that archiving would
// remove; its "two terminal states" was amended 2026-09-17 by task 119,
// because closing a chat linked to a task must leave the task's worktree
// exactly where archiving would remove it.
func TestTheThreeTerminalStates(t *testing.T) {
	terminal := map[State]bool{Archived: true, HandedOff: true, Closed: true}
	for _, s := range All {
		if got, want := Terminal(s), terminal[s]; got != want {
			t.Errorf("Terminal(%s) = %v, want %v", s, got, want)
		}
		if len(Actions(s)) == 0 && len(ActionsFor(true, s)) == 0 && !terminal[s] {
			t.Errorf("state %s is a dead end but is not terminal", s)
		}
	}
	// A closed chat accepts nothing under either table.
	for _, a := range []Action{Send, Answer, Cancel, Archive, HandOff, Close} {
		if Allowed(Closed, a) || AllowedFor(true, Closed, a) {
			t.Errorf("closed accepts %s", a)
		}
	}
	// A handed-off chat accepts nothing at all: not another message, not an
	// answer, not a cancel, not an archive, and not a second handoff.
	for _, a := range []Action{Send, Answer, Cancel, Archive, HandOff} {
		if Allowed(HandedOff, a) {
			t.Errorf("Allowed(handed_off, %s) = true", a)
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
		{HandedOff, false},
		{Closed, false},
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
	if got, want := Actions(Idle), []Action{Archive, HandOff, Send}; !reflect.DeepEqual(got, want) {
		t.Errorf("Actions(idle) = %v, want %v", got, want)
	}
	if got := Actions(Archived); len(got) != 0 {
		t.Errorf("Actions(archived) = %v, want none", got)
	}
	if got := Actions(HandedOff); len(got) != 0 {
		t.Errorf("Actions(handed_off) = %v, want none", got)
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
