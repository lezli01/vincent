{% raw %}

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

# Delete a task's branch when it is archived and carries no commits past the
# base it was cut from — the branch a workflow that never writes to the
# repository leaves behind. A branch holding any commit is always kept, and a
# check git cannot answer keeps the branch too.
delete_empty_branch_on_archive: true

# Also delete that branch's upstream counterpart, when it has one. Off by
# default and honoured only when a human archives the task: a forge is shared
# with other people and the deletion cannot be undone. Nothing happens here
# unless delete_empty_branch_on_archive is also on.
delete_remote_branch_on_archive: false

# Transcripts of archived tasks older than this many days are pruned.
transcript_retention_days: 90

# Per-attempt transcript cap.
transcript_max_bytes: 512MB

# How long a quota-stopped task waits when the agent CLI named no reset time.
usage_limit_recheck_interval: 15m

# Sub-steps of one `parallel` step group running at once.
parallel:
  max_parallel: 4

# Bounds on a `type: fan_out` tree, checked at task creation.
fan_out:
  max_depth: 3
  max_tasks: 64

# Ceiling on a `type: loop` step's iterations.
loop:
  max_iterations: 10

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

# What clients render, not what the daemon does.
tui:
  board:
    group_by: [project, workflow]
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

### `branch_template`

```yaml
branch_template: "vincent/{{.ID}}{{with .Slug}}-{{.}}{{end}}"
```

The naming convention for task branches. Empty (the default) means the built-in
`vincent/{id}-{slug}`, with `vincent/{id}` when a title sanitizes to nothing.

Names resolve through a chain, most specific first:

| Level | Set where |
|---|---|
| the task's own name | `--branch` / `branch_name`, used **verbatim**, never rendered |
| project template | `PATCH /v1/projects/{id}` → `branch_template` |
| global template | this key |
| built-in | nothing configured |

Available in a template:

| Reference | Meaning |
|---|---|
| `{{.ID}}` | The task id. Resolved after the row exists, so it is always unique |
| `{{.Title}}` | The title verbatim |
| `{{.Slug}}` | The title slugged the way the built-in name does it |
| `{{.BaseBranch}}` | What the task branches from |
| `{{.Fields.NAME}}` | A task field — **errors** when the task does not set it |
| `{{.Project.Name}}`, `.Path`, `.DefaultBranch` | The project |
| `{{ slug X }}` | Slug any value: `{{ slug (index .Fields "ticket") }}` |

Two things to get right, because git will not catch them for you:

**Put a discriminator in it.** vincent never deletes a branch that carries a
commit, so a template like `feat/{{.Fields.ticket}}` collides on the *second* task
for the same ticket. Include `{{.ID}}`, or expect to name repeats by hand.
([`delete_empty_branch_on_archive`](#delete_empty_branch_on_archive) reclaims only
branches that received no commit, which is not the case this is about.)

**Prefer `{{.Fields.x}}` over `{{ index .Fields "x" }}`.** The first fails loudly when
the field is missing; the second renders *nothing*, giving you `feat/-fix-login` — a
perfectly legal branch name that is not what you meant. Use `index` only for a segment
you deliberately want optional, and wrap it:

```yaml
branch_template: 'feat/{{ with index .Fields "ticket" }}{{.}}-{{ end }}{{.Slug}}'
```

An invalid template fails at startup rather than silently reverting to the built-in
name. On hot-reload the previous value is kept and a warning logged, so one bad edit
does not also revert an unrelated change in the same save.

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

### `delete_empty_branch_on_archive`

```yaml
delete_empty_branch_on_archive: true
```

When a task is archived, delete its branch if that branch has **no commits past
the base it was cut from**. That is the branch a workflow which never writes to
the repository leaves behind — filing an issue, posting a summary, reviewing
read-only — one empty ref per run, with no reliable glob to find them again since
branch names are configurable.

The test is `git rev-list -n 1 {base}..{branch}` producing nothing: the branch tip
is an ancestor of the base. It stays correct when the base moves on after the task
started, and it is exact — **a branch carrying any commit is never deleted**.

Three refusals are deliberate, and all of them keep the branch:

| Situation | What happens |
|---|---|
| The branch has commits | Kept, reported `has_commits` |
| git cannot judge it — base branch renamed or deleted, repository gone | Kept, reported `unknown`, logged |
| The delete itself fails — the branch is checked out in another worktree | Kept, reported `error`, logged. The delete is `git branch -d`, never `-D` |

A branch problem never fails an archive: the task reaches `archived` either way,
and the branch simply survives, which is what always used to happen.
`POST /v1/tasks/{id}/archive` reports the outcome in its response and the TUI
prints it in one line; every other path logs to `daemon.log`.

The same rule runs for every task row `DELETE /v1/projects/{id}?force` is about to
delete, archived ones included — that cascade erases the branch names for good, so
it is the last moment they exist. `vincent gc` and `vincent doctor --fix` delete no
branch at all: an orphaned directory has no task row, so there is no base branch to
judge it against.

Set it to `false` to keep every branch on every path, which is what vincent did
before this key existed. Read per archive, so a hot reload applies to the next one.

### `delete_remote_branch_on_archive`

```yaml
delete_remote_branch_on_archive: false
```

Also delete the upstream counterpart of a branch that qualified above. **Off by
default**, and unlike its local sibling it is honoured only when *you* archive a
task (`POST /v1/tasks/{id}/archive`) — deleting a branch on a forge you share with
other people cannot be undone, so no unattended path does it. `DELETE
/v1/projects/{id}?force` never touches a remote.

It runs only after the local delete succeeded, and only when the branch has a
configured upstream (`branch.{name}.remote` and `branch.{name}.merge`). No
upstream means nothing was pushed as far as vincent knows, so nothing is
attempted — and the remote name is never guessed from the local one. The push is
`git push --delete {remote} {ref}` under a 60-second timeout; a rejection, an
unreachable host or a timeout is logged, reported in the archive response, and
never fails the archive.

It does nothing while `delete_empty_branch_on_archive` is `false`. That
combination still loads — a key that cannot do anything is not an invalid one —
but the daemon logs a warning at startup and on any reload that introduces it.

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

### `usage_limit_recheck_interval`

```yaml
usage_limit_recheck_interval: 15m
```

How long a task waits before vincent tries it again after its agent reported
that the account's usage quota for the current window is spent.

It applies only when the CLI named **no** reset time. When it names one, that
timestamp is used instead and this value is unused.

While it waits the task is `queued`, not `blocked`, and holds **no** concurrency
slot — everything else keeps running, and the task recovers on its own when the
window reopens. Nothing is retried in the meantime: a quota stop consumes none
of the step's retry budget. See
[Task lifecycle](task-lifecycle.md#failure-reasons).

Must be positive. There is no exponential backoff; if you know your plan's
window, set this to match it. Hot-reloaded, so a change applies to the next
task that hits a limit.

### `parallel`

```yaml
parallel:
  max_parallel: 4
```

How many sub-steps of a `type: parallel` step group run at once, when the group
does not set its own `max_parallel:`. Must be at least 1.

**This is a second concurrency dimension, and your task caps do not cover it.**
`max_parallel_tasks` counts *tasks* in a slot-holding state; a group runs
inside one such task, so one running task can keep four processes busy. A board
reading "1 running" is not a promise about the load on the machine — size this
for the hardware, not for the board.

Read when a group starts, so a reload governs the next group rather than
resizing one already running. See
[Workflow schema](workflow-schema.md#type-parallel).

### `fan_out`

```yaml
fan_out:
  max_depth: 3
  max_tasks: 64
```

Bounds on a `type: fan_out` tree, both checked when a task is created and both
reported as a `400` naming the bound crossed. `max_tasks` counts the child
tasks one creation would produce, **not** counting the root.

They are enforced at creation because the whole tree's shape is known there —
lane lists live in the task's snapshot — which is what turns a depth-3
explosion into an error in front of the person typing rather than two hundred
worktrees discovered six hours later.

Depth is unlimited by design and bounded by a default: a deeper tree is a
config edit, not a code change. See
[Workflow schema](workflow-schema.md#type-fan_out).

### `loop`

```yaml
loop:
  max_iterations: 10
```

The ceiling on a `type: loop` step's iterations, used when the step declares no
`max_iterations:` of its own. Must be at least 1.

It is a ceiling in two places. At **load** it is what `count:` is checked
against, so `count: 5000` is a validation error in front of the person typing
rather than a discovery on iteration 300. At **run time** it is what a
`for_each:` list is measured against: a list longer than the ceiling blocks the
task with `loop_limit` before the first iteration, naming the count. A loop
that hits the ceiling does not truncate and does not advance — running out of
tries is not a decision the workflow made.

The default is deliberately low. An agent step is minutes and dollars, and ten
iterations of a three-step body is already thirty agent runs. Raise it in
config for a whole machine, or per step with `max_iterations:` for the one loop
that needs more.

Read when a loop starts, so a reload governs the next loop — including one
already running, which will block with `loop_limit` if the lowered ceiling is
already behind it. See [Workflow schema](workflow-schema.md#type-loop).

### `include`

```yaml
include:
  max_depth: 5
```

How many levels of `type: include` one workflow may expand to. A workflow
including another is depth 1; that one's own include is depth 2. Must be at
least 1.

Checked when a task is created, which is where an include is resolved: the
callee's steps are spliced into the task's snapshot there and the registry is
never read again. Exceeding it is a `400` naming the depth and the bound.

There is no bound on the expanded *step count*, because step ids must be unique
across an expansion — a workflow reached twice is an error rather than a
doubling, so an expansion cannot multiply silently. Depth is the only dimension
left to bound, and a deeper chain is a config edit rather than a code change.

See [Workflow schema](workflow-schema.md#type-include).

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

### `environment`

```yaml
environment:
  inherit: all          # all (default) | none | a list of names
  unset: [MSYSTEM]      # dropped after inherit
  set:                  # literal values, last word
    LANG: C.UTF-8
```

What child processes inherit from the daemon — **agent steps, command steps
and their checks alike**. Resolved in one order: `inherit`, then `unset`, then
`set`.

Before this key existed nothing decided it. The detached daemon inherits the
shell that started it, so the same task, workflow and agent ran differently
depending on what launched the daemon, and nothing recorded which. The default
`inherit: all` is exactly that old behaviour, so this changes nothing until you
ask it to.

| Field | Meaning |
|---|---|
| `inherit` | `all` takes the daemon's whole environment; `none` takes nothing; a list takes only those names. An **empty list means nothing**, not everything |
| `unset` | Names dropped after inheriting. This is the "inherit all except" case |
| `set` | Literal values, applied last — so `set` wins over both `inherit` and `unset` |

**Values under `set` are literal.** `$` is not special and nothing is
expanded, so `PATH: "${PATH}:/opt/bin"` sets that string verbatim rather than
appending.

**`set` is not a secret store.** A literal value is exactly where an API token
would go, and nothing here encrypts one: it is plaintext in a file you may hand
to version control or sync between machines. vincent creates `config.yaml`
`0600` inside a `0700` directory and re-tightens both on every daemon start on
POSIX — see [Files and directories](files.md#the-config-directory) — but that
only keeps other local accounts out. Prefer naming the variable under `inherit`
and letting the value come from the environment that starts the daemon; see
[Security model](../security-model.md#credentials).

Command and check steps layer the [`VINCENT_*` variables](../guides/workflows.md#55-environment-variables)
and then their own `env:` on top. Neither `unset` nor `set` can touch those —
they are facts about the run, not inherited state — and a step's `env:` still
wins over everything.

#### What it is for

The case that motivated it: on Windows, a daemon started from Git Bash carries
`MSYSTEM`, and Cursor imports Claude Code's hooks and evaluates them under
bash, so **every** cursor tool call is silently blocked. No amount of
`unset MSYSTEM` in the launching shell helps — the MSYS runtime re-injects it
into every child. vincent sets the child's environment block directly, so:

```yaml
environment:
  unset: [MSYSTEM]
```

genuinely removes it.

More generally: pin the environment when a run has to be reproducible, and
`inherit: none` plus an explicit `set` when it has to be hermetic.

#### Two things to know

**vincent honours the policy as written and warns rather than correcting it.**
If the resolved environment has no `PATH` (or no `SystemRoot` on Windows) the
daemon logs a warning at startup and runs anyway. The failure it is warning
about is silent and late: adapters are resolved with `exec.LookPath` inside the
daemon and started by absolute path, so an agent with no `PATH` starts
perfectly and then fails several steps in, when the CLI shells out to git.

**The resolved variable names are logged, and never the values.** At startup
and again whenever a hot-reload changes the set:

```
level=INFO msg="child environment resolved" inherit=all unset=1 set=0 count=42
  names="APPDATA,HOME,LANG,PATH,USERPROFILE,…"
```

Values are never written to the log at any level — an environment block is
where every agent CLI's credentials live, and a log is something people paste
into issues. For the same reason the resolved environment is **not** recorded
in step transcripts, even under `debug: true`.

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

### `tui`

```yaml
tui:
  board:
    group_by: [project, workflow]
```

The one section the daemon does not act on. It validates it, hot-reloads it with
everything else and serves it on `GET /v1/config`; the TUI reads it from there.
It lives here rather than in a file of the TUI's own because the TUI is a pure
API client — it reads no configuration from disk, and a second file would be a
second path, a second reload story and a second `vincent doctor` line for one
setting.

#### `tui.board.group_by`

How the task table is grouped, outermost level first.

| Value | Board |
|---|---|
| `[project, workflow]` (default) | Projects, and the workflows of a project nested inside it |
| `[project]` | One group per project |
| `[workflow]` | One group per workflow, across every project |
| `[]` | One flat list — the table every version before this one rendered |

Accepted levels are `project` and `workflow`. An unknown level, a repeated one,
or anything that is not a list fails the load and names the key. There is no
`state` level on purpose: the board already orders by state and pins everything
waiting on a human to the top, and grouping by it would fight that.

Grouping never reorders tasks. They are sorted exactly as before and each group
takes the position of its first task, so the group holding the oldest thing
waiting on a human is the first group. Group headers carry the task count and,
when the group holds any, the needs-attention badge and count.

A grouped level costs no column — the header names it, so `PROJECT` and
`WORKFLOW` drop out and the width goes to the title.

**`g` cycles the grouping for the session** — project›workflow → project →
workflow → flat — and never writes to this file. The Tasks panel title names the
grouping whenever it is not the one configured here.

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
| `branch_template` | This repository's branch convention; overrides the global [`branch_template`](#branch_template). Set it to `null` or `""` to inherit the global one again |

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
| `EDITOR` | Used by the TUI for edit-and-retry, repair prompts, and description editing |

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
[Writing workflows](../guides/workflows.md#55-environment-variables).

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

{% endraw %}
