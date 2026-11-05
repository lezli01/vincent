# Task 143 gate — auto-archive on issue close

**Acceptance (task 143.5):** when an issue reaches `closed`, by the API or by
a sync from GitHub, the daemon archives the settled tasks it started, with
`archived_by: "issue_closed"` on the archive event; the unattended archive
deletes an empty branch locally and never on the remote, never forces a dirty
worktree — which `vincent doctor` then reports as held, without a problem —
archives a task that was unsettled at the close once it settles, archives
nothing of an issue reopened before its task settled, and does nothing at
all with `archive_tasks_on_issue_close: false`.

## Scripted: `scripts/143-gate.sh`

Six scenarios over curl, `vincent doctor --json` and git, each with its own
daemon, run in `ci.yml`'s `gate-group` (group 1) on Linux, macOS and Windows.
No agent CLI is involved: every step is an `exit 0` command step or a manual
gate, and the GitHub half is `cmd/fakegh` installed as `gh`.

| # | Scenario | Asserts |
|---|---|---|
| 1 | close by API | `delete_remote_branch_on_archive: true`; a done main task with no commits, its branch pushed to a bare `origin`, reaches `archived` after `POST /v1/issues/{id}/close`; its `task.state_changed → archived` event carries `archived_by: "issue_closed"` and the `issue_id`; the local branch is gone and `git ls-remote origin` still lists it (decision 6) |
| 2 | close while running | a task waiting at a manual gate is still `awaiting_gate` after the close (ordering barrier); approved, it reaches `archived` from `done` |
| 3 | dirty worktree | an untracked file in a done task's worktree: after the close and the barrier the task is still `done`, doctor's `issue_archive.held` lists it with `dirty` among its `reasons`, `problems` is empty and doctor exits 0 (decision 16), and the file is still there |
| 4 | sync close | an issue imported from the fake GitHub is closed in its corpus; after `POST /v1/projects/{id}/issues/sync` it reads `closed`, its done task is `archived` with `archived_by: "issue_closed"`, and the fake's argv log shows no write |
| 5 | reopen before settle | closed then reopened while its task waits at a manual gate; approved, the task reaches `done` and is still `done` once the barrier is archived |
| 6 | key off | with `archive_tasks_on_issue_close: false` a closed issue's done task stays `done` over a 10 s window, and doctor's `issue_archive` is `enabled: false` with both lists empty |

The **ordering barrier** in scenarios 2, 3 and 5 is a second issue of the
same project whose done task must archive: closed after the asserted event
and waited for. The worker walks the event table in order from one cursor,
so once the barrier is archived the earlier events have been processed.
Scenario 6 has no barrier — with the key off the worker reads nothing — and
is the gate's one bounded-window negative assertion.

## Runs

| Date | Where | Result | By |
|---|---|---|---|
| 2026-11-05 | `scripts/143-gate.sh` on macOS (darwin/arm64) | PASS, all six scenarios | task 143.5 |
