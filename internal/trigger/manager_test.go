package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// waitTimeout bounds every wait-for-condition here. It is generous because a
// poll spawns the test binary, which a loaded Windows runner starts slowly; a
// passing test waits only as long as its condition takes.
const waitTimeout = 20 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncBuffer is a log sink a poller goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never errors
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testClock is the manager's Now seam, set from the test while pollers read it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// harness is a manager over a real store, a registry on a temp directory, the
// fake inner mux, `triggers.enabled` behind an atomic and the secret variables
// behind a map.
type harness struct {
	t       *testing.T
	st      *store.Store
	api     *fakeAPI
	dir     string
	reg     *Registry
	enabled atomic.Bool
	env     sync.Map
	clock   *testClock
	logs    *syncBuffer
	m       *Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := openStore(t)
	h := &harness{
		t: t, st: st, api: newFakeAPI(t, st, 0, ""),
		dir:   filepath.Join(t.TempDir(), "triggers"),
		clock: &testClock{t: ghT0},
	}
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.enabled.Store(true)
	h.restart()
	return h
}

// restart builds a new registry and manager over the same database and
// directory — exactly what survives a daemon restart.
func (h *harness) restart() *Manager {
	h.t.Helper()
	if h.m != nil {
		h.m.Stop()
	}
	h.logs = &syncBuffer{}
	log := slog.New(slog.NewTextHandler(h.logs, nil))
	h.reg = NewRegistry(h.dir, log)
	h.reg.Reload()
	m := NewManager(Deps{
		Store: h.st, Handler: h.api, Registry: h.reg, Enabled: h.enabled.Load,
		Getenv: func(k string) string {
			v, _ := h.env.Load(k)
			s, _ := v.(string)
			return s
		},
		Logger: log, Now: h.clock.Now, CommandTimeout: 30 * time.Second,
	})
	h.t.Cleanup(m.Stop)
	h.m = m
	return m
}

// write puts a trigger file in place and reloads the registry, as the API
// does after a write.
func (h *harness) write(id, src string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, id+".yaml"), []byte(src), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.reg.Reload()
}

// def is the loaded definition of the `tix` trigger the overrun tests write.
func (h *harness) def() *Definition {
	h.t.Helper()
	e, ok := h.reg.Get("tix")
	if !ok || !e.Valid() {
		h.t.Fatalf("trigger tix not loaded: %v", e.Errors)
	}
	return e.Def
}

// jira is the loaded definition of the `jira` command trigger every poller
// test writes.
func (h *harness) jira() *Definition {
	h.t.Helper()
	e, ok := h.reg.Get("jira")
	if !ok || !e.Valid() {
		h.t.Fatalf("trigger jira not loaded: %v", e.Errors)
	}
	return e.Def
}

func (h *harness) cursor(id string) *store.TriggerCursor {
	h.t.Helper()
	c, err := h.st.GetTriggerCursor(context.Background(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

// ledger is a trigger's rows, oldest first.
func (h *harness) ledger(id string) []store.TriggerDelivery {
	h.t.Helper()
	rows, err := h.st.ListTriggerDeliveries(context.Background(), id, 10000)
	if err != nil {
		h.t.Fatal(err)
	}
	slices.Reverse(rows)
	return rows
}

func (h *harness) events(types ...string) []store.Event {
	h.t.Helper()
	evs, err := h.st.ListEvents(context.Background(), store.EventFilter{Types: types})
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

type pollChanged struct {
	TriggerID string `json:"trigger_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
}

// pollChanges is every trigger.poll_changed a trigger published, in order.
func (h *harness) pollChanges(id string) []pollChanged {
	h.t.Helper()
	var out []pollChanged
	for _, e := range h.events(store.EventTriggerPollChanged) {
		var p pollChanged
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			h.t.Fatal(err)
		}
		if p.TriggerID == id {
			out = append(out, p)
		}
	}
	return out
}

func (h *harness) pollerCount() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return len(h.m.pollers)
}

func count(rows []store.TriggerDelivery, outcome string) int {
	n := 0
	for _, r := range rows {
		if r.Outcome == outcome {
			n++
		}
	}
	return n
}

func hasRow(rows []store.TriggerDelivery, eventID, outcome string) bool {
	for _, r := range rows {
		if r.EventID == eventID && r.Outcome == outcome {
			return true
		}
	}
	return false
}

// commandDoc is a command trigger polling every second through the helper
// child, which prints the file at out. The argv is written as JSON, which is
// YAML, so a Windows path with backslashes and spaces survives.
func commandDoc(t *testing.T, id string, enabled bool, out string) string {
	t.Helper()
	argv, err := json.Marshal(helperArgv(t, "file", out))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`id: %s
enabled: %t
source:
  type: command
  project: 1
  poll_interval: 1s
  command: %s
action:
  type: create_task
  title: 'task {{ .Event.id }}'
`, id, enabled, argv)
}

// setOutput is what the helper child prints on its next run.
func setOutput(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func evLine(id string) string { return fmt.Sprintf(`{"id":%q}`, id) }

func cursorLine(c string) string { return fmt.Sprintf(`{"cursor":%q}`, c) }

// polls is the cursor the helper child was handed on each of its runs.
func polls(t *testing.T, out string) []string {
	t.Helper()
	b, err := os.ReadFile(out + ".polls")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, strings.TrimPrefix(l, "cursor="))
		}
	}
	return got
}

// TestManagerPollsOnlyArmedTriggers: a disabled file and an invalid one get
// no poller, and the enabled one seeds on its own.
func TestManagerPollsOnlyArmedTriggers(t *testing.T) {
	h := newHarness(t)
	outs := t.TempDir()
	on, off := filepath.Join(outs, "on.out"), filepath.Join(outs, "off.out")
	setOutput(t, on, evLine("e1"))
	setOutput(t, off, evLine("e1"))
	h.write("on", commandDoc(t, "on", true, on))
	h.write("off", commandDoc(t, "off", false, off))
	h.write("bad", "id: bad\nenabled: true\nsource:\n  type: nope\n")

	h.m.Start(context.Background())
	waitFor(t, "the armed trigger's seed", func() bool { return h.cursor("on") != nil })

	h.m.mu.Lock()
	_, onPolls := h.m.pollers["on"]
	n := len(h.m.pollers)
	h.m.mu.Unlock()
	if !onPolls || n != 1 {
		t.Errorf("pollers: on=%v, %d running; want only on", onPolls, n)
	}
	if c := h.cursor("off"); c != nil {
		t.Errorf("disabled trigger has a cursor: %+v", c)
	}
	if p := polls(t, off); len(p) != 0 {
		t.Errorf("disabled trigger ran its command %d times", len(p))
	}
	if c := h.cursor("bad"); c != nil {
		t.Errorf("invalid trigger has a cursor: %+v", c)
	}
}

// TestManagerSeedThenNoFlood: the first poll records `seeded` rows and fires
// nothing, so a source that keeps no cursor sees its backlog deduped on the
// second poll, and only a genuinely new event fires (decision 31B).
func TestManagerSeedThenNoFlood(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"), evLine("e2"))
	h.write("jira", commandDoc(t, "jira", true, out))
	d := h.jira()

	h.m.pollCommand(ctx, d)
	c := h.cursor("jira")
	if c == nil || c.Cursor == nil || *c.Cursor != "" || !c.LastPollOK || c.LastPollAt == nil {
		t.Fatalf("cursor after the seed = %+v", c)
	}
	rows := h.ledger("jira")
	if len(rows) != 2 || !hasRow(rows, "e1", store.DeliverySeeded) || !hasRow(rows, "e2", store.DeliverySeeded) ||
		rows[0].DedupeKey != "e1" || rows[1].DedupeKey != "e2" {
		t.Fatalf("ledger after the seed = %+v", rows)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("the seed replayed %d requests", n)
	}
	if pc := h.pollChanges("jira"); len(pc) != 1 || !pc[0].OK {
		t.Fatalf("poll_changed after the seed = %+v, want one ok", pc)
	}

	// The same backlog again: deduped against the seed rows, nothing replayed,
	// and no health event because health did not change.
	h.m.pollCommand(ctx, d)
	rows = h.ledger("jira")
	if len(rows) != 4 || count(rows, store.DeliveryDeduped) != 2 {
		t.Fatalf("ledger after poll 2 = %+v", rows)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("poll 2 flooded: %d replays", n)
	}
	if pc := h.pollChanges("jira"); len(pc) != 1 {
		t.Errorf("poll 2 published poll_changed: %+v", pc)
	}

	setOutput(t, out, evLine("e1"), evLine("e2"), evLine("e3"))
	h.m.pollCommand(ctx, d)
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].body.Title != "task e3" {
		t.Fatalf("poll 3 replays = %+v, want only e3", reqs)
	}
	rows = h.ledger("jira")
	last := rows[len(rows)-1]
	if last.EventID != "e3" || last.Outcome != store.DeliveryFired || last.TaskID == nil {
		t.Fatalf("e3's row = %+v", last)
	}
	fired := h.events(store.EventTriggerFired)
	if len(fired) != 1 || fired[0].TaskID == nil || *fired[0].TaskID != *last.TaskID {
		t.Errorf("trigger.fired = %+v, want one naming task %d", fired, *last.TaskID)
	}
	if c := h.cursor("jira"); c.LastFireAt == nil {
		t.Error("last_fire_at not recorded")
	}
	if got := polls(t, out); !slices.Equal(got, []string{"", "", ""}) {
		t.Errorf("cursors handed to the command = %q, want three empty ones", got)
	}
}

// TestManagerCursorAdvances: a watermark line is stored and handed back, and
// a poll that prints none leaves it where it was.
func TestManagerCursorAdvances(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"), cursorLine("c1"))
	h.write("jira", commandDoc(t, "jira", true, out))
	d := h.jira()

	h.m.pollCommand(ctx, d)
	if c := h.cursor("jira"); c == nil || c.Cursor == nil || *c.Cursor != "c1" {
		t.Fatalf("seed stored cursor %+v, want c1", c)
	}
	setOutput(t, out, evLine("e2"), cursorLine("c2"))
	h.m.pollCommand(ctx, d)
	if c := h.cursor("jira"); *c.Cursor != "c2" {
		t.Fatalf("cursor = %q, want c2", *c.Cursor)
	}
	setOutput(t, out, evLine("e3"))
	h.m.pollCommand(ctx, d)
	if c := h.cursor("jira"); *c.Cursor != "c2" {
		t.Errorf("a poll without a cursor line moved the cursor to %q", *c.Cursor)
	}
	rows := h.ledger("jira")
	if !hasRow(rows, "e1", store.DeliverySeeded) || !hasRow(rows, "e2", store.DeliveryFired) ||
		!hasRow(rows, "e3", store.DeliveryFired) || len(rows) != 3 {
		t.Errorf("ledger = %+v", rows)
	}
	if got := polls(t, out); !slices.Equal(got, []string{"", "c1", "c2"}) {
		t.Errorf("cursors handed to the command = %q", got)
	}
}

// TestManagerRestartIsCappedCatchUp: a new manager over a database that holds
// the cursor does not re-seed; it judges at most 20 events a poll and warns
// about the truncation once, however many polls truncate (decision 13).
func TestManagerRestartIsCappedCatchUp(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("old"), cursorLine("c0"))
	h.write("jira", commandDoc(t, "jira", true, out))
	h.m.pollCommand(t.Context(), h.jira())

	lines := make([]string, 0, 26)
	for i := range 25 {
		lines = append(lines, evLine(fmt.Sprintf("n%02d", i+1)))
	}
	setOutput(t, out, append(lines, cursorLine("c1"))...)

	m := h.restart()
	m.Start(context.Background())
	waitFor(t, "the catch-up poll", func() bool {
		c := h.cursor("jira")
		return c != nil && c.Cursor != nil && *c.Cursor == "c1"
	})
	m.Stop()
	// A second truncating poll on the same manager, driven directly.
	m.pollCommand(t.Context(), h.jira())

	rows := h.ledger("jira")
	if n := count(rows, store.DeliverySeeded); n != 1 {
		t.Errorf("%d seeded rows: the restart re-seeded", n)
	}
	var fired []string
	for _, r := range rows {
		if r.Outcome == store.DeliveryFired {
			fired = append(fired, r.EventID)
		}
		// "old" is the seed's row; only the catch-up's events are capped.
		if strings.HasPrefix(r.EventID, "n") && r.EventID > "n20" {
			t.Errorf("an event past the cap reached the ledger: %+v", r)
		}
	}
	want := make([]string, 0, MaxEventsPerPoll)
	for i := range MaxEventsPerPoll {
		want = append(want, fmt.Sprintf("n%02d", i+1))
	}
	if !slices.Equal(fired, want) {
		t.Errorf("fired %v, want the first %d", fired, MaxEventsPerPoll)
	}
	if n := count(rows, store.DeliveryDeduped); n < MaxEventsPerPoll || n%MaxEventsPerPoll != 0 {
		t.Errorf("%d deduped rows, want whole polls of %d", n, MaxEventsPerPoll)
	}
	if n := len(h.api.requests()); n != MaxEventsPerPoll {
		t.Errorf("%d replays, want %d", n, MaxEventsPerPoll)
	}
	if n := strings.Count(h.logs.String(), "catch-up truncated"); n != 1 {
		t.Errorf("truncation logged %d times, want once:\n%s", n, h.logs.String())
	}
	if !strings.Contains(h.logs.String(), "level=WARN msg=\"trigger catch-up truncated") {
		t.Errorf("truncation not logged at warn:\n%s", h.logs.String())
	}
}

// TestManagerFailingPollKeepsCursor: a failing exit is never "no events" —
// the cursor stays, health turns failing with one poll_changed however many
// polls fail, and recovery publishes one more.
func TestManagerFailingPollKeepsCursor(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"), cursorLine("c1"))
	h.write("jira", commandDoc(t, "jira", true, out))
	d := h.jira()
	h.m.pollCommand(ctx, d)

	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	h.m.pollCommand(ctx, d)
	h.m.pollCommand(ctx, d)
	c := h.cursor("jira")
	if c.Cursor == nil || *c.Cursor != "c1" || c.LastPollOK || !strings.Contains(c.LastPollError, "exit status 3") {
		t.Fatalf("cursor after failing polls = %+v", c)
	}
	pc := h.pollChanges("jira")
	if len(pc) != 2 || pc[1].OK || !strings.Contains(pc[1].Error, "exit status 3") {
		t.Fatalf("poll_changed = %+v, want the seed's and exactly one failing", pc)
	}
	if n := len(h.ledger("jira")); n != 1 {
		t.Errorf("failing polls wrote %d ledger rows", n-1)
	}

	setOutput(t, out, evLine("e2"), cursorLine("c2"))
	h.m.pollCommand(ctx, d)
	if c := h.cursor("jira"); *c.Cursor != "c2" || !c.LastPollOK || c.LastPollError != "" {
		t.Errorf("cursor after recovery = %+v", c)
	}
	if pc := h.pollChanges("jira"); len(pc) != 3 || !pc[2].OK {
		t.Errorf("poll_changed after recovery = %+v", pc)
	}
	if got := polls(t, out); !slices.Equal(got, []string{"", "c1", "c1", "c1"}) {
		t.Errorf("cursors handed to the command = %q, want c1 on every failing retry", got)
	}
}

// TestManagerDisarmDropsCursorAndRearmSeeds: disarming by either switch drops
// the cursor, and re-arming seeds again, so nothing from the off period fires
// (decision 16).
func TestManagerDisarmDropsCursorAndRearmSeeds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		disarm, rearm func(t *testing.T, h *harness, out string)
	}{
		{
			"enabled: false",
			func(t *testing.T, h *harness, out string) { h.write("jira", commandDoc(t, "jira", false, out)) },
			func(t *testing.T, h *harness, out string) { h.write("jira", commandDoc(t, "jira", true, out)) },
		},
		{
			"triggers.enabled off",
			func(_ *testing.T, h *harness, _ string) { h.enabled.Store(false); h.m.Wake() },
			func(_ *testing.T, h *harness, _ string) { h.enabled.Store(true); h.m.Wake() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			out := filepath.Join(t.TempDir(), "poll.out")
			setOutput(t, out, evLine("e1"))
			h.write("jira", commandDoc(t, "jira", true, out))
			h.m.Start(context.Background())
			waitFor(t, "the seed", func() bool { return h.cursor("jira") != nil })

			tc.disarm(t, h, out)
			waitFor(t, "the cursor to drop", func() bool { return h.cursor("jira") == nil })
			if n := h.pollerCount(); n != 0 {
				t.Fatalf("%d pollers still running after disarming", n)
			}
			if n := len(h.ledger("jira")); n != 1 {
				t.Errorf("disarming touched the ledger: %d rows", n)
			}

			// The source moves on while the trigger is off. Written only now
			// that no poller runs, so no child holds the file.
			setOutput(t, out, evLine("e1"), evLine("e2"))
			tc.rearm(t, h, out)
			// The cursor is the seed poll's last write; the ledger rows come
			// before it, so stopping on them alone can cancel the poll short
			// of its cursor and its poll_changed.
			waitFor(t, "the re-arm seed", func() bool {
				return hasRow(h.ledger("jira"), "e2", store.DeliverySeeded) && h.cursor("jira") != nil
			})
			h.m.Stop()

			if n := count(h.ledger("jira"), store.DeliveryFired); n != 0 {
				t.Errorf("%d events from the off period fired", n)
			}
			if n := len(h.api.requests()); n != 0 {
				t.Errorf("%d replays", n)
			}
			if pc := h.pollChanges("jira"); len(pc) != 2 {
				t.Errorf("poll_changed = %+v, want one first poll per arming", pc)
			}
		})
	}
}

// TestManagerRemovedFileDropsCursorKeepsLedger: a file that leaves the
// registry stops its poller and drops its cursor at once; its ledger stays
// (decision 21).
func TestManagerRemovedFileDropsCursorKeepsLedger(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"))
	h.write("jira", commandDoc(t, "jira", true, out))
	h.m.Start(context.Background())
	waitFor(t, "the seed", func() bool { return h.cursor("jira") != nil })

	if err := os.Remove(filepath.Join(h.dir, "jira.yaml")); err != nil {
		t.Fatal(err)
	}
	h.reg.Reload() // OnChange drops the cursor before Reload returns.
	if c := h.cursor("jira"); c != nil {
		t.Errorf("cursor survived the file: %+v", c)
	}
	if n := h.pollerCount(); n != 0 {
		t.Errorf("%d pollers after removal", n)
	}
	if rows := h.ledger("jira"); !hasRow(rows, "e1", store.DeliverySeeded) {
		t.Errorf("ledger after removal = %+v, want the seed row kept", rows)
	}
}

// TestManagerInvalidFileKeepsCursor: a half-saved file stops the poller but
// keeps the cursor, so restoring it resumes without a re-seed.
func TestManagerInvalidFileKeepsCursor(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"))
	valid := commandDoc(t, "jira", true, out)
	h.write("jira", valid)
	h.m.Start(context.Background())
	waitFor(t, "the seed", func() bool { return h.cursor("jira") != nil })

	h.write("jira", "id: jira\nenabled: true\nsource: [half-saved\n")
	waitFor(t, "the poller to stop", func() bool { return h.pollerCount() == 0 })
	if c := h.cursor("jira"); c == nil {
		t.Fatal("an invalid file dropped the cursor")
	}

	setOutput(t, out, evLine("e1"), evLine("e2"))
	h.write("jira", valid)
	waitFor(t, "e2 to fire", func() bool { return hasRow(h.ledger("jira"), "e2", store.DeliveryFired) })
	h.m.Stop()
	if hasRow(h.ledger("jira"), "e2", store.DeliverySeeded) {
		t.Error("restoring the file re-seeded")
	}
}

// TestManagerDryRunsWriteNothing: Test and PollDry judge through the real
// pipeline, work while the trigger is off, and leave the cursor, its health,
// the ledger and the events table exactly as they were.
func TestManagerDryRunsWriteNothing(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	out := filepath.Join(t.TempDir(), "poll.out")
	setOutput(t, out, evLine("e1"), cursorLine("c1"))
	h.write("jira", commandDoc(t, "jira", true, out))
	h.m.pollCommand(ctx, h.jira())

	setOutput(t, out, evLine("e1"), evLine("e2"), cursorLine("c2"))
	h.write("jira", commandDoc(t, "jira", false, out))
	h.write("hook", httpDoc(true))
	h.write("fresh", commandDoc(t, "fresh", true, out))
	h.enabled.Store(false)

	type state struct {
		cursor *store.TriggerCursor
		ledger []store.TriggerDelivery
		events []store.Event
		reqs   int
	}
	snap := func() state {
		return state{h.cursor("jira"), h.ledger("jira"), h.events(), len(h.api.requests())}
	}
	before := snap()

	j, err := h.m.Test(ctx, "jira", Event{"id": "e9"})
	if err != nil || j.Outcome != store.DeliveryFired || j.Action == nil || j.Action.Path != createPath {
		t.Errorf("Test(new event) = %+v, %v", j, err)
	}
	j, err = h.m.Test(ctx, "jira", Event{"id": "e1"})
	if err != nil || j.Outcome != store.DeliveryDeduped || !j.WouldDedupe {
		t.Errorf("Test(seeded event) = %+v, %v", j, err)
	}

	dry, err := h.m.PollDry(ctx, "jira")
	if err != nil {
		t.Fatalf("PollDry: %v", err)
	}
	if dry.Seed || len(dry.Events) != 2 || dry.Events[0].Outcome != store.DeliveryDeduped ||
		dry.Events[1].Outcome != store.DeliveryFired || dry.Cursor == nil || *dry.Cursor != "c2" {
		t.Errorf("PollDry = %+v", dry)
	}
	if p := polls(t, out); len(p) != 2 || p[1] != "c1" {
		t.Errorf("the dry poll was handed %q, want the stored cursor", p)
	}

	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	var pe *PollError
	if _, err := h.m.PollDry(ctx, "jira"); !errors.As(err, &pe) {
		t.Errorf("PollDry on a failing command = %v, want *PollError", err)
	}

	if after := snap(); !reflect.DeepEqual(before, after) {
		t.Errorf("a dry run wrote:\nbefore %+v\nafter  %+v", before, after)
	}

	setOutput(t, out, evLine("e1"))
	if dry, err := h.m.PollDry(ctx, "fresh"); err != nil || !dry.Seed {
		t.Errorf("PollDry(unseeded) = %+v, %v; want seed", dry, err)
	}
	if c := h.cursor("fresh"); c != nil || len(h.ledger("fresh")) != 0 {
		t.Errorf("an unseeded dry poll wrote cursor %+v or ledger rows", c)
	}
	if _, err := h.m.PollDry(ctx, "hook"); !errors.Is(err, ErrNoPoll) {
		t.Errorf("PollDry(http) = %v, want ErrNoPoll", err)
	}
	if _, err := h.m.Test(ctx, "nope", Event{"id": "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Test(unknown) = %v, want ErrNotFound", err)
	}
	h.write("bad", "id: bad\n")
	var ie *InvalidError
	if _, err := h.m.PollDry(ctx, "bad"); !errors.As(err, &ie) {
		t.Errorf("PollDry(invalid) = %v, want *InvalidError", err)
	}
}

// scheduleDoc is a schedule trigger on project 1 whose clock is the given
// `cron:` or `every:` line.
func scheduleDoc(id string, enabled bool, clock string) string {
	return fmt.Sprintf(`id: %s
enabled: %t
source:
  type: schedule
  project: 1
  %s
action:
  type: create_task
  title: 'sweep {{ .Event.date }} {{ .Event.hour }}'
`, id, enabled, clock)
}

// TestScheduleSeedsOnArmAndFiresOnce is the whole life of a scheduled
// trigger (task 121): arming anchors the clock and fires nothing, one tick
// past an occurrence fires once, a stretch of downtime that missed many
// occurrences still fires once, and a disable/enable cycle re-anchors.
func TestScheduleSeedsOnArmAndFiresOnce(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("sweep", scheduleDoc("sweep", true, `every: 2h`))

	// Arming seeds the anchor at now and fires nothing.
	h.m.tickSchedules(ctx)
	c := h.cursor("sweep")
	if c == nil || c.Cursor == nil || *c.Cursor != ghT0.Format(store.TimeFormat) {
		t.Fatalf("cursor after arming = %+v, want the anchor %s", c, ghT0.Format(store.TimeFormat))
	}
	if rows := h.ledger("sweep"); len(rows) != 0 {
		t.Fatalf("arming wrote %d ledger rows, want none", len(rows))
	}
	// A tick before the first occurrence is not due.
	h.clock.Set(ghT0.Add(time.Hour))
	h.m.tickSchedules(ctx)
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("%d replays before the first occurrence", n)
	}

	// Five hours later two occurrences have passed: one fire, at the last.
	h.clock.Set(ghT0.Add(5 * time.Hour))
	h.m.tickSchedules(ctx)
	rows := h.ledger("sweep")
	if len(rows) != 1 || rows[0].Outcome != store.DeliveryFired {
		t.Fatalf("ledger = %+v, want one fired row", rows)
	}
	want := ghT0.Add(4 * time.Hour).Format(store.TimeFormat)
	if rows[0].EventID != want {
		t.Errorf("fired occurrence %q, want the last one that passed %q", rows[0].EventID, want)
	}
	if c := h.cursor("sweep"); c.Cursor == nil || *c.Cursor != want {
		t.Errorf("anchor = %v, want the occurrence it fired %q", c.Cursor, want)
	}
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].path != createPath || !reqs[0].body.Paused {
		t.Fatalf("replays = %+v, want one paused create (the propose default)", reqs)
	}
	if reqs[0].body.Title == "" || strings.Contains(reqs[0].body.Title, "{{") {
		t.Errorf("title %q did not render over the schedule's event", reqs[0].body.Title)
	}

	// The same tick again: the occurrence is behind the anchor, so nothing.
	h.m.tickSchedules(ctx)
	if rows := h.ledger("sweep"); len(rows) != 1 {
		t.Fatalf("a second tick at the same instant produced %d rows", len(rows))
	}

	// An evaluation of an occurrence the ledger already holds is deduped
	// rather than fired again, which is what makes `id` the occurrence worth
	// it: rewind the anchor and tick.
	rewound := ghT0.Add(2 * time.Hour).Format(store.TimeFormat)
	if err := h.st.PutTriggerCursor(ctx, &store.TriggerCursor{TriggerID: "sweep", Cursor: &rewound}); err != nil {
		t.Fatal(err)
	}
	h.m.tickSchedules(ctx)
	rows = h.ledger("sweep")
	if len(rows) != 2 || rows[1].Outcome != store.DeliveryDeduped || rows[1].EventID != want {
		t.Fatalf("ledger = %+v, want the re-evaluated occurrence deduped", rows)
	}
	if n := len(h.api.requests()); n != 1 {
		t.Errorf("%d replays, want the deduped occurrence to have replayed nothing", n)
	}

	// Disabling drops the anchor (decision 16); enabling anchors at the new
	// now and fires nothing, even though hours of occurrences went by while
	// it was off.
	h.write("sweep", scheduleDoc("sweep", false, `every: 2h`))
	h.m.reconcile(ctx)
	if c := h.cursor("sweep"); c != nil {
		t.Fatalf("cursor survived a disarm: %+v", c)
	}
	h.clock.Set(ghT0.Add(30 * time.Hour))
	h.write("sweep", scheduleDoc("sweep", true, `every: 2h`))
	h.m.tickSchedules(ctx)
	if c := h.cursor("sweep"); c == nil || c.Cursor == nil || *c.Cursor != h.clock.Now().Format(store.TimeFormat) {
		t.Errorf("cursor after re-arming = %+v, want a fresh anchor", c)
	}
	if rows := h.ledger("sweep"); count(rows, store.DeliveryFired) != 1 {
		t.Errorf("re-arming fired: %+v", rows)
	}
}

// TestScheduleNotArmedDoesNotTick: a disabled schedule, an invalid one and a
// global switch that is off all leave the clock alone.
func TestScheduleNotArmedDoesNotTick(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("off", scheduleDoc("off", false, `every: 1s`))
	h.write("bad", "id: bad\nenabled: true\nsource:\n  type: schedule\n  project: 1\n")
	h.write("on", scheduleDoc("on", true, `every: 1s`))
	h.enabled.Store(false)
	h.m.tickSchedules(ctx)
	for _, id := range []string{"off", "bad", "on"} {
		if c := h.cursor(id); c != nil {
			t.Errorf("%s anchored while triggers.enabled is off: %+v", id, c)
		}
	}
	h.enabled.Store(true)
	h.m.tickSchedules(ctx)
	if h.cursor("on") == nil {
		t.Error("the armed schedule did not anchor once the global switch came on")
	}
	if c := h.cursor("off"); c != nil {
		t.Errorf("the disabled schedule anchored: %+v", c)
	}
	if c := h.cursor("bad"); c != nil {
		t.Errorf("the invalid schedule anchored: %+v", c)
	}
	// A schedule has no source to run once, so the live-poll dry run refuses.
	if _, err := h.m.PollDry(ctx, "on"); !errors.Is(err, ErrNoPoll) {
		t.Errorf("PollDry on a schedule = %v, want ErrNoPoll", err)
	}
	// The other dry run is unchanged: the author supplies the event.
	if j, err := h.m.Test(ctx, "on", Event{"id": "x", "date": "2026-09-18", "hour": 9}); err != nil || j.Outcome == "" {
		t.Errorf("Test on a schedule = %+v, %v", j, err)
	}
}

// TestScheduledReactionResolvesByBranch: everything downstream of the source
// is the same code, so a scheduled `follow_up` resolves its branch and lands
// paused under the propose default.
func TestScheduledReactionResolvesByBranch(t *testing.T) {
	st := openStore(t)
	api := newFakeAPI(t, st, http.StatusOK, `{}`)
	id := taskOnBranch(t, st, "release/2026-09-21", false)
	d := reactionDef(t, ActionFollowUp, func(doc map[string]any) {
		doc["source"] = map[string]any{"type": SourceSchedule, "project": 1, "cron": "0 9 * * 1-5"}
		setPath(doc, "action.branch", "release/{{ .Event.date }}", false)
	})
	sch, err := ParseSchedule(d.Source)
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	at := time.Date(2026, time.September, 21, 9, 0, 0, 0, sch.Zone())
	del, err := fire(t.Context(), st, api, d, sch.Event(at), at)
	if err != nil || del.Outcome != store.DeliveryFired || del.TaskID == nil || *del.TaskID != id {
		t.Fatalf("fire = %+v, %v, want a fired follow_up on task %d", del, err, id)
	}
	reqs := api.requests()
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].path, "/follow_up") ||
		!strings.Contains(string(reqs[0].raw), `"paused":true`) {
		t.Errorf("replays = %+v, want one paused follow_up", reqs)
	}
}
