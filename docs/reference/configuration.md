# Configuration

The daemon reads one file, `{config_dir}/config.yaml`. It is written with
commented defaults on first start, and the daemon **watches it and hot-reloads
valid changes** — there is no apply step.

- [Reload semantics](#reload-semantics)
- [The default file](#the-default-file)
- [Keys](#keys)
- [Per-project settings](#per-project-settings)
- [Environment variables](#environment-variables)
- [Reading the config in effect](#reading-the-config-in-effect)

---

## Reload semantics

| Situation | Behavior |
|---|---|
| Valid edit while running | Applied immediately |
| **Invalid edit** while running | Rejected and logged; the **last good** configuration stays active |
| Invalid config at **startup** | Fatal — the daemon refuses to start and says which key |
| `listen:` changed | Ignored until the next daemon restart, with a warning |
| Unknown key | Rejected (strict decoding) — a typo is an error, not a setting that silently never applied |

Omitted keys keep their built-in defaults, so a partial file is fine.

## The default file

This is what first start writes, verbatim:

```yaml
# Address the HTTP API binds to. Loopback only. Port 0 picks an ephemeral
# port, published for clients in {data_dir}/daemon.json.
listen: 127.0.0.1:0

# Global cap on concurrently running tasks. Per-project caps are configured
# on each project.
max_parallel_tasks: 3

# Fallback step timeouts, used when a workflow step declares none.
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h

# Transcripts of archived tasks older than this many days are pruned.
transcript_retention_days: 90

# Per-attempt transcript cap.
transcript_max_bytes: 512MB

# Daemon log verbosity: debug | info | warn | error.
log_level: info

# Record how every step was actually invoked in its transcript.
debug: false

# Agent CLI locations. An empty path resolves the binary from PATH.
agents:
  claude:
    path: ""
  codex:
    path: ""
  cursor:
    path: ""
```

## Keys

### `listen`

```yaml
listen: 127.0.0.1:0
```

The address the HTTP API binds to. **Loopback only** — `127.0.0.1`, `::1` or
`localhost`; anything else is rejected at load. Port `0` picks an ephemeral port,
published for clients in `{data_dir}/daemon.json`. Pin a port if you want a
stable URL for scripts.

There is no TLS and no remote binding. The API is loopback plus a bearer token,
which is what keeps other local users and drive-by browser requests out; CORS is
disabled for the same reason.

Changes take effect on the next restart.

### `max_parallel_tasks`

```yaml
max_parallel_tasks: 3
```

Global cap on tasks in a slot-consuming state. Must be at least 1.

Only `running` and `awaiting_input` consume a slot. A task at a gate, blocked, or
paused does not — a human is the bottleneck there, and holding a slot would
starve everything else. `awaiting_input` **does** hold its slot, because the
agent process is alive mid-step and killing it would lose the session the answer
belongs to.

Real agents cost money; this is the knob that bounds it.

### `defaults`

```yaml
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h
```

Fallback timeouts used when a workflow step declares none. All are Go duration
strings (`45m`, `1h30m`, `90s`) and all must be positive.

| Key | Applies to |
|---|---|
| `agent_timeout` | One attempt of an agent step |
| `command_timeout` | One attempt of a command step, and checks |
| `input_timeout` | Each wait in `awaiting_input` — measured **per request**, so a new question starts a fresh window |

A timed-out process is killed and the attempt counts as a failure under the
normal retry policy. The step clock **pauses** while a task is
`awaiting_input`: it measures agent work, not human latency.

Workflow `defaults:` and per-step fields override these.

### `transcript_retention_days`

```yaml
transcript_retention_days: 90
```

Transcripts of **archived** tasks older than this are pruned, measured from the
archive time. `0` disables pruning. The pruner runs at daemon start and on a
24-hour ticker, which is what makes retention work on a daemon that survives
reboots rather than only on the restarts it no longer has.

Task and step rows are **never** deleted — only the transcript files.

### `transcript_max_bytes`

```yaml
transcript_max_bytes: 512MB
```

Per-attempt transcript cap. Written as a human size (`512MB`, `1GB`, `4096` for
bare bytes); suffixes are binary multiples.

Past the limit, the transcript latches, later writes are dropped, the process
tree is killed, and the step fails with `transcript_limit`. The tripping
annotation is written whole and bypasses the cap, so the file records *why* it
ends — a half-written line would turn a size failure into a parse failure for
every later reader.

`0` disables the cap.

### `log_level`

```yaml
log_level: info
```

One of `debug`, `info`, `warn`, `error`. The log lives at
`{data_dir}/logs/daemon.log`, is size-capped and rotated, and is tailed by the
TUI's daemon view.

### `debug`

```yaml
debug: false
```

Records, in **every step's transcript**, the exact conditions the step ran under:
the resolved agent, model and effort, the permission mode, the working directory,
and the full argv of the process spawned.

Turn it on when a run does something you cannot explain — "the step asked for
restricted, did it get it?" is otherwise unanswerable without reading the stored
snapshot out of the database. Off by default because argv carries the rendered
prompt and transcripts are something people paste into issues.

### `agents`

```yaml
agents:
  claude: { path: "" }
  codex:  { path: "/usr/local/bin/codex" }
  cursor: { path: "" }
```

Where to find each adapter's CLI. An empty path resolves the binary from `PATH`;
an explicit path is used as given and **never consults `PATH`**, which makes it
the standing fix for a CLI the daemon cannot see.

`cursor` resolves the **`cursor-agent`** binary, never `cursor` — that one is the
editor launcher and would open a GUI.

## Per-project settings

These live in the database, not in `config.yaml`, because they belong to a
registered repository rather than to the daemon. Set them at registration, edit
them in the TUI's projects view, or `PATCH /v1/projects/{id}`:

| Setting | Meaning |
|---|---|
| `name` | Display name (defaults to the directory name) |
| `path` | The repository root; can be re-pointed if the repo moves |
| `default_branch` | What new tasks branch from |
| `default_workflow` | Used when a task names none |
| `max_parallel_tasks` | Per-project cap, applied on top of the global one |

```sh
vincent project add /path/to/repo --name api --default-branch develop \
  --workflow feature-pr --max-parallel 2
```

## Environment variables

| Variable | Effect |
|---|---|
| `VINCENT_CONFIG_DIR` | Override the config directory outright |
| `VINCENT_DATA_DIR` | Override the data directory outright |
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` | Honored on Linux in the normal way |
| `EDITOR` | Used by the TUI for edit-and-retry and description editing |

The two `VINCENT_*` overrides are how the test suite isolates state, and they
are equally useful for running a second, throwaway instance:

```sh
VINCENT_CONFIG_DIR=/tmp/v-cfg VINCENT_DATA_DIR=/tmp/v-data vincent daemon start
```

> **They do not reach a service by themselves.** A service does not inherit the
> shell that installed it, so `vincent service install` **captures** the
> directories in effect and writes them into the unit. Change them and reinstall,
> or the CLI and the service will quietly use different databases. See
> [Running at login](../guides/running-at-login.md).

Command and check steps additionally receive `VINCENT_TASK_ID`,
`VINCENT_TASK_TITLE`, `VINCENT_PROJECT_NAME`, `VINCENT_PROJECT_PATH`,
`VINCENT_WORKTREE`, `VINCENT_BRANCH`, `VINCENT_BASE_BRANCH`, `VINCENT_STEP_ID`,
`VINCENT_STEP_ATTEMPT` and `VINCENT_WORKFLOW` — see
[Writing workflows](../guides/workflows.md#writing-portable-command-steps).

## Reading the config in effect

The API exposes it read-only, which is the reliable way to see what the daemon
actually loaded after a reload:

```sh
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:$PORT/v1/config | jq
```

The TUI's daemon view shows the same thing, alongside the adapters detected and
the log tail.

---

## See also

- [Files and directories](files.md) — where `{config_dir}` and `{data_dir}` are.
- [Workflow schema](workflow-schema.md) — the per-step overrides for these
  defaults.
- [Agent CLIs](../guides/agents.md).
