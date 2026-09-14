# 062 — Agent steps inside the task's container

*Issue #256, second half. Planned 2026-08-30.*

Status: **planned (0/1)**.

[061](061-container-step-execution.md) built the seam and proved it with
`command`, `check` and `manual` steps. This task puts the **agent** steps
inside the same container. It is split off because the spawn seam it needs
touches all three adapters, and a pull request that changes where every step
runs *and* how every agent is launched is not independently reviewable.

## 062.1 — agent steps in the container

- **A spawn seam in `internal/agent`.** `claude.go`, `codex.go` and `cursor.go`
  build argv exactly as they do now and hand it to a launcher the engine
  chooses. Today there is no shared spawn helper — three `exec.Command` sites
  plus the one in `taskrun/steps.go` — and introducing one is the bulk of this
  task's risk.
- **061 decision 1's MCP rewrite**: the per-step endpoint's host becomes
  `host.docker.internal` for a containerized agent step, with
  `--add-host=host.docker.internal:host-gateway` on the container.
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

## Testing

`internal/agent/agenttest` gains a cross-compile of `cmd/fakeagent` for
`linux/amd64` so the fake agent can be bind-mounted into a small image carrying
`git`, the way `scripts/m12-gate.sh` already does it. Everything else stays
hermetic: `go test` never needs a container daemon.
