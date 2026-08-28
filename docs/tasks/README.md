# Engineering work records

These are maintainer planning and decision records, not the product
documentation. If you want to install or use vincent, start with the
[feature tour](../features.md) or [documentation home](../README.md).

One document is kept per substantial piece of work, numbered in the order it
was opened and carrying both its task list and the decisions behind it.
Completed records stay in place so pull requests and code comments retain stable
citations. Current user-visible behavior belongs in the user documentation;
the living engineering specification records implementation contracts.

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
| [014](014-workflow-fan-out.md) | Parallel steps and workflow fan-out | ✅ done (14/14) |
| [015](015-conditional-steps.md) | Conditions between steps (`if:`, `type: condition`) | ✅ done (10/10) |
| [016](016-workflow-loops.md) | Loops in workflows (`type: loop`, `type: break`) | ✅ done (10/10) |
| [017](017-workflow-visualization.md) | Workflow visualization in the TUI | ✅ done (9/9) |
| [018](018-control-flow-review.md) | Control-flow review: four correctness fixes | ✅ done (9/9) |
| [019](019-workflow-includes.md) | Including one workflow in another (`type: include`) | ✅ done (10/10) |
| [020](020-guided-takeover-layouts.md) | Guided takeover layouts for task, project, and workflow views | ⚠ verification blocked (6/7) |
| [021](021-package-distribution-channels.md) | WinGet, Scoop, mise, deb, and rpm distribution | ⚠ verification blocked (5/7) |
| [022](022-workflow-fields.md) | Workflow-declared task fields | ⚠ verification blocked (6/7) |
| [023](023-vincent-workflow-authoring-skill.md) | Portable, cost-aware vincent workflow authoring skill | ✅ done (10/10) |
| [024](024-create-workflow-builtin.md) | `create-workflow`, a built-in that writes workflows | ✅ done (7/7) |
| [025](025-ad-hoc-repair-agent.md) | An ad-hoc repair agent for a blocked step | ✅ done (8/8) |
| [026](026-agent-quota-visibility.md) | Reporting each agent's usage-quota state in the daemon and TUI | ✅ done (7/7) |
| [027](027-follow-up-runs.md) | Follow-up runs on a done or aborted task | ✅ done (9/9) |
| [028](028-retry-backoff.md) | `retry_backoff`: pacing step retries through task 003's admission hold | ✅ done (5/5) |
| [029](029-database-size-reporting.md) | Reporting the database's footprint, row counts and retention span | ✅ done (6/6) |
| [030](030-daemon-backup-and-restore.md) | `vincent daemon backup` / `restore` | ✅ done (6/6) |
| [031](031-native-process-identity.md) | Exact native process identity for the §12.4 PID-reuse guard | ✅ done (5/5) |
| [032](032-macos-notarization.md) | macOS Developer ID signing and notarization | ✅ built, never activated (6/6, 032.7 dropped) |
| [033](033-task-cost-cap.md) | `max_task_cost_usd`: a per-task cost cap enforced at attempt boundaries | ✅ done (4/4) |
| [034](034-workflow-init.md) | `vincent workflow init`, a CLI on-ramp for authoring a workflow | ✅ done (5/5) |
| [035](035-github-issue-selection.md) | Select a GitHub issue when creating a task | ✅ done (12/12) |
| [036](036-step-status-message.md) | A step-authored status message, live and terminal | ✅ done (7/7) |
| [037](037-update-workflows-builtin.md) | `update-workflows`, a built-in that maintains the ones you have | ✅ done (5/5) |
| [038](038-release-signing-posture.md) | Release signing posture for an MIT project | ⚠ blocked on the owner (1/7) |
| [039](039-unsigned-releases-by-default.md) | A missing certificate must not destroy a release | ⚠ verification blocked (6/7) |
| [040](040-api-idempotency-keys.md) | Idempotency keys for `POST /v1/tasks` | ✅ done (7/7) |
| [041](041-adapter-compatibility-health.md) | Adapter version compatibility and protocol health | ✅ done (5/5) |
| [042](042-gosec-static-analysis.md) | gosec in the normal static-analysis gate, with reviewed suppressions | ✅ done (5/5) |
| [043](043-workflow-origin.md) | Persisting where a task's workflow definition came from | ✅ done (6/6) |

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
  is the part that earns its keep later. A recorded decision remains binding
  until it is explicitly superseded with the new reasoning.
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
