package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// The manager is the part of the package the daemon runs: it arms and
// disarms triggers as the registry and `triggers.enabled` change, polls the
// armed `type: command` ones on their own goroutines, judges the GitHub ones
// when the reconciler's tick hands it a listing, accepts pushed events for
// `type: http`, strikes the armed `type: schedule` ones on its own
// wall-clock tick, and answers the two dry runs.
//
// The rules it enforces are decision 16's:
//
//   - a trigger is *armed* when its file is present and valid, `enabled:
//     true`, and `triggers.enabled` is on; only an armed trigger polls or
//     accepts a push;
//   - the first poll after arming seeds and fires nothing (decision 6), and
//     for a command source records a `seeded` ledger row per event it was
//     shown (decision 31B), so a source that keeps no cursor does not flood on
//     its second poll;
//   - disarming — `enabled: false`, or `triggers.enabled` off — drops the
//     cursor, so re-arming seeds again and an off period never fires; a file
//     that leaves the registry drops it too, while an invalid file keeps it;
//   - a restart is neither: the cursor persists, and the next poll is a
//     capped catch-up (decision 13), or for a schedule one fire for however
//     many occurrences were missed (task 121).

// reconcileEvery is how long an arming change can wait when no Wake reaches
// the manager. Every change the daemon makes wakes it; this is the backstop.
const reconcileEvery = 5 * time.Second

// scheduleEvery is how often the schedule tick compares every armed
// schedule's anchor against the wall clock (task 121).
//
// A tick rather than a `time.NewTimer(untilNextOccurrence)` per trigger,
// because Go's timers run on the monotonic clock, which does not advance
// while a laptop is asleep — a timer would come due hours late on exactly
// the machine this source is for. Reading the wall clock makes suspend, a
// daemon restart and a config edit one code path: each is only "the anchor is
// older than the last due occurrence". One second, because that is what keeps
// the `every:` floor honest; the five-second reconcile tick would not.
const scheduleEvery = time.Second

// githubOverlap is how far before its watermark a GitHub listing starts. The
// gh leg answers through the search index, which lags writes by seconds to
// minutes; the diff is idempotent over an unchanged item, so an overlap costs
// nothing and a gap would lose a transition.
const githubOverlap = 2 * time.Minute

// Deps are the daemon facilities a Manager runs on.
type Deps struct {
	Store Store
	// Handler is the daemon's inner API mux, below authentication, which every
	// action replays against (decision 30).
	Handler  http.Handler
	Registry *Registry
	// Enabled reads `triggers.enabled` per use, so a hot reload reaches the
	// next poll.
	Enabled func() bool
	// Env is what a poll command inherits: the §12.3 environment policy every
	// child of the daemon gets. Nil means the daemon's own environment.
	Env func() []string
	// Getenv reads a `type: http` source's secret. Nil is os.Getenv.
	Getenv func(string) string
	// GitHub fetches a project's listing for a GitHub dry run. The reconciler
	// fetches for real polls; the manager never reaches the network itself.
	GitHub GitHubLister
	Logger *slog.Logger
	// Now is the clock, seamed for tests.
	Now func() time.Time
	// CommandTimeout bounds one poll command; zero is DefaultCommandTimeout.
	CommandTimeout time.Duration
}

// Manager arms, polls and fires triggers.
type Manager struct {
	deps Deps
	log  *slog.Logger
	wake chan struct{}

	mu      sync.Mutex
	pollers map[string]*poller

	warnMu sync.Mutex
	warned map[string]bool

	// schedMu serializes the schedule tick against itself: the tick is one
	// goroutine, but Stop and a test's manual tick must not overlap it. It is
	// not a firing lock — fireBy below is — and it is always taken first, so
	// the one ordering schedMu -> a trigger's lock is the only one there is.
	schedMu sync.Mutex

	// fireMu holds one mutex per trigger, covering every firing path: the
	// poller goroutine, GitHub judging, a pushed event, the schedule tick and
	// the backlog drain (task 121 decision 10). It replaces the coarse
	// ghMu/ingestMu pair, which did not cover the drain — and the drain can
	// run for a trigger whose poll is live. Under it, the overrun read, the
	// backlog write and the ledger write of one delivery never interleave with
	// another's for the same trigger.
	fireMu sync.Mutex
	fireBy map[string]*sync.Mutex

	cancel context.CancelFunc
	done   chan struct{}
}

type poller struct {
	version string
	cancel  context.CancelFunc
	done    chan struct{}
}

func (p *poller) stopWait() {
	p.cancel()
	<-p.done
}

// NewManager builds a manager and subscribes it to the registry. It starts
// nothing; Start does.
func NewManager(deps Deps) *Manager {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}
	if deps.Enabled == nil {
		deps.Enabled = func() bool { return false }
	}
	if deps.CommandTimeout <= 0 {
		deps.CommandTimeout = DefaultCommandTimeout
	}
	m := &Manager{
		deps: deps, log: deps.Logger, wake: make(chan struct{}, 1),
		pollers: map[string]*poller{}, warned: map[string]bool{}, fireBy: map[string]*sync.Mutex{},
	}
	deps.Registry.OnChange(func(removed []string) {
		m.dropRemoved(removed)
		m.Wake()
	})
	return m
}

// lockTrigger takes a trigger's firing lock and returns its release.
func (m *Manager) lockTrigger(id string) func() {
	m.fireMu.Lock()
	mu := m.fireBy[id]
	if mu == nil {
		mu = &sync.Mutex{}
		m.fireBy[id] = mu
	}
	m.fireMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// SetGitHubLister installs the fetch a GitHub dry run uses. It exists because
// the daemon builds its GitHub client after the manager; call it before Start.
func (m *Manager) SetGitHubLister(l GitHubLister) { m.deps.GitHub = l }

// Start runs the arming loop and the schedule tick until Stop or ctx is done.
func (m *Manager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.done = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.loop(ctx) }()
	go func() { defer wg.Done(); m.scheduleLoop(ctx) }()
	go func() { wg.Wait(); close(m.done) }()
}

// Stop ends the loop and every poller, and waits for them.
func (m *Manager) Stop() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
}

// Wake asks for a reconcile now: a config reload, a registry change.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) loop(ctx context.Context) {
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	m.reconcile(ctx)
	// The first drain of a daemon run empties groups whose tasks settled
	// while it was down (task 121 decision 9).
	m.drain(ctx)
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for id, p := range m.pollers {
				p.stopWait()
				delete(m.pollers, id)
			}
			m.mu.Unlock()
			return
		case <-m.wake:
		case <-ticker.C:
		}
		m.reconcile(ctx)
		m.drain(ctx)
	}
}

// OnEvent is the broker subscription the daemon wires beside notify's (task
// 121 decision 9). A task reaching a new state is what empties a group, so it
// is what asks for a drain; the reconcileEvery tick is the backstop. It runs
// on the publishing goroutine, so it does no work of its own.
func (m *Manager) OnEvent(e *store.Event) {
	if e == nil || e.Type != store.EventTaskStateChanged {
		return
	}
	m.Wake()
}

// drain fires what the backlog holds for every group whose work has finished,
// and discards what a disarmed trigger still holds.
func (m *Manager) drain(ctx context.Context) {
	groups, err := m.deps.Store.ListTriggerBacklogGroups(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("trigger backlog not listed", "error", err)
		}
		return
	}
	discarded := map[string]bool{}
	for _, g := range groups {
		if ctx.Err() != nil {
			return
		}
		e, err := m.entry(g.TriggerID)
		switch {
		case err != nil:
			// The file left the registry: the backlog goes with the cursor,
			// so an off period never fires (decision 5, decision 16's rule).
			if !discarded[g.TriggerID] {
				discarded[g.TriggerID] = true
				m.discardBacklog(ctx, g.TriggerID, "the trigger file is gone")
			}
		case !e.Valid() || !m.Armed(e):
			if !discarded[g.TriggerID] {
				discarded[g.TriggerID] = true
				m.discardBacklog(ctx, g.TriggerID, "the trigger was disarmed while the event was held")
			}
		case !Queues(e.Def.EffectiveOverrun()):
			if !discarded[g.TriggerID] {
				discarded[g.TriggerID] = true
				m.discardBacklog(ctx, g.TriggerID, "the trigger no longer queues events")
			}
		default:
			m.drainGroup(ctx, e.Def, g.ConcurrencyKey)
		}
	}
}

// discardBacklog drops every event a trigger holds and records each
// `superseded` with why. Re-arming seeds afresh and fires nothing it held.
func (m *Manager) discardBacklog(ctx context.Context, id, why string) {
	unlock := m.lockTrigger(id)
	defer unlock()
	items, err := m.deps.Store.ListTriggerBacklogFor(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("trigger backlog not listed", "trigger", id, "error", err)
		}
		return
	}
	now := m.deps.Now()
	for _, it := range items {
		if err := m.deps.Store.DeleteTriggerBacklog(ctx, it.ID); err != nil {
			m.log.Warn("held trigger event not dropped", "trigger", id, "error", err)
			return
		}
		if _, err := m.deps.Store.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
			TriggerID: id, EventID: it.EventID, ConcurrencyKey: it.ConcurrencyKey,
			Outcome: store.DeliverySuperseded, Detail: "dropped: " + why, CreatedAt: now,
		}); err != nil {
			m.log.Warn("discarded trigger event not recorded", "trigger", id, "error", err)
			return
		}
	}
}

// drainGroup fires one held event of a group whose tasks have all settled:
// the newest under queue_coalesce, discarding the rest, and the oldest under
// queue_serial, leaving the rest for the next drain — which is what keeps
// serial serial, since the fire it just made holds the group again.
func (m *Manager) drainGroup(ctx context.Context, d *Definition, key string) {
	unlock := m.lockTrigger(d.ID)
	defer unlock()
	inFlight, err := m.deps.Store.TriggerGroupInFlight(ctx, d.ID, key)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("trigger group not read", "trigger", d.ID, "error", err)
		}
		return
	}
	if len(inFlight) > 0 {
		return
	}
	items, err := m.deps.Store.ListTriggerBacklog(ctx, d.ID, key)
	if err != nil || len(items) == 0 {
		return
	}
	pick, drop := items[0], []store.TriggerBacklogItem(nil)
	if d.EffectiveOverrun() == OverrunQueueCoalesce {
		pick, drop = items[len(items)-1], items[:len(items)-1]
	}
	now := m.deps.Now()
	for _, it := range drop {
		if err := m.deps.Store.DeleteTriggerBacklog(ctx, it.ID); err != nil {
			m.log.Warn("held trigger event not dropped", "trigger", d.ID, "error", err)
			return
		}
		if _, err := m.deps.Store.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
			TriggerID: d.ID, EventID: it.EventID, ConcurrencyKey: it.ConcurrencyKey,
			Outcome: store.DeliverySuperseded, Detail: "coalesced into a newer held event",
			CreatedAt: now,
		}); err != nil {
			m.log.Warn("coalesced trigger event not recorded", "trigger", d.ID, "error", err)
			return
		}
	}
	// The row goes before the fire: a crash between the two loses the event,
	// where a crash after a fire whose row is still there would replay it.
	if err := m.deps.Store.DeleteTriggerBacklog(ctx, pick.ID); err != nil {
		m.log.Warn("held trigger event not dropped", "trigger", d.ID, "error", err)
		return
	}
	var ev Event
	if err := json.Unmarshal(pick.EventJSON, &ev); err != nil || ev == nil {
		m.log.Warn("held trigger event did not decode", "trigger", d.ID, "event", pick.EventID)
		return
	}
	if _, err := firePlan(ctx, m.deps.Store, m.deps.Handler, d, ev, now,
		plan{drained: true, group: key}); err != nil && ctx.Err() == nil {
		m.log.Error("held trigger event not delivered", "trigger", d.ID, "error", err)
	}
}

// Armed reports whether an entry is armed right now.
func (m *Manager) Armed(e *Entry) bool {
	return m.deps.Enabled() && e.Valid() && e.Def.Enabled
}

// DisarmedReason says why an entry is not armed, "" when it is.
func (m *Manager) DisarmedReason(e *Entry) string {
	switch {
	case !e.Valid():
		return "the trigger file does not validate"
	case !e.Def.Enabled:
		return "the trigger is disabled"
	case !m.deps.Enabled():
		return "triggers.enabled is off in config.yaml"
	}
	return ""
}

// reconcile brings the running pollers and the stored cursors in line with
// what is armed.
func (m *Manager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursors, err := m.deps.Store.ListTriggerCursors(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("trigger cursors not listed", "error", err)
		}
		return
	}
	seen := map[string]bool{}
	for _, e := range m.deps.Registry.List() {
		seen[e.ID] = true
		armed := m.Armed(&e)
		p := m.pollers[e.ID]
		if armed && e.Def.Polls() {
			if p != nil && p.version == e.Version {
				continue
			}
			if p != nil {
				p.stopWait()
			}
			m.pollers[e.ID] = m.startPoller(ctx, e)
			continue
		}
		if p != nil {
			p.stopWait()
			delete(m.pollers, e.ID)
		}
		// Disarmed, not invalid: the next arming must seed (decision 16). An
		// invalid file keeps its cursor — it is probably half-saved.
		if e.Valid() && !armed && cursors[e.ID] != nil {
			if err := m.deps.Store.DeleteTriggerCursor(ctx, e.ID); err != nil {
				m.log.Warn("trigger cursor not dropped on disarm", "trigger", e.ID, "error", err)
			}
		}
	}
	for id, p := range m.pollers {
		if !seen[id] {
			p.stopWait()
			delete(m.pollers, id)
		}
	}
}

// dropRemoved stops the pollers of files that left the registry and drops
// their cursors. The ledger stays (decision 21).
func (m *Manager) dropRemoved(ids []string) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if p := m.pollers[id]; p != nil {
			p.stopWait()
			delete(m.pollers, id)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := m.deps.Store.DeleteTriggerCursor(ctx, id); err != nil {
			m.log.Warn("trigger cursor not dropped on removal", "trigger", id, "error", err)
		}
		cancel()
	}
}

func (m *Manager) startPoller(ctx context.Context, e Entry) *poller {
	pctx, cancel := context.WithCancel(ctx)
	p := &poller{version: e.Version, cancel: cancel, done: make(chan struct{})}
	d := e.Def
	go func() {
		defer close(p.done)
		for {
			m.pollCommand(pctx, d)
			t := time.NewTimer(d.Interval())
			select {
			case <-pctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	return p
}

// scheduleLoop is the wall-clock tick task 121 runs on: one goroutine
// walking every armed schedule, rather than a timer per trigger.
func (m *Manager) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(scheduleEvery)
	defer ticker.Stop()
	for {
		m.tickSchedules(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tickSchedules is one pass over the armed schedules.
func (m *Manager) tickSchedules(ctx context.Context) {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	for _, e := range m.deps.Registry.List() {
		if ctx.Err() != nil {
			return
		}
		if !m.Armed(&e) || !e.Def.IsSchedule() {
			continue
		}
		m.tickSchedule(ctx, e.Def)
	}
}

// tickSchedule compares one armed schedule's stored anchor against the wall
// clock. It writes the cursor row on exactly two occasions — the seed and a
// fire — so an idle schedule costs one read a second and no write.
func (m *Manager) tickSchedule(ctx context.Context, d *Definition) {
	log := m.log.With("trigger", d.ID)
	sch, err := ParseSchedule(d.Source)
	if err != nil {
		// Unreachable: an armed entry validated, and validateSchedule refuses
		// everything ParseSchedule does. Recorded rather than ignored.
		log.Warn("trigger schedule not parsed", "error", err)
		return
	}
	prev, err := m.cursorOf(ctx, d.ID)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("trigger cursor not read", "error", err)
		}
		return
	}
	now := m.deps.Now()
	// Arming seeds the anchor at now and fires nothing (decision 2): the
	// first occurrence a schedule ever fires is one that falls after the
	// keypress that armed it. Disarming drops the cursor (decision 16), so a
	// disable/enable cycle re-seeds and an off period never fires — while a
	// daemon stop, a suspend or a reboot is none of those and keeps the
	// anchor, which is the case this source exists for.
	anchor, seeded := scheduleAnchor(prev)
	if !seeded {
		next := carry(d.ID, prev, now)
		stamp := now.UTC().Format(store.TimeFormat)
		next.Cursor, next.LastPollOK = &stamp, true
		log.Info("trigger schedule seeded", "anchor", stamp)
		m.putCursor(ctx, prev, next)
		return
	}
	due := sch.LastDue(anchor, now)
	if due.IsZero() {
		return
	}
	// One fire however many occurrences were missed: a weekend of downtime
	// produces one task, not forty. Decision 13's catch-up cap is not reused
	// here because fire-once is a strictly stronger bound.
	//
	// The tick is a firing path like any other, so it fires under the
	// trigger's lock (task 121 decision 10): a strike and a backlog drain for
	// the same schedule must not interleave their overrun read and ledger
	// write. schedMu is already held here, and nothing takes it while holding
	// a trigger's lock, so the ordering stays one-way.
	unlock := m.lockTrigger(d.ID)
	defer unlock()
	del, err := fire(ctx, m.deps.Store, m.deps.Handler, d, sch.Event(due), now)
	if err != nil {
		// The anchor stays put, so the occurrence is judged again next tick;
		// the ledger dedupes it if the row was in fact written.
		if ctx.Err() == nil {
			log.Error("trigger delivery not recorded", "error", err)
		}
		return
	}
	next := carry(d.ID, prev, now)
	stamp := due.UTC().Format(store.TimeFormat)
	next.Cursor, next.LastPollOK = &stamp, true
	if del.Outcome == store.DeliveryFired {
		next.LastFireAt = &now
	}
	m.putCursor(ctx, prev, next)
}

// scheduleAnchor reads the last handled occurrence out of the cursor column
// (decision 2). An unparseable value is treated as unseeded, which re-seeds
// at now and fires nothing — the safe direction.
func scheduleAnchor(prev *store.TriggerCursor) (time.Time, bool) {
	if prev == nil || prev.Cursor == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(store.TimeFormat, *prev.Cursor)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// cursorOf reads a trigger's cursor row, nil when it has none.
func (m *Manager) cursorOf(ctx context.Context, id string) (*store.TriggerCursor, error) {
	c, err := m.deps.Store.GetTriggerCursor(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return c, err
}

func (m *Manager) env() []string {
	if m.deps.Env == nil {
		return nil
	}
	return m.deps.Env()
}

// pollCommand is one real poll of a `type: command` trigger.
func (m *Manager) pollCommand(ctx context.Context, d *Definition) {
	log := m.log.With("trigger", d.ID)
	prev, err := m.cursorOf(ctx, d.ID)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("trigger cursor not read", "error", err)
		}
		return
	}
	seeding := prev == nil || prev.Cursor == nil
	cursor := ""
	if !seeding {
		cursor = *prev.Cursor
	}
	res, runErr := runCommand(ctx, d.Source.Command, cursor, m.env(), m.deps.CommandTimeout, log)
	if ctx.Err() != nil {
		// Disarmed or shutting down mid-poll: nothing it saw is recorded.
		return
	}
	now := m.deps.Now()
	next := carry(d.ID, prev, now)
	if runErr != nil {
		// A failing exit is never "no events", and the cursor stays put.
		next.LastPollOK, next.LastPollError = false, runErr.Error()
		log.Warn("trigger poll failed", "error", runErr)
		m.putCursor(ctx, prev, next)
		return
	}
	next.LastPollOK = true
	if seeding {
		m.seed(ctx, d, res.Events, now)
		c := ""
		if res.Cursor != nil {
			c = *res.Cursor
		}
		next.Cursor = &c
		log.Info("trigger seeded", "events", len(res.Events))
		m.putCursor(ctx, prev, next)
		return
	}
	events := m.capEvents(d.ID, res.Events)
	unlock := m.lockTrigger(d.ID)
	defer unlock()
	for _, ev := range events {
		del, err := fire(ctx, m.deps.Store, m.deps.Handler, d, ev, now)
		if err != nil {
			// A store failure mid-poll: keep the old cursor so a cursor-keeping
			// source re-sends what was not recorded; the ledger dedupes what
			// was.
			next.LastPollOK, next.LastPollError = false, "recording deliveries: "+err.Error()
			log.Error("trigger delivery not recorded", "error", err)
			m.putCursor(ctx, prev, next)
			return
		}
		if del.Outcome == store.DeliveryFired {
			next.LastFireAt = &now
		}
	}
	if res.Cursor != nil {
		next.Cursor = res.Cursor
	}
	m.putCursor(ctx, prev, next)
}

// carry starts the next cursor row from the previous one.
func carry(id string, prev *store.TriggerCursor, now time.Time) *store.TriggerCursor {
	next := &store.TriggerCursor{TriggerID: id, LastPollAt: &now}
	if prev != nil {
		next.Cursor, next.LastFireAt = prev.Cursor, prev.LastFireAt
	}
	return next
}

// seed records one `seeded` row per event a seed poll was shown (decision
// 31B). No match or `if:` — recording is not firing — and no cap: every event
// the source showed before arming must be covered.
func (m *Manager) seed(ctx context.Context, d *Definition, events []Event, now time.Time) {
	for _, ev := range events {
		key, err := dedupeKey(d, ev, renderData{Event: ev})
		if err != nil {
			m.log.Warn("trigger seed skipped an event whose dedupe key did not render",
				"trigger", d.ID, "event", ev.ID(), "error", err)
			continue
		}
		if done, err := m.deps.Store.TriggerKeyDelivered(ctx, d.ID, key); err != nil || done {
			continue
		}
		if _, err := m.deps.Store.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
			TriggerID: d.ID, EventID: ev.ID(), DedupeKey: key, Outcome: store.DeliverySeeded, CreatedAt: now,
		}); err != nil {
			m.log.Warn("trigger seed row not recorded", "trigger", d.ID, "error", err)
		}
	}
}

// capEvents applies decision 13's catch-up cap, warning once per trigger per
// daemon run (§17's warn-once lines).
func (m *Manager) capEvents(id string, events []Event) []Event {
	if len(events) <= MaxEventsPerPoll {
		return events
	}
	m.warnMu.Lock()
	first := !m.warned[id]
	m.warned[id] = true
	m.warnMu.Unlock()
	if first {
		m.log.Warn("trigger catch-up truncated: events past the cap were dropped",
			"trigger", id, "events", len(events), "judged", MaxEventsPerPoll)
	}
	return events[:MaxEventsPerPoll]
}

// putCursor writes the row and publishes trigger.poll_changed on the first
// poll and on a health transition (decision 24).
func (m *Manager) putCursor(ctx context.Context, prev, next *store.TriggerCursor) {
	if err := m.deps.Store.PutTriggerCursor(ctx, next); err != nil {
		if ctx.Err() == nil {
			m.log.Warn("trigger cursor not written", "trigger", next.TriggerID, "error", err)
		}
		return
	}
	first := prev == nil || prev.LastPollAt == nil
	if !first && prev.LastPollOK == next.LastPollOK {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"trigger_id": next.TriggerID, "ok": next.LastPollOK, "error": next.LastPollError,
	})
	if err != nil {
		return
	}
	// The row is committed, so its event must follow it even when a disarm or
	// shutdown cancels ctx between the two writes: the next poll reads this
	// row as not-first with unchanged health, and would never publish it.
	if err := m.deps.Store.AppendEvent(context.WithoutCancel(ctx), &store.Event{Type: store.EventTriggerPollChanged, Payload: payload}); err != nil {
		m.log.Warn("trigger.poll_changed not recorded", "trigger", next.TriggerID, "error", err)
	}
}

// entry returns a registry entry or ErrNotFound.
func (m *Manager) entry(id string) (*Entry, error) {
	e, ok := m.deps.Registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return &e, nil
}

// validEntry returns an entry that parsed, or *InvalidError.
func (m *Manager) validEntry(id string) (*Entry, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.Valid() {
		return nil, &InvalidError{Errors: e.Errors}
	}
	return e, nil
}

// Test is the first dry run (#362): it judges a supplied event through the
// real pipeline and writes nothing. It works while the trigger or
// `triggers.enabled` is off, since it fires nothing.
func (m *Manager) Test(ctx context.Context, id string, ev Event) (*Judgement, error) {
	e, err := m.validEntry(id)
	if err != nil {
		return nil, err
	}
	return judge(ctx, m.deps.Store, e.Def, ev, m.deps.Now())
}

// ErrNoPoll is a live-poll dry run against a source that has no poll: `http`
// is pushed and `schedule` is the clock, so neither has a source to run once.
var ErrNoPoll = errors.New("this source has no poll; send a sample event to POST /v1/triggers/{id}/test instead")

// DryPoll is what the second dry run found.
type DryPoll struct {
	// Seed says a real poll now would seed and fire nothing; the events are
	// still judged, to show what the filter would do once armed.
	Seed   bool         `json:"seed"`
	Events []*Judgement `json:"events"`
	// Truncated counts events past the catch-up cap, which were not judged.
	Truncated int `json:"truncated"`
	// Refused counts command output lines that were not events.
	Refused int `json:"refused"`
	// Cursor is the watermark a command printed, which a real poll would
	// store.
	Cursor *string `json:"cursor,omitempty"`
	// Error is a poll that failed: the command's exit status and stderr, or
	// why GitHub could not be listed. A failed dry run is still an answer.
	Error string `json:"error,omitempty"`
}

// PollDry is the second dry run (#362): it runs the source once for real and
// judges what it returns — no fire, no cursor advance, no ledger row, no poll
// health change.
func (m *Manager) PollDry(ctx context.Context, id string) (*DryPoll, error) {
	e, err := m.validEntry(id)
	if err != nil {
		return nil, err
	}
	d := e.Def
	prev, err := m.cursorOf(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	var events []Event
	out := &DryPoll{Events: []*Judgement{}}
	switch {
	case d.Source.Type == SourceHTTP, d.IsSchedule():
		return nil, ErrNoPoll
	case d.IsGitHub():
		snap, seeded := decodeSnapshot(prev)
		out.Seed = !seeded
		if m.deps.GitHub == nil {
			return nil, &PollError{Reason: "the GitHub integration is not wired in this daemon"}
		}
		listing := m.deps.GitHub(ctx, d.Source.Project, wantFor(d, snap, seeded))
		if listing.Err != nil {
			return nil, &PollError{Reason: listing.Err.Error()}
		}
		if seeded {
			events = snap.diff(d.Source.Type, listing)
		}
	default:
		cursor := ""
		out.Seed = prev == nil || prev.Cursor == nil
		if !out.Seed {
			cursor = *prev.Cursor
		}
		res, err := runCommand(ctx, d.Source.Command, cursor, m.env(), m.deps.CommandTimeout, m.log.With("trigger", d.ID))
		if err != nil {
			return nil, err
		}
		events, out.Refused, out.Cursor = res.Events, res.Refused, res.Cursor
	}
	if len(events) > MaxEventsPerPoll {
		out.Truncated = len(events) - MaxEventsPerPoll
		events = events[:MaxEventsPerPoll]
	}
	now := m.deps.Now()
	for _, ev := range events {
		j, err := judge(ctx, m.deps.Store, d, ev, now)
		if err != nil {
			return nil, err
		}
		out.Events = append(out.Events, j)
	}
	return out, nil
}
