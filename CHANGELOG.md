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

- `go run mage.go vuln` and a weekly `Vulnerabilities` workflow: govulncheck
  over the module's reachable code, swept across `linux`, `darwin` and `windows`
  because 15 packages (`x/sys/windows/svc`, `modernc.org/libc/*`) reach the
  binary on Windows only.
- `CHANGELOG.md`, [`RELEASING.md`](RELEASING.md) and `.github/CODEOWNERS`.
- `go install github.com/lezli01/vincent/cmd/vincent@latest` as a documented
  install path, and a versioning-and-stability policy in the README and
  `SECURITY.md`.

### Changed

- A `go install`ed binary now reports the module version from `vincent version`
  instead of `dev`.
- CI runs the three acceptance gates as steps of one per-OS job rather than
  three separate jobs, sharing a checkout and a toolchain setup.
- Release notes are grouped into Features / Bug fixes / Other changes instead of
  one flat list of commit SHAs.
- Third-party actions in the release workflow are pinned to commit SHAs; every
  job carries a `timeout-minutes`.

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
