# 083 — Make a loop's position and its earlier iterations legible

**Status:** ✅ done (1/1)
**Issue:** #317
**Amends:** §7.6, §7.8, §14, §15
**Extends:** [016](016-workflow-loops.md) decision 8 ("the row carries its item; the
extent is a fact the rows hold") by one word, and leaves decision 7 ("no
persisted loop cursor") and decision 14 ("latest iteration open on arrival")
standing as written

A task sitting on a `loop` was the one shape of work the TUI could not answer
"where is this, right now" about. The outer `step k/n` counts a whole loop as a
single step; the §7.8 rollup carried `{driver, iteration, max_iterations,
item}` and so named neither the body step running nor, for a `for_each`, a
denominator the loop was actually going to reach; and the timeline folded every
iteration but the latest with no key that opened one. Four faults, confirmed in
the code rather than taken on the issue's word:

1. Which body step is running was nowhere on the wire.
2. `renderTimeline` skipped folded rows and never entered their ids, while
   `moveSelection` walked *every* row — so `↑`/`↓` moved the selection onto
   rows that were not drawn, the highlight vanished and the window jumped to
   the top of the timeline. The comment there ("cannot happen, because it never
   entered ids") was true for mouse hit-testing and false for the arrow keys.
3. `loopDefinitionOf` took the total from `count:`, else the step's
   `max_iterations:`, else the config ceiling — so a 3-item `for_each` with
   neither rendered `loop 2/10`.
4. `loopIndexes` keyed off `Iteration > 0`, which a multi-round `fan_out` sets
   too (task 080 decision 3), so its rounds were titled "iteration N" and round
   0 got no header at all.

## Decisions

1. **The `for_each` extent is persisted on the row, beside its item.**
   Migration 0026 adds `step_runs.loop_total`, written on every body row from
   the admission's `loopPlan.total` exactly as `loop_item` is written from
   `plan.items`. This is task 016 decision 8's sentence carried one word
   further: the row already recorded *which item* iteration 3 ran on, and now
   records *how many iterations that admission planned*. It is **not** a cursor
   — decision 7 refused one and still does: nothing reads it back to decide what
   runs next, the loop's position is still derived from the rows, and §12.4
   recovery has nothing to reconcile with it.

   Two alternatives were rejected. Asking the live `taskrun.Runner` for the
   in-flight plan needs no migration but reports the real total only while an
   actor exists — so a **blocked or paused** loop, the exact case a human is
   staring at, would silently fall back to the ceiling again. Materializing the
   resolved list into the task snapshot (task 080 decision 5's precedent) would
   freeze a list decision 8 deliberately re-derives per admission for
   iterations that have not started, making it a change to the loop rather than
   to the rollup.

2. **`total` and `max_iterations` are two numbers and neither is made to mean
   the other.** `max_iterations` keeps its documented meaning — the ceiling
   this loop is bounded by, the number it would block on. `total` is the extent
   the running admission planned, absent (0) until a row records one, because
   before the first iteration there is nothing to report and a guess would read
   like an answer. The client prefers `total` when it has one, so a pre-0026
   row keeps rendering exactly what it rendered before the upgrade.

3. **The body clause is absent whole, or not at all.** A row whose `step_id` is
   not one of the snapshot's body ids — a repair row, or any row once the
   snapshot no longer parses — gets no `body_step`, no `body_index` and no
   `body_total`, rather than a position counted against a body it did not run
   in. A rollup that degrades to driver + iteration is honest; one that guesses
   is not.

4. **The timeline's selection stays a run id; a folded tier is one cursor
   stop.** `↑`/`↓` treat a folded iteration or round as a single stop — its
   first row — and `renderTimeline` highlights that tier's *header* while such
   a row is selected. Every selection the arrow keys can reach is therefore
   drawn, which is the issue's fourth acceptance criterion, and no second kind
   of selection enters `moveSelection`, `syncOutput`, `visibleRuns` hit-testing
   or the Output tab's `←`/`→`. Making a header its own kind of cursor stop was
   considered and rejected on exactly that cost.

   The two moves are separate entry points (`moveTimelineSelection` beside
   `moveSelection`) rather than one function reading focus off a field: which
   move is wanted is the caller's business, and correlating it with a field is
   how the two came to share the wrong behaviour in the first place.

5. **`enter` means both things, chosen by the row under the cursor.** On a
   folded tier it opens the fold — the attempt it would otherwise carry the
   reader to is one they cannot see yet — and on a drawn attempt it keeps
   today's meaning and opens the Output tab. `space` toggles, `→` opens, `←`
   closes, `O`/`C` open and close every tier of the task: the Diff tab's fold
   vocabulary verbatim, because it is the same gesture on the same screen.

6. **Latest-open stays the arrival default** (task 016 decision 14). The folds
   become *openable*, not open. The fold map holds decisions only, so a tier
   arriving mid-run opens by itself, a refresh cannot close one a reader
   opened, and opening another task starts fresh.

7. **A `fan_out`'s rounds are labelled 0-based** — "round 0", "round 1", … The
   label is the column's value, so the screen, the log line and the §12.2
   transcript name (`{step_index}-i{iteration}-{step_id}-{attempt}`) all say
   the same number. Which noun titles the tier comes from the step's type in
   the task's workflow snapshot, since the rows alone cannot tell a round from
   a pass; `loopIndexes`' row-only derivation stays the answer for a task whose
   snapshot has not arrived, and keeps the loop's word there.

8. **A rollup too wide for the `STEP` column drops clauses from the tail** —
   body step first, then the `for_each` item, then the counter — rather than
   wrapping. Three clauses outgrow every width below the ceiling, and a board
   row grown a line to finish a counter spends its height on the least of what
   it says. `widthStepMax` rose from 32 to 34, measured off
   `3/7 green · loop 4/10 · repair 2/3`.

## What landed

| Package | Change |
|---|---|
| `internal/store` | Migration `0026_loop_extent.sql`, `StepRun.LoopTotal`, carried through the insert and every `step_runs` read path |
| `internal/taskrun` | `loopEnv.total` from `plan.total`, `stepEnv.loopTotal()`, written on every row that already carries `LoopItem`. `planLoop` untouched, clamp included |
| `internal/api` | `loopDefinition.body` (the body's step ids in declaration order), `total` / `body_step` / `body_index` / `body_total` on the loop rollup, `loop_total` on `GET /v1/tasks/{id}/steps` |
| `internal/apiclient` | The matching fields, `LoopRollup.Clauses()` in priority order with `Display()` joining them, and a live test against the real handlers |
| `internal/tui` | Fitted board clauses and `widthStepMax` 34; the per-tier fold map, `moveTimelineSelection`, header-carries-the-cursor rendering, `round N` tiers from the snapshot's step type, and the `ctxTimeline` fold bindings |
| `scripts/m8-gate.sh` | Scenario 4 asserts the recorded extent is the list length, read off the rows because a `done` task has no rollup |

## Follow-ups

None open. `scripts/screenshots.sh` seeds no workflow containing a `loop` or a
multi-round `fan_out`, so no `docs/assets/tui-*.png` capture shows a loop
rollup or an iteration tier and none needed re-taking. Seeding one is a new
seed, a new tape and a new asset, and is not what this work was about — it is
the sub-task to open if the pictures should cover loops.
