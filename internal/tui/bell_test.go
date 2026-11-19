package tui

import (
	"encoding/json"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// bellRoot is a root whose bell counts rings and whose clock the test moves.
func bellRoot(t *testing.T) (m *root, rings *int, now *time.Time) {
	t.Helper()
	m = attentionRoot(t)
	rings, now = new(int), new(time.Time)
	*now = testNow
	m.bell.ring = func() { *rings++ }
	m.bell.now = func() time.Time { return *now }
	return m, rings, now
}

// ring feeds one note through the root's bell decision and runs the ring.
func ring(m *root, n apiclient.Note) {
	if cmd := m.bellCmd(n); cmd != nil {
		cmd()
	}
}

func chatEventNote(id int64, typ string, body map[string]string) apiclient.EventNote {
	payload, _ := json.Marshal(body)
	return apiclient.EventNote{Event: apiclient.Event{ID: id, Type: typ, Payload: payload}}
}

// TestFocusDefaultsTrue: a terminal that never reports focus is treated as
// looked at, so the bell stays quiet until a blur says otherwise.
func TestFocusDefaultsTrue(t *testing.T) {
	m, rings, _ := bellRoot(t)
	if !m.focused {
		t.Fatal("a new root is unfocused; want focused until the terminal says otherwise")
	}
	ring(m, stateEvent(1, stateAwaitingInput))
	if *rings != 0 {
		t.Fatalf("rings = %d with no focus report, want 0", *rings)
	}
	if !m.View().ReportFocus {
		t.Error("the root's view does not ask for focus reports")
	}
}

// TestBellOnlyWhileUnfocused: focused never rings; a blur lets the three
// events that ask for a human ring, once per event id and within the rate
// limit; a focus silences it again.
func TestBellOnlyWhileUnfocused(t *testing.T) {
	m, rings, now := bellRoot(t)

	m.Update(tea.FocusMsg{})
	ring(m, stateEvent(1, stateAwaitingInput))
	ring(m, chatEventNote(2, "chat.state_changed", map[string]string{"state": "awaiting_input"}))
	if *rings != 0 {
		t.Fatalf("rings = %d while focused, want 0", *rings)
	}

	m.Update(tea.BlurMsg{})
	if m.focused {
		t.Fatal("a blur left the root focused")
	}
	ring(m, stateEvent(3, stateRunning))
	ring(m, chatEventNote(4, "chat.state_changed", map[string]string{"state": "running"}))
	ring(m, chatEventNote(5, "chat.turn_changed", map[string]string{"state": "running", "turn_state": "running"}))
	if *rings != 0 {
		t.Fatalf("rings = %d for events that ask nothing, want 0", *rings)
	}

	cases := []apiclient.EventNote{
		stateEvent(10, stateAwaitingInput),
		chatEventNote(20, "chat.state_changed", map[string]string{"state": "awaiting_input"}),
		chatEventNote(30, "chat.turn_changed", map[string]string{"state": "running", "turn_state": "done"}),
		chatEventNote(40, "chat.turn_changed", map[string]string{"state": "running", "turn_state": "failed"}),
	}
	for i, n := range cases {
		*now = testNow.Add(time.Duration(i+1) * 2 * bellInterval)
		ring(m, n)
		if *rings != i+1 {
			t.Fatalf("rings = %d after %s, want %d", *rings, n.Event.Type, i+1)
		}
	}

	// A Last-Event-ID replay is silent, even past the rate limit.
	*now = now.Add(2 * bellInterval)
	ring(m, cases[1])
	if *rings != len(cases) {
		t.Fatalf("rings = %d after a replay, want %d", *rings, len(cases))
	}

	// A burst inside the window is one interruption, and the swallowed
	// event cannot ring on replay later either.
	ring(m, stateEvent(50, stateAwaitingInput))
	ring(m, stateEvent(51, stateAwaitingInput))
	if *rings != len(cases)+1 {
		t.Fatalf("rings = %d after a burst, want %d", *rings, len(cases)+1)
	}
	*now = now.Add(2 * bellInterval)
	ring(m, stateEvent(51, stateAwaitingInput))
	if *rings != len(cases)+1 {
		t.Fatalf("rings = %d replaying a rate-limited event, want %d", *rings, len(cases)+1)
	}

	m.Update(tea.FocusMsg{})
	*now = now.Add(2 * bellInterval)
	ring(m, stateEvent(60, stateAwaitingInput))
	if *rings != len(cases)+1 {
		t.Fatalf("rings = %d after focus came back, want %d", *rings, len(cases)+1)
	}
}

// TestBellDecidedOncePerNote: the bell is the root's, decided as the note
// arrives — before the broadcast that both boards hear — so the event id is
// recorded once and nothing downstream can ring it again.
func TestBellDecidedOncePerNote(t *testing.T) {
	m, rings, _ := bellRoot(t)
	m.focused = false
	m.notes = make(chan apiclient.Note)
	m.updateNote(stateEvent(1, stateAwaitingInput))
	// The note's batch holds the ring among the views' commands; run only
	// the bell's decision a second time to prove the id was recorded.
	ring(m, stateEvent(1, stateAwaitingInput))
	if *rings != 0 {
		t.Fatalf("rings = %d replaying an event updateNote already decided, want 0", *rings)
	}
	if m.bell.lastEvent != 1 {
		t.Fatalf("lastEvent = %d, want the note's id recorded once", m.bell.lastEvent)
	}
}
