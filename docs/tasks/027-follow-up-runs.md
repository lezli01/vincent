# 027 — Follow-up runs on a done or aborted task

**Status:** ✅ done (9/9)
**Opened:** 2026-08-25

A new human action, `follow_up`, valid from `done` and `aborted` — the two
unarchived non-terminal states, the pair `archive` is already scoped to. The
operator supplies what to run in one of three forms (an agent prompt, a shell
command, or a named workflow from the registry); the daemon runs it in the
task's *existing* worktree and branch; the runs land in the task's own ledger
with transcripts, events, and token and cost accounting. A follow-up is
repeatable any number of times before the task is archived.

The gap is the last mile of real work. `master` moved while the task ran and the
branch needs a rebase; a review comment wants one more commit; the agent left a
stray file that should go before the branch is opened as a PR. Every one of
those meant leaving vincent, finding the worktree by hand, doing the work
outside the daemon's ledger, and coming back only to press `archive`. Creating a
new task is not the workaround: task creation allocates a fresh worktree and a
fresh branch, and cannot reach the finished task's.

Nothing recorded forbids this. §20's future-work list does not mention post-`done`
work, `docs/history/v0-tasks.md` carries nothing binding on it, and task 025's
decisions are about `repair`'s scope rather than about the lifecycle's other end.
Two of 025's decisions are deliberately superseded below, each with its reasoning
recorded; the rest are followed.

## Decisions

1. **2026-08-25 — The follow-up run is not a step of the workflow snapshot.**
   The snapshot stays what §5.3 says it is — the workflow as authored, captured
   at creation and mutated only by `edit + retry`'s in-place override of an
   existing step. A follow-up appends nothing to it.

   *Alternative rejected:* the issue's own proposal, appending real steps so
   `current_step` advances past the original last index. It buys describability
   at the price of every reader of the snapshot: the task 017 graph, the
   `internal/api/snapcache.go` step totals and every "step k of n" a client
   renders would start showing operator-appended steps on a finished task, and
   repeat follow-ups would need an id-collision rule and a growth bound.

2. **2026-08-25 — A follow-up round writes its rows at
   `step_index = len(snapshot.Steps) + round - 1`.** The cursor space past the
   end of the snapshot is unused; a row there is unambiguously a follow-up row,
   and the round is legible from the index alone. Round 1 lands at `len(steps)`,
   round 2 at `len(steps)+1`, and so on.

   This is what makes full workflow fidelity affordable without a `step_runs`
   migration. Distinct rounds occupy distinct indices, so `CountStepAttempts`'s
   existing `(task_id, step_index, step_id, iteration)` key separates the rounds
   for free, `ListStepRunsAt` reads one round, and `iteration` keeps its loop
   meaning (§7.8) instead of being commandeered for the round number. Authored
   step ids are preserved rather than rewritten, so `if:` guards and `.Steps`
   references *inside* a follow-up workflow keep working.

   *Alternative rejected:* one reserved `__follow_up` step id at a single index,
   mirroring `__repair` (task 025 decision 3). It cannot describe a multi-step
   workflow, and numbering rounds by `iteration` collides with a `loop` inside a
   follow-up.

3. **2026-08-25 — All three run forms ship, and the workflow form is the general
   case.** The agent and command forms are compiled into a synthetic one-step
   workflow at request time, so `runFollowUp` has exactly one shape to execute.
   Every step type is allowed in a follow-up workflow — `agent`, `command`,
   `manual`, `parallel`, `fan_out`, `condition`, `loop`, `break` — and `include`
   is spliced at request time exactly as §7.9 splices it at creation.

   This was chosen over the cheaper flat agent/command-only form knowing the
   cost, which is recorded here rather than discovered later: `manual` and
   `fan_out` inside a follow-up both need machinery that currently keys on
   `current_step`, and decision 4 is what pays for them.

4. **2026-08-25 — A follow-up has its own cursor, persisted in the request.**
   The pending follow-up record carries the spliced follow-up workflow, the
   round, the origin state, and a step cursor into that workflow. `current_step`
   on the task is left where the finished run left it and is not used to walk a
   follow-up; the row index of decision 2 is derived from it plus the round.

   This is what makes `manual` and `fan_out` work inside a follow-up. A gate
   parks in `awaiting_gate` as usual, and `approve`/`skip` advance the
   *follow-up* cursor rather than `current_step`; a `fan_out` parks in
   `awaiting_children` and the join resumes at the follow-up cursor. Crash-first
   holds: the cursor is persisted as each step is finished with, so §12.4
   recovery re-runs an interrupted follow-up step as a follow-up step.

5. **2026-08-25 — A follow-up returns the task to the state it came from.**
   `done → done`, `aborted → aborted`. A follow-up decides nothing about the
   task's verdict — task 025 decision 1 applied at the other end of the
   lifecycle.

   *Alternative rejected:* the issue's proposal that a successful follow-up on
   an `aborted` task promotes it to `done`. It makes a human's abort reversible
   by any command that exits 0, and it is not needed: an operator who wants the
   verdict changed has `follow_up` and then nothing left to say — the task is
   already where they can archive it.

6. **2026-08-25 — A failed follow-up step blocks the task, and the resolution
   set is the existing one.** The task reaches `blocked` at the follow-up's
   index carrying that step's block reason. `retry` re-admits and re-runs the
   follow-up from its persisted cursor, because the request survives the block.
   `skip` marks the request abandoned but keeps the recorded origin, so the next
   admission restores `done` or `aborted` without running anything. `cancel`
   aborts and drains, as every other way out of `blocked` does (task 025
   decision 9's rule, with `retry` added to the list of transitions that keep
   the request). `repair` is extended to read a follow-up row as its failure
   context instead of no-oping — before this, `runRepair` warned and returned
   whenever `current_step` was past the end of the snapshot, which is every
   finished task and therefore every blocked follow-up.

   One consequence found while building it and recorded rather than papered
   over: `edit + retry` on a blocked follow-up is refused with a 400. An
   override rewrites the step it names *in the snapshot* (§5.3), and decision 1
   says a follow-up is not in the snapshot — so there is nothing there to
   rewrite. A plain `retry` is the action that means what the operator wants.

7. **2026-08-25 — One new engine action: `restore`, `running → aborted`.**
   Returning a done-origin task is `complete`, which already exists. Returning
   an aborted-origin one has no edge behind it — `cancel` is a human action and
   would misreport what happened — so §6 gains one engine row, used for nothing
   else.

8. **2026-08-25 — `done → aborted` becomes reachable, and §6 says so.** `cancel`
   during a running follow-up aborts the task, which was previously impossible
   from `done`. This is not a special case to suppress: the follow-up's process
   is live and `cancel` means what it always means (task 025 decision 2 —
   `available_actions` cannot express "this cancel means something else right
   now"). It is called out in the spec amendment because a client author reading
   the old table would not expect it.

9. **2026-08-25 — `.Steps` shows the original workflow's rows and the current
   round's own, and nothing else.** Rows at indices below `len(steps)` stay
   visible — a follow-up agent reading `.Steps.review.Output` is the point. Rows
   from *earlier* follow-up rounds are hidden, the way `__repair` rows are
   (task 025 decision 10): they are not steps anyone wrote into the workflow
   being run. Where a follow-up workflow reuses an id from the original
   workflow, the round's own row shadows it.

10. **2026-08-25 — Every block reason and every origin is offered a follow-up;
    no filtering.** An `aborted` task that never got a worktree — cancelled from
    `queued`, or blocked on `branch_exists` before abort — has `ensureWorktree`
    create the worktree and branch on the follow-up admission, or re-block on
    the same reason. That is the right outcome reached by code that already
    exists, and it is task 025 decision 8's reasoning unchanged.

    The one carve-out is a follow-up a human has already abandoned with `skip`:
    it restores the origin state *before* `ensureWorktree` runs, because a run
    with nothing left to do must not be able to block on creating a worktree it
    will never use.

11. **2026-08-25 — `vincent task follow-up <id>` ships, superseding task 025
    decision 12 for this action only.** That decision made repair TUI-and-API-only
    because retry, skip and approve already were. The reason to break with it is
    that "rebase these six finished branches onto current master" is a batch and
    a batch wants a command line, which is not true of any of the four actions
    025.12 covered. The unevenness this leaves — follow-up scriptable, retry and
    repair not — is accepted rather than papered over; filling the gap for every
    human action is a separate piece of work and out of scope here.

    *Superseded 2026-08-28 by [task 048](048-cli-human-actions.md)*, which is
    that separate piece of work: every §6 human action now has a subcommand, so
    the unevenness this decision accepted is gone. The reason recorded here
    stands for `follow-up` itself — its motivation was a batch — and 048's is
    different: a client that cannot unblock work is not a client (§2).

12. **2026-08-25 — Agent, model and effort resolve request > task override >
    workflow `defaults` > adapter default**, §8.6's chain with the request
    standing in for the step level, exactly as `repair` applies it. For the
    workflow form, an explicit step field in the follow-up workflow still wins
    over the request, because that is what §8.6 already says about a step field.
    The request is written into the steps that declare nothing of their own at
    request time, before the compiled document is re-validated, so what
    validates is what runs.

## Work

- [x] **027.1 — Store: migration `0012_follow_up.sql`, `FollowUpRequest`, the
  `TaskChange` field and its drain rules, `SetPendingFollowUp`, `MaxStepIndex`.**
  ✓ 2026-08-25
- [x] **027.2 — `taskstate`: the `FollowUp` human action from `done` and
  `aborted`, the `Restore` engine action, and the restated §6 table.**
  ✓ 2026-08-25
- [x] **027.3 — `taskrun`: `followup.go`, the `stepWalk` extraction that lets
  one walk serve both cursors, `Runner.FollowUp`, and the follow-up-aware
  `advance`/`Approve`/`Skip`/`Retry`.** ✓ 2026-08-25
- [x] **027.4 — `taskrun`: `repair.go` reads a follow-up row as its failure
  context, and `blindTo`/`precedes` learn decision 9's rule.** ✓ 2026-08-25
- [x] **027.5 — API: `POST /v1/tasks/{id}/follow_up`, its DTO, its compile and
  splice path, and its 400s and 409s.** ✓ 2026-08-25
- [x] **027.6 — `apiclient`: `ActionFollowUp`, `FollowUpInput`, `FollowUp`.**
  ✓ 2026-08-25
- [x] **027.7 — TUI: the `F` binding, `followupform.go`, and the timeline's own
  tier for a follow-up round.** ✓ 2026-08-25
- [x] **027.8 — CLI: `vincent task follow-up <id>` with its three mutually
  exclusive run forms.** ✓ 2026-08-25
- [x] **027.9 — Tests across `taskstate`, `store`, `taskrun`, `api`, the TUI
  live harness and the CLI e2e binary; spec amendments, derived documentation,
  and an `m2` gate scenario.** ✓ 2026-08-25

## Notes taken while building

- **The migration is `0012`, not the brief's `0011`.** `0011_agent_quota.sql`
  (task 026) landed first. Migrations are append-only, so the number is
  whichever is free.
- **The gate scenario is `m2` scenario 10, not the brief's 9.** Scenario 9 is
  task 025's repair walkthrough.
- **`execute`'s step walk was extracted rather than copied.** The ordinary
  admission and the follow-up differ in exactly three things — where the walk
  starts, where its rows go, and what ending it reaches — so `stepWalk` carries
  those three as fields and `runSteps` is written once. Duplicating ninety lines
  of guard, gate, group, loop, fan-out, retry, pause and interruption handling
  is how the two would have drifted.
- **The sweep decision 4's risk note asked for turned up no sixth `CurrentStep`
  reader.** `advance`, `Approve`, `Skip`, the fan-out join and §12.4 recovery
  were the five, and of those, recovery needed no change at all: it re-queues
  through `Interrupt`, which is not a settled state, so the request survives and
  the next admission is a follow-up admission by construction. `recordStepDecision`
  was the one that had to learn the follow-up position, because a `skip` on a
  blocked follow-up would otherwise have written its row against whatever the
  snapshot holds at `current_step` — which for a finished task is nothing.
- **A `fan_out` inside a follow-up shares one row index with the rest of its
  round.** That is decision 2 working as intended, and it means a follow-up
  workflow with *two* `fan_out` steps in one round cannot tell its lanes apart:
  `tasks.parent_step_index` is an index, and both steps have the same one. It is
  a pre-existing shape of that column rather than something this work
  introduced — a `loop` containing a `fan_out` has the same property — and it is
  left as it is rather than migrated for a case nobody has asked for.

## Verification

- `go test ./...` passes. The substance is in `internal/taskrun/followup_test.go`:
  an agent follow-up changes the finished task's existing worktree and the
  change is visible afterwards; a command follow-up runs under the daemon's
  shell (§8.3) with no agent; a workflow follow-up runs every step through a
  `manual` gate that parks and is approved, and a `loop` whose iterations keep
  their §7.8 numbering; a `fan_out` inside a follow-up spawns lanes off the
  finished task's branch, parks the parent, and joins on the follow-up's cursor.
  Rows land at `len(steps)+round-1` with authored step ids, **the original
  workflow's rows are untouched**, and a second follow-up is round 2 rather than
  attempt 2 of round 1. A done task returns to `done` and an aborted task
  returns to `aborted`, on success and on `skip`-after-block alike. A failed
  follow-up step blocks at its own index; `retry` there re-runs the follow-up
  and specifically does not complete the task; `edit + retry` there is refused;
  `repair` there reads the follow-up row's failure rather than no-oping. An
  interrupted follow-up re-runs as a follow-up from its persisted cursor after
  recovery, as the same round rather than a new one. Earlier rounds' rows are
  absent from a later round's `.Steps`.
- `internal/store/followup_test.go` round-trips the request, proves it survives
  the `fail` that blocks a follow-up step and the `retry` that re-runs it, that
  every transition into a settled state drains it, that the cursor is written
  without a transition, and that `MaxStepIndex` reports what numbers a round.
- `internal/api/followup_test.go` covers 409 with `details.state` from every
  non-`done`, non-`aborted` state; 400 on no run form, a blank one and two at
  once; 400 on an unknown workflow name and an unregistered agent, each naming
  its cause; 404 for a task that does not exist; the registry form's compile and
  splice; and `available_actions` carrying `follow_up` exactly on those two
  states.
- `internal/tui/followuplive_test.go` runs against the real handlers over
  `httptest`: the action bar offers follow-up only when the daemon does, `F`
  opens the form, what was typed is what the daemon stores, each of the three
  run forms posts its own field and no other, and the timeline renders a
  follow-up row as `↳ follow-up 1` rather than as an attempt of a workflow step.
- `internal/cli/commands_e2e_test.go` drives `vincent task follow-up` through
  the real binary against a real daemon: the three run flags are mutually
  exclusive and one is required, an unknown workflow is exit 1 naming the
  workflow, `--prompt` carries `--agent` to the daemon and an unregistered one
  is exit 1 naming the agent, and `--run` against an `aborted` task queues the
  follow-up. Only one form is driven to a successful queue, because a second
  would race the first follow-up's admission; the other two forms' success
  paths are proven in `internal/api` and `internal/taskrun`.
- `VINCENT_GATE_SCENARIO=10 ./scripts/m2-gate.sh` drives the whole path against
  the fake agent: a task reaches `done`, a follow-up commits on that task's own
  branch and lands as its own row past the snapshot's last index without
  `step_total` growing, a second follow-up is round 2 at the next index, and
  `follow_up` from `blocked` is a 409 carrying `details.state`.
- `golangci-lint run ./...` is clean for `GOOS=windows`, `GOOS=darwin` and
  `GOOS=linux`.
