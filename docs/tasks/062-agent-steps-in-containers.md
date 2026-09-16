# 062 — Agent steps inside the task's container

*Issue #256, second half. Planned 2026-08-30.*

Status: **in progress (1/2)** — 062.1 landed 2026-09-16.

[061](061-container-step-execution.md) built the seam and proved it with
`command`, `check` and `manual` steps. This task puts the **agent** steps
inside the same container. It is split off because the spawn seam it needs
touches all three adapters, and a pull request that changes where every step
runs *and* how every agent is launched is not independently reviewable.

*Amended 2026-09-16 (issues #396 and #397):* the single task is split in two,
for the same reason the task was split from 061. 062.1 narrows to the launch
seam, which changes how every agent is launched and nothing about where; the
container bullets that were 062.1's move, unchanged, to a new 062.2 that depends
on it. Nothing was deleted.

## 062.1 — the launch seam ✅

(tracked in [#396](https://github.com/lezli01/vincent/issues/396), 2026-09-13)

- [x] **062.1** Give the three agent adapters one launch seam ✓ 2026-09-16

- **A spawn seam in `internal/agent`.** `claude.go`, `codex.go` and `cursor.go`
  build argv exactly as they do now and hand it to a launcher the engine
  chooses. Today there is no shared spawn helper — three `exec.Command` sites
  plus the one in `taskrun/steps.go` — and introducing one is the bulk of this
  task's risk.

Landed: `agent.Command`, `agent.Launcher`, `agent.Process` and
`agent.HostLauncher` (`internal/agent/launch.go`), `RunSpec.Launcher` with nil
meaning the host launcher, the three adapters' `Start` building a `Command` and
their `Kill`, `Terminate`, `PID`, `Argv` and `Wait` delegating to the `Process`,
`taskrun`'s `agentLauncher` helper as the engine's one choice, a
`RecordingLauncher` in `internal/agent/agenttest`, and the §9.1, §12.3, §16 and
§20 amendments. No behaviour changes: every run still spawns on the host, the
way it did.

## 062.2 — agent steps in the container

(tracked in [#397](https://github.com/lezli01/vincent/issues/397); moved here
from 062.1 on 2026-09-16)

- [ ] **062.2** Run agent steps inside the task's container. *Depends: 062.1.*

- **A container launcher.** The `agent.Launcher` 062.1 left room for, chosen in
  `taskrun`'s `agentLauncher` for a containerized task. Two questions 062.1
  deliberately did not answer are this task's: whether claude's in-`Start`
  `--version` probe (which decides input mode) has to follow the run into the
  image, and how the launcher resolves a binary that is not at the
  host-resolved `Command.Path`.
- **061 decision 1's MCP rewrite**: the per-step endpoint's host becomes
  `host.docker.internal` for a containerized agent step. *Amended 2026-09-14
  (issue #378):* only the endpoint rewrite is this task's work — the
  `--add-host=host.docker.internal:host-gateway` mapping is already on every
  networked task container, since 061 (`internal/container/docker.go`).
- **061 decision 1's creation-time refusal, reinstated.** `network: false`
  with `mcp.wire_steps: true` is refused with `400 validation_failed` once an
  agent step can run inside the container. 061 shipped it and issue #366
  deferred it here (2026-09-14), because no agent ran in the container and the
  pair worked. Restore the branch in `containerMismatch`
  (`internal/api/container.go`), its API test, `m12` scenario 5, and the
  refusal rows in §12.3 and the configuration reference.
- **`mount_agent_config` flipped back on by default**: `~/.claude`, `~/.codex`
  and `~/.cursor` bind-mounted read-write, because subscription auth takes no
  key from the environment and cursor persists `--model` to its own config and
  writes `.cursor/mcp.json` into the worktree (§9.7). Issue #366 turned the
  default off (2026-09-14) while nothing in the container read them; this task
  flips it back in `config.Default()`, the §12.3 listing, §16 and the
  configuration and security pages. The read-only knob and its consequences
  are documented, not hidden.
- **`RunHandle.Terminate` / `Kill` / `PID` made container-aware** via 061
  decision 9's pid file.
- **Transcripts, §17 token and cost parsing, and exit codes proven identical**
  to a host run. 061 decision 10 (`-i`, never `-t`) exists for exactly this.
- **§9.4 amended**: `permission_mode` and containerization are orthogonal axes
  that compose; there is no `contained` mode name. Cursor's
  restricted-needs-macOS/Linux rule keeps being evaluated against the **host**,
  which is already true of every containerized task under 061 decision 2.
- **§12.4's cursor `.cursor/mcp.json` recovery sweep re-checked** against a
  containerized worktree. The file is on the host mount, so the existing sweep
  reaches it — a test should pin that rather than assume it.

## Decisions

Settled with the author on 2026-09-16 (issue #396), and binding.

1. **The launcher owns the process, not just the argv.** `internal/agent` has a
   `Command` (resolved path, argv, directory, environment with nil inheriting,
   the stdin source or a request for a retained pipe, the stderr writer), a
   `Launcher` with one `Launch(Command) (Process, error)`, and a `Process`
   exposing stdout, the retained stdin, `Wait` returning the exit code,
   `Terminate`, `Kill`, `PID`, `Argv` and the release of the platform handle.
   `RunSpec.Launcher` nil is the host launcher, matching `Env`'s convention.
   *Beaten:* a launcher that only rewrites argv, the way `container.Runtime.Exec`
   does, behind a shared host spawn helper. `RunHandle.Kill`, `Terminate` and
   `PID` must become container-aware in 062.2 (061 decision 9), and the engine's
   cancel path reaches them through the handle, so an argv-only seam would force
   062.2 to add a stop hook and reopen all three adapters — the thing this split
   exists to avoid.
2. **Only the three run spawns go through the seam** — `claude.Start`,
   `codex.Start` and `cursor.Start`. `agent/probe.go`'s Detect and Options
   probes, codex's `app-server --stdio` quota exchange, claude's in-`Start`
   `--version` probe and `taskrun/steps.go`'s command and check spawn stay as
   they are: the probes and the quota exchange describe the host's CLI and
   account, and the command spawn is 061's gated path. Where the version probe
   and binary resolution run for a containerized step is 062.2's decision, and
   `Command` is where 062.2 adds a field without changing the adapters' call
   shape.
3. **This document records the split as 062.1 plus a new 062.2**, with a dated
   note and nothing deleted; the index row goes from `planned (0/1)` to `1/2`.

## Testing

*Amended 2026-09-16:* this section is 062.2's. 062.1's tests are hermetic
against fakeagent on the host: a recording launcher per adapter, nil against
explicit host launcher, and the host launcher's process semantics in
`internal/agent`.

`internal/agent/agenttest` gains a cross-compile of `cmd/fakeagent` for
`linux/amd64` so the fake agent can be bind-mounted into a small image carrying
`git`, the way `scripts/m12-gate.sh` already does it. Everything else stays
hermetic: `go test` never needs a container daemon.
