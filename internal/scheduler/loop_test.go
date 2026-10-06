package scheduler

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestLoopTickReleasesExpiredHold drives the loop itself, which the tests
// that call admit directly cannot reach: a task whose §11 hold expires has no
// state change to Wake anyone, so the tickInterval ticker is what admits it
// in production (task 003). It runs in a synctest bubble with no injected
// clock — the bubble's clock is the clock — so the hold and the tick are
// measured against the same virtual time and every boundary is exact.
func TestLoopTickReleasesExpiredHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 10)
		p := h.project(t, "proj", nil)
		held := h.task(t, p, "held", store.TaskQueued, 0, time.Minute)
		// Expires between the first tick and the second, so nothing but the
		// second tick can be what admits it.
		start := time.Now()
		h.hold(t, held, start.Add(7*time.Second), "retry_backoff")

		h.sched.Start(t.Context())
		t.Cleanup(h.sched.Stop)

		// The loop's first walk, and the first tick: held both times.
		synctest.Wait()
		if got := h.admitter.ids(); len(got) != 0 {
			t.Fatalf("admitted %v while the hold stood", got)
		}
		time.Sleep(tickInterval)
		synctest.Wait()
		if got := h.admitter.ids(); len(got) != 0 {
			t.Fatalf("admitted %v on the first tick, before the hold expired", got)
		}

		// The hold has expired, but no tick has come: nothing wakes the loop.
		time.Sleep(tickInterval - time.Nanosecond)
		synctest.Wait()
		if !time.Now().After(start.Add(7 * time.Second)) {
			t.Fatal("the clock has not passed the hold; the test measures nothing")
		}
		if got := h.admitter.ids(); len(got) != 0 {
			t.Fatalf("admitted %v with no tick and no Wake", got)
		}

		// The second tick admits it, exactly once.
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := h.admitter.ids(); !slices.Equal(got, []int64{held.ID}) {
			t.Fatalf("admitted %v on the tick after the hold expired, want [%d]", got, held.ID)
		}
		time.Sleep(tickInterval)
		synctest.Wait()
		if got := h.admitter.ids(); !slices.Equal(got, []int64{held.ID}) {
			t.Fatalf("admitted %v a tick later, want [%d] once", got, held.ID)
		}

		// Wake is the ordinary path: a newly queued task is admitted on it,
		// with no virtual time passing and no tick.
		fresh := h.task(t, p, "fresh", store.TaskQueued, 0, 0)
		before := time.Now()
		h.sched.Wake()
		synctest.Wait()
		if got := h.admitter.ids(); !slices.Equal(got, []int64{held.ID, fresh.ID}) {
			t.Fatalf("admitted %v after Wake, want [%d %d]", got, held.ID, fresh.ID)
		}
		if elapsed := time.Since(before); elapsed != 0 {
			t.Errorf("Wake admitted after %s of virtual time; it must not wait for a tick", elapsed)
		}
	})
}
