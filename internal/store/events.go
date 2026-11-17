package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Durable event types written outside the engine and the transition path
// (spec §13.3). An archive is visible as task.state_changed with
// `to: archived`; there is no separate task.archived type (PR D decision).
const (
	EventTaskCreated             = "task.created"
	EventProjectCreated          = "project.created"
	EventProjectUpdated          = "project.updated"
	EventProjectDeleted          = "project.deleted"
	EventWorkflowRegistryChanged = "workflow.registry_changed"
	EventDaemonShuttingDown      = "daemon.shutting_down"
	// EventAgentQuotaChanged announces that what the daemon knows about an
	// adapter's usage window changed (task 026): a quota stop was observed,
	// or a successful run proved the window reopened. Payload
	// `{agent, spent, resets_at, source}`; it carries no task_id, like
	// workflow.registry_changed, because the fact is about an adapter rather
	// than about any one task.
	//
	// It is emitted only on a real change — never on a re-observation
	// identical to what is stored — and `scheduler.WakeOn` is false for it:
	// nothing about admission changes when an agent is near-spent. The
	// near-exhausted agent is displayed, never withheld.
	EventAgentQuotaChanged = "agent.quota_changed"
	// EventTaskGitHubPullChanged announces that a task's pull-request link
	// changed (task 052): the reconciler matched one, or a human linked or
	// unlinked one. Payload `{repo, number, source, suppressed}`, empty when
	// the link was cleared.
	//
	// It carries a task_id and is not a transition: the task's state is
	// unchanged, `updated_at` is untouched, and `scheduler.WakeOn` is false
	// for it, because nothing about admission depends on a pull request. It
	// exists so a running TUI re-renders a task whose pull request appeared
	// without polling the endpoint.
	EventTaskGitHubPullChanged = "task.github_pull_changed"
)

// SetEventHook registers fn to run after an event's transaction commits.
// It is the daemon's single notification seam: the scheduler wakes from it
// (spec §11) and T2.7's SSE broker will publish from it, which is why it
// fires strictly post-commit — a subscriber must never observe an event the
// database has not durably recorded.
//
// fn sees events in ascending id order — what makes the SSE streams' dedupe
// by id lossless (§13.3, issue #963). It runs synchronously on a writing
// goroutine, though not always the one that wrote the event: while one
// goroutine is delivering, events other writers commit are queued and it
// delivers them too. An event fn appends itself is delivered after fn
// returns, never from inside it. fn must not block.
func (s *Store) SetEventHook(fn func(*Event)) { s.eventHook.Store(&fn) }

// eventPublisher puts the event hook's deliveries in id order (issue #963).
//
// Every SSE stream dedupes its replay-to-live seam by id (`ev.ID <= lastID`,
// phase 2 / T2.7 decision, §13.3), which is lossless only if the hook sees
// ids in ascending order. Calling the hook straight from each notify did not
// guarantee that: a hook that appends events itself (the daemon's
// children_changed relay, task 014 decision 14) published them before the
// rest of the batch that triggered it, and two writers could publish in the
// opposite order to their commits.
//
// So a transaction's commit queues every event it wrote, under seq (commitTx,
// review F2), and one goroutine at a time drains: pending events are kept
// sorted by id, and whoever finds the queue undrained delivers them, in
// order, until it is empty. An event appended from inside the hook is
// enqueued behind the current batch and delivered by the same drain loop
// after it — never recursively, which is also why the lock is not held while
// the hook runs.
type eventPublisher struct {
	mu       sync.Mutex
	pending  []*Event // sorted by ID, ascending
	draining bool
	// seq spans a transaction's commit and the enqueue of the events it
	// wrote (commitTx), so no two writers can reach the queue in the
	// opposite order to the ids they were assigned. It is taken only while
	// holding the connection and never held while the hook runs, so an
	// append from inside the hook cannot deadlock on it.
	seq sync.Mutex
	// drained, when set, runs after a drain has released the queue. It is a
	// test seam only: a write made from it lands exactly where review F1's
	// lost wakeup did.
	drained func()
}

// notify publishes evs, and the events chained behind each, to the event hook
// in id order. Every event row is written by appendEventTx, whose commitTx
// has already queued the whole transaction's events — so the batch is queued
// before its first is delivered, and an event the hook appends cannot
// overtake the batch's later ones. A caller's post-commit notify therefore
// finds its events queued and only drains; it is kept so a write states what
// it announces, and so an event that never went through commitTx is still
// published.
func (s *Store) notify(evs ...*Event) {
	for _, e := range evs {
		s.enqueue(e)
	}
	s.drain()
}

// enqueue adds e and its follow chain to the publication queue.
func (s *Store) enqueue(e *Event) {
	p := &s.pub
	p.mu.Lock()
	defer p.mu.Unlock()
	// A nil event is a write that had nothing to announce — updateChat takes
	// an empty type for the columns no client is told about (session id,
	// worktree path, pending input) and hands the nil straight here. Publishing
	// it reached every open SSE stream, each of which dereferences the event it
	// is handed, so writing a chat's session id panicked
	// `GET /v1/events` for everyone. Dropped here rather than guarded in three
	// handlers: "there is no event" is this function's business (task 067).
	for ; e != nil; e = e.follow {
		// commitTx already queued every event its transaction wrote, which
		// is all of them; a caller's post-commit notify then finds them
		// queued and only drains. Never queue one twice.
		if e.queued {
			continue
		}
		e.queued = true
		// After every event with an id <= e's, so equal ids keep their
		// arrival order.
		i := len(p.pending)
		for i > 0 && p.pending[i-1].ID > e.ID {
			i--
		}
		p.pending = append(p.pending, nil)
		copy(p.pending[i+1:], p.pending[i:])
		p.pending[i] = e
	}
}

// drain delivers the queue to the hook unless another call is already doing
// so — on this goroutine, further up the stack, or on another one; that call
// delivers whatever was enqueued here before it returns.
func (s *Store) drain() {
	p := &s.pub
	p.mu.Lock()
	if p.draining {
		p.mu.Unlock()
		return
	}
	p.draining = true
	finished := false
	defer func() {
		// Only a panicking hook gets here with draining still set. Clearing
		// it keeps the publisher alive: the events still queued behind the
		// one that panicked are delivered by the next write's drain, not
		// re-delivered from this unwinding goroutine.
		if !finished {
			p.mu.Lock()
			p.draining = false
			p.mu.Unlock()
		}
	}()
	for len(p.pending) > 0 {
		e := p.pending[0]
		p.pending[0] = nil
		p.pending = p.pending[1:]
		p.mu.Unlock()
		if fn := s.eventHook.Load(); fn != nil && *fn != nil {
			(*fn)(e)
		}
		p.mu.Lock()
	}
	// Cleared in the same critical section that saw the queue empty (review
	// F1). Releasing the lock first left a window in which another writer
	// could enqueue, find draining still set and return, and nobody would
	// deliver its event until some unrelated later write.
	p.draining = false
	finished = true
	p.mu.Unlock()
	if p.drained != nil {
		p.drained()
	}
}

// AppendEvent inserts e and assigns its ID (the SSE Last-Event-ID cursor).
// A zero TS means now; a nil Payload is stored as "{}".
func (s *Store) AppendEvent(ctx context.Context, e *Event) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	// A transaction of its own, so the insert reaches the hook through
	// commitTx like every other event write and takes the publication lock
	// in the same order they do: connection first, then seq.
	return s.withTx(ctx, func(tx *sql.Tx) error { return appendEventTx(ctx, tx, e) })
}

// commitTx commits tx and queues the events appendEventTx recorded in it as
// one step under the publication lock, then delivers the queue (review F2).
//
// The single connection already serializes transactions, so ids are
// assigned in commit order; holding seq from the commit until the events are
// queued makes queue order commit order too. Without it a writer could
// commit id N, lose the CPU, and watch another writer commit and deliver
// N+1 first — after which every live stream drops N by its `<= lastID`
// dedupe and a reconnect never replays it (issue #963, cause (a)).
//
// seq is only ever taken by a goroutine that already holds the connection
// (it is inside tx), and nothing holding seq waits for the connection, so
// the two cannot deadlock. The hook runs after both are released: an event
// it appends opens a transaction of its own.
func (s *Store) commitTx(tx *sql.Tx) error {
	p := &s.pub
	p.seq.Lock()
	evs := txEvents.take(tx)
	if err := tx.Commit(); err != nil {
		p.seq.Unlock()
		return err
	}
	for _, e := range evs {
		s.enqueue(e)
	}
	p.seq.Unlock()
	s.drain()
	return nil
}

// rollbackTx rolls tx back and forgets the events it recorded. It is safe
// after a successful commitTx, where it does nothing.
func rollbackTx(tx *sql.Tx) {
	txEvents.take(tx)
	_ = tx.Rollback()
}

// txEvents holds, per open transaction, the events appendEventTx inserted in
// it, until commitTx queues them or rollbackTx drops them. It is keyed by
// the transaction rather than kept on the Store because the helpers that
// append inside a transaction are handed only the *sql.Tx.
var txEvents = txEventLog{m: map[*sql.Tx][]*Event{}}

type txEventLog struct {
	mu sync.Mutex
	m  map[*sql.Tx][]*Event
}

func (l *txEventLog) record(tx *sql.Tx, e *Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[tx] = append(l.m[tx], e)
}

func (l *txEventLog) take(tx *sql.Tx) []*Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	evs := l.m[tx]
	delete(l.m, tx)
	return evs
}

// EventFilter narrows ListEvents. Zero values mean "no filter".
type EventFilter struct {
	AfterID   int64    // only events with id > AfterID (SSE resume cursor)
	Types     []string // empty = all types
	ProjectID int64    // 0 = all projects
	TaskID    int64    // 0 = all tasks (the per-task stream's resume query)
	Limit     int      // 0 = unlimited
}

// MaxEventID returns the largest committed event id, or 0 when no event has
// been written yet. It is the high-water mark an SSE replay pages up to
// (§13.3): a resume walks only to the id that existed when the stream opened
// and lets the subscription carry everything after it, so a replay on a busy
// daemon cannot chase a tail that keeps moving.
func (s *Store) MaxEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	return id, nil
}

// ListEvents returns events matching f in id order — the catch-up query
// behind SSE Last-Event-ID resume (spec §13.3).
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	q := `SELECT id, ts, type, task_id, project_id, payload_json FROM events WHERE id > ?`
	args := []any{f.AfterID}
	if len(f.Types) > 0 {
		//nolint:gosec // G202: placeholders() emits bind markers; the types bind below
		q += ` AND type IN ` + placeholders(len(f.Types))
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.ProjectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.TaskID != 0 {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
	}
	q += ` ORDER BY id ASC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return out, nil
}

func scanEvent(r rowScanner) (*Event, error) {
	var (
		e                 Event
		taskID, projectID sql.NullInt64
		ts, payload       string
	)
	if err := r.Scan(&e.ID, &ts, &e.Type, &taskID, &projectID, &payload); err != nil {
		return nil, err
	}
	if taskID.Valid {
		e.TaskID = &taskID.Int64
	}
	if projectID.Valid {
		e.ProjectID = &projectID.Int64
	}
	e.Payload = json.RawMessage(payload)
	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return nil, err
	}
	return &e, nil
}
