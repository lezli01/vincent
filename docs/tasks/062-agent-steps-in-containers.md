# 062 — Agent steps inside the task's container

*Issue #256, second half. Planned 2026-08-30.*

Status: **done (2/2)** — 062.1 landed 2026-09-16, 062.2 landed 2026-09-17.

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

## 062.2 — agent steps in the container ✅

(tracked in [#397](https://github.com/lezli01/vincent/issues/397); moved here
from 062.1 on 2026-09-16)

- [x] **062.2** Run agent steps inside the task's container. *Depends: 062.1.* ✓ 2026-09-17

- **A container launcher.** The `agent.Launcher` 062.1 left room for, chosen in
  `taskrun`'s `agentLauncher` for a containerized task. Two questions 062.1
  deliberately did not answer are this task's: whether claude's in-`Start`
  `--version` probe (which decides input mode) has to follow the run into the
  image, and how the launcher resolves a binary that is not at the
  host-resolved `Command.Path`. *Answered 2026-09-17 (decision 2 below): both
  follow the run into the image.*
- **061 decision 1's MCP rewrite**: the per-step endpoint's host becomes
  `host.docker.internal` for a containerized agent step. *Amended 2026-09-14
  (issue #378):* only the endpoint rewrite is this task's work — the
  `--add-host=host.docker.internal:host-gateway` mapping is already on every
  networked task container, since 061 (`internal/container/docker.go`).
  *Amended 2026-09-17 (decision 1 below):* on native Docker Engine the rewrite
  alone cannot reach a loopback daemon, so the endpoint is served on a
  step-only listener bound on the container network's gateway, with the
  loopback port as the fallback where that bind is impossible.
- **061 decision 1's creation-time refusal, reinstated.** `network: false`
  with `mcp.wire_steps: true` is refused with `400 validation_failed` once an
  agent step can run inside the container. 061 shipped it and issue #366
  deferred it here (2026-09-14), because no agent ran in the container and the
  pair worked. Restore the branch in `containerMismatch`
  (`internal/api/container.go`), its API test, `m12` scenario 5, and the
  refusal rows in §12.3 and the configuration reference. *Amended 2026-09-17
  (decision 5 below):* reinstated **narrowed** to a workflow that has an agent
  step at any depth after include expansion; a command-only workflow still
  runs with no network.
- **`mount_agent_config` flipped back on by default**: `~/.claude`, `~/.codex`
  and `~/.cursor` bind-mounted read-write, because subscription auth takes no
  key from the environment and cursor persists `--model` to its own config and
  writes `.cursor/mcp.json` into the worktree (§9.7). Issue #366 turned the
  default off (2026-09-14) while nothing in the container read them; this task
  flips it back in `config.Default()`, the §12.3 listing, §16 and the
  configuration and security pages. The read-only knob and its consequences
  are documented, not hidden. *Amended 2026-09-17 (decision 3 below):* the
  directories are mounted beneath a vincent-provided `HOME`, not at their own
  host paths, and the macOS Keychain gap is documented.
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

Landed: `Resolve` and `Probe` on `agent.Launcher`, with `HostLauncher`
implementing both as the adapters did (the configured path, else `PATH`; the
shared probe), and the optional `agent.InputProber` that claude implements; the
three adapters' `Start` resolving through the run's launcher and claude's
`--version` probe running through it; the container launcher and its `Process`
(`internal/taskrun/agentcontainer.go`), picked by `agentLauncher` when the
task's container is active and `HostLauncher` otherwise; the resolve, probe and
wrapped-run `docker exec` argv with step variables passed by name; the
container-aware stop and the `128+n` → `-1` exit mapping for a signal vincent
sent; `step_runs.container_id` journaled for containerized agent runs; the
step-only gateway listener and the `host.docker.internal` endpoint URL; the
vincent home and `mount_agent_config` back on by default; the narrowed
creation refusal; `agenttest.BuildFakeAgentLinux`; and the §2, §7.4, §8.5,
§9.1, §9.4, §12.1, §12.3, §12.4, §13.1, §13.4, §16 and §20 amendments. Chats
keep a nil launcher and run on the host.

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

### 062.2 decisions

*Added 2026-09-17 (issue #397).* Settled with the author on 2026-09-17, and
binding. They are cited as "062.2 decision N"; the three above are 062.1's.
They settle the three questions this document left open, plus one fact it had
not accounted for — 061 decision 1's rewrite cannot reach a loopback daemon on
native Linux Docker — and they amend task 061 in three places, each recorded
there with a date: decision 1's reachability on Linux, decision 1's refusal,
and where the agent configuration directories are mounted.

1. **The per-step MCP endpoint is reached through a step-only bridge listener
   on Linux.** The daemon's `listen` must be loopback (§13.1). On native Docker
   Engine `host-gateway` resolves to the bridge gateway IP (docker0, for
   example `172.17.0.1`), where a `127.0.0.1` listener cannot be reached;
   Docker Desktop on macOS forwards `host.docker.internal` to the host's
   loopback, so the plain rewrite works there. For a containerized agent step
   with `mcp.wire_steps: true` the daemon looks up the container network's
   gateway IP (`docker inspect`) and binds a second listener on it, on an
   ephemeral port. That listener serves **only** `/mcp/step/{run_id}`, which
   the per-run secret already authenticates; `/v1` and `/mcp` are `404` on it.
   Where the gateway IP is not a local address — Docker Desktop, including
   Docker Desktop for Linux, and runtimes like rootless podman — the bind fails
   and the daemon falls back to the loopback port, which is the Desktop path.
   Either way the URL handed to the adapter, cursor's `.cursor/mcp.json`
   included, is `http://host.docker.internal:{port}/mcp/step/{run_id}`, with
   the port of whichever listener serves it. Listeners are reference counted
   per gateway IP, closed after the last step releases one, and closed at
   shutdown. §13.1's "loopback only" and §16's "the endpoint is not a boundary"
   are amended to say exactly this — one path, on the container gateway,
   guarded by a per-run secret — and that anything else on that bridge can
   reach the port. **The alternatives it beat:** `--network=host` on Linux,
   which is 061 decision 1's beaten alternative and a second code path; and
   refusing `wire_steps` on native Linux, which would make a containerized step
   quietly less capable.
2. **The agent CLI is resolved and probed in the image, never on the host.**
   The launcher gains the two hooks 062.1 decision 2 left for this task,
   `Resolve` and `Probe`, and `HostLauncher` implements both exactly as the
   adapters did: the configured path, else `exec.LookPath`, and `agent.Probe`.
   The container launcher resolves the adapter's **bare binary name** on the
   image's `PATH` with a plain `docker exec` (`command -v`) that skips the
   pid-file wrapper, and runs claude's in-`Start` `--version` probe inside the
   container the same way, so §7.4 input mode matches the CLI that actually
   runs. `agents.*.path` is a host path and does not apply inside the image.
   Adapters call the hooks from their binary resolution and the version probe;
   no `RunSpec` call shape changes. A CLI missing from the image fails `Start`,
   so the step fails `agent_unavailable` exactly as a missing host CLI does, and
   the host does not need the CLI installed. The §7.4 `require` pre-flight for
   a containerized step is judged against the image's CLI through the optional
   `agent.InputProber`, which claude implements; the other adapters keep the
   catalog verdict, whose only "cannot" is static. Only a positive "cannot"
   fails the step. 062.1 decision 2 still holds: `Detect`, `Options`, the
   catalog, codex's `app-server` quota exchange and the usage-limit holds keep
   describing the host's CLI and account. **The alternative it beat:** keeping
   resolution and the probe on the host, which judges a binary that is not the
   one about to run and requires the host to carry a CLI it never runs.
3. **`mount_agent_config` defaults back to true, and containerized steps get a
   vincent-provided HOME.** The mounts only help if the CLI's `$HOME` finds
   them, and HOME comes from the image (061 decision 7) — under `--user uid` on
   Linux usually `/`. With the knob on, the container is created with a
   writable tmpfs home at `/vincent-home` (mode 1777, beside `/vincent-run`),
   and `~/.claude`, `~/.codex` and `~/.cursor`, those that exist on the host,
   are bind-mounted read-write beneath it. Every containerized step, command
   and agent alike, runs with `HOME=/vincent-home`; a HOME the user's own
   `environment` policy sets, lists in `inherit` or unsets still wins. The
   config directories therefore move from "at their own host paths" to under
   the vincent home, while the worktree and repository keep 061 decision 2's
   identical paths, so claude's cwd-keyed session store still lines up. The
   docs state the consequences: the image's own HOME contents are hidden while
   the mounts are on, and on macOS claude keeps its OAuth login in the Keychain
   rather than `~/.claude`, so a Mac host must supply `CLAUDE_CODE_OAUTH_TOKEN`
   or `ANTHROPIC_API_KEY` through `environment` (codex's `auth.json` and Linux
   claude's `.credentials.json` do carry over). `config.Default()`, §12.3's
   listing, §16's credentials bullet and the configuration and security pages
   flip back, with dated notes where the page carries them; a `config.yaml`
   that sets the key keeps its value. **The alternative it beat:** mounting the
   directories at their own host paths and leaving HOME to the image, which on
   Linux points the CLI at a HOME the mounts are not in.
4. **`vincent status` inside a container is out of scope, and documented.** The
   image carries no vincent binary, and `127.0.0.1` inside the container is not
   the daemon. The agents guide, the MCP guide and the container sections say
   that a containerized agent reports its status through the `step_status` MCP
   tool when steps are wired. The same limit applies to a host-path
   `vincent statusline` hook in a mounted `~/.claude/settings.json`: claude
   tolerates a failing hook, and that run's usage-limit observations are lost.
   *Recorded 2026-09-17:* the decision as first written named the tool
   `update_status`; the tool that sets a step's status is `step_status`
   (`update_status` is the release check, §13.4), and the docs name
   `step_status`.
5. **The `network: false` + `mcp.wire_steps: true` refusal returns, narrowed to
   workflows with agent steps.** `containerMismatch`
   (`internal/api/container.go`) returns `400 validation_failed` only when the
   workflow, after §7.9 include expansion, contains an agent step at any depth —
   top level, or a `parallel`, `fan_out` or `loop` body (a `condition` step
   has no body of its own). A
   command-only workflow with no network still works, as it has since issue
   #366, for #366's own reason. This narrows 061 decision 1's refusal as
   written, and the amendment is recorded in 061 and in §12.3's refusal table.
   **The alternative it beat:** restoring the refusal as 061 wrote it, which
   rejects a command-only workflow that works.

Rules already binding on 062.2, restated so the record is in one place: step
variables stay off the host argv (task 057 decision 8 — they are passed to
`docker exec` by name with their values only in the client's environment, and
`HOME`, `PATH` and `DOCKER_*` literally, so the client keeps the daemon's own);
stopping uses 061 decision 9's pid file, and the container survives a step
stop; `-i`, never `-t` (061 decision 10); a signalled exit is mapped to the
host's `-1` when vincent sent the signal; permission mode and containerization
compose (§9.4); chats keep a nil launcher. Moving the command-step path to the
same by-name environment mechanism is a follow-up, not this task.

*Left for implementation to confirm, not to redecide (2026-09-17):*

- That `docker exec -i` closes the in-container stdin when the host pipe
  closes. Claude's retained stdin in input mode depends on it; `m12` proves it.
- That a headless claude starts with an empty `~/.claude.json` under the
  vincent home when its credentials come from a token variable. This is a
  manual run against the real CLI, recorded in
  [`docs/gates/m12-gate.md`](../gates/m12-gate.md) the way M5's legs are.
- That docker applies the nested bind mounts under the tmpfs home in order.
  `m12` proves it.

## Testing

*Amended 2026-09-16:* this section is 062.2's. 062.1's tests are hermetic
against fakeagent on the host: a recording launcher per adapter, nil against
explicit host launcher, and the host launcher's process semantics in
`internal/agent`.

`internal/agent/agenttest` gains a cross-compile of `cmd/fakeagent` for
`linux/amd64` so the fake agent can be bind-mounted into a small image carrying
`git`, the way `scripts/m12-gate.sh` already does it. Everything else stays
hermetic: `go test` never needs a container daemon.

*Amended 2026-09-17 (062.2):* the cross-compile is
`agenttest.BuildFakeAgentLinux(t, arch)`, taking the architecture as a
parameter and defaulting to `amd64`, because Docker Desktop on Apple Silicon is
`arm64`. What the tests prove:

- `container.image: ""` still consults no runtime, and agent steps get
  `HostLauncher` byte for byte — the regression that matters most.
- A containerized agent step launches through the container launcher with the
  image-resolved bare binary; `agents.*.path` is ignored there, and a missing
  image CLI yields `agent_unavailable`. Claude's input mode follows the
  image's `--version` probe, not the host's.
- `Terminate` and `Kill` deliver `TERM` then `KILL` through the pid file and the
  container survives; a containerized agent run journals `container_id`, and
  recovery removes a labelled orphan and leaves an unlabelled one alone.
- The MCP URL host is `host.docker.internal` for a containerized step and
  unchanged for a host step; the gateway listener serves `/mcp/step/{run_id}`
  and `404`s `/v1/...` and `/mcp`; a non-local gateway IP falls back to the
  loopback port.
- Codex's MCP token appears in no host argv.
- With mounts on, HOME is the vincent home and the three directories are
  mounted beneath it; a user-set HOME wins, and with mounts off HOME is
  untouched.
- Decision 5's refusal fires only when an agent step is present, including one
  nested in structure and one spliced in by an include.
- §12.4's cursor `.cursor/mcp.json` sweep is pinned against a containerized
  task's worktree path.
- `m12`, on the Linux leg with real docker: a fakeagent step runs inside the
  image with transcript, token and cost records and exit code equal to the same
  scenario on the host; a containerized agent step's timeout stops the process
  while the container survives; fakeagent's `mcp-callback` scenario
  reaches `step_status` from inside the container through the gateway
  listener; a daemon killed mid agent step leaves no container behind; and
  scenario 5 is split — no network with wired MCP is refused for an agent
  workflow and accepted for a command-only one. The macOS and Windows skips stay
  as documented in 061.
