# Tasks

Planned and in-flight work. One document per piece of work, numbered in the
order they were opened, each carrying its own task list and — more importantly —
the **decisions** behind it.

This replaced the per-version ledgers v0 was built with
(`docs/versions/v0/tasks.md`, now [history/v0-tasks.md](../history/v0-tasks.md)).
That file reached 279 KB because one document accumulated every decision and
every verification note for a whole release. Splitting by piece of work keeps
each one readable; the spec, which is *not* versioned, stays the single
description of how the system actually behaves.

## Index

| ID | Work | Status |
|---|---|---|
| [001](001-configurable-branch-names.md) | Configurable branch names | ✅ done (11/11) |
| [002](002-homebrew-tap.md) | Homebrew tap for macOS | ✅ done (6/6) |
| [003](003-usage-limit-classification.md) | Classify agent usage limits and auth expiry | ✅ done (7/7) |
| [004](004-go-toolchain-pin.md) | Pin the Go toolchain and automate its patch bumps | ✅ done (3/3) |
| [005](005-orphaned-worktree-gc.md) | Reclaim orphaned worktrees: `vincent gc` and a startup reconcile | ✅ done (8/8) |
| [006](006-vincent-doctor.md) | `vincent doctor` — one diagnostic report | ✅ done (8/8) |
| [007](007-release-please.md) | Release Please automation | ✅ done (6/6) |
| [008](008-archive-branch-cleanup.md) | Archive cleanup: delete a branch with no commits past its base | ✅ done (7/7) |
| [009](009-configurable-tasks-view.md) | A configurable tasks view, grouped by project then workflow | ✅ done (6/6) |
| [010](010-workflow-platform-restrictions.md) | Restricting a workflow to platforms (`platforms:`) | ✅ done (6/6) |
| [011](011-bulk-task-selection.md) | Selecting several tasks for one action | ✅ done (5/5) |
| [012](012-diff-view-file-grouping.md) | A diff tab grouped by file, folded shut | ✅ done (5/5) |
| [013](013-interactive-workflow-agent-gating.md) | Gating workflows that require mid-run interaction | ✅ done (8/8) |
| [014](014-workflow-fan-out.md) | Parallel steps and workflow fan-out | 🚧 in progress (3/14) |

## How to add and update a task document

- **Filename:** `NNN-kebab-case-title.md`, taking the next free number. The
  number is the citable identity: a PR says "closes 001.4", which is unambiguous
  in a way "closes T4" is not.
- **Task IDs** inside a document are `NNN.n` — `001.1`, `001.2` — numbered in
  dependency order. A task with a non-obvious dependency carries an explicit
  `Depends:` tag.
- **Statuses:** `- [ ]` not started · `- [~]` in progress · `- [x]` done (append
  `✓ YYYY-MM-DD`) · `- [!]` blocked (append a one-line reason).
- Mark a task `[~]` **before** starting it. Mark it `[x]` only when every
  done-criterion is verified — tests actually run, not assumed.
- **Update the index row in the same edit** as any status change.
- **Never delete a task.** Descoped: strike it through (`~~001.4~~`) with a dated
  note. Newly discovered work: append with the next free ID.
- **Decisions go in the document, dated, with the alternative they beat.** This
  is the part that earns its keep later. A decision recorded here is binding in
  the same way v0's phase decisions are — don't relitigate one without saying so
  explicitly.
- **Finished documents stay put.** Status lives in the markers and the index, so
  there is no "done" folder to move things to and no link to break.
- No time estimates are tracked — only status.

## What belongs here, and what does not

A task document is for work big enough that the *reasoning* needs to outlive the
pull request — a feature, a migration, a design change with trade-offs someone
will question in six months. A one-line fix does not need one; its commit
message is the record.

Behaviour changes land in [the spec](../spec.md) as dated in-place amendments, in
the same pull request as the code that makes them true — never ahead of it, or
the spec describes a system that does not exist yet.
