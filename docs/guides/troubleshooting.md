{% raw %}

# Troubleshooting

The failures people actually hit, what each one means, and what to do.

- [Start here: `vincent doctor`](#start-here-vincent-doctor)
- [The daemon](#the-daemon)
- [Agent CLIs](#agent-clis)
- [Projects and worktrees](#projects-and-worktrees)
- [Workflows](#workflows)
- [Tasks that block](#tasks-that-block)
- [The TUI](#the-tui)
- [Reading the log](#reading-the-log)
- [Reporting a bug](#reporting-a-bug)

---

## Start here: `vincent doctor`

When the symptom is "nothing is happening" and you do not yet know which section
below you are in, run one command:

```sh
vincent doctor          # 0 healthy · 1 problems found · 2 no daemon answered
```

It prints, in one pass: the config and data directories, whether `config.yaml`
parses and whether either is readable beyond its owner (with the `chmod` that
fixes it); the daemon's status, pid, port, uptime and version; the daemon log's
size and last lines; the database's size on disk (including its WAL sidecar),
schema version, `PRAGMA integrity_check`, per-table row counts,
workflow-snapshot bytes and how far back its event history reaches; every agent
CLI with its path, version and
[`logged_in`](agents.md#found-is-not-usable); whether the
[GitHub integration](../reference/configuration.md#github) can read issues, and
if not which piece is missing; free disk, worktree count and bytes, and any
orphaned worktrees; and task counts by state — so "12 blocked" is visible
without opening the board.

**It works with no daemon**, which is the point: the daemon being down is one of
the answers. In that mode it exits `2`, still prints everything it can read from
disk, and reports the database and task rows as *unknown — daemon not running*
rather than opening SQLite behind the daemon's back.

The bottom of the report is a `PROBLEMS` table — the closed set that makes it
exit `1`: a `config.yaml` that does not parse, a daemon alive but unresponsive,
a failed `integrity_check`, a database written by a newer vincent, orphaned
worktrees, or an unreconciled task. A missing or logged-out agent CLI is printed
plainly and deliberately does **not** set the exit code, so doctor stays usable
in a script on a machine that only ever installs one of the three adapters.

An **unreconciled** task is one whose state and step runs contradict each
other — it is `queued` (or finished) while one of its step runs is still marked
`running`. Crash recovery finalizes the old attempt before the task returns to
the queue, so this means recovery could not complete: the task is refused by
admission and will sit at `queued` forever. Recovery runs at startup, so
restarting the daemon retries it; the daemon log says why it failed the first
time.

```sh
vincent doctor --json > doctor.json   # the whole report, for a bug report
vincent doctor --fix                  # reclaim orphaned worktrees, compact the DB
vincent doctor --fix --force          # also remove orphans that have local changes
```

`--fix` needs a running daemon — every repair is a write, and only the daemon
writes. It reclaims orphaned directories (an entry under a data root that no
task row claims — the same set [`vincent gc`](../reference/cli.md#vincent-gc)
reclaims, run by the same code) and runs a real `VACUUM`. Two refusals are by
design and are reported rather than hidden: an orphan with local changes is
skipped until you pass `--force`, and the `VACUUM` is skipped while any task is
in flight, because it would stall that task mid-step.

Vincent does not run `git worktree prune` in your repositories. After doctor
removes an orphan, the repo it came from may still carry a stale registration;
`git -C <repo> worktree prune` clears it.

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

**Crash recovery is fatal too.** If it cannot close a previous run's open step
runs, the daemon refuses to start — `startup failed: crash recovery`, with the
underlying error beside it, a storage failure nearly every time. Nothing is
half-done: each task is reconciled whole or left exactly as it was found, so
starting the daemon again simply retries it.

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
healthy and then fails at the first API call. `vincent doctor` prints the answer
per adapter under `AGENTS`, re-probing each time:

```
AGENTS
codex   found  0.147.0  auth NOT LOGGED IN  /opt/homebrew/bin/codex
```

Run the CLI by hand once and log in (`codex login`, `cursor-agent login`,
`claude` interactively), then run doctor again. Cursor and codex answer this
cheaply, so vincent asks them; claude has no non-interactive auth surface, so it
shows *unknown* rather than claiming fine.

### `restricted_unsupported`

The step asked for `permission_mode: restricted` and the adapter cannot restrict
on this platform. Today that is **cursor on Windows**: its sandbox requires macOS
or Linux.

The step fails rather than running unrestricted, which is deliberate — a
restricted mode that quietly isn't restricted is worse than none. Use claude or
codex for that step on Windows, or drop the step to `full-auto` **knowingly**.

### A workflow is listed but cannot be selected — `platform_unsupported`

The workflow declares a `platforms:` list this host is not in
([schema](../reference/workflow-schema.md#platforms)). `vincent workflow ls`
shows it with status `unsupported` and the platforms it needs, the TUI's
workflow view says "not on this platform", and the new-task picker refuses it —
creating the task would only produce a run that fails at the first `cat` or
`Get-ChildItem`.

Three honest fixes, in order of preference:

1. run the task on a host the workflow names;
2. widen the list, if the workflow really is portable — `[posix, windows]` is
   the same as removing the key;
3. write a host-specific copy under the project scope, which shadows the
   global one by name.

The block reason `platform_unsupported` on an existing task means the task was
created elsewhere and its data directory has since moved between machines. The
snapshot is fine; it is the host that changed.

### An agent cannot be picked for a workflow — `input_unsupported`

The workflow has a step declaring
[`on_input: require`](../reference/workflow-schema.md#type-agent): it needs an
agent that can stop mid-run and ask you something. Codex and cursor never can —
neither CLI has a control channel — and claude can only inside the CLI version
family vincent has verified the protocol against.

Where you meet it:

- **The new-task picker greys the agent out**, and `POST /v1/tasks` answers
  `400` naming the step. Pick an agent whose `input_verdict` in
  `vincent agents` (or `GET /v1/agents`) is `supported`.
- **The workflow fails to validate** when a step pins `agent: codex` or
  `agent: cursor` outright. Change the pin, or drop `require` back to `wait` if
  the questions are optional after all.
- **A task blocks with `input_unsupported`** when the answer changed after the
  task was created — almost always a claude upgrade past the verified family.
  Check `vincent agents`; the fix is on the machine, not in the workflow, and
  `retry` refuses until it is done rather than reproducing the block.

An agent that is **not installed** never triggers any of this: an unknown
verdict is not a refusal, and you get `agent_unavailable` at run time instead if
it is still missing.

### Every cursor tool call is blocked on Windows

If cursor steps run, produce no edits, and report blocked tool calls, check
what started the daemon. A daemon parented by **Git Bash** carries `MSYSTEM`,
and `cursor-agent` imports Claude Code's hooks and composes them as PowerShell
while executing them with bash — so every hook errors, and an erroring hook
blocks the call. It is a Cursor interop bug on Windows, not a vincent one.

`unset MSYSTEM` in the launching shell does not help: the MSYS runtime
re-injects it into every child. Vincent sets the child's environment block
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

Vincent detects the default branch from `origin/HEAD`, then a local `main`, then
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
earlier run. Vincent **never deletes a branch that carries a commit**, so
leftovers accumulate if you re-create tasks with the same title — or if a branch
template has no discriminator in it, in which case the *second* task for the same
input collides every time. (Archiving cleans up only the branches that received
no commit at all, which is precisely the set that never collides with anything
you would miss.)

Most collisions are caught at creation with a `400`, but the check at creation is
racy by nature, so this block is the authority.

Two ways out. Give the task a different name and let it re-admit, which keeps its
history and transcripts:

```sh
curl -X POST .../v1/tasks/7/retry -H 'Content-Type: application/json' \
  -d '{"branch_override":"feat/second-attempt"}'
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

### `condition_error`

A step's `if:` guard did not produce a verdict. Guards must render to exactly
`true` or `false`; anything else is this error, including the two spellings a
broken guard usually produces:

- **an empty string or `<no value>`** — the guard is reading something that is
  not there. `{{ .Steps.probe.ExitCode }}` for a step that has not run yet does
  this;
- **a value that is merely truthy** — `yes`, `1`, `0`, a number. Loose
  truthiness is deliberately not accepted, because it would treat the two cases
  above as decisions.

Write comparisons, not values:

```yaml
if: '{{ ne (index .Steps "probe").ExitCode 0 }}'    # ✅ renders true or false
if: '{{ index .Steps "probe" }}'                    # ❌ renders a struct
```

This is the one block reason vincent does **not** retry. A guard is evaluated
before the step becomes an attempt, so there is no attempt to retry, and
re-rendering the same template against the same context cannot answer
differently. Fix the workflow file and retry the task — the guard is
re-evaluated from scratch, as it is on every pass.

`vincent workflow validate <file>` catches a guard that does not *parse*, but
not one that parses and renders the wrong thing — that needs the task's
context, which only exists at run time.

### `loop_limit`

A `loop` step has more iterations ahead of it than `max_iterations` allows, so
the task blocked instead of doing some of them. Two ways to get here:

- a **`for_each:` list longer than the ceiling**. It blocks *before* the first
  iteration and names the count, so nothing has run yet;
- a **`count:` the ceiling moved under** — `loop.max_iterations` was lowered
  while the task was queued or paused. The ceiling is read per loop, so a
  config reload reaches a task already in flight.

It blocks rather than truncating the list or advancing past the loop, because a
loop that ran out of iterations did not do what it was looping for, and
advancing would hand every later guard a `.Steps` saying the work is finished.
`condition` (the step type) is what a workflow uses to stop *and succeed*;
running out of tries is not that.

Three fixes, in the order worth trying:

- **filter the list at its source.** `git diff --name-only … | grep -v
  _test.go` is usually what you wanted anyway, and it keeps the filtering where
  the output already is;
- **raise the ceiling for this loop** with `max_iterations:` on the step;
- **raise it for the machine** with `loop.max_iterations` in `config.yaml`. The
  default of 10 is low on purpose: ten iterations of a three-step body is
  thirty agent runs.

### A loop ran fewer iterations than I expected after a retry

Working as intended. A loop's position is derived from its step rows on every
admission, so a `retry` resumes at the body step that failed, in the iteration
it failed in — body steps that already succeeded in that iteration are not run
again, and earlier iterations are not repeated. The detail view groups the rows
by iteration, folded, with the latest one open, which is where to check what
actually ran.

If you wanted the whole loop again, the loop is not the unit to retry — retry
the task from an earlier step, or `skip` the loop and start over.

### A workflow finished having run only half its steps

Almost certainly working as intended. Two things end a run early:

- a **`condition` step** whose guard was false. The task is `done`, and the
  detail view shows a `stopped` row where it ended;
- a chain of **`if:` guards** that all evaluated false, each leaving a
  `skipped` row with reason `condition`;
- a **`break`** inside a loop, which ends the loop successfully and moves on —
  a `stopped` row on the break step, and the loop's own iterations stopping
  short of its `count:`.

Both are visible in the task's step list, which is the place to look: the board
shows a finished task as finished, and does not try to say how much of the
workflow it walked.

If it was *not* intended, the guard is the thing to read. `.Steps` holds only
steps the workflow has moved past, so a guard naming a later step, or naming a
step that was itself skipped, evaluates against something you did not mean.

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
is ever silently abandoned. From `blocked` you can retry, edit-and-retry, repair
with a one-off agent, skip the step, or cancel the task.

Repair is the one that can change files: it runs a throwaway agent, prompted by
you and handed the blocked step's failure context, in the task's existing
worktree and branch. It always returns the task to `blocked` at the same step
with the same reason, so you look at what it did before deciding to retry. See
[repairing a blocked task](tui.md#repairing-a-blocked-task).

The block reason names what happened:

| Reason | Meaning |
|---|---|
| `check_failed` | The step ran but its `check` command exited non-zero |
| `nonzero_exit` | A command step exited non-zero |
| `agent_error` | The agent's own event stream reported an error |
| `agent_unavailable` | The adapter's CLI could not be resolved or started |
| `agent_unauthenticated` | The agent CLI is installed but not logged in (see below) |
| `usage_limit` | The agent's usage quota for the window is spent — **not** a failure; the task waits and re-runs itself (see below) |
| `retry_backoff` | Not a block reason: it is what a task shows while a step's [`retry_backoff`](../reference/workflow-schema.md#step-fields) paces the next attempt (see below). The failure itself keeps its own reason |
| `timeout` | The attempt exceeded its `timeout` and was killed |
| `input_timeout` | A mid-run question went unanswered past `input_timeout` |
| `template_error` | A template failed to render (see above) |
| `condition_error` | A step's `if:` guard rendered nothing usable (see below) |
| `loop_limit` | A `loop` has more iterations to run than it is allowed (see below) |
| `restricted_unsupported` | The adapter cannot restrict on this platform |
| `platform_unsupported` | The workflow's `platforms:` list does not admit this host |
| `input_unsupported` | A step needs an agent that can answer questions mid-run, and this one cannot (see below) |
| `transcript_limit` | The attempt's transcript hit `transcript_max_bytes` |
| `cost_limit` | The task has spent past `max_task_cost_usd` (see below) |
| `transcript_io_error` | The attempt's transcript could not be written or closed (see below) |
| `agent_protocol_error` | vincent could not read the agent's stream to the end (see below) |
| `rejected` | You rejected a manual gate |
| `shell_unavailable` | The requested shell is not installed |
| `interrupted` | The daemon stopped mid-step — **not** a failure; the step re-runs and does not consume a retry |
| `invalid_snapshot` | The task's stored workflow snapshot is unusable |

`check_failed` is the common and healthy one: it means a check caught something
the agent claimed was done. Read the step's transcript, then `E` to edit the
prompt and retry with better instructions.

### `usage_limit` — do nothing

The agent CLI stopped because your account's usage quota for the current window
is spent. Vincent treats this as a wait, not a failure:

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

**The daemon remembers which adapter it was.** A human action drops the task's
own wait, but the observation is per adapter and outlives it: the board header
badges that agent (`claude ⏳14:20`), the daemon view names the reset beside its
path and version, and the new-task form warns before you queue more work against
the same window. `→` means the CLI stated that time; `≈` means vincent estimated
it from the recheck interval. It clears itself the next time a step on that
adapter succeeds — so if the badge is still up, nothing has proved otherwise yet.
See [Agents › Nobody can tell you how much quota is left](agents.md#nobody-can-tell-you-how-much-quota-is-left).

Only the claude adapter recognizes usage-limit wording today. On codex and
cursor a quota stop still surfaces as `agent_error` or `nonzero_exit`, because
their wordings have not been captured from a real run and vincent will not guess
at one: a wrong guess would park a genuinely failed task forever.

### `retry_backoff` — also do nothing, but for a different reason

A task showing `queued · retry backoff → 14:20` in the detail header (and
`queued → 14:20` on the board) is not walled by a quota. Its
step *failed*, it has retries left, and the workflow asked for a pause between
attempts with [`retry_backoff`](../reference/workflow-schema.md#step-fields).
Like a quota hold it gives up its concurrency slot and comes back on its own.

The difference is the cost, and it is the thing to read the row for:

|  | `usage_limit` | `retry_backoff` |
|---|---|---|
| The attempt | `interrupted` | `failed`, with the reason it actually failed with |
| Retry budget | untouched | one spent |
| What ends the wait | the quota window reopening | the configured duration elapsing |
| If it keeps happening | it waits again, indefinitely | the budget runs out and the task **blocks** with the step's own reason |

So a task that keeps reappearing on `retry_backoff` is a task on its way to
being blocked — the transcripts of the attempts already made are what say why.
A quota hold is not: it will sit there until the window returns.

If you would rather not wait out a paced retry, any human action drops it —
cancel, or pause and resume.

### `agent_unauthenticated`

The agent CLI is installed and runs, but is not logged in. This one **does**
block — waiting cannot fix it. Log in with the CLI's own command (`claude`
interactively, `cursor-agent login`), then `r` to retry the task.

`vincent doctor` catches most of these before a task is ever created: it reports
`logged_in` for codex and cursor, both re-probed on every run. claude has no
cheap probe, so there the first sign is still a failed step.

### `transcript_limit`

One attempt produced more output than `transcript_max_bytes` (default 512MB).
Vincent kills the process rather than filling your disk, and writes the tripping
annotation into the file so it records *why* it ends. Usually this means an agent
or command is in a loop; fix that rather than raising the cap.

### `cost_limit` — raise the cap and retry

The task's cost so far — every attempt of every step, retries included — has
passed [`max_task_cost_usd`](../reference/configuration.md#max_task_cost_usd).
Vincent stopped it rather than letting it keep spending.

It is a policy stop, not a broken step. The step run that just finished keeps
its own state and reason — if it succeeded, it still reads `succeeded` — it
consumed no retry, and a retry that was already due did not run.

What to do:

1. Look at what the money went on. The detail view shows cost per attempt, and
   a task that spent more than you expected has usually been looping — an agent
   redoing the same work every turn, or a step retrying into the same wall.
2. If the work is worth it, raise `max_task_cost_usd` in `config.yaml` and press
   `r` to retry. The file is hot-reloaded, so no restart is needed.
3. If it is not, cancel the task, or skip the step and let the rest run.

**Retrying without raising the cap makes exactly one more attempt, then blocks
here again.** That is deliberate — the check runs after an attempt finishes, so
a retry always buys one attempt of progress, and pressing retry is your decision
to spend it. Nothing is lost either way, but nothing is free either.

Three things that surprise people:

- **You overshoot by up to one attempt.** An agent reports what it spent when it
  finishes, so the attempt that crosses the line has already run. `agent_timeout`
  is what bounds a single run.
- **The cap counts one task.** Every lane of a `fan_out` step is its own task
  with its own budget, so a tree can spend a multiple of it.
- **The total is the task's whole life and never resets.** A repair run and a
  follow-up run are step runs of that task, so their cost counts too — a
  finished task already over the cap blocks here again on the first attempt of
  its next follow-up. `skip` abandons that follow-up and returns the task to
  `done`.

If a runaway task on codex or cursor sailed past the cap, that is expected:
neither CLI reports cost, so the cap cannot see them. See
[Agents](agents.md).

### `transcript_io_error` — check the disk

Vincent could not write, encode or close the attempt's transcript, so the record
of the run is incomplete. The step **fails** rather than reporting a success it
cannot evidence: the usual cause is a full disk or a data directory whose
permissions changed under the daemon. `df` the data directory (`vincent doctor`
prints it), fix the cause, and retry — a new attempt writes a new file, so this
clears on its own once there is room.

It is **not** what a very long line of output produces. Those are captured in
`partial` pieces and the step succeeds normally; only `transcript_limit` fails a
step for size.

### `agent_protocol_error` — vincent's reader, not the CLI's fault

Vincent could not read the agent's event stream to the end, so its transcript is
missing lines the CLI wrote. The reason is named separately from `agent_error`
on purpose: the agent may have completed its work perfectly, and there is
usually nothing to fix in the CLI. Retry the task; if it repeats on the same
step, the transcript up to the cut is the thing to attach to a bug report.

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

- `vincent doctor --json` — one paste covering paths, daemon, log tail,
  database, agents, storage and task counts. **Read it first:** it carries your
  directory paths and the tail of the daemon log
- `vincent version` output, for the build details doctor does not carry
- your OS and terminal
- the relevant slice of `{data_dir}/logs/daemon.log`
- the workflow YAML, if a step misbehaved
- the step's transcript if you can share it — **read it first**, it contains the
  prompt and everything the agent did

[Open an issue](https://github.com/lezli01/vincent/issues/new/choose). Security
issues go through
[private advisories](https://github.com/lezli01/vincent/security/advisories/new)
instead — see [SECURITY.md](../../SECURITY.md).

{% endraw %}
