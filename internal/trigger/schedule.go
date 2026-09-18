package trigger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// The `type: schedule` source (task 121): the clock as an event source. A
// schedule is decision 1's robot pressing a key a human could have pressed,
// so everything downstream of the source is the same code every other source
// reaches — `match:`, `if:`, `dedupe_key:`, the four actions, the propose
// gate, `limits.max_per_hour` and the ledger.
//
// The cron parser here is hand-written (task 121 decision 1), which narrows
// task 115 decision 2 rather than overturning it: that decision beat cron for
// `backup.interval` because it "needs a parser dependency", and the parser is
// written instead. It also keeps the promise the issue asked for — that the
// accepted grammar is exactly what the served schema documents — by
// construction, which a third-party parser with descriptors and extensions
// could not.

// CronGrammar is the whole accepted syntax, in one sentence, so the schema
// help, the guide and the validator's message cannot drift apart.
const CronGrammar = "five space-separated fields — minute (0-59), hour (0-23), " +
	"day of month (1-31), month (1-12) and day of week (0-7, where 0 and 7 are Sunday) — " +
	"each a '*', a single value, a range 'a-b', a comma list of either, or any of those with " +
	"a '/n' step; no @descriptors, no seconds field and no 'L' or '#' extensions"

// maxCatchUpOccurrences bounds one tick's walk from the stored anchor to the
// last due occurrence. It is not a cap on what fires — a tick fires once
// however many occurrences it skipped (task 121, "fire once if overdue") —
// only on how far one walk moves the anchor. An anchor older than this many
// occurrences catches up over successive one-second ticks, which is seconds
// even for a per-minute schedule that stood still for a month.
const maxCatchUpOccurrences = 20000

// minuteSearchLimit bounds a civil-minute search for the next matching
// occurrence: over four years of minutes, so `0 0 29 2 *` is found and an
// impossible combination such as 31 April terminates.
const minuteSearchLimit = 4 * 366 * 24 * 60

// Schedule is a validated `type: schedule` source: either a cron expression
// or a fixed interval, in a zone.
type Schedule struct {
	cron  *cronExpr
	every time.Duration
	loc   *time.Location
}

// Zone is the schedule's location, which `.Event`'s parts are broken out in.
func (s *Schedule) Zone() *time.Location { return s.loc }

// ParseSchedule builds the clock a valid `type: schedule` source describes.
// The field-by-field refusals live in validateSource, which reports each at
// the path a form renders it against; this returns one error and is what the
// manager runs on.
func ParseSchedule(src Source) (*Schedule, error) {
	loc := time.Local
	if src.Timezone != "" {
		l, err := time.LoadLocation(src.Timezone)
		if err != nil {
			return nil, fmt.Errorf("time zone %q: %w", src.Timezone, err)
		}
		loc = l
	}
	s := &Schedule{loc: loc}
	switch {
	case src.Cron != "" && src.Every != "":
		return nil, errBothSchedules
	case src.Every != "":
		iv, err := time.ParseDuration(src.Every)
		if err != nil {
			return nil, fmt.Errorf("every: %w", err)
		}
		if iv < MinPollInterval {
			return nil, fmt.Errorf("every: must be at least %s", MinPollInterval)
		}
		s.every = iv
	case src.Cron != "":
		c, err := parseCron(src.Cron)
		if err != nil {
			return nil, err
		}
		s.cron = c
		// An expression no calendar ever satisfies — 31 April — would arm a
		// clock that never strikes. Probed from a fixed leap year so the
		// answer does not depend on today.
		if s.Next(time.Date(2024, time.January, 1, 0, 0, 0, 0, loc)).IsZero() {
			return nil, fmt.Errorf("cron %q has no occurrence: no date satisfies its day, month and weekday fields", src.Cron)
		}
	default:
		return nil, errNoSchedule
	}
	return s, nil
}

// Next is the first occurrence strictly after t, or the zero time when the
// expression has none.
//
// The two DST rules (spec §12.2) both fall out of mapping each *civil* minute
// the expression matches to one instant:
//
//   - an occurrence the spring-forward jump skipped has no instant of its own,
//     so it fires once at the first real instant past the jump;
//   - an occurrence in the repeated fall-back hour maps to the first of its two
//     instants and fires once there, because the second pass is no longer
//     "after" the fire that already happened.
//
// `every:` is a duration and is unaffected by either.
func (s *Schedule) Next(t time.Time) time.Time {
	if s.cron == nil {
		return t.Add(s.every)
	}
	cur := civilOf(t.In(s.loc))
	for range minuteSearchLimit {
		switch {
		case !s.cron.month.has(int(cur.month)):
			cur = cur.nextMonth()
			continue
		case !s.cron.matchesDay(cur):
			cur = cur.nextDay()
			continue
		case !s.cron.hour.has(cur.hour):
			cur = cur.nextHour()
			continue
		case !s.cron.minute.has(cur.minute):
			cur = cur.nextMinute()
			continue
		}
		if at := s.at(cur); at.After(t) {
			return at
		}
		cur = cur.nextMinute()
	}
	return time.Time{}
}

// LastDue is the last occurrence in (anchor, now], or the zero time when the
// schedule is not due. It is what one tick fires at: a weekend of downtime
// produces the weekend's last occurrence and one task, not forty.
func (s *Schedule) LastDue(anchor, now time.Time) time.Time {
	if s.cron == nil {
		if !now.After(anchor) || s.every <= 0 {
			return time.Time{}
		}
		// Counted from the anchor, not from a wall-clock boundary (decision
		// 4), so `every: 90m` has an honest answer.
		n := now.Sub(anchor) / s.every
		if n < 1 {
			return time.Time{}
		}
		return anchor.Add(n * s.every)
	}
	var last time.Time
	at := anchor
	for range maxCatchUpOccurrences {
		at = s.Next(at)
		if at.IsZero() || at.After(now) {
			break
		}
		last = at
	}
	return last
}

// Event is the `.Event` a fired occurrence carries.
//
// The keys are the lowercase ones the other sources emit (decision 3):
// appendix A reserves `id` in lower case, so a CamelCase set would put two
// conventions in one document. `id` being the occurrence is what makes the
// default dedupe key right with no template — two evaluations inside one
// minute cannot double-fire, because the ledger already holds that
// occurrence.
func (s *Schedule) Event(at time.Time) Event {
	stamp := at.UTC().Format(store.TimeFormat)
	local := at.In(s.loc)
	return Event{
		"id":           stamp,
		"scheduled_at": stamp,
		// Broken out in the schedule's own zone: an author who wrote
		// `0 9 * * 1-5` means their own Monday morning.
		"weekday": local.Weekday().String(),
		"hour":    local.Hour(),
		"minute":  local.Minute(),
		"date":    local.Format(time.DateOnly),
	}
}

// at is the instant a civil minute names in the schedule's zone.
func (s *Schedule) at(c civil) time.Time {
	t := time.Date(c.year, c.month, c.day, c.hour, c.minute, 0, 0, s.loc)
	if civilOf(t) == c {
		return t
	}
	// The civil minute does not exist: a forward jump skipped it, and
	// time.Date normalized to an instant on the near side of the gap. Step to
	// the first instant whose clock has passed it, which is the transition.
	for range 3 * 60 {
		t = t.Add(time.Minute)
		if !civilOf(t).before(c) {
			return t
		}
	}
	return t
}

// civil is a wall-clock minute with no zone: what a cron expression matches.
type civil struct {
	year   int
	month  time.Month
	day    int
	hour   int
	minute int
}

func civilOf(t time.Time) civil {
	y, mo, d := t.Date()
	return civil{year: y, month: mo, day: d, hour: t.Hour(), minute: t.Minute()}
}

func (c civil) before(o civil) bool {
	return c.at(time.UTC).Before(o.at(time.UTC))
}

func (c civil) at(loc *time.Location) time.Time {
	return time.Date(c.year, c.month, c.day, c.hour, c.minute, 0, 0, loc)
}

func (c civil) nextMinute() civil { return civilOf(c.at(time.UTC).Add(time.Minute)) }

func (c civil) nextHour() civil {
	c.minute = 0
	return civilOf(c.at(time.UTC).Add(time.Hour))
}

func (c civil) nextDay() civil {
	c.hour, c.minute = 0, 0
	return civilOf(c.at(time.UTC).AddDate(0, 0, 1))
}

func (c civil) nextMonth() civil {
	c.day, c.hour, c.minute = 1, 0, 0
	return civilOf(c.at(time.UTC).AddDate(0, 1, 0))
}

// weekday is the civil date's day of week, 0 for Sunday.
func (c civil) weekday() int { return int(c.at(time.UTC).Weekday()) }

// cronExpr is a parsed five-field expression. Each field is a bit set over
// its own range, so matching is a shift and a mask.
type cronExpr struct {
	minute, hour, dom, month, dow bitset
	// domStar and dowStar record which of the two day fields was written
	// `*`, because that is what decides whether they are ANDed or ORed.
	domStar, dowStar bool
}

// matchesDay applies the day rule the crontab(5) grammar has had since Vixie
// cron, and which the schema documents: with one day field `*` the other
// decides, and with both restricted an occurrence matches *either*, so
// `0 9 1 * 1` is the first of the month and every Monday.
func (c *cronExpr) matchesDay(d civil) bool {
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.dowStar:
		return c.dom.has(d.day)
	case c.domStar:
		return c.dow.has(d.weekday())
	}
	return c.dom.has(d.day) || c.dow.has(d.weekday())
}

// bitset is a set over 0..63, which covers every cron field.
type bitset uint64

func (b bitset) has(v int) bool { return v >= 0 && v < 64 && b&(1<<v) != 0 }

var (
	errNoSchedule    = errors.New("a schedule needs a cron expression or an interval")
	errBothSchedules = errors.New("cron and every cannot both be set")
)

// cronField is one field's name and accepted range.
type cronField struct {
	name     string
	min, max int
}

var cronFields = []cronField{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day of month", 1, 31},
	{"month", 1, 12},
	{"day of week", 0, 7},
}

// parseCron reads the five-field grammar CronGrammar states, and nothing
// else. Every refusal names the field and the expression, because a form
// reports it against `source.cron` and the author has to find the typo in a
// line of punctuation.
func parseCron(expr string) (*cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != len(cronFields) {
		return nil, fmt.Errorf("cron %q has %d fields, want 5: %s", expr, len(parts), CronGrammar)
	}
	out := &cronExpr{}
	sets := []*bitset{&out.minute, &out.hour, &out.dom, &out.month, &out.dow}
	for i, f := range cronFields {
		set, star, err := parseCronField(parts[i], f)
		if err != nil {
			return nil, err
		}
		*sets[i] = set
		switch i {
		case 2:
			out.domStar = star
		case 4:
			out.dowStar = star
		}
	}
	// 7 is Sunday, the same day 0 names, so a set holding either holds both.
	if out.dow.has(7) {
		out.dow |= 1
	}
	return out, nil
}

// parseCronField reads one field, reporting whether it was written `*` — the
// day fields need to know, and a `*/n` step is not a `*`.
func parseCronField(text string, f cronField) (bitset, bool, error) {
	fail := func(format string, args ...any) (bitset, bool, error) {
		return 0, false, fmt.Errorf("cron %s field %q: "+format, append([]any{f.name, text}, args...)...)
	}
	var set bitset
	for _, term := range strings.Split(text, ",") {
		if term == "" {
			return fail("has an empty list entry")
		}
		spec, stepText, hasStep := strings.Cut(term, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return fail("step %q must be a positive whole number", stepText)
			}
			step = n
		}
		lo, hi := f.min, f.max
		if spec != "*" {
			a, b, isRange := strings.Cut(spec, "-")
			var err error
			if lo, err = cronValue(a, f); err != nil {
				return 0, false, err
			}
			hi = lo
			if isRange {
				if hi, err = cronValue(b, f); err != nil {
					return 0, false, err
				}
				if hi < lo {
					return fail("range %q runs backwards", spec)
				}
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << v
		}
	}
	return set, text == "*", nil
}

func cronValue(text string, f cronField) (int, error) {
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("cron %s field: %q is not a whole number; %s", f.name, text, CronGrammar)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("cron %s field: %d is outside %d-%d", f.name, v, f.min, f.max)
	}
	return v, nil
}
