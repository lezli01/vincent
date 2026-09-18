package trigger

import (
	"strings"
	"testing"
	"time"

	// The IANA database, for the two DST tests below. cmd/vincent imports it
	// for the shipped binary (task 121); a test binary links no cmd, and
	// Windows has no tz database on disk, so without this the DST cases would
	// silently not run on the one platform whose zone handling differs.
	_ "time/tzdata"

	"github.com/lezli01/vincent/internal/store"
)

// nyc and budapest are the zones the DST rules are read in. Loading them here
// also proves the embedded database reaches every platform's test binary.
func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

// TestParseCron: the grammar the parser accepts is the one CronGrammar
// states, and nothing wider. A form renders that sentence as the field's
// help, so an expression it describes must load and one it does not must
// refuse.
func TestParseCron(t *testing.T) {
	for _, expr := range []string{
		"* * * * *",
		"0 9 * * 1-5",
		"*/15 * * * *",
		"0 0,6,12,18 * * *",
		"30 1-5 1,15 1-6/2 *",
		"0 9 * * 0",
		"0 9 * * 7",
		"59 23 31 12 *",
		"0 0 29 2 *",
		"0 9-17/4 * * 1-5",
	} {
		if _, err := parseCron(expr); err != nil {
			t.Errorf("parseCron(%q) refused a documented expression: %v", expr, err)
		}
	}
	for _, tc := range []struct{ expr, why string }{
		{"* * * *", "four fields"},
		{"* * * * * *", "a seconds field"},
		{"@daily", "a descriptor"},
		{"0 9 L * *", "the L extension"},
		{"0 9 * * 1#2", "the # extension"},
		{"60 * * * *", "minute out of range"},
		{"* 24 * * *", "hour out of range"},
		{"* * 0 * *", "day of month out of range"},
		{"* * * 13 *", "month out of range"},
		{"* * * * 8", "day of week out of range"},
		{"5-1 * * * *", "a backwards range"},
		{"*/0 * * * *", "a zero step"},
		{"*/-1 * * * *", "a negative step"},
		{"0,,5 * * * *", "an empty list entry"},
		{"x * * * *", "a non-number"},
	} {
		if _, err := parseCron(tc.expr); err == nil {
			t.Errorf("parseCron(%q) accepted %s", tc.expr, tc.why)
		}
	}
}

// TestScheduleNextWeekdays: the expression §20 named as the reason to reopen
// means what a crontab means by it.
func TestScheduleNextWeekdays(t *testing.T) {
	loc := zone(t, "Europe/Budapest")
	s, err := ParseSchedule(Source{Type: SourceSchedule, Cron: "0 9 * * 1-5", Timezone: "Europe/Budapest"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	// Friday 2026-09-18 10:00 local: the next is Monday, not Saturday.
	from := time.Date(2026, time.September, 18, 10, 0, 0, 0, loc)
	got := s.Next(from)
	want := time.Date(2026, time.September, 21, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("Next = %s, want %s", got, want)
	}
	if wd := s.Event(got)["weekday"]; wd != "Monday" {
		t.Errorf("weekday = %v, want Monday", wd)
	}
}

// TestScheduleDST: the two rules spec §12.2 writes out, in a real zone that
// has both transitions.
func TestScheduleDST(t *testing.T) {
	loc := zone(t, "America/New_York")
	// 02:30 every day. On 2026-03-08 the clock jumps 02:00 → 03:00, so
	// 02:30 never happens: the occurrence fires once, at the first real
	// instant past the jump.
	s, err := ParseSchedule(Source{Type: SourceSchedule, Cron: "30 2 * * *", Timezone: "America/New_York"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	got := s.Next(time.Date(2026, time.March, 7, 23, 0, 0, 0, loc))
	want := time.Date(2026, time.March, 8, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("spring forward: Next = %s, want the first instant past the jump %s", got, want)
	}
	if next := s.Next(got); next.Day() != 9 {
		t.Errorf("spring forward: the skipped occurrence fired twice; next = %s", next)
	}

	// 01:30 every day. On 2026-11-01 the clock repeats 01:00–01:59, so 01:30
	// happens twice: it fires once, at the first of the two.
	s, err = ParseSchedule(Source{Type: SourceSchedule, Cron: "30 1 * * *", Timezone: "America/New_York"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	first := s.Next(time.Date(2026, time.October, 31, 23, 0, 0, 0, loc))
	if _, off := first.Zone(); off != -4*3600 {
		t.Fatalf("fall back: first 01:30 is %s (offset %ds), want the EDT pass", first, off)
	}
	next := s.Next(first)
	if next.Day() != 2 {
		t.Errorf("fall back: 01:30 fired twice; after %s came %s, want the next day", first, next)
	}
	// LastDue agrees: the whole repeated hour yields that one occurrence.
	endOfHour := first.Add(90 * time.Minute)
	if due := s.LastDue(first, endOfHour); !due.IsZero() {
		t.Errorf("fall back: LastDue found a second occurrence at %s", due)
	}
}

// TestScheduleEveryFromAnchor: `every:` is counted from the anchor arming
// stored (decision 4), and an overdue schedule yields one occurrence however
// many it missed.
func TestScheduleEveryFromAnchor(t *testing.T) {
	s, err := ParseSchedule(Source{Type: SourceSchedule, Every: "6h"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	anchor := time.Date(2026, time.September, 18, 10, 17, 0, 0, time.UTC)
	if got, want := s.Next(anchor), anchor.Add(6*time.Hour); !got.Equal(want) {
		t.Errorf("Next = %s, want the anchor plus the interval %s", got, want)
	}
	if due := s.LastDue(anchor, anchor.Add(5*time.Hour)); !due.IsZero() {
		t.Errorf("LastDue before the first occurrence = %s, want none", due)
	}
	// Two days asleep: one occurrence, and it keeps the anchor's phase.
	due := s.LastDue(anchor, anchor.Add(48*time.Hour))
	if want := anchor.Add(48 * time.Hour); !due.Equal(want) {
		t.Errorf("LastDue = %s, want the last missed occurrence %s", due, want)
	}
	if due.Minute() != anchor.Minute() {
		t.Errorf("LastDue = %s, want the anchor's phase (minute %d)", due, anchor.Minute())
	}
}

// TestScheduleLastDueFiresOnce: a cron schedule that stood still over many
// occurrences yields the last one, not all of them.
func TestScheduleLastDueFiresOnce(t *testing.T) {
	s, err := ParseSchedule(Source{Type: SourceSchedule, Cron: "0 * * * *", Timezone: "UTC"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	anchor := time.Date(2026, time.September, 18, 0, 0, 0, 0, time.UTC)
	now := anchor.Add(50 * time.Hour)
	due := s.LastDue(anchor, now)
	if want := time.Date(2026, time.September, 20, 2, 0, 0, 0, time.UTC); !due.Equal(want) {
		t.Errorf("LastDue = %s, want the last hour that passed %s", due, want)
	}
	// From the occurrence it just handled, nothing is due until the next one.
	if again := s.LastDue(due, due.Add(30*time.Minute)); !again.IsZero() {
		t.Errorf("LastDue right after a fire = %s, want none", again)
	}
}

// TestScheduleEventKeys: the keys decision 3 settled, in the case decision 3
// settled them for — the occurrence in UTC, its parts in the author's zone.
func TestScheduleEventKeys(t *testing.T) {
	s, err := ParseSchedule(Source{Type: SourceSchedule, Cron: "0 9 * * 1-5", Timezone: "Europe/Budapest"})
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	at := time.Date(2026, time.September, 21, 7, 0, 0, 0, time.UTC) // 09:00 in Budapest
	ev := s.Event(at)
	stamp := at.Format(store.TimeFormat)
	for k, want := range map[string]any{
		"id": stamp, "scheduled_at": stamp,
		"weekday": "Monday", "hour": 9, "minute": 0, "date": "2026-09-21",
	} {
		if got := ev[k]; got != want {
			t.Errorf(".Event.%s = %v, want %v", k, got, want)
		}
	}
	if len(ev) != 6 {
		t.Errorf(".Event has %d keys (%v), want the six decision 3 names", len(ev), ev)
	}
	// The stamp round-trips through the cursor column it is stored in.
	back, err := time.Parse(store.TimeFormat, stamp)
	if err != nil || !back.Equal(at) {
		t.Errorf("the occurrence did not round-trip through store.TimeFormat: %v %v", back, err)
	}
}

// TestScheduleRefusals: every load-time rule, at the path a form renders it
// against.
func TestScheduleRefusals(t *testing.T) {
	base := func() map[string]any { return docFor(SourceSchedule, ActionCreateTask) }
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"both cron and every", func(d map[string]any) { setPath(d, "source.every", "1h", false) }, "source.every"},
		{"neither cron nor every", func(d map[string]any) { setPath(d, "source.cron", nil, true) }, "source.cron"},
		{"every under the floor", func(d map[string]any) {
			setPath(d, "source.cron", nil, true)
			setPath(d, "source.every", "500ms", false)
		}, "source.every"},
		{"every not a duration", func(d map[string]any) {
			setPath(d, "source.cron", nil, true)
			setPath(d, "source.every", "soon", false)
		}, "source.every"},
		{"a cron the grammar does not accept", func(d map[string]any) {
			setPath(d, "source.cron", "@daily", false)
		}, "source.cron"},
		{"a cron no calendar satisfies", func(d map[string]any) {
			setPath(d, "source.cron", "0 0 31 4 *", false)
		}, "source.cron"},
		{"an unknown zone", func(d map[string]any) {
			setPath(d, "source.timezone", "Mars/Olympus", false)
		}, "source.timezone"},
		{"a poll interval", func(d map[string]any) {
			setPath(d, "source.poll_interval", "1m", false)
		}, "source.poll_interval"},
		{"a command", func(d map[string]any) {
			setPath(d, "source.command", []any{"poll"}, false)
		}, "source.command"},
		{"a signature", func(d map[string]any) {
			setPath(d, "source.signature", map[string]any{
				"scheme": SignatureGitHubHMACSHA256, "secret_env": "S",
			}, false)
		}, "source.signature"},
		{"allowed_actors", func(d map[string]any) {
			setPath(d, "allowed_actors", []any{"someone"}, false)
		}, "allowed_actors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := base()
			tc.edit(doc)
			errs := parseDoc(t, doc)
			if !hasPath(errs, tc.want) {
				t.Fatalf("errors %v, want one at %s", errs, tc.want)
			}
			if len(errs) != 1 {
				t.Errorf("errors %v, want exactly one", errs)
			}
		})
	}

	// A cancel still needs on_fire: create on a schedule (decision 31C), and
	// the reaction fields render over a schedule's event.
	doc := docFor(SourceSchedule, ActionCancel)
	setPath(doc, "on_fire", nil, true)
	if errs := parseDoc(t, doc); !hasPath(errs, "on_fire") {
		t.Errorf("a scheduled cancel without on_fire: create was accepted: %v", errs)
	}
	// The default zone is the daemon host's, and nothing refuses it.
	if errs := parseDoc(t, base()); len(errs) > 0 {
		t.Errorf("a schedule with no timezone: was refused: %v", errs)
	}
	s, err := ParseSchedule(Source{Type: SourceSchedule, Cron: "0 9 * * *"})
	if err != nil {
		t.Fatalf("ParseSchedule with no zone: %v", err)
	}
	if s.Zone() != time.Local {
		t.Errorf("zone = %v, want the host's %v", s.Zone(), time.Local)
	}
	if !strings.Contains(CronGrammar, "no @descriptors") {
		t.Error("CronGrammar no longer states what it refuses")
	}
}
