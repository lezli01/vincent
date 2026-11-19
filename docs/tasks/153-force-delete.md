# 153 — Force delete for tasks, chats and issues

**Status:** ✅ done (5/5) · **Opened:** 2026-11-19
**Spec:** §5.5, §5.6, §10, §13.2 · **Branch:** `feat/force-delete`

The author reported that a task whose archive failed on a git error it could
not resolve stayed on the board for good. Archive was the only way to
`archived`, and permanent delete accepted nothing but `archived` (task 092
decision 2). The TUI never offered `force` for anything but a dirty worktree, so
a `git_error` was a dead end. The author asked for a force delete for tasks,
chats and issues "when there is an error".

Conventions for this file are in [the tasks README](README.md).

## Decisions

Taken 2026-11-19 by the author.

1. **A force delete works from any state but one with a live process.** It
   explicitly relaxes task 092 decision 2 ("archived rows only") for an explicit
   `force`, and only for it. A task in `running` or `awaiting_input`, a lane of
   it in either, or a chat mid-turn is refused with `live_process`: the actor
   that owns the row is still writing to it, and cancel is the way out.
   *Beat:* only hardening `archive --force` to swallow git errors. That keeps
   task 092 intact, but it takes two steps and never reaches a row that cannot
   be archived, such as a blocked task.
2. **The rows go first; the cleanup can only warn.** The task, its lanes and
   its tied chats are deleted in one transaction. The container, the worktree,
   the transcripts and the proposals are cleaned afterwards, and what could not
   be cleaned comes back as `warnings`. The cleanup is exactly what was failing,
   so it must not be able to hold the row again. This is the ordinary delete's
   order, for its own reason.
3. **The worktree goes through `worktree.Discard`.** That is `git worktree
   remove --force`, then the directory itself, but only inside the worktrees
   root, as `Reclaim` deletes, and never the project's own checkout. A
   registration git would not prune is reported, not fixed.
4. **A force delete never deletes a branch.** `force` with `delete_branch` is a
   `400`. A broken repository is the wrong place to judge a branch empty, and a
   kept branch is recoverable.
5. **A force-deleted task takes everything tied to it.** That is its fan-out
   lanes (deepest first), its linked chats (the schema's cascade, as before),
   and any chat handed off to it. A handed-off chat would otherwise be left
   pointing at nothing. Its worktree was the task's, so only its transcripts
   are left to clean.
6. **A forced chat's worktree is cleaned only when it is the chat's own.** A
   linked or handed-off chat's worktree belongs to its task. An issue chat's
   directory stays while a main task waits to receive it (task 140 decision 3),
   which is the same rule archive applies.
7. **An issue's force delete cascades to its holders.** It force-deletes the
   unsettled main and side root tasks and the open issue chats, then deletes
   the issue the ordinary way. A holder with a live process refuses the whole
   call before anything is removed (`Store.ForceDeleteBlocker`). *Beat:*
   detaching the holders (`issue_id` NULL). That leaves a task working in an
   issue's main worktree with no issue to name the line. The CLI spells it
   `issue delete --force --cascade`, because `--force` there was already the
   delete's confirmation (task 130).
8. **The TUI offers force delete where the error happens, never as a key of its
   own.** An archive that fails on its worktree (a `409` with a `reason` other
   than `worktree_dirty`, or a `5xx`) becomes the question
   `force delete #7? (y/n)`. A refused delete on an archived board becomes the
   two-answer force question, unless it was refused for `live_process`. An
   issue delete held by its tasks or chats becomes a force confirmation. The
   dirty re-ask set the precedent for this shape.
9. **The chat archive's `409` carries `details.reason`.** It never did, so the
   TUI's dirty re-ask for chats could not fire against the real daemon. The
   force offer needs the same discriminator.

## Tasks

- [x] **153.1** Store: `ForceDeleteTaskTree`, `ForceDeleteChat`,
  `IssueDeleteHolders`, `ForceDeleteBlocker`, and the `live_process` refusal.
  ✓ 2026-11-19
- [x] **153.2** `worktree.Discard` and `Runner.ForceDelete`. ✓ 2026-11-19
- [x] **153.3** API: `force` on `DELETE /v1/tasks/{id}`, `/v1/chats/{id}` and
  `/v1/issues/{id}`, and `reason` on the chat archive's `409`. ✓ 2026-11-19
- [x] **153.4** Client and CLI: `ForceDeleteTask`, `ForceDeleteChat`,
  `ForceDeleteIssue`; `task delete --force`, `chat delete --force`,
  `issue delete --force --cascade`; and archive failures that name the way out.
  ✓ 2026-11-19
- [x] **153.5** TUI: the force offers on the task board and workspace, the
  chats board, the archived boards and the issue screens. ✓ 2026-11-19
