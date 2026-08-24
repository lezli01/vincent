# 025 — An ad-hoc repair agent for a blocked step

**Status:** ✅ done (8/8)
**Opened:** 2026-08-24

A new human action, `repair`, valid from `blocked` only. The operator supplies a
free-form prompt and optionally an agent, model and effort; the daemon runs one
agent process in the task's *existing* worktree and branch, with the task's own
context and the blocked step's failure context in the prompt; the run is recorded
as its own step run; and when it ends the task returns to `blocked` at the same
step with the same block reason, from which the operator retries, repairs again,
skips or cancels as before.

The gap is real. From `blocked`, `retry` re-runs an unchanged step, `edit +
retry` can rewrite only that step's own prompt or command, and `skip` advances
past an unsatisfied check. Nothing there can change a file. A check that fails
on a missing fixture, a lint rule the agent could not satisfy, a merge artifact
in the tree — all of them need the worktree changed, and the only way to do it
was to leave vincent, open the worktree by hand, and come back.

This does **not** reopen task 018's declined `on_failure:` / try-catch. That
decision is about what a workflow author can declare ahead of time, and 018
concluded `allow_failure:` plus an `if:` on the next step composes the same
thing from parts that already exist. The whole point of a repair is that nothing
was declared ahead of time: it adds a human action, not a workflow field, and
the two do not overlap. No other recorded decision covers ad-hoc repair — §20's
future-work list does not mention it, and `docs/history/v0-tasks.md` carries
nothing binding on it.

## Decisions

1. **2026-08-24 — A repair decides nothing about the blocked step.** When the
   repair run ends — succeeded, failed, either way — the task returns to
   `blocked` at the same step index carrying the block reason it had before. The
   operator inspects the diff and then chooses.

   *Alternative rejected:* auto-retrying the step when the repair succeeds. It
   would mean the operator never sees the repair's diff before the step re-ran,
   and it makes a repair agent's exit code the thing that authorizes more agent
   spend. This is §7.2's posture unchanged — a human decides what a machine
   could not.

2. **2026-08-24 — The repair runs as an ordinary admission, in `running`.**
   `repair` is a human action `blocked → queued` that persists a request on the
   task; the scheduler admits it exactly like anything else, so both §11
   concurrency caps apply and `internal/scheduler` remains the only producer of
   `queued → running`. No new lifecycle state.

   `cancel` keeps its present meaning during a repair — it kills the process and
   aborts the task — because `available_actions` cannot express "this cancel
   means something else right now", and a second meaning for one action decided
   by invisible context is worse than the extra keystroke.

   *Alternative rejected:* a `repairing` state. It would cost an FSM row, a
   board legend, slot rules and a recovery path — what task 014 paid for
   `awaiting_children` — to buy one nicer `cancel`.

3. **2026-08-24 — The repair run is a `step_run` row at the blocked step's index
   under the reserved step id `__repair`.** `step_index` is the blocked step's;
   `attempt` numbers repairs of that step independently, so a second repair is
   attempt 2 of the repair rather than attempt N+1 of the step.

   The retry-budget exclusion then falls out of code that already exists:
   `store.CountStepAttempts` keys on `(task_id, step_index, step_id, iteration)`,
   so a row under a different step id is invisible to the blocked step's budget
   without a single query changing. The id starts with an underscore, which no
   workflow step id may (§8.1), so it cannot collide with one somebody wrote.
   This is the mechanism the fan-out `on_conflict: agent` resolver already uses
   (`internal/taskrun/join.go`) — a synthetic step run through the ordinary
   executor with `inGroup: true` — and reusing it brings transcripts, events,
   token and cost accounting along for free.

   *Alternatives rejected:* a `kind` column on `step_runs`, and a separate
   `repair_runs` table. Both pay a migration and a second ledger for a
   separation the composite key already gives.

4. **2026-08-24 — The repair prompt carries a bounded failure block, assembled
   by the daemon.** Task title, description and fields; the blocked step's id,
   type and *rendered* prompt or command; the failure reason, exit code and
   check exit code; the result summary; the last 200 lines of the failed
   attempt's transcript (§8.4's existing bound, reused rather than invented);
   the absolute path of that transcript so the agent can read the rest itself;
   then the operator's prompt. The whole transcript is not inlined — it is
   capped at `transcript_max_bytes`, which is megabytes.

5. **2026-08-24 — The operator's prompt is literal text, not a `text/template`
   source.** It is prose typed at a form, and §8.4 renders with
   `missingkey=error`, so a `{{` in prose would fail the repair before the
   process started. It reaches the step escaped, through
   `workflow.EscapeTemplate` — extracted from `internal/workflow/builtin.go`,
   where task 024 needed it for the same reason.

   This differs deliberately from `edit + retry`, whose override goes into the
   task's snapshot and is therefore a workflow-authoring surface. A repair
   prompt is not.

6. **2026-08-24 — Agent/model/effort resolve request > task override > workflow
   `defaults` > adapter default.** §8.6's chain with the request standing in for
   the step level, which is what "the usual optional agent/model settings"
   means. The blocked step's own selection is deliberately not the base — a
   `command` step has none, and a repair is a different job from the step it is
   repairing.

7. **2026-08-24 — The synthetic step carries `max_retries: 0`.** A failed repair
   fails fast rather than silently paying for a second agent run. This is the
   built-in `adhoc` workflow's reasoning (phase 2 decision,
   `internal/workflow/builtin.go`) applied to the same shape of one-off run.
   Permission mode, `on_input` and timeout resolve through
   `internal/taskrun/resolve.go` unchanged, so the workflow's `defaults:` govern
   them and full-auto / `wait` / the agent timeout are the fallbacks.

8. **2026-08-24 — Every block reason offers repair; no filtering.** A task
   blocked before its worktree existed (`branch_exists`, `base_branch_missing`)
   re-enters `ensureWorktree` on the repair admission and re-blocks on the same
   reason without spawning an agent, which is the right outcome reached by
   existing code. A task blocked on `agent_unavailable` has its repair fail with
   `agent_unavailable`, which is honest.

   *Alternative rejected:* filtering `available_actions` by block reason. It
   would put a second, reason-shaped policy next to §6's state-shaped one for no
   behavioral gain.

9. **2026-08-24 — The request is drained by the re-block, not by the row
   insert.** `pending_override_json` drains at insert; `pending_repair_json`
   deliberately does not.

   The difference is load-bearing. Recovery (§12.4) finalizes a running row as
   `interrupted` and re-queues the task, and the actor then walks from
   `current_step`. If the request were already drained, a crash mid-repair would
   silently turn the repair into a plain retry of the blocked step — consuming
   its budget and possibly unblocking the task without the operator asking.
   Leaving the request set means an interrupted repair re-runs as a repair,
   which is exactly what §7.2 and §12.4 already promise for an interrupted step.
   Every *other* way out of `blocked` drops the request, because it describes
   exactly the block it was made about.

   A pause requested during a repair needs no handling: the repair's terminal
   state is `blocked` either way, and the transition clears the flag as every
   other human action does.

10. **2026-08-24 — The repair row is invisible to the rest of the workflow.**
    `blindTo` hides a `__repair` row from `.Steps` (§8.4) unconditionally, not
    only for group members. The existing same-index rule is conditioned on the
    *reading* step being `inGroup`, so an ordinary later step would otherwise
    see `__repair` in `.Steps` — a key no workflow author wrote, present exactly
    when somebody happened to press a key. A test pins this rather than leaving
    it to inference.

11. **2026-08-24 — `R` in the task-action scope, and a popup rather than a
    key that acts.** `r` and `E` are retry and edit+retry; `R` was free in that
    scope (its other binding is new-task re-probe, a different scope). A repair
    needs prose written for one task, which is also why it is excluded from bulk
    actions (task 011).

    The detail timeline needed its own change: it groups by `step_index` and
    prints a sub-step header only when the index is a `parallel` or `loop`
    group, so a repair row at an ordinary index would have rendered as a bare
    attempt line of the blocked step — the one thing this work must not say. It
    gets its own labelled tier, and it is excluded from the group detection that
    would otherwise make every repaired step read as a `parallel` group of
    itself.

12. **2026-08-24 — No CLI subcommand.** `internal/cli/task.go` exposes `add`,
    `ls`, `show` and `cancel`; retry, skip and approve are already TUI-and-API
    only, and repair follows them.

## Work

- [x] **025.1 — Store: migration `0010_repair.sql`, `RepairRequest`, the
  `TaskChange` field and its drain rules.** ✓ 2026-08-24
- [x] **025.2 — `taskstate`: the `Repair` action, `blocked → queued`, in
  `humanActions`.** ✓ 2026-08-24
- [x] **025.3 — `taskrun`: `repair.go` (the synthetic step, the prompt assembly,
  the re-block), `Runner.Repair`, and the `execute` branch.** ✓ 2026-08-24
- [x] **025.4 — API: `POST /v1/tasks/{id}/repair`, its DTO, its 400s and its
  agent/model/effort validation.** ✓ 2026-08-24
- [x] **025.5 — `apiclient`: `ActionRepair`, `RepairInput`, `Repair`,
  `RepairStepID`.** ✓ 2026-08-24
- [x] **025.6 — TUI: the `R` binding, `repairform.go`, and the timeline's own
  entry for a repair row.** ✓ 2026-08-24
- [x] **025.7 — Tests across `taskstate`, `store`, `taskrun`, `api`,
  `apiclient` and the TUI live harness.** ✓ 2026-08-24
- [x] **025.8 — Spec amendments, derived documentation, and an `m2` gate
  scenario.** ✓ 2026-08-24

## Verification

- `go test ./...` passes. The substance is in `internal/taskrun/repair_test.go`:
  a repair runs an agent in the task's existing worktree and its change is
  visible afterwards; the row carries `__repair` at the blocked index and a
  second repair is attempt 2 under that id; **the blocked step's budget is
  untouched** — after a repair, a `retry` gets the same number of attempts it
  would have got with no repair at all; the task returns to `blocked` at the
  same `current_step` with the same `block_reason` whether the repair succeeded
  or failed; a successful repair does not re-run the step; after a repair that
  fixed the underlying problem a `retry` passes the step and the workflow
  reaches `done`; an interrupted repair re-runs as a repair on restart and
  specifically does not re-run the blocked step; the repair row never appears in
  `.Steps` for a later step's prompt or guard; and a repair on a task with no
  worktree re-blocks on the original reason without starting an agent.
- `internal/store/repair_test.go` round-trips the request, proves it survives a
  `blocked → queued` transition, is cleared by the re-block and dropped by every
  other way out of `blocked`, and that `CountStepAttempts` returns the same
  numbers with repair rows present as without.
- `internal/api/repair_test.go` covers 409 with `details.state` from every
  non-blocked state, 400 on an empty prompt, `available_actions` carrying
  `repair` exactly while blocked, and agent/model/effort validation matching
  task creation's.
- `internal/tui/repairlive_test.go` runs against the real handlers over
  `httptest`: the action bar offers repair only when the daemon does, the form
  posts what was typed, and the timeline renders the repair row as its own
  labelled entry rather than an attempt of the blocked step.
- `VINCENT_GATE_SCENARIO=8 ./scripts/m2-gate.sh` drives the whole path against
  the fake agent: a task whose check fails blocks, a repair whose agent writes
  the file the check wants runs and lands as a separate `__repair` row, the task
  is `blocked` again with the same reason, and a `retry` then reaches `done`.
- `golangci-lint run ./...` is clean for `GOOS=windows` and `GOOS=darwin`. The
  `GOOS=linux` run crashes inside staticcheck's `buildir` while analyzing the
  standard library's `internal/poll` package; it does so on an unmodified
  checkout too (task 024 recorded the same), so it is a toolchain/analyzer
  incompatibility, not a finding against this change.
