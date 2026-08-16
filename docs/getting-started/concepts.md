# Concepts

Five nouns and one process. Everything else in vincent is a consequence of how
these fit together.

---

## The daemon

A background process that owns **all** state and **all** execution: the SQLite
database, the git worktrees, and the agent CLI subprocesses. It listens on
loopback only, with a bearer token read from a file in your data directory.

Every client — the TUI, the `vincent` subcommands, `curl` — is a thin consumer
of that API. This is not an implementation detail; it is the property the whole
design is built on:

- **Killing every client changes nothing about running work.** Close the TUI,
  reboot your terminal, log out of the session: tasks keep running.
- **There is one writer.** One process opens the database, so writes serialize
  and no client can corrupt state by racing another.
- **Recovery is the daemon's job.** Every transition is persisted *before* it is
  acted on, so a crash mid-step is recoverable: on restart, interrupted step
  runs are finalized, verified orphan processes are killed, and the step re-runs
  as an attempt that does not consume a retry.

Start it explicitly with `vincent daemon start`, or let the TUI start one. Make
it survive reboots with [`vincent service install`](../guides/running-at-login.md).

## A project

A registered local git repository. Registering one records its path, a display
name, the default branch new tasks branch from, an optional default workflow,
and an optional per-project concurrency cap.

```sh
vincent project add /path/to/repo --name api --default-branch develop
```

vincent never modifies your checkout. It reads the repository to create
worktrees; your working tree, your current branch and your stash are untouched.

## A workflow

An ordered list of steps, written as YAML. Workflows live in three scopes, and
a name in a narrower scope shadows the same name in a wider one:

| Scope | Location |
|---|---|
| Project | `.vincent/workflows/*.yaml` inside the repository |
| Global | `{config_dir}/workflows/*.yaml` |
| Built-in | `adhoc` — a single agent step, always available |

The daemon watches both directories and reloads on save. A file that fails to
parse is reported invalid; the previously loaded version keeps running.

There are exactly **three step types**:

- **`agent`** — runs an agent CLI headlessly in the worktree, with a prompt.
- **`command`** — runs a shell command in the worktree.
- **`manual`** — stops and waits for a person (a gate).

`check` is a **field** on agent and command steps, not a fourth type. It is the
mechanism that stops an agent from grading its own homework: the step succeeds
only if the check command also exits 0.

See [Writing workflows](../guides/workflows.md).

## A task

One run of one workflow against one project. Creating a task snapshots the
workflow, so editing the YAML afterwards never changes a task already in
flight — the snapshot is that task's execution truth.

A task moves through a small state machine:

```
queued → running → done
   ▲        │  ├──────────→ awaiting_gate  (a manual step)
   │        │  ├──────────→ awaiting_input (the agent asked a question)
   │        │  └──────────→ blocked        (a step failed, retries exhausted)
   └── paused / retry / approve / skip …
```

The full table of states, the human actions valid in each, and what each action
does is in [Task lifecycle](../reference/task-lifecycle.md). Two properties are
worth knowing up front:

- **Nothing is silently abandoned.** A step that exhausts its retries blocks the
  task and waits for a human. It does not skip ahead and it does not fail the
  task.
- **Every task representation carries `available_actions`** — the actions valid
  right now, computed by the daemon from one state machine. The TUI's action bar
  and the CLI both render that list rather than re-deriving it, so "what may
  happen next" has exactly one definition.

## A worktree

Every task gets its own `git worktree` on its own branch, named
`vincent/{id}-{slug}` by default, based on the task's base branch. You can set a
different convention per project or globally, or name one task's branch outright —
see [Configuration](../reference/configuration.md). That is where the agent
runs and where commands execute.

What this **does** buy you: two tasks in the same repository never collide, your
own checkout is never touched, and the diff of a task is a real git diff you can
read before anything is pushed.

What it **does not** buy you: privilege isolation. A full-auto agent runs as
you, with your credentials and your network. See the
[Security model](../security-model.md).

Archiving a task removes its worktree and keeps the record. It keeps the branch
too, unless that branch has no commits past the base it was cut from — a
workflow that never writes to the repository leaves nothing on its branch, and
archiving deletes it rather than leaving an empty ref behind. **A branch
carrying any commit is never deleted.** Turn the cleanup off with
[`delete_empty_branch_on_archive: false`](../reference/configuration.md#delete_empty_branch_on_archive).

---

## How a step actually runs

Steps execute strictly in order. Executing one means:

1. **Render templates.** Prompts, `run`, `check` and `instructions` are Go
   `text/template`, rendered against the task, project, worktree, and the
   results of completed steps. Rendering uses `missingkey=error`, so a typo
   fails the step *before* any process starts rather than writing a silent hole
   into a prompt.
2. **Run the body.** An agent CLI subprocess, a shell command, or a wait for a
   human.
3. **Evaluate success.** An agent step succeeds when the process exits 0 *and*
   its event stream produced a terminal result *and* any declared `check` exits
   0. A command step succeeds when it exits 0 and its check does. A manual step
   succeeds when someone approves it.
4. **On failure, retry** up to `max_retries` (default 1, i.e. two attempts). For
   agent steps the previous failure is appended to the retried prompt as a
   structured block, so the agent is told exactly what went wrong.
5. **Advance**, persist, repeat. When the last step succeeds the task is `done`.

**Every agent step is a fresh session.** No conversation is resumed between
steps or attempts. State flows forward through the worktree (files and commits)
and through `{{.Steps}}` in templates. That is what keeps steps individually
re-runnable and keeps context windows small.

## The scheduler

Admission from `queued` to `running` happens in exactly one place, in a single
goroutine, which is what makes both concurrency caps safe:

- `max_parallel_tasks` — a global cap (default 3).
- A per-project cap, set on the project.

Order is by priority, then creation time. Only `running` and `awaiting_input`
consume a slot — a task waiting at a gate, blocked, or paused does not, because
a human is the bottleneck there and holding a slot would starve everything else.

`awaiting_input` **does** keep its slot: the agent process is alive mid-step,
idle on its stdin, and killing it would lose the very session the answer belongs
to.

## Agent adapters

Three adapters ship: `claude`, `codex`, `cursor`. Each runs the CLI you already
installed and authenticated; vincent stores no credentials.

Adapters differ in what they can do, and the differences are **documented, never
faked**. A capability an adapter lacks is stated and ignored at run time rather
than emulated: codex ships no model catalog and no mid-run input; cursor has no
effort concept at all (it lives in the model id) and cannot honor `restricted`
on Windows, where the step fails rather than quietly running full-auto.

See [Agent CLIs](../guides/agents.md).

## Events and transcripts

Two streams come out of a run, and they have different durability guarantees on
purpose:

- **State events** — durable. `task.created`, `task.state_changed`,
  `task.step_advanced`, `project.*`, `workflow.registry_changed`, and friends
  are written to the database with a monotonic id and served over SSE, so a
  client that reconnects with `Last-Event-ID` misses nothing.
- **Live output** — ephemeral. Agent output, tool calls, reasoning and usage
  chunks stream on the per-task SSE stream and are *not* stored in the events
  table. They are durable in the **transcript file** instead: catch-up means
  fetching the transcript, then following live.

Every attempt of every step writes a JSONL transcript at
`{data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl`, containing the
agent's own stream verbatim plus vincent's namespaced `vincent.*` annotations.
Because transcripts are normalized on *read*, improving a parser improves
transcripts already on disk.

---

## Where to go next

- [Quickstart](quickstart.md) — do it.
- [Writing workflows](../guides/workflows.md) — the part you will spend time in.
- [Task lifecycle](../reference/task-lifecycle.md) — states and actions in full.
- [The spec](../spec.md) — the normative version of everything above.
