---
title: Changelog
description: Product-focused release history for Vincent.
permalink: /changelog.html
---

# Changelog

This is the human-readable Vincent release history. The generated [canonical CHANGELOG](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) remains the source used by release automation, while this page removes duplicate commit subjects and keeps the product impact clear.

## 0.6.0 — Finished-task operations, backups and durability

Released 2026-08-25.

### Added

- **Follow-up runs on a finished task.** A `done` or `aborted` task still owns its worktree and branch until you archive it. `follow_up` uses that window: give the task an agent prompt, a shell command or a workflow, and it runs on that task's own branch with its own step run, transcript, events and cost accounting. It never changes the task's verdict, and it is repeatable. Available as `F` in the TUI, `POST /v1/tasks/{id}/follow_up`, and `vincent task follow-up`. ([#183](https://github.com/lezli01/vincent/pull/183))
- **Ad-hoc repair agents for a blocked task.** `R` on a blocked task runs one throwaway agent in the task's existing worktree with the blocked step's failure context. It is the escape hatch for a block that retry, edit and skip cannot clear because the worktree itself is wrong. The repair decides nothing — the task returns to `blocked` at the same step — and it does not consume the retry budget. ([#175](https://github.com/lezli01/vincent/pull/175))
- **`vincent daemon backup` and `vincent daemon restore`.** One archive holds a consistent copy of the database, every transcript, `config.yaml` and your global workflows. The daemon takes it, so it is safe **while tasks are running** — unlike copying `vincent.db` by hand, which under WAL silently omits recent work. Restore runs against a stopped daemon and never deletes existing state. ([#191](https://github.com/lezli01/vincent/pull/191))
- **Each agent's usage window is visible.** When an agent CLI stops because its quota for the window is spent, the board badges the agent with the reset time, the daemon view names it, and the new-task form warns before you queue more work against it. `→` marks a reset the CLI stated and `≈` one Vincent estimated, so an estimate is never presented as a fact. Advisory only: nothing is refused. ([#180](https://github.com/lezli01/vincent/pull/180))
- **`retry_backoff` paces step retries.** A step or a workflow's defaults may carry `retry_backoff: 30s`. The task returns to `queued`, **gives up its concurrency slot** so other work keeps running, and re-runs itself when the wait is over. The default is `0s`, so existing workflows are unchanged. ([#184](https://github.com/lezli01/vincent/pull/184))
- **The `create-workflow` built-in.** A workflow whose deliverable is another workflow, so a first workflow does not have to be written by hand. Its agent step carries the published Vincent Workflows authoring skill and writes into a live registry directory — the project's `.vincent/workflows`, or the global one. ([#171](https://github.com/lezli01/vincent/pull/171))
- **The database reports its own footprint.** `vincent doctor`, the TUI daemon view and `GET /v1/info` now show the on-disk size including the WAL and SHM sidecars, row counts biggest-table-first, stored workflow bytes and how far the event history reaches. Purely informational — nothing prunes and no threshold exists. ([#190](https://github.com/lezli01/vincent/pull/190))

### Fixed

- **Crash recovery no longer re-queues a task it could not close.** Recovery ran as two independent sweeps, so a failed step-run finalize was logged and walked past while the task went back to `queued` anyway — and the scheduler then started a second attempt against one the database still called `running`. Recovery is now atomic per task and fail-closed: an unreconcilable task is left as found and the daemon refuses to start. ([#189](https://github.com/lezli01/vincent/pull/189))
- **A step whose output could not be captured is no longer reported as a success.** An over-long output line stopped capture dead and left the step `succeeded` on its exit code alone. Long lines are now captured in bounded pieces that rejoin in order, and genuine evidence loss fails the attempt with `transcript_io_error` or `agent_protocol_error` instead. ([#178](https://github.com/lezli01/vincent/pull/178))
- **Reconnecting to the event stream no longer stalls the daemon.** A `Last-Event-ID` resume read the whole backlog into memory in one query, holding the daemon's single SQLite connection for the length of the scan. The catch-up now reads in fixed pages; what a client receives is unchanged. ([#188](https://github.com/lezli01/vincent/pull/188))
- **`config.yaml` is no longer created world-readable.** It held `0644` on Linux and macOS, exposing literal `environment.set` values to any other local account. It is now `0600` in a `0700` directory, and every daemon start re-tightens an existing installation and says so. ([#185](https://github.com/lezli01/vincent/pull/185))
- **A request body is now exactly one JSON document, and it is bounded.** Trailing content was silently discarded, so a client that framed two documents into one request got a `201` for work Vincent never saw. Trailing content is now `400`, bodies are read under a fixed bound with a new `413`, and a non-JSON `Content-Type` is `415`. ([#186](https://github.com/lezli01/vincent/pull/186))
- **Workflow loading is bounded and refuses non-regular files.** A named pipe in a registered repository's `.vincent/workflows/` could park the loader in `open()` forever, and a symlink was followed out of the repository. Sources are now capped at 1 MiB and non-regular entries are listed as invalid with a reason. ([#172](https://github.com/lezli01/vincent/pull/172))
- **A human action racing scheduler admission no longer returns `409`.** Cancelling or pausing a queued task at the moment the scheduler started it failed, though both actions are valid from `running` too. An action that loses its compare-and-swap is re-applied once from the state it lost to. ([#176](https://github.com/lezli01/vincent/pull/176))
- **Simultaneous worktree creation in one project.** Tasks admitted at the same moment no longer fail each other's `git worktree add`; creation and cleanup are serialized per project. Fan-out was the reliable way to hit this. ([#174](https://github.com/lezli01/vincent/pull/174))
- **`vincent task ls` reports each task's branch**, and documentation that promised branches are never deleted was corrected — archiving does delete a branch with no commits past its base. ([#177](https://github.com/lezli01/vincent/pull/177))
- **The documentation site renders again.** Jekyll's Liquid parser was evaluating the Go-template expressions the reference pages exist to document. Template-carrying pages are wrapped in `raw` blocks, internal engineering records are excluded from the build, and the changelog and contributing pages are published and linked.

## 0.5.0 — Workflow authoring and structured task input

Released 2026-08-22.

### Added

- **Vincent Workflows agent skill.** The portable authoring skill helps agents design cost-aware workflows, prefers deterministic commands and native control flow, and asks about human gates, interaction, acceptance checks, side effects and failure policy before generating a workflow. ([#165](https://github.com/lezli01/vincent/pull/165))
- **Workflow-declared task fields.** Workflows can define ordered inputs with labels, descriptions, required flags, typed values and optional validation patterns. The TUI pre-renders them while still accepting additional undeclared fields. ([#163](https://github.com/lezli01/vincent/pull/163))

## 0.4.2 — Release verification hardening

- **RPM verification** now normalizes payload paths before extraction, avoiding GNU cpio warning exits while keeping validation isolated. ([#161](https://github.com/lezli01/vincent/pull/161))

## 0.4.1 — Package validation fixes

- **Release package verification** was hardened so provenance generation and Linux, macOS and Windows smoke tests complete correctly for published packages. ([#159](https://github.com/lezli01/vincent/pull/159))

## 0.4.0 — Better TUI workflows and broader distribution

- **Roomier responsive TUI workflows** added guided task creation plus persistent navigation for Projects and Workflows. ([#153](https://github.com/lezli01/vincent/pull/153))
- **More installation channels** added WinGet, Scoop, mise, deb and rpm distribution with checksummed native release packages. ([#158](https://github.com/lezli01/vincent/pull/158))

## 0.3.0 — Workflow language and task operations

- Reusable workflow composition with `type: include`.
- Platform restrictions and interactive-agent capability gating.
- Configurable task-board grouping, bulk actions and file-grouped diffs.
- Workflow graph visualization for structured control flow.
- Loops, breaks, conditions and `allow_failure` for data-driven execution.
- Parallel sub-steps and fan-out child tasks with isolated worktrees and merge-back.

## Older releases

Earlier releases are available in the [canonical changelog](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) and on [GitHub Releases](https://github.com/lezli01/vincent/releases). This page is intentionally edited for humans; the canonical file remains complete and machine-maintained.
