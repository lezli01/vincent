# CLI reference

One binary serves every role. `vincent` with no arguments opens the
[TUI](../guides/tui.md); the subcommands are thin clients over the same
localhost API.

- [Exit codes](#exit-codes)
- [Global behavior](#global-behavior)
- [`vincent`](#vincent)
- [`vincent version`](#vincent-version)
- [`vincent doctor`](#vincent-doctor)
- [`vincent daemon`](#vincent-daemon)
- [`vincent service`](#vincent-service)
- [`vincent project`](#vincent-project)
- [`vincent task`](#vincent-task)
- [`vincent workflow`](#vincent-workflow)
- [`vincent gc`](#vincent-gc)

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The daemon answered and rejected the request (bad id, invalid state transition, invalid workflow) |
| `2` | No daemon answered |

`vincent daemon status` overloads them usefully: `0` healthy, `1` not running,
`2` running but unresponsive. `vincent doctor` follows the same shape: `0`
healthy, `1` problems found, `2` no daemon answered.

## Global behavior

- **`--json`** is available on every subcommand that prints anything. Empty
  results render as `[]`, never `null`. Advisory warnings go to stderr so stdout
  stays pipeable.
- **No subcommand auto-starts a daemon.** Only the TUI does. A subcommand that
  cannot reach one exits `2` with a pointer to `vincent daemon start` —
  except `vincent doctor`, which prints its whole report first, because the
  daemon being down is one of the things it is there to tell you.
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

## `vincent doctor`

```sh
vincent doctor [--json] [--fix [--force]]
```

One report answering "why is nothing running?". Seven groups:

| Group | Rows |
|---|---|
| Paths | config dir, data dir, config file, and whether it parses |
| Daemon | running / not running / unresponsive, pid, port, version, uptime |
| Log | daemon log path, size, mtime, and the last 20 lines |
| Database | path, size, applied schema version, `PRAGMA integrity_check` |
| Agents | per adapter: found, path, version, and `logged_in` |
| Storage | disk free under the data dir, worktree count and bytes, orphans |
| Tasks | counts by state, so "12 blocked" is visible without opening the board |

Exit `0` when everything checked is healthy, `1` when problems were found (they
are listed under `PROBLEMS`), `2` when no daemon answered.

**Unhealthy is a closed set**: `config.yaml` exists and does not parse, the
daemon is alive but not answering, `integrity_check` is not `ok`, the database
is at a schema version newer than this binary understands, or orphaned worktrees
are present. A missing or logged-out agent CLI is reported and deliberately does
*not* set the exit code — most machines have one of three adapters installed,
and a doctor that exits `1` almost everywhere is no use in a script. Neither do
task counts: twelve blocked tasks is information, not a defect.

**Without a daemon** the report is still printed in full — paths, whether the
config parses, adapter detection, the log tail, disk free and the worktree
count — and the database and task rows read `unknown — daemon not running`.
They are not read from a second process: only the daemon opens the database.

`--json` emits the whole report for scripting and for pasting into a bug report.

### `vincent doctor --fix`

Reclaims orphaned directories and compacts the database. Both are writes, so the
daemon performs them: `--fix` without a running daemon is refused, and the
report is printed anyway.

- **An orphan** is an entry under a data root that no task row claims — the
  residue of a forced project delete or of a removal that failed partway. This
  is the same scan and the same removal [`vincent gc`](#vincent-gc) runs, so the
  two commands cannot disagree; `gc` is the one to reach for when reclaiming is
  the whole point. A non-directory there is reported and never removed.
- A worktree with **local changes** (untracked files included) is skipped unless
  you add `--force`. An orphan whose dirty check cannot run at all — the project
  repo is gone, so the directory is just files — counts as dirty for this
  purpose: nothing is deleted on the strength of a check that did not happen.
- vincent does **not** run `git worktree prune` in your repositories. A stale
  registration can therefore survive there; the report says so and names the
  command.
- Compaction is a real `VACUUM`, and it is **skipped while any task is running
  or awaiting input** — the rewrite takes an exclusive lock, and stalling a step
  mid-write is worse than declining. The skip is reported with its reason.

```sh
vincent doctor --json | jq '.problems[]'
vincent doctor --fix --force
```

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

The `PLATFORMS` column is the workflow's
[platform restriction](workflow-schema.md#platforms); status `unsupported`
means this host is not in it, so the workflow is listed but cannot back a task
here.

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

## `vincent gc`

```sh
vincent gc [--dry-run] [--force] [--json]
```

Reclaims directories under the data dir that **no task claims**. Two things
produce one: deleting a project whose worktree removal failed (the task rows go
regardless, so nothing can name the directory again), and a crash between
creating a worktree and recording its path.

```
KIND       PATH                                  SIZE     STATUS
worktree   ~/.local/share/vincent/worktrees/41   12.4MB   removed
worktree   ~/.local/share/vincent/worktrees/58   3.1MB    skipped: dirty_unknown
transcript ~/.local/share/vincent/transcripts/41 88.2KB   removed
reclaimed 2 of 3 orphan(s), 12.5MB freed
```

| Flag | Effect |
|---|---|
| `--dry-run` | Prints the identical report and removes nothing |
| `--force` | Also removes worktrees that are dirty or that git cannot judge |
| `--json` | The raw report, including per-entry `skip_reason` and `error` |

**Skip reasons.** A worktree with local changes — untracked files included, the
same rule `git worktree remove` uses — is `worktree_dirty`. One whose repository
has been deleted, or in which `git worktree prune` has run, is `dirty_unknown`:
`git status` fails there, so nobody can say what is inside. That is the *common*
case for a real orphan, so expect a plain `vincent gc` to skip most of what it
lists and to need `--force` once you have looked at the paths. A file sitting
directly under a data root is `not_a_directory` and is never removed.

**What it never does.** It never deletes a branch, never touches a directory any
task row claims, never removes anything outside `{data_dir}/worktrees` and
`{data_dir}/transcripts`, and never modifies a task row. A task pointing at a
worktree that is gone is *reported* at the end of the output and left alone —
recover that one with a retry, which recreates the worktree from the branch.

An entry that could not be removed (a file locked by another process, a
permissions problem) is reported on its own line and the run continues; the
reclaimed totals count only what actually went.

The daemon reports the same orphans at startup — one warning per directory in
`daemon.log`, plus a count on `GET /v1/info` and in the TUI daemon view — but it
never deletes anything by itself.

---

## See also

- [Scripting vincent](../guides/scripting.md) — patterns built on these commands.
- [HTTP API](api.md) — what every subcommand calls.
- [Configuration](configuration.md).
