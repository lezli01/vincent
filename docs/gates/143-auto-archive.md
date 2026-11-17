# Task 143 gate — auto-archive on issue close

**Acceptance (task 143.5):** when an issue reaches `closed`, by the API or by
a sync from GitHub or Jira, the daemon archives the settled tasks it started, with
`archived_by: "issue_closed"` on the archive event; the unattended archive
deletes an empty branch locally and never on the remote, never forces a dirty
worktree — which `vincent doctor` then reports as held, without a problem —
archives a task that was unsettled at the close once it settles, archives
nothing of an issue reopened before its task settled, and does nothing at
all with `archive_tasks_on_issue_close: false`.

## Scripted: `scripts/143-gate.sh`

Seven scenarios over curl, `vincent doctor --json` and git, each with its own
daemon, run in `ci.yml`'s `gate-group` (group 1) on Linux, macOS and Windows.
No agent CLI is involved: every step is an `exit 0` command step or a manual
gate, the GitHub half is `cmd/fakegh` installed as `gh`, and the Jira half
is `cmd/fakejira` on a loopback port, edited only through its REST routes.

| # | Scenario | Asserts |
|---|---|---|
| 1 | close by API | `delete_remote_branch_on_archive: true`; a done main task with no commits, its branch pushed to a bare `origin`, reaches `archived` after `POST /v1/issues/{id}/close`; its `task.state_changed → archived` event carries `archived_by: "issue_closed"` and the `issue_id`; the local branch is gone and `git ls-remote origin` still lists it (decision 6) |
| 2 | close while running | a task waiting at a manual gate is still `awaiting_gate` after the close (ordering barrier); approved, it reaches `archived` from `done` |
| 3 | dirty worktree | an untracked file in a done task's worktree: after the close and the barrier the task is still `done`, doctor's `issue_archive.held` lists it with `dirty` among its `reasons`, `problems` is empty and doctor exits 0 (decision 16), and the file is still there |
| 4 | sync close | an issue imported from the fake GitHub is closed in its corpus; after `POST /v1/projects/{id}/issues/sync` it reads `closed`, its done task is `archived` with `archived_by: "issue_closed"`, the project's sync status shows no pending, failed or conflicting write-back, and the fake's argv log shows no write |
| 5 | reopen before settle | closed then reopened while its task waits at a manual gate; approved, the task reaches `done` and is still `done` once the barrier is archived |
| 6 | key off | with `archive_tasks_on_issue_close: false` a closed issue's done task stays `done` over a 10 s window, and doctor's `issue_archive` is `enabled: false` with both lists empty |
| 7 | Jira sync close (Won't Do) | an issue imported from the fake Jira (VIN-1) has a done task; the gate POSTs transition `31` (Done) with resolution "Won't Do" to the fake's REST API, then `POST /v1/projects/{id}/issues/sync`; the issue reads `closed`/`not_planned`, the task is `archived` with `archived_by: "issue_closed"` and the `issue_id`, the project's sync status shows no pending, failed or conflicting write-back, and the fake's request log has no transition POST for VIN-1's id ([#988](https://github.com/lezli01/vincent/issues/988)) |

The **ordering barrier** in scenarios 2, 3 and 5 is a second issue of the
same project whose done task must archive: closed after the asserted event
and waited for. The worker walks the event table in order from one cursor,
so once the barrier is archived the earlier events have been processed.
Scenario 6 has no barrier — with the key off the worker reads nothing — and
is the gate's one bounded-window negative assertion.

Scenarios 4 and 7's "no write-back" has its own barrier, in the outbox: a
write-back row is written in the transaction that closes the issue, and the
drain settles it only after its request has reached the fake. So with the
issue read `closed`, sync status's `writes_pending`, `writes_failed` and
`writes_conflict` all `0`, read *before* the fake's log, leave no write-back
the log could have missed.

## Runs

| Date | Where | Result | By |
|---|---|---|---|
| 2026-11-05 | `scripts/143-gate.sh` on macOS (darwin/arm64) | PASS, all six scenarios | task 143.5 |
| 2026-11-17 | `scripts/143-gate.sh` on macOS (darwin/arm64) | PASS, all seven scenarios; scenario 7 also alone (`VINCENT_GATE_SCENARIO=7`) | #988 |
