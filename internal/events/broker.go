// Package events is the daemon's post-commit fan-out (spec §13.3): the
// store's event hook publishes every committed durable event here, and
// subscribers — the scheduler's wake, the SSE endpoints — receive it after
// the database has recorded it, never before.
//
// Two kinds of traffic flow through the broker, with different loss rules
// (phase 2 decision):
//
//   - Durable state events. A slow channel subscriber is disconnected — it
//     can resume losslessly from the events table via `Last-Event-ID`, so
//     dropping the connection loses nothing.
//   - Live output. Ephemeral and high-volume; a slow subscriber drops
//     chunks. The transcript file is the durable copy.
package events

import (
	"sync"

	"github.com/lezli01/vincent/internal/store"
)

// Chunk is one ephemeral live-output item on a task's stream (§13.3):
// agent.output, agent.tool_use, agent.usage, or command.output.
type Chunk struct {
	Type    string
	Payload map[string]any
}

// Broker fans committed events out to subscribers. The zero value is not
// usable; call New.
type Broker struct {
	mu     sync.Mutex
	closed bool
	// funcs are internal subscribers (the scheduler's wake). They run
	// synchronously on the publishing goroutine, must not block, and are
	// never dropped.
	funcs []func(*store.Event)
	subs  map[*EventSub]struct{}
	outs  map[int64]map[*OutputSub]struct{}
}

// New returns an open broker.
func New() *Broker {
	return &Broker{
		subs: map[*EventSub]struct{}{},
		outs: map[int64]map[*OutputSub]struct{}{},
	}
}

// OnEvent registers an internal subscriber. fn runs on the publishing
// goroutine for every event and must not block — it is the same contract as
// store.SetEventHook, one hop downstream.
func (b *Broker) OnEvent(fn func(*store.Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.funcs = append(b.funcs, fn)
}

// Publish delivers one committed event. Wire this to store.SetEventHook: it
// never blocks the writing goroutine — a channel subscriber whose buffer is
// full is disconnected instead (it resumes via Last-Event-ID).
func (b *Broker) Publish(e *store.Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	fns := b.funcs
	var overflowed []*EventSub
	for sub := range b.subs {
		select {
		case sub.ch <- e:
		default:
			overflowed = append(overflowed, sub)
		}
	}
	for _, sub := range overflowed {
		delete(b.subs, sub)
		close(sub.ch)
	}
	b.mu.Unlock()

	for _, fn := range fns {
		fn(e)
	}
}

// PublishOutput delivers one live-output chunk to the task's subscribers.
// A full subscriber buffer drops the chunk for that subscriber only.
func (b *Broker) PublishOutput(taskID int64, c Chunk) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for sub := range b.outs[taskID] {
		select {
		case sub.ch <- c:
		default:
			sub.dropped++
		}
	}
}

// OutputSubscribers reports how many live-output subscribers a task has.
// Chunks published with nobody listening are dropped by design, so a caller
// that must not lose one — a test, mainly — can wait for a reader to attach.
func (b *Broker) OutputSubscribers(taskID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.outs[taskID])
}

// EventSub is one durable-event subscription. C closes when the subscriber
// fell behind or the broker shut down; either way the client reconnects and
// resumes from its cursor.
type EventSub struct {
	C  <-chan *store.Event
	ch chan *store.Event
	b  *Broker
}

// SubscribeEvents registers a durable-event subscriber with the given
// buffer. Close the subscription when done.
func (b *Broker) SubscribeEvents(buffer int) *EventSub {
	sub := &EventSub{ch: make(chan *store.Event, buffer), b: b}
	sub.C = sub.ch
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(sub.ch)
		return sub
	}
	b.subs[sub] = struct{}{}
	return sub
}

// Close unsubscribes. Idempotent, and safe after an overflow disconnect.
func (s *EventSub) Close() {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.subs[s]; ok {
		delete(s.b.subs, s)
		close(s.ch)
	}
}

// OutputSub is one task's live-output subscription.
type OutputSub struct {
	C       <-chan Chunk
	ch      chan Chunk
	b       *Broker
	taskID  int64
	dropped uint64
}

// SubscribeOutput registers a live-output subscriber for one task.
func (b *Broker) SubscribeOutput(taskID int64, buffer int) *OutputSub {
	sub := &OutputSub{ch: make(chan Chunk, buffer), b: b, taskID: taskID}
	sub.C = sub.ch
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(sub.ch)
		return sub
	}
	if b.outs[taskID] == nil {
		b.outs[taskID] = map[*OutputSub]struct{}{}
	}
	b.outs[taskID][sub] = struct{}{}
	return sub
}

// Dropped reports how many chunks this subscriber missed to backpressure.
func (s *OutputSub) Dropped() uint64 {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.dropped
}

// Close unsubscribes. Idempotent.
func (s *OutputSub) Close() {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	set := s.b.outs[s.taskID]
	if _, ok := set[s]; ok {
		delete(set, s)
		if len(set) == 0 {
			delete(s.b.outs, s.taskID)
		}
		close(s.ch)
	}
}

// Close shuts the broker down: every subscription channel closes (ending
// its SSE stream) and later publishes are dropped. Part of the graceful
// shutdown order — after the runner has persisted its final transitions,
// before the HTTP drain (§12.4).
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		delete(b.subs, sub)
		close(sub.ch)
	}
	for taskID, set := range b.outs {
		for sub := range set {
			delete(set, sub)
			close(sub.ch)
		}
		delete(b.outs, taskID)
	}
}
