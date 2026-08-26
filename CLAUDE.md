# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

vincent is a local-first control plane for AI coding-agent workloads: a background
daemon owns state and execution (SQLite + git worktrees + agent CLI subprocesses),
and every client — the Bubble Tea TUI, `vincent` CLI subcommands, curl — is a thin
consumer of its localhost REST+SSE API. One Go binary (`cmd/vincent`) serves all
roles.

## Documentation model

Public documentation is feature-first and rooted at `docs/README.md`:
`docs/features.md` presents the product surface, while `getting-started/`,
`guides/`, `platforms/`, `reference/`, `security-model.md`, and `faq.md` explain
how to use it. It is derived from the source, not from planning records: the
config reference tracks `internal/config`, the CLI page the cobra tree, the API
page `internal/api/server.go`'s route table, the TUI key tables
`internal/tui/bindings.go`, and the block reasons the `Reason*` constants. A
change to any of those is a change to its page. The same rule covers the
pictures: every `docs/assets/tui-*.png` is a capture of the running TUI produced
by `scripts/screenshots.sh` (below), never a drawing of one.

Implementation work also uses these maintainer records. They are **not**
optional when a change touches the behavior or decision they describe:

- `docs/spec.md` — the product and implementation spec, and the **only** one.
  Section numbers (§6, §12.2, §13.3 …) are used as identifiers throughout the code
  comments. When a comment says "spec §9.1", go read it before changing that code.
  It is deliberately **not versioned**: it describes the system as it is now, and is
  amended in place with dated notes, in the same PR as the code that makes an
  amendment true. Two specs would make every `spec §9.1` citation ambiguous, which
  is why the original version's copy was extracted here rather than forked.
- `docs/tasks/` — planned and in-flight work, one document per piece of work
  (`001-configurable-branch-names.md`), with its decisions. `docs/tasks/README.md`
  is the index and defines the conventions: status markers
  (`[ ]`/`[~]`/`[x]`/`[!]`), `NNN.n` task IDs, updating the index row in the same
  edit, and never deleting a task. Mention the task ID in the PR ("closes 001.4").
- `docs/history/v0-tasks.md` — the **closed** initial-release ledger (70/70,
  released as `v0.1.0`). Frozen: there is no open task in it to claim. Read it for the design
  decisions it carries, which are binding and are cited from code comments
  ("phase 2 decision", "T1.5/T1.6 decision", "PR L decision").
- `docs/gates/` — the manual walkthroughs behind the scripted acceptance gates,
  and the record of when each was last walked.

`skills/vincent-workflows/SKILL.md` is **runtime text, not just documentation**:
`skills/embed.go` embeds it and `internal/workflow/builtin.go` splices it into
the built-in `create-workflow` workflow's prompt at build time (task 024). An
edit to that file changes what the daemon tells an agent to do. It reaches the
prompt escaped for `text/template` and re-indented into a YAML block scalar, so
it may contain anything — but the three standing corrections the prompt applies
to it (what asking costs, destination, missing `references/`) live in
`createWorkflowHeader`, never in the skill.

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
against the fake agent; CI runs every one of them on Linux, macOS and Windows:

```sh
./scripts/m1-gate.sh
./scripts/m2-gate.sh
./scripts/m5-gate.sh                            # cursor adapter (§9.7)
./scripts/m6-gate.sh                            # parallel steps and fan-out (§7.5, §7.6)
./scripts/m7-gate.sh                            # conditions between steps (§7.7)
./scripts/m8-gate.sh                            # loops (§7.8)
./scripts/m9-gate.sh                            # workflow includes (§7.9)
VINCENT_GATE_SCENARIO=2 ./scripts/m2-gate.sh    # single scenario, for debugging
VINCENT_GATE_AGENT=claude ./scripts/m2-gate.sh  # manual run against the real CLI
VINCENT_GATE_AGENT=cursor ./scripts/m5-gate.sh  # ditto, for cursor-agent
```

All seven run in `ci.yml`'s `gates` job on all three platforms. `m6`, `m7` and
`m8` spent a while unwired because a cloud session's token has no `workflow`
scope and so cannot write `.github/workflows/` by any route — push or API
(#120, #122, #125). A gate that has never run on a platform is not known to
pass there: wiring these in turned up two Windows-only faults in a script that
had passed on Linux for weeks, both recorded below.

`scripts/m3-gate.sh` seeds a human walkthrough instead of asserting — M3's
acceptance is a judgement about a TUI. The manual legs of M5 are walked
through in `docs/gates/m5-gate.md`, which also records the runs. Task 017's
workflow graph has no script at all for the same reason M3's has none — what
is judged is whether a picture is legible — so `docs/gates/017-workflow-graph.md`
carries the corpus (`docs/gates/corpus/*.yaml`, kept parseable by a test) and
the run record.

New gate scripts must be committed **executable** (`git update-index
--chmod=+x`): `chmod` on Windows never reaches the index, Git Bash ignores the
bit, so a non-executable gate passes on Windows and fails both POSIX legs with
exit 126 before running an assertion.

`scripts/screenshots.sh` is the other seeding script, and the **only** source of
the images under `docs/assets/tui-*.png`. It seeds a throwaway installation —
its own config and data dirs, seven git repos, a daemon, fourteen tasks covering
every state — and photographs the running TUI with VHS (`brew install vhs`).
Documentation never draws a screen: no ASCII mock-ups of panels, no hand-written
"example" frames. If a panel changed, re-run the script:

```sh
./scripts/screenshots.sh            # seed, capture every shot, clean up
./scripts/screenshots.sh seed       # leave the daemon up to iterate on tapes
VINCENT_SHOTS_ONLY=tui-diff ./scripts/screenshots.sh capture
```

It is macOS/Linux-only and CI does not run it (VHS needs ttyd and ffmpeg, and
its workflows use a POSIX shell rather than the sh∩pwsh intersection the gates
are held to). Two VHS traps are already worked around in it and will bite again
in any new tape: a `Screenshot` is written on the *next* captured frame, so a
tape that ends on one records nothing, and keys pressed inside a `Hide` block
never reach a screenshot at all — only the launch is hidden.

A gate's *workflow* `run:` bodies run under the daemon's shell — `/bin/sh` on
POSIX, `pwsh` on Windows (§8.3) — not under the gate's bash, so they must be
spelled in the intersection of the two: `exit N`, `sleep N` and `git ...`.
That excludes `touch`, `seq`, `[ -f ]` and `for`/`if`, and it is why a lane
that needs a file on disk writes it with `git config -f x k v` and one that
only needs a commit uses `git commit --allow-empty`. `exit N` has to be the
*whole* body: pwsh's `&&`/`||` take pipelines, so `... && exit 0` is parsed
as a command named `exit` and fails. A body that must pass or fail on a
condition is one command whose own exit code says so — `git config --get`
exits 1 on a missing key. Assert concurrency and timing from the API instead
of from inside a step body.

In the gate's own bash, a capture spanning **several lines** of `jq` output
needs `| tr -d '\r'`: jq writes CRLF on Windows, `$(...)` drops only the
trailing one, and the interior CRs then fail an exact comparison against a
`$'a\nb'` literal. Single-line captures are unaffected, which is how this hid
until `m8` compared a list.

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
  `interrupted`, kills verified orphans (the PID must still hold the process the
  row journaled, proved by an exact platform-native identity — `procx.Identity`,
  with the older start-time tolerance kept only for rows carrying none — the PID
  reuse guard), and re-runs the step as an attempt that does not consume a retry.

**Package map** (each has a `doc.go` stating its role and spec sections):

| Package | Role |
|---|---|
| `internal/config` | Platform-native config/data dirs, `config.yaml` load + hot reload |
| `internal/store` | SQLite: embedded up-only migrations, typed CRUD, event hook |
| `internal/daemon` | Lifecycle: foreground/detached start, lock file, token, `daemon.json`, log rotation |
| `internal/api` | Localhost REST + SSE, bearer auth, snake_case error envelope |
| `internal/apiclient` | Typed HTTP+SSE client — the one client for TUI and CLI; owns its wire types (server DTOs stay unexported) |
| `internal/workflow` | YAML registry (builtin < global < project shadowing), live reload, `text/template` step engine, creation-time expansion of `include` steps (§7.9) |
| `internal/taskrun` | Step executors — agent, command and manual are the ones that run something; `parallel`, `fan_out`, `condition`, `loop` and `break` are structure, `check` is a *field* agent and command steps may carry, never a type, and `include` never arrives at all — it is spliced away at task creation (§7.9) — plus guards (`if:`, §7.7), loop iteration (§7.8), retries, timeouts, human actions, recovery, transcripts |
| `internal/agent` | `AgentAdapter` interface + option catalog; `agent/claude`, `agent/codex`, `agent/cursor` implement it |
| `internal/worktree` | Per-task git worktrees, `vincent/{id}-{slug}` branches, dirty detection |
| `internal/tui` | Bubble Tea client: six views (board, detail, new-task, projects, workflows, daemon) routed by `viewID` |

Adapters differ in what they *can* do, and the differences are documented, never
faked: codex ships no model catalog and no mid-run input; cursor has no effort
concept at all (it lives in the model id), probes its models over an
authenticated network call rather than from `--help`, and cannot honor
`restricted` on Windows — where it returns `agent.ErrRestrictedUnsupported` from
`Start` so the engine fails the step rather than silently running it full-auto.
A capability an adapter lacks is stated in spec §9.x and ignored at run time; it
is never emulated.

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
  argv shape (`exec` first arg ⇒ codex-shaped; `--trust` anywhere ⇒ cursor-shaped;
  otherwise claude-shaped — cursor's run argv is claude's plus flags, and `--trust`
  is the one flag only cursor passes, in both permission modes); `models` and
  `status` as argv[1] answer cursor's option and login probes. Scenarios come from
  env (`FAKEAGENT_SCENARIO`, `FAKEAGENT_VERSION`, `FAKEAGENT_EDIT_FILE`,
  `FAKEAGENT_CURSOR_LOGGED_OUT`, …) so argv stays faithful to the real CLIs. Add
  scenarios here rather than special-casing adapters for tests.
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
  full suite plus every gate in CI. Platform-specific code goes in
  `_unix.go`/`_windows.go`/`_darwin.go`/`_linux.go` files (see `internal/procx`,
  `internal/daemon/spawn*.go`, `internal/service`). Never assume a POSIX shell,
  `/`-separated paths, or signal semantics.
- **Lint the other platforms before pushing, not just build them.** `go build`
  cross-compiles with `GOOS=…`, but `go tool golangci-lint` *cross-builds the
  linter* and then cannot run it. Build it for the host once and run that:

  ```sh
  LINT=$(go tool -n golangci-lint)
  for os in windows darwin linux; do GOOS=$os "$LINT" run ./...; done
  ```

  A host-only lint hides findings in every build-tagged file — `unparam` fired
  on macOS alone for a helper whose argument is constant there and varying on
  Linux, and Windows does not compile that file at all.
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
  `type/short-description`. Conventional prefixes belong on commits, not human PR
  titles: use a plain-language title such as `Add workflow fields`, never
  `feat: add workflow fields`. GitHub copies the PR title into the merge commit
  body, so a conventional title makes Release Please record both the merge and
  the matching inner commit in `CHANGELOG.md`. The `PR title` workflow enforces
  this; Release Please and Dependabot PRs are narrowly exempt because those tools
  couple their PR titles to their generated commit messages. CI green on all
  three platforms is required.
- **Never** add co-author to commits.

## Security posture

Agents run **full-auto by default** — a documented design decision (spec §16), not
a bug. An agent can run arbitrary commands as the invoking user; the git worktree
isolates collisions between tasks, not privileges. The TUI shows this warning once
on first run (`internal/tui/firstrun.go`). Keep that property in mind when changing
permission modes, workflow defaults, or the first-run acknowledgment.
