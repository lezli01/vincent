# 143 — Auto-archive a closed issue's tasks

**Status:** ✅ done (5/5) · **Opened:** 2026-11-04

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
- [x] **143.4** ([#929](https://github.com/lezli01/vincent/issues/929)) A
  `vincent doctor` row for settled tasks of closed issues that auto-archive had
  to skip. Depends: 143.3. The daemon-only `issue_archive` group
  (`internal/api/doctorissuearchive.go`) over
  `Store.ClosedIssueArchiveCandidates`, which shares its predicates with
  `ArchivableIssueTasks`; the F3 rule shared as `issuearchive.SideWorkHold`;
  decisions 12–16 below. ✓ 2026-11-05
- [x] **143.5** ([#930](https://github.com/lezli01/vincent/issues/930)) An
  acceptance gate proving issue-close auto-archive end to end on all three
  platforms. Depends: 143.3. `scripts/143-gate.sh`, wired in `ci.yml`'s
  `gate-group` (group 1); its record is
  [docs/gates/143-auto-archive.md](../gates/143-auto-archive.md). ✓ 2026-11-05

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

12. **The doctor row lists every candidate, held or not** (143.4, settled
    2026-11-05). `vincent doctor`'s `issue_archive.held` lists every root
    the archivable rule's *task-level* predicates cover: the issue is
    closed, the task is a done or aborted root, and its cutoff (the
    merge-back cutoff of `ArchivableIssueTasks`) is at or before
    `closed_at`. Issue-level quiescence does not filter the list; it is a
    reason. A candidate nothing holds now is listed as `awaiting_retry`,
    because the worker retries only when an event touches the issue: a dirty
    worktree cleaned since, an issue closed while the key was off, and an
    issue closed before the upgrade (decision 7) all stay on the board
    otherwise. After an upgrade, the tasks of issues closed before it appear
    here too — intended, since they are exactly what the worker will never
    act on.
13. **A closed vocabulary of reasons** (143.4). `dirty` — the task's
    worktree, or that of an unarchived lane under a fan-out root, has local
    changes, untracked included (`worktree.Manager.IsDirty`; a missing
    directory, or the project's own checkout, is not dirty);
    `project_path_missing` — such a worktree exists but the project's
    directory does not, which `worktree.Manager.Remove` refuses as
    `ReasonProjectPathMissing` before git runs, so it is never reported
    `dirty` (review F2, amended 2026-11-05); `locked_by_chat`
    — an open linked chat on the task or on any unarchived lane under it
    (`Store.SubtreeOpenLinkedChatID`), since the cascade archives each lane
    through the ordinary `Archive` and `RefuseLocked` refuses each (review
    F1, amended 2026-11-05);
    `unfinished_lane` — `Store.NonTerminalDescendants`, the predicate
    behind `UnfinishedDescendantsError`; `issue_not_quiescent` — another
    root of the issue (one created after the close included) is unsettled,
    or an issue chat is live; `handoff_claimed` —
    `Store.IssueHandoffClaimed` (decision 10); `held_for_side_work` — only
    on an issue main task (merge-backs are not), when a side task or
    merge-back candidate of the issue carries `dirty`,
    `project_path_missing`, `locked_by_chat` or `unfinished_lane`, which is review F3, shared with the worker as
    `issuearchive.IsIssueMain` and `issuearchive.SideWorkHold`;
    `awaiting_retry` — none of the others. The constants are
    `doctor.Hold*`.
14. **Every reason, as a list** (143.4). Each entry carries `reasons: [...]`
    in `doctor.HoldReasons` order, never one precedence-picked reason, so
    fixing one never reveals another. `awaiting_retry` appears only alone.
15. **Leftover issue-chat directories are their own sub-list** (143.4). For
    each closed issue, the chat `Store.IssueMainWorktreeChatHolder` returns
    and its directory, while that directory exists, are reported in
    `issue_archive.chat_dirs`. It is a directory, not a task; a row still
    claims it, so it is not a gc orphan, and doctor does not reclaim it
    (decision 11).
16. **Information, never a problem** (143.4). Held tasks and leftover
    directories never enter `problems[]`, and `vincent doctor` still exits
    0; task 006 decision 7's closed unhealthy set is unchanged. The group
    only reports: it offers no `--fix`, `doctor --fix` gains nothing, and it
    never archives, forces or deletes — forcing past a dirty worktree stays
    a human's call. It is computed only with `archive_tasks_on_issue_close`
    on (empty, and not printed, when the key is off or nothing is held) and
    is unknown without a daemon. It needs no event, migration or
    event-vocabulary change: a derived row cannot go stale, and a durable
    `task.auto_archive_skipped` event stays rejected.

### Left open

Both questions 143.3 owned are settled: the handoff hold is decision 10, and
the chat-directory leftover is decision 11. 143.4's are decisions 12–16.
