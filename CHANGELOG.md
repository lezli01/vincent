# Changelog

All notable changes to vincent are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with the pre-1.0 caveat spelled out
in [Versioning and stability](#versioning-and-stability) below.

This file is curated by hand. Each GitHub release additionally carries a
generated list of the commits between two tags, grouped by Conventional Commit
type — that is the exhaustive index; this is the human summary.

## [Unreleased]

### Added

- **Agent usage limits are now a wait, not a failure**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). When a step stops
  because the agent's usage quota for the window is spent, vincent records the
  attempt as `usage_limit`, **consumes no retry**, releases the task's
  concurrency slot, and re-queues it until the window reopens — the step then
  re-runs with **no human action**. Previously that run was indistinguishable
  from a genuine failure: it burned the whole retry budget in seconds (there is
  no delay between attempts) and blocked the task with `agent_error`, which sent
  you to read a transcript about a task that was fine. With several tasks running
  on the same agent, the whole board went down at once.

  A held task shows when it resumes — `queued → 14:20` on the board,
  `queued · usage limit → 14:20` in the detail header — and gives up its slot, so
  other work carries on. The resume time is the reset the CLI reported; when it
  reports none, vincent waits
  [`usage_limit_recheck_interval`](docs/reference/configuration.md#usage_limit_recheck_interval)
  (new, default 15m) and tries again. Cancelling, pausing or resuming a held task
  drops the wait immediately.

  Only the **claude** adapter recognizes usage-limit wording today. Capturing a
  real quota exhaustion means burning a real five-hour window, so codex and
  cursor deliberately ship no pattern and behave exactly as before — a wrong
  guess would park a genuinely failed task in a wait it never leaves.

- **`agent_unauthenticated` block reason**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). A claude step that
  fails because the CLI is not logged in now says so, instead of surfacing as
  `nonzero_exit` or `agent_error`. Nothing else changes: the step still runs, the
  retry budget still applies, and the task still blocks — the reason just names
  the fix.

- `queued_reason` and `admit_not_before` on every task in the API and on
  `GET /v1/config`'s `usage_limit_recheck_interval`. Both task fields are `null`
  for an ordinarily queued task, so the addition is invisible to existing
  clients, and they are separate from `block_reason`, which still means only
  "stopped, needs a human".

- **Homebrew install on macOS** ([task 002](docs/tasks/002-homebrew-tap.md)).
  `brew install lezli01/tap/vincent`. The cask clears the quarantine attribute
  during install, so macOS users no longer meet the Gatekeeper "unidentified
  developer" prompt or have to run `xattr -d com.apple.quarantine` by hand — the
  release binaries are still cosign-signed rather than Apple-notarized, so the
  archive path is unchanged. `brew uninstall --zap vincent` also unloads the
  LaunchAgent and removes the config and data directory. Linux and Windows keep
  the release archives and `go install`.

- **Configurable branch names**
  ([task 001](docs/tasks/001-configurable-branch-names.md)). A task's branch no
  longer has to be `vincent/{id}-{slug}`. Names resolve through a chain, most
  specific first: a per-task literal (`vincent task add --branch feat/OPS-123`, or
  `branch_name` on `POST /v1/tasks`), a project template
  (`branch_template` on `PATCH /v1/projects/{id}`), the global
  [`branch_template`](docs/reference/configuration.md#branch_template) in
  `config.yaml`, and finally the built-in name. **The default is unchanged**, so
  nothing moves unless you configure it.

  Templates get `{{.ID}}`, `{{.Title}}`, `{{.Slug}}`, `{{.BaseBranch}}`,
  `{{.Fields.NAME}}`, `{{.Project.*}}` and a `slug` function. The new-task form
  previews the resolved name and the level it came from as you type, via
  `POST /v1/resolve`.

  Two things to know. Because vincent never deletes branches, a template with no
  discriminator in it collides on the *second* task for the same input — put
  `{{.ID}}` in it or expect to name repeats by hand. And `{{.Fields.x}}` fails
  loudly on a missing field while `{{ index .Fields "x" }}` renders nothing, which
  yields a legal-but-wrong name like `feat/-fix-login`; prefer the first.

- `branch_override` on `POST /v1/tasks/{id}/retry`, which makes a `branch_exists`
  block recoverable: the branch is renamed and the task re-admitted, keeping its
  id and history. Previously nothing in the API could change a branch name.

- `branch_name_invalid` block reason. Branch names are validated by
  `git check-ref-format` rather than a hand-rolled matcher, and a rejected name is
  reported rather than silently rewritten.

- `go run mage.go vuln` and a weekly `Vulnerabilities` workflow: govulncheck
  over the module's reachable code, swept across `linux`, `darwin` and `windows`
  because 15 packages (`x/sys/windows/svc`, `modernc.org/libc/*`) reach the
  binary on Windows only.
- `CHANGELOG.md`, [`RELEASING.md`](RELEASING.md) and `.github/CODEOWNERS`.
- `go install github.com/lezli01/vincent/cmd/vincent@latest` as a documented
  install path, and a versioning-and-stability policy in the README and
  `SECURITY.md`.

### Changed

- A task's branch name is now resolved and written inside the same transaction as
  the task row, so no committed task can carry an empty one. This removes a window
  in which a crash between two writes left the name unset — harmless while names
  were derived from `(id, title)`, but it would have silently discarded a
  configured name and run the task on a default branch.
- `docs/` is no longer versioned: `docs/versions/v0/spec.md` is now
  [`docs/spec.md`](docs/spec.md), the single platform spec, amended in place.
  Planned work lives in [`docs/tasks/`](docs/tasks/README.md), the closed v0 ledger
  in `docs/history/`, and the gate walkthroughs in `docs/gates/`.
- A `go install`ed binary now reports the module version from `vincent version`
  instead of `dev`.
- CI runs the three acceptance gates as steps of one per-OS job rather than
  three separate jobs, sharing a checkout and a toolchain setup.
- Release notes are grouped into Features / Bug fixes / Other changes instead of
  one flat list of commit SHAs.
- Third-party actions in the release workflow are pinned to commit SHAs; every
  job carries a `timeout-minutes`.

### Fixed

- **The TUI answer form no longer truncates what it asks you**
  ([#83](https://github.com/lezli01/vincent/issues/83)). A question, an option
  label or a permission summary longer than the popup's inner width was cut with
  an `…` and there was no way to see the rest from inside the form — no wrap, no
  scroll, no expand — and because the popup is capped at 76 columns, a wider
  terminal did not help. That hid the end of an agent's question, the
  `(Recommended)` suffix agents put at the *end* of an option label, and, in
  `restricted` mode, the tail of the command you were being asked to approve.
  Rows now wrap inside the popup, with continuation lines indented under the
  marker so a wrapped option still reads as one option; `up`/`down` still move a
  whole option at a time, and the focused row is kept fully on screen.

## [0.1.0] — 2026-08-12

First release. All 70 tasks of the
[v0 breakdown](docs/history/v0-tasks.md) are complete, and the M1, M2, M4 and
M5 acceptance gates are met.

### Added

- **Daemon** owning all state and execution — SQLite (WAL, single writer),
  git worktrees, and agent CLI subprocesses. Work continues with no client
  attached; crash recovery finalizes interrupted step runs and kills verified
  orphans (PID *and* start time must match).
- **Localhost REST + SSE API** with bearer auth, the single interface every
  client uses.
- **Workflow engine** — YAML registry with builtin < global < project
  shadowing, live reload, and three step types (agent, command, manual) plus
  retries, timeouts and human actions.
- **Bubble Tea TUI** with six views (board, detail, new-task, projects,
  workflows, daemon), holding no state the daemon does not have.
- **CLI subcommands** over the same API.
- **Agent adapters** for Claude Code, Codex and Cursor. Capability differences
  are documented and ignored at run time, never emulated.
- **OS service registration** on Windows (Scheduled Task, as the invoking
  user), macOS (launchd) and Linux (systemd user unit).
- **Signed releases** — cross-compiled archives for linux/darwin/windows on
  amd64/arm64, SHA-256 checksums, a keyless cosign signature over the checksum
  file, and GitHub build provenance attestations.

### Security

- Agents run **full-auto by default** — a documented design decision
  ([spec §16](docs/spec.md)), surfaced once by the TUI on first run.
  Git worktrees isolate collisions between tasks, not privileges.
- The daemon's trust boundary is the OS user: loopback-only listener, bearer
  token stored `0600` in the data directory, and no agent credentials stored.

## Versioning and stability

vincent is `0.x`. Until `1.0.0`:

- **Breaking changes may land in any minor release** (`0.1` → `0.2`), and are
  called out under a `### Changed` heading here with the migration needed.
- **Patch releases** (`0.1.0` → `0.1.1`) are fixes only, with no breaking
  change to the config file, the workflow YAML schema, the REST API or the CLI
  flags.
- The **config file, workflow schema, REST API and CLI surface are not stable
  yet.** Pin a version if you script against them.
- The **on-disk database migrates forward automatically** and is append-only by
  policy (`internal/store/migrations/`); downgrading a binary across a
  migration is not supported.

[Unreleased]: https://github.com/lezli01/vincent/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/lezli01/vincent/releases/tag/v0.1.0
