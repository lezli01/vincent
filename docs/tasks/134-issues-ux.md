# 134 — Issues UX: a four-lane issues board and one main worktree per issue

**Status:** 🔄 in progress (7/19)

Issue [#748](https://github.com/lezli01/vincent/issues/748), part of
[#747](https://github.com/lezli01/vincent/issues/747), the epic. Spec §5.6 in
this document's pull request; §5.3, §6, §10, §11, §12.4, §13, §15 view 12 and
§18 in the pull request of each item whose code makes them true.

## What this is

The requirement, as #747 records it:

> I want the UX of the issues view to be board like, having 3 lanes, open, in-progress, hand-off. Issue is open if no task or anything actionable has been done toward it. In-progress is when the issue is assigned at least one task. Also I want to enforce that an issue can have one main worktree in which at most at one point only one task can be in progress. It should be possible to run other tasks but they need to choose to work on a separate worktree and choose how that work will be merged back in the main worktree of the issue when the main worktree is free. User should be able to choose manually or agentically similarly like how fanout steps merge back together.

It has two connected halves, built on task 130's issues pillar
([130-issues-pillar.md](130-issues-pillar.md)):

1. **An issues board.** The TUI issues view (§15 view 12) stops being a flat
   list and shows four lanes — `open`, `in progress`, `hand-off` and `done`.
   The daemon derives each issue's lane; the TUI never computes it. A fourth
   lane, `done`, was added to the requirement's three by #747's amendment of
   2026-10-07: every closed issue, whatever its close reason.
2. **One main worktree per issue.** An issue has one main branch and
   worktree, and at most one task is in progress in it at a time. Other tasks
   for the issue run in a side worktree cut from the issue's main branch, and
   their work merges back once the main worktree is free — "manual" or
   "agentic", mirroring `fan_out`'s `merge.on_conflict: block | agent` (§7.6).

This document is the plan and the decision record; it changes no code. Each
`134.n` item is one sub-issue of #747 and roughly one pull request. The
research behind it is summarised in #747.

## What this reverses

Every binding record this work overturns, narrows or keeps, by file and
decision number. Each one keeps governing the code until the named item's
pull request merges. The dated notes in task 130, task 132 and spec §5.6 are
written now, by this document's pull request.

- **Task 130 decision 6** and **spec §5.6**: an issue "has no process, no
  steps, no worktree and no slot", and the issue archive was rejected because
  "an issue holds no worktree, branch or transcript to keep" —
  **superseded in part** (decisions 7, 17). An issue now has a main branch and
  worktree, carried by its main-role tasks rather than by the issue. Still no
  process, steps, slot or archive.
- **Task 130 decision 6**, "delete in any state" — **narrowed** (decision 17):
  delete is refused while a main-role task of the issue is unsettled.
- **Task 130 decision 3**, activity "derived, never stored" — **kept and
  extended** (decisions 1–4): the lane and `attention` are two more derived
  values beside `active` and `task_count`.
- **Task 130 decision 16.1**, already superseded by 132.11 (a flat list scoped
  to the selected project), and **132.11**'s flatness — **superseded**
  (decision 6): the scoped list becomes lane sections. 132.11's scoping is kept.
- **Task 130 decision 16.4**, a row's summary "the count plus an active
  marker" — **superseded in part** (decisions 3, 6): the row also sits in its
  lane section and carries `attention`. The exact card is 134.8's and 134.16's.
- **Task 130 open question 1**, a human-settable "in progress" — **answered**
  (decision 1): no; `in_progress` exists only as a derived lane.
- **Task 130 decision 5**, "a closed issue may back a new task"
  (`130-issues-pillar.md:151-154`) — **kept**; decision 4 relies on it.
- **Task 125 decision 2**, a claimed directory "makes the next task wait, it
  does not block it" — **kept and reused** (decisions 7, 10).
- **Spec §12.4**, "recovery is the only path allowed to abort" a merge —
  **departed from** (decision 15), amended by 134.14.

## Decisions

Recorded 2026-10-07. Decisions 1–13 are #747's research findings and their
resolution; decisions 14–18 are the author's answers to #747's open questions
5–11, given while scoping #748; decision 4 is #747's amendment of 2026-10-07.
Each decision is **taken now; its effect lands with its item.**

### 1. Lanes are derived, never stored (2026-10-07)

An issue's lane is a third derived value beside `active` and `task_count`
(task 130 decision 3), read from its root tasks and its own state. There is no
stored lane, no human-settable lane and no dragging a card between lanes.
[#641](https://github.com/lezli01/vincent/issues/641), which proposed a stored
`in_progress`, was closed as not planned.

*Alternative beaten:* a stored, human-settable `in_progress`. GitHub cannot
hold it, so sync would have to preserve it (task 130 open question 1), and it
would drift from the tasks it describes.

### 2. Four lanes, computed once in SQL (2026-10-07)

The daemon computes the lane in SQL and serves the same value to the TUI, the
CLI, the API and MCP. For an **open** issue, over its root tasks
(`parent_task_id IS NULL`, task 130 decision 5):

- `in_progress` — some root task is not settled (`taskstate.Settled`,
  `internal/taskstate/taskstate.go`). This is exactly today's `active`.
- `hand_off` — no unsettled root task, and at least one root task that ended
  `done`, including one archived after `done`.
- `open` — no root task, or only root tasks that ended `aborted`.

A **closed** issue is `done` (decision 4). Waiting-on-human states stay
`in_progress` and are marked by `attention` (decision 3). Chats and pull
requests drive no lane: chats carry no `issue_id`, and a pull request hangs
off a task.

The lane needs two facts the store does not keep today:

- an archived task does not record whether it was `done` or `aborted` — only
  the `task.state_changed` event payload has the previous state
  (`internal/store/transitions.go`) — so 134.3 adds it;
- task events carry no `issue_id`, so clients re-list every issue on any task
  event and triggers cannot see a lane move — 134.6 adds it.

*Alternatives beaten:* waiting states in `hand_off`, which mixes "stuck,
unfinished" with "finished, deliver it"; "any task ⇒ in progress until a pull
request", under which an issue whose only task was cancelled would stay in
progress forever.

*Amended 2026-10-07 (134.6, #753), by the author:* **`issue_id` rides every
task that carries one, lanes included.** `task.created`, `task.state_changed`,
`task.deleted` and `task.restored` carry `issue_id` whenever the task row has
one, omitted otherwise — this mirrors the row and supersedes #753's "root
tasks only". A fan-out lane's events therefore carry `issue_id`, but a lane
never moves its issue's lane, so it never writes `issue.lane_changed`. That
event's payload is `{ id, from, to, task_id?, by? }`: `task_id` names the task
whose create, transition, delete or restore moved the lane and is omitted on a
close or reopen; `by` is the close or reopen actor and is omitted on a
task-caused move.

### 3. `attention`, and `paused` carries none (2026-10-07)

`attention` is true when some root task of the issue is in a
`taskstate.NeedsHuman` state — `awaiting_input`, `awaiting_gate` or `blocked`
(`internal/taskstate/taskstate.go`). It is computed for every lane, `done`
included, so a `done` card can flag a stray live task.

A `paused` task is `in_progress` **without** `attention`: a human paused it,
and nothing waits on them. #747's research listed `paused` among the waiting
states; `NeedsHuman` does not include it, and the author settled on the code's
definition.

### 4. The `done` lane mirrors `closed` (2026-10-07, #747's amendment)

`lane = 'done'` exactly when `issues.state = 'closed'`, for every close reason
— `completed`, `not_planned` and `duplicate`
(`internal/issuestate/issuestate.go`). No stored state, issue state or close
reason is added: §5.6's lifecycle already records "won't be worked on any
more", and the lane only names it on the board.

Closing wins over live tasks: task 130 decision 5 lets a closed issue back a
new task, and that task does not pull the issue out of `done`. Reopening
returns the issue to the lane its tasks give, so `hand_off` keeps its meaning
— finished work on an issue that is still open.

*Alternatives beaten:* deriving `done` from task outcomes, such as "every root
task settled" — a task's outcome does not say whether the issue is over; only
a close by a human, an agent or sync says that. A fifth `cancelled` lane split
by close reason — the requirement is one lane for "finished, for whatever
reason", and the card shows the close reason instead.

### 5. The board hides `done` by default; `s` shows it (2026-10-07)

On the issues view, `s` (the `Scope` op, `internal/tui/bindings.go`, today
"cycle the listing between open, closed and all") becomes "show/hide done".
The toggle lasts for the session, like the folds. While `done` is hidden the
view lists `state=open` only — the closed set grows without bound, so it is
not fetched until asked for. While it is shown the view lists every state and
draws `done` last.

The default is the board's alone. `vincent issue ls`, `GET /v1/issues` and MCP
keep listing every state unless filtered, and gain `--lane done` and
`?lane=done`. Closing and reopening change the lane, so 134.6 writes
`issue.lane_changed` for them in the same transaction as `issue.state_changed`.

### 6. Stacked lane sections, not kanban columns (2026-10-07)

The view shows foldable lane sections with counts, in the order
`open → in progress → hand-off → done`, for the selected project (132.11
already scopes the list, so the board needs no cross-project grouping).

- At the 80-column floor (§15, "Below **80×20**") side-by-side columns get
  about 26 cells each.
- The TUI has no column precedent: the task board is one vertical table with
  foldable group headers.
- The board's `←`/`→` already mean fold; columns would give them a different
  meaning on the issues view.

*Alternatives beaten:* side-by-side columns, kept only as a possible later
wide-terminal renderer over the same section model and not planned; a
hand-off-first, attention-first order.

### 7. The main worktree: a role on `tasks` (option A) (2026-10-07)

Two new task columns: `tasks.issue_worktree` (`main` | `side`, NULL on every
existing row) and `tasks.end_sha`. The issue's main branch is **derived from
its main-role tasks**; nothing is stored on `issues`, so its `version`,
`updated_at` and the sync outbox are untouched. Successive main tasks reuse
one directory: at admission a settled predecessor hands it to its successor
in one transaction, the way `Store.HandoffChat`
(`internal/store/chathandoff.go`) transfers a chat's claim. Enforcement is at
admission, as a predicate in the scheduler walk beside task 125's directory
claim (task 125 decision 2), with a per-walk tally.

A shared branch breaks three per-task assumptions, each fixed by the item
that introduces sharing: branch uniqueness (`claimBranchTx`,
`internal/store/tasks.go`), deleting an empty branch on archive (decision 17),
and commit attribution, which needs `end_sha` (134.12).

An experiment found a bug in the claim this builds on: two queued adopt-mode
tasks on one free branch are both admitted in a single walk
(`admitted [1 2]`), and on the human's checked-out branch both then run in the
project path. 134.2 fixes it first; the new predicate keeps a per-walk tally
for the same reason.

*Alternatives beaten:* B, an `issue_worktrees` table that makes the issue the
owner of the directory — a third claimant for gc, archive, recovery and
doctor. C, a fresh worktree per main task — "free" would mean "archived"
rather than "not in progress", and uncommitted work would be lost at each
hand-over.

*Amended 2026-10-07 (134.10, #757), the author's answers while scoping it:*

- The issue's main branch is the `branch_name` of any **unarchived** main-role
  task — the view `claimBranchTx` takes. Once every main task is archived the
  next main task cuts a fresh name through task 001's chain; the old branch
  stays in git, off the issue's line (see Risks).
- The binding happens in `insertTaskTx`, inside the create transaction: no
  main branch yet makes this task's name it; one existing sets `branch_name`
  to it. `claimBranchTx` exempts unarchived main-role tasks of the same issue
  from each other and from nothing else.
- An explicit `branch_name` or `existing_branch` on a main task becomes the
  main branch when there is none, is accepted when it names the main branch,
  and is a 400 when it names another. Adopt mode stays outside the claim.
- `merge_back: {on_conflict: block|agent}` is a 400 without `issue_id`, on an
  issue with no main branch yet, beside `branch_name` or `existing_branch`, or
  with an unknown value; otherwise it is recorded as `tasks.merge_on_conflict`
  even when the main worktree is free. An empty `on_conflict` is `block`.
- `issue_id` beside `github_pull` stays a 400; the issue's `github_pull`
  criterion is dropped.
- A chat handoff with `issue_id` is main when the issue has no main branch
  (the chat's branch becomes it) and side with `block` otherwise, until 134.15
  lets the handoff body choose; the handoff refuses `merge_back` until then.

### 8. Occupancy: admitted and not settled (2026-10-07)

A main-role task holds the issue's main worktree from its admission
(`started_at` set, which happens on its first transition to `running`,
`internal/store/transitions.go`) until it is settled. `blocked`,
`awaiting_gate` and `paused` hold no slot (`taskstate.HoldsSlot`) but leave
files — and possibly a half-done git operation — behind, so slot-holding alone
is too weak. A blocked or gated main task therefore stalls the issue's main
line until a human acts; that is deliberate, and the issue stays
`in_progress` with `attention`.

*Amended 2026-10-07 (134.11, #758):* **a linked chat keeps the directory
occupied.** An occupant is an **unarchived** main-role task of the issue,
other than the one asking, that is either (a) admitted (`started_at` set)
and not settled, or (b) `done`/`aborted` with a non-empty `worktree_path`
and an open linked chat — the `chats.linked_task_id` predicate
`OpenLinkedChatIDs` uses. A human working in a settled task's worktree holds
it as surely as an agent does. `archived_at IS NULL` is explicit because an
archived task is settled but clause (b) alone would not exclude it. The
scheduler, the `201` hint and the issue's `main_worktree.occupant_task_id`
read this one definition (`issueMainOccupantSQL`).

### 9. A new task with `issue_id` is a main task by default (2026-10-07)

Settled by the author (#747 question 6). New tasks carrying `issue_id` get the
`main` role unless they opt into a side worktree with `merge_back` on
`POST /v1/tasks`. Existing rows (NULL role) and fan-out lanes are exempt.
Within one issue, new tasks now queue behind each other; per-issue triggers
and workflows are unaffected unless two of their tasks target the same issue.

### 10. A busy main worktree queues the task, never 409 (2026-10-07)

Settled by the author (#747 question 5). A main task created while the main
worktree is occupied is queued and admitted when the occupant settles.

- Task 125 decision 2 established that a claimed directory "makes the next
  task wait, it does not block it".
- Triggers replay `POST /v1/tasks` through an `http.Handler`
  (`replay` in `internal/trigger/fire.go`) and cannot answer a 409.
- The requirement's "choose" is satisfied by the opt-in `merge_back`; a user
  who does not choose waits for the main worktree.

A creation-time hint — the TUI form, a CLI note — says the main worktree is
busy (134.10).

*Alternative beaten:* #747's issue-main-worktree finding, a 409
`issue_worktree_busy` unless the caller chooses a side worktree.

*Amended 2026-10-07 (134.10, #757):* the hint is
`main_worktree_occupant_task_id` on `POST /v1/tasks`' 201 for a main task
whose issue's main worktree is occupied (decision 8's definition, read after
the commit), and `main_worktree: {branch, occupant_task_id}` on the issue
detail and row. Rendering it in the TUI form and the CLI stays with 134.15 and
134.16.

### 11. Merge-back is a daemon-created task (option B) (2026-10-07)

When a side task reaches `done`, the daemon inserts, in the same transaction,
a queued, main-role root task with a synthesized one-step `__merge_back`
snapshot. It waits for the main worktree through the same admission predicate
as any main task, so it needs no new task state and no second claim kind. It
reuses `fan_out`'s git primitives (`internal/worktree/merge.go`) and its
crash-safe re-entry (`resumeMerge`, `resumedFromConflict` and
`handleConflict` in `internal/taskrun/join.go`).

*Alternatives beaten:* a merge phase on the side task, which would act in a
directory it does not own (`internal/taskrun/steps.go`); making the next
occupant merge pending work, which couples unrelated tasks; an engine-issued
follow-up, which breaks the actor ownership invariant.

### 12. "Manual" is `fan_out` parity; "agentic" adds a resolver (2026-10-07)

"Manual" is `merge.on_conflict: block` (§7.6): the merge runs automatically
and blocks `merge_conflict` only on a conflict, for a human to resolve in
place. "Agentic" is `on_conflict: agent` with a built-in resolver tried first,
falling back to the block. The agent-resolver path has no test today; 134.9
adds the first ones.

### 13. A side task's base: no fetch, and `base_sha` (2026-10-07)

A side task is cut from the issue's main branch with no fetch: a
`fetch_base_branch` fast-forward (`internal/worktree/basefastforward.go`)
could move a clean main worktree under its occupant. It records `base_sha`,
the fork commit, so its diff does not collapse to empty after its merge-back
(134.13).

*Amended 2026-10-07 (134.13, #760), the author's answers while scoping it:*
the main branch must exist in git when the side task is created, or
`POST /v1/tasks` gives the ordinary `base_branch` 400 — a side task created
while the first main task is still queued or paused is refused rather than
held until the branch appears (which would wait forever if that task failed
before cutting) or failed at admission. An explicit `base_branch` beside
`merge_back` is accepted only when it names the main branch, mirroring
decision 5's `branch_name` rule. `base_sha` is the main branch's tip, resolved
locally under the repository lock, and the side branch is cut from that SHA;
`base_refresh` reads `disabled`/`not_attempted`. A side task handed off from a
chat keeps the chat's worktree and base (134.15 owns that choice).

### 14. Follow-up or chat on a main task that handed its worktree on ⇒ 409 (2026-10-07)

Settled by the author (#747 question 7). The directory now belongs to a
successor, so a follow-up or a chat on the predecessor is refused (134.12).

*Alternative beaten:* queuing a new main task in its place.

### 15. Cancelling a conflicted merge-back runs `git merge --abort` (2026-10-07)

Settled by the author (#747 question 8). Cancelling a merge-back task blocked
on `merge_conflict` aborts the merge, restoring the main worktree for the next
occupant. This departs from spec §12.4's "Recovery is the **only** path
allowed to abort"; 134.14 amends §12.4 with the code.

*Alternative beaten:* leaving the conflict in place and having the next
admission refuse a worktree with a git operation in progress.

### 16. The issue branch checked out in the main checkout blocks (2026-10-07)

Settled by the author (#747 question 10). When the issue's main branch is
checked out in the human's main checkout, a main task blocks with
`issue_branch_checked_out`, a new `Reason*` constant added by whichever of
134.11 and 134.12 adds the predicate.

*Alternative beaten:* running in the main checkout, as task 125's adopt mode
does.

### 17. Issue delete and the shared branch (2026-10-07)

Settled by the author (#747 question 11).

1. **Delete is refused while a main-role task of the issue is unsettled**
   (`409`). Settled and archived tasks do not block it. This narrows task 130
   decision 6's "delete in any state".
2. **The shared main branch is deleted only when the last unarchived
   main-role task of the issue is archived**, by task 008's empty-branch rule.
   Archiving any earlier main task leaves it.

*Alternatives beaten:* allowing delete as today, which would orphan a running
occupant's directory; never deleting the branch automatically; deleting it on
the issue's close or delete.

### 18. `merge_back.approve` is a later addition (2026-10-07)

Settled by the author (#747 question 9). A human approval before each merge,
`merge_back.approve: true`, is a cheap addition on top of decision 12, and is
**not** part of #747. No item builds it.

### 19. #747's other open questions keep their defaults (2026-10-07)

Settled by the author (#747 questions 1–4): hand-off is finished `done` work
on an open issue, an issue whose only tasks were cancelled is `open`
(decision 2), the board is stacked sections, and the order is
open → in progress → hand-off (decision 6).

### 20. `tasks.archived_from` records the state an archive left (2026-10-07, 134.3)

Decision 2 needs to know whether an archived task was `done` or `aborted`.
134.3 makes it a column.

1. **A column, not the event.** `tasks.archived_from` is `done` or
   `aborted`, NULL unless archived, written by `TransitionTask` on
   `→ archived`. `finished_at` cannot tell the two apart — both states stamp
   it — and the archiving event's `from` is too costly to read in a list
   query and goes with the project's events. *Alternative beaten:* treating
   every archived task as `done`, which would put an issue whose cancelled
   attempts were archived in `hand_off`.
2. **The backfill defaults to `done`** when no archiving event survives. The
   error it can make asks a human to close an issue; the opposite error would
   hide finished work back in `open`.
3. **Store-only.** It is on the row and on `store.Task`, and the lane SQL
   reads it. No API, CLI or MCP task representation carries it, so no client
   changes with it.
4. **No new event type.** `task.state_changed` already carries `from`.
5. **Backup and restore need no code.** `ImportTask` copies every column it
   does not name as an exception, and the staged backup database is opened
   through `store.Open`, which migrates it — a backup taken before migration
   0042 is backfilled from its own events at restore.

Only a human's `DELETE /v1/tasks/{id}` or a project's deletion removes a task
row; the §17 pruner removes transcript files and never a row (task 092). An
archived task therefore stays in its issue's lane derivation until it is
deleted.

## Non-goals

- GitHub write-back beyond task 130: posting comments, auto-closing on `done`,
  opening pull requests. These belong to the handoff pillar.
- Sub-issues, issue templates and new issue fields.
- Changes to the task board, or to task 130's sync and outbox.
- A web UI.
- Rebasing the issue branch onto an updated project base — a known gap.
- Human-settable lanes, and dragging a card between lanes (decision 1).

## Risks

- New tasks with `issue_id` are main tasks by default (decision 9), so tasks
  within one issue now queue behind each other.
- A blocked or gated main task stalls the issue's main line until a human acts
  (decision 8).
- Archiving every main task of an issue drops its main branch (decision 7's
  134.10 amendment): the next main task cuts a fresh branch, and the old one
  stays in git with the earlier work, off the issue's line, until someone
  merges or deletes it.
- Until 134.12 removes the wait, a later main task waits for the previous
  one's **archive**, not its settlement (review F1 of #768); 134.11's
  occupancy predicate does not shorten it. It is bound to the
  main branch as an adopted branch, so task 125 decision 2's working-directory
  claim queues it while any earlier main task still has the branch checked
  out, and a done main task keeps its worktree until it is archived. Archive
  keeps the branch while another unarchived main task carries it (decision
  17's "the branch goes with the last main-role task's archive").
- The `hand_off` lane only grows until someone closes the issue; nothing
  auto-closes a local issue.
- A live task on a closed issue sits in the hidden `done` lane. The task board
  still shows it, and its card's `attention` flags it once `done` is shown.

## Open questions

#747's twelve, all answered. None changed a #747 decision.

1. **Hand-off meaning.** *Settled 2026-10-07 by the author:* finished work on
   an open issue (decision 2).
2. **Aborted-only issues.** *Settled 2026-10-07 by the author:* `open`
   (decision 2).
3. **Columns or sections.** *Settled 2026-10-07 by the author:* stacked
   sections; columns only as a possible later renderer (decision 6).
4. **Lane order.** *Settled 2026-10-07 by the author:*
   open → in progress → hand-off, then `done` (decision 6).
5. **Busy main worktree without a choice.** *Settled 2026-10-07 by the
   author:* queue, no 409 (decision 10).
6. **Opt-in or default.** *Settled 2026-10-07 by the author:* main role by
   default for new `issue_id` tasks (decision 9).
7. **Follow-up or chat on a task that handed its worktree on.** *Settled
   2026-10-07 by the author:* 409 (decision 14).
8. **Cancelling a `merge_conflict`-blocked merge-back.** *Settled 2026-10-07
   by the author:* `git merge --abort` (decision 15).
9. **"Manual" with an approval.** *Settled 2026-10-07 by the author:* a later
   addition, outside #747 (decision 18).
10. **Issue branch in the main checkout.** *Settled 2026-10-07 by the
    author:* block `issue_branch_checked_out` (decision 16).
11. **Issue delete.** *Settled 2026-10-07 by the author:* refused while a
    main-role task is unsettled; the branch goes with the last main-role
    task's archive (decision 17).
12. **Closed issues.** *Settled 2026-10-07 by the author,* in #747's
    amendment: the fourth `done` lane (decisions 4, 5).

## Tasks

In delivery order. The lane track (134.3 → 134.8) and the worktree track
(134.2, 134.9 → 134.14) can proceed in parallel. Each item amends the spec
sections and public pages its code makes true, in its own pull request.

- [x] **134.1** ([#748](https://github.com/lezli01/vincent/issues/748)) This
  document; the dated notes in task 130 (decisions 3, 6, 16, open question
  1), task 132.11 and spec §5.6. ✓ 2026-10-07
- [x] **134.2** ([#749](https://github.com/lezli01/vincent/issues/749)) Fix
  task 125's claim admitting two tasks for one directory in one walk.
  ✓ 2026-10-07
- [x] **134.3** ([#750](https://github.com/lezli01/vincent/issues/750)) A
  migration recording whether an archived task was `done` or `aborted`:
  migration 0042 with its backfill, `TransitionTask` writing it,
  `store.Task.ArchivedFrom`, the migration, transition and restore tests,
  and spec §13.2/§14 (decision 20). ✓ 2026-10-07
- [x] **134.4** ([#751](https://github.com/lezli01/vincent/issues/751))
  `lane` (all four values) and `attention` in the store, the API, project
  stats and the spec (decisions 1–4). Depends: 134.3. `issuestate.Lane` and
  `LaneOf`; one set of SQL fragments (`internal/store/issuelane.go`) behind
  the issue row, `ActiveIssueTaskIDs`, project stats and `?lane=`; the API
  reference and spec §5.6/§13.2. ✓ 2026-10-07
- [ ] **134.5** ([#752](https://github.com/lezli01/vincent/issues/752))
  `--lane` and a `LANE` column on the CLI, and the MCP descriptions.
  Depends: 134.4.
- [x] **134.6** ([#753](https://github.com/lezli01/vincent/issues/753))
  `issue_id` on task events, and `issue.lane_changed`, including on close and
  reopen. Depends: 134.4. Written in the causing write's transaction, after
  its event, only when the lane moves; the TUI issues list re-lists on a task
  event only for an issue it shows; spec §13.3 and the API reference.
  ✓ 2026-10-07
- [ ] **134.7** ([#754](https://github.com/lezli01/vincent/issues/754)) A
  `type: issues` trigger fires on a lane change. Depends: 134.6.
- [x] **134.8** ([#755](https://github.com/lezli01/vincent/issues/755)) The
  TUI board's lane sections, the hidden-by-default `done` lane and its `s`
  toggle, fold keys, the guide's key table and spec §15 view 12 (decisions
  5, 6). Depends: 134.4. `internal/tui/issuesections.go` partitions by the
  served `lane`; `←`/`→`/`C`/`O` fold; the guide's issue tables joined
  `TestGuideKeyTablesMatchRegistry`. ✓ 2026-10-07
- [x] **134.9** ([#756](https://github.com/lezli01/vincent/issues/756)) The
  merge message factored out, `handleConflict` taking a policy, the first
  agent-resolver tests and a fakeagent scenario (decision 12). ✓ 2026-10-07
- [x] **134.10** ([#757](https://github.com/lezli01/vincent/issues/757)) The
  `issue_worktree` role, `end_sha`, the main-branch binding at creation,
  `merge_back` on `POST /v1/tasks` and `main_worktree` on the issue DTO
  (decisions 7, 9, 10). Depends: 134.1.
- [x] **134.11** ([#758](https://github.com/lezli01/vincent/issues/758)) The
  scheduler predicate with a per-walk tally (decisions 7, 8).
  Depends: 134.10, 134.2. `ListAdmissible` serves `IssueOccupied` from the
  shared `issueMainOccupantSQL`, widened to a settled main task kept open by a
  linked chat (decision 8's amendment); the walk skips an occupied issue's
  main candidate, and a second main task of one issue in the same walk, as a
  skip rather than a block; spec §5.6, §6, §11, §13.2. ✓ 2026-10-07
- [ ] **134.12** ([#759](https://github.com/lezli01/vincent/issues/759)) The
  claim transfer, archive safety, refusing follow-up or chat on a
  predecessor, and `end_sha` in commits and diff (decisions 14, 17).
  Depends: 134.11.
- [x] **134.13** ([#760](https://github.com/lezli01/vincent/issues/760)) Side
  tasks cut from the issue branch with no fetch, recording `base_sha`
  (decision 13). Depends: 134.10. ✓ 2026-10-07
- [ ] **134.14** ([#761](https://github.com/lezli01/vincent/issues/761)) The
  merge-back task's schema, creation, executor and reasons; spec §12.4
  (decisions 11, 15). Depends: 134.9, 134.12, 134.13.
- [ ] **134.15** ([#762](https://github.com/lezli01/vincent/issues/762)) The
  choice in the CLI, MCP, triggers and the chat handoff. Depends: 134.14.
- [ ] **134.16** ([#763](https://github.com/lezli01/vincent/issues/763)) The
  occupant and merge-backs on cards and the detail, and the side-worktree
  rows in the new-task form. Depends: 134.8, 134.15.
- [ ] **134.17** ([#764](https://github.com/lezli01/vincent/issues/764)) An
  end-to-end gate for the occupant rule and both merge modes.
  Depends: 134.14.
- [ ] **134.18** ([#765](https://github.com/lezli01/vincent/issues/765)) The
  user guide for main worktrees, side tasks and merge-back. Depends: 134.15.
- [ ] **134.19** ([#766](https://github.com/lezli01/vincent/issues/766)) Seeds
  for every lane, the re-captured pictures and the walkthrough.
  Depends: 134.16.
