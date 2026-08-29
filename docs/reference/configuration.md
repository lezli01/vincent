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
# vincent daemon configuration (spec §12.3).
# Generated with defaults on first start. Edit freely: the daemon watches this
# file and hot-reloads valid changes. Invalid edits are logged and rejected,
# and the last good configuration stays active. Changes to "listen" take
# effect on the next daemon restart.

# Address the HTTP API binds to. Loopback only. Port 0 picks an ephemeral
# port, published for clients in {data_dir}/daemon.json.
listen: 127.0.0.1:0

# Global cap on concurrently running tasks. Per-project caps are configured
# on each project.
max_parallel_tasks: 3

# Fallback step timeouts, used when a workflow step declares none.
# input_timeout bounds each wait for an answer to an agent's input request
# (awaiting_input, §7.4); on expiry the attempt fails under the retry policy.
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

# Per-attempt transcript cap. A step whose transcript passes this fails with
# transcript_limit rather than filling the disk.
transcript_max_bytes: 512MB

# Ceiling on what one task may spend, in US dollars, summed over every attempt
# of every step it runs. Past it the task blocks with cost_limit at the next
# attempt boundary, so expect to overshoot by at most one attempt; raise this
# and retry to carry on. 0 disables it, which is the default.
#
# It counts one task. Each fan_out lane is its own task and gets its own
# budget, so a tree of twenty lanes may spend twenty times this. Only agents
# that report cost are counted — codex and cursor report none, so the cap is
# inert on them.
max_task_cost_usd: 0

# How long a task waits before trying again after its agent reported that the
# usage quota for the window is spent, when the CLI named no reset time. When
# it does name one, that wins and this is unused. The task keeps its place in
# the queue and holds no slot while it waits.
usage_limit_recheck_interval: 15m

# Daemon log verbosity: debug | info | warn | error.
log_level: info

# Record how every step was actually invoked in its transcript: resolved
# agent/model/effort, permission mode, working directory, and the full argv.
# Turn this on when a run does something you cannot explain, then paste the
# transcript. Off by default because argv includes the rendered prompt.
debug: false

# What child processes — agent steps, command steps and their checks —
# inherit from the daemon (T4.23). Resolved in one order: inherit, then
# unset, then set. Command steps layer the VINCENT_* variables and their own
# "env:" on top, so those are never affected.
#
# The default inherits everything, which is what the daemon always did
# implicitly. Pin it when a run has to be reproducible, or drop a single
# variable that breaks a CLI — on Windows, a daemon started from Git Bash
# carries MSYSTEM, which blocks every cursor tool call.
#
# Values under "set" are literal: "$" is not special and nothing is expanded.
environment:
  inherit: all          # all | none | a list of names, e.g. [PATH, HOME]
  # unset:
  #   - MSYSTEM
  # set:
  #   LANG: C.UTF-8

# Agent CLI locations. An empty path resolves the binary from PATH.
agents:
  claude:
    path: ""
  codex:
    path: ""
  # cursor resolves the "cursor-agent" binary — not "cursor", which is the
  # editor launcher (§9.7).
  cursor:
    path: ""

# Reading GitHub issues, so a task can be created from one (task 035). It
# applies only to a project whose "origin" remote is a github.com repository,
# and the daemon makes no call at all until you open the issue picker or name
# an issue on the command line.
#
# vincent stores no credential: it drives the "gh" CLI when it is installed
# and authenticated, and otherwise reads GITHUB_TOKEN or GH_TOKEN out of the
# environment the daemon inherited. Set enabled to false to stop the daemon
# reading GitHub at all.
#
# poll_interval is how often the daemon reconciles the link between a task and
# its pull request, by matching an open pull request's head branch against the
# task's branch (task 052). It is the only background network traffic vincent
# makes: set it to 0 to switch the reconciler off and keep the rest of the
# integration, which then calls GitHub only when you ask it to.
github:
  enabled: true
  poll_interval: 5m

# Tell someone when a task needs them, without a client attached (task 046).
# The daemon runs "command" whenever a task enters one of the states in "on",
# and writes a JSON envelope describing the transition to the command's stdin
# — task id and title, from/to, block_reason, project, workflow, step cursor,
# branch and worktree path — so a one-line script can write a message without
# calling back into the API.
#
# "command" is argv, not a shell line: nothing is expanded, quoted or split,
# and there is no shell on any platform. Both keys are needed; either alone
# loads and warns. Off by default.
#
# The states are the ones from §6, most usefully: blocked, awaiting_gate,
# awaiting_input, done. Only root tasks notify — a fan_out lane is a task row,
# and a twenty-lane tree would otherwise send twenty-one messages.
#
# It is fire-and-forget: at most 4 notifiers run at once, a child is killed
# after 10 seconds, failures are logged and never retried, and nothing is
# replayed for events that happened while the daemon was down.
#
# WARNING: this is arbitrary code the daemon runs as you, and its argv can
# carry a secret (a webhook URL), which is part of why this file is
# owner-only.
#
# notify:
#   on: [blocked, awaiting_gate, awaiting_input]
#   command: ["/usr/local/bin/notify-me"]

# What clients render, not what the daemon does. The daemon validates these,
# hot-reloads them and serves them on GET /v1/config; the TUI reads them from
# there.
#
# group_by nests the task table under headers, outermost level first. Accepted
# levels: project, workflow. Use [] for one flat list of tasks. A grouped
# level drops its own column — the header already names it — and "g" cycles
# the grouping for the session without touching this file.
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

**Put a discriminator in it.** Vincent never deletes a branch that carries a
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

That same pass also drops
[idempotency keys](api.md#transport-and-auth) older than a fixed 24 hours. This
setting does not govern them: `0` keeps every transcript, and expired keys still
go.

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

### `max_task_cost_usd`

```yaml
max_task_cost_usd: 5.00
```

A ceiling, in US dollars, on what **one task** may spend. `0` — the default —
means no cap, so this changes nothing until you set it.

The figure it compares against is the task's cost so far: the sum of every
attempt of every step it has run, retries included — and repair runs and
follow-up runs, which are step runs of that task like any other. It is a
lifetime total and never resets. That is the same number the board and detail
views show. When it goes over, the task is `blocked` with `cost_limit` and
nothing further runs.

It is a **block, not a step failure**. The step run that finished keeps its own
state and its own reason — if it succeeded, it still reads `succeeded` — no
retry is consumed, and a retry that was already due does not happen.

**You will overshoot by up to one attempt.** The check runs after an attempt
finishes, because that is when an agent reports what it spent; there is no
running total to watch mid-run. An `agent_timeout` still bounds a single run.

**It counts one task.** Every lane of a `fan_out` step is its own task with its
own budget, so a cap of `$5.00` on a twenty-lane fan-out permits `$100` across
the tree.

**It is inert on codex and cursor.** Only claude reports cost. A task run
entirely on an agent that reports none is never blocked by this, whatever you
set — vincent will not estimate money from token counts. See
[Agents](../guides/agents.md).

The remedy when a task blocks is to raise this value and press `retry`; the file
is hot-reloaded, so no restart is needed. Retrying *without* raising it makes
one more attempt and blocks again. See
[Troubleshooting](../guides/troubleshooting.md).

Must not be negative.

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
`{data_dir}/logs/daemon.log`, is size-capped and rotated, and is read by
[`vincent daemon logs`](cli.md#vincent-daemon-logs) and the TUI's daemon view.

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
to version control or sync between machines. Vincent creates `config.yaml`
`0600` inside a `0700` directory and re-tightens both on every daemon start on
POSIX — see [Files and directories](files.md#the-config-directory) — but that
only keeps other local accounts out. Prefer naming the variable under `inherit`
and letting the value come from the environment that starts the daemon; see
[Security model](../security-model.md#credentials).

Agent, command and check steps layer the [`VINCENT_*` variables](../guides/workflows.md#55-environment-variables)
on top of that, and command and check steps then layer their own `env:`. Neither
`unset` nor `set` can touch the `VINCENT_*` block — those are facts about the
run, not inherited state — and a step's `env:` still wins over everything.

#### What it is for

The case that motivated it: on Windows, a daemon started from Git Bash carries
`MSYSTEM`, and Cursor imports Claude Code's hooks and evaluates them under
bash, so **every** cursor tool call is silently blocked. No amount of
`unset MSYSTEM` in the launching shell helps — the MSYS runtime re-injects it
into every child. Vincent sets the child's environment block directly, so:

```yaml
environment:
  unset: [MSYSTEM]
```

genuinely removes it.

More generally: pin the environment when a run has to be reproducible, and
`inherit: none` plus an explicit `set` when it has to be hermetic.

#### Two things to know

**Vincent honours the policy as written and warns rather than correcting it.**
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

### `github`

```yaml
github:
  enabled: true
  poll_interval: 5m
```

Whether the daemon may read GitHub issues and pull requests, so a task can be
created from an issue and linked to the pull request opened from its branch. It
is an **opt-out**: on by default, and it costs nothing until you use it.

It governs **reading only**. Nothing under this key writes to GitHub, and no
GitHub call happens while a step runs — the daemon calls when you open the issue
picker, when it creates a task from an issue, when a client asks for a pull
request, and on the reconciler's tick. The issue is stored on the task at that
moment and never re-read, which is why a step's `.Issue` renders offline and
cannot fail because GitHub is down. A pull request is the opposite: only the
*link* is stored, and its title, state, draft and merged status are re-read
every time, because a snapshot of them would go stale and lie.

`poll_interval` is how often the daemon reconciles those links: it lists each
GitHub-based project's open pull requests and links the ones whose head branch
equals a task's branch. It is vincent's **only** standing outbound network
traffic, which is why it has its own key — set it to `0` and the reconciler
stops while the rest of the integration keeps working on demand. A link a human
made by hand is never overwritten by it, and a link a human removed is never
re-applied.

It applies only to a project whose `origin` remote parses as a github.com
repository. On every other project it does nothing at all — the issue row is not
offered, and no `gh` process is started.

Setting it to `false` stops the daemon reading GitHub entirely: the TUI's issue
row disappears, `GET /v1/projects/{id}/github` reports `disabled`, the pull
request listing answers `409`, the reconciler stops, and creating a task with
`--github-issue` is refused. Read per use, so a
[reload](#reload-semantics) governs the next call and the next tick.

**There is deliberately no token key here.** Vincent stores no credential of its
own. It uses the `gh` CLI when that is installed and logged in — which carries
your own host, enterprise and SSO configuration — and otherwise reads
`GITHUB_TOKEN` or `GH_TOKEN` from the environment the daemon inherited. Both are
described under [environment variables](#environment-variables), and
`vincent doctor` reports which one is in play.

### `notify`

```yaml
notify:
  on: [blocked, awaiting_gate, awaiting_input, done]
  command: ["/usr/local/bin/notify-me"]
```

Run a command when a task enters one of these states. **Off by default** — both
keys are empty, and nothing is spawned.

It exists because vincent's premise is that you start work and walk away, and
the daemon is designed to run with no client attached. Without this, the only
alert in the whole system is the TUI's terminal bell, which rings on
`awaiting_input` and only while a board is open: a task could wait a full day
for an answer, fail on the timeout, and the first you knew was the next time you
looked.

**`on`** is a list of [task states](task-lifecycle.md). Any of the ten is
accepted; the four above are the ones worth waking up for. A name that is not a
state **fails the load** and names the value, so the daemon keeps its last good
configuration rather than silently never firing.

**`command`** is **argv, not a shell line**. The first element is the program;
the rest are passed through unchanged. Nothing is expanded, split, quoted or
interpreted, and there is no shell on any platform — `command: ["notify.sh
--urgent"]` looks for a program with a space in its name. Both keys are needed:
either one alone loads, warns in the log, and never fires.

#### The envelope

The daemon writes one JSON object to the command's standard input and closes it.
It is enriched on purpose: a notifier told only `{task_id, to}` would have to
call back into the API with a bearer token to say anything useful, which defeats
a one-line script.

```json
{
  "event_id": 1841,
  "ts": "2026-08-28T09:30:00Z",
  "type": "task.state_changed",
  "task_id": 42,
  "title": "Fix the flaky gate",
  "from": "running",
  "to": "blocked",
  "block_reason": "step_failed",
  "queued_reason": "",
  "current_step": 2,
  "steps_total": 5,
  "worktree_path": "/home/you/.local/share/vincent/worktrees/42",
  "branch": "vincent/42-fix-the-flaky-gate",
  "project_id": 7,
  "project": "vincent",
  "workflow": "review"
}
```

`block_reason` is empty unless `to` is `blocked`. A transition into
`awaiting_input` additionally carries the agent's question:

```json
  "input": { "kind": "question", "summary": "Which migration should I keep?" }
```

`steps_total` comes from the task's own workflow snapshot, so it is the count
for *that run* even if the workflow file has been edited since.

A one-liner that turns it into a desktop notification on macOS:

```sh
#!/bin/sh
jq -r '"\(.title) → \(.to)"' | xargs -0 terminal-notifier -title vincent -message
```

#### What it does and does not promise

| | |
|---|---|
| **Root tasks only** | A `fan_out` lane is a task of its own, so a twenty-lane tree would send twenty-one messages. Lanes are skipped; the parent's own transitions fire. |
| **At most 4 at once** | Drained from a 64-entry queue. Five tasks blocking together all notify; only a genuinely full queue drops, and a drop is logged. |
| **10 s per command** | Fixed, not configurable. Past it the command's whole process tree is killed and the failure is logged. |
| **Never retried** | A failure is logged with the exit code and the tail of its stderr, and dropped. |
| **Never replayed** | Only transitions the running daemon observes fire. A weekend of downtime does not produce a storm on the next start. |
| **Never blocks a task** | A notifier that hangs, fails or is missing changes nothing about the task it was about. |

The environment the command inherits is the one
[`environment`](#environment) resolves, like every other process the daemon
spawns. It gets **no** `VINCENT_*` variables — those belong to command steps —
because the envelope on stdin is this hook's whole contract.

Read per event, so a [reload](#reload-semantics) governs the next transition. It
is not served on `GET /v1/config`: no client needs it, and `command` can
reasonably hold a webhook URL with a token in it. See the
[security model](../security-model.md) for what running it means.

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
| `GITHUB_TOKEN` / `GH_TOKEN` | Read by the [`github`](#github) integration when `gh` is absent or logged out. The daemon inherits whatever the process that started it had; vincent never stores a token of its own, and never reports its value — `vincent doctor` names the variable only |

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

Agent, command and check steps additionally receive `VINCENT_TASK_ID`,
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
