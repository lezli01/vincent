# 155 — Archive a task that has sat finished for a while

**Status:** ✅ done (2/2) · **Opened:** 2026-11-19

A finished task keeps its worktree until somebody archives it, so a board fills
with `done` and `aborted` rows and a disk with their worktrees. Task 143 made
the daemon archive a closed issue's tasks; this archives every other finished
task on a clock: a root that has sat in `done` or `aborted` for
`archive_finished_tasks_after` (default `12h`) is archived unattended.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **155.1** The `archive_finished_tasks_after` key (config, bootstrap,
  `GET`/`PATCH /v1/config`, `vincent config`, the TUI's config keys and
  Settings screen), `Store.FinishedArchiveDue`, the timer worker
  `internal/finishedarchive` wired in `daemon.Run` beside the issue worker,
  and the docs: configuration, task lifecycle, API events, issues guide, TUI
  guide, features, and the §12.3 / §13.3 amendments. ✓ 2026-11-19
- [x] **155.2** Re-capture `tui-settings.png` and `tui-settings-branch.png`
  with `scripts/screenshots.sh`: the Settings screen gained a row under
  "Archiving a task", and its count went from 61 to 62. ✓ 2026-11-19

## Decisions

Confirmed by the author on 2026-11-19.

1. **On by default, at `12h`, for existing installs too.** Scheduled backups
   (task 115 decision 1) went the other way, off so an upgrade spends no disk.
   Here an upgrade *frees* disk, and nothing committed is lost: an archive
   keeps a branch with commits past its base (§10), and an unattended archive
   never forces a dirty worktree. `0` is off.
2. **`done` and `aborted` both start the clock** — the pair `archive` accepts
   (§6). An aborted task's branch with commits survives its archive.
3. **Tasks of an open issue are in scope.** This **supersedes, in part,** task
   143 decision 1 ("Archive stays a human action everywhere else … nothing here
   archives a task whose issue is open"): the author asked for every finished
   task to age out. What does not change is why 143 held an issue's tasks, so
   an issue's **main** task keeps that hold — while another root of the issue
   is unsettled, an issue chat is live, a handoff item claims the issue (143
   decision 10: a `return` follows up the issue's latest main task, which an
   archived one refuses), or a side task or merge-back of the issue is not yet
   archived (143 decision 5 and review F3: a merge-back lands on the main
   worktree). The last is a hold, not an order, because each task ages on its
   own clock. Side tasks, merge-backs and issue-less tasks are never held.
4. **The clock is the last finish.** `finished_at` is restamped by every entry
   into `done` or `aborted`, so a follow-up (task 027) restarts it: a task
   someone is still working with stays on the board.
5. **A timer, not an event cursor.** What makes a task due is time passing,
   which records no event. The worker sweeps at start (a deadline passed while
   the daemon was down is met on return) and every minute. A one-minute lag on
   a twelve-hour clock is immaterial, and the query is one statement.
6. **A refusal is retried hourly, warned once.** A refused archive costs a
   `git status`, and cleaning a dirty worktree records nothing to watch, so
   the worker remembers the refusal in memory for an hour rather than retrying
   every sweep; repeats log at debug. A task that leaves the due set is
   forgotten. A restart forgets everything, which costs one more warning.
7. **`archived_by: "finished_after"`** on the archive event (§13.3), beside
   143's `issue_closed`. It is not the issue worker's own marker, so it wakes
   that worker as a human archive does (#985) — which is what releases a main
   task the issue worker held behind side work.

## Not done

- No `vincent doctor` row for tasks the timer could not archive; the warning
  in `daemon.log` is the only report. 143.4's row is the model if one is
  wanted.
- No gate script: the unit tests drive the real runner and real worktrees
  (`internal/api/finishedautoarchive_test.go`), and a gate would have to wait
  out or rewrite a clock.
