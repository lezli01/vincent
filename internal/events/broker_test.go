package events

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

func ev(id int64) *store.Event { return &store.Event{ID: id, Type: "task.state_changed"} }

func TestPublishReachesSubscribers(t *testing.T) {
	b := New()
	var internal []*store.Event
	b.OnEvent(func(e *store.Event) { internal = append(internal, e) })
	sub := b.SubscribeEvents(4)
	defer sub.Close()

	b.Publish(ev(1))
	b.Publish(ev(2))

	if len(internal) != 2 || internal[0].ID != 1 || internal[1].ID != 2 {
		t.Errorf("internal subscriber saw %v, want ids 1,2", internal)
	}
	if got := (<-sub.C).ID; got != 1 {
		t.Errorf("first event id = %d, want 1", got)
	}
	if got := (<-sub.C).ID; got != 2 {
		t.Errorf("second event id = %d, want 2", got)
	}
}

func TestSlowEventSubscriberIsDisconnected(t *testing.T) {
	b := New()
	slow := b.SubscribeEvents(1)
	fast := b.SubscribeEvents(8)
	defer fast.Close()

	b.Publish(ev(1))
	b.Publish(ev(2)) // slow's buffer is full: disconnected, channel closed

	if got := (<-slow.C).ID; got != 1 {
		t.Errorf("buffered event id = %d, want 1", got)
	}
	if _, open := <-slow.C; open {
		t.Error("slow subscriber channel still open; want closed after overflow")
	}
	slow.Close() // must be safe after the overflow disconnect

	// The fast subscriber is unaffected.
	if got := (<-fast.C).ID; got != 1 {
		t.Errorf("fast subscriber first id = %d, want 1", got)
	}
	if got := (<-fast.C).ID; got != 2 {
		t.Errorf("fast subscriber second id = %d, want 2", got)
	}
}

func TestOutputDropsWhenFull(t *testing.T) {
	b := New()
	sub := b.SubscribeOutput(7, 2)
	defer sub.Close()

	for range 5 {
		b.PublishOutput(7, Chunk{Type: "command.output"})
	}
	b.PublishOutput(8, Chunk{Type: "command.output"}) // other task: not ours

	if len(sub.ch) != 2 {
		t.Errorf("buffered chunks = %d, want 2", len(sub.ch))
	}
	if got := sub.Dropped(); got != 3 {
		t.Errorf("dropped = %d, want 3", got)
	}
	select {
	case c := <-sub.C:
		if c.Type != "command.output" {
			t.Errorf("chunk type = %q", c.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no chunk delivered")
	}
}

func TestCloseEndsEverySubscription(t *testing.T) {
	b := New()
	sub := b.SubscribeEvents(4)
	out := b.SubscribeOutput(1, 4)

	b.Close()
	b.Close() // idempotent

	if _, open := <-sub.C; open {
		t.Error("event channel open after Close")
	}
	if _, open := <-out.C; open {
		t.Error("output channel open after Close")
	}
	b.Publish(ev(1)) // dropped, no panic
	b.PublishOutput(1, Chunk{})
	sub.Close() // safe after broker close
	out.Close()

	// Subscribing after Close yields an already-closed channel.
	late := b.SubscribeEvents(1)
	if _, open := <-late.C; open {
		t.Error("late subscription channel open; want closed")
	}
}
