package trigger

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// Task 122: `overrun:` and `concurrency_key:`. Acceptance criteria 1–8 each
// have a test here, named for what it proves.

// overrunSrc is a `type: command` trigger on project 1 — the project
// newFakeAPI creates — grouped by the event's ticket.
func overrunSrc(mode string) string {
	const key = "{{ .Event.ticket }}"
	src := `id: tix
enabled: true
source:
  type: command
  project: 1
  poll_interval: 1s
  command: [poll]
action:
  type: create_task
  title: '{{ .Event.ticket }}'
on_fire: create
overrun: ` + mode + "\n"
	if key != "" {
		src += "concurrency_key: '" + key + "'\n"
	}
	return src
}

// overrunDef is a trigger in the ticket group, which is every case here that
// does not go through the registry.
func overrunDef(t *testing.T, mode string) *Definition {
	t.Helper()
	d, errs := Parse([]byte(overrunSrc(mode)), "tix")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	return d
}

func event(id, ticket string) Event {
	return Event{"id": id, "ticket": ticket}
}

// setState moves a task the fake API created, which is how a test says the
// group emptied. It reads the current state first so a case can walk a task
// through several without tracking where it is.
func setState(t *testing.T, st *store.Store, id int64, state store.TaskState) {
	t.Helper()
	task, err := st.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if task.State == state {
		return
	}
	if _, _, err := st.TransitionTask(t.Context(), id, task.State, state, store.TaskChange{}); err != nil {
		t.Fatalf("transition to %s: %v", state, err)
	}
}

func outcomes(rows []store.TriggerDelivery) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Outcome)
	}
	return out
}

// TestOverrunAbsentFiresAsBefore is acceptance criterion 1: a trigger with no
// `overrun:` never reaches the group, so two events in a row both fire. The
// real assertion is the rest of the package's suites passing unchanged; this
// pins the absent default explicitly.
func TestOverrunAbsentFiresAsBefore(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d, errs := Parse([]byte(overrunSrcBase+"on_fire: create\n"), "tix")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	if d.EffectiveOverrun() != OverrunParallel {
		t.Fatalf("absent overrun = %q", d.EffectiveOverrun())
	}
	for _, id := range []string{"a", "b"} {
		del, err := fire(t.Context(), st, api, d, event(id, "V-1"), ghT0)
		if err != nil {
			t.Fatal(err)
		}
		if del.Outcome != store.DeliveryFired || del.ConcurrencyKey != "" {
			t.Fatalf("event %s = %s, group %q; want fired with no group", id, del.Outcome, del.ConcurrencyKey)
		}
	}
	if got := len(api.requests()); got != 2 {
		t.Errorf("replays = %d, want 2", got)
	}
}

// TestOverrunSkipHoldsTheGroup is acceptance criterion 2: two events in one
// group with the first task unsettled produce one task and one `superseded`
// row.
func TestOverrunSkipHoldsTheGroup(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunSkip)

	first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != store.DeliveryFired || first.TaskID == nil || first.ConcurrencyKey != "V-1" {
		t.Fatalf("first = %+v", first.Judgement)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)

	second, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != store.DeliverySuperseded || !second.WouldSkip {
		t.Fatalf("second = %+v, want superseded", second.Judgement)
	}
	if len(second.InFlight) != 1 || second.InFlight[0] != *first.TaskID {
		t.Errorf("in flight = %v, want [%d]", second.InFlight, *first.TaskID)
	}
	// A different ticket is a different group and fires.
	other, err := fire(t.Context(), st, api, d, event("e3", "V-2"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if other.Outcome != store.DeliveryFired {
		t.Errorf("other group = %s, want fired", other.Outcome)
	}
	if got := len(api.requests()); got != 2 {
		t.Errorf("replays = %d, want 2", got)
	}
}

// TestOverrunSkipReleasesOnSettle: the group holds until the task settles, and
// `done` releases it. Settled is not Terminal — a `done` task is not archived.
func TestOverrunSkipReleasesOnSettle(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunSkip)
	first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)
	setState(t, st, *first.TaskID, store.TaskDone)
	second, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != store.DeliveryFired {
		t.Fatalf("after done = %s, want fired", second.Outcome)
	}
}

// TestOverrunHoldingStatesHoldTheGroup is acceptance criterion 7, widened by
// decision 3: `paused` — the `on_fire: propose` default's unreviewed proposal
// — holds its group, and so do `awaiting_gate` and `awaiting_children`, which
// the issue's own enumeration omitted.
func TestOverrunHoldingStatesHoldTheGroup(t *testing.T) {
	for _, state := range []store.TaskState{
		store.TaskQueued, store.TaskRunning, store.TaskAwaitingInput,
		store.TaskAwaitingGate, store.TaskAwaitingChildren, store.TaskBlocked, store.TaskPaused,
	} {
		t.Run(string(state), func(t *testing.T) {
			st := openStore(t)
			api := newFakeAPI(t, st, 0, "")
			d := overrunDef(t, OverrunSkip)
			first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
			if err != nil {
				t.Fatal(err)
			}
			// The fake API creates every task paused; move it where the case
			// wants it, tolerating the FSM by writing the row directly.
			setState(t, st, *first.TaskID, state)
			second, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
			if err != nil {
				t.Fatal(err)
			}
			if second.Outcome != store.DeliverySuperseded {
				t.Errorf("%s = %s, want the group held", state, second.Outcome)
			}
		})
	}
}

// TestOverrunSettledStatesReleaseTheGroup is the other half of decision 3.
func TestOverrunSettledStatesReleaseTheGroup(t *testing.T) {
	for _, state := range []store.TaskState{store.TaskDone, store.TaskAborted, store.TaskArchived} {
		t.Run(string(state), func(t *testing.T) {
			st := openStore(t)
			api := newFakeAPI(t, st, 0, "")
			d := overrunDef(t, OverrunSkip)
			first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
			if err != nil {
				t.Fatal(err)
			}
			setState(t, st, *first.TaskID, state)
			second, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
			if err != nil {
				t.Fatal(err)
			}
			if second.Outcome != store.DeliveryFired {
				t.Errorf("%s = %s, want the group released", state, second.Outcome)
			}
		})
	}
}

// TestOverrunGroupExcludesDeletedTask: a `fired` row whose task_id went NULL
// — ON DELETE SET NULL, the task was deleted — is not in flight, so it does
// not hold its group forever.
func TestOverrunGroupExcludesDeletedTask(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunSkip)
	if _, err := st.RecordTriggerDelivery(t.Context(), &store.TriggerDelivery{
		TriggerID: "tix", EventID: "gone", DedupeKey: "gone", ConcurrencyKey: "V-1",
		Outcome: store.DeliveryFired, CreatedAt: ghT0,
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.TriggerGroupInFlight(t.Context(), "tix", "V-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("group = %v, want empty: the task is gone", ids)
	}
	del, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliveryFired {
		t.Errorf("against a deleted task = %s, want fired", del.Outcome)
	}
}

// TestOverrunCancelPrevious is acceptance criterion 3: the in-flight task is
// cancelled, the second delivery fires, and its supersede link is readable.
func TestOverrunCancelPrevious(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunCancelPrevious)
	first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)

	second, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != store.DeliveryFired || !second.WouldCancel {
		t.Fatalf("second = %+v, want a cancelling fire", second.Judgement)
	}
	if second.SupersededTaskID == nil || *second.SupersededTaskID != *first.TaskID {
		t.Errorf("supersede link = %v, want %d", second.SupersededTaskID, *first.TaskID)
	}
	if second.TaskID == nil || *second.TaskID == *first.TaskID {
		t.Errorf("the new task is %v, want one of its own", second.TaskID)
	}
	// The cancel was replayed against the first task's §6 route before the
	// create, so the new task's branch is its own.
	var cancelled bool
	for _, r := range api.requests() {
		if strings.HasSuffix(r.path, "/cancel") {
			cancelled = true
		}
	}
	if !cancelled {
		t.Errorf("no cancel was replayed: %+v", api.requests())
	}
	// The link survives a read back through the ledger.
	rows, err := st.ListTriggerDeliveries(t.Context(), "tix", 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SupersededTaskID == nil || *rows[0].SupersededTaskID != *first.TaskID {
		t.Errorf("ledger supersede link = %v", rows[0].SupersededTaskID)
	}
}

// TestOverrunQueueHoldsAndRecords: a held event writes a backlog row and a
// `queued` ledger row, and is not "delivered" — a second identical event
// reaches the overrun step rather than being deduped away (decision 8).
func TestOverrunQueueHoldsAndRecords(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunQueueSerial)
	first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)

	held, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Outcome != store.DeliveryQueued || !held.WouldQueue {
		t.Fatalf("held = %+v, want queued", held.Judgement)
	}
	items, err := st.ListTriggerBacklog(t.Context(), "tix", "V-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].EventID != "e2" {
		t.Fatalf("backlog = %+v", items)
	}
	delivered, err := st.TriggerKeyDelivered(t.Context(), "tix", "e2")
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Error("a queued row counted as delivered; a repeat event would be swallowed as a duplicate")
	}
}

// TestOverrunQueueCoalesceDrainsTheNewest is acceptance criterion 4: three
// events during one in-flight task fire once, on the newest, with two
// `superseded` rows.
func TestOverrunQueueCoalesceDrainsTheNewest(t *testing.T) {
	h := newHarness(t)
	h.write("tix", overrunSrc(OverrunQueueCoalesce))
	d := h.def()

	first, err := fire(t.Context(), h.st, h.api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	for _, id := range []string{"e2", "e3", "e4"} {
		if _, err := fire(t.Context(), h.st, h.api, d, event(id, "V-1"), ghT0); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing drains while the group is busy.
	h.m.drain(t.Context())
	if got := len(h.api.requests()); got != 1 {
		t.Fatalf("replays while busy = %d, want 1", got)
	}
	setState(t, h.st, *first.TaskID, store.TaskDone)
	h.m.drain(t.Context())

	if got := len(h.api.requests()); got != 2 {
		t.Fatalf("replays after the drain = %d, want 2", got)
	}
	if title := h.api.requests()[1].body.Title; title != "V-1" {
		t.Errorf("drained title = %q", title)
	}
	rows := h.ledger("tix")
	// e1 fired, e2–e4 queued, e2 and e3 superseded by the coalesce, e4 fired.
	var superseded, fired int
	for _, r := range rows {
		switch r.Outcome {
		case store.DeliverySuperseded:
			superseded++
		case store.DeliveryFired:
			fired++
		}
	}
	if superseded != 2 || fired != 2 {
		t.Errorf("outcomes = %v, want two superseded and two fired", outcomes(rows))
	}
	left, err := h.st.ListTriggerBacklog(t.Context(), "tix", "V-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("backlog after a coalesced drain = %+v, want empty", left)
	}
}

// TestOverrunQueueSerialDrainsInOrder is acceptance criterion 5: three events
// fire three times, oldest first, never two at once.
func TestOverrunQueueSerialDrainsInOrder(t *testing.T) {
	h := newHarness(t)
	h.write("tix", overrunSrc(OverrunQueueSerial))
	d := h.def()

	del, err := fire(t.Context(), h.st, h.api, d, Event{"id": "e1", "ticket": "V-1", "n": "one"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	live := *del.TaskID
	setState(t, h.st, live, store.TaskRunning)
	for _, n := range []string{"two", "three"} {
		if _, err := fire(t.Context(), h.st, h.api,
			d, Event{"id": "e-" + n, "ticket": "V-1", "n": n}, ghT0); err != nil {
			t.Fatal(err)
		}
	}
	for want := 2; want <= 3; want++ {
		setState(t, h.st, live, store.TaskDone)
		h.m.drain(t.Context())
		reqs := h.api.requests()
		if len(reqs) != want {
			t.Fatalf("after drain %d: replays = %d, want %d", want, len(reqs), want)
		}
		// The next drain is held by the task this one just created.
		h.m.drain(t.Context())
		if got := len(h.api.requests()); got != want {
			t.Fatalf("a second drain fired again: %d replays, want %d", got, want)
		}
		rows := h.ledger("tix")
		id := rows[len(rows)-1].TaskID
		if id == nil {
			t.Fatalf("the drained delivery has no task: %+v", rows[len(rows)-1])
		}
		live = *id
	}
	if got := len(h.api.requests()); got != 3 {
		t.Fatalf("total replays = %d, want 3", got)
	}
	// Arrival order: e1, then the two held events oldest first.
	var ids []string
	for _, r := range h.ledger("tix") {
		if r.Outcome == store.DeliveryFired {
			ids = append(ids, r.EventID)
		}
	}
	if len(ids) != 3 || ids[0] != "e1" || ids[1] != "e-two" || ids[2] != "e-three" {
		t.Errorf("fired in order %v, want [e1 e-two e-three]", ids)
	}
}

// TestOverrunBacklogSurvivesRestart is acceptance criterion 6 in process: a
// held event outlives the manager that held it, because the backlog is a
// table and not a field. The real-binary half is scripts/m16-gate.sh.
func TestOverrunBacklogSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.write("tix", overrunSrc(OverrunQueueSerial))
	d := h.def()
	first, err := fire(t.Context(), h.st, h.api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	if _, err := fire(t.Context(), h.st, h.api, d, event("e2", "V-1"), ghT0); err != nil {
		t.Fatal(err)
	}
	h.restart()
	setState(t, h.st, *first.TaskID, store.TaskDone)
	h.m.drain(t.Context())
	if got := len(h.api.requests()); got != 2 {
		t.Fatalf("replays after a restart and a drain = %d, want 2", got)
	}
}

// TestOverrunBacklogDiscardedOnDisarm is decision 5: a disarm drops the
// backlog and records each held event, as decision 16 drops the cursor.
func TestOverrunBacklogDiscardedOnDisarm(t *testing.T) {
	h := newHarness(t)
	h.write("tix", overrunSrc(OverrunQueueSerial))
	d := h.def()
	first, err := fire(t.Context(), h.st, h.api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	if _, err := fire(t.Context(), h.st, h.api, d, event("e2", "V-1"), ghT0); err != nil {
		t.Fatal(err)
	}
	h.write("tix", strings.Replace(overrunSrc(OverrunQueueSerial),
		"enabled: true", "enabled: false", 1))
	h.m.drain(t.Context())

	items, err := h.st.ListTriggerBacklogFor(t.Context(), "tix")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("backlog after a disarm = %+v, want empty", items)
	}
	rows := h.ledger("tix")
	last := rows[len(rows)-1]
	if last.Outcome != store.DeliverySuperseded || !strings.Contains(last.Detail, "disarmed") {
		t.Errorf("discarded event recorded as %s %q", last.Outcome, last.Detail)
	}
	// Re-arming fires nothing it held.
	h.write("tix", overrunSrc(OverrunQueueSerial))
	setState(t, h.st, *first.TaskID, store.TaskDone)
	h.m.drain(t.Context())
	if got := len(h.api.requests()); got != 1 {
		t.Errorf("replays after re-arming = %d, want 1", got)
	}
}

// TestOverrunBacklogCap: at MaxBacklogPerTrigger the oldest held event is
// dropped and recorded, which is what makes a stuck group explicable.
func TestOverrunBacklogCap(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunQueueSerial)
	first, err := fire(t.Context(), st, api, d, event("e0", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)
	for i := range MaxBacklogPerTrigger + 2 {
		if _, err := fire(t.Context(), st, api, d, event(string(rune('a'+i%26))+strings.Repeat("x", i), "V-1"), ghT0); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.ListTriggerBacklogFor(t.Context(), "tix")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != MaxBacklogPerTrigger {
		t.Fatalf("backlog = %d rows, want the cap of %d", len(items), MaxBacklogPerTrigger)
	}
	var dropped int
	rows, err := st.ListTriggerDeliveries(t.Context(), "tix", 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Outcome == store.DeliverySuperseded && strings.Contains(r.Detail, "cap") {
			dropped++
		}
	}
	if dropped != 2 {
		t.Errorf("cap drops recorded = %d, want 2", dropped)
	}
}

// TestOverrunDrainedEventIsRejudged is decision 6: a drain runs judge again in
// full, so a rate cap met in the meantime is honoured rather than replayed
// past.
func TestOverrunDrainedEventIsRejudged(t *testing.T) {
	h := newHarness(t)
	src := overrunSrc(OverrunQueueSerial) + "limits:\n  max_per_hour: 1\n"
	h.write("tix", src)
	d := h.def()
	first, err := fire(t.Context(), h.st, h.api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != store.DeliveryFired {
		t.Fatalf("first = %s", first.Outcome)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	if _, err := fire(t.Context(), h.st, h.api, d, event("e2", "V-1"), ghT0); err != nil {
		t.Fatal(err)
	}
	setState(t, h.st, *first.TaskID, store.TaskDone)
	h.m.drain(t.Context())
	rows := h.ledger("tix")
	last := rows[len(rows)-1]
	if last.Outcome != store.DeliveryRateLimited {
		t.Errorf("drained under a spent cap = %s, want rate_limited", last.Outcome)
	}
	if got := len(h.api.requests()); got != 1 {
		t.Errorf("replays = %d, want 1", got)
	}
}

// TestOverrunTestWritesNothing is acceptance criterion 8: the dry run reports
// the overrun decision and leaves both the ledger and the backlog empty.
func TestOverrunTestWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.write("tix", overrunSrc(OverrunQueueSerial))
	d := h.def()
	first, err := fire(t.Context(), h.st, h.api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	before := len(h.ledger("tix"))

	j, err := h.m.Test(t.Context(), "tix", event("e2", "V-1"))
	if err != nil {
		t.Fatal(err)
	}
	if j.Outcome != store.DeliveryQueued || !j.WouldQueue || j.Overrun != OverrunQueueSerial ||
		j.ConcurrencyKey != "V-1" || len(j.InFlight) != 1 {
		t.Fatalf("dry run = %+v", j)
	}
	if got := len(h.ledger("tix")); got != before {
		t.Errorf("the dry run wrote %d ledger rows", got-before)
	}
	items, err := h.st.ListTriggerBacklogFor(t.Context(), "tix")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("the dry run wrote %d backlog rows", len(items))
	}
}

// TestOverrunReactionSkipsAgainstItsTarget is decision 2: a reaction's group
// is the resolved target's own state, so it skips against a running target it
// has never delivered to.
func TestOverrunReactionSkipsAgainstItsTarget(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	task := &store.Task{
		ProjectID: 1, Title: "live", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/9-live", State: store.TaskPaused,
	}
	if err := st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	src := `id: react
enabled: true
source:
  type: command
  project: 1
  poll_interval: 1s
  command: [poll]
action:
  type: follow_up
  target: branch
  branch: '{{ .Event.branch }}'
  prompt: again
on_fire: create
overrun: skip
`
	d, errs := Parse([]byte(src), "react")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	del, err := fire(t.Context(), st, api, d, Event{"id": "e1", "branch": "vincent/9-live"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliverySuperseded || !del.WouldSkip {
		t.Fatalf("reaction against an unsettled target = %+v, want skipped", del.Judgement)
	}
	// The default group is the resolved task, ledger or no ledger.
	if del.ConcurrencyKey != "task:"+strconv.FormatInt(task.ID, 10) {
		t.Errorf("group = %q, want the resolved task", del.ConcurrencyKey)
	}
	if got := len(api.requests()); got != 0 {
		t.Errorf("the reaction was replayed %d times", got)
	}
	// Settled, and it goes through.
	setState(t, st, task.ID, store.TaskDone)
	again, err := fire(t.Context(), st, api, d, Event{"id": "e2", "branch": "vincent/9-live"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != store.DeliveryFired {
		t.Errorf("against a settled target = %s, want fired", again.Outcome)
	}
}

// TestOverrunValidation: the enum is closed, the key must parse, and a
// concurrency_key with no overrun is an error rather than a silent no-op.
func TestOverrunValidation(t *testing.T) {
	cases := []struct {
		name, extra, wantPath string
	}{
		{"unknown mode", "overrun: sometimes\n", "overrun"},
		{"key does not parse", "overrun: skip\nconcurrency_key: '{{ .Event'\n", "concurrency_key"},
		{"key without a mode", "concurrency_key: '{{ .Event.key }}'\n", "concurrency_key"},
		{"key with parallel", "overrun: parallel\nconcurrency_key: 'k'\n", "concurrency_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := Parse([]byte(overrunSrcBase+tc.extra), "tix")
			if len(errs) == 0 {
				t.Fatalf("%s was accepted", tc.extra)
			}
			if errs[0].Path != tc.wantPath {
				t.Errorf("error at %q, want %q: %v", errs[0].Path, tc.wantPath, errs)
			}
		})
	}
	// The valid modes all parse.
	for _, mode := range Overruns() {
		if _, errs := Parse([]byte(overrunSrcBase+"overrun: "+mode+"\n"), "tix"); len(errs) > 0 {
			t.Errorf("overrun: %s refused: %v", mode, errs)
		}
	}
}

const overrunSrcBase = `id: tix
enabled: true
source:
  type: command
  project: 1
  poll_interval: 1s
  command: [poll]
action:
  type: create_task
  title: t
`

// TestOverrunQueueHoldsAnEventOnce: a source that re-shows its whole window
// every poll — one that keeps no cursor — does not fill the backlog with
// copies of one held event, and queue_serial does not fire it once per copy.
func TestOverrunQueueHoldsAnEventOnce(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, 0, "")
	d := overrunDef(t, OverrunQueueSerial)
	first, err := fire(t.Context(), st, api, d, event("e1", "V-1"), ghT0)
	if err != nil {
		t.Fatal(err)
	}
	setState(t, st, *first.TaskID, store.TaskRunning)
	for range 3 {
		if _, err := fire(t.Context(), st, api, d, event("e2", "V-1"), ghT0); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.ListTriggerBacklog(t.Context(), "tix", "V-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("backlog holds %d copies of one event, want 1", len(items))
	}
	rows, err := st.ListTriggerDeliveries(t.Context(), "tix", 100)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Outcome != store.DeliverySuperseded || !strings.Contains(rows[0].Detail, "already held") {
		t.Errorf("a re-shown held event recorded as %s %q", rows[0].Outcome, rows[0].Detail)
	}
}
