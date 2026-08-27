---
title: Changelog
description: Product-focused release history for vincent.
permalink: /changelog.html
---

# Changelog

This is the human-readable vincent release history. The generated [canonical CHANGELOG](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) remains the source used by release automation, while this page removes duplicate commit subjects and keeps the product impact clear.

## 0.7.0 — GitHub issues, live step status and signed macOS downloads

Released 2026-08-27.

### Added

- **Create a task straight from a GitHub issue.** On a project whose `origin` points at github.com, the new-task form gains an **issue row** with the same type-to-filter picker the project and workflow rows use. Picking an issue fills in the title, the body plus a trailing link line back to the issue, and any workflow field named `labels`, `assignee` or `milestone` — all into ordinary editable rows you can rewrite before creating. The issue also reaches your templates as `.Issue` (`.Number`, `.Title`, `.Body`, `.URL`, `.State`, `.Labels`, `.Author`, `.Assignee`, `.Milestone`), zero-valued when none is linked, and fan-out lanes inherit it. It is fetched once at creation and persisted on the task, so no step ever makes a network call. Also available as `vincent task add --github-issue N`, `vincent github issues`, `vincent github status`, and over the API. Vincent stores **no GitHub credential** — it prefers the `gh` CLI you already configured, falls back to `GITHUB_TOKEN`/`GH_TOKEN`, is strictly read-only, and simply hides the row when GitHub cannot be reached. ([#204](https://github.com/lezli01/vincent/pull/204))
- **A step can say what it is doing, in its own words.** Any `agent` or `command` step can run `vincent status "<one short line>"` from inside itself. The message shows live on the board's new `STATUS` column and on the attempt line in task detail, and the last value set stays on the finished attempt as its own account of how it went. It is opt-in per workflow — vincent never appends a protocol instruction to an agent's prompt — and it is deliberately *not* a failure reason, invisible to `if:` guards and `.Steps`. Messages are bounded rather than refused: one line, control characters stripped, 256 bytes, with writes within a second coalesced. A new durable `task.status_changed` SSE event means a client that reconnects recovers a message it missed. ([#205](https://github.com/lezli01/vincent/pull/205))
- **macOS downloads are signed, notarized and stapled.** Every darwin binary is codesigned with an Apple Developer ID identity under the hardened runtime and notarized before the release publishes, and a new `vincent_*_darwin_universal.pkg` installer is signed, notarized and **stapled** — the one artifact whose first launch works with no network. The Homebrew cask no longer strips the quarantine attribute, because there is now a real signature instead. A stable release fails outright without a signing identity; a dry run or a fork build warns and produces unsigned artifacts, so contributors still need no secrets. Windows is unchanged and its SmartScreen prompt stays documented. ([#196](https://github.com/lezli01/vincent/pull/196))
- **`vincent workflow init` writes your first workflow file.** `vincent workflow init <name> [--from <example>] [--project N]` writes a valid workflow into the right scope directory and prints the path — a commented one-agent-step skeleton by default, or any of the shipped `examples/*.yaml` with its comments intact. It never clobbers an existing file or a name another workflow in the same scope already declares. ([#203](https://github.com/lezli01/vincent/pull/203))
- **A cap on what one task may spend.** The new top-level `max_task_cost_usd` is checked against the task's rolled-up cost after every attempt, and blocks the task with `cost_limit` when it is over. It is a block, not a failure: no retry is consumed and a due retry does not fire. Default `0` (off); it counts one task, so a fan-out tree spends a multiple of it, and it is inert on agents that report no cost. ([#201](https://github.com/lezli01/vincent/pull/201))
- **Agent steps receive the `VINCENT_*` environment variables.** They were specified for command and check steps only, leaving an agent unable to name the task or step it was executing. This is what makes `vincent status` work from an agent's shell tool. ([#205](https://github.com/lezli01/vincent/pull/205))

### Changed

- **Vincent is once again released under the MIT License.** The source, documentation, release archives and package-manager metadata all carry the same permissive license, with no separate commercial-use restriction.
- **A failed attempt's result summary is finally on screen.** The agent's final message, or the tail of a command's output, had been recorded and served since the first release and rendered nowhere — so diagnosing a block always meant opening the transcript. It now appears under any attempt that did not succeed. ([#205](https://github.com/lezli01/vincent/pull/205))

### Fixed

- **Crash recovery can no longer kill an unrelated process.** It had compared the daemon's own wall clock at spawn against the kernel's start time for the PID, accepting anything within ±5 seconds — a narrow crash/PID-reuse window in which a stranger's process could be killed as an orphan. A spawn now journals an exact per-OS process identity beside the PID and recovery kills only on a byte-for-byte match. Rows written before the upgrade keep the old behaviour, and the rule underneath is unchanged: what cannot be proved is not killed. ([#195](https://github.com/lezli01/vincent/pull/195))
- **A question longer than 256 bytes can be answered again.** Field keys and answers keys shared one bound, but an answers key is the agent's verbatim question text, not something the caller chooses. Any longer question became unanswerable and the task sat in `awaiting_input` holding a concurrency slot until it was cancelled or timed out — routine on the one adapter that supports mid-run input. Answers keys now have their own bound. ([#198](https://github.com/lezli01/vincent/pull/198))
- **The workflows bundled with this repository pace their retries and no longer mistake a blip for an answer.** A single transient GitHub API failure could end a 75-minute wait outright. Poll loops are now paced, read a failed call as "no answer yet", and tolerate a run of them; two postconditions that raced the effect they check now wait for it to settle. The `release` workflow also blocks on either changelog being unwritten, and `verify-base` waits out pending CI instead of reading "not finished" as "no". ([#193](https://github.com/lezli01/vincent/pull/193))

## 0.6.0 — Finished-task operations, backups and durability

Released 2026-08-25.

### Added

- **Follow-up runs on a finished task.** A `done` or `aborted` task still owns its worktree and branch until you archive it. `follow_up` uses that window: give the task an agent prompt, a shell command or a workflow, and it runs on that task's own branch with its own step run, transcript, events and cost accounting. It never changes the task's verdict, and it is repeatable. Available as `F` in the TUI, `POST /v1/tasks/{id}/follow_up`, and `vincent task follow-up`. ([#183](https://github.com/lezli01/vincent/pull/183))
- **Ad-hoc repair agents for a blocked task.** `R` on a blocked task runs one throwaway agent in the task's existing worktree with the blocked step's failure context. It is the escape hatch for a block that retry, edit and skip cannot clear because the worktree itself is wrong. The repair decides nothing — the task returns to `blocked` at the same step — and it does not consume the retry budget. ([#175](https://github.com/lezli01/vincent/pull/175))
- **`vincent daemon backup` and `vincent daemon restore`.** One archive holds a consistent copy of the database, every transcript, `config.yaml` and your global workflows. The daemon takes it, so it is safe **while tasks are running** — unlike copying `vincent.db` by hand, which under WAL silently omits recent work. Restore runs against a stopped daemon and never deletes existing state. ([#191](https://github.com/lezli01/vincent/pull/191))
- **Each agent's usage window is visible.** When an agent CLI stops because its quota for the window is spent, the board badges the agent with the reset time, the daemon view names it, and the new-task form warns before you queue more work against it. `→` marks a reset the CLI stated and `≈` one vincent estimated, so an estimate is never presented as a fact. Advisory only: nothing is refused. ([#180](https://github.com/lezli01/vincent/pull/180))
- **`retry_backoff` paces step retries.** A step or a workflow's defaults may carry `retry_backoff: 30s`. The task returns to `queued`, **gives up its concurrency slot** so other work keeps running, and re-runs itself when the wait is over. The default is `0s`, so existing workflows are unchanged. ([#184](https://github.com/lezli01/vincent/pull/184))
- **The `create-workflow` built-in.** A workflow whose deliverable is another workflow, so a first workflow does not have to be written by hand. Its agent step carries the published vincent Workflows authoring skill and writes into a live registry directory — the project's `.vincent/workflows`, or the global one. ([#171](https://github.com/lezli01/vincent/pull/171))
- **The database reports its own footprint.** `vincent doctor`, the TUI daemon view and `GET /v1/info` now show the on-disk size including the WAL and SHM sidecars, row counts biggest-table-first, stored workflow bytes and how far the event history reaches. Purely informational — nothing prunes and no threshold exists. ([#190](https://github.com/lezli01/vincent/pull/190))

### Fixed

- **Crash recovery no longer re-queues a task it could not close.** Recovery ran as two independent sweeps, so a failed step-run finalize was logged and walked past while the task went back to `queued` anyway — and the scheduler then started a second attempt against one the database still called `running`. Recovery is now atomic per task and fail-closed: an unreconcilable task is left as found and the daemon refuses to start. ([#189](https://github.com/lezli01/vincent/pull/189))
- **A step whose output could not be captured is no longer reported as a success.** An over-long output line stopped capture dead and left the step `succeeded` on its exit code alone. Long lines are now captured in bounded pieces that rejoin in order, and genuine evidence loss fails the attempt with `transcript_io_error` or `agent_protocol_error` instead. ([#178](https://github.com/lezli01/vincent/pull/178))
- **Reconnecting to the event stream no longer stalls the daemon.** A `Last-Event-ID` resume read the whole backlog into memory in one query, holding the daemon's single SQLite connection for the length of the scan. The catch-up now reads in fixed pages; what a client receives is unchanged. ([#188](https://github.com/lezli01/vincent/pull/188))
- **`config.yaml` is no longer created world-readable.** It held `0644` on Linux and macOS, exposing literal `environment.set` values to any other local account. It is now `0600` in a `0700` directory, and every daemon start re-tightens an existing installation and says so. ([#185](https://github.com/lezli01/vincent/pull/185))
- **A request body is now exactly one JSON document, and it is bounded.** Trailing content was silently discarded, so a client that framed two documents into one request got a `201` for work vincent never saw. Trailing content is now `400`, bodies are read under a fixed bound with a new `413`, and a non-JSON `Content-Type` is `415`. ([#186](https://github.com/lezli01/vincent/pull/186))
- **Workflow loading is bounded and refuses non-regular files.** A named pipe in a registered repository's `.vincent/workflows/` could park the loader in `open()` forever, and a symlink was followed out of the repository. Sources are now capped at 1 MiB and non-regular entries are listed as invalid with a reason. ([#172](https://github.com/lezli01/vincent/pull/172))
- **A human action racing scheduler admission no longer returns `409`.** Cancelling or pausing a queued task at the moment the scheduler started it failed, though both actions are valid from `running` too. An action that loses its compare-and-swap is re-applied once from the state it lost to. ([#176](https://github.com/lezli01/vincent/pull/176))
- **Simultaneous worktree creation in one project.** Tasks admitted at the same moment no longer fail each other's `git worktree add`; creation and cleanup are serialized per project. Fan-out was the reliable way to hit this. ([#174](https://github.com/lezli01/vincent/pull/174))
- **`vincent task ls` reports each task's branch**, and documentation that promised branches are never deleted was corrected — archiving does delete a branch with no commits past its base. ([#177](https://github.com/lezli01/vincent/pull/177))
- **The documentation site renders again.** Jekyll's Liquid parser was evaluating the Go-template expressions the reference pages exist to document. Template-carrying pages are wrapped in `raw` blocks, internal engineering records are excluded from the build, and the changelog and contributing pages are published and linked.

## 0.5.0 — Workflow authoring and structured task input

Released 2026-08-22.

### Added

- **vincent Workflows agent skill.** The portable authoring skill helps agents design cost-aware workflows, prefers deterministic commands and native control flow, and asks about human gates, interaction, acceptance checks, side effects and failure policy before generating a workflow. ([#165](https://github.com/lezli01/vincent/pull/165))
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
