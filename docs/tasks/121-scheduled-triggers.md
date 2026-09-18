# 121 — Scheduled triggers: a `type: schedule` source

**Status:** ✅ done (1/1)
**Issue:** [#480](https://github.com/lezli01/vincent/issues/480)
**Spec:** amends §3 decision row 33, §12.2, §13.2, §15, §16, §20
**Builds on, without relitigating:** [096](096-event-triggers.md) — decisions
2, 3, 6, 7, 10, 11, 12, 13, 16, 17, 19, 21, 28, 30, 31B, 31C, 31F and 31G all
hold unchanged; [098](098-agent-authored-triggers.md) decisions 3 and 7
**Narrows deliberately:** [115](115-scheduled-daemon-backups.md) decision 2's
"a cron expression … needs a parser dependency" — see decision 1

## Problem

§20 deferred "scheduled and recurring triggers" with a named reopening
condition: *the first recurring task that a `type: command` source printing one
event per period cannot express.* It came due. A weekday-morning schedule —
"09:00, Monday to Friday, in my zone" — is not expressible as a poll interval,
and the workarounds all lose something `internal/trigger` exists to provide. A
crontab line calling `vincent task create` skips the `match:` filter, the dedupe
ledger, the `propose` gate, the hourly limit and the delivery history, which is
the whole of what a trigger is.

The entry's own prediction was the shape of the answer: task 096 designed the
`action:` block so a clock source reuses it verbatim, so this is one more
`source.type`, not a subsystem.

## Decisions

### 1. The cron parser is hand-written, in `internal/trigger` (2026-09-18)

Five fields — minute, hour, day of month, month, day of week — with `*`,
ranges, lists and `/n` steps, and nothing else: no `@daily` descriptors, no
seconds field, no `L` or `#` extensions. `go.mod`'s direct requires are
untouched.

This **narrows** [115](115-scheduled-daemon-backups.md) decision 2 rather than
overturning it, and decision 2's own reasoning is not edited. That decision beat
cron for `backup.interval` on two grounds. The first — *"needs a parser
dependency"* — is kept by writing the parser. The second — *"something cron and
Task Scheduler already do outside vincent"* — does not transfer, for the reason
in *Problem* above: an external crontab entry bypasses the filter, the ledger,
the gate, the limit and the history. `backup.interval` stays a
`config.Duration` and is untouched by this work.

**Beat:** `github.com/robfig/cron/v3`, which brings ranges, steps, descriptors
and zone handling free but contradicts 115 decision 2 head-on and accepts a
grammar wider than the served schema documents. The issue's own condition was
that *"the accepted grammar should be exactly what the served schema
documents"*, and a hand-written parser gets that by construction.

**Beat:** `every:` alone, which would uphold 115 decision 2 untouched but
cannot express "weekdays at 09:00" — the case §20 named as the reason to
reopen.

- *Decided at implementation time:* **with both day fields restricted, an
  occurrence matches either**, as crontab(5) has done since Vixie cron, so
  `0 9 1 * 1` is the first of the month *and* every Monday. It is documented in
  three places rather than left to be discovered. **Beat:** ANDing them, which
  differs from every crontab a reader has seen and makes `0 9 1 * 1` fire
  roughly once a year.
- *Decided at implementation time:* **`0` and `7` both name Sunday**, as a
  crontab does, and `8` is refused.
- *Decided at implementation time:* **an expression no calendar satisfies is
  refused at load.** `0 0 31 4 *` arms a clock that never strikes, so
  `ParseSchedule` probes it once from a fixed leap year — a deterministic
  answer that does not depend on today.

### 2. The schedule's position lives in `trigger_cursors.cursor` (2026-09-18)

The string holds the last handled occurrence in `store.TimeFormat` — fixed-width
RFC3339 UTC — the way a GitHub source holds its snapshot JSON in the same
column. No migration, no new column, no ledger scan, and decision 13's 30-day
`trigger_deliveries` prune stays irrelevant to it, so a quarterly schedule
works.

096 decision 16 already drops the cursor on disarm, which gives the issue's
required first-arm behaviour for free: **arming seeds the anchor at `now` and
fires nothing.** A daemon stop, a suspend or a reboot does *not* disarm, so the
anchor survives exactly the case the feature is motivated by — a laptop asleep
overnight. A trigger explicitly disabled and re-enabled, or `triggers.enabled`
toggled off and on, resets the anchor and fires nothing on the re-arm. That
consequence is correct and will surprise someone, so it is written down in the
skill, the guide and §12.2.

**Beat:** the existing `trigger_cursors.last_fire_at` column, which is set only
by an actual fire and so cannot distinguish "armed, never fired" from "overdue";
it would need the seed time stored beside it anyway. **Beat:** the issue's own
proposal of scanning `trigger_deliveries`, which costs a query per check and
forces one of its two fixes for the 30-day prune.

`store.timeFormat` is exported as `store.TimeFormat` for this: a value a caller
keeps *in* a TEXT column has to be written in the same layout, and a second copy
of the layout is the kind of thing that drifts.

### 3. `.Event` uses the sources' existing lowercase keys (2026-09-18)

`id` and `scheduled_at` are the occurrence in `store.TimeFormat`; `weekday`,
`hour`, `minute` and `date` are broken out **in the schedule's own zone**,
because an author who wrote `0 9 * * 1-5` means their own Monday morning. Six
keys, no more.

`id` being the occurrence makes the default dedupe key do the right thing with
no template: two evaluations inside one minute cannot double-fire, because the
ledger already holds that occurrence.

The issue's example wrote `{{ .Event.ScheduledAt }}`; that is corrected to
`{{ .Event.scheduled_at }}` in the spec, the skill, the guide and the schema
help, to match the `id`/`action`/`author`/`number` keys
`internal/trigger/github.go` already emits. Appendix A reserves `id` in lower
case, so a CamelCase set would put two conventions in one document.

### 4. `every:` is counted from the last occurrence, seeded at arming (2026-09-18)

`every: 6h` first fires six hours after arming and then every six hours, read
back from the same stored anchor after a restart. **Beat:** wall-clock
boundaries (00:00/06:00/12:00/18:00), which are identical on every machine but
have no honest answer for `every: 7h` or `every: 90m`.

### 5. Schedules are evaluated on a one-second wall-clock tick (2026-09-18)

One goroutine in the manager walking every armed schedule entry, not a
`time.NewTimer(untilNextOccurrence)` per trigger.

Go's timers run on the monotonic clock, which does not advance while a Mac is
asleep, so a per-trigger timer would come due hours late on exactly the machine
this feature is for. Reading the wall clock also makes suspend, a daemon
restart and a config edit one code path — each is only "the anchor is older
than the previous due occurrence" — and it keeps the one-second `every:` floor
honest, which the existing five-second `reconcileEvery` tick would not. The tick
writes the cursor row on exactly two occasions, the anchor and a fire, so an
idle schedule costs one read a second and no write.

### 6. `cmd/vincent` embeds the IANA database (2026-09-18)

`import _ "time/tzdata"`, ~450 KB in the binary. `time.Local` comes from the
Windows registry, so the *default* zone works there, but `time.LoadLocation`
reads no tz database on Windows: without the embedded copy a named
`timezone: Europe/Budapest` would load on Linux and macOS and refuse on
Windows. Cross-platform is a hard requirement, so this is decided deliberately
here rather than discovered by the Windows CI leg. It is imported in `cmd` and
in `internal/trigger`'s test file, never in the package itself, so a test
binary cannot pass on a database the shipped binary lacks.

## Taken from the issue as written, not re-asked

- **Two mutually exclusive fields, exactly one required.** Both set refuses at
  load; neither set refuses at load. `poll_interval` on a `type: schedule`
  source **refuses** rather than being ignored, as do `command` and
  `signature`; `allowed_actors` is refused by the existing `!d.IsGitHub()`
  clause. The three new keys are refused on the other four source types by the
  mirror-image clause.
- **`MinPollInterval`'s one-second floor applies to `every:`**, for its own
  recorded reason: it stops `1ms` or a bare `5` from becoming a fire loop, and
  it is what lets the m16 gate watch a fire in seconds rather than minutes.
- **DST.** An occurrence inside the skipped spring-forward hour fires once, at
  the first real instant after the jump. An occurrence inside the repeated
  fall-back hour fires **once**, not twice. `every:` is a duration and is
  unaffected by either. Both rules are in §12.2 rather than left to what the
  implementation happens to do — and both fall out of mapping each *civil*
  minute the expression matches to one instant.
- **Fire once if overdue, then resume.** A weekend of downtime produces one
  task, not forty. Decision 13's catch-up cap of 20 events per poll is
  explicitly *not* reused: fire-once is a strictly stronger bound.
- **An unknown `timezone:` refuses at load**, never falls back to UTC silently.

## Two things the issue asserted that were not quite true

- **The TUI was not free.** The form is — it renders from the served
  descriptor — but two cells in `internal/tui/triggersrender.go` hardcoded the
  source type. `trigPollCell` answered `"push"` for `http` and otherwise
  reported poll health, so a schedule would have read `"not yet"` forever; it
  now answers `"clock"`. The armed hint's "its next poll seeds and fires
  nothing" is wrong for a clock and now has a schedule wording.
- **A long timer under-fires on a laptop** — decision 5.

## Work

- [x] **121.1** The source: `SourceSchedule` and the three fields with their
  refusals; `internal/trigger/schedule.go` (the parser, `Next`, `LastDue`, the
  `.Event` builder); the manager's schedule tick and seed-on-arm; `PollDry`'s
  `ErrNoPoll`; the schema variant; the two TUI cells; `time/tzdata`;
  `store.TimeFormat`; the skill and its reference; `update-triggers`'
  version-coupled checklist; m16 scenario 11; the spec amendments.

## Notes

- No migration, no new route, no new CLI command, and no new client field: the
  starter writes a command source and one `PATCH` turns it into a clock, which
  is the path the form takes too.
- `POST /v1/triggers/{id}/test` is unchanged and still works on a schedule —
  the author supplies a synthetic event object. `POST /v1/triggers/{id}/poll`
  refuses, as it does for `http`: there is no source to run once.
- The never-arm rule in both trigger built-ins' headers is unchanged, and
  `vincent trigger apply` keeps enforcing it (096 decision 3).
