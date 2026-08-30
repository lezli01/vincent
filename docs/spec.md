# vincent engineering specification

**Status:** Living engineering reference · **Owner:** László Szabó

> [!NOTE]
> This document preserves code-level contracts and design decisions for
> maintainers. It is not the product landing page or the recommended starting
> point for users. See the [feature tour](features.md),
> [documentation home](README.md), and [workflow schema](reference/workflow-schema.md)
> for the current user-facing view of vincent.

Vincent is a single-user, local-first control plane for AI coding-agent
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
- OS desktop notifications. *Amended 2026-08-28 (task 046, issue #90): the
  **platform-native notification stack** is still deferred — three backends and
  a packaging story for one hard-coded delivery channel. What was never decided
  here is that the daemon may not signal outward at all, and that is what
  `notify:` (§12.3) now does: it runs a command of the user's choosing when a
  task enters a state they named, which reaches `terminal-notifier`,
  `notify-send` and `msg` as easily as it reaches Slack, mail or a file drop.
  The exec hook is the reason the native stack is now cheap to leave deferred
  rather than the reason to build it.*
- LLM-as-judge step verification.
- Workflow branching or conditionals within one task. *Amended 2026-08-17
  (task 014): parallel steps and step fan-out are no longer deferred —
  see §7.5 and §20.*
- Sandboxing agents beyond worktree isolation (a worktree is not a security
  boundary). *Amended 2026-08-30 (task 061, issue #256): the **container** half
  of this is no longer deferred — §16's container execution mode runs a task's
  step processes inside one container, on an image the user supplies. Agent
  steps are the one kind still spawned on the host in this delivery; task 062
  moves them in. The
  boundary the parenthesis names is unchanged and still true: a worktree is not
  a security boundary, and neither is a container whose network is open and
  whose agent credentials are mounted inside it (§16 says so in those words).
  What moved is that the filesystem outside the two mounts, the shell and the
  installed tooling can now be confined, which is what people were actually
  asking this non-goal for. VM-level sandboxing stays deferred (§20).*
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
| 22 | Agent input requests | Structured requests only (`question`/`permission`); new `awaiting_input` state that keeps its slot; step clock pauses, bounded by `input_timeout` (default 24h); normalized schema + raw passthrough; `POST /v1/tasks/{id}/answer`; per-adapter capability (claude yes, codex no); `on_input: wait\|deny` opt-out; TUI-level alerts only (§6, §7.4, §13.2, §15). *Narrowed 2026-08-28 (task 046, issue #90): "TUI-level alerts only" decided how one agent question is normalized, surfaced and answered **inside a client** — it did not decide that the daemon may never signal outward, and it never spoke to `blocked` or `awaiting_gate` at all. The daemon-side `notify:` hook (§12.3) signals on any §6 state and leaves the TUI bell exactly as it was.* |

| 23 | Parallel steps | `type: parallel` runs sub-steps concurrently in the task's one worktree: one step, one index, one slot, no branch and no merge. `manual`, nested groups and `on_input: require` are refused inside one; `max_parallel` (default 4) is a second concurrency dimension the §11 caps do not govern (§7.5, task 014) |
| 24 | Workflow fan-out | `type: fan_out` makes each lane a real child task with its own worktree and branch, merged back `--no-ff` in declared order at the end of the same step. The parent parks in `awaiting_children` holding no slot, so no depth deadlocks; a conflict blocks by default, a lane that did not finish blocks the join, and the tree's bounds are checked at creation (§7.6, task 014) |
| 25 | Conditions between steps | `if:` guards any step (skip and carry on) and any fan-out lane or group sub-step (subset the set); `type: condition` ends the sequence with the task `done`; `allow_failure:` turns the failures a step itself produced into an advance, so a guard has a run's own findings to read. Guards are §8.4 templates that must render exactly `true` or `false`, re-evaluated every time and never cached (§7.7, task 015) |
| 26 | GitHub issue linking | **Read-only, daemon-side.** A task may be created *from* a GitHub issue when the project's `origin` parses as a github.com repository and `github.enabled` is on. The daemon prefers the `gh` CLI and falls back to `GITHUB_TOKEN`/`GH_TOKEN` from its inherited environment; **vincent stores no credential**, keeping §2's secret-management non-goal intact. The issue is fetched **once at creation**, snapshotted onto the task and never re-fetched, so `.Issue` (§8.4) renders offline and a run stays reproducible. The daemon makes every call — at pick time and create time only, never in the step path — and nothing here writes to GitHub, so row 11 is untouched (§5.3, §8.4, §12.3, §13.2, §14, §15; task 035, added 2026-08-26). *Narrowed 2026-08-29 (task 052):* this row is about **issues**; pull requests are row 27, which stores a pointer rather than a snapshot and reverses nothing here |
| 27 | GitHub pull requests | **Read-only, daemon-side**, like row 26 and through the same gate and credential. A project's **open** pull requests are listed on demand, and a task is linked to the pull request whose head branch equals its own `branch_name` — by a daemon-side reconciler on a `github.poll_interval` tick, never as a side effect of a GET. Only the *link* is stored (`github_pull_json`: repo, number, source, suppressed) and it is a **pointer, not a snapshot** — the deliberate opposite of row 26, because draft, state and merged status are live by nature and a stored copy of them would read exactly like a current one while being wrong. A human may link or unlink; a human unlink is *sticky* and the reconciler never re-applies it, never overwrites a human link and never un-suppresses one. **Row 11 stands unamended**: vincent pushes nothing, opens nothing and merges nothing, and the “create a PR” affordance is a *constructed* compare URL — no request is made to GitHub when it is built — that a human clicks. `internal/github` gains no write method, no `POST` and no mutating `gh` subcommand. Task 035 decision 5's “repo identity is not stored” was revisited exactly as it predicted: the identity landed on the **task**, beside the number, and no `github_repo` column was added to projects (§5.3, §12.3, §13.2, §13.3, §14, §20; task 052, added 2026-08-29). *Narrowed 2026-08-30 (task 064):* the read-only posture holds in full — no write method, no `POST`, no mutating `gh` subcommand — and a task may now be created **from** a pull request and run on its head branch. That adds a flag to the same envelope (`branch`, `fork`) rather than a snapshot: nothing renderable is stored, so "a pointer, never a snapshot" is unchanged, and there is still no `.Pull` template variable. The consequences live in §10 (a second worktree creation mode, and archive never touching a branch vincent did not cut) and in §5.3's branch-name chain, which gains `pull` above the per-task literal. The listing above is narrowed the same way: it still **defaults** to open, but `?state=` (§13.2) makes a closed or merged pull request reachable, because acting on a merged one and redoing a reverted one are exactly what creating a task from one is for |
| 28 | MCP from the daemon | **A second protocol on the existing listener, not a second server.** `/mcp` is registered in §13.2's route table inside the same `recover → log → auth` chain, so row 4 is *added to*, not reversed: same loopback listener, same `Authorization: Bearer {token}` from `{data_dir}/token`, same `daemon.json` discovery. The tool surface **is** the route table — a call replays its arguments as an in-process request against the same handler, so the §13.1 bounds, the validation, the `409` + `details.state` envelopes and `Idempotency-Key` hold by construction — **minus five destructive-admin routes** (`daemon/stop`, `daemon/backup`, `DELETE projects/{id}`, `maintenance/gc`, `doctor/fix`), which is a design line: an agent must not be able to stop, garbage-collect or reconfigure the daemon supervising it. §13.3's SSE routes are replaced by a bounded blocking `task_wait` with a hard ceiling, whose result is complete for a client that drops every progress notification. A step parked in that wait **keeps its §11 slot** and a self-blocking wait is *refused*, not released — releasing it would create a §6 state owning a live agent process and holding no slot, which no state does today. The daemon wires its own agent steps to a **per-step endpoint** (`/mcp/step/{run_id}`, per-run secret), which is identity for the refusal and the provenance column and is explicitly **not** a security boundary (§16). Recursion is bounded by `created_by_task_id` + `mcp.max_depth`/`mcp.max_tasks`, deliberately **not** by `parent_task_id`, which the `awaiting_children` join counts (§9.1, §9.2, §9.3, §9.4, §9.7, §11, §12.3, §12.4, §13.4, §14, §16, §20; task 057, issue #243, added 2026-08-29) |
| 29 | Free chat | **A first-class entity beside Task, never a task with a `kind` column.** A `chat` is a titled conversation with an agent, scoped to a project, running in its own git worktree and `vincent/{id}-{slug}` branch, with its own four-state lifecycle (§5.5), its own `chats`/`chat_turns` tables (§14) and its own route family (§13.2). It never appears on the task board, in `GET /v1/tasks` or in any §17 aggregate over `step_runs`, whose `task_id` stays `NOT NULL`. Continuity comes from the **agent CLI resuming its own session** — §7.3's fresh-session rule is amended *for chats only* — so turn N sees turns 1..N-1 without vincent replaying any log as prompt context. A turn is bounded by its own cap `max_parallel_chats` (default 3) and is **refused with 409, never queued**: `internal/scheduler` stays the only place `queued → running` happens because a chat turn is never `queued`, and row 28's "no live-but-uncounted agent CLI" reasoning is extended rather than excepted (§11). **No chat route is an MCP tool** — row 28's exclusion list grows by the whole chat family, because an agent must not start unqueued agent processes and `mcp.max_depth`/`mcp.max_tasks` bound tasks by walking `created_by_task_id`, a chain a chat is not in (§13.4). Only adapters that can resume may hold a chat: claude yes (§9.2), **codex and cursor are refused at creation with a typed reason, not emulated** (§9.3, §9.7). A stored session the CLI no longer knows fails the turn with `session_lost` and leaves the chat usable; a turn interrupted by a daemon restart is finalized `interrupted` and is **never re-run**, because re-running would re-send the human's message into a session that died with the process (§12.4). Chat worktrees join gc's claim namespace and worktree directories are named by owner, so chat 7 and task 7 cannot collide (§10). §16 is untouched: chats are full-auto by default exactly as tasks are (§5.5, §6, §7.3, §9.1, §9.2, §9.3, §9.7, §10, §11, §12.3, §12.4, §13.2, §13.3, §13.4, §14, §15, §20; task 063, issue #255, added 2026-08-30) |

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
| `default_branch` | base branch for new tasks. Detected **once, at registration**, from `origin/HEAD`, falling back to `main`/`master`; editable afterwards, and never re-detected. *Amended 2026-08-29 (task 056):* what is refreshed at run time is the branch's *content*, not this name — see `fetch_base_branch` (§10, §12.3) |
| `default_workflow` | optional workflow name preselected in task creation |
| `max_parallel_tasks` | per-project cap; `null` = no per-project limit (global cap still applies) |
| `branch_template` | *added 2026-08-13 (task 001).* Optional branch-naming template for this project; `null` inherits `config.yaml`'s `branch_template`, and an unset config means the built-in name. Parsed when written, so a broken template fails at `PATCH /v1/projects/{id}` rather than at every task creation |

Registering a project performs validation only (path exists, is a git repo, worktrees
supported). The repo itself is never modified by registration.

### 5.2 Workflow

A named, ordered list of steps defined in YAML (§8). Workflows live in files, not the
DB; the daemon maintains a registry of parsed workflows from three scopes:

- **Built-in:** shipped in the binary. Lowest precedence — a global or project file
  of the same name shadows it. Three are present:
  - `adhoc` — the single-step agent workflow used when a task is created without
    naming one (§5.3). *Amended 2026-08-27 (task 037): its prompt — and every
    other built-in agent prompt — asks the agent to report through `vincent
    status` (§5.6). The daemon appends no such instruction to any prompt, so a
    built-in that does not ask is one that runs silent on the board.*
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
  - `update-workflows` — *added 2026-08-27 (task 037).* A maintenance pass over
    the workflows the task's own project versions under `.vincent/workflows`:
    it brings them up to the current schema and the `vincent-workflows` skill's
    practices without changing what any of them does. It declares no task
    fields and has six steps — a `git ls-files --error-unmatch` probe whose
    stdout is both the file list and the "this project versions no workflows"
    signal, a `condition` that ends the run `done` when there are none, one
    agent step carrying the same embedded skill `create-workflow` carries, a
    relist, a `for_each` loop validating every file (`vincent workflow
    validate` takes one file, so per-file iteration is the loop's job), and a
    `git diff --stat` for the record. Its agent step runs under `on_input:
    deny`: the answers are in the repository and the result is reviewed as a
    diff, so parking the task in `awaiting_input` would buy a held slot and
    nothing else. Unlike `create-workflow`, its deliverable is **the task's own
    worktree and branch**, reviewed and merged like any other diff — these
    files are versioned by the repository, and merging is what makes a
    rewritten workflow live.
- **Global:** `{config_dir}/workflows/*.yaml` — available to every project.
- **Project:** `{repo}/.vincent/workflows/*.yaml` — available to that project only,
  git-versioned and shareable with a team. A project workflow **shadows** a global
  workflow with the same name.

The daemon watches both locations (fsnotify) and reloads on change. Invalid files are
surfaced as registry errors (visible in TUI/API) without breaking valid ones.

*Amended 2026-08-30 (task 065, issue #261).* **A client may author these files
through the daemon**, with `POST` and `PATCH /v1/workflows` (§13.2). Three
properties come with that and are part of this section, not of the endpoint:

- **A file the daemon creates is mode 0644**, not `config.yaml`'s 0600. A
  project workflow is meant to be committed and shared with a team, and it
  carries no secret — the agent options it names are not credentials. An
  existing file keeps whatever mode it already has: the daemon is not the
  authority on a file a repository owns.
- **A name a scope already declares is refused.** Two files in one directory
  declaring the same `name:` is the duplicate this section already describes,
  and the write endpoint makes that judgement before the second file exists
  rather than after the registry lists one of them as an error.
- **Concurrent writers are real here**, unlike for `config.yaml`. The
  `create-workflow` built-in writes the live registry directory from an agent
  run, `$EDITOR` is one key away in the workflows view, and an external editor
  is always possible — so a `PATCH` carries a version token (mtime + hash) and
  a file that moved underneath is a 409. Task 060 decision 6's refusal of
  preconditions still stands for `PATCH /v1/config`, where the race is a human
  against themselves; this is a scoped extension, not a reversal of it.

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

*Amended 2026-08-28 (task 043, issue #145).* Built-in shadowing **stands**. A
global or project file named `adhoc`, `create-workflow` or `update-workflows`
still wins the lookup, including for a task created without naming a workflow —
the phase 2 reasoning holds: creation is one uniform path and `workflow` stays
optional. There is no reserved namespace, no `builtin:` selector and no
`allow_shadow_builtin` declaration; a qualified name would be a new grammar
four resolution sites would have to honour at once, and the current name
pattern admits no colon.

What changes is that the substitution is no longer **invisible**. Every task
records a `workflow_origin` beside `workflow_snapshot` (§5.3) saying which
scope won the walk, which file it was, and a digest of that file's bytes, so a
task created six months ago can still be told apart from one created against
the built-in of the same name.

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
| `branch_name` | `vincent/{id}-{slug}` by default (slug: lowercase title, `[a-z0-9-]`, max 40 chars). *Amended 2026-08-13 (task 001):* configurable through the chain `built-in < config.yaml < project < per-task literal`. Resolved and persisted inside the task's insert transaction, so no committed task carries an empty one. *Amended 2026-08-30 (task 064):* the chain gains a level above the literal — a task created from a pull request (`github_pull`, §13.2) runs on that pull request's **head branch**, which nothing else may override |
| `worktree_path` | assigned when the worktree is created |
| `base_sha` | *Added 2026-08-29 (task 056).* The commit `branch_name` was actually cut from, written beside `worktree_path` when creation fetched `base_branch` from its upstream (§10). NULL means `base_branch` itself still names the fork point — every task predating this and every task created with `fetch_base_branch: false`. It exists because once a task branch starts at a fetched remote tip, `base_branch` names a moving ref that is no longer where the task began, and the two places that read it as the fork point — `GET /v1/tasks/{id}/diff`'s merge-base (§13.2) and archive's empty-branch check (§10) — would otherwise both answer against the stale local commit. *Amended 2026-08-30 (task 064):* on a task created from a pull request it is the **head commit as it stood at admission**, so the diff tab answers "what did this task change" rather than re-rendering the pull request's own diff |
| `priority` | integer, default 0; higher runs first |
| `agent_override` / `model_override` / `effort_override` | optional, chosen at creation (§13.2); replace the workflow's `defaults` but never an explicit step field (§8.6) |
| `state` | §6 |
| `current_step` | index into the snapshot's step list |
| `pending_input` | normalized InputRequest (§7.4) while state is `awaiting_input`; cleared on answer, timeout, or process exit |
| `pending_follow_up` | *Added 2026-08-25 (task 027).* The follow-up run a human asked for from `done` or `aborted` (§6): its compiled workflow, the run form and text it came from, the optional agent/model/effort, the **origin state** the task is returned to, the 1-based **round**, and the run's own **step cursor**. NULL when no follow-up is in flight |
| `workflow_origin` | *Added 2026-08-28 (task 043).* Where the definition behind `workflow_name` came from, captured **once at creation** beside `workflow_snapshot`. It holds the **scope** that won §5.2's shadowing walk (`builtin`, `global`, `project`, or `derived`), the source **file relative to that scope's root** (`.vincent/workflows/adhoc.yaml`, `workflows/release.yaml`; absent for a built-in, which has none), and a **digest** — `sha256:<hex>` over the registry entry's source bytes exactly as loaded, with no normalization. It is **never recomputed**, so it identifies the *file version the task was created from* rather than the bytes the engine runs: include expansion (§7.9), fan-out resolution (§7.6) and `edit + retry` all rewrite `workflow_snapshot` afterwards, and `edit + retry` is separately audited through `step_runs.prompt_override` / `run_override`. A `fan_out` lane records `derived` naming its parent task (§7.6): its steps come from the parent's snapshot, resolved at the *parent's* creation, so it never read a registry at all. NULL for a task created before this was recorded, which is reported as `unknown` — never re-derived from today's registry, which would report a substitution as though it had always been there |
| `github_issue` | *Added 2026-08-26 (task 035).* The GitHub issue this task was created from, captured **once at creation** and NULL for every task created without one. It holds the normalized issue — repo, number, title, body, url, state, labels, author, assignee, milestone (title and number), the issue's own timestamps and the instant it was fetched — and it is **never re-fetched**: every step renders `.Issue` (§8.4) from this snapshot, so an issue edited on GitHub afterwards is deliberately not reflected. That is the reasoning `workflow_snapshot` already rests on: a run is reproducible, no network call enters the step path, and a step render still cannot fail for an external reason. A `fan_out` lane inherits its parent's copy verbatim (§7.6) |
| `github_pull` | *Added 2026-08-29 (task 052).* The pull request this task is linked to (`github_pull_json`, migration 0018); NULL for a task no pull request has ever matched. Unlike `github_issue` it is a **pointer, not a snapshot** — repo, number, `source` (`auto` when the reconciler (§12.3) matched an open pull request's head branch to this task's `branch_name`, `human` when a person said so), `suppressed` (the sticky record of a human unlink, which is why the column needs three states and not two), and `linked_at`. Nothing renderable is stored: title, state, draft and merged status are re-read on every request (§13.2), because they are live by nature and a stored copy of them would read exactly like a current one while being wrong. Deliberately **not** folded into `github_issue_json`, which is defined as "NULL = no linked issue" holding a bare issue. *Amended 2026-08-30 (task 064):* the envelope gains `branch` — this task's `branch_name` **is** the pull request's head branch, because the task was created from it — and `fork`, meaning that head lives in another repository so the branch carries no upstream and nothing can push back. Both are read by admission (§10), by archive (§10, which then touches neither branch leg) and by the retry guard (§18); neither is renderable, so the pointer-not-snapshot rule is untouched. A JSON shape change, not a migration |


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
check exit code, failure reason, skip reason, result summary, status message
(both added to this list 2026-08-26 — see the task 036 amendment below),
transcript file path, input/output tokens, cost (USD,
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

*Amended 2026-08-26 (task 036).* The field list above gains two entries. The
first, **`result_summary`**, is not new — it has been recorded, served on the
§13.2 DTO and read by `.Steps.<id>.Result` and the repair prompt since the first
release — and was simply never listed here. It holds the agent's final result
text, or the last 200 lines of a command step's stdout.

The second is new: the **status message**, a short piece of free text a
*running* step writes about itself through
`POST /v1/tasks/{id}/steps/{step_id}/status` (§13.2). It is nullable, and null
is the ordinary case.

It is one field with two readings, not two features. While the row is `running`
it is the live answer to "what is this doing"; the last value written before the
attempt ends stays on the finished row as the step's self-report, which is the
half that answers "why did that fail" in terms a human wants — "3 tests red in
internal/store" rather than `check_failed`.

Four properties are normative:

- **It is not a `failure_reason`, and no client may render it as one.** That
  enum stays the closed, daemon-authored vocabulary shared with
  `internal/worktree` (T1.5/T1.6 decision). A step killed on `timeout` after
  forty minutes may be carrying a message it set thirty-five minutes earlier,
  so presenting it beside the reason as though it were the daemon's verdict
  would be a lie the daemon never told. It renders as *the step's last status*,
  visually distinct (§15).
- **Only `agent` and `command` steps have one.** `manual`, `parallel`,
  `fan_out`, `condition`, `loop` and `break` write `step_run` rows but run no
  process, so they have no voice and their status stays null; `include` never
  reaches the engine at all (§7.9). Synthesising daemon text for the
  process-less types was rejected: it would put daemon-authored and
  step-authored strings in one field, which is the confusion this field exists
  to escape. So is a workflow-authored `status:` template — it can only restate
  what the author knew before the run, and the whole value here is what only the
  run can know.
- **The daemon never asks for it.** No protocol instruction is appended to a
  rendered agent prompt; §8.4's automatic append stays reserved for
  `<previous-attempt-failure>`. A workflow author who wants live status writes
  the instruction into their own prompt. The cost is a low hit rate until
  authors adopt it; the alternative charges every workflow that does not care
  for tokens on every step.
- **It is human-facing only.** It is not in `.Steps` (§8.4) and not in the
  `<previous-attempt-failure>` block (§7.2). Free text an agent chose at run
  time is not something an `if:` guard should branch on, and `.Steps.<id>.Status`
  already means the run *state* — exposing the message there would need a
  second, confusable key.

There is no `status_updated_at` column and no rule that clears the value: the
event announces when it changed (§13.3), the row carries what it is, and a
second column would have to be kept true by every writer for something no
surface renders.

### 5.5 Chat and ChatTurn (task 063)

A **Chat** is a titled conversation with an agent, scoped to a project. It is a
first-class entity beside §5.3's Task, not a task wearing a different hat: it
has no workflow snapshot, no step ledger, no `current_step`, no verdict and no
§6 lifecycle. It never appears on the board or in `GET /v1/tasks`.

What it does share with a task is the isolation: a chat gets its own git
worktree and its own `vincent/{id}-{slug}` branch (§10), so an agent it is
talking to can edit files and make commits without colliding with any task.

| Field | Notes |
|---|---|
| `id`, `project_id`, `title` | as a task's |
| `state` | `idle` \| `running` \| `awaiting_input` \| `archived` (below) |
| `agent` | fixed at creation, and must be an adapter that can resume (§9.1) |
| `model`, `effort`, `permission_mode` | resolved once at creation, not per turn |
| `branch`, `base_branch`, `base_sha`, `worktree_path` | §10, exactly a task's |
| `session_id` | **the agent CLI's own conversation id** — the whole of §7.3's chat-only amendment. Empty before the first turn finishes |
| `pending_input` | the §7.4 request being awaited; non-null exactly in `awaiting_input` |

A **ChatTurn** is one exchange: the human's message and the agent run it
produced. Its accounting columns are `step_runs`' — tokens, cost, duration,
pid, exit code, proc identity — because closing the accounting gap is half of
what chats are for: a conversation held outside vincent has no transcript and
no cost record at all. `step_runs` itself is untouched and its `task_id` stays
`NOT NULL`, so every existing query and every §17 aggregate keeps its current
meaning. Each turn has its own transcript file.

A turn is `running`, then one of `done`, `failed` or `interrupted`, forever.
`session_id` also rides on the turn, so a reader can see which session a given
turn actually ran in — claude may hand a resumed conversation a new id, and a
turn that failed `session_lost` names the id that was refused.

#### Chat lifecycle

The vocabulary is deliberately **separate from §6's** and lives here rather
than there. A chat has no `queued` (it is never admitted), no `blocked` (a
failed turn is a property of the turn, and the chat is usable the instant the
process is gone), no gate and no verdict. Folding four states into §6 would
make every existing task query and every board legend decide whether it means
chats too — the same objection that kept a `kind` column off `tasks`.

| State | Meaning |
|---|---|
| `idle` | no live turn; the state a chat is created in and the one every finished turn returns it to |
| `running` | a live turn: an agent process is up, owned by the chat's runner goroutine |
| `awaiting_input` | a turn holding its process while the agent waits on a §7.4 request. It **holds** its cap slot, for §6's reason: the process is alive on its stdin |
| `archived` | the only terminal state. The worktree is gone; nothing further can run in it |

Human actions: `send` (idle → running), `answer` (awaiting_input → running),
`cancel` (running/awaiting_input → idle), `archive` (idle → archived). There is
no pause: a chat is a foreground conversation, and a paused one is just an idle
one nobody has sent to. Anything outside this table is a `409`, decided by
`internal/chatstate` — the pure FSM both the API and `internal/chatrun` consult,
the arrangement `internal/taskstate` has for §6.

`send` over `max_parallel_chats` is **refused, not queued** (§11). That refusal
is not in the FSM: the cap is about how many chats are running, not about what
this chat may do.

## 6. Task lifecycle

> Chats have their own lifecycle and their own vocabulary — `idle`, `running`,
> `awaiting_input`, `archived` — deliberately kept out of this section and
> documented with the entity in §5.5 (task 063). Nothing in §6 changed when
> chats landed.

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
- **A step's status message is not part of the retry block.** *Added
  2026-08-26 (task 036).* The status a step wrote about itself (§5.4) is never
  a `failure_reason` and is deliberately **not** put in the
  `<previous-attempt-failure>` block the daemon appends on retry. The block is
  the daemon's account of what went wrong — reason plus output — and mixing an
  agent's own free text into it hands the next attempt a claim it cannot tell
  apart from a fact vincent established. The failed attempt's status is
  displayed to humans (§15) and reaches nothing the run itself reads.
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

**Amended 2026-08-30 for chats only (task 063, issue #255).** A **chat turn**
(§5.5) is the one run that *does* resume: it starts the agent CLI with the
session id that CLI itself reported on the previous turn, so turn N has turns
1..N-1 in context. The three properties above are traded away knowingly and
only there — a chat turn is not individually re-runnable, its context window
grows with the conversation, and it couples to the CLI's session semantics
hard enough that an adapter which cannot resume simply cannot hold a chat
(§9.3, §9.7). Nothing about a workflow step changed: `agent` steps still get a
fresh session, and no step ever sets `RunSpec.ResumeSessionID`.

Replaying a prior conversation into the prompt is **not** an alternative
implementation of this and is rejected: it is an emulation of a capability the
adapter does not have, and §9.x's rule is that a missing capability is stated,
never faked.

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
    type: string                 # string (default) | integer | number | boolean | enum
    required: true               # default false
    pattern: '^OPS-[0-9]+$'      # optional Go RE2 expression; string only
    default: OPS-1               # optional; any type
  - name: environment
    type: enum
    values: [dev, staging, prod] # required for enum, rejected on every other type
    multiple: false              # enum only; default false
    default: staging
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
  `fields:` map continues to bind internal values. A lane's `fields:` overrides
  are **not** validated against the root's declarations: a lane may bind a value
  the root declares as an enum member to something that is not one. That is the
  original behaviour of lane overrides, not a hole this section introduces.

*Amended 2026-08-30 (task 058).* The vocabulary gains a fifth type, `enum`, and
every type gains a `default:`.

- `values:` carries an `enum`'s members in declared order. It is required for
  `enum` and an error on every other type; it must be non-empty, its members
  unique, non-empty, and free of `,`. `pattern:` stays string-only and is an
  error alongside `enum` — the members *are* the constraint, and only a list can
  be published to a client that wants to build a control from it.
- `multiple:` (default false) says an `enum` accepts more than one member. It is
  per field and `enum`-only. The picked members are joined with `,` in
  **declared** order, deduplicated, with no spaces (`dev,prod`): declared order
  rather than click order is what makes the same selection the same string, so
  template output and branch names are stable. `POST /v1/tasks` normalizes a
  supplied value that way — split, trim, drop empties, deduplicate, reorder,
  rejoin — *before* checking membership, so every client produces the same task
  row and a rejection names the offending element.
- `default:` may be declared on any field and is validated against its own
  declaration when the workflow loads. `default:` and `values:` take native YAML
  scalars — `default: true`, `default: 3`, `default: 1.5`, `values: [1, 2]` —
  canonicalized to the string the field carries, using the scalar's literal
  source text. A mapping, or a sequence anywhere but a `multiple` enum's
  `default:`, is a load error at `fields[i].default`.
- `POST /v1/tasks` substitutes a **required** field's `default:` for an omitted
  key before validating and inserting, so the task row records the value that
  actually applied and a scripted caller that omits it no longer gets a 400. An
  **optional** field's default is published through `GET /v1/workflows` and
  seeded by clients only; the daemon never invents it, so an optional field the
  caller omitted stays genuinely absent from `.Task.Fields` and adding a
  `default:` to one is not a silent change for a workflow that guards on
  presence. A key present but empty is never defaulted.
- A client that predates `enum` sees an unknown type, falls through to a
  free-text row and runs no local check. The daemon still gates the value.

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

*Amended 2026-08-30 (task 061):* a **containerized** step (§16) inverts the
first rule and the inversion is documented rather than translated. The body
executes under the **container's** `/bin/sh`, not the daemon host's shell — on
a POSIX host that is the same spelling but a different `sh`, and the image's
`PATH` and installed tooling are what the body sees. `platforms:` keeps gating
on the **host**, which is where the daemon and its worktrees are. A step that
pins `shell: pwsh` or `shell: cmd` cannot be honoured by a Linux image and is
refused: at **load** when the workflow's own `defaults.container.image` pins an
image, which is the only case load-time validation can judge, and at **task
creation** with `400 validation_failed` naming the step otherwise — because
containerization also resolves from the hot-reloadable `config.yaml`, and a
workflow being parsed does not know which task will run it.

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
| `.Issue` | *Added 2026-08-26 (task 035).* The GitHub issue the task was created from (§5.3): `Number`, `Repo` (`owner/name`), `Title`, `Body`, `URL`, `State`, `Labels` (a **list**, so a prompt can range over it), `Author`, `Assignee`, `Milestone`, `MilestoneNumber`. Its zero value — `Number: 0` — is what every task created without an issue renders with, exactly the way `.Loop`'s `Index: 0` works, so `{{ if .Issue.Number }}` tells the two apart and one template serves both. It is read from the task's snapshot and **never from the network**: rendering stays pure and offline, and an issue edited on GitHub after creation does not change what a later step renders |
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

*Recorded 2026-08-26 (task 036).* That block stays the **only** thing the
daemon appends to a rendered prompt. In particular the step-status protocol
(§5.4, §13.2) is documented rather than injected: a workflow author who wants
an agent to report on itself writes the instruction into their own prompt, and
`.Steps.<id>` deliberately does not gain the status message — it means what a
completed step *produced*, and `Status` there already means the run state.

*Added 2026-08-28 (task 044).* **The preview binding.** `vincent workflow
render` (§12.1) executes these same templates with no task, no worktree and no
completed step, so every value a run discovers is bound to a visible sentinel
rather than to empty: binding them empty under `missingkey=error` would report
every legitimate `{{ .Steps.plan.Result }}` as a failure a real run would not
hit, and printing empty would make a preview read as the literal prompt an
agent will receive. The vocabulary is:

| Binding | Preview value |
|---|---|
| `.Task.ID` | `0`, following `.Loop.Index`'s precedent |
| `.Task.Title` / `.Description` / `.BranchName` / `.BaseBranch` | `<task.title>`, `<task.description>`, `<branch>`, `<base_branch>` |
| `.Task.Fields` | one entry per **required** declared field (§8.1.2), bound to its `default:`, else an `enum`'s first declared value, else `<field.NAME>` — a sentinel is never a member of its own enum, so a preview binds a value the workflow could actually receive where one exists *(amended 2026-08-30, task 058)*. Optional declared and undeclared names stay absent, so reading one without `{{ with index … }}` is the error the defensive-read rule above says it is |
| `.Project.*` | `<project.name>`, `<project.path>`, `<project.default_branch>` |
| `.Steps` | one entry per step id the **file** declares, nested bodies and inline fan-out lanes included — an `include` step and a lane naming a registry workflow contribute none, since neither survives as a step of this task (§7.9, §7.6) — each `{Status: <steps.ID.status>, Result: <steps.ID.result>, ExitCode: 0}`. A forward reference renders clean: restricting the map to steps that would have completed interacts with `parallel` blindness, loop iterations and `allow_failure` in ways that produce false positives, and a false positive exits 1 inside a pre-commit hook |
| `.Step.Attempt` | `1`, and the `<previous-attempt-failure>` block above is not appended |
| `.Loop` | the zero value outside a loop; `{Index: 1, Item: <loop.item>, IsFirst: true}` for a step inside one |
| `.Issue` | the zero value, so `{{ if .Issue.Number }}` takes the unlinked branch |
| `.Worktree.Path` / `.LastFailure` | `<worktree>`, `{<last_failure.reason>, <last_failure.output>}` |
| `.Conflicts` | one element, `<conflicts[0]>`, on an `on_conflict: agent` resolver step; empty everywhere else |
| `.Host` | the **CLI host's** real GOOS/GOARCH — the only honest offline answer, and the one place a preview and a remote daemon can differ |

A guard is rendered and shown but not judged against `true`/`false`: a sentinel
can legitimately make one non-boolean, so that is a warning, never an error.

### 8.5 Environment for command, check and agent steps

*Retitled 2026-08-26 (task 036): this block now reaches `agent` steps too. It
had always been specified for command and check steps alone, which left an
agent process able to see the resolved environment and none of the facts about
the run it was executing — so an agent could not name its own step even to the
daemon that started it. `vincent status` (§12.1) addresses itself with
`VINCENT_TASK_ID` and `VINCENT_STEP_ID`, which is what made the gap
load-bearing.*

Inherits the daemon's environment (which inherits the user's), with cwd set to the
worktree, plus:

*Amended 2026-08-30 (task 061):* a **containerized** step's base is the
**image's** environment, not the daemon's. `environment.inherit: all` — §12.3's
default — is read as `none` for such a step and logged once per task, because a
macOS or Linux host's `PATH`, `HOME`, `TMPDIR` and `SHELL` inside a Linux image
is a broken container rather than an inherited one. An explicit name list in
`environment.inherit` is honoured verbatim, and `environment.unset`,
`environment.set` and the `VINCENT_*` block below apply exactly as specified on
top of the image's own environment. The `VINCENT_*` values stay true on both
sides because the worktree and the repository are mounted at their own absolute
paths (§16), so `VINCENT_WORKTREE` and `VINCENT_PROJECT_PATH` name the same
directory inside the container as out.

```
VINCENT_TASK_ID, VINCENT_TASK_TITLE, VINCENT_PROJECT_NAME, VINCENT_PROJECT_PATH,
VINCENT_WORKTREE, VINCENT_BRANCH, VINCENT_BASE_BRANCH, VINCENT_STEP_ID,
VINCENT_STEP_ATTEMPT, VINCENT_WORKFLOW
```

*Amended 2026-08-30 (task 064).* A workflow that declares a field named `pull`
(§8.1.2) receives the pull request's **number** in it when the task was created
from one, and reads it here as any declared field is read — a `run:` body sees
this environment and not §8.4's template context, which is why the number has to
be a field at all. There is no `.Pull` template variable and never was; `pull` is
the only way a workflow learns the number, exactly as `issue` is for an issue.

The precedence is one rule for all three step types: the §12.3 resolved base
environment, then this block, then a `command` step's own `env:` (which is a
command-step field, so an agent step has none). Because the block is layered
*after* the policy, `environment.unset` cannot reach a `VINCENT_*` variable:
these are facts about the run, not inherited state.

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

*Recorded 2026-08-26 (task 036).* The step-status channel (§5.4) is
**adapter-independent** and is not part of this section's surface. A step sets
its status by calling `POST /v1/tasks/{id}/steps/{step_id}/status` from its own
process — usually through `vincent status` (§12.1) — so nothing is parsed out
of an adapter's stream and no `AgentAdapter` method is involved. No adapter can
therefore lack the feature, and nothing about it is emulated for one that would
have: the difference this section documents everywhere else does not arise
here.

That is also why the status is not a marked line in a step's output. A marker
lifted out of the normalized `AgentEvent` stream would miss the obvious agent
spelling — an agent running `echo '::vincent:status:: …'` through its shell
tool produces a tool-use event, not an `Output` event — would force a
strip-or-keep choice over the transcript and `result_summary` with no good
answer, and would make every step's stdout a control channel, so that any
program which happened to print the marker changed daemon state.

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
    MCP            *MCPServer        // §13.4 endpoint this run is wired to; nil = no vincent tools (task 057)
    ResumeSessionID string           // resume the CLI's own prior session (§7.3 amended; task 063).
                                     // "" is the fresh session every workflow step gets and always got;
                                     // only a chat turn ever sets it
}

// Resumer is the optional capability an adapter implements when its CLI can
// resume its own session (task 063). Optional rather than a method on
// AgentAdapter so "a new adapter is one implementation with zero core changes"
// stays true — an adapter that says nothing cannot resume. All three shipped
// adapters implement it anyway, because §9.x states a missing capability
// positively: codex and cursor return false with the reason written down.
type Resumer interface {
    SupportsResume() bool
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

*Added 2026-08-29 (task 057).* `RunSpec.MCP` carries the §13.4 MCP server a step
is wired to: `{Name, URL, Token}`, where the URL is the daemon's per-step
endpoint and the token is a secret minted for that step run. `nil` is a run with
no vincent tools — every run before task 057, and every run under
`mcp.wire_steps: false`.

Each adapter carries it its own way (§9.2, §9.3, §9.7); none share a mechanism.
An adapter — or an installed CLI version — that **cannot** carry one returns
`ErrMCPUnsupported` from `Start`, and the engine fails the step with
`mcp_unsupported`, mirroring `ErrRestrictedUnsupported`.

That is a deliberate departure from the standing rule that a capability an
adapter lacks is stated here and ignored at run time, and it is recorded as a
departure rather than left to read as an oversight. The reasoning: a workflow
whose prompt depends on the vincent tools should fail loudly rather than burn an
agent run producing work premised on a channel that was never there. A user who
prefers the older behaviour turns the wiring off with one line
(`mcp.wire_steps: false`, §12.3). Task 041's version-compatibility surface is
where the gap is reported ahead of a run.

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
- **Resume (added 2026-08-30, task 063).** `--resume <session_id>` reloads one of
  claude's own conversations. The id comes from claude itself: every stream line
  carries `session_id`, and the adapter records the last one it saw as
  `RunResult.SessionID` — the last rather than the first because a *resumed*
  conversation may be handed a new id, and what a chat must store is the session
  the run actually ran in. This is the only adapter that implements
  `agent.Resumer` in the affirmative, and it is what makes a chat's second turn
  see its first (§5.5, §7.3). An id claude refuses is classified
  `FailureSessionLost` — matched only for a run that actually passed `--resume`,
  so no workflow step can be misdiagnosed as a lost session.
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

*Amended 2026-08-28 (task 041).* The builds this adapter's parsers were
captured against are now a machine-readable list, not prose alone:
`2.1.224` (the `--help` fixture the option probe is parsed from) and `2.1.226`
(the §7.4 control-protocol stream fixtures). `Detect` compares the probed
version against it and reports `version_verdict` (§9.5) — `tested` for an exact
match, `untested` for anything else. `untested` is the normal answer for a user
on a current CLI and **changes no behaviour whatsoever**. The separate
`supports_input` family gate `[2.1.0, 3.0.0)` is untouched: it gates one
capability and degrades the invocation visibly, which is a different question
from whether vincent has ever seen this build.

*Added 2026-08-29 (task 057).* The §13.4 MCP server rides on
`--mcp-config <inline JSON>` with `--strict-mcp-config` beside it, so the
user's own `.mcp.json` and global servers never leak into a vincent step.
Per-run, no global state. The bearer token is consequently **on the command
line** — visible to `ps` for the life of the step — which is a real cost rather
than an oversight: claude offers no env-var indirection for an inline config,
the alternative is writing a file into the worktree (which is what cursor has to
do, §9.7), and the token is a per-step secret against a loopback listener that
dies when the step ends. The §12.3 `debug` record redacts it, because that
transcript is something people paste into issues.

### 9.3 Codex adapter

- Invocation (pinned against codex-cli 0.142.5): `codex exec --json`, cwd =
  worktree, prompt via stdin (piped; no prompt argument). Full-auto maps to
  `--dangerously-bypass-approvals-and-sandbox` — the documented automation
  switch; restricted maps to `--sandbox workspace-write`, writes confined to
  the worktree, the closest analog of claude's allowlist. Caveat: in a linked
  worktree the real git dir lives under the main repo, so a `git commit` from
  a restricted codex step may be denied; vincent itself never needs commits
  (the diff reads the working tree).
- **Cannot resume (stated positively, 2026-08-30, task 063).** `agent.CanResume`
  is false for codex, so a chat on it is refused at creation (§13.2,
  `agent_cannot_resume`). codex does have `exec resume <thread_id>` and its
  stream does carry a `thread_id` — neither is read here yet, and no fixture
  captured against a named codex build proves the argv, so the capability is
  absent rather than approximate. Replaying the conversation as prompt context
  would be an emulation, which §9.x forbids; a refusal a human can read is the
  honest alternative. Deferred to §20 with the fixture requirement attached.
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

*Amended 2026-08-28 (task 041).* Verified builds: `0.142.5` (the invocation
pinned above) and `0.147.0` (the reasoning capture, T4.17). `Detect` reports
`version_verdict` against that list, advisory in exactly the way §9.2 records.

*Added 2026-08-29 (task 057).* codex has no `--mcp-config`, but `codex exec`
takes `-c key=value` dotted TOML overrides and (verified against 0.150.1)
supports streamable-HTTP servers with a bearer token read from an environment
variable. The §13.4 server is wired as `-c mcp_servers.vincent.url=…` plus
`-c mcp_servers.vincent.bearer_token_env_var=VINCENT_MCP_TOKEN`, with the token
passed through the step's environment. Per-run: nothing mutates the user's
`~/.codex/config.toml`. The token is therefore **not** on the command line here,
unlike claude's — codex offers the indirection and claude does not.

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

*Amended 2026-08-28 (task 041).* The refusal moves forward: **task creation
rejects a `restricted` step whose resolved adapter cannot restrict on this
host**, with a `400 validation_failed` naming the step and the agent, built as
an exact mirror of task 013's `on_input: require` gate. It is the one
security-sensitive capability gap vincent has — every other missing capability
degrades visibly, while running a step full-auto because restricting was
unavailable inverts the choice the step made — and it is the only one that can
be judged with no binary installed, because the answer depends on adapter
identity and `GOOS` rather than on the build. The verdict is published as
`restricted_verdict` (§9.5, §9.6) and is resolved from `workflow.PermissionMode`,
the same function the engine runs under, so the gate and the run cannot disagree
about what a step's `permission_mode` resolves to.

`agent.ErrRestrictedUnsupported` and the `restricted_unsupported` reason stay
exactly where they are, as the backstop for a task whose daemon has changed
underneath it — a data directory carried to Windows, or a workflow edited after
the task was queued (§18). Retries are deliberately **not** gated: the decision
was creation-time enforcement, not creation-plus-admission, and a retry that
would reproduce the condition is caught by that backstop.

*Added 2026-08-29 (task 057).* **`restricted` bounds what a step does to the
filesystem and the shell, not what it does to vincent.** Claude's restricted
allow-list carries `mcp__vincent__*` in full, so a restricted step wired to
§13.4 can create, cancel and archive vincent tasks.

The alternative was leaving it out, and that is worse rather than safer: the
allow-list does not match `mcp__vincent__*`, so a restricted step would see the
whole tool list and be denied every call — a tool list that is a lie, and an
agent burning its turns discovering it. Stated here and in §16 because it is
only defensible written down.

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

*Amended 2026-08-28 (task 041).* Adapter health is **five separate facets**,
reported separately and never emulated:

| Facet | Field |
|---|---|
| installed | `available` / `path` |
| authenticated | `logged_in` (tri-state, never a guess) |
| protocol-compatible | `input_verdict` (§7.4) **and** `version_verdict` |
| permission-compatible | `restricted_verdict` |
| model-catalog | `probe_error` (§9.6) |

`Availability` gains `version_verdict` — `tested` / `untested` / `incompatible`,
empty when there is no build to judge — and `tested_versions`, the list it was
judged against, so a row saying `untested` can say what it is untested against.
Comparison is **exact string equality**, because cursor's version is calver plus
a commit sha and admits no range (§9.7); a range that worked for two adapters of
three would answer a different question depending on which one you asked. The
`incompatible` list ships **empty for all three adapters** — vincent has
observed no such build — and is wired, rendered and exercised by tests through
an injected list, so the day one is found the change is one string in one table.

`restricted_verdict` rides the *catalog* rather than `Availability`
(`agent.Options.RestrictedSupport`, mirroring `InputSupport`), because it is
static: it depends on adapter identity and `GOOS`, never on the installed
binary. `Curated()` therefore answers it with no subprocess, which is what §8.2
validation and the creation-time gate require.

Model-catalog health is **not** a new field. §9.6 already defines `probe_error`
as exactly "the option probe failed and you are reading the curated catalog";
duplicating it as a verdict would give one fact two names.

None of these verdicts blocks anything except `restricted_verdict`, and none of
them is a `vincent doctor` problem (§17, task 005 decision 7): an untested build
is the normal state of a healthy machine. There is still **no pre-flight refusal
on `logged_in: false`** (task 003 decision 4) — this re-states that decision
rather than reopening it.

### 9.6 Option discovery (`GET /v1/agents`)

`GET /v1/agents` returns, per adapter, the availability data of §9.5 plus the
selectable options — models and efforts with provenance, and the adapter
defaults:

```json
{ "agents": [ {
    "name": "claude", "available": true, "path": "…", "version": "2.1.224",
    "supports_input": true, "input_verdict": "supported", "logged_in": null,
    "version_verdict": "tested", "tested_versions": "2.1.224, 2.1.226",
    "restricted_verdict": "supported",
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

- **`version_verdict`, `tested_versions`, `restricted_verdict`** (added
  2026-08-28, task 041) ride alongside it as siblings, not as a nested `health`
  object: nesting one of the five §9.5 facets while four stayed flat is worse
  than five flat fields. `version_verdict` is advisory everywhere.
  `restricted_verdict` is the one the daemon refuses task creation on (§9.4),
  and the one that is answered even for an adapter with nothing installed.
  Model-catalog health is `probe_error`, below — it is not repeated as a
  verdict.

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

  *Amended 2026-08-25 (task 029): "unconditionally" narrows to "by default".*
  `GET /v1/doctor?probe=false` serves availability from the cache instead. The
  decision above is **not** relitigated — it is about a human running
  `vincent doctor`, and that path still forces, so its loop is intact. What
  changed is that the report acquired a second caller the decision was not
  written about: the TUI's daemon panel now reads the database group from this
  endpoint, and it opens on a keypress. Making `6` spawn three subprocesses
  every time would be a real regression in a view that is otherwise cheap. The
  flag defaults to forcing, so every caller the original decision covers is
  unaffected.

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
- **Cannot resume (stated positively, 2026-08-30, task 063).** As with codex,
  `agent.CanResume` is false and a chat on cursor is refused at creation.
  cursor-agent has a `--resume`, and its stream carries `session_id`, but the
  adapter reads neither and no captured fixture pins the behaviour. Same rule,
  same reason, same deferral (§20).
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
- **Version verdict compares whole strings** (*added 2026-08-28, task 041*).
  The verified builds are `2026.08.04-aaa8809` and `2026.08.11-e8db854`, and the
  comparison is exact string equality — calver plus a commit sha has no ordering
  to range over, and the sha is part of the binary's identity, not decoration.
  Rather than let one adapter answer a version question differently from the
  other two, **all three** adapters compare exact strings (§9.5).
- **Restricted verdict is a static platform fact** (*added 2026-08-28, task
  041*). `restricted_verdict` is `unsupported` on Windows and `supported`
  elsewhere, derived from the same `sandboxAvailable` value `buildArgs` refuses
  on, so the creation-time gate (§9.4) and the run cannot disagree. It needs no
  installed binary: cursor cannot restrict on Windows whether or not
  `cursor-agent` is there, which is what makes refusing at creation safe.

*Added 2026-08-29 (task 057).* Cursor has **no per-run MCP flag at all**:
`cursor-agent mcp` reads only `.cursor/mcp.json` in the workspace or
`~/.cursor/mcp.json` globally. So the adapter writes `.cursor/mcp.json` **into
the task worktree** before `Start`, removes it after `Wait`, and passes
`--approve-mcps` (without which a headless run stops on a trust prompt for the
server vincent just configured). Workspace-scoped and per-task: nothing here
touches the user's global cursor config. This extends the §16 note about vincent
writing to cursor's own config.

Two consequences are handled rather than assumed away:

- The file is **untracked inside a git worktree**, so while the step runs it is
  visible to `git status`, to the task diff and to dirty detection. It is written
  0600, because unlike claude's argv it persists on disk for the life of the run.
- A daemon crash leaves it behind, so §12.4 recovery removes a leftover one from
  every live task's worktree. An empty `.cursor` goes with it; a `.cursor` the
  user or the agent put something else in stays.

## 10. Worktree management

- **Location:** `{data_dir}/worktrees/{task_id}` — outside every repo, so IDE file
  watchers and repo tooling in the main checkout are never disturbed.
- **Creation** (when the scheduler first admits the task):
  `git -C {project.path} worktree add {worktree_path} -b {branch_name} --no-track {start}`.
  If `base_branch` doesn't resolve locally, task creation fails fast with a clear error.

  *Amended 2026-08-29 (task 056).* `{start}` used to be `base_branch` itself, which
  meant every task built on whatever the human's last `git pull` left behind — on a
  daemon that runs for days over projects receiving merged pull requests, arbitrarily
  stale. With **`fetch_base_branch` (§12.3), default true,** creation first runs
  `git fetch {remote} {ref}` — bounded by the same 60s remote timeout archive's
  `push --delete` uses — and `{start}` is the commit `FETCH_HEAD` resolved to, which
  is also recorded as the task's `base_sha` (§5.3).

  - **The remote is the base branch's own** — `branch.{base}.remote` plus
    `branch.{base}.merge`, the pair task 008 already refuses to guess. `origin` is
    never assumed. A local `master` tracking `refs/heads/main` therefore fetches the
    right ref, and "no remote at all", "a branch that never left the machine" and
    "a `fan_out` lane whose base is its parent's branch (§7.6)" are one answer
    rather than three special cases: no upstream, no fetch, today's behaviour.
  - **Nothing local is mutated.** The user's base branch keeps its SHA and its
    working tree; fast-forwarding it was rejected because it is frequently checked
    out and often dirty, and would need its own refusal path. The visible cost is
    that `git log {base}` in the human's checkout no longer matches what tasks build
    on.
  - **A fetch never blocks.** No remote, no upstream, an unreachable host, an auth
    failure or a timeout all fall back to the local base with a log line. No new
    `block_reason` exists for it, and no step can fail for a network reason — §26's
    rule is untouched, since admission is outside the step path.
  - **`--no-track` is not optional.** Under `branch.autoSetupMerge` git copies the
    start point's upstream onto the new branch, and a task branch carrying one is a
    live hazard: archive's remote leg would run
    `git push --delete origin refs/heads/master`, deleting the project's default
    branch on the forge, and a `fan_out` child would fetch that upstream instead of
    inheriting its parent's branch. Starting from a resolved SHA already avoids it;
    the flag is the belt behind the braces and also covers `autoSetupMerge = always`
    on a local base.
  - **`POST /v1/tasks` is unchanged.** Task creation stays entirely offline and still
    400s on a `base_branch` with no local branch; a base that exists only on the
    remote is not a case this serves.

  *Amended 2026-08-30 (task 064).* There is now a **second creation mode**, for a
  task created from a pull request (`github_pull`, §13.2). Everything above
  describes the first mode and is unchanged for it; a pull-request task inverts
  both halves of "cut a new branch, refuse a pre-existing one", because its branch
  **is** the pull request's head branch and its commits have to reach the pull
  request.

  - **No `-b`, and no `branch_exists` refusal.** The head is fetched, the local
    branch is created at it or fast-forwarded to it, and the worktree is added with
    `git worktree add {worktree_path} {branch_name}`. A pre-existing local branch of
    that name is the *normal* case for anyone who has already looked at the pull
    request.
  - **The fetch is fatal.** `git fetch {remote} refs/heads/{head}` — or
    `refs/pull/{n}/head` for a fork — and there is nothing to fall back to, because
    the fetched commit is where the branch has to be. A failure blocks the task with
    `pull_fetch_failed` (§18). This is the one place §10 fetches and can block; the
    base fetch above still never does.
  - **Fast-forward or block.** A local branch behind the head is fast-forwarded; one
    that already contains it is left alone; a **diverged** one blocks with
    `pull_branch_diverged`. It is never `reset --hard`: the local copy may hold
    unpushed commits. A branch already checked out in another worktree — vincent's
    or the human's own — blocks with `pull_branch_checked_out`, because git cannot
    put one branch in two worktrees.
  - **`--no-track` is narrowed, not reversed.** On a pull-request task the upstream
    is the deliverable: `branch.{head}.remote` and `branch.{head}.merge` are set
    deliberately, so a workflow's push reaches the pull request. The hazard the flag
    exists for is closed from the other end instead — see the archive exception
    below. A **fork** gets no upstream at all: nothing can push back, and that is
    said on the task at creation rather than discovered when a delivery step fails.
    The daemon never runs `git remote add` for a fork.
  - **`base_sha` is the head commit at admission** (§5.3), so
    `GET /v1/tasks/{id}/diff` answers "what did this task change" rather than
    re-rendering the pull request's own diff.
  - **`POST /v1/tasks` is still entirely offline.** It resolves the pull request over
    GitHub for the prefill, exactly as `github_issue` does, and runs no git.
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

  *Amended 2026-08-30 (task 064).* The chain grows a level above the literal:
  `built-in < config.yaml < project < per-task literal < pull request`, reported by
  `/v1/resolve` as source `pull`. A task created from a pull request runs on that
  pull request's head branch and nothing else may name it — a project template or a
  typed literal would put the commits somewhere the pull request never sees. Two
  consequences, both refusals: the creation-time collision check does not apply to a
  pull-request task (its branch is expected to exist; the in-transaction claim check
  against other unarchived tasks still does, and still 400s), and
  `retry { branch_override }` is **refused with a 409** on such a task, since
  renaming its branch would detach it from the pull request it was created for.
- **Chat worktrees (added 2026-08-30, task 063).** A chat (§5.5) gets a worktree
  and a `vincent/{id}-{slug}` branch on exactly the terms above: same root, same
  branch template, same dirty detection, same archive semantics (§13.2's
  `POST /v1/chats/{id}/archive` removes the worktree and deletes the branch only
  when it received nothing, with the same `--force` way out of a dirty refusal).

  Two things changed to make room for it. A worktree directory is now named by
  its **owner**, not by a bare id — `{root}/{task_id}` for a task, unchanged, and
  `{root}/chat-{chat_id}` for a chat — because both live under one root and
  `{root}/7` would otherwise be claimed by task 7 and chat 7 at once. And chats
  join **gc's claim namespace**: `vincent gc` builds its claim sets from *both*
  tables, so a chat's worktree and its transcripts are not strays and do not
  inflate `GET /v1/info`'s orphan count. The directory name stays informational —
  the rule is still that the claim decides, not the name — but two owners
  resolving to one path is a collision, not a naming preference.

  Keeping chat directories in a root the reclaimer does not scan was considered
  and rejected: it trades a false positive for no gc coverage at all.
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
  - *Amended 2026-08-29 (task 056).* The check runs against `base_sha` when the task
    has one, and against `base_branch` when it does not. Both halves matter: a task
    that wrote nothing but started at a fetched upstream tip is *ahead* of the local
    base branch, so reading the name answers "has commits" and the policy silently
    stops firing for every project whose local base is behind. For the same reason
    the delete is `git branch -D` — never `-d` — in exactly that case: `-d`'s own
    check is "merged into HEAD or its upstream", and HEAD in the project repository
    *is* the stale local base. The `rev-list` against the recorded fork point is the
    better authority, and the guard that matters is unaffected — git refuses to
    delete a branch checked out in any worktree under either flag. Without a recorded
    `base_sha` nothing has been proved against the right commit, so `-d` stays.
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
  - *Amended 2026-08-30 (task 064).* **Neither leg runs on a branch vincent did not
    cut.** Task 008 was designed on the premise that vincent only ever deletes
    branches it created; that premise was implicit until a task could be created from
    a pull request (§13.2). A task made from a **merged** pull request is exactly "no
    commits past its base" — the case this policy fires on — and with
    `delete_remote_branch_on_archive` opted in it would delete a contributor's head
    branch on the forge. Such a task's archive reports `not_ours` and skips both legs;
    the worktree is still removed and pruned. This is also what lets a pull-request
    task carry a real upstream (§10's `--no-track` narrowing) without reopening the
    hazard that flag exists for.
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
  - **Non-directory entries are reported, never removed.** Vincent only ever creates
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

  *Amended 2026-08-25 (issue #142).* A fourth check now sits **between the pause
  and the hold**: a `queued` task that still has a `running` StepRun is refused,
  left queued, and logged once per daemon process. Its previous attempt was
  never finalized, so admitting it would start a second attempt against a first
  the database still calls live — the §12.4 contradiction that recovery now
  fails startup rather than produce. This is the guard for a row that predates
  that fix or arrives by a route nobody has thought of. Nothing in the scheduler
  reconciles such a task, which is why the refusal is permanent for the life of
  the process, why it is logged once rather than every tick, and why
  `GET /v1/doctor` reports the same finding (§17).

*Added 2026-08-29 (task 057).* §13.4's `task_wait` **does not change what a slot
means.** A step blocked in a wait keeps its slot, because its agent process is
live — exactly the `awaiting_input` rule above, and the mirror of
`awaiting_children`, which releases its slot precisely because the parent owns
no process.

Releasing it was considered and rejected. It would create a fourth quadrant no
§6 state occupies today — owning a live agent process *and* holding no slot —
which would redefine what these caps bound, leave live-but-uncounted agent CLIs
accumulating, and (because `awaiting_children` re-queues on wake) let a parked
task sit *behind* the caps after its target had already finished, blowing past
the very ceiling the wait tool promises.

So the deadlock is prevented by **refusal** instead: `task_wait` returns a typed
error, immediately, when the caller is itself a running step and the target
cannot be admitted while the caller holds its slot. A silent hang becomes an
error the agent can act on, no new state is introduced, and these caps keep
their current meaning.

### Chats (task 063, added 2026-08-30)

Chat turns are bounded by their **own** cap, `max_parallel_chats` (default 3),
which counts chats in `running` or `awaiting_input` — the §5.5 states that own a
live agent process, for the same reason `awaiting_input` counts above.

A chat turn is **never queued**. It is not admitted, it does not go through
`internal/scheduler`, and a `send` over the cap is refused with `409`
immediately rather than parked. That preserves the foreground property chats
exist for — a reply never waits behind batch work — and it leaves the "only
`internal/scheduler` performs `queued → running`" invariant exactly as it was,
because a chat turn is never `queued` in the first place.

This is the 2026-08-29 amendment above **extended, not excepted**. That
amendment's cost was named verbatim — live-but-uncounted agent CLIs
accumulating — and that reasoning does not stop applying because the noun
changed from step to turn. So a chat turn is counted; what differs is only that
the response to a full cap is a refusal a human sees rather than a queue a
human waits in.

The two caps are independent by design: a running chat consumes no
`max_parallel_tasks` or per-project slot, and does not delay an admissible task.
The combined ceiling on live agent processes is therefore
`max_parallel_tasks + max_parallel_chats`, which is the honest number and is
documented as such in §12.3.

## 12. The daemon

### 12.1 Binary and commands

One Go binary, `vincent`:

| Command | Behavior |
|---|---|
| `vincent` | Launches the TUI; auto-starts the daemon in the background if unreachable |
| `vincent daemon` | Runs the daemon in the foreground (logs to stderr; for debugging/service managers). `--config-dir`/`--data-dir` pin the §12.2 directories for a manager with no per-process environment |
| `vincent daemon start / stop / status` | Background daemon management (start detaches; stop = graceful shutdown) |
| `vincent daemon logs [-n N] [-f]` | *Added 2026-08-28 (task 047).* Prints the tail of `{data_dir}/logs/daemon.log` (§17), 500 lines by default, `-f` following it on a two-second cadence. It reads the file **from disk and never calls the API**, so it needs no daemon and starts none — it cannot exit 2. A missing file is an error naming the path; an empty one prints nothing and succeeds |
| `vincent daemon backup <path.tar.gz> / restore <path.tar.gz>` | *Added 2026-08-25 (task 030).* One `.tar.gz` of the database (`VACUUM INTO`, §14), `transcripts/`, `config.yaml` and `workflows/`, plus a manifest. `backup` is a thin API client and needs a **running** daemon; `restore` runs client-side and needs a **stopped** one, and refuses a newer schema or an occupied destination without `--force` |
| `vincent service install / uninstall / status` | Registers OS-native autostart, always as the invoking user: launchd agent, systemd user unit, Windows Scheduled Task |
| `vincent workflow ls / validate [file] / render <file> / init <name>` | Registry listing / YAML validation / template dry run / writing a new registry file. *Amended 2026-08-26 (task 034):* `init` writes the §5.2 scope directory a `--project` flag selects — global by default, resolved from §12.2 with **no daemon**; `--project N` needs one, purely to resolve the id to a repository root. `--from <example>` writes an embedded `examples/*.yaml` with its top-level `name:` rewritten. It refuses an existing path (`O_EXCL`) or a name another file in the same scope already declares, and only warns when the name shadows a lower scope. *Added 2026-08-28 (task 044):* `render` executes every template the file declares — `prompt`, `run`, `check`, `instructions`, `if` and `for_each` — against a synthetic §8.4 preview context and prints what each step would send, with the §8.6 triple each agent step resolves to. Where `validate` parses a template, this **executes** it, which is the only way `missingkey=error` catches a typo'd field. It is offline for the same reason `validate` is; `--task`/`--project` reach the daemon for a real task's facts and for registry lookups. Exit 0 clean · 1 a render error · 2 no daemon answered a `--task`/`--project` |
| `vincent task add / ls / show <id> / cancel <id> / follow-up <id>` | Thin API clients for scripting. *Amended 2026-08-25 (task 027):* `follow-up` takes exactly one of `--prompt`, `--run` and `--workflow`, plus optional `--agent`/`--model`/`--effort` (§13.2). *Amended 2026-08-28 (task 045):* `add` fills the §8.1.2 field map from repeatable `--field name=value` and/or `--fields-file <path\|->` |
| `vincent task transcript <id>` | *Added 2026-08-28 (task 047).* Prints one attempt's transcript through `GET /v1/tasks/{id}/steps/{run_id}/transcript` (§13.2). `--step` takes a **step_run id**; omitted, it selects the running attempt, else the newest by run id. Default output is the normalized records rendered as text, `--json` is those records as NDJSON, `--raw` is the agent's own dialect byte for byte. `-f` opens on a tail and resumes from `X-Next-Offset`, ending when that attempt stops running |
| `vincent project add <path> / ls / rm <id>` | Thin API clients for scripting. *Amended 2026-08-28 (task 048):* `rm` deletes the registration and its task rows, forwarding `--force` as `?force`. It never prompts — the daemon's two 409s (`N non-archived task(s)`, and one naming a `running` task) are the confirmation story, and an interactive question would be the first in a command tree whose purpose is scripting |
| `vincent task pause / resume / skip / approve / reject / retry / repair / archive / answer <id>` | *Added 2026-08-28 (task 048).* The rest of §6's human actions, one subcommand each, one id per invocation. All carry `--json` and print the daemon's post-action view of the task; a 409 from the FSM is exit 1 with the daemon's own wording. `retry` takes `--branch` (§18's `branch_exists` recovery) and the edit+retry pair `--prompt`/`--run`; `repair` requires `--prompt` and takes the §8.6 triple; `archive` takes `--force` and surfaces `details.reason: worktree_dirty` with the way out; `answer` takes `--answer <n>=<value>` against the questions `task show` numbers, `--allow`/`--deny` for a permission request, or `--body <file\|->` to post a §13.2 payload verbatim. Each of `--prompt`, `--run` and `--body` has a `-file` twin, and `-` reads stdin |
| `vincent status <message>` | *Added 2026-08-26 (task 036).* Records what the current step is doing, in its own words (§5.4). Runs **from inside a step**: it addresses itself with §8.5's `VINCENT_TASK_ID` and `VINCENT_STEP_ID`, takes no id argument, and errors naming those variables when they are unset. Silent on success — its stdout is the step's transcript |
| `vincent gc [--dry-run] [--force] [--json]` | Reclaims data-root directories no task claims (§10); a thin API client like the rest |
| `vincent config get [key] / set <key> <value>` | *Added 2026-08-30 (task 060).* Reads and writes `config.yaml` through `GET`/`PATCH /v1/config` (§12.3) — a thin API client like the rest, never a second editor, so the CLI and the TUI's editor are one operation with one validation. `get` with no key prints every key as `path = value` in the file's own order; with one, that key's value alone. Keys are the dotted paths the file carries. Lists and argv are whitespace-separated inside a single argument (`notify.on "blocked awaiting_gate"`), which is also why an argv element containing a space has to be edited in the file. A `set` is in force when it answers; `listen` is the exception the command says out loud. Exit 0 · 1 the daemon refused it, with the file byte-identical · 2 no daemon answered |
| `vincent github issues / status --project <id>` | *Added 2026-08-26 (task 035).* Read-only GitHub views: the project's issues newest first, and whether they can be read at all. Thin API clients like the rest — the daemon makes every GitHub call. Nothing under this command writes to GitHub |
| `vincent doctor` | One diagnostic report: paths, daemon, log tail, database, agents, storage, task counts (§17). `--json` for scripting and bug reports; `--fix` (`--force`) reclaims orphaned worktrees and compacts the database. Exit 0 healthy · 1 problems found · 2 no daemon answered. *Amended 2026-08-26 (task 035):* it also reports the GitHub integration — the `github.enabled` toggle, `gh`'s presence, version and login state, whether a token variable is set (its **name**, never its value), and whether issues are readable. It is a **row, not a problem**: every "no" it can report leaves task creation without an issue working exactly as before, so none of it changes the exit code. *Amended 2026-08-29 (task 055):* it also reports the release check (§12.3) — whether `update.check` is on, the latest stable release and when it was last seen, this binary's version, and whether the running daemon is older than it. Rows, not problems, for the same reason: a newer release and a daemon still running the previous build both leave everything working |
| `vincent update [--check] [--dry-run] [--require-signature] [--json]` | *Added 2026-08-29 (task 055).* Asks GitHub for the latest **stable** release and, unless `--check` is given, installs it over this binary. It queries the feed **itself** rather than through the daemon, so it works with no daemon and before the daemon's own check has polled — and so `update.check: false` (§12.3) stays a literal promise. A binary a package manager owns is never modified: the channel is detected from the resolved `os.Executable()` path and its upgrade command is printed. A binary vincent owns is verified before anything runs (§16) and swapped in place; on any failure nothing is replaced. `--check`: exit 0 up to date · 1 the check failed · 2 an update is available. Otherwise: 0 nothing to do or swapped · 1 verification or the swap failed and the binary is untouched · 2 an update exists but this install is package-managed. `--json` carries `swapped`, which separates the two 0s |
| `vincent version` | Build info |

*Added 2026-08-26 (task 035).* `vincent task add --github-issue <n>` creates a
task from a GitHub issue. The flag carries the **number and nothing else**: the
issue is resolved daemon-side (§13.2), so the command line and the TUI's
previewed prefill go through one implementation and cannot drift into producing
different tasks from the same issue. Every other flag still wins over what the
issue would have filled in, and `--title` becomes optional when it is given —
requiring both would make the flag a decoration on a title the user had to
retype.

*Added 2026-08-28 (task 045).* `vincent task add --fields-file <path>` reads the
§8.1.2 field map from one JSON object of **string** values, and `-` reads it from
standard input. It combines with `--field`, which wins name by name: the file is
the base map and the flag typed on the same command line is the more specific of
the two, which is the last-wins rule `--field` already follows extended one level
out. Making them mutually exclusive was rejected — it forces a script that varies
one input to regenerate the whole document.

The client rejects, with exit 1 and before any request is made, a value that is
not a JSON string (naming the **key** and never the value), an empty name,
anything after the first JSON object, and a read over §13.1's 4 MiB large-body
bound — the read is bounded because standard input can be an unbounded pipe, and
answering locally gives the caller the answer the daemon would have given them.
Everything else stays daemon-authoritative: required, `type`, `pattern` and the
per-field bounds are the API's, and declaring `fields:` still does **not** close
the map (§8.1.2). Without `--json`, creation confirms the recorded fields by
**name and count, never value**, read off the response so a field prefilled from
`--github-issue` is confirmed with the rest.

*Added 2026-08-28 (task 047).* The two artifacts a failure is diagnosed from —
the daemon log and a step's transcript — had no command line at all: both were
reachable only from the TUI, or by knowing where the files live. `daemon logs`
and `task transcript` close that, and they close it on opposite sides of the
API boundary, deliberately.

`daemon logs` reads the log off disk rather than through an endpoint, because
an endpoint cannot serve the log in the failure mode that most often sends a
reader to it — a daemon that will not start, or one that is wedged. That is the
same reasoning `LogPath` already carries for clients deriving the path
themselves. `GET /v1/daemon/logs` is **left unbuilt on purpose**: it becomes
right for the first client that is not on the daemon's machine, at which point
the CLI can prefer it and keep the disk read as the fallback. Adding it now
would mean the one client that exists reads the log through the process that
may be what is broken.

`task transcript` is a thin API client like the rest, and needs no daemon-side
change: the endpoint already serves `format=raw|normalized`, `offset`/`tail`
and a record-boundary `X-Next-Offset`. It follows by **polling that endpoint**
rather than subscribing to §13.3's live output stream, and the reason is an
ownership invariant rather than simplicity: live chunks are dropped for a slow
subscriber because the transcript file is the durable copy, and a CLI writing
into a slow pipe is exactly that subscriber — the stream would silently lose
output in the case the command exists for. Reading a transcript is also not a
§6 human action, so task 025's decision that `retry`, `repair`, `skip` and
`approve` stay TUI-and-API only is untouched: that decision is about writes.

*Amended 2026-08-15 (task 005).* `gc` breaks this table's noun-verb pattern
(`project add`, `task ls`) knowingly: `git gc` is the idiom users already have, and the
scope spans two directory trees — worktrees and transcripts — so a `worktree` noun
would have been wrong on the day it shipped.

*Added 2026-08-26 (task 036).* `status` is the second command in this table
invoked by a *program* rather than by a human at a prompt, after `follow-up`,
and it is the reason the noun-verb pattern is broken again: there is no noun. It
does not act on a task the caller names, it reports on the step the caller *is*,
which is also why it takes its addressing from the environment rather than from
flags nobody would be there to type. It is a thin client for
`POST /v1/tasks/{id}/steps/{step_id}/status` (§13.2) and carries `--json` like
the rest.

*Added 2026-08-25 (task 027).* `follow_up` is the one §6 human action with a
command line. `retry`, `repair`, `skip` and `approve` are deliberately
TUI-and-API only, and stay that way; the reason to break with them here is that
"rebase these six finished branches onto current master" is a batch, and a batch
wants a shell loop rather than six visits to a form. The unevenness that leaves
is accepted rather than papered over — giving every human action a command line
is separate work.

*Amended 2026-08-28 (task 048).* That separate work is this one, and "stay that
way" no longer holds: every §6 human action has a command line. The reasoning
above is kept rather than deleted because it is what lost. What it did not
weigh is that the actions it left out are the ones a **blocked** task needs, so
the recovery half of the product was reachable from exactly one client — the
heavyweight interactive one — which contradicts §2's own claim that the daemon
owns the work and clients are disposable. An agent auth outage is the case that
settles it: `agent_unauthenticated` blocks each task once the retry budget is
spent (§7.2), waiting fixes nothing, and a board full of those blocks could not
be cleared from a script. `follow-up` remains the action whose *motivation* was
a batch; the rest are here because a client that cannot unblock work is not a
client.

*Added 2026-08-25 (task 030).* `daemon restore` is a **stated exception** to
"clients never touch the DB" (§4), and is written down here rather than left to
be noticed. The invariant is that only the daemon *opens* SQLite; restore opens
nothing. It probes the single-instance lock, refuses unless the daemon is down,
reads the archive's `manifest.json` for the schema version — never the database
— and then moves files. It cannot be an endpoint for the same reason: the
daemon whose files it replaces has to be gone before it is safe to run.

`daemon backup` takes the opposite side of the same rule and refuses without a
daemon, in `doctor --fix`'s words: only the daemon opens the database, so only
the daemon can copy it. There is no `--cold` flag. That is not a hardship in
§18's corrupt-database case — what rescues a corrupt database is an *earlier*
good copy, not a fresh copy of the damage — and the documentation keeps "stop
the daemon, then copy `vincent.db`, `vincent.db-wal` and `vincent.db-shm`
together" as the no-binary fallback, which is also the honest answer for a
daemon that will not start.

*Added 2026-08-29 (task 055).* `vincent update` is the **second stated
exception** to "the daemon owns everything" (§4), beside `daemon restore`'s
above, and for the same kind of reason twice over. The operation must work with
**no daemon** — the user this feature exists for is on a direct-download binary
and may never have started one — and a daemon cannot cleanly rewrite its own
running image on Windows, where the running executable can be renamed aside but
not overwritten. So the CLI downloads, verifies and swaps; what the daemon keeps
is the background check and the cached answer (§12.3, §13.2).

The swap changes the binary and nothing else. It drains nothing, pauses nothing
and kills nothing: the running daemon keeps its old code until it is restarted,
which is a state `vincent daemon status` and `vincent doctor` both report and
neither treats as a fault. Applying an update is never automatic — agents run
full-auto (§16), and swapping the orchestrator underneath running tasks with no
human in the loop is not something vincent does quietly. There is also no
prompt: the command is already the explicit human act, and this tree does not
prompt because its purpose is scripting (task 048). `--dry-run` prints what
would happen.

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
  tui.json                   # TUI-local view state: the §16 first-run acknowledgment, the board's collapsed groups (§15)
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
- **Persisted, not fsynced.** Vincent writes and closes, and checks both. It
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
max_parallel_chats: 3        # chats holding a live agent process (§11, §5.5); over it a send is 409, never queued
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h           # max wait in awaiting_input (§7.4)
delete_empty_branch_on_archive: true   # archive deletes a branch with no commits past its base (§10)
delete_remote_branch_on_archive: false # …and its upstream counterpart; attended archive only
fetch_base_branch: true        # refresh base_branch from its upstream before cutting a worktree (§10)
transcript_retention_days: 90   # transcripts of *archived* tasks older than this are pruned
transcript_max_bytes: 512MB     # per-run transcript cap (§18); past it the step fails `transcript_limit`
max_task_cost_usd: 0            # per-task spend ceiling (§17, §18); 0 = no cap
usage_limit_recheck_interval: 15m  # how long a quota-held task waits when the CLI named no reset (§11)
parallel:
  max_parallel: 4            # sub-steps of one `parallel` group at once (§7.5); the §11 caps do not see these
log_level: info
debug: false                 # record each step's resolved settings and full argv in its transcript
environment:                 # what child processes inherit (T4.23)
  inherit: all               # all (default) | none | [PATH, HOME, …]; an empty list means none
                             # a containerized step reads `all` as `none` (§8.5, task 061)
  unset: []                  # names dropped after inherit
  set: {}                    # literal values, applied last; no expansion
agents:
  claude: { path: "" }         # "" = resolve from PATH
  codex:  { path: "" }
  cursor: { path: "" }         # resolves `cursor-agent`, never `cursor` (§9.7)
github:
  enabled: true                # read GitHub issues and pull requests (§13.2)
  poll_interval: 5m            # reconcile task↔pull-request links this often; 0 = off (task 052)
update:                        # check for a newer vincent release (task 055)
  check: true                  # opt-out; false = the daemon makes no such request at all
  poll_interval: 24h           # 0 = off, same as check: false; negative is refused
# `notify:` is silent on chats (task 063, added 2026-08-30). It exists for
# unattended work that finishes hours after the human left; a chat turn ends
# while its human is looking at it, and a hook that fired on every turn would be
# noise on the one signal that is meant to be rare. `internal/config` therefore
# gains no import of the chat state package, and task 046 decision 4's
# arrangement — config imports `taskstate` and only `taskstate` — is untouched.
# If `awaiting_input` on a long-open chat proves to need one, the named trigger
# is a separate `notify.chat_on` key, not a widening of `notify.on`.
notify:                        # run a command when a task enters one of these states (task 046)
  on: []                       # §6 state names; [] (the default) fires nothing
  command: []                  # argv, never a shell string; the envelope arrives on stdin
mcp:                           # the §13.4 MCP server (task 057)
  wire_steps: true             # give vincent's own agent steps the tool list; opt-out
  max_depth: 3                 # how deep tasks created over MCP may chain
  max_tasks: 32                # how many tasks one MCP creation chain may hold
container:                     # run a task's steps in a container (§16, task 061)
  image: ""                    # "" (default) = every step runs on this host
  runtime: docker              # a docker-CLI-compatible binary; only docker is verified in CI
  mount_agent_config: true     # bind-mount ~/.claude, ~/.codex, ~/.cursor read-write
  network: true                # false drops the container off the network entirely
  extra_mounts: []             # host:container[:ro]; the repo and worktree are mounted already
tui:                           # view preference; the daemon validates and relays it (§15)
  board:
    group_by: [project, workflow]  # task-table grouping, outermost first; [] = flat
```

**`container:` (task 061, added 2026-08-30).** `image` is the whole switch and
`""` is the default: no image means every step runs on this host, no runtime is
consulted, and an existing installation is byte-for-byte unchanged. Set it and
the task's step processes run inside **one** container, created with the task's
worktree and removed with it. *As of task 061 that is every `command` step, and
every `check:` — including a check hanging off an agent step; a `manual` step
runs no process, so containerizing it is vacuous. The **agent** process itself
is still spawned on the host: moving it needs a spawn seam across all three
adapters and is task 062. Until that lands, a containerized task whose workflow
has agent steps is a mixed run, and it is neither refused nor warned about.* The
image is the user's: it must already carry the agent CLI a workflow's agent
steps resolve to, and `git`. Vincent builds nothing, publishes nothing and
bundles nothing, the posture it already takes toward `gh` and `cosign`.

The block resolves at **two** levels — a workflow's `defaults.container:` over
this one, per field (task 061 decision 6). There is no task level, no
`POST /v1/tasks` field and no CLI flag; §20 records the trigger for adding one.
`runtime` names a docker-CLI-compatible binary, and only `docker` is verified in
CI — podman and nerdctl are accepted because they take the same argv, which is
a different claim from "tested". The repository and the worktree are mounted
**at their own absolute host paths**, so §8.4's `.Worktree` and §8.5's
`VINCENT_WORKTREE` are true on both sides and no path in a workflow means two
things; `extra_mounts` is for anything else, and both sides of each entry are
validated as `/`-rooted paths on every platform rather than by the host's own
rule — the only daemon that acts on the key runs Linux containers, so a shared
`config.yaml` must not fail to *load* on a Windows machine over a mount that
machine will never make. Reads happen per admission, so a hot reload governs
the next task admitted rather than one already running.

What is refused, and where (task 061 decision 3):

| Condition | Where | Outcome |
|---|---|---|
| The daemon runs on **Windows** | task creation | `400 validation_failed` — a `C:\...` path cannot exist in a Linux container, and paths are identical inside and out |
| `runtime` is missing or cannot talk to a daemon | task creation | `400 validation_failed` — cheap, local, one `docker version` |
| `network: false` with `mcp.wire_steps: true` | task creation | `400 validation_failed` — a container with no network cannot reach the daemon's per-step MCP endpoint |
| A step pins `shell: pwsh` or `shell: cmd` | load (workflow pins its own image) or task creation | validation error naming the step (§8.3) |
| The image is missing and cannot be pulled | **admission** | task blocks `container_image_unavailable`, before a worktree, a branch or a retry is spent |
| The runtime disappeared under a created task | **admission** | task blocks `container_unavailable` |

The image check is an admission block rather than a creation refusal on
purpose: pulling inside `POST /v1/tasks` runs a multi-gigabyte download against
§13.1's request timeouts, and inspecting local-only would `400` every first run
on a fresh machine. Task 041 decision 4 re-affirms task 003 decision 4 — there
is no pre-flight refusal on an unhealthy environment — and an image's contents
sit on that side of the line. A containerized step is never quietly run on the
host *because the runtime or the image failed*: that would invert the choice
the workflow made, which is §9.4's reasoning verbatim. It is not a claim about
agent steps, which task 061 has not moved into the container at all.

**`mcp:` (task 057, added 2026-08-29).** There is deliberately **no `enabled`
key.** `/mcp` is part of the API surface the way `/v1` is — same listener, same
bearer token — so "serving MCP" is not a mode the daemon is in. What a user can
meaningfully turn off is vincent wiring the server into its *own* agent steps,
which is `wire_steps`. It defaults **true**, an opt-out on the same reasoning as
`github.enabled` (task 035 decision 6): the whole point of the work is that a
step's agent has the tools without anyone configuring anything, and one line
turns it off. `max_depth` and `max_tasks` bound a chain of tasks created over
MCP; both are read in the task-creation path, so a reload governs the next task
rather than anything already running.

**`github.poll_interval` and the pull-request reconciler (task 052, added
2026-08-29).** Every `poll_interval` the daemon lists each GitHub-based
project's **open** pull requests and links the ones whose `head` branch equals a
task's `branch_name` within that project, marking the link `source: auto`. The
branch is the ground truth — vincent named it (§10) — and the stored link is a
durable cache of it, which is what lets a task still name a pull request after
that pull request merged and dropped off an open-only listing.

It is a daemon subsystem wired in `internal/daemon.Run` beside the scheduler and
the notifier, not a side effect of the listing endpoint (§13.2): a link written
only when a human opens a screen exists only for the projects somebody happened
to open, and a GET that mutates rows is a shape no other write in this API
takes. It reads the config per tick, so a reload governs the next one.

The §13.2 gate runs **first and stops at the first "no"**, so a disabled
integration or a non-GitHub project makes no call on this path either. It never
overwrites a `human` link, never clears one and never un-suppresses one. Its
failure policy is deliberately **quiet**: a rate-limited or unreachable GitHub
degrades to "no new links this tick" and logs at debug — never a per-tick error
storm, and never a task state change.

`poll_interval: 0` switches the reconciler off while leaving the rest of the
integration on. It must be refusable without refusing `github.enabled` entirely.

*Amended 2026-08-29 (task 055).* This was the daemon's **first** standing
outbound network traffic when it landed, and that sentence read as though it
were the only one. It is now the first that fires for a *subset* of installs:
the gate above stops at the first "no", so a daemon with no GitHub-origin
project makes no call under this key. The release check below is the first that
fires for **every** install, which is why it carries its own switch rather than
riding this one.

**`update` and the release check (task 055, added 2026-08-29).** Every
`update.poll_interval` the daemon asks GitHub for vincent's latest **stable**
release and caches the answer in memory, which `GET /v1/update` (§13.2),
`vincent doctor` and `vincent daemon status` render. It is another daemon
subsystem wired in `internal/daemon.Run` beside the scheduler, the notifier and
the pull-request reconciler, with the same posture: one goroutine, config read
per tick so a reload governs the next one, and a **quiet** failure policy —
offline, rate-limited and malformed all degrade to "no new answer this tick" at
debug level, and the previously cached answer survives.

The call is one unauthenticated GET with no identifying header (§16). Stable-only
is enforced twice: `releases/latest` excludes drafts and prereleases server-side,
which already honours `.goreleaser.yaml`'s `prerelease: auto`, and a tag carrying
a semver prerelease suffix is rejected client-side so the guarantee does not rest
on one API's documented behaviour. Comparison normalizes the `v` prefix —
goreleaser injects `{{.Version}}` without one while tags carry one — and a `dev`
build is never reported as behind.

Either `check: false` or `poll_interval: 0` stops the poller; a negative interval
refuses the file, because rounding a typo to "do not poll" would look like it
worked. The cache is in memory and not in SQLite: a restart re-polls, no
migration is needed, and §12.4's "persist before acting" governs task transitions,
which this is not.

**`vincent update --check` does not go through the daemon**, and neither does
`vincent update` (§12.1). That is what makes `check: false` a literal promise —
with the poller off the daemon makes no request, and only an explicit command
does — and it is what makes the check answer before the first poll and with no
daemon running. The endpoint therefore serves the cache and never refreshes: a
`?refresh` parameter would hand any client the ability to make the request the
user disabled.

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

**`max_task_cost_usd` (task 033, added 2026-08-26).** A ceiling, in US dollars,
on what **one task** may spend — the §17 rollup of `cost_usd` over every attempt
of every step it runs, retries included. Past it the task goes `blocked` with
`block_reason = cost_limit` (§18). Zero, which is the default, is no cap, so
nothing changes for anyone who does not ask; a negative value fails the load.
It sits at the top level beside `transcript_max_bytes` rather than under
`defaults:`, which is timeouts a step may override — a budget is not something a
step inherits — and it is a plain number rather than a `Duration`- or
`ByteSize`-style string because USD is already the unit and it is in the key
name. Read per check, so a hot reload reaches a task that is already running.

It counts **one task**, which is not the same as one *tree*: a `fan_out` lane is
an ordinary task row (§7.6), so a twenty-lane tree may spend twenty times this
before any single row trips, and the parent's own rollup never sees a lane's
spend. A per-tree cap was considered and deferred — it needs a recursive rollup
over `parent_task_id` and a rule for which task blocks when the total trips — and
the multiplication is documented here rather than worked around. It is also
inert on the adapters that report no cost: codex (§9.3) and cursor (§9.7) leave
`cost_usd` unset, and the check is guarded by "some attempt reported a cost"
rather than by arithmetic, so a cap must never be estimated from token counts.

**`fetch_base_branch` (task 056, added 2026-08-29).** Refreshes a task's base branch
from its own configured upstream before the worktree is created, and starts the task
branch at the fetched commit (§10). Default **true**: without it every task builds on
a base as stale as the human's last `git pull`, which is the failure this key exists
to end, and default-on outbound traffic needs no separate argument — `github.enabled`
already defaults true and §26 settled that posture; a fetch reads. `false` restores
the pre-056 behaviour exactly — the local ref, and no `base_sha` recorded — for a
repository where fetching is slow or needs interactive auth. Read per worktree
creation, so a hot reload reaches the next admission. There is deliberately **no
per-project override yet**: the global key is the escape hatch, and a per-project one
is its own piece of work if a real repository needs the granularity.

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

**`github` (task 035, added 2026-08-26).** Whether the daemon may read GitHub
issues, so a task can be created from one (§5.3, §13.2, §15). It governs
**reading only**: nothing under this key writes to GitHub, and no call is made
from a step — the daemon calls at pick time and at create time and nowhere else.

It is an opt-**out**, defaulting to `true`. It is inert on every project whose
`origin` is not a github.com repository, and makes no call at all until a human
opens the issue picker or names an issue on the command line, so on by default
costs nothing unasked for. Setting it to `false` stops the daemon reading
GitHub entirely: the TUI's issue row disappears, `GET
/v1/projects/{id}/github` answers `disabled`, and `github_issue` on `POST
/v1/tasks` is refused.

**`notify` (task 046, added 2026-08-28; issue #90).** The daemon's outward
signal. When a task enters one of the states in `on`, the daemon runs `command`
and writes a JSON envelope describing the transition to the child's stdin.

It exists because §2's third goal is a daemon that runs with **zero clients
attached**, and the one thing it could not do with zero clients attached was say
it needed a human: the only alert in the tree is the TUI's terminal bell (§15),
which rings on a transition into `awaiting_input` and only while a board is
open. A task could sit in `awaiting_input` for the whole 24-hour `input_timeout`
(§7.4), fail on expiry, and the first anyone knew was the next time they opened
the board. `blocked` and `awaiting_gate` had no alert at all.

*The selector is target **states**, not event types.* `on` lists §6 state names,
matched against the `to` field of `task.state_changed` — exactly what the TUI
bell keys off. No event type is introduced for this (§13.3). A name outside §6's
vocabulary **fails the load**, naming the offending value, so a typo is refused
whole and the watcher keeps the last good configuration.

*The payload is an enriched envelope, assembled by the daemon*, not the raw
`events` row: a notifier handed `{task_id, to}` cannot write a message without
calling back into the API with a bearer token, which defeats the point of a
one-line script. One JSON object, on stdin: `event_id`, `ts`, `type` (always
`task.state_changed`); `task_id`, `title`, `from`, `to`, `block_reason` (empty
unless `to` is `blocked` — §14's rule that a block reason means "set while
blocked"), `queued_reason`, `current_step`, `steps_total`, `worktree_path`,
`branch`; `project_id`, `project`, `workflow`; and `input` (`{kind, summary}`)
on a transition into `awaiting_input`, taken from what that §7.4 transition
already carries. `steps_total` comes from the task's own workflow snapshot,
which is the honest *n* for that run.

*Global, not per-project, and hot-reloading.* Projects are database rows, not
YAML, so a per-project override would need a column and API surface for a case
nobody has asked for. The hook reads the current configuration per event, so an
edit takes effect on the next transition with no restart.

*Only **root** tasks notify.* A `fan_out` lane is an ordinary task row (§7.6),
so a twenty-lane tree reaching `done` would otherwise produce twenty child
notifications on top of the parent's. A task whose `parent_task_id` is non-null
is skipped: the parent's own `awaiting_children` → `running` → `done` is the
human-meaningful signal, a lane that blocks blocks its parent's join, and a lane
finishing is machinery. There is deliberately no `include_children` key.

*Delivery is fire-and-forget, bounded, and does not replay.* At most **4**
notifier processes run at once, drained from a bounded **64**-entry FIFO;
`command` is argv and never a shell string, because there is no portable shell
to assume; a child gets a **fixed 10 s** and then its whole process tree is
killed. The timeout is not configurable, and that is the same posture as
transcript pruning: a daemon that stops serving because a notifier hung has its
priorities backwards. Only a full *queue* drops — the ordinary burst of five
tasks blocking at once, which is exactly when the feature earns its keep, is
lossless. Failures are logged and never retried, nothing is persisted, and
**nothing is replayed on restart**: a weekend of downtime must not produce a
notification storm on the next start.

Children inherit the `environment` policy above like every other process the
daemon spawns, and get **no** `VINCENT_*` variables — those are §8.5's contract
for command steps, and the envelope on stdin is this hook's.

Both keys are needed. `command` with an empty `on`, and `on` with no `command`,
both load, take effect and can never fire; each is a startup/reload **warning**
rather than a load failure, for the reason
`delete_remote_branch_on_archive`'s is: commenting `command` out for an
afternoon should not revert every unrelated edit in the same save.

`notify` is deliberately **not** exposed on `GET /v1/config`: that response is a
curated DTO for clients (§13.2), no client needs this, and `command` can
reasonably carry a webhook URL with a token in its argv (§16).

*Amended 2026-08-30 (task 060, issue #244).* **Superseded: `notify` is served,
values included.** The endpoint serves every key in `config.yaml`, because a
key it omits is a key no client can see — the TUI reads no configuration of its
own (§15) — and this task makes the file editable from those clients, which
cannot be done to a value that cannot first be read. The disclosure argument
does not survive the boundary being named: `GET /v1/config` is loopback-only
behind the 0600 bearer token, which is the same trust boundary as the 0600
file, so anyone who can call it can already `cat` it. What the argument was
really protecting is the **log** and step transcripts, and that rule is
unchanged and narrowed to say so — the daemon still logs variable names and
never values. The one place the old reading survives is the MCP rendering of
this route, where the result lands in an agent's context and its transcript:
`config_get` masks `environment.set`'s values and `notify.command`'s argv, and
nothing else (§13.4).

There is deliberately **no token key here**. Vincent stores no credential of
its own: it drives `gh` when that is installed and authenticated, and otherwise
reads `GITHUB_TOKEN` or `GH_TOKEN` out of the environment the daemon already
inherited (§2's "secret management" non-goal, decision record row 26). Read per
use, so a hot reload governs the next call rather than requiring a restart.

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

*Amended 2026-08-30 (task 060, issue #244).* **The API also writes it:
`PATCH /v1/config`.** The file stays authoritative — the daemon edits it and
reloads from it; it is not a cache of anything. The endpoint is partial and
snake_case, mirroring the read shape, on the pattern `PATCH /v1/projects/{id}`
and `PATCH /v1/tasks/{id}` already set. What it guarantees:

- **The write is comment-preserving.** `config.yaml` ships as a documented
  template whose `notify:` block is commented out, and for most installations
  it is the only explanation of what the keys mean. The daemon edits a key in
  place, uncomments a documented block where it stands, and appends only a key
  the file has no block for at all. Comments, key order and blank lines survive
  (`config.Apply`).
- **Nothing is written when the patch does not hold.** The candidate file is
  decoded through the path `Load` takes and checked against
  `worktree.ValidateBranchTemplate`, and a refusal answers §13.1's envelope
  with the file byte-identical. There is no partial application.
- **The write is atomic and 0600.** A rename over the target, from a temporary
  file beside it named so the watcher's base-name filter ignores it (§12.2).
- **The result is applied before the response is sent.** `daemon.Run`'s reload
  callback is a named function with two callers — the fsnotify watcher and this
  handler — so the `listen` pin and the `branch_template` fallback are the same
  code on both paths, and it is idempotent: the watcher's later fire re-reads
  identical bytes. A `GET` issued the instant a `200` lands reads the new
  values, with no sleep.
- **`listen` is written and does not take effect.** The reload rule above is
  unchanged, so the running daemon keeps the address it bound and `GET
  /v1/config` goes on reporting it. Clients say "takes effect on restart"
  rather than showing the pending value as though it were in force.
- **Concurrent patches serialize; a hand-edit racing one is last-writer-wins.**
  One mutex around the read-modify-write, and the file is read fresh at patch
  time. There is no `ETag`/`If-Match`: a precondition concept no other endpoint
  in this API carries, for a race between a human and themselves.

`PATCH /v1/config` is **not** an MCP tool (§13.4), and the four keys that decide
what the daemon executes or exposes — `notify.command`, `environment`,
`agents.*.path` and `listen` — are behind an explicit confirmation in the TUI
(§15).

*Amended 2026-08-30 (task 065, issue #261).* **The workflow write routes carry
the same posture, with one deliberate difference.** `POST` and
`PATCH /v1/workflows` (§13.2) are line-oriented, write nothing when the
candidate does not parse, write atomically, and put the result in force before
answering — every one of the guarantees above, for the same reasons. What
differs is the precondition: a workflow file has second writers that are not
the same human (§5.2), so a `PATCH` carries a version token and a file that
moved underneath is a 409. The refusal of preconditions in the last bullet is
about `config.yaml` and stays about `config.yaml`.

### 12.4 Crash recovery

- Before starting any step process, the daemon persists the StepRun (`running`) with
  the child PID and start time once spawned.
- On startup: any StepRun still marked `running` is finalized as `interrupted`; if its
  recorded PID still exists *and* the process holding it is provably the one the row
  spawned (the guard against PID reuse — see the 2026-08-26 amendment below), the
  process is killed
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
- *Added 2026-08-30 (task 061).* A **containerized** run journals the container
  it ran in alongside its PID (`step_runs.container_id`, migration 0021). The
  host PID such a row carries names the runtime *client*, not the process inside
  the container, so recovery does not rely on it: it reads the container id,
  confirms the container still carries the `com.vincent.task` label naming this
  task, and **removes the container**, which kills every process inside it. The
  label check is the PID-reuse rule from the other direction — a container id is
  never reused, so there is nothing to guard there, but a container whose label
  names a *different* task is somebody else's and what cannot be proved is not
  killed. `procx.Identity` and the PID-reuse guard below are untouched for host
  steps, and still apply to the runtime client the daemon really did spawn. A
  **step** timeout, cancel or graceful shutdown is the opposite of this and does
  **not** remove the container: it signals the process inside by the pid file the
  step wrote to a container-private scratch mount, waits the same 15 s, then
  kills. The task's container survives a step, so a retry finds whatever an
  earlier step installed.
- *Added 2026-08-15 (task 005).* Recovery reconciles **rows and processes, not
  directories**. The directory tree is reconciled by a separate startup pass that only
  reports (§10): it logs one warning per orphan and raises the `orphans` count on
  `GET /v1/info`, and it deletes nothing — `vincent gc` does that, with a human behind
  it.

*Amended 2026-08-30 (task 063) — chat turns are the exception to the rule
above.* A chat turn (§5.5) found `running` on startup is finalized
`interrupted` and is **not re-run**, and its chat returns to `idle`. The
bullet's justification is what stops applying: re-running a step is safe by
construction because the step is a *fresh session over a surviving worktree*,
and a chat turn is neither half of that. Re-running one would re-send the
human's message without their asking, into a session that died with the
process — so the outcome would be a message they did not send answered without
the context they expected.

Everything else carries over verbatim: the same `procx.Identity` PID-reuse
guard before killing a verified orphan, the same fail-closed atomic transaction
shape as `store.InterruptTask`, and the same "rows and processes, not
directories" rule. The human sees the interrupted turn against the
conversation and decides whether to say it again.

*Amended 2026-08-25 (issue #142).* Recovery is **fail-closed and atomic per
task**. Finalizing a task's `running` StepRuns and re-queueing the task are one
store transaction (`store.InterruptTask`), so the order above can never come
apart: the daemon cannot hand the scheduler a `queued` task whose previous
attempt the database still calls `running`. A task whose transaction will not
commit is left exactly as found — recoverable, not re-queued — and the failure
**stops daemon startup** rather than being logged and walked past; continuing
past a storage failure is least defensible when storage is what is failing.
Re-running recovery converges: the second pass finds the same open rows and the
same `from` state, and the compare-and-swap refuses a task already reconciled,
so no duplicate StepRun, event or retry consumption is possible. Killing an
orphan stays outside the transaction and before it — a kill cannot be rolled
back, and killing then failing to commit is the already-tolerated case, since
the next recovery finds a dead PID.

Two surfaces guard the same invariant from the other side. Admission (§11)
refuses a `queued` task that still has a `running` StepRun, leaving it queued
and logging why once per daemon process; and `GET /v1/doctor`'s `problems[]`
(§17) reports the impossible combination — a `running` StepRun under a task
that is `queued`, `done`, `aborted` or `archived` — naming the task. The
waiting states are excluded deliberately: a `running` row is *correct* under
`awaiting_input`, where a live process waits for an answer (§7.4), and under
`awaiting_gate`, whose manual row its actor writes open before exiting (§6).

*Amended 2026-08-17 (task 014).* A `fan_out` join interrupted mid-merge is
recovered the same way any step is — the attempt is `interrupted` and re-runs
— with one extra move: if a merge is still in progress in the worktree, it is
aborted before the lanes are re-merged from the top, which is a no-op for the
ones already in. Recovery is the **only** path allowed to abort. A human retry
after a `merge_conflict` block finds the same in-progress merge and must
commit their resolution instead; the two are told apart by how the previous
attempt ended, read before the new attempt's row exists.

*Amended 2026-08-26 (issue #149, task 031).* The PID-reuse guard compares a
**platform-native process identity**, exactly, and no longer a wall clock
within a tolerance. Beside the PID and `proc_started_at`, a spawn journals
`step_runs.proc_identity` (migration 0013): an opaque, versioned, per-OS token
— on Linux the raw start-tick count from `/proc/<pid>/stat` joined with
`/proc/sys/kernel/random/boot_id`, on macOS the `kinfo_proc` fork stamp to the
microsecond, on Windows the creation `FILETIME` in its raw 100 ns unit — each
of them ending in the PID it belongs to, because every platform's stamp is a
tick-wide bucket rather than an instant and processes started inside one share
it. Recovery reads the token again and kills only on a byte-for-byte match; it
never parses one. What remains after the pairing is one narrow case, and it is
the same on all three: a PID **reused inside a single tick** of the platform's
clock. This supersedes the PR D decision recorded in
`docs/history/v0-tasks.md` — "within ±5 s of the journaled spawn time" — which
compared the daemon's own clock against kernel bookkeeping and so had to
tolerate a window a reused PID could theoretically fall inside. Keeping the
Linux value as a count since boot rather than an absolute instant is what makes
it immune to an NTP step or a suspend/resume, and the boot id makes a reboot a
guaranteed mismatch.

The ±5 s comparison survives as the **fallback for a row with no identity** —
written before 0013, or by a spawn whose identity read failed, which is a real
case rather than a hypothetical one. No installation is worse off than it was,
and the rule underneath is untouched in both branches: *what cannot be proved
is not killed.* An identity that cannot be read during recovery, when one was
journaled, is never a kill. A mismatch is a logged warning and nothing more —
the task re-queues normally, there is no new block reason and no doctor
problem.

*Added 2026-08-29 (task 057).* Recovery also removes a leftover
`.cursor/mcp.json` from every live task's worktree. The cursor adapter writes
that file for the duration of an agent run (§9.7) and removes it in `Wait`; a
daemon that died mid-step never got there, and the file is untracked inside a
git worktree — so a leftover shows up in `git status`, in the task diff and in
dirty detection, on a task that is about to be re-queued. A removal failure is
logged rather than fatal: its token died with the daemon that minted it, so a
stale copy is a nuisance and not a correctness problem.

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

  *Amended 2026-08-26 (issue #197).* A `fields` key and an `answers` key are
  bounded **separately**, because they are not the same kind of thing. A `fields`
  key is a caller-chosen identifier a human or a workflow author types (§8.1.2),
  and its bound is sized for one. An `answers` key is not chosen by the caller:
  it is the agent's verbatim question text, which §7.4 makes the lookup key and
  §9.2 writes back to the CLI unchanged, so no layer between the agent and the
  answer route may shorten it. It is therefore bounded as agent-authored text —
  the size an answer *value* gets — not as an identifier. Bounding the two alike
  made any question past the identifier bound unanswerable: the daemon parked on,
  persisted and rendered a question it then refused every answer to, leaving the
  task holding its slot in `awaiting_input` until it was cancelled or timed out.
  The count bounds and the route's body bound are unchanged, so nothing about the
  key is unbounded. This section still fixes no numbers; `docs/reference/api.md`
  publishes them.
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

*Amended 2026-08-28 (task 040, issue #146).* A request may carry an optional
**`Idempotency-Key`** header, and exactly one route acts on it:
`POST /v1/tasks`. That route is the only one in §13.2 where a replayed request
produces a second side effect — it inserts a row, claims a branch and wakes the
scheduler, none of which is a compare-and-swap, so a client that times out
*after* the commit and re-sends gets a second task, a second worktree and a
second agent run against the same repository. Every other mutating route is
already safe and ignores the header: the §6 actions are a compare-and-swap on
the state the request read (amended 2026-08-24), `POST /v1/projects` refuses an
already-registered path, and the `PATCH`es, `DELETE /v1/projects/{id}`,
`POST /v1/maintenance/gc` and `POST /v1/doctor/fix` are desired-state
operations that reach the same end state when re-sent.

- **The key** is at most 255 bytes of printable ASCII; anything else is `400`
  `validation_failed` naming the field and the limit, like every other §13.1
  field bound. It is scoped `(method, path, key)`, which is the whole of what
  exists to scope by: there is one daemon, one token and no caller identity.
- **The digest** is taken over the *decoded* request, canonically re-marshalled
  — not over the bytes as they arrived — so whitespace and JSON key order
  cannot manufacture a conflict. It is taken **before** the `github_issue`
  prefill mutates the request, so an issue edited between two identical sends
  cannot either.
- **Same key, same digest** replays: `201` carrying the task the first request
  created. The stored row is a *reference*, not a recorded response body, and
  the replay renders the task **as it is now** — so a task the scheduler has
  since admitted replays as `state: running` under a `201`. Persisting the
  rendered JSON instead would put a workflow snapshot under this section's
  4 MiB bound into a table that grows with every create.
- **Same key, a different digest** is `409` `invalid_state` with
  `details.reason = "idempotency_key_reused"`, and no task is created. It is
  deliberately **not** a new error code: this section fixes every `409` at
  `invalid_state` with the specific reason in `details`, and that rule holds.
- **Retention** is a fixed **24 hours**, pruned by the daemon's existing
  retention pass (§17). Fixed the way this section's body bounds are fixed: a
  key exists to cover a transport retry, which happens in seconds.
- **No client sends it.** `internal/apiclient` has no REST retry — a create
  that times out is reported to the person, who looks at the board and decides —
  so there is no retry for a key to survive, and minting one per composed form
  would fight the rule that a new explicit user action is a new operation. The
  header is for external callers.

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
                                        (§13.1); the shape of a user's disk does not belong on it.
                                        *Added 2026-08-25 (task 029):* a `database` object —
                                        { path, size_bytes, wal_bytes, shm_bytes, total_bytes }.
                                        Byte figures only, by the same cheapness rule that admits
                                        `orphans`: three os.Stat calls per request. The row counts,
                                        the retention span and the workflow-snapshot total are
                                        scans and ride /v1/doctor instead — this endpoint is
                                        polled by the board, the projects view and the daemon view
                                        on every debounced refresh, and a COUNT(*) over a
                                        multi-million-row events table on the daemon's single
                                        SQLite connection is not that. Nothing here is cached
GET    /v1/config                       effective global config
                                        *Amended 2026-08-30 (task 060, issue #244):* it is no
                                        longer read-only, and no longer a subset. Every key in
                                        config.yaml is served, values included — a key omitted
                                        here is one no client can see (§12.3's amendment).
PATCH  /v1/config                       partial, snake_case, mirroring the read shape. The
                                        daemon validates the whole candidate file, writes it
                                        comment-preservingly and atomically at 0600, and applies
                                        it before answering. An invalid patch writes nothing and
                                        answers §13.1's envelope. Not an MCP tool (§13.4)
GET    /v1/agents                       per-adapter availability + model/effort options (§9.6);
                                        ?refresh=true forces a re-probe.
                                        *Added 2026-08-28 (task 041):* the §9.5 health facets
                                        `version_verdict`, `tested_versions` and
                                        `restricted_verdict`, siblings of `input_verdict`. All
                                        three also ride /v1/info's `agents[]` and
                                        /v1/doctor's `agents[]`, alongside `supports_input`, so
                                        a client reads one adapter the same way on all three
GET    /v1/doctor                       the whole §17 diagnostic in one body: paths, daemon,
                                        log (stat + tail), database (size, schema version,
                                        integrity_check), agents, storage (disk free, worktree
                                        count/bytes, orphans), tasks (counts by state), plus
                                        `problems[]` — the closed set that makes
                                        `vincent doctor` exit 1. Read-only. Agent availability
                                        is re-probed unconditionally (§9.6): auth state is not
                                        a function of the binary.
                                        *Amended 2026-08-25 (task 029):* the database group also
                                        carries `wal_bytes`, `shm_bytes`, `total_bytes`,
                                        `table_rows` (every table in the schema with its row
                                        count, enumerated from sqlite_master so a later
                                        migration's table appears with no code change),
                                        `oldest_event_at` (null on an install with no events) and
                                        `workflow_snapshot_bytes`. The scans live here because
                                        this endpoint is the cold path. `?probe=false` serves
                                        agent availability from the §9.6 cache instead of forcing
                                        the re-probe; the default is unchanged, and the forcing
                                        rule still holds for `vincent doctor`, which is the
                                        deliberate-command loop it was written about. The TUI's
                                        daemon panel opens on a keypress and passes probe=false
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
GET    /v1/update                       *Added 2026-08-29 (task 055).* the daemon's cached release
                                        check (§12.3) →
                                        { enabled, current_version, latest_version,
                                          update_available, published_at, release_url,
                                          checked_at, error }.
                                        It serves the **cache and never polls**: `update.check:
                                        false` promises the daemon makes no outbound request, and
                                        a `?refresh` parameter would hand any client the ability
                                        to break that. `vincent update --check` queries the feed
                                        itself instead (§12.1), which is also why it answers
                                        before the first poll and with no daemon running.
                                        `checked_at: null` with an empty `latest_version` is the
                                        **never-polled** state, and is a different answer from
                                        "no update available". `update_available` is computed
                                        server-side so every client agrees, and a `dev` build is
                                        never reported as behind. `current_version` is the
                                        **daemon's** build, which may be older than the binary
                                        that asked — that is what `vincent daemon status` reports
                                        after a swap. A prerelease never appears here
POST   /v1/daemon/stop                  graceful shutdown (§12.4); 202, then the daemon exits.
                                        `vincent daemon stop` calls this and waits for exit
POST   /v1/daemon/backup                { path } → { path, bytes, database_bytes,
                                        transcript_bytes, schema_version, created_at }.
                                        Writes one .tar.gz holding a `VACUUM INTO` copy of the
                                        database (§14), `transcripts/`, `config/config.yaml`,
                                        `config/workflows/` and `manifest.json`. `path` must be
                                        absolute, must not exist, and must not sit under
                                        `{data_dir}/transcripts` — each a 400. The daemon
                                        assembles the whole archive, so exactly one process
                                        walks daemon-owned state; taking it needs no quiet
                                        moment, but it holds the store's single connection for
                                        the duration of the copy. There is no restore endpoint:
                                        restore runs client-side, against a stopped daemon
                                        (§12.1, task 030)

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
GET    /v1/projects/{id}/github         *Added 2026-08-26 (task 035).* The capability probe:
                                        { enabled, repo?, available, reason?, message?, via? }.
                                        `enabled` is the §12.3 toggle; `repo` is `owner/name`
                                        derived from this project's `origin` at the point of use
                                        and absent when it is not a github.com remote; `via` is
                                        `gh` or `token` when available. `reason` is one of the
                                        named unavailability reasons — `disabled`, `not_github`,
                                        `no_credential`, `unauthorized`, `forbidden`,
                                        `not_found`, `rate_limited`, `timeout`, `unreachable`,
                                        `bad_response` — and never carries `gh`'s stderr or an
                                        HTTP body; the daemon logs those.
                                        It is its own endpoint rather than three fields on the
                                        project DTO because the board lists projects constantly
                                        and answering this there would probe `gh auth` per
                                        project per refresh; and it is not inferred from a failed
                                        listing, which would surface the reason only after the
                                        call it exists to prevent. Answered from a short
                                        daemon-side cache
GET    /v1/projects/{id}/github/issues  *Added 2026-08-26 (task 035).* The project's issues,
                                        newest first, never including pull requests.
                                        `?state=` (open — the default — closed or all),
                                        `?limit=`, and `?workflow=` which adds a `prefill`
                                        object per row: { title, description, fields }, the
                                        server's own answer to "what would creating a task from
                                        this issue fill in". No `?q=`: a client filters what it
                                        was given, as every §15 picker does.
                                        An unusable integration is a **409** carrying
                                        `details.reason` from the vocabulary above, not a 200
                                        with an empty list
GET    /v1/projects/{id}/github/pulls   *Added 2026-08-29 (task 052).* The project's **open**
                                        pull requests, newest first; `?limit=`. Same gate, same
                                        409-with-`details.reason` on an unusable integration.
                                        A **pure read**: it fetches, normalizes, sorts and
                                        returns, and persists nothing — linking is the
                                        reconciler's job (§12.3), because a link that appears
                                        only when someone looks is not a durable link, and no
                                        other write in this API is a GET. Rows a task claims
                                        carry `task_id` and `link_source` (auto | human)
                                        *Amended 2026-08-30 (task 064):* `?state=` (open |
                                        closed | all, default **open**) and `?workflow=`, which
                                        adds a computed `prefill` per row — the same shape the
                                        issues listing carries, and the same one POST /v1/tasks
                                        applies, so a preview a human accepted and a create
                                        call naming only the number produce the same task. The
                                        default stays open-only: closed and merged are now a
                                        choice a human makes, not a listing everyone pays for
GET    /v1/tasks/{id}/github/pull       *Added 2026-08-29 (task 052).* This task's pull request:
                                        the stored link plus the **live** pull request, fetched
                                        by number. Always **200**, whatever GitHub says — a
                                        workspace asks it on every open and the stored link is a
                                        fact vincent owns, so an unusable integration rides
                                        along as `reason` rather than refusing the row. Fetching
                                        by number rather than searching the listing is what lets
                                        a task still name a pull request that has **merged** and
                                        dropped off an open-only listing. A task with no link
                                        gets `compare_url` instead: GitHub's own “open a pull
                                        request” page, prefilled from the task and **built, not
                                        fetched** — no request is made to GitHub to produce it
POST   /v1/tasks/{id}/github/pull       *Added 2026-08-29 (task 052).* `{ number }` — the human
                                        link, for a pull request the head-branch rule misses or
                                        gets wrong. Writes vincent's own column only; **no**
                                        GitHub call is made, not even to check the number
                                        exists. Clears any earlier suppression
DELETE /v1/tasks/{id}/github/pull       *Added 2026-08-29 (task 052).* The human unlink. It does
                                        **not** clear the column: it marks the link
                                        `suppressed`, keeping repo and number, which is what
                                        makes the refusal survive the next reconciler tick

GET    /v1/workflows?project_id=        merged registry view: built-in + global + that project's
                                        (shadowing applied); each entry:
                                        { name, scope, project_id, file, description, fields[], steps[],
                                          platforms[]?, platform_supported, requires_input,
                                          includes[]?, version?, errors[]?, warnings[]?, error? }
                                        `version` (added 2026-08-30, task 065) is the token a
                                        `PATCH /v1/workflows` of that entry must carry; a built-in
                                        has no file and so no version
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
GET    /v1/chats                        *Added 2026-08-30 (task 063).* Chats, newest first.
       ?project_id=&state=              `state` may repeat. Chats appear here and nowhere else:
                                        never in GET /v1/tasks and never on the board
POST   /v1/chats                        { project_id, title, agent?, model?, effort?,
                                          base_branch? } → 201 with the chat, its
                                        `vincent/{id}-{slug}` branch and its worktree (§10).
                                        An omitted `agent` resolves to the first registered
                                        adapter that can resume — there is no `defaults.agent`
                                        key, and a chat's premise is continuity. An adapter that
                                        cannot resume is refused `400 agent_cannot_resume`
                                        (§9.3, §9.7): vincent will not replay the log as prompt
                                        context in its place
GET    /v1/chats/{id}                   { chat, turns[] } — the whole conversation, oldest turn
                                        first, each with its accounting (§5.5)
POST   /v1/chats/{id}/send              { message } → 202 with the new turn. `409` outside
                                        `idle` (§5.5), and `409 chat_cap_reached` when
                                        `max_parallel_chats` chats already hold a live process
                                        — **refused, never queued** (§11)
POST   /v1/chats/{id}/answer            the §7.4 answer flow verbatim: same normalized request,
                                        same `Respond()`. `409` outside `awaiting_input`.
                                        *Corrected 2026-08-30 (task 063, same PR): the wait is
                                        **not** bounded by `input_timeout`. `internal/chatrun`
                                        applies no clock at all — neither `input_timeout` nor
                                        `agent_timeout` — so a chat turn runs, and an
                                        `awaiting_input` chat waits, until it is answered or
                                        cancelled. It holds its `max_parallel_chats` slot for as
                                        long as it does. Bounding it is open work, recorded in
                                        `docs/tasks/063-free-chat.md`*
POST   /v1/chats/{id}/cancel            stops the live turn and kills its process tree
POST   /v1/chats/{id}/archive           removes the worktree and deletes the branch when it
                                        received nothing (§10, task 008 semantics), `?force=`
                                        for the dirty-worktree refusal. Terminal
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
POST   /v1/workflows                    *Added 2026-08-30 (task 065).*
                                        { scope, project_id?, name, from?, from_project_id? } →
                                        { name, scope, file, version, errors[], warnings[] }
                                        Creates a workflow file in the named scope. The daemon
                                        resolves the path and chooses the bytes — the §8 skeleton
                                        with `name:` rewritten, or a fork source copied verbatim —
                                        so **no YAML travels on the wire in either direction**.
                                        A fork keeps the source's own `name:`, because §5.2 shadows
                                        by name. 409 when the file exists, or when another file in
                                        the target scope already declares that name
PATCH  /v1/workflows?name=&project_id=  *Added 2026-08-30 (task 065).*
                                        { version, ops[] } → the create response's shape.
                                        Each op is { op: set|insert|remove|move, path, value?,
                                        block?, item[]?, to? }; `path` is dotted with list indices
                                        (`steps[2].prompt`, `steps[3].lanes[0].merge.on_conflict`).
                                        The daemon holds the original bytes end to end and applies
                                        the ops to them line by line, so an untouched region comes
                                        back **byte-identical** — comments, key order and blank
                                        lines included. `version` is the token the read handed back
                                        (mtime + hash); a file that moved underneath is a **409**
                                        carrying the current one in `details.version`. A patch that
                                        would not parse is a 400 and writes nothing
GET    /v1/workflows/schema             *Added 2026-08-30 (task 065).* §8.2 as data: the top-level,
                                        `defaults`, field-declaration, lane and merge rows, the
                                        common step fields, and every step type with the fields it
                                        accepts and the contexts it may be nested in. Generated from
                                        the table `workflow.Parse` validates against, so a client
                                        renders forms from it instead of carrying a second copy
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
                                        *Amended 2026-08-28 (task 044):* the rule is
                                        "§8.6 has one implementation", not "only the
                                        server may call it". `vincent workflow render`
                                        resolves a **file** — one the registry has
                                        frequently not picked up yet, which is why this
                                        endpoint, which takes a workflow *name*, does not
                                        serve it — by calling the same
                                        agent.ResolveWithSources this handler calls, and
                                        reports the same {value, source}. That is what PR L
                                        was protecting; `workflow validate` has resolved
                                        levels 1 and 3 locally against the curated catalogs
                                        since it shipped. No client re-implements the
                                        precedence.

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
                                          model?, effort?, github_issue?, github_pull? }
                                        branch_name is used verbatim and wins over every
                                        template (§10, task 001)
                                        → task (state=queued); agent/model/effort form the
                                        task-level override (§8.6), validated per §8.2 —
                                        known-invalid = 400, catalog-unknown values are
                                        reported in `warnings[]` on the 201 body
                                        The selected root workflow's §8.1.2 declarations are
                                        validated before insert. Additional, undeclared field
                                        names remain accepted and are recorded on the task
                                        *Added 2026-08-26 (task 035):* github_issue is an issue
                                        **number**. The daemon fetches it, computes the same
                                        prefill the issues endpoint previews, and folds it into
                                        the request wherever the caller left a value unset —
                                        **any value supplied explicitly wins**. Presence is what
                                        counts for `fields` and `description`: a key sent with an
                                        empty value is a row a human cleared on purpose and is
                                        left cleared, which is what lets the §15 form send its
                                        emptied rows verbatim. Only `title` keys on blank as well
                                        as absent — an untitled task is not something anyone
                                        creates on purpose. The
                                        resulting issue snapshot is persisted on the task (§5.3)
                                        and served back on every task representation as
                                        `github_issue`. `title` becomes optional when
                                        github_issue is given, because the issue supplies one.
                                        An unusable integration is the same **409** with
                                        `details.reason` the issues endpoint returns; a request
                                        without github_issue makes no GitHub call at all
                                        *Added 2026-08-30 (task 064):* github_pull is a pull
                                        request **number**, and behaves exactly as github_issue
                                        does — one prefill implementation, explicit values win,
                                        `title` becomes optional, the same 409, and no GitHub
                                        call without it — with three additions. The task's
                                        `branch_name` **is** the pull request's head branch and
                                        outranks `branch_name` in the request (§5.3, §10). The
                                        `github_pull` link is written at creation as `human`, so
                                        the takeover reads "claimed" immediately rather than a
                                        poll interval later, carrying `branch` and (for a fork)
                                        `fork`. No snapshot is persisted: the prefilled title and
                                        description become ordinary task text, and nothing
                                        re-renders draft/state/merged later. Naming both
                                        github_issue and github_pull is a **400** — two prefills
                                        over one title and description, with no defensible order
                                        *Added 2026-08-28 (task 040):* accepts an optional
                                        `Idempotency-Key` header. Same key + same request →
                                        `201` with the task the first send created; same key +
                                        a different request → `409` with
                                        `details.reason = "idempotency_key_reused"`; no header
                                        → unchanged. §13.1 has the rules
GET    /v1/tasks/{id}                   full task incl. step runs summary and pending_input (§7.4).
                                        Every task representation carries `available_actions`
                                        (the §6 human actions valid right now) and
                                        `pause_requested`, so clients never restate the FSM.
                                        *Added 2026-08-28 (task 043):* every task
                                        representation also carries `workflow_origin` —
                                        the scope that won §5.2's shadowing walk, the
                                        source file relative to that scope's root and a
                                        digest of the bytes it was loaded from, or
                                        `derived` naming a fan-out lane's parent (§5.3).
                                        null for a task created before origin was
                                        recorded, which is *not recorded* and never a
                                        re-lookup of today's registry
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

GET    /v1/tasks/{id}/workflow          this task's own workflow snapshot as a full definition
                                        (task 051, 2026-08-29): { task_id, name, definition,
                                        errors?, warnings?, error }. `definition` is the same
                                        body GET /v1/workflows/definition serves, so one DTO
                                        describes a registry entry and a snapshot alike — but
                                        the registry envelope's `scope`, `file`, `platforms`
                                        and `platform_supported` are absent, because a snapshot
                                        has none of them and a task's provenance is its
                                        `workflow_origin` instead. A snapshot that does not
                                        parse is a 200 with findings and a null `definition`,
                                        never a 4xx — the same rule the definition endpoint has
GET    /v1/tasks/{id}/steps             all StepRuns (every attempt)
                                        (task 015, 2026-08-18: each carries `skip_reason`
                                        — "condition" for a false `if:`, null for the human
                                        skip — and `state` may now be "stopped", §5.4/§7.7)
                                        (task 036, 2026-08-26: each also carries
                                        `status_message` — what the step said about itself,
                                        null when it said nothing — and `result_summary`,
                                        which has always been on this DTO and is now listed
                                        in §5.4 as well. `GET /v1/tasks` carries
                                        `status_message` too, denormalized from the task's
                                        *newest* step run the way `step_name` and `cost_usd`
                                        are, so a board never fetches step rows for it)
POST   /v1/tasks/{id}/steps/{step_id}/status
                                        { message } → { message }, the value as stored
                                        (added 2026-08-26, task 036). Records what the
                                        **running** step at `step_id` is doing, in its own
                                        words (§5.4). The caller is that step's own process:
                                        it addresses itself with §8.5's VINCENT_TASK_ID and
                                        VINCENT_STEP_ID, which is why the path names a step
                                        id rather than a `step_runs` row id — a step knows
                                        which step it is and cannot know its row. It is keyed
                                        by step id and not by task alone because a `parallel`
                                        group's sub-steps share one task and run at the same
                                        time (§7.5); within one task a step id has at most one
                                        running row.
                                        `message` is bounded rather than validated: it is
                                        flattened to a single line, stripped of control
                                        characters and truncated to **256 bytes**, and the
                                        response reports what was stored. An empty message
                                        clears the status. An unknown task is a 404; a step
                                        that is **not running** is a **409** — never a silent
                                        no-op, so a script still reporting progress after its
                                        step was killed learns that.
                                        Writes are paced, not rejected: two writes for one
                                        step run inside **1 s** coalesce to the later value,
                                        which lands when the floor expires (§13.3). The first
                                        write after a quiet period is always immediate
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

*Added 2026-08-25 (task 030).* `/v1/daemon/*` is no longer only process
lifecycle: it now holds `backup` beside `stop`. That is accepted knowingly and
recorded here rather than left for a reader to notice. `/v1/maintenance/*` was
the alternative and was rejected — maintenance is the family that reconciles
what is on disk against what the rows say (§10), while a backup is the daemon
copying its own state out, which reads correctly beside `daemon stop` and
spells the same way on the command line (`vincent daemon backup`). The
grouping's meaning widens from "the daemon process" to "the daemon itself".

### 13.3 Events (SSE)

Two kinds of streams:

1. **State events** — durable. Persisted to the `events` table with a monotonic id,
   emitted as SSE with `id:` set, so clients reconnect with `Last-Event-ID` and miss
   nothing. Types:
   `task.created`, `task.state_changed`, `task.priority_changed`, `task.step_advanced`,
   `task.status_changed`, `task.children_changed`, `project.*`,
   `workflow.registry_changed`, `agent.quota_changed`,
   `task.github_pull_changed`, `daemon.shutting_down`.
   *Amended 2026-08-29 (task 052, issue #231): `task.github_pull_changed` —
   payload `{repo, number, source, suppressed}`, empty when the link was
   cleared — announces that a task's pull-request link changed, because the
   reconciler (§12.3) writes it in the background and a running TUI must
   re-render without polling. It carries a `task_id` and is **not** a
   transition: the task's state is unchanged, `updated_at` is untouched, and
   `scheduler.WakeOn` is false for it, since nothing about admission depends on
   a pull request.*
   *Amended 2026-08-28 (task 046, issue #90): the `notify:` hook (§12.3)
   introduces **no new event type**. Its selector reads the `to` field of
   `task.state_changed`, the way the TUI bell does; `task.blocked` and
   `task.gate_pending` do not exist and were not invented for it. The hook is a
   daemon-side subscriber on this same post-commit fan-out, one hop downstream
   of the store's event hook, and it never blocks the publishing goroutine.*
   (`task.created` — *amended 2026-08-28, task 043* — carries
   `workflow_origin` beside `workflow`: the scope, scope-relative file and
   source digest the task's workflow name resolved to (§5.3), omitted only for
   a task whose origin was not recorded. The name alone cannot tell a project
   `adhoc.yaml` from the built-in it shadows, and a consumer that never fetches
   the task should not have to.)
   (`task.status_changed` — *added 2026-08-26, task 036* — carries
   `{task_id, step_id, message}` and announces that a running step changed what
   it says about itself (§5.4). It is on the **durable** side deliberately: the
   message is state, not output, so a client that blinks must be able to recover
   it through `Last-Event-ID`, which a live output chunk cannot offer. Three
   bounds keep it off the events table's critical path. A write whose message is
   byte-identical to the stored value appends **no** event — the rule
   `agent.quota_changed` already records, so a board that refetches on it is not
   woken by news it has. A **1 s** minimum interval per step run coalesces
   anything faster to the latest value rather than rejecting it, and the first
   write after a quiet period is always immediate, so the live reading is never
   delayed. The message itself is capped at **256 bytes**, truncated rather than
   refused, forced to a single line with control characters stripped.
   `scheduler.WakeOn` is **false** for it: nothing about admission changes when
   a step describes itself.)
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

**Chat events (added 2026-08-30, task 063).** Chats ride the one durable event
table and the one broker; there is no second stream. The durable kinds are
`chat.created`, `chat.state_changed`, `chat.turn_changed` and `chat.archived`,
carrying the chat id and — for turn changes — the turn id, seq and state. A
turn's live output is published exactly as a step's is, with the same ~10 Hz
coalescing and the same drop-the-slow-subscriber rule, because the turn's
transcript file is the durable copy. `Last-Event-ID` resumes the durable chat
events and not the output, for the reason it does not resume a step's.

### 13.4 Model Context Protocol (task 057)

*Added 2026-08-29 (task 057, issue #243).*

The daemon serves **MCP over streamable HTTP** on the same listener as `/v1`, so
an AI coding agent is a first-class client of the same API every other client
consumes. It is a second protocol, not a second server.

**Transport and auth are §13.1's, unchanged.** `POST /mcp` is registered in
`internal/api/server.go`'s route table beside the `/v1` routes and sits inside
the same `recover → log → auth` chain: loopback only, no TLS,
`Authorization: Bearer {token}` from `{data_dir}/token`, discovery through
`daemon.json`. There is no new listener and no new auth story. The §13.1 timeout
posture already suits a long-lived MCP response and is unchanged: a read-header,
a whole-request *read* and an idle timeout, and deliberately no write timeout —
the same property §13.3's streams rely on.

**The tool surface is the §13.2 route table minus destructive admin.** Every
route is one tool, and a call is dispatched by replaying the arguments as an
in-process request against the same handler the route table built. Parity is
therefore mechanical rather than maintained: the §13.1 body bounds, the field
bounds, the validation, the `409` + `details.state` envelopes and
`Idempotency-Key` all apply by construction. One tool result is capped at 256 KiB
with an explicit truncation note; a route's own `offset`/`limit` parameters are
how a client asks for less. `POST /v1/tasks` gains one argument its route does
not have as a body field: `idempotency_key`, which becomes the header. A tool
call has no header surface at all, and §13.1's replay protection exists for a
client whose response got lost — which is exactly what an agent is.

Five routes are **deliberately not tools**, and this is a design line rather than
an oversight:

    POST   /v1/daemon/stop
    POST   /v1/daemon/backup
    DELETE /v1/projects/{id}
    POST   /v1/maintenance/gc
    POST   /v1/doctor/fix

An agent must not be able to stop, garbage-collect or reconfigure the daemon
supervising it — least of all one running as a vincent step. They stay
CLI-and-curl only.

*Amended 2026-08-30 (task 060, issue #244).* **Six**, with `PATCH /v1/config`.
The sentence above already named the case before the route existed: a patch can
change the argv the daemon spawns (`notify.command`, `agents.*.path`), what its
children inherit (`environment`), and whether steps are wired to MCP at all
(`mcp.wire_steps`) — a step editing any of those is a step rewriting the rules
it runs under. The route-table parity test fails on either an unexposed or a
silently exposed route, so this cannot drift.

*Added 2026-08-30 (task 060, issue #244).* **`config_get` is the one tool whose
body differs from its route's.** §12.3 serves `environment.set`'s values and
`notify.command`'s argv over HTTP, where the boundary is loopback plus an 0600
bearer token. An MCP tool result is not that boundary: it is replayed on behalf
of an agent step and lands in the model's context and in the step's transcript.
So the MCP rendering masks those two fields — values only; the variable names
survive, which is the same line §12.3 draws for the log — and nothing else. A
test asserts the two bodies differ in exactly those fields and nowhere else.

*Amended 2026-08-30 (task 063, issue #255).* The **whole chat family** (§13.2)
is excluded too, on the same kind of line:

    GET    /v1/chats
    POST   /v1/chats
    GET    /v1/chats/{id}
    POST   /v1/chats/{id}/send
    POST   /v1/chats/{id}/answer
    POST   /v1/chats/{id}/cancel
    POST   /v1/chats/{id}/archive

Two reasons, either sufficient. A chat turn starts an agent CLI **without going
through admission** (§11), so a tool that could send one would let an agent
start unqueued agent processes — the exact thing `mcp.max_tasks` exists to
bound. And the recursion bounds walk `created_by_task_id`: a chat is not in that
chain, so making chats reachable would mean inventing depth semantics for a
non-task rather than reusing the ones that exist. An agent that needs a
conversation already has its own session; it does not need vincent to hold one
for it.

*Amended 2026-08-30 (task 065, issue #261).* **Fifteen**, with `POST` and
`PATCH /v1/workflows`, under the same wording task 057 decision 4 gave the
config route: an agent must not reconfigure the daemon supervising it, and a
workflow file is what that daemon runs. Nothing regresses — the
`create-workflow` built-in writes its deliverable through the filesystem, not
through this API. `GET /v1/workflows/schema` is an ordinary tool.

The task 057 property that the tool surface **equals** `Routes()` minus the
exclusions is unchanged, and is still asserted by a test — the exclusion list it
compares against is what grew. Everything else in §13.2 is a tool, including the three the
proposal left unclassified: `POST`/`DELETE /v1/tasks/{id}/github/pull`,
`POST /v1/tasks/{id}/steps/{step_id}/status`, and `POST /v1/tasks/{id}/archive`
despite its worktree removal and its possible empty-branch delete. The unlink
one carries a consequence worth stating: decision record row 27 makes a *human*
unlink **sticky**, so an agent unlink suppresses that link permanently.

§13.3's two SSE routes are not tools. A tool call is a request/response and an
event stream is not; `task_wait` replaces them for an MCP client.

**`task_wait`** blocks until a task reaches a terminal or human-blocking state —
`done`, `aborted`, `archived`, `awaiting_input`, `blocked`, `awaiting_gate` — by
subscribing to the §13.3 broker server-side. It takes a timeout with a hard
30-minute ceiling, so a call cannot hang forever, and it returns the task's state
either way with a `woke` flag distinguishing a wake from a timeout. Step
transitions arrive as MCP progress notifications while the call is open, and the
result is complete without them: progress is an enhancement to the wait, never
the means of delivering its result.

A step parked in `task_wait` **keeps its §11 slot**, and the deadlock §7.6 was
designed around is prevented by refusal instead: the tool returns a typed
`would_deadlock` error, immediately, when the caller is itself a running step and
the target cannot be admitted while the caller holds its slot. See §11.

**Per-step endpoints.** The daemon wires each agent step's CLI to
`/mcp/step/{run_id}`, carrying a secret minted for that step run and forgotten
when the step ends. Identity comes out of band, so the agent does not have to
cooperate to be identified, and it is what makes the wait refusal and the
provenance column correct. It is **not** a security boundary and must not be read
as one — see §16.

**Recursion is bounded by provenance.** A task created through MCP records
`created_by_task_id` (§14), deliberately distinct from `parent_task_id`:
`store/subtree.go` counts children by that column for the `awaiting_children`
join and `ListTasks`'s `ChildrenExclude` filters roots by it, so an MCP-created
task placed there would make its creator's `fan_out` step wait on a lane it never
spawned. `mcp.max_depth` and `mcp.max_tasks` (§12.3) are enforced at task
creation by walking the new ancestry chain with a recursive CTE, the way
`subtree.go` walks `parent_task_id`. Neither §7.6's `fan_out` bounds nor §7.9's
`include.max_depth` covers this path: both are creation-time checks over a static
snapshot, and this depth is discovered at run time.


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
  base_sha            TEXT,                   -- commit branch_name was cut from (§5.3, task 056); NULL = base_branch is the fork point
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
  github_issue_json   TEXT,                   -- the GitHub issue this task was created from (§5.3, task 035,
                                              -- migration 0014); NULL = no linked issue. A snapshot: written
                                              -- once at creation and never refreshed, which is what lets
                                              -- `.Issue` (§8.4) render offline. Nothing queries inside it — no
                                              -- index, no generated column — so a linked task costs the same
                                              -- as any other on every board query. A fan_out lane inherits
                                              -- its parent's copy verbatim (§7.6)
  github_pull_json    TEXT,                   -- the pull request this task is linked to (§5.3, task 052,
                                              -- migration 0018); NULL = never matched. A **pointer**, not a
                                              -- snapshot: { repo, number, source, suppressed, linked_at } and
                                              -- nothing renderable, because draft/state/merged are live by
                                              -- nature and a stored copy of them would read exactly like a
                                              -- current one while being wrong. `repo` rides beside `number`
                                              -- because a number alone is meaningless — this is where task
                                              -- 035 decision 5's "repo identity is not stored" was revisited,
                                              -- landing on the task rather than as a projects column.
                                              -- `suppressed` records a *human unlink*: the reconciler needs
                                              -- three states, not two — never matched, linked, and
                                              -- matched-but-refused — and an absent column carries only the
                                              -- first. Not folded into github_issue_json: that column is
                                              -- defined as "NULL = no linked issue" holding a bare Issue, so
                                              -- widening it would leave every existing row in the old shape
                                              -- and force a shape-sniffing read path forever
  workflow_origin_json TEXT,                  -- where workflow_name's definition came from (§5.2/§5.3, task 043,
                                              -- migration 0017); NULL = origin not recorded, reported as `unknown`.
                                              -- {scope, file, digest} for a registry-backed task and
                                              -- {scope:"derived", parent_task_id} for a fan_out lane. `file` is
                                              -- relative to its scope root, because an absolute path is where a
                                              -- checkout happens to live rather than provenance. Frozen at
                                              -- creation and never recomputed, so it names the file version the
                                              -- task came from, not the bytes the engine runs. Nothing queries
                                              -- inside it — no index, no generated column
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
  proc_started_at     TEXT,                   -- daemon wall clock just after spawn; the legacy reuse guard
  -- Platform-native identity of that process, compared byte-for-byte by §12.4
  -- recovery and never parsed (issue #149, task 031, migration 0013).
  -- NULL = none journaled (pre-0013 row, or the read failed) — recovery then
  -- falls back to the proc_started_at tolerance. Cleared with `pid` when the
  -- row is terminalized.
  proc_identity       TEXT,
  -- The container this run's process lived in (§16, task 061, migration 0021).
  -- NULL is the ordinary value and means the step ran on the host. `pid`,
  -- `proc_started_at` and `proc_identity` stay journaled for a containerized
  -- run — they name the host-side runtime *client*, a real process the daemon
  -- spawned — but recovery acts on this id instead: removing the container
  -- kills every process inside it, which is the identity a host PID cannot
  -- supply. Cleared with `pid` when the row is terminalized.
  container_id        TEXT,
  exit_code           INTEGER,
  check_exit_code     INTEGER,
  failure_reason      TEXT,
  skip_reason         TEXT,                   -- 'condition' for a false `if:` (§7.7); NULL for the human skip (§6)
  result_summary      TEXT,                   -- agent result text / command stdout tail
  -- What the step said about *itself* (§5.4, task 036, migration 0015): short
  -- free text its own process set through
  -- POST /v1/tasks/{id}/steps/{step_id}/status while it was running. NULL is
  -- the ordinary case — the step types that run no process never speak, and an
  -- agent or command step only speaks when its prompt or script was written to.
  -- Never written by the actor's own row updates, which is what makes the last
  -- live value survive onto the finished row.
  status_message      TEXT,                   -- NULL = the step said nothing
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

CREATE TABLE idempotency_keys (       -- §13.1 replay protection (task 040)
  method       TEXT NOT NULL,
  path         TEXT NOT NULL,
  key          TEXT NOT NULL,
  request_sha  TEXT NOT NULL,          -- digest of the decoded request, canonically re-marshalled
  task_id      INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  PRIMARY KEY (method, path, key)
);
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys(created_at);

-- Chats and their turns (task 063, added 2026-08-30; migration 0022). Two new
-- tables rather than a `kind` column on `tasks`: a chat has no workflow
-- snapshot, no step ledger and no §6 lifecycle, so a chat row in `tasks` would
-- force the board, admission and every §17 aggregate to decide whether they
-- mean chats too. For the same reason `chat_turns` is not `step_runs` with a
-- nullable `task_id` — `step_runs.task_id` stays NOT NULL and every query over
-- it keeps exactly its current meaning.
CREATE TABLE chats (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    state           TEXT    NOT NULL, -- §5.5: idle | running | awaiting_input | archived
    agent           TEXT    NOT NULL, -- must be an adapter that can resume (§9.1)
    model           TEXT,
    effort          TEXT,
    permission_mode TEXT    NOT NULL DEFAULT 'full_auto',
    branch          TEXT    NOT NULL, -- vincent/{id}-{slug}, as a task's (§10)
    base_branch     TEXT    NOT NULL,
    base_sha        TEXT,
    worktree_path   TEXT,             -- the §10 claim; NULL once archived
    session_id      TEXT,             -- the agent CLI's own session (§7.3 amended)
    pending_input   TEXT,             -- the §7.4 request being awaited, as JSON
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE TABLE chat_turns (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL, -- 1-based position in the conversation
    prompt        TEXT    NOT NULL,
    state         TEXT    NOT NULL, -- running | done | failed | interrupted
    fail_reason   TEXT,             -- the shared snake_case vocabulary; `session_lost` lives here
    error_message TEXT,
    result_text   TEXT,
    session_id    TEXT,             -- the session this turn actually ran in
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL,             -- NULL = the adapter reports none (§9.3, §9.7)
    exit_code     INTEGER,
    pid           INTEGER,          -- while running, for §12.4's orphan kill
    proc_identity TEXT,             -- the same PID-reuse guard step_runs carries
    started_at    TEXT    NOT NULL,
    ended_at      TEXT,
    duration_ms   INTEGER
);

CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
```

*Added 2026-08-29 (task 057).* `tasks.created_by_task_id INTEGER REFERENCES
tasks(id) ON DELETE SET NULL`, with `idx_tasks_created_by`, records the task
whose agent step created this one over §13.4's MCP server. NULL is every task a
human, the CLI, the TUI or a `fan_out` step created. It is not `parent_task_id`
and must not be conflated with it — see §13.4 for why.

WAL mode, `busy_timeout` set, all writes through the daemon's single connection pool.
Migrations are embedded in the binary and applied at startup.

*Added 2026-08-25 (task 030).* A **backup is a `VACUUM INTO` copy, never a file
copy.** Under WAL a committed row lives in `vincent.db-wal` until a checkpoint,
so copying `vincent.db` while the daemon runs yields a file missing recent
commits, and copying the three files separately yields a non-atomic set that can
restore into a torn database. `VACUUM INTO` runs in a read transaction and emits
one self-contained file with no `-wal`/`-shm` sidecar. It is also the only
mechanism available: the driver is `modernc.org/sqlite`, which does not expose
SQLite's C online-backup API through `database/sql`. It refuses an existing
destination, so the copy is staged under a fresh name. Unlike `VACUUM` — which
task 005 decision 4 skips while work is in flight because it rewrites the live
file under an exclusive lock — this takes no such lock and needs no quiet
moment; its cost is that the store's single connection is held for the duration
of the copy, so every other daemon query queues behind it, bounded by the size
of the database.

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

*Added 2026-08-28 (task 040, issue #146).* `idempotency_keys` stores a
**reference**, not a response: a replay re-reads `task_id` and renders the task
as it is now. Persisting the rendered `201` would mean storing a workflow
snapshot per create, under §13.1's 4 MiB body bound, in the one table that grows
with every task created — exactly the storage surface task 029 was opened to
measure. The primary key is `(method, path, key)` rather than `key` alone so a
later route joins the table with no migration, even though `POST /v1/tasks` is
the only writer today. `ON DELETE CASCADE` because a key whose task has been
destroyed has nothing left to replay: force-deleting a project deletes its
tasks, foreign keys are enforced on every connection, and the key goes with
them, so a send inside the remaining window creates a fresh task. That beat
`ON DELETE SET NULL` plus a `410`, which adds a status and an error code for a
case only a deliberate destructive act inside a 24-hour window can reach. The
key row is written **in the same transaction as the task**, so the two commit
together or not at all; a concurrent duplicate loses on the primary key, rolls
its task insert back, and replays the winner's.

## 15. TUI

> **Chats in the TUI are not in the first cut (task 063, 2026-08-30).** The
> entity, its API, its CLI (`vincent chat`) and its runner landed together;
> the chats view did not, and this section is unchanged until it does. A chat
> is driven from `vincent chat` and from curl in the meantime. The view, when
> it lands, is a sibling of the existing six routed by `viewID` and gets its
> screenshots from `scripts/screenshots.sh` like every other panel — never a
> drawing.

Built with Bubble Tea. The TUI is a pure API client — it holds no *task* state
the daemon doesn't have, and killing it never affects work. What it does own is
view state: where the cursor is, which tabs are open, and (amended 2026-08-29,
task 054) which board groups are folded, which persists in `{data_dir}/tui.json`
beside the §16 acknowledgment. That is not configuration — the TUI still reads
none from disk, and `tui.board.group_by` arrives over `GET /v1/config` as it
always did. It subscribes to `/v1/events` and
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
   (§7.4). OS desktop notifications remain out of v1 (§20). *Amended 2026-08-28
   (task 046): the bell is **unchanged** — it is still the in-client alert and
   still rings only on `awaiting_input`. Signalling outside a client is the
   daemon's `notify:` hook (§12.3), which is independent of it in both
   directions.*
   **Grouped by default (task 009, added 2026-08-16):** the rows nest under group
   headers — projects, and the workflows of a project inside it — configured by
   `tui.board.group_by` (§12.3) and cycled for the session with `g`. See
   *Grouping* below.
   **Several tasks can be selected at once (task 011, added 2026-08-16):**
   `space` marks the row under the cursor, `V` marks everything the filter is
   showing, and while anything is marked the task-action keys act on the whole
   selection. See *Bulk selection* below.
   **A STATUS column (task 036, added 2026-08-26)** shows what the task's
   newest step run said about itself (§5.4), truncated with an ellipsis. It sits
   at the top of the shedding ladder — dropped before cost, the step name, the
   workflow and the project — and it carries a higher bar than the others: it is
   admitted only when the title still clears a comfortable width, so a board
   narrow enough to lose it renders exactly as it did before the column existed,
   and the width a grouped board frees by dropping PROJECT and WORKFLOW still
   goes to the title. *Amended 2026-08-29 (task 052): the status is no longer
   truncated with an ellipsis — it wraps, along with TITLE, STATE and STEP, per
   the row-height rule below; and "the width goes to the title" is now "the
   width is spent on the row", per the title cap below.* The status of the *newest* row, not the newest message: a
   step that spoke and finished must not have its line linger beside the next
   step, which is doing something else. The state cell is untouched — the
   recorded reasoning that keeps a hold's reason out of it (it does not fit, and
   widening a column for a rare state costs every board the columns that shed
   first) stands unchanged, and this column is why the status did not go there.

   **Column widths (task 052, added 2026-08-29).** `TITLE` is the only flexible
   column, and it takes the width the fixed set leaves — but only up to a
   ceiling. Past that ceiling the surplus is spent in a fixed order: `STEP`
   first, up to a maximum wide enough for a step name with a loop rollup beside
   it (`3/7 green · loop 4/10`); then `STATUS`, up to a maximum of a couple of
   the board's lines of prose; and only then does the remainder go back to the
   title. The give-back is not a softening of the ceiling — it is what stops a
   board that has shed `STATUS` leaving dead cells on the right, and the title
   passes the ceiling only once neither other column has any appetite left. The
   ceiling and the `STATUS` column's admission gate are one value, because they
   are one fact: the title has cleared a comfortable width, so there is room to
   spend elsewhere. Below that width nothing changes — the shedding ladder, the
   minimum title and every narrow board render exactly as they did. `STATE` is
   deliberately not among the columns a surplus reaches: the recorded reasoning
   above holds, and the wrap is what makes its overflow readable.

   **Row height (task 052, added 2026-08-29).** A cell too long for its column
   wraps onto further lines of the same row rather than being truncated away.
   Every row on a board is the same height — the tallest row in the list the
   board is currently showing, clamped to three lines — so a board where nothing
   overflows is one line per row and renders exactly as it always did. The list,
   not the visible window: the table's scroll offset is private, and a height
   that changed as the board scrolled would move rows under the cursor. One long
   title far down therefore raises the rows above it, and a filter that excludes
   it lowers them again. Anything still overflowing at the third line is cut
   there with an ellipsis. Four cells wrap: `TITLE`,
   `STATE`, `STEP` and `STATUS`. `ID`, `ELAPSED`, `COST` and the marker column
   cannot meaningfully overflow, and `PROJECT` / `WORKFLOW` are identifiers used
   for scanning, which a fourteen-cell wrap makes unreadable — under width
   pressure those two are shed, which is the answer the ladder already gives.
   A group header stays exactly one line at every row height, and the marker
   glyph sits on a row's first line only. The cursor highlight is honest about
   what it can do: it shades the selected row's first line, because the shading
   is applied per row and faking it per cell would come out with unshaded
   gutters between the columns.

2. **Task detail.** *Amended 2026-08-28 (task 049): task detail is a separate
   full-screen workspace with four full-view tabs. **Steps & Attempts** is the
	   default and renders the existing step/attempt timeline. **Task Details** is
	   a read-only inspector with a section sidebar: only the selected section is
	   rendered, `↑`/`↓` or a mouse click selects another, and `pgup`/`pgdn`
	   scrolls long section content. Its sections cover the title, description, declared
	   fields, project, workflow and recorded origin, branch/worktree, state,
   priority, usage/cost, lifecycle timestamps, holds/blocks, pending input,
   fan-out/loop state, captured issue, available actions and workflow snapshot.
	   *Amended 2026-08-29 (task 052.6): a **GitHub pull request** section follows
	   the captured issue, from `GET /v1/tasks/{id}/github/pull` — a linked pull
	   request with its live state, the named reason when the integration is
	   unusable, or the compare-URL offer when nothing is linked. It carries two
	   keys, and the narrowing is to this section rather than to the tab: `o`
	   opens the linked pull request in a browser and `P` opens the compare-URL
	   editor. Both only reach a browser; neither writes anything in vincent,
	   which is the sense in which "read-only inspector" was written. Link and
	   unlink — the two actions that do write vincent's own column — live only in
	   view 7.*
	   **Output** renders the selected attempt's live or historical transcript and
	   lets the reader move that selection with `←`/`→` (or `h`/`l`) without
	   returning to the timeline. `enter` on a Steps & Attempts row opens Output
	   on that attempt.
   **Diff** renders the task's grouped git diff. Each owns the whole task body;
   `tab`/`shift+tab` and `[`/`]` walk them, `1`–`4` select directly, and `esc`
   returns to the board. The attempt selection persists across tabs.*
   *Amended 2026-08-29 (task 051): a fifth tab, **Workflow**, draws this task's
   own workflow snapshot as the control-flow graph of *Workflow graph* below,
   with a per-node run-state overlay. It is appended after Diff, so `1`–`4`
   keep the tabs they had and `5` selects it; `tab`/`shift+tab` and `[`/`]`
   cycle through it. Inside that tab `tab` stays the workspace's tab cycle and
   does **not** walk the graph's nodes in source order — the graph component's
   own `tab` binding stands down there rather than shadowing the workspace's.*
   *Amended 2026-08-26 (task 036): the attempt line gains two
   fields.* The step's own **status message** (§5.4) renders last on the line,
   in its own style and behind a glyph, so it reads as a quotation from the step
   rather than as another of the daemon's fields — and specifically **not** in
   `failure_reason`'s style, because a step killed on `timeout` can be carrying
   a line it wrote half an hour earlier and a client must never present that as
   the daemon's verdict. And **`result_summary`**, which had been stored and
   served since the first release and rendered on no screen at all, appears as a
   dim continuation line under an attempt that did **not** succeed — where a
   reader is asking "what went wrong" and the reason answers only which
   category. Under every attempt it would double a healthy timeline's height to
   restate what the output pane already shows for the selected one.
   The step timeline carries every attempt, with durations, tokens and cost;
   selecting one drives the full-view Output tab's live tail or scrollback into
   past transcripts. Attempt duration here is
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

   **The popup's own tab strip (task 063, added 2026-08-30).** All three form
   popups below — answer, repair and follow-up — carry a two-tab strip of their
   own as their first body line: the form (**Question** / **Repair** /
   **Follow-up**) and **Task details**. `ctrl+t` cycles them while the popup
   stays open. The second tab is the same read-only inspector the workspace's
   Task Details tab shows — the section sidebar, every section, its own scroll —
   and it is the popup's own instance of it, so reading inside the popup never
   moves the workspace tab behind it and switching tabs never disturbs the
   draft. That is the point: deciding what to answer needs the prompt, the
   workflow and step asking, the agent, and the linked GitHub issue, and until
   this the only way to any of them was `esc`, which costs the repair and
   follow-up forms their draft outright.

   Inside the popup the details tab is read-only more strictly than the
   workspace tab is: unhandled keys stop at the pane rather than reaching the
   task's actions, and neither `o` (open the pull request) nor `P` (open the
   compare-URL editor) is offered — a popup that can raise a second popup is
   not a reference surface. `ctrl+t` is taken by the workspace *before* the form
   sees the press, which is what makes it work while the free-text editor, a
   prompt `textarea` or an agent/model/effort picker has the keyboard. A popup
   with tabs takes the whole height budget on both tabs rather than shrinking to
   its form, so the frame does not resize under the reader on a `ctrl+t`. The
   compare-URL editor (§13.2, task 052.6) has no tab strip.

   **Repair popup (task 025, added 2026-08-24).** On a `blocked` task, `R` opens
   a second popup that owns the keyboard the way the answer form does: a
   required free-text prompt (`enter` edits it inline, `e` opens it in
   `$EDITOR`) and optional agent/model/effort rows fed by the same
   `GET /v1/agents` pickers the new-task flow uses (§8.6, with the request
   standing in for the step level). `ctrl+s` starts the repair, `esc` closes it
   and discards the draft — which is why `ctrl+t` (above, task 063) rather than
   `esc` is the way out to the task's details. It is a popup and not an action
   key because a repair needs prose written for this one task — which is also
   why it is excluded from bulk actions.

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
   follow. `ctrl+s` starts the run, `esc` closes and discards the draft; as with
   repair, `ctrl+t` (above, task 063) is the way to read the task's details
   without paying that. Like repair it is excluded from bulk actions (task 011):
   the input is written for one task, and the batch case is
   `vincent task follow-up` (§12.1).

   The detail timeline must render a follow-up round as **its own tier**, headed
   as a round rather than numbered as a step: its rows sit at
   `step_index >= step_total` (§5.4), and numbering round 1 of a four-step
   workflow "step 5" would say the workflow grew, which it did not (§5.3). A
   round's steps share one index and are named individually beneath that header,
   the way a `parallel` group's members are.
3. **New task.** Project picker → workflow picker (shows description + step list;
   flags steps whose agent is unavailable) → *(GitHub issue, conditional)* →
   title → description (inline or
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
   **GitHub issue row (task 035, added 2026-08-26):** a conditional row between
   workflow and title. It is present **only** when
   `GET /v1/projects/{id}/github` says this project's issues can be read — the
   integration on, the project's `origin` a github.com repository, a credential
   that answers — and is simply absent otherwise, in all three cases, so the
   form never offers a control that would fail. When it is absent the form makes
   no GitHub call at all. Its picker has the same windowed, type-filterable
   shape as every other one and lists open issues newest first, plus a `(none)`
   row that unlinks. Selecting an issue drops the daemon's computed prefill into
   the form's **own editable rows** — title, description with its trailing
   `GitHub issue #N: <url>` line, and any §8.1.2 declared field the mapping
   filled — because the mapping guesses, and a guess has to be visible before
   creation rather than applied silently at run time. *Amended 2026-08-27:* the
   prefilled title is the issue title prefixed `#N`, and `issue` joins `labels`,
   `assignee` and `milestone` as a declared field the mapping fills — with the
   issue **number**, which is the only way a `command` step can read it, since
   §8.5's environment is what a `run:` body sees and `.Issue` is not in it. Nothing is locked: every
   prefilled value can be rewritten or cleared, and a cleared value stays
   cleared (§13.2's precedence rule). It belongs to the **Task details** stage
   of the guided layout, not a stage of its own: picking an issue is how the
   title and description get filled in, and separating the pick from what it
   fills would put the guess and its review on different screens.
   **Guided wide layout (task 020, added 2026-08-20):** the same row order is
   grouped into six visual stages — Project, Workflow, Task details, Git &
   priority, Execution, Review. The stage is derived from the field cursor,
   not independently navigated; Review summarizes the whole request and the
   existing `ctrl+s` shortcut still submits from anywhere.
   **Enum rows (task 058, added 2026-08-30):** the boolean toggle gains a
   sibling. `enter` on a declared `enum` row opens the same windowed,
   type-filterable picker every other catalog uses, listing the declared
   `values:` with the current selection highlighted and the `default:` noted;
   `←`/`→` step a single-choice row through the members in place the way the
   boolean toggle cycles, so a two- or three-value field stays a single
   keypress. A `multiple: true` row is not stepped — "the next set" has no
   meaning — and is changed only through the list, which toggles membership
   with itself open and rewrites the row in **declared** order on every toggle,
   so it always shows the canonical string the daemon would store. An optional
   single-choice row gets an `(unset)` stop,
   which is the only way back to empty for a row the workflow owns and that
   therefore cannot be deleted. A declared `default:` seeds its row when the
   workflow is selected, and never over a value already entered: seeding is the
   client's job for an optional field, because the daemon deliberately does not
   invent one (§8.1.2).
4. **Projects.** List/add/edit/remove; per-project cap and defaults. On a wide
   terminal the project list remains as a rail while the selected repository's
   configuration, execution defaults, current workload, or add/edit form uses
   the focused surface (task 020, added 2026-08-20).
5. **Workflows.** Merged registry with scope badges and validation status; `e` opens
   the file in `$EDITOR`; live reload reflects saves immediately.
   *Amended 2026-08-30 (task 065, issue #261).* **The view authors the registry
   as well as reading it.** The PR M decision this replaces — "creating a
   workflow file from the TUI is out of v1 — new files are written in the
   editor and appear on the next reload" — named three blockers, and every one
   of them has since been removed: `workflow.SkeletonSource` and `--from` are
   the starter template (task 034); the write endpoint takes
   `{scope, project_id, name}` and the daemon resolves the path itself, so no
   server-exposed global workflows directory is needed at all; and a filename
   prompt is a form row. Task 060 supplies the affirmative argument: a file the
   daemon owns and already hot-reloads, which a human may edit by hand at any
   moment, is a different object from the process supervising the TUI. What
   PR M decided about `e` is **unamended** — `e` still edits the real file in
   place and the view still waits for `workflow.registry_changed`; validation
   moved to the daemon endpoint, not into the TUI.

   | Key | Operation |
   |---|---|
   | `i` | edit the entry under the cursor in a structured form |
   | `a` | create a workflow in a chosen scope (global, or a project's own) |
   | `f` | fork a built-in or global entry into another scope, where it shadows the original per §5.2 |
   | `e` | **unchanged** — open the file in `$EDITOR` |

   `e` keeps its one meaning: it means `$EDITOR` in all seven contexts
   `internal/tui/bindings.go` gives it, and taking it for the structured
   editor would give one key two meanings depending on the view. The forms
   are rendered from `GET /v1/workflows/schema` (§8.2 as data), not from a
   second copy of §8.2 in the client — PR L recorded that re-deriving the
   daemon's checks in the TUI is how the two drift. There is **no delete**:
   the view gains no destructive action. A file the forms cannot load is what
   `e` is still there for.
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
   **A database block (task 029, added 2026-08-25):** the footprint including WAL
   and SHM, the per-table row counts, the workflow-snapshot total and how far back
   the events table reaches (§17). The bytes come from the `/v1/info` this view
   already fetches; the counts and the span come from `GET /v1/doctor?probe=false`,
   fetched on activation and on `R` alongside the other two. It reports and offers
   nothing to press, like the orphans line beside it — §17 keeps rows indefinitely
   and this block is the measurement, not a policy. Its three empty states are
   named separately: a failed fetch keeps the last-good counts behind the dim stale
   line, a disconnected daemon says unavailable, and a report with `known: false`
   says unknown rather than zero.
   **Adapter verdicts (task 041, added 2026-08-28):** each adapter row trails
   with its §9.5 health facets — `untested` and the builds it was judged
   against, `incompatible`, and "no restricted mode here" where the adapter
   cannot restrict on this host. They trail deliberately: rows carry absolute
   binary paths and elide to the pane width, so the blocking conditions ("not
   found", "not logged in", a spent usage window) keep leading and a verdict is
   what a narrow terminal loses first. A `tested` build says nothing at all —
   one green word per adapter is what makes the one warning invisible.
   The view reports, it does not act: stopping the daemon from the TUI is out
   of v1 — `vincent daemon stop` owns that, and a TUI that auto-started the daemon
   at launch has no business killing it.

   *Amended 2026-08-30 (task 060, issue #244).* **The configuration block is the
   one exception, and only it.** "It reports, it does not act" still holds for
   stopping the daemon and for `vincent gc`: both act on the **process
   supervising the TUI**, and that is the whole of the argument above. A
   configuration edit is a different object — a **file the daemon owns and
   already hot-reloads**, which a human may edit by hand at any moment anyway,
   and which no client could previously even see in full. So the block becomes
   navigable (`tab`, then `↑`/`↓`), `enter` opens a typed editor on the selected
   key, and applying it is `PATCH /v1/config` (§12.3, §13.2). Each row shows the
   value in force and, when they differ, the built-in default; the endpoint
   carries no provenance, so what the marker claims is "differs from the
   default", not "written in the file". Four keys — `notify.command`,
   `environment.*`, `agents.*.path` and `listen` — are behind an explicit
   confirmation, because they decide what the daemon executes or exposes and
   agents already run full-auto by default (§16). `listen` is written and does
   not take effect until a restart, and the editor says so before it applies
   rather than showing a pending value as though it were in force. While the
   editor is open the view **captures input**, which it never did before: every
   single-key global would otherwise land in the text field. There is still no
   seventh view — the daemon view already owned this block.

   The log tail is read straight from `{data_dir}/logs/daemon.log`, the one
   place the TUI is not a pure API client: an endpoint cannot serve the log when
   the daemon is the thing that died, which is when the log is worth reading —
   so it is the one view with something true to show while disconnected. See *Disconnected* below for what the rest of the UI
   does in that state.

7. **Pull requests.** *Added 2026-08-29 (task 052.6).* Every available project's
   **open** pull requests, grouped by project, from
   `GET /v1/projects/{id}/github/pulls` — one call per project, issued
   concurrently on open. The screen answers the cross-project question, "what is
   open across everything I run", which one project at a time cannot. Rows carry
   the number, the folded status word (merged beats closed beats draft beats
   open, §13.2), the title, the head branch and the task that claims the row with
   its `link_source`. A project whose listing answers 409 renders as a failed
   group carrying that reason's message and does not affect the others: each
   group holds its own error.

   **Availability.** The entry is a keyless nav row like Projects, Workflows and
   Daemon, and it is present only when at least one registered project answers
   `GET /v1/projects/{id}/github` with `available: true`. There is no stored
   notion of a GitHub project — it is derived from `origin` plus a credential
   probe, which is why §13.2 keeps it off the project DTO — so the TUI issues one
   probe per project as the connection comes up, concurrently, and again on
   reconnect, where the daemon's short cache absorbs the repeat cost. While every
   answer is unavailable, **including while they are all still in flight**, the
   row is withheld from the palette, the `?` overlay and the footer alike, and
   the view is unreachable. This is the `fold` precedent (task 054) applied to a
   nav row: a row whose screen would have nothing on it is withheld rather than
   shown dead.

   **Actions.** `o` opens the selected pull request in a browser. `enter` opens
   the workspace of the task that claims it, and is inert on a row no task
   claims — the link key is its own, and a key that means two unrelated things
   depending on the row is worse than one that sometimes does nothing. A link key
   opens a task picker **scoped to the row's own project**: `POST
   /v1/tasks/{id}/github/pull` takes a bare number and the daemon resolves the
   repository from the task's project, so offering a task from elsewhere would
   link that project's repository to a number that means something else there. An
   unlink key asks first, and the confirmation says the refusal is sticky and the
   reconciler will not re-apply it — which is what `DELETE` does, and a UI that
   said "clear" would be lying. `R` re-lists, `/` filters, `↑`/`↓` move.
   The view subscribes to nothing of its own: the root broadcasts every event to
   every view, so a `task.github_pull_changed` tick re-renders it with no
   keypress.

### Layout

The list above is also the screen contract. View 1 is the board-only home
screen. `enter` on its selected row opens view 2, the full-screen task workspace;
`esc` returns. The workspace's five tabs each take its whole body (four until
task 051, 2026-08-29, added the Workflow tab). Views 3–7 are
full-screen takeovers reached from the command palette (view 7 added
2026-08-29 by task 052.6).

*Amended 2026-08-30 (task 064).* View 7 gains two keys. **`c`** opens the
new-task form seeded with the selected pull request: the daemon computes the
prefill and the form previews it in editable rows, so the TUI still makes no
GitHub call of its own (task 035 decision 2). The created task runs on the pull
request's head branch, which is the one row of that form the human cannot
change. A row another task already claims is refused on the row rather than at
the create call — two live tasks cannot hold one branch. **`s`** cycles the
listing between `open`, `closed` and `all`; the default stays `open`, so the
screen still answers "which of my branches has a pull request" without pulling a
repository's whole history to do it. **`P` on a task workspace is unchanged** and
already withholds itself on a task that has a live link: the pull request such a
task was created from already exists.

**Opening a URL (task 052.6, added 2026-08-29).** The two screens above hand a
URL to the platform's own opener — `open` on macOS, `xdg-open` on the other
unixes, the shell's protocol handler on Windows — and to nothing else. Only
`http` and `https` are opened. Unlike the clipboard fallback, which is silent
by design because the terminal's own paste is the working path, this one
**fails visibly**: a human pressed a key expecting a browser, and silence is
indistinguishable from a browser that opened on another desktop.

**Guided takeovers (task 020, added 2026-08-20).** At a terminal size of at
least **128 columns by 24 rows**, New task, Projects and Workflows use a
persistent navigation rail beside one focused work surface. Below either
dimension they use their compact single-column form, table or registry. A
resize changes composition only: it may not reset the field/resource cursor,
filter, open picker or form, expansion, or graph. The split introduces no new
daemon state and no capability that exists only at one size.

```
┌─ Tasks ────────────────────────────────────────────────────┐
│  #12  api    add rate limiting   running   3/5  …          │
│  #13  web    fix flaky test      ● gate    2/4  …          │
│                                                            │
└────────────────────────────────────────────────────────────┘
 enter open · / filter                    : commands  ? help  q quit

┌─ Task #12 ─────────────────────────────────────────────────┐
│ Steps & Attempts │ Task Details │ Output │ Diff │ Workflow │
│  1 ✓ plan                                      1m2s        │
│  2 ▸ implement                                 4m9s        │
└────────────────────────────────────────────────────────────┘
 tab views · ↑/↓ attempts · esc board      : commands  ? help  q quit
```

The task table keeps the full §15 column set at full width. The task workspace
does not reserve a rail or a second pane: metadata, transcripts and diffs are
all width-sensitive, while the selected attempt is durable view state that can
drive Output without staying visible beside it.

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
  *Amended 2026-08-29 (task 052): the freed width is spent on the row in the
  allocation order above — the title first, then `STEP`, then `STATUS`, then
  back to the title. A grouped board is therefore never worse off than a flat
  one at the same width, but above the title's ceiling the two render equal
  titles and the gain shows up in the other two columns instead. The earlier
  wording, and task 036 decision 9's "strictly wider at every width", are
  amended to that; the reasoning that a **new column** must not silently
  re-spend the freed width is untouched, and is still what the `STATUS` gate
  enforces.*
- **An open header is a label, not a row.** The cursor steps over it in the
  direction it was travelling, and clicking it selects nothing.
- **Groups fold** (amended 2026-08-29, task 054; this replaces the original
  "nothing folds away" rule, which is superseded together with task 009's
  decision 4). `←` collapses the group the cursor is in and `←` again the group
  around that, `→` opens one level, `C` and `O` do the whole table. A *collapsed*
  header stands in for tasks that are not on screen, so it **is** a row: the
  cursor rests on it, and it shows `▸` rather than `▾`, its task count, its
  attention badge and how many of its tasks the bulk selection holds. It is a
  row and not a task: it has no state and no `available_actions`, so the §6
  action keys, `space`, `enter` and `L` do nothing while the cursor is on one
  and the detail panels hold the last task rather than blanking. Folding is
  a view over the same band sort — the rows that remain are in the order they
  were. It is never refused: what protects the failure the original rule named is
  that the header keeps its badge, that `!` opens whatever group it lands in, and
  that a collapsed group opens by itself the moment a task inside it enters
  `awaiting_input`. The set is keyed by label path, persists in
  `{data_dir}/tui.json`, survives `g` and a filter, and drops a path whose
  project or workflow has left the board. `group_by: []` has no groups, so the
  four keys are inert; a fresh install has nothing folded.
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

Views 3–7 stay full-screen because they are forms and lists, not observations: the
new-task flow is eight fields with pickers, and squeezing it beside a live tail
serves neither. Takeovers are for surfaces you visit deliberately; popups are
for what interrupts you — the palette, confirmations, and the three form popups.
*(Amended 2026-08-30, task 063: the dividing line is the interruption, not the
size. A form popup with a tab strip takes the whole height budget and carries
the task inspector inside it, and is no longer a small thing.)*

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

*Amended 2026-08-29 (task 053).* **`enter` opens the selected node in full**, as
a bordered popup over the graph. It shows every field of the DTO that applies to
that node — including the ones nothing else in the TUI shows: the `prompt`, the
`run:` body, `env`, `instructions`, `permission_mode`, the input and check
timeouts, `max_parallel`, a loop's `count`/`for_each` and `max_iterations` —
above a header naming the workflow the node sits in: its description, declared
fields, platforms and file. Long values wrap and the popup scrolls; nothing in
it is truncated.

A value the step authors is shown as authored. A value the step leaves empty
and the file's `defaults` block supplies is shown as the effective value and
**marked as inherited** (§8.6); a field neither sets is omitted, and the
daemon's own run-time fallback is never printed here — the modal shows the
file. Every node opens something: a merge shows its conflict policy and its
resolver agent, a collapsed reference names the workflow it stands for and
whether it becomes a child task or spliced steps, a group header shows its
bounds, and END says the workflow ends there.

While the modal is open it owns the keyboard — scroll and pager keys move it,
`e` and `R` still carry through — so the Escape ladder is now modal, then
graph, then the takeover. A terminal below the minimum width has no node drawn
and `enter` opens nothing. The picture itself is unchanged: node boxes and the
inspector strip keep truncating exactly as they did, and the strip stays the
glance view the selection follows without a keystroke.

`e` and `R` work inside the layer: `e` opens the graphed workflow's own file,
and a save redraws the graph in place through the same live reload the list
uses — the selected node survives it, because a node's identity is its step id
and not its position. `R` refetches the one definition, which is the layer's
recovery from a failed fetch. Nothing is cached: the registry changing is
exactly when someone is editing files in this view.

An entry that does not parse has no graph, and `g` says so instead of opening a
layer that would repeat the findings already on screen.

**The task workspace's Workflow tab (task 051, added 2026-08-29).** The same
pipeline draws a second surface: the fifth tab of view 2 (§15 above), showing
the workflow **this task** ran with what each step did on it.

What it draws is the task's own §5.3 **snapshot**, served by
`GET /v1/tasks/{id}/workflow`, and never the registry entry of the same name.
The snapshot is what ran — includes already spliced (§7.9), any `edit + retry`
rewrite reflected (§6) — while the registry's copy is whatever the file says
now. A spliced include therefore shows as the *N* flat steps it expanded into,
each attributed by `resolved_from` in the inspector, where the workflows screen
shows one collapsed node for the same file.

Topology is unchanged by the overlay. A loop still draws once with a
back-edge and a fan_out still draws its authored lanes: nothing unrolls, because
re-laying out on every discovered iteration would move nodes under a reader
watching a running task. Applying an overlay changes no coordinate and loses no
selection.

The overlay is words and glyphs first, colour second — the whole picture still
reads with every style stripped:

- **A node carries its newest attempt's state**, its iteration and its attempt
  number when there is room, and nothing at all when the task never reached it.
  A false `if:` guard (§7.7) reads `skipped if`; the human `skip` action (§6)
  reads `skipped`; a node never reached is bare. Those are three different
  things and they never render alike.
- **A parked task says where it is parked.** `blocked`, `awaiting_input` or
  `paused` lands on the step that owns it, with its §12.2 `block_reason`.
- **A `fan_out` lane's state rides on its lane caption**, with the child task's
  id — never on the lane's inline step nodes. Those steps run in the child
  task, so the parent holds no `step_run` for them and cannot honestly paint
  them. The lane rollup comes from `GET /v1/tasks?parent_id=`.
- **An attempt no node answers for is drawn off-graph**, in a frame below the
  single END node: a follow-up round runs a step that is not part of the
  snapshot, and a repair rewrites one. They are neither dropped nor drawn as if
  the workflow had declared them.

Node ids inside a `fan_out` lane are namespaced by the lane (`<fanout>.<lane>/<step>`)
because step-id uniqueness is per body (§7.6): a top-level `build` and a lane's
`build` are two steps, and were two nodes answering to one id before this. The
node keeps the raw step id as its `step_id`, which is what a `step_run` row is
joined on.

`e` and `R` are absent from this tab: a snapshot has no file to open and no
registry entry to re-read.

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

**`esc` cancels one layer per press**, by a fixed stack: *(amended 2026-08-30,
task 063)* a form popup's Task details tab → popup (palette, confirmation,
answer form) → takeover screen → bulk selection → active filter →
nothing. The innermost layer is the newest: on a form popup's details tab `esc`
returns to the form tab with the draft untouched and the popup still open, and
only a second press carries the popup's own `esc` meaning. It is a
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
- **Container execution confines the filesystem, not the network or the
  credentials (task 061, added 2026-08-30).** With `container.image` set (§12.3)
  a task's step processes run inside one container created with the task's
  worktree and removed with it — every `command` step and every `check:` as of
  task 061, and the **agent** process itself once task 062 lands the spawn seam.
  Until then a containerized task's agent steps still run on the host, so the
  confinement below is the container's and does not yet reach them. What that
  confines is real and is the point: the
  filesystem outside the two bind mounts — the project repository and the task's
  worktree, both at their own absolute paths — the shell, and whatever tooling
  the image carries. An agent that `rm -rf`s the wrong directory reaches the
  worktree and the repository and nothing else of the user's machine.
  What it does **not** confine is stated here with the same honesty §16 already
  applies to `/mcp/step/{run_id}` not being a boundary:
  - **Outbound network is open by default.** `container.network: false` closes
    it, and is refused together with `mcp.wire_steps: true` because a container
    with no network cannot reach the daemon's per-step MCP endpoint.
  - **The agent's credentials are inside it.** `mount_agent_config` defaults to
    true and bind-mounts `~/.claude`, `~/.codex` and `~/.cursor` **read-write**,
    because subscription auth takes no key from the environment and cursor
    persists `--model` to its own config (§9.7). An agent in the container can
    therefore read the host's agent credentials and write to those directories.
    The knob turns it off; an agent CLI that then cannot authenticate is the
    documented consequence, not a bug.
  - **The daemon is reachable.** Every container is created with
    `--add-host=host.docker.internal:host-gateway` whenever it has a network at
    all, so a process inside it can call the daemon's MCP surface — with the
    same per-run token scoping §13.4 already specifies, and the same caveat that
    the endpoint is not a boundary. The per-step endpoint's *host rewrite* to
    `host.docker.internal`, which is what makes a containerized **agent** step
    use it, is task 062.
  - **It is not a privilege boundary.** On a Linux host every exec runs as the
    invoking user's uid:gid so files land owned correctly, which means a
    container escape lands on the same user the daemon already runs as.
  Containerization and `permission_mode` are orthogonal axes that compose:
  `restricted` inside a container is still restricted, and full-auto inside a
  container is still full-auto with the container's own reach. There is no
  `contained` permission mode.
- **The release check and `vincent update` (task 055, added 2026-08-29).** The
  check sends **one unauthenticated GET** of the project's public
  latest-release feed and nothing else: no `Authorization` header, no
  telemetry, no machine, install or user identifier, and no header saying
  anything about this host. It downloads nothing and executes nothing.
  `update.check: false` (§12.3) stops it entirely, and `vincent update`
  queries the feed itself rather than through the daemon so that switch means
  what it says. `vincent update` **verifies before it executes anything it
  downloaded**, in the order the release notes tell a human to: the cosign
  signature over `checksums.txt` against the project's pinned certificate
  identity and OIDC issuer, then the downloaded archive's SHA-256 against that
  now-trusted file, then extraction and the swap. On any mismatch nothing is
  replaced and the previous binary is left byte-identical. The identity and
  issuer are constants, not flags — a verification whose identity the caller
  chooses verifies nothing. `cosign` is preferred from the user's `PATH` and
  never bundled (the posture `internal/github` takes toward `gh`); without it
  the checksum check runs alone and the command says plainly that the signature
  was not verified, and `--require-signature` makes its absence fatal.
  A binary a package manager owns is never modified.
- Worktrees provide *collision* isolation, not *security* isolation.
- The daemon stores no secrets; agent CLIs use their own auth (keychain/config). The
  token file gates only the vincent API itself.
- **"Stores no secrets" is about vendor credentials, not about the user's own.**
  *Amended 2026-08-25 (#141).* Vincent has no key store and no vendor
  credentials of its own, but `config.yaml` takes literal `environment.set`
  values (§12.3) and a user can reasonably put an API token there. That file and
  its directory are therefore owner-only on POSIX and re-tightened on every
  daemon start (§12.2). `environment.set` is still not a secret store: it is
  plaintext on disk, and inheriting a name from the surrounding environment is
  the better answer. A secret-provider design — a keychain or an external
  provider — is out of scope here and is not what this amendment provides.
  Transcripts are the other place user-supplied sensitive data lands, and are
  `0600` for the same reason.
- Command steps and checks execute user-authored workflow content — same trust level
  as the user's own shell; no additional sandboxing is attempted or implied.
- **Adapter full-auto switches are all equivalent in blast radius**:
  `--dangerously-skip-permissions` (claude),
  `--dangerously-bypass-approvals-and-sandbox` (codex), `--force` (cursor).
  Cursor's reads mildest and is not; the first-run notice covers all three.
- **`notify.command` is arbitrary code the daemon runs as the invoking user.**
  *Added 2026-08-28 (task 046, issue #90).* It is spawned by the daemon, not by
  an agent or a task, and nothing from a task, an agent or the API reaches its
  argv — it is exactly what the owner of `config.yaml` wrote. That is consistent
  with the posture above rather than a new risk: an agent step already runs
  arbitrary commands as this user. Two consequences are worth stating rather
  than leaving implicit. Its argv can carry a **secret** — a webhook URL with a
  token in it is the obvious use — which is a second reason `config.yaml` and
  its directory are owner-only (§12.2, the 2026-08-25 amendment above), and it
  is why `notify` is not served on `GET /v1/config`. And it is argv, never a
  shell string, on every platform: nothing is expanded, split or quoted, so a
  task title cannot reach a shell through it.

  *Amended 2026-08-30 (task 060, issue #244).* The clause about `GET
  /v1/config` is superseded: `notify` **is** served, values included, because
  that endpoint sits behind the same loopback-plus-0600 boundary as the file
  (§12.3's amendment). The MCP rendering masks the argv, because that one does
  not. And `notify.command` is now writable over `PATCH /v1/config` — which is
  to say a client can change what the daemon executes as you, without a
  restart. That is why the route is excluded from the MCP tool surface
  (§13.4), and why the TUI's editor puts it, `environment`, `agents.*.path`
  and `listen` behind an explicit confirmation.
- **Vincent writes to one CLI's own config**: a cursor step passes `--model`,
  which cursor persists to `~/.cursor/cli-config.json` (§9.7). It is not a
  secret and not an escalation, but it is the one place vincent mutates state
  outside its own data dir, so it is recorded here rather than discovered.

*Added 2026-08-29 (task 057).* **An agent can now create and cancel vincent
tasks.** §13.4 serves MCP from the daemon and, by default
(`mcp.wire_steps: true`), wires vincent's own agent steps to it. In
blast-radius terms this is not a new privilege — the posture above already says
an agent step runs arbitrary commands as the user, and a full-auto agent can
read `{data_dir}/token` and `daemon.json` and drive `/v1` with curl today — but
it is a change worth stating rather than leaving to be discovered. Three
specifics:

- **The five destructive-admin routes are not tools** (§13.4). An agent cannot
  stop, back up, garbage-collect or reconfigure the daemon supervising it, nor
  force-delete a project.
- **`restricted` does not restrict what a step does to vincent** (§9.4). The
  allow-list carries `mcp__vincent__*` in full, so a restricted step can create
  and cancel tasks. It bounds the filesystem and the shell, and that is all it
  claims to bound.
- **The per-step endpoint is not a security boundary.** `/mcp/step/{run_id}`
  carries a secret minted for one step run, and it exists to make `task_wait`'s
  deadlock refusal correct and to attribute provenance — not to confine the
  agent. A full-auto agent can read the daemon token and reach `/mcp` directly.
  It must not be documented, or relied on, as a sandbox.

The cursor adapter additionally writes `.cursor/mcp.json` into the **task
worktree** (§9.7), which extends this section's existing note about vincent
writing to cursor's own config. It is workspace-scoped and per-task; the user's
global cursor config is untouched.

## 17. Observability

- **Per step:** duration (active time — time spent `awaiting_input` is tracked
  separately as input wait, §7.4), exit codes, tokens in/out, cost (when reported),
  full JSONL transcript on disk (agent events, command output, check output,
  input requests and answers).
- **Per task:** aggregate duration/tokens/cost across attempts (rolled up from
  step_runs; shown on board and detail views). *Amended 2026-08-26 (task 033):
  the cost rollup is no longer only reported.* When `max_task_cost_usd` (§12.3)
  is set, the engine compares this figure against it at every attempt boundary
  and blocks the task `cost_limit` (§18) once it is over. "Across attempts,
  retries included" is therefore load-bearing rather than a reporting nicety: a
  step that failed twice before succeeding spent money three times, and a cap
  reading only the surviving attempt would under-count exactly the tasks that
  burned it.
- **Daemon log:** structured (slog), rotated; scheduler decisions at debug level.
- **Retention:** transcripts of archived tasks pruned after
  `transcript_retention_days` (default 90); DB rows kept indefinitely (rows are small,
  history is valuable). *Amended 2026-08-28 (task 040):* one exception —
  §13.1's `idempotency_keys` rows are pruned after a **fixed 24 hours** by the
  same pass, with no config knob and independent of
  `transcript_retention_days`' "zero keeps everything". A key is opaque,
  small, and useless once the retry window it covers has passed, so there is no
  operator reason to keep one. The table is counted in
  `GET /v1/doctor`'s `database.table_rows` like every other, by enumeration
  rather than by name.
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

  *Amended 2026-08-25 (task 029): the database is now measured.* "Rows are small,
  history is valuable" was an assumption nobody could check on their own machine
  after six months of use. Four figures now answer it, and **the retention decision
  above is unchanged** — rows are still kept indefinitely, nothing prunes them,
  nothing warns, no threshold exists and no exit code moves. A later decision to
  prune would be the amendment; this is the evidence such a decision would need.

  - **Footprint.** `size_bytes`, `wal_bytes`, `shm_bytes` and `total_bytes`, on
    both `GET /v1/info` and `GET /v1/doctor`. The store runs in WAL mode, so the
    main file alone understates the footprint between checkpoints; the total is the
    figure to quote.
  - **Per-table row counts** (`table_rows`), on `/v1/doctor` only. Enumerated from
    `sqlite_master`, never listed in code, so a table a later migration adds is
    counted with no edit and this binary's database is described rather than a
    fixed contract. A byte total says the database is big; this says which table
    made it so — `events` gets one row per state change and is the growth driver
    the retention decision is really about.
  - **`workflow_snapshot_bytes`**, on `/v1/doctor` only: the second growth driver
    (§14 — every task stores the workflow YAML as it stood at creation). It is
    separate because "412 MB of events" and "412 MB of snapshots" point at
    different decisions, and a single byte count cannot tell them apart.

  *Amended 2026-08-26 (task 035): the GitHub integration is reported too.* The
  same "why is nothing offered?" question the rows above answer for tasks has a
  GitHub-shaped version — why is the new-task form not offering an issue
  picker? — and it had exactly one surface: a line in the daemon log. The
  report now carries `github`: the `github.enabled` toggle, whether `gh` was
  found and at what path and version, whether `gh auth status` succeeds,
  whether `GITHUB_TOKEN` or `GH_TOKEN` is set (the variable's **name**, never
  its value — a diagnostic is something people paste into issues), and whether
  issues are readable and by which credential. **Nothing here is a problem and
  nothing moves the exit code:** every "no" it can report leaves task creation
  without an issue working exactly as it did before, so accusing the machine of
  being unhealthy over it would be wrong.
  - **`oldest_event_at`**, on `/v1/doctor` only, null on an install with no events.
    A count without a span is not extrapolable.

  The split is by cost, not preference: the byte figures are three `os.Stat` calls
  and ride the endpoint every TUI refresh polls, while the counts and the span are
  scans and ride the deliberately cold one (§13.2). Every figure is the daemon's —
  only it opens SQLite (§4), so a client with no daemon reports them **unknown**
  rather than opening the file itself, exactly as the existing database rows do.

**What the notify hook logs (task 046, added 2026-08-28).** The `notify:` hook
(§12.3) is invisible by construction — it runs a process that reports to
somewhere else — so the daemon log is the only place its behaviour surfaces. A
fire is logged at **debug**, with the task, the target state, the event id and
`command[0]`; the command's arguments are not logged, because argv can carry a
webhook secret (§16). Four things are logged at **warn**, each once and never
retried: a non-zero exit, with the exit code and a truncated tail of the
child's stderr; a child killed at the 10 s timeout; an event dropped because
the queue was full, with the capacity; and a task the notification could not be
read for. A skipped `fan_out` lane is debug, not warn — it is the designed
behaviour, not a fault. Nothing here touches task state, a step run or the exit
code of anything: a notifier that fails loses one notification and nothing
else.

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
| `restricted` step on an adapter that cannot restrict on this OS | Step fails to start with `restricted_unsupported` (cursor on Windows, §9.7), under the retry policy → typically blocked. Never downgraded to full-auto, and deliberately *not* `agent_unavailable`: the CLI is installed and healthy, so "not found" would send the user to reinstall what is already there. *Amended 2026-08-28 (task 041):* **task creation refuses these** with a `400` naming the step and the agent (§9.4), and `GET /v1/agents` publishes the `restricted_verdict` the gate uses. Reaching the engine anyway means the task and its daemon parted company — a data directory carried to Windows, or a workflow edited after the task was queued — so the reason above stays exactly as it is, as the backstop. Retries are not gated: enforcement is creation-time, and the backstop is what catches the rest |
| Step declaring `on_input: require` on an agent that cannot ask | *Added 2026-08-17 (task 013).* A workflow pinning an adapter with no control channel (codex, cursor) fails §8.2 validation outright. Otherwise creation is refused with a `400` naming the step and the agent, and the TUI's picker will not select that agent; `GET /v1/agents` publishes the `input_verdict` the gate uses. A task that reaches the engine anyway — claude upgraded past the §9.3 ceiling, a data directory moved — fails the attempt with `input_unsupported` under the §7.2 budget, before anything is spawned. Only a positive "cannot" refuses: an absent or unprobed binary is unknown, and unknown never blocks (§9.6) |
| Workflow restricted to platforms this host is not | *Added 2026-08-16 (task 010).* Creation is refused with a `400` naming the restriction and the host (§8.1.1); the entry stays listed and says why, and the TUI's picker will not select it. A task that *already* holds such a snapshot — the data directory moved to another OS, or the workflow narrowed after the task was queued — blocks at admission with `platform_unsupported`, before a worktree or any step. Not `invalid_snapshot`: the snapshot is valid, just not here |
| Runaway step output (agent or command) | Past `transcript_max_bytes` (§12.3) the process tree is killed and the attempt fails `transcript_limit`, under the retry policy. The line that trips the cap is written **whole** — a truncated line would turn a size failure into a parse failure for every later reader of the JSONL — and the partial transcript is kept with a closing `vincent.transcript_limit` annotation, because the lines that got there are what explain the runaway |
| A task spends past `max_task_cost_usd` | *Added 2026-08-26 (task 033).* The task goes `blocked` with `block_reason = cost_limit` and nothing further runs. It is a **block, not a step failure**: the finished `step_run` keeps its own state and its own reason, no retry is consumed (§7.2), and a retry that was already due does not run — retrying spends more money to arrive at the same wall, and that pre-empts `retry_backoff` too. The check happens at every **attempt boundary**, including inside a `loop` body and a `parallel` group, so the attempt that crossed the line ran to completion and the overshoot is at most one attempt: cost arrives on an agent run's terminal result line and nowhere else (§9.1), and there is no mid-run usage signal to poll. The remedy is to raise the cap (hot-reloaded, §12.3) and `retry`; a `retry` **without** raising it makes exactly one attempt of progress and blocks here again, which is idempotent and loses no work. `resume` is not the escape hatch — it is valid only from `paused` (§6). The cap counts one task, so each `fan_out` lane carries its own budget, and it is inert on codex and cursor, which report no cost at all (§9.3, §9.7) |
| A command emits a single line larger than one output record | *Added 2026-08-24 (#139).* Captured, not failed: the line becomes a run of `vincent.output` records marked `partial`, in order, on one stream, preserving phase, stream identity and live offsets. Minified JSON, a base64 blob and a `git diff` of a generated file all reach a megabyte on one line, so this is an ordinary command; failing it would only retry it into the same wall until the task blocked. It was previously a *silent success* — a line-bound reader stopped dead on the first such line, the rest of the stream went to `io.Discard`, and the attempt was judged from exit 0 alone |
| A transcript write, encode or close fails | *Added 2026-08-24 (#139).* The failure latches on the transcript and the attempt fails `transcript_io_error` under the §7.2 budget — disk full, a revoked permission, a short write, and ENOSPC surfaced at `Close`, which is where a buffered filesystem reports it. Never swallowed by `allow_failure:` (§7.2): vincent failing to record a step is not an outcome the step produced. Only a *success* is overridden — an attempt that already failed keeps the more useful reason. `transcript_max_bytes` is unaffected and stays the only size-based failure (§12.3) |
| An adapter cannot read its agent's stream to the end | *Added 2026-08-24 (#139).* The adapter latches its reader's error, drains the pipe so the CLI is not left blocked on it until the step timeout, and reports `agent.FailureStreamError`; the engine fails the attempt `agent_protocol_error` under the §7.2 budget. Deliberately not `agent_error`, which means "the CLI reported a failure" and would send a user to inspect a CLI that did nothing wrong — the reader that failed is vincent's. Deliberately not `input_protocol_error` either: that names a control message vincent could not render, and such a message arrived intact |
| Transcript of an archived task past retention | Deleted by the pruner at daemon start and every 24 h (§17). DB rows are never deleted; retention is measured from `archived_at`, so a long-running task archived yesterday is one day old. `transcript_retention_days: 0` disables pruning entirely |
| Base branch doesn't exist | Task creation fails fast |
| Branch already exists (or a ref hierarchy conflict blocks the name) | Rejected at creation with `400` where the name is known then; otherwise the task blocks with `branch_exists` at admission, which stays the authority. Never reused, never auto-renamed. Recover with `retry { branch_override }` (§10, task 001) |
| A pull request's head cannot be fetched | *Added 2026-08-30 (task 064).* A task created from a pull request runs on that pull request's head branch, so the fetch has nothing to fall back to. The task blocks with `pull_fetch_failed` at admission — deliberately unlike §10's base fetch, which is silent because a local base is always a valid answer |
| A local branch of a pull request's head has diverged | *Added 2026-08-30 (task 064).* Blocked with `pull_branch_diverged`. Never `reset --hard`: the local copy may hold commits nobody has pushed, and discarding them silently is the same dishonesty §10 refuses for branch names. A branch merely *behind* the head is fast-forwarded and the task proceeds; one already *containing* it is left alone |
| A pull request's head branch is checked out elsewhere | *Added 2026-08-30 (task 064).* Blocked with `pull_branch_checked_out`, naming the worktree that holds it — vincent's or the human's own main checkout. git cannot put one branch in two worktrees, and this is the honest way to say so rather than letting git's own message surface. Within vincent a second task for the same branch is already a `400` from task 001's in-transaction claim check |
| `branch_override` on a task created from a pull request | *Added 2026-08-30 (task 064).* `409`. Renaming the branch would detach the task from the pull request it was created for, so every later commit would go somewhere that pull request never sees. Such a task cannot have a `branch_exists` block in the first place — its creation mode does not refuse a pre-existing branch (§10) |
| Configured branch name is not a legal git ref | `400` with `branch_name_invalid`, quoting git's own rules. Never sanitized into something legal — a branch the user did not ask for is worse than a rejection (task 001) |
| Branch template references a field the task does not set | `400` at creation. Note that `{{.Fields.x}}` errors while `{{ index .Fields "x" }}` renders empty by design (§8.4's `missingkey=error` covers map *field* access only), and `feat/-slug` is a legal ref — so the loud form is the documented default for branch templates |
| Archive-time branch delete fails | *Added 2026-08-16 (task 008).* Checked out in another worktree (git refuses, and refuses the same under `-d` and `-D`), the base branch renamed away so the emptiness test cannot run, a remote that rejects the push or never answers inside `RemoteTimeout` — none of it fails the archive. The worktree is already gone and the task must still reach `archived`. It is logged, reported on the response as `error`/`unknown`, and the branch survives, which is the pre-008 behaviour. The remote leg cannot even be reached without a local delete that succeeded first. *Amended 2026-08-29 (task 056):* the local delete is `git branch -D` when the task recorded a `base_sha` (§10), which is why the refusal above is stated of git rather than of the lower-case flag |
| Worktree dir manually deleted | Next step fails → blocked with `worktree_missing`; retry recreates the worktree from the branch if it survives. *Amended 2026-08-15 (task 005):* the same mismatch found by a scan rather than by a step is **reported** — at daemon start and in `vincent gc`'s output — and no row is modified |
| Orphaned directory under a data root | *Added 2026-08-15 (task 005).* An entry under `{data_dir}/worktrees` or `{data_dir}/transcripts` that no task row claims — left by a project delete whose worktree removal failed (the cascade drops the rows regardless, §10) or by a crash between `git worktree add` and the claim write. Daemon start logs one warning per orphan and raises `orphans` on `GET /v1/info`; it **never** deletes, for the same reason DB corruption never auto-deletes. `vincent gc` reclaims them, and only them — archive remains the only path that removes a *task's* worktree |
| Dirtiness of an orphan cannot be determined | *Added 2026-08-15 (task 005).* An orphan's `.git` file points at `{repo}/.git/worktrees/{n}`, so a deleted or pruned repository makes `git status --porcelain` fail outright. Reported as `dirty_unknown` — distinct from `worktree_dirty`, because "git says you have local changes" and "nobody can tell what is in here" are different facts — and skipped until `vincent gc --force`. This is the *common* case where the projects really are gone, so a default run there reclaims little; that is the deliberate trade for never deleting work nobody can vouch for |
| Project path missing | New/step-starting tasks in that project → blocked with `project_path_missing` |
| Daemon port taken | Ephemeral port by default makes this nearly impossible; pinned-port conflict fails startup with a clear message |
| User wants a copy of daemon state | *Added 2026-08-25 (task 030).* `vincent daemon backup <path.tar.gz>` — one archive holding a `VACUUM INTO` copy of the database (§14), `transcripts/`, `config.yaml`, `workflows/` and a manifest. It needs a **running** daemon and refuses without one, in `doctor --fix`'s words: only the daemon opens the database. It needs no quiet daemon, so a backup may be taken while tasks run. `vincent daemon restore` is the reverse and needs a **stopped** daemon; it refuses a manifest whose schema version exceeds the binary's, and an occupied destination without `--force`, which moves the displaced state aside as `<name>.bak-<ts>` rather than deleting it — the same posture as the row below |
| DB corruption | Startup fails loudly, points at the file, never auto-deletes. *Amended 2026-08-25 (task 030):* what rescues this case is an **earlier** good copy, which is what `vincent daemon backup` is for; a fresh copy of the damage is not a remedy, and taking one is not offered as a cold-copy mode |
| Agent emits gigabytes of output | Transcript writes are streamed to disk; SSE output chunks are rate-limited/coalesced (~10 Hz); per-run transcript size cap (`transcript_max_bytes`, default 512 MB) fails the step past the cap with `transcript_limit` |
| Template references missing field | Step fails at render time (before any process starts) with the template error. *Amended 2026-08-28 (task 044):* this outcome is now reachable without creating a task — `vincent workflow render <file>` executes the same templates against the §8.4 preview context and names the step and the field |
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

\* **"author workflow" meant editing in place** when M3 was accepted, not
creating a file: `e` opened an existing entry in `$EDITOR`, the registry reload
reflected the save, and new files were written in the editor and appeared on the
next reload. The M3 walkthrough exercised that edit path and required no create
path, because the TUI deliberately had none.

*Amended 2026-08-30 (task 065, issue #261).* It now means either. The TUI
creates, edits and forks workflow files through structured forms (§15 view 5,
`a`/`i`/`f`), backed by `POST` and `PATCH /v1/workflows` (§13.2). This does not
rewrite M3's history — the milestone was accepted on the edit path, and that
path is unchanged — it records what the phrase means to a reader walking the
loop today.

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
without Authenticode/Apple notarization, package metadata preserves the MIT
license, and no root package script registers vincent's
per-user service. External bootstrap and first-release proof are tracked in
`docs/tasks/021-package-distribution-channels.md`; documentation must not infer
catalog availability from a successful local manifest render.

**Amended 2026-08-26 — macOS OS code signing is accepted; Windows Authenticode
is not.** This reverses the **Apple half** of the † descope above, and it
supersedes the 2026-08-14 amendment's "the binaries are still not notarized …
so the `xattr` instructions stay". The ~$99/yr Apple Developer Program cost that
the descope priced correctly is now paid, in exchange for a macOS install path
that clears Gatekeeper on its own rather than by stripping the quarantine
attribute — which the cask had been doing, and no longer does. Darwin binaries
are `codesign`ed with a Developer ID Application identity under the hardened
runtime and a secure timestamp, inside a build hook so the signature is in the
Mach-O before archiving and before `checksums.txt`; those binaries are notarized;
and a new universal `vincent_*_darwin_universal.pkg`, signed with a Developer ID
Installer identity, is notarized and **stapled**, which is the only artifact here
that can carry a ticket and therefore the only one that clears Gatekeeper
offline. **The direct-download macOS path now meets Gatekeeper**: neither
`xattr -d com.apple.quarantine` nor `brew`'s former `postflight` hook is part of
any documented install, and re-adding either would bypass the protection just
bought. Windows Authenticode remains descoped for exactly the reasons the X
decision gave — an OV certificate on a hardware token is a recurring purchase
with no equivalent to Apple's single notary service — so §19's SmartScreen
wording, `docs/platforms/windows.md` and the WinGet installation notes are
unchanged. A Microsoft Store MSIX was weighed as a way to obtain a
Microsoft-applied signature without buying a certificate, and rejected in the
same session. The release job consequently builds on `macos-latest`
(`codesign`/`notarytool`/`stapler`/`pkgbuild` exist nowhere else, and
GoReleaser's own `notarize:` block is Pro-only); the single-runner shape the X
decision chose survives. The `.pkg` is deliberately **not** in `checksums.txt` —
it is built after the GoReleaser run, because a universal binary needs both
darwin slices — and is covered by Apple's installer signature plus a GitHub
build attestation instead. Reasoning and the external enrolment blocker are in
`docs/tasks/032-macos-notarization.md`.

**Amended 2026-08-27 — deb and rpm ship deliberately unsigned.** The 2026-08-20
amendment above generated deb and rpm assets without deciding whether they
should carry a maintainer signature; `nfpms` has never had a `signature:` block,
which until now was an undecided gap rather than a decision. It is decided: the
packages stay unsigned, and `nfpms` is correct as it stands. Vincent publishes
**no APT or YUM repository**, and `dpkg`/`apt` do not verify a per-package
signature on a `.deb` downloaded from a release page at all — apt verifies a
repository's `Release` file — so signing the deb buys nothing on the path
vincent's users actually take. `rpm -K` *does* verify once its key is imported,
and that is the half of the case with real content; the alternative it beat was
therefore adding `signature:` with a **release-held GPG key**, publishing that
key and documenting `rpm -K`. That was declined because it improves the rpm path
only, leaves the deb path untouched, and trades this project's keyless supply
chain for a long-lived secret with publication and rotation duty — precisely what
keyless cosign exists to avoid. Both formats remain covered by cosign over
`checksums.txt` and by GitHub build attestations, which is what
`docs/platforms/linux.md` now states beside the existing "no Gatekeeper or
SmartScreen equivalent" note. Reasoning in
`docs/tasks/038-release-signing-posture.md`. **Windows Authenticode is untouched
by this amendment and remains descoped**: the free-for-OSS signing route is being
surveyed under task 038, and §19 will not describe a Windows signature before one
exists.

**Amended 2026-08-27 — the macOS signature is conditional, and today absent.**
The 2026-08-26 amendment above says the ~$99/yr Apple Developer Program cost
"is now paid". **It was not**, and the pipeline it describes was written as
though it had been: `MACOS_SIGN_REQUIRED` was keyed on the *tag*, so a `v*` tag
without the certificates was a hard error. `v0.7.0` proved what that costs —
the tag build died at its first signing step and produced no archives, no deb or
rpm, no attestations and no Homebrew, Scoop or WinGet metadata, and the release
was unwound. Signing is therefore keyed on the *certificates* instead: with them
configured a tag must not publish an unsigned macOS artifact, and without them
every signing step warns and the release ships **unsigned**. An unsigned release
is worse than a signed one; no release is not a release. The macOS install path
consequently meets Gatekeeper again — the direct-download path documents `xattr
-d com.apple.quarantine` once per download, the `.pkg` is installed with
right-click → *Open* or `sudo installer`, and the Homebrew cask's
quarantine-stripping `postflight` hook is **restored**, because brew installs the
same unsigned archive and would otherwise deliver a binary that will not start.
The rest of the 2026-08-26 amendment stands unchanged and is not relitigated:
every mechanism it describes is implemented and dormant, and installing the six
`MACOS_*` secrets is the whole of the switch — the `.pkg` is still built, still
universal, still outside `checksums.txt`, still covered by a build attestation,
and the release job still runs on `macos-latest`, now for `pkgbuild` alone. The
enrolment blocker stays 032.7's; **this amendment is not a decision to abandon
signing**, only to stop a missing certificate from destroying a release. Apple's
fee waiver reaches nonprofit, educational and government *organizations* only,
which task 038 already recorded as unavailable here. Reasoning in
`docs/tasks/039-unsigned-releases-by-default.md`.

**Amended 2026-08-27 (later the same day) — macOS OS code signing is descoped
again; the machinery is retained.** The amendment immediately above deliberately
left the enrolment open. It is now closed: the ~$99/yr Apple Developer Program
membership is **not being bought**, 032.7 is dropped, and the 2026-08-26
amendment's reversal of the Apple half of † is itself reversed. **Neither
desktop platform carries an OS code signature**, which returns §19 to the shape
the X decision gave it — macOS Gatekeeper and Windows SmartScreen both prompt on
first launch, and the documented macOS path is `xattr -d com.apple.quarantine`
once per download, with the Homebrew cask stripping the attribute itself. What
does **not** revert: the `.pkg`, which stands on being one universal binary at a
fixed install path rather than on a stapled ticket; cosign over `checksums.txt`
and the GitHub build attestations, which were never conditional; and the signing
implementation itself, which is complete, verified and kept dormant so that
installing six repository secrets is the entire cost of reversing this — the
decision was made on price, and is meant to stay cheap to unmake.
`docs/tasks/032-macos-notarization.md` is retained as that design and is no
longer a description of what ships. Windows is untouched here: task 038's
free-for-OSS Authenticode survey continues, and a free route accepted there would
sign Windows while macOS stays unsigned. Reasoning in
`docs/tasks/039-unsigned-releases-by-default.md`.

**M4's acceptance is met, 2026-08-11.** The T4.6 walkthrough ran on a clean VM
per OS with no Go toolchain, against the `v0.1.0-rc1` artifacts: **5:00** on
Windows 11, **4:30** on macOS, **3:35** on Linux — every run under half the
ten-minute budget, and the slowest is the OS carrying SmartScreen, which prices
the † descoping at roughly its gap to Linux. Details in tasks.md T4.6.

## 20. Future work (explicitly out of v1)

- Web UI on the same API; auth story for non-loopback exposure.
- ~~MCP server so agents can drive vincent directly~~ — **promoted out of
  future work on landing, 2026-08-29** (§13.4, task 057, issue #243). It was
  never listed here, so this is a new entry recorded as promoted rather than a
  strike-through of a deferral. Still deferred, and named here so the next
  person does not have to rediscover them: a **`vincent mcp` stdio subcommand**
  for MCP clients that cannot set an `Authorization` header (the tool
  definitions would be shared; it is a process per client, which is why it is
  not the primary shape), and a **narrower default tool surface for wired
  steps** specifically, if ~40 tool schemas prove costly in a step agent's
  context — the answer there is a different default for that one caller, not a
  different rule for external clients.
- ~~OS desktop notifications (blocked / gate / awaiting input / done)~~ —
  **the outward-signalling half is done, 2026-08-28** (§12.3 `notify:`, task
  046, issue #90): the daemon runs a command of the user's choosing on any §6
  state, with an enriched envelope on stdin. The **platform-native** stack —
  three OS backends and the packaging that comes with them — stays deferred,
  and the exec hook is why that is now cheap: `terminal-notifier`,
  `notify-send` and `msg` are all one `command:` line away.
- ~~Free chat: conversational agent sessions beside tasks~~ — **promoted out of
  future work on landing, 2026-08-30** (§5.5, task 063, issue #255). Like the
  MCP entry above it was never listed here, so it is recorded as promoted rather
  than struck through. Named here so the next person does not rediscover them,
  the pieces deliberately left out of the first cut: **codex `exec resume
  <thread_id>` and cursor `--resume`**, each of which lands with a fixture
  captured against a named CLI version the way every other adapter capability
  has — until then those adapters are *refused* at chat creation, never
  emulated by replaying a log as prompt context; a **`notify.chat_on` key**, if
  `awaiting_input` on a long-open chat proves to need an outward signal (§12.3);
  and **chat routes as MCP tools**, which stays refused on the design line in
  §13.4 rather than merely deferred.
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
- Task templates & recurring tasks; ~~issue-tracker ingestion (Jira → task)~~ —
  **the GitHub half promoted out of future work, 2026-08-26** (§5.3, §8.4,
  §12.3, §13.2, §14, §15; decision record row 26, task 035): a task can be
  created *from* a GitHub issue, which prefills it and reaches templates as
  `.Issue`. It is **reading only** — nothing writes to GitHub, so decision
  record row 11 is untouched — and the issue is snapshotted at creation rather
  than re-fetched, so the step path is unchanged. Jira and task templates stay
  deferred; nothing here was a decision *against* them, only a v1 scope line,
  and v1 shipped. ~~**Pull requests** — checking, listing or reporting on them
  — are the intended next piece and are deliberately not built~~ — **promoted
  out of future work, 2026-08-29** (§5.3, §12.3, §13.2, §13.3, §14; decision
  record row 27, task 052): a project's open pull requests are listed, and a
  task is linked to the one opened from its branch by a daemon-side reconciler.
  It is **reading only** — decision record row 11 stands unamended, and the
  "create a PR" affordance is a constructed compare URL a human clicks, not a
  write. The one thing this paragraph predicted wrongly is the migration: the
  task column could *not* carry a PR shape, because `github_issue_json` is
  defined as "NULL = no linked issue" holding a bare `Issue`, so `github_pull_json`
  is a sibling column (migration 0018) rather than a widening.
  **Promoted further, 2026-08-30** (§5.3, §8.5, §10, §13.2, §15, §18; decision
  record row 27, task 064): a pull request is not only visible but **runnable** —
  a task can be created from one and its worktree is that pull request's head
  branch, checked out with an upstream, so the agent's commits reach the pull
  request when a workflow pushes. Still reading only: no write method, no `POST`,
  no mutating `gh` subcommand, and row 11 stands. The costs are recorded where
  they land rather than here — §10 gained a second worktree creation mode and an
  archive exception (vincent never deletes a branch it did not cut), §18 gained
  three block reasons, and the branch-name chain gained a level above the
  per-task literal.
- ~~Container/VM-sandboxed step execution~~ — **the container half is
  promoted out of future work, 2026-08-30** (§16, task 061, issue #256): a
  `container:` block names an image, and a task's step processes run inside one
  container created with its worktree and removed with it — command steps and
  checks as of task 061, agent steps once **062** lands the spawn seam the three
  adapters need. The image
  is the user's and must already carry the agent CLI and `git`; vincent builds,
  publishes and bundles nothing, which is the posture it already takes toward
  `gh` and `cosign`. **VM-level** sandboxing stays deferred, and so do three
  container-shaped things, named here so they are not rediscovered: a
  **per-task image override** (task 061 decision 6 — two levels ship, workflow
  `defaults:` over `config.yaml`; the trigger is the first person who needs one
  task run against a different image), **canonical-path mounting for Windows
  hosts** (decision 2 — paths are identical inside and out, so a Windows daemon
  refuses a containerized task rather than translating `C:\...` into something
  a Linux container could hold; the trigger is a Windows user asking), and
  **Windows container images**, a no-network-by-default profile,
  `devcontainer.json` support and vincent-published images.
