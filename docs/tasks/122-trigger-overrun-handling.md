# 122 — Configurable trigger overrun handling

**Status:** ✅ done (7/7)
**Opened:** 2026-09-18
**Spec:** decision record row 33, §12.3 (the trigger file), §14
(`trigger_deliveries`, `trigger_backlog`), §16 (the trigger security posture),
§17 (the prune)

## Problem

A trigger had no idea whether the work it started last time was still running.
`internal/trigger/fire.go`'s `judge` walked `match:` → `allowed_actors` →
`if:` → dedupe → `limits.max_per_hour` → render, and none of those steps looked
at the state of any task. Every event that survived the filter fired, however
many of this trigger's tasks were mid-flight.

That is right for a source whose events are naturally one-shot. It is wrong for
one that emits repeatedly about the same object — the motivating case is a
ticket-modification trigger, where saving a ticket four times in a minute
starts four tasks on four worktrees, three of them already stale by the time an
agent picks them up.

The two levers that existed did not cover it. `dedupe_key` is all-or-nothing
and permanent (a 30-day ledger, task 096 decision 21): keyed on the ticket the
trigger fires exactly once ever, keyed on the event id every modification
fires. `limits.max_per_hour` is a wall-clock cap, per trigger and unrelated to
whether anything is running, and decision 28 fixes that it drops rather than
queues. Neither expresses *what should happen to this event given that the
previous one's task has not finished*.

## Decisions

### 1. All five modes land as one task (2026-09-18)

`parallel`, `skip`, `cancel_previous`, `queue_coalesce` and `queue_serial` ship
together with the backlog table, the drain path, restart survival and the
TUI/ledger/skill surfaces. The backlog is the part that shapes the schema, so
no half of it lands before the other half is designed against it.

**Beaten:** the issue's own `parallel`+`skip`-first split, and a `skip`-only
narrowing that would have avoided widening §16 for `cancel_previous`.

### 2. A reaction's group is its resolved target task's own state (2026-09-18)

Read from `tasks.state` directly, ledger or no ledger, and `concurrency_key`
defaults to the resolved task id for `follow_up`/`retry`/`cancel`. The issue
asserted both readings: its *What counts as "in progress"* section defines the
group purely through `trigger_deliveries.task_id`, while its *Scope* section
wants "skip this follow-up while the target is still running" — which that
definition cannot deliver, because on the first follow-up the target has never
appeared in this trigger's ledger. The Scope reading wins.

For `create_task` the group is still found through `trigger_deliveries.task_id`,
and a `fired` row whose `task_id` is NULL (the task was deleted,
`ON DELETE SET NULL`) is not in flight.

### 3. In flight means `!taskstate.Settled(state)` (2026-09-18)

Not `done`, `aborted` or `archived`. The issue's enumeration (`queued`,
`running`, `awaiting_input`, `blocked`, `paused`) is wrong twice, and the spec
amendment corrects it: §6's `Terminal` is `archived` alone, so "non-terminal"
is the wrong word; and the list omits `awaiting_gate` and `awaiting_children`,
both of which are unfinished work on the same object and both of which now hold
the group. `taskstate.Settled` already exists for precisely this question (task
014 decision 20), so no new vocabulary is introduced.

**Beaten:** `HoldsSlot`, which would have let unreviewed proposals stack up —
the motivating case.

### 4. The supersede link is a ledger column (2026-09-18)

`trigger_deliveries.superseded_task_id`, `REFERENCES tasks(id) ON DELETE SET
NULL` like `task_id`. The chain reads out of `GET /v1/triggers/{id}/deliveries`,
the CLI ledger and the TUI ledger — the surfaces the issue already names.
`tasks` gains no column and the task DTO no field: the board is not asked to
render a relationship only triggers ever set.

### 5. The backlog survives a restart and is discarded on disarm (2026-09-18)

Held rows are durable (crash-first). When a trigger disarms — `enabled: false`,
`triggers.enabled` off, or the file leaving the registry — the backlog is
dropped and each held event recorded `superseded`, exactly as decision 16 drops
the cursor so that an off period never fires. Re-arming seeds afresh and fires
nothing it held. Deleting a trigger drops the backlog and keeps the ledger, per
decision 21.

### 6. A held event is re-judged at drain (2026-09-18)

Not replayed frozen. The backlog row stores the raw event JSON; draining runs
`judge` again in full — dedupe, `max_per_hour`, render, reaction target
resolution — minus the overrun step, which the event has already passed and
which would otherwise re-queue it forever. A rate cap met in the meantime is
honoured, and a reaction re-resolves its branch against the tasks that exist
now.

### 7. The overrun check is the last step of `judge` (2026-09-18)

After render and after a reaction's target resolution, immediately before the
`fired` verdict. A reaction cannot know its group before resolution, and one
position keeps one definition. It reads only, so
`POST /v1/triggers/{id}/test` reports it exactly as it reports `would_dedupe`,
and writes nothing.

### 8. A `queued` ledger row is not "delivered" (2026-09-18)

The dedupe lookup keeps treating `fired` and `seeded` alone as delivered, so a
second identical event arriving while one is held falls through to the overrun
step and is coalesced or queued, rather than being swallowed as a duplicate.
`max_per_hour` keeps counting `fired` alone, so a drained event is counted when
it fires.

### 9. The drain signal is a broker subscription (2026-09-18)

Wired beside `notify`'s in `internal/daemon/daemon.go:Run`
(`broker.OnEvent(triggers.OnEvent)`). The manager takes `*store.Event` and
imports no new package — `internal/notify` already has this exact shape. The
5 s `reconcileEvery` tick is the backstop, and the first drain of a daemon run
empties groups whose tasks settled while it was down.

### 10. Firing gets one per-trigger mutex (2026-09-18)

The manager had three firing paths (a poller goroutine per command trigger,
`ghMu`, `ingestMu`); the drain is a fourth, and it can run for a trigger whose
poll is live. A per-trigger lock covering all four replaces the two coarse
mutexes, so the overrun read, the backlog write and the ledger write of one
delivery never interleave with another's for the same trigger.

### 11. `concurrency_key` is recorded on the delivery row (2026-09-18)

The issue's surface list did not name it, but the group query cannot work
without it: the in-flight set is `fired` deliveries of this trigger in this
group joined to `tasks.state`, and a group whose key were re-rendered at read
time would move whenever the template did — and would be unrecoverable for an
event long since consumed. So `trigger_deliveries.concurrency_key` lands in the
same migration, `''` for every existing row and for every trigger that declares
no `overrun:`.

### 12. An event a group already holds is not held twice (2026-09-18)

Decision 8 keeps a `queued` row out of the dedupe lookup so a *repeat* event
reaches the overrun step rather than being swallowed. A source that keeps no
cursor re-shows its whole window every poll — the shape decision 31B's `seeded`
rows exist for — and would otherwise append the same event once a second until
it hit the cap, with `queue_serial` firing it once per copy. So holding checks
the group for the event id first and records `superseded` when it finds it.
Found by `scripts/m16-gate.sh`, whose poll source is exactly that shape.

## Risks, recorded because they are intended

- **`overrun: skip` plus the `on_fire: propose` default is a foot-gun by
  construction.** One unreviewed proposal holds its group forever. Intended,
  and the first thing a confused author hits — so it is in §12.3, in the skill,
  in `debugging.md` and in the guide, not only in a release note.
- **`queue_serial` under `propose`** drains only as fast as a human approves, so
  the backlog reaches its cap of 100 on a busy object. The cap's drop is
  recorded `superseded`, which is what makes it explicable.
- **`cancel_previous` lets an inbound event destroy in-flight agent work.** §16
  already says triggers invert its premise; the amendment says this mode raises
  what `allowed_actors` is worth, at the point the mode is introduced.

## Tasks

- [x] 122.1 `overrun:` and `concurrency_key:` on the definition, with the
      validator clauses and the schema descriptor. ✓ 2026-09-18
- [x] 122.2 Migration 0033: the widened `CHECK`, `concurrency_key`,
      `superseded_task_id` and `trigger_backlog`; the group query, the backlog
      CRUD and the prune. ✓ 2026-09-18
- [x] 122.3 The overrun step in `judge`, the `cancel_previous` replay loop, the
      backlog write and its cap. ✓ 2026-09-18
- [x] 122.4 The drain path, `OnEvent`, per-trigger locking and discard on
      disarm. ✓ 2026-09-18
- [x] 122.5 The API, apiclient, TUI and CLI surfaces. ✓ 2026-09-18
- [x] 122.6 Spec amendments, the skill, both trigger built-ins and the public
      docs. ✓ 2026-09-18
- [x] 122.7 Tests for the eight acceptance criteria, and an `m16` lane per
      mode. ✓ 2026-09-18

## Verification

`go test ./...` and `go run mage.go lint` on macOS, plus `./scripts/m16-gate.sh`
against the fake agent. The eight acceptance criteria each have a named test in
`internal/trigger/overrun_test.go`; the restart leg is proved twice, in process
over a rebuilt manager on the same database and in the gate against the real
binary.
