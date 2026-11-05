# 143 — Auto-archive a closed issue's tasks

**Status:** 🔄 in progress (3/5) · **Opened:** 2026-11-04

The auto-archive stream of [#925](https://github.com/lezli01/vincent/issues/925):
when an issue reaches `closed`, by any route, the daemon archives the settled
tasks that issue started. #925 also carries an issue Work tab and a change to
this repository's DAG workflow; those are separate streams and not recorded
here.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **143.1** ([#926](https://github.com/lezli01/vincent/issues/926))
  `Runner.Archive` takes `ArchiveOptions{Force, Unattended, EventPayload}`. An
  unattended archive never forces and never runs the remote leg of the branch
  step; the fan-out cascade passes the whole of its root's options to every
  lane; the payload is merged into the archive's `task.state_changed` event.
  This document, its index row, and the §10 / §12.3 amendment. ✓ 2026-11-04
- [x] **143.2** ([#927](https://github.com/lezli01/vincent/issues/927)) The
  store query for the archivable tasks of a closed, quiescent issue:
  `Store.ArchivableIssueTasks` returns a closed issue's settled roots created
  at or before its close, side work before main tasks, or nothing while any
  root is unsettled or an issue chat is live. ✓ 2026-11-04
- [x] **143.3** ([#928](https://github.com/lezli01/vincent/issues/928)) The
  durable event-cursor worker that archives them, and the
  `archive_tasks_on_issue_close` key. Depends: 143.1, 143.2.
  `internal/issuearchive`, wired in `daemon.Run`; its cursor is migration
  0053's one row, seeded at the newest event; decisions 10 and 11 below.
  ✓ 2026-11-05
- [ ] **143.4** ([#929](https://github.com/lezli01/vincent/issues/929)) A
  `vincent doctor` row for settled tasks of closed issues that auto-archive had
  to skip. Depends: 143.3.
- [ ] **143.5** ([#930](https://github.com/lezli01/vincent/issues/930)) An
  acceptance gate proving issue-close auto-archive end to end on all three
  platforms. Depends: 143.3.

## Decisions

Confirmed by the author on 2026-11-04.

1. **The daemon may archive a closed issue's tasks.** #761 (task 134.14)
   recorded "Archive stays a human action (§10)". That line is **superseded**
   by this stream: the requirement asks for automatic archiving outright.
   Archive stays a human action everywhere else — §6 still lists it as one, and
   nothing here archives a task whose issue is open.
2. **Every close reason counts** — `completed`, `not_planned` and `duplicate`
   alike. A reason-filtered rule was rejected: a task of a not-planned issue is
   no more worth keeping on the board than one of a completed one.
3. **Only roots created at or before the close.** A root with
   `created_at <= closed_at` is archived; a task deliberately started on an
   already-closed issue is left alone, because someone chose to work there.
4. **Chats are never auto-archived, and a live issue chat holds the archive.**
   A chat is a conversation a human may come back to; and a live issue chat may
   still produce a merge-back the main branch has to be there for.
5. **The rule is per-issue quiescence.** Nothing is archived while any of the
   issue's roots is unsettled or an issue chat is live; otherwise a pending
   merge-back could lose its main branch (task 134). Side tasks and merge-backs
   are archived before main tasks, so the last main task's empty-branch rule
   (task 134 decision 17.2) sees no successor carrying the branch.
6. **Unattended archives never force and never run the remote leg** (143.1).
   This *enforces* task 008 decision 3 ("the remote leg is attended-only") in
   code rather than relitigating it: before 143.1 `deleteArchivedBranch` passed
   `delete_remote_branch_on_archive` on every call, and its comment ("every
   caller of Archive is a human") was already false for the cascade. The leg is
   gated on the archive's mode, not on its caller, so a lane inherits its
   root's mode and a new caller cannot reach the remote by forgetting to say
   who it is. `Unattended` overrides `Force`, so "never forces" is a property of
   the mode rather than something each caller must remember. A dirty worktree
   refuses and leaves the task untouched, as project delete's unattended
   removal already does — task 005 decision 6 ("the unattended path never
   deletes") is about orphan directories with no task row, and is not
   contradicted.
7. **No grace delay, and no backfill.** The worker's cursor is seeded at the
   newest event on upgrade, so no issue closed before the worker existed is
   archived.
8. **No persisted `archived_by` column.** The marker lives only on the
   `task.state_changed → archived` event payload. A migration for a field no
   reader needs was rejected.
9. **`archived_by` is set only by unattended callers**, as `"issue_closed"`. A
   human archive, and a cascade from one, carries no `archived_by` and is
   byte-identical to before. A closed vocabulary on every archive (`"human"`,
   `"issue_closed"`, …) was considered and rejected: it would change every
   existing archive payload for no consumer. The payload is merged *underneath*
   the core keys (`TaskChange.EventPayload`), so an `issue_id` passed in it can
   never contradict the row's.

10. **A claimed issue holds its archive** (143.3, settled 2026-11-05).
    While any handoff item on a closed issue has an outcome that claims it
    (`HandoffOutcome.Claims()`: unresolved or waiting), the worker archives
    none of the issue's tasks. A handoff `return` after a reopen follows up
    the issue's latest main task, and that follow-up is refused once the
    task is archived. The hold lives in the worker, through
    `Store.IssueHandoffClaimed` over `idx_handoff_items_claim`'s condition;
    `ArchivableIssueTasks` is unchanged, and #929's doctor row can ask the
    same helper. The worker wakes on `handoff.item_changed` and
    `handoff.settled`, so the release, return, delivery or settle that ends
    the claim archives the issue then. Ordinary delivery closes the issue and
    marks the item delivered in one transaction, so the hold is normally a
    no-op.
11. **A released issue chat's leftover main-worktree directory is out of
    scope** (143.3, settled 2026-11-05). A closed or archived issue chat can
    still name the issue's main-worktree directory (task 140 decision 3,
    `Store.IssueMainWorktreeChatHolder`). The worker does not touch it,
    consistent with decision 4. It is a known leftover; reclaiming it is a
    separate follow-up — a new issue, or an item #929's doctor row reports.

### Left open

Both questions 143.3 owned are settled: the handoff hold is decision 10, and
the chat-directory leftover is decision 11.
