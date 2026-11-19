package tui

import (
	"encoding/json"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// bellInterval is the minimum gap between two terminal bells. Several tasks
// or chats starting to wait at once is one interruption, not five.
const bellInterval = time.Second

// bellState is the root's one terminal bell (task 156). It was the board's,
// for tasks alone; chats asking for a human ring too now, and two rules side
// by side would ring twice for one burst and replay each other's ids, so
// tasks and chats share one once-per-event-id rule and one rate limiter.
//
// The bell rings only while the terminal is unfocused (root.focused): a
// human looking at the TUI sees the row, the badge and the count change, and
// a BEL on top of that is noise. That makes the task bell focus-gated too,
// deliberately. A terminal that never reports focus leaves focused at its
// initial true — so it never rings either, which is the price of not
// guessing: the TUI cannot tell "never reports" from "focused for good".
//
// The TUI never runs notify.command; that is the daemon's outward signal
// (§12.3, task 046), and the bell is only the terminal's.
type bellState struct {
	// ring rings the terminal. Injected so tests can count rings; the
	// default writes BEL straight to stdout, because tea.Printf is
	// unmanaged scrollback output and a no-op under altscreen.
	ring func()
	// now is injected so the rate limit is deterministic under test.
	now func() time.Time
	// lastEvent is the highest event id that has rung, or would have but
	// for the rate limit. Event ids are monotonic, so this alone makes a
	// Last-Event-ID replay silent while a genuinely missed transition still
	// rings.
	lastEvent int64
	lastAt    time.Time
}

func newBellState() bellState { return bellState{ring: ringBell, now: time.Now} }

func ringBell() {
	// Errors are deliberately ignored: a terminal that will not take a BEL
	// is not a reason to disturb the render loop.
	_, _ = os.Stdout.WriteString("\a")
}

// bellCmd is the ring an event earns, or nil. It is decided in the root's
// Update, where focus is known, and rung in a command so the write never
// happens inside the render loop.
func (m *root) bellCmd(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok || !m.bell.ringsFor(ev.Event, m.focused) {
		return nil
	}
	ring := m.bell.ring
	return func() tea.Msg {
		ring()
		return nil
	}
}

// ringsFor decides whether one event rings, and records it.
//
// Three events ask for a human (task 156): a task entering awaiting_input, a
// chat entering awaiting_input, and a chat turn ending done or failed — the
// answer someone is waiting to read. Each rings once per event id: ids are
// monotonic, so a Last-Event-ID replay after a reconnect is silent, while a
// transition that genuinely happened while disconnected still rings — being
// told about work that started waiting while you were away is the point. The
// rate limit collapses a burst into one interruption.
//
// While focused nothing rings, and nothing is recorded either: an event
// seen while the human was looking is not owed a ring later, and its id is
// below anything that arrives after a blur anyway.
func (b *bellState) ringsFor(ev apiclient.Event, focused bool) bool {
	if focused || ev.ID <= b.lastEvent || !asksForHuman(ev) {
		return false
	}
	// The id advances even when the rate limit swallows the ring, so a
	// later replay of this event cannot ring either.
	b.lastEvent = ev.ID
	now := b.now()
	if now.Sub(b.lastAt) < bellInterval {
		return false
	}
	b.lastAt = now
	return true
}

// asksForHuman reads the three events the bell rings for. The payloads are
// the store's: a task state change carries `to`; a chat event carries the
// chat's `state` and, on chat.turn_changed, the turn's `turn_state`
// (internal/store chatEvent).
func asksForHuman(ev apiclient.Event) bool {
	var p struct {
		To        string `json:"to"`
		State     string `json:"state"`
		TurnState string `json:"turn_state"`
	}
	switch ev.Type {
	case "task.state_changed", "chat.state_changed", "chat.turn_changed":
	default:
		return false
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return false
	}
	switch ev.Type {
	case "task.state_changed":
		return p.To == stateAwaitingInput
	case "chat.state_changed":
		return chatNeedsAttention(p.State)
	default:
		return p.TurnState == "done" || p.TurnState == "failed"
	}
}
