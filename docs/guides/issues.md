---
title: Working an issue
---

# Working an issue

An issue is a project's unit of intent: a bug or a feature, filed in vincent or
[imported from GitHub](../features.md#start-from-an-issue). Work on it is done
by tasks created from it, and those tasks share **one main line of work** — a
main branch and a main worktree — so each task builds on what the last one
left instead of starting over from the project's base branch. This page
explains that model: where an issue sits on the board, what the main worktree
is and who holds it, how to run work beside it in a side task, and what to do
when that work merges back with a conflict.

The fields and routes are listed in the [issues reference](../reference/api.md#issues);
this page is the story behind them.

## Lanes

Every issue is on one of four lanes, derived by vincent from the issue and its
**root** tasks (fan-out lanes never count). A lane is never set by hand:
starting a task, finishing one, or closing the issue is what moves it.

| Lane | When |
|---|---|
| `open` | The issue is open and no root task is unfinished or done: no task yet, or only cancelled ones |
| `in_progress` | The issue is open and some root task is not `done`, `aborted` or archived — `paused` included |
| `hand_off` | The issue is open, every root task has finished, and at least one finished `done`. The work is back with you: review it and close the issue. Nothing closes it for you |
| `done` | The issue is closed — completed, not planned or a duplicate, whatever its tasks are doing. Reopening it returns it to the lane its tasks give it |

Beside the lane, **attention** — the `!` badge — means one of the issue's root
tasks is waiting on you: `awaiting_input`, `awaiting_gate` or `blocked`. A
`paused` task carries none. Attention can show on any lane, `done` included.

The TUI's issues screen stacks the four lanes as foldable sections and hides
`done` until `s` shows it; its keys are in [Using the TUI](tui.md#issues). From
a shell, `vincent issue ls --lane hand_off` lists one lane (the flag repeats);
the API takes `?lane=` on `GET /v1/issues`, and the MCP tool `issue_list` takes
`lane` in its `query`.

## The main worktree

A task created from an issue is the issue's **main** task unless you ask
otherwise. The first main task's branch becomes the issue's **main branch**,
and every later main task runs on that branch, in the same directory.

**One occupant at a time.** The main worktree is *occupied* while either of
these holds it:

- a main task that has started and not yet finished — even while it is
  `blocked`, `paused` or `awaiting_gate`;
- a main task that finished `done` or `aborted` but still has its worktree
  open in a [chat](../reference/api.md#a-chat-on-a-stopped-task) linked to it.

A main task created while the worktree is occupied is **queued, never
refused**. It waits `queued` with no error and no reason, and is admitted once
the occupant lets go. `vincent task add` says so on stderr:

```sh
vincent task add --project 1 --issue 7
```

```
queued behind #61 (main worktree busy); pass --separate-worktree to run now
task 62 created: Crash on cold start (adhoc, branch vincent/61-crash-on-cold-start)
  from issue 7: Crash on cold start
```

The branch is task 61's: task 62 continues the issue's line rather than
cutting a new one.

**The hand-over.** When the next main task is admitted, the finished main task
before it hands over its worktree as it stands — **uncommitted changes
included**. The new task's `base_sha` is the branch's tip at that moment, and
the earlier task's commits and diff stop there, so each task shows its own
work.

A main task that blocks or waits at a gate keeps the worktree, so every main
task queued behind it waits too until you answer, retry, skip or cancel it.
If you need work done meanwhile, run it as a side task.

**Seeing the occupant.** `vincent issue show 7 --json`, `GET /v1/issues/7` and
the MCP tool `issue_get` carry it:

```json
"main_worktree": { "branch": "vincent/61-crash-on-cold-start", "occupant_task_id": 61 }
```

`occupant_task_id` is `null` when nothing holds the worktree, and
`main_worktree` is absent while the issue has no main branch. The human output
of `vincent issue show` does not print it. `vincent task show` prints each
task's role: a `worktree` row reading `main` or `side`, and for a side task a
`merge` row reading `manual` or `agent`.

## Side tasks and merge-back

A **side task** runs now, beside the main line: in a worktree of its own, cut
from the issue's main branch, and merged back into it when it is done. Ask for
one with `--separate-worktree`:

```sh
vincent task add --project 1 --issue 7 --separate-worktree
vincent task add --project 1 --issue 7 --separate-worktree --merge agent
```

`--merge` picks how the merge-back handles a conflict:

- **`block`** (the default, shown as `manual`) — the merge runs automatically;
  on a conflict it stops and blocks for you. This is the same behaviour as a
  `fan_out` step's [`on_conflict: block`](workflows.md#merge-and-conflicts).
- **`agent`** — a built-in resolver, run with the side task's agent, model and
  effort, tries to resolve the conflict first. If it fails, or leaves conflict
  markers behind, the merge-back blocks exactly as `block` does.

A side task needs a main branch to fork from. On an issue with no main task
yet it is refused (`issue 7 has no main branch yet; create a main task
first`), and while the first main task is still queued or paused its branch is
not in git yet, which is refused as a `base_branch` that does not resolve.
Start a main task first. The side task is cut from the main branch as it is
on your machine: nothing is fetched and the main worktree is not touched.

**The merge-back task.** When a side task finishes `done` with commits of its
own, vincent creates a **merge-back** task: a queued main task of the issue,
titled `Merge task {side} into issue #{issue}`, running the workflow
`__merge_back`. That title is how you recognise it on the board. It waits for
the main worktree like any main task, then merges the side branch with
`--no-ff`. A clean merge ends `done`.

- A side task with no commits past where it was cut creates no merge-back, and
  neither does one whose branch is already on the main branch.
- A side task has at most one pending merge-back. A follow-up that finishes it
  again while one is pending creates no second one; the pending one merges the
  side branch's tip as it is when it runs.

## When a merge-back blocks

A merge-back blocks with one of these reasons. Each is in the
[failure reasons table](../reference/task-lifecycle.md#failure-reasons).

- [`merge_conflict`](../reference/task-lifecycle.md#failure-reasons) — the
  merge conflicted, and the issue's main worktree is left mid-merge on
  purpose. Resolve the files there, stage them, and **retry**: the merge-back
  commits your resolution. Or **skip** it, ending it without merging, or
  **cancel** it. Both run `git merge --abort`, so the next main task gets a
  clean worktree; the side task's branch stays where it is.
- [`merge_source_missing`](../reference/task-lifecycle.md#failure-reasons) —
  the side task was deleted, or its branch is gone from git. There is nothing
  to merge: skip or cancel.
- [`merge_target_missing`](../reference/task-lifecycle.md#failure-reasons) —
  every main task of the issue was archived and the main branch was deleted
  too. There is nothing to merge into: skip or cancel.

Two more can block any main task, a merge-back included, as it takes over the
worktree:

- [`issue_branch_checked_out`](../reference/task-lifecycle.md#failure-reasons) —
  the issue's main branch is checked out in the project's own checkout. An
  issue's main tasks never run there. Switch that checkout to another branch,
  then retry.
- [`repo_operation_in_progress`](../reference/task-lifecycle.md#failure-reasons) —
  the previous main task left a merge, rebase, cherry-pick, revert or bisect
  half done in the worktree, so it keeps the directory. Finish or abort the
  operation there, then retry.

If a chat on the previous main task holds the directory instead, see
[Projects and worktrees](troubleshooting.md#projects-and-worktrees) in
Troubleshooting.

## Follow-ups, chats, deleting and archiving

**A predecessor is history.** Once a finished main task has handed the
worktree to a later one, a follow-up on it is refused, and so is opening a
chat on it: [`409 issue_worktree_moved`](../reference/api.md#errors), with
`holder_task_id` naming the task that holds the worktree now. The CLI names
it too. Continue in that task, or start a new main task.

**Deleting an issue** is refused with `409 issue_has_live_main_task` while any
of its main or side tasks has not finished — a side task's completion creates
a merge-back on the issue, so the issue must still be there. Finish or cancel
the task named in the error, then delete again.

**Archiving.** While another unarchived main task carries the main branch,
archiving a main task never deletes the branch. When every main task of the
issue has been archived, the issue has no main branch any more and the next
main task cuts a fresh one. A merge-back created before that checks the old
branch out again and merges into it, or blocks `merge_target_missing` if it
was deleted.

## From the API, MCP, triggers and a chat

Everything above is available from every surface:

- **API.** `POST /v1/tasks` with `issue_id` creates a main task; add
  `"merge_back": {"on_conflict": "block"}` or `"agent"` for a side task. A
  task reads back `issue_worktree` (`main` or `side`) and `merge_back`; a
  main task created behind an occupant names it in the `201` as
  `main_worktree_occupant_task_id`; an issue carries `main_worktree`. See
  [The issue's main branch](../reference/api.md#the-issues-main-branch).
- **MCP.** `task_create` takes the same body, and `issue_get` returns the
  same issue, `main_worktree` included. See
  [What the tools are](mcp.md#what-the-tools-are).
- **Triggers.** A [`create_task`](triggers.md#create_task) action with
  `issue` takes `merge_back: {on_conflict: block|agent}`.
- **A chat handoff.** [`vincent chat handoff --issue ID`](../reference/cli.md#vincent-chat-handoff)
  on an issue that already has a main branch makes the task a side task,
  merged back with `block` unless `--merge agent` asks for the resolver. On an
  issue with no main branch yet, the chat's branch becomes the main branch,
  the task is the issue's main task, and `--merge` does nothing.

In the TUI, a task started from an issue is a main task; the choice of a side
task is made from the surfaces above.
