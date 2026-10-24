# 137 — The handoff pillar: what becomes of an issue once its work is done

**Status:** 🔄 in progress (5/15)

Issue [#834](https://github.com/lezli01/vincent/issues/834), part of
[#833](https://github.com/lezli01/vincent/issues/833), the epic. Spec §3 (a
new row 38, and dated notes on rows 11, 33 and 36) in this document's pull
request, by the same exception task 130 decision 13 and task 132 decision 14
took (open question 5, settled); §5, §8, §12, §13, §15 and §16 in the pull
request of each item whose code makes them true.

## What this is

Vincent is meant to be a frame an admin builds their own AI-assisted
development lifecycle in, not one lifecycle hard-coded. The three-pillar
architecture (<https://blog.lezli01.is-a.dev/blog/vincent-three-pillars/>,
2026-09-30) names the parts: **intake**, where work enters; the **workshop**,
where it is done; and the **handoff**, where the result passes back to
someone else — "a pull request waiting for review and a ticket comment waiting
for its reporter equally well".

Two of the three exist:

| Pillar | Built by | What vincent owns |
|---|---|---|
| Intake | task 130, task 134 | vincent-owned issues, GitHub import and state sync, the four derived lanes, `type: issues` triggers, `issue:` on `create_task` |
| Workshop | v0 through task 133 | tasks, workflows, worktrees, agents, checks, gates, fan-out, merge-back |
| Handoff | **nothing** | — |

The gap is recorded in three places. Task 130 decision 10: "Nothing closes an
issue automatically — closing on task `done` belongs to the handoff pillar."
Task 134's non-goals: "posting comments, auto-closing on `done`, opening pull
requests. These belong to the handoff pillar." And task 134's risks: "The
`hand_off` lane only grows until someone closes the issue."

This document plans the handoff pillar and lists what else the frame is
missing. It is the plan and the decision record; it changes no code. The
author settled the questions that shaped it on 2026-10-24 — the grant, the
lanes, the pull-request guard, the spec's timing, MCP writes, agent-started
handoffs, where returned work runs, the name and the order of the frame gaps —
and each is written in as a decision below. The plan's own proposals —
decisions 1, 2, 3, 7, 9, 10 and 12 — were accepted as written the same day, so
every decision is settled.

### What handoff looks like today

The only automated handoff in the repository is a workflow tail.
`github-resolve-issue.yaml` ends at a **delivered branch**: its `deliver` step
writes a file convention, `<git common dir>/vincent-delivered/<task id>/`
(`branch`, `head.sha`, `pr-title.txt`, `pr.md`, `brief.md`, …), and the task is
`done`. `github-iterate-work.yaml` then reads those files, stacks the branches
into one merge train, verifies it, opens one pull request, reviews it, resolves
the findings and merges it — all in bash, all through `gh` called from steps
(spec §3 row 11's "agent's own path"). The issues close because the pull
request body carries `Closes #N` and the importer later reads GitHub's close.

That works, and it is the requirement's own example. What vincent cannot do
with it:

- **See it.** A handoff in flight is invisible: the issue sits in `hand_off`
  whether nobody is handing it off or a train is mid-merge.
- **Stop two handoffs taking one issue.** Nothing claims an issue.
- **Record the outcome.** "Delivered", "sent back for rework" and "waiting on
  the reporter" all look the same until GitHub's close arrives — and a
  needs-info answer, the blog's dashed path, ends as nothing at all.
- **Write through its own path.** A step's `vincent issue close` on an
  imported issue is refused `forge_write_needs_human` (task 130 decision 10),
  so automation goes round vincent with `gh`: no outbox, no crash safety, no
  attribution, and the close comes back as an untrusted `sync` event.
- **Offer it to anyone else.** The delivery record is a private contract
  between two workflow files; a third workflow has to re-read 2,000 lines of
  YAML to learn it.

### What the pillar adds, in one paragraph

A **delivery** becomes a first-class record a workshop task leaves behind. A
**handoff** becomes a first-class record that claims a set of `hand_off`
issues and is executed by an ordinary task running a handoff workflow. The
workflow records, per issue, what became of it — **delivered**, **returned**
to the workshop, **waiting** on someone outside, or **released** — and those
outcomes drive the issue's close, its comment and its lane. Automated writes
to GitHub are allowed through vincent's own routes only under a **grant** a
human gave, by a keypress or by arming a trigger. Everything a handoff
actually *does* — stacking, reviewing, opening pull requests, sending mail,
deploying — stays in the admin's workflow. Vincent owns the state; the admin
owns the behaviour.

## The requirement's example, end to end

The author's example, mapped onto what exists and what this plan adds. The
YAML is proposed syntax.

1. **Intake.** vincent syncs GitHub issues (exists, task 130.8).
2. **Start.** A trigger decides which issues may start (exists, task 130.15):

   ```yaml
   # {config_dir}/triggers/start-ready.yaml
   id: start-ready
   enabled: true
   source: {type: issues, project: 1}
   match: {action: labeled, labels: ready, by: [human, sync]}
   action:
     type: create_task
     workflow: github-resolve-issue
     title: '{{ .Event.Issue.Title }}'
     issue: '{{ .Event.issue_id }}'
   on_fire: create
   dedupe_key: 'issue:{{ .Event.issue_id }}:ready'
   ```

   The issue moves to `in_progress` (exists, task 134).
3. **Workshop.** The resolve workflow's last step runs `vincent task deliver`
   instead of writing `vincent-delivered/` (new, 137.2). The task ends `done`
   and, the workflow being `hand_off: auto`, the issue moves to `hand_off`,
   its card showing the delivery (new). Under `hand_off: explicit` it stays
   `in_progress` and `idle` until a step or a human says it is ready; until a
   handoff claims it, more tasks — on the main worktree, or as side tasks
   merged back into it — can run on it (new, 137.15).
4. **Handoff trigger.** A trigger grabs a batch (new, 137.9):

   ```yaml
   # {config_dir}/triggers/nightly-train.yaml
   id: nightly-train
   enabled: true
   source: {type: schedule, project: 1, cron: "0 2 * * 1-5"}
   action:
     type: start_handoff
     workflow: github-handoff-train
     select: {lane: hand_off, delivery: change, max: 8}
     grant: [pull.create, pull.comment, pull.merge, issue.comment, issue.close]
   on_fire: create
   overrun: skip
   ```

   The eight issues are claimed by handoff H7; their cards read "handing off
   (H7)".
5. **Handoff run.** `github-handoff-train` reads `$VINCENT_HANDOFF_FILE`,
   stacks each item's `head_sha` onto a train cut from the fresh base, builds
   and tests it, has an agent review it, fixes what it can on the train and
   sends back what it cannot (`vincent handoff return 7 42 --follow-up …`,
   which queues a follow-up on issue 42's main task and moves it back to
   `in_progress`), opens one pull request and merges it through vincent's own
   routes under the grant.
6. **Outcome.** `vincent handoff deliver 7 --all --close completed
   --comment-file merged.md` closes every remaining item through the outbox,
   attributed to H7, and the issues reach `done`.

The same frame, configured differently:

- **Questions answered, no code.** A workshop task on a `kind: question`
  issue delivers `kind: reply` with an answer. A handoff with grant
  `[issue.comment, issue.close]` posts it and closes the issue — zero agent
  sessions.
- **Needs info.** A workshop task that cannot proceed delivers `kind: reply`
  with its questions. The handoff posts them and marks the item `waiting`.
  When the reporter answers (needs gap F4 below), a trigger returns the item
  to the workshop.
- **Mail, chat, a deploy.** A command step in the handoff workflow runs
  `sendmail`, `curl` to a webhook, or the deploy script, then `vincent handoff
  deliver`. Vincent needs no adapter for any of them.
- **A pull request per issue, reviewed by people.** A handoff fans out over
  `.Handoff.Items`, opens one pull request per item, and marks each `waiting`.
  The item resolves itself when GitHub's `Closes #N` closes the issue.

## What this reverses

Every binding record this plan overturns, narrows or keeps. Each keeps
governing the code until the named item's pull request merges.

- **Spec §3 row 11** — "Nothing on the step path reaches GitHub … every write
  route is excluded from the MCP tool surface" — **narrowed** (decision 6) to
  "nothing on the step path reaches GitHub through vincent **without a grant a
  human gave**". "A `merge` step type is **not licensed**" is **kept**: no step
  type is added; a granted step calls an existing route.
- **Task 068 decision 1**, "no step type, default workflow or automatic
  behaviour calls a write", and its restatement in `internal/github/doc.go` —
  **narrowed** the same way.
- **Task 130 decision 10**, only a human writes back and an agent's write to
  an imported issue is refused `forge_write_needs_human` — **narrowed**
  (decision 6): a granted handoff is a third path, with its own actor.
- **Task 069 decision 3** (the pull-request write routes are excluded from
  MCP) — **narrowed** (decision 6, open question 2): they appear as tools on
  the per-step MCP endpoint of a granted handoff task, and nowhere else. The
  shared `/mcp` endpoint keeps excluding them.
- **Task 096 decision 1**'s replay exclusion list (pull-request create
  excluded from trigger replay) — **kept**. A trigger still never calls a
  pull-request route; it starts a handoff, and the handoff's task calls the
  route under the grant the trigger file carries.
- **Task 096 decision 1**, "a trigger is a robot pressing a key a human could
  have pressed" — **kept**: `start_handoff` replays `POST /v1/handoffs`, which
  a human reaches from the TUI and the CLI with the same grant.
- **Task 130 decision 3** and **task 134 decision 1**, lanes "derived, never
  stored" — **kept**. The handoff sub-status (decision 5) is derived from
  handoff rows, as the lane is derived from task rows. No issue state is added;
  decision 3's anticipated "handoff pillar's `waiting`" is a derived marker,
  not a state GitHub would have to preserve.
- **Task 134 decision 2**, the lane rules — **amended** (decision 13).
  `hand_off` is no longer "no unsettled root task and at least one ended
  `done`"; it is "nothing running, and the latest done root task's delivery
  is **ready**". Readiness is automatic for a `hand_off: auto` workflow — the
  default, and today's behaviour — set only by `vincent task deliver --ready`
  for `hand_off: explicit`, and set or withdrawn by a human. An open issue
  whose finished work is not ready stays `in_progress`, marked `idle`, so
  "some root task is unsettled" becomes one of two ways to be in progress. A
  handoff task carries no `issue_id` (decision 2), so it never counts as an
  issue's work. The trigger guide's warning that "a `lane: hand_off` trigger
  that creates a task for the same issue loops through the lanes" stops
  applying to `start_handoff`, and keeps applying to `create_task`.
- **Task 134 decision 19** and its open question 1, "hand-off is finished
  `done` work on an open issue" — **narrowed** (decision 13) to finished work
  that the workflow or a human declared ready.
- **Task 130 decision 5**, one issue backs many tasks — **narrowed while a
  handoff claims the issue** (decision 14): a task created on a claimed issue
  is refused `issue_in_handoff`. Task 134 decision 10 (a busy main worktree
  queues, never `409`) is untouched; the refusal is about the claim, not the
  worktree.
- **Task 130 decision 5**, one `tasks.issue_id` per task — **kept**. A handoff
  links many issues through its items, not through the task row.
- **Task 130 decision 20.3**, "edits, comments, deletes … fire nothing" —
  **departed from** by frame gap F4, not by this pillar's core.
- **The repository's `vincent-delivered/` convention** — **superseded** by the
  delivery record (decision 3), the way task 130.14 superseded `gh issue view`
  with `$VINCENT_ISSUE_FILE`.

## Decisions

Recorded 2026-10-24. Every decision is settled by the author: 4, 5, 6, 11,
13 and 14 by their answers to the questions that shaped them, as were open
questions 1, 2, 3, 5, 7 and 8, and 1, 2, 3, 7, 9, 10 and 12 — the plan's own
proposals — accepted as written the same day. Each says so where it is
settled. Each decision is **taken now; its effect lands with its item**, and
the older record keeps governing the code until that item's pull request
merges.

### 1. A handoff is a record, executed by an ordinary task (2026-10-24)

Two new tables. `handoffs(id, project_id, task_id, trigger_id, grant_json,
created_by, created_at)` and `handoff_items(handoff_id, issue_id,
delivery_task_id, delivery_json, outcome, outcome_at, outcome_by, note)`. The
run is the task `handoffs.task_id` names: a root task on its own
`vincent/{id}-{slug}` branch, cut from the project's base with task 099's
fresh-base fast-forward, running a handoff workflow (decision 7). It has steps,
retries, gates, cost caps, transcripts and recovery because it is a task. A
task is a handoff task because a `handoffs` row points at it; no `tasks`
column is added.

The handoff has no state machine of its own. It is **live** while its task is
unsettled, **waiting** while its task is settled and some item is `waiting`,
and **settled** otherwise.

*Alternatives beaten:*

- **Convention only** — workflows and `gh`, as today. Every limitation in
  "What handoff looks like today" stays, and each admin re-solves claiming and
  outcomes in bash.
- **A handoff executor beside tasks.** A second engine, a second state
  vocabulary beside `internal/taskstate`, and a second admission path beside
  the scheduler — each one an ownership invariant in `CLAUDE.md`.
- **A handoff as a step type.** A step acts inside one task's worktree; a
  handoff spans many issues' branches and outlives any one task's slot (the
  `waiting` outcome).

*Settled by the author 2026-10-24:* accepted as written.

### 2. An item is an issue; an issue is in at most one live handoff (2026-10-24)

A handoff's items are **issues**, each carrying the delivery it is handed off
with (decision 3), snapshotted onto the item at claim time. An issue — not a
task — is the unit because task 134 gives an issue one main branch across
successive main tasks: what is handed off is that branch at its latest
delivery.

**Claiming** happens in the create transaction, so the single writer makes it
race-free, and a partial unique index on `handoff_items(issue_id)` over
unresolved and `waiting` items makes a second live claim impossible. An issue
is **eligible** when it is open, in the `hand_off` lane, unclaimed, and its
current delivery (identified by `delivery_task_id` and `head_sha`) has not
already been `delivered` by an earlier handoff. That last clause is what lets
a handoff deliver without closing — a staging deploy, an email — without the
next selection picking the issue up again, while a follow-up that produces a
new delivery makes it eligible once more.

A handoff spans **one project**: one repository, one base.

A claim is held from creation, so an `on_fire: propose` handoff — created
`paused` — holds its issues until a human resumes or cancels it. That is task
122's "an unreviewed proposal holds its group", applied to issues, and the card
says so.

*Settled by the author 2026-10-24:* accepted as written.

### 3. A delivery is the workshop's result, and every done task has one (2026-10-24)

`vincent task deliver` (`POST /v1/tasks/{id}/delivery`, an MCP tool on the
step endpoint) records, on the running task:

| Field | Meaning |
|---|---|
| `kind` | `change` (commits to integrate), `reply` (an answer, a refusal or questions, to be posted), or `none` |
| `title`, `body` | Plain-language title and Markdown body — what a pull request or a comment would say |
| `summary` | What the change is for: the brief, a conflict resolver's best input |
| `head_sha` | The commit the workshop verified; HEAD at the call by default |
| `branch`, `base` | Filled by the daemon from the task |
| `close_reason` | Optional suggestion: `completed`, `not_planned`, `duplicate` |
| `proof` | Optional: what proved it — tests run, or "none" with why |
| `ready`, `ready_by` | Whether the issue may move to `hand_off` on this delivery, and who said so (decision 13) |

It is one JSON column on `tasks`; a later call replaces it, and a follow-up
run's call replaces the previous run's. A `done` task that never called it has
an **implicit delivery**: `change` when its branch has commits past its base,
`none` otherwise, titled and described by the task, and ready when its
workflow is `hand_off: auto`. Every `hand_off` issue therefore has a delivery.

The issue DTO gains `delivery`, read from its latest `done` root task, so the
board can say what is waiting to be handed off.

*Amended 2026-10-24, after a research pass for 137.2 (#835):*

- **Stored at `→ done`, never derived at read time.** The lane is pure SQL
  (`internal/store/issuelane.go`, mirrored by `issuestate.LaneOf`) and cannot
  run git, so the implicit delivery is materialized in `Runner.transition`
  (`internal/taskrun/engine.go`), the one path to `done`, beside the merge-back
  pre-check that already reads git there (task 134.14-b). It is passed into the
  same write as the transition. `kind` is `none` when the branch tip is the
  task's `base_sha` or the branch is already in its base branch, and `change`
  otherwise.
- **A follow-up clears it.** Re-admitting a `done` task clears its delivery in
  the same transition, so each `→ done` describes the run that reached it, and
  "the latest done root task" is the one with the greatest `finished_at`.
- **A merge-back's delivery is its source side task's**, with the merge commit
  as `head_sha`, so the issue's latest delivery describes the work rather than
  "Merge task N".
- **Existing rows.** A one-time, idempotent reconcile at daemon start fills an
  implicit delivery for every finished root task with an `issue_id` that has
  none. A missing branch reads `none`.
- **The daemon resolves `head_sha`**, because clients never touch git. A
  containerized step records its delivery through the `task_deliver` MCP tool,
  because `vincent` is not in the image (task 062.2 decision 4, as for
  `vincent status`).

*Alternative beaten:* keeping the file convention and documenting it. It lives
in the git common directory, which a containerized handoff cannot assume is
mounted, it cannot be listed or rendered by any client, and its schema is
whatever the last workflow to write it decided.

*Settled by the author 2026-10-24:* accepted as written.

### 4. Outcomes, per item (2026-10-24)

`vincent handoff deliver|return|wait|release <handoff> <issue>...` (`--all`
for every unresolved item), over `POST /v1/handoffs/{id}/items/{issue_id}/…`
and MCP tools on the step endpoint. Each records `outcome`, `outcome_at`,
`outcome_by` and an optional `note`, and may post `--comment-file` on the
issue.

- **`deliver [--close completed|not_planned|duplicate|none]`** — the work left.
  The issue is closed with the reason unless `none`; `none` leaves it open and
  out of later selections (decision 2).
- **`return [--follow-up PROMPT]`** — the work comes back. The claim is
  released and the delivery's readiness withdrawn, so the issue is
  `in_progress` again. With `--follow-up`, a follow-up run (task 027) is
  queued on the issue's latest main-role task — the one task 134 decision 14
  allows, since a predecessor that handed its worktree on answers `409` —
  carrying the prompt; `--paused` holds it for a human. Without it, the issue
  waits `idle` for a human to add tasks, which is the TUI's "return from
  handoff" (decision 14).
- **`wait`** — the result is with someone outside: a pull request in review, a
  question with the reporter. The item **outlives the handoff's task**, holds
  its claim, and resolves itself as `delivered` when the issue closes, by any
  actor — which is how a pull request's `Closes #N`, imported by sync, ends a
  waiting item with no extra wiring. A human or a reaction may resolve it
  earlier with any other verb.
- **`release`** — the handoff gives the issue back untouched: claim dropped,
  still eligible.

When a handoff's task settles, every item still unresolved is **released**
automatically, recorded `outcome_by: settle`. An aborted handoff therefore
frees its issues; a `done` one that forgot an item does too, visibly.

*Settled by the author 2026-10-24 (open question 7):* `return` is a follow-up
on the issue's latest main-role task; `--side` opts into a side task with
task 134's `merge_back` instead. *Alternatives beaten:* always a new main task,
which loses the earlier run's context, and a per-handoff `return:` policy with
no default.

### 5. The lane gains a handoff marker, not a fifth lane (2026-10-24)

The issue DTO, the list row and `vincent issue ls` gain a derived `handoff`
block on `hand_off` issues: `{state, handoff_id, task_id}`, with `state` one of
`pending` (eligible, unclaimed), `claimed` (in a live handoff), `waiting` and
`delivered` (delivered without a close). An `in_progress` issue with nothing
running — finished work not yet declared ready (decision 13) — carries
`idle`. The TUI card renders it the way it renders `attention`. `?handoff=pending` filters on it, and `select:`
(decision 8) reads the same SQL fragment, so the board, the CLI and a trigger
can never disagree about what is eligible.

A `waiting` or `claimed` issue whose work restarts — the handoff itself filed
a fix task for it — is `in_progress`, as task 134 decision 2 already says; the
marker stays on the card until the handoff resolves the item.

*Alternative beaten:* a fifth stored or derived lane (`handing_off`,
`in_review`). The lane is "where the work is"; the marker is "what the handoff
is doing with it". A fifth lane would split one column of finished work into
two by a fact a sub-status already carries, and every board, filter and
trigger's `lane:` vocabulary would grow with it. Configurable lanes, the
other alternative, would have to be stored, reversing task 130 decision 3 and
task 134 decision 1.

*Settled by the author 2026-10-24 (open question 1):* four lanes and the
marker. An admin's stages beyond the four are labels and `kind`.

### 6. Automated forge writes need a grant a human gave (2026-10-24)

A handoff carries a **grant**: a closed set of capabilities over existing
routes.

| Capability | Route |
|---|---|
| `issue.comment` | `POST /v1/issues/{id}/comments` on an imported issue |
| `issue.close` | `POST /v1/issues/{id}/close` (and the close in `deliver`) on an imported issue |
| `pull.create` | `POST /v1/tasks/{id}/github/pull/create` |
| `pull.comment` | `…/github/pull/comment` |
| `pull.merge` | `…/github/pull/merge` |
| `pull.close` | `…/github/pull/close`, `/reopen` |
| `checks.rerun` | `…/github/pull/checks/rerun` |

**One forge-write guard** covers issue and pull-request writes. A write from an
`agent` caller (an MCP call, or a request carrying `X-Vincent-Task-Id`,
`internal/api/issues.go:296`) is allowed when the caller's task is a live
handoff task whose grant names the capability **and** whose scope covers the
target: an item's issue, the handoff task's own pull request, or an item's
delivering task's pull request. Otherwise it is refused
`forge_write_needs_human`, as today. Local issues need no grant, as today.

Granted writes are attributed to a new actor, **`handoff`**, beside `human`,
`agent` and `sync` in `internal/issuestate`, and the issue events carry
`handoff_id`. An imported issue's granted state change enqueues on task
130.10's outbox exactly as a human's does. A `type: issues` trigger sees
`by: handoff` and treats it as trusted, like every local actor, and
`match.by` can exclude it to break an echo loop.

**Who grants:**

- **A human**, starting a handoff from the TUI or the CLI. The keypress is the
  consent (task 069 decision 2), and the TUI form shows each capability as a
  checkbox, off by default.
- **A trigger file**, through `action.grant`. Triggers are global, off twice,
  and armed only by a human (task 098 decision 3's never-arm rule, enforced by
  `vincent trigger apply`). The TUI asks before writing a non-empty grant, as
  it asks before `permission: workflow`. `create-trigger` and
  `update-triggers` may stage a grant and never arm it.
- **Never an agent.** An MCP `handoff_start` may only create a grant-less
  handoff (open question 3), the way an MCP caller may stage a trigger and
  never arm it.

The default grant is empty. A grant-less handoff can still claim, record every
outcome, and close and comment on local issues.

**Over MCP**, the pull-request write routes become tools on the per-step
endpoint (`/mcp/step/{run_id}`) of a granted handoff task only, checked by the
same guard, because a `restricted` agent step has no shell and MCP is its only
way to act. The shared `/mcp` endpoint keeps excluding them, so task 069
decision 3 holds for every other caller.

**This also closes an inconsistency.** Today only the issue routes consult the
step marker: `issueActor` is called from `internal/api/issues.go` and
`issuecomments.go` alone, so a step's `vincent github pr merge` is accepted as
a human's while its `vincent issue close` is refused. Under the one guard a
step's ungranted pull-request write is refused too. No workflow in this
repository calls `vincent github pr` writes — they use `gh` — but a user's
might, so the release notes carry it as a breaking change (open question 8).

The marker header stays what task 130 decision 17 says it is: best-effort,
not a privilege boundary (spec §16). A full-auto step can unset its
environment, or call `gh` directly, and the grant does not pretend otherwise.
What it decides is what **vincent itself** will do for automation, and it moves
automated handoff from the `gh` path vincent cannot see onto the outbox path it
can.

*Alternative beaten:* keeping forge writes human-only and leaving automation on
`gh`. The refusal prevents nothing — row 11 keeps the agent's own path open —
and costs crash safety, attribution, echo suppression and trust: a close made
by the admin's own automation comes back as an untrusted `sync` event that an
`issues` trigger must allow-list against the issue's author. *Also beaten:*
grants for issue writes only, with pull-request create and merge left on `gh`
— the requirement's own example merges, and would stay invisible.

*Settled by the author 2026-10-24:* the grant model as written; the one guard
over issue and pull-request writes, accepting the break for ungranted step
calls with release notes rather than a deprecation release (open question 8);
pull-request tools on a granted handoff's per-step MCP endpoint only (open
question 2); and agents may start grant-less handoffs only (open question 3),
not child handoffs within their own grant.

### 7. A handoff workflow declares itself and gets its items (2026-10-24)

A workflow declares `role: handoff` to be offered when a handoff starts; one
that does not is refused there, and one that does is not offered by the
new-task form. It sees:

- **`.Handoff`** in every §8.4 template: `ID`, `Grant`, and `Items`, each with
  `.Issue` (the shape a task's `.Issue` has), `.Delivery`, `TaskID`, `Branch`
  and `HeadSHA`. Zero-valued outside a handoff task, like `.Issue`.
- **`$VINCENT_HANDOFF_FILE`**, the same content as JSON, written into the
  worktree's git dir before every attempt (task 130 decision 23.3's
  placement). Each item embeds the `$VINCENT_ISSUE_FILE` shape, so a
  workflow's `jq` for one issue works on each item.
- **`fan_out` over the items**, with task 080's lanes-from-a-list, so "one pull
  request per issue" is a lane per item.
- **`vincent workflow render --handoff-file F`**, previewing a handoff
  workflow against a sample (task 044).

The skills and the five built-ins are re-read in the same piece of work, per
`CLAUDE.md`'s rule, and `update-workflows`' checklist gains the line.

*Settled by the author 2026-10-24:* accepted as written.

### 8. Starting a handoff, and `select:` (2026-10-24)

`POST /v1/handoffs {project_id, workflow, issue_ids | select, grant, title,
paused}` creates the handoff, claims its items and creates its task in one
transaction. `select` is evaluated inside that transaction:

```yaml
select:
  lane: hand_off            # the only lane a handoff selects from, in v1
  delivery: change          # change | reply | none, or a list
  kind: [bug, feature]
  labels: [ready-to-ship]
  max: 8
  order: priority           # issue priority, then oldest delivery first
```

Explicit `issue_ids` must each be eligible, or the create is a `409` naming the
first that is not. A selection that finds nothing is `409 invalid_state`,
`details.reason: nothing_to_hand_off`, which a trigger's ledger records
`refused` — not delivered, so the dedupe key is not spent.

Surfaces: `vincent handoff start`; the TUI issues board, where the bulk
selection of task 011 picks `hand_off` cards and one key opens the handoff
form; a trigger's `action.type: start_handoff`; and MCP, grant-less.

**Batching needs no new trigger primitive.** A schedule plus `select` is the
nightly train. An event-driven batch is task 122's overrun:

```yaml
source: {type: issues, project: 1}
match: {action: lane_changed, lane: hand_off, by: task}
action:
  type: start_handoff
  workflow: github-handoff-train
  select: {lane: hand_off, max: 8}
overrun: queue_coalesce
```

The first issue to reach `hand_off` starts a handoff; while it runs, later
arrivals coalesce into one held event, which fires when the handoff's task
settles and selects everything that accumulated.

*Amended 2026-10-24 (task 137.5, issue #838), settled with the author:*

- **The body takes the task options** of `POST /v1/tasks`, flat at the top
  level — `description`, `fields`, `priority`, `agent`, `model`, `effort`,
  `restricted` and `max_task_cost_usd` — and the task is built by that route's
  own code. `restricted` is there now because 137.9 maps a trigger's
  `permission` onto it. The fields that would make the task anything but a
  root task with no issue, cut from the project's base, are refused.
- **A grant is refused from every agent caller**, not only MCP: an MCP call
  or a request carrying `X-Vincent-Task-Id` or `X-Vincent-Chat-Id`, which is
  `issueActor`'s definition and decision 6's. Best-effort, like every marker
  (spec §16).
- **The three reasons** are `issue_not_eligible` (with `issue_id`, and
  `handoff_id` when a handoff in the project claims it), `nothing_to_hand_off`
  and `grant_needs_human`, all `409 invalid_state`.
- **`order: priority` puts priority 0 last**: urgent (1) through low (4),
  then "none", then the oldest delivery, then issue id.

### 9. Trains, reviews and pull requests stay in workflows (2026-10-24)

Vincent ships no merge-train step, no review step and no `merge` step type
(row 11 kept). It ships **example handoff workflows** — one pull request per
issue, a merge train, comment-and-close, and a notify-only handoff — and splits
the repository's own `github-iterate-work` into the workshop dispatcher it
already includes and a `github-handoff-train` driven by a trigger. A helper
such as `vincent handoff stack` is considered only once two shipped workflows
carry the same bash for it — the blog's "extract first, let the second adapter
prove the boundary".

*Settled by the author 2026-10-24:* accepted as written.

### 10. No in-tree target adapters (2026-10-24)

GitHub is the only target vincent writes natively, because it is the only
source vincent syncs and its outbox exists. Every other target — GitLab, Jira,
mail, chat, a deploy — is a command step in the admin's handoff workflow, with
credentials from the daemon's inherited environment (§2's secret-management
non-goal, kept). This is task 096 decision 2's line for sources, applied to
targets. When a second intake provider lands (frame gap F5), its write-back
arrives with it.

*Settled by the author 2026-10-24:* accepted as written.

### 11. The name (2026-10-24)

The entity is **handoff**: `/v1/handoffs`, `vincent handoff`, `.Handoff`. It
collides with task 074's **chat handoff** (`vincent chat handoff`, the
`handed_off` chat state, `Store.HandoffChat`), which hands a chat's worktree to
a task. The chat verb keeps its name under its own namespace, and every
document says "chat handoff" in full where both could be meant.

*Alternatives beaten:* "delivery", which decision 3 needs for the workshop's
result and the resolve workflows already use for it; "closure", which reads as
"the issue closed" when `wait` and `return` are outcomes that do not close; and
renaming the chat feature, which breaks a shipped CLI verb for a word.

*Settled by the author 2026-10-24:* keep `handoff`.

### 12. Events and surfaces (2026-10-24)

- **Events:** `handoff.created`, `handoff.item_changed {handoff_id, issue_id,
  from, to, by}`, `handoff.settled`. `issue.lane_changed` is unchanged; the
  marker change rides `handoff.item_changed`.
- **Triggers:** a `type: issues` source gains an opt-in `handoff_changed`
  action, as task 134.7 made `lane_changed` opt-in.
- **TUI:** the `hand_off` card's marker and delivery kind; an issue detail
  Handoff section listing every handoff that took the issue and its outcome; a
  handoff task's workspace Overview listing its items. Handoff tasks are tasks,
  so they sit on the task board with no new view.
- **CLI:** `vincent handoff start|ls|show|deliver|return|wait|release`.
- **MCP:** the item verbs and `handoff_show` on the step endpoint, the
  pull-request write tools on a granted handoff's step endpoint, and
  `handoff_start` grant-less (decision 6).

*Settled by the author 2026-10-24:* accepted as written.

### 13. Ready for hand-off is declared, by the workflow or by a human (2026-10-24)

An issue may carry many tasks while it is in progress. Task 134 already gives
it one main worktree with one task running there at a time, queues later main
tasks behind it, and runs side tasks in their own worktrees, merged back when
the main worktree is free (`merge_back`, manual or agentic). What moves the
issue on is no longer "a root task ended `done`" but **its latest delivery
being ready**:

- **The workflow decides.** A workflow declares `hand_off: auto`, the default
  and today's behaviour — the delivery is ready when the task ends `done` — or
  `hand_off: explicit`, where it is ready only when a step ran `vincent task
  deliver --ready`. A research or spike workflow is `explicit`; a resolve
  workflow can stay `auto`, or decide in a step.
- **A human overrides.** `vincent issue ready <id>` and the TUI's ready key
  mark an `in_progress` issue's finished work ready; `vincent issue unready
  <id>` pulls an unclaimed `hand_off` issue back to `in_progress`. A step's or
  MCP caller's mark is allowed and attributed `agent`: it is local state, not a
  forge write.
- **The mark lives on the delivery** of the latest `done` root task
  (`ready`, `ready_by`), so the lane is still derived from task rows (task 134
  decision 1, kept) and nothing is stored on the issue. A later task that
  finishes produces a new delivery with its own readiness, so a mark never
  outlives the work it judged. A merge-back carries its source side task's
  readiness.
- **The lanes**, for an open issue: `open` with no root task, or only
  `aborted` ones; `in_progress` while some root work task is unsettled, or
  when finished work exists but the latest delivery is not ready (`idle`,
  decision 5); `hand_off` when nothing is running and the latest delivery is
  ready. A human's mark moves the lane with `issue.lane_changed` `by: human`.
- **Readiness is stamped at `→ done` from the workflow snapshot**, never the
  live registry, so editing a workflow never re-labels finished work.
  `issuestate.LaneOf` and the lane SQL change together.
- **Upgrading keeps every lane.** The migration sets `ready: true, ready_by:
  migration` on every task that finished `done` and has no `ready` key. Those
  tasks reached `done` under the old automatic rule, so no issue changes lane
  when the daemon upgrades.

Unclaimed issues in either lane take new tasks exactly as today; a new task on
a `hand_off` issue moves it back to `in_progress` while it runs.

*Settled by the author 2026-10-24.* *Alternatives beaten:* always explicit,
which makes every existing workflow add `--ready` to keep its flow; and
keeping it automatic, under which a handoff trigger grabs an issue the moment
its first task finishes, before a human can decide it needs a second.

*Amended 2026-10-24 (task 137.15, issue #848), settled by the author when the
item was evaluated.* Three gaps in the decision as written:

1. **An absent `ready` reads as ready, and there is no migration.** The last
   bullet above is replaced. The lane SQL reads a finished root task whose
   delivery is NULL, or has no `ready` key, as ready — the old automatic rule —
   spelled once (`COALESCE(json_extract(t.delivery_json, '$.ready'), 1)`).
   Every write from this change on stores `ready` explicitly. A migration
   cannot compute a delivery's `kind`, so it would either leave the 137.2
   backfill skipping rows for ever or leave issues not ready until the
   backfill finished; `ready_by: migration` is never written.
2. **A correction keeps readiness.** `vincent task deliver` on a `done` task
   replaces the delivery's text and keeps its stored `ready`/`ready_by`;
   `--ready` there is a mark attributed like `vincent issue ready` (`human`,
   or `agent` over MCP). Only `vincent issue unready` withdraws readiness. On
   a running task the last call wins: `--ready` stores `ready_by: task`, and a
   later call without it clears it.
3. **A side-only issue is `in_progress`, idle.** The latest delivery stays the
   main-line one. An issue whose only finished work is a side task whose
   merge-back never ran has finished work but no ready main-line delivery, so
   it waits in `in_progress`; this keeps decision 3's "every `hand_off` issue
   has a delivery". It is the one lane that moves on upgrade (it was
   `hand_off`), and `vincent issue ready` on it is refused with
   `issue_no_finished_work`.

`ready_by` is `workflow`, `task`, `human` or `agent`. An `auto` workflow
stamps `ready_by: workflow` at `→ done` unless a step already said `--ready`;
it cannot say "not ready". The refusals are `409 invalid_state` with
`issue_work_running`, `issue_no_finished_work`, `issue_in_handoff` and, for
`unready` outside `hand_off`, `issue_not_hand_off`.

### 14. A claimed issue takes no new workshop tasks (2026-10-24)

While a live handoff claims an issue — `claimed` or `waiting` — `POST
/v1/tasks` with that `issue_id` is `409 invalid_state`, `details.reason:
issue_in_handoff`, with the handoff in `details.handoff_id`. It applies to a
human, a trigger (whose delivery lands `refused`) and MCP alike. Two things
still add work:

- **The handoff's own task** may create tasks for its items — a fix task, a
  side task — since it holds the claim.
- **A human who wants to add work returns the item first:** the TUI's "return
  from handoff", or `vincent handoff return` without `--follow-up` (decision
  4), releases the claim and withdraws readiness, so the issue is `in_progress`
  and `idle` and takes tasks again.

*Settled by the author 2026-10-24.* *Alternatives beaten:* allowing new tasks
while the claim stands, under which a merge train can stack a delivery that is
already stale; and letting a new task release the claim implicitly, which
pulls an issue out of a handoff mid-run with nobody deciding to.

*Amended 2026-10-24 (137.4 evaluation, settled by the author):* the refusal
covers **every route that adds work to a claimed issue**, not only `POST
/v1/tasks`: a chat handed off with that `issue_id`, and `POST
/v1/tasks/{id}/follow_up` on a root task of the issue, which clears its
delivery and moves its lane — the same stale-delivery hazard. The handoff's
own task is recognised by the MCP creator id or by the `X-Vincent-Task-Id`
marker's value, best-effort like every marker (spec §16). The check runs
**inside the store's insert or transition transaction**, so it cannot race a
handoff create, and an engine merge-back of a side task the handoff's task
created is exempt. *Amended 2026-10-24 (review F1 of #850): every engine
merge-back is exempt — it is inserted inside its side task's `→ done`, so a
refusal there stranded the side task in `running`, and its source already
passed the check when it was created or re-admitted.* 137.4 also carries the store primitives for item outcomes
and `handoff.settled`, so the derived state and the events are testable;
release-on-settle, the item verbs and their routes stay in 137.6, and `POST
/v1/handoffs` in 137.5.

## The rest of the frame

What else stands between vincent and "a frame an admin builds their own
lifecycle in". Each is its own task document when it starts. *Order settled by
the author 2026-10-24:* F1 and F4 first, then F2 and F3; the rest follow.

| # | Gap | Why the frame needs it | Proposed shape | Touches |
|---|---|---|---|---|
| F1 | **Selecting work to start** | "A trigger listens which issues can be started" — today a trigger fires per event, and picking *N ready issues* takes a dispatcher workflow (`github-resolve-issue-multiple`) | decision 8's `select:` on `create_task` too: `lane: open`, `kind`, `labels`, `max`, `order` | task 096 decision 1 (kept) |
| F2 | **Dependencies and readiness** | `github-resolve-issue-multiple`'s survey derives "ready" from GitHub's blocked-by relation in bash | `issue_links` (`blocks`, `parent`), mirrored from GitHub and local, and a derived `ready` that `select` reads | task 134 non-goal "sub-issues" (departed from) |
| F3 | **WIP limits** | A kanban frame caps work per stage; the §11 caps count tasks, not issues | `issues.wip` per project and lane, honoured by `select` and queued, never refused, at the cap | task 134 decision 10's queue-not-409 (kept) |
| F4 | **Comments as events** | The needs-info loop: a `waiting` item must wake when the reporter answers | `commented` on `type: issues`, `by`-attributed; `sync` comments untrusted, so `allowed_actors` applies | task 130 decision 20.3 (departed from) |
| F5 | **A generic intake provider** | Jira, Linear, a markdown backlog, a mailbox — and handoff write-back to them | a `command`-backed issue source: a script prints issues as NDJSON into the issue set under provider `command:<id>`, and a write-back command receives state changes and comments from the outbox | task 130 decision 15.1, "the first provider does not design for the second" (superseded once a second exists) |
| F6 | **Flow metrics** | An admin tuning a lifecycle needs lead time, time per lane, throughput, rework and cost per issue | derived from `issue.lane_changed`, handoff outcomes and task costs: `GET /v1/projects/{id}/flow`, `vincent issue flow`, a project-overview panel | §17 |
| F7 | **The pipeline, visible** | The lifecycle is spread over trigger files; nobody can see that `hand_off` has no trigger draining it | a flow panel listing, per lane, the armed triggers that watch or select from it, and a `vincent doctor` row for a lane nothing drains | task 132's project overview |
| F8 | **A starter kit** | An empty frame is a hard start | `vincent sdlc init`, staging a coherent set — intake trigger, resolve workflow, handoff trigger, handoff workflow — as a proposal the human arms; the skills gain a handoff chapter | task 098 decision 3 (kept) |
| F9 | **Approval before an external effect** | Some teams want a person on every merge | a grant capability may be `approve`-qualified (`pull.merge: approve`), parking the route call as a gate; this is task 134 decision 18's `merge_back.approve`, generalized | task 134 decision 18 |
| F10 | **Outward signals on outcomes** | Stakeholders hear about results, not task states | `notify.on` accepts handoff outcomes, with the item in the envelope | task 046 decision 4 (`notify.on` is §6's state vocabulary) and decision 5 (the envelope) |

F1 and F4 are what the requirement's example and the needs-info path need
first; F2 and F3 make selection trustworthy at scale; F5 is what makes the
frame vendor-independent at both ends, which is the blog's thesis.

## Non-goals

- A merge-train, review or deploy engine in Go (decision 9).
- Target adapters beyond GitHub (decision 10).
- Configurable lanes. The four lanes are derived from facts vincent can prove;
  an admin's own stages are labels and `kind`, which triggers and `select`
  already branch on (open question 1).
- A handoff across projects (decision 2).
- Writing issue title, body or labels back to GitHub (task 130 decision 9,
  kept).
- Webhooks from GitHub.com (task 096's transport line, kept).

## Risks

- **A grant is a standing authorization.** An armed trigger with
  `pull.merge` merges at 2 a.m. with no keypress. The mitigations are task
  096's: off twice, `on_fire: propose` by default, the TUI's confirmation, and
  F9 for teams that want a person on each merge.
- **A forgotten proposal holds issues** (decision 2). The card names the
  handoff and its `paused` state, and `vincent handoff ls` lists claims.
- **`waiting` items accumulate.** A pull request nobody reviews keeps its
  issue claimed indefinitely. The issue detail shows the age; F6's metrics
  surface it.
- **Breaking: ungranted pull-request writes from steps are refused**
  (decision 6). No in-repository workflow is affected.
- **Prompt injection reaches a granted agent.** An issue body flows into a
  handoff agent that can merge. The answer is task 096's: `restricted` agent
  steps by default under a trigger, containers advisory, and grants as narrow
  as the workflow needs.

## Open questions

Each has a proposed default, which stands unless the author answers otherwise
before the item that needs it starts.

1. **A fifth lane, or configurable lanes?** *Proposed:* neither — a derived
   marker (decision 5), and stages beyond the four are labels and `kind`.
   *Settled 2026-10-24 by the author:* the default stands (decision 5).
2. **May a granted handoff's agent reach pull-request writes over MCP?** A
   `restricted` agent has no shell, so MCP is its only path. *Proposed:* yes,
   on the per-step endpoint of a granted handoff task only, through the same
   guard; the shared `/mcp` endpoint keeps excluding them. *Settled
   2026-10-24 by the author:* the default stands (decision 6).
3. **May an agent start a handoff?** *Proposed:* yes, grant-less only.
   *Settled 2026-10-24 by the author:* the default stands; a granted agent
   may not start child handoffs within its own grant (decision 6).
4. **Does a `waiting` item survive its handoff task's archive?** *Proposed:*
   yes. The rows belong to the handoff, not the worktree.
5. **Does spec §3 record this ahead of its code?** Task 130 decision 13 and
   task 132 decision 14 did, by the author's explicit exception. *Proposed:*
   yes, row 38, saying in its own note that it is ahead of its code.
   *Settled 2026-10-24 by the author:* yes, in 137.1's pull request — an
   explicit exception to `README.md`'s same-pull-request rule, covering §3
   only, as task 130 decision 13's did.
6. **Must an issue have an explicit delivery to reach `hand_off`?**
   *Proposed:* no — the implicit delivery (decision 3) keeps task 134's lane
   meaning.
7. **Where does `return` send the work?** *Proposed:* a follow-up on the
   issue's latest main-role task; a side task (task 134's `merge_back`) only
   when the handoff asks for one with `--side`. *Settled 2026-10-24 by the
   author:* the default stands (decision 4).
8. **Is refusing ungranted pull-request writes from steps worth the break?**
   *Proposed:* yes; the alternative is a guard on issues and none on pull
   requests, which is the inconsistency decision 6 names. *Settled 2026-10-24
   by the author:* yes, as a breaking change in the release notes, with no
   deprecation release (decision 6).

## Tasks

In delivery order. Items without a `Depends:` can proceed in parallel. Each
item amends the spec sections and public pages its code makes true, in its own
pull request.

- [x] **137.1** ([#834](https://github.com/lezli01/vincent/issues/834)) This document; the dated notes on task 130 decision 10, task
  068 decision 1, task 069 decision 3 and task 134's risks and non-goals; spec
  §3 row 38, ahead of its code (open question 5), with dated notes on rows 11,
  33 and 36. ✓ 2026-10-24
- [x] **137.2** ([#835](https://github.com/lezli01/vincent/issues/835)) The delivery: a `tasks.delivery_json` migration, `POST
  /v1/tasks/{id}/delivery`, `vincent task deliver`, its MCP tool, the implicit
  delivery materialized at `→ done`, clearing on follow-up, a merge-back
  copying its source, the startup reconcile for existing issue tasks,
  `task.delivery_changed`, and `delivery` on the task and issue DTOs
  (decision 3). Spec §5.3, §5.6, §6, §12.1, §13.2–§13.4, §14. ✓ 2026-10-24
- [x] **137.3** ([#836](https://github.com/lezli01/vincent/issues/836)) The repository's resolve workflows move off
  `vincent-delivered/` onto `vincent task deliver`, and `github-iterate-work`
  reads deliveries over the API. Depends: 137.2. `collect` admits only an
  explicit `change` delivery and snapshots it into the run's scratch; the
  landing step no longer clears anything, and there is no transition shim.
  ✓ 2026-10-24 *Amended 2026-10-24 (review F2 of #850):* unlike the file,
  a delivery does not survive a follow-up (decision 3), so the train is no
  longer identical to the file-built one there — `collect` leaves a task
  followed up after delivering out under a reason of its own, naming
  `vincent task deliver` as the way back in, and "has moved on" now covers
  hand edits only. Settled by the author: no shim keeps the cleared record.
  *Amended 2026-10-24 (review F3 of #850):* the workflows now need a vincent
  built from 137.2 or later, CLI and daemon; the resolve workflows' `fetch`
  and `github-iterate-work`'s `headroom` refuse an older one up front.
- [x] **137.4** ([#837](https://github.com/lezli01/vincent/issues/837)) `handoffs` and `handoff_items`, the claim index, eligibility
  SQL shared with the lane fragments (`internal/store/issuelane.go`), the
  `handoff` actor, `handoff.*` events, and `POST /v1/tasks` refusing a claimed
  issue with `issue_in_handoff` (decisions 1, 2, 12, 14). Depends: 137.2.
  Spec §5.6, §5.7, §13.2, §13.3. ✓ 2026-10-24
- [x] **137.5** ([#838](https://github.com/lezli01/vincent/issues/838)) `POST /v1/handoffs` with `issue_ids` and `select`, the read
  routes, apiclient, `vincent handoff start|ls|show`, and grant-less MCP
  (decision 8). Depends: 137.4. Spec §5.7, §13.1, §13.2, §13.4, §18.
  ✓ 2026-10-24 The list route is a third tool, `handoff_list`, because
  §13.4's surface is the route table minus its exclusions and a read is no
  exclusion.
- [ ] **137.6** ([#839](https://github.com/lezli01/vincent/issues/839)) The item verbs, release on settle, resolve-on-close for
  `waiting`, and `return` as a follow-up (decision 4). Depends: 137.5.
- [ ] **137.7** ([#840](https://github.com/lezli01/vincent/issues/840)) The grant and the one forge-write guard over issue and
  pull-request writes; the outbox under the `handoff` actor; the pull-request
  tools on a granted handoff's per-step MCP endpoint; the restatement in
  `internal/github/doc.go`; spec §13.4, §16 and the security model
  (decision 6). Depends: 137.4.
- [ ] **137.8** ([#841](https://github.com/lezli01/vincent/issues/841)) `role: handoff`, `.Handoff`, `$VINCENT_HANDOFF_FILE`, fan-out
  over items and `vincent workflow render --handoff-file`; the
  `vincent-workflows` skill, the five built-ins re-read and the
  `update-workflows` checklist line (decision 7). Depends: 137.5.
- [ ] **137.9** ([#842](https://github.com/lezli01/vincent/issues/842)) `action.type: start_handoff` with `select` and `grant`, the
  TUI's grant confirmation, the `handoff_changed` action on `type: issues`;
  the `vincent-triggers` skill and `update-triggers`' checklist line
  (decisions 6, 8, 12). Depends: 137.5, 137.7.
- [ ] **137.10** ([#843](https://github.com/lezli01/vincent/issues/843)) The derived `handoff` marker on the issue DTO, `?handoff=`,
  and the CLI's lane column, with `idle` on an `in_progress` issue whose
  finished work is not ready (decisions 5, 13). Depends: 137.4, 137.15.
- [ ] **137.11** ([#844](https://github.com/lezli01/vincent/issues/844)) The TUI: the `hand_off` card's marker and delivery, bulk
  selection and the handoff form with its grant checkboxes, the issue detail's
  Handoff section, the handoff task's Overview, the ready and unready key and
  `idle` on in-progress cards, and "return from handoff" on a claimed issue
  (decisions 13, 14). Depends: 137.6, 137.10.
- [ ] **137.12** ([#845](https://github.com/lezli01/vincent/issues/845)) Example handoff workflows under `examples/`, and
  `github-iterate-work` split into its dispatcher and `github-handoff-train`
  with a trigger (decision 9). Depends: 137.8, 137.9.
- [ ] **137.13** ([#846](https://github.com/lezli01/vincent/issues/846)) `scripts/137-gate.sh` on all three platforms, against
  `cmd/fakegh` and `cmd/fakeagent`: claim exclusivity, an ungranted write
  refused and a granted one written back through the outbox, `wait` resolved by
  a sync close, `return` moving the issue to `in_progress`, an aborted handoff
  releasing its items, an `explicit` workflow's issue staying `idle` until
  `deliver --ready` or `vincent issue ready`, and a task refused on a claimed
  issue. Wired into `gate-group`'s lightest group. Depends: 137.6, 137.7,
  137.15.
- [ ] **137.14** ([#847](https://github.com/lezli01/vincent/issues/847)) `docs/guides/handoff.md`, the features page, the API, CLI,
  configuration and template references, and the screenshot seed with new
  tapes appended at the end of `do_capture`. Depends: 137.11, 137.12.
- [x] **137.15** ([#848](https://github.com/lezli01/vincent/issues/848)) Readiness: the workflow's `hand_off: auto|explicit`, `vincent
  task deliver --ready`, `vincent issue ready|unready` with their routes and
  MCP tools, the mark on the latest delivery stamped at `→ done` from the
  snapshot, a merge-back carrying its source's readiness, the lane SQL's and
  `issuestate.LaneOf`'s new `hand_off` rule, the migration that keeps every
  issue's lane on upgrade, and the workflow editor's row for the key (task
  086's coverage test); spec §5.2, §5.6, §13.2 and §13.3, the workflow-schema
  reference, the `vincent-workflows` skill and `update-workflows`' checklist
  line (decision 13). Depends: 137.2. Added 2026-10-24. ✓ 2026-10-24 — with
  no migration (decision 13's amendment).
