# 137 — The handoff pillar: delivering finished issues, unattended

**Status:** 🔄 in progress (0/17)

Issue [#812](https://github.com/lezli01/vincent/issues/812), part of
[#811](https://github.com/lezli01/vincent/issues/811), the epic. Spec §3 (a
new row, and dated notes on rows 11, 27 and 36), §5.6, §7, §8, §12, §13, §15
and §18 in the pull request of each item whose code makes them true — this
document changes no code and amends no spec section.

## What this is

The third pillar of the three-pillar architecture
(<https://blog.lezli01.is-a.dev/blog/vincent-three-pillars/>):
**Issues → Work → Handoff.** Task 130 built intake and named this pillar as
the future owner of "posting comments to GitHub, closing an issue when its
work is done, merging" (`130-issues-pillar.md`, "What this is", and decision
10). Task 134 built the `hand-off` lane, where finished work on an open issue
waits, and nothing drains it today except a human closing the issue.

The requirement, as the author stated it on 2026-10-23:

> Basically the handoff will be responsible for things like open pr, merge,
> review, close issue, send it back to in progress with details signaling
> needing more work and whatever the administrator configures vincent for,
> what their SDLC means handoff for their case. I would imagine a simple SDLC
> where vincent periodically takes N number of issues for example using a
> trigger and starts tasks on them, when those tasks are done those issues are
> moved to handoff, handoff periodically or by trigger takes M number of
> handed off issues which require merging creates a train from them opens a
> pr and handles the review, fixing and merging of the train. And this cycle
> would go automatically, user would only be needed if a task asks a question
> or if there is some fault or error that requires user interaction but
> otherwise if an issue is opened and is well defined the manager can expect
> it to be done and be part of the product eventually.

The loop it describes, and what this document adds to each arrow:

```
intake trigger ──► N work tasks ──► issues reach hand_off ──► delivery trigger ──► one train task over M issues
  (137.10)            (exists)            (exists, 134)            (137.10)                (137.5)
                                                                                              │
     assemble (137.6) ► verify ► open PR (137.8) ► wait for CI/review (137.2–137.4) ► fix ► gate ► merge ► close (137.9)
                                                                                              │
          ◄──────── eject a car: feedback + a follow-up work task on the issue (137.7) ◄──────┘
```

The pillar is **policy, not a feature** (decision 1). vincent supplies the
primitives: a task that delivers several issues, a step that waits on the
world, a step that assembles a train, delivery writes a workflow may be
licensed to make, issue selection in triggers, and a breaker. It also ships one
SDLC built from them as built-ins and a preset (137.12, 137.14). An
administrator whose process differs edits YAML. They do not wait for a release.

## What this reverses

Every binding record this work overturns, narrows or keeps. Each keeps
governing the code until the named item's pull request merges, and that pull
request writes the dated note.

- **Spec §3 row 11** (Delivery, rewritten 2026-09-15 by 068.4): "Nothing on
  the step path reaches GitHub: no step type, no default workflow and no
  automatic behaviour delivers… A `merge` step type is not licensed by this
  row." — **superseded in part** by decision 13, in 137.8. The step path may
  deliver, but only when the workflow declares the write and the project has
  turned handoff on. Without both, the row stands as written. Its "the push
  never forces" half is **kept** (decision 7).
- **Spec §3 row 27** (GitHub pull requests): every PR write is "excluded from
  MCP" — **narrowed** by decision 13, in 137.8. Writes are exposed to the
  per-step MCP endpoint of a permitting workflow, and nowhere else. "A
  pointer, not a snapshot" is **kept** (decision 12).
- **Spec §3 row 36** and **task 130 decision 10**: "an MCP- or
  step-originated [state change] is refused (`forge_write_needs_human`) and
  nothing closes an issue automatically" — **superseded in part** by
  decisions 13 and 15, in 137.8 and 137.9. A permitting workflow's step may
  close and reopen an imported issue, and the outbox writes that back. 130
  decision 10 deferred exactly this to "a future handoff pillar".
- **Task 130 decision 5** (a task links one issue: a pointer **and** a
  snapshot) — **kept** for work tasks. A delivery task links its issues
  through a second relation (decision 3), never through `tasks.issue_id`.
- **Task 134 decision 2** (lane derivation from root tasks linked by
  `issue_id`) — **kept**. Delivery membership never moves a lane (decision 4).
- **Spec §3 row 33 / task 096** (one event, at most one action) — **kept**.
  Issue selection (decision 16) makes one action from one event, and that
  action may create several tasks or one task over several issues.
- **Loop bodies refuse anything that ends the actor goroutine**
  (`workflow.go` `validateBodyStep`, task 016) — **kept**. `wait` does not
  end the actor through a new state. It holds in `queued` the way
  `retry_backoff` does (decision 10). 137.2 must prove the loop position
  survives that, or narrow `wait` out of loop bodies.

## Decisions

Recorded 2026-10-23. Decisions 3, 5, 11 and 14 are the author's answers to
four questions asked that day, and are written in as decisions.

### 1. The pillar is policy; vincent ships primitives and one preset (2026-10-23)

The author: "whatever the administrator configures vincent for, what their
SDLC means handoff for their case." Every delivery behaviour is a workflow
step or a trigger action, so row 11's founding idea (delivery belongs to
workflows) survives in spirit even where its letter is amended. The built-in
SDLC is two triggers and a built-in workflow (137.12, 137.14), held to the
same bar as every other built-in (task 037 decision 5).

*Alternative beaten:* a hardcoded delivery engine with a fixed state machine
(PR → review → merge). One process fits few teams, and it would be a fourth
lifecycle vocabulary beside `taskstate`, `chatstate` and `issuestate`.

### 2. Vocabulary: "handoff" is the pillar, `delivery` is the code word (2026-10-23)

`hand_off` already names two things: the chat action and state (task 074,
`chatstate.HandOff`, `POST /v1/chats/{id}/handoff`, `vincent chat handoff`)
and the issue lane (`issuestate.LaneHandOff`, `?lane=hand_off`). A third
meaning in an identifier would be a trap. Prose and docs call the pillar
**handoff**. Identifiers, routes, config keys and reasons say **delivery**:
a *delivery task*, its *cars*, `issue_ids`, `handoff.*` only as the config
section name. A **train** is the built-in delivery workflow's name for a
delivery task that merges cars into one branch.

### 3. A train is a task over many issues (2026-10-23, author)

A new relation `task_issues(task_id, issue_id, position, status, …)` links
a delivery task to its cars. `status` is `pending | merged | ejected |
delivered`. A task has `issue_id` (a work task, task 130) **or** delivery
issues, never both. The delivery task is an ordinary task: it gets the
actor, recovery, gates, questions, transcripts, the board and notify with no
new machinery. `.Issues` (a list of `.Issue`-shaped contexts plus each car's
status and main branch) and `$VINCENT_ISSUES_FILE` carry the cars to steps.

*Alternative beaten (author):* a `deliveries` entity with its own FSM owning
a task. It is cleaner vocabulary, but costs a fourth lifecycle, a second
recovery story and another board.

### 4. Delivery membership never moves a lane; it is shown beside it (2026-10-23)

**Only a `hand_off` issue may be a car**, on every surface and with no
override (author, 2026-10-23): an `open`, `in_progress` or `done` issue is
refused with `issue_not_handed_off`, even when named by id.

The lane SQL (`store/issuelane.go`) reads root tasks linked by
`tasks.issue_id` only, so a car stays `hand_off` while its train runs. The
issue DTO gains `delivery: {task_id, status}` for the unsettled train that
holds it, and the board shows it on the card. An ejected car returns to
`in_progress` only because decision 8 creates a work task on it, and the
lane derives that, as it already does.

### 5. One pull request per train (2026-10-23, author)

The delivery task's own branch is the train branch, cut from the base like
any task's. Every car's issue main branch is merged into it (decision 6),
and one PR from it is opened, reviewed and merged. That PR is opened through
069's existing create path.

*Alternatives beaten (author):* one PR per car, merged in order (M reviews
and M CI runs per train), or both (the cost of two models).

### 6. `merge_issues` assembles the train from the issues' main branches (2026-10-23)

A step type that merges each `pending` car's issue main branch (resolved as
task 134 resolves it: the lowest-id unarchived main-role task's branch) into
the task worktree `--no-ff`, in car order. It reuses merge-back's machinery
(`taskrun/mergeback.go`: `Worktrees.MergeBranch`, `resumeMerge`,
`handleConflict`), which already merges arbitrary branches. It uses its own
merge message, never `MergeLane`'s, which `diff?by=lane` parses.
`on_conflict: block | agent | eject`. `merge.agent` is required for `agent`
as in §7.6. `eject` ejects the conflicting car (decision 7) and continues. A
task with no cars blocks with `no_delivery_issues`.

### 7. Ejecting a car reverts, never rewrites (2026-10-23)

Removing a car from a train whose branch is already pushed reverts that
car's merge commit (`git revert -m 1`) and pushes normally. Row 11's "the
push never forces" is kept, and a reviewer's view of the PR history stays
honest. A car not yet merged is just marked `ejected`. Ejection is one
action: `POST /v1/tasks/{id}/issues/{issue_id}/eject` with a feedback body.
It is reachable from a human (TUI, CLI), from a step (CLI) and from an agent
(MCP), and from `merge_issues` itself.

*Alternative beaten:* rebuilding the train without the car and
force-pushing. That loses the review history and amends row 11 a second
time.

### 8. Send-back is structured feedback plus a follow-up work task (2026-10-23)

An ejection writes an `issue_feedback` row: issue, train task, `kind`
(`conflict | checks | review | human | agent`), body, time. It then creates
a main-role work task on the issue. That task continues the issue's main
branch and uses the workflow the issue's last work task ran unless the
delivery config names one. Its context carries `.Issue.Feedback`, oldest
first. The lane moves to `in_progress` by derivation, and the intake loop is
closed.

Feedback is not an issue comment. Comments are free text, carry no kind,
and are refused on a mirrored GitHub issue (`issue_mirrored`). A structured
row needs neither of those to change.

### 9. The return budget makes repeated failure a human's problem (2026-10-23)

`handoff.max_returns` (default 3) caps the feedback rows an open issue may
collect. At the cap, no follow-up task is created, and the issue's derived
`attention` becomes true with reason `returns_exhausted`. That is derived
from the row count like every other lane value (task 130 decision 3, kept),
never stored. It stops an unattended loop spending forever on an issue that
is not well defined.

### 10. `wait` holds in `queued`, not in a new state (2026-10-23)

A `wait` step evaluates `until:` (a template, rendered with
`missingkey=error` and judged exactly `true`/`false` like `if:`, §7.7)
against a freshly built context. If it is true, the step succeeds. If not,
the task is held the way `retry_backoff` (task 028) and the usage-limit hold
(task 003) hold it: `taskstate.Interrupt` to `queued`, `queued_reason:
waiting`, `admit_not_before = now + poll`. The slot is released, the task
stays pausable and cancellable, and recovery needs nothing new. `timeout:`
(measured from the step's first evaluation) and `on_timeout: block | fail |
continue` bound it. A hold does not consume a retry and does not write an
attempt row per poll.

*Alternatives beaten:* a new `awaiting_external` state, which about 25 files
enumerate (TUI, notify, MCP, CLI, store) and which would need its own
recovery and resume path; and an `awaiting_children`-style park resumed by
the scheduler, which ends the actor and so cannot live in a loop body. The
review cycle (wait → fix → wait) is a loop.

### 11. A poll floor, and events that cut it short (2026-10-23, author)

`poll:` (default 60 s, minimum 10 s) is the floor, and it works with no
webhook. Anything that may have changed the answer clears the hold's
`admit_not_before` early: the PR reconciler noticing a changed head, checks
or review state (137.4), and `POST /v1/tasks/{id}/wake`. That route is what
an `http` trigger fed by a GitHub webhook calls, and the scheduler's tick
admits the task within 5 s.

*Alternatives beaten (author):* polling only (latency is the interval);
webhooks required (a local-first daemon rarely has a public endpoint).

### 12. The review read model is live, never stored (2026-10-23)

`internal/github` learns the review decision, reviews (author, state, body),
unresolved review threads (path, line, comments), the mergeable state and
the check rollup it already reads. They are rendered as `.Pull` in a delivery
or work task's context and served by `GET /v1/tasks/{id}/github/pull/reviews`.
They are read at render time, like `GET …/pull/checks`, so row 27's "a
pointer, not a snapshot" holds. `cmd/fakegh` grows reviews, threads and a
review-submission scenario.

### 13. Delivery writes are licensed per workflow and per project (2026-10-23)

A step-originated write (the step marker header, or the per-step MCP
endpoint) to a PR or to an imported issue's state is allowed only when:

1. the project's `handoff.enabled` is true (default false), **and**
2. the task's workflow declares the write in `delivery.permits:` — any of
   `pull.create`, `pull.merge`, `pull.comment`, `pull.rerun`,
   `pull.close`, `issue.close`, `issue.reopen`, `issue.comment`.

Such a write is attributed `agent`, author `task N`. Anything else is refused
with `delivery_not_permitted`. That **tightens** today's PR routes, which do
not read the marker header at all. A step's `vincent github pr merge`
succeeds today unchecked, and after 137.8 it needs a license. The marker
header remains best-effort, not a privilege boundary: the agent's own
`gh`/`git push` path in its full-auto worktree stays open as row 11 already
says. The license governs what *vincent* does on an agent's behalf.

### 14. Merging waits for a human unless the project opts out (2026-10-23, author)

The built-in train ends at a `manual` gate before `pull.merge`.
`handoff.auto_merge: true` skips the gate through its `if:`. Branch
protection and required checks still apply either way, and the merge stays
pinned to the confirmed head SHA (068.4's preflight).

*Alternative beaten (author):* auto-merge by default.

### 15. Closing records a resolution; imported issues hear about it (2026-10-23)

`issue close` gains `resolution` (text, optional URL), stored on the issue
and shown everywhere a close reason is. A delivered car is closed
`completed` with "Delivered in <PR URL> (<merge SHA>)". A work task that
found nothing to change closes its issue the same way, with its evidence,
which covers "closing issues which did not require code change". On an
imported issue, the outbox writes the state and, new, posts the resolution
as a GitHub comment: the first issue-content write vincent makes, licensed
by `issue.comment` (decision 13).

### 16. Triggers select issues; one action may create many tasks or one train (2026-10-23)

`create_task` gains `select_issues: {project, lane, labels, kind,
priority, limit, order}` and `per: issue | batch`.

- `per: issue` creates one work task per selected issue. That is intake:
  "take N".
- `per: batch` creates one delivery task over all of them. That is the
  train: "take M".

Claimed issues (in an unsettled train) and issues with an unsettled root task
are skipped. Each created task's dedupe key derives from its issue(s). Dry
runs (`trigger_test`) print the selection. A `schedule` source is the
intended pairing, but any source may carry it.

**"Ready" is the administrator's filter, not a vincent state.** The preset
uses a `ready` label.

### 17. A breaker stops an unattended loop that keeps failing (2026-10-23)

A trigger gains `breaker: {after: K}`. After K consecutive tasks it created
end `blocked` or `aborted` (or, for a delivery trigger, eject every car),
the trigger disarms itself, records `trigger.tripped`, and notify can carry
it (`notify.on` gains `trigger_tripped`). `limits.max_in_flight` caps
unsettled tasks created by one trigger. Together with decision 9 these are
the "fault or error that requires user interaction" the author wants
surfaced instead of retried forever.

### 18. One place lists everything a human owes (2026-10-23)

`GET /v1/attention` (CLI `vincent attention`, MCP, a TUI view) lists:

- tasks in `awaiting_input`, `awaiting_gate` or `blocked`
- chats waiting
- issues with `attention`, including `returns_exhausted`
- tripped triggers
- trains at their merge gate

In unattended operation it is the only screen the author needs: "user would
only be needed if a task asks a question or if there is some fault".

### 19. Delivered work is cleaned up (2026-10-23)

After a car is `delivered`:

- its issue's main and side worktrees are reclaimed
- the issue's local branches are deleted (task 008's rule)
- its settled work tasks are archived

The train task is archived once every car is settled. `handoff.cleanup:
false` keeps everything.

## Non-goals

- **Speculative or bisecting trains, and GitHub merge-queue delegation.** A
  train is sequential: assemble, verify, one PR. A later task may add either
  over the same `task_issues` relation.
- **Issue dependencies and train ordering by them.** Car order is
  `select_issues.order` (priority, then age). `parent_issue_id` stays unused.
- **Local delivery for projects without GitHub.** The primitives work
  (`merge_issues`, `wait` on a command, gates), but no built-in merges into
  the base branch without a PR.
- **Automatic triage of "well defined".** An optional built-in that asks
  clarifying questions on an issue is a natural follow-on, not part of this.
- **Several PRs per train.** Decision 5.

## Risks

- **Unattended spend.** Mitigated by decisions 9 and 17,
  `limits.max_task_cost_usd` (task 033) and `on_fire: propose` in the preset
  (it ships disarmed).
- **A review loop that never converges** (a bot reviewer asking for changes
  forever). The built-in's review loop has `max_iterations`, and exhausting
  it blocks.
- **Flaky CI ejecting good cars.** The built-in re-runs failed jobs once
  (`pull.rerun`) before attributing a failure. Attributing a failing check to
  a car is a heuristic (the agent reads the logs). A wrong attribution costs
  one return, not the issue.
- **The `wait` hold's interaction with loop positions and pause** (decision
  10). 137.2 proves it with tests or narrows `wait` out of loop bodies before
  the built-in relies on it.
- **Tightening PR routes** (decision 13) could break a workflow that today
  merges through `vincent github pr merge` from a step. The 137.8 release
  note names the license that restores it.

## Open questions

1. **In-progress issues in a train.** *Settled 2026-10-23 by the author:*
   never, not even by explicit id. Only an open issue in the `hand_off` lane
   may be a car; anything else is refused with `issue_not_handed_off`
   (decision 4).
2. Should the attention view be a fourth main TUI tab (`tui.tabs`) or a
   takeover? 137.15 decides with screenshots.

## Tasks

In delivery order. Three tracks can proceed in parallel after 137.1:

- **waiting:** 137.2 → 137.3 → 137.4
- **trains:** 137.5 → 137.6 → 137.7
- **licensing:** 137.8 → 137.9

Each item amends the spec sections and public pages its code makes true, in
its own pull request.

- [~] **137.1** ([#812](https://github.com/lezli01/vincent/issues/812)) This document and its index row.
- [ ] **137.2** ([#813](https://github.com/lezli01/vincent/issues/813)) The `wait` step type (decisions 10, 11): workflow constant,
  validator, schema descriptor, the `queued`/`waiting` hold, `until`,
  `poll`, `timeout`, `on_timeout`, `POST /v1/tasks/{id}/wake` (API,
  apiclient, CLI, MCP), the `waiting` queued reason in `internal/reasons` and
  the lifecycle page, the skill and the `update-workflows` checklist line, and
  tests proving the hold inside a loop body, across pause and across
  recovery. Spec §7, §12.4, §13.2.
- [ ] **137.3** ([#814](https://github.com/lezli01/vincent/issues/814)) The review read model and `.Pull` (decision 12): review
  decision, reviews, unresolved threads, the mergeable state in
  `internal/github`; `cmd/fakegh` reviews and threads; `GET
  …/github/pull/reviews` (API, apiclient, CLI `vincent github pr reviews`,
  MCP); `.Pull` in the render context; skill and checklist lines.
- [ ] **137.4** ([#815](https://github.com/lezli01/vincent/issues/815)) Event wake (decision 11): the reconciler notices head,
  check and review changes on linked PRs, emits `task.github_pull_state_changed`,
  and clears the `waiting` hold of the linked task. Depends: 137.2, 137.3.
- [ ] **137.5** ([#816](https://github.com/lezli01/vincent/issues/816)) Delivery tasks (decisions 3, 4): migration `task_issues`;
  `issue_ids` on `POST /v1/tasks` (API, apiclient, CLI `--issues`, MCP),
  exclusive with `issue_id`; the claim (`issue_claimed`,
  `issue_not_handed_off`) in the create transaction; `.Issues` and
  `$VINCENT_ISSUES_FILE`; the task DTO's cars and the issue DTO's
  `delivery`; a new spec §3 row for the pillar.
- [ ] **137.6** ([#817](https://github.com/lezli01/vincent/issues/817)) The `merge_issues` step type (decision 6), with its own
  merge message and the car `merged` status. Depends: 137.5.
- [ ] **137.7** ([#818](https://github.com/lezli01/vincent/issues/818)) Eject and send-back (decisions 7, 8, 9): migration
  `issue_feedback`; the eject route (API, apiclient, CLI, MCP); revert on a
  pushed train; the follow-up work task; `.Issue.Feedback`;
  `handoff.max_returns` and the `returns_exhausted` attention reason.
  Depends: 137.5, 137.6.
- [ ] **137.8** ([#819](https://github.com/lezli01/vincent/issues/819)) Delivery licensing (decision 13): `handoff.enabled` config,
  `delivery.permits` in the workflow schema, the guard on every PR write
  route and the issue state routes, the per-step MCP exposure,
  attribution, `delivery_not_permitted`; dated notes on spec §3 rows 11, 27
  and 36 and task 130 decision 10.
- [ ] **137.9** ([#820](https://github.com/lezli01/vincent/issues/820)) Resolutions (decision 15): migration for `issues.resolution`,
  close with a resolution on every surface, and the GitHub comment write
  through the outbox. Depends: 137.8.
- [ ] **137.10** ([#821](https://github.com/lezli01/vincent/issues/821)) Issue selection in triggers (decision 16): `select_issues`
  and `per`, the skip rules, dedupe, the dry run; the `vincent-triggers`
  skill and the `update-triggers` checklist line. Depends: 137.5.
- [ ] **137.11** ([#822](https://github.com/lezli01/vincent/issues/822)) The breaker and `max_in_flight` (decision 17): trigger
  schema, disarm, `trigger.tripped`, `notify.on: trigger_tripped`.
- [ ] **137.12** ([#823](https://github.com/lezli01/vincent/issues/823)) The built-in delivery workflow `deliver-train` (decisions 5,
  14):
  - steps: `merge_issues` → verify → self-review → open PR → a review loop
    (`wait` → re-run flaky jobs → address review → push) → merge gate → merge
    → close cars with resolutions
  - the `update-workflows` checklist
  - a re-read of all five built-in sources per task 037 decision 5

  Depends: 137.2–137.9.
- [ ] **137.13** ([#824](https://github.com/lezli01/vincent/issues/824)) Cleanup after delivery (decision 19), with
  `handoff.cleanup`. Depends: 137.7.
- [ ] **137.14** ([#825](https://github.com/lezli01/vincent/issues/825)) The SDLC preset: `vincent handoff init --project P`. It
  writes an intake trigger and a delivery trigger (disarmed, `on_fire:
  propose`) and enables `handoff` for the project. It also adds the guide
  `docs/guides/unattended-sdlc.md` and `docs/features.md`. Depends: 137.10,
  137.11, 137.12.
- [ ] **137.15** ([#826](https://github.com/lezli01/vincent/issues/826)) The attention list (decision 18): API, apiclient, CLI, MCP,
  TUI view; answers open question 2. Depends: 137.7, 137.11.
- [ ] **137.16** ([#827](https://github.com/lezli01/vincent/issues/827)) Trains in the TUI: a Cars tab on the task workspace (status,
  feedback, eject), the delivery marker on issue cards, feedback on the issue
  screen; screenshots via `scripts/screenshots.sh`. Depends: 137.5, 137.7.
- [ ] **137.17** ([#828](https://github.com/lezli01/vincent/issues/828)) `scripts/137-gate.sh` drives the whole loop end to end with
  the fake agent and fake gh:
  - intake
  - work
  - hand-off
  - train
  - a review requesting changes
  - an ejected car returning
  - a merge gate
  - merge, close and cleanup

  Wired into `ci.yml`'s lightest gate group. Depends: 137.14.
