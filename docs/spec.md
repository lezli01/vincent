# vincent — Local AI Workload Orchestrator

**Status:** Draft v1 · **Date:** 2026-08-06 · **Owner:** László Szabó

vincent is a single-user, local-first facade for AI engineers: one central place to
monitor and manage many AI coding-agent workloads on a host. A background daemon
(`vincentd` role of the `vincent` binary) owns all state and execution; clients (a TUI
in v1, a web UI later) are thin, disposable views over the daemon's HTTP API. Work
continues even when no client is attached.

---

## 1. Overview

An engineer registers any number of local git repositories ("projects"), authors
reusable **workflows** (ordered steps: agent prompts, shell commands, manual gates),
and creates **tasks** against a project. Each task selects a workflow and executes its
steps sequentially inside a dedicated **git worktree**, so parallel tasks never
collide. Agent steps run locally installed agent CLIs (Claude Code and Codex in v1)
headlessly. The daemon schedules tasks under configurable global and per-project
concurrency caps, records full transcripts and run metrics, streams live progress over
SSE, and pauses for human input when a step fails, a manual gate is reached, or a
running agent asks a structured question (§7.4).

**Nothing about delivery is hardcoded.** Whether a finished task pushes a branch,
opens a PR, or just leaves a diff for review is entirely determined by the workflow's
steps.

## 2. Goals and non-goals

### Goals (v1)

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

### Non-goals (v1) — explicitly deferred

- Web UI (the API is designed for it; it is not built in v1).
- Multi-user / remote access / multi-host orchestration.
- OS desktop notifications.
- LLM-as-judge step verification.
- Workflow branching, conditionals, or parallel steps within one task.
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
| 12 | Step types | `agent`, `command`, `manual` (gate) |
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

- **Built-in:** shipped in the binary; currently only `adhoc`, the single-step agent
  workflow used when a task is created without naming one (§5.3). Lowest precedence —
  a global or project file of the same name shadows it.
- **Global:** `{config_dir}/workflows/*.yaml` — available to every project.
- **Project:** `{repo}/.vincent/workflows/*.yaml` — available to that project only,
  git-versioned and shareable with a team. A project workflow **shadows** a global
  workflow with the same name.

The daemon watches both locations (fsnotify) and reloads on change. Invalid files are
surfaced as registry errors (visible in TUI/API) without breaking valid ones.

### 5.3 Task

A unit of work delivered by running a workflow against a project.

| Field | Notes |
|---|---|
| `id` | integer, auto-increment; used in branch and worktree names |
| `project_id` | FK |
| `title` | short summary; slugged into the branch name |
| `description` | markdown, arbitrary length |
| `fields` | free-form string key/value map (e.g. `ticket: OPS-123`); available to templates |
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

### 5.4 StepRun

One attempt at executing one step of one task. Every attempt (including retries and
re-runs after interruption) is a distinct StepRun row — history is append-only.

Records: step id/index/type, attempt number, state (`running`, `succeeded`, `failed`,
`interrupted`, `approved`, `rejected`, `skipped`), timestamps, agent/model/effort used (as resolved per §8.6), exit code,
check exit code, failure reason, transcript file path, input/output tokens, cost (USD,
nullable — not all agents report cost), input wait time (ms spent in `awaiting_input`,
§7.4 — excluded from duration metrics).

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

### States

| State | Meaning | Consumes a concurrency slot? |
|---|---|---|
| `queued` | Ready to run; waiting for scheduler admission | no |
| `running` | A step process is executing (or about to) | **yes** |
| `awaiting_gate` | Paused at a `manual` step, waiting for approval | no |
| `awaiting_input` | The running agent emitted a structured input request (§7.4); its live process is idle, waiting for the answer | **yes** |
| `blocked` | A step failed and retries are exhausted; waiting for a human decision | no |
| `paused` | Engineer-requested soft pause (takes effect at the next step boundary) | no |
| `done` | All steps succeeded; worktree/branch retained for inspection | no |
| `aborted` | Engineer aborted, or rejected terminally; worktree/branch retained | no |
| `archived` | Terminal. Worktree removed; record kept for history. The branch is retained unless it carries no commits past its base, in which case `delete_empty_branch_on_archive` deletes it (§10, *amended 2026-08-16, task 008*) | no |

### Human actions

| Action | Valid from | Effect |
|---|---|---|
| `cancel` (abort) | queued, running, awaiting_input, awaiting_gate, blocked, paused | Kills any running process (graceful term, then kill after 10 s; `taskkill /T /F` on Windows); → `aborted` |
| `pause` | queued, running | `running`: finishes the current step, then holds; → `paused`. The request is persisted, so it survives a daemon crash; every other human action clears it |
| `resume` | paused | → `queued` |
| `retry` | blocked | Re-runs the failed step (fresh attempt, retry counter reset); → `queued` |
| `edit + retry` | blocked | Overrides the step's prompt/command **in this task's snapshot only**, then retries; the override is recorded on the StepRun |
| `skip` | blocked, awaiting_gate | Marks the step `skipped`, advances to the next step; → `queued` |
| `answer` | awaiting_input | Delivers the answer to the pending input request into the live agent session (§7.4); → `running` (step clock resumes) |
| `approve` | awaiting_gate | Gate step → `approved`; advances; → `queued` |
| `reject` | awaiting_gate | Gate step → `rejected`; → `blocked` (from which: retry earlier via edit, skip, or abort) |
| `set priority` | queued, paused | Reorders scheduler admission |
| `archive` | done, aborted | Removes worktree (warns if dirty — uncommitted changes would be lost; requires `force` in that case); → `archived` |

Tasks are `queued` immediately upon creation (no draft state in v1).

## 7. Step execution semantics

Steps execute strictly in order. Executing step *i* means: render templates → run the
step body → evaluate success → on success advance to step *i+1* (or `done` if last) →
persist → repeat.

### 7.1 Success criteria

A step **succeeds** iff:

1. **`agent` step:** the agent process exits 0 **and** its event stream produced a
   terminal result (not an error event); **and** if a `check` command is declared, the
   check exits 0.
2. **`command` step:** the command exits 0; **and** any declared `check` exits 0.
3. **`manual` step:** the engineer approves the gate.

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
- **`agent_unauthenticated` stays under the normal budget.** *Added 2026-08-14
  (task 003).* A CLI that refuses because it is not logged in fails the attempt
  like any other, retries as usual, and blocks when the budget is spent. Waiting
  cannot fix it, and short-circuiting the budget would make it the only reason in
  vincent that bypasses this section — to save one process spawn at the default
  `max_retries: 1`. Its value is that the reason names the fix.

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

## 8. Workflow definition (YAML)

### 8.1 File format

```yaml
# .vincent/workflows/feature-pr.yaml  (project scope)
# or  {config_dir}/workflows/feature-pr.yaml  (global scope)

name: feature-pr                      # required; unique per scope; project shadows global
description: Implement, test, review, then push and open a PR.

defaults:                             # optional; per-step values override
  agent: claude                       # claude | codex
  model: ""                           # adapter-native id/alias (e.g. sonnet); options via GET /v1/agents (§9.6)
  effort: ""                          # adapter-native effort (claude: low…max; codex: minimal…high) (§8.6)
  permission_mode: full-auto          # full-auto | restricted   (§9.4)
  on_input: wait                      # wait | deny — agent input requests (§7.4)
  input_timeout: 24h                  # max wait in awaiting_input (§7.4)
  max_retries: 1
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

### 8.2 Step types and fields

Common to all steps: `id` (required), `name`, `type` (required), `max_retries`,
`timeout`.

| Type | Required | Optional |
|---|---|---|
| `agent` | `prompt` | `agent`, `model`, `effort`, `permission_mode`, `on_input`, `input_timeout`, `check`, `check_timeout` |
| `command` | `run` | `shell`, `env` (map), `check`, `check_timeout` |
| `manual` | `instructions` | — |

Constraints (validated on load and via `POST /v1/workflows/validate`):

- `steps` non-empty; step ids unique; templates must parse; `type` known; durations
  parse as Go durations; `on_input` is `wait` or `deny`; unknown keys are errors
  (strict decoding) to catch typos.
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

### 8.3 Command steps and shells

`run` and `check` strings are template-rendered, then executed via a platform shell:

- POSIX: `/bin/sh -c "<rendered>"`
- Windows: `pwsh -NoProfile -Command "<rendered>"` (falls back to `powershell`)

A step may pin `shell: sh | pwsh | cmd` explicitly. Cross-OS portability of command
steps is the workflow author's responsibility; the spec makes no attempt to translate.

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
| `.Steps` | map of *completed* step id → `{Status, Result, ExitCode}`; `Result` is the agent's final result text (agent steps) or the last 100 lines of stdout (command steps) |
| `.Worktree` | `Path` |
| `.LastFailure` | on retry attempts only: `{Reason, Output}` from the previous attempt; empty otherwise |

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
    "supports_input": true, "logged_in": null,
    "models":  [ { "value": "sonnet", "source": "cli" }, { "value": "opus", "source": "cli" } ],
    "efforts": [ { "value": "low", "source": "cli" }, { "value": "max", "source": "cli" } ],
    "default_model": "", "default_effort": "",
    "probed_at": "2026-08-07T10:00:00Z", "probe_error": null } ] }
```

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
- One task runs at most one step process at a time.
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
  `queued_reason` naming what it is waiting for. `usage_limit` is the only
  producer today (§7.2); the pair is generic so the next wait-shaped case costs
  no second mechanism. The walk applies the three checks **in this order**:
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
| `vincent task add / ls / show <id> / cancel <id>` | Thin API clients for scripting |
| `vincent gc [--dry-run] [--force] [--json]` | Reclaims data-root directories no task claims (§10); a thin API client like the rest |
| `vincent doctor` | One diagnostic report: paths, daemon, log tail, database, agents, storage, task counts (§17). `--json` for scripting and bug reports; `--fix` (`--force`) reclaims orphaned worktrees and compacts the database. Exit 0 healthy · 1 problems found · 2 no daemon answered |
| `vincent version` | Build info |

*Amended 2026-08-15 (task 005).* `gc` breaks this table's noun-verb pattern
(`project add`, `task ls`) knowingly: `git gc` is the idiom users already have, and the
scope spans two directory trees — worktrees and transcripts — so a `worktree` noun
would have been wrong on the day it shipped.

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
{config_dir}/
  config.yaml                # §12.3
  workflows/*.yaml           # global workflows
{data_dir}/
  vincent.db                 # SQLite, WAL mode
  token                      # API bearer token, created 0600 at first start
  daemon.json                # { "port": N, "pid": N, "started_at": … } for client discovery
  daemon.lock
  tui.json                   # TUI-local state (the §16 first-run acknowledgment)
  worktrees/{task_id}/
  transcripts/{task_id}/{step_index}-{attempt}.jsonl
  logs/daemon.log            # rotated, size-capped
```

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
```

**`usage_limit_recheck_interval` (task 003, added 2026-08-14).** How long a task
waits before being re-admitted after its agent reported a spent usage quota
*without* a reset time; when the CLI reports one, that timestamp wins and this is
unused. Must be positive — zero would re-admit on the very next tick, which is the
respawn loop the hold exists to stop. 15 m bounds a five-hour window at roughly
twenty wasted spawns, and a user who knows their plan can tighten or widen it.
There is deliberately **no** exponential backoff: that would be per-task state the
row has to carry and a second retry-ish concept beside §7.2's. Read per hold, so a
hot reload reaches the next one.

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
                                        { name, scope, project_id, file, description, steps[], errors[]?, warnings[]?, error? }
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

GET    /v1/tasks?project_id=&state=&archived=&limit=&offset=
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
POST   /v1/tasks                        { project_id, workflow, title, description?, fields?,
                                          base_branch?, branch_name?, priority?, agent?,
                                          model?, effort? }
                                        branch_name is used verbatim and wins over every
                                        template (§10, task 001)
                                        → task (state=queued); agent/model/effort form the
                                        task-level override (§8.6), validated per §8.2 —
                                        known-invalid = 400, catalog-unknown values are
                                        reported in `warnings[]` on the 201 body
GET    /v1/tasks/{id}                   full task incl. step runs summary and pending_input (§7.4).
                                        Every task representation carries `available_actions`
                                        (the §6 human actions valid right now) and
                                        `pause_requested`, so clients never restate the FSM.
                                        Detail-only: `workflow_steps[]` — the task's snapshot
                                        as { index, id, type, prompt?, run?, instructions? },
                                        which is what edit+retry prefills an editor with. It
                                        reflects edits made by a previous edit+retry, since
                                        the snapshot is this task's execution truth (§5.3)
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
   `project.*`, `workflow.registry_changed`, `daemon.shutting_down`.
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
  pending_input_json  TEXT,                   -- normalized InputRequest while state='awaiting_input' (§7.4)
  admit_not_before    TEXT,                   -- §11 admission hold; NULL = admissible now (task 003)
  queued_reason       TEXT,                   -- why a queued task waits on more than a slot; NULL = the ordinary queue
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,
  archived_at         TEXT
);
CREATE INDEX idx_tasks_sched ON tasks(state, priority DESC, created_at);

CREATE TABLE step_runs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id             INTEGER NOT NULL REFERENCES tasks(id),
  step_index          INTEGER NOT NULL,
  step_id             TEXT NOT NULL,
  step_type           TEXT NOT NULL,          -- agent | command | manual
  attempt             INTEGER NOT NULL,       -- 1-based
  state               TEXT NOT NULL,          -- running | succeeded | failed | interrupted
                                              -- | approved | rejected | skipped
  agent               TEXT,                   -- adapter name, agent steps only
  model               TEXT,                   -- resolved model as passed to the adapter (§8.6)
  effort              TEXT,                   -- resolved effort as passed to the adapter (§8.6)
  pid                 INTEGER,                -- while running
  proc_started_at     TEXT,
  exit_code           INTEGER,
  check_exit_code     INTEGER,
  failure_reason      TEXT,
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
   syntax-highlighted); action
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
3. **New task.** Project picker → workflow picker (shows description + step list;
   flags steps whose agent is unavailable) → title → description (inline or
   `$EDITOR`) → custom fields (key/value) → base branch (default prefilled) →
   priority → optional agent/model/effort override (pickers fed by
   `GET /v1/agents` with provenance-tagged options and free-text entry;
   replaces workflow defaults, never explicit step fields, §8.6) → create.
   **Pickers are windowed and type-filterable (M5, §9.7):** through v1 every
   catalog fit on a screen (claude: 3 models, 5 efforts; codex: efforts only),
   so the picker rendered all options unconditionally. Cursor's ~180-model
   catalog makes a viewport with a scroll indicator and incremental filtering
   mandatory; the flagging of unavailable agents grows a second reason —
   *installed but not authenticated* (`logged_in: false`, §9.5).
4. **Projects.** List/add/edit/remove; per-project cap and defaults.
5. **Workflows.** Merged registry with scope badges and validation status; `e` opens
   the file in `$EDITOR`; live reload reflects saves immediately. The view reads the
   registry, it does not author it: creating a workflow file from the TUI is out of
   v1 — new files are written in the editor and appear on the next reload. §19's M3
   acceptance loop says "author workflow"; it means this edit path.
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

Task actions act on the selected task and are offered only when the daemon reports
them in `available_actions`: `p` pause/resume · `a` approve · `x` reject · `r`
retry · `s` skip · `E` edit+retry in `$EDITOR` · `c` cancel · `A` archive. `x`
rejects because `r` is taken; `r` doubles as *retry connecting* while disconnected,
where no task is reachable anyway. Destructive actions confirm inline: `c` kills a
live process, `A` removes the worktree and a dirty one re-prompts for `force`.
`set priority` (§6) has no key — priority is chosen in the new-task flow.

Panel-local: `/` filters **whichever list has focus** — tasks, projects, workflows
— so one key means one thing everywhere. `enter` opens or expands. `[`/`]` switch
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
confirmation, answer form) → takeover screen → active filter → nothing. It is a
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
| Option probe fails (help unparseable) | `GET /v1/agents` serves the curated catalog with `probe_error` set; selection and free text keep working (§9.6) |
| Model/effort unknown to the catalog | Validation warning only; the CLI is the final authority — a rejected value fails the step with the CLI's error (retry policy applies) |
| Model *in* the catalog but rejected at run time | Real, not hypothetical, on cursor (§9.7): the step fails with the stderr tail as the message, since no `result` event arrives. Catalog membership is advisory in both directions |
| Agent CLI installed but not authenticated | `logged_in: false` where the adapter can tell (§9.5); the new-task form flags it like an unavailable agent. Where it cannot (`null`), the step runs and fails. *Amended 2026-08-14 (task 003):* where the adapter recognizes the CLI's auth wording, that failure is now named `agent_unauthenticated` instead of surfacing as `nonzero_exit`/`agent_error`. Everything else about the row is unchanged and deliberately so — the step still runs, the attempt still fails, the §7.2 budget still applies, and the task still ends up blocked. There is no pre-flight refusal on `logged_in: false`. *Amended 2026-08-15 (task 005):* the "where it cannot (`null`)" set is now **claude alone** — codex probes `login status`, cursor probes `status` (§9.5). Every other clause of this row stands untouched, task 003 decision 4 included: making the state visible is not the same as blocking on it, and `vincent doctor` is where a user sees it before a task burns its retry budget |
| Agent stopped by a usage limit | *Added 2026-08-14 (task 003).* Where the adapter recognizes the wording, the attempt is recorded `interrupted` with reason `usage_limit`, consumes **no** retry (§7.2), and the task returns to `queued` with an admission hold (§11) — releasing its slot, so other work keeps running. The hold ends at the reset time the CLI reported, or `usage_limit_recheck_interval` after the stop when it reported none. Recovery is unattended: the scheduler re-admits and the step re-runs. The board says `queued` *with* its reason rather than `blocked` (§15). Where the adapter recognizes nothing — codex and cursor today (§9.1) — the run reads as `nonzero_exit`/`agent_error` exactly as before |
| `effort` set on a step whose agent has no effort concept | Ignored by the adapter and documented as ignored (cursor, §9.7); a claude/codex effort value on a cursor step is already an §8.2 *error* — it belongs to another adapter's catalog |
| `restricted` step on an adapter that cannot restrict on this OS | Step fails to start with `restricted_unsupported` (cursor on Windows, §9.7), under the retry policy → typically blocked. Never downgraded to full-auto, and deliberately *not* `agent_unavailable`: the CLI is installed and healthy, so "not found" would send the user to reinstall what is already there |
| Runaway step output (agent or command) | Past `transcript_max_bytes` (§12.3) the process tree is killed and the attempt fails `transcript_limit`, under the retry policy. The line that trips the cap is written **whole** — a truncated line would turn a size failure into a parse failure for every later reader of the JSONL — and the partial transcript is kept with a closing `vincent.transcript_limit` annotation, because the lines that got there are what explain the runaway |
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
- Workflow branching/conditionals, parallel steps, and step fan-out.
- LLM-as-judge verification as an optional third success layer.
- Multi-user / remote daemons / fleet view across hosts.
- Task templates & recurring tasks; issue-tracker ingestion (Jira → task).
- Container/VM-sandboxed step execution.
