# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

vincent is a local-first control plane for AI coding-agent workloads: a background
daemon owns state and execution (SQLite + git worktrees + agent CLI subprocesses),
and every client — the Bubble Tea TUI, `vincent` CLI subcommands, curl — is a thin
consumer of its localhost REST+SSE API. One Go binary (`cmd/vincent`) serves all
roles.

## Spec-driven workflow

Two documents govern the work and are **not** optional reading:

- `docs/versions/v0/spec.md` — the product and implementation spec. Section numbers
  (§6, §12.2, §13.3 …) are used as identifiers throughout the code comments. When a
  comment says "spec §9.1", go read it before changing that code.
- `docs/versions/v0/tasks.md` — the single source of truth for progress, broken into
  task IDs (`T2.5`, `T3.7`). Its "How to update this file" header defines the status
  markers (`[ ]`/`[~]`/`[x]`/`[!]`), the requirement to update the dashboard table in
  the same edit, and the rule that tasks are never deleted. Update it when a PR
  completes a task, and mention the task ID in the PR.

Phase decisions ("phase 2 decision", "PR G decision", "T1.5/T1.6 decision") recorded
inline in comments and in tasks.md are binding — they are the outcome of design
sessions, not incidental notes. Don't relitigate one without saying so explicitly.

## Commands

Build targets run through mage with zero install (`go run mage.go -l` lists them).
CI runs exactly these:

```sh
go run mage.go build     # build bin/vincent with version ldflags injected
go run mage.go test      # go test ./...
go run mage.go testrace  # go test -race ./...  (needs cgo + a C compiler)
go run mage.go lint      # go tool golangci-lint run (pinned via go.mod tool directive)
```

Plain toolchain works too (`go build ./...`, `go test ./...`). Single test:

```sh
go test ./internal/taskrun -run TestRunnerStop -v
go test -race ./internal/tui -run TestLiveChangeFromRealServer
```

Acceptance gates — bash scripts that drive a real daemon end to end over curl
against the fake agent; CI runs both on Linux, macOS, and Windows:

```sh
./scripts/m1-gate.sh
./scripts/m2-gate.sh
VINCENT_GATE_SCENARIO=2 ./scripts/m2-gate.sh   # single scenario, for debugging
VINCENT_GATE_AGENT=claude ./scripts/m2-gate.sh # manual run against the real CLI
```

Requires bash, go, git, curl, jq.

## Architecture

Dependency direction is one-way: `cli` → `tui`/`daemon`; `daemon` wires
`store` + `workflow` + `scheduler` + `taskrun` + `events` + `api`; leaf packages
(`taskstate`, `gitx`, `procx`, `version`, `config`) depend on nothing internal.
`internal/daemon/daemon.go:Run` is the single wiring point — read it first to see
how the pieces connect.

**Ownership invariants.** These are what make the concurrency safe; violating one
is a correctness bug, not a style issue:

- **The daemon owns everything.** Clients never touch git, the DB, or agent
  processes — only the API. Killing every client changes nothing about running work.
- **One DB writer.** Only the daemon opens SQLite (WAL, single connection), so
  writes serialize and `SQLITE_BUSY` cannot occur in-process.
- **`internal/scheduler` is the only place `queued → running` happens.** Single
  goroutine; both concurrency caps (global + per-project) are safe only because
  admission is unraced. Ordering and caps come from SQL (`store.ListAdmissible`);
  the walk is in Go because admission changes the tallies mid-statement.
- **`internal/taskrun` actors are the sole writer of a task's state and its
  step_run rows.** One goroutine per admitted task, living for exactly one
  admission — a gate, a block, or a pause releases the slot and ends the goroutine.
- **`internal/taskstate` is the pure FSM** (no I/O). Both the API (409 on invalid
  action) and the engine consult it, so "what may happen next" has one definition.
- **`internal/events` fan-out is post-commit only.** The store's event hook
  publishes after the DB records the event. Durable state events disconnect slow
  subscribers (they resume from the events table via `Last-Event-ID`); live output
  chunks are dropped for slow subscribers (the transcript file is the durable copy).
- **Crash-first.** Every transition is persisted before it is acted on. Recovery
  (`internal/taskrun/recover.go`, spec §12.4) finalizes `running` step runs as
  `interrupted`, kills verified orphans (PID **and** start time must match — PID
  reuse guard), and re-runs the step as an attempt that does not consume a retry.

**Package map** (each has a `doc.go` stating its role and spec sections):

| Package | Role |
|---|---|
| `internal/config` | Platform-native config/data dirs, `config.yaml` load + hot reload |
| `internal/store` | SQLite: embedded up-only migrations, typed CRUD, event hook |
| `internal/daemon` | Lifecycle: foreground/detached start, lock file, token, `daemon.json`, log rotation |
| `internal/api` | Localhost REST + SSE, bearer auth, snake_case error envelope |
| `internal/apiclient` | Typed HTTP+SSE client — the one client for TUI and CLI; owns its wire types (server DTOs stay unexported) |
| `internal/workflow` | YAML registry (builtin < global < project shadowing), live reload, `text/template` step engine |
| `internal/taskrun` | Step executors (agent/command/manual/check), retries, timeouts, human actions, recovery, transcripts |
| `internal/agent` | `AgentAdapter` interface + option catalog; `agent/claude`, `agent/codex` implement it |
| `internal/worktree` | Per-task git worktrees, `vincent/{id}-{slug}` branches, dirty detection |
| `internal/tui` | Bubble Tea client: six views (board, detail, new-task, projects, workflows, daemon) routed by `viewID` |

`internal/worktree` and `internal/taskrun/engine.go` share one snake_case vocabulary
for failure/block reasons (`ReasonTimeout`, `ReasonWorktreeDirty`, …) — a
`block_reason` means the same thing wherever it originated. Reuse those constants
rather than inventing strings.

The TUI holds no state the daemon doesn't have. Views are sub-models behind the
`view` interface; the root routes non-global messages to the active view and checks
`inputCapturing` before applying single-key global bindings.

## Testing conventions

Tests are self-contained and hermetic — no real agent CLI, no network, no running
daemon, no shared fixtures:

- `internal/testrepo` — throwaway git repos (skips when git is absent).
- `internal/agent/agenttest` — compiles `cmd/fakeagent` once per test process.
- `cmd/fakeagent` — scenario-driven stand-in for an agent CLI. Dialect comes from
  argv shape (`exec` first arg ⇒ codex-shaped, otherwise claude-shaped); scenarios
  come from env (`FAKEAGENT_SCENARIO`, `FAKEAGENT_VERSION`, `FAKEAGENT_EDIT_FILE`,
  …) so argv stays faithful to the real CLIs. Add scenarios here rather than
  special-casing adapters for tests.
- `*live_test.go` — the TUI/apiclient wired to the **real** API handlers over
  `httptest`, which is what keeps client and server wire types from drifting.
- `e2e_test.go` in `internal/cli` and `internal/tui` build the real binary in
  `TestMain`; detached self-exec and daemon auto-start can't be proven in-process.
- Adapter parsing is table-driven against captured real-CLI output in
  `testdata/*.jsonl` / `help_*.txt`, named with the CLI version they came from.

Tests isolate state via `VINCENT_CONFIG_DIR` / `VINCENT_DATA_DIR` (see
`internal/config/dirs.go`) — use those, never the user's real dirs.

## Conventions

- **Cross-platform is a hard requirement.** Windows, macOS, and Linux all run the
  full suite plus both gates in CI. Platform-specific code goes in
  `_unix.go`/`_windows.go`/`_darwin.go` files (see `internal/procx`,
  `internal/daemon/spawn*.go`). Never assume a POSIX shell, `/`-separated paths, or
  signal semantics.
- **Package docs carry the design.** New packages get a `doc.go` explaining their
  role and citing spec sections. Non-obvious choices get a comment saying *why*,
  the way the existing code does.
- **Lint is strict** — golangci-lint v2 standard set plus errorlint, gocritic,
  revive, copyloopvar, intrange, misspell, unconvert, unparam, nolintlint,
  sqlclosecheck, with gofumpt formatting. Run `go run mage.go lint` before pushing.
- **Migrations are append-only.** Add `internal/store/migrations/000N_*.sql`; never
  edit an applied one.
- **Time in SQLite** is RFC3339 UTC with fixed-width nanoseconds
  (`store.timeFormat`) so lexicographic TEXT ordering matches chronological order.
- **Git flow:** everything lands via PR to `master`, merged with merge commits (no
  squash — branch history becomes `master`'s history). Conventional Commits
  (`feat`, `fix`, `docs`, `ci`, `chore`, `refactor`, `test`), branches named
  `type/short-description`. CI green on all three platforms is required.
- **Never** add co-author to commits.

## Security posture

Agents run **full-auto by default** — a documented design decision (spec §16), not
a bug. An agent can run arbitrary commands as the invoking user; the git worktree
isolates collisions between tasks, not privileges. The TUI shows this warning once
on first run (`internal/tui/firstrun.go`). Keep that property in mind when changing
permission modes, workflow defaults, or the first-run acknowledgment.
