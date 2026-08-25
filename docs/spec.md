# vincent engineering specification

**Status:** Living engineering reference · **Owner:** László Szabó

> [!NOTE]
> This document preserves code-level contracts and design decisions for
> maintainers. It is not the product landing page or the recommended starting
> point for users. See the [feature tour](features.md),
> [documentation home](README.md), and [workflow schema](reference/workflow-schema.md)
> for the current user-facing view of vincent.

vincent is a single-user, local-first control plane for AI coding-agent
workloads. A background daemon owns all state and execution; the TUI, CLI, and
other integrations are clients of its localhost HTTP API. Work continues when
no client is attached.

The current product combines isolated git worktrees, durable scheduling,
Claude Code/Codex/Cursor adapters, a structured workflow language, deterministic
checks, human gates and mid-run input, crash recovery, live output and diffs,
and REST + SSE automation in one cross-platform binary. The numbered sections
below remain stable because source comments cite them; dated amendments record
how the implementation has evolved.

---

## 1. Overview

An engineer registers any number of local git repositories ("projects"), authors
reusable **workflows**, and creates **tasks** against a project. A workflow can
combine agent prompts, shell commands and manual gates with parallel groups,
isolated fan-out, conditions, loops and reusable includes. Each task runs inside
a dedicated **git worktree**, so parallel tasks never collide. Agent steps run
locally installed Claude Code, Codex, or Cursor CLIs headlessly. The daemon
schedules tasks under configurable global and per-project concurrency caps,
records full transcripts and run metrics, streams live progress over SSE, and
pauses for human input when a step fails, a manual gate is reached, or a running
agent asks a structured question (§7.4).

**Nothing about delivery is hardcoded.** Whether a finished task pushes a branch,
opens a PR, or just leaves a diff for review is entirely determined by the workflow's
steps.

## 2. Goals and non-goals

The lists in this section preserve the original design scope so later decisions
remain understandable. They are not the current product feature matrix; dated
amendments in this specification record superseded boundaries, while
[Features](features.md) describes what ships now.

### Original goals

- One daemon per developer machine; localhost-only API; the OS user is the trust boundary.
- Cross-platform: Windows, macOS, Linux from day one.
- Full decoupling: daemon runs and progresses tasks with zero clients attached.
- Unlimited projects, workflows, and tasks; every task isolated in its own worktree.
- Workflow-defined delivery: agent, command, and manual-gate step types; linear execution.
- Two agent adapters — Claude Code and Codex — behind one interface. (A third,
  Cursor, is **milestone M5**: §9.7. Its adapter merged ahead of M4 on
  2026-08-11 — see §19 ‡ — so the tree carries three. This goal is annotated
  rather than restated as "three": it records what v1 was *scoped* to deliver,
  and the scope was met before the third arrived.)
- Agent, model, and effort selectable per workflow, per step, and per task at
  creation; selectable options discovered ad hoc from the installed CLIs (§9.6).
- Unattended operation by default (agents run full-auto), with per-workflow/step overrides.
- Monitoring: live task board, per-task live output tail, per-step duration/token/cost metrics, durable transcripts.
- Human-in-the-loop when needed: gates, blocked tasks, and mid-run agent questions alert in the client; a question is answered into the still-live agent session (§7.4).
- Crash-safe: daemon restart recovers and resumes interrupted work automatically.

### Original non-goals — explicitly deferred

- Web UI (the API is designed for it; it is not built in v1).
- Multi-user / remote access / multi-host orchestration.
- OS desktop notifications.
- LLM-as-judge step verification.
- Workflow branching or conditionals within one task. *Amended 2026-08-17
  (task 014): parallel steps and step fan-out are no longer deferred —
  see §7.5 and §20.*
- Sandboxing agents beyond worktree isolation (a worktree is not a security boundary).
- Secret management (daemon inherits the user's environment).

## 3. Decision record

Decisions fixed during the design interview; the rest of this document elaborates them.

| # | Topic | Decision |
|---|-------|----------|
| 1 | User model | Single-user, local daemon; localhost-only API; no accounts |
| 2 | Platforms | Windows, macOS, Linux in v1 |
| 3 | Stack | Go; single binary; Bubble Tea TUI; net/http API |
| 4 | API | REST + SSE over localhost HTTP; bearer-token file auth |
| 5 | Persistence | SQLite (pure-Go driver) + transcript/log files on disk |
| 6 | Agent driver | Headless CLI subprocess with JSON event stream, behind `AgentAdapter` |
| 7 | Step success | Process exit 0 + result event, plus optional per-step check command |
| 8 | Failure policy | Per-step retries with failure feedback; exhausted → task `blocked`, human decides |
| 9 | Workflow storage | YAML files: global (config dir) + per-project (`.vincent/workflows/`); runs snapshot content |
| 10 | Step context | Fresh agent session per step; Go `text/template` context; state persists via worktree |
| 11 | Delivery | Owned entirely by workflow steps; no hardcoded push/PR/merge behavior |
| 12 | Step types | `agent`, `command`, `manual` (gate); `parallel` and `fan_out` added by row 23/24, `condition` by row 25 |
| 13 | Permissions | Agents run full-auto by default; workflow/step can restrict |
| 14 | Concurrency | Configurable global cap **and** per-project cap on parallel running tasks |
| 15 | Task shape | Title, markdown description, project, workflow, base branch, free-form key/value fields |
| 16 | Monitoring | Board + live tail (SSE) + per-step duration/tokens/cost + durable transcripts |
| 17 | Worktrees | Under daemon data dir; branch `vincent/{task}-{slug}` by default, configurable per project or globally (§10, task 001); removed only on archive; branches never auto-deleted |
| 18 | Daemon lifecycle | TUI auto-starts daemon; `vincent daemon start/stop/status`; optional OS service install; interrupted steps re-run on restart |
| 19 | Name | `vincent` |
| 20 | v1 scope | Everything above, both agent adapters |
| 21 | Agent/model/effort selection | Adapter-native values; per-step resolution `step > task override > workflow defaults > adapter default` with agent-scoped inheritance; options probed ad hoc from the installed CLIs, merged with a curated catalog, free text always allowed (§8.6, §9.6) |
| 22 | Agent input requests | Structured requests only (`question`/`permission`); new `awaiting_input` state that keeps its slot; step clock pauses, bounded by `input_timeout` (default 24h); normalized schema + raw passthrough; `POST /v1/tasks/{id}/answer`; per-adapter capability (claude yes, codex no); `on_input: wait\|deny` opt-out; TUI-level alerts only (§6, §7.4, §13.2, §15) |

| 23 | Parallel steps | `type: parallel` runs sub-steps concurrently in the task's one worktree: one step, one index, one slot, no branch and no merge. `manual`, nested groups and `on_input: require` are refused inside one; `max_parallel` (default 4) is a second concurrency dimension the §11 caps do not govern (§7.5, task 014) |
| 24 | Workflow fan-out | `type: fan_out` makes each lane a real child task with its own worktree and branch, merged back `--no-ff` in declared order at the end of the same step. The parent parks in `awaiting_children` holding no slot, so no depth deadlocks; a conflict blocks by default, a lane that did not finish blocks the join, and the tree's bounds are checked at creation (§7.6, task 014) |
| 25 | Conditions between steps | `if:` guards any step (skip and carry on) and any fan-out lane or group sub-step (subset the set); `type: condition` ends the sequence with the task `done`; `allow_failure:` turns the failures a step itself produced into an advance, so a guard has a run's own findings to read. Guards are §8.4 templates that must render exactly `true` or `false`, re-evaluated every time and never cached (§7.7, task 015) |

## 4. Architecture

```
┌────────────────────────────── host machine ──────────────────────────────┐
│                                                                          │
│  ┌──────────┐   REST + SSE    ┌──────────────────────────────────────┐   │
│  │ vincent  │◄───────────────►│           vincent daemon             │   │
│  │  (TUI)   │  127.0.0.1:PORT │                                      │   │
│  └──────────┘   bearer token  │  ┌────────────┐  ┌────────────────┐  │   │
│  ┌──────────┐                 │  │ HTTP API   │  │   Scheduler    │  │   │
│  │ curl /   │◄───────────────►│  │ (REST+SSE) │  │ (caps, queue)  │  │   │
│  │ scripts  │                 │  └────────────┘  └───────┬────────┘  │   │
│  └──────────┘                 │  ┌────────────┐  ┌───────▼────────┐  │   │
│  ┌──────────┐                 │  │ Workflow   │  │  Task Runner   │  │   │
│  │ web UI   │  (future)       │  │ Registry   │  │ (per task FSM) │  │   │
│  └──────────┘                 │  │ (YAML)     │  └───────┬────────┘  │   │
│                               │  └────────────┘  ┌───────▼────────┐  │   │
│                               │  ┌────────────┐  │ Step Executors │  │   │
│                               │  │  SQLite +  │  │ agent│cmd│gate │  │   │
│                               │  │ transcripts│  └───────┬────────┘  │   │
│                               │  └────────────┘          │           │   │
│                               └──────────────────────────┼───────────┘   │
│                                            AgentAdapter  │  subprocess   │
│                              ┌────────────────────┬──────┴─────┐         │
│                              ▼                    ▼            ▼         │
│                        claude (CLI)         codex (CLI)   sh/pwsh        │
│                              runs inside per-task git worktrees          │
│   repo A ──worktree──► ~/…/vincent/worktrees/12    (branch vincent/12-…) │
│   repo A ──worktree──► ~/…/vincent/worktrees/13    (branch vincent/13-…) │
│   repo B ──worktree──► ~/…/vincent/worktrees/14    (branch vincent/14-…) │
└──────────────────────────────────────────────────────────────────────────┘
```

Principles:

- **Daemon owns everything.** Clients never touch git, the DB, or agent processes
  directly; they only speak the API. Killing every client changes nothing about
  running work.
- **One writer.** Only the daemon opens the SQLite DB (WAL mode). Clients get state
  via the API.
- **Crash-first design.** Every state transition is persisted before it is acted on;
  recovery is a first-class path, not an afterthought (§12.4).

## 5. Domain model

### 5.1 Project

A registered local git repository.

| Field | Notes |
|---|---|
| `id` | integer, auto-increment |
| `name` | display name, unique; defaults to repo directory name |
| `path` | absolute path to the repo root; must contain a `.git` |
| `default_branch` | base branch for new tasks (auto-detected from `origin/HEAD`, falls back to `main`/`master`; editable) |
| `default_workflow` | optional workflow name preselected in task creation |
| `max_parallel_tasks` | per-project cap; `null` = no per-project limit (global cap still applies) |
| `branch_template` | *added 2026-08-13 (task 001).* Optional branch-naming template for this project; `null` inherits `config.yaml`'s `branch_template`, and an unset config means the built-in name. Parsed when written, so a broken template fails at `PATCH /v1/projects/{id}` rather than at every task creation |

Registering a project performs validation only (path exists, is a git repo, worktrees
supported). The repo itself is never modified by registration.

### 5.2 Workflow

A named, ordered list of steps defined in YAML (§8). Workflows live in files, not the
DB; the daemon maintains a registry of parsed workflows from three scopes:

- **Built-in:** shipped in the binary. Lowest precedence — a global or project file
  of the same name shadows it. Two are present:
  - `adhoc` — the single-step agent workflow used when a task is created without
    naming one (§5.3).
  - `create-workflow` — *added 2026-08-23 (task 024).* One agent step that writes
    another workflow file. It declares two task fields: `workflow_name`
    (required; becomes both the new workflow's `name:` and its file name, so it
    is held to `^[a-z0-9][a-z0-9._-]*$`) and `global`. Its prompt carries the
    `vincent-workflows` skill,
    embedded from `skills/vincent-workflows/SKILL.md` at build time, so the
    published skill is the only copy of that guidance. The step runs under
    `on_input: wait`: it may stop and ask a design question the repository
    cannot answer, at the §7.4 cost of holding its slot while parked and
    failing on `input_timeout` if nobody replies. Its optional boolean task field `global` picks the
    destination registry: `true` writes `{config_dir}/workflows`, and `false` or
    unset writes `{repo}/.vincent/workflows` for the task's own project. Both are
    the live registry directory rather than the task's worktree — the registry
    watches project repo roots, so a file left in a worktree would not become a
    workflow until the branch merged.
- **Global:** `{config_dir}/workflows/*.yaml` — available to every project.
- **Project:** `{repo}/.vincent/workflows/*.yaml` — available to that project only,
  git-versioned and shareable with a team. A project workflow **shadows** a global
  workflow with the same name.

The daemon watches both locations (fsnotify) and reloads on change. Invalid files are
surfaced as registry errors (visible in TUI/API) without breaking valid ones.

*Amended 2026-08-23 (issue #136).* What a scope may source is bounded, because a
project scope is whatever a registered repository contains and it is read while the
scope is **catalogued** — at daemon start, at project registration, and on every
reload — before any human picks or runs a workflow:

- Only **regular files** are sourced. A symlink, FIFO, socket, device or directory
  whose name ends in `.yaml`/`.yml` is never opened or followed. The type is checked
  on the directory entry and again on the opened handle, so replacing a checked file
  between the two does not smuggle one in.
- A source is at most **1 MiB**. A file of exactly that size still parses; a larger
  one is rejected without being read whole. Fixed, not configurable.

A file rejected by either bound becomes an **invalid registry entry** naming the path
and the violated type or bound — the same treatment as a file that fails to parse, so
its valid siblings in the scope stay available.

### 5.3 Task

A unit of work delivered by running a workflow against a project.

| Field | Notes |
|---|---|
| `id` | integer, auto-increment; used in branch and worktree names |
| `project_id` | FK |
| `title` | short summary; slugged into the branch name |
| `description` | markdown, arbitrary length |
| `fields` | open string key/value map (e.g. `ticket: OPS-123`); available to templates. The selected workflow may declare expected names and validate those values (§8.1.2), but undeclared names remain accepted and recorded |
| `workflow_name` | name as resolved at creation time |
| `workflow_snapshot` | full YAML content captured at creation; **execution always uses the snapshot**, so later edits to workflow files never mutate in-flight or historical tasks |
| `base_branch` | defaults to project `default_branch` |
| `branch_name` | `vincent/{id}-{slug}` by default (slug: lowercase title, `[a-z0-9-]`, max 40 chars). *Amended 2026-08-13 (task 001):* configurable through the chain `built-in < config.yaml < project < per-task literal`. Resolved and persisted inside the task's insert transaction, so no committed task carries an empty one |
| `worktree_path` | assigned when the worktree is created |
| `priority` | integer, default 0; higher runs first |
| `agent_override` / `model_override` / `effort_override` | optional, chosen at creation (§13.2); replace the workflow's `defaults` but never an explicit step field (§8.6) |
| `state` | §6 |
| `current_step` | index into the snapshot's step list |
| `pending_input` | normalized InputRequest (§7.4) while state is `awaiting_input`; cleared on answer, timeout, or process exit |
| `pending_follow_up` | *Added 2026-08-25 (task 027).* The follow-up run a human asked for from `done` or `aborted` (§6): its compiled workflow, the run form and text it came from, the optional agent/model/effort, the **origin state** the task is returned to, the 1-based **round**, and the run's own **step cursor**. NULL when no follow-up is in flight |


*Amended 2026-08-17 (task 014).* A snapshot may carry a whole fan-out tree: a
`fan_out` step's lanes are resolved through the registry at creation and
written in, so later edits to a lane's workflow file never reach a task that
already exists. Nesting lives only at authoring time — each lane's steps become
a **child task's own flat snapshot** when it is spawned, so `edit + retry`,
`Marshal` and the locator never meet a nested workflow.

*Amended 2026-08-25 (task 027).* A **follow-up run** (§6) never touches
`workflow_snapshot`. The snapshot is the workflow as authored, and the only
thing that rewrites it is `edit + retry`'s in-place override of a step that is
already in it. A follow-up's steps live in `pending_follow_up` and its rows live
past the snapshot's last index (§5.4), so `step_total`, "step k of n" and the
task 017 graph go on describing the workflow somebody wrote rather than
whatever an operator ran afterwards.

`current_step` is left where the finished run put it — one past the last step —
for the whole of a follow-up, and a follow-up is walked by the cursor inside
`pending_follow_up` instead. Two cursors rather than one is what lets a `manual`
gate or a `fan_out` inside a follow-up park the task and be resumed: the gate's
`approve` advances the follow-up's cursor, and nothing has to decide which of
two meanings `current_step` carries at that moment.

### 5.4 StepRun

One attempt at executing one step of one task. Every attempt (including retries and
re-runs after interruption) is a distinct StepRun row — history is append-only.

Records: step id/index/type, attempt number, state (`running`, `succeeded`, `failed`,
`interrupted`, `approved`, `rejected`, `skipped`, `stopped`), timestamps, agent/model/effort used (as resolved per §8.6), exit code,
check exit code, failure reason, skip reason, transcript file path, input/output tokens, cost (USD,
nullable — not all agents report cost), input wait time (ms spent in `awaiting_input`,
§7.4 — excluded from duration metrics).

*Amended 2026-08-18 (task 015).* `stopped` is a `condition` step whose guard
was false: the run ended there, deliberately and successfully (§7.7). It is
neither a success nor a failure of the step — the step evaluated perfectly, and
its answer was "stop". `skip_reason` says why a `skipped` row is skipped:
`condition` for a false guard, empty for the human `skip` action (§6), which
share one state.

*Amended 2026-08-24 (task 025).* An ad-hoc **repair** run (§6) is a StepRun like
any other — same table, same states, same transcript, same token and cost
accounting — recorded under the **reserved step id `__repair`** at the *blocked
step's* `step_index`. `attempt` numbers repairs of that step independently, so a
second repair is attempt 2 of the repair rather than attempt N+1 of the step.

The reserved id is the whole mechanism. Attempts are counted per
`(task_id, step_index, step_id, iteration)`, so a row under a different step id
is invisible to the blocked step's retry budget with no query changing at all
(§7.2). It begins with an underscore, which no workflow step id may (§8.1), so
it cannot collide with an id somebody wrote. A `kind` column and a separate
repair ledger were both considered and rejected: they pay a migration and a
second history for a separation this composite key already gives.

Clients must tell a `__repair` row apart from an attempt of the step at its
index and render it as its own entry (§15) — displaying it as an attempt of that
step would say the opposite of what happened. For the same reason a repair row
is not visible in `.Steps` (§8.4) to any later step's prompt or guard: it is not
a step of the workflow, and no workflow author wrote that key.

*Amended 2026-08-25 (task 027).* A **follow-up run** (§6) is likewise a StepRun
like any other, and is told apart by **position** rather than by a reserved id.
Round *n* of a task whose snapshot has *k* steps writes every row it produces at
`step_index = k + n - 1`. That cursor space is unused, so a row at or past *k*
is unambiguously a follow-up row and its round is legible from the index alone.

The consequences are all things that then need no new mechanism. Distinct rounds
occupy distinct indices, so the same `(task_id, step_index, step_id, iteration)`
key that keeps a repair out of a step's retry budget keeps round 2 out of round
1's — a second follow-up is round 2, not attempt 2. `iteration` keeps its §7.8
meaning, so a `loop` inside a follow-up numbers its passes normally. And the
step ids are the ones the follow-up's author wrote, never rewritten, so `if:`
guards and `.Steps` references *inside* a follow-up workflow keep working. The
rows of a multi-step round share one index the way a `parallel` group's
sub-steps do, and are told apart by step id.

Clients must tell a follow-up row apart from an attempt of a workflow step —
`step_index >= step_total` is the whole test — and render it as its own round
(§15), never as step *k+1* of a workflow that did not grow.

For `.Steps` (§8.4) a follow-up step sees the **original workflow's** rows and
its **own round's**, and nothing else. Reading the finished run's results is the
point of a follow-up; rows from *earlier* rounds are hidden for the reason a
`__repair` row is, because nobody wrote them into the workflow being run. Where
a follow-up workflow reuses an id from the original workflow, the round's own
row shadows it.

## 6. Task lifecycle

```
                 create
                   │
                   ▼
              ┌────────┐   slot free    ┌─────────┐  input request  ┌────────────────┐
   ┌─────────►│ queued ├───────────────►│ running │◄───────────────►│ awaiting_input │
   │          └────────┘  (scheduler)   └──┬──┬──┬┘    answer*      └────────────────┘
   │   approve ▲   ▲ retry/skip            │  │  │
   │           │   │                       │  │  │ all steps
   │      ┌────┴───┴─┐   manual step       │  │  │ succeeded
   │      │          │◄────────────────────┘  │  ▼
   │      │ awaiting │                        │ ┌──────┐
   │      │  _gate   │      step failed,      │ │ done │
   │      └────┬─────┘      retries exhausted ▼ └──┬───┘
   │           │ reject   ┌─────────┐              │
   │           └─────────►│ blocked │              │
   │                      └──┬──────┘              │
   │ resume                  │ abort               │ archive
┌──┴─────┐  pause            ▼                     ▼
│ paused │◄───────┐      ┌─────────┐          ┌──────────┐
└────────┘ (from  │      │ aborted ├─────────►│ archived │
           queued/│      └─────────┘  archive └──────────┘
           running┘

* answer resumes the step in place; input_timeout fails the attempt (normal
  retry/blocked policy §7.2) — a wait that ends without an answer while retries
  remain (input_timeout, agent process death, withdrawn request) re-enters
  `running` via the engine's `input_closed` transition; exhausted retries go to
  `blocked` as usual. On daemon restart, an interrupted step re-runs
  as a fresh attempt and the task is re-queued (§12.4).
```

**Amended 2026-08-14 (task 003): `running → queued` has a second producer.** It
used to mean only "interrupted — a crash or a shutdown cut the step short". It now
also means "the agent reported that its usage quota for this window is spent"
(§7.2). Both consume no retry and both release the concurrency slot; they differ
in that the second re-queues the task with an **admission hold** — `admit_not_before`
plus a `queued_reason` (§11, §14) — so the scheduler does not walk it straight
back into the same wall. A re-queued task may therefore be waiting on a clock
rather than on a slot, which is a distinction clients render (§15).

A hold describes exactly one queued period: **any** transition out of `queued`
clears it, so admission, parking and cancel all drop it. One consequence, accepted
rather than fixed: pausing a held task and resuming it re-admits it at once and it
re-discovers the wall. That costs one process spawn and buys the rule this section
already applies to every other pending flag — a human action means go.

**Amended 2026-08-24 (task 025): `blocked → queued` has a second producer.** It
used to mean only "the human decided — retry or skip". It now also means "the
human asked for an ad-hoc **repair**": a one-off agent run, prompted by the
operator, in the task's *existing* worktree and branch (§7.2, §13.2). The
repair is an ordinary admission in every mechanical respect — the scheduler
admits it, both §11 caps apply, `internal/scheduler` stays the only producer of
`queued → running` — and it runs exactly one agent, which is not a step of the
workflow. When that agent exits, whatever it exited with, the task returns to
`blocked` at the **same** `current_step` carrying the **same** `block_reason`,
and the human retries, repairs again, skips or cancels as before.

There is no `repairing` state. A repair is a human action, not a lifecycle
state: a state of its own would cost an FSM row, a board legend, slot rules and
a recovery path — what task 014 paid for `awaiting_children` — to buy one nicer
`cancel`, and `cancel` keeps its present meaning throughout a repair (it kills
the process and aborts the task) because `available_actions` cannot express
"this cancel means something else right now".

The request is persisted on the task (`pending_repair_json`, §14) and is drained
by the transition that returns the task to `blocked`, **not** by the insert of
the row it produced. That is load-bearing for §12.4: recovery finalizes a
running row as `interrupted` and re-queues the task, and the actor then walks
from `current_step`. A request already drained would make a crash mid-repair
silently become a plain *retry* of the blocked step — consuming its budget and
possibly unblocking the task without the operator asking. Leaving it set means
an interrupted repair re-runs as a repair. Every other way out of `blocked`
drops the request, because it describes exactly the block it was made about.

Repair is offered from `blocked` whatever the block reason — no filtering. A
task blocked before its worktree existed (`branch_exists`, `base_branch_missing`)
re-enters worktree preparation on the repair admission and re-blocks on the same
reason without spawning an agent, which is the right outcome reached by the code
that already handles it; a task blocked on `agent_unavailable` has its repair
fail with `agent_unavailable`, which is honest. Filtering `available_actions` by
block reason would put a second, reason-shaped policy beside this section's
state-shaped one for no behavioral gain.

**Amended 2026-08-25 (task 027): `done` and `aborted` are no longer dead ends
until `archive`.** A new human action, **`follow_up`**, is valid from those two
states — the pair `archive` is already scoped to, and the two where the task's
worktree, branch and commits still exist. It runs one more piece of work in that
worktree: an agent prompt, a shell command, or a named workflow from the
registry, chosen at the point of asking. Like a repair it is an ordinary
admission — the scheduler admits it, both §11 caps apply — and the run lands in
the task's own ledger with step runs, transcripts, events and token and cost
accounting.

**A follow-up decides nothing about the task's verdict.** `done` returns to
`done` and `aborted` returns to `aborted`, whatever the run did. Promoting a
successful follow-up on an aborted task to `done` was considered and rejected:
it makes a human's abort reversible by any command that exits 0, and it buys
nothing — an operator who wants the verdict changed already has the task where
they can archive it. Returning an aborted-origin task is the engine action
**`restore`** (`running → aborted`); a done-origin one uses the existing
`complete`. `restore` exists for this and nothing else, because `cancel` is a
human action and using it here would report a decision nobody made.

**A failed follow-up step blocks the task**, at the follow-up's own row index,
carrying that step's `block_reason`. The resolution set is the existing one, and
the request is what tells the four apart: `retry` re-admits and re-runs the
follow-up from its persisted cursor (the request survives the block *and* the
retry — the one place `blocked → queued` keeps it); `repair` runs an ad-hoc
agent against the follow-up's failure (§7.2); `skip` marks the request abandoned
and the next admission restores `done` or `aborted` without running anything;
`cancel` aborts and drains. `edit + retry` is refused with a 400: an override
rewrites a step **in the snapshot** (§5.3), and a follow-up is deliberately not
in the snapshot, so there is nothing there to rewrite.

**`done → aborted` is therefore reachable**, which it was not before. `cancel`
during a running follow-up kills the live process and aborts the task, because
that is what `cancel` always means and `available_actions` cannot express "this
one means something else right now" (the same reasoning that refused a
`repairing` state above). A client author reading the older table would not
expect that edge, which is why it is called out here.

A follow-up is offered from `done` and `aborted` whatever the origin — no
filtering, for the reason repair is unfiltered. An `aborted` task that never got
a worktree has one created on the follow-up admission, or re-blocks on the same
reason it would have. The one carve-out is a follow-up already abandoned with
`skip`: it restores the origin state *before* worktree preparation runs, because
a run with nothing left to do must not be able to block on creating a worktree it
will never use.

A follow-up is repeatable: a finished task can be followed up any number of
times before it is archived, and each is a **round** with its own rows (§5.4).

### States

| State | Meaning | Consumes a concurrency slot? |
|---|---|---|
| `queued` | Ready to run; waiting for scheduler admission | no |
| `running` | A step process is executing (or about to) | **yes** |
| `awaiting_gate` | Paused at a `manual` step, waiting for approval | no |
| `awaiting_input` | The running agent emitted a structured input request (§7.4); its live process is idle, waiting for the answer | **yes** |
| `awaiting_children` | A `fan_out` step's lanes are running as child tasks (§7.6, *added 2026-08-17, task 014*); the parent owns no process. Cancel is the only human action — approve/reject/skip would be meaningless, which is why this is not a reuse of `awaiting_gate` | no |
| `blocked` | A step failed and retries are exhausted; waiting for a human decision | no |
| `paused` | Engineer-requested soft pause (takes effect at the next step boundary) | no |
| `done` | All steps succeeded; worktree/branch retained for inspection | no |
| `aborted` | Engineer aborted, or rejected terminally; worktree/branch retained | no |
| `archived` | Terminal. Worktree removed; record kept for history. The branch is retained unless it carries no commits past its base, in which case `delete_empty_branch_on_archive` deletes it (§10, *amended 2026-08-16, task 008*) | no |

### Human actions

| Action | Valid from | Effect |
|---|---|---|
| `cancel` (abort) | queued, running, awaiting_input, awaiting_gate, awaiting_children, blocked, paused | Kills any running process (graceful term, then kill after 10 s; `taskkill /T /F` on Windows); → `aborted`. *Amended 2026-08-17 (task 014): from `awaiting_children` it cascades to every unsettled descendant, whose branches and worktrees survive.* |
| `pause` | queued, running | `running`: finishes the current step, then holds; → `paused`. The request is persisted, so it survives a daemon crash; every other human action clears it |
| `resume` | paused | → `queued` |
| `retry` | blocked | Re-runs the failed step (fresh attempt, retry counter reset); → `queued` |
| `edit + retry` | blocked | Overrides the step's prompt/command **in this task's snapshot only**, then retries; the override is recorded on the StepRun |
| `repair` | blocked | *Added 2026-08-24 (task 025).* Runs one ad-hoc agent, prompted by the operator, in the task's existing worktree and branch (§7.2, §8.6, §13.2); → `queued`, and back to `blocked` at the same step with the same reason when it exits. It decides nothing about the blocked step and does not consume its retry budget |
| `skip` | blocked, awaiting_gate | Marks the step `skipped`, advances to the next step; → `queued` |
| `answer` | awaiting_input | Delivers the answer to the pending input request into the live agent session (§7.4); → `running` (step clock resumes) |
| `approve` | awaiting_gate | Gate step → `approved`; advances; → `queued` |
| `reject` | awaiting_gate | Gate step → `rejected`; → `blocked` (from which: retry earlier via edit, skip, or abort) |
| `set priority` | queued, paused | Reorders scheduler admission |
| `archive` | done, aborted | Removes worktree (warns if dirty — uncommitted changes would be lost; requires `force` in that case); → `archived` |
| `follow_up` | done, aborted | *Added 2026-08-25 (task 027).* Runs one more piece of work — an agent prompt, a shell command or a registry workflow — in the task's existing worktree and branch (§7.2, §8.3, §8.6, §13.2); → `queued`, and back to the state it came from when the run ends. Repeatable; it decides nothing about the task's verdict and spends none of the workflow's retry budgets |

**Amended 2026-08-24 (issue #127): an action that loses a race re-applies itself
once, when the state it lost to still allows it.** Every action in this table is
applied as a compare-and-swap on the state the request read. If a concurrent
transition takes that swap first, the task is re-read and the action applied once
more — but only when this table allows the action from the state actually found.
Otherwise the conflict stands and the client gets its `409` with `details.state`
(§13.1). Once, never in a loop: a second conflict is returned as it stands.

The producer this exists for is scheduler admission (§11). `queued → running` is
bookkeeping, not intent, and `cancel` and `pause` — the two actions valid from
`queued` **and** from a slot-holding state — were legal before the admission and
legal after it, so the human lost a race they could neither see nor influence and
whose only remedy was to issue the identical request again. Races against another
human are unaffected: a winning human transition almost always lands somewhere the
loser's action is invalid, and a cancelled task cannot be cancelled again. A
deferred row stays deferred across the retry — a `pause` that re-reads `running`
holds at the next step boundary, it does not park a task whose process is live.

This **supersedes the PR C decision** of 2026-08-07 (`docs/history/v0-tasks.md`,
the frozen v0 ledger) that a cancel losing the race to admission returns `409` and
takes no internal retry. That decision was protecting a human's informed consent
before "kill a live process", but a `409` does not deliver it: `cancel` is one row
here with one effect whose process-killing half is conditional on state, and the
remedy the clients document is the same keypress, which performs exactly that kill.

Tasks are `queued` immediately upon creation (no draft state in v1).

## 7. Step execution semantics

Steps execute strictly in order. Executing step *i* means: render templates → run the
step body → evaluate success → on success advance to step *i+1* (or `done` if last) →
persist → repeat.

*Amended 2026-08-18 (task 015).* A step may carry an `if:` guard and a
workflow may carry `condition` steps (§7.7). Neither changes the order: a
guarded step is skipped in place and the cursor advances past it, and a
`condition` step advances the cursor to the end. The walk is still forward,
one index at a time, over a flat list.

*Amended 2026-08-17 (task 014).* Order is still strict **between** steps; a
`parallel` step is one step whose body runs several sub-steps at once. It
occupies one index, holds one scheduler slot, and the cursor advances past it
only when it succeeds, so nothing above changes. What the sub-steps do inside
it is described in §7.5.

### 7.1 Success criteria

A step **succeeds** iff:

1. **`agent` step:** the agent process exits 0 **and** its event stream produced a
   terminal result (not an error event); **and** if a `check` command is declared, the
   check exits 0.
2. **`command` step:** the command exits 0; **and** any declared `check` exits 0.
3. **`manual` step:** the engineer approves the gate.

**…and in every case its output was captured and persisted.** *Added 2026-08-24
(#139).* Success is a claim about the run *and* about the record of it. An
attempt whose stream vincent could not read to the end, or whose transcript
could not be written or closed, fails with `transcript_io_error` (§12.2, §18)
rather than being judged from its exit status alone. The original wording made
exit 0 sufficient, which let a step whose megabyte-long output was thrown away
land as `succeeded` with nothing a client could query saying the record was
incomplete. Note the limit this is *not*: an over-long line is captured in
bounded pieces, not failed — `transcript_max_bytes` (§12.3) remains the only
size-based failure.

Checks run in the worktree with the same environment as command steps (§8.5). Check
stdout/stderr are captured to the step transcript.

### 7.2 Failure and retry

- Each step has `max_retries` (default **1**, i.e. up to 2 attempts).
- On a retryable failure, the daemon re-runs the step. For `agent` steps, a structured
  failure block is appended to the rendered prompt (§8.4) so the agent can correct
  itself. For `command` steps the command simply re-runs.
- When retries are exhausted the task enters `blocked` and emits a `task.blocked`
  event. Nothing further happens until a human acts (§6).
- **Timeouts:** per-step `timeout` (defaults: agent 60 m, command 15 m, check 15 m;
  all configurable). A timed-out process is killed and the attempt counts as a failure
  subject to the retry policy.
- **Interruption** (daemon crash/stop while a step runs) is *not* a failure: the
  attempt is marked `interrupted`, does **not** consume a retry, and the step is
  automatically re-run when the daemon restarts (§12.4).
- **Usage limits are not failures either.** *Added 2026-08-14 (task 003).* When an
  agent adapter recognizes that its CLI stopped because the account's usage quota
  for the current window is spent, the attempt is recorded `interrupted` with
  reason `usage_limit`, consumes **no** retry, and the task returns to `queued`
  carrying an admission hold (§11) until the window is plausibly back. The retry
  budget bounds genuine failure; a quota wall is not one, and with no delay
  between attempts a walled step would otherwise spend its whole budget in
  seconds. `usage_limit` is therefore a `queued_reason`, never a `block_reason`.
  Recovery needs no human: the scheduler re-admits the task when the hold expires.
- **`retry_backoff` paces the retries.** *Added 2026-08-25 (task 028).* A step
  may carry `retry_backoff`, a duration, settable per step and in
  `defaults:`. Its default is **zero**, which is an immediate retry — every
  workflow written before this behaves byte-identically. A non-zero value is
  spent the way the `usage_limit` hold above is spent: instead of retrying in
  place, the task returns to `queued` carrying `admit_not_before = now +
  retry_backoff` and `queued_reason: retry_backoff`, releasing its concurrency
  slot (§11). Nothing sleeps — a sleeping actor holds its slot for the whole
  wait, and with `max_parallel_tasks` slots held that way nothing runs at all.

  What separates it from the bullet above is the attempt: it is recorded
  `failed` with whatever it actually failed with, and it **consumes a retry**.
  The budget still bounds the work; the backoff only decides *when* an attempt
  the budget already allows happens. A step out of budget blocks at once,
  however long its backoff. `retry_backoff` is therefore a `queued_reason` and
  never a `block_reason` — and never a step's `failure_reason` either.

  It applies to every failure that would be retried, with no per-reason policy:
  an agent that hits a transient upstream error and one that leaves a compile
  error both exit non-zero and both classify `nonzero_exit`, so exempting that
  reason would remove the knob's reach from the failure it exists for. A step
  that wants an immediate second shot at its own output writes
  `retry_backoff: 0`. `usage_limit` and `interrupted` are untouched — this
  section already says they are not failures — and `condition_error` is
  untouched because a guard never becomes an attempt.

  One attempt is exempt by construction and one by pin. `repair` (§6) runs
  with `max_retries: 0`, so it never reaches the retry branch. The
  `on_conflict: agent` merge resolver (§7.6) is pinned to zero: its attempts are
  the join's own, and a resolver that does not resolve leaves the conflict for a
  human, so there is no failure there for the engine to hold and re-admit.
  Pinned rather than left alone, because `defaults.retry_backoff` would
  otherwise reach it and spend half its budget on a wait nothing would honour.

  The delay is fixed, not exponential: a growth curve is per-task state the row
  would have to carry, which §12.3 rejected for `usage_limit_recheck_interval`
  and which is not reopened here. There is deliberately **no** `config.yaml`
  key, mirroring `max_retries`: retry policy is a workflow's business, and
  `config.Defaults` is timeouts. And the wait is a *minimum* — re-admission
  competes for slots under the §11 caps and is noticed within the scheduler's
  5 s tick, so the observed wait is `retry_backoff` plus queueing.
- **`allow_failure: true` advances instead of blocking.** *Added 2026-08-18
  (task 015).* On an `agent` or `command` step, the failures **the step itself
  produced** — `nonzero_exit`, `check_failed`, `agent_error`, `timeout`,
  `transcript_limit` — advance the cursor once the retry budget is spent,
  rather than blocking the task. The row keeps its `failed` state and its
  reason: the failure happened, it just did not stop the workflow, and that row
  is what a later guard reads through `.Steps` (§8.4). It is what gives a
  condition something a run *discovered* to branch on.

  Everything else is vincent failing to **run** the step —
  `agent_unavailable`, `agent_unauthenticated`, `restricted_unsupported`,
  `input_unsupported`, `platform_unsupported`, `invalid_snapshot`,
  `template_error`, `condition_error`, and (*added 2026-08-24, #139*)
  `transcript_io_error` and `agent_protocol_error` — and is never swallowed: a
  workflow must not be able to branch on "the CLI is not installed" as though
  that were a test result. The two added reasons are vincent failing to
  **record** the step rather than to run it, which is the same rule read once
  more: "the disk filled up" is not a test result either, and a guard that
  reads a row whose evidence is missing is reading nothing. Both run this
  section's budget in full — a new attempt writes a new transcript, which is
  exactly what clears a transient one. `usage_limit` and `interrupted` are
  untouched for a different reason: this section already says they are not
  failures.

  It is orthogonal to the retry budget, which runs first and in full. A probe
  that should not retry says `max_retries: 0`. There is no
  `defaults: allow_failure:` — a workflow-wide "failures do not block" turns
  this section off in one line at the top of a file.
- **`condition_error` is the one reason that does not run this budget.**
  *Added 2026-08-18 (task 015).* A guard is evaluated before the step becomes
  an attempt, so there is no attempt to retry (§7.7).
- **`agent_unauthenticated` stays under the normal budget.** *Added 2026-08-14
  (task 003).* A CLI that refuses because it is not logged in fails the attempt
  like any other, retries as usual, and blocks when the budget is spent. Waiting
  cannot fix it, and short-circuiting the budget would make it the only reason in
  vincent that bypasses this section — to save one process spawn at the default
  `max_retries: 1`. Its value is that the reason names the fix.
- **An ad-hoc repair does not consume the blocked step's budget.** *Added
  2026-08-24 (task 025).* From `blocked`, `retry` re-runs an unchanged step,
  `edit + retry` can rewrite only that step's own prompt or command, and `skip`
  advances past an unsatisfied check — none of them can change the worktree. A
  `repair` (§6) runs one operator-prompted agent that can, in the task's
  existing worktree and branch. Its row sits at the blocked step's index under
  the reserved step id `__repair` (§5.4), and attempts are counted per
  `(task_id, step_index, step_id, iteration)` — so the blocked step's budget is
  untouched by construction, not by a special case. After any number of repairs
  a `retry` gets exactly the attempts it would have got with none.

  The repair itself carries `max_retries: 0`: a failed repair fails fast rather
  than silently paying for a second agent run, which is the built-in `adhoc`
  workflow's reasoning applied to the same shape of one-off run.

  A repair decides **nothing** about the blocked step. Whatever the agent exits
  with, the task returns to `blocked` at the same step with the same reason and
  a human chooses. Auto-retrying on a repair's success was considered and
  rejected: the operator would never see the repair's diff before the step
  re-ran, and a repair agent's exit code is not the right thing to authorize
  more agent spend with — this section's posture is that a human decides what a
  machine could not.

  This does not reopen task 018's declined `on_failure:` / try-catch. That
  decision is about what a workflow author can declare ahead of time; the whole
  point of a repair is that nothing was declared ahead of time. It adds a human
  action, not a workflow field.
- **A follow-up run spends none of the workflow's budgets, and blocks at its
  own index.** *Added 2026-08-25 (task 027).* A `follow_up` (§6) on a finished
  task writes its rows past the snapshot's last step index (§5.4), so the same
  `(task_id, step_index, step_id, iteration)` count that keeps a repair out of a
  step's budget keeps a follow-up out of every step's. After any number of
  follow-ups, the original steps count exactly the attempts they would have
  counted with none.

  A follow-up step that fails with its budget spent blocks the task **at the
  follow-up's row index**, carrying that step's reason. The resolution set is
  this section's existing one: `retry` re-runs the follow-up from its own
  cursor, `repair` runs an ad-hoc agent against the follow-up's failure —
  reading that row as its failure context rather than the snapshot's — `skip`
  abandons the follow-up and restores the task's origin state, and `cancel`
  aborts. `edit + retry` is refused (§6): the follow-up is not in the snapshot
  an override rewrites.

  The agent and command forms carry `max_retries: 0`, the repair's reasoning
  applied to the same shape of one-off run. A workflow-form follow-up uses the
  budgets its own steps declare, because those are steps somebody wrote.

### 7.3 Fresh session per step

Every `agent` step is an independent agent session — no conversation is resumed
between steps or attempts. Durable state flows between steps through:

- the **worktree** (files, commits made by earlier steps), and
- **prior step results** exposed to templates as `{{.Steps}}` (§8.4).

This keeps steps individually re-runnable, keeps context windows small, and avoids
coupling to any one agent's session semantics.

### 7.4 Interactive input requests

While an `agent` step runs, an input-capable adapter (§9.1, §9.5) may surface a
structured **input request** from the agent:

- **`question`** — the agent asks the user something (e.g. Claude Code's
  AskUserQuestion tool): one or more questions, each optionally with predefined
  options and multi-select; free-text answers are always accepted.
- **`permission`** — in `restricted` mode, the agent requests approval for a
  denied action (tool/command summary included).

Only machine-readable requests from the agent's event stream qualify; vincent never
infers "the agent seems to be asking something" from output text.

Behavior with `on_input: wait` (the default):

1. The task moves `running` → `awaiting_input`, **keeping its concurrency slot**
   (the agent process is alive mid-step, idle on its stdin — killing or re-queuing
   it would lose the very session the answer belongs to). The normalized request
   (§9.1 `InputRequest`; the adapter-native payload is preserved in `raw`) is
   stored on the task as `pending_input`; the durable `task.state_changed` event
   for the transition carries the request kind and a one-line summary in its
   payload (§13.3) — this is the alert clients key off.
2. The step `timeout` clock **pauses** — it measures agent work, not human
   latency. A separate `input_timeout` (global default 24h, §12.3; overridable in
   workflow `defaults` and per step) bounds each wait, measured **per request** —
   a new request starts a fresh window: on expiry the process is
   killed and the attempt fails with reason `input_timeout` (normal retry/blocked
   policy, §7.2), freeing the slot.
3. The engineer answers via `POST /v1/tasks/{id}/answer` or the TUI answer form
   (§15). The adapter translates the answer back to its native protocol and
   writes it to the live process; the task returns to `running` and the step
   clock resumes. **At most one `pending_input` exists per task**, because a
   task has one `awaiting_input` state.

   That is enforced by the *adapter*, not assumed of the CLI. The original
   wording said requests were serial because an agent blocks on its pending
   request; real claude disproves it — it batches parallel tool calls, so a
   restricted run can raise several `can_use_tool` requests at once. Treating
   the second as a protocol violation failed live runs (Windows testing,
   2026-08-11). An adapter that receives a concurrent request now **queues**
   it, transcripts it verbatim, and surfaces it when the current one is
   answered — the CLI is blocked on both, so nothing else would ever carry
   it. Clients and the engine are unchanged: they still see one at a time.

`on_input: deny` (workflow `defaults` or per step) keeps runs strictly unattended:
vincent immediately auto-responds through the adapter's `Respond()` — questions
get a canned "no user is available; decide with your best judgment" answer,
permission requests are denied — and the task never leaves `running`.

`on_input: require` (workflow `defaults` or per step, added 2026-08-17, task
013) is `wait` plus a **precondition**: the step will only run on an adapter
that can stop and take a human answer. It is for a workflow whose point is the
conversation — an agent that cannot ask does not degrade there, it guesses.

The requirement is enforced at three layers, and only ever on a *positive*
"this adapter cannot":

- **Load (§8.2).** A requiring step resolving to an adapter that can never take
  input — codex, cursor: no control channel exists in any version — is a
  validation error, attributed to the `agent` field that supplied the value.
  claude is not judged here: its support is a version question (§9.3) that only
  a probe answers, and validation never spawns a process.
- **Task creation (§13.2).** `POST /v1/tasks` resolves every requiring step
  against the task's agent override (§8.6) and refuses a selection the daemon
  knows cannot ask. An absent binary or a probe that did not answer is
  *unknown*, and unknown never refuses — §9.6's degrade-never-block rule
  outranks this gate. `GET /v1/workflows` reports `requires_input` for a
  workflow whose requiring steps leave their agent to the task, and
  `GET /v1/agents` reports each adapter's `input_verdict`
  (`supported` | `unsupported` | `unknown`), so a client marks its picker
  without re-deriving the asymmetry.
- **Run (§7.2).** The engine re-checks before spawning, and fails the attempt
  with `input_unsupported` when the answer is now no — the task and its daemon
  having parted company is the only way to get here.

A step's own `on_input` wins over `defaults:` as every other field does, so
`defaults: {on_input: require}` with one step's `on_input: deny` leaves that
step deliberately unattended. `require` changes nothing else: `input_timeout`
keeps its meaning and its three levels, and once the step is running `require`
and `wait` are the same thing.

Adapters without mid-run input support (`supports_input: false`, §9.5 — codex in
v1) never produce input requests; their steps behave exactly as today. Requests
and answers are appended to the step transcript as namespaced `vincent.*` lines;
time spent waiting is recorded per StepRun (`input_wait_ms`) and excluded from
duration metrics (§17). Crash recovery treats `awaiting_input` like `running`: on
restart the attempt is `interrupted` and re-runs as a fresh session (§12.4) — the
pending request is discarded and the fresh run may re-ask.

Full-auto note: in `full-auto`, permission prompts are bypassed at the CLI level,
so `permission` requests should not occur; `question` requests can occur in any
permission mode.

### 7.5 Parallel step groups

*Added 2026-08-17 (task 014).* A `parallel` step runs its sub-steps
concurrently in the task's **one** worktree. It creates no branch, no child
task and nothing to merge — that is `fan_out` (§7.6), a different mechanism
that happens to share the word.

```yaml
- id: verify
  type: parallel
  max_parallel: 4
  steps:
    - { id: test,  type: command, run: go test ./... }
    - { id: lint,  type: command, run: golangci-lint run }
```

- **Sub-steps are ordinary steps.** `agent` and `command` only, each with its
  own `check`, `timeout`, `max_retries` and agent selection, resolved exactly
  as they would be at the top level. `manual` is rejected: a gate ends the
  actor goroutine and releases the slot (§6), and no state means "one sub-step
  is gated". `on_input: require` is rejected for the same reason —
  `awaiting_input` holds one pending request for the whole task (§7.4).
  Groups do not nest. *Amended 2026-08-18 (task 015):* `condition` steps are
  rejected here too — a group is a set, with no sequence to end — and a
  sub-step may carry an `if:`, which subsets the group. Sub-step guards are
  evaluated **once, before anything in the group starts**, so no sibling has
  run and none can be read by another's guard; a group whose sub-steps are all
  guarded off succeeds having run nothing. *Amended 2026-08-19 (task 018):*
  the blindness is a property of the *set*, not of that ordering, and holds in
  every admission. A group re-admitted after one sub-step failed skips the
  ones that already succeeded, and their rows are still on disk — so a
  sub-step's context omits **every** row sharing the group's `step_index` that
  is not its own, whatever state that row ended in. Without it the same guard
  against the same context would answer one way on the first run and another
  after a human pressed `retry`.
- **Rows.** One `step_runs` row per sub-step, all sharing the group's
  `step_index` and told apart by `step_id`. The group has no row of its own;
  its outcome is derived. Attempt numbers and retry budgets are per sub-step,
  and a sub-step's transcript is
  `{step_index}-{step_id}-{attempt}.jsonl` (§12.2).
- **Success** is every sub-step succeeding. A failure does **not** cancel its
  siblings: the group waits for everything it started, then blocks with the
  first failure in declaration order, so the same failures always produce the
  same `block_reason`. A group-level `timeout:` bounds the whole group and
  fails it with `timeout`.
- **Retry** re-runs only what did not succeed, within an admission and across
  one: a re-admitted group skips sub-steps whose latest attempt succeeded,
  derived from the rows rather than from a stored cursor.
- **Concurrency.** `max_parallel` (default `parallel.max_parallel`, 4) bounds
  how many run at once. It is a **second concurrency dimension**: the §11 caps
  count tasks in slot-holding states, and a group runs inside one such task,
  so one task can keep `max_parallel` processes busy while the board reads a
  single running task. See §11.
- **Concurrent writes are undefined.** The sub-steps share one working tree.
  §10 isolates working trees between tasks, not processes within one; a group
  whose sub-steps write the same files is a workflow bug, not something the
  daemon arbitrates.

### 7.6 Fan-out steps

*Added 2026-08-17 (task 014).* A `fan_out` step turns each of its lanes into
a **real child task** — its own row, worktree, branch, scheduler slot, gates,
blocks, transcripts and recovery — and merges their branches back into the
branch this task already owns. One branch is still delivered, because the step
does not finish until every lane is merged.

```yaml
- id: build
  type: fan_out
  merge:
    on_conflict: block        # block (default) | agent
  lanes:
    - { id: api,  workflow: implement-module, fields: { module: api } }
    - { id: docs, steps: [ { id: write, type: agent, prompt: "Document the API." } ] }
```

- **A lane is a named workflow or inline steps**, exactly one. A named lane is
  resolved through the usual builtin < global < project shadowing at **task
  creation** and written into the task's snapshot — never read from the
  registry again (§5.3). A lane's workflow may itself fan out, to any depth.
- **A lane may carry an `if:`.** *Added 2026-08-18 (task 015).* It is
  evaluated when the `fan_out` step runs, in the parent's context, so a lane
  can depend on what an earlier step found. A guarded-off lane is not spawned;
  its siblings still run and the join still merges them, in **declared** lane
  order — the absent lane's index is not reused, so a re-run merges identically.
  A step whose lanes are *all* guarded off is a no-op success: it records a row
  saying so and advances, and specifically does **not** park, because a parent
  in `awaiting_children` with no children would be re-queued, spawn nothing and
  park again.

  A conditional lane makes the tree's shape non-static, so the creation-time
  checks below count **every** lane, guarded ones included. A tree that could
  never spawn `max_tasks` descendants may still be refused. That
  over-approximation is stated here rather than left to be discovered.
- **Creation-time checks**, possible because the whole tree's shape is static
  once lane lists are in the snapshot: a cycle is a `400` naming the path, and
  so is a tree past `fan_out.max_depth` (3) or `fan_out.max_tasks` (64,
  counting descendants). A depth explosion is refused in front of the person
  typing rather than discovered as two hundred worktrees six hours later.
- **Inheritance.** A child's base branch is the parent's branch; its
  `agent`/`model`/`effort` overrides and its priority propagate, and its
  fields merge with the parent's, the lane winning. A lane spec overrides any
  of them for its own subtree. Priority inheritance is load-bearing: admission
  is `priority DESC, created_at ASC` and descendants are created late, so
  priority-0 children of a priority-5 root would queue behind unrelated work.
- **The spawn is one transaction.** *Added 2026-08-19 (task 018).* Every lane
  of a step is inserted together, so a failure part-way leaves **no** lane
  behind and the step blocks having spawned nothing; `retry` re-spawns from a
  clean slate. Creating them one at a time made a partial spawn reachable, and
  it had no honest recovery: whatever is cleaned up afterwards, a lane that
  committed stays attached to the step, so the parent's next admission reads
  it as its lanes and takes the join path below — blocking `lane_failed`
  forever on work that never ran. Deleting the committed rows was the
  alternative, and it would have made a hard task delete the first thing in
  vincent that destroys work.
- **Parking.** After spawning, the parent moves to `awaiting_children`, which
  holds **no** slot (§6, §11). That is what makes fan-out deadlock-free at any
  depth: a parent releases its slot *before* its children need one, so there
  is no hold-and-wait anywhere in the chain under any cap. A fan-out is not a
  way to exceed the caps — it is a way to fill them.
- **Resuming.** The scheduler returns the parent to the queue once every
  descendant has *settled* (`done` or `aborted`). *Amended 2026-08-19
  (task 018):* the step decides "spawn or join" on whether its lanes have
  settled, not merely on whether they exist. A parent admitted at the step
  with unsettled lanes **parks again** rather than joining. That state is not
  produced on purpose — it is a park transition that lost its compare-and-swap
  or failed to commit, after which §12.4 re-queues a `running` parent whose
  lanes are still `queued` — and joining there would read every lane as "not
  done" and block `lane_failed` on work about to run perfectly well, which
  `retry` cannot clear because the lanes are still not done. A `blocked`, `awaiting_gate`
  or `paused` lane holds the join open until a human resolves it; the §13.2
  `children` rollup is what makes that visible.
- **The join** merges each lane branch with `git merge --no-ff` in **declared**
  lane order, message `Merge lane '{lane_id}' of task {child_id}`, stopping at
  the first conflict. Declared rather than completion order is what makes a
  re-run conflict identically. Git identity is the user's own: vincent runs as
  the invoking user (§16) and invents no author.
- **A conflict blocks** with `merge_conflict`, leaving the worktree conflicted
  so a human resolves in place. `on_conflict: agent` opts into an agent
  attempt first — a full agent step, gated by its own `check` — falling back
  to the block. Blocking by default is §7.2's posture: a human decides what a
  machine could not.
- **A lane that settles without finishing** blocks the join with
  `lane_failed`, and **nothing** is merged. *Clarified 2026-08-18 (task 015):*
  "without finishing" means `blocked` or `aborted`. A lane whose own workflow
  stopped early at a `condition` step (§7.7) settles `done`, and `done` is
  `done` — it merges normally. Lanes doing different amounts of work is the
  point of guarding them. A partial merge is
  indistinguishable downstream from a complete one. `retry` re-checks the
  lanes; the remedy is to fix the child, which is an ordinary task. `skip`
  keeps its meaning — it skips the whole join — and is deliberately not a
  "proceed without that lane" button.
- **Re-entry** into a half-merged join is disambiguated by the previous
  attempt's outcome, with no merge cursor persisted: which lanes are already
  merged is a fact git holds, and an already-merged lane re-merges as a no-op.
  A crash aborts the in-progress merge and re-merges from the top; a human
  retry after `merge_conflict` commits their resolution and continues. Only
  the crash may abort — see §12.4.
- **Cancel cascades** to every unsettled descendant, keeping their branches
  and worktrees: the work is stopped, not destroyed. **Archive refuses** while
  any descendant is unfinished, then cascades, each child under §10's ordinary
  dirty-worktree rules.
- **Cost.** N lanes leave N worktrees on disk until someone archives them.
  That is what `vincent gc` and `vincent doctor` are for, and it will be felt.

### 7.7 Conditions between steps

*Added 2026-08-18 (task 015).* A workflow decides at run time what to do next.
Three fields do it, and they are deliberately not one:

```yaml
steps:
  - id: probe
    type: command
    run: git diff --quiet HEAD~1
    allow_failure: true              # a nonzero exit is data, not a block

  - id: nothing-to-do
    type: condition                  # false ends the run; the task is `done`
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'

  - id: changelog
    type: agent
    if: '{{ eq (index .Task.Fields "changelog") "yes" }}'   # skip, then carry on
    prompt: Update CHANGELOG.md.
```

**`if:` on a step is a guard.** It renders against the §8.4 context — the same
context, the same `missingkey=error`, the same parse-at-load check as `prompt`,
`run` and `check` — and must produce, after trimming, exactly `true` or
`false`. Loose truthiness was rejected: a guard reading a field that is not
there renders the empty string or `<no value>`, and a permissive rule would
accept either as a decision. A guard that renders anything else fails the step
with `condition_error`.

**A false guard skips the step and the workflow carries on.** The step records
a `step_runs` row in state `skipped` with `skip_reason: condition` — the same
state the human `skip` action writes (§6), told apart by that column — and the
row stays visible in `.Steps` (§8.4) so a later guard can see that the step did
not run. This is the answer §8.1.1 deferred for per-step `platforms:`.

**A false guard on a *set* subsets it instead.** On a `fan_out` lane (§7.6) and
on a `parallel` sub-step (§7.5), "skip and carry on" is subsetting: the other
members still run, the group still succeeds, the join still happens. One word,
one meaning — "this member does not run" — whose consequence follows from
whether it is attached to a sequence or a set.

**`type: condition` ends the sequence.** It carries `id`, `name` and a required
`if:`, and nothing else — no `run`, no `timeout`, no `max_retries`, no
`allow_failure` — because it starts no process: it cannot time out, cannot be
interrupted, has nothing to retry and writes no transcript. Its `if:` is its
condition rather than a skip-guard on itself.

- **True** continues: the step records `succeeded` and the cursor advances.
- **False** stops: the step records **`stopped`**, the cursor advances to the
  end of the step list, and the task is `done`. The steps after it record
  nothing, because they were never considered.

There is no `on_false:` policy. "Stop and block for a human" already exists —
it is a `command` step that exits nonzero (§7.1, §7.2) — and the gap this type
fills is *stop and succeed*.

The shell phrasing of an early finish composes rather than being built in:

```yaml
- { id: probe, type: command, run: git diff --quiet, allow_failure: true }
- { id: gate,  type: condition, if: '{{ ne (index .Steps "probe").ExitCode 0 }}' }
```

A `condition` step is valid at the top level and in a lane's own workflow. It
is **rejected inside a `parallel` group**, joining `manual` and
`on_input: require` on §7.5's list: a group is a set, so "end the sequence" has
nothing there to name.

**Guards are re-evaluated every time, never sticky.** Every attempt, every
human `retry`, every re-run after §12.4 recovery asks the question again; no
verdict is persisted. A human who retries a blocked step whose guard is now
false will see it skipped, and that is correct — if the guard is false now,
running the step now would be wrong. The alternative is a decision cache
recovery would have to reason about, holding a verdict computed against facts
that have since changed.

**A guard error blocks without consuming the retry budget** — the one failure
in §18's vocabulary that does not run §7.2's budget. A guard is evaluated
*before* the step becomes an attempt, so there is no attempt to retry, and
re-rendering an unchanged template against an unchanged context cannot answer
differently. One `failed` row is recorded for the step carrying
`condition_error`, so the block names where and why. The second try that can
succeed is the human's, after they fix the workflow.

**`.Host`** (§8.4) is what a guard reads to gate on the platform:
`if: '{{ ne .Host.OS "windows" }}'` is the per-step `platforms:` §8.1.1
deferred, with no new schema. The whole-workflow `platforms:` stays as it is —
it gates *offering* a workflow, which a run-time guard cannot do.

### 7.8 Loops

*Added 2026-08-18 (task 016).* A `loop` step runs its body repeatedly in the
task's **one** worktree. It creates no branch, no child task and nothing to
merge — that is `fan_out` (§7.6). Where a `parallel` group (§7.5) is a set run
once, a loop is a **sequence** run more than once.

```yaml
- id: green
  type: loop
  count: 5
  steps:
    - { id: suite,  type: command, run: go test ./..., allow_failure: true, max_retries: 0 }
    - { id: passed, type: break,   if: '{{ eq (index .Steps "suite").ExitCode 0 }}' }
    - { id: repair, type: agent,   prompt: "The suite is red: {{ (index .Steps \"suite\").Result }}" }
```

- **Exactly one driver.** `count:` (a positive integer, at most
  `loop.max_iterations`) or `for_each:` (a YAML sequence of templates, or a
  scalar template). Every `for_each` entry is rendered, trimmed and split on
  newlines with empty lines dropped, so a hand-written list and a command's
  multi-line output are one mechanism. There is no `while:`; the converge loop
  is `count:` plus `break`, which puts the condition in the body where it can
  see the body.

  A list drawn from `.Steps[…].Result` is bounded by that field's **200-line
  tail** (§8.4): a producer printing more paths than that loses the earliest
  ones silently. In practice `max_iterations` bites an order of magnitude
  sooner and blocks loudly, but a producer meant to feed a loop should filter
  at the source rather than rely on either.
- **The body is `agent`, `command`, `condition` and `break`.** `manual`,
  `on_input: require`, `parallel`, `fan_out` and a nested `loop` are rejected
  at load, each for the reason §7.5 rejects it: anything that ends the actor
  goroutine mid-body is state a derived loop position cannot express.
- **Rows.** One `step_runs` row per body step per iteration, all sharing the
  loop's `step_index`, told apart by `step_id` and a 1-based `iteration`; a
  `for_each` row also carries its `loop_item`. The loop has no row of its own;
  its outcome is derived. A body step's transcript is
  `{step_index}-i{iteration}-{step_id}-{attempt}.jsonl` (§12.2).
- **`.Loop`** (§8.4) is `Index`, `Item`, `IsFirst`, `IsLast`, with `Index: 0`
  outside any loop.
- **Ending.** The driver being exhausted, or a `break` whose guard is true,
  ends the loop **successfully** and the cursor advances. A `condition` whose
  guard is false inside a body ends **that iteration**; the loop continues. A
  loop that cannot run within `max_iterations` **blocks** with `loop_limit` —
  running out of tries is not a decision, and `condition` (§7.7) is what a
  workflow uses to stop and succeed. A `for_each` list longer than
  `max_iterations` blocks before the first iteration, naming the count. An
  empty list, or a whole loop guarded off by its `if:`, succeeds having run
  nothing. *Amended 2026-08-19 (task 018):* an empty list records **one** row
  under the loop's own id — `succeeded`, `iteration: 0`, with a summary saying
  the list was empty. "The loop has no row of its own" is about its
  *iterations*: those are the body's rows, and with none of those the step index
  a task passed through would carry no row at all, breaking the phase 2
  invariant that every one has at least one and leaving a detail view unable to
  tell "ran nothing" from "never reached". A `fan_out` that selects no lane has
  recorded exactly this row since task 015. The row is invisible to the loop's
  own derivation, which filters on `iteration > 0`, and it is **not** a
  `.Steps` entry (§8.4): a loop's id is never one, or it would be a key present
  exactly when the loop did nothing and absent when it did something.
- **Failure.** A body step that exhausts its retry budget fails the iteration
  and blocks the task with that step's own reason. `allow_failure:` (§7.2) is
  how a probe's red result becomes data a `break` can read. Retries are for a
  step that failed; iterations are for a body that succeeded and must run
  again — each body step spends its own `max_retries` **within** an iteration.
- **Resuming.** Position is derived from the rows, never persisted: a
  re-admitted loop skips body steps whose latest attempt succeeded and
  continues mid-iteration. Iterations that already have rows take their item
  from those rows; only new iterations draw from a re-derived `for_each` list.
  *Amended 2026-08-19 (task 018):* the loop's **extent** likewise never falls
  below the iterations it has rows for. A re-derived list shorter than those
  rows would otherwise leave the loop reporting success over iterations it
  started and never revisited, so the extent is the longer of the two and the
  `max_iterations` ceiling is re-checked against it. Every `for_each` source
  §8.4 offers is stable between admissions, so this bounds the derivation
  rather than a reachable failure; a `for_each` whose source is *not* stable
  across admissions is a workflow bug, and the one silent way it could fail is
  the one closed here.
- **Human actions** (§6). `skip` skips the **whole loop step** and advances
  past it; there is no "skip this iteration". `retry` resumes at the failed
  body step of the current iteration with a fresh budget. `edit + retry`
  rewrites that body step in the task's snapshot and therefore applies to
  **every remaining iteration**, which is the useful behaviour: fix the
  prompt, let it keep going.
- **Concurrency.** A loop is one step, one slot, one worktree, and its
  iterations are strictly sequential. §11's caps see one running task, exactly
  as they always did. `max_parallel` has no meaning on a loop.

**`type: break` ends the loop.** It carries `id`, `name` and a required `if:`,
and nothing else — the same fields, for the same reason, as `condition`
(§7.7): it starts no process, so it cannot time out, be interrupted, be
retried or write a transcript. A true guard ends the loop and the cursor
advances past it; the loop **succeeds**. It is rejected outside a loop body,
symmetric with `condition` being rejected inside a `parallel` group.

There is no `continue` type. A `condition` inside a loop body keeps the
meaning §7.7 gave it — "end the sequence" — and the enclosing structure
supplies the consequence; a loop body *is* a sequence, so ending it ends that
iteration. One word, one meaning, whose consequence follows from what it is
attached to.

**`.Steps` visibility is positional.** A failed row is visible to a template
only once the run has passed it, and "passed it" is compared on
`(step_index, iteration, body position)`. Outside a loop that is the step
index alone, as it always was. Inside one it is what lets a `break` read the
`allow_failure` probe two lines above it in its own body. A `parallel`
sub-step has no body position and therefore never precedes a sibling, so
§7.5's set-invisibility is unchanged. A step's own failed attempt still stays
out of `.Steps["itself"]` mid-retry, because `.LastFailure` is that channel.

### 7.9 Included workflows

*Added 2026-08-19 (task 019).*

A `type: include` step names another registry workflow, and is replaced by
that workflow's steps when the task is **created**:

```yaml
# .vincent/workflows/go-checks.yaml
name: go-checks
defaults: { max_retries: 0 }
steps:
  - { id: lint, type: command, run: go run mage.go lint }
  - { id: test, type: command, run: go run mage.go test }

# .vincent/workflows/feature.yaml
name: feature
steps:
  - { id: implement, type: agent, prompt: "{{ .Task.Description }}" }
  - { id: checks, type: include, workflow: go-checks }
  - { id: review, type: agent, prompt: "lint said: {{ .Steps.lint.Result }}" }
```

The created task's snapshot holds four steps — `implement`, `lint`, `test`,
`review` — each with its own `step_index`. **No include survives into the
run.** There is no `step_runs` row for one, no cursor, no boundary, and
nothing in §7's engine, §11's scheduler or §12.4's recovery knows the word:
they see the flat step list they already saw.

That is what separates an include from `parallel` (§7.5) and `loop` (§7.8),
which own a `step_index` and run a body under it, and from `fan_out` (§7.6),
which creates tasks. An include creates nothing. It is an authoring-time
construct that has been resolved away before anything runs.

**Resolved once, at creation.** §5.3 says execution uses the snapshot
precisely so that later edits to a workflow file cannot mutate an in-flight
task, and a callee read from the registry six hours into a run would be
exactly that mutation. It is also what makes the whole expanded shape
checkable in the insert path, so every failure below is a 400 in front of the
person creating the task.

**An include may appear anywhere a step may**: at the top level, inside a
`parallel` group, inside a `loop` body, and inside a `fan_out` lane's inline
`steps:`. This is the point of splicing rather than nesting — a callee may
itself contain a `loop`, a `parallel` or a `fan_out`, because those land at
the caller's own level rather than one level inside something. The nesting
rules of §8.2 are therefore checked **after** expansion: a fragment containing
a `loop`, included into a loop body, is refused at creation with the same
message a hand-written nested loop gets.

**Step ids are shared, and a collision is refused.** Ids are unique across the
whole expansion, so a callee bringing an id the caller already uses is a 400
naming both workflows. A given callee can therefore appear at most once in one
expansion. Ids are *not* rewritten or prefixed: a callee's own templates read
`.Steps.<id>`, and renaming its steps would mean rewriting them.

**A callee's `defaults:` travel with its steps.** At creation each spliced step
is given the callee's defaults for any field it does not set itself, so a
fragment keeps the behaviour it was written with rather than adopting its
caller's. The resolution order is §8.6's, with the callee inserted below the
task: **step field → task override → callee `defaults:` → caller `defaults:` →
daemon default**, innermost callee first when includes nest. Because task-level
overrides are immutable (§13.2 — `priority` is the only mutable task field),
this is decided at creation and written into the snapshot; a value no level
supplies is left unset, so the caller's defaults still apply at run time.

**A `condition` inside a callee ends the whole task's sequence.** There is no
include boundary at run time for it to end instead. A fragment ending in a
`condition` therefore stops the caller too, and the task is `done` — which is
§7.7's meaning applied to a step list that no longer records where it came
from.

**`break` cannot be factored out.** It is valid only inside a loop body
(§7.8), so a workflow whose top-level steps contain one does not load and can
never be a callee.

**Provenance.** Every spliced step records `resolved_from:` — the chain of
workflow names it came through, outermost first — written by the resolver and
never by hand. It is what the TUI attributes a step to; it has no effect on
execution.

**Refused at creation** (each a 400, and each a warning at registry load,
because which files a name reaches is decided by builtin < global < project
shadowing and only a task picks a root):

| Refusal | Message names |
|---|---|
| A cycle: A includes B includes A | the path, `a → b → a` |
| A name this project cannot resolve | the missing workflow |
| More than `include.max_depth` levels (§12.3) | the depth and the bound |
| A step id the expansion already used | both workflows |
| A callee whose `platforms:` (§8.1.1) excludes this host | the callee and the host |

The caller's own `platforms:` is **not** rewritten from its callees': it stays
a property of the file as written, so a workflow's declared restriction means
one thing. The consequence is that `vincent workflow validate` cannot tell you
a caller includes a fragment this host cannot run — the same trade §8.1.1
already makes by checking the list for shape rather than against the
validating host.

## 8. Workflow definition (YAML)

### 8.1 File format

```yaml
# .vincent/workflows/feature-pr.yaml  (project scope)
# or  {config_dir}/workflows/feature-pr.yaml  (global scope)

name: feature-pr                      # required; unique per scope; project shadows global
description: Implement, test, review, then push and open a PR.
platforms: [posix]                    # optional; where this workflow may run (§8.1.1)

fields:                               # optional; ordered task-input contract (§8.1.2)
  - name: ticket
    label: Ticket
    description: Issue tracker key.
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: dry-run
    label: Dry run
    type: boolean

defaults:                             # optional; per-step values override
  agent: claude                       # claude | codex | cursor (§9.7)
  model: ""                           # adapter-native id/alias (e.g. sonnet); options via GET /v1/agents (§9.6)
  effort: ""                          # adapter-native effort (claude: low…max; codex: minimal…high) (§8.6)
  permission_mode: full-auto          # full-auto | restricted   (§9.4)
  on_input: wait                      # wait | deny | require — agent input requests (§7.4)
  input_timeout: 24h                  # max wait in awaiting_input (§7.4)
  max_retries: 1
  retry_backoff: 0s                   # wait between attempts (§7.2); 0 retries at once
  timeout: 60m

steps:
  - id: implement                     # required; slug, unique within the workflow
    name: Implement the change        # optional display name (defaults to id)
    type: agent
    prompt: |                         # required for agent steps; Go text/template
      You are working in a git worktree of {{.Project.Name}}
      on branch {{.Task.BranchName}} (based on {{.Task.BaseBranch}}).

      Implement the following task. Commit your work with clear messages.

      # {{.Task.Title}}
      {{.Task.Description}}

      {{ with index .Task.Fields "ticket" }}Related ticket: {{ . }}{{ end }}
    check: go test ./...              # optional; must exit 0 in the worktree
    max_retries: 2
    timeout: 45m

  - id: self-review
    type: agent
    agent: codex                      # per-step agent override — defaults.model/effort
    effort: high                      # don't follow across the agent switch (§8.6)
    prompt: |
      Review the diff of this branch against {{.Task.BaseBranch}} for bugs and
      missed requirements. The implementation summary was:
      {{ (index .Steps "implement").Result }}
      Fix anything you find and commit the fixes.

  - id: gate-review
    type: manual
    instructions: |                   # rendered and shown in the TUI
      Inspect the diff for task #{{.Task.ID}} before it is pushed.

  - id: publish
    type: command
    run: git push -u origin {{.Task.BranchName}} && gh pr create --fill
    timeout: 5m
    max_retries: 0
```

#### 8.1.1 Platform restriction (`platforms:`)

*Added 2026-08-16 (task 010).* A workflow may declare the platforms it is
written for. §8.3 leaves cross-OS portability of command steps to the author;
this is how an author says they did not attempt it, instead of shipping a
workflow that pipes `cat` into `wc` and fails wherever it is offered on
Windows.

```yaml
platforms: [posix]           # or: [linux, darwin] · [windows] · [posix, windows]
```

- Tokens are GOOS values — `linux`, `darwin`, `windows` — plus one group
  token, `posix`, which matches **every non-Windows host**. Matching is exact:
  `macos` or `Linux` is a typo that fails validation, the way every other enum
  in the schema does.
- Omitted or empty means every platform. Nothing changes for a workflow that
  does not declare it, which is the majority.
- The restriction is judged against the **daemon's** host, because the daemon
  is what runs the steps. Clients do not re-derive it: `GET /v1/workflows`
  serves `platforms[]` and the daemon's own verdict as `platform_supported`
  (§13.2).
- A restricted workflow that does not match stays in the registry and is still
  listed, with its reason — the same rule that keeps an invalid file visible
  (§5.2). It is *offering* that stops: the TUI's new-task picker refuses it,
  and `POST /v1/tasks` rejects it with a 400 naming the restriction and the
  host.
- A task already holding such a snapshot — a data directory carried to another
  OS, or a workflow narrowed after the task was queued — is blocked at
  admission with `platform_unsupported` (§18), before any step runs. That is
  distinct from `invalid_snapshot`: the snapshot is valid, just not here.

The restriction is **whole-workflow**. A per-step `platforms:` was considered
and deferred: it needs an answer to "what does a skipped step do to `.Steps`
and to the task's success", which is a lifecycle question, not a schema one.

*Resolved 2026-08-18 (task 015).* §7.7 answers that question, and the answer
made the schema unnecessary: a per-step platform gate is
`if: '{{ ne .Host.OS "windows" }}'`, using `.Host` (§8.4) and the ordinary skip
semantics. The whole-workflow `platforms:` stays exactly as described above,
because it does something a run-time guard cannot — it stops the workflow being
*offered*, in the picker and at task creation, rather than skipping steps once
a task exists.

#### 8.1.2 Declared task fields (`fields:`)

*Added 2026-08-21 (task 022).* A workflow may publish the task fields it expects
as an ordered list. Clients use the order to build a form before the task
exists; the values themselves remain strings everywhere — in the API, task
row, templates, branch naming, and fan-out inheritance.

```yaml
fields:
  - name: ticket                 # required lowercase slug; .Task.Fields key
    label: Ticket                # optional presentation label
    description: Issue tracker key.
    type: string                 # string (default) | integer | number | boolean
    required: true               # default false
    pattern: '^OPS-[0-9]+$'      # optional Go RE2 expression; string only
```

- `integer` is a base-10 whole number, `number` is a finite decimal, and
  `boolean` is exactly `true` or `false`. A `pattern` is compiled when the workflow loads;
  authors use `^` and `$` when the whole value must match.
- Names are unique within the list. A missing type becomes `string`; a missing
  `required` becomes false. An optional absent or empty value is valid.
- `POST /v1/tasks` is the authoritative validation boundary. A required,
  mistyped, or pattern-mismatched declared value is a 400 before any task is
  inserted. The TUI mirrors the pure checks to place feedback on its Fields row.
- The map deliberately stays **open**: additional names not declared by the
  workflow are accepted, recorded, inherited by fan-out lanes, and available to
  templates exactly as before. Declarations add a public form contract; they do
  not mean `additionalProperties: false`.
- Only the selected root workflow owns this contract. Declarations on included
  workflows or named fan-out lane workflows are not recursively merged. A
  composing workflow re-declares any input it wants to expose; a lane's own
  `fields:` map continues to bind internal values.

### 8.2 Step types and fields

Common to all steps: `id` (required), `name`, `type` (required), `max_retries`,
`timeout`, — *added 2026-08-18 (task 015)* — `if` (§7.7), and — *added
2026-08-25 (task 028)* — `retry_backoff` (§7.2). A `condition` step is the
exception: it takes `id`, `name` and `if` only.

| Type | Required | Optional |
|---|---|---|
| `agent` | `prompt` | `agent`, `model`, `effort`, `permission_mode`, `on_input`, `input_timeout`, `check`, `check_timeout`, `allow_failure` |
| `command` | `run` | `shell`, `env` (map), `check`, `check_timeout`, `allow_failure` |
| `manual` | `instructions` | — |
| `parallel` | `steps` | `max_parallel` |
| `fan_out` | `lanes` | `merge` |
| `condition` | `if` | — |
| `loop` | `steps`, and exactly one of `count` / `for_each` | `max_iterations` |
| `break` | `if` | — |
| `include` | `workflow` | — |

*`include` added 2026-08-19 (task 019); see §7.9. It takes `id`, `name`,
`type` and `workflow` and nothing else — not `if`, `timeout`, `max_retries`,
`retry_backoff`, `allow_failure` or `check` — because it is resolved away at
task creation and owns no attempt for any of them to bind to. It is the third exception to this
table's common fields, after `condition` and `break`.*

*`parallel` and `fan_out` added 2026-08-17 (task 014); see §7.5 and §7.6.
`condition`, `if` and `allow_failure` added 2026-08-18 (task 015); see §7.7.
`loop` and `break` added 2026-08-18 (task 016); see §7.8. A `loop` also takes
the common `if` and `timeout`, and rejects `max_retries`, `retry_backoff`
(*2026-08-25, task 028*) and `allow_failure`: it has no attempt of its own. A `break` is the exception a `condition` is —
`id`, `name` and `if` only.*

A lane carries `id` plus exactly one of `workflow` (a registry name) or
`steps` (inline), and optionally `if` (*added 2026-08-18, task 015*), `fields`,
`agent`, `model`, `effort` and `priority`, which override the inherited values
for that lane's subtree.
`merge` carries `on_conflict` (`block` | `agent`) and, for the latter, an
`agent` step.

Constraints (validated on load and via `POST /v1/workflows/validate`):

- `steps` non-empty; step ids unique; templates must parse; `type` known; durations
  parse as Go durations; `on_input` is `wait`, `deny` or `require`; unknown keys are errors
  (strict decoding) to catch typos.
- *Amended 2026-08-17 (task 014).* Step ids are unique across the **whole**
  workflow, sub-steps included: a `parallel` group's sub-steps share the
  group's `step_index` and are told apart by id alone (§7.5). A group needs at
  least one sub-step and a `max_parallel` of at least 1; its sub-steps may not
  be `manual`, may not be `parallel`, may not be `fan_out`, and may not
  resolve to `on_input: require`. A `fan_out` step needs at least one lane;
  lane ids are unique within the step, and a lane's inline steps have their
  own id namespace because each lane becomes a separate task. `merge.agent`
  is required by, and only valid with, `on_conflict: agent`.
- *Added 2026-08-18 (task 015).* A `condition` step requires `if` and rejects
  every other field, `timeout`, `max_retries`, `retry_backoff` (*2026-08-25,
  task 028*) and `allow_failure` included: it starts no process. `allow_failure` is valid only on `agent` and `command`
  steps, sub-steps of a group included. Every `if` — a step's, a sub-step's and
  a lane's — must parse as a template at load, like every other template field.
  A `condition` step in **last** position is a **warning**, not an error: the
  task is `done` whether it continues or stops, so the step cannot do anything
  a missing step would not.
- *Added 2026-08-18 (task 016).* A `loop` step needs at least one body step
  and **exactly one** driver: `count` (at least 1, and at most the effective
  ceiling — the step's own `max_iterations` when it declares one, else
  `loop.max_iterations` from config) or `for_each`. `max_iterations` is at
  least 1. Body step ids join the workflow-wide namespace, for the reason a
  group's sub-steps do: they share the loop's `step_index`. A body may not
  contain `manual`, `fan_out`, `parallel` or a nested `loop`, and may not
  resolve to `on_input: require`. A `break` requires `if`, rejects every other
  field, and is valid **only** inside a loop body. `count`, `for_each` and
  `max_iterations` are rejected on every other step type.
- *Added 2026-08-19 (task 019).* An `include` step requires `workflow` and
  rejects every other field. `workflow` is rejected on every other step type,
  and `resolved_from` — which the resolver writes into a task's snapshot — may
  not be set by hand beside it. Everything an include implies about the
  *expansion* is checked at task creation rather than at load, because it
  depends on which files the name resolves to: see §7.9's table.
- `platforms` entries are known tokens and carry no duplicate (§8.1.1). The
  list is checked for *shape*, never against the validating host: a POSIX-only
  workflow validates on a Windows CI runner exactly as it does on Linux, or
  `vincent workflow validate` could not be a portable pre-commit check.
- `agent` values must name a known adapter. Each step's resolved
  (agent, model, effort) triple (§8.6) is checked against that adapter's option
  catalog (§9.6). The rule is cross-catalog: a value present in the resolved
  adapter's own catalog is valid; a value found only in *another* adapter's
  catalog (e.g. claude's `sonnet` or `max` reaching a codex step) is a
  validation error; a value in no catalog at all (free-text models, future CLI
  values) passes with a warning — the CLI stays the final authority at run
  time. Validation never probes: it consults the §9.6 cache when primed and
  the curated catalogs otherwise (probing only ever adds values, so a verdict
  can soften but never harden). Warnings surface structurally: `warnings[]`
  beside `errors[]` on registry entries and the validate response (§13.2),
  `warnings[]` on the task-creation response, and the daemon log.
- A step declaring `on_input: require` (§7.4, added 2026-08-17, task 013) must
  resolve to an adapter that can take mid-run input. Only the *static* half is
  judged here — an adapter with no control channel in any version (codex,
  cursor) is an error; claude, whose support is version-gated (§9.3), is left
  to the creation-time and run-time checks, since deciding it would mean
  probing. The finding is attributed to the `agent` field that supplied the
  value: the step's own when it pins one, `defaults.agent` otherwise, reported
  once however many steps inherit it.

### 8.3 Command steps and shells

`run` and `check` strings are template-rendered, then executed via a platform shell:

- POSIX: `/bin/sh -c "<rendered>"`
- Windows: `pwsh -NoProfile -Command "<rendered>"` (falls back to `powershell`)

A step may pin `shell: sh | pwsh | cmd` explicitly. Cross-OS portability of command
steps is the workflow author's responsibility; the spec makes no attempt to translate.
*Amended 2026-08-16 (task 010):* an author who did not attempt it says so with
`platforms:` (§8.1.1), which is enforced rather than translated.

### 8.4 Template context

Templates are Go `text/template`, rendered with `missingkey=error`. Rendering failures
(bad field references, and unknown `.Task.Fields` keys) fail the step *before* any
process is started, with a clear error — a typo never renders a silent hole into a
prompt. Because `Fields` is free-form per task, an *optional* field must be read
defensively: `{{ with index .Task.Fields "ticket" }}…{{ end }}`.

| Variable | Contents |
|---|---|
| `.Task` | `ID`, `Title`, `Description`, `Fields` (map[string]string), `BaseBranch`, `BranchName` |
| `.Project` | `Name`, `Path` (original repo root), `DefaultBranch` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Steps` | map of *completed* step id → `{Status, Result, ExitCode}`; `Result` is the agent's final result text (agent steps) or the last **200** lines of stdout (command steps). *Corrected 2026-08-18 (task 016): this said 100; the daemon has always used 200, and a `for_each:` reading `.Steps[…].Result` (§7.8) makes the exact bound load-bearing rather than incidental.* *Amended 2026-08-18 (task 015):* a step skipped by its guard appears with `Status: "skipped"`, and a **failed** step appears once the engine has advanced past it — which happens only under `allow_failure` (§7.2), and is what a downstream guard reads. A step's own failed attempt stays out of `.Steps` mid-retry, because `.LastFailure` is already that channel; `interrupted` never appears, since §7.2 says it is not an outcome. *Amended 2026-08-18 (task 016):* "advanced past it" is compared on `(step_index, iteration, body position)`, which is what lets a loop body's later steps read its earlier ones while a `parallel` group's members stay blind to each other (§7.8). Under repetition a step id resolves to its **latest** iteration |
| `.Loop` | *Added 2026-08-18 (task 016).* `Index` (1-based iteration, and **0** outside any loop, so a shared template can tell), `Item` (the `for_each` item this iteration runs on — a string; empty for a `count:` loop), `IsFirst`, `IsLast`. See §7.8 |
| `.Host` | *Added 2026-08-18 (task 015).* `OS`, `Arch` — the **daemon's** GOOS/GOARCH, since the daemon is what runs the steps (§8.1.1). This is the per-step platform gate: `{{ ne .Host.OS "windows" }}`. There is deliberately no `.Now`: a guard reading wall-clock makes a run non-reproducible |
| `.Worktree` | `Path` |
| `.LastFailure` | on retry attempts only: `{Reason, Output}` from the previous attempt; empty otherwise |
| `.Conflicts` | *Documented 2026-08-18: the field has shipped since task 014 but was never listed here.* The conflicted file paths a `fan_out` join hands an `on_conflict: agent` resolver (§7.6). Empty for every other step, so a prompt may read it defensively anywhere |

For `agent` steps on attempt > 1, in addition to `.LastFailure` being available, the
daemon appends a structured block to the rendered prompt automatically:

```
<previous-attempt-failure attempt="1">
reason: check command failed (exit 1)
--- output (last 200 lines) ---
...
</previous-attempt-failure>
```

### 8.5 Environment for command/check steps

Inherits the daemon's environment (which inherits the user's), with cwd set to the
worktree, plus:

```
VINCENT_TASK_ID, VINCENT_TASK_TITLE, VINCENT_PROJECT_NAME, VINCENT_PROJECT_PATH,
VINCENT_WORKTREE, VINCENT_BRANCH, VINCENT_BASE_BRANCH, VINCENT_STEP_ID,
VINCENT_STEP_ATTEMPT, VINCENT_WORKFLOW
```

### 8.6 Agent, model, and effort resolution

For each `agent` step, the effective agent, model, and effort resolve in this
order (first hit wins):

1. explicit step field (`agent` / `model` / `effort`)
2. task-level override chosen at creation (§13.2) — replaces workflow
   `defaults`, never an explicit step field
3. workflow `defaults`
4. adapter default (empty = the CLI's own default)

**Agent-scoped inheritance:** `model` and `effort` only inherit from a level
whose resolved agent matches the step's resolved agent. When a step (or a task
override) switches agent without setting them, they reset to the new adapter's
default rather than leaking across — a claude alias like `sonnet` must never
reach codex.

The resolved triple is recorded on every StepRun (§5.4, §14) and passed to the
adapter via `RunSpec` (§9.1).

*Amended 2026-08-24 (task 025).* An ad-hoc **repair** run (§6) has no step in a
workflow file to carry level 1, so the **repair request itself** stands in for
it: `agent` / `model` / `effort` on `POST /v1/tasks/{id}/repair` (§13.2) resolve
ahead of the task override, the workflow `defaults` and the adapter default,
with agent-scoped inheritance applying unchanged. The blocked step's own
selection is deliberately **not** the base — a `command` step has none, and a
repair is a different job from the step it is repairing. Everything else the
run needs (permission mode, `on_input`, timeout) resolves exactly as it does for
a workflow `agent` step, so the workflow's `defaults:` govern it and full-auto,
`wait` and the agent timeout are the fallbacks.

## 9. Agent adapters

### 9.1 Interface

```go
type AgentAdapter interface {
    Name() string                                   // "claude", "codex"
    Detect(ctx context.Context) (Availability, error) // found on PATH? version? logged in (best effort)? supports mid-run input (§7.4)?
    Options(ctx context.Context) (AgentOptions, error) // selectable models/efforts, probed ad hoc (§9.6)
    Start(ctx context.Context, spec RunSpec) (RunHandle, error)
}

type RunSpec struct {
    Prompt         string
    WorkDir        string            // the task worktree
    Model          string            // resolved per §8.6; "" = CLI default
    Effort         string            // resolved per §8.6; adapter-native; "" = CLI default
    PermissionMode PermissionMode    // FullAuto | Restricted
    OnInput        InputPolicy       // Wait | Deny (§7.4); ignored when the adapter lacks input support
    Env            []string
}

type RunHandle interface {
    Events() <-chan AgentEvent  // normalized stream: Output, ToolUse, Usage, InputRequest, InputCanceled, Result, Error
    Respond(resp InputResponse) error // answer the pending InputRequest (§7.4); error if none pending
    Wait() (RunResult, error)   // blocks until process exit
    Kill() error
}

type InputRequest struct {          // §7.4; one surfaced at a time, adapter-queued
    Kind       string           // "question" | "permission"
    Questions  []Question       // kind=question: one or more structured questions
    Permission *PermissionReq   // kind=permission: tool name + action summary
    Raw        json.RawMessage  // adapter-native payload, passed through to clients untranslated
}

type Question struct {
    Text        string
    Header      string
    Options     []string // may be empty; free-text answers are always accepted
    MultiSelect bool
}

type InputResponse struct {
    Answers  map[string][]string // question text → selected/typed answer(s)
    Allow    *bool               // kind=permission: approve or deny
    Response string              // free-text response: the §7.4 deny-mode canned answer,
                                 // or the message a permission denial carries (PR F addition)
}

type ToolUse struct {                // T4.14
    Name    string
    Summary string // the call's subject: the command run, the file edited; "" when
                   // the dialect's arguments carried nothing recognizable
    CallID  string // correlates with the ToolResult reporting this call's outcome
}

type ToolResult struct {             // T4.16
    CallID  string // the ToolUse this reports on
    Name    string // when the dialect repeats it; "" is normal
    Summary string // the outcome in a few words: "exit 0", "+1 −0" — never the
                   // tool's output body, which stays in the transcript
    IsError bool   // "known to have failed", never "assumed fine"
}

type RunResult struct {
    ExitCode     int
    ResultText   string   // agent's final answer/summary
    InputTokens  int64    // 0 if unreported
    OutputTokens int64
    CostUSD      *float64 // nil if unreported (e.g. codex)
    Failure      *Failure // task 003: the adapter's verdict, nil = nothing recognized
}

type Failure struct {                // task 003, added 2026-08-14
    Kind       FailureKind // usage_limit | unauthenticated
    RetryAfter *time.Time  // absolute UTC; nil = the CLI reported no usable reset
}

type AgentOptions struct {
    Models        []Option // known model ids/aliases; never exhaustive — free text is always accepted
    Efforts       []Option // adapter-native effort levels
    DefaultModel  string   // "" = the CLI decides
    DefaultEffort string   // "" = the CLI decides
}

type Option struct {
    Value  string
    Source string // "cli" (probed from the installed binary) | "curated" (catalog shipped with vincent)
}
```

The daemon consumes only this interface; adding an agent (Gemini CLI, etc.) is one new
adapter with zero core changes.

**Tool subjects (T4.14).** `ToolUse` carried only a name through M4, so the
output pane rendered `▸ Bash` — a keyword, not an event. Every dialect has the
detail to hand and threw it away: claude's `input`, cursor's `args`, codex's
item fields. `Summary` is filled by one shared extractor over an ordered
preference of *argument names* (`command`, `file_path`, `pattern`, `path`,
`url`, `query`, `prompt`, `description`) rather than three tables of
tool-name → field: the names converge because the underlying tools do, an
absent key costs nothing, and the summary is flattened to one line and capped
at the adapter so no client needs its own guard. `CallID` exists because
claude batches parallel tool calls (T4.8) — "the result below the call is that
call's result" is false exactly when an agent is doing several things at once.

**Reasoning and outcomes (T4.16).** Two event types join the normalized
stream, and both were reconstructible from streams vincent was already
recording and discarding:

- **`ToolResult`** reports what an invocation *did*. Claude replays results on
  `user` lines as `tool_result` blocks, codex closes a tool item with
  `item.completed`, cursor with `tool_call/completed` — all three fell through
  to `agent.raw`, so a tool call was never followed by its outcome. The event
  carries an **outcome only**, capped at the adapter: the tool's output body
  can be hundreds of lines, and the transcript already holds it verbatim.
- **`Thinking`** carries the model's reasoning. Adapters emit it only for
  **whole blocks**; a dialect that streams token-level deltas coalesces them
  itself and emits when the block closes. See the §9.7 amendment for why that
  constraint is the part of the original decision worth keeping.

**The adapter's failure verdict (task 003, added 2026-08-14).** `RunResult`
carries an optional `Failure`: the adapter's reading of *why* its CLI stopped,
when the wording is one it recognizes. It rides on the result rather than behind
a new interface method because the material — the terminal result and the stderr
tail — already lives inside the handle, and the engine never sees either.

`FailureKind` is an adapter-side enum (`usage_limit`, `unauthenticated`), **not** a
`block_reason`: that vocabulary belongs to the engine and to worktree management,
and the engine does the kind → reason mapping, so a reason string keeps exactly
one source of truth. A nil `Failure` means "nothing recognized" and is every run
that behaves as it did before this field existed.

Parsing is **layered and conservative**, in the precedent §9.7's logged-out
wording sets: recognize the documented shapes, fall through to nil for everything
else, and never guess a reset time — an unparseable or implausible one leaves
`RetryAfter` nil and `usage_limit_recheck_interval` (§12.3) decides instead. The
wordings are **not fixture-verified**: capturing a genuine quota exhaustion means
burning a real five-hour window. Claude ships patterns on that basis; **codex and
cursor recognize nothing** and behave exactly as before, which is a deliberate
asymmetry rather than an oversight — an adapter that guessed would send a
genuinely failed task into a wait it never recovers from.

Both ride the live stream *and* scrollback. A record that appeared only after
a step finished would read as output that went missing while it was running,
and the two mappings — `taskrun.publishAgentEvent` for §13.3 chunks,
`api.normalizeLine` for §13.2 records — are kept in agreement deliberately,
because a client renders both through one path.

An adapter that cannot produce one of these stays silent rather than
approximating it. Codex reasoning items went unnormalized through M4 for
exactly that reason: no capture of one existed, the convention is table-driven
tests against captured real-CLI output, and implementing a
documented-but-unobserved shape fails *silently* if it is wrong — the
reasoning simply never appears, and nothing distinguishes that from a model
that did not reason.

**Closed by capture (T4.17, `codex-cli 0.147.0`).** Reasoning arrives as
`item.completed` with item type `reasoning`, carrying whole `text`, several
times per turn — no `item.started` to correlate against and no deltas to
accumulate. That is claude's shape, not cursor's, so it needs none of the
cursor parser's buffering and satisfies `EventThinking`'s whole-blocks-only
contract directly. The wait was the point: the observed shape settled in one
line a question a blind implementation would have answered wrongly and
silently. Note that emission is effort-dependent — an earlier capture spent
`reasoning_output_tokens: 25` and emitted no item at all, so tokens spent is
not evidence of an item to parse.

**Scored against a third adapter (§9.7, M5):** the claim held for the daemon,
the API, and the engine — cursor is one package plus registry wiring. It did
**not** hold for two edges the interface does not cover: the `§15` option
picker renders every option with no viewport, which a ~180-model catalog
overflows, and `cmd/fakeagent` selects its dialect from argv shape, which
cursor's claude-shaped argv collides with. Both are recorded as M5 tasks
rather than glossed: "zero core changes" is true of the adapter seam, not of
every consumer that assumed two adapters.

### 9.2 Claude Code adapter

- Invocation (indicative; exact flags pinned per detected CLI version at
  implementation time): `claude -p --output-format stream-json --verbose` with
  `--dangerously-skip-permissions` in full-auto mode, cwd = worktree.
- **Prompt is written to stdin**, never passed as an argv element — Windows has an
  8 KB argument limit and prompts embed task descriptions.
- Parses the stream-json events into `AgentEvent`s; token usage and cost come from the
  result event.
- Restricted mode maps to Claude's allowlist flags (`--allowedTools` with an
  edit/read/git/test set).
- Model and effort pass through as `--model` / `--effort`.
- `Options()` probes `claude --help` ad hoc: the `--effort` enum (`low, medium,
  high, xhigh, max` as of 2.1.x) and the documented model aliases are parsed
  from the help text (source `cli`) and merged with the curated catalog (§9.6).
- **Mid-run input (§7.4):** pinned against claude 2.1.226 (fixtures captured from
  real runs live in `internal/agent/claude/testdata/`). The process is additionally
  started with `--input-format stream-json --permission-prompt-tool stdio` (the
  latter is undocumented; it is what enables the AskUserQuestion tool in `-p` mode
  and routes permission prompts over the stream) and stdin is kept open; the prompt
  is then delivered as a single `{"type":"user","message":{…}}` JSONL line instead
  of raw text. Requests arrive as `{"type":"control_request","request_id":…,
  "request":{"subtype":"can_use_tool","tool_name":…,"input":…}}`:
  `tool_name: "AskUserQuestion"` normalizes to a `question` InputRequest (option
  labels from `input.questions[].options[].label`, `multiSelect` honored); any
  other tool normalizes to a `permission` request. `Respond()` writes back
  `{"type":"control_response","response":{"subtype":"success","request_id":…,
  "response":R}}` where R is `{"behavior":"allow","updatedInput":…}` (question
  answers ride `updatedInput.answers` keyed by question text, arrays for
  multi-select; the deny-mode canned answer rides `updatedInput.response`) or
  `{"behavior":"deny","message":…}` for permission denial. In full-auto the CLI
  auto-approves every regular tool before the callback, so only `question`
  requests occur; in restricted mode allowlisted tools auto-approve and every
  other tool falls through as a `permission` request. `supports_input` is
  version-gated to the fixture-verified family `[2.1.0, 3.0.0)` — outside it, or
  when the version is unparseable, the adapter reports `supports_input: false`
  and runs exactly as before (no input flags, raw-text prompt). A control request
  the adapter cannot parse fails the attempt with `input_protocol_error`, never
  hangs; an inbound `control_cancel_request` withdrawing the pending request
  resumes the run (`input_closed`).
- **No non-interactive quota surface** (*added 2026-08-24, task 026*). Against
  claude 2.1.241 the subcommands are `agents auth auto-mode doctor gateway
  import install mcp plugin project setup-token ultrareview update` — there is
  no `usage` and no `limits`. "How much quota is left" is therefore not a
  question this adapter can answer, and per the standing rule a capability an
  adapter lacks is stated here and **not emulated**: `AgentAdapter` gains no
  quota method and this adapter grows no quota parser. What vincent reports
  instead is what it has watched happen — the `usage_limit` stops this adapter
  already classifies, recorded per adapter and served as `quota` on §9.6
  (task 026).

### 9.3 Codex adapter

- Invocation (pinned against codex-cli 0.142.5): `codex exec --json`, cwd =
  worktree, prompt via stdin (piped; no prompt argument). Full-auto maps to
  `--dangerously-bypass-approvals-and-sandbox` — the documented automation
  switch; restricted maps to `--sandbox workspace-write`, writes confined to
  the worktree, the closest analog of claude's allowlist. Caveat: in a linked
  worktree the real git dir lives under the main repo, so a `git commit` from
  a restricted codex step may be denied; vincent itself never needs commits
  (the diff reads the working tree).
- Normalizes Codex's JSONL events (`thread.started`, `item.started`,
  `item.completed`, `turn.completed`, `turn.failed`, `error`); token usage
  comes from `turn.completed` (`input_tokens` taken verbatim, as with claude);
  `CostUSD` is nil. The final `agent_message` item is the result text; a
  stream ending without `turn.completed`/`turn.failed` is an error result,
  mirroring the claude adapter.
- Model passes through as `-m` (a first-class flag as of 0.142.x); effort as
  `-c model_reasoning_effort=…`.
- The CLI enumerates nothing (`--help` documents only `-c key=value`), so
  `Options()` returns the curated catalog (source `curated`): efforts
  `minimal, low, medium, high, xhigh`; **no curated models** — codex model
  availability is account-dependent (the same id is accepted on one plan and
  rejected on another), so pickers offer free text and the CLI default only.
- `codex exec` is strictly non-interactive once started — no mid-run input
  channel exists. `supports_input: false`; codex steps never enter
  `awaiting_input`, and `on_input` has no effect on them (§7.4).
- **`logged_in` is answerable** (*added 2026-08-15, task 005*): `Detect` probes
  `codex login status` alongside `--version`, with cursor's layering exactly
  (§9.5) — non-zero exit `false`, explicit negative `false`, explicit positive
  `true`, timeout or spawn failure `null`. The logged-out wording is not
  fixture-verified, which is why the unknown leg is load-bearing rather than
  defensive.
- **No non-interactive quota surface** (*added 2026-08-24, task 026*). codex
  0.149.0 has no `usage` and no `limits` subcommand; `login status` and
  `doctor` are the whole diagnostic surface. Same conclusion as §9.2: stated
  here, not emulated. codex additionally does not classify a quota stop at all
  (§18) — it surfaces as `agent_error` or `nonzero_exit` — so this adapter
  contributes no observations either, and its `quota` is `null` on §9.6 until
  that changes.

### 9.4 Permission modes

- `full-auto` (default): permission prompts are bypassed. This is the point of
  unattended orchestration; the worktree is disposable and every change is
  inspectable before the engineer merges anything. **This is a real risk surface
  (agents can run arbitrary commands as the user) and is documented prominently.**
- `restricted`: adapter-specific allowlists. On input-capable adapters, denied
  actions surface as `permission` input requests (§7.4, subject to `on_input`);
  on others, steps may stall or fail on denied actions. For sensitive projects.
  **An adapter that cannot restrict on the host platform fails the step rather
  than running it unrestricted** — cursor on Windows is the one such case
  today (§9.7). A restricted mode that silently isn't restricted is worse than
  no restricted mode. The adapter signals this by returning
  `agent.ErrRestrictedUnsupported` from `Start`, which the engine classifies
  as `restricted_unsupported`; the sentinel lives in the adapter *interface*
  package so the engine recognizes the condition without depending on any
  implementation.

Set at workflow `defaults` or per step; there is no daemon-global hardcoded policy.

### 9.5 Detection

`GET /v1/info` reports, per adapter: found/not-found, path, version,
`supports_input` (§7.4), and `logged_in` — `null` when the adapter has no
cheap authentication probe (**claude**), a definite boolean when it does
(codex, cursor). The distinction is load-bearing: an installed-but-unauthenticated
CLI probes as healthy and then fails every single run, so a client that can
only say "found" misleads. Availability is served from the §9.6 binary-identity
cache (primed asynchronously at startup, stat-checked per request), so
installing or upgrading a CLI becomes visible on the next request without a
daemon restart. The TUI surfaces
missing agents at task-creation time (a workflow whose steps need an unavailable agent
is flagged).

*Amended 2026-08-15 (task 005).* The `null` set was "claude, codex"; codex now
probes `codex login status` in `Detect`, so only claude reports `null` — and
the reason is recorded rather than left bare, because "cannot cheaply tell" is
a claim about a CLI, not a gap in an adapter:

- **claude** exposes no non-interactive auth surface at all. The captured
  `--help` (`internal/agent/claude/testdata/help_2.1.224.txt`) carries no
  `login`, `auth` or `status` command, and the only definite answer available
  is a real prompt round-trip — which costs API tokens and seconds on a cold
  cache, contradicting §9.6's "always dynamic, never slow". So claude keeps
  `null`, which also keeps the v0 T1.7 decision (no state-file parsing) intact.
- **codex** has `login status`, and **cursor** has `status`. Both parses are
  layered identically, and the layering is the contract: a non-zero exit is
  `false`, an explicit negative is `false`, an explicit positive is `true`, and
  **anything else — including a timeout or a failure to spawn — is `null`,
  never a guess.** The timeout rule is not optional: on Windows a deadline is a
  `TerminateProcess(pid, 1)`, so a probe killed by its own bound exits 1, and
  reading that as a definite "not authenticated" is a false accusation against
  a logged-in account (T4.22).

There is still **no pre-flight refusal** on `logged_in: false` (§18, task 003
decision 4). This makes the state visible, not blocking.

### 9.6 Option discovery (`GET /v1/agents`)

`GET /v1/agents` returns, per adapter, the availability data of §9.5 plus the
selectable options — models and efforts with provenance, and the adapter
defaults:

```json
{ "agents": [ {
    "name": "claude", "available": true, "path": "…", "version": "2.1.224",
    "supports_input": true, "input_verdict": "supported", "logged_in": null,
    "models":  [ { "value": "sonnet", "source": "cli" }, { "value": "opus", "source": "cli" } ],
    "efforts": [ { "value": "low", "source": "cli" }, { "value": "max", "source": "cli" } ],
    "default_model": "", "default_effort": "",
    "probed_at": "2026-08-07T10:00:00Z", "probe_error": null,
    "quota": null } ] }
```

- **`input_verdict`** (added 2026-08-17, task 013) is the daemon's answer to
  whether this adapter may back an `on_input: require` step (§7.4):
  `supported`, `unsupported`, or `unknown`. It is not derivable from
  `supports_input` alone — `false` there means "no" for an installed binary and
  "nobody can say" for an absent one, and only the first refuses anything — so
  the daemon publishes the verdict its own gate uses rather than leaving each
  client to re-derive the asymmetry.

- **Always dynamic, never slow:** probes run on demand and results are cached
  keyed by *binary identity* (resolved path + mtime + version). Help output is
  a pure function of the installed binary, so the cache is never stale by
  construction: updating the CLI invalidates it and the next request re-probes.
  `?refresh=true` forces a re-probe.
- **Probe failure degrades, never blocks:** if the CLI is missing or its help
  output can't be parsed, the endpoint serves the curated catalog with
  `probe_error` set; free-text entry is unaffected.
- **A failed probe expires; a clean one does not** (T4.22). Binary identity is a
  sound cache key for an answer, not for a failure: nothing about the binary
  changes when a probe times out, so a single bad moment would otherwise be
  served for the daemon's whole lifetime — which is exactly what happened at the
  logon after a reboot, where a cold `codex --version` exceeded its bound and a
  healthy CLI read as unavailable until the daemon was restarted. An entry whose
  availability failed, or whose option probe failed, is re-probed by the next
  request more than a minute later. Re-probing an absent CLI costs no subprocess:
  an unresolved path fails before anything is spawned.
- **Probes never put a window on screen.** The daemon usually has no console of
  its own, and on Windows a console-subsystem child of a console-less parent is
  given a console unless its creator passes `CREATE_NO_WINDOW` (§12.1, T3.8,
  T4.21). Every probe goes through one runner that sets it — and that also
  distinguishes a timeout from a nonzero exit, which a Windows deadline
  (`TerminateProcess(pid, 1)`) otherwise renders identical.
- **Only this endpoint probes:** validation paths (registry load/reload,
  `/validate`, task creation) read the cached catalog when primed and the
  curated catalog otherwise — they never spawn a probe subprocess (§8.2).
- Catalogs are advisory: pickers (§15) always accept free text, and validation
  treats catalog membership per §8.2.
- **Server-side enumeration (§9.7):** an adapter's option probe is normally a
  pure function of the installed binary (`--help`), which is what makes the
  binary-identity key exact. The Cursor adapter breaks that assumption — its
  model list comes from an authenticated network call — so for it binary
  identity is a *floor*, not a guarantee: a plan change adds models the cache
  will not notice until the binary changes or `?refresh=true` is passed. The
  probe is bounded by a timeout and degrades to the curated catalog with
  `probe_error` set, exactly like a failed help parse.
- **`logged_in` is the other value binary identity is only a floor for**
  (*added 2026-08-15, task 005*). Auth state is not a function of the binary at
  all: a cached `false` survives the user logging in until the CLI is upgraded
  or `?refresh=true` arrives. `GET /v1/doctor` therefore asks the cache with
  **refresh forced, unconditionally** — otherwise doctor would break in the
  exact loop it exists for (run doctor, log in, run doctor again, still told
  you are logged out). The cost is one probe per adapter per invocation of a
  command the user ran deliberately, bounded by the adapters' own probe
  timeouts. Giving `logged_in` its own short TTL inside the cache would fix
  every surface rather than one and is the better follow-up if the board's
  staleness becomes a complaint of its own; it was beaten here because it
  splits a cache line that is currently one clean rule.

  *Amended 2026-08-24 (task 026): `logged_in` now has that per-field TTL, and
  this decision is superseded rather than relitigated.* The follow-up the note
  named is implemented: an entry that is otherwise a cache hit but whose
  `logged_in` is older than **`authTTL` = 5 minutes** re-runs **`Detect` only**.
  The option catalog keeps binary identity as its key, which is exact for it —
  help output really is a pure function of the binary — so the cache line is
  split along the seam that was already there rather than abandoned. The
  trigger for doing it now is that the board grew a second per-adapter fact
  (`quota`, below) and a staleness rule that fixed one surface and not the
  others stopped being defensible. Only adapters that *can* answer are
  re-asked: an adapter whose `logged_in` is nil has no auth state to go stale,
  and spawning a subprocess every five minutes to be told nothing again is pure
  cost. Five minutes is chosen the way `failureTTL`'s minute was — long enough
  that a board, a detail view and a new-task form asking in the same second
  cost one probe between them, short enough that a user who logs in and looks
  again is told the truth. **A failed re-`Detect` keeps the previous
  availability, including its `logged_in`, and records the error**: that is
  T4.22's rule applied to the field the TTL exists for, since a Windows
  deadline is `TerminateProcess(pid, 1)` and reading that as "not
  authenticated" is a false accusation against a logged-in account. The clock
  is stamped either way, so a persistently failing probe costs one subprocess
  per `authTTL`, not one per request. `GET /v1/doctor` keeps forcing refresh
  unconditionally — a command the user ran deliberately does not wait out a TTL.

- **`quota`: the observed usage window** (*added 2026-08-24, task 026*). Each
  adapter carries a nullable block describing what the daemon has **watched
  happen** to its usage window:

  ```json
  "quota": { "spent": true, "used_percent": null, "window": null,
             "observed_at": "2026-08-24T14:05:00Z",
             "resets_at": "2026-08-24T14:20:00Z",
             "resets_at_reported": true, "source": "observed" }
  ```

  It is an observation, never a probe. No supported CLI can report remaining
  quota from a non-interactive invocation (§9.2, §9.3, §9.7), so there is no
  quota capability on `AgentAdapter`, no caller in `agent.Probe`, and no quota
  parser in any adapter — shipping the seam with three null implementations
  would cost an interface change and three "cannot report" paragraphs in
  exchange for four permanently-unknown renders. What exists instead is the
  `usage_limit` stop task 003 already recognizes (§18), made durable per
  adapter (§14) and published on change (§13.3).

  - `null`, never a zeroed block, means nothing has been observed for that
    adapter. A zero would read as "empty quota", which is the opposite.
  - `spent` is derived per request (`now < resets_at`). A lapsed reset does
    **not** delete the row: `spent: false` with the timestamps intact is how
    "ran out at 14:05, has since recovered" is said. There is no sweeper and no
    timer.
  - `resets_at_reported` separates a fact from an estimate — `true` when the
    CLI named the reset, `false` when `usage_limit_recheck_interval` (§12.3)
    supplied it. §15 renders `→` for the first and `≈` for the second; a
    computed 15-minute guess must never be shown as something the CLI stated.
  - `used_percent` and `window` are permanently null. They are on the wire so a
    client is written once against the final shape, and fill in the day a
    vendor ships a surface, at which point `source` changes from `observed`.
  - An observation is **retired by evidence**: the next successful agent step
    on that adapter deletes it, because a hold with no reported reset is only
    an estimate and a step that completes proves the window reopened.
  - The same block rides `GET /v1/info` per adapter, from the same read, so the
    board header (which fetches /v1/info) needs no second request and the two
    endpoints cannot disagree.
  - **Probe-failure degradation is untouched.** Nothing here can fail a probe,
    and `probe_error` keeps meaning exactly "the option probe failed and you
    are reading the curated catalog".
  - **§11 is unchanged.** This is display. Admission ordering, both concurrency
    caps and the walk's pause→hold→caps sequence are as they were; a
    near-exhausted agent is shown, never withheld.

### 9.7 Cursor adapter (M5)

Placed after §9.6 rather than between §9.3 and §9.4 deliberately: section
numbers are identifiers cited from code comments, and renumbering §9.4–§9.6
would invalidate every one of them.

- **Binary is `cursor-agent`, never `cursor`.** `cursor` on PATH is the editor
  launcher and would open a GUI; the adapter resolves `cursor-agent` only. The
  adapter's `Name()` — and therefore the workflow `agent:` value and the
  `agents.cursor.path` config key — is `cursor`.
- Invocation (pinned against cursor-agent 2026.08.04-aaa8809):
  `cursor-agent -p --output-format stream-json --trust`, cwd = worktree,
  prompt via **stdin** (piped, no prompt argument — verified: the echoed
  `user` line carries the piped text). Full-auto adds `--force`; restricted
  adds `--sandbox enabled` instead. `--trust` is passed in **both** modes: a
  vincent task runs in a git worktree the CLI has never seen, and a workspace
  trust prompt in a headless run is a hang, not a question.
- **Restricted mode is unavailable on Windows, and fails rather than
  degrades.** `--sandbox enabled` exits 1 with *"Sandbox mode is enabled but
  not available on this system. Sandbox requires macOS or Linux"* before doing
  any work. A cursor step whose permission mode is `restricted` therefore
  **fails to start on Windows** with a stated reason, under the retry policy
  like any other step failure. Falling back to `--force` was rejected outright:
  it would run full-auto a step that explicitly asked not to be, converting a
  §9.4 safety choice into its opposite on exactly one OS — the failure mode a
  user would never think to check for. Cursor's other approval paths do not
  substitute: `--auto-review` prompts for anything its classifier doesn't
  clear (a hang, headless) and is account-gated, and allowlist mode is global
  user config in `cli-config.json` with no per-run flag. This is the first
  place a vincent capability is genuinely platform-dependent; it is stated
  here, in §9.4, and in §18 rather than discovered.
- **`--worktree` / `--worktree-base` are never passed.** Cursor has its own
  worktree feature; worktrees belong to vincent (§10), and two owners of the
  same concept is a defect.
- Normalizes Cursor's stream-json events. The dialect is claude-*shaped* but
  is not claude's, and is parsed by its own package:
  `system/init` → `user` → `thinking/{delta,completed}` →
  `assistant` → `tool_call/{started,completed}` → `result/{success,error}`.
  - `assistant` messages arrive whole (content blocks), not as deltas, and
    normalize to `output`.
  - ~~`thinking` events normalize to `unknown` — transcripted verbatim, never
    surfaced live. They are token-level deltas; a live tail of reasoning
    fragments buries the assistant text it exists to show.~~
    **Amended 2026-08-11 (T4.16).** Reasoning is now surfaced, coalesced. The
    original decision was right about cursor's *shape* and wrong to generalize
    from it: claude delivers thinking as whole blocks and never had the
    fragment problem, so a rule written against `thinking/delta` was
    suppressing reasoning for an adapter that does not stream deltas at all —
    and it was captured on disk, in this repo's own fixtures, and shown to
    nobody. What the decision actually protected survives as a constraint on
    the **interface** rather than a ban on the feature: `EventThinking` is
    emitted for whole blocks only, so this parser accumulates `delta` lines
    and emits one event at `completed`. Two costs, both accepted and both
    pinned by tests. `Event.Raw` gains a documented exception — a coalesced
    block's Raw is the line that *closed* it, while its Text came from the
    deltas before it, which keeps transcript offsets correct because the
    closing line is the one just written. And a run killed mid-block loses the
    buffer, which is the right trade for reasoning text. The swallowed delta
    lines still normalize to `unknown`: they are genuinely unmodeled lines,
    and a reader who asks to see raw lines should see them.
  - `tool_call` carries the tool as the **object key** (`editToolCall`,
    `shellToolCall`), not a `name` field; the `ToolCall` suffix is stripped
    for the normalized name (`edit`, `shell`). `started` is the tool_use
    event, mirroring codex's `item.started`, and `completed` is the
    **tool_result** (T4.16) — never a second tool_use, which would
    double-count every call. Its outcome keys on the *presence* of
    `result.success`: present is a success carrying its detail (an edit's
    `linesAdded`/`linesRemoved` render as `+1 −0`, which says something the
    invocation line did not), absent is a failure with **no detail at all**.
    No capture of a failed cursor tool call exists — the fixture's `completed`
    payloads are reconstructed, because the capture machine had a user-level
    hook that rejected every call — so keying on presence is correct in both
    directions today and degrades to a true statement rather than a silent
    hole, where a guessed failure shape would not.
  - Usage keys are camelCase (`inputTokens`, `outputTokens`, plus
    `cacheReadTokens`/`cacheWriteTokens` which vincent does not record);
    **`CostUSD` is nil** — cursor reports no cost.
  - `result.result` is the concatenation of *every* assistant message in the
    turn, not the last one; it is used verbatim as the result text.
- **Errors do not arrive in the stream.** An invalid model id exits 1 with
  `ActionRequiredError: … Model name is not valid: "…"` on **stderr** and no
  `result` line at all. The adapter therefore keeps codex's stderr tail and
  reports "stream ended without a result event" plus that tail — this is the
  likely shape of an everyday user mistake, so the tail is what makes it
  diagnosable.
- **Effort is not supported.** Cursor has no effort flag: effort is encoded in
  the model id (`claude-sonnet-5-thinking-xhigh`, `gpt-5.4-mini-high`) or in
  an undocumented per-model bracket override
  (`claude-opus-4-8[context=1m,effort=high]`, whose parameter name varies by
  model — `reasoning` for the gpt-5.4 family). The catalog's `Efforts` is
  therefore **empty**, the adapter ignores `RunSpec.Effort`, and §9.7 steps
  select reasoning depth through `model`. This mirrors codex having no curated
  models, and `on_input` having no effect on codex steps: a field an adapter
  cannot honor is documented as ignored, not faked. §8.2 already rejects a
  claude/codex effort value on a cursor step ("it belongs to claude's
  catalog"), which is the error message a workflow author needs.
- **Models are enumerated, and the enumeration is not authoritative.**
  `cursor-agent models` lists ~180 ids (source `cli`); the curated floor is
  `auto` alone. The list is account-scoped *and still over-broad*: a listed id
  can be rejected at run time (`gpt-5.4-nano-low` → `AI Model Not Found`), so
  membership is advisory in both directions and free text stays accepted
  (§9.6). The probe is a network call — see the §9.6 note above.
- **`--model` mutates global CLI state.** Cursor persists the selection in
  `~/.cursor/cli-config.json` (`selectedModel`), so an unset model means "what
  the last invocation chose", not "the CLI default" — including a selection
  made by a *previous vincent step*. The adapter therefore **always passes
  `--model`**, defaulting to `auto` when §8.6 resolves empty, making cursor the
  first adapter with a non-empty `DefaultModel` (the `/v1/resolve` level-4 seam
  from T4.7 reports it with no further change). The cost is accepted and
  documented: running a cursor step overwrites the user's saved interactive
  model selection. Determinism is worth more to an orchestrator than preserving
  an interactive preference, and pinning to `auto` at least lands on cursor's
  own default rather than on wherever the previous task left it.
- **`supports_input: false`.** `cursor-agent` has no input-format flag and no
  control channel; cursor steps never enter `awaiting_input` and `on_input`
  has no effect on them (§7.4), exactly as with codex.
- **Version is recorded verbatim** (`2026.08.04-aaa8809`) — calver plus a
  commit sha, not semver. No version gate exists to parse it into, and the
  sha is part of the binary's identity.
- **`logged_in` is answerable here.** `cursor-agent status` reports
  authentication cheaply, making cursor the first adapter that can populate
  the §9.5 field. This matters because "installed, version-probes fine, fails
  every run at the API" is otherwise indistinguishable from a healthy adapter
  (§9.5). *Note (2026-08-15, task 005):* codex has since gained the same
  ability through `codex login status`, built as a copy of this probe's
  layering. "First" is history, not an exclusive.
- **A plan tier, not a quota** (*added 2026-08-24, task 026*).
  `cursor-agent about --format json` (2026.08.11-e8db854) reports
  `{cliVersion, model, subscriptionTier, osPlatform, osArch, userEmail,
  terminalProgram, shell, lastRequestId}` — the closest thing any supported CLI
  has to a quota surface, and it carries no numbers: no remaining requests, no
  window, no reset. It cannot answer "how much is left", so per §9.2's rule it
  is stated and not emulated. Like codex, cursor does not classify a quota stop
  (§18), so it contributes no observations to §9.6's `quota` either.

## 10. Worktree management

- **Location:** `{data_dir}/worktrees/{task_id}` — outside every repo, so IDE file
  watchers and repo tooling in the main checkout are never disturbed.
- **Creation** (when the scheduler first admits the task):
  `git -C {project.path} worktree add {worktree_path} -b {branch_name} {base_branch}`.
  If `base_branch` doesn't resolve locally, task creation fails fast with a clear error.
- **Branch naming:** `vincent/{task_id}-{slug}` by default. A pre-existing branch of
  the same name fails the task with a clear error rather than reusing it.

  *Amended 2026-08-13 (task 001).* This section used to add "collisions are impossible
  (ids are unique)". That is no longer true, and the change is the reason most of the
  rest of this bullet exists. Names are configurable —
  `built-in < config.yaml < project < per-task literal` — and because vincent **never
  deletes branches**, a template without a discriminator collides on the *second* task
  for the same input. (*Amended 2026-08-16, task 008:* still true of every branch that
  carries a commit, which is the case a discriminator-less template is about — the one
  branch archive may now delete is one that received nothing. The collision checks
  below are unchanged, and task 001's decisions are not reopened.) So collision is a
  routine outcome, not a defensive check:
  - **Legality** is delegated to `git check-ref-format --branch`, never a
    reimplementation of git's grammar, and a rejected name is `branch_name_invalid`
    (§18) rather than silently sanitized.
  - **Collision** is checked twice. At creation, against existing refs and against
    other unarchived tasks' claimed names → `400`, mirroring how `base_branch` already
    fails fast. At admission, `branch_exists` remains the **authority**, because the
    creation check is inherently racy.
  - The collision probe is wider than an exact ref match: git stores refs as a path
    hierarchy, so `feat/foo` cannot be created while `feat/foo/bar` exists, and
    `git rev-parse --verify refs/heads/feat/foo` reports *not found* in that case.
  - A name that needs the task id is rendered inside the insert transaction; the
    git-side checks never run with that transaction open, since a slow git would stall
    every write in the daemon.
  - `branch_exists` is recoverable through `POST /v1/tasks/{id}/retry`'s
    `branch_override` (§12.2). Without it a blocked task would be permanently dead.
- **Isolation caveat (documented, not solved):** git worktrees isolate the working
  tree and index, but share the object store and refs — and **do not** isolate
  process-level resources (global caches, package stores, ports, docker). True
  sandboxing is out of scope for v1.

  *Amended 2026-08-17 (task 014).* This now cuts two ways. A `parallel` group
  runs several processes inside **one** worktree (§7.5), so its sub-steps are
  not isolated from each other at all — concurrent writes to the same file are
  undefined, and that is a workflow bug rather than something the daemon
  arbitrates. A `fan_out` lane, being a real task, gets the ordinary isolation
  (§7.6) — and leaves its own worktree behind until someone archives it, so an
  N-lane fan-out costs N worktrees on disk. `vincent gc` and `vincent doctor`
  are what that pressure is for.
- **Cleanup:** on `archive`: `git worktree remove` (+ `--force` after an explicit
  dirty-worktree confirmation), then `git -C {project.path} worktree prune`. A branch
  that carries **any commit past its base** is never deleted by vincent.

  *Amended 2026-08-16 (task 008).* This bullet used to read "the branch is **never**
  deleted by vincent". It has exactly one exception now: a branch with **no commits
  past the base recorded on its task** is deleted at archive time. A workflow that
  files an issue, posts a summary or reviews read-only writes nothing, so every run
  used to leave a ref that holds nothing to lose, and branch names are configurable —
  there is no `vincent/*` glob that reliably finds them again. The rules:
  - **The test is `git rev-list -n 1 {base_branch}..{branch_name}` producing no
    output** — the tip is an ancestor of the base. Both fields are on the task row.
    It stays correct when the base moves forward after the task started, costs one
    cheap git call, and **any** git failure (base renamed or deleted, repository gone)
    reads as *cannot judge* and keeps the branch, reported as `unknown` and distinct
    from `has_commits` the way `dirty_unknown` is from `worktree_dirty`. Deleting when
    the *net diff* is empty was rejected: it destroys real commit objects.
  - **`git branch -d`, never `-D`.** Its own merged check is a second belt behind the
    rev-list, and its refusal is what covers a branch checked out in another worktree.
  - **Ordering is worktree removal → transition → branch.** The branch is checked out
    in the worktree until the worktree is gone, and an archive that has committed must
    not be reversible by a branch problem. A dirty worktree refused without `force`
    therefore never reaches the branch step at all.
  - **`delete_empty_branch_on_archive` (§12.3), default true,** is the standing policy;
    a per-archive flag beside `force` was rejected, since the project-delete path has
    no human to ask. Setting it false restores this bullet's pre-008 behaviour exactly.
  - **The remote counterpart is a separate key, `delete_remote_branch_on_archive`,
    default false, honoured only by `POST /v1/tasks/{id}/archive`.** Deleting a branch
    on a forge other people share is unrecoverable and outward-facing, which is further
    than "the unattended path never deletes" (task 005) was ever written about. It runs
    only after a local delete that succeeded, only when the branch has a configured
    upstream (`branch.{name}.remote` + `.merge`; no upstream ⇒ nothing was pushed as
    far as vincent knows, so nothing is attempted), and its failures — rejection,
    unreachable host, timeout — are logged and never fail the archive.
  - **`DELETE /v1/projects/{id}?force` sweeps every row it is about to drop,** archived
    ones included: the cascade erases the branch names, so that is the last moment they
    exist. Local leg only, best-effort, exactly like the worktree removal beside it.
  - **`vincent gc` and `vincent doctor --fix` gain nothing** — see the task 005
    amendment below.
  - **The outcome is reported on the archive response (§13.2), not as an event.**
    `archived` is terminal and a `block_reason` would be a lie on it; every other path
    logs to `daemon.log`. No new event type and no migration.

  *Amended 2026-08-15 (task 005).* "Only on archive" was true of a **task's** worktree
  and remains so. It left a second reclaim path missing entirely, because two things
  produce a directory under a data root that no task will ever name again:
  - `DELETE /v1/projects/{id}` removes worktrees best-effort by the T1.5 decision and
    `DeleteProjectCascade` drops the rows regardless. A removal that fails — a file
    locked by another process on Windows, a permissions problem, a shell sitting in the
    directory — leaves the directory behind with every reference to it gone.
  - A crash between `git worktree add` and the write that records `worktree_path`
    leaves the directory present while the row survives claiming nothing. The task's
    next admission then fails `worktree_path_occupied` (§18).

  So **an orphan is an entry directly under a data root that no task row claims**, and
  `vincent gc` (§12.1) reclaims them. Claim is by `worktree_path`, not by directory
  name: the name-based reading misses the second producer, whose directory *is* named
  after a live row. The rules:
  - **Archive stays the only path that removes a task's worktree.** gc removes only
    what no task claims. Making project delete's removal authoritative was rejected in
    the same discussion: it strands the user with an undeletable project over a locked
    file and does nothing about the crash case.
  - **Deletion is confined to the data roots.** `{data_dir}/worktrees` and
    `{data_dir}/transcripts` — the same containment check a forced archive uses, so a
    `worktree_path` naming anything outside is refused whatever the database says.
  - **A dirty worktree is skipped without `--force`,** by `Manager.IsDirty`'s rule
    (`git status --porcelain`, untracked included). Dirtiness git cannot *determine*
    is `dirty_unknown` (§18) and is likewise skipped: an orphan's `.git` file points
    into a repository that is often deleted or pruned, which makes this the common
    answer rather than the rare one, and it is reported distinctly because "you have
    uncommitted work" and "nobody can tell" are different facts.
  - **Non-directory entries are reported, never removed.** vincent only ever creates
    directories under these roots.
  - **Branches are never deleted here either.** §10's standing rule has no gc
    exception. (*Confirmed 2026-08-16, task 008*, which gave archive one: no orphan has
    a branch that is both **known** and **safe to delete**. A row-less orphan has no
    `base_branch` and no `branch_name` to test, and usually no reachable repository to
    test them in; the crash-window orphan is named after a **live** task row whose
    branch must survive. A deletion path here would have no input, and a report line
    would read the same on every row.)
  - **The reverse mismatch is reported, not repaired:** a task row whose
    `worktree_path` names a directory that is gone (§18's `worktree_missing` shape).
    There is nothing to delete and no row is modified.
  - **The unattended path never deletes.** Daemon start scans and logs (§12.4, §17);
    the count rides `GET /v1/info`. `vincent gc` deletes by default, and the dirty
    check, the containment rule and the printed byte report are what make that
    acceptable when a human is behind it.
  - Removal is a direct delete inside the data roots, not `git worktree remove`:
    there is no task row left, so there is no project path to run it from.
    **`git worktree prune` is not run in the user's repos**, so a stale
    registration can survive there after the directory goes; the report names
    that and points at the command, rather than reaching into a repository it
    was not asked to touch.

  *Amended 2026-08-16 (task 006).* `vincent doctor` reports this same set and
  `vincent doctor --fix` reclaims it by calling the same code — one classifier,
  one removal path, one definition of "orphan". Doctor adds no rule of its own:
  a second, name-based reading was written first and withdrawn here, because the
  crash-window orphan is named after a live task and a name-based scan would
  leave it in place forever while the task's next admission kept failing
  `worktree_path_occupied`. What doctor contributes is the *report* — the count
  and bytes beside the disk figures, in the one command that answers "why is
  nothing running?" — plus, with no daemon answering, an explicit "orphans
  unknown" rather than a guess, since the claim set lives in a database only the
  daemon opens (§4).
- **Repo deletion / path moves:** if the project path disappears, affected tasks go
  `blocked` with a descriptive reason; project records can be re-pointed via
  `PATCH /v1/projects/{id}`.

## 11. Scheduler and concurrency

- Two caps, both counting tasks in a **slot-holding** state — `running` and
  `awaiting_input` (§6; the latter's agent process is alive, merely idle on its
  stdin, so it costs a slot exactly like a running one):
  - **global** `max_parallel_tasks` (config file, default 3),
  - **per-project** `max_parallel_tasks` (project setting, default unlimited).
- A `queued` task is admitted when both caps have headroom. Admission order:
  `priority` DESC, then `created_at` ASC (FIFO within a priority).
- One task runs at most one step process at a time. *Amended 2026-08-17
  (task 014):* a `parallel` step (§7.5) runs up to `max_parallel` processes
  inside that one task's single slot. This is a **second concurrency
  dimension the caps above do not govern** — they count tasks, not
  processes — so a board reading "1 running" may be a machine running four
  compilers. `parallel.max_parallel` (config, default 4) is what bounds it,
  and a group's own `max_parallel:` overrides that per group.
- `awaiting_gate`, `blocked`, and `paused` tasks hold **no** slot — a gate can wait
  hours without starving the queue. After approve/retry/skip/resume, the task
  re-enters `queued` and competes under the normal ordering (its original
  `created_at` naturally favors it).
- The scheduler re-evaluates on every state change, on config reload, and when a
  project's cap changes. It is a single goroutine and the only place `queued → running`
  happens, so the caps cannot race.
- A `queued` task whose pause was requested while it was running (§6) is not admitted:
  the scheduler moves it straight to `paused` instead. A pause therefore survives a
  crash, which re-queues the task without clearing the request.
- **Admission holds** (*added 2026-08-14, task 003*). A queued task may carry
  `admit_not_before` — an instant before which it is not admissible — and a
  `queued_reason` naming what it is waiting for. There are two producers, both
  §7.2's: `usage_limit`, and — *added 2026-08-25 (task 028)* — `retry_backoff`,
  the wait between two attempts of a step that asked for one. The pair of
  columns is generic, which is why the second producer cost no migration, no
  second branch in this walk and no client change. The walk applies the three checks **in this order**:
  1. **pause** — a pending pause parks the task, held or not. This runs first
     because a human asked for `paused`, and a task showing `queued` until a hold
     expired would be the same lie the cap check already avoids. It is also why
     the hold is evaluated in the walk and **not** filtered out in SQL.
  2. **the hold** — skip and keep walking; a held task must not starve the queue.
  3. **the caps**, as above.

  No timer is needed: the scheduler's 5 s safety-net tick is what notices an
  expired hold, since nothing commits a state change when one lapses. That is the
  tick's second reason to exist — otherwise it normally finds nothing to do.

## 12. The daemon

### 12.1 Binary and commands

One Go binary, `vincent`:

| Command | Behavior |
|---|---|
| `vincent` | Launches the TUI; auto-starts the daemon in the background if unreachable |
| `vincent daemon` | Runs the daemon in the foreground (logs to stderr; for debugging/service managers). `--config-dir`/`--data-dir` pin the §12.2 directories for a manager with no per-process environment |
| `vincent daemon start / stop / status` | Background daemon management (start detaches; stop = graceful shutdown) |
| `vincent service install / uninstall / status` | Registers OS-native autostart, always as the invoking user: launchd agent, systemd user unit, Windows Scheduled Task |
| `vincent workflow ls / validate [file]` | Registry listing / YAML validation |
| `vincent project add <path> / ls` | Thin API clients for scripting |
| `vincent task add / ls / show <id> / cancel <id> / follow-up <id>` | Thin API clients for scripting. *Amended 2026-08-25 (task 027):* `follow-up` takes exactly one of `--prompt`, `--run` and `--workflow`, plus optional `--agent`/`--model`/`--effort` (§13.2) |
| `vincent gc [--dry-run] [--force] [--json]` | Reclaims data-root directories no task claims (§10); a thin API client like the rest |
| `vincent doctor` | One diagnostic report: paths, daemon, log tail, database, agents, storage, task counts (§17). `--json` for scripting and bug reports; `--fix` (`--force`) reclaims orphaned worktrees and compacts the database. Exit 0 healthy · 1 problems found · 2 no daemon answered |
| `vincent version` | Build info |

*Amended 2026-08-15 (task 005).* `gc` breaks this table's noun-verb pattern
(`project add`, `task ls`) knowingly: `git gc` is the idiom users already have, and the
scope spans two directory trees — worktrees and transcripts — so a `worktree` noun
would have been wrong on the day it shipped.

*Added 2026-08-25 (task 027).* `follow_up` is the one §6 human action with a
command line. `retry`, `repair`, `skip` and `approve` are deliberately
TUI-and-API only, and stay that way; the reason to break with them here is that
"rebase these six finished branches onto current master" is a batch, and a batch
wants a shell loop rather than six visits to a form. The unevenness that leaves
is accepted rather than papered over — giving every human action a command line
is separate work.

*Added 2026-08-15 (task 006).* `vincent doctor` is the one data subcommand that
still produces a **full report when no daemon answers**, the way
`workflow validate` deliberately works offline: the daemon being down is one of
the answers, and a diagnostic that refuses to speak until the thing it
diagnoses is healthy would be useless. In that mode the database and task rows
read *unknown — daemon not running* rather than being read from a second
process ("only the daemon opens SQLite" is an ownership invariant, §4), and
`--fix` is refused — every repair is a write, and the daemon performs every
write.

Single-instance enforcement: a lock file in the data dir; a second daemon exits with a
pointer to the running instance.

**Service registration** (T4.1) is per-user on every platform, because the OS
user is the trust boundary (§16) and the daemon reads that user's config and
writes that user's data dir:

- **launchd** — a LaunchAgent in `~/Library/LaunchAgents`, not a root
  LaunchDaemon. `KeepAlive` is conditional on a *non*-clean exit: a daemon
  that exits 0 was asked to stop, and relaunching it would make
  `vincent daemon stop` impossible. The same reasoning makes the systemd unit
  `Restart=on-failure` rather than `always`.
- **systemd** — a user unit in `~/.config/systemd/user`. Surviving logout
  additionally needs `loginctl enable-linger`, which the installer attempts
  and, on failure, reports as the exact command to run: the service is
  installed and running either way, so this is a warning, not a failed
  install.
- **Windows** — a **Scheduled Task triggered at logon**, running as the
  invoking user with an `InteractiveToken` principal (T4.19). Not a Windows
  Service: the SCM has no per-user services, and an empty `ServiceStartName`
  defaults to **LocalSystem**, so the daemon resolved `LOCALAPPDATA` to the
  SYSTEM profile, wrote its database and `daemon.json` under
  `C:\Windows\System32\config\systemprofile\`, and every TUI launch found
  nothing there and auto-started a second daemon of its own. Pinning the
  directories alone would have hidden that behind a worse defect — §16's
  full-auto agents running as SYSTEM, without the user's agent-CLI
  credentials, `.gitconfig` or `PATH`. A task in the user's own session is the
  per-user registration this section already required, and it needs **no
  elevation** to install, uninstall or query.

  Four scheduler defaults are overridden because each one stops a long-running
  daemon: `ExecutionTimeLimit` (`P3D` by default) is `PT0S`, both battery
  settings and `StopOnIdleEnd` are `false`. `RestartOnFailure` is the analog of
  `Restart=on-failure` and works for the same reason — a nonzero exit is a
  failure, a daemon that exited 0 was asked to stop. The directories travel as
  `--config-dir`/`--data-dir` **arguments**, since a task's `Exec` action has no
  environment; both flags simply publish the same variables the plist and the
  unit set, so §12.2 keeps one resolution point. The definition is handed to
  `schtasks /Create /XML` as UTF-16LE, which is the encoding it accepts for
  anything not pure ASCII.

  The action runs `vincent daemon --hide-console` (T4.20). An `InteractiveToken`
  principal runs on the user's desktop, and nothing in a task definition
  suppresses a console-subsystem process's window — `<Hidden>` governs whether
  the *task* is listed in Task Scheduler, not whether its process draws
  anything. So every logon left a terminal on the desktop whose close button
  stopped the daemon, since closing a console sends `CTRL_CLOSE_EVENT` to
  everything attached to it. Only the creator of a process can suppress its
  console and here that is the scheduler, so the daemon deals with the console it
  is handed, and only when it is that console's sole owner — passed by hand in a
  terminal the flag does nothing, rather than taking the user's own shell down.

  The daemon **releases** the console (`FreeConsole`) rather than hiding its
  window (T4.21, revising T4.20). Hiding is a race it cannot win: on Windows 11
  the default terminal is Windows Terminal, so the console is handed off to it,
  the handoff *replaces* the console window, and Windows Terminal's cold start at
  logon far outlasts the daemon's first few milliseconds — so the hide applied to
  a superseded window and a live terminal tab was still on the desktop after a
  reboot. Releasing the console is not a window property but a terminal state:
  the last client leaving ends the console session, so the host exits and takes
  any window with it, including a handoff still in flight. The standard handles
  are pointed at `NUL` first, since they are console handles until they are not:
  foreground logging writes stderr and the log file through one `io.MultiWriter`
  that stops at the first error, and every child process inherits them. What
  remains is one flash between the scheduler creating the process and the daemon
  reaching that call. Because the daemon then has no console, every probe
  subprocess must pass `CREATE_NO_WINDOW` too (§9.5, §9.6) or each would be given
  a console — a window — of its own.

  Running `daemon start` from the action and letting the existing detached spawn
  give the daemon no console at all was the alternative, and is rejected: the
  task's process would be a launcher that exits immediately, so the registration
  would report `Ready` while the daemon ran, and `RestartOnFailure` would
  supervise the launcher's exit code instead of the daemon. The daemon stays the
  task's own process, which is what keeps this section's promise identical on all
  three platforms.

  **Install unelevated.** A task registered by an *elevated* process is owned by
  `BUILTIN\Administrators`, and the ACL Task Scheduler writes leaves the account
  itself read-only — so a later `/Create /F` or `/Delete` from an ordinary prompt
  fails with `ERROR: Access is denied`, naming neither the owner nor the remedy.
  Installed from an ordinary prompt, `CREATOR OWNER` grants the account full
  control and every later install and uninstall needs no elevation. `install` and
  `uninstall` detect the denied case — from the definition's ACL, not from
  schtasks' localized message — and answer with the elevated `uninstall` that
  clears it.

  What this costs is boot survival: the task starts at the next **logon**, not
  at boot. That is exactly what a LaunchAgent does and what a systemd user unit
  does without lingering, so the promise is now the same on all three
  platforms. Running with nobody logged in needs a service account with a
  stored password, which is a different feature.

  A pre-T4.19 LocalSystem service is detected and refused by `install`, removed
  by `uninstall`, and named by `status` — it is machine-wide, so removing it is
  the one Windows operation that still asks for an elevated prompt, and says
  so. `vincent daemon` keeps its `svc.IsWindowsService()` branch: nothing
  vincent installs trips it (a task's parent is the scheduler's `svchost`, not
  `services.exe`), but it is what makes a hand-rolled `sc.exe create` work at
  all, since the SCM kills a silent process after ~30 s with error 1053. Its
  Stop handler cancels the very context the daemon already drains, so §12.4's
  shutdown is not reimplemented.

**The config and data directories in effect at install time are written into
the unit.** A service does not inherit the shell that installed it, so
`VINCENT_CONFIG_DIR`/`VINCENT_DATA_DIR` overrides would otherwise apply to the
CLI and not to the service, and the two would silently use different
databases.

**So is `PATH`, for the same reason** (T4.15, found on the macOS service leg).
A service manager supplies its own minimal `PATH` — launchd's is
`/usr/bin:/bin:/usr/sbin:/sbin`, a systemd user manager's is barely wider —
and every agent CLI installs outside it: Homebrew, an npm prefix, an nvm shim
dir, `~/.local/bin`. Since §9.5 resolves adapters with `exec.LookPath`, an
installed service found **none** of them while the same daemon started by hand
found them all: the daemon ran, the TUI listed every adapter as missing, and
nothing in either said why. The shell running `service install` has, by
construction, the `PATH` that works.

Two consequences are deliberate. The captured `PATH` goes **stale**: a CLI
installed somewhere new after the service was installed needs a
`vincent service install` to be seen again, which is the same "reinstall to
recapture" contract the dirs already have. And **Windows does not capture it**
— since T4.19 the task runs in the user's logon session and therefore already
has the user's own `PATH`, including the `%APPDATA%\npm` prefix this finding
was about; freezing a copy would replace a live correct value with a stale one.
(Before T4.19 the reason was the opposite one: a LocalSystem service inherited
the *machine* environment, which has no per-user npm prefix at all.) On every
platform the standing answer to an agent that will not resolve is the §12.3
`agents.<name>.path` knob, which is absolute and never consults `PATH`.

### 12.2 Directories (platform-native)

| Purpose | Linux | macOS | Windows |
|---|---|---|---|
| Config | `~/.config/vincent/` | `~/Library/Application Support/vincent/` | `%APPDATA%\vincent\` |
| Data | `~/.local/share/vincent/` | `~/Library/Application Support/vincent/data/` | `%LOCALAPPDATA%\vincent\` |

```
{config_dir}/                # created 0700 (§12.2 amendment below)
  config.yaml                # §12.3, created 0600
  workflows/*.yaml           # global workflows
{data_dir}/
  vincent.db                 # SQLite, WAL mode
  token                      # API bearer token, created 0600 at first start
  daemon.json                # { "port": N, "pid": N, "started_at": … } for client discovery
  daemon.lock
  tui.json                   # TUI-local state (the §16 first-run acknowledgment)
  worktrees/{task_id}/
  transcripts/{task_id}/{step_index}-{attempt}.jsonl
  transcripts/{task_id}/{step_index}-{step_id}-{attempt}.jsonl  # sub-step of a parallel group (§7.5)
  transcripts/{task_id}/{step_index}-i{iteration}-{step_id}-{attempt}.jsonl  # loop body step (§7.8)
  logs/daemon.log            # rotated, size-capped
```

**The config directory and `config.yaml` are owner-only.** *Added 2026-08-25
(#141).* On POSIX the daemon creates `{config_dir}/` `0700` and
`{config_dir}/config.yaml` `0600`, subject only to a stricter umask. This
section was previously silent on both, and the code created them `0755`/`0644`.
`config.yaml` is the one file vincent creates that can hold user-supplied
secrets — values under `environment.set` are literal (§12.3), which is where an
API token or a license key ends up — so it matches `{data_dir}/token` rather
than being the outlier.

- **Existing installations are re-tightened, not warned about.** Every daemon
  start drops group and other access from both paths, the way the token file is
  chmodded back to `0600` on every start. Owner bits are kept and *contents are
  never rewritten*. Because this can undo a mode a user set deliberately, it is
  never silent: the daemon logs the path, the mode it found and the mode §12.2
  asks for, and `vincent doctor` reports the same as a warning row carrying the
  exact `chmod` (§13.2). The warning is **not** part of the closed unhealthy set
  and does not change `vincent doctor`'s exit code.
- **Windows is unchanged and stays that way.** The mode argument is ignored
  there; access comes from the per-user ACL `%APPDATA%` inherits, which is the
  story already recorded for the token file (T1.3). No DACL code, and no
  mode-based warning a reader has no `chmod` to act on.
- **Scope is the config directory and `config.yaml`.** `{data_dir}` is already
  `0700` in practice — the daemon creates `{data_dir}/logs` `0700` before the
  store opens — and `vincent.db` keeps the driver's mode.

**What a transcript promises, exactly.** *Added 2026-08-24 (#139).* A
transcript is the complete record of one attempt: agent stream lines verbatim,
command and check output, and vincent's own `vincent.*` annotations. Three
limits are stated rather than assumed:

- **A line is not a unit of capture.** Command output longer than one record
  is written as a run of `vincent.output` records marked `partial`, in order,
  on one stream. Rejoining them in order reproduces the line. Nothing is
  dropped and nothing is truncated to make a line fit.
- **Incompleteness is never silent.** A failed write, encode or close latches
  on the transcript, and the attempt fails `transcript_io_error` (§7.1, §18)
  instead of reporting a success over a record that is missing the run it
  describes. `Close` is checked, because a buffered filesystem reports ENOSPC
  there and nowhere else.
- **Persisted, not fsynced.** vincent writes and closes, and checks both. It
  does not fsync per line; a transcript can therefore lose its tail to a host
  that loses power, and an audit-grade durability mode is a separate decision.

The one size-based exception stays §12.3's `transcript_max_bytes`: past the cap
the run is killed and the attempt fails `transcript_limit`, with the partial
transcript kept.

Live-output offsets (§13.3) never over-claim: an append advances the published
offset by the bytes the write actually returned, so an offset always names a
position the file has reached.

### 12.3 Configuration (`config.yaml`)

```yaml
listen: 127.0.0.1:0          # 0 = ephemeral port, published via daemon.json; may be pinned
max_parallel_tasks: 3        # global cap
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h           # max wait in awaiting_input (§7.4)
delete_empty_branch_on_archive: true   # archive deletes a branch with no commits past its base (§10)
delete_remote_branch_on_archive: false # …and its upstream counterpart; attended archive only
transcript_retention_days: 90   # transcripts of *archived* tasks older than this are pruned
transcript_max_bytes: 512MB     # per-run transcript cap (§18); past it the step fails `transcript_limit`
usage_limit_recheck_interval: 15m  # how long a quota-held task waits when the CLI named no reset (§11)
parallel:
  max_parallel: 4            # sub-steps of one `parallel` group at once (§7.5); the §11 caps do not see these
log_level: info
debug: false                 # record each step's resolved settings and full argv in its transcript
environment:                 # what child processes inherit (T4.23)
  inherit: all               # all (default) | none | [PATH, HOME, …]; an empty list means none
  unset: []                  # names dropped after inherit
  set: {}                    # literal values, applied last; no expansion
agents:
  claude: { path: "" }         # "" = resolve from PATH
  codex:  { path: "" }
  cursor: { path: "" }         # resolves `cursor-agent`, never `cursor` (§9.7)
tui:                           # view preference; the daemon validates and relays it (§15)
  board:
    group_by: [project, workflow]  # task-table grouping, outermost first; [] = flat
```

**`usage_limit_recheck_interval` (task 003, added 2026-08-14).** How long a task
waits before being re-admitted after its agent reported a spent usage quota
*without* a reset time; when the CLI reports one, that timestamp wins and this is
unused. Must be positive — zero would re-admit on the very next tick, which is the
respawn loop the hold exists to stop. 15 m bounds a five-hour window at roughly
twenty wasted spawns, and a user who knows their plan can tighten or widen it.
There is deliberately **no** exponential backoff: that would be per-task state the
row has to carry and a second retry-ish concept beside §7.2's. Read per hold, so a
hot reload reaches the next one. *Amended 2026-08-25 (task 028):* §7.2's own
`retry_backoff` does not reopen that. It is a **fixed** delay computed from
resolved configuration at the moment of the wait, so it carries no per-task
state either, and it is §7.2's concept rather than a second one beside it. It
has no key in this file for the same reason `max_retries` has none: retry
policy is a workflow's business, and `defaults:` here is timeouts.

**`delete_empty_branch_on_archive` / `delete_remote_branch_on_archive` (task 008,
added 2026-08-16).** The §10 branch-cleanup pair. The local key is the standing
policy: on archive, a branch with no commits past its recorded base is deleted, and
`false` restores the pre-008 behaviour exactly — no branch is deleted on any path.
The remote key is deliberately **not** its default-true sibling. Everything it does
happens on a forge shared with other people and cannot be undone, so it defaults to
`false` and is honoured only by `POST /v1/tasks/{id}/archive`, where a human asked for
this one task; `DELETE /v1/projects/{id}?force` never touches a remote. It is inert
while the local key is off — the remote leg runs only after a local delete that
succeeded — and that combination is a startup/reload **warning**, not a load failure:
a key that is merely unreachable is not an invalid one, and refusing the file over it
would revert every unrelated edit in the same save. Both are read per archive, so a
hot reload reaches the next one.

**`tui` (task 009, added 2026-08-16).** The one section the daemon does not act
on. It validates it, hot-reloads it with the rest of the file and serves it on
`GET /v1/config`; the TUI reads it from there. It lives in this file rather than
one of the TUI's own because the TUI is a pure API client (§15) — it reads no
configuration from disk, and a second file would be a second path, a second
reload story and a second `vincent doctor` line for one setting. `board.group_by`
is the task table's grouping, outermost level first; the accepted levels are
`project` and `workflow`, an unknown or repeated one fails the load, and `[]` is
the flat table every version before this one rendered. `state` is deliberately
not a level: the band sort already orders by state and pins what is waiting on a
human above everything, and a state grouping would fight the one ordering rule
the board is not allowed to lose.

**`environment` (T4.23).** Governs every process the daemon spawns — agent
steps via `RunSpec.Env` (§9.1), command steps and their checks via §8.5's
environment — resolved in one order: `inherit` → `unset` → `set`. Command
steps then layer the §8.5 `VINCENT_*` variables and their own `env:` on top,
so neither `unset` nor `set` can reach those, and a step's `env:` still wins.

The default `inherit: all` is what the daemon did implicitly before the key
existed: the detached spawn inherits the launching shell, `RunSpec.Env` was
never populated, and each adapter overrides only a non-nil one — so a task's
environment was decided by whatever started the daemon and recorded nowhere.
Scrubbing variables by default was rejected: an inherited `USERPROFILE` is
where an agent CLI's own credentials live, and deleting it would trade a rare
loud failure for quiet breakage across every adapter. The defect was that the
value was *accidental and unrecorded*, not that it was set.

- **Values under `set` are literal.** `$` is not special. Expansion would have
  to arrive as its own key rather than as a change of meaning here, since
  adding it later would silently reinterpret an existing literal containing
  `${`.
- **An empty `inherit` list means nothing, not everything.** The form is an
  explicit mode rather than an inference from list length, so the narrowest
  request expressible cannot be read as the widest.
- **The policy is honored as written; a missing load-bearing variable is
  warned about, not corrected.** §9.5 resolves adapters with `exec.LookPath`
  *in the daemon* and starts them by absolute path, so an agent with no `PATH`
  starts and then fails when the CLI shells out — silent and late. A hermetic
  environment with an absolute-path toolchain is a legitimate request, so
  vincent says so and runs.
- **The daemon logs the resolved variable *names* at startup and on any reload
  that changes the set — never the values, at any level.** An environment
  block holds credentials and a log gets pasted into issues; the same
  reasoning keeps `debug` off by default because argv can carry a prompt. For
  that reason the resolved environment is not written to step transcripts
  either.
- **Daemon-global.** A workflow cannot pin its own; command steps already
  carry `env:` for additions. Per-workflow environment would need a §8.2
  schema addition and a second precedence chain beside §8.6's, and is additive
  later if it is ever asked for.

Config is authoritative in the file; the daemon watches and hot-reloads it. The API
exposes it read-only (`GET /v1/config`). Per-project settings live in the DB and are
edited via `PATCH /v1/projects/{id}`.

### 12.4 Crash recovery

- Before starting any step process, the daemon persists the StepRun (`running`) with
  the child PID and start time once spawned.
- On startup: any StepRun still marked `running` is finalized as `interrupted`; if its
  recorded PID still exists *and* its start time matches the journaled spawn time
  (within a small tolerance — the guard against PID reuse), the process is killed
  (orphan). The owning task returns to `queued` and the interrupted step re-runs as a
  fresh attempt that does **not** consume a retry. Tasks found in `awaiting_input`
  are treated identically — the pending request is discarded with the process, and
  the fresh session may re-ask (§7.4).
- Graceful shutdown (`daemon stop`, SIGTERM, Windows service stop): a
  `daemon.shutting_down` event is emitted first, then admission stops, running
  processes get 15 s to exit after a termination signal, then kill; those runs are
  marked `interrupted` (same resume path as a crash). The API — SSE streams included —
  stays up through the grace so clients watch the wind-down live; streams close
  before the final HTTP drain.
- Because agent steps are fresh sessions operating on a worktree whose committed state
  survives, re-running an interrupted step is safe by construction; workflow authors
  are advised to have agents commit incrementally.
- *Added 2026-08-15 (task 005).* Recovery reconciles **rows and processes, not
  directories**. The directory tree is reconciled by a separate startup pass that only
  reports (§10): it logs one warning per orphan and raises the `orphans` count on
  `GET /v1/info`, and it deletes nothing — `vincent gc` does that, with a human behind
  it.

*Amended 2026-08-17 (task 014).* A `fan_out` join interrupted mid-merge is
recovered the same way any step is — the attempt is `interrupted` and re-runs
— with one extra move: if a merge is still in progress in the worktree, it is
aborted before the lanes are re-merged from the top, which is a no-op for the
ones already in. Recovery is the **only** path allowed to abort. A human retry
after a `merge_conflict` block finds the same in-progress merge and must
commit their resolution instead; the two are told apart by how the previous
attempt ended, read before the new attempt's row exists.

## 13. HTTP API

### 13.1 Transport and auth

- HTTP/1.1 + JSON on `127.0.0.1` only. No TLS in v1 (loopback).
- Every request requires `Authorization: Bearer {token}` where the token is read from
  `{data_dir}/token` (0600). This blocks other local users and drive-by browser
  requests (CORS is additionally disabled).
- Discovery: clients read `{data_dir}/daemon.json` for the port, then `GET /v1/health`.
- Versioning: path-prefixed (`/v1`); additive changes only within a version.
- Errors: `{"error": {"code": "task_not_found", "message": "…"}}` with proper HTTP
  status codes. Invalid state transitions return `409` with the current state.
- The envelope carries an optional `details` object for values a client must branch
  on rather than parse out of prose. A `409` from a state conflict sets
  `details.state` to the state actually found:
  `{"error": {"code": "invalid_state", "message": "task 7 is running, not queued",
  "details": {"state": "running"}}}`. `details` is omitted when empty, so responses
  that carry no structured detail are unchanged.

*Amended 2026-08-25 (issue #140).* A request body is **exactly one JSON document,
bounded, and labelled JSON** — three rules the transport applies before any endpoint
in §13.2 sees the body:

- **One document.** The body carries one JSON value, followed only by whitespace.
  Two concatenated documents, or a document followed by anything else, are `400`
  `invalid_json`; the second document is never acted on and never silently
  discarded. (Discarding it is what happened before this amendment: the decoder
  stopped at the end of the first value and the request was answered `2xx`.)
- **Bounded.** A body is read up to a fixed limit and no further: **64 KiB** for an
  ordinary request, and **4 MiB** for the routes that legitimately carry a workflow
  source or an agent prompt (`POST /v1/tasks`, `retry`/`repair`/`answer` on a task,
  `POST /v1/resolve`, `POST /v1/workflows/validate`). Over the bound is
  `413` `payload_too_large`, naming the limit and never echoing the body. Fixed, not
  configurable — the same treatment §5.2 gives a workflow source, and for the same
  reason. Individual fields are bounded too (title, description, `fields` and
  `answers` keys, values and entry counts, prompt and run overrides, names and
  branch names); over a field bound is `400` `validation_failed` naming the field
  and the limit.
- **Labelled JSON, leniently.** A body with no `Content-Type`, or any `*/json` or
  `*+json` type with any parameters, is accepted. A non-empty body labelled a
  clearly non-JSON type — `text/html`, or the `application/x-www-form-urlencoded`
  a plain `curl -d` sends — is `415` `unsupported_media_type`.

`POST /v1/workflows/validate` bounds its `yaml` at §5.2's 1 MiB source limit, the
same artifact under the same bound wherever it enters the daemon; a source of
exactly the limit still validates.

The server also bounds how long a request may take to arrive: a read-header
timeout, a whole-request read timeout, and an idle timeout on kept-alive
connections. There is deliberately **no write timeout** — §13.3's streams are
long-lived by contract and a server-wide write deadline would sever every one of
them. The read deadline covers reading the *request*, so it does not shorten an
SSE response.

### 13.2 Endpoints

```
GET    /v1/health                       liveness (also unauthenticated) → { status, version }
GET    /v1/info                         daemon version, uptime, agent availability, caps in effect,
                                        and `orphans`: how many data-root directories no task
                                        claims right now (§10, task 005). Computed per request
                                        from a readdir plus the id queries — no size walk, no git —
                                        so it is cheap and never stale after a gc run. It is here
                                        and not on /v1/health deliberately: health is
                                        {status, version} and is the one unauthenticated endpoint
                                        (§13.1); the shape of a user's disk does not belong on it
GET    /v1/config                       effective global config (read-only)
GET    /v1/agents                       per-adapter availability + model/effort options (§9.6);
                                        ?refresh=true forces a re-probe
GET    /v1/doctor                       the whole §17 diagnostic in one body: paths, daemon,
                                        log (stat + tail), database (size, schema version,
                                        integrity_check), agents, storage (disk free, worktree
                                        count/bytes, orphans), tasks (counts by state), plus
                                        `problems[]` — the closed set that makes
                                        `vincent doctor` exit 1. Read-only. Agent availability
                                        is re-probed unconditionally (§9.6): auth state is not
                                        a function of the binary
POST   /v1/doctor/fix                   { force? } or ?force — runs gc's reclaim (§10) and
                                        compacts the database, then answers
                                        { actions[], report } with a report taken afterwards.
                                        A dirty orphan needs force; a non-directory is
                                        reported and never removed. VACUUM is **skipped**
                                        while any task holds a slot (§11) and says so, rather
                                        than stalling a step mid-write.
                                        A separate method from the GET on purpose: a call that
                                        deletes directories is a different promise from a
                                        report (task 005)
POST   /v1/daemon/stop                  graceful shutdown (§12.4); 202, then the daemon exits.
                                        `vincent daemon stop` calls this and waits for exit

GET    /v1/maintenance/orphans          what gc would consider, with sizes; removes nothing
                                        (§10, task 005) → { orphans[], mismatches[], bytes,
                                        reclaimed, reclaimed_bytes, dry_run, force }.
                                        Each orphan: { path, kind (worktree|transcript),
                                        task_id (null when the name is not an id), bytes,
                                        skip_reason?, error?, removed }. skip_reason is why gc
                                        declined (`worktree_dirty`, `dirty_unknown`,
                                        `not_a_directory`); error is a removal that was
                                        attempted and failed. mismatches[] are the reverse
                                        case — rows whose worktree_path is gone (§18) —
                                        report-only, no row modified
POST   /v1/maintenance/gc               { force?, dry_run? } → the same body, with `removed`
                                        set and the reclaimed totals filled in. force also
                                        removes a worktree git calls dirty, or cannot judge;
                                        dry_run returns the identical report and removes
                                        nothing. The totals count only what actually went, so
                                        a locked file is reported per path and the rest of the
                                        run continues

GET    /v1/projects                     list
POST   /v1/projects                     { path, name?, default_branch?, default_workflow?, max_parallel_tasks? }
GET    /v1/projects/{id}
PATCH  /v1/projects/{id}                any mutable field, incl. path re-pointing
DELETE /v1/projects/{id}                hard-deletes the project and its task history (rows);
                                        only when no non-archived tasks; ?force first archives
                                        them (worktrees force-removed; refused while any task
                                        is running). Before the cascade, every row it drops —
                                        archived ones too — loses its branch if that branch has
                                        no commits past its base (§10, task 008); best-effort,
                                        local only, never a remote

GET    /v1/workflows?project_id=        merged registry view: built-in + global + that project's
                                        (shadowing applied); each entry:
                                        { name, scope, project_id, file, description, fields[], steps[],
                                          platforms[]?, platform_supported, requires_input,
                                          includes[]?, errors[]?, warnings[]?, error? }
                                        fields is the ordered §8.1.2 declaration list; an empty
                                        list means the workflow publishes no task-input contract
                                        platform_supported is this daemon's own verdict on the
                                        entry's §8.1.1 restriction (task 010, added 2026-08-16);
                                        requires_input marks an entry whose §7.4 `require` steps
                                        leave their agent to the task, so the agent picked for a
                                        task must be one that can ask (task 013, added 2026-08-17);
                                        includes names the workflows this one splices in (§7.9,
                                        task 019, added 2026-08-19). Whether those names resolve
                                        is not answered here: it depends on the project's
                                        resolved view and becomes a 400 at task creation
GET    /v1/workflows/definition         one workflow's whole recursive structure, selected with
       ?name=&project_id=               the same §5.2 shadowing the list applies (task 017,
                                        added 2026-08-18):
                                        { name, scope, project_id, file, platforms[]?,
                                          platform_supported, requires_input, errors[]?,
                                          warnings[]?, error?, definition }
                                        definition is { name, description, platforms[]?, fields[],
                                        defaults, steps[] }, each step carrying every field its
                                        type uses plus nested `steps`, fan-out `lanes`, `merge`,
                                        guards and loop drivers. Steps are reported **as
                                        authored**: workflow defaults stay in their own block and
                                        are never folded into the steps that inherit them, so
                                        "this step sets `agent`" and "this step inherits it" stay
                                        distinguishable — the distinction §8.6 rests on. The
                                        resolved answer is `POST /v1/resolve`'s.
                                        The name travels in the query string because a registry
                                        name is neither URL-safe nor unique: an entry whose file
                                        does not parse is still listed, under a name that was
                                        never validated, and the loser of a duplicate name is
                                        listed beside the winner.
                                        A workflow that does not parse is a **200** with its
                                        findings and `definition: null`, the same way the list
                                        shows a broken file rather than hiding it; 404 means no
                                        entry of that name in that project's view at all
POST   /v1/workflows/validate           { yaml } → { valid, errors[], warnings[] }
POST   /v1/resolve                      { workflow, project_id?, agent?, model?, effort?,
                                          title?, fields?, base_branch?, branch_name? } →
                                        { workflow, steps[], branch } — §8.6 applied to every step
                                        plus, when project_id is given, the branch name this
                                        draft would get as { value, source, placeholder }.
                                        source is the winning level (default|config|project|task);
                                        placeholder means value carries a literal `<id>` because
                                        the task id does not exist yet — deliberately not a
                                        guess (task 001)
                                        under a candidate task-level override. Each agent
                                        step carries { value, source } per field, source being
                                        the winning level (step|task|workflow|adapter); non-agent
                                        steps keep their index with null fields. An empty value
                                        with source "adapter" means the adapter names no default
                                        of its own — the CLI decides at run time.
                                        Resolution is server-side only: clients report it,
                                        never re-derive it (§8.6).

GET    /v1/tasks?project_id=&state=&archived=&limit=&offset=&parent_id=&include_children=
                                        list rows additionally carry the §15 board fields:
                                        project_name, step_total, step_name, and cost_usd /
                                        input_tokens / output_tokens rolled up across every
                                        attempt (§17) — so a board renders without an N+1.
                                        These are list-only; GET /v1/tasks/{id} serves the
                                        same numbers per attempt in steps[].
                                        ?archived= defaults to false: archived tasks are
                                        excluded unless asked for (?archived=true → only
                                        archived, ?archived=all → both). state=archived
                                        still selects them explicitly.
                                        Every task shape (list and detail) additionally
                                        carries admit_not_before (RFC3339 or null) and
                                        queued_reason (task 003): a queued task waiting on
                                        something other than a slot, per §11. Both are null
                                        for every other task, so the pair is additive
                                        Amended 2026-08-17 (task 014): fan-out lanes are
                                        excluded by default — the list is the work someone
                                        asked for, and a 64-task tree would bury it.
                                        ?parent_id= lists one parent's lanes in merge order;
                                        ?include_children=true is the flat everything. Every
                                        task shape carries parent_task_id / lane_id /
                                        lane_order (null for a root), and GET /v1/tasks/{id}
                                        carries a `children` rollup — subtree counts by
                                        state plus the ids of blocked and awaiting-gate
                                        descendants — whenever the task has lanes at all.
                                        Derived per request from one recursive CTE, never
                                        stored: a counter would be a second truth that
                                        drifts from the rows it counts.
POST   /v1/tasks                        { project_id, workflow, title, description?, fields?,
                                          base_branch?, branch_name?, priority?, agent?,
                                          model?, effort? }
                                        branch_name is used verbatim and wins over every
                                        template (§10, task 001)
                                        → task (state=queued); agent/model/effort form the
                                        task-level override (§8.6), validated per §8.2 —
                                        known-invalid = 400, catalog-unknown values are
                                        reported in `warnings[]` on the 201 body
                                        The selected root workflow's §8.1.2 declarations are
                                        validated before insert. Additional, undeclared field
                                        names remain accepted and are recorded on the task
GET    /v1/tasks/{id}                   full task incl. step runs summary and pending_input (§7.4).
                                        Every task representation carries `available_actions`
                                        (the §6 human actions valid right now) and
                                        `pause_requested`, so clients never restate the FSM.
                                        Detail-only: `workflow_steps[]` — the task's snapshot
                                        as { index, id, type, prompt?, run?, instructions?,
                                        resolved_from[]? }, which is what edit+retry prefills
                                        an editor with. It reflects edits made by a previous
                                        edit+retry, since the snapshot is this task's execution
                                        truth (§5.3). resolved_from is the chain of workflows a
                                        step was spliced through (§7.9, task 019, added
                                        2026-08-19), absent for a step the task's own workflow
                                        wrote
PATCH  /v1/tasks/{id}                   { priority }               (queued/paused only);
                                        emits task.priority_changed and re-runs admission
POST   /v1/tasks/{id}/cancel
POST   /v1/tasks/{id}/pause
POST   /v1/tasks/{id}/resume
POST   /v1/tasks/{id}/retry            { prompt_override?, run_override?, branch_override? }
                                        (blocked only). branch_override renames the task's
                                        branch before re-admission — the recovery path for a
                                        branch_exists block (§10, task 001); it is validated
                                        and collision-checked exactly as creation is, and
                                        unlike the other two it does not touch the snapshot
POST   /v1/tasks/{id}/repair           { prompt, agent?, model?, effort? }
                                        (blocked only; added 2026-08-24, task 025). Runs one
                                        ad-hoc agent in the task's existing worktree and
                                        branch (§6, §7.2). `prompt` is required and is
                                        **literal text**, never a text/template source — it is
                                        prose typed at a form, and the failure context around
                                        it is assembled by the daemon; an empty or
                                        whitespace-only prompt is a 400. The optional triple
                                        stands in for the step level of §8.6's chain for this
                                        one run and is validated exactly as creation validates
                                        a task's: an unregistered agent or a known-invalid
                                        model/effort is a 400, a value no catalog knows rides
                                        back in `warnings[]`. The response is the task (now
                                        queued) plus `warnings`. The repair returns the task
                                        to `blocked` at the same step with the same
                                        `block_reason` whatever the agent exits with
POST   /v1/tasks/{id}/follow_up        { prompt? | run? | workflow?, agent?, model?, effort? }
                                        (done/aborted only; added 2026-08-25, task 027). Runs
                                        one more piece of work in the task's existing worktree
                                        and branch (§6, §7.2). **Exactly one** of `prompt`
                                        (an agent run), `run` (a shell command, §8.3) and
                                        `workflow` (a name from the registry) is required:
                                        none says nothing to run, and two say two things with
                                        no rule for which wins — both are 400s. `prompt` and
                                        `run` are **literal text**, never text/template
                                        sources; the daemon escapes them when it compiles the
                                        one-step workflow it runs. A `workflow` name is
                                        resolved, §8.1.1 platform-checked, include-expanded
                                        (§7.9) and fan-out-resolved (§7.6, with the depth
                                        budget re-derived from this task's own depth) now, and
                                        stored as it will run — an unknown name, a workflow
                                        that does not validate here, or a tree past its bounds
                                        is a 400. The optional triple stands in for the step
                                        level of §8.6's chain for this run and is validated
                                        exactly as creation validates a task's: an
                                        unregistered agent or a known-invalid model/effort is
                                        a 400, a value no catalog knows rides back in
                                        `warnings[]`. The response is the task (now queued)
                                        plus `warnings`. The run returns the task to the state
                                        it came from — done to done, aborted to aborted —
                                        whatever it exits with
POST   /v1/tasks/{id}/skip             (blocked/awaiting_gate only)
POST   /v1/tasks/{id}/approve          (awaiting_gate only)
POST   /v1/tasks/{id}/reject           (awaiting_gate only)
POST   /v1/tasks/{id}/answer           { answers?, allow? }        (awaiting_input only, §7.4)
POST   /v1/tasks/{id}/archive          { force? } or ?force        (done/aborted only);
                                        the worktree is removed before the transition, so a
                                        dirty worktree without force is a 409 and the task
                                        stays done/aborted. The response is the task plus
                                        `branch: { name, result, error?, remote? }` — result is
                                        deleted | has_commits | unknown | error, remote is
                                        { remote, ref, result: deleted|no_upstream|error,
                                        error? } and rides only an opted-in remote leg. The
                                        whole object is **absent** when the branch step did not
                                        run, and never affects the status code: an archive is
                                        never failed by a branch problem (§10, task 008)

GET    /v1/tasks/{id}/steps             all StepRuns (every attempt)
                                        (task 015, 2026-08-18: each carries `skip_reason`
                                        — "condition" for a false `if:`, null for the human
                                        skip — and `state` may now be "stopped", §5.4/§7.7)
GET    /v1/tasks/{id}/steps/{run_id}/transcript?offset=&tail=&format=
                                        the attempt's JSONL transcript, ranged.
                                        `offset=` (bytes) and `tail=` (last N bytes) are
                                        mutually exclusive; `tail` opens at the start of the
                                        record its byte count lands in (so a window narrower
                                        than the last record still returns that record, never
                                        nothing), `offset` is taken as given. The body always
                                        ends on a complete line and `X-Next-Offset` reports that
                                        boundary — never mid-record, so a follow-up fetch on a
                                        file still being appended to resumes cleanly.
                                        `format=normalized` maps each line through the owning
                                        adapter's parser into the §13.3 live-output shapes plus
                                        `agent.result`, `agent.error`, the `vincent.*` kinds and
                                        `agent.raw` for anything the parser doesn't recognize —
                                        one render path for live tail and scrollback alike.
                                        Default (absent) is the raw file, byte for byte.
                                        **v0 wire change (T4.14):** `agent.tool_use` records
                                        carry `tools: [{name, summary, call_id}]`; through M4
                                        the field was `tools: []string`. Nothing durable broke
                                        — normalized records are computed from the raw file on
                                        every read and never stored, and live chunks are
                                        ephemeral — so the handler and the one in-tree client
                                        moved together rather than carrying two shapes.
                                        **T4.16** adds two record types: `agent.thinking`
                                        (`text`) and `agent.tool_result`
                                        (`results: [{call_id, name, summary, is_error}]`).
                                        Because normalization is re-run on read, enriching a
                                        parser improves transcripts **already on disk** — the
                                        reasoning in a run recorded last week renders today.
GET    /v1/tasks/{id}/diff              unified diff of worktree vs merge-base with base branch
                                        (includes uncommitted changes)

GET    /v1/events                       SSE stream (§13.3)
GET    /v1/tasks/{id}/events            SSE, single task incl. live output
```

### 13.3 Events (SSE)

Two kinds of streams:

1. **State events** — durable. Persisted to the `events` table with a monotonic id,
   emitted as SSE with `id:` set, so clients reconnect with `Last-Event-ID` and miss
   nothing. Types:
   `task.created`, `task.state_changed`, `task.priority_changed`, `task.step_advanced`,
   `task.children_changed`, `project.*`, `workflow.registry_changed`,
   `agent.quota_changed`, `daemon.shutting_down`.
   (`agent.quota_changed` — *added 2026-08-24, task 026* — carries
   `{agent, spent, resets_at, source}` and, like `workflow.registry_changed`,
   no `task_id` and no `project_id`: the fact is about an adapter, not about
   any one task. It is appended when the §14 `agent_quota` upsert actually
   changed a value or the clear actually deleted one — **never** on a
   re-observation identical to what is stored, so a client that refetches on it
   is not woken by news it already has, and never merely because a window
   lapsed. Reusing `task.state_changed` was beaten because it makes every
   client re-derive "a task hold implies an agent-level fact", which is the
   kind of inference the daemon publishes rather than delegates.
   `scheduler.WakeOn` is **false** for it: nothing about admission changes.)
   (`task.children_changed` — *added 2026-08-17, task 014* — carries
   `{task_id, child_id, to_state}` and is emitted on **every** fan-out ancestor
   when a descendant is created or transitions, so a client re-fetches the
   §13.2 rollup. It exists because the per-task stream filters on `task_id`
   alone: a root's stream would otherwise never see a depth-2 transition. The
   alternative — widening that filter to a subtree test — fails because the
   subtree is not fixed at subscribe time, since children appear as fan-outs
   fire. The cost is bounded: at most `max_depth` extra rows per transition.)
   (`step.started`, `step.finished`, `step.retrying` and `gate.waiting` were listed
   here through M2 but were never emitted — PR D completed the vocabulary without
   them, since a step's lifecycle is reconstructable from `GET /v1/tasks/{id}/steps`
   and the per-task stream. `task.step_advanced` — PR I decision — is the one piece
   that was not: it carries `{ current_step }` when the engine moves the cursor
   without a state change, so a board's `k/n` tracks a run instead of freezing at
   the step the task started on. It is emitted only when the cursor actually moves,
   never on a bare worktree-path write, and deliberately does not wake the scheduler:
   nothing about admission changes when a running task advances a step.)
   (An archive is visible as `task.state_changed` with `to: archived`; there is no
   separate `task.archived` type — PR D decision. Likewise there is no separate
   `task.awaiting_input` type — PR F decision: entering the state is
   `task.state_changed` with `to: awaiting_input`, whose payload additionally
   carries the request kind and a one-line summary — the full request comes from
   `GET /v1/tasks/{id}` (§7.4).)
   Payloads carry ids + the new state, not full objects (clients re-fetch as needed).
   `/v1/events` supports `?types=` and `?project_id=` filters. A connection without
   `Last-Event-ID` starts live at the next committed event — the stream never replays
   history unasked; state catch-up is a REST snapshot, then the stream.

2. **Live output** — ephemeral, high-volume. `agent.output`, `agent.tool_use`,
   `agent.tool_result`, `agent.thinking` (T4.16),
   `agent.usage`, `command.output` chunks are streamed on the **per-task** stream only
   and are *not* written to the events table (they are durable in transcript files;
   catch-up = fetch the transcript, then follow live). Chunks are one SSE event each,
   flushed on a ~100 ms coalescing timer (~10 Hz); `Last-Event-ID` on the per-task
   stream resumes its durable events only — live output is not replayable.
   Every chunk carries `run_id` (the `step_runs` row that produced it) and `offset`
   (the byte position in that attempt's transcript file *after* its line was written;
   the write always precedes the publish). Together they make the catch-up seam exact:
   a client fetches the transcript, then discards buffered chunks whose `run_id`
   matches the attempt it fetched and whose `offset` is at or before the fetch's
   `X-Next-Offset`. `run_id` is load-bearing on its own — offsets restart at zero in
   every attempt's file, so a step advance or a retry mid-stream would otherwise have
   its output compared against a position in a different file.

## 14. Data model (SQLite)

```sql
CREATE TABLE projects (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT NOT NULL UNIQUE,
  path                TEXT NOT NULL,
  default_branch      TEXT NOT NULL,
  default_workflow    TEXT,
  max_parallel_tasks  INTEGER,                -- NULL = unlimited (global cap still applies)
  created_at          TEXT NOT NULL,          -- RFC3339 UTC throughout
  updated_at          TEXT NOT NULL
);

CREATE TABLE tasks (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id          INTEGER NOT NULL REFERENCES projects(id),
  title               TEXT NOT NULL,
  description         TEXT NOT NULL DEFAULT '',
  fields_json         TEXT NOT NULL DEFAULT '{}',
  workflow_name       TEXT NOT NULL,
  workflow_snapshot   TEXT NOT NULL,          -- full YAML at creation (incl. any edit+retry overrides)
  base_branch         TEXT NOT NULL,
  branch_name         TEXT NOT NULL,
  worktree_path       TEXT,
  priority            INTEGER NOT NULL DEFAULT 0,
  agent_override      TEXT,                   -- task-level selection (§8.6); NULL = none
  model_override      TEXT,
  effort_override     TEXT,
  state               TEXT NOT NULL,          -- §6
  current_step        INTEGER NOT NULL DEFAULT 0,
  block_reason        TEXT,                   -- set while state='blocked'
  pause_requested     INTEGER NOT NULL DEFAULT 0, -- §6 pause accepted, not yet taken effect
  retry_cursor_at     TEXT,                   -- last human `retry`; the retry budget counts failures after it (§7.2)
  pending_override_json TEXT,                 -- edit+retry text awaiting the next attempt's step_run
  pending_repair_json TEXT,                   -- ad-hoc repair request awaiting its admission (§6, task 025, migration 0010);
                                              -- drained by the transition that returns the task to blocked, not by the
                                              -- step_run insert — an interrupted repair must re-run as a repair (§12.4)
  pending_follow_up_json TEXT,                -- follow-up run awaiting or in flight (§6, task 027, migration 0012);
                                              -- carries the compiled workflow, the origin state, the round and the run's
                                              -- own step cursor. Survives the fail that blocks a follow-up step and the
                                              -- retry that re-runs it; dropped by any transition into a settled state
  pending_input_json  TEXT,                   -- normalized InputRequest while state='awaiting_input' (§7.4)
  admit_not_before    TEXT,                   -- §11 admission hold; NULL = admissible now (task 003)
  queued_reason       TEXT,                   -- why a queued task waits on more than a slot; NULL = the ordinary queue
  -- Fan-out lane link (§7.6, task 014, migration 0007). All NULL for a root
  -- task; set together for a lane. lane_order is the *declared* order, which
  -- is the order the join merges in — spawn order coincides only by luck.
  parent_task_id      INTEGER REFERENCES tasks(id),
  parent_step_index   INTEGER,                -- the fan_out step's index in the parent
  lane_id             TEXT,                   -- the lane's id in that step
  lane_order          INTEGER,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,
  archived_at         TEXT
);
CREATE INDEX idx_tasks_sched ON tasks(state, priority DESC, created_at);
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id, lane_order);  -- §7.6 subtree walks (task 014)

CREATE TABLE step_runs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id             INTEGER NOT NULL REFERENCES tasks(id),
  step_index          INTEGER NOT NULL,
  step_id             TEXT NOT NULL,
  step_type           TEXT NOT NULL,          -- agent | command | manual | condition | break | fan_out
  attempt             INTEGER NOT NULL,       -- 1-based, within the position below
  -- Where inside a `loop` step this row sits (§7.8, task 016, migration 0009).
  -- A loop's body steps share the loop's step_index and repeat, so step_id
  -- alone stops telling two rows apart; iteration is what does. 0 for every
  -- row outside a loop, which keeps pre-0009 rows correct without a backfill.
  -- The loop itself writes no row: its position and its outcome are derived
  -- from these.
  iteration           INTEGER NOT NULL DEFAULT 0, -- 1-based inside a loop; 0 outside one
  loop_item           TEXT,                   -- the `for_each` item this iteration ran on; NULL otherwise
  state               TEXT NOT NULL,          -- running | succeeded | failed | interrupted
                                              -- | approved | rejected | skipped | stopped
  agent               TEXT,                   -- adapter name, agent steps only
  model               TEXT,                   -- resolved model as passed to the adapter (§8.6)
  effort              TEXT,                   -- resolved effort as passed to the adapter (§8.6)
  pid                 INTEGER,                -- while running
  proc_started_at     TEXT,
  exit_code           INTEGER,
  check_exit_code     INTEGER,
  failure_reason      TEXT,
  skip_reason         TEXT,                   -- 'condition' for a false `if:` (§7.7); NULL for the human skip (§6)
  result_summary      TEXT,                   -- agent result text / command stdout tail
  prompt_override     TEXT,                   -- edit+retry: the prompt a human supplied for this attempt (§6)
  run_override        TEXT,                   -- edit+retry: the command a human supplied for this attempt (§6)
  transcript_path     TEXT,
  input_tokens        INTEGER,
  output_tokens       INTEGER,
  cost_usd            REAL,                   -- NULL when the agent doesn't report cost
  input_wait_ms       INTEGER NOT NULL DEFAULT 0, -- time spent awaiting_input (§7.4); excluded from durations
  started_at          TEXT NOT NULL,
  finished_at         TEXT
);
CREATE INDEX idx_step_runs_task ON step_runs(task_id, step_index, attempt);

CREATE TABLE events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- SSE Last-Event-ID cursor
  ts            TEXT NOT NULL,
  type          TEXT NOT NULL,
  task_id       INTEGER,
  project_id    INTEGER,
  payload_json  TEXT NOT NULL
);
CREATE INDEX idx_events_task ON events(task_id, id);

-- The daemon's last first-hand observation of an adapter's usage window
-- (task 026, added 2026-08-24, migration 0011). One row per adapter, not per
-- stop: this is current state, and current state is what every §15 surface
-- wants. History on step_runs was beaten because every read would then be a
-- scan-and-pick-latest per adapter; deriving it from held task rows with no
-- schema at all was beaten because the signal vanishes the instant the last
-- held task is admitted, which is exactly when the window is still shut.
CREATE TABLE agent_quota (
  agent              TEXT PRIMARY KEY,   -- adapter name, not a binary path
  observed_at        TEXT NOT NULL,      -- when the stop was seen
  resets_at          TEXT NOT NULL,      -- the effective reset the engine acted on
  resets_at_reported INTEGER NOT NULL,   -- 1 = the CLI named it; 0 = usage_limit_recheck_interval supplied it
  source             TEXT NOT NULL       -- 'observed'; the seam a probe would fill
);

CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
```

WAL mode, `busy_timeout` set, all writes through the daemon's single connection pool.
Migrations are embedded in the binary and applied at startup.

*Added 2026-08-14 (task 003).* `admit_not_before` / `queued_reason` carry no index:
`ListAdmissible` already returns the whole queued set in §11 order and the hold is
evaluated during the walk. Both are cleared by **any** transition out of `queued`,
in the transition itself rather than by each caller — the same construction that
makes "`pending_input_json` non-null iff `awaiting_input`" hold. `block_reason` was
deliberately not overloaded for this: §14 says it is set while `state='blocked'`,
clients key off it to mean exactly that, and a queued task carrying one would break
them.

*Added 2026-08-24 (task 026).* `agent_quota` carries no `used_percent` and no
`window` column. Both exist on the §9.6 wire as permanent nulls so clients are
written once against the final shape, but nothing can fill either, and a column
with no writer is dead schema in an append-only migration set. The upsert is
**monotonic** — an observation older than the stored one is discarded — so two
actors hitting the same wall in the same second cannot make the state go
backwards. The row is written by `internal/taskrun` alongside the §11 hold and
deleted by the next successful agent step on that adapter; the daemon remains
the single writer and the row is agent-scoped rather than task-scoped, so no
taskrun or scheduler ownership invariant moves.

## 15. TUI

Built with Bubble Tea. The TUI is a pure API client — it holds no state the daemon
doesn't have, and killing it never affects work. It subscribes to `/v1/events` and
re-renders on change; the task-detail view additionally subscribes to that task's
stream for the live tail.

### Views

1. **Board (home).** Table of tasks: id, project, title, state (color-coded), current
   step `k/n` + step name, elapsed, cost-so-far. *Elapsed here is wall clock from
   `started_at`* — §17's active-time rule (which excludes time spent `awaiting_input`)
   governs the per-step figures in the detail view, not this column: a task idle on a
   human for 35 of its 40 minutes must not read as "5m" on the board that is trying to
   flag it. Cost-so-far sums every attempt, retries included (§17). Filter by
   project/state; sort respects scheduler order for queued tasks. Header shows daemon status, agent
   availability, running/cap counts, and a needs-attention count. Tasks waiting on
   a human (`awaiting_input`, `awaiting_gate`, `blocked`) are pinned to the top
   with a distinct badge, and the TUI rings the terminal bell when a task enters
   `awaiting_input` — most terminals flash/badge the window even unfocused
   (§7.4). OS desktop notifications remain out of v1 (§20).
   **Grouped by default (task 009, added 2026-08-16):** the rows nest under group
   headers — projects, and the workflows of a project inside it — configured by
   `tui.board.group_by` (§12.3) and cycled for the session with `g`. See
   *Grouping* below.
   **Several tasks can be selected at once (task 011, added 2026-08-16):**
   `space` marks the row under the cursor, `V` marks everything the filter is
   showing, and while anything is marked the task-action keys act on the whole
   selection. See *Bulk selection* below.
2. **Task detail.** Step timeline (every attempt, with durations, tokens, cost);
   live output tail of the running step (follow mode); scrollback into full
   transcripts of past steps. Timeline and output are **side by side, both always
   visible** — selecting an attempt *is* how scrollback is navigated, so neither
   half can hide behind the other. The output side is a tabbed pane the diff
   joins. Attempt duration here is
   §17's active time (`finished_at - started_at - input_wait_ms`) with the excluded
   wait shown beside it rather than silently subtracted; this is deliberately not
   the board's wall-clock `elapsed`, because the per-step figure is diagnostic while
   the board's is an alarm. Follow mode is a property of the *live* attempt: it is
   unavailable on a finished one, and a step advance moves the selection only when
   the cursor was already on the live attempt. **Diff tab** (`GET …/diff`,
   syntax-highlighted, grouped by file and folded shut — see *Diff tab* below);
   action
   bar for exactly the actions valid in the current state (§6), including gate
   approve/reject with the rendered gate instructions, and edit+retry which opens
   `$EDITOR` on the failing step's prompt/command. When the task is
   `awaiting_input`, the pending question or permission request (options,
   multi-select, free-text entry) opens as a **popup**, and submitting the answer
   resumes the run in the same session (§7.4). It is a popup rather than a pane
   region because it is an interrupt, not a view of the task — the same reason
   the board pins those tasks and rings the bell. It **never steals focus**:
   auto-opening under a keystroke is how an answer gets lost, so it announces
   itself with a badge on the row and a footer hint, and the human opens it.

   **Repair popup (task 025, added 2026-08-24).** On a `blocked` task, `R` opens
   a second popup that owns the keyboard the way the answer form does: a
   required free-text prompt (`enter` edits it inline, `e` opens it in
   `$EDITOR`) and optional agent/model/effort rows fed by the same
   `GET /v1/agents` pickers the new-task flow uses (§8.6, with the request
   standing in for the step level). `ctrl+s` starts the repair, `esc` closes it
   and discards the draft. It is a popup and not an action key because a repair
   needs prose written for this one task — which is also why it is excluded from
   bulk actions.

   The detail timeline must render a repair's StepRun as **its own labeled
   entry** under the blocked step, never as another attempt of that step (§5.4):
   its row sits at that step's index under the reserved id `__repair`, and
   showing it as an attempt would tell the operator the opposite of what
   happened. It also does not make its index read as a `parallel` group, which
   is otherwise what more than one distinct step id at one index means.

   **Follow-up popup (task 027, added 2026-08-25).** On a `done` or `aborted`
   task, `F` opens a third popup of the same shape, with one row the others do
   not have: a **run-form chooser** above the text, because the three forms of
   §13.2's `follow_up` need choosing between and a key that had to guess between
   "prompt" and "shell command" would guess wrong half the time. The chooser
   decides what the row under it means — a prompt, a command, or a workflow
   picked from `GET /v1/workflows` — and the same agent/model/effort pickers
   follow. `ctrl+s` starts the run, `esc` closes and discards the draft. Like
   repair it is excluded from bulk actions (task 011): the input is written for
   one task, and the batch case is `vincent task follow-up` (§12.1).

   The detail timeline must render a follow-up round as **its own tier**, headed
   as a round rather than numbered as a step: its rows sit at
   `step_index >= step_total` (§5.4), and numbering round 1 of a four-step
   workflow "step 5" would say the workflow grew, which it did not (§5.3). A
   round's steps share one index and are named individually beneath that header,
   the way a `parallel` group's members are.
3. **New task.** Project picker → workflow picker (shows description + step list;
   flags steps whose agent is unavailable) → title → description (inline or
   `$EDITOR`) → fields → base branch (default prefilled) →
   priority → optional agent/model/effort override (pickers fed by
   `GET /v1/agents` with provenance-tagged options and free-text entry;
   replaces workflow defaults, never explicit step fields, §8.6) → create.
   **Workflow fields (task 022, added 2026-08-21):** selecting a workflow
   pre-renders its ordered §8.1.2 declarations with labels, descriptions,
   type/required badges, pattern help, and a boolean toggle. Declared names are
   locked but their values remain editable; additional custom key/value rows can
   still be added and deleted. Values survive workflow switches, and local
   feedback mirrors the daemon's authoritative create-time validation.
   **Pickers are windowed and type-filterable (M5, §9.7):** through v1 every
   catalog fit on a screen (claude: 3 models, 5 efforts; codex: efforts only),
   so the picker rendered all options unconditionally. Cursor's ~180-model
   catalog makes a viewport with a scroll indicator and incremental filtering
   mandatory; the flagging of unavailable agents grows a second reason —
   *installed but not authenticated* (`logged_in: false`, §9.5).
   **Guided wide layout (task 020, added 2026-08-20):** the same row order is
   grouped into six visual stages — Project, Workflow, Task details, Git &
   priority, Execution, Review. The stage is derived from the field cursor,
   not independently navigated; Review summarizes the whole request and the
   existing `ctrl+s` shortcut still submits from anywhere.
4. **Projects.** List/add/edit/remove; per-project cap and defaults. On a wide
   terminal the project list remains as a rail while the selected repository's
   configuration, execution defaults, current workload, or add/edit form uses
   the focused surface (task 020, added 2026-08-20).
5. **Workflows.** Merged registry with scope badges and validation status; `e` opens
   the file in `$EDITOR`; live reload reflects saves immediately. The view reads the
   registry, it does not author it: creating a workflow file from the TUI is out of
   v1 — new files are written in the editor and appear on the next reload. §19's M3
   acceptance loop says "author workflow"; it means this edit path.
   **A control-flow graph (task 017, added 2026-08-18):** `g` draws the entry under
   the cursor as a graph — sequence, `parallel` groups, `fan_out` lanes and their
   merge, guards, `condition`, `loop` and `break` — in a sub-layer over the list.
   See *Workflow graph* below.
   On a wide terminal the registry remains as a rail while the selected entry's
   provenance, availability and resolved steps use the focused surface; an open
   graph replaces that surface, not the rail (task 020, added 2026-08-20).
6. **Daemon.** Version, uptime, config in effect, adapters detected, recent daemon
   log, and — *added 2026-08-15 (task 005)* — the `orphans` count from `/v1/info`
   beside the words `vincent gc`, shown only when it is non-zero. It offers no way to
   run gc, for exactly the reason it offers no way to stop the daemon.
   The view reports, it does not act: stopping the daemon from the TUI is out
   of v1 — `vincent daemon stop` owns that, and a TUI that auto-started the daemon
   at launch has no business killing it. The log tail is read straight from
   `{data_dir}/logs/daemon.log`, the one place the TUI is not a pure API client:
   an endpoint cannot serve the log when the daemon is the thing that died, which
   is when the log is worth reading — so it is the one view with something true to
   show while disconnected. See *Disconnected* below for what the rest of the UI
   does in that state.

### Layout

The list above is a contract about **capabilities**, not about screens. Views 1
and 2 — the daily loop — are one persistent screen of three panels; views 3–6 are
full-screen takeovers reached from the command palette.

**Guided takeovers (task 020, added 2026-08-20).** At a terminal size of at
least **128 columns by 24 rows**, New task, Projects and Workflows use a
persistent navigation rail beside one focused work surface. Below either
dimension they use their compact single-column form, table or registry. A
resize changes composition only: it may not reset the field/resource cursor,
filter, open picker or form, expansion, or graph. The split introduces no new
daemon state and no capability that exists only at one size.

```
┌─ Tasks ──────────────────────────────────────────────┐
│  #12  api    add rate limiting   running   3/5  …    │   ← always full width
│  #13  web    fix flaky test      ● gate    2/4  …    │
└──────────────────────────────────────────────────────┘
┌─ Timeline ───────────┐┌─ Output │ Diff ──────────────┐
│  1 ✓ plan      1m2s  ││  … live tail …               │
│  2 ▸ implement 4m9s  ││                              │
└──────────────────────┘└──────────────────────────────┘
 tab focus · enter open · / filter        : commands  ? help  q quit
```

The task table keeps the full §15 column set at full width. A narrow left rail
would have to drop most of it, and cost-so-far and step `k/n` are the two columns
a human scans to decide where to intervene.

**Two kinds of `queued` (task 003, added 2026-08-14).** A task waiting on an
agent's usage window and a task waiting for a free slot are both `queued`, and
conflating them is what the reason exists to prevent. The **board's** state cell
renders the resume time — `queued → 14:20` — for a task carrying an
`admit_not_before`; the reason itself does not fit the state column's width, and
widening it for a rare state would cost every board the columns that get shed
first. The **detail header**, which has the room, renders the full
`queued · usage limit → 14:20`. Band ordering is unchanged: a held task stays in
the queued band, in normal §11 order, because that is where it will run from.

**The agent's window, not just the task's (task 026, added 2026-08-24).** The
observation behind that hold is recorded per adapter (§14) and published on
change (§13.3), which gives three surfaces something the task rows cannot say:

- **Board header.** The per-agent summary grows a third badge beside `✓ / ⚠ /
  ✗`: `claude ⏳14:20` for an adapter that is installed, authenticated, and out
  of quota until a stated time. It ranks below `✗` (missing) and `⚠` (not
  logged in) because it is temporary and self-clearing, and above `✓` because a
  tick there is the wrong answer to "why is nothing running". The state column
  is untouched — the constraint above still holds; this is the header line,
  which already carries a per-agent summary. The board refetches `/v1/info` on
  `agent.quota_changed`, and derives "still shut" from `resets_at` against its
  own clock rather than from the wire's `spent`, because nothing is emitted
  when a window merely lapses.
- **Daemon view, agents panel.** The reset beside path, version and login
  state, with the §9.6 provenance made visible: `usage limit → 14:20` for a
  reset the CLI stated, `usage limit ≈ 14:20` for one
  `usage_limit_recheck_interval` supplied. This is the one surface that renders
  **"unknown" out loud** — a trailing `quota unknown` for an adapter nothing
  has been observed for — because listing every fact about an adapter,
  including the ones nobody has, is what this view is for. The board, which has
  no room to explain, says nothing instead.
- **New-task form.** An advisory under the agent row, in the shape the workflow
  row already uses for `· needs an interactive agent`: `· usage limit until
  14:20`, naming the adapter only when the draft resolves to more than one. It
  **warns and submits** — task 003's decision 4 (no pre-flight refusal) stands,
  and admission is untouched.

The task detail header needs nothing: task 003's amendment above already gives
it `queued · usage limit → 14:20`.

**Grouping (task 009, added 2026-08-16).** The task table nests its rows under
group headers, `[project, workflow]` by default: a board with more than one
repository on it is read project by project, and within one project the workflow
is what says what a task is *doing*. It is configuration — `tui.board.group_by`,
served on `GET /v1/config` — because the shape that suits three projects and one
workflow is not the shape that suits one project and six; `[]` is the flat table
of every earlier version, and `g` cycles project›workflow → project → workflow →
flat for the session without writing to the file. The rules the grouping does not
get to bend:

- **Ordering is untouched.** The tasks are sorted by band exactly as before and
  the groups take the order of their first task, so a group holding work that
  needs a human is the first group, and §15's pinning rule survives grouping.
  A header carries its task count and, when it has any, the attention badge and
  count — a header must never be the reason someone missed a task that is
  waiting.
- **A grouped level costs no column.** The header names it, so `PROJECT` and
  `WORKFLOW` drop out of the column set (the §15 shedding order is otherwise
  unchanged) and the width goes to the title, which is where a grouped board
  needs it — the titles are indented under their headers.
- **A header is a label, not a row.** The cursor steps over headers in the
  direction it was travelling, clicking one selects nothing, and nothing folds
  away: a board whose whole job is showing you every task has no business
  hiding rows behind a collapse.
- **The panel title names the grouping only when it is not the configured one**,
  the same rule the output pane's `v` follows.

**Bulk selection (task 011, added 2026-08-16).** Triage arrives in batches — a
sweep of finished tasks to archive, a run of queued ones to cancel after a bad
workflow edit — and one row at a time that is the same keypress N times with a
confirmation between each, which is where a human either stops tidying up or
stops reading the confirmations. `space` marks the row under the cursor, `V`
marks (and unmarks) everything the filter is showing, `esc` clears the
selection, and while anything is marked the §6 action keys act on the marked
tasks instead of the cursor row. The rules:

- **The selection is a set of tasks, not of rows.** It survives a filter, a `g`
  regroup and a refresh, because all three are ways of *looking* at the board;
  narrowing it to what is visible would mean typing a filter silently changes
  what a confirmed archive destroys. The panel title carries the count
  (`Tasks — 5 selected`), which is what keeps a marked task the filter is hiding
  honest, and a marker column — one cell, present only while something is marked
  — carries the per-row glyph.
- **An action is offered when *some* marked task offers it, and the key carries
  the count**: `A archive (7)` on nine marked rows. Requiring all of them would
  make `archive` vanish from a sweep of finished work because one task in it is
  still running, with nothing on screen to explain the absence. Tasks that do not
  offer the action are left alone; the invariant is "an action that cannot happen
  is not on screen", and one that can happen to seven of nine can happen.
- **One confirmation for the batch, one call per task.** There is no bulk
  endpoint: §6 lives in the API and the TUI is one of three clients, so the
  daemon still sees an ordinary action per task, sent sequentially in board
  order. `force` stays the dirty confirmation — a bulk archive archives the clean
  worktrees and re-asks about exactly the refusals ("2 of 5 selected tasks have
  uncommitted changes") — and the batch reports one line: `archive · 5 of 7`,
  the branch cleanup, and the first refusal named.
- **The keys work from any panel**, since the footer counts the selection
  wherever the focus is, and **what the daemon accepted leaves the selection**
  while refusals stay marked, so a retry needs no re-selection.

**Diff tab, grouped by file (task 012, added 2026-08-17).** The tab used to
render `git diff` as it arrives — one stream of lines, the first file's hunks
filling the pane — which answers "what is in this file" before it answers the
question the tab is opened with: *what did this task touch, and which file do I
read first?* So the diff is parsed into per-file sections and rendered as a
**list of foldable file rows**, each carrying its path and its added/removed
counts, with a summary line above it (`6 files  +128 -33`) pinned outside the
scroll. **Every file starts collapsed**, `enter`/`space` (and `→`/`←`) folds the
one under the cursor, `↑`/`↓` move between files, and `O`/`C` expand and
collapse the lot. The rules:

- **↑/↓ belong to the file list, not to the lines.** On this tab they move the
  cursor between files; line-level scrolling is the pager keys (`pgup`/`pgdn`,
  `f`/`b`, `u`) and the wheel. With everything folded there is nothing to
  scroll, and a tab whose ↑/↓ did nothing until you opened a file would be a
  dead key on the screen it opens with.
- **The diff tab is its own footer surface.** The two tabs of one pane answer to
  different keys — follow and verbosity against folds and file navigation — so
  `bindings.go` gives the diff its own context, and the footer, `?` and the
  palette follow the live tab. `]` is repeated in it, because the way back must
  stay on screen.
- **Folds are keyed by path and survive a refresh.** Re-entering the tab
  re-fetches (the endpoint runs git per call), and a file folding shut under the
  reader because the agent touched a different one is the failure this avoids.
  They are *not* carried to another task: another task's files are not these
  files. Fold state is never persisted — it is a way of looking at one diff, not
  configuration, which is what separates it from `tui.board.group_by`.
- **A file's header replaces the four lines that repeated it.** `diff --git`,
  `index`, `---` and `+++` are dropped from the body — the row above says the
  file — while a mode change, a rename or `Binary files … differ` stays, because
  the body is the last place those can be read. Nothing else is reinterpreted:
  what a file expands to is git's own lines.
- **A binary file says `binary` where the counts go.** Its ± counts are both
  zero, and `+0 -0` reads as "unchanged" rather than "not a text diff". The same
  reason keeps the summary line's counts off a change made only of renames and
  mode bits.
- **The line cap is unchanged** (5000 lines, §18): it bounds the terminal, not
  the truth. It now cuts the *parse* as well, so a truncated diff's last file
  shows the counts of the part that arrived and the notice still says the whole
  change is on the branch.

**The focused panel expands; the others collapse** to their title bar plus the
selected line. The task table never collapses below 5 rows — it is the navigation
spine, and a spine you cannot see is a modal round-trip wearing a border.

Terminal size is a stated floor, not a hope. Below **80×20** the shell drops to
single-panel mode: the focused panel alone, full screen, `tab` swapping which.
Below **60×15** it renders the size it has and the size it needs, and nothing
else. A layout that silently becomes illegible is worse than one that says so, and
a floor is testable where "looks cramped" is not.

**The output pane's line model (T4.16).** Every record renders as a two-column
**gutter** plus its content: assistant prose gets a blank gutter and sits flush
left, reasoning is `· `, a tool call `▸ `, and its outcome is indented under it
with `✓ `/`✗ `. What the agent *says* is unmarked and what it *does* is glyphed
— a scheme a monochrome terminal or an SSH session loses nothing to, which
colour alone would not survive. An assistant message following anything else
gets a blank line before it, which is what separates one turn from the next
without spending a column on it. There are **no timestamps**: on an 80-column
pane they would cost nine columns of every line to answer a question the
timeline panel already answers per attempt.

The pane **wraps its own lines**, with a hanging indent the width of the
gutter, rather than setting the viewport's soft wrap. Two reasons, and the
first is a defect this fixes: the viewport never enabled wrapping at all, so a
paragraph of assistant text was **clipped at the pane width** and the rest was
unreachable. Soft wrap would fix the clipping and fold every continuation to
column 0, where a wrapped line of reasoning is indistinguishable from assistant
prose — destroying the one distinction the gutter exists to make. Plain text is
wrapped first and each resulting line styled after, so no ANSI-aware wrapping
is involved and no escape sequence is ever split by a break.

A run's terminal `agent.result` shows its **outcome**, not its text: every
dialect's result text repeats assistant messages already on screen, and
cursor's is the entire turn concatenated. The text is kept when the attempt
rendered no output at all — a codex turn with no `agent_message` — and always
on an error, where it is the error and may be the only content there is.

Views 3–6 stay full-screen because they are forms and lists, not observations: the
new-task flow is eight fields with pickers, and squeezing it beside a live tail
serves neither. Takeovers are for surfaces you visit deliberately; popups are for
small things — the palette, confirmations, and the answer form.

### Workflow graph

*Added 2026-08-18 (task 017).* `g` on the workflows screen draws the selected
entry's control flow. It is a **viewer**: nothing here creates, edits or deletes
a step, and `e` remains the way a workflow is changed.

It is a sub-layer over the list rather than a screen of its own. The list's
`enter` expansion stays as it was — it carries findings, platform notes and the
§8.6 resolution the graph does not show — and `Esc` closes one layer at a time:
an error note, then the graph, then the takeover.

What the picture says, all of it readable with every style stripped:

- **Nodes** are boxes: a name line and a line carrying the §8.2 type word and
  any badges. `if` marks a guard, `chk` a `check:` field, `×3`/`for_each` a
  loop's driver, `max N` an explicit bound, `agent` a merge that may be resolved
  by one. The badge says a thing exists; the inspector strip says what it is.
- **Frames** enclose structure, by weight: light for a `parallel` group, heavy
  for a `fan_out`, double for a `loop`. A fan_out's lanes are captioned with the
  lane id and its guard.
- **A `fan_out` has a merge node** below its frame, because the join is a git
  merge that runs and can block (§7.6). **A `parallel` group has none**: its
  join is its members finishing (§7.5).
- **Exactly one END** terminates the top-level sequence. A `condition` whose
  guard is false routes there — except inside a loop body, where false ends that
  *iteration* (§7.8) and routes to the loop header. A `break` routes to whatever
  follows the loop, never back to it.
- **A guard on an ordinary step draws no branch.** False means skip and carry
  on (§7.7), so the node carries an `if` badge and the flow is unchanged.
- **A lane naming another workflow is one collapsed node.** Opening it is
  navigation and is not in this version.

Selection is by node, and the viewport follows it: arrows or `hjkl` move the
selection, `shift`+arrows pan, `tab`/`shift+tab` walk source order, and the
pager keys page. A terminal too narrow to draw a node readably says so rather
than flattening the graph into a shape that is not true; a graph larger than
the terminal is cropped and panned, never reflowed.

`e` and `R` work inside the layer: `e` opens the graphed workflow's own file,
and a save redraws the graph in place through the same live reload the list
uses — the selected node survives it, because a node's identity is its step id
and not its position. `R` refetches the one definition, which is the layer's
recovery from a failed fetch. Nothing is cached: the registry changing is
exactly when someone is editing files in this view.

An entry that does not parse has no graph, and `g` says so instead of opening a
layer that would repeat the findings already on screen.

### Discovery

Three surfaces, one source. **`bindings.go` is the single registry** — every
binding declares its key, label, scope (global · panel · task-action) and priority,
and the palette, the footer and `?` all render from it. Hand-maintained parallel
lists drift within two PRs and the drift is invisible until a human presses a key
the help promised.

**`:` opens the command palette.** It lists, searchable by intent: the task
actions valid *right now*, navigation to views 3–6, and the focused panel's own
commands — each with its direct key beside it, so the palette teaches shortcuts
rather than replacing them. Invalid task actions are omitted rather than greyed,
holding the same invariant the action bar always had: an action that cannot happen
is not on screen. Navigation living here is what lets view-switching keys be
retired without substituting a different set to memorise.

**The footer is one line and never wraps.** Left to right: the focused panel's
keys (at most five, in registry priority order), then the task's
`available_actions`, then — pinned right and never truncated — `: commands`,
`? help`, `q quit`. Overflow truncates from the left with `…`. The pinned segment
is exempt because `:` is the escape hatch that makes every other key optional; a
narrow terminal dropping it would fail exactly when the human is most lost.

`?` remains, as a compact cheat sheet grouped by panel, rendered from the registry.

### Keys

Global: `:` palette · `?` help · `n` new task · `q` quit (the daemon keeps running;
a status line reminds of the running task count on exit) · `tab`/`shift+tab` move
focus between panels · `M` toggle mouse.

Task actions act on the selected task — or on the whole bulk selection when there
is one (task 011) — and are offered only when the daemon reports them in
`available_actions`: `p` pause/resume · `a` approve · `x` reject · `r`
retry · `R` repair · `s` skip · `E` edit+retry in `$EDITOR` · `c` cancel · `A`
archive · `F` follow up. `x`
rejects because `r` is taken; `r` doubles as *retry connecting* while disconnected,
where no task is reachable anyway. `R` (*added 2026-08-24, task 025*) opens the
repair popup rather than acting immediately, and is excluded from bulk actions
(task 011) — a repair needs a prompt written for one task. `F` (*added
2026-08-25, task 027*) opens the follow-up popup on the same terms and for the
same reason, and is likewise not a bulk action; the capital is free because `f`
is the panel-scoped follow-output key. Destructive actions confirm inline: `c` kills a
live process, `A` removes the worktree and a dirty one re-prompts for `force`.
`set priority` (§6) has no key — priority is chosen in the new-task flow.

Panel-local: `/` filters **whichever list has focus** — tasks, projects, workflows
— so one key means one thing everywhere. `g` cycles the task table's grouping
(task 009), taking the key from the table widget's undocumented go-to-top alias,
which `home` still is. `space` marks the row for a bulk action and `V` marks
every row the filter is showing (task 011); neither moves the cursor, because
marking a run of rows is the human's own `down` and auto-advancing would put an
unmarking mis-press on the wrong row. `enter` opens or expands. `[`/`]` switch
the output pane's tabs (`d` kept as an alias). `f`/`G` re-arm follow on a live
tail or the daemon log. `v` cycles how much of the output pane's records show —
compact → normal → verbose (T4.16): reasoning is hidden, truncated to its first
lines, then whole, and unrecognized lines expand out from behind their count at
verbose. One key rather than a toggle per record type, because "show me more" is
one intention; the level is **session state**, so switching task does not reset
what a reader chose to see, and the pane's title names any level but the default
— `v` is the one key here whose effect can be invisible, on a run that has no
reasoning and nothing unrecognized. `e` opens `$EDITOR` where a view has a file to edit. `R`
re-reads a registry or the daemon blocks. One key jumps to the next task needing a
human, surfaced in the footer only when that count is non-zero — the board has
always pinned and belled those tasks without offering any way to *go* to one.

**`esc` cancels one layer per press**, by a fixed stack: popup (palette,
confirmation, answer form) → takeover screen → bulk selection → active filter →
nothing. It is a
no-op at the bottom and it **never quits** — `esc`-to-exit surprises anyone who
pressed it meaning "back". "Back to the board" is not among its meanings any more,
because the board is always on screen.

A filter is view state, not a mode: `tab` **commits** it and moves focus, leaving
it applied and named in the panel title; only `esc` clears it. Losing a filter
because you glanced at the output pane is the kind of thing that trains people to
distrust a UI. The shell consults the **focused** panel for whether it is capturing
input, so global single-key bindings stay live everywhere else without leaking
keystrokes into a text field.

Deleting a project confirms inline, and a project holding non-archived tasks
re-prompts to archive them (the `?force` of `DELETE /v1/projects/{id}`) — but a
*running* task is refused outright, since no confirmation makes that delete legal.

In the daemon view, identity, config and adapters refresh on open and on `R`; the
log alone re-reads on a short timer, because it is the only part that changes while
you watch. Uptime ticks locally from the daemon's `started_at` rather than from a
fetched figure, so it cannot drift between refreshes.

### Mouse

On by default, `M` toggles it, and the toggle is in the palette. Click to focus a
panel, click a row to select it, wheel to scroll the focused panel, click a footer
hint to fire it, click a tab to switch it. No drag, no right-click.

Capturing the mouse costs native click-drag text selection. Every terminal has a
modifier override for it (shift-drag; option-drag on Terminal.app and iTerm) and
the toggle covers what is left — whereas shipping it off by default would make the
feature that exists for discoverability itself undiscoverable.

### Colour

A fixed palette: panel borders, a focus colour, and the existing state colours. It
degrades under `NO_COLOR` and on 16-colour terminals. No theme setting in v1 — that
is configuration surface, a docs section and a support burden for no acceptance
value.

### Disconnected

The panels stay on screen with their contents **marked stale**, behind a banner
saying the daemon is unreachable, with `r` to retry. Nothing force-navigates: the
last known task table is information as long as it is labelled as such, and
connect and reconnect are the same state, so a takeover on every blip would be
hostile. `:` still reaches the daemon view, which is the one surface with something
currently true to show (§15 view 6).

## 16. Security considerations

- **Trust boundary = the OS user.** API on loopback only + bearer token file (0600)
  so other local users can't drive the daemon. No TLS, no accounts in v1.
- **Full-auto agents are the headline risk.** In full-auto, an agent can execute
  arbitrary commands *as the user*, not confined to the worktree. Mitigations:
  per-workflow/step `restricted` mode, everything transcripted, nothing merges or
  pushes unless a workflow step does it. This risk is documented in the README and
  shown once in the TUI on first run. The acknowledgment persists in
  `{data_dir}/tui.json` and is written when the notice is *dismissed*, never when
  it is shown — a quit two seconds in must not bury it. Every failure reading or
  writing that file shows the notice again: a security warning that suppresses
  itself because a parse failed has failed in the wrong direction.
- Worktrees provide *collision* isolation, not *security* isolation.
- The daemon stores no secrets; agent CLIs use their own auth (keychain/config). The
  token file gates only the vincent API itself.
- **"Stores no secrets" is about vendor credentials, not about the user's own.**
  *Amended 2026-08-25 (#141).* vincent has no key store and no vendor
  credentials of its own, but `config.yaml` takes literal `environment.set`
  values (§12.3) and a user can reasonably put an API token there. That file and
  its directory are therefore owner-only on POSIX and re-tightened on every
  daemon start (§12.2). `environment.set` is still not a secret store: it is
  plaintext on disk, and inheriting a name from the surrounding environment is
  the better answer. A secret-provider design is out of scope here and tracked
  separately. Transcripts are the other place user-supplied sensitive data
  lands, and are `0600` for the same reason.
- Command steps and checks execute user-authored workflow content — same trust level
  as the user's own shell; no additional sandboxing is attempted or implied.
- **Adapter full-auto switches are all equivalent in blast radius**:
  `--dangerously-skip-permissions` (claude),
  `--dangerously-bypass-approvals-and-sandbox` (codex), `--force` (cursor).
  Cursor's reads mildest and is not; the first-run notice covers all three.
- **vincent writes to one CLI's own config**: a cursor step passes `--model`,
  which cursor persists to `~/.cursor/cli-config.json` (§9.7). It is not a
  secret and not an escalation, but it is the one place vincent mutates state
  outside its own data dir, so it is recorded here rather than discovered.

## 17. Observability

- **Per step:** duration (active time — time spent `awaiting_input` is tracked
  separately as input wait, §7.4), exit codes, tokens in/out, cost (when reported),
  full JSONL transcript on disk (agent events, command output, check output,
  input requests and answers).
- **Per task:** aggregate duration/tokens/cost across attempts (rolled up from
  step_runs; shown on board and detail views).
- **Daemon log:** structured (slog), rotated; scheduler decisions at debug level.
- **Retention:** transcripts of archived tasks pruned after
  `transcript_retention_days` (default 90); DB rows kept indefinitely (rows are small,
  history is valuable).
- **Entry point** (*added 2026-08-15, task 005*): `vincent doctor` and
  `GET /v1/doctor`. Everything above answers "what happened to this task"; the
  question that had no surface at all was "why is nothing running?", which took
  five — `daemon status`, reading `daemon.json` by hand, the TUI's daemon view
  for a log tail, `curl /v1/agents` with a hand-extracted token, and finding the
  config file yourself — and produced nothing pasteable into a bug report.
  Doctor answers it in one pass and adds the three rows nothing reported before:
  the **database's size, applied schema version and `PRAGMA integrity_check`**,
  the **disk free** under the data dir, and the **worktree count, total bytes
  and orphan count** (§10). Retention above prunes transcripts and never rows,
  so unbounded growth is a real outcome; `--fix` is what reclaims it, and both
  its writes are the daemon's.

  *Amended 2026-08-15 (task 005).* Retention is about **archived rows**: the pruner
  walks `archived_at`, so a transcript directory whose row was cascade-deleted with its
  project is reached by no retention pass, ever. That directory is `vincent gc`'s
  (§10), under the same claim rule as a worktree and with no dirty check — a transcript
  is vincent's own output, not a working tree. While the row exists, its transcripts
  stay the pruner's, archived or not.

## 18. Edge cases and errors

| Case | Behavior |
|---|---|
| Workflow file edited mid-task | Irrelevant — execution uses the task's snapshot |
| Workflow deleted before task creation | Creation fails: `workflow_not_found` |
| Agent CLI missing at step start | Step fails (retry policy applies) with a `agent_unavailable` reason; typically → blocked |
| A fan-out lane's merge conflicts | The join stops at that lane and the task blocks `merge_conflict`, with the worktree left conflicted so a human resolves in place (*added 2026-08-17, task 014*). `on_conflict: agent` tries a resolver first, gated by its `check`, and falls back to the same block. Archive gets no special case: a conflicted worktree is dirty by construction and §10 already refuses a dirty worktree without confirmation |
| A fan-out lane ends without finishing | The join blocks `lane_failed` and merges **nothing** — a partial merge is indistinguishable downstream from a complete one. `retry` re-checks the lanes; the remedy is to fix the child, which is an ordinary task (*added 2026-08-17, task 014*) |
| A workflow's includes are cyclic, unresolvable or too deep | Refused at task creation with a `400` naming the cycle path, the missing workflow, or `include.max_depth`. A callee bringing a step id the expansion already uses, and one whose `platforms:` excludes this host, are refused there too. Possible because expansion happens in the insert path (*added 2026-08-19, task 019*) |
| A fan-out tree is cyclic or too large | Refused at task creation with a `400` naming the cycle path or the bound crossed (`fan_out.max_depth`, `fan_out.max_tasks`). Possible because the whole tree's shape is static once lane lists are in the snapshot (*added 2026-08-17, task 014*) |
| Two sub-steps of a `parallel` group write the same file | Undefined: the group shares one worktree, and §10 isolates working trees between *tasks*, not processes within one. A workflow bug, documented as such rather than arbitrated (*added 2026-08-17, task 014*) |
| Option probe fails (help unparseable) | `GET /v1/agents` serves the curated catalog with `probe_error` set; selection and free text keep working (§9.6) |
| Model/effort unknown to the catalog | Validation warning only; the CLI is the final authority — a rejected value fails the step with the CLI's error (retry policy applies) |
| Model *in* the catalog but rejected at run time | Real, not hypothetical, on cursor (§9.7): the step fails with the stderr tail as the message, since no `result` event arrives. Catalog membership is advisory in both directions |
| Agent CLI installed but not authenticated | `logged_in: false` where the adapter can tell (§9.5); the new-task form flags it like an unavailable agent. Where it cannot (`null`), the step runs and fails. *Amended 2026-08-14 (task 003):* where the adapter recognizes the CLI's auth wording, that failure is now named `agent_unauthenticated` instead of surfacing as `nonzero_exit`/`agent_error`. Everything else about the row is unchanged and deliberately so — the step still runs, the attempt still fails, the §7.2 budget still applies, and the task still ends up blocked. There is no pre-flight refusal on `logged_in: false`. *Amended 2026-08-15 (task 005):* the "where it cannot (`null`)" set is now **claude alone** — codex probes `login status`, cursor probes `status` (§9.5). Every other clause of this row stands untouched, task 003 decision 4 included: making the state visible is not the same as blocking on it, and `vincent doctor` is where a user sees it before a task burns its retry budget |
| Agent stopped by a usage limit | *Added 2026-08-14 (task 003).* Where the adapter recognizes the wording, the attempt is recorded `interrupted` with reason `usage_limit`, consumes **no** retry (§7.2), and the task returns to `queued` with an admission hold (§11) — releasing its slot, so other work keeps running. The hold ends at the reset time the CLI reported, or `usage_limit_recheck_interval` after the stop when it reported none. Recovery is unattended: the scheduler re-admits and the step re-runs. The board says `queued` *with* its reason rather than `blocked` (§15). Where the adapter recognizes nothing — codex and cursor today (§9.1) — the run reads as `nonzero_exit`/`agent_error` exactly as before. *Amended 2026-08-24 (task 026):* the reset the engine acted on is additionally recorded per adapter (§14) and published on change (§13.3), so the fact outlives the hold — `admit_not_before` is cleared by the next transition out of `queued`, and until now the observation went with it. It is retired by the next successful agent step on that adapter, never by a timer |
| `effort` set on a step whose agent has no effort concept | Ignored by the adapter and documented as ignored (cursor, §9.7); a claude/codex effort value on a cursor step is already an §8.2 *error* — it belongs to another adapter's catalog |
| `restricted` step on an adapter that cannot restrict on this OS | Step fails to start with `restricted_unsupported` (cursor on Windows, §9.7), under the retry policy → typically blocked. Never downgraded to full-auto, and deliberately *not* `agent_unavailable`: the CLI is installed and healthy, so "not found" would send the user to reinstall what is already there |
| Step declaring `on_input: require` on an agent that cannot ask | *Added 2026-08-17 (task 013).* A workflow pinning an adapter with no control channel (codex, cursor) fails §8.2 validation outright. Otherwise creation is refused with a `400` naming the step and the agent, and the TUI's picker will not select that agent; `GET /v1/agents` publishes the `input_verdict` the gate uses. A task that reaches the engine anyway — claude upgraded past the §9.3 ceiling, a data directory moved — fails the attempt with `input_unsupported` under the §7.2 budget, before anything is spawned. Only a positive "cannot" refuses: an absent or unprobed binary is unknown, and unknown never blocks (§9.6) |
| Workflow restricted to platforms this host is not | *Added 2026-08-16 (task 010).* Creation is refused with a `400` naming the restriction and the host (§8.1.1); the entry stays listed and says why, and the TUI's picker will not select it. A task that *already* holds such a snapshot — the data directory moved to another OS, or the workflow narrowed after the task was queued — blocks at admission with `platform_unsupported`, before a worktree or any step. Not `invalid_snapshot`: the snapshot is valid, just not here |
| Runaway step output (agent or command) | Past `transcript_max_bytes` (§12.3) the process tree is killed and the attempt fails `transcript_limit`, under the retry policy. The line that trips the cap is written **whole** — a truncated line would turn a size failure into a parse failure for every later reader of the JSONL — and the partial transcript is kept with a closing `vincent.transcript_limit` annotation, because the lines that got there are what explain the runaway |
| A command emits a single line larger than one output record | *Added 2026-08-24 (#139).* Captured, not failed: the line becomes a run of `vincent.output` records marked `partial`, in order, on one stream, preserving phase, stream identity and live offsets. Minified JSON, a base64 blob and a `git diff` of a generated file all reach a megabyte on one line, so this is an ordinary command; failing it would only retry it into the same wall until the task blocked. It was previously a *silent success* — a line-bound reader stopped dead on the first such line, the rest of the stream went to `io.Discard`, and the attempt was judged from exit 0 alone |
| A transcript write, encode or close fails | *Added 2026-08-24 (#139).* The failure latches on the transcript and the attempt fails `transcript_io_error` under the §7.2 budget — disk full, a revoked permission, a short write, and ENOSPC surfaced at `Close`, which is where a buffered filesystem reports it. Never swallowed by `allow_failure:` (§7.2): vincent failing to record a step is not an outcome the step produced. Only a *success* is overridden — an attempt that already failed keeps the more useful reason. `transcript_max_bytes` is unaffected and stays the only size-based failure (§12.3) |
| An adapter cannot read its agent's stream to the end | *Added 2026-08-24 (#139).* The adapter latches its reader's error, drains the pipe so the CLI is not left blocked on it until the step timeout, and reports `agent.FailureStreamError`; the engine fails the attempt `agent_protocol_error` under the §7.2 budget. Deliberately not `agent_error`, which means "the CLI reported a failure" and would send a user to inspect a CLI that did nothing wrong — the reader that failed is vincent's. Deliberately not `input_protocol_error` either: that names a control message vincent could not render, and such a message arrived intact |
| Transcript of an archived task past retention | Deleted by the pruner at daemon start and every 24 h (§17). DB rows are never deleted; retention is measured from `archived_at`, so a long-running task archived yesterday is one day old. `transcript_retention_days: 0` disables pruning entirely |
| Base branch doesn't exist | Task creation fails fast |
| Branch already exists (or a ref hierarchy conflict blocks the name) | Rejected at creation with `400` where the name is known then; otherwise the task blocks with `branch_exists` at admission, which stays the authority. Never reused, never auto-renamed. Recover with `retry { branch_override }` (§10, task 001) |
| Configured branch name is not a legal git ref | `400` with `branch_name_invalid`, quoting git's own rules. Never sanitized into something legal — a branch the user did not ask for is worse than a rejection (task 001) |
| Branch template references a field the task does not set | `400` at creation. Note that `{{.Fields.x}}` errors while `{{ index .Fields "x" }}` renders empty by design (§8.4's `missingkey=error` covers map *field* access only), and `feat/-slug` is a legal ref — so the loud form is the documented default for branch templates |
| Archive-time branch delete fails | *Added 2026-08-16 (task 008).* Checked out in another worktree (`git branch -d` refuses), the base branch renamed away so the emptiness test cannot run, a remote that rejects the push or never answers inside `RemoteTimeout` — none of it fails the archive. The worktree is already gone and the task must still reach `archived`. It is logged, reported on the response as `error`/`unknown`, and the branch survives, which is the pre-008 behaviour. The remote leg cannot even be reached without a local delete that succeeded first |
| Worktree dir manually deleted | Next step fails → blocked with `worktree_missing`; retry recreates the worktree from the branch if it survives. *Amended 2026-08-15 (task 005):* the same mismatch found by a scan rather than by a step is **reported** — at daemon start and in `vincent gc`'s output — and no row is modified |
| Orphaned directory under a data root | *Added 2026-08-15 (task 005).* An entry under `{data_dir}/worktrees` or `{data_dir}/transcripts` that no task row claims — left by a project delete whose worktree removal failed (the cascade drops the rows regardless, §10) or by a crash between `git worktree add` and the claim write. Daemon start logs one warning per orphan and raises `orphans` on `GET /v1/info`; it **never** deletes, for the same reason DB corruption never auto-deletes. `vincent gc` reclaims them, and only them — archive remains the only path that removes a *task's* worktree |
| Dirtiness of an orphan cannot be determined | *Added 2026-08-15 (task 005).* An orphan's `.git` file points at `{repo}/.git/worktrees/{n}`, so a deleted or pruned repository makes `git status --porcelain` fail outright. Reported as `dirty_unknown` — distinct from `worktree_dirty`, because "git says you have local changes" and "nobody can tell what is in here" are different facts — and skipped until `vincent gc --force`. This is the *common* case where the projects really are gone, so a default run there reclaims little; that is the deliberate trade for never deleting work nobody can vouch for |
| Project path missing | New/step-starting tasks in that project → blocked with `project_path_missing` |
| Daemon port taken | Ephemeral port by default makes this nearly impossible; pinned-port conflict fails startup with a clear message |
| DB corruption | Startup fails loudly, points at the file, never auto-deletes |
| Agent emits gigabytes of output | Transcript writes are streamed to disk; SSE output chunks are rate-limited/coalesced (~10 Hz); per-run transcript size cap (`transcript_max_bytes`, default 512 MB) fails the step past the cap with `transcript_limit` |
| Template references missing field | Step fails at render time (before any process starts) with the template error |
| A step's `if:` does not render, or renders something that is not `true`/`false` | *Added 2026-08-18 (task 015).* The step blocks with `condition_error` and records one `failed` row naming it. The only reason in this table that does **not** run the §7.2 retry budget: a guard is evaluated before the step becomes an attempt, so there is no attempt to retry, and re-rendering an unchanged template cannot answer differently (§7.7). A human `retry` re-evaluates it |
| A guard skips a step | *Added 2026-08-18 (task 015).* A `skipped` row with `skip_reason: condition`, visible in `.Steps`; the workflow carries on. The same guard on a fan-out lane or a group sub-step subsets the set instead — the others still run |
| A `condition` step's guard is false | *Added 2026-08-18 (task 015).* The run ends there: one `stopped` row, the cursor moves to the end of the step list, the task is `done`. The steps after it record nothing, because they were never considered. *Amended 2026-08-18 (task 016):* inside a `loop` body the same step ends **that iteration** and the loop carries on — the sequence it ends is the body's (§7.8) |
| A `loop` cannot run within `max_iterations` | *Added 2026-08-18 (task 016).* The task blocks with `loop_limit`: a `for_each` list longer than the ceiling blocks before iteration 1 naming the count, and a `count:` the ceiling moved under (config lowered while the task was queued) blocks too. It does not truncate and does not advance — running out of tries is not a decision, and advancing would hand every downstream guard a `.Steps` that says the work is finished (§7.8) |
| A `break` step's guard is true | *Added 2026-08-18 (task 016).* The loop ends there and **succeeds**: one `stopped` row, the cursor advances past the loop step. A false guard records `succeeded` and the body carries on |
| A step's retry is paced by `retry_backoff` | *Added 2026-08-25 (task 028).* The attempt is recorded `failed` with its own reason and consumes a retry, and the task returns to `queued` with `queued_reason: retry_backoff` and an `admit_not_before` of `now + retry_backoff` (§7.2, §11) — releasing its slot, so other work keeps running. Recovery is unattended: the scheduler re-admits and the same step re-runs with the budget the recount says is left. Distinct from `usage_limit`, whose attempt is `interrupted` and costs nothing, so a reader can tell a quota wall from a flaky step. It never becomes a `block_reason`: when the budget *is* spent the task blocks with the step's own failure reason, with no wait first |
| A loop body step exhausts its retry budget | *Added 2026-08-18 (task 016).* The iteration fails and the task blocks with **that step's own** reason, not `loop_limit`. `allow_failure:` (§7.2) is how a probe's red result becomes data a `break` can read instead |
| The daemon dies mid-iteration | *Added 2026-08-18 (task 016).* §12.4 finalizes the running row as `interrupted`, and the re-admitted loop derives its position from the rows: body steps whose latest attempt succeeded are skipped, and it continues **mid-iteration**. Iterations that already have rows keep the `for_each` item those rows recorded; only new iterations draw from a re-derived list (§7.8) |
| Every lane of a `fan_out` is guarded off | *Added 2026-08-18 (task 015).* A no-op success: the step records a row saying no lane was selected and advances. It must not park — a parent in `awaiting_children` with no children would be re-queued, spawn nothing and park again (§7.6) |
| `answer` posted when task isn't `awaiting_input` | `409` with the current state (standard invalid-transition handling) |
| Agent process dies while `awaiting_input` | Attempt fails with its exit code (retry policy applies); `pending_input` cleared |
| `input_timeout` expires | Process killed; attempt fails with reason `input_timeout`; normal retry/blocked policy (§7.2) |
| Unparseable/unknown control request from an agent | Transcripted verbatim; attempt fails with `input_protocol_error` (retry policy applies) — vincent never waits on a request it can't render |
| Clock skew / DST | All timestamps stored UTC RFC3339 |

## 19. Milestones

| Milestone | Contents | Acceptance |
|---|---|---|
| **M1 — Spine** | Daemon skeleton, SQLite + migrations, config, token auth, projects CRUD, task creation with worktree (incl. optional agent/model/effort override), Claude adapter (model/effort passthrough + options probe), single hardcoded-format one-step run, transcripts, health/info | `curl` can register a repo, create a 1-step agent task, watch it finish, and see the branch/diff |
| **M2 — Workflow engine** | YAML registry (global+project, watch/validate/snapshot), all three step types, templates, checks, retry/blocked flow, gates, scheduler with both caps, pause/cancel/skip/edit+retry, SSE, crash recovery, Codex adapter, agent option catalog (`GET /v1/agents`) + §8.6 resolution/validation, agent input requests (`awaiting_input`, answer endpoint, `input_timeout`, `on_input`, §7.4) | Multi-step workflow incl. gate + command publish step runs unattended to the gate; an agent question round-trips awaiting_input → answer → resume; kill -9 of the daemon mid-step recovers correctly; caps honored under load |
| **M3 — TUI** | All six views, live tail, diff view, all actions, input-request alerts + answer form, `$EDITOR` integration, daemon auto-start | The full loop (register → author workflow\* → run 3 parallel tasks → answer an agent question → approve gate → archive) is doable without leaving the TUI |
| **M4 — Polish** | `service install` for all 3 OSes, CLI subcommands, retention pruning, docs, first-run experience, packaged releases (signed binaries†) | Fresh-machine install to first completed task in under 10 minutes on each OS |
| **M5 — Cursor adapter** (post-v1‡) | `internal/agent/cursor` (§9.7), fakeagent cursor dialect, config/registry wiring, picker viewport + filter, `logged_in` on the wire, docs | A workflow whose steps name `agent: cursor` runs unattended to completion against the real `cursor-agent`, on each OS |

\* **"author workflow" means editing in place,** not creating a file. §15 view 5
records that creating a workflow file from the TUI is out of v1: `e` opens an
existing entry in `$EDITOR` and the registry reload reflects the save, while new
files are written in the editor and appear on the next reload. The M3 acceptance
walkthrough exercises the edit path; it does not require a create path the TUI
deliberately does not have.

‡ **M5 is sequenced after M4, 2026-08-11** (Phase 5 grill session). Cursor
support is a feature, and M4's charter is polish; more concretely, T4.6's
ten-minute fresh-machine clock excludes agent-CLI installation as a documented
prerequisite, and folding a third CLI into that phase would either inflate the
prerequisite list or tempt the gate into measuring someone else's onboarding.
M5 was developed on a branch in parallel with M4 — it touches no file M4 owns
except `config.go`, `daemon.go`, and the README.

**Revised the same day, at the owner's direction:** M5's adapter work
(T5.1–T5.5) **merged into `master` ahead of M4** rather than waiting for it.
The *sequencing* rationale above still holds for the milestone — M5's gate
(T5.7) and its remaining docs (T5.6) come after M4 — but the code no longer
waits, so **v1's tree carries three adapters**. §2's goal is annotated
accordingly rather than restated: the two-adapter line describes what v1 was
scoped to deliver, not what the repository contains. T4.6's fresh-machine
clock is unaffected: cursor is not a prerequisite of the M4 walkthrough, and
an uninstalled `cursor-agent` is simply an unavailable adapter (§9.5).

† **"Signed binaries" is descoped, 2026-08-10** (Phase 4 grill session). Releases
carry cosign keyless signatures, checksums, and GitHub build attestations —
supply-chain verifiable without a certificate purchase. OS code signing
(Windows Authenticode, Apple notarization) is a recurring cost v1 does not take
on, so macOS Gatekeeper and Windows SmartScreen prompt on first launch; the
README documents that path, including `xattr -d com.apple.quarantine`. The M4
acceptance clock in T4.6 absorbs that friction deliberately — it is vincent's
own cost — while excluding agent-CLI installation and authentication, which is
a documented prerequisite of the walkthrough.

**Amended 2026-08-14 — macOS no longer meets Gatekeeper on the default path.**
`brew install lezli01/tap/vincent` is now the macOS install, and the cask's
`postflight` hook clears `com.apple.quarantine` during install. The descope
above is unchanged — the binaries are still not notarized, and a **downloaded
archive** still prompts, so the `xattr` instructions stay. What changed is which
path most macOS users take. Windows is untouched: no packager erases SmartScreen
the same way, so Scoop and winget stay rejected. Reasoning in
`docs/tasks/002-homebrew-tap.md`.

**Amended 2026-08-20 — package-manager distribution is now accepted on
Windows and Linux.** This explicitly supersedes the final two sentences above
and task 002's Windows-only rejection. Stable releases generate deb and rpm
assets, update `lezli01/scoop-bucket`, and submit `lezli01.Vincent` from
`lezli01/winget-pkgs` to Microsoft's public catalog. Prereleases may carry
deb/rpm assets but never move the Homebrew, Scoop, or WinGet stable channels.
mise uses the standard
`github:lezli01/vincent` backend over the existing archives and therefore adds
no repository or release-time publisher.

The maintenance cost named by the original X decision is accepted: the Scoop
bucket and WinGet fork are release dependencies with separate credentials.
Scoop is destination-scoped; WinGet's cross-owner catalog
pull request requires the explicitly documented classic-`public_repo`
exception. All formats consume the same GoReleaser build, checksums and release
tag. None changes the security boundary: binaries remain
without Authenticode/Apple notarization, package metadata preserves the
PolyForm Noncommercial license, and no root package script registers Vincent's
per-user service. External bootstrap and first-release proof are tracked in
`docs/tasks/021-package-distribution-channels.md`; documentation must not infer
catalog availability from a successful local manifest render.

**M4's acceptance is met, 2026-08-11.** The T4.6 walkthrough ran on a clean VM
per OS with no Go toolchain, against the `v0.1.0-rc1` artifacts: **5:00** on
Windows 11, **4:30** on macOS, **3:35** on Linux — every run under half the
ten-minute budget, and the slowest is the OS carrying SmartScreen, which prices
the † descoping at roughly its gap to Linux. Details in tasks.md T4.6.

## 20. Future work (explicitly out of v1)

- Web UI on the same API; auth story for non-loopback exposure.
- OS desktop notifications (blocked / gate / awaiting input / done) — natural M4+1.
- ~~More adapters~~ — **Cursor promoted out of future work to M5, 2026-08-11**
  (§9.7). Gemini CLI, opencode, and adapter capability flags remain here.
- ~~parallel steps and step fan-out~~ — **promoted out of future work,
  2026-08-17** (§7.5, §7.6, task 014).
- ~~workflow branching/conditionals~~ — **promoted out of future work,
  2026-08-18** (§7.7, task 015): `if:` guards on steps, lanes and group
  sub-steps, `type: condition` for early finish, and `allow_failure:` so a
  guard has a run's own findings to read.
- ~~loops in workflows~~ — **promoted out of future work, 2026-08-18** (§7.8,
  task 016): `type: loop` with `count:` and `for_each:`, `type: break`, and
  `.Loop`. This was task 015's named trigger firing — "the first workflow that
  cannot be written flat" — and a loop body was affordable where `branch` is
  not, because a loop has one arm: the step list stays a list and
  `current_step` stays an integer.
- ~~reusable workflows / including one workflow in another~~ — **promoted out
  of future work, 2026-08-19** (§7.9, task 019): `type: include`, spliced into
  the caller's snapshot at task creation. Splicing rather than nesting is what
  kept it affordable: the step list stays a list, `current_step` stays an
  integer, and a callee may contain a `loop`, a `parallel` or a `fan_out`
  because those land at the caller's own level. Deliberately *not* included:
  per-call parameters. Two calls with no arguments are the same call, which is
  why a duplicate id is refused outright; the trigger for both is the first
  workflow that must include one callee twice with different values.
- **`branch`/`switch` step types** with `then:`/`else:` bodies, which would
  make the step list a tree and the §7 cursor something other than an integer.
  Deferred by task 015 decision 1 and still deferred: §7.7's guards plus
  §7.8's loop cover the shapes that have come up. The trigger is a workflow
  needing two *different* bodies chosen at run time, which no guard-and-skip
  spelling can express without duplicating every step of both.
- **Dynamic per-item fan-out** — plausibly `for_each:` on a `fan_out` step, one
  child task and branch per item. Kept apart from §7.8 deliberately (task 016
  decision 4): §7.6's creation-time cycle, `max_depth` and `max_tasks` checks
  are possible **only** because the lane list is static in the snapshot, and
  015 decision 11 already had to weaken them into a conservative
  over-approximation to allow *conditional* lanes. A width discovered at run
  time leaves nothing to check at creation. The trigger is an answer to "what
  replaces the creation-time bound".
- **A template FuncMap for §8.4** — `hasSuffix`, `contains`, `split`, `trim`,
  `default`. `text/template` builtins are all any template gets today, which
  `for_each:` makes felt: `.Loop.Item` is a string authors immediately want to
  test by extension or path segment, and the answer is to filter at the source
  (`git diff --name-only … | grep -v _test.go`). A FuncMap lands in *every*
  prompt, check, run and guard at once and invites the expression-language
  argument 015 decision 4 settled, so it earns its own task. The trigger is
  the first `for_each` that cannot filter at its source.
- LLM-as-judge verification as an optional third success layer.
- Multi-user / remote daemons / fleet view across hosts.
- Task templates & recurring tasks; issue-tracker ingestion (Jira → task).
- Container/VM-sandboxed step execution.
