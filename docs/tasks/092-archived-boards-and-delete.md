# 092 — Archived boards for tasks and chats, with permanent delete

**Status:** ✅ done (7/7)
**Issue:** #350
**Amends:** §5.5 — delete is legal from `archived` alone and refused from
`handed_off`; §6 — delete is not a human action and never appears in
`available_actions`; §10 — task 008's exception widens to "at archive time
**and at permanent delete**", the standing rule unchanged and the remote leg
not offered; §13.2 — the two `DELETE` routes, the new query parameters on
`GET /v1/tasks` and `GET /v1/chats`, and the archived-only ordering; §13.3 —
`task.deleted` and `chat.deleted`; §13.4 — both routes join the
destructive-admin exclusion; §15 — a tenth view and its palette rows; §17 —
the retention table's "DB rows are never deleted" qualified to the *pruner*.
Decision record row 30.
**Keeps, without relitigating:** [008](008-empty-branch-cleanup.md) — the
§10 rule that a branch carrying any commit past its base is never deleted, and
[056](056-fetch-base-branch.md)'s `base_sha` fork point, both reused verbatim.
[011](011-bulk-actions.md) — "there is no bulk endpoint and there is not going
to be one"; every sweep here is one `DELETE` per row.
[049](049-command-palette.md) — retiring `1..6` without substituting new
memorized keys is the point, so the two boards get palette rows and no key,
exactly as [067](067-chats-in-the-tui.md) gave chats one.
[074](074-chat-handoff.md) decision 5 — `handed_off` means the task owns the
worktree, which is why both directions of that link are a refusal.
[074](074-chat-handoff.md) decision 6 and [079](079-chat-listing-scope.md)
decision 2 — a chat's `updated_at` *is* when it ended, so no `archived_at`
column is added for chats.

Archived history existed and was unreachable. `board.tasksCmd` asked for
`ListTasksOptions{}`, whose zero-value `Archived` is `ArchivedExclude`, and no
other TUI surface asked for anything else; `store.ListTasks` had understood
`ArchivedOnly` since it was written, and `archived_at` has been a column since
`0001_init.sql`, written on the terminal transition and read by nothing but
retention. Chats got half of it in task 079: an `s` scope cycle on the chats
board, which is a peek rather than a place.

And nothing deleted a task or a chat. The only hard delete in the system was
`DELETE /v1/projects/{id}`, and the only thing that reclaimed anything on a
clock was `TranscriptPruner`, which removes transcript *files* and never a row.
An installation that ran for a year accumulated every row it ever created, with
no way to discard one and no screen on which to see them.

## Decisions

1. **The branch is judged by task 008's rule, which is not amended.** §10's
   standing rule keeps its full force: a branch carrying **any** commit past its
   base is never deleted by vincent. `delete_branch=true` reuses
   `worktree.Manager.DeleteEmptyBranch` verbatim — the same `base_sha`-or-
   `base_branch` fork point (task 056), the same `-D`-only-with-a-recorded-
   `base_sha` rule, the same outcome vocabulary — and a branch with commits is
   reported `has_commits` and kept whatever the human answered. Task 008's
   exception widens from "at archive time" to "at archive time and at permanent
   delete" and gains nothing else. **The remote leg is not offered at all**:
   `delete_remote_branch_on_archive` stays honoured only by
   `POST /v1/tasks/{id}/archive`, because deleting a branch on a forge other
   people share is unrecoverable and a delete has no second chance to
   reconsider.

2. **Refusals name the row that is holding on.** Delete applies to archived rows
   only; anything else is a `409` in the snake_case envelope and is never
   performed. Three further refusals, each a named `409` rather than a
   foreign-key error leaking out of the driver:
   - An archived fan-out parent whose lane rows still exist. `parent_task_id` is
     a plain `REFERENCES tasks(id)` with no `ON DELETE` clause
     (`0007_fan_out.sql`) and `PRAGMA foreign_keys` is on, so without the guard
     the delete fails *in the driver*. The lanes go first, and the refusal says
     so.
   - A `handed_off` chat. The task named by `handoff_task_id` owns the worktree
     and branch (task 074 decision 5), and `handed_off` means "that task owns
     it" — a deleted row cannot say that.
   - The mirror of it: an archived task that a `handed_off` chat points at.
     `chats.handoff_task_id` is `ON DELETE SET NULL` (`0023_chat_handoff.sql`),
     so deleting the task would leave a terminal chat pointing at nothing.

   `created_by_task_id` (`ON DELETE SET NULL`) and the idempotency rows
   (`ON DELETE CASCADE`) need no refusal — they clear themselves, and
   `mcp.max_depth`'s walk simply stops one link early.

3. **The age sweep is a client-side loop.** Task 011's decision stands verbatim:
   there is no bulk endpoint and there is not going to be one. The sweep lists
   the rows archived before a cutoff and sends one `DELETE` per row,
   sequentially, reporting `done` / `refused` the way `actionBar.dispatchBulk`
   already does for bulk archive. The same holds for the `space`/`V` selection
   and for the CLI's `--before`.

4. **Palette rows, no dedicated key.** Task 049 retired the `1..6` takeover keys
   and the registry records why — "retiring `1..6` without substituting new
   memorized keys is the point". Chats, the closest prior art, got a palette nav
   row and no key (task 067). The archived boards get the same: two
   `scopeGlobal` `nav: true` rows. The issue's premise that `s` is skip and `A`
   is archive is right; the answer 049 gave was to stop adding keys, not to hunt
   for a free letter.

5. **One board model with an archived mode.** `internal/tui/board.go` is where
   grouping, folding, `/` filtering and bulk selection live, and the archived
   board needs all four. The board gains an archived mode and `viewArchived`
   routes to the same model constructed in it; `internal/tui/chats.go` takes the
   same treatment at a much smaller size, keeping its `s` cycle untouched.
   Grouping, folding, the selection and the report are shared *by construction*
   and cannot drift. `TestArchivedBoardSharesTheLiveBoardsBehaviour` is the
   fence around that: if the `if archived` branches ever start reaching into
   rendering, the fallback is this decision's runner-up — a view that embeds the
   board — and that is a change of shape better made early than half-way.

6. **Delete is not a §6 action.** It is not a state transition, `taskstate` has
   no opinion on it, and it must never appear in `available_actions` — which is
   what gates every `scopeTaskAction` binding. So the TUI's delete key is a
   `scopePanel` binding on the two archived contexts, and the API refusal is the
   handler's own check on `state`, not the FSM's `409`. The precedent is
   `DELETE /v1/projects/{id}`, which is likewise no action. The payoff is that
   the workspace `enter` opens is read-only *for free*: an archived task offers
   no actions, so there is nothing to withhold and no flag saying so.

7. **Events are not purged, and a delete emits one.** `events` has no foreign
   keys and its `id` is the SSE `Last-Event-ID` cursor. `DeleteProjectCascade`
   purges a project's event rows because the project's entire history is going
   and every one of them would afterwards reference nothing at all; deleting one
   archived row is not that. So the historical rows stay, and each delete
   appends a durable `task.deleted` / `chat.deleted` event carrying the id. PR
   D's "there is no separate `task.archived` type" does not reach this: that
   type was redundant with `task.state_changed`, and a delete has no state to
   change to, so without a type no other client ever learns the row is gone. A
   chat's events carry no `chat_id` column at all — `chatEvent` puts the id in
   the payload — which is a second reason not to try to purge them.

8. **No migration.** Every column and cascade this needs already exists:
   `archived_at` since `0001_init.sql`, the chat cascade since `0022_chats.sql`,
   the idempotency cascade since `0016_idempotency.sql`, the handoff
   `SET NULL` since `0023_chat_handoff.sql`. Chats measure their window over
   `updated_at`, which task 074 decision 6 and task 079 decision 2 both recorded
   as already being when a terminal chat ended; adding an `archived_at` column
   to restate that would be relitigating a decision twice recorded.

## What landed

| # | Piece | Status |
|---|---|---|
| 092.1 | `TaskFilter`/`ChatFilter` date bounds, chat paging, archived-only ordering | ✅ |
| 092.2 | `DeleteTaskCascade` / `DeleteChatCascade` with the four refusals and the two events | ✅ |
| 092.3 | `DELETE /v1/tasks/{id}` and `DELETE /v1/chats/{id}`, the new query parameters, the §13.4 exclusions | ✅ |
| 092.4 | apiclient: `DeleteTask`/`DeleteChat`, `ListChatsOptions`, the two bounds | ✅ |
| 092.5 | `vincent task delete` / `vincent chat delete`, with `--branch`, `--before` and `--json` | ✅ |
| 092.6 | The two archived TUI boards: the mode, the window, the pages, the delete | ✅ |
| 092.7 | `scripts/m15-gate.sh`, wired into `ci.yml` on all three platforms | ✅ |
