---
title: Changelog
description: Product-focused release history for vincent.
permalink: /changelog.html
---

# Changelog

This is the human-readable vincent release history. The generated [canonical CHANGELOG](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) remains the source used by release automation, while this page removes duplicate commit subjects and keeps the product impact clear.

## 0.7.0 — A command line for every action, notifications and a full-screen task workspace

Released 2026-08-29.

### Added

- **Every human action now has a command line.** `vincent task` gained `pause`, `resume`, `skip`, `approve`, `reject`, `retry`, `repair`, `archive` and `answer`, and `vincent project` gained `rm`. The recovery half of the product used to be reachable from the TUI and from `curl` and nowhere else, so a board full of tasks blocked on an agent auth outage could not be cleared from a script. It can now. ([#223](https://github.com/lezli01/vincent/pull/223))
- **A full-screen workspace for the task you are looking at.** The home view is the board and nothing else; opening a task gives it the whole terminal, with Steps & Attempts, Task Details, Output and Diff tabs, an attempt picker on the output, a metadata sidebar, and timeline entries that jump straight to the output they describe. ([#226](https://github.com/lezli01/vincent/pull/226))
- **The daemon can tell you it needs a human, with no client attached.** A `notify:` block in `config.yaml` names the states you care about — `blocked`, `awaiting_gate`, `awaiting_input`, `done` — and a command to run when a task enters one; the event envelope arrives on that command's stdin. Until now the only alert in vincent was a terminal bell that rang for `awaiting_input` alone, and only while a board was open. ([#225](https://github.com/lezli01/vincent/pull/225))
- **`vincent daemon logs` and `vincent task transcript`.** The two artifacts a failure is diagnosed from now have command lines, both with `-f` to follow. `daemon logs` reads the log file from disk and never contacts the daemon — which is the point rather than a shortcut, because the log is worth reading exactly when the daemon is not there to serve it. ([#224](https://github.com/lezli01/vincent/pull/224))
- **Create a task from a GitHub issue.** On a project whose `origin` points at github.com, vincent lists that repository's issues and prefills a task from the one you pick: the title, the body with a link back to the issue, and any workflow-declared fields whose names match the issue's `labels`, `assignee`, `milestone` or number. Everything lands in an ordinary editable row — nothing is locked, so a guess is reviewed before the task exists. ([#204](https://github.com/lezli01/vincent/pull/204))
- **A per-task cost cap.** `max_task_cost_usd` blocks a task with a new `cost_limit` reason at the next attempt boundary once its rolled-up spend crosses the ceiling. Cost was measured and displayed but never acted on, and an agent that loops *productively* — quick turns, modest output, no hang — trips neither the attempt timeout nor the transcript bound. Zero, the default, means no cap. ([#201](https://github.com/lezli01/vincent/pull/201))
- **A step can say what it is doing.** `vincent status "<message>"` from inside a `run:` body, or from an agent that was asked for it, puts a line on the board's STATUS column while the step runs, and the last line written stays on the finished attempt as that step's own account of how it went. A step that has been `running` for twenty-five minutes no longer has to be silent about it. ([#205](https://github.com/lezli01/vincent/pull/205))
- **`vincent workflow init` and `vincent workflow render`.** `init` writes a valid workflow into the right registry directory, optionally from one of the shipped examples, and refuses to damage anything already there. `render` dry-runs one: it executes every template a workflow file carries — prompts, `run:` bodies, checks, guards, loop items — against a synthetic or a real task, so a misspelled task field or step reference is caught in a second instead of costing a worktree, an admission slot and an agent run to find. ([#203](https://github.com/lezli01/vincent/pull/203), [#221](https://github.com/lezli01/vincent/pull/221))
- **`vincent task add --fields-file`** reads the task field map from a JSON object in a file, or on stdin with `-`. That is the form a generator produces, and the one that carries newlines, quotes and spaces without the shell having to survive them first. Repeatable `--field` still combines with it and wins name by name. ([#222](https://github.com/lezli01/vincent/pull/222))
- **Creating a task can be retried safely.** Send an `Idempotency-Key` header on `POST /v1/tasks` and a re-send after a timeout returns the task the first request created, instead of making a second task, a second worktree and a second agent run. The same key with a different body is refused with a `409`. Without the header nothing changes: two identical sends still make two tasks, which is what pressing enter twice means. ([#218](https://github.com/lezli01/vincent/pull/218))
- **Vincent now says whether it has ever been tested against the agent CLI you have installed.** Each adapter reports a `version_verdict` for your build — `tested`, `untested` or `incompatible` — alongside whether it can honor `restricted` on this machine, in `vincent doctor`, `GET /v1/agents`, `GET /v1/info` and the TUI's daemon view. It is advisory and blocks nothing; `untested` is the normal answer a few weeks after any vincent release. ([#220](https://github.com/lezli01/vincent/pull/220))
- **Every task records which workflow definition it actually ran** — the scope that won (`builtin`, `global`, `project` or `derived`), the file it came from, and a SHA-256 of that file's bytes as the registry loaded them. It is captured once at creation and never recomputed, so editing a workflow file later does not rewrite the history of the tasks that already ran it. ([#217](https://github.com/lezli01/vincent/pull/217))
- **The `update-workflows` built-in**, which brings a project's own workflows up to date with the features a new vincent release added, and the `prepare-release` workflow this repository uses on itself. ([#208](https://github.com/lezli01/vincent/pull/208))
- **"Why vincent is awesome"** — ten linked articles on the ideas behind the product, plus per-page titles and descriptions, canonical URLs, social cards, a sitemap and a custom 404 across the documentation site. ([#211](https://github.com/lezli01/vincent/pull/211))

### Changed

- **vincent is MIT-licensed again.** The separate commercial license is gone, from the repository, the documentation, the release archives and the package-manager metadata alike. ([#202](https://github.com/lezli01/vincent/pull/202))
- **Releases ship unsigned by design, and a missing certificate can no longer destroy one.** macOS code signing, notarization and a stapled `vincent_*_darwin_universal.pkg` are implemented and run whenever Developer ID certificates are configured; without them every signing step warns and the release publishes unsigned rather than failing at its first step. deb and rpm are unsigned by decision, and Windows Authenticode stays out of scope. cosign signatures and build provenance are unchanged, always present, and are not a substitute for either. macOS and Windows therefore prompt on first launch; the README and the platform pages say what to do about it. ([#196](https://github.com/lezli01/vincent/pull/196), [#210](https://github.com/lezli01/vincent/pull/210), [#216](https://github.com/lezli01/vincent/pull/216))
- **A `restricted` step bound for an agent that cannot restrict on this machine is now refused when you create the task**, not after it has spent a worktree, an admission and a retry. Cursor's restricted mode needs its CLI sandbox, which exists on macOS and Linux only. Nothing is quietly downgraded to full-auto: a restricted mode that silently is not restricted is worse than none. ([#220](https://github.com/lezli01/vincent/pull/220))
- **gosec runs on every build**, inside the lint gate that already existed, so `go run mage.go lint` is still one command and still the one CI runs on all three platforms. Every current finding was read individually and either fixed or suppressed with its reason at the site. No runtime behaviour, file permission or directory permission changed. ([#219](https://github.com/lezli01/vincent/pull/219))

### Fixed

- **Crash recovery can no longer kill an unrelated process.** The PID-reuse guard compared a wall-clock stamp taken by the daemon against kernel bookkeeping, accepting anything within five seconds as the same process, so in a narrow crash-and-reuse window a process that merely inherited the PID could be killed as an orphan. A spawn now journals a platform-native process identity beside the PID and recovery compares it exactly. ([#195](https://github.com/lezli01/vincent/pull/195))
- **A question longer than 256 bytes can be answered.** A task parked in `awaiting_input` on a long question refused every answer with a `400`, then held its concurrency slot until it was cancelled or the 24-hour input timeout failed the step. The limit belonged to task fields, not to the agent's own question text. ([#198](https://github.com/lezli01/vincent/pull/198))
- **`vincent doctor` no longer times out instead of answering.** The report asks the daemon to probe every agent CLI, and those probes carry their own deadlines — up to 145 seconds in total, cursor's model catalog being an authenticated network call. The client gave up after ten, so on a machine where a probe was merely slow the command printed `context deadline exceeded` rather than the report that would have named the adapter holding it up. That call and a forced refresh of the agent picker now wait longer than the adapters can; every other call still gives up after ten seconds. ([#223](https://github.com/lezli01/vincent/pull/223))
- **An earlier `v0.7.0` tag was withdrawn and re-cut as this release.** That build died at its first signing step and produced no archives, no deb or rpm, no attestations and no Homebrew, Scoop or WinGet metadata; the tag and its empty release were removed. ([#213](https://github.com/lezli01/vincent/pull/213), [#216](https://github.com/lezli01/vincent/pull/216))

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
