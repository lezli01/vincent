# CLI reference

One binary serves every role. `vincent` with no arguments opens the
[TUI](../guides/tui.md); the subcommands are thin clients over the same
localhost API.

- [Exit codes](#exit-codes)
- [Global behavior](#global-behavior)
- [`vincent`](#vincent)
- [`vincent version`](#vincent-version)
- [`vincent daemon`](#vincent-daemon)
- [`vincent service`](#vincent-service)
- [`vincent project`](#vincent-project)
- [`vincent task`](#vincent-task)
- [`vincent workflow`](#vincent-workflow)

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The daemon answered and rejected the request (bad id, invalid state transition, invalid workflow) |
| `2` | No daemon answered |

`vincent daemon status` overloads them usefully: `0` healthy, `1` not running,
`2` running but unresponsive.

## Global behavior

- **`--json`** is available on every subcommand that prints anything. Empty
  results render as `[]`, never `null`. Advisory warnings go to stderr so stdout
  stays pipeable.
- **No subcommand auto-starts a daemon.** Only the TUI does. A subcommand that
  cannot reach one exits `2` with a pointer to `vincent daemon start`.
- Clients discover the daemon by reading `{data_dir}/daemon.json` and then
  health-probing it, so a stale file from an unclean shutdown produces the same
  "no daemon" answer rather than a transport error later.
- `--help` works at every level.

---

## `vincent`

```sh
vincent
```

Opens the TUI, starting a daemon in the background if none is reachable.

## `vincent version`

```sh
vincent version
```

Prints one line: version, commit and build date. A binary built with the plain
`go build` toolchain falls back to `debug.ReadBuildInfo`, so the fields are never
empty.

## `vincent daemon`

```sh
vincent daemon [--config-dir DIR] [--data-dir DIR] [--hide-console]
```

Runs the daemon **in the foreground**, logging to stderr as well as the log
file. This is what a service manager invokes.

| Flag | Effect |
|---|---|
| `--config-dir` | Pin the config directory (for a manager with no per-process environment) |
| `--data-dir` | Pin the data directory |
| `--hide-console` | Windows only: release the console the process was handed, when it owns it. Does nothing when run by hand in a terminal |

### `vincent daemon start`

```sh
vincent daemon start
```

Starts the daemon detached in the background and returns once it is answering.

### `vincent daemon stop`

```sh
vincent daemon stop [--force]
```

Graceful shutdown: admission stops, a `daemon.shutting_down` event is emitted,
running processes get 15 seconds to exit before being killed, and their step runs
are marked `interrupted` — the same resume path as a crash, so nothing is lost.

`--force` kills the process if the graceful stop fails.

### `vincent daemon status`

```sh
vincent daemon status [--json]
```

Reports whether the daemon is running, its identity, and which agent CLIs it
resolved. Exit `0` healthy, `1` not running, `2` unresponsive.

## `vincent service`

Registers the daemon with the OS so it starts at login. Always as the invoking
user; no elevation needed on any platform. See
[Running at login](../guides/running-at-login.md).

```sh
vincent service install      # register and start; idempotent
vincent service uninstall    # stop and remove
vincent service status       # installed? running?
```

Backends: a launchd user agent (macOS), a systemd user unit (Linux), a Scheduled
Task (Windows).

## `vincent project`

### `vincent project add`

```sh
vincent project add <path> [--name NAME] [--default-branch BRANCH]
                           [--workflow NAME] [--max-parallel N] [--json]
```

Registers a local git repository.

| Flag | Default |
|---|---|
| `--name` | The directory name |
| `--default-branch` | Detected: `origin/HEAD`, then local `main`, then `master`, then the current branch |
| `--workflow` | None — tasks then name their own |
| `--max-parallel` | Unset — only the global cap applies |

Registration is refused if no default branch can be determined (a detached or
unborn HEAD); pass `--default-branch` explicitly.

### `vincent project ls`

```sh
vincent project ls [--json]
```

Lists registered projects with their ids, paths and defaults.

## `vincent task`

### `vincent task add`

```sh
vincent task add --project ID --title TITLE
                 [--workflow NAME] [--description TEXT] [--base-branch BRANCH]
                 [--branch NAME] [--priority N] [--agent NAME] [--model M]
                 [--effort E] [--json]
```

Creates a task. It is `queued` immediately — there is no draft state.

| Flag | Notes |
|---|---|
| `--project` | **Required** |
| `--title` | **Required**; also the source of the branch slug |
| `--workflow` | Defaults to the project's default workflow |
| `--base-branch` | What the task branches **from**. Defaults to the project's default branch |
| `--branch` | What the task's branch is **called**. Used verbatim and wins over any template; defaults to the project's or the global [`branch_template`](configuration.md#branch_template) |
| `--priority` | Higher runs first; default 0 |
| `--agent` / `--model` / `--effort` | The task-level override. It replaces workflow `defaults`, never an explicit step field |

Model and effort **only inherit from a level whose agent matches**, so switching
agent without setting them resets them to the new adapter's default rather than
leaking a claude alias onto a codex step.

A value no catalog knows is accepted with a warning on stderr (the CLI is the
final authority); a value belonging to a *different* adapter's catalog is
rejected with exit 1.

### `vincent task ls`

```sh
vincent task ls [--project ID] [--state STATE] [--archived] [--limit N] [--json]
```

Lists tasks. Archived tasks are excluded unless `--archived` is passed. Rows
carry the board fields — project name, step progress, and cost and token totals
rolled up across every attempt.

Valid states: `queued`, `running`, `awaiting_gate`, `awaiting_input`, `blocked`,
`paused`, `done`, `aborted`, `archived` — see
[Task lifecycle](task-lifecycle.md).

### `vincent task show`

```sh
vincent task show <id> [--json]
```

Shows one task with its step runs, the actions valid right now, and any pending
input request.

### `vincent task cancel`

```sh
vincent task cancel <id> [--json]
```

Aborts the task, killing any running process (graceful termination, then a kill
after 10 seconds). Valid from `queued`, `running`, `awaiting_input`,
`awaiting_gate`, `blocked` and `paused`; anything else exits 1 with the state it
actually found.

The remaining human actions — approve, reject, retry, skip, pause, resume,
answer, archive — are TUI and API operations; see
[the API reference](api.md#tasks).

## `vincent workflow`

Aliased as `vincent wf`.

### `vincent workflow ls`

```sh
vincent workflow ls [--project ID] [--json]
```

Lists the merged registry — built-in plus global, with scope badges and
validation status. **Add `--project` to include that repository's
`.vincent/workflows/`**, with shadowing applied; without it you see global scope
only.

Needs a daemon: only the daemon knows which projects exist.

### `vincent workflow validate`

```sh
vincent workflow validate <file> [--json]
```

Validates a workflow file. **This is the one command that needs no daemon** — no
network, no agent CLI installed — which makes it usable from a pre-commit hook or
a CI job.

Exit `0` valid, `1` invalid. Warnings (a model in no catalog) print but do not
fail the command.

---

## See also

- [Scripting vincent](../guides/scripting.md) — patterns built on these commands.
- [HTTP API](api.md) — what every subcommand calls.
- [Configuration](configuration.md).
