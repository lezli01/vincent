package taskrun

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// StatusMessageLimit bounds a step's status message (§5.4, §13.3, task 033).
//
// 256 bytes is a line, not a report: the message is rendered in a board cell
// and on one attempt line, and anything longer is a transcript. Over-long
// text is truncated rather than refused — the caller is a script or an agent
// mid-run, and failing its status write because it was wordy would turn a
// display nicety into a step failure.
const StatusMessageLimit = 256

// statusMinInterval is the floor between two persisted status writes of one
// step run. A write inside the floor is not rejected: it is coalesced, and
// the latest value lands when the floor expires, so a step that narrates in a
// tight loop costs one `events` row per second rather than thousands.
//
// The throttle is leading-edge on purpose. The first write after a quiet
// period goes through immediately, which is what the live reading needs — a
// step that says one thing and then works for ten minutes must not have that
// one thing delayed.
const statusMinInterval = time.Second

// SetStepStatus records what a running step says about itself (§5.4, task
// 033).
//
// It is the engine's entry point for
// `POST /v1/tasks/{id}/steps/{step_id}/status`, and it is here rather than in
// the API for the reason every other write is: the engine owns the step_run
// rows, and this one carries policy — §13.3's bounds and the coalescing
// floor — that has to hold whoever calls it.
//
// The task lookup is separate from the row resolution so the two failures
// stay distinguishable: an unknown task is store.ErrNotFound (a 404) and a
// step that is not running is store.ErrStepNotRunning (a 409). A write
// addressed at a finished attempt is never a silent no-op — a script still
// reporting progress after its step was killed should learn that.
func (r *Runner) SetStepStatus(ctx context.Context, taskID int64, stepID, message string) error {
	if _, err := r.deps.Store.GetTask(ctx, taskID); err != nil {
		return err
	}
	// Resolved before the throttle is consulted, and unconditionally: the 409
	// for a step that is not running must not depend on how recently
	// something else wrote a status, and the throttle needs a row id to key
	// on. The row can still finish between here and the write, which the
	// write itself catches — it re-resolves inside its own transaction.
	runID, err := r.deps.Store.RunningStepRunID(ctx, taskID, stepID)
	if err != nil {
		return err
	}
	msg := NormalizeStatusMessage(message)
	if !r.status.admit(runID, msg) {
		// Coalesced, not refused: the caller succeeded, and the value it
		// wrote is the one the pending flush will persist.
		return nil
	}
	if _, _, err := r.deps.Store.SetStepRunStatus(ctx, taskID, stepID, msg); err != nil {
		return err
	}
	r.status.accept(runID, msg)
	return nil
}

// NormalizeStatusMessage puts a status message into the one shape every
// surface can render: a single line of printable text, no longer than
// StatusMessageLimit bytes.
//
// Newlines become spaces rather than a truncation point — a step that writes
// two sentences means both — and other control characters are dropped
// outright, because this text lands in a table cell and an embedded escape
// sequence there is a terminal-control bug wearing a status message (§16).
// Truncation is on a rune boundary: a message cut mid-rune renders as a
// replacement character, which reads as corruption rather than as brevity.
func NormalizeStatusMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r == utf8.RuneError, unicode.IsControl(r):
			// Dropped: the C0 and C1 ranges, and any byte that is not valid
			// UTF-8 (which ranging over a string yields as RuneError).
		default:
			b.WriteRune(r)
		}
	}
	// Collapse the runs of whitespace the flattening just created, so a
	// message written as a paragraph reads as a sentence rather than as gaps.
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) <= StatusMessageLimit {
		return out
	}
	cut := StatusMessageLimit
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return strings.TrimRight(out[:cut], " ")
}

// statusThrottle enforces statusMinInterval per step run. It holds one slot
// per run that has spoken recently; a slot is retired an interval after its
// last write, so a daemon running for a week accumulates nothing.
type statusThrottle struct {
	// interval is a field rather than the constant so tests can drive the
	// coalescing without spending a wall-clock second per case. It is
	// deliberately not a config key: the floor is a property of the events
	// table's shape (§13.3), not an operator's choice.
	interval time.Duration
	// write persists a coalesced value, addressed at the row it was written
	// against. Injected so the throttle is testable without a store, and so
	// the flush writes through the runner's shutdown-proof context rather
	// than a request's.
	write func(runID int64, message string)

	mu    sync.Mutex
	slots map[int64]*statusSlot
}

// statusSlot is one step run's recent status traffic.
type statusSlot struct {
	last    time.Time
	pending string
	// armed is the timer that will flush pending; nil when nothing waits.
	armed *time.Timer
}

func newStatusThrottle(write func(runID int64, message string)) *statusThrottle {
	return &statusThrottle{
		interval: statusMinInterval,
		write:    write,
		slots:    map[int64]*statusSlot{},
	}
}

// admit reports whether a write for runID may go straight to the database.
// When it may not, message is remembered as the pending value and a flush is
// armed for the remainder of the floor, so the caller still succeeds and the
// latest value still lands — exactly once.
func (t *statusThrottle) admit(runID int64, message string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot := t.slots[runID]
	if slot == nil || time.Since(slot.last) >= t.interval {
		return true
	}
	if slot.pending == message {
		// Nothing new to say. The store would drop this as a duplicate
		// anyway, and not arming a timer for it stops a step that repeats
		// itself from keeping a slot alive forever.
		return false
	}
	slot.pending = message
	if slot.armed == nil {
		slot.armed = time.AfterFunc(t.interval-time.Since(slot.last), func() { t.flush(runID) })
	}
	return false
}

// accept records that runID's status was just persisted, which is what makes
// the next write inside the floor coalesce instead of reaching the database.
func (t *statusThrottle) accept(runID int64, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot := t.slots[runID]
	if slot == nil {
		slot = &statusSlot{}
		t.slots[runID] = slot
	}
	slot.last = time.Now()
	slot.pending = message
}

// flush persists whatever the slot ended up holding, then schedules its
// retirement. It writes by row id and does not re-check that the attempt is
// still running: the step wrote this while it *was*, and the floor is the
// daemon's choice about when to persist it, not a licence to lose the last
// thing a step said before it exited.
func (t *statusThrottle) flush(runID int64) {
	t.mu.Lock()
	slot := t.slots[runID]
	if slot == nil {
		t.mu.Unlock()
		return
	}
	message := slot.pending
	slot.armed = nil
	slot.last = time.Now()
	t.mu.Unlock()

	t.write(runID, message)
	time.AfterFunc(t.interval, func() { t.retire(runID) })
}

// retire drops a slot whose step has stopped talking. A write arriving in the
// meantime re-creates it, and a fresh slot admits immediately — which is
// correct, because the floor has passed.
func (t *statusThrottle) retire(runID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.slots[runID]; s != nil && s.armed == nil && time.Since(s.last) >= t.interval {
		delete(t.slots, runID)
	}
}
