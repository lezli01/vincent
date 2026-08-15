# Troubleshooting

The failures people actually hit, what each one means, and what to do.

- [The daemon](#the-daemon)
- [Agent CLIs](#agent-clis)
- [Projects and worktrees](#projects-and-worktrees)
- [Workflows](#workflows)
- [Tasks that block](#tasks-that-block)
- [The TUI](#the-tui)
- [Reading the log](#reading-the-log)
- [Reporting a bug](#reporting-a-bug)

---

## The daemon

### Every subcommand exits 2

Exit `2` means nothing answered. Start one:

```sh
vincent daemon start
vincent daemon status      # 0 healthy, 1 not running, 2 unresponsive
```

Subcommands never auto-start a daemon by design — only the TUI does. If
`daemon status` says *unresponsive* (exit 2 with a daemon present), the process
is alive but not serving; `vincent daemon stop --force` clears it.

### "A daemon is already running"

Single-instance is enforced with a lock file in the data directory that releases
automatically when the process dies. If you get this after a hard kill, check
whether the daemon really is running (`vincent daemon status`) before assuming a
stale lock — an orphaned lock is rare, an orphaned daemon is not.

### The TUI starts its own daemon even though the service is installed

Almost always: the service and your shell resolved **different data
directories**, so the TUI looked for `daemon.json` where the service did not
write one.

- On Windows, this is the signature of a pre-Scheduled-Task install that ran as
  LocalSystem. See
  [Running at login → upgrading](running-at-login.md#upgrading-from-a-pre-scheduled-task-version).
- Otherwise, check whether `VINCENT_DATA_DIR` is set in one context and not the
  other, and reinstall the service from the shell you actually use.

### The daemon starts and immediately exits

Read `{data_dir}/logs/daemon.log`. An invalid `config.yaml` at startup is fatal
and says which key and line. (An invalid config on *hot reload* is not fatal —
the last good config keeps running and the rejection is logged.)

## Agent CLIs

### An agent CLI is not found

`vincent daemon status` and the TUI's daemon view list what the daemon resolved.
If your shell finds the binary and the daemon does not, it is a `PATH` problem —
in this order:

1. **Service-installed daemons capture `PATH` at install time** on macOS and
   Linux. Install a CLI after that, and the service cannot see it. Fix:
   `vincent service install` again, from a shell where `which claude` works.
2. **Point at it directly** and skip `PATH` entirely:

   ```yaml
   agents:
     claude: { path: "/opt/homebrew/bin/claude" }
   ```

3. **Cursor:** vincent resolves **`cursor-agent`**, never `cursor` — `cursor` is
   the editor launcher. If only `cursor` is on your `PATH`, `cursor-agent` is not
   installed.

Detection is cached by binary identity (path + mtime + version), so upgrading a
CLI is picked up automatically. Force a re-probe with `R` in the new-task view
or `GET /v1/agents?refresh=true`.

### The adapter is found but every run fails

Suspect authentication. An installed-but-unauthenticated CLI probes as perfectly
healthy and then fails at the first API call. Run the CLI by hand once and log
in. Cursor is the one adapter that can answer this cheaply — it reports
`logged_in` — so for claude and codex vincent shows *unknown* rather than
claiming fine.

### `restricted_unsupported`

The step asked for `permission_mode: restricted` and the adapter cannot restrict
on this platform. Today that is **cursor on Windows**: its sandbox requires macOS
or Linux.

The step fails rather than running unrestricted, which is deliberate — a
restricted mode that quietly isn't restricted is worse than none. Use claude or
codex for that step on Windows, or drop the step to `full-auto` **knowingly**.

### Every cursor tool call is blocked on Windows

If cursor steps run, produce no edits, and report blocked tool calls, check
what started the daemon. A daemon parented by **Git Bash** carries `MSYSTEM`,
and `cursor-agent` imports Claude Code's hooks and composes them as PowerShell
while executing them with bash — so every hook errors, and an erroring hook
blocks the call. It is a Cursor interop bug on Windows, not a vincent one.

`unset MSYSTEM` in the launching shell does not help: the MSYS runtime
re-injects it into every child. vincent sets the child's environment block
directly, so this does:

```yaml
environment:
  unset: [MSYSTEM]
```

A daemon started by `vincent service install` (a Scheduled Task in your logon
session) never had `MSYSTEM` in the first place.

### A restricted codex step cannot commit

In a linked worktree the real git directory lives under the main repository,
outside `--sandbox workspace-write`. Move the commit into a `command` step,
which runs outside the agent's sandbox.

## Projects and worktrees

### `project add` refuses: no default branch

vincent detects the default branch from `origin/HEAD`, then a local `main`, then
`master`, then the current branch. A detached or unborn HEAD hits none of them:

```sh
vincent project add /path/to/repo --default-branch main
```

### `worktree_dirty`

The task's worktree has uncommitted changes at a point where vincent needs it
clean — most often when archiving. Archiving removes the worktree, so
uncommitted work would be lost; it refuses unless you force it.

Inspect the worktree first (its path is on the task detail view), commit or
discard, then archive again.

### `branch_exists` / `worktree_path_occupied`

The branch this task would use, or its worktree directory, already exists from an
earlier run. vincent **never deletes a branch**, so leftovers accumulate if you
re-create tasks with the same title — or if a branch template has no discriminator
in it, in which case the *second* task for the same input collides every time.

Most collisions are caught at creation with a `400`, but the check at creation is
racy by nature, so this block is the authority.

Two ways out. Give the task a different name and let it re-admit, which keeps its
history and transcripts:

```sh
curl -X POST .../v1/tasks/7/retry -d '{"branch_override":"feat/second-attempt"}'
```

Or delete the leftover branch yourself, then retry:

```sh
vincent task ls --archived      # find the branches vincent made
git worktree list
```

A related failure is a **ref hierarchy conflict**: `feat/foo` cannot be created
while `feat/foo/bar` exists, because git stores refs as a path hierarchy. The
message names the branch that is in the way.

`worktree_path_occupied` has a second cause with a different fix: a crash between
creating the worktree and recording its path leaves the directory on disk while
the task claims nothing, so every later admission finds it occupied. Run
[`vincent gc`](../reference/cli.md#vincent-gc) — it reclaims exactly the
directories no task claims — then retry the task. `vincent gc --dry-run` shows
you the path first, and `--force` is needed when git cannot judge the directory
because the repository behind it is gone.

### `base_branch_missing` / `project_path_missing`

The base branch was deleted, or the repository moved. Re-point the project
(`PATCH /v1/projects/{id}`, or edit it in the projects view) or set the task's
base branch to one that exists.

## Workflows

### A workflow file does not show up

- `vincent workflow ls` shows built-in and global scopes only. Add
  `--project <id>` to include that repository's `.vincent/workflows/`.
- The daemon reloads on save; a file that fails to parse is reported as
  **invalid** and the previously loaded version keeps running. The workflows view
  shows validation status per entry, and `vincent workflow validate <file>` tells
  you exactly what is wrong.
- Check the scope: a project file **shadows** a global file of the same name.

### `template_error`

A template referenced something that does not exist. Rendering uses
`missingkey=error` on purpose, so this fails the step *before* any process
starts rather than writing a silent hole into a prompt.

The usual cause is an optional task field read directly. Read it defensively:

```yaml
{{with index .Task.Fields "ticket"}}Ticket: {{.}}{{end}}
```

The other cause is `.Steps.<id>` for a step that has not completed — `.Steps`
holds *completed* steps only.

### `shell_unavailable`

The shell a command step asked for is not installed. On Windows vincent uses
`pwsh -NoProfile -Command` and falls back to `powershell`; a step pinned to
`shell: sh` on a machine with no POSIX shell fails here. Pin a shell that exists,
or write the command so the platform default can run it.

### Validation warns about a model

A value in no catalog at all passes with a **warning** rather than an error: the
CLI is the final authority on what your account can run. A value found in a
*different* adapter's catalog is an error — that is a claude effort reaching a
codex step, and it is a real mistake.

## Tasks that block

A step that exhausts its retries blocks the task and waits for a human. Nothing
is ever silently abandoned. From `blocked` you can retry, edit-and-retry, skip
the step, or cancel the task.

The block reason names what happened:

| Reason | Meaning |
|---|---|
| `check_failed` | The step ran but its `check` command exited non-zero |
| `nonzero_exit` | A command step exited non-zero |
| `agent_error` | The agent's own event stream reported an error |
| `agent_unavailable` | The adapter's CLI could not be resolved or started |
| `agent_unauthenticated` | The agent CLI is installed but not logged in (see below) |
| `usage_limit` | The agent's usage quota for the window is spent — **not** a failure; the task waits and re-runs itself (see below) |
| `timeout` | The attempt exceeded its `timeout` and was killed |
| `input_timeout` | A mid-run question went unanswered past `input_timeout` |
| `template_error` | A template failed to render (see above) |
| `restricted_unsupported` | The adapter cannot restrict on this platform |
| `transcript_limit` | The attempt's transcript hit `transcript_max_bytes` |
| `rejected` | You rejected a manual gate |
| `shell_unavailable` | The requested shell is not installed |
| `interrupted` | The daemon stopped mid-step — **not** a failure; the step re-runs and does not consume a retry |
| `invalid_snapshot` | The task's stored workflow snapshot is unusable |

`check_failed` is the common and healthy one: it means a check caught something
the agent claimed was done. Read the step's transcript, then `E` to edit the
prompt and retry with better instructions.

### `usage_limit` — do nothing

The agent CLI stopped because your account's usage quota for the current window
is spent. vincent treats this as a wait, not a failure:

- the attempt is recorded `interrupted` and consumes **no** retry;
- the task goes back to `queued` and **gives up its concurrency slot**, so other
  work carries on;
- the row shows the time it will resume — `queued → 14:20` on the board, and
  `queued · usage limit → 14:20` in the detail header;
- when that time comes the step re-runs by itself. There is nothing to press.

The resume time is the reset the CLI reported. When it reported none, vincent
waits [`usage_limit_recheck_interval`](../reference/configuration.md#usage_limit_recheck_interval)
(default 15 minutes) and tries again, repeating until the window reopens. If you
know your plan's window, set that knob to match it.

If you would rather not wait, cancel the task, or pause and resume it to try
again immediately — any human action drops the wait.

Only the claude adapter recognizes usage-limit wording today. On codex and
cursor a quota stop still surfaces as `agent_error` or `nonzero_exit`, because
their wordings have not been captured from a real run and vincent will not guess
at one: a wrong guess would park a genuinely failed task forever.

### `agent_unauthenticated`

The agent CLI is installed and runs, but is not logged in. This one **does**
block — waiting cannot fix it. Log in with the CLI's own command (`claude`
interactively, `cursor-agent login`), then `r` to retry the task.

The check the daemon view shows for cursor (`logged_in`) catches most of these
before a task is ever created; claude and codex have no cheap probe, so the first
sign is a failed step.

### `transcript_limit`

One attempt produced more output than `transcript_max_bytes` (default 512MB).
vincent kills the process rather than filling your disk, and writes the tripping
annotation into the file so it records *why* it ends. Usually this means an agent
or command is in a loop; fix that rather than raising the cap.

### A task is stuck in `awaiting_input`

It is waiting on you. It is pinned to the top of the board with a badge; press
`enter` on the row to open the answer form. If nobody will ever be there, set
`on_input: deny` in the workflow so vincent answers immediately and the run stays
unattended.

Note that `awaiting_input` **keeps its concurrency slot** — the agent process is
alive mid-step. A forgotten question does occupy a slot until `input_timeout`
(default 24h) expires.

### A task is stuck in `queued`

It is waiting for a scheduler slot. Check the caps: `max_parallel_tasks`
globally (default 3) and the per-project cap. Raise the priority
(`vincent task add --priority`, `PATCH /v1/tasks/{id}`, or `+` in the new-task
form) to move it up the queue.

## The TUI

### I cannot select text with the mouse

The TUI owns mouse events while the mouse is on. Press `M` to toggle it off, or
hold shift while dragging — most terminals treat that as "bypass the
application".

### `esc` does not quit

By design: `esc` closes one layer (popup → screen → filter). `q` quits.

### The screen looks wrong after a resize

Re-render with any key. If a pane is genuinely unusable, the terminal is likely
too small for the three-panel layout — the board alone needs a reasonable width
to show state and step columns.

## Reading the log

```
{data_dir}/logs/daemon.log
```

Rotated and size-capped. The TUI's daemon view tails it — and reads it **from
disk**, so it still works when the daemon is the thing that died.

Turn up detail in [`config.yaml`](../reference/configuration.md):

```yaml
log_level: debug
debug: true      # record each step's resolved settings and full argv in its transcript
```

`debug: true` is the one to reach for when a step "ran with the wrong settings":
it writes the resolved agent, model, effort, permission mode, working directory
and the exact argv into the step's transcript. It is off by default because argv
can carry a prompt and transcripts get pasted into issues.

## Reporting a bug

Include:

- `vincent version` output
- your OS and terminal
- the relevant slice of `{data_dir}/logs/daemon.log`
- the workflow YAML, if a step misbehaved
- the step's transcript if you can share it — **read it first**, it contains the
  prompt and everything the agent did

[Open an issue](https://github.com/lezli01/vincent/issues/new/choose). Security
issues go through
[private advisories](https://github.com/lezli01/vincent/security/advisories/new)
instead — see [SECURITY.md](../../SECURITY.md).
