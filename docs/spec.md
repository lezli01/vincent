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
  moves them in. *(Amended 2026-09-17, task 062.2, issue #397: it has — an
  agent step of a containerized task now runs inside that container too, so no
  step process of such a task is spawned on the host.)* The
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
| 11 | Delivery | *Rewritten 2026-09-15 (task 068 decision 1, task 068.4, issue #386). It read "owned entirely by workflow steps; no hardcoded push/PR/merge behavior", with a human-initiated exception for pull-request creation added 2026-08-31 (task 069, issue #273); both are in this file's history and in the task 068 and 069 records. The row is obsolete rather than narrowed, so it is replaced rather than qualified.* **vincent delivers, human-triggered.** A human in vincent may push a task's branch to `origin` and open its pull request (§13.2 `POST /v1/tasks/{id}/github/pull/create`, task 069), and may merge, close, reopen and comment on a task's linked pull request and re-run the failed jobs of its GitHub Actions runs (§13.2 `…/github/pull/merge`, `/close`, `/reopen`, `/comment`, `/checks/rerun`, task 068.4). **Nothing on the step path reaches GitHub**: no step type, no default workflow and no automatic behaviour delivers, so §8.4's property that a step render cannot fail for an external reason is untouched, and every write route is excluded from the MCP tool surface (§13.4), so an agent cannot reach one through vincent. An agent's own path — a step running `git push`, `gh pr create` or `gh pr merge` in its full-auto worktree (§16) — was the original one and stays open. A `merge` step type is **not licensed** by this row. The push **never forces** — a diverged, protected or rejected push creates no pull request and changes nothing on the remote (§18) — and a merge is refused before anything is sent when a preflight read says GitHub would refuse it, and is pinned to the head commit the human confirmed. The reasoning is recorded because this is the decision most likely to be re-litigated: the round trip to a browser is the cost this exists to remove, and a checks view that cannot merge the pull request whose checks it just showed you is half a sentence |
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
| 27 | GitHub pull requests | **Daemon-side, and read-only until task 069**, like row 26 and through the same gate and credential. *Amended 2026-08-31 (task 068): the read side grows a live **check rollup** for a linked pull request's head commit — `GET /v1/tasks/{id}/github/pull/checks`, one normalized row per check run and per legacy commit status from either leg, never stored — and unlink gains a second home on the task workspace's Pull Request tab (§15 view 2). Task 068 decision 1 settled that merge, close, re-run and comment are written from the TUI only, human-triggered, and 068.4 is the sub-task that lands them and rewrites row 11.* A project's **open** pull requests are listed on demand, and a task is linked to the pull request whose head branch equals its own `branch_name` — by a daemon-side reconciler on a `github.poll_interval` tick, never as a side effect of a GET. Only the *link* is stored (`github_pull_json`: repo, number, source, suppressed) and it is a **pointer, not a snapshot** — the deliberate opposite of row 26, because draft, state and merged status are live by nature and a stored copy of them would read exactly like a current one while being wrong. A human may link or unlink; a human unlink is *sticky* and the reconciler never re-applies it, never overwrites a human link and never un-suppresses one. **Row 11 stands unamended**: vincent pushes nothing, opens nothing and merges nothing, and the “create a PR” affordance is a *constructed* compare URL — no request is made to GitHub when it is built — that a human clicks. `internal/github` gains no write method, no `POST` and no mutating `gh` subcommand. Task 035 decision 5's “repo identity is not stored” was revisited exactly as it predicted: the identity landed on the **task**, beside the number, and no `github_repo` column was added to projects (§5.3, §12.3, §13.2, §13.3, §14, §20; task 052, added 2026-08-29). *Narrowed 2026-08-30 (task 064):* the read-only posture holds in full — no write method, no `POST`, no mutating `gh` subcommand — and a task may now be created **from** a pull request and run on its head branch. That adds a flag to the same envelope (`branch`, `fork`) rather than a snapshot: nothing renderable is stored, so "a pointer, never a snapshot" is unchanged, and there is still no `.Pull` template variable. The consequences live in §10 (a second worktree creation mode, and archive never touching a branch vincent did not cut) and in §5.3's branch-name chain, which gains `pull` above the per-task literal. The listing above is narrowed the same way: it still **defaults** to open, but `?state=` (§13.2) makes a closed or merged pull request reachable, because acting on a merged one and redoing a reverted one are exactly what creating a task from one is for *Amended 2026-08-31 (task 069, issue #273):* the read-only posture gains **exactly one write path** — pull-request creation, from a human. `internal/github.CreatePull` is the only method here that writes, on both legs (`gh pr create`, `POST /repos/{owner}/{name}/pulls`); nothing updates, comments on, closes or merges anything, and `github.enabled` is the only gate on it (§12.3, decision 2: the consent is the keypress and the editable popup in front of it, not a second config key nobody would turn on). Every *other* half of this row is unchanged and load-bearing: the link is still a pointer and never a snapshot, the listing is still pure, the reconciler still never overwrites a human link, and the compare URL is still built by string construction with no request made — it is now the **fallback**, opened when there is no write credential or the create call fails, and the branch behind it has been pushed, so it is no longer a dead page. A create writes the link immediately as `source: human`, which is why the reconciler's poll interval does not make a just-created pull request read as unlinked. *Amended 2026-09-15 (task 068.4, issue #386):* the "exactly one write path" above, its "nothing updates, comments on, closes or merges anything", and the task 052 "Row 11 stands unamended: vincent pushes nothing, opens nothing and merges nothing" are no longer true — row 11 is rewritten. `internal/github` gains five more human-triggered writes on both legs: `MergePull` (`gh pr merge --match-head-commit`, `PUT /pulls/{n}/merge` with `sha`), `ClosePull` and `ReopenPull` (`gh pr close`/`reopen`, `PATCH /pulls/{n}`), `CommentPull` (`gh pr comment --body-file -`, `POST /issues/{n}/comments`) and `RerunFailedJobs` (`gh run rerun --failed`, `POST /actions/runs/{id}/rerun-failed-jobs`). Each acts only on a task's **live** link, `github.enabled` is still the only gate, and each route is excluded from MCP (§13.4). A merge reads first: GitHub's merge state and, when the merge is blocked, the live check rollup map onto named refusals before anything is sent — and the merge state is **not** a `PullRequest` field, because the REST listing cannot fill it. A re-run is validated against the live rollup: only the run behind a failed, Actions-backed row of the current head. The reason vocabulary grows by `no_write_scope`, `not_mergeable`, `checks_running`, `branch_behind` and `head_changed` (§18). A 403 on **any** write, `CreatePull` included, is now `no_write_scope`, so the 069 fallback carries it where it carried `forbidden`; `forbidden` is the read side's alone. Everything else here stands: the link is a pointer, the listing is pure, and nothing about a pull request is stored |
| 28 | MCP from the daemon | **A second protocol on the existing listener, not a second server.** `/mcp` is registered in §13.2's route table inside the same `recover → log → auth` chain, so row 4 is *added to*, not reversed: same loopback listener, same `Authorization: Bearer {token}` from `{data_dir}/token`, same `daemon.json` discovery. The tool surface **is** the route table — a call replays its arguments as an in-process request against the same handler, so the §13.1 bounds, the validation, the `409` + `details.state` envelopes and `Idempotency-Key` hold by construction — **minus five destructive-admin routes** (`daemon/stop`, `daemon/backup`, `DELETE projects/{id}`, `maintenance/gc`, `doctor/fix`), which is a design line: an agent must not be able to stop, garbage-collect or reconfigure the daemon supervising it. §13.3's SSE routes are replaced by a bounded blocking `task_wait` with a hard ceiling, whose result is complete for a client that drops every progress notification. A step parked in that wait **keeps its §11 slot** and a self-blocking wait is *refused*, not released — releasing it would create a §6 state owning a live agent process and holding no slot, which no state does today. The daemon wires its own agent steps to a **per-step endpoint** (`/mcp/step/{run_id}`, per-run secret), which is identity for the refusal and the provenance column and is explicitly **not** a security boundary (§16). Recursion is bounded by `created_by_task_id` + `mcp.max_depth`/`mcp.max_tasks`, deliberately **not** by `parent_task_id`, which the `awaiting_children` join counts (§9.1, §9.2, §9.3, §9.4, §9.7, §11, §12.3, §12.4, §13.4, §14, §16, §20; task 057, issue #243, added 2026-08-29). *Amended 2026-09-14 (issue #377):* the five were task 057's list; §13.4 carries the current exclusions — sixteen admin, workflow, trigger, forge-write, quota and permanent-delete routes, plus the whole ten-route chat family |
| 29 | Free chat | **A first-class entity beside Task, never a task with a `kind` column.** A `chat` is a titled conversation with an agent, scoped to a project, running in its own git worktree and `vincent/{id}-{slug}` branch, with its own four-state lifecycle (§5.5), its own `chats`/`chat_turns` tables (§14) and its own route family (§13.2). It never appears on the task board, in `GET /v1/tasks` or in any §17 aggregate over `step_runs`, whose `task_id` stays `NOT NULL`. Continuity comes from the **agent CLI resuming its own session** — §7.3's fresh-session rule is amended *for chats only* — so turn N sees turns 1..N-1 without vincent replaying any log as prompt context. A turn is bounded by its own cap `max_parallel_chats` (default 3) and is **refused with 409, never queued**: `internal/scheduler` stays the only place `queued → running` happens because a chat turn is never `queued`, and row 28's "no live-but-uncounted agent CLI" reasoning is extended rather than excepted (§11). **No chat route is an MCP tool** — row 28's exclusion list grows by the whole chat family, because an agent must not start unqueued agent processes and `mcp.max_depth`/`mcp.max_tasks` bound tasks by walking `created_by_task_id`, a chain a chat is not in (§13.4). Only adapters that can resume may hold a chat: claude yes (§9.2), **codex and cursor are refused at creation with a typed reason, not emulated** (§9.3, §9.7). *Amended 2026-08-31 (issue #279):* `GET /v1/agents` publishes that answer as `supports_resume` (§9.6) so a client's picker offers only adapters that can hold a chat; the creation-time refusal is unchanged and stays the authority, and an absent field — an older daemon — filters nothing. A stored session the CLI no longer knows fails the turn with `session_lost` and leaves the chat usable; a turn interrupted by a daemon restart is finalized `interrupted` and is **never re-run**, because re-running would re-send the human's message into a session that died with the process (§12.4). Chat worktrees join gc's claim namespace and worktree directories are named by owner, so chat 7 and task 7 cannot collide (§10). §16 is untouched: chats are full-auto by default exactly as tasks are (§5.5, §6, §7.3, §9.1, §9.2, §9.3, §9.7, §10, §11, §12.3, §12.4, §13.2, §13.3, §13.4, §14, §15, §20; task 063, issue #255, added 2026-08-30). *Amended 2026-08-31 (task 067, issue #269, closing 063.2 and 063.3):* chats reach the TUI, as **two views of their own** — a chats board and a chat workspace — never as rows on the task board, so "it never appears on the task board" is unchanged and now literal in the client too. Attention is the chats board's own: an `awaiting_input` chat is pinned and badged there and nowhere else, and `!` and the home board's needs-attention count stay task-only. A chat turn is bounded by §7.2's `agent_timeout` and §7.4's `input_timeout` verbatim, so the slot this row says it holds is no longer held forever (§11, §12.3, §13.2, §13.3, §13.4, §15). *Amended 2026-08-31 (task 070, issue #268):* **codex resumes now**, so "codex and cursor are refused at creation with a typed reason" reads **cursor alone** — codex met the precondition task 063 decision 3 attached to it, a capture against a named build (codex-cli 0.150.1) pinning `codex exec --json resume <thread_id>`, and its `thread.started` id is read into `RunResult.SessionID` (§9.3). Nothing else in this row moves: continuity still comes from the CLI resuming its own session, vincent still replays no log as prompt context, and the refusal is still the authority for the adapters that cannot (§9.7). *Amended 2026-08-31 (task 071, issue #282):* a chat's live output is **normalized in the daemon exactly as a task's is** — §13.3's typed chunks, with the verbatim line kept as `raw` — and the chat workspace renders it with the output pane's own renderer at the session's shared verbosity level. The chat is still its own entity; what it is not is its own rendering vocabulary *Amended 2026-08-31 (task 072, issue #283):* **cursor resumes too**, pinned to a capture against cursor-agent 2026.08.11-e8db854, so "codex and cursor are refused at creation" is retired entirely and **no shipped adapter is refused**. The refusal path is unchanged and unretired — it is the contract for the next adapter — and is now proven against a stub adapter rather than a shipped one, which is what stops it asserting the opposite of the truth the day a capability lands. Two consequences are stated positively rather than worked around: a resumed codex run is always full-auto, because `codex exec resume` has no `--sandbox`, guarded structurally by chats having no requestable permission mode; and cursor cannot report a lost session at all, because it adopts an unknown `--resume` id and answers rather than refusing (§9.3, §9.7). *Amended 2026-09-01 (task 074, issue #288):* a chat has **two** terminal states, not one — an idle chat may `hand_off` its worktree and branch to a task that adopts them verbatim, and `handed_off` is terminal because reusing `archived` would run the archive path, which removes the worktree this transfers. The chat remains a separate entity and this row's "never a task with a `kind` column" is untouched: what is added is a lifecycle transition *between* the two entities, with one authoritative foreign key (`chats.handoff_task_id`) and the reverse direction served by a lookup. It is one transaction — task row, branch claim, link, transition, claim release, both events — with the scheduler notified after the commit, so the scheduler cannot admit a task before it owns a complete workspace and gc never sees the directory claimed twice or not at all. The task becomes the sole owner of that worktree and branch (§10). The new route joins this row's own MCP exclusion list rather than excepting it, which is why it is a chats-family route and not a field on `POST /v1/tasks` (§13.4). §7.3 is untouched: the chat's session is not transferred, and workflow steps still start fresh (§5.5, §10, §12.3, §13.2, §13.3, §13.4, §14, §15). *Amended 2026-09-17 (task 119, issue #472):* "running in its own git worktree" now describes a **free** chat. A chat may instead be **linked** to a stopped task — opened by the §6 action `chat` from `blocked`, `awaiting_gate`, `done` or `aborted` — and work in that task's worktree and on its branch, which the task goes on owning alone; the chat claims nothing (§10). While it is open the task is **locked**: every §6 action but `cancel` is refused, checked inside the store's compare-and-swap. A linked chat ends in a **third** terminal state, `closed`, under a transition table of its own that has no `archive` and no `hand_off`. It is still a chat — still no `kind` column, still off the task board, still excluded from MCP by this row's own list, and its turns' cost stays on the chat rather than the task. A linked chat takes its task's permission mode, so task 072's "guarded structurally by chats having no requestable permission mode" no longer guards a resumed codex run: that combination is now refused at open and by the adapter, failing closed (§5.5, §6, §9.3, §10, §12.4, §13.2, §13.3, §13.4, §14, §15, §16, §17, §18) |
| 30 | Archived boards and permanent delete | *Added 2026-09-09 (task 092, issue #350).* **Archived history is a screen, and a permanent delete is a route.** Two TUI views — archived tasks, archived chats — are the live boards *in a second mode* rather than two new models (§15): the archive needs grouping, folding, `/` and the bulk selection, and a copy would drift on the first change to any of the four. They get palette rows and no keys, because task 049 retired `1..6` to stop adding memorized ones and task 067 gave chats the same treatment. `DELETE /v1/tasks/{id}` and `DELETE /v1/chats/{id}` (§13.2) are the only things in vincent that delete a task or chat row — the §17 pruner removes transcript *files* and never a row, and that sentence in the retention table is amended to say so. Delete is **not a §6 action**: `taskstate` has no opinion on it and it never appears in `available_actions`, which is what makes the workspace an archived row opens read-only for free; its precedent is `DELETE /v1/projects/{id}`, likewise no action, and both routes join that route's §13.4 destructive-admin exclusion. It **refuses rather than cascading**, naming the row that is holding on: a live row (`not_archived`), an archived fan-out parent whose lanes still exist (`has_lanes` — `parent_task_id` has no `ON DELETE` clause, so without the guard it is a driver error), a `handed_off` chat (`handed_off` — the task owns the worktree, §5.5), and an archived task such a chat points at (`handoff_target`). §10's standing rule is untouched and its task 008 exception merely widens to "at archive time **and at permanent delete**": a branch carrying any commit past its base is reported `has_commits` and kept whatever was answered, and the remote leg is not offered at all. Two durable events are added, `task.deleted` and `chat.deleted` (§13.3) — PR D's "there is no separate `task.archived` type" does not reach them, because that type was redundant with `task.state_changed` and a delete has no state to change to — while the historical `events` rows are deliberately kept, their id being the `Last-Event-ID` cursor. No migration: `archived_at` has been a column since `0001_init.sql`, chats measure the same window over `updated_at` by task 074 decision 6, and every cascade this needs already exists. There is no bulk endpoint and there is not going to be one (task 011): every sweep, in the TUI and in `vincent task delete --before`, is one `DELETE` per row (§5.5, §6, §10, §13.2, §13.3, §13.4, §15, §17, §20) |
| 31 | TUI key vocabulary | *Added 2026-09-10 (task 093, issue #353).* **One operation, one key, and the registry is what says so.** The binding registry made the help *accurate* from T3.11 — `?`, the footer and the palette all render from it — which is exactly what let the *vocabulary* drift unseen: it faithfully advertised four different keys for "refresh". §15 now carries the table (refresh `R`, archive `A`, delete-a-persisted-record `D`, remove-a-draft-row `d`, add `a`, `$EDITOR` `e`, free text `t`, browser `o`, open-the-row `enter`, cycle-a-listing `s`, filter `/`, fold, lane `l`, page) and **three clauses, not the one the issue asked for**: a key may be shared only for the same operation; it may mean two things only where the registry can prove the surfaces never co-exist; and a key already carrying a term takes no second meaning. The second clause is task 025's deliberate partition of `R` promoted from an accident to the rule, which is why "exactly one key registry-wide" was not adopted literally. The §6 action letters `p a x r E R s c A F` **do not move**, so they decide the contested cases: `R` won refresh, `A` won archive, and `D`/`d` split on persisted-versus-draft, which is what makes pressing `d` on an archived board safe. Enforcement is three tests in `internal/tui/bindings_test.go` beside `TestEveryPanelKeyIsHandled`, with an allow-list that must carry a reason and must stay non-empty; the disjointness the archived boards rely on is **derived from `taskstate.HumanActionsFrom`**, not listed, so an FSM change that starts offering an action on an archived row fails the test rather than shipping a shadowed key. It closes a live bug rather than only a style one: the task workspace's Pull Request tab intercepted `r` and `c`, so **retry and cancel were unreachable there** while the footer advertised both. Scope is `internal/tui` and the docs — no CLI, API, MCP, store or workflow change, and no user-configurable keymap, which is a larger question this does not answer (§15). *Amended 2026-09-17 (task 118, issue #412):* "no user-configurable keymap" is narrowed to no keymap **outside these rules**. Row 35 adds `tui.keys`, which makes this table the **default** keymap and holds an override to the same three clauses with the same checker, so the question is answered rather than set aside; the §6 letters still do not move as defaults |
| 32 | The footer fills its width, and says what it hides | *Added 2026-09-10 (task 094, issue #352).* **A cap is not a layout.** The footer's five-key limit was a constant, and a constant is wrong at both 80 and 200 columns: eleven of the twenty-one binding contexts declare more hinted keys than five, so on more than half the surfaces keys were dropped silently while `pad := max(width-lw-pw, 2)` spent the remaining columns on blank space. Width now decides, as a strict prefix of registry priority order, measured exactly rather than iterated: every candidate admission count is costed against the `+N` that count itself implies, so there is no "admitted because +9 shrank to +8" state to detect afterwards. The segments to the right of the hints are measured **first** and come out of the budget — the line truncates from the left, so hints are what a full line loses first, and admitting them against the whole width would let the actions push them straight back off. This **supersedes** the phase 3 refactor decision and the PR R / T3.12 decision in `docs/history/v0-tasks.md` ("max 5, priority-ordered"), and only those: what they were protecting — one line that never wraps, and a pinned `: commands  ? help  q quit` that never truncates — is untouched, and §15 is amended in place to say so. The `+N` counts this surface's palette-reachable rows that the line is not advertising, **not** everything the palette lists: the five global rows and the eight navigation entries are what the pinned segment stands for, and counting them would pin `N` near fourteen and never at zero. Alias rows are **declared** (`binding.aliased`) rather than parsed out of hint text — splitting on `/` and mapping `↑↓←→` back to key names is text parsing over a human-written field that breaks silently the first time a hint is reworded — and a test asserts the declaration against what the hints actually say. `paletteEntries` still lists the board's fold rows where the footer, gated by `shell.liveBindings`, does not; that mismatch is left where it is rather than widened into here. *Amended 2026-09-14 (task 096):* the triggers view is a ninth navigation entry, so the pinned segment stands for nine; the reasoning is unchanged (§15). *Amended 2026-09-14 (issue #372):* the mismatch is gone — `root.openPalette` hands `paletteEntries` the shell's `liveBindings`, so a flat board's palette drops the fold rows the footer drops, and a grouped board's still lists them |
| 33 | Event triggers | *Added 2026-09-13 (task 096; issues #356, #362, #365).* **A trigger is a robot pressing a key a human could have pressed.** A YAML file under `{config_dir}/triggers/` names a source (`command`, `github_issues`, `github_prs`, `http` or, since 2026-09-18, `schedule`), a `match:` prefilter and an §8.4 `if:` guard, an action and a dedupe key. Triggers are global scope only, never `.vincent/`, so that anyone who can merge to a repository cannot start agents on a maintainer's machine. An action **replays an existing route** in-process, the way row 28's MCP does: `create_task` is `POST /v1/tasks`, and the reactions `follow_up`, `retry` and `cancel` are the §6 action routes against the task whose `branch_name` the event names. §13.1's bounds, the validation, the FSM's 409 and `Idempotency-Key` therefore hold by construction, and from the step path down a triggered task is indistinguishable from a hand-created one (`.Event` is never snapshotted, §8.4). Where a route lacked an affordance a trigger needed, the route grew it for every client: `paused` on create, follow-up and retry, and `restricted` and `max_task_cost_usd` on create. **It inverts §16's premise that a human pressed the key**, so its defaults differ from every other default in the product. Triggers are off twice, by `triggers.enabled` and by each file's own `enabled:`. `on_fire: propose` holds every task a trigger creates or re-queues in `paused` for a human, agent steps are clamped `restricted`, untrusted GitHub events are refused without an author allowlist, and each trigger has a rate limit. Runtime state lives in SQLite: a cursor per trigger, and a delivery ledger kept 30 days. The first poll after arming seeds and fires nothing, so neither a cold start nor re-arming floods. *Amended 2026-09-18 (task 121, issue #480):* the source list takes a fifth member, `schedule` — a hand-written five-field cron expression or an `every:` interval, evaluated against the wall clock on a one-second tick, with its last handled occurrence in `trigger_cursors.cursor`. It is the same robot pressing the same key: everything downstream of the source is reused, arming anchors the clock and fires nothing, and an overdue schedule fires once however many occurrences it missed. *Amended 2026-09-18 (task 122, issue #483):* a trigger also says what to do when **its own previous work is still running**. `overrun:` — `parallel` (the default and today's behaviour), `skip`, `cancel_previous`, `queue_coalesce`, `queue_serial` — is consulted as the pipeline's last step against the group `concurrency_key:` names, and "in flight" is §6's `!Settled`, so an unreviewed `on_fire: propose` proposal holds its group. The two queue modes hold events in a durable table so a restart loses none, and a disarm discards that backlog the way it drops the cursor. `cancel_previous` is the one mode that destroys work nobody asked to lose, and is `dangerous` to a client for that reason (§5.3, §6, §8.4, §12.2, §12.3, §13.2, §13.3, §13.4, §14, §15, §16, §17, §20) |
| 34 | Trigger ingress | *Added 2026-09-13 (task 096 decision 31G).* **A pushed event needs the bearer token and a signature, and no route is exempt from row 4.** `POST /v1/triggers/{id}/events` sits in the same `recover → log → auth` chain as every other route, then verifies the trigger's own `github_hmac_sha256` signature over the raw body, using a secret the daemon reads from its environment (§2). Only a caller on this machine that can read `{data_dir}/token` and holds the secret can deliver, so rows 1 and 4 are untouched. A GitHub.com webhook through a tunnel **cannot** deliver. What works is a sender on the same machine, such as a self-hosted runner, or a relay on the same machine that adds the header. The route is not an MCP tool, because an agent that can inject events can start agents (§13.1, §13.2, §13.4, §16) |
| 35 | User-configurable TUI keymap | *Added 2026-09-17 (task 118, issue #412).* **An override moves an operation, never a surface's key, and the defaults' own rules hold it.** `tui.keys` in `config.yaml` maps an operation id to one key string in Bubble Tea's key-string form, and `{}` is the shipped keymap. The rebindable set is exactly row 31's vocabulary terms, §6's actions and the global chrome (`palette`, `palette_alt`, `help`, `help_alt`, `next_attention`, `mouse`, `quit`, `new` — the last also the chats board's `n`, one gesture); every surface-local row, multi-key set, `esc`, `ctrl+c`, `ctrl+v`, `tab`, popup confirmation and unregistered alias is fixed and refused by name. One override rebinds its operation on every surface that carries it, and it **replaces** the default rather than aliasing it, so the vacated key is free and two operations may swap in one edit. The registry becomes the TUI's **dispatch** source as well as its rendering source — handlers ask it for an operation's key rather than matching literals — because a keymap the help advertises and a handler ignores is the Pull Request tab bug row 31 closed; a translation layer at the root was beaten because it would be a second source of truth for which context is live. The catalog, the fixed keys, the exceptions and the clause checker live in a new leaf, `internal/keymap`; the registry tests run that checker over the defaults and `internal/config` runs it over the effective keymap at load, on hot reload and on `PATCH /v1/config`, so a bad keymap is refused with the file byte-identical, the `tui.board.group_by` precedent. That is config's second internal import beside `taskstate`, an explicit amendment of task 046 decision 4, and `keymap` is a leaf so the direction stays one-way. A key that already means anything else anywhere is refused, and an exception recorded for a default key does not travel with an operation that moves onto it. `palette_alt` and `help_alt`, and any operation answered where a text field owns the printable keys, refuse a printable key. The daemon still publishes no config event: the TUI applies the keymap on connect, reconnect and the daemon view's config fetch, and after its own editor saves it (§12.3, §13.3, §15) |

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
  of the same name shadows it. Five are present. *Amended 2026-09-14 (task 098):
  this said three, before `create-trigger` and `update-triggers`.*
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
    *Amended 2026-09-19 (task 123, reopening task 037 decision 2 and narrowing
    decision 6).* It declares one optional boolean task field, `global`, and
    has ten steps. False or unset is the project pass above, unchanged: every
    step it had renders byte for byte as before, and its only new row is a
    trailing `condition`, `global-only`, recorded `stopped`. `true` points the
    same pass at `{config_dir}/workflows`, and one run is one scope, never
    both. The shared steps branch by template: `inventory` runs `vincent
    workflow ls --global` (§12.1), the agent step's prompt says the
    deliverable is a proposal staged in `{data_dir}/workflow-proposals/{task
    id}/` (§12.2) — whole files plus a `manifest.json` of version tokens, the
    staging directory cleared first so its one retry stays safe — that the
    files have no git history and the task's repository is no evidence of how
    they are used, and it keeps the same skill, checklist and "may not change"
    rules. `relist` runs `vincent workflow apply --proposal {{.Task.ID}}
    --check`, so the validation loop iterates the staged files and a stale or
    malformed proposal blocks before the gate; `changes` records the empty
    diff that shows the worktree was left alone. After `global-only` come a
    `manual` step, `approve`, immediately before `apply` (`vincent workflow
    apply --proposal {{.Task.ID}}`, `max_retries: 0`), and a final `vincent
    workflow ls --global` for the record. The gate stands even for an empty
    proposal, as `update-triggers`' does. An approved change is live in every
    project the moment it is written; rejecting leaves the global registry
    untouched.
  - `create-trigger` — *added 2026-09-14 (task 098).* Writes one event-trigger
    file (§12.2, task 096) for the task's own project, **disarmed**. It declares
    one task field, `trigger_id`: required, held to `^[a-z0-9][a-z0-9._-]*$`,
    and both the trigger's `id:` and its file name. It has two steps. `author`
    is an agent step under `on_input: wait` and `max_retries: 0`, for
    `create-workflow`'s reasons: it stages the file and its manifest in
    `{data_dir}/trigger-proposals/{task id}/` (§12.2), checks the file with
    `vincent trigger validate`, and may ask. `install` is a command step running
    `vincent trigger apply --proposal {{.Task.ID}} --project {{.Project.ID}}`
    (§12.1). There is no manual gate, as there is none on `create-workflow`,
    because what it writes cannot fire until a human arms it (§16). The prompt
    sets `source.project` to `.Project.ID`, asks before replacing a trigger
    `vincent trigger ls` already lists, never writes a workflow (it points to
    `create-workflow` when `action.workflow` does not exist), and puts a poll
    script under `{config_dir}/trigger-scripts/`. It refuses a `cancel` trigger,
    which loads only with `on_fire: create`, and any request that arms one.
  - `update-triggers` — *added 2026-09-14 (task 098).* A maintenance pass over
    the trigger files whose `source.project` is the task's own project. It
    declares no task fields and has six steps: a `vincent trigger ls --project`
    inventory under `allow_failure`, whose exit 1 is the "this project has no
    triggers" signal; a `condition` that ends the run `done` when there are
    none; a `propose` agent step under `on_input: deny` and `max_retries: 1`,
    which clears its own staging directory, stages full proposed files and the
    manifest, and validates each; a `manual` approval whose instructions render
    the proposal and name the staging path; an `apply` command step running
    `vincent trigger apply`; and a final `ls` for the record. Rejecting the gate
    ends the task with every trigger untouched. The pass may not change a
    trigger's `id`, file name or `source.project`, delete a file, change
    `enabled`, `on_fire` or `permission`, or change what a `dedupe_key` renders
    for an event already delivered, which would fire it again. Its review
    checklist is version-coupled to trigger features, as `update-workflows`' is
    to workflow features.

  Both trigger built-ins carry the `vincent-triggers` skill, embedded from
  `skills/vincent-triggers/SKILL.md` at build time the way the workflow pair
  carries `vincent-workflows` (§9.8).
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
global or project file named `adhoc`, `create-workflow`, `update-workflows`,
`create-trigger` or `update-triggers` (the last two named here since
2026-09-14, task 098) still wins the lookup, including for a task created without naming a workflow —
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
| `base_sha` | *Added 2026-08-29 (task 056).* The commit `branch_name` was actually cut from, written beside `worktree_path` when creation fetched `base_branch` from its upstream (§10). NULL means `base_branch` itself still names the fork point — every task predating this and every task created with `fetch_base_branch: false`. It exists because once a task branch starts at a fetched remote tip, `base_branch` names a moving ref that is no longer where the task began, and the two places that read it as the fork point — `GET /v1/tasks/{id}/diff`'s merge-base (§13.2) and archive's empty-branch check (§10) — would otherwise both answer against the stale local commit. *Amended 2026-08-30 (task 064):* on a task created from a pull request it is the **head commit as it stood at admission**, so the diff tab answers "what did this task change" rather than re-rendering the pull request's own diff. *Amended 2026-09-14 (task 099, issue #430):* now served on every task representation (§13.2), reversing 056 decision 4 — without it a human cannot tell a task cut from a fresh upstream tip from one cut from a stale local branch |
| `base_refresh` | *Added 2026-09-14 (task 099, issue #430).* JSON: what worktree creation's base fetch and the fast-forward of the local base that follows it did (§10) — `{fetch: {result: fetched\|no_upstream\|error\|disabled, remote?, ref?, error?}, fast_forward: {result: advanced\|up_to_date\|skipped\|not_attempted, reason?: diverged\|local_ahead\|checkout_dirty\|checkout_busy\|error, worktree?, error?}}`. Written in the same claim write as `worktree_path` and `base_sha`, so a worktree that already existed is never re-recorded. NULL means no worktree was created since migration 0031, or the task came from a pull request, which refreshes no base; `disabled` is recorded, so "key off" and "not recorded" stay apart. A chat handoff copies the chat's (§5.5). Display-only: nothing reads it to decide anything, so a malformed value reads as NULL rather than making the row unreadable |
| `priority` | integer, default 0; higher runs first |
| `agent_override` / `model_override` / `effort_override` | optional, chosen at creation (§13.2); replace the workflow's `defaults` but never an explicit step field (§8.6) |
| `restricted` | *Added 2026-09-11 (task 096).* A one-way permission clamp, chosen at creation (§13.2) and snapshotted: when set, every agent step runs `restricted`, including one whose own field says `full-auto` (§9.4). False for every task created without it, which runs the workflow as written |
| `max_task_cost_usd` | *Added 2026-09-11 (task 096).* This task's own spend cap, chosen at creation (§13.2). The engine blocks `cost_limit` at the lower of it and `config.yaml`'s `max_task_cost_usd` (§12.3); 0 means no cap from this side, so it can tighten the global cap and never lift it |
| `state` | §6. *Amended 2026-09-11 (task 096):* `queued` at creation, or `paused` when the request asked for `paused` — no column of its own, a task created held is an ordinary row in `paused`. *Amended 2026-09-13 (task 096):* a `retry` or a `follow_up` sent with `paused` lands an existing task there the same way (§6) |
| `current_step` | index into the snapshot's step list |
| `pending_input` | normalized InputRequest (§7.4) while state is `awaiting_input`; cleared on answer, timeout, or process exit |
| `pending_follow_up` | *Added 2026-08-25 (task 027).* The follow-up run a human asked for from `done` or `aborted` (§6): its compiled workflow, the run form and text it came from, the optional agent/model/effort, the **origin state** the task is returned to, the 1-based **round**, and the run's own **step cursor**. NULL when no follow-up is in flight. *Amended 2026-09-14 (task 027 decision 14):* also the round's own **fields** when they differ from the task's — the request's values laid over the task's, with a named workflow's required defaults applied (§8.1.2) — which that round renders in place of the task row's |
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

*Amended 2026-09-01 (task 080).* There is a **second** in-place rewriter, and
"the only thing" above should be read as "the only thing a human does": a
`fan_out` whose lanes are derived (§7.6) writes the lanes it rendered back into
this task's snapshot at spawn. It is the same kind of write for the same
reason — the task's own owner replacing part of its own snapshot, so that every
later reader sees one shape — and after it the step is an ordinary static
`fan_out`, which is what keeps the graph, the preview, the editor and
`edit + retry` free of a derived case. The registry is still not re-read: the
lane's `workflow:` was resolved at creation like any other (§5.3 above).
*(Amended 2026-09-14, issue #370: "the preview" here is a snapshot's; `vincent
workflow render` previews an authored file and has a derived case — §7.6.)*

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
text, or the last 200 lines of a command step's stdout. *Corrected 2026-09-02
(issue #311): the second half of that sentence was never true of the column —
it holds the last 200 lines of a command step's stdout **and stderr**, which is
what a human reading a failed step wants and what the repair prompt appends.
The stdout-only value that sentence promised is a separate column,
`stdout_tail` (§14, migration 0025), added because `.Steps.<id>.Result` is a
value a template consumes rather than prose a person reads. A command step's
`.Result` reads that column, and falls back to `result_summary` only for a row
that recorded none — every row written before 0025, and every step type that
runs no command.*

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

*Amended 2026-09-05 (issue #323).* The field list gains **what the attempt was
given**, and the run-time resolution behind it (migration 0027, §14): the
rendered prompt an `agent` step handed its adapter, the rendered script a
`command` step handed its shell, the rendered `check:` command, what an `if:`
guard rendered to, the resolved `for_each` list an iteration drew its item from,
a marker saying a recorded field was cut at the size ceiling, and — beside them
— which §8.6 level supplied each of agent, model and effort, plus the permission
mode, the step timeout, the check timeout, the resolved shell and the working
directory.

None of it was recorded anywhere. `prompt_override`/`run_override` hold only the
text a human typed at edit+retry, so they are null on the ordinary attempt; the
transcript's `step_started` note carries the triple but not the prompt, and the
claude adapter passes the prompt on **stdin**, so not even the `debug: true`
argv note contains it. A human asking "what did this attempt actually get?" had
the workflow's *template* in the snapshot (§15 view 2) and the §8.4 substitution
nowhere — and the template is not the answer when the render is the thing that
went wrong.

Four properties are normative:

- **The recorded prompt is the bytes the adapter received**, which is the §8.4
  render *after* the daemon appends the `<previous-attempt-failure>` block
  (§7.2), not before. That half is the one a re-render can never reproduce: it
  draws on the previous attempt's row. A client marks the appended part as
  daemon-authored so a reader can tell what the workflow wrote from what vincent
  added.
- **These are recorded, never re-derived on read.** `config.yaml` hot-reloads
  (§12.3), so a timeout or a shell default can move under a row that already
  ran, and a task's agent/model/effort overrides are patchable after an attempt
  (§6), so re-resolving later can name a level that had nothing to do with it. A
  derived answer would quietly disagree with what the attempt got. For the same
  reason `vincent workflow render --task` is not this: it is a *preview*, binds
  run-discovered values to visible sentinels (§8.4) and renders against *now*.
- **Every one of them is display-only, the rendered `if:` included.** Task 015
  decision 10 — a guard is re-evaluated every time it is reached and is never
  sticky (§7.7) — stands unamended and is not relitigated: it refuses a
  *persisted verdict consulted later*, and nothing in the engine reads any of
  these columns back to decide anything. `retry` and §12.4 recovery both
  re-render the guard against current facts. This extends that decision's own
  closing clause — the mitigation is visibility — by recording what the guard
  rendered *to*, beside the raw template `result_summary` already carries.
- **The record exists before the process does.** It is written as the engine
  renders each input, so it is already on the row while the attempt is
  `running` and after §12.4 recovery finalizes it `interrupted` — which is
  precisely the attempt a human opens it for.

Each recorded field is bounded at **64 KiB**, cut on a rune boundary, and the
row says a cut happened rather than eliding it. The ceiling is the record's cost
control, not a display concern: on a retry the bytes an adapter receives are the
render plus a failure block whose output tail is itself bounded at 200 lines or
256 KiB (§8.4), nothing prunes `step_runs` (§17) and the database ships whole in
`vincent daemon backup`. `result_summary`'s 4096 is deliberately not reused —
that bounds a summary a board renders, and this is a record whose value is being
exact.

Null on a rendered field means **no input was recorded** — every row written
before the migration, and every field the step type has no input for — and a
client must say so rather than draw an empty body, which would claim the step
was handed nothing. An empty string is a distinct fact: a render that genuinely
produced nothing. Empty or 0 on the resolution fields reads the same "not
recorded", following `loop_total`'s precedent, so a task in flight over the
upgrade renders as it did.

### 5.5 Chat and ChatTurn (task 063)

A **Chat** is a titled conversation with an agent, scoped to a project. It is a
first-class entity beside §5.3's Task, not a task wearing a different hat: it
has no workflow snapshot, no step ledger, no `current_step`, no verdict and no
§6 lifecycle. It never appears on the board or in `GET /v1/tasks`.

What it does share with a task is the isolation: a chat gets its own git
worktree and its own `vincent/{id}-{slug}` branch (§10), so an agent it is
talking to can edit files and make commits without colliding with any task.

*Amended 2026-09-17 (task 119, issue #472): that sentence now describes a
**free** chat, and is deliberately narrowed.* A chat **linked to a task**
(below) gets no worktree and no branch of its own: it works in the task's, on
the task's branch, and the task stays their sole owner. Everything else in this
section applies to both kinds.

| Field | Notes |
|---|---|
| `id`, `project_id`, `title` | as a task's |
| `state` | `idle` \| `running` \| `awaiting_input` \| `archived` \| `handed_off` \| `closed` (below; `closed` added 2026-09-17, task 119) |
| `agent` | fixed at creation, and must be an adapter that can resume (§9.1) |
| `model`, `effort`, `permission_mode` | resolved once at creation, not per turn |
| `branch`, `base_branch`, `base_sha`, `base_refresh`, `worktree_path` | §10, exactly a task's. *Amended 2026-09-14 (task 099):* `base_refresh` added (§5.3), and creating a chat now honours `fetch_base_branch` rather than always fetching. *Amended 2026-09-17 (task 119):* on a linked chat `branch`, `base_branch` and `base_sha` are copies of the task's, taken at open as display history the way a `handed_off` chat keeps its own, and never trusted for a delete; `worktree_path` is always empty, because the task holds the §10 claim and the chat runner reads the task's path at the start of every turn |
| `session_id` | **the agent CLI's own conversation id** — the whole of §7.3's chat-only amendment. Empty before the first turn finishes |
| `pending_input` | the §7.4 request being awaited; non-null exactly in `awaiting_input` |
| *(permanent delete)* | *Added 2026-09-09 (task 092, issue #350):* `DELETE /v1/chats/{id}` is legal from **`archived` alone**, and is refused from `handed_off` for the reason `archive` is: the task named by `handoff_task_id` owns the worktree and the branch, and a deleted row cannot say that. It is not a §5.5 transition — it removes the row rather than moving it — so the state machine above is unchanged. Its task mirror is refused too: an archived task a `handed_off` chat points at cannot be deleted while that chat exists, because `handoff_task_id` is `ON DELETE SET NULL` and the chat would be left pointing at nothing. *Amended 2026-09-17 (task 119):* legal from `closed` too, and `delete_branch=true` on a linked chat is refused `409 chat_linked_to_task` naming the task — the branch is the task's. Permanently deleting a task takes its linked chats with it (`linked_task_id` is `ON DELETE CASCADE`), which needs no refusal: a task can only reach `archived` once its chat is closed |
| `handoff_task_id` | *Added 2026-09-01 (task 074, issue #288):* the task this chat's worktree and branch were handed to. The **one authoritative foreign key** between the two records; a task's `source_chat_id` (§13.2) is this column read backwards, one indexed query per list, never a second stored copy. Non-null exactly in `handed_off` |
| `linked_task_id` | *Added 2026-09-17 (task 119, issue #472):* the task this chat was opened on. Null for a free chat, fixed at open, and the **one authoritative foreign key** for the link, as `handoff_task_id` is for a handoff (task 074 decision 2): the lock (§6) and a task's `open_chat_id` (§13.2) are this column read backwards, never a stored copy. It selects the linked transition table below |
| `opening_context` | *Added 2026-09-17 (task 119):* the task context the daemon assembled when a linked chat was opened, prepended to the **first** turn's prompt and to no later one. A snapshot is exact because the task cannot move while the chat is open. Empty on a free chat |

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
| `awaiting_input` | a turn holding its process while the agent waits on a §7.4 request. It **holds** its cap slot, for §6's reason: the process is alive on its stdin. *Amended 2026-08-31 (task 067, issue #269): that hold is now **bounded** by `defaults.input_timeout` — the wait expires, the turn fails `input_timeout`, the process tree is killed and the chat returns to `idle`, releasing the slot* |
| `archived` | terminal. The worktree is gone; nothing further can run in it. *Amended 2026-09-01 (task 074, issue #288): this was "the only terminal state"; there are now two.* *Amended 2026-09-17 (task 119, issue #472): there are now **three** — `closed` below, deliberately* |
| `handed_off` | *Added 2026-09-01 (task 074, issue #288):* terminal. The worktree and branch belong to the task named by `handoff_task_id`, which is the **sole owner** of their cleanup and lifecycle from then on (§10). `worktree_path` is cleared in the handoff transaction — that is what transferring the §10 claim means concretely — while `branch`, `base_branch` and `base_sha` stay on the row as history. Reusing `archived` was rejected on mechanism, not taste: archiving *removes* the worktree and may delete the branch, which is exactly the state a handoff transfers, so "archiving a handed-off chat must never remove task-owned workspace state" is true by construction — `archive` is simply not legal from here. *Amended 2026-09-01 (issue #298): that refusal now **says so**. `POST /v1/chats/{id}/archive` on a `handed_off` chat answers `409` naming the handoff — the task owns the worktree now — and on an `archived` one that it is already archived, rather than the single sentence "a chat with a live turn cannot be archived" that both terminal states used to get and neither could be true of (§11, §13.2)* |
| `closed` | *Added 2026-09-17 (task 119, issue #472):* terminal, and the only terminal state a **linked** chat can reach. The conversation is over; the worktree and branch it worked in were never its own, and closing touches neither. Not `archived`, because `archived` means "the worktree is gone", which closing must never make true; not `handed_off`, because nothing was transferred |

Human actions: `send` (idle → running), `answer` (awaiting_input → running),
`cancel` (running/awaiting_input → idle), `archive` (idle → archived) and
*(added 2026-09-01, task 074)* `hand_off` (idle → handed_off). There is
no pause: a chat is a foreground conversation, and a paused one is just an idle
one nobody has sent to. Anything outside this table is a `409`, decided by
`internal/chatstate` — the pure FSM both the API and `internal/chatrun` consult,
the arrangement `internal/taskstate` has for §6.

*Amended 2026-09-17 (task 119, issue #472).* `internal/chatstate` holds a
**second table, for linked chats**, the way `internal/taskstate` holds a held
table beside its main one (task 096). It differs only at `idle`, which offers
`send` and *(added)* `close` (idle → closed) and nothing else; `running` and
`awaiting_input` are the free table's rows, and all three terminal states offer
nothing. `archive` and `hand_off` are therefore not refused by a guard on a
linked chat: they are absent, the way `archive` is absent from `handed_off`
(task 074 decision 5), and the API's `409` names the task that owns the
worktree rather than the generic refusal.

#### Linked to a task (added 2026-09-17, task 119, issue #472)

A chat can be opened **on a task** that has stopped for a human — the §6 action
`chat`, from `blocked`, `awaiting_gate`, `done` or `aborted`. It is the same
entity with the same turns, transcripts, cap, clocks and §7.4 answer flow; what
differs is where it works and how it ends.

- **It works in the task's worktree, on the task's branch.** The task keeps the
  §10 claim. The chat stores `linked_task_id`, copies the branch names and base
  SHA as history, leaves `worktree_path` empty, and each turn reads the task's
  `worktree_path` through the store before it starts. A task whose path is empty
  is refused at open (`task_has_no_worktree`), and a turn that finds it empty
  fails: the daemon never cuts a worktree for a chat, because worktree
  preparation is the engine's. A git operation in progress is **not** refused —
  unlike a handoff, a half-finished rebase is exactly what a conversation is for.
- **Its agent, model and effort resolve as a repair's do** (task 025 decision
  6): the request, then the task's override, then the workflow's `defaults`,
  then the adapter's default, with an unset agent falling to the first
  registered adapter that can resume. Its permission mode is the workflow's
  `defaults:` with full-auto as the fallback, clamped by the task's `restricted`
  (§9.4) — a task that runs restricted does not get a full-auto chat, and an
  adapter that cannot keep a resumed turn restricted (codex, §9.3) is refused
  at open rather than letting a later turn run full-auto.
- **Its first turn opens with the task's context**, assembled by
  `internal/taskrun` at open and stored as `opening_context`: title,
  description and fields for every state; for `blocked`, the repair prompt's
  bounded failure block (task 025 decision 4) — rendered step, reason, exit
  codes, the last 200 lines of the failed attempt's transcript and its path;
  for `awaiting_gate`, the gate's id and rendered text; for `done` and
  `aborted`, the last step run's summary and, for `aborted`, its reason. The
  human's own message follows it verbatim, never as a template (task 025
  decision 5). Later turns carry no context: the session already has it.
  *Amended 2026-09-19 (task 124.3, issue #499):* the context reaches the
  adapter apart from the message (`RunSpec.Preamble`), never fused into it.
  On claude's input path (§9.2) the two ride as two text blocks of one user
  message, context first and the message last, byte for byte — claude
  expands a `/name` invocation only when it starts the last block, so a skill
  typed on the first turn now runs as it would on any other. Every path that
  takes one string — claude outside the input gate, codex, cursor — still
  receives the context, a blank line, and the message in `<message>` tags,
  the bytes a first turn always sent; codex and cursor find an invocation
  anywhere, and a claude outside the gate leaves `/name` to the model. The
  stored `opening_context` and turn prompt are unchanged.
- **It locks the task while it is open** — every §6 action but `cancel` is
  refused (§6). There is at most one open linked chat per task; a second open is
  refused, and after a close a new one can be opened. Closed chats stay listed
  under the task as its history (`GET /v1/chats?task_id=`, §13.2).
- **Closing is `POST /v1/chats/{id}/close`.** A live turn is cancelled and waited
  for first; then the chat moves `idle → closed` and the lock lifts. Nothing in
  the worktree or on the branch changes. `cancel` on the locked task is the
  other way a linked chat ends: it closes the chat and aborts the task in one
  transaction (§6).
- **On a task that runs in a container, its turns run in that container**
  (§16). A free chat still runs on the host.

#### Skills available to a chat (added 2026-09-19, task 124.9, issue #505)

`GET /v1/chats/{id}/skills` (§13.2) answers which skills the chat's agent CLI
would load for its **next** turn, and how a message invokes one. It asks
nothing of the chat but its directory, so it answers from `POST /v1/chats` on,
before the first message (task 124 decision 3). The directory is the turn's
own placement, `chatrun.Runner.Workspace`: a free chat's `worktree_path`, or
its linked task's, read fresh on every call because the task owns that claim.
`turnPlace` resolves a turn's directory through the same method, so the list
and the turn cannot disagree about where the CLI starts. Whether a linked
chat's task runs in a container is read from the task's workflow snapshot and
container settings alone, never from the runtime, so asking spawns no
`docker inspect` (task 124 decision 39).

It answers `200` with verdicts and refuses only on state (task 124
decision 4):

| chat | answer |
|---|---|
| `archived`, `handed_off`, `closed` | `409 invalid_state`, `details: {state, action: "skills"}`; checked before `refresh`, so a terminal chat never costs a probe |
| linked, task has no worktree | `409 task_has_no_worktree`, `details.task_id` |
| linked, task's workflow runs in a container | `list_verdict: unknown` with `unavailable_reason`; nothing spawned, the cache not asked, never a host listing (decision 11) — including a configured container that is gone |
| adapter not registered | `list_verdict: unknown`, `probe_error` set, `invoke_verdict: unknown` — never `unsupported` (decision 41) |
| adapter without `SkillLister` | `list_verdict: unsupported`; the route writes `unavailable_reason` itself (decision 40) |
| `ErrSkillsUnsupported` from the probe | `list_verdict: unsupported`, its wrapped text as `unavailable_reason` |
| probe failed, an earlier list cached | `list_verdict: supported`, the earlier list, `probe_error` set |
| probe failed, nothing earlier | `list_verdict: unknown`, `probe_error` set |
| otherwise | `list_verdict: supported`, the list for that directory |

A chat that is `idle`, `running` or `awaiting_input` is listed alike: a probe
is not a turn and holds no `max_parallel_chats` slot (§11). The list is served
from §9.6's skill cache, which a turn's ending invalidates for its directory.

#### Handoff (added 2026-09-01, task 074, issue #288)

`hand_off` creates a task in the chat's project that **adopts** the chat's
`worktree_path`, `branch`, `base_branch` and `base_sha` verbatim — and, *amended
2026-09-14 (task 099)*, `base_refresh`, so the task says how its base was
refreshed. Nothing is
copied, renamed, merged or committed: committed *and* uncommitted work are both
present when the task's first step runs, because the directory is not touched.
No third worktree-creation mode exists behind this — the engine's
`ensureWorktree` already returns early for a task that arrives with a path, so
admission runs no git at all. The worktree keeps the name the chat gave it,
which is informational: the claim decides, not the name (§10).

Everything is validated before anything is written, and everything written
commits together: the task row and its branch claim, the link, the transition,
the release of the chat's §10 claim and both durable events (`task.created` and
`chat.handed_off`, §13.3) are one transaction, and the scheduler is notified
only after it commits. So the scheduler cannot admit a task before it owns a
complete workspace, and gc never observes the directory claimed twice or not at
all. The chat's state is re-read and required to be `idle` **inside** that
transaction: a `send` racing a handoff loses one of the two, never both.

Two refusals, both `409`. A worktree partway through a git operation — merge,
rebase, cherry-pick, revert or bisect — is refused with the operation named
(`repo_operation_in_progress`, §18); gating on merge alone would let a
half-finished rebase be inherited silently and surface later as an unexplained
`git_error` inside a step. A chat with no `worktree_path` has nothing to hand
over, and is refused rather than producing a task whose empty path admission
would quietly fill in by cutting a new worktree. **Ordinary dirty state is not a
refusal**: preserving it is the feature.

Conversational context reaches the workflow through the task's `description`
and nothing else. There is no `handoff_note` column and no new §8.4 template
key: the handoff form asks the human for the objective the way the new-task
form does, so "the transcript is not injected wholesale into workflow prompts"
holds by there being nothing to inject. §7.3 is untouched and the chat's
`session_id` is not transferred — workflow steps still start fresh sessions.

`send` over `max_parallel_chats` is **refused, not queued** (§11). That refusal
is not in the FSM: the cap is about how many chats are running, not about what
this chat may do.

## 6. Task lifecycle

> Chats have their own lifecycle and their own vocabulary — `idle`, `running`,
> `awaiting_input`, `archived` — deliberately kept out of this section and
> documented with the entity in §5.5 (task 063). Nothing in §6 changed when
> chats landed.
>
> *Amended 2026-09-17 (task 119, issue #472):* §6 changed when chats could be
> **linked to a task** — one human action, `chat`, and a lock. The chat's own
> states are still §5.5's.

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
*Amended 2026-09-16 (task 106):* while the adapter's observation (§14) is still
live, re-discovering the wall costs **no** spawn. The re-admission meets §7.2's
pre-spawn check, which finds the window shut before a process starts and writes
the same hold again. The rule is unchanged: the human action still drops the
hold, and the task is still re-admitted at once.

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

**Amended 2026-09-11 (task 096): `create` may land in `paused` as well as
`queued`.** `POST /v1/tasks` with `paused: true` (§13.2) inserts the row
directly in `paused`, so there is no instant at which it is admissible, and
`resume` admits it like any paused task. It is not a new state: create-then-pause
was rejected because the scheduler can start the agent between the two calls,
which is what a held create exists to prevent (task 096 decision 9).

**Amended 2026-09-13 (task 096): `retry` and `follow_up` may land in `paused`
too.** Both routes accept `paused: true` (§13.2), which is the affordance a
trigger's `on_fire: propose` needs on a task that already exists (task 096
decision 31C). `internal/taskstate` keeps these rows in a second, *held* table
beside the one below, not as new actions. A held follow-up is still a
follow-up: its 409 still names `follow_up`, and `available_actions` lists one
spelling. Each held row replaces a `queued` with `paused` and changes nothing
else, so `retry` goes from `blocked` to `paused`, and `follow_up` from `done` or
`aborted` to `paused`. Everything the plain action writes is still written: the
retry cursor stamp, the override, and the follow-up request with its origin and
round. `resume` then admits the task.

There is no held row from `awaiting_children`. The retry there is the cascade,
which never queues the parent, so a held one is a `400` rather than a `409`. A
held retry on a `blocked` parent also holds every lane its cascade re-admits,
because a held call starts nothing anywhere in the tree (task 096 decision 33).
An aborted-origin follow-up needs no change to `restore`: `paused` is not
settled, so the request survives the hold and the resume, and the run ends
through `restore` exactly as an unheld one does (decision 34). One consequence
is stated rather than fixed. A hold can make the wait before a human's choice
indefinite, and cancelling a held follow-up on a `done` task leaves it
`aborted`, as cancelling any unfinished follow-up does.

**Amended 2026-09-17 (task 119, issue #472): a stopped task can be talked
about, and is locked while it is.** A new human action, **`chat`**, is valid
from `blocked`, `awaiting_gate`, `done` and `aborted` — the four states a task
stops in for a human with its worktree still there — and opens a §5.5 chat
linked to the task, working in the task's worktree and on its branch. Every row
is a self-loop: opening a chat decides nothing about the task, emits no
`task.*` event and writes nothing to the task row. It is an action at all so
that `available_actions` can gate a client's key, the way every other
affordance is gated. `available_actions` stays state-shaped (task 025 decision
8): a task with no worktree still lists `chat`, and the open is refused with a
typed `409` instead.

**While the linked chat is open the task is locked.** Every action in the table
below except `cancel` is refused `409 task_locked_by_chat`, naming the chat, and
the task stays exactly as it was — its state, `current_step`, `block_reason`
and retry budget included. Locking only while a turn is running was rejected: a
`retry` or `skip` could fire between two turns of a conversation that is still
changing the worktree. The lock is **one fact**, not a state or a flag: a task
is locked when a non-terminal chat's `linked_task_id` names it. It is checked
inside the transaction that performs the compare-and-swap, which every path to
a transition goes through — the #127 re-apply below, the task 090 cascade, the
held rows above — so SQLite's single writer makes check and write one step.
Actions with a side effect ahead of their swap — the decision row `skip`,
`approve` and `reject` write, `archive`'s worktree removal, `retry`'s
`branch_override` rename — are refused before that side effect as well. Opening
is the same shape: one transaction proves the state is still the one read, that
no open linked chat exists and that the task has a worktree, then inserts the
chat, so a second open is refused and a racing open and `retry` lose exactly
one side. While locked, `available_actions` is `[cancel]` where `cancel` is
legal and `[]` elsewhere; `chat` itself is withdrawn, and the task carries
`open_chat_id` instead (§13.2). The lock's scope is this table's vocabulary:
`PATCH /v1/tasks/{id}` and the pull-request routes are not §6 actions, run
nothing in the worktree, and stay available; `DELETE` needs `archived`, which
the lock already prevents.

**`cancel` on a locked task keeps its one meaning** (task 025 decision 2's
reading): the chat's live turn is stopped and waited for, then **one**
transaction closes the chat and aborts the task. A crash between the two leaves
an idle open chat on a task that is still locked, and repeating the `cancel`
finishes it. A fan-out parent's cancel cascade is `cancel` per lane, so it does
the same to every locked lane. The parent's **retry** cascade (task 090) does
not: it skips a locked lane, leaves it `blocked`, does not count it in
`retried_descendants`, and the parent stays in `awaiting_children` until that
lane is retried by hand after its chat closes.

### States

| State | Meaning | Consumes a concurrency slot? |
|---|---|---|
| `queued` | Ready to run; waiting for scheduler admission | no |
| `running` | A step process is executing (or about to) | **yes** |
| `awaiting_gate` | Paused at a `manual` step, waiting for approval | no |
| `awaiting_input` | The running agent emitted a structured input request (§7.4); its live process is idle, waiting for the answer | **yes** |
| `awaiting_children` | A `fan_out` step's lanes are running as child tasks (§7.6, *added 2026-08-17, task 014*); the parent owns no process. Cancel and retry are the only human actions — approve/reject/skip would be meaningless, which is why this is not a reuse of `awaiting_gate`. *Amended 2026-09-05 (task 090, issue #328): `retry` from here is the cascade to every blocked descendant, and writes nothing to the parent's own row* | no |
| `blocked` | A step failed and retries are exhausted; waiting for a human decision | no |
| `paused` | Engineer-requested soft pause (takes effect at the next step boundary). *Amended 2026-09-11 (task 096): or created held, with `paused: true` on `POST /v1/tasks`*. *Amended 2026-09-13 (task 096): or held by a `retry` or `follow_up` sent with `paused: true`* | no |
| `done` | All steps succeeded; worktree/branch retained for inspection | no |
| `aborted` | Engineer aborted, or rejected terminally; worktree/branch retained | no |
| `archived` | Terminal. Worktree removed; record kept for history. The branch is retained unless it carries no commits past its base, in which case `delete_empty_branch_on_archive` deletes it (§10, *amended 2026-08-16, task 008*) | no |

### Human actions

| Action | Valid from | Effect |
|---|---|---|
| `cancel` (abort) | queued, running, awaiting_input, awaiting_gate, awaiting_children, blocked, paused | Kills any running process (graceful term, then kill after 10 s; `taskkill /T /F` on Windows); → `aborted`. *Amended 2026-08-17 (task 014): from `awaiting_children` it cascades to every unsettled descendant, whose branches and worktrees survive.* *Amended 2026-09-17 (task 119): the one action a linked chat's lock allows; on a locked task it stops the chat's turn, then closes the chat and aborts the task in one transaction* |
| `pause` | queued, running | `running`: finishes the current step, then holds; → `paused`. The request is persisted, so it survives a daemon crash; every other human action clears it |
| `resume` | paused | → `queued` |
| `retry` | blocked, awaiting_children | Re-runs the failed step (fresh attempt, retry counter reset); → `queued`. *Amended 2026-09-05 (task 090, issue #328): from `awaiting_children` it is the **cascade** — every `blocked` descendant at any depth is re-admitted in one call, and the parked parent's own row is not written at all: no transition, no `task.state_changed` whose from and to are equal, and no `retry_cursor_at` stamp, because nothing was retried on the parent and stamping the cursor would hand the join a fresh §7.2 budget nobody asked for. A `blocked` parent takes both: its own `blocked → queued`, then the same cascade over anything blocked beneath it. The count is reported as `retried_descendants` (§13.2). This amends [task 014 decision 22](tasks/014-workflow-fan-out.md) in scope, not in substance — an `aborted` lane is still fixed by hand before the parent is retried, because nothing here re-admits an aborted task*. *Amended 2026-09-13 (task 096): with `paused: true`, → `paused` instead of `queued`, from `blocked` only, and the cascade's lanes are held too; from `awaiting_children` that is a `400`* |
| `edit + retry` | blocked | Overrides the step's prompt/command **in this task's snapshot only**, then retries; the override is recorded on the StepRun. *Amended 2026-09-05 (task 090): every override is a `400` from `awaiting_children` — a parked parent's cursor is a `fan_out` step, which carries no prompt or command to rewrite, and `branch_override` would rename the branch every live lane holds as its `base_branch`* |
| `repair` | blocked | *Added 2026-08-24 (task 025).* Runs one ad-hoc agent, prompted by the operator, in the task's existing worktree and branch (§7.2, §8.6, §13.2); → `queued`, and back to `blocked` at the same step with the same reason when it exits. It decides nothing about the blocked step and does not consume its retry budget |
| `skip` | blocked, awaiting_gate | Marks the step `skipped`, advances to the next step; → `queued` |
| `answer` | awaiting_input | Delivers the answer to the pending input request into the live agent session (§7.4); → `running` (step clock resumes) |
| `approve` | awaiting_gate | Gate step → `approved`; advances; → `queued` |
| `reject` | awaiting_gate | Gate step → `rejected`; → `blocked` (from which: retry earlier via edit, skip, or abort) |
| `set priority` | queued, paused | Reorders scheduler admission |
| `archive` | done, aborted | Removes worktree (warns if dirty — uncommitted changes would be lost; requires `force` in that case); → `archived` |
| `follow_up` | done, aborted | *Added 2026-08-25 (task 027).* Runs one more piece of work — an agent prompt, a shell command or a registry workflow — in the task's existing worktree and branch (§7.2, §8.3, §8.6, §13.2); → `queued`, and back to the state it came from when the run ends. Repeatable; it decides nothing about the task's verdict and spends none of the workflow's retry budgets. *Amended 2026-09-13 (task 096): with `paused: true`, → `paused` instead of `queued`, the request persisted, and `resume` starts the run* |
| `chat` | blocked, awaiting_gate, done, aborted | *Added 2026-09-17 (task 119, issue #472).* Opens a §5.5 chat linked to the task, working in the task's existing worktree and branch (§13.2); no transition — the task stays where it is, **locked** against every other action but `cancel` until the chat is closed. Refused `409 task_has_no_worktree` on a task that never got one, and while a linked chat is already open |

**Amended 2026-09-09 (task 092, issue #350): permanent delete is deliberately
not in this table.** `DELETE /v1/tasks/{id}` and `DELETE /v1/chats/{id}` (§13.2)
are not §6 actions: delete is not a state transition, `taskstate` has no opinion
on it, and it must **never** appear in `available_actions` — which is what gates
every action key in §15. Its refusals are the handler's own check on the row's
state, not the FSM's 409, and its precedent is `DELETE /v1/projects/{id}`, which
is likewise no action. One consequence is load-bearing rather than incidental:
the workspace an archived row opens in §15 is read-only **for free**, because an
archived task offers no actions, not because a flag says so.

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
*Amended 2026-09-11 (task 096):* or `paused`, when the create asked for it
(above). There is still no draft state — a task created held is an ordinary
`paused` row that `resume` admits.

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

  *Amended 2026-09-08 (task 091).* The wait now has an off switch, so the two
  sentences above hold only in the default mode.
  `usage_limit_auto_continue` (§12.3) selects what a recognized quota stop
  does: `always` — the default, and everything described above — holds and
  re-queues; `reported_only` holds only when the CLI actually named a reset
  time and **blocks** when the wait would be the
  `usage_limit_recheck_interval` estimate; `never` always blocks. Where it
  blocks, the task stops at the step it is on with `block_reason:
  usage_limit` and waits for a human retry. `usage_limit` is therefore a
  `queued_reason` *or* a `block_reason` depending on the mode — it names the
  same condition either way, which is why no second reason was minted for it
  and why the vocabulary `internal/taskrun` and `internal/worktree` share
  stays single. Everything else in this bullet is unchanged in every mode:
  the attempt is still recorded `interrupted`, it still consumes **no**
  retry, the cursor does not advance, and the observation still reaches
  §14's per-adapter record so the board can say why the task stopped. The
  block is taken in the *interrupted* arm of the engine's outcome switch,
  above the failed arm, so the `allow_failure` bullet below never reaches
  it: an account out of quota is not a result a workflow may branch on.

  *Amended 2026-09-16 (task 106).* A recognized stop now also holds the
  **other** tasks on that adapter, each at its next agent spawn. Just before
  an agent process would start, after the step's adapter is resolved (§8.6),
  the engine reads the adapter's observation (§14). Guards, `parallel` lanes,
  loop bodies, repairs and follow-up rounds have all been resolved by then.
  If the observation's `resets_at` is still ahead and the mode is one where a
  stop would wait, the process is not spawned. The modes that wait are
  `always`, and `reported_only` when the reset was CLI-reported; `never`
  never holds. The task gets no attempt row, no transcript and no retry, and
  is re-queued on the same `queued_reason: usage_limit` hold until the
  observation's `resets_at`. The hold transition's payload names the `agent`
  on both routes. Nothing is re-recorded: no new `observed_at`, no promotion
  of an estimate to a reported reset, no `agent.quota_changed`. A command
  step ahead of the agent step still runs and the cursor advances past it. In
  a `parallel` group the open lanes run, the walled lane is not started, and
  only it runs on re-admission (§7.5). A pending `edit + retry` override stays
  on the task for the attempt that does run. A failed read of the observation
  is logged and the step spawns: a display table's read must not stall work.
  Only `observed` rows count; a reported reading (§9.6) never holds a task.
  Under `never`, and under `reported_only` against an estimate, each task
  still finds the limit itself, because those modes exist for an operator who
  does not trust the classifier, and the next spawn is what retires a wrong
  observation. Every held task becomes admissible under §11's caps when the
  reset passes, so re-checking an estimated window costs up to
  `max_parallel_tasks` spawns rather than one per queued task.
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

*Amended 2026-09-17 (task 119, issue #472).* A chat **linked to a task** (§5.5)
resumes exactly as any chat does, in the task's worktree. That is still a chat
turn and not a step: its session is never handed to the task, and the task's
next step, repair or follow-up starts fresh. The context it opens with is
assembled by the daemon from the task's ledger, once, ahead of the first
message — not a replay of a conversation, so the rejection below is untouched.
Multi-turn repair — a `__repair` step run that resumed the previous one's
session — was rejected for breaking this section.

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
  having parted company is the only way to get here. *Amended 2026-09-17
  (task 062.2, issue #397): a **containerized** step is the second way. Its
  re-check is judged against the CLI in the image, not the host catalog,
  because that is the binary about to run: an adapter implementing the optional
  `agent.InputProber` (§9.1; claude, whose answer is a version question) probes
  through the run's launcher, so claude's `--version` runs inside the
  container. An adapter that does not implement it keeps the catalog's verdict,
  whose only "cannot" for codex and cursor is the static one and true in any
  image. As everywhere in this section, only a positive "cannot" fails the
  step. The creation layer above still reads the host catalog.*

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
- **A lane may carry a `needs:`.** *Added 2026-09-01 (task 080).* It names
  sibling lanes of the same step, and the lane is eligible to spawn only once
  every lane it names is `done` **and merged into this task's branch**. Absent
  or empty means eligible immediately, which is every lane's behaviour before
  this field existed. Unknown ids and cycles among the lanes are rejected at
  **load** for a declared list and block at **spawn** for a derived one; this
  is a different check from the workflow-name cycle detection at task creation,
  which is about nesting across levels rather than edges within one step.

  `needs:` is *happens-after*, not isolation. There is one branch, so a lane
  declaring `needs: [api, db]` also sees `docs` when `docs` merged in the same
  round: dependencies are satisfied **at least**, never exactly. Giving a lane
  exactly its declared dependencies would mean per-lane integration branches, a
  merge lattice resolved N times and a final join reconciling divergent
  integrations — and would buy nothing, because the deliverable is one branch
  and a lane that works against `{api, db}` and breaks against
  `{api, db, docs}` is broken in the deliverable too.

- **A lane list may be derived at run time.** *Added 2026-09-01 (task 080).*
  A step may carry `for_each:` and a single `lane:` template in place of
  `lanes:`, and renders one lane per item:

  ```yaml
  - id: plan
    type: agent
    prompt: "Inspect the repo. Emit the work units and their dependencies."
  - id: build
    type: fan_out
    max_lanes: 24
    for_each: '{{ (index .Steps "plan").Result }}'
    lane:
      id:       '{{ .Item.id }}'
      needs:    '{{ .Item.needs }}'
      workflow: implement-module
      fields:   { module: '{{ .Item.id }}' }
  ```

  `for_each` is rendered exactly as §7.8 renders a loop's: every entry
  rendered, trimmed, split on newlines with empty lines dropped, and §8.4's
  200-line `Result` tail applies to a list drawn from a producing step. Each
  resulting line is then parsed as a **JSON object**, and `.Item` in the `lane:`
  template is that object — the one widening of §8.4's "every template value is
  a string" rule, made because a DAG node carries both an identity and its
  edges and one string cannot say both. A line that is not a JSON object blocks
  with `fan_out_invalid`, naming the line.

  Only `id`, `needs`, `fields` and `if` may vary per item. **The lane's
  `workflow:` stays static**, which is what keeps §5.3 true — the registry is
  still read exactly once, at task creation — and keeps the creation-time cycle
  detection and `fan_out.max_depth` meaningful over a fan-out whose *width*
  nobody yet knows. Lane id uniqueness and the slug rule, load-time checks for a
  declared list, block at spawn for a derived one: two items can render to one
  id.

  Derivation runs **once**, at spawn, in this task's render context, and the
  derived lanes are written into the task's own snapshot. After that the step
  is an ordinary static `fan_out`, so the graph, the preview, the editor and
  `GET /v1/tasks/{id}/workflow` are all correct with no further case. An empty
  derived list is the same no-op success an all-guarded-off lane list is.

  *Amended 2026-09-03 (issue #316), amending task 080 decision 5.* "An ordinary
  static `fan_out`" was true of everything that **runs** the step and false of
  everything that **draws** it: after materialization a derived list and a
  hand-authored one are the same lanes, so no reader could be told which they
  were looking at. The `lane:`/`for_each:` pair therefore **moves rather than
  disappearing**, into a `derived_from:` record on the step — the way a
  resolved lane's `workflow:` moves to `resolved_from:`. It carries only the
  `lane:` id template and the `for_each:` templates; the rest of the `lane:`
  template is already visible on every lane it produced.

  `derived_from:` is the first **snapshot-only** field, and three rules make
  that safe. It is exempt from the exclusivity check above — `lanes:` beside a
  live `lane:`/`for_each:` is still a load-time error, but `lanes:` beside a
  *record* of one is what every later admission re-parses (§5.3). It is
  **refused in an authored document** — a registry file, or a body sent to
  `POST`/`PATCH /v1/workflows` or the validate route — with the wording an
  unknown key gets, because it cannot be refused at decode and still be
  readable back out of a snapshot. And it is **absent from
  `GET /v1/workflows/schema`**, so the structured editor never offers a control
  for something no author may set. Recovering the provenance from the spawn
  round's `step_runs` row was the alternative and was rejected: the retry
  budget can rewrite that row, and the picture a reader is shown must not
  change because a lane was retried.

  *Amended 2026-09-14 (issue #370), amending task 080 decision 5.* "The
  preview" above is true only of a snapshot. `vincent workflow render` previews
  an **authored** file, where the `lane:` template is still live, so it does
  carry a derived case: the template is walked like a declared lane, its own
  `if`, `id`, `needs` and `fields` render with `.Item` bound to placeholders,
  and its steps are marked with the `for_each` they expand over (§8.4, task 044
  decision 10).

  *Amended 2026-09-17 (issue #407).* `vincent workflow render` also draws a
  `fan_out` step's lane graph on the step's own row. Declared lanes are listed
  by wave, from `LaneWaves` and numbered from 1, with their `needs:` edges, and
  a guarded lane is tagged because it may impose no ordering. `schedule: eager`
  shows only where declared, noting when a flat list runs as barrier. A `lane:`
  template draws `<derived lane>: unknown width`, with `max_lanes` when set,
  and no waves (task 044 decision 11).

  `fan_out.max_depth` is unchanged: it counts nesting, and a dynamic width does
  not nest. `fan_out.max_tasks` **cannot** be checked at task creation for a
  derived list, so a per-step `max_lanes:` and a run-time tree-size check block
  with `fan_out_limit` before anything is spawned — §7.8's `loop_limit` for the
  other dynamic step, and §13.4's run-time `mcp.max_tasks` for the same reason.

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

  *Amended 2026-09-01 (task 080).* A **derived** lane list gives half of this
  up, and only half. The cycle check and `fan_out.max_depth` are untouched —
  the lane's `workflow:` is still resolved at creation, and a dynamic width
  does not nest — but `fan_out.max_tasks` cannot be counted for a list nobody
  has produced yet, so a derived step counts as **one** task here and the real
  bound moves to spawn, where it blocks with `fan_out_limit` alongside the
  step's own `max_lanes:` (above). Everything in this bullet still holds
  verbatim for a declared `lanes:` list.
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
  `children` rollup is what makes that visible. *Amended 2026-09-05 (task 090,
  issue #328): for a `blocked` lane that resolution is one
  `POST /v1/tasks/{id}/retry` on the **parent** — it re-admits every blocked
  descendant at any depth and leaves the parent parked, and the tree converges
  from there whatever order the scheduler wakes it in, because this guard is
  exactly what parks it again while a re-admitted lane is unsettled.*

  *Amended 2026-09-02 (task 081).* Under `schedule: eager` an unsettled lane is
  the ordinary case, not a lost transition, so that guard is barrier-only and a
  merely running lane reads as "not yet" rather than as a failure. Such a
  parent is instead resumed on a **watermark**: it records, as it parks, how
  many of its **direct** children had settled when that admission started, and
  the scheduler re-queues it once the live count exceeds that number (§11). The
  predicate stays pure SQL — the scheduler never parses the lane graph — and it
  clears itself, which "a direct child has settled" does not: that one stays
  true after the parent parks again, so the parent would be re-queued by its
  own park. The watermark is a wake *position*, recomputed from the rows at
  every park, so `retry` and `edit + retry` rewriting the snapshot cannot stale
  it, and a wake lost to a race degrades to barrier timing rather than
  stranding the parent. The settled-descendant rule above keeps running for an
  eager parent too. A wake that finds nothing to merge and nothing ready parks
  again without recording a *new* `step_runs` row and without holding a slot.
  *Narrowed 2026-09-05 (issue #322):* that last sentence read "without
  recording a `step_runs` row" until the park began opening the round's row
  (below). A re-park that finds the round's row already open still writes
  nothing, so the property this was buying — a wake that did no work spends no
  retry budget — is unchanged.
- **The join** merges each lane branch with `git merge --no-ff` in **declared**
  lane order, message `Merge lane '{lane_id}' of task {child_id}`, stopping at
  the first conflict. Declared rather than completion order is what makes a
  re-run conflict identically. Git identity is the user's own: vincent runs as
  the invoking user (§16) and invents no author. *Amended 2026-09-03 (issue
  #316): that message is a **machine-read contract**, not a convenience.* It is
  the only record of which commits came from which lane, and
  `GET /v1/tasks/{id}/diff?by=lane` (§13.2) parses it to attribute the parent's
  merged diff back to the lanes that produced it. Changing the wording breaks
  attribution for every branch already on disk.
- **A conflict blocks** with `merge_conflict`, leaving the worktree conflicted
  so a human resolves in place. `on_conflict: agent` opts into an agent
  attempt first — a full agent step, gated by its own `check` — falling back
  to the block. Blocking by default is §7.2's posture: a human decides what a
  machine could not.
- **The step runs in rounds.** *Added 2026-09-01 (task 080).* On each
  admission this task merges every lane that is `done` and not yet on its
  branch, in declared lane order; spawns the lanes whose `needs:` those merges
  have just satisfied; and parks. When nothing is left unspawned and everything
  is merged, the step succeeds and the cursor advances. Wave structure is
  **derived from the graph** — no workflow names a wave and none names a
  maximum — and a lane list with no `needs:` is exactly one round, which is
  spawn / park / merge / advance, unchanged.

  Scheduling is **barrier rounds**: the scheduler wakes a parked parent when
  every descendant has settled, so a lane needing only `wire` still waits for
  its whole round. That is deliberate — it is what makes "what did this lane
  start from" reproducible across re-runs.

  *Amended 2026-09-02 (task 081).* A step may ask for the other trade with
  **`schedule:`**, one of `barrier` (the default, and what an absent field
  means) and `eager`. Under `eager` a lane is merged and its dependents spawned
  as soon as **its own** `needs:` are done and merged, without waiting for
  unrelated siblings; the parked parent is woken by a lane settling rather than
  by the whole subtree settling (§11). Three consequences, stated rather than
  left to be discovered:

  - A lane's **starting tree is timing-dependent**. Under a barrier, a lane
    always starts from "everything in the rounds before it", the same tree on
    every re-run. Under `eager` it starts when its dependencies merge, and
    whether an unrelated sibling happened to merge by then is a stopwatch
    question. That is why `barrier` is the default: §8.4 keeps `.Now` out of
    the template context on exactly this argument, so a workflow trading
    reproducibility for throughput says so in the file.
  - The parent branch's **commit topology is not reproducible** even when the
    delivered tree is: the set of lanes a given merge commit joins varies
    between runs. Blocking behaviour on `lane_failed` and `merge_conflict` is
    otherwise identical, and so are the cancel cascade and the archive refusal.
  - An eager step **writes up to one merge row per lane**, and `iteration` on
    those rows is a monotonic merge counter rather than the lane's wave: two
    admissions can merge two lanes of one wave and would otherwise collide on
    the retry budget and the §12.2 transcript name. `.Steps["build"].Result`
    still resolves to the latest row.

  *Amended 2026-09-03 (issue #317).* Because rounds ride the loop's column, the
  TUI renders them as the loop's tier too (§15): a multi-round step's rows are
  grouped and folded under a **`round N`** header, 0-based, opened and closed
  with the same keys a loop's iterations are. The number is the column's value,
  so the screen, the log line and the §12.2 transcript name all say the same
  one. Which noun titles the tier comes from the step's type in the task's
  workflow snapshot — the rows alone cannot tell a round from a pass — and a
  task whose snapshot has not arrived keeps the loop's word.

  A step whose **selected lanes declare no `needs:`** among themselves runs as
  a barrier whatever `schedule:` says. Such a list spawns in one round under
  either mode, so eager could only change *when* the lanes merge — and merging
  them as they finish would widen the `lane_failed` amendment below to flat
  lane lists, which task 080 deliberately kept bit-for-bit. The decision is
  taken at **spawn**, over the selected lanes, so a derived list that turns out
  flat is covered by the same rule; `schedule: eager` on a list that happens to
  be flat is redundant, not a load-time error, and for a derived list nobody
  can know at load.

  Each round's merge writes its own `step_runs` row, discriminated by
  `iteration`. That column was a loop's alone; since task 080 it is "which
  repeat of this step's rows is this", which is what keeps round 2's merge from
  spending round 1's retry budget. The two meanings cannot collide, because a
  `fan_out` is not valid inside a loop body (§7.8).

  *Amended 2026-09-05 (issue #322):* the round's row is **opened by the park
  and finalized by the merge**, not written by the merge alone. A parent that
  had spawned a round and parked in `awaiting_children` carried no row for the
  step at all until its lanes settled, so the Steps & Attempts timeline (§15),
  `vincent task show`, `GET /v1/tasks/{id}/steps` and the `task_steps` MCP
  tool — all of them `store.ListStepRuns` and nothing else — stopped at the
  step *before* the fan-out for the whole time the lanes worked, and a parent
  busy fanning work out could not be told from a task that stalled. It was the
  one park in the engine breaking the phase 2 "every step index a task passes
  through has at least one row" decision (§7.8): `enterGate` writes its row on
  entry, and an `awaiting_input` step keeps the row its agent attempt already
  has. It stays **one row per round**. The park inserts it `running` at the
  round's `iteration`; a re-park at a round that already has an open row writes
  nothing; and the merge admission for that round **adopts** that row — same
  id, same `attempt`, same `started_at` — instead of inserting a second one, so
  "which repeat of this step's rows is this" and the retry budget it scopes are
  untouched. Nothing killable is journaled on it, because §12.4 kills what a
  `running` row recorded and there is no process behind a park, and no lane
  counts are frozen into it, because nothing rewrites the row until the merge:
  what the lanes are doing is the `children` rollup §13.2 serves on the task
  itself. A `fan_out` row is for that reason the one `running` row a `queued`
  task may legitimately hold, and §11's admission guard and the doctor's
  unreconciled report both skip it. A daemon restart finalizes an open park row
  `interrupted` like any other owner's (§12.4) and the next merge admission,
  finding none open, creates a fresh one — the `awaiting_gate` precedent.

- **A lane that settles without finishing** blocks the step with
  `lane_failed`, and **nothing of that round** is merged. *Clarified
  2026-08-18 (task 015):*
  "without finishing" means `blocked` or `aborted`. A lane whose own workflow
  stopped early at a `condition` step (§7.7) settles `done`, and `done` is
  `done` — it merges normally. Lanes doing different amounts of work is the
  point of guarding them. A partial merge is
  indistinguishable downstream from a complete one. `retry` re-checks the
  lanes; the remedy is to fix the child, which is an ordinary task. `skip`
  keeps its meaning — it skips the whole join — and is deliberately not a
  "proceed without that lane" button.

  *Amended 2026-09-01 (task 080), reversing task 014 decision 21 where — and
  only where — `needs:` is used:* rounds that already merged **stay merged**.
  Round 1's commits are on the branch before round 2 is known to fail, and the
  alternatives are worse. Resetting the branch would make vincent destroy
  already-integrated commits, which this section's "the work is stopped, not
  destroyed" refuses everywhere else; deferring every merge to the end would
  leave `needs:` as ordering with no code behind it, because a dependent lane's
  worktree would no longer contain its dependencies' commits. The task is
  `blocked`, not `done`, so nothing downstream consumes the branch, and which
  lanes are in it is legible from the child rows. In-flight lanes of other
  rounds are left to finish; no further lane is spawned. A lane list with no
  `needs:` is one round, so its failure semantics are bit-for-bit what they
  were.

  *Amended 2026-09-02 (task 081), for `schedule: eager` only:* an admission
  that finds a lane settled without finishing blocks `lane_failed` **merging
  nothing new**, while other lanes are still in flight. Lanes merged by earlier
  admissions stay merged, in-flight lanes are left to finish, and no further
  lane is spawned — the same posture, applied to an admission rather than a
  round. Merging everything mergeable first and blocking afterwards would make
  the branch content at block time depend on which wake noticed the failure,
  which is a stopwatch question about *delivered commits* rather than about
  scheduling.
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
- **A tree shares one budget when `max_tree_cost_usd` is set.** *Added
  2026-09-17 (task 116, issue #409).* A lane is its own task row, so
  `max_task_cost_usd` gives every lane a budget of its own and a tree may spend
  lanes × that cap (§12.3). `max_tree_cost_usd` is the tree's shared budget: the
  root and every descendant at any depth count against one total, and the task
  whose attempt crosses it blocks `tree_cost_limit` (§18). That is usually a
  lane, and its `blocked` state holds the join open like any other. The key is
  off by default. The parent's `children.cost_usd` (§13.2) is what its lanes
  have spent.

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
  *Amended 2026-09-03 (issue #317):* every body row also carries `loop_total`
  (migration 0026) — how many iterations **the admission that wrote it**
  planned, which is the `count:` or the resolved `for_each` list's length,
  clamp included. That is the sentence beside `loop_item` carried one word
  further: the row already said which item iteration 3 ran on, and now says how
  many iterations that pass was one of. It is not a cursor (task 016 decision 7
  refused one): nothing reads it back to decide what to run next and §12.4 has
  nothing to reconcile with it, because the position is still derived from the
  rows. It is 0 for every row outside a loop and for every row written before
  the column existed, and a reader falls back to the ceiling on 0.
- **Position on the wire.** *Added 2026-09-03 (issue #317).* The `loop` rollup
  a task carries while its current step is a loop (§13.2) reports the loop's
  **real extent** as `total` — the newest row's `loop_total`, absent until the
  loop has a body row at all, because before the first iteration a denominator
  would be a guess that reads like an answer, and falling back to the snapshot's
  bound for a row that predates the column, which is the number that row's
  rollup reported for its whole life — and names the body step that iteration is
  on as `body_step` with its 1-based `body_index` of `body_total`. The outer
  `step k/n` counts a whole loop as one step, so without that clause "where is
  this task" stops at the loop's own name. `max_iterations` keeps its own
  meaning beside `total`: the bound the loop would block on. They are two
  numbers and neither stands in for the other — a 3-item `for_each` under a
  ceiling of 10 is `total: 3`, `max_iterations: 10`, and reads `loop 2/3`. The
  body clause is absent **whole** — no id, no index, no total — for a row whose
  `step_id` is not one of the snapshot's body ids: a repair row, or a row whose
  step an edit-and-retry rewrite of the snapshot (§6) has taken out of the body.
  A snapshot that no longer parses is the other case and not this one: it does
  not narrow the rollup, it removes it, because nothing is then left to say the
  current step is a loop.
- **`.Loop`** (§8.4) is `Index`, `Item`, `IsFirst`, `IsLast`, with `Index: 0`
  outside any loop.
- **`iteration` is not only a loop's.** *Added 2026-09-01 (task 080).* A
  `fan_out` step's rounds ride on the same column, 0-based, so a flat lane list
  still writes `iteration: 0`. The two meanings cannot collide because a
  `fan_out` is not valid inside a loop body, and what they share is what every
  reader of the column wants — "which repeat of this step's rows is this". A
  loop's own derivation filters on `iteration > 0` under the *loop's*
  `step_index`, which a `fan_out`'s rows never share.
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
  *Amended 2026-09-15:* "succeeded" means **work**, not a verdict. A `break`
  that did not take and a `condition` that let the body carry on also write
  `succeeded` rows, but those rows are a guard's answer, and §7.7 says a guard
  is asked again every time it is reached. A resumed iteration therefore
  re-evaluates every `break` and `condition` it reaches, whatever their latest
  row says; only `agent` and `command` rows that succeeded are skipped. Before
  this, a retried merge pass whose probe re-ran and turned green walked past
  the `break` that read it — its old "not yet" row counted as done — and ran a
  whole extra iteration against a pull request that had already merged.
  *Amended 2026-09-15, reopening task 016 decision 7:* a row is kept only
  while **nothing before it has run again**. A resumed iteration still re-runs
  every body step whose latest row did not succeed — the one that blocked,
  and any earlier one that failed under `allow_failure` — but once a body step
  starts an attempt on this admission, every body step after it runs again
  too, whatever its row says. A row produced downstream of an answer that has
  just been replaced is not finished work: keeping it paired a retried pass's
  fresh rebase with the old pass's push and CI wait. Re-asking a question is
  not running a step, so a guard that skips its step again, or a `break` or
  `condition` answering again, keeps what follows. The cost is accepted: an
  expensive step after one that re-ran runs again.
- **Human actions** (§6). `skip` skips the **whole loop step** and advances
  past it; there is no "skip this iteration". `retry` resumes at the failed
  body step of the current iteration with a fresh budget. *Amended
  2026-09-15:* "resumes" is the rule under **Resuming** — an earlier body step
  that failed under `allow_failure` runs again first, and every body step after
  one that runs again runs with it. `edit + retry`
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

*Amended 2026-09-14 (task 027 decisions 13 and 14, issue #369).*
`POST /v1/tasks/{id}/follow_up` is a **second validation boundary**. A follow-up
that names a workflow (§6) runs that workflow, so it is held to that workflow's
declarations exactly as creation holds a new task to its own: the task's stored
fields, with any `fields` the request supplies laid over them key by key, go
through the same required-default substitution, enum normalization and
validation above, against the named registry workflow alone. A failure is a
400 before anything is persisted. A value the task was created with is checked
too — it was legal under the workflow the task ran, not under the one about to
run. The `prompt` and `run` forms compile to a workflow that declares nothing,
so there the supplied `fields` are an overlay with nothing to check.

The result is **round-scoped**. It is stored on `pending_follow_up` (§5.4) and
is what that round's `.Task.Fields` (§8.4) renders — including the fields a
`fan_out` lane spawned inside the round inherits, and the listing a repair of
the round shows. The task row keeps the fields creation recorded, and a later
follow-up starts again from those.

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

*Amended 2026-09-14, issue #374: `parallel` and `manual` also reject
`max_retries` and `retry_backoff`, because neither owns an attempt — a group's
retry budgets are per sub-step (§7.5), and a gate is decided once by a person
(§7.3). Both keep `timeout`: it bounds a `parallel` group, and on a `manual`
step it is still accepted and unread, a gap this change leaves open. The
rejection applies to authored documents only; a task snapshot written before
this change, including one whose include expansion copied a callee's retry
defaults onto such a step, still loads and ignores the value as before.*

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
| `.Project` | `ID`, `Name`, `Path` (original repo root), `DefaultBranch`. *Amended 2026-09-14 (task 098):* `ID` is the project's numeric id, the one `source.project` names in a trigger file and `--project` takes on the command line. It was added for the trigger built-ins (§5.2), which pass it to `vincent trigger ls` and `apply` |
| `.Workflow` | `Name`, `Description` |
| `.Step` | `ID`, `Name`, `Index`, `Attempt` (1-based) |
| `.Steps` | map of *completed* step id → `{Status, Result, ExitCode}`; `Result` is the agent's final result text (agent steps) or the last **200** lines of stdout (command steps). *Corrected 2026-08-18 (task 016): this said 100; the daemon has always used 200, and a `for_each:` reading `.Steps[…].Result` (§7.8) makes the exact bound load-bearing rather than incidental.* *Amended 2026-08-18 (task 015):* a step skipped by its guard appears with `Status: "skipped"`, and a **failed** step appears once the engine has advanced past it — which happens only under `allow_failure` (§7.2), and is what a downstream guard reads. A step's own failed attempt stays out of `.Steps` mid-retry, because `.LastFailure` is already that channel; `interrupted` never appears, since §7.2 says it is not an outcome. *Amended 2026-08-18 (task 016):* "advanced past it" is compared on `(step_index, iteration, body position)`, which is what lets a loop body's later steps read its earlier ones while a `parallel` group's members stay blind to each other (§7.8). Under repetition a step id resolves to its **latest** iteration. *Amended 2026-09-02 (issue #311):* "stdout" was always the statement here, and until now the engine put a command's **stdout and stderr** into `Result`, interleaved in whichever order the two reader goroutines observed them. It now captures stdout separately (`step_runs.stdout_tail`, migration 0025), so a `for_each:` (§7.6, §7.8) cannot pick up a progress meter, a `Switched to branch …` or a deprecation notice as an item. `result_summary` still carries both streams and is unchanged: it is what a human reads on the board, in the detail view and in the repair prompt, where a step that failed with a stderr-only diagnostic must not summarize as blank. A row written before the migration records no stdout tail and renders `Result` from `result_summary` as it did, so a task in flight over an upgrade is unaffected. `.LastFailure` and the `<previous-attempt-failure>` block below are **both** streams, deliberately: what a human reads on a failure is not the value a template consumes. *Amended 2026-09-02 (issue #313):* the bound is **200 lines or 256 KiB**, whichever binds first — the byte half has always been enforced and was never written down here, which is what a `for_each:` author needs to know to size a list. Both halves cut by dropping whole **leading** lines, so what survives is a shorter list of intact items rather than a truncated one; only a single line longer than 256 KiB on its own is cut mid-line, at a rune boundary. The engine had instead been persisting the stdout tail at `result_summary`'s own 4096-byte cap, a raw head slice on no boundary at all: a lane list over 4 KiB lost its items mid-line, and one item that size destroyed the whole list. `result_summary` keeps that cap — it bounds a row a human reads, and never bounded this |
| `.Loop` | *Added 2026-08-18 (task 016).* `Index` (1-based iteration, and **0** outside any loop, so a shared template can tell), `Item` (the `for_each` item this iteration runs on — a string; empty for a `count:` loop), `IsFirst`, `IsLast`. See §7.8 |
| `.Item` | *Added 2026-09-01 (task 080).* The item a **derived `fan_out`'s** `lane:` template is being rendered for (§7.6), and present in that template only. It is a **parsed JSON object**, not a string: this is the one deliberate widening of the rule that every value here is plain text, made because a DAG node carries both an identity and its edges — `{{ .Item.id }}` and `{{ .Item.needs }}` — and one string cannot say both. `.Issue.Labels` is the precedent for structure in this context. `.Loop.Item` is **unchanged** and still a string; the widening does not reach it |
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
| `.Project.*` | `<project.name>`, `<project.path>`, `<project.default_branch>`, and `.Project.ID` `0`, following `.Task.ID`'s precedent *(added 2026-09-14, task 098)* |
| `.Steps` | one entry per step id the **file** declares, nested bodies and inline fan-out lanes included — a derived fan-out's `lane:` template's inline steps too *(amended 2026-09-14, issue #370)* — an `include` step and a lane naming a registry workflow contribute none, since neither survives as a step of this task (§7.9, §7.6) — each `{Status: <steps.ID.status>, Result: <steps.ID.result>, ExitCode: 0}`. A forward reference renders clean: restricting the map to steps that would have completed interacts with `parallel` blindness, loop iterations and `allow_failure` in ways that produce false positives, and a false positive exits 1 inside a pre-commit hook |
| `.Step.Attempt` | `1`, and the `<previous-attempt-failure>` block above is not appended |
| `.Loop` | the zero value outside a loop; `{Index: 1, Item: <loop.item>, IsFirst: true}` for a step inside one |
| `.Item` | only in a derived fan-out's `lane:` template's own `if`, `id`, `needs` and `fields` (§7.6), which render with `RenderLane` exactly as spawn does: an object binding each `.Item` key chain those fields spell out to `<item.KEY>` — `<item.id>`, `<item.meta.owner>` — so a well-formed read renders a placeholder and a typo beside it still fails. A key read through a rebound dot (`{{ with .Item }}{{ .id }}`) is not bound. The template's inline steps render once, marked with the `for_each` they expand over; they see no `.Item`, as at run time *(added 2026-09-14, issue #370)* |
| `.Issue` | the zero value, so `{{ if .Issue.Number }}` takes the unlinked branch |
| `.Worktree.Path` / `.LastFailure` | `<worktree>`, `{<last_failure.reason>, <last_failure.output>}` |
| `.Conflicts` | one element, `<conflicts[0]>`, on an `on_conflict: agent` resolver step; empty everywhere else |
| `.Host` | the **CLI host's** real GOOS/GOARCH — the only honest offline answer, and the one place a preview and a remote daemon can differ |

A guard is rendered and shown but not judged against `true`/`false`: a sentinel
can legitimately make one non-boolean, so that is a warning, never an error.

*Recorded 2026-09-13 (task 096 decision 11).* **There is no `.Event` root.** An
event trigger's templates (its `if:`, its `dedupe_key` and its action's fields)
render inside the daemon at fire time, over a context of their own whose only
root is `.Event`. The event is gone once the action has replayed. Nothing of it
reaches the task except the title, description, fields, prompt or branch those
templates rendered to. A step therefore never sees `.Event`, and a triggered
task renders exactly like a hand-created one. The claim that `.Event` would join
`.Task`, `.Issue` and `.Loop` here was wrong on its own terms: those three are
read from the task row, and an event is not on it. Trigger templates use the
same `text/template` builtins as this section, and like it they have no FuncMap
(§20).

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
*Amended 2026-09-17 (task 062.2, issue #397): a containerized **agent** step now
runs in the container, so this image-based base applies to it exactly as to a
command step. With `container.mount_agent_config` on — the default — every
containerized step also gets `HOME=/vincent-home`, the vincent home its agent
configuration is mounted beneath (§12.3), unless the `environment` policy sets
`HOME`, names it in `inherit` or unsets it. Inside the container `vincent
status` is unavailable (§13.4), although these variables are set.*

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
    Preamble       string            // context ahead of Prompt, never part of the human's message: a linked
                                     // chat's first turn (§5.5; task 124.3, added 2026-09-19). claude's input
                                     // mode sends it as its own text block (§9.2); every path that takes one
                                     // string sends JoinedPrompt(). "" is every other run
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
    Launcher       Launcher          // starts the run's process (task 062.1); nil = HostLauncher
}

// Command, Launcher and Process are the launch seam (task 062.1, added
// 2026-09-16). An adapter builds the Command; the Launcher the caller chose
// starts it; the adapter's RunHandle stops, identifies and waits on the run
// only through the Process.
type Command struct {
    Path      string    // the resolved binary
    Args      []string  // argv after Path, exactly as the adapter built it
    Dir       string    // the task worktree
    Env       []string  // nil = inherit the daemon's, as RunSpec.Env
    Stdin     io.Reader // the prompt; ignored when StdinPipe is set
    StdinPipe bool      // retain a stdin to write to after launch (claude's §7.4 input mode)
    Stderr    io.Writer // the adapter's stderr tail
}

type Launcher interface {
    Launch(cmd Command) (Process, error)
    // Resolve and Probe answer what a run asks about its binary before it
    // starts, where the run will execute (task 062.2, added 2026-09-17).
    Resolve(adapter, configured, binary string) (string, error) // configured = agents.*.path, "" when unset
    Probe(ctx context.Context, timeout time.Duration, path string, args ...string) (stdout, stderr []byte, err error)
}

// InputProber is optional (task 062.2): an adapter whose §7.4 input support is
// a version question judges it through a given Launcher, so a containerized
// step's `require` re-check asks the image's CLI. claude implements it.
type InputProber interface {
    InputVerdictWith(ctx context.Context, l Launcher) InputVerdict
}

type Process interface {
    Stdout() io.Reader
    Stdin() io.WriteCloser         // nil unless Command.StdinPipe
    Wait() (exitCode int, err error) // err is waiting failing, never a non-zero exit
    Terminate() error
    Kill() error                   // the whole tree
    PID() int                      // journaled for §12.4
    Argv() []string
    Release()                      // the platform handle (a Job object on Windows)
}

// Resumer is the optional capability an adapter implements when its CLI can
// resume its own session (task 063). Optional rather than a method on
// AgentAdapter so "a new adapter is one implementation with zero core changes"
// stays true — an adapter that says nothing cannot resume. All three shipped
// adapters implement it anyway. Since task 070 (2026-08-31) all three return
// true, each pinned to a named build; the false leg is exercised against a
// stub adapter in internal/agent/agenttest, which is what keeps the refusal
// proven now that nothing shipped answers no.
type Resumer interface {
    SupportsResume() bool
}

// SkillLister and SkillInvoker are optional too (task 124, added 2026-09-19);
// see "Skills" below. CanListSkills and CanInvokeSkills report whether an
// adapter implements each, and nothing more.
type SkillLister interface {
    // The skills a run started in q.WorkDir would load, without starting a
    // conversation. ErrSkillsUnsupported = this adapter or build can never list.
    ListSkills(ctx context.Context, q SkillQuery) (SkillList, error)
}

type SkillQuery struct {
    WorkDir  string   // the directory the next run starts in
    Launcher Launcher // nil = the host, as RunSpec.Launcher
    Env      []string // nil = the daemon's, as RunSpec.Env
}

type SkillList struct {
    Skills   []Skill        // the CLI's order; names may repeat
    Problems []SkillProblem // entries the CLI could not load (codex's errors[])
}

type Skill struct {                // the CLI's own words; "" = unreported
    Name, Description, ArgumentHint string
    Aliases                         []string
    Scope, Plugin, Path             string // Scope is never normalized
}

type SkillProblem struct{ Path, Message string }

type SkillInvoker interface {
    SkillSyntax() SkillSyntax                 // static per adapter
    Invocation(s Skill, among []Skill) string // the exact text a client inserts
}

type SkillSyntax struct {
    Sigil    string        // "/" | "$"
    Position SkillPosition // "leading" | "anywhere"
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
    Summary string // the outcome in a few words: "exit 0", an edit's "+13 −9"
                   // (task 110) — never the tool's output body, which stays in
                   // the transcript or rides on its own event field
    Verb    string // task 066: the dialect's structured outcome ("created"); ""
                   // when it reported none, or named a type no capture has shown
    Blocked bool   // task 066: a permission rule refused the call, as distinct
                   // from one that ran and failed
    IsError bool   // "known to have failed", never "assumed fine"
}

type RunHeader struct {              // task 066, added 2026-08-31
    WorkDir string   // where the CLI said it was running
    Tools   []string // the tool set the run was given, in the CLI's order
}

type Subagent struct {               // task 109, added 2026-09-17
    CallID      string        // the spawning call: its children's ParentCallID
    Description string        // the sub-run's short title
    AgentType   string        // the kind of agent the run asked for
    Background  bool          // the main loop did not wait on it
    Status      string        // how it ended, verbatim from the dialect
    Summary     string        // its final report as one capped line, never the body
    ToolUses    int           // running or final tally; 0 = unreported
    TotalTokens int64
    Duration    time.Duration
    LastTool    string        // the tool it most recently used
}

type SkillInvocation struct {        // task 124.2, added 2026-09-19; rides on Event.Skill
    Name   string // as the CLI resolved it, namespace included; "" only on a
                  // refusal that names no skill
    Args   string // one line, capped at agent.ToolSummaryMax
    By     string // "human" | "agent"
    CallID string // By=="agent": the Skill tool call the load came from
    Forked bool   // ran as its own sub-run (claude `context: fork`)
    Error  string // the CLI refused to load it, when it said so on a line of its own
}                                    // every field is the CLI's; "" = unreported

type Patch struct {                  // task 110, added 2026-09-17; rides on Event.Patch
    CallID    string // the ToolUse whose edit this is
    Name      string // that tool, when the dialect names it on the outcome; "" for claude
    Text      string // unified hunks, each under its `@@ -a,b +c,d @@` header,
                     // capped at agent.PatchMax runes
    Truncated bool   // the cap cut Text, stated rather than silent
}

type RunResult struct {
    ExitCode     int
    ResultText   string   // agent's final answer/summary
    InputTokens  int64    // 0 if unreported
    OutputTokens int64
    CostUSD      *float64 // nil if unreported (e.g. codex)
    Failure      *Failure // task 003: the adapter's verdict, nil = nothing recognized
    // task 066, added 2026-08-31: the run's own account of itself, as its
    // terminal result reported it. Zero/nil for every member an adapter does
    // not report (amended 2026-09-17, task 108: codex fills the cache counts
    // since task 070, cursor the durations and cache counts since task 108,
    // and neither fills the rest — §9.3, §9.7). None of it is persisted — it
    // reaches a reader through the transcript (§13.2).
    Duration            time.Duration      // the CLI's own wall clock, not vincent's
    APIDuration         time.Duration      // of which was spent in API calls
    NumTurns            int
    StopReason          string             // why the model stopped ("end_turn")
    TerminalReason      string             // why the run stopped ("completed")
    CacheReadTokens     int64
    CacheCreationTokens int64
    // task 070, added 2026-08-31. codex's cached_input_tokens and
    // cache_write_input_tokens are the two counts above under the other
    // dialect's names, so this task added exactly one field.
    ReasoningOutputTokens int64            // share of OutputTokens spent thinking; 0 = unreported
    ModelUsage          []ModelUsage       // per-model share; nil = unreported
    PermissionDenials   []PermissionDenial // refused calls; nil = unreported and none
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

**Skills (task 124, added 2026-09-19, issue #497).** Two optional capabilities
let the daemon ask an adapter which skills its CLI would load in a given
directory (`SkillLister`) and how a message names one (`SkillInvoker`). They are
optional interfaces for the reason `Resumer` and §9.6's `QuotaReporter` are: an
adapter that cannot answer says so by not implementing one, and grows no stub
saying it cannot. `CanListSkills` is a fact about the adapter, not about the
installed build (task 124 decision 13); both capabilities' false legs are proven
against `agenttest` stubs, never against whichever shipped CLI lacks the
capability today.

- **Nothing is synthesized** (task 124 decisions 8 and 17). Every `Skill` field is the
  CLI's own words and `""` is unreported. There is no normalized scope and no
  kind: `Scope` carries codex's own `user|repo|system|admin` and stays empty for
  claude, whose scope appears only as a display label inside its description,
  which is never parsed. `Skills` keeps the CLI's order, and a name may repeat —
  both CLIs document same-name entries they do not merge, and vincent does not
  merge them either. `Problems` carries codex's `errors[]`.
- **A refusal is not a failure** (task 124 decision 16). `ErrSkillsUnsupported`
  means this adapter, or this installed build, can never list — the positive no
  of `InputVerdict`'s `unsupported`. A probe that timed out, crashed or answered
  something unparseable is an ordinary error — `unknown`, nobody can say. Callers
  tell the two apart with `errors.Is`.
- **Invocation syntax is static per adapter**, and a client writes it into the
  message; the daemon passes the message through verbatim and neither validates
  nor rewrites an invocation:

  | adapter | sigil | position | `Invocation` | lists (`SkillLister`) |
  |---|---|---|---|---|
  | claude | `/` | `leading` — expanded only at the start of the message (under stream-json input, its last text block) | `/name` | not yet (#503) |
  | codex | `$` | `anywhere` | `$name`; the linked `[$name](path)` when the exact name occurs more than once among the listed skills, or `$name` if the skill has no path | yes, `skills/list` (§9.3) |
  | cursor | `/` | `anywhere`, as a token | `/name` | never in v1 (§9.7) |

  Observed on claude 2.1.277, codex-cli 0.154.0 and cursor-agent 2026.09.18.
  The rows state the adapters as shipped; §9.2 and §9.3 add nothing to them.
  *Amended 2026-09-19 (task 124.8, issue #504):* §9.2 still adds nothing;
  §9.3 now specifies codex's listing and when its `Invocation` links.
- **The daemon lists only through the skill cache** (*added 2026-09-19, task
  124.9, issue #505*). `ListSkills` is called by §9.6's skill cache and by
  nothing else, and only `GET /v1/chats/{id}/skills` asks the cache (§5.5,
  §13.2). The route reads `SkillInvoker` directly — its syntax and
  `Invocation` are static and spawn nothing — and writes every skill's
  `invocation` from it, so no client builds one. The cache spawns nothing of
  its own: a probe is the adapter's `ListSkills` on the host, and whatever it
  spawns goes through the adapter's own path, `CREATE_NO_WINDOW` included
  (task 124 decision 42).

**The launch seam (task 062.1, added 2026-09-16).** An adapter builds its run's
argv and hands it over; it never spawns the process itself. `Start` resolves the
binary, builds a `Command` — argv, worktree, environment, the prompt on stdin or
a request for a retained stdin pipe, the stderr tail — and passes it to
`agent.Launch(spec.Launcher, cmd)`. Everything the `RunHandle` does to the
process afterwards goes through the returned `Process`: `Kill`, `Terminate`,
`PID`, `Argv` and `Wait`'s exit code delegate to it, while stream reading, §17
parsing and failure classification stay in the adapter. The launcher owns the
process and not just the argv because `Kill`, `Terminate` and `PID` are exactly
what a containerized run has to answer differently, and the engine's cancel path
reaches them through the handle (task 062 decision 1).

`HostLauncher` is the only launcher that ships. It reproduces the spawn the three
adapters each performed before the seam: the resolved path, a process group on
POSIX, a Job object with `CREATE_NO_WINDOW` on Windows (procx), and a PID
`procx.Identity` can journal. A nil `RunSpec.Launcher` means the host launcher,
the same nil-keeps-today's-behaviour rule `Env` follows; the task engine passes
it explicitly from one helper, and chats leave it nil. Only the three runs go
through the seam: the §9.5/§9.6 probes, claude's in-`Start` `--version` probe,
codex's `app-server` quota exchange and a command step's spawn describe or run
on the host and do not.

*Amended 2026-09-17 (task 062.2, issue #397): a container launcher ships
beside the host's, and the launcher answers two pre-start questions as well.*
`Launcher` gains `Resolve` and `Probe`, the hooks 062.1 decision 2 left for
this task. `Start` resolves its binary through the run's launcher, and claude's
in-`Start` `--version` probe (§7.4 input mode) runs through it too, so both
follow the run to wherever it executes. `HostLauncher` implements them exactly
as the adapters did: the configured `agents.*.path`, else a `PATH` lookup, and
the shared probe. Everything else 062.1 decision 2 kept on the host stays
there: `Detect`, `Options`, the catalog, codex's `app-server` quota exchange
and the usage-limit holds keep describing the **host's** CLI and account.

*Amended 2026-09-19 (task 124.8, issue #504):* codex's `app-server` exchange
is now spawned through `agent.Launch` rather than beside it, because it has a
second caller. The quota read passes a nil launcher, so it is the host spawn
above byte for byte and still describes the host's account; the skill listing
(§9.3) passes its query's launcher, which today is always nil too (task 124
decision 34).

The engine's one helper picks the launcher. A task with an active container
gets the container launcher; any other task gets `HostLauncher` byte for byte,
and `container.image: ""` consults no runtime. Chats keep a nil launcher and run
on the host. The container launcher:

- **Resolves in the image.** It looks up the adapter's bare binary name on the
  image's `PATH` with a plain `docker exec {id} /bin/sh -c 'command -v "$1"'
  vincent {binary}` — no pid-file wrapper — and ignores `agents.*.path`, which
  is a host path. A CLI missing from the image fails `Start`, so the step fails
  `agent_unavailable` exactly as a missing host CLI does. The host does not
  need the CLI installed. Probes run the same unwrapped way.
- **Launches through 061 decision 9's wrapper.** The run is
  `docker exec --interactive --workdir {worktree} [--user {uid}:{gid}] --env NAME
  … {id} /bin/sh -c '{pid-file wrapper}' vincent {path} {args…}`, with `--user`
  on a Linux host only. `-i`, never `-t` (061 decision 10), so transcripts and
  §17's token and cost records match a host run byte for byte.
- **Keeps environment values off the host argv.** Every step variable is passed
  as a bare `--env NAME`, its value present only in the docker client's own
  process environment, so a secret — codex's `VINCENT_MCP_TOKEN`, which task 057
  decision 8 moved off argv for exactly this reason — never appears on a host
  command line. `HOME`, `PATH` and `DOCKER_*` are the exception and are passed
  literally as `--env NAME=value`: the client resolves itself with those, so it
  keeps the daemon's own values for them. The base environment is the image's
  (§8.5, 061 decision 7), as it already was for command steps.
- **Stops inside the container.** `Terminate` sends `TERM` to the pid-file
  process group inside the container; `Kill` sends `KILL` there and then kills
  the host client, and stays idempotent. Each signal is its own `docker exec`
  with a context of its own, because the run context is already cancelled when
  a stop arrives. The container survives a step stop. `PID` is the host
  client's, and the run also journals `step_runs.container_id` (§12.4). A
  signalled exit that `docker exec` reports as `128+n` is mapped to the host's
  `-1` when vincent sent the signal, so `exit_code` and failure classification
  see what a host run would.

A command step's spawn is still 061's own path and does not go through this
launcher.

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

  *Amended 2026-09-17 (task 110, issue #402).* An edit's outcome is its line
  delta, `+N −M`, which is what T4.14 first promised `Summary` would say and
  task 066 decision 3 withdrew for want of a capture. The edit's hunks are its
  body, and they stay out of `ToolResult`: they ride on `Event.Patch`, a
  `Patch` carried by the same `EventToolResult`, the way task 070 carried a
  command's output on `Event.Output`. No event type of its own exists, because
  no dialect sends a patch on a line of its own. Only claude fills it (§9.2);
  codex and cursor never do (§9.3, §9.7).
- **`Thinking`** carries the model's reasoning. Adapters emit it only for
  **whole blocks**; a dialect that streams token-level deltas coalesces them
  itself and emits when the block closes. See the §9.7 amendment for why that
  constraint is the part of the original decision worth keeping.

**The run header, the run's account of itself, and subagent attribution (task
066, added 2026-08-31).** A third event type joins the normalized stream —
`EventRunHeader`, carrying a `RunHeader` — and it is the one event that
describes the *run* rather than something that happened inside it: where the CLI
said it was working, and what tools it was given. "What could this agent
actually reach" had no answer in a transcript at any level.

`Event` also gains `ParentCallID`: the tool call a subagent's lines belong to.
It is read, carried on the wire and on the live chunk, and **not rendered** —
§15's pane is a flat two-column gutter, and every captured run has the field
`null`, so there is nothing to test a tree against. Nesting is its own work with
its own capture; because §13.2 re-normalizes on read, transcripts recorded now
will render under it when it lands.

*Amended 2026-09-17 (task 109, issue #401).* `ParentCallID` **is rendered**
now: §15 draws a record carrying it behind a rail, one level quieter than the
main loop. The capture turned out to exist already, in 16 recorded claude runs
(§9.2). Three event types join the normalized stream, the main loop's own
account of a sub-run, each carrying a `Subagent` keyed by the spawning call:
`EventSubagentStarted` (`subagent_started`: description, agent type,
background), `EventSubagentProgress` (`subagent_progress`: the running tally
and last tool) and `EventSubagentFinished` (`subagent_finished`: status, the
final tally and a one-line summary). The records the sub-run itself produces
stay ordinary events stamped with `ParentCallID`; these three are not stamped.
Only claude produces them. codex and cursor never do, which §9.3 and §9.7 state
positively, and nothing synthesizes one from tool calls. Nothing keys on the
spawning tool's name.

*Amended 2026-09-19 (task 124.2, issue #498).* Two more event types join the
normalized stream. **`EventSkill`** (`skill`) reports that a skill's content
entered the conversation — the CLI loaded one the agent asked for, or expanded
one the human's message named — at most once per load, carrying a
`SkillInvocation` on `Event.Skill`. The skill's rendered body never rides on it
(T4.16): the verbatim line is in the transcript. Only claude produces it
(§9.2); codex's stream has no skill item and cursor's says nothing about a skill
it expanded, so neither ever produces it (§9.3, §9.7), and nothing synthesizes
one from a tool call. **`EventInputEcho`** (`input_echo`) is a line on which the
CLI echoed back the prompt vincent wrote to its stdin. It carries nothing: the
text is already on screen, as a chat's human message or a step's rendered
prompt, and the event exists so the line stops counting as unrecognized. Only
cursor produces it (§9.7). Neither is an unmodeled line. §9.7's "genuinely
unmodeled lines stay `unknown`" still governs every line these two do not claim
(task 124 decision 25).

The run header is emitted from the CLI's init line, which is **not** always the
stream's first line: claude writes SessionStart hook lines before it, and a
forked skill's kickoff (§9.2). Nothing in a client relies on the header opening
the stream.

Both are stated positively where an adapter lacks them (§9.3, §9.7) and neither
is ever emulated. Nothing is persisted: `step_runs` keeps vincent's own timing
and token columns, and a claude-only duration there would be a second duration
disagreeing with vincent's own, since claude's excludes what a §7.4 input wait
adds to ours. *Amended 2026-09-17 (task 108):* cursor's duration is read too,
and is not persisted either, for the same reason — it is the CLI's clock, not
vincent's.

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

*Amended 2026-09-08:* **a run that finished cleanly is never classified.** The
matching is substring matching over the error message, the terminal result text
and the stderr tail — and the result text is the agent's own prose, so a step
that *discusses* a quota stop matches the wording of one. An agent asked to file
an issue about usage limits quoted the marker list back in its final message;
every attempt of that succeeded step was recorded `interrupted` with reason
`usage_limit` and re-queued behind a fresh hold, at full agent cost per round,
with nothing to end the loop but a human. The verdict is therefore only read
from a run that failed — a nonzero exit or a terminal result carrying
`is_error`, which is what a genuine quota stop is — so a `Failure` is never the
reason a *successful* step stops counting as success. The stream-error verdict
is the deliberate exception and still outranks a clean exit (§7.1): there the
evidence is vincent's own reader, not the CLI's words.

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
An adapter that **cannot** carry one returns `ErrMCPUnsupported` from `Start`,
and the engine fails the step with `mcp_unsupported`, mirroring
`ErrRestrictedUnsupported`.

That is a deliberate departure from the standing rule that a capability an
adapter lacks is stated here and ignored at run time, and it is recorded as a
departure rather than left to read as an oversight. The reasoning: a workflow
whose prompt depends on the vincent tools should fail loudly rather than burn an
agent run producing work premised on a channel that was never there. A user who
prefers the older behaviour turns the wiring off with one line
(`mcp.wire_steps: false`, §12.3).

*Amended 2026-09-14 (issue #375, task 057 decision 8).* **No shipped adapter
returns `ErrMCPUnsupported`.** claude, codex and cursor all carry the server on
every run, and none of them looks at the installed CLI version to decide. This
paragraph used to say "an adapter — or an installed CLI version —" refuses, and
that §9.5's task 041 surface reports the gap ahead of a run. Both halves are
withdrawn. Vincent does not probe a CLI build for MCP support, and no facet of
§9.5, `GET /v1/agents`, `/v1/info` or `/v1/doctor` reports it. The refusal
path and `mcp_unsupported` stay unretired as the contract for the next adapter,
one with no per-run MCP surface at all. They are proven against a stub adapter,
not a shipped one, the precedent task 072 set for the chat-resume refusal
(§3 row 29). Version floors were considered and rejected, both for all three
adapters and for claude and codex only. They would contradict task 041 decision
1: version comparison is exact-string, a version verdict blocks nothing, and
cursor's calver admits no range. And they would guard nothing, since every
pinned build is far past every known floor (claude `--mcp-config` since 0.2.75,
codex `mcp_servers.*.url` with `bearer_token_env_var` since 0.46.0; cursor
records no date for `--approve-mcps`).

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
- **`logged_in` is answerable** (*added 2026-09-16, task 107*): `Detect` probes
  `claude auth status --json` alongside `--version`, for builds in
  `[2.1.41, 3.0.0)` only — 2.1.41 is where the subcommand was introduced, and
  outside the range nothing is spawned and the field stays `null`. Only a
  boolean `loggedIn` in the JSON on stdout decides, whatever the exit code;
  everything else, a bare exit 1 included, is `null`. Pinned against 2.1.268
  (`testdata/auth_status_logged_in_2.1.268.json`,
  `auth_status_logged_out_2.1.268.json`, `help_auth_status_2.1.268.txt`). The
  reasoning, and why this is stricter than codex's and cursor's rule, is in
  §9.5.
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
  resumes the run (`input_closed`). *Amended 2026-09-19 (task 124.3, issue
  #499):* the line is still one user message, but a run with a preamble (a
  linked chat's first turn, §5.5) carries it as its own text block ahead of
  the prompt's: `"content":[{"type":"text","text":<preamble>},
  {"type":"text","text":<prompt>}]`. Captured against claude 2.1.277
  (`testdata/stream_blocks_context_2.1.277.jsonl`): a `/name` that starts
  the last block expands natively with the earlier block kept in context,
  while the same bytes fused into one block, or the message block placed
  first, do not expand. One message is still one model turn and one result.
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

  *Amended 2026-09-02 (task 082, issue #310): the pull is still absent; a push
  arrived.* claude has gained no `usage` and no `limits` subcommand — every
  sentence above stands — but Claude Code hands its **status line** the
  numbers. `statusLine.command` is invoked once per render with a JSON object
  on stdin carrying `.rate_limits.five_hour.used_percentage`,
  `.rate_limits.seven_day.used_percentage` and each window's `resets_at`, so
  vincent is a bystander to delivery rather than a caller: `vincent statusline`
  (§12.1) reads that object, pushes the windows to
  `POST /v1/agents/claude/quota` (§13.2) and prints whatever status line it
  displaced. This adapter therefore implements **no** quota capability — it is
  not a §9.6 `QuotaReporter`, because there is nothing here to ask.
  A fourth route exists and is **rejected**: `GET
  https://api.anthropic.com/api/oauth/usage`, with the OAuth token from
  `~/.claude/.credentials.json`, answers `five_hour.utilization` and
  `seven_day.utilization` directly and would need no settings write at all.
  Reading that token is **state-file parsing**, which the v0 **T1.7 decision**
  forbids and which §9.5 records as still standing; T1.7 is not reopened here.
  Both consequences are accepted rather than worked around: the claude reading
  exists only while a human is running Claude Code, and vincent writes another
  tool's configuration file to get it (§16).

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
*Amended 2026-09-16 (task 107):* `2.1.268` joins the list — the build the
`auth status` fixtures were captured from (§9.5). The `auth status` gate
`[2.1.41, 3.0.0)` is, like the input gate, a family range rather than this
list, and for the same reason.
*Amended 2026-09-19 (task 124.2):* `2.1.277` joins it, the build the skill
captures below were recorded on.

*Amended 2026-08-31 (task 066).* The parser reads more of the dialect it was
already recording. Four groups, all of them present in the `2.1.226` fixtures
since they were captured:

- **The `system`/`init` line** normalizes to the §9.1 run header — `cwd` and
  `tools` — instead of falling through to `EventUnknown`. Any other `system`
  subtype still does fall through, which is the phase 1 tolerant-parsing rule
  and is asserted rather than assumed.
- **The `result` line's metadata:** `duration_ms`, `duration_api_ms`,
  `num_turns`, `stop_reason`, `terminal_reason`, `permission_denials[]`,
  `usage.cache_read_input_tokens` / `usage.cache_creation_input_tokens`, and the
  `modelUsage` map flattened into a per-model slice with its keys **sorted** —
  Go randomizes map iteration, and a pane whose per-model lines reorder between
  two reads of one transcript is a bug a reader would blame on the run.
  `modelUsage`'s `contextWindow`, `maxOutputTokens`, `canonicalModel` and
  `provider` are deliberately **not** read: they describe the model rather than
  the run, and §9.6's catalog is where a fact about a model belongs.
- **The structured tool outcome.** `tool_use_result` and `tool_result_meta` ride
  on a `user` line *beside* `message`, not inside it. `tool_use_result` decodes
  as either an object or a bare string — the deny fixture's is
  `"Error: no user is available; permission denied"` — so it is held as
  `json.RawMessage` and probed, the way `resultSummary` already handles claude's
  two content shapes. Its `type` becomes `ToolResult.Verb` through a table
  holding exactly the values a capture has shown (`create` → `created`); an
  unobserved type yields **no verb** rather than a guessed past tense, because a
  wrong verb is indistinguishable from a tool that reported none (the T4.17
  rule). A `tool_result_meta[].non_execution_kind` of `permission-rule` sets
  `ToolResult.Blocked` — the call never ran, which a reader acts on differently
  from one that ran and failed. `Blocked` does not clear `IsError`: the dialect
  flags a refused call as an error too, and that is true as far as the model is
  concerned.
- **`parent_tool_use_id`** is read onto `Event.ParentCallID`. It is `null` on
  every line of all three captures.

*Amended 2026-09-17 (task 110, issue #402).* **Edit deltas and patches.** Pinned
against vincent's own recorded transcripts from claude 2.1.232 to 2.1.268 rather
than a fresh capture, and trimmed into `testdata/stream_edit_2.1.268.jsonl`:

- **An `Edit` result has no `type`.** Its `tool_use_result` keys are
  `filePath`, `oldString`, `newString`, `originalFile`, `structuredPatch`,
  `userModified` and `replaceAll` (2,342 results with a non-empty patch). It
  therefore gets **no verb**; inferring one from the keys is the guess task 066
  decision 3 refused.
- **A `Write` that overwrote a file has `type: "update"`** and a non-empty
  `structuredPatch` (52 results). `update` joins the verb table as `updated`,
  under the T4.17 rule. A `Write` of `type: "create"` (981 results) always
  sends `structuredPatch: []`, and keeps `created` and its prose summary.
- **The delta comes from `structuredPatch`.** Each hunk is
  `{oldStart, oldLines, newStart, newLines, lines[]}`, and every line starts
  with `+`, `-` or a space. For a non-error result with a non-empty patch,
  `ToolResult.Summary` becomes `+N −M` — the `+` and `-` lines across every
  hunk, never the headers' context-inclusive counts, and counted before any cap
  — which is exactly cursor's form (§9.7), with no path because the call line
  already names the file. An empty patch yields no delta, and `+0 −0` is never
  invented.
- **The hunks become `Event.Patch`**, rendered as unified text with an
  `@@ -a,b +c,d @@` header per hunk and capped at `agent.PatchMax` (8,000
  runes; 41 of 2,394 recorded patches exceed it) with `Truncated` stated. Only
  the hunk fields are read — never `originalFile`, `content`, `oldString` or
  `newString`. `tool_use_result` belongs to the line, not to a block, so the
  patch is attributed only on a line reporting exactly one `tool_result`,
  which every recorded line carrying a patch does.
- **A subagent's edits carry no `tool_use_result`** (380 `Edit` and 48 `Write`
  results with a `parent_tool_use_id` have none), so they keep their prose
  summary and produce no patch. That is the dialect, stated rather than worked
  around.
- **A failed edit** ("String to replace not found") sends a string
  `tool_use_result` with `is_error: true`, and the object probe yields no delta
  and no patch.

The tool's output **body** still never enters the normalized stream (T4.16), and
the transcript remains the durable copy. Nothing is persisted (task 066
decision 4).

*Amended 2026-09-17 (task 109, issue #401).* **Subagents.** Pinned against 16
recorded runs from 2.1.251 to 2.1.268, trimmed into
`testdata/stream_subagent_async_2.1.268.jsonl`,
`stream_subagent_sync_2.1.263.jsonl` and
`stream_subagent_failed_2.1.268.jsonl`:

- **The spawning tool is `Agent`** in every captured call, although the
  `system`/`init` tool list names it `Task`. Neither name appears in the parser
  or the pane: attribution is `parent_tool_use_id` and the task lines'
  `tool_use_id` (task 109 decision 6). Subagents run concurrently, and their
  lines interleave with each other and with the main loop's. `spawn_depth` is 1
  in every capture.
- **The `local_agent` task lines normalize** to §9.1's three subagent events,
  keyed by `tool_use_id`. `task_started` whose `task_type` is `local_agent`
  becomes `EventSubagentStarted` (`description`, `subagent_type`,
  `is_backgrounded`). `task_progress` becomes `EventSubagentProgress`
  (`usage.tool_uses`, `usage.total_tokens`, `usage.duration_ms`,
  `last_tool_name`). `task_notification` becomes `EventSubagentFinished`, with
  `status` verbatim, the same `usage`, and `summary` (the subagent's whole final
  report) reduced to one line of 120 runes: the report is a body, and the
  transcript holds it (T4.16). The statuses a subagent's notification has shown
  are `completed` and `failed`.
- **Recognition is stateful per stream** (task 109 decision 7). Only
  `task_started` carries `task_type`. `task_progress` has been seen only for
  subagents and carries `subagent_type`. A subagent's `task_notification` never
  carries `task_type`, and carries `usage` when `completed` but none when
  `failed`, which on its face is a background shell's notification. The parser
  therefore remembers every call id it has seen act as a subagent (a
  `local_agent` start, a progress line, any line's `parent_tool_use_id`), and
  takes a notification as a subagent's when its call is remembered or it
  carries `usage`. `NewLineParser` now returns a fresh parser per stream rather
  than one pure function. The cost is stated: a range fetched with `tail=` or
  `offset=` that opens after every line of a subagent can leave that
  subagent's `failed` notification as `agent.raw`.
- **A background launch has a verb.** A `user` line whose
  `tool_use_result.status` is `async_launched` yields a `ToolResult` with verb
  `started in background` and **no summary**: the text beside it is claude's
  internal metadata, addressed to its own model and asking never to be quoted.
  A synchronous call's `status: "completed"` result is unchanged; its summary is
  the report's first line, which says more than a verb would.
- **Still `EventUnknown`**, with `Raw` intact: `local_bash` task lines (a
  background shell's, including those with `owned_by_subagent: true`, and the
  only lines `stopped` has been seen on), `task_updated`,
  `background_tasks_changed`, hook lines and every other `system` subtype.
  Background shells are out of scope; the phase 1 tolerant-parsing rule covers
  the rest.

*Amended 2026-09-19 (task 124.2, issue #498).* **Skill loads.** Pinned against
claude 2.1.277 with this section's own input-mode argv, trimmed into
`testdata/stream_skill_model_2.1.277.jsonl`,
`stream_skill_permission_2.1.277.jsonl` and `stream_skill_fork_2.1.277.jsonl`.
`2.1.277` joins the tested list (task 124 decision 27).

- **The `Skill` call reads as its skill.** Its input is
  `{"skill":"echo-probe","args":"zebra"}`; `skill` joins T4.14's argument
  preference ahead of `prompt` and `description`, so the tool_use's summary is
  `echo-probe`, the way `Bash`'s is its command.
- **A model-loaded skill is three lines,** and the third is its load. The call;
  a `user` `tool_result` whose content is `Launching skill: echo-probe`, beside a
  line-level `tool_use_result: {"success":true,"commandName":"echo-probe"}`,
  which stays `EventToolResult`; and a `user` line flagged `isSynthetic: true`
  whose text **is the rendered `SKILL.md`**, opening `Base directory for this
  skill:`. That third line becomes `EventSkill{By: "agent", Name, CallID,
  Args}`, with `Name` the result's `commandName` and `CallID` its
  `tool_use_id`. The body is never carried (T4.16); the line stays verbatim in
  the transcript.
- **The pairing is structural and scoped by parent** (task 124 decision 22). A
  non-error result that is its line's only one and carries `commandName` arms
  its `parent_tool_use_id` scope; the next line in that scope claims the load
  if it is an `isSynthetic` `user` line and disarms it otherwise. A line from
  another scope — an async subagent's, interleaving — does neither, and the
  §7.4 control lines are in no scope, because the live run answers them before
  the parser sees them and the transcript route does not. The rule reads
  `commandName`, never the tool's name. An `isSynthetic` line with nothing armed
  in its scope stays `EventUnknown`.
- **Args come from the call** (task 124 decision 21). The parser remembers each
  `Skill` call's `input.args` by call id, and the load claims and drops them,
  flattened to one line of `agent.ToolSummaryMax` runes. The cost is stated as
  the subagent memory's is: a range that opens after the call yields the load
  with no `args`.
- **A refused call is no load** (task 124 decision 23). A `Skill` call whose
  result is `is_error` arms nothing; its `agent.tool_result` already reports
  the refusal and pairs with the call by `call_id`.
- **A forked skill's kickoff is a load** (task 124 decision 20). A
  `context: fork` skill the human's message invoked starts with a `system`
  `task_started` line whose `task_type` is `local_agent`, with **no**
  `tool_use_id`, a `description` of `/fork-probe`, and the rendered body under
  `prompt`. It arrives before `system`/`init`. It becomes `EventSkill{By:
  "human", Forked: true, Name}`, `Name` being the description's first word
  without the `/`. 2.1.277 puts no arguments in the description even when the
  message passed some — they are substituted into the body, which is never read
  — so `Args` is empty. Any other `task_started` without a `tool_use_id` stays
  unknown, and so does the fork's `task_notification`, which carries none
  either.
- **A `Skill` permission request names the skill** (task 124 decision 26). In
  restricted mode a skill the model loads raises `can_use_tool` with
  `tool_name: "Skill"`, `input: {"skill":"bash-probe"}` and the skill's own
  description in `description`. The §7.4 summary is `input.skill`, falling back
  to the description when it is empty, keyed on the tool name as the
  `AskUserQuestion` branch beside it is.
- **Out of scope:** a skill the human invoked inline, which claude writes to
  the stream only under `--replay-user-messages` (task 124.10), and the
  `local_command_run` and `conversation_reset` lines.

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
- **Reports no run header and no run metadata (stated positively, 2026-08-31,
  task 066).** Codex's stream opens with `thread.started`, which carries a
  thread id and nothing else — no working directory, no tool list — so there is
  no run header to normalize and this adapter emits no `EventRunHeader`.
  `item.completed` reports an outcome in prose with no structured type and no
  non-execution kind, so `ToolResult.Verb` and `.Blocked` stay empty. The one
  field codex's dialect *does* carry that claude's new ones parallel is
  `turn.completed.usage.cached_input_tokens`. *Amended 2026-08-31 (task 070):*
  the sentence that said it "is **not** read, and that is scope rather than
  absence" is retired — it is read now, into `RunResult.CacheReadTokens`,
  alongside `cache_write_input_tokens` into `.CacheCreationTokens` and
  `reasoning_output_tokens` into the one new field this task added,
  `.ReasoningOutputTokens`. Everything else in this bullet stands: every
  remaining field stays zero here and **nothing emulates a value**, which is
  the standing §9.x rule and is what `TestNoRunHeaderOrResultMetadata`, now
  narrowed to exactly those fields, asserts over every codex fixture.
  *Amended 2026-09-17 (task 109):* likewise **no subagent events**. No codex
  capture has a subagent in it, so this adapter produces none of §9.1's three
  `EventSubagent*` events, which is asserted over every codex fixture, and
  nothing emulates one from its tool items.
  *Amended 2026-09-19 (task 124.2):* likewise **no `EventSkill` and no
  `EventInputEcho`**. `exec --json` has no skill item (`exec_events.rs` at
  `rust-v0.154.0`) — a model reading a `SKILL.md` is an ordinary
  `command_execution` and normalizes as one — and echoes no prompt, so this
  adapter produces neither event, and nothing synthesizes a skill load from a
  command's output.
- **Resumes its own thread** (*replaces "Cannot resume (stated positively,
  2026-08-30, task 063)", 2026-08-31, task 070*). `agent.CanResume` is **true**
  for codex, and a chat on it is created like a claude one. The precondition
  task 063 decision 3 attached to this is met: `thread.started.thread_id` is
  read into `RunResult.SessionID`, and the argv is
  `codex exec --json resume <thread_id> …` — pinned by a capture against
  codex-cli **0.150.1** (`testdata/resume_0.150.1.jsonl`), in which the resumed
  turn reports the *same* `thread_id` and answers a question about the previous
  turn. The prompt stays off argv: `codex exec resume` documents `-` for stdin,
  but a run with no `PROMPT` argument reads stdin anyway, which is what
  `RunSpec.Prompt`'s stdin-only contract (the Windows argv limit) requires.
  Nothing is emulated — codex resumes its own session, and vincent never
  replays a conversation as prompt context. *Amended 2026-08-31 (task 072,
  issue #283):* the sentence that said "cursor still cannot" is retired —
  cursor resumes too (§9.7), so `m14` has no shipped adapter left to state a
  refusal over and that leg moved to a stub in Go.
- **A resumed codex run is always full-auto (2026-08-31, task 072 decision
  1).** `codex exec resume` carries no `-s/--sandbox` at all, so `restricted`
  has no argv spelling on it and the adapter does not invent one — it always
  passes `--dangerously-bypass-approvals-and-sandbox`. That is safe only
  because the combination is unreachable rather than merely unreached:
  `POST /v1/chats` hardcodes `full_auto` and exposes no request field to
  override it, and nothing else in vincent sets `RunSpec.ResumeSessionID`. The
  guard is structural — an `internal/api` test asserts chat creation cannot
  produce a chat in any other mode — so the day chats gain a permission mode
  this decision is reopened deliberately instead of being discovered as a
  silent escalation in the field. Dropping a restriction quietly is the one
  outcome worth spending a test on.
  *Reopened 2026-09-17 (task 119):* a chat linked to a task takes the task's
  permission mode, so a restricted resumed codex run is now reachable. It
  fails closed rather than escalating: the adapter reports it cannot resume
  restricted (`SupportsRestrictedResume`), its `Start` refuses such a run with
  `agent.ErrRestrictedUnsupported` (a chat turn that reached it would fail
  `agent_error`), and `POST /v1/tasks/{id}/chat` refuses the adapter with
  `400 validation_failed` before anything is written.
- **`session_lost` is the only failure codex classifies (2026-08-31, task 072
  decision 2).** A thread id codex no longer knows is refused on stderr with a
  nonzero exit and no JSONL at all — `no rollout found for thread id <id>
  (code -32600)`, captured from 0.150.1 — and that wording is matched **only**
  for a run that actually passed a resume id, so no workflow step can be
  misdiagnosed (§9.2's rule, verbatim). The usage-limit and unauthenticated
  wordings stay unclassified, and task 003's decision still holds for them: an
  account cannot be made to hit its quota on demand, so those have no
  capturable fixture, while a bad thread id costs nothing to reproduce.
- Normalizes Codex's JSONL events (`thread.started`, `item.started`,
  `item.updated`, `item.completed`, `turn.completed`, `turn.failed`, `error`);
  token usage comes from `turn.completed` (`input_tokens` taken verbatim, as
  with claude); `CostUSD` is nil. The final `agent_message` item is the result
  text; a stream ending without `turn.completed`/`turn.failed` is an error
  result, mirroring the claude adapter.
- **The event and item surface, and what is deliberately outside it**
  (*added 2026-08-31, task 070; captures against codex-cli 0.150.1*):
  - `item.updated` is handled for `todo_list` — the agent's running plan,
    normalized to `EventPlan` and the shared `agent.plan` record (§13.2). Every
    version arrives whole, so the record carries the whole list rather than a
    delta.
  - `turn.started` is **not** an event vincent normalizes. It appears in every
    fixture and carries nothing a client renders; it stays `EventUnknown`,
    transcripted verbatim. Stated here so its absence reads as a decision.
  - `thread.started` is still not an event either — it carries a thread id and
    nothing else — but the id is now held for the terminal result.
  - `command_execution.aggregated_output` is read into `EventCommandOutput` /
    `agent.command_output`, capped at `agent.CommandOutputMax` runes with the
    truncation visible. It rides on the same line as the outcome, so one
    `item.completed` produces two records (§13.2).
  - *Added 2026-09-17 (task 110).* codex produces **no `agent.patch`**:
    `file_change` is read for each change's path and kind and for no hunks,
    which the adapter's tests state positively over every fixture.
  - `file_change` and `mcp_tool_call` summaries are built **by this adapter**,
    not by widening `agent.toolSummaryKeys`: `changes` is an array of objects
    the shared extractor cannot read, and `server`/`tool` are codex-shaped
    names in a list whose design is names that converge across dialects
    (task 070 decision 4).
  - **Deferred, with the fixture requirement attached.** These are named rather
    than implemented from the upstream schema, which is the rule that kept
    codex reasoning unimplemented until `testdata/reasoning_0.147.0.jsonl`
    existed: `item.updated` on a running `command_execution` (0.150.1 goes
    `started → completed` even for a command that runs for half a minute, so
    no capture shows a streaming body); `mcp_tool_call.error.message` (the
    capture in `testdata/mcp_0.150.1.jsonl` reports `error: null` even on a
    call the same line marks `status: "failed"` — the server's explanation came
    back inside `result`, so a populated `error` has never been seen);
    `collab_tool_call`; and `web_search.action`. Each stays `EventUnknown` with
    `Raw` intact, which `TestUnmodelledShapesStayUnknown` asserts rather than
    assumes.
  - Verified builds for this section: **0.142.5, 0.147.0, 0.150.1**.
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

  *Amended 2026-09-02 (task 082, issue #310): superseded for codex.* Both
  halves of that sentence are still true — 0.150.1 has no `usage` and no
  `limits` subcommand either — and both missed a third route. `codex
  app-server --stdio` speaks JSON-RPC over stdio and, after `initialize` and
  the `initialized` notification, answers `account/rateLimits/read` with
  `result.rateLimitsByLimitId.codex` (0.150.1 also duplicates it at
  `result.rateLimits`, which the parser accepts as a fallback), carrying
  `primary` and `secondary`, each with `usedPercent`, `windowDurationMins` and
  a `resetsAt` in **unix epoch seconds**. This adapter therefore implements
  §9.6's `QuotaReporter` and its `quota` is a *reported* reading sourced
  `codex_app_server`, captured verbatim as
  `internal/agent/codex/testdata/app_server_ratelimits_0.150.1.json` against
  the build already in the verified list below. The whole exchange measured
  **0.80 s**, which is why it runs on the ordinary catalog seam (§9.6's
  `quotaTTL`) rather than needing a poller. The observation half is unchanged:
  codex still does not classify a quota stop (§18), so it writes no `observed`
  rows. The response's `credits`, `planType`, `spendControlReached`,
  `individualLimit` and `rateLimitReachedType` are read by nothing — this
  reports a usage window, and a plan tier is exactly what §9.7 declined to call
  a quota.
- **Skills are listed by the app-server** (*amended 2026-09-19, task 124.8,
  issue #504*). codex implements §9.1's `SkillLister` over the same exchange
  as the quota reader: `initialize`, the `initialized` notification, then
  `skills/list` with `{"cwds":[<worktree>],"forceReload":true}`, spawned
  through the query's launcher in the worktree with the query's environment.
  The docs allow the server to reuse a cached result per cwd; a freshly spawned
  server has none, so `forceReload` costs nothing and keeps the answer honest.
  **No login is needed** — with an empty `CODEX_HOME` and no API key the request
  still answers. Exactly one `data` entry is expected for the one cwd sent, and
  it is taken without comparing its echoed `cwd`, which codex may normalize
  (task 124 decision 33); zero or several entries are malformed.
  - **Mapping.** `name`, `description`, `scope` (codex's own
    `user|repo|system|admin`, verbatim), `path` and `pluginId` (as `Plugin`,
    `null` → `""`) become a `Skill`; `shortDescription`, `interface` and
    `dependencies` are not read. **No argument hint exists in codex's format**,
    so `ArgumentHint` and `Aliases` stay empty. Each `errors[]` item becomes a
    `Problem` with its path and message verbatim. Rows codex reports
    `enabled: false` are **dropped**: codex will not load them and its own name
    counting (`name_counts.rs`) excludes them (task 124 decision 32). A skill
    with `allow_implicit_invocation: false` in `agents/openai.yaml` **stays
    listed** — it is kept out of the model's context but is still explicitly
    invocable, and the list is the human-invocable set.
  - **Scopes.** codex documents `.agents/skills` from the cwd up to the
    repository root (`repo`), `$HOME/.agents/skills` (`user`),
    `/etc/codex/skills` (`admin`) and its bundled skills (`system`). Observed on
    0.154.0 but **not documented**: `.codex/skills` in the repository (`repo`),
    `~/.codex/skills` (`user`), `~/.codex/skills/.system` (`system`), and plugin
    skills under `~/.codex/plugins/cache`, reported with their `pluginId`. codex
    reads neither `.claude/skills` nor `.cursor/skills`.
  - **Invocation.** A plain `$name` selects a skill only when exactly one
    enabled skill carries that exact name (`selection.rs`), so `Invocation`
    returns the linked form `[$name](path)` when the name occurs more than once
    among the listed skills — compared exactly and case-sensitively, as codex
    counts — and `$name` otherwise, or when the skill has no path. codex reads
    the link's path up to the first `)`, trims it and normalizes backslashes
    (`mentions.rs`), so a worktree under `Application Support` and a Windows
    path both pass verbatim. **Known gap** (task 124 decision 30): codex also
    refuses a plain `$name` whose lowercased form equals an enabled app
    connector's slug, and `skills/list` cannot reveal connectors, so in that
    case `$name` selects nothing. It is documented here rather than worked
    around by linking every invocation.
  - **Failures.** A missing binary, a spawn failure, the 10 s timeout, a
    JSON-RPC error reply, an unparseable result and a wrong entry count are
    each an ordinary error — `list_verdict: unknown` — never
    `ErrSkillsUnsupported`. There is **no listing floor** (task 124 decision
    29): no old build's refusal has been captured, so neither a version table
    nor the app-server's `-32600 "Invalid request: unknown variant …"` wording
    may claim a positive no.
  - **Versions.** `skills/list` arrived in openai/codex#7914 (`rust-v0.73.0`),
    the enable flag in #9328 and extra roots in #10835; every verified build
    postdates all three. The response is captured on **0.154.0** as
    `internal/agent/codex/testdata/app_server_skills_0.154.0.json`, home paths
    scrubbed. The capture ran in an isolated `CODEX_HOME`, where no plugin
    loads, so the plugin leg is proven by derived rows rather than captured
    ones.

*Amended 2026-08-28 (task 041).* Verified builds: `0.142.5` (the invocation
pinned above) and `0.147.0` (the reasoning capture, T4.17). `Detect` reports
`version_verdict` against that list, advisory in exactly the way §9.2 records.
*Amended 2026-08-31 (task 070):* `0.150.1` joins them — the `exec resume`
capture above, which pins the resumed argv, `thread.started` and the refusal of
an unknown thread id. Three entries, one per capture, because the list is what
`tested_versions` publishes and a build vincent has fixtures for belongs in it.
*Amended 2026-09-19 (task 124.8, issue #504):* `0.154.0` joins them — the
`skills/list` capture above (task 124 decision 31).

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

*Amended 2026-09-11 (task 096).* A task may carry a **`restricted` clamp**,
set by `restricted: true` on `POST /v1/tasks` (§13.2) and snapshotted on the
task (§5.3): every agent step of that task runs `restricted`, including one
whose own field says `full-auto`. It is applied **after** resolution
(`workflow.ClampedPermissionMode`), not as a level in the "step field → task
override → defaults" chain, because a step field would otherwise beat it — and
so it is one-way: it can never make a step looser than its workflow wrote it.
Task 041's creation gate evaluates the clamped mode through the same function
the engine runs under, so a clamped task whose agent steps resolve to an adapter
that cannot restrict on this host (cursor on Windows) is refused at creation
with `400 validation_failed` rather than failing its step. "No daemon-global
hardcoded policy" above stays true: the clamp is per task (task 096 decision 17).

*Amended 2026-09-17 (task 062.2, issue #397).* **Permission mode and
containerization are orthogonal axes that compose.** Now that an agent step of a
containerized task runs inside the container (§9.1, §12.3), `restricted` there
is still restricted and `full-auto` there is still full-auto, with the
container's reach rather than the host's (§16). There is no `contained` mode,
and neither axis implies the other. Cursor's "cannot restrict" rule keeps being
judged against the **host** platform, which for a containerized task is already
settled: a Windows daemon refuses the task at creation (task 061 decision 2).

### 9.5 Detection

`GET /v1/info` reports, per adapter: found/not-found, path, version,
`supports_input` (§7.4), and `logged_in` — `null` when the adapter has no
cheap authentication probe (**claude** — *until 2026-09-16: task 107 gave it
`auth status`, so no adapter is `null` by design; see that amendment below*), a
definite boolean when it does
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

*Amended 2026-09-16 (task 107).* The claude bullet above was **wrong when it
was written**, not merely overtaken. `help_2.1.224.txt` holds only the Options
section of `claude --help` — no Commands list — and the Claude Code changelog
records `claude auth login`, `claude auth status` and `claude auth logout` as
added in **2.1.41**, so 2.1.224 already had the command. The fixture is not
recaptured (the option probe it serves is unaffected); it is named here as cut
down so no future reader draws the same conclusion from it. claude now reports
a definite boolean, which leaves **no adapter `null` by design**: `null` means a
probe that could not answer, or a claude outside the gate below.

- **Only the JSON field decides.** `Detect` runs `claude auth status --json`
  (the flag passed although it is the default, so a change of default cannot
  change the format). A JSON object on stdout whose `loggedIn` is a boolean is
  the answer **whatever the exit code**; exit 1 with no readable JSON, a
  missing or non-boolean `loggedIn`, a timeout, a cancellation and a failure
  to spawn are all `null`. This is deliberately stricter than codex's and
  cursor's rule, whose "non-zero exit is `false`" leg exists because their
  logged-out wording has never been captured. claude's has: logged out is exit
  1 *with* `{"loggedIn":false,…}` on stdout (captured against 2.1.268 with an
  empty `CLAUDE_CONFIG_DIR`, which signs nobody out). Exit 1 is also what every
  ordinary CLI failure returns — an unknown option, a settings deadlock, a
  config error — and reading those as "not authenticated" is the false
  accusation T4.22 forbids. The codex/cursor rule is unchanged.
- **Version gate `[2.1.41, 3.0.0)`.** Outside it, or when the version does not
  parse, nothing is spawned and the field is `null`. A CLI older than the
  subcommand could take `auth status` for a prompt and open an interactive
  session with no TTY, which would hang until the probe timeout on every
  re-probe; not spawning it is the only safe probe. The ceiling follows the
  `supports_input` family; a 3.x CLI stays `null` until its output is captured,
  which is harmless because `null` is what every claude reported before.
- **`true` means credentials are configured, not checked.** The CLI's boolean
  is passed through: a claude.ai login, `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN` and a Bedrock/Vertex/Foundry switch all report `true`,
  just as they will run, and none of them is validated. codex's `login status`
  has the same property. **vincent reads `loggedIn` and nothing else** — the
  same answer carries the account's email, organization and subscription, and
  none of it is decoded, logged, stored or sent over the API.
- It is an official, documented subcommand whose stdout is read; vincent never
  touches `~/.claude*`, the keychain or `.credentials.json`, so the v0 **T1.7
  decision** (no state-file parsing) stands untouched. So does task 003
  decision 4: a claude `false` is a visible warning, never a block, and not a
  `vincent doctor` problem. A claude that answers joins the §9.6 `authTTL`
  refresh with no change to the cache; one that returns `null` costs nothing
  extra.

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
them is a `vincent doctor` problem (§17, task 006 decision 7): an untested build
is the normal state of a healthy machine. *Added 2026-09-10 (task 095):*
the published-skill row of §9.8 is **not** a sixth facet. Task 041 closed this
vocabulary at five, and a skill is a property of the machine's agent
configuration rather than of an adapter binary — one store copy serves every
agent at once, and a machine with no adapter installed can still hold it. It is
a group beside this one in every report, and the five stay five. There is still **no pre-flight refusal
on `logged_in: false`** (task 003 decision 4) — this re-states that decision
rather than reopening it.

### 9.6 Option discovery (`GET /v1/agents`)

`GET /v1/agents` returns, per adapter, the availability data of §9.5 plus the
selectable options — models and efforts with provenance, and the adapter
defaults:

```json
{ "agents": [ {
    "name": "claude", "available": true, "path": "…", "version": "2.1.224",
    "supports_input": true, "input_verdict": "supported", "logged_in": true,
    "supports_resume": true,
    "supports_skill_listing": false, "skill_sigil": "/", "skill_position": "leading",
    "version_verdict": "tested", "tested_versions": "2.1.224, 2.1.226, 2.1.268, 2.1.277",
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

- **`supports_resume`** (added 2026-08-31, issue #279) is whether the adapter
  can resume its own session, and so whether it may hold a chat (§5.5,
  decision row 29). It is the same `agent.CanResume` that `POST /v1/chats`
  gates on, published so a client's picker need not re-derive it and cannot
  offer a choice the daemon is certain to refuse; the `agent_cannot_resume`
  refusal stays the authority. Like `restricted_verdict` it is answered for an
  adapter with nothing installed — resume support is a fact about the adapter,
  not about this machine — and it is `null`, never `false`, when there is no
  adapter registry to ask: "nobody can say" and "no" are different answers,
  and only the second may filter anything out. It rides `GET /v1/agents`
  alone; `/v1/info` and `/v1/doctor` are health surfaces and a chat is not a
  health question.

- **`supports_skill_listing`, `skill_sigil`, `skill_position`** (*added
  2026-09-19, task 124, issue #497*) are the §9.1 skill capabilities, flat
  siblings of `supports_resume` by task 041's rule. `supports_skill_listing` is
  `agent.CanListSkills`: whether the adapter implements `SkillLister` at all.
  It answers "can this agent list at all", **not** "will the installed build
  list" — it is a fact about the adapter, like `supports_resume`, and a build
  too old to list surfaces only at list time, as `ErrSkillsUnsupported` (task
  124 decision 13; a tri-state `skill_list_verdict` modelled on
  `input_verdict` was the alternative it beat). *Amended 2026-09-19 (task
  124.8, issue #504):* codex, the one adapter that lists today, has no listing
  floor, so a codex build too old to answer `skills/list` also surfaces only
  at list time, but as an ordinary error — `unknown`, not a positive no (§9.3,
  task 124 decision 29). `skill_sigil` (`/` or `$`)
  and `skill_position` (`leading` or `anywhere`) are the adapter's
  `SkillSyntax`, verbatim, and both are `""` for a registered adapter that
  cannot invoke. All three are `null` when there is no adapter registry to
  ask, or the registry does not know the name, and come from the same
  registry lookup as `supports_resume`, so the four cannot disagree about the
  adapter they asked. They spawn nothing, so the MCP `agent_list` tool carries
  them unchanged. Whether an adapter's stream *reports* a skill load is not a
  field (task 124 decision 14): it is stated in the agents guide only.

  **The skill list itself is not on this endpoint.** Everything here is cached
  by binary identity because help output is a pure function of the installed
  binary; a skill list is also a function of the directory a run starts in —
  the project's skills live in its worktree — so no key this cache has would
  be exact for it. The list is read per chat instead, from a chat-scoped route
  that knows the directory its next turn runs in (task 124 decision 3).

  *Amended 2026-09-19 (task 124.9, issue #505): that route exists,
  `GET /v1/chats/{id}/skills` (§5.5, §13.2), and it is served from the
  **skill cache**, this cache's sibling for the one answer it cannot key.*
  `agent.SkillCache` is in memory only, with no table and no event (task 124
  decision 5), and follows this section's rules wherever they apply:

  - **Key:** the adapter's name, its binary identity (the same resolved path
    plus mtime, found without spawning) and the cleaned directory. An
    upgraded CLI is a new key, so it is asked at once rather than after a TTL.
  - **`skillTTL` = 5 minutes for a clean answer, `skillFailureTTL` = 1 minute
    for a failed probe** (task 124 decision 37). A listing is not a pure
    function of the binary — a person adds a skill or installs a plugin and
    nothing about the CLI changes — so a TTL expires it, on `authTTL`'s
    argument and at `authTTL`'s number: any number of clients asking in
    the same second cost one probe, and a human who changes something and
    looks again is told the truth. The failure minute is `failureTTL`'s, for
    T4.22's reason. Neither has a config key, because no
    other TTL here has one. `?refresh=true` bypasses both.
  - **A turn's ending invalidates its directory**, across every adapter and
    binary identity: a turn is the one event vincent observes that can have
    written a `SKILL.md` or installed a plugin. The chat runner does it once
    the process is gone and **before** it records the ending, because the
    ending's `chat.turn_changed` (§13.3) is the cue a client refetches on. A
    probe already in flight across an invalidation answers its own callers
    but stores nothing a later request can see.
  - **Single flight per key.** Probes are serialized per key, a reader that
    finds a fresh answer never waits behind one, and a request queued behind a
    probe that finished after it arrived is served that probe's answer —
    `refresh` or not — so N concurrent refreshes cost one subprocess.
  - **A failed probe keeps the previous answer** (T4.22). Its error is
    recorded beside the last clean list, whose `probed_at` it keeps; with no
    earlier list the answer is `unknown`. Only `ErrSkillsUnsupported`, wrapped
    or not, is a no, and it is a clean answer about that build, trusted as
    long as a list. A failure whose caller hung up is not stored.
  - **Bounded at 64 keys, least recently used evicted first** (task 124
    decision 38). The directory is in the key and worktrees churn, so an
    unbounded cache would grow with every chat ever listed. It is not coupled
    to `max_parallel_chats`: a probe is not a turn and holds no slot (§11).
    There is no config key.

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
    *Amended 2026-09-16 (task 106):* still true of §11, no longer true of the
    engine. An **observed** window now keeps agent processes from spawning on
    that adapter in the modes where a stop would wait (§7.2), through the
    ordinary `usage_limit` hold. Reported readings are still display only: a
    window at 100% is not a proven stop, and a restart clears them.

*Amended 2026-09-02 (task 082, issue #310): the clause's own expiry condition
is taken.* "`used_percent` and `window` … fill in the day a vendor ships a
surface, at which point `source` changes from `observed`" — two of the three
adapters now have one, so they fill. The block becomes the union of two kinds
of fact, and `source` is what says which:

```json
"quota": { "spent": false, "used_percent": 53, "window": "7d",
           "observed_at": "2026-09-02T09:14:00Z",
           "resets_at": "2026-09-06T11:00:00Z",
           "resets_at_reported": true, "source": "codex_app_server",
           "windows": [
             { "name": "primary", "used_percent": 28, "window": "5h",
               "resets_at": "2026-09-02T13:00:00Z",
               "resets_at_reported": true },
             { "name": "secondary", "used_percent": 53, "window": "7d",
               "resets_at": "2026-09-06T11:00:00Z",
               "resets_at_reported": true } ] }
```

- **A reported reading or an observation, never both.** A reading measures a
  window still open; an observation records a wall already hit, and only the
  first can say how much is left before anything stops. The reading wins where
  there is one; the observation is the fallback, including when every dated
  window of a reading has since reopened, because a stale percentage is worse
  than an honest older fact. `null` still means neither exists.
- **`windows[]` is added and the scalars are filled, not repurposed.** Every
  window the source named rides `windows[]` — always an array, never null,
  empty for an observation — and the block's `used_percent`, `window`,
  `resets_at` and `resets_at_reported` carry the **tightest** of them: highest
  `used_percent`, ties broken by the earliest reset. A client written against
  task 026's shape keeps working unchanged and gets the number that matters.
  Window names are the source's own vocabulary (`primary`/`secondary`,
  `five_hour`/`seven_day`) and are deliberately **not** normalized across
  vendors: two vendors' windows are not the same thing, and a shared vocabulary
  would claim they are.
- **`spent` splits on `source`.** An observation is spent while
  `now < resets_at`, as before. A reading is spent at `used_percent >= 100` —
  a reading's reset is always in the *future*, which is what an open window
  means, so the observation's derivation would light the badge permanently for
  everyone whose adapter reports.
- **A window may name no reset.** `resets_at_reported: false` with a zero
  `resets_at` is that statement, and it is a third case beside "the CLI said
  so" and "vincent estimated it". §15 renders no time for it at all: `≈ 00:00`
  is a time nobody is waiting for.
- **The capability is an optional interface, not a method on `AgentAdapter`.**
  `agent.QuotaReporter` — `Quota(ctx) (*ReportedQuota, error)` — is satisfied
  by codex alone (§9.3). claude has no pull and pushes instead (§9.2, §13.2);
  cursor has no surface and grows **no** "cannot report" stub, which is exactly
  the interface change task 026 decision 1 refused. A capability an adapter
  lacks is stated in §9.x and never emulated (§9.7).
- **`quotaTTL` = 5 minutes, on `authTTL`'s seam and for `authTTL`'s reason.**
  Binary identity vouches for a percentage no better than it vouches for
  `logged_in`, so a cache hit older than the TTL asks again. Only an adapter
  that is both installed and a `QuotaReporter` is ever asked, so cursor and an
  uninstalled CLI cost no subprocess however often they are read. Failure
  follows the auth rule exactly: the previous reading stands, the error is
  recorded off the wire, and the clock is stamped either way — a persistently
  broken reporter costs one subprocess per TTL, not one per request.
  `GET /v1/doctor` refreshes the reading by already forcing the probe (task
  029's amendment), and needs no second knob.
- **A reported reading lives in the catalog cache and nowhere else.** There is
  **no migration and no schema change** (§14): a reading is exactly as durable
  as the daemon, and a restart drops it until the next probe or push. That is
  deliberate rather than a gap — a percentage nothing has confirmed since the
  daemon started is one vincent should not be showing, and if the source is
  live it refills within a render.
- **Probe-failure degradation is still untouched.** No quota path can fail a
  probe: a missing binary, an unauthenticated account, a handshake that times
  out, a malformed answer and a spawn failure all degrade to the
  observation-only behaviour task 026 shipped, and `probe_error` keeps meaning
  only "the option probe failed and you are reading the curated catalog".
- **§11 is still unchanged.** Admission, both concurrency caps and the
  `usage_limit` classification behave exactly as they do today. This is
  display.

### 9.7 Cursor adapter (M5)

Placed after §9.6 rather than between §9.3 and §9.4 deliberately: section
numbers are identifiers cited from code comments, and renumbering §9.4–§9.6
would invalidate every one of them.

- **Binary is `cursor-agent`, never `cursor`.** `cursor` on PATH is the editor
  launcher and would open a GUI; the adapter resolves `cursor-agent` only. The
  adapter's `Name()` — and therefore the workflow `agent:` value and the
  `agents.cursor.path` config key — is `cursor`.
- **Reports a run header and part of the run metadata** (*replaces "Reports
  no run header and no run metadata yet (stated positively, 2026-08-31, task
  066)", 2026-09-17, task 108*). Cursor's `tool_call/completed` carries no outcome
  type and no non-execution kind, so `ToolResult.Verb` and `.Blocked` stay
  empty. The rest is scope, not absence, and saying so is the point of stating
  it here: cursor's `system`/`init` line **does** carry `cwd` (though no tool
  list), and its `result` line **does** carry `duration_ms`, `duration_api_ms`
  and `usage.cacheReadTokens`/`cacheWriteTokens`. None of it is read. Task 066
  widened claude's parser only — the dialects diverge and each deserves its own
  fixtures — and the shared vocabulary was designed so cursor can fill these
  later without another wire change. Until it does, all of them stay zero and
  none of them is emulated, asserted over every cursor fixture.
  *Amended 2026-09-17 (task 108, issue #400):* cursor fills its share now, and
  "none of it is read" is retired. A `system` line with subtype `init` is the
  run header, `RunHeader.WorkDir` from its `cwd`, with **no tool list** —
  `Tools` stays nil, because the line lists none and a set assembled from the
  calls seen later would be a guess; any other `system` subtype stays
  `unknown` with its raw line. The `result` line's `duration_ms` and
  `duration_api_ms` become `Duration` and `APIDuration`, and
  `usage.cacheReadTokens`/`cacheWriteTokens` become `CacheReadTokens` and
  `CacheCreationTokens`, copied as reported, whatever the result's subtype.
  What stays zero, and is still asserted zero over every cursor fixture:
  `NumTurns`, `StopReason`, `TerminalReason`, `ModelUsage`,
  `PermissionDenials`, `ReasoningOutputTokens`, `ToolResult.Verb`/`.Blocked`
  and `ParentCallID` — no cursor line carries them, and nothing emulates one.
  What cursor reports and is still **not read**: the init line's `model`,
  `permissionMode` and `apiKeySource` (`RunHeader` has no field for them, a
  field would be a wire change, and `apiKeySource` concerns authentication,
  which has no place in a transcript record) and the result line's
  `request_id`. Pinned against captures from cursor-agent 2026.08.25-3e8eec8
  as well as every earlier fixture, which already carried the same fields.
  *Amended 2026-09-17 (task 109):* likewise **no subagent events**. No cursor
  capture has a subagent in it, so this adapter produces none of §9.1's three
  `EventSubagent*` events, which is asserted over every cursor fixture, and
  nothing emulates one from its tool calls.
  *Amended 2026-09-19 (task 124.2):* likewise **no `EventSkill`**. Piped
  `/echo-probe zebra`, cursor-agent 2026.09.18-9a7762b ran the skill — its
  reply was the skill's output — and wrote nothing in the stream saying so
  (`testdata/skill_slash_2026.09.18.jsonl`). This adapter produces no skill
  event, which is asserted over every cursor fixture, and nothing infers one
  from a `/name` in the prompt or from the reply.
- **Resume (pinned against cursor-agent 2026.08.11-e8db854, 2026-08-31, task
  072).** `agent.CanResume` is true for cursor, so a chat may run on it (§5.5,
  §13.2), replacing task 063's "cannot resume" on that decision's own deferral
  terms. The flag is `--resume <chatId>`, and the id it accepts **is** the
  `session_id` the stream stamps on every line — the single open question this
  work carried, settled by capture, and the reason no `SessionCreator` seam
  was built. `--resume` takes an *optional* value, which would be a hazard for
  a CLI that also took a positional prompt; it is safe here only because this
  adapter passes none (the prompt is piped on stdin), so the id is the last
  thing on argv with nothing after it to swallow.
- **cursor cannot report a lost session (stated positively, 2026-08-31, task
  072 decision 2).** Handed a `--resume` id it has never seen, cursor-agent
  does not refuse: it starts a fresh chat *under that id*, stamps it on every
  line and exits 0 (captured against 2026.08.11-e8db854). So this adapter
  ships no `session_lost` classifier — there is no refusal to recognize, and
  inventing a match for a CLI that answered would be exactly the emulation
  §9.x forbids. A cursor chat whose id has aged out gets an answer with no
  memory of the conversation rather than a `session_lost` failure. The
  usage-limit and unauthenticated wordings stay unclassified for task 003's
  original reason, unchanged.
- Invocation (pinned against cursor-agent 2026.08.04-aaa8809, and
  2026.08.11-e8db854 for `--resume`):
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
  - *Amended 2026-09-19 (task 124.2, issue #498).* The `user` line is cursor's
    echo of the piped prompt, once per run, and normalizes to
    `EventInputEcho`: a record with no payload and no live chunk (§13.2,
    §13.3), because its text is already on screen as a chat's message or a
    step's rendered prompt. It used to fall through to `unknown`, which made
    every cursor turn carry one "unrecognized line". It is modeled now, so it
    is not one; the verbatim line is still in `format=raw`. Nor does it split
    a run of them: the pane skips it the way it skips
    `agent.subagent_progress` (§15), so unrecognized lines on either side of
    it are one count.
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
    and a reader who asks to see raw lines should see them. *Amended
    2026-09-19 (task 124.2):* so do every `system` subtype but `init`; the
    `user` line no longer does (above).
  - `tool_call` carries the tool as the **object key** (`editToolCall`,
    `shellToolCall`), not a `name` field; the `ToolCall` suffix is stripped
    for the normalized name (`edit`, `shell`). `started` is the tool_use
    event, mirroring codex's `item.started`, and `completed` is the
    **tool_result** (T4.16) — never a second tool_use, which would
    double-count every call. Its outcome keys on the *presence* of
    `result.success`: present is a success carrying its detail (an edit's
    `linesAdded`/`linesRemoved` render as `+1 −0`, which says something the
    invocation line did not — *amended 2026-09-17 (task 110):* the form claude's
    edit delta now shares, §9.2), absent is a failure with **no detail at
    all**. cursor reports no hunks, and produces **no `agent.patch`**, which
    the adapter's tests state positively over every fixture.
    No capture of a failed cursor tool call exists — the fixture's `completed`
    payloads are reconstructed, because the capture machine had a user-level
    hook that rejected every call — so keying on presence is correct in both
    directions today and degrades to a true statement rather than a silent
    hole, where a guessed failure shape would not.
  - Usage keys are camelCase (`inputTokens`, `outputTokens`, plus
    ~~`cacheReadTokens`/`cacheWriteTokens` which vincent does not record~~
    `cacheReadTokens`/`cacheWriteTokens`, *amended 2026-09-17 (task 108):*
    copied as reported into `CacheReadTokens`/`CacheCreationTokens` and never
    folded into the plain in/out counts. The captures show no fixed relation
    between `inputTokens` and `cacheReadTokens`, so this spec does not say
    whether one includes the other);
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

  *Restated 2026-09-02 (task 082, issue #310): unchanged, and now the only
  one.* codex reports through its app-server (§9.3) and claude pushes through
  its status line (§9.2); cursor has neither. It implements no §9.6
  `QuotaReporter`, is never asked for a reading, spawns nothing, and grows no
  stub saying it cannot — which is this bullet's own rule applied to the day
  the other two changed. Its `quota` value and its rendering are what task 026
  shipped, byte for byte.
- **Version verdict compares whole strings** (*added 2026-08-28, task 041*).
  The verified builds are `2026.08.04-aaa8809`, `2026.08.11-e8db854` and
  `2026.08.25-3e8eec8` (*added 2026-09-17, task 108*, the capture build for the
  run header and the result metadata) and `2026.09.18-9a7762b` (*added
  2026-09-19, task 124.2*, the capture build for the prompt echo and a skill
  turn), and the
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
- **Skills: invoked, never listed** (*added 2026-09-19, task 124, issue
  #497*). The `-p` dialect vincent drives has no way to list the skills a run
  would load, so cursor does not implement §9.1's `SkillLister`,
  `supports_skill_listing` is `false` for it, and it grows no stub saying so.
  ACP's `available_commands_update` does carry a list, but it is not adopted:
  it took about 4 s, needs a login and the network, and disagrees with what a
  `-p` turn loads — it misses claude plugin skills a `-p` turn does load
  (observed on cursor-agent 2026.09.18). Serving it would list skills the
  chat's agent will not actually load, which is emulation by another name;
  whether to adopt it is issue #514's decision (task 124 decision 2).
  Invocation needs no listing: cursor recognizes `/name` **anywhere** in a
  message, as a token (<https://cursor.com/docs/skills.md>), so it implements
  `SkillInvoker` with sigil `/` and position `anywhere`.

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

### 9.8 Published skills (task 095, added 2026-09-10)

This repository publishes agent **skills** — directories under `skills/`, each
with a `SKILL.md` — so that an agent somebody is talking to *directly*, outside
a vincent run, knows how to author a vincent workflow. A run inside vincent
does not need them: the built-in `create-workflow` and `update-workflows`
workflows splice the same text into their own prompts at build time (§7,
task 024 decision 7), which is why the gap this section closes only bites where
it is hardest to notice.

*Amended 2026-09-14 (task 098).* The published skills are no longer only about
workflow authoring. `vincent-triggers` covers `{config_dir}/triggers` files,
poll scripts, arming, dry runs and the delivery ledger, and the
`create-trigger` and `update-triggers` built-ins (§5.2) splice it into their
prompts the way the workflow pair splices `vincent-workflows`. For the workflow
a trigger's `action.workflow` names, it defers to `vincent-workflows`. It needed
no change to any surface below, which is the glob doing its job.

**The published set is a glob, not a list.** `skills.FS` embeds `*/SKILL.md`;
every surface below enumerates that. A second directory under `skills/` is
reported by `vincent doctor`, listed by `vincent skills` and offered by the TUI
with no Go change.

**Versioning.** Each `SKILL.md`'s front matter carries `metadata.version`. It
sits under `metadata:` — the format's extension point, already holding `author`
— rather than as a bare top-level `version:`, which is not a key the skills
format defines and which a strict validator could reject. Content hashing was
rejected: a hash cannot tell a user's local edit from a stale copy, and cannot
tell newer from older. A test in `skills/` hashes each published tree and fails
when the tree changed and the version did not.

**What is on disk.** `skills add … -g` keeps **one** copy in a global store,
`~/.agents/skills/<name>/`, and links it into each selected agent's directory.
The link is a symlink by default and a real directory under `--copy`, which is
what Windows needs, where creating a symlink is privileged. There is no
per-agent copy and therefore no per-agent version.

| vincent adapter | `skills --agent` slug | global directory |
|---|---|---|
| claude | `claude-code` | `~/.claude/skills/` |
| codex | `codex` | `~/.codex/skills/` |
| cursor | `cursor` | `~/.cursor/skills/` |

The slug is a **table, not an identity**: claude's is `claude-code`.

**Detection is a filesystem read and never runs `npx`.** The store's
`SKILL.md` answers the installed version; each `~/.<agent>/skills/<name>` that
exists answers who it is linked into. That is what makes the report work in
`vincent doctor`, in the TUI, and on a machine with no node installed at all.
The agent list is discovered by scanning the home directory rather than from a
table of paths, so it names agents vincent does not drive — one store serves
Cline, Copilot, Zed and the rest, and hiding them would be a report that is not
about the machine.

This is **not** the v0 T1.7 "no state-file parsing" decision being reopened.
That decision is about inferring another tool's *authentication* from its
private state. `~/.agents/skills/` and `~/.<agent>/skills/` are a public CLI's
documented install locations, and the file read out of them is one this
repository published.

**States a row reports**, one row per skill carrying the agent list — never one
row per adapter per skill, which the single-store model would make identical on
all three by construction:

| state | meaning |
|---|---|
| `absent` | no copy on this machine |
| `current` | the installed version is the shipped one |
| `older` | the installed copy predates this binary's — including a copy that carries no `metadata.version` at all, which is every copy installed before the marker existed |
| `newer` | the installed copy is ahead of it — a downgraded binary, never "up to date" |
| `differs` | the versions are unequal and at least one is not semver, so no direction is claimed and both are printed |
| `unreadable` | a copy exists and its `SKILL.md` could not be read or parsed. A manifest that reads and simply has no version is `older`, not this |

Comparison uses `golang.org/x/mod/semver`. Where either side does not parse the
row says `differs` and prints both versions; it never guesses a direction.

**Install shells out**, to `npx skills add lezli01/vincent --skill <name>
--agent <slug>… --yes --global`. That is the published command plus three
flags, and the difference is required: `skills add` with no agent selection
opens an interactive multi-select, which cannot be driven from a TUI takeover
or a non-TTY CLI. `--agent` is variadic on the skills CLI, so each slug is its
own argv element rather than a comma-joined string. *Corrected 2026-09-19
(issue #489): this said `--agent <slugs>`, and what shipped joined them with
commas into one element, which the skills CLI reads as a single unknown agent
name and rejects — every install with more than one adapter selected failed.
It splits nothing; it consumes each following argument until the next flag.*
The agent list is mapped through the table above, and the two surfaces answer
the picker differently on purpose: the **CLI** defaults to all three adapters,
because a person who typed the command means "put it where my agents look" and
`--agent` is right there to narrow it, while the **TUI** answers with the
adapters the daemon actually detected, because that offer is one keypress on a
screen already showing which ones those are. `npx` is a runtime dependency of
the **install action only**; its absence is a reported outcome naming the
dependency and printing the command to run once node is available, never a
crash. The first run downloads the package, so an install is slow and needs
network.

Writing the files from the embedded copy instead was rejected: `skills/embed.go`
embeds only `SKILL.md`, so doing it would mean embedding `references/`,
`LICENSE.txt` and `agents/openai.yaml` too and reimplementing another tool's
install layout, symlink/copy split included.

**Surfaces.** `vincent doctor` grows a `SKILLS` group *beside* the agents group
(§9.5, §17); `vincent skills ls` / `vincent skills install` is the command
(§12.1); the daemon view offers it under `S` (§15). All three read the same
detection, composed server-side when a daemon answers and client-side when none
does — identical results, because vincent's daemon is localhost and runs as the
invoking user.

**Adapter differences, stated rather than emulated.** The directory table above
is the `skills` CLI's, and it says where that CLI *writes*. *Amended 2026-09-19
(task 124, issue #497): this said that whether codex and cursor read
`~/.codex/skills/` and `~/.cursor/skills/` "is not confirmed by this
repository". Both are now confirmed, each from its own source.* **codex**
reads `~/.codex/skills/`: codex-cli 0.154.0 was observed answering its
app-server `skills/list` with a skill from there, reported as `scope: user`.
**cursor** reads `~/.cursor/skills/`: cursor's skills documentation
(<https://cursor.com/docs/context/skills>, read 2026-09-19, when cursor-agent
2026.09.18 was current) lists it as the user-level location, and adds that
"Cursor also loads skills from Claude and Codex directories" —
`~/.claude/skills/` and `~/.codex/skills/`, and their project-level
counterparts. Beyond those two statements vincent still reports what is on disk
and nothing about what each agent does with it; the skill also ships
`agents/openai.yaml` for codex-side packaging. A user who finds the skill
unused by one of them is looking at that agent's own support, not at a vincent
fault. This is §9's standing rule applied to skills — a capability an adapter
lacks is documented and ignored, never emulated.

**vincent will disagree with `npx skills list -g`.** That command's agent
column is the CLI's remembered selection (`lastSelectedAgents` in
`~/.agents/.skill-lock.json`), not an on-disk fact; it can name agents that
hold no link. vincent reports the links. The lock file also carries no version,
which is why `metadata.version` is needed rather than reusable from it.

**Nothing here moves an exit code.** A missing, stale or unreadable skill is a
row, on the GitHub (task 035), release-check (task 055) and container (task 061)
precedent. `vincent doctor` still exits 0 (§17, task 006 decision 7).

## 10. Worktree management

- **Location:** `{data_dir}/worktrees/{task_id}` — outside every repo, so IDE file
  watchers and repo tooling in the main checkout are never disturbed.
  *Amended 2026-09-14 (task 099):* still true of the worktrees themselves, but
  creating one may now fast-forward the base branch's own checkout — usually the
  main one — changing its files the way a `git pull` there would (below).
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

    *Amended 2026-09-14 (task 099, issue #430): no longer true — the refusal path
    now exists.* After a fetch that resolved a commit, and only then, creation
    fast-forwards `refs/heads/{base}` to it, still under the per-repository lock and
    before `worktree add`. Only a strict fast-forward moves anything: a local base
    already at the commit is `up_to_date`, and one ahead of it (`local_ahead`) or
    diverged from it (`diverged`) is left exactly where it is. A base checked out in
    any worktree — normally the human's own checkout — moves **with** its working
    tree or not at all: a checkout partway through a merge, rebase, cherry-pick,
    revert or bisect is `checkout_busy`, and one with any `status --porcelain`
    output, untracked files included (the T1.5/T1.6 rule), is `checkout_dirty`. A
    clean one is switched with `git read-tree -m -u {old} {new}`, then the ref is
    written with the compare-and-swap `git update-ref refs/heads/{base} {new} {old}`;
    if the ref moved in between, the tree is switched back and the outcome is
    `error`, so the checkout's HEAD and working tree never disagree. It is plumbing
    throughout — never `merge --ff-only`, `pull` or `checkout` — so no post-merge or
    post-checkout hook runs in the human's checkout during an admission;
    `reference-transaction` still fires on the ref update, as it already did for
    `worktree add`. None of it blocks, fails the creation or adds a `block_reason`,
    and the task branch starts at the fetched commit whatever it answers. What the
    fetch and the fast-forward did is recorded as `base_refresh` (§5.3) in the
    claim write. Chats follow the same key: `POST /v1/chats` reads
    `fetch_base_branch` per request instead of always fetching. A pull-request task
    (the second mode, below) refreshes no base and records no `base_refresh`.
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
  *Amended 2026-08-31 (task 069).* §10 gains one **outbound** operation, and it
  is the second thing vincent has ever pushed. `PushBranch` runs
  `git push --set-upstream origin {branch}`, bounded by `gitx.RemoteTimeout` and
  with `GIT_TERMINAL_PROMPT=0` layered over the daemon's environment so a
  credential helper that wants a terminal fails instead of hanging a request
  handler. It runs only from `POST /v1/tasks/{id}/github/pull/create` (§13.2) —
  a human's act, never a step's — and it **never forces**: no `--force`, no
  `--force-with-lease`, no `+refs/...` refspec, asserted by a test on the argv
  rather than left to review. A rejected push answers `push_rejected`,
  `push_no_credential` or `push_failed` (§18), creates no pull request and
  changes nothing on the remote, for the reason `pull_branch_diverged` gives:
  the local branch may hold commits nobody pushed, and discarding them silently
  is dishonest. Uncommitted work in the worktree is not in the push and
  therefore not in the pull request; §15's form says so before the human
  confirms.

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

  *Amended 2026-09-01 (task 074, issue #288): ownership transfers.* A chat may
  hand its worktree and branch to a task (§5.5). The transfer is one write —
  the task row names the directory in the same transaction the chat's
  `worktree_path` is cleared in — so the claim set never holds two owners for
  one path and never holds none. The claim is **by path**, so the reclaimer
  needs no change at all: the task's claim covers the inherited directory,
  whose name still says `chat-{id}` and is still informational. Only the
  transcript half stays keyed to the chat, under `chat-{id}`, which the task
  never claims. After the handoff the **task is the sole owner** of that
  worktree's cleanup and that branch's lifecycle; `archive` is not legal from
  `handed_off`, so no chat-side path can reach them.

  *Amended 2026-09-17 (task 119, issue #472): a linked chat claims nothing.* A
  chat opened on a task (§5.5) works in the task's worktree and on its branch,
  but its `worktree_path` is empty and its branch fields are history, so the
  **task stays the sole owner** and the rule above — two owners resolving to one
  path is a collision — holds unchanged. gc's claim sets need no change, and
  `vincent gc` and `GET /v1/info` see no stray while the task claims the
  directory, whatever state the chat is in. Every chat-side removal has nothing
  to act on: `archive` and `hand_off` are not in a linked chat's transition
  table, closing touches no file and no ref, and `DELETE /v1/chats/{id}` refuses
  `delete_branch=true` on a linked chat rather than trusting its copy of the
  branch name. Storing the path on both rows and having the claim sets union
  them was rejected: it amends this section's one-owner rule and needs a
  linked-chat guard on every removal path.
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

  *Amended 2026-09-09 (task 092, issue #350).* Task 008's exception below widens
  from "at archive time" to "**at archive time and at permanent delete**", and
  gains nothing else. `DELETE /v1/tasks/{id}?delete_branch=true` and
  `DELETE /v1/chats/{id}?delete_branch=true` (§13.2) reuse the same judgement
  verbatim — the same `base_sha`-or-`base_branch` fork point, the same
  `-D`-only-with-a-recorded-`base_sha` rule, the same outcome vocabulary
  (`deleted` / `has_commits` / `not_ours` / `unknown` / `error`) — so the
  standing rule above is untouched: a branch carrying any commit past its base
  is reported `has_commits` and **kept**, whatever the human answered. The
  remote leg is **not offered** on a delete at all;
  `delete_remote_branch_on_archive` stays honoured only by
  `POST /v1/tasks/{id}/archive`, because deleting a branch on a forge other
  people share is unrecoverable and a delete has no second chance to reconsider.

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

  *Amended 2026-09-05 (issue #324).* **The daemon publishes both counts**, so
  no client re-derives what a slot is. `GET /v1/info` carries `slots` — `used`,
  plus `lanes` and `awaiting_input` to explain it — and every project row
  carries `slots_used` (§13.2). Both read the same slot-holding states the
  admission counters read, over **every** task row, fan-out lanes included
  (§7.6). A client cannot compute this figure: a task list excludes descendants
  by default (§13.2, task 014 decision 13), so a running lane is a held slot
  with no row to count, and `awaiting_input` holds one without being the
  literal state `running`. The TUI counted `state == "running"` over its
  root-only list and so read `0/3` with every slot taken — a number that
  answers "why is nothing starting" wrongly is worse than no number, and a
  count a client cannot compute is one it must be served.
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
  second branch in this walk and no client change. *Amended 2026-09-08 (task
  091):* `usage_limit` is a producer only in the modes that hold — under
  `usage_limit_auto_continue: never`, and under `reported_only` when the CLI
  named no reset, a quota stop blocks the task (§7.2) and writes no hold at
  all. `retry_backoff` is unconditional. Either way this walk is unchanged: a
  stop that produced no hold never reaches it. *Amended 2026-09-16 (task 106):*
  `usage_limit` has a second route in, the engine's pre-spawn check (§7.2). It
  writes the identical hold for a task whose next agent step resolves to an
  adapter with a still-shut observed window. The walk is unchanged by it too
  and still parses no snapshot: a queued task has no stored adapter, and only
  the engine, at the spawn, knows which one the step at the cursor resolves to.
  The walk applies the three checks **in this order**:
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

  *Narrowed 2026-09-05 (issue #322).* A `running` row whose `step_type` is
  `fan_out` does not count for this guard. Since that issue the park that
  spawns a round opens the round's row and the merge admission that ends the
  round finalizes it (§7.6), so a parent passing through `queued` between the
  two holds a row that is *this* round's rather than an unfinalized previous
  attempt — counting it would have every fan-out refuse its own merge
  admission. Nothing this guard was defending is given up: a crash during a
  merge leaves a row of that type too, and the merge admission that follows
  adopts it and re-runs the merge, which is the reconciliation the guard would
  otherwise wait for a human to perform.

*Added 2026-09-02 (task 081).* A `fan_out` step running `schedule: eager`
(§7.6) is woken by a lane settling rather than by its whole subtree settling,
so it takes a slot more often than a barrier one — once per direct lane
settling, at worst. The churn is bounded by the step's **direct** lane count
and not by the size of the tree below it: the watermark counts direct children,
so a depth-2 descendant settling does not move a root's number. Each such wake
either does work or parks again immediately, releasing the slot; a parent that
finds nothing to do writes no *new* `step_runs` row (§7.6, narrowed 2026-09-05
by issue #322: the round's row is opened once, by the park that spawned it).
The deadlock-freedom argument in §7.6 is untouched — the parent still releases
its slot before its children need one — and `barrier` remains the default, so
no existing workflow pays this.

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

*Amended 2026-08-31 (task 067, issue #269).* A chat's slot is now **bounded in
time as well as counted**. §7.2's `defaults.agent_timeout` bounds a running
turn and §7.4's `defaults.input_timeout` bounds an `awaiting_input` one;
either expiry kills the process tree, fails the turn with the matching
snake_case reason and returns the chat to `idle`, which frees the slot. That
closes the hole this section named in its own words: before it, a human who
walked away from a question held one of `max_parallel_chats` slots forever, and
a `send` refused with `409 chat_cap_reached` had nothing that would ever make it
succeed. The two clocks are §7.2's and §7.4's numbers verbatim — no
`defaults.chat_*` key, and no per-turn override, because §8.2's `timeout` and
`input_timeout` are workflow step fields and a chat has no workflow.

*Amended 2026-09-17 (task 119, issue #472).* A chat **linked to a task** (§5.5)
is counted here exactly as a free one is, and its task consumes no slot while
the chat talks in its worktree: the task is stopped, and the lock (§6) moves
nothing.

*Amended 2026-09-19 (task 124.9, issue #505).* Listing a chat's skills
(`GET /v1/chats/{id}/skills`, §5.5) is **not a turn and holds no slot**. Its
probe is a short-lived CLI invocation bounded by the adapter's own deadline,
not a conversation, so it is neither counted here nor refused at the cap, and
it runs while the chat's own turn is `running` or `awaiting_input`. It touches
no chat or turn row.

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
| `vincent daemon backup <path.tar.gz> / restore <path.tar.gz>` | *Added 2026-08-25 (task 030).* One `.tar.gz` of the database (`VACUUM INTO`, §14), `transcripts/`, `config.yaml` and `workflows/`, plus a manifest. `backup` is a thin API client and needs a **running** daemon; `restore` runs client-side and needs a **stopped** one, and refuses a newer schema or an occupied destination without `--force`. *Amended 2026-09-17 (task 115):* the daemon can also take the same archive **on a schedule**, into `backup.dir`, keeping the newest `backup.keep` of its own archives (§12.3). There is no new command: `restore` takes a scheduled archive exactly as it takes a manual one |
| `vincent task import <archive.tar.gz> <task-id> [--project <id>]` | *Added 2026-09-17 (task 117, issue #411).* Copies one task that was `archived` in a `daemon backup` archive — its row, its step runs and its `transcripts/{id}/` — back into this installation, with its id, `archived`. The undo for `vincent task delete`. A thin API client of `POST /v1/tasks/import` (§13.2) that needs a **running** daemon, exits 2 without one, and resolves the archive path before sending it |
| `vincent service install / uninstall / status` | Registers OS-native autostart, always as the invoking user: launchd agent, systemd user unit, Windows Scheduled Task |
| `vincent workflow ls / validate [file] / render <file> / init <name>` | Registry listing / YAML validation / template dry run / writing a new registry file. *Amended 2026-08-26 (task 034):* `init` writes the §5.2 scope directory a `--project` flag selects — global by default, resolved from §12.2 with **no daemon**; `--project N` needs one, purely to resolve the id to a repository root. `--from <example>` writes an embedded `examples/*.yaml` with its top-level `name:` rewritten. It refuses an existing path (`O_EXCL`) or a name another file in the same scope already declares, and only warns when the name shadows a lower scope. *Added 2026-08-28 (task 044):* `render` executes every template the file declares — `prompt`, `run`, `check`, `instructions`, `if` and `for_each` — against a synthetic §8.4 preview context and prints what each step would send, with the §8.6 triple each agent step resolves to. Where `validate` parses a template, this **executes** it, which is the only way `missingkey=error` catches a typo'd field. It is offline for the same reason `validate` is; `--task`/`--project` reach the daemon for a real task's facts and for registry lookups. Exit 0 clean · 1 a render error · 2 no daemon answered a `--task`/`--project` |
| `vincent task add / ls / show <id> / cancel <id> / follow-up <id>` | Thin API clients for scripting. *Amended 2026-08-25 (task 027):* `follow-up` takes exactly one of `--prompt`, `--run` and `--workflow`, plus optional `--agent`/`--model`/`--effort` (§13.2). *Amended 2026-08-28 (task 045):* `add` fills the §8.1.2 field map from repeatable `--field name=value` and/or `--fields-file <path\|->`. *Amended 2026-09-14 (task 096):* `follow-up` also takes `--paused`, §13.2's `paused: true`: the follow-up is recorded and the task held in `paused` until `resume`. *Amended 2026-09-15 (task 101):* `show` prints a `hold` row for a queued task carrying a §11 hold — `<queued_reason> until <admit_not_before>` in local RFC3339, or the reason alone when there is no resume time — and `show <id> --step RUN` prints one step_run's §5.4 recorded inputs as ASCII text in the Step Details tab's four sections (input, resolution, control flow, outcome), looked up in the detail's own `steps[]`; with `--json` it prints that element unchanged. An id that is not one of the task's runs exits 1. No wire change |
| `vincent task transcript <id>` | *Added 2026-08-28 (task 047).* Prints one attempt's transcript through `GET /v1/tasks/{id}/steps/{run_id}/transcript` (§13.2). `--step` takes a **step_run id**; omitted, it selects the running attempt, else the newest by run id. Default output is the normalized records rendered as text, `--json` is those records as NDJSON, `--raw` is the agent's own dialect byte for byte. `-f` opens on a tail and resumes from `X-Next-Offset`, ending when that attempt stops running |
| `vincent chat transcript <id>` | *Added 2026-09-16 (task 103, issue #392).* The first `vincent chat` row in this table: the rest of the family — `start`, `send`, `answer`, `cancel`, `list`, `show`, `archive`, `handoff`, `delete` — is described in `docs/reference/cli.md`, not here. Prints one chat turn's transcript through `GET /v1/chats/{id}/turns/{seq}/transcript` (§13.2), and needs **no wire change**: that route already serves `format=raw|normalized`, `offset`/`tail` and `X-Next-Offset`. `--turn` takes the turn's 1-based **seq**, the number `chat show` prints; omitted, it selects the running turn, else the newest by seq — a chat's turns are strictly sequential, so seq is chronological. An unknown seq and a chat with no turns each exit 1. Output mirrors `vincent task transcript`, through the same printer: the normalized records rendered as text by default, `--json` those records as NDJSON, `--raw` the agent's own dialect byte for byte; `--json` and `--raw` are mutually exclusive. `-f` opens on a tail and polls from `X-Next-Offset` — never the §13.3 chat stream, whose live chunks are dropped for a slow subscriber — reading the **turn's** state before each fetch, and ends with that turn: a turn in `awaiting_input` is still `running`, and a later `send` is a different turn. Whether a turn has no transcript is decided **client-side** from its row: a `failed` turn whose `fail_reason` is `agent_unavailable` or `transcript_io_error` ended before `internal/chatrun` opened `{seq}.jsonl` (`chatrun.TurnWritesNoTranscript`), so the command says so on stderr, makes no request and exits 0. Any other 404 is a file pruned by `transcript_retention_days` or removed, exit 1 |
| `vincent task diff <id>` | *Added 2026-09-15 (task 100).* Prints the task's diff through `GET /v1/tasks/{id}/diff` (§13.2), which does not change. `--by lane` reads `?by=lane` and prints every section in the daemon's order under an ASCII header — `# lane <lane_id> (task <child_task_id>, merge <12-char sha>)`, or `# remainder (…)` last — including a section with no change. `--stat` is a per-file `FILE`/`ADDED`/`REMOVED` table the **CLI** computes from the body (counted inside hunks only; `binary` for a binary file), with a leading `LANE` column under `--by lane`. `--json` is `{"diff": …}`, the sections array, or `[{path, added, removed, binary}]` rows that under `--by lane` also carry `lane_id`, `child_task_id` and `remainder`; the stat shapes are the CLI's, not wire types. The ungrouped form is **streamed with no size cap**, unlike the TUI's bounded read, because its piped output is meant for `git apply` and a cut patch is a corrupt one. It is the CLI's **first colour**: only when stdout is a terminal and `NO_COLOR` is unset (colorprofile also honours `TERM=dumb`), and piped output is the daemon's bytes exactly. Any `--by` other than `lane` is refused client-side before a request, exit 1 |
| `vincent project add <path> / ls / edit <id> / rm <id>` | Thin API clients for scripting. *Amended 2026-08-28 (task 048):* `rm` deletes the registration and its task rows, forwarding `--force` as `?force`. It never prompts — the daemon's two 409s (`N non-archived task(s)`, and one naming a `running` task) are the confirmation story, and an interactive question would be the first in a command tree whose purpose is scripting. *Amended 2026-09-16 (task 105, issue #394):* `edit <id>` is `PATCH /v1/projects/{id}` (§13.2, unchanged), sending only the fields whose flags were given — `--name`, `--path`, `--default-branch`, `--workflow`, `--max-parallel`, `--branch-template` — and refusing, with exit 1 and no daemon contacted, an edit that names none. An empty or whitespace-only value clears an optional field (`--workflow`, `--max-parallel`, `--branch-template` send `null`), the TUI form's rule and `config set`'s; `--name`, `--path` and `--default-branch` have no clear form and an empty value goes to the daemon as typed. It covers all six patchable fields, `branch_template` included, although `add` and the TUI form do not set that one. `--path` is sent as typed, like `add <path>`, so a relative path gets the daemon's must-be-absolute refusal rather than being resolved against the caller's directory |
| `vincent task pause / resume / skip / approve / reject / retry / repair / archive / answer <id>` | *Added 2026-08-28 (task 048).* The rest of §6's human actions, one subcommand each, one id per invocation. All carry `--json` and print the daemon's post-action view of the task; a 409 from the FSM is exit 1 with the daemon's own wording. `retry` takes `--branch` (§18's `branch_exists` recovery) and the edit+retry pair `--prompt`/`--run`; `repair` requires `--prompt` and takes the §8.6 triple; `archive` takes `--force` and surfaces `details.reason: worktree_dirty` with the way out; `answer` takes `--answer <n>=<value>` against the questions `task show` numbers, `--allow`/`--deny` for a permission request, or `--body <file\|->` to post a §13.2 payload verbatim. Each of `--prompt`, `--run` and `--body` has a `-file` twin, and `-` reads stdin. *Amended 2026-09-14 (task 096):* `retry` also takes `--paused`, §13.2's `paused: true`, holding the task (and a blocked parent's lanes) in `paused`; the daemon refuses it on a parent parked in `awaiting_children` |
| `vincent trigger test <id> --event <file\|->` | *Added 2026-09-14 (task 096 decision 29).* The one trigger command: a dry run through `POST /v1/triggers/{id}/test` (§13.2) of the JSON event in `--event`, which is required, `-` reading stdin. It prints each pipeline stage and the outcome the event would get, or the route's body with `--json`, fires nothing and writes nothing, and exits `1` when that outcome is `error`. Every other trigger operation is the TUI's view 11 or the API. *Amended 2026-09-14 (task 098):* no longer the one trigger command. The three rows below add two offline reads and the verb the trigger built-ins install through |
| `vincent trigger validate <file> [--json]` | *Added 2026-09-14 (task 098 decision 3).* Validates one trigger file **with no daemon**: `trigger.Parse` with the file's stem as the expected id, so the verdict is `POST /v1/triggers/validate`'s (§13.2) plus the check that `id:` equals the file name and that the name ends in `.yaml`. Text output is `<file>: ok — trigger <id>`, or one `  error: line <line>: <path>: <message>` line per error on stderr followed by `<file>: invalid (<n> error(s))`; `--json` is `{file, id, valid, errors: [{path, line, message}]}`, `id` present only when the file is valid. Exit 0 valid · 1 invalid or unreadable, mirroring `vincent workflow validate` |
| `vincent trigger ls --project <id> [--json]` | *Added 2026-09-14 (task 098 decision 4).* Reads `{config_dir}/triggers/*.yaml` **with no daemon** and prints, one per line, the path of every file whose `source.project` is `<id>`. A file that does not parse is still listed when its `source.project` can be read leniently, so a broken trigger can be found and repaired; a file whose project cannot be read at all is reported on stderr and left out. `--json` is an array of `{file, id, project, version, valid, enabled, on_fire, permission, errors}`, with `on_fire` and `permission` `""` when the file leaves them out and `version` the token `apply` compares. Exit 0 at least one file matched · 1 none did, with `--json` too — the probe shape of `git ls-files --error-unmatch` |
| `vincent workflow ls --global [--json]` · `vincent workflow apply --proposal <task_id> [--check]` | *Added 2026-09-19 (task 123 decisions 3 and 5).* Both need no daemon. `ls --global` reads `{config_dir}/workflows/*.y*ml` directly, under §5.2's regular-file and 1 MiB bounds, and prints one absolute path per line; it exits 1 when there is none, including when the directory is missing. `--json` prints each file's `file`, `name`, `version` (the `workflow.Version` token `GET /v1/workflows` reports and PATCH checks), `valid` and `errors`; a file that does not parse is still listed. Without `--global`, `ls` stays daemon-backed; `--global` with `--project` is a usage error. `apply` installs the proposal staged in `{data_dir}/workflow-proposals/<task_id>/` (§12.2) into `{config_dir}/workflows/`. The directory holds whole `*.yaml`/`*.yml` files named by their live base name and `manifest.json`, mapping each to the version `ls --global --json` reported or to `"absent"`. It runs every check before any write and refuses, writing nothing, when a staged file fails `Parse` (the verdict `validate` gives); files and manifest entries do not pair one for one, or anything else is in the directory; a version no longer matches, a file recorded `absent` now exists, or a recorded file is gone; a staged name is not a bare base name, or a new file is not `FileName(name:)`; a staged `name:` differs from the live file's (a rename — skipped when the live file has no readable name); or a staged `name:` is declared by another global file the proposal does not replace, or by two staged files (§5.2's duplicate). Each file is written with the atomic workflow writer, an existing file keeping its mode and a new one `0644`, `wrote <path>` per file, then the staging directory is removed. A failure part-way is reported, not rolled back. An empty manifest installs nothing and succeeds. `--check` runs every check, writes and removes nothing, and prints the staged absolute paths, sorted. Exit 0 installed or checked · 1 refused, nothing staged at that path, or a write failed |
| `vincent trigger apply --proposal <task_id> --project <id>` | *Added 2026-09-14 (task 098 decisions 3 and 5).* Installs the staged proposal in `{data_dir}/trigger-proposals/<task_id>/` (§12.2) into `{config_dir}/triggers/`, **without arming anything**. The directory holds full proposed `<id>.yaml` files and `manifest.json`, an object mapping each trigger id to the version token `ls --json` reported or to `"absent"` for a new file. It refuses, writes nothing and names every offending file and key when any staged file fails `Parse`; a staged file's `source.project` is not `--project`; a staged file has no manifest entry or an entry has no staged file; an existing file's version no longer matches, a file recorded `absent` now exists, or a recorded file is gone; or any file **arms** relative to the file on disk, a new file comparing against absent: `enabled` from false or absent to `true`, `on_fire` from absent or `propose` to `create`, `permission` from absent or `restricted` to `workflow`. An already-armed value may be kept and disarming is always allowed; there is no override flag (§16). Each file is written `0600` through `internal/trigger`'s version-guarded whole-file replace, `wrote <path>` is printed per file, and the staging directory is removed once every file is written, printing `removed <dir>`. A proposal with an empty manifest and no staged file installs nothing and is removed the same way — `update-triggers` finding every trigger already right. It never touches `triggers.enabled` in `config.yaml`. Exit 0 installed · 1 refused, nothing staged at that path, or a write failed |
| `vincent status <message>` | *Added 2026-08-26 (task 036).* Records what the current step is doing, in its own words (§5.4). Runs **from inside a step**: it addresses itself with §8.5's `VINCENT_TASK_ID` and `VINCENT_STEP_ID`, takes no id argument, and errors naming those variables when they are unset. Silent on success — its stdout is the step's transcript. *Amended 2026-09-17 (task 062.2 decision 4): not from inside a container — the image carries no vincent binary and `127.0.0.1` there is not the daemon; a containerized agent uses the `step_status` MCP tool (§13.4)* |
| `vincent gc [--dry-run] [--force] [--json]` | Reclaims data-root directories no task claims (§10); a thin API client like the rest |
| `vincent config get [key] / set <key> <value>` | *Added 2026-08-30 (task 060).* Reads and writes `config.yaml` through `GET`/`PATCH /v1/config` (§12.3) — a thin API client like the rest, never a second editor, so the CLI and the TUI's editor are one operation with one validation. `get` with no key prints every key as `path = value` in the file's own order; with one, that key's value alone. Keys are the dotted paths the file carries. Lists and argv are whitespace-separated inside a single argument (`notify.on "blocked awaiting_gate"`), which is also why an argv element containing a space has to be edited in the file. A `set` is in force when it answers; `listen` is the exception the command says out loud. Exit 0 · 1 the daemon refused it, with the file byte-identical · 2 no daemon answered |
| `vincent github issues / prs / pr create / status --project <id>` | *Added 2026-08-26 (task 035).* Read-only GitHub views: the project's issues newest first, and whether they can be read at all. Thin API clients like the rest — the daemon makes every GitHub call. Nothing under this command writes to GitHub. *Amended 2026-08-31 (task 069, issue #273):* the last clause stops being true for **one** subcommand. `vincent github pr create --task <id> --title <t> [--body <text>] [--draft]` drives §13.2's create route: it pushes the task's branch and opens its pull request, and it is the one thing under `vincent github` that writes to GitHub — `issues`, `prs` and `status` still write nothing. It exists for the reason every other subcommand does (the TUI holds no action the daemon does not) and because a gate script has to be able to drive that route without driving a terminal. `--body` is optional: a pull request with no description is a legal one. The fallback is **not** an error — a push that succeeded and a create that did not prints the compare URL and exits 0. *Amended 2026-09-15 (task 068.4, issue #386):* `pr create` is no longer the one writer. `vincent github pr merge --task <id> --method merge\|squash\|rebase --head-sha <sha>`, `pr close --task <id>`, `pr reopen --task <id>`, `pr comment --task <id> (--body <text> \| --body-file <path>)` and `pr rerun --task <id> --run-id <id>` drive §13.2's five write routes on the task's linked pull request. `merge` requires both flags because the CLI has no confirmation popup: they are where the human names exactly what is sent (task 068 decision 4). `--body-file -` reads stdin. `issues`, `prs` and `status` still write nothing. *Amended 2026-09-15 (task 102, issue #391):* four more subcommands under `pr` drive §13.2's existing task pull-request routes, all taking the task as `--task <id>` like `pr create`: `vincent github pr link <number> --task <id>` (POST), `pr unlink --task <id>` (DELETE), `pr show --task <id>` (GET the live row) and `pr checks --task <id>` (GET the live rollup). `link` and `unlink` write **only vincent's own link column** — no request reaches GitHub from either, and `link` does not check that the number exists — so the only commands under `vincent github` that write to GitHub stay `pr create` and task 068.4's five, and `show` and `checks` write nothing anywhere. `unlink` refuses with exit 1 and sends nothing when the task has no live link (never linked, or already suppressed): a DELETE there would record a suppressed number-0 link that stops the reconciler ever auto-linking the task. That is a client-side fast failure; the route is unchanged. Both GET routes answer 200 whatever they found, so `show` and `checks` set their own exit code: 0 when the pull request or rollup was read — for `checks`, **whatever CI concluded**, the verdict being `--json`'s `.state` — 1 when there is no live link or a named `reason` stopped the read (printed as `github.Message(reason)`), 2 when no daemon answered. `--json` emits each route's body unchanged under the same exit rule |
| `vincent doctor` | One diagnostic report: paths, daemon, log tail, database, agents, storage, task counts (§17). `--json` for scripting and bug reports; `--fix` (`--force`) reclaims orphaned worktrees and compacts the database. Exit 0 healthy · 1 problems found · 2 no daemon answered. *Amended 2026-08-26 (task 035):* it also reports the GitHub integration — the `github.enabled` toggle, `gh`'s presence, version and login state, whether a token variable is set (its **name**, never its value), and whether issues are readable. It is a **row, not a problem**: every "no" it can report leaves task creation without an issue working exactly as before, so none of it changes the exit code. *Amended 2026-08-29 (task 055):* it also reports the release check (§12.3) — whether `update.check` is on, the latest stable release and when it was last seen, this binary's version, and whether the running daemon is older than it. Rows, not problems, for the same reason: a newer release and a daemon still running the previous build both leave everything working. *Amended 2026-09-10 (task 095):* it also reports the published skills of §9.8 — one row per skill with the version this binary ships, the version installed in the global store and the agents it is linked into. A row and not a problem, on the same precedent: the built-in workflows carry the skill's text in their own prompts, so nothing a skill row can say stops a task from running, and `vincent doctor` still exits 0. *Amended 2026-09-17 (task 115):* it also reports scheduled backups (§12.3) in a `BACKUP` group: whether `backup.interval` turns them on, the directory, interval and keep, the last success and last attempt, when the next run is due, the last archive's size, how many scheduled archives are kept, and the last error. Unlike the rows above, **a failed attempt is a problem** and exits 1 (§17, task 115 decision 4). A backup that is merely overdue is not |
| `vincent agents [--json] [--refresh]` | *Added 2026-09-16 (task 104, issue #393).* A thin client of `GET /v1/agents` (§9.6, §13.2) that, like every data subcommand, **never auto-starts a daemon**. It prints one row per adapter in registration order — `AGENT`, `VERSION`, `BUILD` (the task 041 `version_verdict`), `LOGIN` (§9.5's tri-state in `vincent doctor`'s words, `-` for an adapter not installed) and `QUOTA` — then a `NOTES` cell holding only bad news: `not found`, `no mid-run input`, `no restricted mode on <os>`, `no skill listing` (*added 2026-09-19, task 124*: a `false` `supports_skill_listing`; a `null` adds nothing), `option probe failed (curated catalog)`. `QUOTA` renders the **one merged block** the endpoint serves, labelled by its `source` (a reading wins, an observation is the fallback, task 082), and merges nothing client-side: `unknown` for a null block; a reading's windows with `read <observed_at>`; `spent → <reset>` for a reset the CLI stated and `spent ≈ <reset>` for one vincent estimated (task 026 decision 2); `ok · last spent <observed_at>` for a lapsed observation. Times are local RFC3339. By default it answers from the catalog cache; `--refresh` sends `?refresh=true`. `--json` emits the endpoint's `agents` array unchanged. Exit 0 whenever the daemon answered, whatever the adapters' health (task 041 decision 4) · 1 the API returned an error · 2 no daemon answered. No wire change |
| `vincent update [--check] [--dry-run] [--require-signature] [--json]` | *Added 2026-08-29 (task 055).* Asks GitHub for the latest **stable** release and, unless `--check` is given, installs it over this binary. It queries the feed **itself** rather than through the daemon, so it works with no daemon and before the daemon's own check has polled — and so `update.check: false` (§12.3) stays a literal promise. A binary a package manager owns is never modified: the channel is detected from the resolved `os.Executable()` path and its upgrade command is printed. A binary vincent owns is verified before anything runs (§16) and swapped in place; on any failure nothing is replaced. `--check`: exit 0 up to date · 1 the check failed · 2 an update is available. Otherwise: 0 nothing to do or swapped · 1 verification or the swap failed and the binary is untouched · 2 an update exists but this install is package-managed. `--json` carries `swapped`, which separates the two 0s |
| `vincent skills ls / install [name...]` | *Added 2026-09-10 (task 095).* Lists the agent skills this repository publishes with the version shipped, the version installed in the global skills store and the agents each is linked into, and installs them (§9.8). **It never talks to the daemon**, so it cannot exit 2: detection is a filesystem read that works with no node on the machine, and the install writes into the invoking user's own agent directories — nothing daemon-owned, which is why the write is here and not behind `doctor --fix`. `install` shells out to `npx skills add … --agent <slug>… --yes --global`; with no name it installs everything not already current, `--agent` narrows the selection. Both carry `--json`. Exit 0 fine · 1 an install failed, `npx` missing included |
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

*Added 2026-09-17 (task 117, issue #411).* `vincent task import` is **not** a
third exception to §4, though it reads the same archive `daemon restore` does.
Restore is client-side only because it opens nothing; an import inserts rows,
which means opening SQLite, and only the daemon does that. So it is a thin API
client like `backup`: the CLI resolves the archive to an absolute path and POSTs
it, and the daemon reads the archive, migrates a *staged copy* of its database —
never the live file — and writes the rows. Without a daemon it refuses in
`backup`'s words.

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
write. *Amended 2026-09-17 (task 115):* the backup group reads `known: false` in
that mode for the same reason. Its settings come from `config.yaml` and are
shown, but the last attempt and its error live in the daemon's memory, so the
local report raises no backup problem.

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
  triggers/*.yaml            # event triggers, written 0600; global scope only (§12.3, task 096)
  trigger-scripts/           # poll scripts for command triggers, by convention only (task 098)
{data_dir}/                  # created 0700 (§12.2 amendment below)
  vincent.db                 # SQLite, WAL mode
  token                      # API bearer token, created 0600 at first start
  daemon.json                # { "port": N, "pid": N, "started_at": … } for client discovery
  daemon.lock
  tui.json                   # TUI-local view state: the §16 first-run acknowledgment, the board's collapsed groups (§15)
  worktrees/{task_id}/
  transcripts/{task_id}/{step_index}-{attempt}.jsonl
  transcripts/{task_id}/{step_index}-{step_id}-{attempt}.jsonl  # sub-step of a parallel group (§7.5)
  transcripts/{task_id}/{step_index}-i{iteration}-{step_id}-{attempt}.jsonl  # loop body step (§7.8)
  trigger-proposals/{task_id}/  # staged trigger files + manifest.json, 0700/0600 (task 098)
  workflow-proposals/{task_id}/ # staged global workflow files + manifest.json, 0700/0600 (task 123)
  backups/                   # scheduled backups when backup.dir is "", created 0700 (§12.3, task 115)
    vincent-backup-{YYYYMMDDTHHMMSSZ}.tar.gz  # one timer-written archive, 0600
  logs/daemon.log            # rotated, size-capped
```

*Amended 2026-09-17 (task 115).* `{data_dir}/backups/` is where scheduled
backups (§12.3) go when `backup.dir` is `""`. The timer creates it `0700` on
first use, and its archives are `0600`, as `vincent daemon backup` writes them.
It is not a data root: `vincent gc` does not scan it, and a backup archive does
not include it. It is on the same disk as `vincent.db`, so it guards against
corruption and mistakes, not against losing the disk.

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

  *Amended 2026-09-14 (#367).* The store now creates `{data_dir}` `0700`
  itself, like every directory beside it, instead of relying on the daemon's
  startup order to have created it first. This is hardening, not a fix to an
  exposed installation: no vincent code path ever produced a `0755`
  `{data_dir}`. The scope above is otherwise unchanged, deliberately: an
  existing `{data_dir}` — which can only be broader than `0700` if something
  outside vincent made it so (a pre-created `VINCENT_DATA_DIR`, a hand `chmod`,
  another tool) — is **not** re-tightened, logged or reported by
  `vincent doctor`. `vincent.db` still keeps the driver's mode.

*Amended 2026-09-13 (task 096).* `{config_dir}/triggers/` holds event-trigger
definitions, one `{id}.yaml` per trigger, and the daemon watches it with live
reload. There is no project-scope twin under `.vincent/` (task 096 decision 8).
Every write through the API is `0600`, whether the file is new or not. That is
`config.yaml`'s rule rather than a workflow's "an existing file keeps its
mode", because a trigger's argv may carry a secret and no repository owns the
file (decision 20). A missing directory is an empty registry, so moving it
aside removes every trigger. A directory the daemon cannot read is **not** read
as empty: the triggers already loaded stay loaded. A trigger's runtime state
(its cursor, its poll status and its ledger) lives in `vincent.db` (§14), never
beside the file.

*Amended 2026-09-14 (task 098).* Two more trigger locations:

- **`{data_dir}/trigger-proposals/{task_id}/`** holds a proposal staged by the
  `create-trigger` or `update-triggers` built-in (§5.2): the full proposed
  `{id}.yaml` files and a `manifest.json` recording each file's version token,
  or `"absent"`. The directory is `0700` and its files `0600`. It is outside
  every repository and outside the registry directory, because a trigger's argv
  can carry a token, so a proposal never goes in a worktree. `vincent trigger
  apply` (§12.1) removes it after a successful install. Otherwise it stays until
  the task is deleted, and `DELETE /v1/tasks/{id}` (§13.2) removes it with the
  transcripts.
- **`{config_dir}/trigger-scripts/`** is where a `type: command` trigger's poll
  script lives. It is a documented convention, not a path the daemon enforces
  or watches. It sits beside `triggers/` rather than inside it, so a script
  never shares a directory with the files the registry reads. On POSIX the
  directory and each script are `0700`. The argv runs with no shell, so on
  Windows it is `[pwsh, -NoProfile, -File, <absolute path>.ps1]`. Credentials
  come from the daemon's inherited environment (§2, §12.3) and never go in the
  script or the trigger file, and a poll script never lives in a repository
  (task 096 decision 8).

*Amended 2026-09-19 (task 123 decision 4).* **`{data_dir}/workflow-proposals/{task_id}/`**
holds the proposal a global `update-workflows` run stages (§5.2): the whole
proposed global workflow files, named by the live file's base name, and a
`manifest.json` recording each file's version token or `"absent"`. The
directory is `0700` and its files `0600`, outside every repository and outside
the watched `{config_dir}/workflows`, so nothing is live until `vincent
workflow apply` (§12.1) has checked it. Apply removes it after a successful
install; a rejected run's directory stays until the task is deleted, and
`DELETE /v1/tasks/{id}` (§13.2) removes it too.

*Amended 2026-09-18 (task 121, issue #480).* A fifth `source.type`,
`schedule`, is the clock:

```yaml
source:
  type: schedule
  project: 1
  cron: "0 9 * * 1-5"        # or: every: 6h — exactly one of the two
  timezone: Europe/Budapest  # optional; default is the daemon host's zone
```

- **Exactly one of `cron:` and `every:`.** Both set is refused at load, and so
  is neither. `poll_interval:`, `command:`, `signature:` and `allowed_actors`
  are all refused on a schedule: there is nothing to poll, run, sign or
  attribute.
- **The `cron:` grammar is five fields and nothing more** — minute (`0-59`),
  hour (`0-23`), day of month (`1-31`), month (`1-12`) and day of week
  (`0-7`, where both `0` and `7` are Sunday) — each a `*`, a single value, a
  range `a-b`, a comma list of either, or any of those with a `/n` step. There
  are no `@daily`-style descriptors, no seconds field and no `L` or `#`
  extensions. The parser is hand-written in `internal/trigger` (task 121
  decision 1), which narrows task 115 decision 2's "needs a parser dependency"
  by writing the parser rather than taking one, and makes the accepted grammar
  *by construction* the grammar the §13.2 descriptor documents. With `*` in
  one of the two day fields the other decides; with both restricted an
  occurrence matches **either**, as crontab(5) has always done. An expression
  no calendar satisfies (`0 0 31 4 *`) is refused at load.
- **`every:` is counted from the anchor, not from a wall-clock boundary**
  (task 121 decision 4). It is a duration of at least one second, the same
  floor `source.poll_interval` has and for the same reason.
- **An unknown `timezone:` is refused at load**, never quietly UTC. The
  shipped binary embeds the IANA database (`time/tzdata` in `cmd/vincent`),
  because `time.LoadLocation` reads no zone database on Windows.
- **The position is the last handled occurrence, in `trigger_cursors.cursor`**
  (task 121 decision 2), written in `store.TimeFormat` the way a GitHub source
  writes its snapshot JSON in the same column. No migration and no new column.
- **Arming anchors the clock at that moment and fires nothing.** Disarming
  drops the cursor, as it does for every source, so a disable/enable cycle —
  or `triggers.enabled` off and on — anchors afresh and fires nothing for the
  off period. A daemon stop, a suspend or a reboot is none of those: the
  anchor survives, which is the case this source exists for.
- **An overdue schedule fires once**, at the last occurrence that passed,
  however many it missed. A weekend of downtime produces one task, not forty,
  which is a strictly stronger bound than the catch-up cap a command source
  gets.
- **Occurrences are read off the wall clock on a one-second tick**, one
  goroutine walking every armed schedule, never a `time.Timer` per trigger:
  Go's timers run on the monotonic clock, which does not advance while a
  laptop is asleep, so a timer would come due hours late on exactly the
  machine this source is for.
- **Daylight saving, written out rather than left to the implementation.** An
  occurrence inside the hour a spring-forward jump skips fires **once**, at the
  first real instant after the jump. An occurrence inside the hour a fall-back
  repeats fires **once**, not twice. `every:` is a duration and is affected by
  neither.
- **`.Event` carries six keys** (task 121 decision 3), in the lowercase
  convention appendix A reserves `id` in: `id` and `scheduled_at` are the
  occurrence in `store.TimeFormat` (UTC), and `weekday`, `hour`, `minute` and
  `date` are the same instant broken out in the schedule's own zone, because an
  author who wrote `0 9 * * 1-5` means their own Monday morning. `id` being
  the occurrence is what makes the default `dedupe_key` right with no
  template: two evaluations of one occurrence cannot both fire.

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
max_tree_cost_usd: 0            # per-tree spend ceiling: a root and every descendant (§7.6, §17, §18); 0 = no cap
usage_limit_recheck_interval: 15m  # how long a quota-held task waits when the CLI named no reset (§11)
usage_limit_auto_continue: always  # what a quota stop does: always | reported_only | never (§7.2)
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
backup:                        # scheduled daemon backups (task 115)
  interval: 0                  # 0 (default) = off; e.g. 24h. Negative, or above 0 and under 1h, is refused
  keep: 7                      # timer-written archives kept; 0 = keep everything; negative is refused
  dir: ""                      # "" = {data_dir}/backups; otherwise an absolute path
# `notify:` is silent on chats (task 063, added 2026-08-30). It exists for
# unattended work that finishes hours after the human left; a chat turn ends
# while its human is looking at it, and a hook that fired on every turn would be
# noise on the one signal that is meant to be rare. `internal/config` therefore
# gains no import of the chat state package, and task 046 decision 4's
# arrangement — config imports `taskstate` and only `taskstate` — is untouched.
# (Amended 2026-09-17, task 118 decision 3: that arrangement is widened by one
# leaf, `keymap`, to validate `tui.keys`. Still no chat state package.)
# If `awaiting_input` on a long-open chat proves to need one, the named trigger
# is a separate `notify.chat_on` key, not a widening of `notify.on`.
notify:                        # run a command when a task enters one of these states (task 046)
  on: []                       # §6 state names; [] (the default) fires nothing
  command: []                  # argv, never a shell string; the envelope arrives on stdin
triggers:                      # event triggers under {config_dir}/triggers/ (task 096)
  enabled: false               # the global switch; each trigger file's own enabled: is the other
mcp:                           # the §13.4 MCP server (task 057)
  wire_steps: true             # give vincent's own agent steps the tool list; opt-out
  max_depth: 3                 # how deep tasks created over MCP may chain
  max_tasks: 32                # how many tasks one MCP creation chain may hold
container:                     # run a task's steps in a container (§16, task 061)
  image: ""                    # "" (default) = every step runs on this host
  runtime: docker              # a docker-CLI-compatible binary; only docker is verified in CI
  mount_agent_config: true     # bind-mount ~/.claude, ~/.codex, ~/.cursor read-write under the vincent home (062.2)
  network: true                # false drops the container off the network entirely
  extra_mounts: []             # host:container[:ro]; the repo and worktree are mounted already
tui:                           # view preference; the daemon validates and relays it (§15)
  board:
    group_by: [project, workflow]  # task-table grouping, outermost first; [] = flat
  hyperlinks: false            # OSC 8 links for sanitized http(s) Markdown links; opt-in (task 111)
  keys: {}                     # operation id → one key, e.g. {refresh: ctrl+e}; {} = the §15 defaults (task 118)
```

*Amended 2026-08-31 (task 067, issue #269).* Four of these keys reach **chats**
(§5.5) as well as tasks, and none of them gains a chat-specific twin:
`defaults.agent_timeout` bounds a running turn and `defaults.input_timeout` an
`awaiting_input` one, either expiry failing the turn and releasing its
`max_parallel_chats` slot (§11, §13.2); `transcript_max_bytes` caps a turn's
transcript and fails it `transcript_limit`; and `transcript_retention_days`
reclaims an **archived** chat's transcripts on the same pass as an archived
task's (§17) — *amended 2026-09-01 (task 074, issue #288): an **ended** chat's,
either terminal state, measured from the same `updated_at`; a handed-off chat's
transcripts stay under `chat-{id}`, which the task that inherited its worktree
never claims*. `notify:` stays the one exception, silent on chats for the reason
its own comment gives.

**`container:` (task 061, added 2026-08-30).** `image` is the whole switch and
`""` is the default: no image means every step runs on this host, no runtime is
consulted, and an existing installation is byte-for-byte unchanged. Set it and
the task's step processes run inside **one** container, created with the task's
worktree and removed with it. *As of task 061 that is every `command` step, and
every `check:` — including a check hanging off an agent step; a `manual` step
runs no process, so containerizing it is vacuous. The **agent** process itself
is still spawned on the host: moving it needs a spawn seam across all three
adapters and is task 062. Until that lands, a containerized task whose workflow
has agent steps is a mixed run, and it is neither refused nor warned about.*
*Amended 2026-09-16 (task 062.1, issue #396): the spawn seam has landed (§9.1),
but its only launcher is the host's, so agent processes still run on the host
and the mixed run above is still what a containerized task gets. Task 062.2
(issue #397) adds the container launcher.*
*Amended 2026-09-17 (task 062.2, issue #397): it has. An agent step of a
containerized task runs inside the task's container through the container
launcher (§9.1), so a containerized task has no mixed run left: every step
process it starts runs in its one container. Chats (§5.5) are not tasks and
keep running on the host.* The
image is the user's: it must already carry the agent CLI a workflow's agent
steps resolve to, and `git`. Vincent builds nothing, publishes nothing and
bundles nothing, the posture it already takes toward `gh` and `cosign`.
*Amended 2026-09-17 (task 062.2): the CLI is resolved on the **image's**
`PATH` by its bare binary name, and `agents.*.path` does not apply inside the
image. A CLI the image lacks fails the step `agent_unavailable`; the host no
longer needs it installed for a containerized task.*

*Amended 2026-09-14 (issue #366).* `mount_agent_config` defaults to **false**
until task 062. With only commands and checks in the container, nothing inside
it reads `~/.claude`, `~/.codex` or `~/.cursor`, so task 061's default of true
handed the host's agent credentials, writable, to the image and to step code
for no benefit. The knob keeps its semantics, and 062 turns the default back on
when it moves the agent in; a `config.yaml` that sets the key keeps its value.
For the same reason `network: false` with `mcp.wire_steps: true` is no longer
refused at task creation (task 061 decision 1): every agent reaches the
per-step MCP endpoint from the host, whatever the container's network is. Task
062 reinstates that refusal together with the `host.docker.internal` rewrite.

*Amended 2026-09-17 (task 062.2 decisions 3 and 5, issue #397).*
`mount_agent_config` defaults to **true** again, now that the agent runs in the
container and reads those directories: subscription auth takes no key from the
environment, and cursor persists `--model` to its own config (§9.7). A
`config.yaml` that sets the key keeps its value. The directories no longer land
at their own host paths. With the knob on, the container is created with a
writable tmpfs **vincent home** at `/vincent-home` (mode 1777, beside the
`/vincent-run` scratch mount), and each of `~/.claude`, `~/.codex` and
`~/.cursor` that exists on the host is bind-mounted read-write beneath it —
`/vincent-home/.claude` and so on. Every containerized step, command and agent
alike, runs with `HOME=/vincent-home`, because a CLI finds its config through
`$HOME` and an image's own `HOME` under `--user {uid}` is usually `/`. A HOME
the user's `environment` policy decides wins: one it sets, lists in `inherit`
or unsets is left alone. Two consequences are stated rather than discovered:
the image's own HOME contents are hidden while the mounts are on, and **on
macOS claude keeps its OAuth login in the Keychain**, not in `~/.claude`, so a
Mac host must supply `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` through
`environment` for a containerized claude step to authenticate. Codex's
file-based `auth.json` and Linux claude's `.credentials.json` do carry over.
The worktree and repository keep their identical paths (task 061 decision 2),
which is what claude's cwd-keyed session store needs. The `network: false`
with `mcp.wire_steps: true` refusal returns, **narrowed** to a workflow that
runs an agent: see the table below.

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
| `network: false` with `mcp.wire_steps: true` | task creation, **once task 062 lands** | `400 validation_failed` — a container with no network cannot reach the daemon's per-step MCP endpoint. *Amended 2026-09-14 (issue #366): not refused until then, because every agent still runs on the host.* *Amended 2026-09-17 (task 062.2 decision 5, issue #397): refused again, but only when the workflow, after §7.9 include expansion, has an agent step at any depth — top level or inside a `parallel`, `fan_out` or `loop` body. A command-only workflow wires nothing and still runs with no network* |
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
*Amended 2026-09-17 (task 062.2): it is now — an agent step of a containerized
task runs through the container launcher or fails, and is never moved to the
host either.*

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

*Amended 2026-09-13 (task 096.3, decision 31D).* The same tick judges
`type: github_issues` and `type: github_prs` triggers. For each project with an
armed GitHub trigger it makes at most one issues listing and one pulls listing,
and hands both to every such trigger, so the cost does not grow with the number
of triggers. Each listing asks for `state: all` and 100 rows, starting two
minutes before the oldest watermark among that project's triggers. These
listings are separate from the open-pull-request listing above, which is
unchanged (decision 35). The quiet failure policy above does **not** apply to
them. Each of the following makes every affected trigger's poll status read
failing, with the reason: a failed listing, `github.enabled: false`,
`poll_interval: 0`, or a project whose `origin` is not a github.com repository.
A trigger that silently never fires is exactly the question its ledger exists
to answer. With GitHub not polled, the reconciler's idle heartbeat still runs
to report that.

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

**`backup` and scheduled backups (task 115, added 2026-09-17).** The daemon
takes the archive `vincent daemon backup` writes (§12.1) on a timer. The timer
is wired in `internal/daemon.Run` beside the transcript pruner (§17), checks
about once a minute, and reads this block on every check, so an edit to any of
the three keys takes effect at the next check with no restart and no hook into
the config applier. `interval` is the only switch, and `0`, the default, is off:
every archive carries every transcript (task 030 decision 1), so turning it on
for existing installs would quietly spend up to `keep` times that much disk. A
negative `interval` or `keep` refuses the file, for `update.poll_interval`'s
reason. So does an interval above 0 and under an hour: each run holds the
store's single connection for the length of `VACUUM INTO` (§14) and then re-tars
every transcript, and anything tighter is cron's job. `dir: ""` resolves to
`{data_dir}/backups` (§12.2), created `0700` on first use. A non-empty `dir` must
be absolute, for the reason `POST /v1/daemon/backup` refuses a relative path:
the daemon would resolve it against its own working directory, which nobody
chose. The default location is on the same disk as the database. It protects
against corruption and mistakes, not against losing the disk.

- **The schedule is counted from the newest archive on disk**, not from daemon
  start and not from memory. The timer names its archives
  `vincent-backup-<UTC YYYYMMDDTHHMMSSZ>.tar.gz`, fixed width, so the newest
  name is the last success and a restart does not reset the clock. An install
  whose newest archive is older than `interval`, or that has none, runs at the
  first check after startup. A failed attempt is retried **one hour** later, not
  at the next check, which would loop hot against a stale newest archive.
- **An archive never carries its final name until it is complete.** The
  database copy and the archive are written into a `.vincent-backup-*` staging
  directory inside `dir` and renamed into place in the same directory. A daemon
  killed mid-run therefore leaves a staging directory, never a truncated file
  that would count as the newest success. Leftover staging directories are swept
  when the timer starts, and only then: a manual backup runs through this same
  daemon, so none can be in progress at that moment. A `dir` inside
  `{data_dir}/transcripts` fails the run, because the archive would read itself.
- **Retention is by count, over the timer's own archives only.** After a
  successful run, and never after a failed one, archives matching the name
  pattern beyond the newest `keep` are deleted. `keep: 0` keeps everything, as
  `transcript_retention_days: 0` does. A manual `vincent daemon backup` archive
  in the same directory, and any other file, is never counted and never
  deleted. A prune that fails is logged at warn and reported as `prune_error`.
  It is not a doctor problem, because the backup it followed succeeded.
- **Status is in memory**, beside the archives that are its durable half. The
  last attempt, its error, the next due time, the last archive's size and the
  retained count are what `GET /v1/doctor`'s `backup` group serves (§13.2). A
  failed last attempt is a doctor problem (§17). The block is not secret, so
  §13.4's `config_get` redaction does not change, but `dir` decides where copies
  of `config.yaml` and every transcript land, and the TUI's editor asks before
  changing it (§15).

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

**`usage_limit_auto_continue` (task 091, added 2026-09-08).** Whether a
recognized quota stop is waited out or handed to a human. One of three values,
default `always`:

| value | a recognized quota stop … |
|---|---|
| `always` | holds and re-queues the task, exactly as §7.2 describes. The behaviour of every version before this key, so no existing installation changes |
| `reported_only` | holds when the adapter parsed a reset the CLI actually named; **blocks** when the wait would be the `usage_limit_recheck_interval` estimate |
| `never` | blocks |

Blocking means `blocked` with `block_reason: usage_limit` at the step the task
is on, cursor unmoved, attempt still `interrupted`, **no** retry consumed — the
shape `cost_limit` takes. A human retry re-runs the step with a full budget.

It exists because the classification can be wrong. An adapter recognizes a
quota wall by wording (§9.1), and a stop the daemon merely guessed at re-queues
unattended and spends money on the next attempt; `never` stops that at the
first occurrence, and `reported_only` stops exactly the guessed class while
keeping the unattended recovery the feature was built for. A tri-state rather
than a boolean for that middle value alone. Overloading
`usage_limit_recheck_interval: 0` was rejected — zero is already invalid
because it re-admits on the next tick, and one key would then mean both "how
long" and "whether".

The mode is read at the moment of the stop, the way the interval beside it is,
so a hot reload reaches the next quota stop rather than the next daemon
restart. A hold **already in flight is not converted**: the task keeps it, is
re-admitted once, and meets the new mode at the next stop. The per-adapter
observation (§14) is recorded in every mode, estimate included, so the board
never disagrees with itself about a spent window — which means
`usage_limit_recheck_interval` keeps a meaning in every mode: it is the
estimate, even where it times no wait. Claude-only in effect, since it is the
only adapter that recognizes the wording at all (§9.1); inert on codex and
cursor.

*Amended 2026-09-16 (task 106).* The mode also decides what a recorded wall
means for the **other** tasks on the adapter, at §7.2's pre-spawn check:

| value | a task reaching an agent spawn while the adapter's observed window is still shut … |
|---|---|
| `always` | is held until that window's `resets_at`, without spawning |
| `reported_only` | is held when that reset was CLI-reported; spawns against an estimate |
| `never` | spawns, and finds the limit itself |

The mode is read at the check as well as at the stop, so a hot reload reaches
the next spawn. Holding every task on a match the operator has said they
distrust would spread one wrong classification across the adapter, and the
spawn that is let through is what retires a wrong observation (§14).

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
spend. ~~A per-tree cap was considered and deferred — it needs a recursive rollup
over `parent_task_id` and a rule for which task blocks when the total trips — and
the multiplication is documented here rather than worked around.~~ *Amended
2026-09-17 (task 116, issue #409):* the tree has its own cap,
`max_tree_cost_usd` below, which answers both questions. This key still counts
one task and the multiplication still holds for it; the parent's
`children.cost_usd` (§13.2) is now where a lane's spend shows. It is also
inert on the adapters that report no cost: codex (§9.3) and cursor (§9.7) leave
`cost_usd` unset, and the check is guarded by "some attempt reported a cost"
rather than by arithmetic, so a cap must never be estimated from token counts.

*Amended 2026-09-11 (task 096).* A task may carry **its own cap** beside this
one: `max_task_cost_usd` on `POST /v1/tasks` (§13.2), stored on the task
(§5.3). The engine blocks `cost_limit` at the **lower** of the two, where 0 on
either side means "no cap from this side" — this key's own convention — so a
task cap can tighten the global cap and never lift it: a task that asks for $50
under a $10 global stops at $10. The task's value is fixed at creation; this key
stays hot-reloaded. Inert on codex and cursor for the same reason as above
(task 096 decision 18).

**`max_tree_cost_usd` (task 116, issue #409, added 2026-09-17).** A ceiling, in
US dollars, on what **one fan-out tree** may spend. A tree is a root task — one
with no `parent_task_id` — and every descendant at any depth (§7.6); a task that
never fans out is a tree of one. Its total is the §17 rollup of `cost_usd` over
every step run of every task in it: retries, repair runs, follow-up rounds and
the lanes a follow-up round spawns all count, and so do archived descendants,
the way §13.2's `children` rollup counts them. It is a lifetime total and never
resets. Past it, the task whose attempt took the tree over goes `blocked` with
`block_reason = tree_cost_limit` (§18). Zero, the default, is no cap, and a
negative value fails the load. It sits beside `max_task_cost_usd` for that key's
reasons and is read per check the same way, so a hot reload reaches running work
and "raise the cap and retry" stays the remedy.

It is checked at the same attempt boundary as `max_task_cost_usd` and is
**independent** of it, not folded into that key's lower-of: the two measure
different quantities. When both are over at one boundary the block is
`cost_limit`, because the narrower cap is the one raising this key cannot clear.
Only the task whose attempt crossed the line blocks. That is usually a lane, but
the parent's own attempts are attempts in the tree too: its steps before the
fan-out, its steps after the join, and a `merge.on_conflict: agent` run. A parent
parked in `awaiting_children` makes no attempts, so it never blocks this way
while parked. Its join stays open because a lane is `blocked`, which §13.2's
`children.blocked` already shows. Every other task still working in the tree
learns the total is over at its own next boundary, so the overshoot is **at most
one attempt per task still working in the tree**, not the one attempt
`max_task_cost_usd` promises.

It is a config key and nothing else. There is no create-time field on
`POST /v1/tasks`, no `vincent task add` flag, no trigger `limits:` key and no
workflow field. A workflow field would reverse "a budget is not something a step
inherits" above, and a create-time field can be layered on the same check later,
the way task 096 added one to `max_task_cost_usd`. It is inert on codex and
cursor by the same guard, now "some step run in the tree reported a cost", and a
tree mixing them with claude counts only what was reported. That undercount is
stated, never estimated from token counts (task 116 decisions 1–7).

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
*Amended 2026-09-14 (task 099, issue #430):* the same key now also governs the
fast-forward of the project's local base branch that follows a successful fetch
(§10). There is no second key — a fast-forward without a fetch has nothing to move
to — so `false` turns both off. It also reaches chats now, which used to fetch
regardless of it.

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

**`github` (task 035, added 2026-08-26; amended 2026-08-31, task 069).** Whether
the daemon may talk to GitHub about a project whose `origin` is a github.com
repository (§5.3, §13.2, §15). No call is ever made from a step — the daemon
calls at pick time, at create time, on the reconciler's tick, and when a human
asks for a pull request.

It governed **reading only** until task 069, which gave it **one write**:
`POST /v1/tasks/{id}/github/pull/create` pushes a task's branch to `origin` and
opens its pull request. There is deliberately **no second key** gating that
write (task 069 decision 2) — the consent is a human's keypress and the editable
popup in front of it, not a line in `config.yaml` nobody would turn on — so
`enabled: false` turns the write off along with every read, and is the one
switch there is. Nothing else under this key writes: no update, no comment, no
close, no merge.
*Amended 2026-09-15 (task 068.4, issue #386):* that last sentence is no longer
true. Merging, closing, reopening and commenting on a task's linked pull
request, and re-running its failed checks (§13.2), are writes under this key
too, gated by it alone on the same reasoning: `enabled: false` turns every write
off, and there is still no second key.

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
which is the honest *n* for that run. *Amended 2026-09-16 (task 106):*
`admit_not_before` rides beside `queued_reason`, RFC3339 or `null` when the
task carries no hold, so a notifier can say *until when* as well as *why*
without calling back. It covers `usage_limit` and `retry_backoff` holds alike.

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

**`triggers` (task 096, added 2026-09-13).** The daemon's inward signal, and the
mirror of `notify`. Where `notify` runs a user's argv when a task changes state,
a trigger file under `{config_dir}/triggers/` (§12.2) polls a command or GitHub,
or accepts a signed push. For each new event that passes its filter it creates
a task or acts on one (decision record rows 33 and 34). `enabled` is the global
switch, and the second of **two** off-by-default keys beside each file's own
`enabled:`. Task 069 decision 2 held that where a keypress is the consent, a
second key nobody would turn on adds nothing. A trigger has no keypress, so
this key has to be the consent. Absent means `false`.

The key is read per poll and per pushed event, and the config applier wakes the
trigger manager on a reload. Turning it on arms every enabled, valid trigger at
once, and turning it off disarms them at once. **Arming seeds:** a trigger's
first poll after arming, whether from its own `enabled:` or from this key,
records what the source shows and fires nothing. **Disarming drops the
cursor**, so events from an off period never fire (task 096 decision 16). A
restart does neither: the cursor persists, and the next poll is a catch-up
capped at 20 events (§17). The key is editable over `PATCH /v1/config`, which
is not an MCP tool (§13.4), and the TUI's editor asks before changing it (task
060's `dangerous` flag). While it is off, `GET /v1/triggers` still lists every
file with `armed: false` and a reason, both dry runs still work, and a pushed
event is a `409` (§13.2).

*Amended 2026-09-18 (task 122, issue #483).* **`overrun:` and
`concurrency_key:` say what to do with an event whose group already has
unfinished work.** Nothing in the pipeline looked at task state before this:
every event that passed the filter fired, however many of the trigger's tasks
were mid-flight, which is right for a one-shot event and wrong for a source
that emits repeatedly about the same object. `dedupe_key` cannot express it —
it is all-or-nothing and permanent — and `limits.max_per_hour` is wall clock,
not liveness. `overrun:` is a closed enum, checked as the **last** step of the
pipeline, after render and after a reaction's target resolution:

| value | behaviour |
|---|---|
| `parallel` | the default, and byte-for-byte today's behaviour: no check at all |
| `skip` | record the event `superseded` and drop it; the in-flight work stands |
| `cancel_previous` | replay §6's cancel against every in-flight task in the group, then fire, recording which task this delivery superseded |
| `queue_coalesce` | hold the event; when the group empties, fire the **newest** held event and record the rest `superseded` |
| `queue_serial` | hold the event; when the group empties, fire the **oldest**, and so on until the backlog drains |

`concurrency_key:` is a `text/template` over `.Event` naming the group. Absent,
it is the trigger id for a `create_task` — so an author who sets only
`overrun:` gets one at a time per trigger — and the **resolved target task**
for a `follow_up`, `retry` or `cancel`, whose group is that task's own state,
read from `tasks.state` and not through this trigger's ledger: on the first
follow-up the target has never appeared in it. It is deliberately not
`dedupe_key` — for the motivating case the dedupe key is per modification while
the group is the ticket — and it carries `dedupe_key`'s standing warning:
**changing `concurrency_key` on a live trigger re-groups events already in
flight.** Setting it without an `overrun:` other than `parallel` is a load
error, not a silent no-op. In flight is `!taskstate.Settled` (§6, §14).

**An unreviewed `on_fire: propose` proposal holds its group.** That is the
intended reading — the human has not acted on it, so a second proposal for the
same object is noise — and with `propose` the default it is the first thing a
confused author hits: a `skip` trigger whose first proposal is never admitted
never fires again for that group. `queue_serial` under `propose` drains only as
fast as a human approves, so a busy object reaches the per-trigger cap of
**100** held events, where the oldest is dropped and recorded `superseded`
rather than silently lost. Held events are a table (§14), not a field: they
survive a restart, and the drain runs when a task reaching a settled state says
the group emptied, with the manager's 5 s reconcile tick as the backstop. A
**disarm discards the backlog** and records each held event, exactly as it
drops the cursor, so an off period never fires; deleting a trigger drops the
backlog and keeps the ledger. A drained event is **re-judged in full** — its
dedupe key, `max_per_hour`, render and target resolution all run again on the
state of the world at drain time — minus the overrun step, which it has already
passed. An event a group **already holds** is not held twice — it is recorded
`superseded`, because a source that re-shows its whole window every poll would
otherwise fill the backlog with copies of one event and `queue_serial` would
fire it once per copy. `POST /v1/triggers/{id}/test` reports the decision the
way it reports `would_dedupe`, and writes neither a ledger row nor a backlog
row.

A `type: command` trigger's argv is executed directly, never through a shell,
with the previous cursor in `VINCENT_TRIGGER_CURSOR`. That is the only
`VINCENT_*` variable vincent sets for it; one the daemon itself inherited is
passed on or not by the same policy as any other name. It runs under a fixed **1-minute** timeout that
kills the whole process tree, and under the `environment` policy above like
every other child of the daemon. A GitHub trigger is judged on
`github.poll_interval`'s tick (above), so it needs `github.enabled` and a
non-zero interval. With either missing, its poll status reads failing with the
reason rather than going quiet.

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

*Amended 2026-09-17 (task 111, issue #404):* `tui.hyperlinks`, a bool, default
`false`, turns on OSC 8 hyperlinks for Markdown links in the output pane (§15,
§16). It is opt-in because nothing probes whether a terminal understands OSC 8,
so there is no env-var or per-terminal override: the human who turns it on is
the capability probe. The TUI reads it with the rest of this section on every
connect and reconnect and again after its config editor saves, and holds one
session value both workspaces read.

*Amended 2026-09-17 (task 118, issue #412):* `tui.keys` rebinds the TUI's
keys. It is a map of operation id → one key string, in Bubble Tea's key-string
form (`R`, `ctrl+r`, `f5`, `shift+tab`: modifiers `ctrl+`, `alt+`, `shift+` in
that order, a shifted character written as itself), and `{}`, the default, is
§15's shipped keymap. The operations are §15's twelve vocabulary terms
(`refresh`, `archive`, `delete`, `draft_remove`, `add`, `editor`, `free_text`,
`browser`, `open_row`, `scope`, `filter`, `lane`), §6's actions (`pause`, one id
for pause and resume, `approve`, `reject`, `retry`, `edit_retry`, `repair`,
`skip`, `cancel`, `follow_up`, `chat` — *added 2026-09-17, task 119* — with
`archive` shared with the term) and the global chrome (`palette`, `palette_alt`, `help`, `help_alt`, `next_attention`,
`mouse`, `quit`, and `new`, which is also the chats board's `n`). Every other key
the TUI answers is fixed (§15). An override **replaces** the operation's
default on every surface that carries it; it is not an alias, so the vacated
key is free for another override in the same edit and two operations may swap.
Mapping an operation to its own default is a no-op. `internal/config`
validates the map through `internal/keymap` — the checker the TUI's registry
tests run over the defaults — at load, on hot reload and on `PATCH /v1/config`,
and so on `vincent config set`. It refuses an unknown operation id; the name of
a fixed key (`group`, `fold`, `esc`, `tab`, `resume`, …), by name and with the
reason; a string no key press produces, including `shift+` on a character; a
key that already means anything else — an operation or a fixed key, on any
surface — unless a recorded exception on that exact default key covers both
meanings, which does not travel with an operation that moves; and a printable
key for `palette_alt`, `help_alt` or any operation answered where a text field
owns the printable keys. Problems are joined with `; ` under a `tui.keys: `
prefix, each naming the operation, the key and what the key already means, in
two passes: every unknown id, fixed name and malformed key string at once, and
only when there are none, every collision and printable-key refusal at once —
a map with both kinds reports the first kind alone. As for `board.group_by`, a refused `PATCH` leaves the file
byte-identical and a refused reload keeps the last good configuration.
`GET /v1/config` serves `tui.keys` as an object, never `null`. This makes
`keymap` `internal/config`'s second internal import beside `taskstate`, and
task 118 decision 3 says so as an explicit amendment of task 046 decision 4;
`keymap` is a leaf, so the direction stays one-way. The daemon still does not
act on the section and still publishes no config event (§13.3): the TUI applies
the keymap on every connect and reconnect, on the daemon view's config fetch,
and right after its own config editor saves `tui.keys`.

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
  values, with no sleep. *Amended 2026-09-11:* the fire is not always later.
  One whose debounce ran out as a patch arrived read the bytes the patch was
  replacing and applied them after the patch's own apply, so the `GET` read
  the old value (the m11 gate, on macOS). The watcher now holds the applier's
  lock from its read of the file through its apply; the patch writes before
  it applies, so a read under that lock is never older than the last applied
  patch.
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
(§15). *Amended 2026-09-17 (task 115):* `backup.dir` joins them. A scheduled
archive holds `config.yaml`, with its `environment.set` values and
`notify.command`, and every transcript, so the directory decides what the daemon
exposes.

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
  earlier step installed. *Amended 2026-09-17 (task 062.2, issue #397): this
  now covers a containerized **agent** run as well as a command step. The agent
  step journals `step_runs.container_id` beside the `docker exec` client's PID,
  so recovery removes the labelled container rather than trusting the client
  PID, and a step stop signals the agent through the same pid file.*
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

*Amended 2026-09-17 (task 119, issue #472).* A turn of a chat **linked to a
task** (§5.5) is recovered by the same rule: finalized `interrupted`, never
re-run, the chat back to `idle`. The chat stays **open**, so the task it locks
stays locked (§6) with no recovery code of its own — the lock is a query over
the chat's state, and `idle` is not terminal. When the task runs in a container
(§16) the turn ran inside it, where the recorded PID is only the runtime
client's, so recovery first stops the agent through its pid file in the task's
container (task 061 decision 9, keyed by the turn) and then runs the host kill
above for the client.

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

*Narrowed 2026-09-05 (issue #322).* Both surfaces exclude one **step type** as
well as those states: a `running` row on a `fan_out` step. §7.6's park opens
the round's row and the merge admission finalizes it, so a parent woken between
the two is `queued` holding this round's row, which is normal operation rather
than the contradiction above. The exclusion is by `step_type`, so it costs
nothing: a crash mid-merge leaves a `fan_out` row too, and the merge admission
that follows adopts it and re-runs the merge on it.

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
stale copy is a nuisance and not a correctness problem. *Amended 2026-09-17
(task 062.2, issue #397): a containerized cursor step writes the same file, on
the host side of the worktree bind mount, so the sweep reaches it unchanged; a
test pins that against a containerized task's worktree path.*

*Amended 2026-09-01 (task 080).* A `fan_out` crashed mid-round is recovered by
re-running **the round**, not the step. The round number is derived from the
child rows — the deepest wave any spawned lane sits at — so recovery lands on
the same round it left, the lanes already merged re-merge as "Already up to
date", and no lane spawns twice: a lane that has a child row is never in the
ready set. The re-run writes a new attempt at the same `iteration`, and because
only *failed* attempts spend the budget, a crash costs no retry.

*Amended 2026-09-02 (task 081).* A `schedule: eager` fan-out (§7.6) is recovered
by the same path with nothing added. Merges are idempotent and no merge cursor
is persisted, so a crashed eager parent re-merges its already-merged lanes as
"Already up to date" exactly as a barrier one does, and a lane that has a child
row is never in the ready set, so none spawns twice. The wake watermark is
cleared by the transition out of `awaiting_children` that recovery performs, and
the next admission recomputes it from the rows.

## 13. HTTP API

### 13.1 Transport and auth

- HTTP/1.1 + JSON on `127.0.0.1` only. No TLS in v1 (loopback). *Amended
  2026-09-17 (task 062.2 decision 1, issue #397): `/v1` and `/mcp` are still
  served on loopback only. The one exception is a **step-only listener** bound
  on a container network's gateway IP while a containerized agent step needs
  it. It serves `/mcp/step/{run_id}` and nothing else, authenticated by that
  step run's secret rather than the token (§13.4, §16).*
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

*Amended 2026-09-13 (task 096; decision record row 34).* **The trigger ingress
is authenticated twice, and exempt from nothing.** `POST /v1/triggers/{id}/events`
requires the bearer token above like every other route. It then requires the
trigger's own signature: under the `github_hmac_sha256` scheme,
`X-Hub-Signature-256` carrying an HMAC-SHA256 of the raw body, keyed by the
value of the environment variable the trigger names and compared in constant
time. The signature covers the bytes as they arrived, so this route alone reads
its body **raw** rather than decoding it. It reads under the **4 MiB** tier,
because a webhook payload routinely exceeds 64 KiB, and only after verifying
does it parse the body as a JSON object. A body over the bound is a `413`, as
everywhere else. A bad or missing signature, or a secret variable the daemon's
environment does not set, is a `401`, and the three cases are indistinguishable
on purpose. `POST /v1/triggers/validate`, `PATCH /v1/triggers/{id}` and
`POST /v1/triggers/{id}/test` also read under the 4 MiB tier, because each
carries a trigger source or a vendor payload.

A trigger's own dedupe is its ledger (§14), keyed by a template over the event.
A `create_task` it replays carries an `Idempotency-Key` the daemon derives from
that key: `trigger:{id}:` followed by a hash. That keeps two triggers, or a
trigger and a CI job pushing in with its build id, from replaying each other's
tasks, and keeps the header inside the bound above whatever the rendered key
says.

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
                                        *Added 2026-09-05 (issue #324):* a `slots` object —
                                        { used, lanes, awaiting_input } — the §11 count of tasks
                                        holding a concurrency slot right now, beside the
                                        `max_parallel_tasks` it is measured against. `used` is
                                        what a "used / cap" header renders; `lanes` and
                                        `awaiting_input` are subsets of it that explain a
                                        numerator not matching the rows a client is showing.
                                        One indexed COUNT over `tasks`, which the scheduler
                                        already runs on every admission pass — admissible by the
                                        same cheapness rule, and unlike the events count it is
                                        over a human-sized table
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
                                        *Added 2026-08-31 (issue #279):* `supports_resume`, the
                                        §5.5 chat gate's own answer, on this route only
                                        *Amended 2026-09-02 (task 082):* the §9.6 `quota`
                                        block gains `windows[]` and fills its scalars —
                                        here and on /v1/info's `agents[]` alike, from the
                                        one read
POST   /v1/agents/{name}/quota          *Added 2026-09-02 (task 082).* a usage reading a
                                        source **pushes**, because the daemon has no way
                                        to go and fetch it:
                                        { source, windows[{ name, used_percent, window,
                                        resets_at? }] } → 204. claude is why it exists
                                        (§9.2) — its windows arrive through Claude Code's
                                        status line, which runs `vincent statusline`
                                        (§12.1) once per render. An unknown adapter is
                                        404 and a body naming no window is 400. The
                                        reading lands in the catalog cache, never in the
                                        database (§14), and `agent.quota_changed` is
                                        appended only when it actually changed (§13.3).
                                        It rides the same recover → log → auth chain and
                                        the same bearer token as everything else, which is
                                        why the status line is the vincent binary and not
                                        a shell script: the token stays out of a file.
                                        Not an MCP tool (§13.4)
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
                                        *Amended 2026-09-17 (task 115):* a `backup` group, the
                                        scheduled timer's state (§12.3) →
                                        { known, enabled, dir, interval, keep,
                                          last_success_at, last_attempt_at, last_error?,
                                          next_due_at, last_bytes, retained, prune_error? }.
                                        `dir` is `backup.dir` resolved; timestamps are null when
                                        unknown; `last_error` and `prune_error` are omitted when
                                        empty. `known` is false only on a report composed with
                                        no daemon, whose settings still come from config.yaml.
                                        It adds a member to the closed set: a `backup` problem,
                                        "the last scheduled backup failed: <error>", present iff
                                        known && enabled && last_error is non-empty (§17). An
                                        overdue backup and a `prune_error` are not problems.
                                        The GET serves a copy of the timer's status and never
                                        takes a backup
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

GET    /v1/projects                     list. *Amended 2026-09-05 (issue #324):* every project
                                        row carries `slots_used` — how many of that project's
                                        tasks hold a slot right now (§11), lanes included, which
                                        is the numerator the per-project `max_parallel_tasks` is
                                        applied against. One GROUP BY for the whole list; a
                                        project holding none reads 0, never absent
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
                                        *Amended 2026-09-06 (issue #345):* `author` is
                                        **normalized to GitHub's own spelling**, here and on the
                                        issues listing above, where it also covers `assignee`.
                                        The two legs agreed about every account except a bot:
                                        `gh` rewrites one to `app/dependabot`, a spelling that
                                        exists nowhere in GitHub's data, where the account is
                                        `dependabot[bot]`. Both legs now fold onto the REST
                                        form, so this field cannot mean two things depending on
                                        which leg answered. It belongs in the same sentence as
                                        "it fetches, normalizes, sorts and returns": a client
                                        matching on `author` was correct against one leg and
                                        silently wrong against the other, and silently is the
                                        operative word — an unmatched author is not an error,
                                        it is a dependabot sweep reporting "0 of 0" against two
                                        open bumps and finishing done
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
GET    /v1/tasks/{id}/github/pull/checks *Added 2026-08-31 (task 068.2).* The live check rollup for
                                        the linked pull request's **head commit**:
                                        { linked, repo?, number?, ref?, runs[], state?, fetched_at?,
                                          reason? }, each run
                                        { name, state, url?, run_id?, started_at?, completed_at? }.
                                        `state` is one word for the whole commit and one word per row
                                        — queued, in_progress, success, failure, cancelled, skipped,
                                        neutral, timed_out, action_required or stale — with GitHub's
                                        check runs and its older commit statuses normalized onto the
                                        same shape by both legs. `run_id` is the GitHub Actions
                                        workflow run behind a row, absent for a third-party check run
                                        and for a legacy commit status, which is what tells a row
                                        re-run can honestly be offered on from one it cannot.
                                        Answers **200 with `reason`** for every failure, as the row
                                        route does: a tab that refuses to render because GitHub is
                                        unreachable is worse than one that says so. Never cached and
                                        never stored — a check result a minute old reads exactly like
                                        a current one while being wrong
POST   /v1/tasks/{id}/github/pull/create
                                        *Added 2026-08-31 (task 069).* `{ title, body, draft }` —
                                        the one route that **writes to GitHub** (*until task 068.4,
                                        2026-09-15, added the five writes below*). Runs the §13.2
                                        gate, refuses a task that already has a live link (409,
                                        `pull_already_linked`), pushes the task's branch to
                                        `origin` with `--set-upstream` and **never** `--force`,
                                        creates the pull request, and writes the link as
                                        `source: human` so the reconciler will not overwrite it.
                                        A **push** failure is a 409 with a named §18 reason and
                                        nothing is attempted at GitHub. A **create** failure is a
                                        **200** carrying `{ pushed: true, compare_url, reason }`
                                        — the fallback, not an error: the branch is on the remote,
                                        so GitHub's own page works. Success answers
                                        `{ created: true, pushed: true, pull, task }`. Not an MCP
                                        tool (§13.4)
POST   /v1/tasks/{id}/github/pull/merge
                                        *Added 2026-09-15 (task 068.4).* `{ method, head_sha }`,
                                        both required: `method` is `merge`, `squash` or `rebase`
                                        with no default and no config key, and `head_sha` is the
                                        head commit the human confirmed. Runs the §13.2 gate, then
                                        refuses a task with no live link (409, `pull_not_linked`;
                                        a suppressed link is not live). Reads the pull request's
                                        merge state and, when it is blocked, the live check rollup,
                                        and refuses **before sending** with a §18 reason:
                                        `branch_behind`, `checks_running`, `head_changed`, or
                                        `not_mergeable` (closed, merged, draft, conflicted or
                                        otherwise blocked). The send is pinned to `head_sha`, so a
                                        push landing after the preflight is refused as well.
                                        **200** with the pull request re-read after the merge.
                                        Never deletes the branch, never `--auto`, never an admin
                                        override
POST   /v1/tasks/{id}/github/pull/close
POST   /v1/tasks/{id}/github/pull/reopen
                                        *Added 2026-09-15 (task 068.4).* No body. The same gate and
                                        `pull_not_linked` refusal; **200** with the pull request
                                        as the write left it
POST   /v1/tasks/{id}/github/pull/comment
                                        *Added 2026-09-15 (task 068.4).* `{ body }`, non-empty
                                        (400 `validation_failed`); **200** `{ url }`, the created
                                        comment's page. No idempotency key: a second call posts a
                                        second comment
POST   /v1/tasks/{id}/github/pull/checks/rerun
                                        *Added 2026-09-15 (task 068.4).* `{ run_id }`. Re-runs the
                                        failed jobs of one GitHub Actions run, only when the live
                                        rollup for the current head has a failed, Actions-backed
                                        row with that `run_id`; any other is refused 409
                                        `bad_request` before sending. **200** `{ run_id }`.
                                        *Amended 2026-09-16 (task 068.5, issue #388):* a `run_id`
                                        below 1 is refused earlier, **400** `validation_failed`,
                                        before the §13.2 gate and so before GitHub is asked.
                                        All five: a GitHub refusal is **409** with `details.reason`
                                        from the vocabulary (`no_write_scope` for a 403) and never
                                        GitHub's own text; no event is published, because the link
                                        does not change; there is no task-state guard; and none is
                                        an MCP tool (§13.4)

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
GET    /v1/chats                        *Amended 2026-09-09 (task 092, issue #350).* Also takes
                                        limit, offset, archived_before and archived_since —
                                        GET /v1/tasks' parameters, spelled the same way, because
                                        issue #298 already settled that one vocabulary covers
                                        both entities. The chat bounds are measured over
                                        **`updated_at`, with no new column**: a terminal
                                        transition is the last write a chat row takes, so
                                        `updated_at` already *is* when it ended (task 074
                                        decision 6, task 079 decision 2), which is what
                                        TerminalChatIDsBefore has measured retention off since.
                                        A terminal-only listing is ordered
                                        `updated_at DESC, id DESC`, the mirror of the task side
                                        *Added 2026-08-30 (task 063).* Chats, newest first.
       ?project_id=&state=              `state` may repeat. Chats appear here and nowhere else:
       &archived=                       never in GET /v1/tasks and never on the board.
                                        *Amended 2026-09-01 (issue #298):*
                                        `archived=false|true|all`, default `false` — spelled and
                                        defaulted exactly as GET /v1/tasks' parameter is, so one
                                        vocabulary covers both entities (`terminal=` was the
                                        rejected alternative). It hides **both** terminal states,
                                        `archived` and `handed_off` alike (§5.5, task 074
                                        decision 5), which its name does not say and this
                                        sentence does. An explicit `state=` wins over it, as it
                                        does for tasks. Anything else is `400 validation_failed`
       &task_id=                        *Amended 2026-09-17 (task 119, issue #472):*
                                        `task_id=` narrows to the chats linked to one task —
                                        with `archived=all`, closed ones included, which is
                                        how a task's workspace lists its conversations; a
                                        non-integer is `400 validation_failed`. `archived=` now hides all **three**
                                        terminal states, `closed` with the other two. Every chat
                                        representation carries `linked_task_id` (omitted on a
                                        free chat)
POST   /v1/chats                        { project_id, title, agent?, model?, effort?,
                                          base_branch? } → 201 with the chat, its
                                        `vincent/{id}-{slug}` branch and its worktree (§10).
                                        An omitted `agent` resolves to the first registered
                                        adapter that can resume — there is no `defaults.agent`
                                        key, and a chat's premise is continuity. An adapter that
                                        cannot resume is refused `400 agent_cannot_resume`
                                        (§9.3, §9.7): vincent will not replay the log as prompt
                                        context in its place.
                                        *Amended 2026-09-14 (task 099):* the worktree is created
                                        under `fetch_base_branch` (§12.3), read per request as a
                                        task's admission reads it, and every chat representation
                                        carries `base_sha` and `base_refresh` (§5.5)
DELETE /v1/chats/{id}                   *Added 2026-09-09 (task 092, issue #350).* Permanent
                                        delete of an **archived** chat: the row, its chat_turns
                                        (through the schema's cascade) and its
                                        `transcripts/chat-{id}` directory. `DELETE /v1/tasks/{id}`
                                        minus the fan-out clause, with one refusal of its own —
                                        `handed_off` (409): the task named by `handoff_task_id`
                                        owns the worktree and branch (§5.5, task 074 decision 5),
                                        and a deleted row cannot say that; delete the task instead.
                                        *Amended 2026-09-17 (task 119):* legal from `closed` as
                                        well. `delete_branch=true` on a **linked** chat, in any
                                        state, is `409 chat_linked_to_task` with
                                        `details.task_id`: the branch is the task's, and the
                                        chat's copy of its name is history
GET    /v1/chats/{id}                   { chat, turns[] } — the whole conversation, oldest turn
                                        first, each with its accounting (§5.5)
POST   /v1/chats/{id}/send              { message } → 202 with the new turn. `409` outside
                                        `idle` (§5.5), and `409 chat_cap_reached` when
                                        `max_parallel_chats` chats already hold a live process
                                        — **refused, never queued** (§11)
POST   /v1/chats/{id}/answer            the §7.4 answer flow verbatim: same normalized request,
                                        same `Respond()`. `409` outside `awaiting_input`.
                                        *Amended 2026-08-31 (task 067, issue #269), replacing the
                                        2026-08-30 correction: the wait **is** bounded, by
                                        `defaults.input_timeout` (24h), and a running turn by
                                        `defaults.agent_timeout` (60m) — §7.2's and §7.4's
                                        numbers verbatim, with no `defaults.chat_*` key and no
                                        per-turn override, because §8.2's `timeout`/`input_timeout`
                                        are workflow step fields and a chat has no workflow.
                                        Expiry fails the turn `timeout` or `input_timeout`, kills
                                        the process tree, returns the chat to `idle` and releases
                                        its `max_parallel_chats` slot. `transcript_max_bytes`
                                        applies to a turn's transcript the same way, failing it
                                        `transcript_limit`*
POST   /v1/chats/{id}/cancel            stops the live turn and kills its process tree
POST   /v1/chats/{id}/archive           removes the worktree and deletes the branch when it
                                        received nothing (§10, task 008 semantics), `?force=`
                                        for the dirty-worktree refusal. Terminal.
                                        *Amended 2026-09-01 (issue #298):* archiving is legal from
                                        `idle` alone, so the `409` **names the state that actually
                                        blocked it** rather than asserting a live turn: an
                                        `archived` chat is told it is already archived and a
                                        `handed_off` one that the task owns its worktree now
                                        (§5.5). The message and `details.state` had disagreed.
                                        *Amended 2026-09-17 (task 119):* on a live **linked**
                                        chat it is `409 chat_linked_to_task`, naming the task
                                        that owns the worktree in the message and in
                                        `details.task_id`; a closed one is told it is already
                                        closed
POST   /v1/chats/{id}/handoff           *Added 2026-09-01 (task 074, issue #288).* Takes
                                        `POST /v1/tasks`' body, **validated by the same code** so
                                        the two routes accept exactly the same task; `project_id`,
                                        `base_branch` and `branch_name` are the chat's and are
                                        ignored. `201 { task, chat }`: the task carries
                                        `source_chat_id` and the chat comes back `handed_off` with
                                        `handoff_task_id` set and `worktree_path` cleared. One
                                        transaction, scheduler notified after the commit (§5.5).
                                        `400` when the task does not validate; `409` outside
                                        `idle`, with no worktree to give, or with a git operation
                                        in progress (`repo_operation_in_progress`, the operation
                                        in `details.operation`). Every refusal leaves the chat
                                        exactly as it was. It is **not** an MCP tool (§13.4).
                                        *Amended 2026-09-17 (task 119):* a live **linked** chat
                                        is `409 chat_linked_to_task` naming the task, replacing
                                        the "nothing to hand over" its empty `worktree_path`
                                        would otherwise produce
POST   /v1/chats/{id}/close             *Added 2026-09-17 (task 119, issue #472).* Ends a chat
                                        linked to a task: a live turn is cancelled and waited
                                        for, then `idle → closed` (§5.5) and the task's lock
                                        lifts. No body. `200` with the chat. The worktree and
                                        branch are the task's and are not touched. `409` on a
                                        free chat (archive it instead) and on a chat already
                                        terminal, with `details.state`. It is **not** an MCP
                                        tool (§13.4)
GET    /v1/chats/{id}/skills            *Added 2026-09-19 (task 124.9, issue #505).* The skills
       ?refresh=                        the chat's agent CLI would load for its next turn, and how
                                        a message invokes one, from §9.6's skill cache;
                                        `?refresh=true` probes again. Resolution is §5.5's table.
                                        `200` with flat siblings (§9.6's rule):
                                        { chat_id, agent, work_dir, list_verdict,
                                          unavailable_reason, probe_error, probed_at,
                                          invoke_verdict, invoke_sigil, invoke_position,
                                          skills[], problems[] }
                                        `list_verdict` and `invoke_verdict` are `supported`,
                                        `unsupported` or `unknown` with `agent.InputVerdict`'s
                                        meaning; `invoke_verdict` is `unknown` only for an
                                        unregistered adapter. `unavailable_reason` is `""` for
                                        none; `probe_error` and `probed_at` (RFC3339 UTC, when
                                        the served list was obtained) are `null` for none. A
                                        `probe_error` beside a `supported` list means the list
                                        is the earlier one the cache kept. `invoke_sigil` and
                                        `invoke_position` are `""` when the adapter cannot
                                        invoke. Each skill is { name, invocation, description,
                                        argument_hint, aliases[], scope, plugin, path }, every
                                        field the CLI's own word; `invocation` is the adapter's
                                        `SkillInvoker.Invocation`, `""` when it cannot invoke,
                                        so no client builds one. Each problem is { path, message }.
                                        `skills`, `problems` and `aliases` are always arrays;
                                        order and duplicate names are the CLI's; there is no
                                        `kind` and no inferred scope; an empty `skills` means
                                        "none" only under `supported`. `409 invalid_state` for
                                        a terminal chat (`details.state`, `details.action:
                                        "skills"`), `409 task_has_no_worktree` for a linked
                                        chat whose task has none (`details.task_id`). No event
                                        and no row. It is **not** an MCP tool (§13.4)
GET    /v1/chats/{id}/events            *Added 2026-08-31 (task 067).* SSE: this chat's durable
                                        `chat.*` events interleaved with its live output, the
                                        per-task stream's shape for a chat. The filter is the
                                        payload's `id` — a chat event carries no `task_id`, by
                                        design — and the output rides the chat's own broker key,
                                        so neither stream can be handed the other's bytes.
                                        `Last-Event-ID` resumes the durable events only (§13.3)
GET    /v1/chats/{id}/turns/{seq}       *Added 2026-08-31 (task 067).* One turn's transcript,
       /transcript                      with the step route's range contract: `?offset=` and
       ?offset=|?tail=&format=          `?tail=` mutually exclusive, whole records at both ends,
                                        `X-Next-Offset` naming where the next fetch resumes.
                                        There is no `run_id` analogue — a chat turn **is** its
                                        run, named by its 1-based `seq`
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
                                        `defaults`, field-declaration, lane, merge and
                                        `defaults.container` rows, the common step fields, and every
                                        step type with the fields it accepts and the contexts it may
                                        be nested in. Generated from the table `workflow.Parse`
                                        validates against, so a client renders forms from it
                                        instead of carrying a second copy
POST   /v1/workflows/validate           { yaml } → { valid, errors[], warnings[] }
GET    /v1/triggers                     *Added 2026-09-13 (task 096).* { enabled, dir, triggers[] }.
                                        `enabled` is `triggers.enabled` (§12.3). Each row is
                                        { id, file, version, valid, errors[], enabled, armed,
                                        disarmed_reason?, source_type, action_type, project_id,
                                        on_fire, permission?, poll: { seeded, last_poll_at, ok,
                                        error?, last_fire_at } }. A file that does not validate
                                        is listed with its errors, never hidden, with no parsed
                                        definition: it omits source_type, action_type,
                                        project_id and on_fire, and reads enabled: false,
                                        whatever its file says. `armed` is
                                        valid + enabled + triggers.enabled, and `disarmed_reason`
                                        names the first of those that is missing
POST   /v1/triggers                     { id, project_id, poll_interval?, command[]?, workflow?,
                                          title? } → 201 { id, file, version, errors[] }.
                                        Writes {config_dir}/triggers/{id}.yaml, 0600, from a
                                        daemon-rendered starter: `type: command`, `create_task`,
                                        `enabled: false` and **no `on_fire` line**, so no YAML
                                        travels on the wire. 409 when the file exists
GET    /v1/triggers/schema              the trigger schema as data, the way /v1/workflows/schema
                                        serves §8.2: top-level fields, the source and action
                                        variants, and the values marked dangerous
                                        (`enabled: true`, `on_fire: create`,
                                        `permission: workflow`), each with the warning a client
                                        shows before committing it
POST   /v1/triggers/validate            { source, id? } → { valid, errors[] }. `id`, when given, is
                                        the file stem the document's `id` must match
GET    /v1/triggers/{id}                the list row plus `source` (the file's bytes) and
                                        `definition` (null when it does not validate)
PATCH  /v1/triggers/{id}                { version, ops[] } → 200, in the create response's shape.
                                        The ops and the version token are PATCH /v1/workflows'.
                                        A stale version is 409 with `details.version`. A result
                                        that does not validate is 400 with the findings, as a JSON
                                        string, in `details.errors`, and the file is left
                                        byte-identical. Written 0600 whatever its mode was
DELETE /v1/triggers/{id}?version=       204. Removes the file. The registry reload drops the
                                        trigger's cursor, and the ledger is **kept**, so a
                                        re-created id cannot refire an event it already
                                        delivered (task 096 decision 21). `version` is required,
                                        and a stale one is 409
POST   /v1/triggers/{id}/test           { event } → a judgement: { event_id, matched, match_miss?,
                                        if?, if_rendered?, dedupe_key?, would_dedupe,
                                        overrun?, concurrency_key?, in_flight?, would_skip?,
                                        would_cancel?, would_queue?, action?,
                                        outcome, error? }. Runs the supplied event through the real
                                        pipeline (match, allowed_actors, if:, dedupe, rate limit,
                                        render, reaction target, overrun), and `action` is the request it
                                        would replay, unsent. Writes nothing, and works while
                                        the trigger or triggers.enabled is off. `outcome: fired`
                                        here means "would be replayed"
POST   /v1/triggers/{id}/poll           → { seed, events[], truncated, refused, cursor?, error? }.
                                        Runs the source once for real (the command, or a GitHub
                                        listing) and judges each event as /test does, with no
                                        fire, no cursor advance, no ledger row and no change to
                                        poll health. `seed` says a real poll now would seed. A
                                        failing command or listing is a 200 carrying `error`.
                                        400 for a source that has no poll: type: http, which is
                                        pushed, and — *amended 2026-09-18 (task 121)* —
                                        type: schedule, which is a clock. Neither has a source
                                        to run once; POST …/test still judges a supplied event
GET    /v1/triggers/{id}/deliveries     ?limit=1..1000 (default 100) → { deliveries[] }, newest
                                        first: { id, trigger_id, event_id, dedupe_key,
                                        concurrency_key?, superseded_task_id?, outcome,
                                        task_id, detail?, created_at }. `outcome` is fired |
                                        seeded | deduped | filtered | rate_limited | refused |
                                        error | superseded | queued, and `task_id` is the task
                                        created or acted on; `superseded_task_id` the task a
                                        `overrun: cancel_previous` fire replaced (task 122).
                                        Served for an id with no file, because the ledger
                                        outlives the file
POST   /v1/triggers/{id}/events         the `type: http` ingress (§13.1, decision record row 34).
                                        A raw body of at most 4 MiB (413 over it), with the bearer
                                        **and** the signature. 404 for no such trigger; 400 for
                                        a source that is not type: http; 409 invalid_state with
                                        `details.reason` when the trigger is not armed or its
                                        file does not validate; 401 for a bad or missing
                                        signature or an unset secret variable, with no ledger
                                        row; 400 for a body that is not a JSON object, or that
                                        has no string `id` and no X-GitHub-Delivery header to
                                        take one from. 200 → the delivery: the judgement plus
                                        { delivery_id, task_id?, detail? }. A push has no
                                        catch-up cap
                                        Across the trigger routes, an unknown id is 404, and a
                                        file that does not validate is 400 with its findings on
                                        /test and /poll
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

GET    /v1/tasks?project_id=&state=&archived=&archived_before=&archived_since=&limit=&offset=
                &parent_id=&include_children=
                                        *Amended 2026-09-09 (task 092, issue #350).*
                                        archived_before and archived_since are RFC3339 instants
                                        over `archived_at`; anything unparseable is a 400
                                        validation_failed. They narrow to the archive on their
                                        own — `archived_at` is NULL on every live row and NULL
                                        fails both comparisons. An **archived-only** listing is
                                        ordered `archived_at DESC, id DESC` rather than
                                        `id DESC`: recency is the only order an archive has, and
                                        a task created last week and archived this morning must
                                        not sort below one archived a year ago. There is
                                        deliberately no `sort=` parameter — there is one right
                                        answer per listing
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
                                        *Amended 2026-09-17 (task 116, issue #409):* the
                                        rollup also carries `cost_usd`, the §17 spend of
                                        the task's descendants at any depth, archived ones
                                        included. It is **not** the task's own spend, which
                                        stays in steps[], and it is `null`, never 0, when no
                                        descendant reported a cost. Beside `blocked` it is
                                        what tells a parent why a lane blocked
                                        `tree_cost_limit` (§12.3). Summed by a query of its
                                        own, so the scheduler's settle check never joins
                                        step_runs
POST   /v1/tasks                        { project_id, workflow, title, description?, fields?,
                                          base_branch?, branch_name?, priority?, agent?,
                                          model?, effort?, github_issue?, github_pull?,
                                          paused?, restricted?, max_task_cost_usd? }
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
                                        *Added 2026-09-11 (task 096):* three optional fields,
                                        for every client. `paused: true` creates the task in
                                        `paused` rather than `queued` — invisible to admission
                                        until `POST /v1/tasks/{id}/resume` (§6). `restricted:
                                        true` is the one-way clamp (§9.4): every agent step
                                        runs `restricted`, and task 041's creation gate judges
                                        the clamped mode, so a clamped task on an adapter that
                                        cannot restrict here is a **400** `validation_failed`.
                                        `max_task_cost_usd` (a number ≥ 0; negative is a
                                        **400**) is the task's own cap, applied at the lower
                                        of it and config's (§12.3). All three enter the
                                        idempotency digest, and are omitted from it when
                                        absent, so a body that names none digests as before.
                                        Every task representation carries `restricted` and
                                        `max_task_cost_usd` (null when the task set none)
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
                                        re-lookup of today's registry.
                                        *Added 2026-09-14 (task 099, issue #430):* every task
                                        representation also carries `base_sha` (omitted when
                                        none was recorded) and `base_refresh` (null, not
                                        omitted, when none was) — the §5.3 columns, reversing
                                        task 056 decision 4
                                        *Added 2026-09-17 (task 119, issue #472):* every task
                                        representation also carries `open_chat_id` while a
                                        chat linked to it is open (omitted otherwise) — the
                                        reverse of `chats.linked_task_id`, one indexed query
                                        per list built into a map, as `source_chat_id` is.
                                        While it is set the task is locked (§6) and
                                        `available_actions` is `[cancel]` where cancel is
                                        legal and `[]` elsewhere
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
DELETE /v1/tasks/{id}                   *Added 2026-09-09 (task 092, issue #350).* Permanent
                                        delete of an **archived** task: the row, its step_runs
                                        and its `{data_dir}/transcripts/{id}` directory.
                                        *Amended 2026-09-14 (task 098):* and its
                                        `{data_dir}/trigger-proposals/{id}` directory, a
                                        staged trigger proposal (§12.2), under the same
                                        data-root containment check as the transcripts, so
                                        nothing outside the data directory is touched.
                                        *Amended 2026-09-19 (task 123):* and its
                                        `{data_dir}/workflow-proposals/{id}` directory, a
                                        staged global workflow proposal, under the same check.
                                        `?delete_branch=true` or `{ delete_branch }` also applies
                                        §10's empty-branch judgement to its branch — never to a
                                        branch with commits, and never to a remote.
                                        200 { deleted: true, branch? }, branch carrying archive's
                                        own shape and vocabulary. It is **not a §6 action**:
                                        taskstate has no opinion on it, it never appears in
                                        `available_actions`, and the state check is this
                                        handler's own — the precedent is DELETE /v1/projects/{id},
                                        which is likewise no action. Four refusals, each a 409
                                        naming the row that is holding on in `details.reason`:
                                        `not_archived`, `has_lanes` (an archived fan-out parent
                                        whose lanes still exist — `parent_task_id` has no
                                        ON DELETE clause, so without this it is a driver error
                                        rather than a refusal), `handoff_target` (a `handed_off`
                                        chat points at it and would be left pointing at nothing,
                                        `chats.handoff_task_id` being ON DELETE SET NULL).
                                        404 on an unknown id. `created_by_task_id` and the
                                        idempotency rows clear themselves and need no refusal.
                                        The `events` rows are **not** purged: their id is the
                                        Last-Event-ID cursor (§13.3). There is no bulk delete and
                                        there is not going to be one (task 011) — a sweep is one
                                        DELETE per row
POST   /v1/tasks/import                 *Added 2026-09-17 (task 117, issue #411).* { path,
                                        task_id, project_id? } → 200 { task_id, project_id,
                                        title, step_runs, step_runs_renumbered,
                                        transcript_files, transcript_bytes, archived_at,
                                        backup_schema_version, backup_created_at }. Copies one
                                        task out of a `daemon backup` archive (§12.1): the row,
                                        its step_runs and `transcripts/{task_id}/`, in one
                                        transaction that appends `task.restored` (§13.3). The
                                        undo for DELETE /v1/tasks/{id}, and a literal segment
                                        with no POST /v1/tasks/{id} to shadow. `path` must be
                                        absolute. The task keeps its id and comes back
                                        `archived`, `archived_at` stamped now (§17),
                                        `worktree_path` NULL, `created_by_task_id` NULL unless
                                        that task is live; every other column is copied as it
                                        is, and no git operation runs. Step run ids are all
                                        kept when all are free and all renumbered, in order,
                                        when any is taken (§14). `project_id` re-homes the task;
                                        without it the backed-up project must be live under the
                                        same id **and** name. `transcript_path` is re-rooted at
                                        this data dir by its `transcripts/{task_id}/` segment,
                                        either separator. The archive is staged under the data
                                        dir with task 030's entry checks, the staged database
                                        is opened (and so migrated) as a store of its own, and
                                        every refusal is checked before anything is placed; a
                                        failed insert removes the transcript directory it
                                        placed. 400 validation_failed: a path that is missing,
                                        relative or not a regular file, a `task_id` that is
                                        not positive, not a vincent backup, no database or one
                                        that cannot be opened, an unsafe entry, or
                                        `details.reason`
                                        `schema_too_new`. 404 not_found with `details.reason`
                                        `task_not_in_backup` or `project_not_found`. 409
                                        invalid_state with `details: {action: "import",
                                        reason}`: `task_exists`, `not_archived` (with the
                                        backed-up `state` — a task live at backup time carries
                                        running step runs §12.4 would treat as orphans),
                                        `project_mismatch`, `parent_missing` (a fan-out lane
                                        whose parent is not live) or `transcripts_present` (a
                                        stray directory, never merged or deleted). One task per
                                        call (task 011)
POST   /v1/tasks/{id}/cancel
POST   /v1/tasks/{id}/pause
POST   /v1/tasks/{id}/resume
POST   /v1/tasks/{id}/retry            { prompt_override?, run_override?, branch_override?, paused? }
                                        (blocked, awaiting_children). branch_override renames
                                        the task's branch before re-admission — the recovery
                                        path for a branch_exists block (§10, task 001); it is
                                        validated and collision-checked exactly as creation is,
                                        and unlike the other two it does not touch the
                                        snapshot. *Added 2026-09-05 (task 090, issue #328):*
                                        from awaiting_children the call cascades to every
                                        blocked descendant instead and writes nothing to the
                                        parked parent, which comes back still
                                        awaiting_children; all three overrides are a 400 from
                                        that state. The response is the ordinary task object
                                        plus retried_descendants, always present and 0 when
                                        nothing was cascaded. *Amended 2026-09-13 (task 096
                                        decision 31C):* `paused: true` lands the task in
                                        `paused` instead of `queued` (§6's held table), and a
                                        blocked parent's cascade holds the lanes it re-admits
                                        too. From awaiting_children `paused` is a 400.
                                        *Amended 2026-09-17 (task 119):* the cascade skips a
                                        lane an open linked chat has locked and does not count
                                        it. On a locked task `branch_override` is refused `409
                                        task_locked_by_chat` **before** the rename, which would
                                        otherwise commit ahead of the refused transition
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
POST   /v1/tasks/{id}/follow_up        { prompt? | run? | workflow?, agent?, model?, effort?, fields?, paused? }
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
                                        whatever it exits with. *Amended 2026-09-13 (task 096
                                        decision 31C):* `paused: true` persists the request and
                                        lands the task in `paused` instead of `queued`, so the
                                        response's task is paused and `resume` starts the run.
                                        *Amended 2026-09-14 (task 027 decisions 13 and 14,
                                        issue #369):* `fields` are laid over the task's stored
                                        fields for this run only; a named workflow's declared
                                        fields are substituted and validated against the result
                                        as creation does (§8.1.2), a failure is a 400, and the
                                        task row keeps its own fields
POST   /v1/tasks/{id}/chat             { title?, agent?, model?, effort? }
                                        (blocked/awaiting_gate/done/aborted only; added
                                        2026-09-17, task 119, issue #472). Opens a §5.5 chat
                                        linked to the task, working in its worktree and on its
                                        branch, and locks the task until the chat closes (§6).
                                        Every field is optional and the body may be absent:
                                        `title` defaults to the task's, and the triple
                                        resolves as a repair's does (task 025 decision 6) —
                                        request, task override, workflow `defaults`, adapter —
                                        with an unset agent falling to the first registered
                                        adapter that can resume. The permission mode is the
                                        workflow's `defaults:` clamped by the task's
                                        `restricted`. `201` with the chat, `idle`, carrying
                                        `linked_task_id` and no `worktree_path`; the task does
                                        not move and no `task.*` event is emitted. Refusals:
                                        `409` with `details.state` outside the four states;
                                        `409 task_locked_by_chat` with `details.chat_id` when a
                                        linked chat is already open; `409
                                        task_has_no_worktree` on a task that never got one —
                                        the daemon does not create it; `400 validation_failed`
                                        for an unregistered agent, and on a restricted task
                                        for an adapter that cannot keep a resumed turn
                                        restricted (codex, §9.3); `400 agent_cannot_resume`
                                        for an adapter that cannot hold a conversation. It is
                                        **not** an MCP tool (§13.4)
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
                                        (issue #323, 2026-09-05: each also carries what the
                                        attempt was **given** and the resolution behind it —
                                        `rendered_prompt`, `rendered_run`, `rendered_check`,
                                        `rendered_if`, `rendered_for_each`,
                                        `input_truncated`, `agent_source`, `model_source`,
                                        `effort_source`, `permission_mode`, `timeout_ms`,
                                        `check_timeout_ms`, `shell`, `work_dir` (§5.4, §14).
                                        The rendered fields are the **full recorded bytes**,
                                        deliberately unlike `prompt_override`/`run_override`,
                                        which this DTO reports as booleans: those flag a
                                        human's edit for a timeline, these are the record a
                                        details pane reads. Null means nothing was recorded —
                                        an attempt from before the record existed, and every
                                        field the step type has no input for — while an empty
                                        string is a render that produced nothing, and a client
                                        must say the two differently. `rendered_if` is display
                                        only (§7.7) and `rendered_for_each` is a JSON array.
                                        `GET /v1/tasks` does **not** carry any of them: they
                                        are per-attempt evidence, not a board column)
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
                                        **v0 wire change (task 066, 2026-08-31):** a third
                                        record type, `agent.run_header`
                                        (`work_dir`, `available_tools: []string` — the tool
                                        list cannot ride on `tools`, which is
                                        `agent.tool_use`'s objects); `agent.result` gains
                                        `duration_ms`, `api_duration_ms`, `num_turns`,
                                        `stop_reason`, `terminal_reason`,
                                        `cache_read_tokens`, `cache_write_tokens`,
                                        `model_usage: [{model, input_tokens, output_tokens,
                                        cache_read_tokens, cache_write_tokens, cost_usd}]` and
                                        `permission_denials: [{tool_name, call_id}]`;
                                        `agent.tool_result`'s entries gain `verb` and
                                        `blocked`; and **any** record may carry
                                        `parent_call_id`. Every one is omitted when
                                        unreported, so a client tells "unreported" from
                                        "zero". Same reasoning as T4.14's and T4.16's: records
                                        are recomputed from the raw file on every read and
                                        never stored, and live chunks are ephemeral, so the
                                        handler, the §13.3 live publisher and the in-tree
                                        clients moved in one commit rather than carrying two
                                        shapes.
                                        **v0 wire change (task 070, 2026-08-31):** two more
                                        record types, both in the **shared** vocabulary
                                        rather than a codex namespace — `agent.plan`
                                        (`items: [{text, completed}]`, `plan_call_id`: the
                                        agent's running to-do list, whole on every record so a
                                        reader who joins mid-run learns where it *is*, not how
                                        it got there) and `agent.command_output` (`output`,
                                        `truncated`, `call_id`, `name`: the output body
                                        `agent.tool_result` refuses to carry, capped at
                                        `agent.CommandOutputMax` runes with the cut stated).
                                        `agent.result` gains `reasoning_tokens`. codex fills
                                        all three today and claude and cursor fill none, which
                                        their adapters' tests state positively — the same
                                        answer task 066 gave for `agent.run_header`, and the
                                        reason these are not per-adapter types. One stream
                                        line may now produce **two** records: codex reports a
                                        command's outcome and what it printed on one
                                        `item.completed`, and the two are separate records
                                        because clients show them at different verbosity
                                        levels.
                                        **v0 wire change (task 109, 2026-09-17):** three
                                        more record types in the shared vocabulary, the
                                        main loop's account of a subagent, each keyed by
                                        the spawning call's `call_id` —
                                        `agent.subagent_started` (`description`,
                                        `subagent_type`, `background`),
                                        `agent.subagent_progress` (`description`,
                                        `subagent_type`, `tool_uses`, `total_tokens`,
                                        `duration_ms`, `last_tool`) and
                                        `agent.subagent_finished` (`status`, `summary` —
                                        one line capped at the adapter, never the report —
                                        `tool_uses`, `total_tokens`, `duration_ms`). Every
                                        key is omitted when unreported. None of the three
                                        carries `parent_call_id`: they are main-loop lines
                                        about a sub-run, and the sub-run's own records are
                                        the ones stamped with it. An `agent.tool_result`
                                        entry may now carry the verb `started in
                                        background` with no `summary`, for a call claude
                                        launched without waiting on it. claude fills all of
                                        it and codex and cursor fill none, which their
                                        adapters' tests state positively. The field is now
                                        rendered (§15), and the same on-read reasoning
                                        applies: claude runs already on disk render nested.
                                        **v0 wire change (task 110, 2026-09-17):** one
                                        more record type in the shared vocabulary,
                                        `agent.patch` (`patch`, `truncated`, `call_id`,
                                        `name`, and `parent_call_id` when set): an edit's
                                        unified hunks, capped at `agent.PatchMax` runes
                                        with the cut stated. It is the body
                                        `agent.tool_result` refuses to carry, as
                                        `agent.command_output` is for a command, and it
                                        follows its outcome from the same line: a claude
                                        `user` line reporting an `Edit`, or a `Write` of
                                        type `update`, yields the result and then the
                                        patch. That result's `summary` is now `+N −M`,
                                        and a `Write` overwrite's verb is `updated`.
                                        Every key is omitted when unreported. claude
                                        fills it and codex and cursor fill none, which
                                        their adapters' tests state positively. On-read
                                        normalization means every claude run already on
                                        disk renders deltas and patches.
                                        **v0 wire change (task 124.2, 2026-09-19):** two
                                        more record types in the shared vocabulary.
                                        `agent.skill` (`name`, `args`, `by` — `human` or
                                        `agent` — `call_id`, `forked`, `error`, and
                                        `parent_call_id` when set) reports that a skill's
                                        content entered the conversation: `call_id` is
                                        the `Skill` call an agent's load came from, and
                                        pairs it with that call's `agent.tool_use` and
                                        `agent.tool_result`; `args` is one line; `forked`
                                        appears only when true. Every key is omitted when
                                        unreported, and the skill's rendered body is never
                                        on it. `agent.input_echo` (`type` alone, and
                                        `parent_call_id` when set) is a line on which the
                                        CLI echoed vincent's own stdin back; the text is
                                        in `format=raw`. Both replace an `agent.raw` the
                                        same line used to yield: claude's `isSynthetic`
                                        skill body and forked-skill kickoff, and cursor's
                                        `user` line. claude fills `agent.skill` and codex
                                        and cursor never do; only cursor writes
                                        `agent.input_echo` (§9.2, §9.3, §9.7). On-read
                                        normalization means runs already on disk — task
                                        steps as well as chat turns, because the parser is
                                        shared — lose those raw lines too.
GET    /v1/tasks/{id}/diff              unified diff of worktree vs merge-base with base branch
                                        (includes uncommitted changes)
                                        ?by=lane -> JSON {sections:[...]} instead: one section per
                                        fan_out lane, cut from the `Merge lane '{id}' of task {n}`
                                        commits on the parent's own first-parent chain (§7.6), plus
                                        one `remainder` section for the parent's own commits and
                                        uncommitted work. A task that fanned nothing out is one
                                        remainder holding the whole diff; the parameter absent is
                                        byte-for-byte the text/plain body above; any other value
                                        is a 400. Added 2026-09-03 (issue #316)

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

*Added 2026-09-17 (task 119, issue #472).* **Three new stable error codes**, the
linked-chat refusals, each a `409` whose `code` names it rather than
`invalid_state`, because a client acts on each differently:

| Code | Returned by | `details` |
|---|---|---|
| `task_locked_by_chat` | Every §6 action route on a task an open linked chat has locked, `cancel` excepted — the MCP `task_*` tools and trigger reactions included, since they replay through the same handlers — and a second `POST /v1/tasks/{id}/chat` | `chat_id`: the chat to close |
| `task_has_no_worktree` | `POST /v1/tasks/{id}/chat` on a task that never got a worktree | `state`, `action` |
| `chat_linked_to_task` | `archive` and `handoff` on a live linked chat, and `DELETE /v1/chats/{id}?delete_branch=true` on any linked chat | `task_id`: the task that owns the worktree and branch; `state`, `action` |

`agent_cannot_resume` (`400`) is unchanged and is also what
`POST /v1/tasks/{id}/chat` answers for an adapter that cannot resume.

### 13.3 Events (SSE)

Two kinds of streams:

1. **State events** — durable. Persisted to the `events` table with a monotonic id,
   emitted as SSE with `id:` set, so clients reconnect with `Last-Event-ID` and miss
   nothing. Types:
   `task.created`, `task.state_changed`, `task.priority_changed`, `task.step_advanced`,
   `task.status_changed`, `task.children_changed`, `project.*`,
   `workflow.registry_changed`, `agent.quota_changed`,
   `task.github_pull_changed`, `task.deleted`, `chat.deleted`, `task.restored`,
   `trigger.fired`, `trigger.poll_changed`, `daemon.shutting_down`.
   *Added 2026-09-13 (task 096): `trigger.fired` announces a delivery whose
   outcome is `fired`, published post-commit after its ledger row. Its payload
   is `{trigger_id, delivery_id, action}`; the event's `project_id` is the
   trigger's project, and its `task_id` is the task created or acted on. It is
   the **only** outcome that publishes. `seeded`, `deduped`, `filtered`,
   `rate_limited`, `refused` and `error` are ledger rows a client reads from
   §13.2, and an event per filtered poll would grow this table with nothing
   anyone reacts to. A task the trigger created still announces itself with its
   own `task.created`. `trigger.poll_changed` announces a trigger's poll health,
   with payload `{trigger_id, ok, error}` and no `task_id` or `project_id`. It
   is emitted on the trigger's first poll after arming and on every transition
   from ok to failing or back, never per poll (task 096 decision 24). A client
   keeps "last poll" times current by re-reading `GET /v1/triggers` on its own
   timer.*
   *Added 2026-09-09 (task 092, issue #350): `task.deleted` and `chat.deleted` —
   payload `{id, title}` — announce a permanent delete (§13.2). PR D's ruling
   that an archive needs no type of its own does **not** cover them: that type
   was redundant with `task.state_changed`, and a delete has no state to change
   to, so without a type no other client ever learns the row is gone. The event
   outlives the row it records, exactly as `project.deleted` does — the events
   table has no foreign keys. The historical rows behind the delete are
   deliberately **not** purged: their id is the Last-Event-ID cursor every
   subscriber is holding, and one archived row going is not a project's whole
   history going. A client resuming from a cursor older than a delete will see
   events for an id that now 404s, which is survivable and is what a project
   delete has always done to a stale cursor. `task.deleted` carries no
   `task_id` column — it is a foreign key, and the point of the event is that
   the task is gone — so it reaches `GET /v1/events` and not the per-task
   stream, and a chat's events never carried a chat_id column at all.*
   *Added 2026-09-17 (task 117, issue #411): `task.restored` — payload
   `{id, title}` — announces a task imported from a backup (§13.2). It is
   `task.deleted`'s mirror and, like it, not a §6 action: an import enters no
   state, so notify and triggers do not react to it. Unlike `task.deleted` it
   carries a `task_id`, because the row exists once the import commits, so it
   also reaches the per-task stream. The imported task's historical events are
   **not** copied: after a same-installation delete they are still here, and a
   copy could only be appended under new cursor ids, replaying old state
   changes to every resuming client.*
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
   `scheduler.WakeOn` is **false** for it: nothing about admission changes.
   *Amended 2026-09-16 (task 106):* still false, although an observation now
   stops agent spawns (§7.2). Recording or clearing one makes no queued task
   admissible: a held task keeps its own `admit_not_before`, and waits it out
   even when a success clears the observation early.
   *Amended 2026-09-02 (task 082, issue #310):* a **reported** reading appends
   the same event with the same four fields — `spent` from the tightest
   window's percentage, `resets_at` from that window and null when the source
   named none, `source` naming the reporter — so a subscriber cannot tell
   whether the news came from a `usage_limit` stop or from a status line, only
   what it now says. It is still emitted on change alone, which matters more
   here than it did: a status line re-renders on every prompt and pushes an
   identical reading many times a minute, and wakes nobody.
   `scheduler.WakeOn` stays **false** — a status line drawing itself must not
   spin the scheduler.)
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
   `agent.tool_result`, `agent.thinking` (T4.16), `agent.run_header` (task 066 —
   it arrives before the run's first word, so a reader who opens the pane on a
   running step sees the run's frame early rather than only once the step has
   finished; *amended 2026-09-19, task 124.2:* it is not necessarily the stream's
   first line — claude's hook lines and a forked skill's kickoff precede it),
   `agent.plan` and `agent.command_output` (task 070 — the same two records
   §13.2 adds, published as chunks with the same keys, because a client renders
   the live tail and the fetched scrollback through one path),
   `agent.subagent_started`, `agent.subagent_progress` and
   `agent.subagent_finished` (task 109, 2026-09-17 — §13.2's three records under
   the same keys, moved together per task 066 decision 5; like the records they
   carry no `parent_call_id`, and a sub-run's own chunks carry it),
   `agent.patch` (task 110, 2026-09-17 — §13.2's record under the same keys,
   published after the `agent.tool_result` chunk its line also produces, in the
   order task 070 set for command output),
   `agent.skill` (task 124.2, 2026-09-19 — §13.2's record under the same keys;
   `agent.input_echo` is a §13.2 record **never published**, like `agent.result`
   and `agent.error`, because its text is already on screen, and it is not
   `agent.raw` either, on a chat's
   stream or anywhere else),
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
`chat.created`, `chat.state_changed`, `chat.turn_changed`, `chat.archived` and
— *added 2026-09-01 (task 074)* — `chat.handed_off`, which carries
`handoff_task_id` beside the chat's id, title and state so a follower can link
the two without a fetch,
carrying the chat id and — for turn changes — the turn id, seq and state. A
turn's live output is published exactly as a step's is, with the same ~10 Hz
coalescing and the same drop-the-slow-subscriber rule, because the turn's
transcript file is the durable copy. `Last-Event-ID` resumes the durable chat
events and not the output, for the reason it does not resume a step's.

*Amended 2026-09-19 (task 124.9, issue #505).* A chat's skill list
(`GET /v1/chats/{id}/skills`, §13.2) has **no event of its own**. It is a
cached read, not a durable fact: a client refetches it on
`chat.turn_changed`, because a turn's ending is the one change the daemon
observes and it invalidates the cache before that event is recorded (§9.6),
and on an explicit `?refresh=true`.

*Amended 2026-09-17 (task 119, issue #472).* One more durable kind,
**`chat.closed`**, for a linked chat reaching `closed` — by
`POST /v1/chats/{id}/close` or by `cancel` on the task it locked, where it is
published after the transaction that also aborts the task. Every event of a
**linked** chat, `chat.created` and `chat.closed` included, carries
`linked_task_id` beside the chat's id, title and state, so a follower knows which
task's lock was placed or lifted and can re-fetch that task — whose
`open_chat_id` and `available_actions` are how the lock is learned. **No
`task.*` event** is emitted for opening or closing: the task's state does not
change, and a `task.state_changed` whose from and to are equal is what task 090
decision 1 already declined to publish. `chat.closed` is in the chat family, so
the per-chat stream carries it.

*Amended 2026-08-31 (task 067, issue #269).* There is now a **per-chat stream**,
`GET /v1/chats/{id}/events` (§13.2), the per-task stream's twin. It narrows the
durable events on the payload's `id` rather than on a column — a chat event
carries no `task_id`, deliberately, so a per-task stream can never deliver one —
and subscribes to the chat's own broker key, the negative half of the int64 key
space, so a task subscriber can never be handed a chat's bytes. Its **catch-up
seam is now exact rather than approximate**: a chunk carries `turn_id` and
`offset`, and `GET /v1/chats/{id}/turns/{seq}/transcript` reports
`X-Next-Offset`; a client fetches, then keeps every chunk whose `offset` is past
what the fetch reported, for that `turn_id`. That is the same pair a task uses,
with `turn_id` where `run_id` stands — a chat turn is its own run.

*Amended 2026-08-31 (task 071, issue #282).* A chat's live-output chunks carry
**the same normalized types and fields a task's do** — `agent.output`,
`agent.thinking`, `agent.tool_use`, `agent.tool_result`, `agent.run_header`,
`agent.usage` — with the verbatim stream line retained beside them as `raw`,
plus one type a task's stream does not publish: **`agent.raw`**, for a line
vincent's parsers do not model. A task leaves those to the transcript route,
which its output pane fetches beside a timeline of steps; a chat has no such
timeline, so a turn whose stream is entirely unmodeled lines would show nothing
at all while it runs. They are collapsed behind a count by the client below its
verbose level, never hidden by the daemon (§12.2). Until this, a chat
published one chunk type, `output`, whose only content was that raw line, so a
client had to parse the dialect itself to render anything. It is normalized in
the daemon and not in the client for the reason §13.3 exists at all:
normalization has one definition, a client that did it would need a second copy
of every adapter's parser, and a line delivered live and the same line refetched
from `GET /v1/chats/{id}/turns/{seq}/transcript` must render identically. The
mapping is shared code with the task path (`internal/agent`), which is what
makes "the same" checkable rather than aspirational. `agent.result` and
`agent.error` are **not** published live: they normalize to their own record
types in the transcript, and a chunk the refetch would contradict is worse than
no chunk — a turn's outcome reaches a client as the turn's own state.
*Amended 2026-09-17 (task 109):* the three subagent types join that list, from
the same shared mapping, so a chat's pane draws the same rail (§15).

### 13.4 Model Context Protocol (task 057)

*Added 2026-08-29 (task 057, issue #243).*

The daemon serves **MCP over streamable HTTP** on the same listener as `/v1`, so
an AI coding agent is a first-class client of the same API every other client
consumes. It is a second protocol, not a second server.

**Transport and auth are §13.1's, unchanged.** `POST /mcp` is registered in
`internal/api/server.go`'s route table beside the `/v1` routes and sits inside
the same `recover → log → auth` chain: loopback only, no TLS,
`Authorization: Bearer {token}` from `{data_dir}/token`, discovery through
`daemon.json`. There is no new listener and no new auth story. *(Amended
2026-09-17, task 062.2: true of `/mcp`; the per-step endpoint alone is also
served on a container gateway listener — see "Per-step endpoints" below.)* The §13.1 timeout
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
    DELETE /v1/tasks/{id}
    DELETE /v1/chats/{id}
    POST   /v1/chats/{id}/handoff

*(The last line added 2026-09-01, task 074, issue #288.)*

*Amended 2026-09-02 (task 082, issue #310).* `POST /v1/agents/{name}/quota`
(§13.2) joins the list, under the same line rather than as a new one: an agent
must not be able to forge a daemon-level fact about the host it runs on. A step
that could report its own adapter at 99% would paint every board and status
line in the installation with a wall that does not exist, and nothing
downstream could tell that from the real thing — the reading carries a source,
not a caller. Nothing is lost by the exclusion: the two things that push are a
status line and an app-server probe, neither of which is an agent reaching for
a tool.

Two reasons, either sufficient. A chat turn starts an agent CLI **without going
through admission** (§11), so a tool that could send one would let an agent
start unqueued agent processes — the exact thing `mcp.max_tasks` exists to
bound. And the recursion bounds walk `created_by_task_id`: a chat is not in that
chain, so making chats reachable would mean inventing depth semantics for a
non-task rather than reusing the ones that exist. An agent that needs a
conversation already has its own session; it does not need vincent to hold one
for it.

*Amended 2026-09-01 (task 074, issue #288).* `POST /v1/chats/{id}/handoff`
joins that list under the same rule rather than as an exception to it, and this
is **why the route is in the chats family at all**. A `source_chat_id` field on
`POST /v1/tasks` — the shape task 064 used for `github_pull` — was rejected
because `POST /v1/tasks` *is* the `task_create` tool: a field on it would need a
field-level MCP guard, a shape this list does not have. Handoff creates a task,
which is exactly what makes it dangerous here — the bounds walk
`created_by_task_id`, and a chat is not in that chain — so an agent that could
hand a chat off would be creating tasks outside the bound.

*Amended 2026-08-30 (task 065, issue #261).* **Fifteen**, with `POST` and
`PATCH /v1/workflows`, under the same wording task 057 decision 4 gave the
config route: an agent must not reconfigure the daemon supervising it, and a
workflow file is what that daemon runs. Nothing regresses — the
`create-workflow` built-in writes its deliverable through the filesystem, not
through this API. `GET /v1/workflows/schema` is an ordinary tool.

*Amended 2026-08-31 (task 067, issue #269).* **Seventeen**, with
`GET /v1/chats/{id}/events` and `GET /v1/chats/{id}/turns/{seq}/transcript` —
the chat family stays whole. They are listed as *exclusions* rather than under
the SSE carve-out below, even though one of them is a stream: the rule that
keeps chats off the tool surface is "a human drives a chat", not "a stream is
not a request/response", and one rule for the whole family is what stops a
later chat route being classified by which sentence it happens to match.

*Amended 2026-08-31 (task 069, issue #273).* **Eighteen**, with
`POST /v1/tasks/{id}/github/pull/create` — the one route in vincent that writes
to a forge. Decision record row 27 was amended to let a **human** push a task's
branch and open its pull request, and decision record row 11's exception is
human-initiated by construction: there is no second gate behind the keypress,
no config key and no confirmation the daemon can check, so an agent-callable
version of it would be consent nobody gave. Nothing is lost. A step's agent
already has a full-auto shell in its own worktree (§16) and can run `git push`
and `gh pr create` there — that is decision record row 11's original path and it
stays open.

*Amended 2026-09-13 (task 096 decisions 22 and 31G).* Four more: the three
trigger writes and the trigger ingress.

    POST   /v1/triggers
    PATCH  /v1/triggers/{id}
    DELETE /v1/triggers/{id}
    POST   /v1/triggers/{id}/events

The writes are excluded under task 065's wording: an agent must not author or
arm a trigger that starts agents, and enabling one is a `PATCH`. The ingress is
excluded because an agent that can inject events can start agents. The
signature it would have to forge is no reason to offer the route. The reads,
`POST /v1/triggers/validate` and both dry runs (`/test` and `/poll`) are
ordinary tools. A dry run fires nothing, and `poll` runs only a command the user
already configured, which a full-auto agent could run anyway (§16).
`PATCH /v1/config`, which switches `triggers.enabled`, was already excluded.

*Amended 2026-09-15 (task 068.4, issue #386).* Five more, **thirty-one** in
all: the pull-request writes.

    POST   /v1/tasks/{id}/github/pull/merge
    POST   /v1/tasks/{id}/github/pull/close
    POST   /v1/tasks/{id}/github/pull/reopen
    POST   /v1/tasks/{id}/github/pull/comment
    POST   /v1/tasks/{id}/github/pull/checks/rerun

They are excluded under task 069 decision 3's wording, and for one more reason.
"The keypress is the consent" holds only while a human presses it, and
`mcp.wire_steps` defaults to true, so a tool here would put these writes on the
step path — which task 068 decision 1 says nothing reaches. This supersedes task
068's plan for MCP tools whose descriptions say they write to GitHub. Nothing is
lost, as with create: a step's agent can run `gh pr merge` in its own worktree,
which is decision record row 11's original path.

*Amended 2026-09-17 (task 117, issue #411).* One more, beside the permanent
deletes it undoes:

    POST   /v1/tasks/import

It reads an arbitrary file the caller names and writes rows — ids, step runs,
provenance — that no agent should be able to create.

*Amended 2026-09-17 (task 119, issue #472).* Two more, **thirty-four** in all:
opening and closing a chat linked to a task.

    POST   /v1/tasks/{id}/chat
    POST   /v1/chats/{id}/close

The first lives under `/v1/tasks` and is still a chat route: it starts a
conversation whose turns start agent processes without admission and outside
the `created_by_task_id` chain, which is the chat family's reason above; the
second is that conversation's lifecycle. Task 063 decision 2 is **extended, not
excepted**, as task 074 extended it for `handoff`. The lock, by contrast, does
reach the tool surface: the `task_*` action tools replay through the same
handlers, so an agent acting on a locked task gets the same
`409 task_locked_by_chat` a human does.

*Amended 2026-09-19 (task 124.9, issue #505).* One more, **thirty-five** in
all:

    GET    /v1/chats/{id}/skills

It is a chat read, and it joins the family's other two reads under the one
rule rather than becoming the family's first tool (task 124 decision 6,
extending task 063 decision 2): the list is what a human's composer offers,
and an agent calling it already has a session, and skills, of its own. The
agent-level facts — whether an adapter can list or invoke at all, and its
sigil — stay on `agent_list` (§9.6).

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

*Amended 2026-09-17 (task 062.2 decision 1, issue #397).* **A containerized
agent step reaches its endpoint through the container gateway.** Inside the
container `127.0.0.1` is not the daemon, and on native Docker Engine
`host-gateway` resolves to the bridge's gateway IP (docker0's `172.17.0.1`, for
example), where a loopback listener cannot be reached. So for a containerized
agent step with `mcp.wire_steps: true` the daemon looks up the container
network's gateway IP (`docker inspect`) and binds a **second listener** on it,
on an ephemeral port. That listener serves **only** `/mcp/step/{run_id}`,
authenticated by the per-run secret; everything else, `/v1` and `/mcp`
included, is `404` there. Listeners are reference counted per gateway IP: one
lives while any containerized step needs it, closes after the last releases it,
and closes at shutdown. Where the gateway IP is not a local address — Docker
Desktop (Docker Desktop for Linux included) and runtimes like rootless podman —
the bind fails, and the daemon falls back to its loopback port, which those
runtimes forward `host.docker.internal` to. Either way the URL the adapter is
handed, cursor's `.cursor/mcp.json` included, is
`http://host.docker.internal:{port}/mcp/step/{run_id}`, `{port}` being whichever
listener serves it; a host step's URL is unchanged. Anything else on that bridge
network can reach the gateway port too, so the per-run secret is the only guard
(§16). *The alternatives beaten:* `--network=host` on Linux, which is task 061
decision 1's beaten alternative and a second code path, and refusing
`wire_steps` on native Linux, which would make a containerized step quietly
less capable. A container with no network cannot reach either listener, which
is why `network: false` with `wire_steps` is refused for a workflow with an
agent step (§12.3).

*Amended 2026-09-17 (task 062.2 decision 4).* **`vincent status` does not work
inside a container.** The image carries no vincent binary, and `127.0.0.1`
there is not the daemon. A containerized agent reports its status through the
`step_status` tool on this endpoint when steps are wired (§5.4). A host-path
`vincent statusline` hook in a mounted `~/.claude/settings.json` fails the same
way; claude tolerates a failing status line, and that run's usage-limit
observations (§9.2) are lost.

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
  base_refresh        TEXT,                   -- JSON: base fetch + local fast-forward outcome (§5.3, §10, task 099); NULL = not recorded
  priority            INTEGER NOT NULL DEFAULT 0,
  agent_override      TEXT,                   -- task-level selection (§8.6); NULL = none
  model_override      TEXT,
  effort_override     TEXT,
  restricted          INTEGER NOT NULL DEFAULT 0, -- one-way permission clamp (§9.4, task 096, migration 0028); 1 = every agent step runs restricted
  max_task_cost_usd   REAL NOT NULL DEFAULT 0,    -- this task's own cost cap (§12.3, task 096, migration 0028); 0 = no cap from this side
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
  settled_children_watermark INTEGER,         -- eager fan_out wake position (§7.6, §11, task 081, migration 0024):
                                              -- settled direct children seen when the parked admission started;
                                              -- NULL = barrier. Cleared by any transition out of awaiting_children
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
  -- Added 2026-09-03 (0026, issue #317). How many iterations the admission
  -- that wrote this row planned: the `count:`, or the resolved `for_each`
  -- list's length. Display only — never read back to decide what runs next.
  loop_total          INTEGER NOT NULL DEFAULT 0, -- 0 outside a loop, and for pre-0026 rows
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
  result_summary      TEXT,                   -- agent result text / tail of a command's stdout *and* stderr
  -- The stdout-only tail of a command attempt (§8.4, issue #311, migration
  -- 0025). `result_summary` above carries both streams and is what a human
  -- reads on the board, in the detail view and in the repair prompt, where a
  -- step that failed with a stderr-only diagnostic must not summarize as
  -- blank; this is what `.Steps.<id>.Result` renders from, because a
  -- `for_each:` (§7.6, §7.8) splitting that into items turns one incidental
  -- `Switched to branch …` into an item that is not an item.
  --
  -- NULL means none was recorded: every row written before migration 0025, and
  -- every step type that runs no command — an agent step's `.Result` is its
  -- final result text, not its output. Readers fall back to `result_summary`
  -- there, so a task already in flight over an upgrade renders as it did.
  -- Empty string is a distinct fact: a command that printed nothing on stdout
  -- has an empty `.Result`.
  stdout_tail         TEXT,                   -- NULL = none recorded; falls back to result_summary
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
  -- What this attempt was *given*, and the resolution behind it (§5.4, issue
  -- #323, migration 0027). The two columns above hold only the text a human
  -- typed at edit+retry; these hold the §8.4 render as the adapter or the shell
  -- received it — for the prompt, *after* the `<previous-attempt-failure>`
  -- block is appended (§7.2), because that is the half a re-render can never
  -- reproduce.
  --
  -- Recorded rather than re-derived on read, and the difference is not
  -- cosmetic: `config.yaml` hot-reloads (§12.3) so a timeout or a shell default
  -- can move under a row that already ran, and a task's agent/model/effort
  -- overrides are patchable (§6) so re-resolving later can name a level that
  -- had nothing to do with this attempt.
  --
  -- All of it is **display-only**, `rendered_if` included: a guard is
  -- re-evaluated every time it is reached and is never sticky (§7.7, task 015
  -- decision 10), and nothing in the engine reads any of these back to decide
  -- anything. `rendered_if` records what the guard rendered *to*, beside the
  -- raw template `result_summary` carries.
  --
  -- Written once, at render, and never updated: the input is known before the
  -- process starts and must already be on the row while the attempt is
  -- `running` and after §12.4 recovery finalizes it `interrupted`, which is
  -- exactly the attempt a human opens it for. The writer is therefore a narrow
  -- additive one, and the actor's own row update does not carry these columns —
  -- a stale struct would erase them.
  --
  -- NULL on a rendered column means no input was recorded: every row written
  -- before the migration, and every field the step type has no input for. An
  -- empty string is distinct and meaningful — a render that produced nothing.
  -- "" and 0 on the resolution columns read the same "not recorded",
  -- `loop_total`'s precedent, so a task in flight over the upgrade renders as it
  -- did. Each rendered field is bounded at 64 KiB on a rune boundary and
  -- `input_truncated` says a cut happened; nothing prunes this table (§17) and
  -- the database ships whole in `vincent daemon backup`, so the ceiling is the
  -- record's cost control rather than a display concern.
  rendered_prompt     TEXT,                   -- NULL = none recorded
  rendered_run        TEXT,                   -- NULL = none recorded
  rendered_check      TEXT,                   -- NULL = none recorded
  rendered_if         TEXT,                   -- NULL = none recorded; display only (§7.7)
  rendered_for_each   TEXT,                   -- NULL = none recorded; JSON array of the resolved items
  input_truncated     INTEGER NOT NULL DEFAULT 0, -- 1 = a recorded field lost bytes to the ceiling
  agent_source        TEXT,                   -- which §8.6 level supplied it: step|task|workflow|adapter
  model_source        TEXT,
  effort_source       TEXT,
  permission_mode     TEXT,                   -- full-auto | restricted (§16)
  timeout_ms          INTEGER NOT NULL DEFAULT 0, -- 0 = not recorded
  check_timeout_ms    INTEGER NOT NULL DEFAULT 0, -- 0 = not recorded
  shell               TEXT,                   -- the shell a command step resolved to (§8.3)
  work_dir            TEXT,                   -- the directory the process ran in
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
    state           TEXT    NOT NULL, -- §5.5: idle | running | awaiting_input | archived | handed_off | closed
    handoff_task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL, -- task 074; the one authoritative edge
    linked_task_id  INTEGER REFERENCES tasks(id) ON DELETE CASCADE,  -- task 119, migration 0032; NULL = a free chat
    opening_context TEXT,             -- task 119: prepended to a linked chat's first turn only
    agent           TEXT    NOT NULL, -- must be an adapter that can resume (§9.1)
    model           TEXT,
    effort          TEXT,
    permission_mode TEXT    NOT NULL DEFAULT 'full_auto',
    branch          TEXT    NOT NULL, -- vincent/{id}-{slug}, as a task's (§10)
    base_branch     TEXT    NOT NULL,
    base_sha        TEXT,
    base_refresh    TEXT,             -- task 099: as a task's (§5.3)
    worktree_path   TEXT,             -- the §10 claim; NULL once archived, and always empty on a linked chat
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

-- Event-trigger runtime state (task 096, added 2026-09-11; migrations 0029, 0030).
-- A trigger's definition is a file under {config_dir}/triggers/, never a row:
-- both tables key on the trigger id as text, and there is no triggers table.
-- cursor is a command's watermark string, or a GitHub trigger's snapshot JSON.
CREATE TABLE trigger_cursors (         -- one row per trigger that has polled
    trigger_id      TEXT PRIMARY KEY,
    cursor          TEXT,              -- NULL = unseeded: the next poll seeds and fires nothing
    last_poll_at    TEXT,
    last_poll_ok    INTEGER NOT NULL DEFAULT 0,
    last_poll_error TEXT NOT NULL DEFAULT '',
    last_fire_at    TEXT
);

CREATE TABLE trigger_deliveries (      -- the ledger: one row per event judged
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id         TEXT NOT NULL,
    event_id           TEXT NOT NULL DEFAULT '',
    dedupe_key         TEXT NOT NULL DEFAULT '',
    concurrency_key    TEXT NOT NULL DEFAULT '',  -- the `overrun:` group, '' when it declares none
    outcome            TEXT NOT NULL CHECK (outcome IN
                         ('fired', 'seeded', 'deduped', 'filtered', 'rate_limited',
                          'refused', 'error', 'superseded', 'queued')),
    task_id            INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    superseded_task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    detail             TEXT NOT NULL DEFAULT '', -- a `refused` row's §13.1 envelope, an `error` row's text
    created_at         TEXT NOT NULL
);
CREATE INDEX idx_trigger_deliveries_key ON trigger_deliveries(trigger_id, dedupe_key);
CREATE INDEX idx_trigger_deliveries_created ON trigger_deliveries(trigger_id, created_at);
CREATE INDEX idx_trigger_deliveries_age ON trigger_deliveries(created_at);
CREATE INDEX idx_trigger_deliveries_group ON trigger_deliveries(trigger_id, concurrency_key);

CREATE TABLE trigger_backlog (         -- events a queue mode holds until its group empties
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id      TEXT NOT NULL,
    concurrency_key TEXT NOT NULL,
    event_id        TEXT NOT NULL DEFAULT '',
    event_json      TEXT NOT NULL,     -- the raw event, re-judged in full at drain
    created_at      TEXT NOT NULL
);
CREATE INDEX idx_trigger_backlog_group ON trigger_backlog(trigger_id, concurrency_key, id);
CREATE INDEX idx_trigger_backlog_age ON trigger_backlog(created_at);

CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
```

*Added 2026-08-29 (task 057).* `tasks.created_by_task_id INTEGER REFERENCES
tasks(id) ON DELETE SET NULL`, with `idx_tasks_created_by`, records the task
whose agent step created this one over §13.4's MCP server. NULL is every task a
human, the CLI, the TUI or a `fan_out` step created. It is not `parent_task_id`
and must not be conflated with it — see §13.4 for why.

*Added 2026-09-02 (task 081, migration 0024); recorded here 2026-09-14 (issue
#378).* `tasks.settled_children_watermark INTEGER` is the wake position of a
parent parked under a `schedule: eager` `fan_out` step (§7.6, §11): how many of
its **direct** children had settled when that parked admission started. The
scheduler re-queues the parent once the live count exceeds it, so the eager
wake stays a pure SQL question. NULL means barrier. `TransitionTask` clears it
on any transition out of `awaiting_children`, so a barrier step never inherits
an earlier eager step's. No index: it is read by id.

*Added 2026-09-11 (task 096, migration 0028).* `tasks.restricted` and
`tasks.max_task_cost_usd` hold the two create-time limits (§5.3). Both default to
0, which is the "not set" value in each case — every earlier row runs its
workflow as written, under config's cap alone — so the pair composes with
`config.yaml`'s `max_task_cost_usd` without a NULL case. A task created
`paused` needs no column: it is an ordinary row whose `state` is `paused`.

*Added 2026-09-11 (task 096, migration 0029).* `trigger_cursors` and
`trigger_deliveries` hold what the daemon learns while running a trigger; the
definition stays in its file, and a table mirroring it would be a second source
of truth. The cursor and its poll status share one row because they share a
lifetime: both follow the file (task 096 decision 16). The ledger deliberately
outlives the file, so deleting a trigger and re-creating its id cannot refire an
event it already delivered, and is pruned at 30 days instead (§17).
`task_id` is `ON DELETE SET NULL`, not `CASCADE`: a `fired` row is the dedupe
record for its key, and deleting the task it created must not let the event fire
again. *Amended 2026-09-13:* the running daemon writes both tables. This
paragraph used to say none did until the rest of 096.2 landed, and that
sentence is retired.

*Added 2026-09-13 (task 096, migration 0030).* `trigger_deliveries.outcome`
gains **`seeded`**, for an event the first poll after arming was shown. It is
recorded so that a source keeping no cursor of its own does not fire its whole
backlog on the second poll (task 096 decision 31B). The dedupe lookup treats
`seeded` as delivered, exactly like `fired`, while the hourly rate limit counts
`fired` alone. SQLite cannot alter a CHECK in place, so the migration renames
the table aside, creates it again under the real name, copies every row with
its id, drops the old table and recreates the three indexes. 0029 is not
edited.

*Added 2026-09-18 (task 122, migration 0033).* `trigger_deliveries.outcome`
gains **`superseded`** — an event `overrun:` dropped in favour of work already
in flight or of a newer event — and **`queued`**, an event held in the new
`trigger_backlog` table until its group empties. Reusing `deduped` for the
first was rejected: a suppressed event is a distinct event deliberately
dropped, not a duplicate, and the ledger is the one surface an author has when
a trigger appears to have done nothing. A `queued` row is **not** delivered:
the dedupe lookup still counts `fired` and `seeded` alone, so a second
identical event arriving while one is held reaches the overrun step and is
coalesced or queued rather than swallowed. `concurrency_key` is the rendered
group the row was judged in; `superseded_task_id` is the task a
`cancel_previous` fire replaced, `ON DELETE SET NULL` like `task_id`, and the
chain reads out of the ledger rather than out of `tasks` — the board is not
asked to render a relationship only triggers ever set. The in-flight predicate
is **`!taskstate.Settled`** in §6's own vocabulary: everything but `done`,
`aborted` and `archived` holds its group, which is `paused`, `blocked`,
`awaiting_gate` and `awaiting_children` included. It is deliberately neither
"non-terminal" — §6's `Terminal` is `archived` alone — nor "holds a slot",
which would let unreviewed `propose` proposals stack up, the case the feature
exists for. The table is rebuilt the way 0030 rebuilt it; 0029 and 0030 are not
edited.

`trigger_cursors.cursor` stays opaque TEXT and carries two shapes, with no
migration. For a `type: command` trigger it is the watermark string its command
printed, or `""` for a command that prints none; the value is non-NULL either
way, which is what "seeded" means. For a GitHub trigger it is a snapshot as
JSON: `{kind, seeded_at, watermark, items}`, holding per issue or pull-request
number the state, labels, assignees, draft, merged, requested reviewers and
`updated_at`. Closed items idle for 30 days are pruned from it (decision 31D).
A GitHub trigger whose cursor is not a snapshot, because the file changed
source type while armed, seeds again. Reactions need no new column: a reaction
finds its task by `project_id` and `branch_name` among unarchived rows, newest
first, which the existing columns serve.

*Added 2026-09-17 (task 119, issue #472, migration 0032).*
`chats.linked_task_id` and `chats.opening_context` carry a chat linked to a task
(§5.5), with `chats_linked_task_idx` on `(linked_task_id, state)` where
`linked_task_id IS NOT NULL`. The link is stored on `chats` for task 074
decision 2's reason: the lock (§6) and a task's `open_chat_id` (§13.2) are this
column read backwards over that index — one lookup inside the compare-and-swap,
one query per task list — and never a second stored copy that could disagree
with it. There is no lock column on `tasks`. The foreign key is
`ON DELETE CASCADE`, not 0023's `SET NULL`: a linked chat is its task's history
rather than its origin, so permanently deleting the task (task 092) takes its
closed chats and their turns with it. A linked chat's `worktree_path` stays
empty — the task keeps the §10 claim — so no claim set changes. `chats` has no
state CHECK, so the new `closed` state needed no table rebuild.

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
of the database. *Amended 2026-09-17 (task 115):* the scheduled timer (§12.3)
takes the same copy, so this cost now **recurs once per `backup.interval`** on
an install that turns backups on, rather than only when someone runs the
command. That is why an interval above 0 and under an hour is refused: each run
holds the connection for the copy and then re-tars every transcript.

*Added 2026-09-17 (task 117, issue #411).* An **import inserts with explicit
ids** (§13.2). The task id never changes: `AUTOINCREMENT` never reissues a
deleted id, so undoing a delete always gets it back, and a live task holding it
is a refusal rather than a fallback to a fresh id — the transcript directory,
the default branch name and the kept `events` rows all carry it. Step run ids
may change, all together and in their original order, because no column
references `step_runs.id` and transcript files are named by step index and
attempt. The row is copied column by column from the staged database, which
`Open` has migrated to this binary's schema, so a column added later travels
without the import naming it. `events` rows are not copied (§13.3).

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
taskrun or scheduler ownership invariant moves. *Amended 2026-09-16 (task
106):* the row is now also **read** by `internal/taskrun` before every agent
spawn, and a still-shut `observed` row holds the task (§7.2). Still one writer,
and the scheduler still never reads it.

*Amended 2026-09-02 (task 082, issue #310).* Still no `used_percent` and no
`window` column, and now for a stronger reason than "nothing can fill them": a
**reported** reading has a writer and deliberately does not use this table. It
lives in the catalog cache (§9.6), so the paragraph above stands rather than
being reversed and this feature adds **no migration**. What narrows is the
retirement rule. "Deleted by the next successful agent step on that adapter"
applies to `source = observed` rows only — which is every row this table holds
— because a step completing proves the wall vincent watched has come down and
proves nothing whatever about a percentage a vendor reported. A reported
reading is retired by a fresher reading or by its own reset instead.

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
   availability, running/cap counts, and a needs-attention count. *Amended
   2026-09-05 (issue #324): the running count is the daemon's `slots.used`
   (§11, §13.2) — every task holding a slot, `awaiting_input` and fan-out lanes
   included — not a walk of the listed rows, which can see neither. It grows
   the clauses that explain it when they are non-zero, `3/6 running · 2 lanes ·
   1 on input`, dropping each at zero so an ordinary board is unchanged and
   shedding them last-first on a narrow panel. The needs-attention count is
   untouched: a task on a question is counted in both, being at once a slot
   holder and a person's problem.* Tasks waiting on
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
   goes to the title. *Amended 2026-08-29 (task 050): the status is no longer
   truncated with an ellipsis — it wraps, along with TITLE, STATE and STEP, per
   the row-height rule below; and "the width goes to the title" is now "the
   width is spent on the row", per the title cap below.* The status of the *newest* row, not the newest message: a
   step that spoke and finished must not have its line linger beside the next
   step, which is doing something else. The state cell is untouched — the
   recorded reasoning that keeps a hold's reason out of it (it does not fit, and
   widening a column for a rare state costs every board the columns that shed
   first) stands unchanged, and this column is why the status did not go there.

   **Column widths (task 050, added 2026-08-29).** `TITLE` is the only flexible
   column, and it takes the width the fixed set leaves — but only up to a
   ceiling. Past that ceiling the surplus is spent in a fixed order: `STEP`
   first, up to a maximum wide enough for a step name with a loop rollup beside
   it (`3/7 green · loop 4/10 · repair 2/3`, amended 2026-09-03 for issue #317
   from `3/7 green · loop 4/10`, which is the same measurement taken before the
   rollup named its body step); then `STATUS`, up to a maximum of a couple of
   the board's lines of prose; and only then does the remainder go back to the
   title. The give-back is not a softening of the ceiling — it is what stops a
   board that has shed `STATUS` leaving dead cells on the right, and the title
   passes the ceiling only once neither other column has any appetite left. The
   ceiling and the `STATUS` column's admission gate are one value, because they
   are one fact: the title has cleared a comfortable width, so there is room to
   spend elsewhere. Below that width nothing changes — the shedding ladder, the
   minimum title and every narrow board render exactly as they did. *Amended
   2026-09-03 (issue #317): a loop rollup that does not fit the `STEP` column it
   is given **drops clauses from the tail** — the body step first, then the
   `for_each` item, then the counter — rather than wrapping onto a second line.
   Three clauses outgrow every width below the ceiling, and a row grown a line
   to finish a counter spends its height on the least of what it says.* `STATE` is
   deliberately not among the columns a surplus reaches: the recorded reasoning
   above holds, and the wrap is what makes its overflow readable.

   **Row height (task 050, added 2026-08-29).** A cell too long for its column
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

   **A fan-out parent is expandable (issue #316, added 2026-09-03).** Task 014
   decision 13 — descendants are excluded from the task list — is **kept**: a
   list is the work someone asked for, and a 64-task tree buries it. What
   changes is that a `fan_out` parent's row is now a disclosure control. `L`
   hangs its lanes underneath it as indented task rows and `L` again takes them
   away, composing to `fan_out.max_depth` (§7.6). It replaces the modal drill
   `L` used to be, which could only be entered from `awaiting_children` and
   backed out to the root board however deep the reader had gone — so a
   `blocked` or `done` parent's lanes, the ones actually worth reading, were
   unreachable. The press acts in **every** state, because no list row carries a
   field saying "this task once had lanes" (§13.2 serves `children` on the
   detail endpoint only): the press asks, and a task with no lanes answers with
   none and nothing moves.

   The lanes stay out of the counts **by construction** rather than by
   filtering. They come from their own `GET /v1/tasks?parent_id=N`, one request
   per expanded parent, and never enter the board's task list — so the flat
   count, every group header's count and its `!` attention badge are computed
   from exactly the list they were computed from before, and a board with
   nothing expanded issues exactly the request it issued before and renders
   exactly the rows it rendered before. A lane row is an ordinary task row to
   everything else: folding, the bulk selection, the action keys and the column
   ladder. It is not filtered — the filter is a question about the list, and
   hiding half of an opened fan-out would make the expansion lie about what it
   opened.

   The expanded set is **session-only** and deliberately not written to
   `{data_dir}/tui.json` beside task 054's folds. A fold is a label path, which
   survives a restart meaning what it meant; a task id is not, and 054
   decision 4's rule for dropping a path whose project or workflow has left the
   board has no honest counterpart for a task archived while the TUI was down.

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
	   *Amended 2026-08-31 (task 069, issue #273): `P` no longer opens a
	   compare-URL editor and no longer only reaches a browser. It opens the
	   **pull-request form**, whose rows are the title, the body and a
	   **draft / ready** toggle, and whose `ctrl+s` calls
	   `POST /v1/tasks/{id}/github/pull/create` (§13.2) — pushing the branch,
	   opening the pull request and writing the link as `human`. `ctrl+o` is
	   the old hand-off, kept, and it is what the fallback points at when there
	   is no write credential or the create fails. So the section now carries a
	   third action that writes vincent's own column, and the "read-only
	   inspector" sense above is narrowed to `o` alone: view 7 is still the
	   only place a link is made or removed by hand, but it is no longer the
	   only place the column is written. The form states before the human
	   confirms that only committed work is pushed. This section's other two
	   states are unchanged, and the popup still has no tab strip.*
	   **Output** renders the selected attempt's live or historical transcript and
	   lets the reader move that selection with `←`/`→` (or `h`/`l`) without
	   returning to the timeline. `enter` on a Steps & Attempts row opens Output
	   on that attempt.
	   *Amended 2026-09-03 (issue #317):* a `loop`'s iterations and a multi-round
	   `fan_out`'s rounds are folded tiers on the Steps & Attempts timeline, and
	   they **open**. Latest-open stays the arrival default (task 016 decision 14
	   — a reader arriving at a blocked task wants the pass it stopped on);
	   `space` toggles the tier the cursor is in, `→` opens it, `←` closes it,
	   and `O`/`C` open and close every tier of the task — the Diff tab's fold
	   vocabulary verbatim. `enter` means both things, chosen by the row under
	   the cursor: on a folded tier it opens the fold, and otherwise it opens
	   Output as above. The timeline's selection stays a run id — `↑`/`↓` treat a
	   folded tier as **one** cursor stop, its first row, and that tier's header
	   carries the highlight while such a row is selected, so every selection
	   they can reach is drawn. They previously walked onto rows a fold had not
	   rendered, which left the highlight invisible and jumped the window to the
	   top of the timeline. The Output tab's `←`/`→` are unchanged and still
	   reach every attempt: a fold is a fact about the timeline pane, not about
	   the task.
	   *Amended 2026-09-05 (issue #322):* a `fan_out` step whose lanes are
	   running is **on** this timeline — §7.6's park opens the round's row — and
	   its `running` row is annotated with what its subtree is doing, since the
	   step itself executes none of the work. The annotation is the `children`
	   rollup §13.2 serves on the task (`2 blocked`, `3/5 done`), rendered live
	   in the same words the board's `awaiting_children (2 blocked)` uses, never
	   a count frozen into the row at spawn. The round is named beside it only
	   when no round tier above the row already names it. Everything else on the
	   row is unchanged, and no other step type is annotated.
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
   *Amended 2026-08-31 (task 068.3): a sixth tab, **Pull Request**, appended
   after Workflow and selected by `6`, for the reason Workflow was appended
   after Diff — `1`–`5` keep the tabs they had. It is the first **conditional**
   tab: it is on the strip only when the task has a live pull-request link and
   the integration is not switched off — `github.enabled: false` hides it as it
   hides the rest, while a link that cannot be *fetched* for any other reason
   keeps the tab and renders that reason on it — and with none linked `6` does
   nothing rather than landing on an empty screen. Being last is what makes its
   absence cost nothing, since no other tab's number moves; the **cycle** is the part that
   changes, and `tab`/`shift+tab` and `[`/`]` walk the strip as it currently
   stands rather than a fixed count. The tab renders the facts view 2's
   pull-request section renders, read from the same row so the two cannot
   disagree, plus one row per check on the pull request's head commit with its
   state and its own GitHub URL, from `GET /v1/tasks/{id}/github/pull/checks`.
   Those rows are **live, never snapshotted**, for the reason the pull request
   is a pointer: fetched on tab open, on `task.github_pull_changed` and on the
   tab's own poll while it is open, and never per render. `↑`/`↓`
   select a check, `enter` opens the selected check's own page, `o` opens the pull
   request, and `u` unlinks it — which **supersedes task 052 decision 6's
   placement of unlink in view 7 alone**. View 7 keeps its copy: a pull request
   no task claims has no workspace to be reached from, and that is the case
   decision 6 exists for. The task-052.6 narrowing above — "both only reach a
   browser, which is the sense in which 'read-only inspector' was written" — is
   **replaced** by this one rather than extended: `u` writes vincent's own
   `github_pull_json` column, so the Task Details tab is a read-only inspector
   and the Pull Request tab is not. Neither writes to **GitHub**; task 068
   decision 1 settled that merge, close, re-run and comment will, and 068.4 is
   where they land and where decision record row 11 is rewritten.*
   *Amended 2026-09-15 (task 068.4, issue #386): the daemon half of 068.4 has
   landed — the five write routes exist (§13.2) and row 11 is rewritten — and
   the tab's confirmed actions that call them follow in #387. Until they do, the
   tab still writes nothing to GitHub.*
   *Amended 2026-09-16 (task 068.4, issue #387): the previous note's "Until they
   do, the tab still writes nothing to GitHub" is no longer true. The tab now
   writes, human-triggered, through the §13.2 routes: `m` merges, `X` closes an
   open pull request (drafts included) or reopens a closed, unmerged one, `i`
   comments, and `ctrl+r` re-runs the failed jobs of the selected check's GitHub
   Actions run. Each confirms first, in one of three shapes: **close, reopen and
   re-run** ask inline with a y/n that names the consequence (the re-run prompt
   lists every failed row of the run, since the route takes one run id), and
   any key but `y` declines; **merge** opens a popup listing merge, squash and
   rebase with **no method preselected** — `y` does nothing until `←`/`→` picks
   one, `n`/`esc` close it, and `enter` never confirms; **comment** opens a
   popup that is its own confirmation (task 069 decision 2's reasoning) —
   `ctrl+s` posts, `esc` discards, a blank body is refused in the client. Every
   write is **absent, not present-and-refusing**, where it cannot apply — task
   068 decision 3, extended from re-run to all four, on the hint line, the
   footer, the palette and the key alike: `m` needs an open, non-draft pull
   request with a known head, `X` is absent on a merged one, `ctrl+r` needs the
   selected row to be both Actions-backed and failed, and all four are absent
   when the pull row carries a reason instead of a pull request. The merge state
   is not on the row, so `m` is not hidden on a blocked or behind pull request —
   the daemon's preflight answers with a named reason. The **head a merge is
   pinned to is the check rollup's `Ref`**, the commit whose checks the human
   was reading (decision 6), and the pull row's `head_sha` only while no rollup
   has loaded; when both are known and differ the popup says the head moved and
   `y` stays inert until a refetch makes them agree. A write in flight refuses
   its own key until it answers, the daemon publishes no event for these
   writes so the tab refetches what it changed, and a refusal is the daemon's
   message on the tab's note line. There is still no merge anywhere but this
   tab, and nothing offers `--delete-branch`, `--auto` or `--admin`.*
   *Amended 2026-09-05 (issue #323): a **Step Details** tab, selected by `6`,
   which **supersedes task 068.3's placement** above — it is inserted ahead of
   Pull Request rather than appended after it, so Pull Request answers to `7`.
   What 068.3 was protecting survives whole: the digits bind to tabs and not to
   positions, and Step Details is unconditional, so no tab's number moves when
   the pull-request tab is absent — `6` is Step Details either way, and `7` does
   nothing when nothing is linked, exactly as `6` did before it. The conditional
   tab stays **last** on the strip, so the strip's shape and the cycle are
   unchanged. What is genuinely paid is that `6` changes meaning once, for a
   reader with a linked pull request who had learned the old number; that cost
   was accepted deliberately, and it is why this is written as a supersession
   rather than a restatement.
   The tab answers "what was **this attempt** actually given", which no client
   could answer at all: it renders the selected attempt's recorded input and
   resolution (§5.4) in four groups — **Input** (the rendered prompt or script,
   the rendered `check:`, the result summary, with the appended
   `<previous-attempt-failure>` block marked as daemon-authored and the 64 KiB
   cut said out loud when the row carries the marker), **Resolution**
   (agent/model/effort each with the §8.6 level that supplied it, permission
   mode, both timeouts, the resolved shell, the working directory, and the §7.9
   `resolved_from` include chain read from the snapshot), **Control flow** (what
   the `if:` rendered to, the iteration and total, this iteration's `for_each`
   item and the resolved list, and the task's own fan-out lane id) and
   **Outcome** (tokens, cost, active duration, human wait, both exit codes,
   failure and skip reason, the edit+retry badge, the transcript path).
   A nil recorded field renders as *not recorded* rather than as an empty body:
   drawing a pre-migration attempt as though it had been handed nothing is the
   one thing this tab must not say, and an empty render is said differently.
   The layout is the Task Details inspector's — a sidebar against an
   independently scrolled content pane — but the sidebar lists **attempts**, and
   the selection is the workspace's shared attempt cursor and not a second one
   (task 049 decision 4): arriving from Output or Diff lands on the attempt
   already being read, `←`/`→` still move it, and `↑`/`↓` move it here too while
   `pgup`/`pgdn` scroll the facts. The tab is about the **open task's own**
   attempts; a lane's inputs are read by opening the lane, which `l` and `U`
   already make a short trip. Task Details' workflow-snapshot section, which
   renders each step's *un-rendered* `prompt`/`run`/`instructions`, stays exactly
   as it is — the two are the template and the substitution, and seeing both is
   the point.*
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
   `vincent task follow-up` (§12.1). *Amended 2026-09-14 (task 096):* a
   **start** row after the pickers, toggled with `enter`, sends §13.2's
   `paused: true`; with it set, `ctrl+s` records the follow-up and holds the
   task in `paused` rather than starting the run.

   The detail timeline must render a follow-up round as **its own tier**, headed
   as a round rather than numbered as a step: its rows sit at
   `step_index >= step_total` (§5.4), and numbering round 1 of a four-step
   workflow "step 5" would say the workflow grew, which it did not (§5.3). A
   round's steps share one index and are named individually beneath that header,
   the way a `parallel` group's members are.

   *Amended 2026-09-03 (issue #316): the workspace can walk a fan-out.* A
   `fan_out`'s lanes are child tasks (§7.6) and were reachable only by knowing
   their ids. Four things change, and all of them are rendering — the engine
   already names the lane in every failure it writes.

   - **`esc` pops one task.** The workspace remembers the chain it was opened
     *through*, so drilling three lanes deep is three presses back rather than
     one jump to the board. A task opened from the board arrives with an empty
     chain, and a task on it that has been archived or has vanished is
     **dropped** from the chain rather than opened.
   - **`l` opens a lane and `U` opens the parent.** `l` resolves the lane from
     where the reader is standing: a tab that carries a lane selection of its
     own — the Workflow tab's graph cursor, the Output pane's selector, the
     Diff tab's lane sections — is taken at its word, the Steps timeline
     means the `fan_out` row under the cursor, and every other tab means the
     lane the failure is about. `U` is its reciprocal — the `parent task` fact
     in the Task Details inspector becomes an action rather than a bare number.
     Both work in **every** state the parent is in. *Amended 2026-09-17 (task
     116, issue #409):* beside those facts, a task with children also shows
     `tree cost`, its own cost plus §13.2's `children.cost_usd`. On a root that
     is the figure `max_tree_cost_usd` (§12.3) is compared against; on a nested
     lane it is that lane's subtree. It renders `—`
     when neither side reported a cost, never `$0.00` (task 033 decision 5).
     The board rows are unchanged.
   - **The Output pane gains a lane selector.** `<`/`>` cycle the task's own
     output and each lane's, one at a time, and **exactly one** extra live
     subscription exists at a time: it is torn down when the selection moves and
     when the workspace leaves the task. Interleaving every lane was rejected —
     it would open 64 streams on a live-refreshing surface, and the daemon drops
     live chunks for a slow subscriber (§13.3), so a wide fan-out would render a
     lossy interleave and read as a bug. The transcript file stays the durable
     copy; this is a view, not a second store.
   - **A failed join names the lane.** A parent parked on `lane_failed`,
     `merge_conflict`, `fan_out_invalid` or `fan_out_limit` (§18) carries the
     engine's own sentence — `lane "api" (task 42) is blocked, not done`, the
     conflicted paths, the offending line or bound — on the detail header *and*
     on the `fan_out` step row, with the lane's own state and block reason
     beside it, which is the one fact the engine's message cannot carry. Only
     the attempt the task is parked on is annotated; an earlier retried one is
     history. The **Diff** tab groups `lane > file` over task 012's file
     grouping, one section per lane in merge order plus a remainder for the
     parent's own work, from `?by=lane` (§13.2); a task that fanned nothing out
     is the flat file list it was, fold state keyed by path and all. The
     **Pull Request** tab grows one row per lane beneath the
     parent's own section — branch, linked pull request and its state, from one
     project listing. Lane rows carry **no checks**: checks stay one call for
     one task, and `l` opens the lane, whose own tab has them.

   *Amended 2026-09-05 (task 089, issue #330).* The **Output** tab's border
   title carries the view 9 in-progress indicator while the attempt it is
   showing is still live — beside the tab strip, the level and the follow state,
   which is where this workspace already keeps its live state and the one spot
   visible at every scroll position. It draws on Output only, never on Diff, and
   the gate is that the attempt is **live** rather than that its step is an
   `agent` step: a long `command` step's pane is silent for exactly the same
   reason and for exactly as long. A workspace whose displayed attempt has
   finished arms no repaint.

   *Amended 2026-09-17 (task 119, issue #472).* **A stopped task can be talked
   to from here.** A task-action binding, `T` ("talk"; Keys, below) — the
   `chat` operation's default, which `tui.keys` may move (§12.3) — is offered when the
   daemon lists `chat` in `available_actions`, and opens view 9 on a new chat
   linked to the task; on a task whose `open_chat_id` is set — where
   `available_actions` has withdrawn `chat` — the same key opens **that** chat
   rather than trying a second one the daemon would refuse. While the task is
   locked the action bar offers what the daemon offers, which is `cancel` or
   nothing. The workspace lists the task's linked chats, closed ones included
   (`GET /v1/chats?task_id=&archived=all`, §13.2), as the history of the
   conversations held about it.

3. **New task.** Project picker → workflow picker (shows description + step list;
   flags steps whose agent is unavailable) → *(GitHub issue, conditional)* →
   title → description (inline or
   `$EDITOR`) → fields → base branch (default prefilled) →
   priority → start (now or paused) → optional agent/model/effort override (pickers fed by
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
   **Start row (task 096, added 2026-09-11):** the Git & priority stage gains
   a `start` row after priority. `enter` toggles it between "when a slot is
   free" and `paused`, which sends `paused: true` (§13.2) so the task waits on
   the board until a human resumes it — a draft without starting an agent.
   Review lists it beside the rest of the request. `restricted` and
   `max_task_cost_usd` have no row: the form offers the held create alone.
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
   *Amended 2026-09-05 (issue #324): the `running / cap` column and the rail's
   workload line render the project row's own `slots_used` (§13.2), so the
   numerator is counted the way the scheduler applies that cap — lanes and
   `awaiting_input` included — rather than from the root-only task list the
   view also holds.*
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

   `e` keeps its one meaning: it means `$EDITOR` in every context
   `internal/tui/bindings.go` gives the bare key to, and taking it for the
   structured editor would give one key two meanings depending on the view.
   *Amended 2026-09-10 (task 093, issue #353): "all seven contexts" was not
   true when it was written and is narrowed here rather than repeated. It is
   `$EDITOR` in the eight contexts that bind `e` itself; the two `enter` rows
   that name it as an alias — the projects view's inline edit and the daemon
   view's config editor — are the stated exception, and the third meaning it
   had, "type your own answer", moved to `t`.* The forms
   are rendered from `GET /v1/workflows/schema` (§8.2 as data), not from a
   second copy of §8.2 in the client — PR L recorded that re-deriving the
   daemon's checks in the TUI is how the two drift. There is **no delete**:
   the view gains no destructive action. A file the forms cannot load is what
   `e` is still there for.

   *Amended 2026-09-03 (tasks 086 and 087, issue #320).* **The structured
   editor reads every block of the file and writes every block it reads.** As
   first built it did neither: its value column was a hand-written switch that
   rendered a dozen published fields — `timeout:`, `max_retries:`,
   `retry_backoff:`, `env:`, `max_parallel:`, `count:`, `for_each:`,
   `max_iterations:`, `schedule:` among them — as `(unset)` however the file
   spelled them, its `lanes:`, `lane:`, `merge:`, `fields:` and `defaults:`
   rows led nowhere, and committing a `prompt:` or a `run:` rewrote the block
   scalar as a single line. One sentence above is therefore narrowed and the
   key table grows; everything else in this view stands:

   - **"No delete" means no *unconfirmed destructive action*, not that the
     editor may never remove anything.** `d` removes the step, lane or
     declared field under the cursor **after a confirmation**; `set`,
     `insert` and `move` still commit on `enter` with no prompt, because
     each of those leaves the removed thing recoverable in the file's own
     history and a removal does not. Removing the **workflow file** is still
     not offered: that is what the sentence was written to refuse, and it
     keeps refusing it.
   - **The editor's key table gains four rows,** all inside its own context so
     the list's `a`/`i`/`f` are untouched: `a` adds an entry after the one
     under the cursor — on a steps list it first asks which type, offering
     only the types the served descriptor marks legal for that path, and then
     writes a skeleton carrying that type's required fields, so the file is
     valid between operations; `d` removes, per the paragraph above; `K` and
     `J` move the entry up and down, capitalised because `k` and `j` still
     move the cursor and a file rewritten by a typo is not a trade worth
     making.
   - **A multi-line value is edited in a multi-line pane.** A `prompt:`,
     `run:` or `instructions:` row opens a full-pane editing surface where
     `enter` inserts a newline, `ctrl+s` saves as a `|` block scalar and `esc`
     abandons; opening one and closing it again writes nothing. The one-line
     field's newline-flattening is deliberate everywhere else it is used and
     is unchanged.

   Unamended: `e` still means `$EDITOR`; the forms are still rendered from the
   served descriptor and never from a second copy of §8.2 — the agent, model
   and effort pickers read `GET /v1/agents`, which is where those sets live
   (they are deliberately not in the descriptor); every write is still one
   operation and one PATCH carrying the version the last read handed back;
   editing from the graph is still out, and the graph stays a read-only
   projection.
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
   **A backup row (task 115, added 2026-09-17):** the `backup` group of the same
   `GET /v1/doctor?probe=false` report (§13.2): whether scheduled backups are on
   and, when they are, how they are going, with the last attempt's error when it
   failed. That error is the one `vincent doctor` counts as a problem (§17), so
   the view shows it rather than leaving it to the command. Like the database
   block it reports and offers nothing to press. `vincent daemon backup` takes a
   manual archive, and the schedule is edited in the view's configuration block.
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
   agents already run full-auto by default (§16). *Amended 2026-09-14 (task
   096):* `triggers.enabled` is a fifth, because turning it on lets a third
   party start agents (§12.3). *Amended 2026-09-17 (task 115):* `backup.dir` is
   a sixth, because a scheduled archive holds `config.yaml` (with
   `environment.set` values and `notify.command`) and every transcript, and a
   synced or shared folder there decides what the daemon exposes.
   `backup.interval` and `backup.keep` are not. `listen` is written and does
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
   depending on the row is worse than one that sometimes does nothing. A create
   key seeds a new task from the row — `a`, per the key vocabulary below;
   *amended 2026-09-10 (task 093, issue #353): it was `c`, which is cancel
   everywhere else in the TUI.* A link key
   opens a task picker **scoped to the row's own project**: `POST
   /v1/tasks/{id}/github/pull` takes a bare number and the daemon resolves the
   repository from the task's project, so offering a task from elsewhere would
   link that project's repository to a number that means something else there. An
   unlink key asks first, and the confirmation says the refusal is sticky and the
   reconciler will not re-apply it — which is what `DELETE` does, and a UI that
   said "clear" would be lying. *Amended 2026-08-31 (task 068.3): unlink is no
   longer only here. The task workspace's Pull Request tab (view 2) carries its
   own `u`, with the same effect and the same sticky suppression. This view
   keeps its copy because the case task 052 decision 6 put it here for — a pull
   request no task claims — has no task workspace to be reached from at all.* `R` re-lists, `/` filters, `↑`/`↓` move.
   The view subscribes to nothing of its own: the root broadcasts every event to
   every view, so a `task.github_pull_changed` tick re-renders it with no
   keypress.

8. **Chats board.** *Added 2026-08-31 (task 067, issue #269, closing 063.2).* A
   second board, not a filter on the first: every chat from `GET /v1/chats`,
   grouped by project, one row per conversation carrying id, state, agent, last
   activity and title. It shares none of view 1's columns because a chat has
   none of them — no workflow, no step `k/n`, no §6 action set — and decision
   row 29's "a chat never appears on the task board" is what makes the second
   board the right answer rather than a `kind` column on the first.

   Grouping is **by project and by nothing else**: `tui.board.group_by`'s
   workflow levels are meaningless for a chat, so the `g` cycle is not offered
   here. Folds persist in `{data_dir}/tui.json` under their own key, beside the
   task board's rather than sharing them — the two boards group by the same
   project names, and one list would make folding a project on one fold it on
   the other.

   **Attention is this board's own.** An `awaiting_input` chat is sorted to the
   top and counted in *this* header's badge. `!` and view 1's needs-attention
   count stay task-only, which is what keeps row 29 literal in the client. The
   `notify:` hook (§12.3) is untouched in both directions: it is the daemon's
   outward signal and this is a client's own ordering.

   **Actions.** `enter` opens view 9. `n` starts a chat — the create form is a
   layer over this board, taking project, title, agent, model, effort and base
   branch, and rendering `400 agent_cannot_resume` as the typed refusal it is
   rather than a generic failure. `A` archives, asking first and re-offering
   with the force when the worktree is dirty. `/` filters on title, agent or
   branch; `←`/`→` fold a project group; `R` re-lists. A `chat.*` event
   re-renders the board with no keypress, and a `task.*` event does not — the
   separation runs both ways.

   *Amended 2026-09-01 (issue #298).* **Terminal chats are off this board by
   default**, and there is a key back to them. The board lists
   `GET /v1/chats?archived=false` (§13.2) rather than everything, so an
   `archived` or `handed_off` chat leaves it the way an archived task leaves
   view 1; `s` cycles the listing — live, then archived and handed-off, then
   both — the way view 7's `s` cycles a pull-request listing (task 064
   decision 9), and the header names the listing whenever it is not the
   default, so an empty board is never mistaken for "no chats". Hiding them
   with no route back was rejected: an archived chat's transcript is still
   worth reading. The terminal band survives, because it still orders the rows
   the toggle brings back.

   Two consequences of a chat being done with, on the same board. The
   **last-activity cell stops**: for a terminal chat it renders *when* the chat
   ended — an absolute stamp from `updated_at`, which is the moment of the last
   write a chat row can take — instead of a duration that ticks up second by
   second and reads exactly like a live conversation. No column is added to
   `chats` for it and task 074 decision 6 is not reopened. And **`a` declines
   on a terminal row** with a note naming the state, instead of opening the
   "archive %q and remove its worktree? (y/n)" prompt: there is no worktree
   left to remove, or — after a handoff — the task owns it, and the daemon's
   own refusal (§13.2) says the same thing from the other side.

   *Amended 2026-09-05 (task 089, issue #330).* A `running` row's state cell
   carries the view 9 in-progress indicator's moving frame **beside** the
   `running` label, never instead of it: the cell is a fixed-width column
   rendered through this board's shared state vocabulary, and swapping one
   state's word for a glyph would break that vocabulary for that state alone.
   The repaint it drives is also what makes the **last-activity cell advance**
   while a chat is running — that cell already rendered `now - updated_at`, and
   `updated_at` is written only on a state change, so for a running chat it was
   already time-since-the-turn-started and was simply never redrawn. No column,
   DTO or endpoint changes. `idle`, `awaiting_input`, `archived` and
   `handed_off` rows do not animate, `awaiting_input` for view 9's reason — it
   is waiting on a human, not working, and this header already badges it — and a
   board with nothing running arms no repaint.

   *Amended 2026-09-17 (task 114, issue #405).* **The wheel moves the cursor**,
   one chat per tick, skipping the project headings exactly as `↑`/`↓` do — the
   home board's wheel on the board where it had been missing. This board is its
   only scrollable panel, so it is always the focused one and the Mouse rule
   below needs no exception. A layer that owns the keyboard owns the wheel too
   (task 078 decision 3): the new-chat form and the archive confirmation hold
   the cursor still, because each is about the row under it. An open filter
   does not; it narrows the rows the wheel walks. Clicking a row is not added.

   *Amended 2026-09-17 (task 119, issue #472).* A chat **linked to a task**
   (§5.5) is a row here like any other, grouped under its project, and the row
   shows the task it works in, so a conversation that is holding a task locked
   can be found from this board as well as from the task. A `closed` chat is
   terminal and leaves the default listing with `archived` and `handed_off`
   ones (§13.2).

9. **Chat workspace.** *Added 2026-08-31 (task 067, closing 063.2 and 063.3).*
   One conversation: the finished turns above, the running turn's live tail
   below them, and a composer at the bottom. `enter` sends, `ctrl+x` stops the
   live turn, `esc` returns to view 8.

   *Amended 2026-08-31 (task 071, issue #282).* Seven keys, not three:
   `enter` sends, `ctrl+x` stops the live turn, **`ctrl+r` cycles the
   verbosity level**, **`pgup`/`pgdown` scroll the conversation**, **`ctrl+g`
   jumps to the live end and re-arms follow**, and `esc` returns to view 8.
   None of the four new ones is `v`, `f` or `G`: `capturesInput` is true
   whenever the composer holds the keyboard, which is nearly always, so a
   letter would be typed into the draft. The composer keeps `↑`/`↓` for
   editing a multi-line message.

   *Amended 2026-09-19 (issue #500).* `ctrl+j`, `shift+enter` and `alt+enter`
   insert a newline in the draft; `enter` still sends it. This makes the
   multi-line editing above reachable without pasting. `ctrl+j` is the one the
   registry row, the footer and the placeholder name, because a legacy
   terminal can always send it: there `shift+enter` arrives as a bare `enter`,
   which sends.

   The conversation body is **the output pane's line model** at the level
   below, not a renderer of its own: every turn's records go through the same
   two-column gutter scheme, so an `agent.tool_use` reads the same in a chat as
   in a task, and `agent.tool_result`, `agent.run_header` and the result line
   are reachable here at the levels this section already specifies for each.
   The body scrolls and follows the way the output pane does — following means
   showing the end, a manual scroll pauses it, `ctrl+g` re-arms it — because at
   verbose it grows several-fold and a bottom-anchored window with no way to
   scroll would put what was just revealed out of reach.

   The live tail consumes `GET /v1/chats/{id}/events` and seams onto
   `GET /v1/chats/{id}/turns/{seq}/transcript` exactly the way the task
   workspace's output tab seams onto a step's: fetch, then keep every chunk
   whose `offset` is past the fetch's `X-Next-Offset`, for that `turn_id`. A
   finished turn renders from its transcript and falls back to `ResultText`
   when the file has gone to retention (§17). *Amended 2026-08-31 (task 071):*
   the client now actually fetches those transcripts — the five newest finished
   turns when the chat opens, the rest as they are scrolled to — and a total
   record cap bounds a long conversation the way `maxRecords` bounds a step's
   pane, with a one-line marker where records were dropped.

   The §7.4 popup is **the same popup** view 2 uses, not a fork of it:
   structured options, multi-select and the permission allow/deny distinction
   come along because the request is the same request, and only where the
   answer is POSTed differs. It opens and closes on the chat's own state, so an
   answer given from `vincent chat answer` or from curl closes it here too. A
   `409 chat_cap_reached` on send renders as a refusal — never a queued turn,
   never a spinner — because that is what the daemon did.

   *Amended 2026-09-01 (issue #300).* The conversation body takes the **mouse
   wheel** as well as `pgup`/`pgdown`: one line per tick, a scroll back pausing
   follow and `ctrl+g` re-arming it, and the lazy transcript fetch running on a
   wheel scroll exactly as it does on a paging key. It is not scoped to the
   pointer's position — the workspace has one scrollable pane, so this section's
   Mouse rule ("the focused panel", not the hovered one) already names it — and
   the §7.4 popup takes the wheel out of the conversation behind it, the way it
   already takes clicks.

   *Amended 2026-09-03 (issue #321).* `ctrl+r` cycles **four** levels here, as
   `v` does there — `quiet → compact → normal → verbose` — and `quiet` is the
   level this view exists to have: a chat read as a conversation, with the tool
   calls, the success outcome line and the unrecognized-line count gone and the
   prose left. It is not a chat dialect of `compact`: the task workspace's
   output pane hides exactly the same records at exactly the same level, because
   the whole point of task 071's amendment above is that the two panes are one
   vocabulary. Nothing else in that amendment moves — the level is still one
   shared session value, and the header still names it when it is not `normal`.

   Two pieces of chrome are this view's own, and are what "the output pane's
   line model" was never a claim about. The **human's half of a turn is a
   right-aligned bubble**: shrink-to-fit, capped at about two thirds of the
   pane, wrapping inside itself, with the `›` marker on every line of it and an
   accent foreground rather than a background band — this section's Colour rule
   requires the palette to degrade under `NO_COLOR` and at 16 colours, and right
   alignment plus a per-line marker are what survive that. The prompt's own line
   breaks survive into the bubble and there is no line cap, for the reason task
   073 decision 5 dropped the §17 fallback's: a truncated prompt puts the tail of
   what a human asked out of reach on a body that scrolls. The agent's half is
   untouched — flush left, full width, the output pane's renderer verbatim — and
   a §17 retained-away turn still renders its `ResultText` as assistant prose.
   The **composer wears a titled border**, drawn with this section's one
   box-drawing routine, so the field reads as a field rather than as one more
   row of the conversation. The border is spent out of the height budget, not
   added after the join: the #299 amendment below governs it verbatim — a
   composer that grew is a body that shrank — and the footer still carries one
   element per *rendered* line, the border's two rows included.

   *Amended 2026-09-05 (task 089, issue #330).* A running turn carries an
   **in-progress indicator** — one moving glyph and an elapsed clock,
   `⠋ working… 14s` — for the **whole** time it is in `running`, not only until
   its first chunk arrives. It sits in the footer, above the composer and beside
   the note line, and not inline at the end of the turn's body: the body
   scrolls, and a reader who has scrolled up is exactly the reader who needs to
   be told that waiting is still the right thing to do. The frame advances every
   120 ms; the clock is derived from the client's own clock on each render and
   is spelled in this section's one duration vocabulary (`14s`, `2m03s`,
   `1h04m`). The elapsed half is load-bearing on its own: a number that advances
   is proof of life in a screenshot or a scrollback, where an animation is not.
   It is gone the moment the turn reaches `done`, `failed`, `interrupted` or
   `awaiting_input` — an `awaiting_input` turn is waiting on the human, not
   working, and the header already says `waiting on you` — and a chat with no
   running turn arms **no** repaint at all.

   This does **not** contradict the "never a spinner" rule four paragraphs
   above, and the two are never in force for the same turn. That rule is about a
   send the daemon **refused**: a `409 chat_cap_reached` produces no turn, and a
   client must not draw a pending anything for work that will never happen. This
   indicator draws only for a turn the daemon is holding in `running` — state
   the daemon does have and has told the client about.

   *Amended 2026-09-17 (task 119, issue #472).* On a chat **linked to a task**
   the workspace offers **close** on `ctrl+q` (`POST /v1/chats/{id}/close`) —
   a control key because the composer takes every printable one: closing ends
   the conversation and lifts the task's lock, and never touches the worktree
   or the branch. Hand-off and archive decline with the daemon's own
   `chat_linked_to_task` reason — the worktree is the task's — rather than
   opening the new-task form or the "remove its worktree?" prompt. The task's
   context is prepended by the daemon to the first turn's prompt; the turn row
   keeps the human's message as typed.

10. **Archived boards.** *Added 2026-09-09 (task 092, issue #350).* Two
   screens — archived tasks and archived chats — reached from the command
   palette with **no key of their own**: task 049 retired `1..6` and the point
   was to stop adding memorized keys, so these get two `nav` palette rows, the
   way chats did (task 067). `s` is skip and `A` is archive; neither was ever
   free.

   Each is the board it mirrors **in a second mode**, not a second model: the
   same grouping, folding, `/` filter and `space`/`V` selection, listing what is
   archived instead of what is live. What the mode adds is the three things only
   an archive has — `s` cycles a date window (7 days / 30 days / all, resolved
   client-side into §13.2's `archived_since`), `<`/`>` turn pages of a hundred
   rows, and `D` deletes permanently. *Amended 2026-09-10 (task 093, issue
   #353): the window was `d`, which is the destructive letter's lower case and
   sat one key from the permanent delete on the one screen where confusing the
   two costs the most. `s` already cycles what a list is showing on the chats
   board and the pull-request list, and a date window is that gesture on a
   third list.* `D` is a `scopePanel` binding on these two
   contexts and nowhere else, because delete is not a §6 action (§6) and every
   action key is gated on `available_actions`.

   The confirmation takes **three** answers: `y` deletes the row and its
   transcripts, `b` deletes its branch as well, `n` does nothing, and no other
   key answers it. The third answer cannot destroy anything `y` would have kept
   — §10's rule keeps a branch carrying commits whichever was pressed. A
   selection deletes one row at a time and reports done / refused, the shape
   bulk archive already has (task 011): there is no bulk endpoint.

   `enter` opens the row's existing workspace, which is **read-only for free**:
   an archived task offers no `available_actions`, so there is nothing to
   withhold and no flag saying so.

   *Amended 2026-09-17 (task 114, issue #405).* **Both boards take the wheel**:
   one row per tick, headings skipped, as on the live boards they mirror. The
   delete confirmation holds the cursor still, because it names the rows it
   would delete. The wheel never turns a page — paging is a fetch and stays on
   `<`/`>`.

11. **Triggers.** *Added 2026-09-13 (task 096.6, issue #362).* A takeover
   reached from the command palette, like Workflows and Projects, with no key
   of its own (task 049). It shows `{config_dir}/triggers/` and what the daemon
   learned running each file. It is deliberately not a section of the projects
   view, which would suggest the project scope that task 096 decision 8 ruled
   out.

   The **list** shows every file the registry holds, broken ones included with
   their findings. Each row gives the id, the source and action type, the
   project, whether the trigger is armed and why not, its poll health and its
   last fire. While `triggers.enabled` is off, a **banner** above the list says
   so, because every row then reads disarmed for a reason no row can fix.

   *Amended 2026-09-18 (task 121, issue #480).* The poll cell reports what the
   source actually has: `push` for `type: http`, `clock` for `type: schedule`,
   and poll health for the three that poll. A schedule never polls, so without
   its own cell it would read "not yet" forever. The armed hint beneath the
   list is worded per source too — for a schedule, that the next tick anchors
   its clock and fires nothing.

   **Create and edit** use task 065's form, rendered from
   `GET /v1/triggers/schema` rather than from a client copy of the rules.
   - `a` creates: the daemon renders a disabled starter.
   - `i` or `enter` opens the form on a file.
   - `e` hands the file to `$EDITOR`.
   - `space` toggles `enabled`.
   - `D` deletes after asking, and says that the ledger is kept.
   - `R` re-reads, and `/` filters.

   Edits travel as ops carrying the version token, so comments survive, and a
   second writer is a 409 the form reports. A refused value stays on its field
   with the daemon's message. Committing any value the schema marks **dangerous**
   (`enabled: true`, `on_fire: create` or `permission: workflow`) asks first,
   with the schema's warning, whether it comes from the form or from `space`
   (task 096 decision 19). Disabling never asks.

   `tab` moves to the selected trigger's **ledger**: its recent deliveries, with
   outcome, event id, task, detail and time, `seeded` among them. There,
   `enter` opens a delivery's task in its workspace. This is the daemon view's
   list/log split (view 6).

   Both **dry runs** are here. One judges a sample event the user supplies
   against the selected trigger. It shows whether the event matched and which
   key missed, what `if:` rendered, the dedupe key and whether the ledger would
   dedupe it, and the request it would replay. The other runs the source once,
   live, and shows the same for each event returned. Neither writes anything,
   and both work on a disarmed trigger. The sample event is **session memory
   only** and is never written to `tui.json`, because a vendor payload may
   carry sensitive text (decision 27). The keys for both, like every key here,
   are registered in `internal/tui/bindings.go`.

   The daemon view's config editor lists `triggers.enabled` with task 060's
   `dangerous` flag, so saving it asks first as well.

### Layout

The list above is also the screen contract. View 1 is the board-only home
screen. `enter` on its selected row opens view 2, the full-screen task workspace;
`esc` returns. The workspace's five tabs each take its whole body (four until
task 051, 2026-08-29, added the Workflow tab). *Amended 2026-08-31 (task
068.3): six on a task whose pull request is linked, five on one whose is not.
The Pull Request tab is conditional, so the strip's length is a property of the
task rather than a constant of the view, and the tab cycle walks the strip as it
stands rather than a fixed count.*
*Amended 2026-09-05 (issue #323): **seven** on a task whose pull request is
linked, six on one whose is not — Step Details is unconditional and takes `6`,
and the conditional tab stays last and takes `7`. Everything the sentence above
says about the strip's length being a property of the task, and about the cycle
walking the strip as it stands, is unchanged.* Views 3–7 are
full-screen takeovers reached from the command palette (view 7 added
2026-08-29 by task 052.6).

*Amended 2026-08-31 (task 067).* Views **8 and 9** join it as a second
board-and-workspace pair, with the same relationship to each other that views 1
and 2 have: view 8 is a keyless nav row in the palette, `enter` on a row opens
view 9, `esc` returns. The chats board keeps `n` for itself — on it, `n` starts
a chat; everywhere else it still opens the new-task form.

*Amended 2026-08-30 (task 064).* View 7 gains two keys. **`a`** opens the
new-task form seeded with the selected pull request *(amended 2026-09-10, task
093, issue #353: it was `c`, which is cancel everywhere else — creating is `a`
per §15's key vocabulary)*: the daemon computes the
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

*Amended 2026-08-31 (task 069, issue #273).* View 7 gains **`P`**, and it is not
a key on the selected row. This screen has no task rows and is deliberately not
given any — its question is "what is open across everything I run", and a task
with no pull request is not an open pull request — so `P` opens a **picker of
tasks**, mirroring `l`'s project-scoped link picker: every task on the screen's
GitHub projects that has a branch and no live link. Choosing one opens that
task's workspace with the form up. That widens 052 decision 6's "the offer to
create is in the workspace" rather than reversing it: the workspace is still
where the form lives, and the takeover reaches it through a picker rather than
by growing a second row type or a second copy of the form.

Eligibility is branch-and-no-live-link and **nothing more** (task 069 decision
4). A task whose push will be refused, and a task 064 task whose branch is
somebody else's head, are both offered and told by the push or the create
failing with a named reason. That last case has teeth and is recorded as a
choice rather than left to be discovered: a fork branch has no upstream by
design, so `git push -u origin` on one either fails or lands a copy of a
contributor's branch in the user's own repository. It is never a force-push and
destroys nothing.

**Text fields wrap (added 2026-09-01, issue #299).** A field being typed into
is bound by the same rule the boards and the rendered Markdown already carry: a
value too long for the pane it is in **wraps onto further rows of that pane**.
It is never drawn past the right edge, and it is never cut with an ellipsis —
truncating a field puts the tail of the value *and the cursor* out of reach,
which is worse than it is on any read-only surface. The field's height is
therefore a property of its value, and the form hosting it recomputes its line
budget from the rows the field actually drew rather than assuming one. That
holds for the multi-line chat composer of view 9 as much as for the single-value
rows: a view is given a width and a height and draws inside both of them, so a
composer that grew is a body that shrank, not a frame that overflowed.

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

*Amended 2026-09-02 (task 082, issue #310).* Where a reading is **reported**
(§9.6) the same surfaces show it, with the provenance rule unchanged — `→` for
a reset the source stated, `≈` for one vincent computed, and **no time at all**
for a window that named none, since `≈ 00:00` is a time nobody is waiting for.
The daemon view's agents panel is where the whole reading is legible, because
it is the panel whose job is to list every fact: the source in prose rather
than as the wire identifier, then one part per window
(`quota codex app-server · 5h 28% → 13:00 · 7d 53% → 11:00 · read 09:14`),
then the time the reading was taken — a percentage with no timestamp invites
reading a stale figure as a live one. `quota unknown` is unchanged for an
adapter with neither a reading nor an observation, which is every state cursor
has (§9.7). The board header's badge is unchanged in form and answers the same
question — is this adapter's window shut *now* — computed per §9.6's `source`
split.

**`i` in the daemon view (task 082).** The one key in vincent that leads to a
write outside its own data dir — *amended 2026-09-10 (task 095): the first of
two, `S` below being the other* — it offers to make `vincent statusline` Claude
Code's `statusLine.command`, and the same key takes it back out. The exact JSON
that will be written is shown first, as a takeover rather than a line competing
with four other blocks — §16 asks that it be on screen, and a preview nobody
can read is not. The displaced `statusLine` object is carried in the installed
command, so removal restores it verbatim, including restoring its absence. The
offer appears only when claude is installed here and its settings do not
already run vincent, and a decline is remembered in `tui.json`
(`status_line_declined`) so it does not come back — a question asked every time
this view opens is one somebody answers by not reading it. Once installed, the
line says so, which is the only place the removal is discoverable.

**`S` in the daemon view (task 095, added 2026-09-10).** The second key that
leads outside vincent's own directories, and the second built on the shape
above: a line under the adapters says how many of the skills of §9.8 are not
installed, `S` opens a takeover listing them with the **exact `npx` command**
each install runs, `enter` runs it, `n` is remembered in `tui.json`
(`skills_declined`) and nothing re-asks while it is set. Once everything is
current the line still says so, which is where `S` stays discoverable — the
same rule the status-line line follows.

It is `S` and not `i`. Both keys lead to a write outside vincent's directories,
but they are two different operations on two different targets, and §15's key
vocabulary lets a key be shared only where it means the same operation. `s` was
unavailable: it is the vocabulary's "cycle a listing's scope".

Two things differ from the status-line flow, and both come from what is being
run. The state it reports comes off the `GET /v1/doctor` report this view
already fetches rather than from a local read, so the TUI still holds no state
the daemon does not — only the decline flag is local, exactly as the
status-line flow already splits it. And the write is a subprocess that
downloads a package rather than a file rewrite, so it runs off the event loop
with the screen saying what is running: a TUI frozen on a network install is a
TUI that cannot be quit.

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
  *Amended 2026-08-29 (task 050): the freed width is spent on the row in the
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

*Amended 2026-08-31 (task 066).* Two lines join the scheme, and one gutter mark:

- **`# ` — the run header.** `agent.run_header` renders the working directory
  the CLI reported and the tools it was given, dim throughout, gutter included:
  it is context for everything below it rather than a thing that happened, and
  it must not compete with the first assistant line for a reader's eye. Its tool
  list wraps to the hanging indent like any other record. It appears at
  **`normal` and `verbose`**, never at `compact` — that level's stated meaning
  is "what the agent said and did, nothing else", and the run's frame is
  neither.
- **`⊘ ` — a blocked tool call.** A call a permission rule refused never ran,
  and that sends a reader somewhere different from a call that ran and failed:
  one is the agent's problem, the other is the step's permission mode. It is
  marked apart from `✗ ` at every level, which is a correction to what the
  record *means* rather than growth in what compact shows. A structured outcome
  verb, where the dialect reports one, leads the tool's own prose about it:
  `✓ created · File created successfully at: hello.txt`.
- **The result line grows by level.** `compact` is byte for byte what it always
  rendered (`done`, or `done · $0.02`). From `normal` up it adds the run's own
  account of itself — elapsed, turns, an *unusual* stop or terminal reason, and
  a count of permission denials. The ordinary reasons are never printed: every
  successful claude run ends `end_turn`/`completed`, so naming them would spend
  columns saying nothing, and the whole point of the field is to distinguish
  "the model finished" from "it hit a limit". `verbose` adds the API-time split
  and, on wrapped continuation lines of the same record, the cache read/write
  split and the per-model breakdown. *Amended 2026-09-03 (issue #321):* the
  line does not shrink below compact — at `quiet` it is **absent**. Only the
  success outcome is: the `✗ ` error form and the fallback that prints the
  result *text* when the attempt rendered nothing else both still render at
  `quiet`, because those two are meanings of the record rather than level
  rules, and a level that erased them would hide a failure outright or leave a
  turn's separator with nothing under it.

*Amended 2026-08-31 (task 070).* One more gutter mark, and one record with no
mark at all:

- **`☰ ` — the agent's plan.** `agent.plan` renders the whole to-do list on one
  wrapping line, done entries `✓ ` and dimmed, pending ones `○ ` and not, so the
  list scans to the entry the agent is on. Every version of the list arrives
  whole, so the pane shows the current state rather than a diff — a reader who
  opens the pane mid-run wants to know where the agent *is*. Like the run
  header it appears at **`normal` and `verbose`** and never at `compact`: a
  plan is what the agent *intends*, which is neither what it said nor what it
  did.
- **`agent.command_output` is `verbose`-only, and gutterless.** It is the body
  a command printed, so it renders like the output of a command step — flush
  left and dim — rather than as vincent's account of one. It is absent below
  `verbose` for the reason it has its own record type at all: a step running
  `go test ./...` must not be able to flood the level most readers use. A body
  the record's cap cut ends in `… output truncated`, because truncation a
  reader cannot see is indistinguishable from a command that printed exactly
  that much.
- *Added 2026-09-17 (task 110, issue #402).* **An edit's outcome is its delta,
  and `agent.patch` is `verbose`-only.** A claude edit's `agent.tool_result`
  renders `✓ +13 −9` (an overwrite `✓ updated · +1 −1`) wherever outcomes render,
  from `compact` up, the same as cursor's. It replaces the prose line rather
  than adding one, so `compact` does not grow (task 066 decision 1). The hunks
  render under it at `verbose` only, for `agent.command_output`'s reason:
  gutterless, `+` and `-` lines in the Diff tab's add and remove colors, `@@`
  headers dim, context lines unstyled. Each line is preformatted — a patch's
  indentation is part of what changed — so a line wider than the pane continues
  on the next row rather than being clipped. A patch the record's cap cut ends
  in `… patch truncated`.

Still **no timestamps**.

*Amended 2026-09-17 (task 109, issue #401).* This paragraph said
`parent_call_id` was deliberately **not rendered**, because the gutter is two
columns and flat and nesting was its own design problem with its own capture
(task 066 decision 2). The capture exists (§9.2), and that sentence is replaced
by what follows. **This amends T4.16's flat two-column gutter model**, and says
so here rather than changing it silently: a subagent's records gain a second
two-column prefix in front of the gutter. Everything else in the model stands.

- **A chronological rail.** A record carrying `parent_call_id` stays exactly
  where it arrived, and is drawn behind a two-column rail, `┊ `, with its own
  gutter composed after it: a nested tool call is `┊ ▸ `, its outcome
  `┊     ✓ `, nested prose `┊ ` and then the prose. Wrapped continuation lines
  keep the rail, for the reason a blockquote's bar is drawn on every line. The
  rejected layout is grouping children under their spawning call: subagents run
  concurrently and interleave with each other and the main loop, so live lines
  would land above the tail, and `vincent task transcript --follow` would have
  to buffer each subagent until it finished (task 109 decision 1).
- **A label on every switch.** When the rendered stream moves into a subagent,
  or from one subagent to another, a line `┊ ↳ <description>` names which one.
  The description is `agent.subagent_started`'s, else the spawning
  `agent.tool_use` call's summary, else `subagent`. The label is emitted only if
  the child record after it renders at the current level, so no level leaves a
  dangling label. Any rendered main-loop line ends the rail, so the next child
  line is labelled again.
- **One level quieter.** A child record renders at level L only where a
  main-loop record of its type renders at L−1. At `quiet` nothing of a
  subagent's internals shows; `compact` shows its prose and errors; `normal` adds
  its tool calls and their outcomes; `verbose` adds its truncated reasoning, its
  plan, and a count of its unrecognized lines on the rail. Two consequences are
  stated rather than left implicit: a subagent's `agent.command_output`, being
  `verbose`-only at top level, **never renders nested** (the transcript, `e`,
  `--raw` and `--json` keep it) — *amended 2026-09-17 (task 110):* nor does a
  subagent's `agent.patch`, for the same reason, though the dialect rarely sends
  one there (§9.2) — and a subagent's `agent.raw` lines are **never
  shown whole** in the pane. The main loop's own rules were rejected because a
  subagent's prose would still read as the agent's at `quiet` (task 109
  decision 2).
- **The sub-run's lifecycle.** `agent.subagent_started` and
  `agent.subagent_progress` never render a line. Progress is normalized only so
  that it does not become an `… N unrecognized line(s)` count between child
  records at `compact` and `normal`. `agent.subagent_finished` is a main-loop
  record that follows the top-level tool-call rules, hidden at `quiet` and shown
  from `compact` up, and is drawn on the rail as the completion line:
  `┊ ✓ completed · <description> · 14 tool uses · 5m00s`. `failed` is
  `┊ ✗ failed · …` and `stopped` is `┊ ■ stopped · …`, apart from `✗ ` because
  an agent that was stopped and one that failed send a reader to different
  places, the reasoning behind `⊘ `. Any other status is `┊ · <status> · …`,
  with no wording of its own (the T4.17 rule). Tool uses and duration appear
  only when reported. The spawn itself is the ordinary `▸ ` tool call, and a
  background launch's outcome is `✓ started in background`.
- **Depth one.** Only `spawn_depth: 1` has been captured, so a record whose
  parent is itself a child call renders on the same single rail. A second rail
  waits for a capture that has one.
- **The command line prints `normal`.** `vincent task transcript` and
  `vincent chat transcript` have no levels, and print the pane's `normal`
  content for a child record, as they do for the run header: prose, tool calls,
  outcomes and errors on an ASCII rail `| `, labelled `| -> <description>`, and
  never its reasoning, plan, unrecognized lines or command output. The
  completion line is `| = completed - <description> - 14 tool uses - 5m00s`,
  `| ! failed - …`, `| ~ stopped - …` or `| - <status> - …`, and a tool outcome
  with no summary prints its verb (`< started in background`). `--json` and
  `--raw` are unchanged.

*Amended 2026-09-01 (task 073).* Assistant prose is rendered as **Markdown**;
every other record stays literal.

- **What is interpreted, and only this.** `agent.output` records, in both
  workspaces — they reach the same renderer — and the chat's §17 retention
  fallback, which is the same prose arriving by the other door. Reasoning, tool
  calls, tool results, command output, `agent.raw`, `agent.usage`, every
  `agent.error` and every `vincent.*` record stay exactly as literal as they
  were, and so does `agent.result`, including the case where it falls back to
  its result text: that is a `✓ `/`✗ `-marked line, an event rather than prose,
  and Markdown must never make prose look like a tool event. Command output and
  tool summaries contain Markdown punctuation constantly, and none of it was
  meant as formatting.
- **A run of consecutive `agent.output` records is one document** *(added
  2026-09-01, issue #291)*. The unit of parsing is the run, not the record: an
  adapter that delivers a message in several records must not turn a table, a
  list or a fence spanning two of them into two broken documents. Any other
  record — reasoning, tool use, tool result, command output, `agent.raw`, a
  result line — closes the document, and prose after it opens the next; so does
  a run or turn boundary, since each pane renders one attempt's or one turn's
  records. The rule is independent of the verbosity level: a level that hides a
  record must not change how the prose around it parses. The blank-line rule
  between a document and a record that is not prose is unchanged. Two records
  that each carry part of one paragraph therefore reflow into that paragraph
  rather than staying two lines, which is the same change seen from the other
  side. *Amended 2026-09-17 (task 109):* a change of `parent_call_id` closes the
  document too, so a subagent's prose and the main loop's, or two subagents',
  never parse as one document. Only main-loop prose counts as the attempt
  having rendered output for the result-text fallback above: a subagent's
  prose is not the agent's answer.
  - **The bound on reflow, and what is deliberately not built.** No part of
    this classifies an unfinished Markdown tail. `agent.output` never carries a
    partial document: claude runs message-level `stream-json` with no
    `--include-partial-messages` (the T1.7 decision), codex emits
    `agent_message` only on `item.completed`, and cursor delivers assistant
    content blocks whole. A record is present whole or is not present. Joining
    does mean a *record* boundary can fall inside a block — a table header in
    one record and its delimiter row in the next renders as a paragraph and
    then becomes a table — and the guarantee is the weaker, true one: a record
    boundary is a message boundary, the parse is deterministic from the
    accumulated source, and **nothing above the last block of the previous
    document moves** when a record arrives. Token-level deltas would need the
    classifier; reopening T1.7 is what would build it.
  - **Rendering is memoized per document**, keyed on the source's digest, the
    pane width, the verbosity level, the raw toggle and — *amended 2026-09-17
    (task 111)* — `tui.hyperlinks`, so toggling the setting re-renders rather
    than serving the previous document. A live chunk re-renders
    the document it extended rather than every record in the pane. There is no
    client-side throttle and no second timer: §13.3's daemon-side coalescing is
    the rate limit.
- **The subset is a written-down list**, not CommonMark: headings, paragraphs,
  emphasis, strong, ordered and unordered lists, nested lists, blockquotes,
  inline code, fenced code, horizontal rules, and — *amended 2026-09-01 (task
  075, issue #290)* — tables, inline links and inline images. Everything else
  renders as safe literal text: reference links (`[label][ref]`), autolinks
  (`<https://…>`), bare URLs, titled links (`[label](url "title")`), HTML
  blocks, footnotes and setext headings. Raw HTML is never parsed, and nothing
  is ever fetched or executed. The parser is vincent's own, over that list: the
  alternatives are a Markdown library plus an AST walker, or glamour, which
  emits a pre-styled ANSI block with its own wrapping and margins that cannot
  be folded into the gutter scheme below. A bare URL displays as exactly
  itself, which is the strongest reading of "without silently changing it"; it
  gains no reference number and no styling.
- **The gutter scheme is unchanged.** Assistant prose still gets the blank
  gutter and still sits flush left. Markdown structure lives *inside* the
  content column: a heading's `▌ `, a list's `• `/`◦ `/`▪ ` or its number, a
  blockquote's `│ ` and a fenced block's `▏ ` are composed after the gutter,
  the way a tool result composes its `✓ ` into a four-column one. Every one of
  them is a glyph rather than a colour, so a monochrome terminal keeps every
  distinction — the same rule the gutters themselves are held to. Inline code
  keeps its backticks for that reason.
- **Measurement is terminal cells** (`ansi.StringWidth`), not runes: a CJK
  glyph and an emoji are two columns, a combining mark is none, and a ZWJ
  sequence is one grapheme of several runes. **The wrap-plain-then-style
  invariant above stands** and is not superseded by this — text is still
  wrapped while plain and each produced line styled afterwards, so no wrapping
  is ANSI-aware and no escape sequence can be split by a break. What changed is
  how wide a character is, not when a style is applied.
- **A blockquote's bar is drawn on every line of the quote.** A wrapped line's
  hanging indent is otherwise spaces of the gutter's width, which would put the
  bar on the first line and drop it from the rest — a content gutter that is
  not a gutter.
- **Code-block overflow is a hard wrap at the cell boundary**, continued at the
  block's own rail, with every space kept and every hard break honored. A tab
  is the four columns lipgloss draws it as, so the measured width and the drawn
  width agree. Not truncation, which would put the tail of a long line out of
  reach of the TUI entirely, and not clipping, which is what wrapping replaced.
- **A table is laid out by arithmetic, and degrades to records** *(added
  2026-09-01, task 075)*. It is recognized only with its delimiter row, so
  prose containing a `|` stays a paragraph. Each column has a **natural** width
  (its widest cell) and a **minimum** width (its widest unbreakable token; a
  cell that is a single inline-code span is unbreakable whole). Against the
  content column, with two spaces between columns and no borders: Σ natural ≤
  available draws natural widths; Σ minimum ≤ available < Σ natural gives every
  column its minimum and shares the surplus **in proportion to each column's
  remaining demand** (natural − minimum); Σ minimum > available switches to a
  **stacked record** per row — `column: value` pairs, one per line, opened by
  the `▪ ` already in the bullet vocabulary and separated by a blank line, so
  the row boundary is a glyph and survives monochrome and an empty cell. A
  wrapped cell's continuation stays inside its own column, and the header is
  separated by a per-column run of `─` — a rule, not a grid. The two rejected
  alternatives are named because the rule exists to avoid them: a **clipped
  pipe table**, which loses the cells the reader came for, and a **second
  scrolling axis** inside the viewport, which makes keyboard and mouse
  behaviour ambiguous in a pane that has only ever scrolled one way.
- **A link's destination is a numbered reference, and a hyperlink escape only
  on opt-in** *(added 2026-09-01, task 075; amended 2026-09-17, task 111)*. An inline link's label renders as ordinary
  prose carrying a dim `[n]`, and the message ends with a block of `[n] dest`
  lines — one per distinct destination, numbered per rendered message,
  identical destinations sharing a number. *Amended 2026-09-01 (issue #291):*
  the message is the **document** above, so a destination named in two records
  of one run gets one number and one line, and the reference block closes the
  document rather than each record. Those lines are preformatted, so a
  destination is never word-collapsed and a long one hard-wraps at the cell
  boundary. An image renders its **alt text** as the link-shaped item and its
  source as the reference; nothing is fetched, and there is no fetch in this
  path to disable. Destinations are shown literally whatever their scheme.
  **vincent emits no OSC 8 hyperlink** by default: there is no reliable
  capability probe, the payload would carry an agent-supplied URL inside an
  escape sequence, and a link spanning a wrap boundary would have to be closed
  and reopened per line under an invariant that keeps escape sequences out of
  wrapping entirely. ~~The numbering is what a later reader action would
  name.~~ *Amended 2026-09-17 (task 112):* the numbering is what the link
  picker names — see the task 112 amendment below.
  *Amended 2026-09-17 (task 111, issue #404):* with `tui.hyperlinks` on (§12.3)
  a link whose destination passes §16's hyperlink sanitizer is clickable in
  three places — its label, its dim `[n]`, and the destination text on its
  reference line — each carrying one `id=` so a label wrapped across lines is
  one link on hover. The reference block is **never** dropped: it is what
  keeps the true destination on screen when a label reads as a different URL.
  The link is a style attribute, applied per produced line after layout, so
  each line opens and closes its own link and wrapping still sees only plain
  text. Only the inline links and images above are linked; bare URLs,
  autolinks, reference and titled links stay literal, raw mode shows source
  and emits no link, and the clipboard payloads carry none. A destination the
  sanitizer refuses renders exactly as it does with the setting off.
- **A fenced block shows its language and is highlighted with styles only**
  *(added 2026-09-01, task 075)*. The info string's first word is drawn as a
  dim header at the block's rail when present. Body lines are tinted by a
  vincent-owned coarse token scanner (comment, string, number, keyword,
  punctuation, plain) over a written-down language list, with a plain fallback
  for everything not on it, from a vincent-owned palette a monochrome or ASCII
  profile flattens to nothing. The scanner is not a parser, and a pathological
  string or a nested-comment dialect will be mis-tinted; nothing depends on it
  being right. **Highlighting emits styles and never characters**: with the
  escapes stripped a rendered block is byte-for-byte the fence's content — tab
  expansion and the hard wrap above aside — which is what makes "highlighting
  cannot change what is copied or transcripted" checkable rather than
  asserted.
- **Rendering is derived and never stored.** The JSONL transcript and every API
  payload keep the agent's exact bytes (§13.3), no render path mutates a
  record, and a resize re-renders from the Markdown rather than re-wrapping
  previously rendered ANSI. ~~A link picker is still a follow-up: the reference
  numbering is what a later action would name.~~ *Amended 2026-09-17 (task
  112):* the numbering is what the link picker names — see the task 112
  amendment below.

*Amended 2026-09-01 (task 076).* The reader can see the source and take it
away. Both actions are client-side and session-scoped; nothing about them is
persisted, sent to the daemon or written to a transcript.

- **`ctrl+o` toggles rendered/raw** for assistant prose, in the task
  workspace's output pane and the chat workspace alike. Raw shows the stored
  Markdown as source — one pane line per source line, preformatted, hard-wrapped
  at the pane's edge, no parse — and it is what the §17 retention fallback shows
  too. The choice is **one session value shared by both workspaces**, held the
  way the verbosity level beside it is and persisted no more than that: moving
  between the two panes, or between tasks, never resets it. Raw is presentation
  only — it does not touch the records, the streaming offset, the level, the
  transcript, follow mode, or any task or chat state — and it still goes through
  §16's stripping (below).
- **`ctrl+y` opens the copy picker**, a popup listing what can be copied out of
  the assistant prose currently loaded, newest first: per document its
  **Markdown** (the stored text) and its **plain text** (the pane's structure
  with the punctuation gone — headings as their own line, the pane's own list
  markers, blockquotes prefixed, fences dropped), plus one row per **fenced code
  block** (its interior, no fence and no info string, whitespace kept). Every
  payload is built from the source, never from rendered lines, so a copy made at
  width 40 and one made at width 200 are byte-identical. *Amended 2026-09-01
  (issue #291):* a row is one per **document**, and it is a **reference to that
  document, resolved when it is picked**, carrying the text captured when the
  popup was built as its fallback. A reference rather than an index, because an
  index drifts the moment a chunk arrives or the record cap prunes the front of
  the window. Two consequences, both intended: picking a document that has
  grown since the popup opened copies it as it is now, because "copy this
  message" means the whole message; and a reference whose records the prune
  took copies the captured text, so a pick can never fail.
- **A paused pane keeps its place across a rebuild** *(added 2026-09-01, issue
  #291)*. Records carry no id on the wire and none is added; identity is
  client-assigned on ingest and is not an index, so it survives the record cap.
  A pane that is not following captures the identity of its topmost visible
  block and restores it after any rebuild — a resize, a front-prune, a
  verbosity change, a raw toggle. Following is untouched: its anchor is the
  bottom.
- **The clipboard has two transports, and the notice says which ran.** The
  system clipboard is tried first because its answer can be trusted; when it
  refuses, the text is handed to the terminal over OSC 52, which is the correct
  destination over SSH but reports nothing back. So a copy reads "copied" only
  when it was verified, and otherwise says it was sent to the terminal and names
  the system clipboard's error. A payload that sanitizes away to nothing is an
  error, not a silent no-op.
- **Link actions are not the copy picker's.** The renderer grew links while
  this was in flight (task 075), so what was missing was no longer the construct
  but a way to say *which* reference a reader means. *Amended 2026-09-17 (task
  112):* copying, inspecting and opening a destination are the link picker's,
  below. What the payloads do owe the numbering is to carry it:
  a plain-text copy keeps each `[n]` and ends with the same `[n] dest` block
  the pane draws, because a destination stripped of both its punctuation and
  its reference would be a destination deleted.
- **Both keys are ctrl-modified in both contexts**, unlike the `v`/`ctrl+r`
  split task 071 chose: one action should have one name in the help overlay and
  the palette. A bare letter cannot work in a chat, where the composer owns
  every printable key.

*Amended 2026-09-17 (task 112).* The reader can act on a link. Like the two
actions above this is client-side only: no route, no stored state, and the
renderer is unchanged.

- **`ctrl+l` opens the link picker**, in the task workspace's output pane and
  the chat workspace alike, and from the palette. It is a popup of its own
  rather than rows in the copy picker, whose `enter` copies: a link needs two
  actions and the search line takes every letter, so the second one is a ctrl
  key, and a popup titled "copy" must not open a browser on `enter`.
- **Its scope is the copy picker's**: every assistant document in the loaded
  records or turns (the §17 retention fallback included), newest first, under
  the same `message n` ordinals — a document with no links takes its ordinal
  and shows no group, so one document has one name in both popups. Rows are
  **one per `[n]`**, exactly the pane's reference block: the same parse and the
  same numbering, identical destinations sharing a row, image sources included.
  A row shows `[n]`, the label of the destination's first occurrence (alt text
  for an image, the destination when there is no label) and the destination;
  search matches the label, the group and the destination. Raw mode changes
  nothing here — links are derived from the source, not from what is drawn.
- **A row is a reference**, `(document, n)`, resolved when it is picked, with
  the destination captured when the popup was built as its fallback. A row
  delivers what it showed: when the document is gone, or its `[n]` now names a
  different destination because the record cap pruned the front of it, the
  captured destination is used.
- **`enter` opens, `ctrl+y` copies**, and `esc` closes. `ctrl+y` means copy
  inside the popup exactly as it does outside it. The popup draws the cursor
  row's **whole destination**, stripped and hard-wrapped like a reference line,
  above a one-line key hint, so a reader always sees the exact bytes `enter`
  would hand to the opener; `enter` is the explicit action and no confirmation
  follows it.
- **A destination the opener refuses stays listed and copyable.** Any scheme
  but `http`/`https` — `mailto:`, `file:`, `javascript:`, a relative path — or
  a destination that does not parse is marked `copy only`; `enter` on it opens
  nothing and says why. The mark is the opener's own validity check, never a
  second copy of the scheme rule.
- **Outcomes are notices where the key was pressed**: the task workspace's
  status line, or the chat's note — never the pull-request note, which is the
  pull-request opener's. A copy is the copy picker's notice (`link [2] copied`,
  or sent to the terminal); an open says `opened <url>` or why it could not,
  because a browser that opened on another desktop is otherwise
  indistinguishable from nothing.

Views 3–7 stay full-screen because they are forms and lists, not observations: the
new-task flow is eight fields with pickers, and squeezing it beside a live tail
serves neither. Takeovers are for surfaces you visit deliberately; popups are
for what interrupts you — the palette, confirmations, the three form popups, the
copy picker and the link picker (task 112).
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

*Amended 2026-09-03 (issue #316): the picture draws what the engine actually
does with a fan-out.* Tasks 080 and 081 gave a `fan_out` a lane DAG and an
eager schedule, and neither was visible — a `needs:` edge, the round a lane runs
in and a derived lane list were all implied by a document nobody was shown. Four
additions, over the **authored** lane columns; task 051's non-goal stands and
nothing unrolls:

- **`needs:` edges** are drawn between lane columns, as their own kind. A lane
  that needs nothing is spawned by the step and keeps its edge from the header;
  a lane that needs others is spawned by *their* merges and takes its incoming
  edges from them instead, because drawing both would say a dependent lane
  starts in round one.
- **Waves are stacked.** A lane sits below the lanes it needs, one row per
  round, so the rounds §7.6 schedules in are the rows the picture has. The wave
  is *derived* — a topological level over `needs:`, the same derivation the
  engine schedules by — never authored. A fan-out whose lanes need nothing is
  one wave and lays out exactly as it always did.
- **`schedule: eager` is badged** on the step node. `barrier` gets none: it is
  the default, and the difference worth seeing without selecting is the one
  where a lane's dependents start before its siblings have finished (§7.6).
- **A derived lane list is marked on its frame**, with what it was derived from,
  from the `derived_from:` record the runner writes into the snapshot (§7.6, task
  080 decision 5 as amended). The frame is where the mark belongs, because the
  derivation produced the lanes and the lanes are what the frame encloses. A
  hand-authored list has no record and its frame is drawn exactly as before.

A lane's caption additionally carries the lane's own **block reason**, not just
its state: the lane's steps run in the child, so the caption is the only place
that fact can be told (051 decision 1 is kept — the state rides on the caption,
never on the inline step nodes). `l` opens the lane under the graph cursor.

`e` and `R` are absent from this tab: a snapshot has no file to open and no
registry entry to re-read.

*Amended 2026-09-14 (task 097, issue #418): the overlay is colored by run
state.* "Words and glyphs first, colour second" now has its second half. Every
color restates words already on screen, so the picture with every style
stripped is byte-identical to the uncolored rendering of the same overlay;
coloring moves no coordinate and loses no selection.

- **A node takes its newest attempt's style**, and a task parked on it wins over
  the step's state: `succeeded` and `approved` green, `running` cyan, `failed`
  and `rejected` red, `interrupted` yellow, `skipped` and `stopped` faint; task
  `blocked` red and bold, `awaiting_input` yellow and bold, `paused` magenta. A
  node never reached is drawn as before. Border, label row and kind row all take
  the style. This is the Steps tab's step-state palette and the board's
  task-state palette — one of each, shared.
- **A colored node shows its selection by the heavy border glyphs alone.** An
  uncolored node keeps the `Selected` style.
- **A lane caption takes its child task's board style** — the parked state when
  the child is parked or pause-requested, else the child's own state.
- **Off-graph attempts carry state words, and then color.** Each prints its
  newest attempt's glyph and state the way an authored node does; before this
  they printed neither.
- **An edge is colored when the run took it.** A flow edge when both ends were
  reached. A `condition`'s `false` branch only when its newest row is
  `stopped`, and its onward edge only when that row is `succeeded` and the
  target was reached; a `break`'s `true` branch and onward edge the same way.
  A back-edge when its source was reached and some node in the loop's body, at
  any depth, ran iteration 2 or later. `needs:` edges and edges touching a
  lane's inline steps never — those steps are the child's. END counts as
  reached only when the task is `done`, and is itself never painted. A
  `parallel` or `loop` header counts as reached when anything in its group
  was, and a fan_out's merge when the fan_out's row is past `running` or
  anything after it was reached; those boxes stay unpainted, because they have
  no row and no words. A taken edge takes its source's style, or its target's
  when the source has none. Edge labels keep their own style.
- **The workflows screen's `g` definition graph is not colored by run state** —
  a definition has no run.

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

*Amended 2026-09-01 (task 076).* **`ctrl+p` opens the same palette**, and it is
hoisted above the input-capture gate the way `ctrl+v` is. `:` is a printable
key, so on a surface that owns the keyboard — the chat composer, a filter — it
types a colon into the draft and opens nothing; the palette is this section's
"what can be done right now" surface, and it must be reachable from everywhere,
not only from a resting list.

*Amended 2026-09-17 (task 114, issue #405).* **`f1` opens help on the same
terms.** `?` is printable too, so on a surface that owns the keyboard it types
itself and help was unreachable there — in a chat, a filter and every form.
`f1` is hoisted beside `ctrl+p` and toggles the same overlay everywhere; `?` is
unchanged wherever it already worked. A function key rather than `ctrl+/`,
which legacy terminals send as `ctrl+_` and which moves with the keyboard
layout. Its costs are accepted rather than worked around: MacBook keyboards
need `fn`, and VS Code's integrated terminal keeps F1 unless its settings hand
it over. **The help overlay owns the keyboard** while it is open, by the
palette's rule: `?`, `esc` and `f1` close it, `ctrl+c` quits, and every other
key is swallowed — a key under the sheet acting on the board, or typing into a
draft nobody can see, is what this rules out. **A global row runs as the
root's own key** wherever it is fired from: a palette row or a footer click
for help, quit, the mouse toggle, `!` or new task takes effect even while a
text field has the keyboard, rather than replaying its key into that field.

**The footer is one line and never wraps.** Left to right: the focused panel's
keys (at most five, in registry priority order), then the task's
`available_actions`, then — pinned right and never truncated — `: commands`,
`? help`, `q quit`. Overflow truncates from the left with `…`. The pinned segment
is exempt because `:` is the escape hatch that makes every other key optional; a
narrow terminal dropping it would fail exactly when the human is most lost.
*Amended 2026-09-17 (task 114, issue #405):* the pinned segment names the keys
that work on the surface in front of the human. While a text field has the
keyboard, `:`, `?` and `q` would be typed, so it reads `ctrl+p commands`,
`f1 help`, `ctrl+c quit` instead; each span still fires its key when clicked.
It is still measured first and never truncated, and its extra width comes out
of the hints' budget.

*Amended 2026-09-10 (task 094, issue #352).* **The width decides how many keys
reach the line, and the line says how many it is not showing.** "At most five"
is superseded: the hinted rows are admitted as a strict prefix of priority
order — priority means priority, so a wide row is never skipped to squeeze in a
narrow one behind it — as many as the composed line holds beside everything
that follows them, which is measured first and comes out of the budget. Five
was the mechanism; the invariant those decisions protected is one line that
never wraps and never truncates the pinned escape hatch, and it is unchanged.
A dim, clickable **`+N`** sits after the action segments and counts what the
palette can reach on this surface and the line is not showing: the rows that
lost the width contest, the rows carrying no hint at all, and whatever the `…`
took. It opens the palette, because that is where those keys already live.
`N` counts the focused surface's own rows only — global keys are what the
pinned segment stands for, and counting them would put `N` near fourteen on
every board and never at zero. A row another row's hint already advertises
(`right` behind `←/→ fold`, `O` behind `C/O fold all`) is not hidden and is
declared as such in the registry rather than parsed back out of the hint text.
A row dropped as inert — the board's fold keys with `group_by: []` — is neither
shown nor counted, because naming a press that does nothing and counting one
are the same lie. `+N` is absent when nothing is left over, while a
confirmation owns the keyboard, and on the popup surfaces whose rows the
palette does not list.

`?` remains, as a compact cheat sheet grouped by panel, rendered from the registry.

### Keys

Global: `:` palette (`ctrl+p` where a text field has the keyboard) · `?` help (`f1` where a text field has the keyboard; *added 2026-09-17, task 114*) · `n` new task · `q` quit (the daemon keeps running;
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
reasoning and nothing unrecognized. *Amended 2026-08-31 (task 071, issue
#282):* that level is **one value for the session, shared by the task
workspace's output pane and the chat workspace** — `v` cycles it there,
`ctrl+r` here, and cycling in either is visible in the other. Nothing persists
it: no `tui.json` entry and no `tui:` config key, for the reason this paragraph
already gives. The collapsed-content hints name whichever key expands them
where they are read, so a chat says `(ctrl+r)` and never `(v)`. `e` opens `$EDITOR` where a view has a file to edit. `R`
re-reads a registry or the daemon blocks. One key jumps to the next task needing a
human, surfaced in the footer only when that count is non-zero — the board has
always pinned and belled those tasks without offering any way to *go* to one.

*Amended 2026-09-03 (issue #321).* That cycle is **four levels, not three**:
`quiet → compact → normal → verbose`, wrapping from verbose back to quiet, so
one press is still "one louder, wrap to the quietest" and the gesture is
unchanged. `quiet` sits below compact and is what the agent **said**, plus
anything that went wrong: relative to compact it drops `agent.tool_use` and
`agent.tool_result`, the success outcome of the result line, and unrecognized
lines altogether. That last one amends the sentence above rather than adding
below it — unrecognized lines stay behind their count at **compact and normal**,
not at every level below verbose, because a count is an offer to expand and
quiet is the level that makes no offers. `agent.error`, a chat turn's own fail
reason and the `── turn N ──` separators render at every level including this
one: a display level is not allowed to hide a failure. `command.output` and
`vincent.output` are untouched, so a command step's pane is byte for byte the
same at quiet as at compact — quiet is a rule about what the *agent* narrated,
and a command step narrates nothing. Everything else here holds verbatim: the
level is still one value for the session shared by both panes, still persisted
nowhere, the default is still `normal`, and the pane title still names any level
but the default — which matters most for this one, since it is the level whose
effect can be to hide the only thing a turn produced.

*Amended 2026-08-31 (task 067).* The chats board and the chat workspace carry
their own rows in the registry (`internal/tui/bindings.go`), which is what the
`?` overlay, the footer and the palette are derived from. On the **chats
board**: `enter` opens the workspace, `n` starts a chat, `A` archives, `/`
filters, `←`/`→` fold a project group and `R` re-lists. *(Amended 2026-09-10,
task 093: archive was `a` and the re-list was `r`; both moved to the vocabulary
key below.)* In the **chat
workspace**: `enter` sends, `ctrl+j` inserts a newline in the draft *(added
2026-09-19, issue #500)*, `ctrl+x` stops the live turn, `ctrl+t` hands the
worktree and branch to a task *(added 2026-09-01, task 074)*, `esc` returns to
the board. In the **new-chat form**: `ctrl+s` creates, `tab`/`shift+tab` move
between fields, `enter` opens the focused field's list, `←`/`→` step the
project and agent fields in place, `esc` discards.
`n` is the one key whose meaning depends on where you are, deliberately: on the
chats board it makes a chat, and everywhere else it still makes a task. `!` is
**not** extended to chats — an `awaiting_input` chat is pinned and badged on
its own board and nowhere else.

*Amended 2026-09-01 (task 074, issue #288).* `ctrl+t` on the chat workspace
opens the **new-task form in handoff mode**, seeded with the chat: the project,
the base branch and the branch are the chat's and are shown read-only, marked
"(from the chat)", because they name a worktree that already exists and there is
nothing here to decide. The issue row is hidden — the chat is the source
already. Submitting posts to the chat's own route, not to `POST /v1/tasks`
(§13.4), and lands on the created task. A handed-off chat's header carries a
**permanent** link to that task; the state renders as "handed off" and the chat
sorts into the board's terminal band beside archived ones — *amended 2026-09-01
(issue #298): and, like an archived one, is off the board's default listing
altogether, reachable by cycling it with `s`. The band is what orders the two
terminal states once the toggle brings them back.* It is a ctrl
combination for the reason `ctrl+r` is: the composer owns every printable key.

*Amended 2026-08-31 (issue #279).* The new-chat form's project and agent
pickers are fed by `GET /v1/projects` and `GET /v1/agents`, and the agent
picker offers **only adapters that report `supports_resume`** (§9.6, decision
row 29) — the daemon's `agent_cannot_resume` refusal stays the authority and is
still rendered, it is simply unreachable from this form. `n` on a chats board
with no registered project does not open the form at all: it says so on the
board, because a form with no project to create in cannot be submitted and
offers no field that would accept one.

*Amended 2026-08-31 (issue #281).* Four of the new-chat form's six rows —
project, agent, model and effort — are the same `picker` the new-task,
follow-up and repair forms use, so the TUI has one idiom for "choose one of a
list": incremental filtering, a bounded window, the `cli`/`curated` provenance
note on the model and effort catalogs, and free text on those two because an
adapter's catalog is a suggestion (§9.6). Both catalogs scope to the selected
adapter and are cleared when it changes, and both lead with an "(agent
default)" row naming that adapter's own default — a chat has no workflow, so
"(workflow default)" would be the wrong words. Title and base branch stay text
fields; the base row's placeholder names the selected project's real default
branch, and submitting it untouched sends no branch at all, so the daemon
resolves the project's default as before. `enter` opens the focused row's list
or moves on from a text one, uniformly: `ctrl+s` is the sole create key.
`←`/`→` survive on the project and agent rows as a fast in-place step — the
enum-row idiom from the new-task fields editor — and are not offered on the
two catalog rows, where "next" answers nothing.

An **open new-chat draft captures input on every row**, not only on the two
text ones. This is a deliberate, scoped exception to the rule above that the
shell consults the focused panel for capture and leaves the global single-key
bindings live: only title and base are text fields, a live draft sits behind
the other four, and a global `q` on the project row quit the TUI with the
draft. An open list adds a filter row and a free-text row on top of that. The
rule is unchanged for every other form — the new-task form still leaves the
globals live while navigating — and `esc`, this form's layer of the stack
below, stays the way out: with a list open it closes the list only, and the
draft survives.

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

*Amended 2026-09-03 (issue #316).* Walking a fan-out is four keys, registered in
`internal/tui/bindings.go` like every other. On the **board**, `L` expands or
collapses the selected fan-out's lanes rather than drilling into them. In the
**task workspace**, `l` opens the lane the current tab's selection resolves to
and `U` opens this lane's parent — both on every tab that can name a lane,
because a reader standing anywhere in a parent means the same thing by them, and
the tabs differ only in which lane `l` resolves to. In the **Output pane**,
`<`/`>` step the lane selector. Where a tab already had a meaning for `l` — the
vim-right that steps the attempt selector — that meaning is kept for a task with
no lane to open. `esc` gains one rung below the popup and above the screen: the
task this one was opened *from*.

*Amended 2026-09-10 (task 093, issue #353).* **The key vocabulary.** The
registry made the help *accurate* long before it made the keys *consistent*:
`?`, the footer and the palette faithfully advertised four different keys for
"refresh", because the rule above — "one key means one thing everywhere" — was
stated for `/` and applied per view for everything else. It is now a table, and
`internal/tui/bindings_test.go` holds it.

| Operation | Key |
|---|---|
| refresh / re-read | `R` |
| archive | `A` |
| delete a persisted record | `D` |
| remove a row from an open draft | `d` |
| add / create | `a` |
| edit in `$EDITOR` | `e` |
| type free text instead of picking from a list | `t` |
| open in a browser | `o` |
| open or expand the row under the cursor | `enter` |
| cycle a listing's scope | `s` |
| filter | `/` |
| fold / unfold | `←`/`→`, `C`/`O`, `space` |
| open a fan-out lane | `l` |
| page | `<`/`>` |

Three clauses, not one — the issue asked for "exactly one key registry-wide",
which taken literally would reopen task 025's deliberate partition of `R`:

1. A key may be shared **only** when it means the same operation. Archive is
   `A` whether it is a §6 action on a task or the chats board's own key; that
   is the vocabulary working, not a collision.
2. A key may mean two different things **only** where the registry can prove
   the two surfaces never co-exist. A takeover screen offers no
   `available_actions`, so `a` may add on projects while `a` approves a gate;
   the **task workspace is not disjoint** from the action set, so nothing there
   may reuse an action letter. This is task 025's finding promoted from an
   accident to the rule.
3. A key already carrying a vocabulary term may not be given a second meaning
   on a new surface.

The §6 action letters `p a x r E R s c A F` do not move, so they decided the
contested cases. `R` won refresh: it already held seven surfaces and repair is
only ever offered where a registry re-read is not. `A` won archive because it
is the action letter. `D` destroys something persisted and `d` edits an unsaved
draft — the inversion is what makes pressing `d` on an archive safe, and it is
why the projects view's remove is `D` while the workflow editor's and the
new-task Fields editor's stay `d`. The disjointness clause 2 rests on for the
archived boards is a fact about §6's own table, not an assertion: `archived` is
never a `from` state in `taskstate`'s transitions, so an archived row offers no
action at all, and the test derives that rather than listing it.

What moved: the chats board's `r`→`R` and `a`→`A`; both archived boards'
`d`→`s`; the projects view's `d`→`D`; the pull-request list's `c`→`a`; the task
workspace's Pull Request tab `c`→`enter`; and "type your own answer" `e`→`t` in
the answer form and in all four pickers.

The Pull Request tab **loses its refresh key outright**. `R` is repair on every
tab of that workspace and does not move, and the tab already re-reads on its own
timer. That key was also half of a live bug this amendment closes: `r` and `c`
were intercepted before the tab fell through to the task's own actions, so
**retry and cancel were unreachable from that tab** while the footer — rendered
from the same registry — went on advertising both. That is exactly the failure
"a key that means two unrelated things depending on the row is worse than one
that sometimes does nothing" was written about.

The new-task form's **Fields editor** is a registered context of its own
(`ctxNewTaskFields`), for the reason the structured workflow editor is one:
nothing the form underneath offers means the same thing inside it. Its `a` and
`d` were handled and hinted inline and the registry had never heard of them, so
no registry test could see them and `?` did not list them; the inline hint line
is gone, because the footer now renders from the registry like every other
surface.

Deliberate exceptions, each with its recorded reason: `n` (§15's one
two-meaning key, task 067) · `l` falling back to vim-right where a tab has no
lane (issue #316) and meaning *link* on the pull-request takeover, which has no
lanes (task 052 decision 6) · `r` as *retry connecting* while disconnected ·
`d` as the output tab's alias · `i` · `u` · the unregistered vim aliases
`h j k l f b u G`. Task 049's retirement of `1..6` is untouched.

*Amended 2026-09-16 (task 068.4, issue #387):* the Pull Request tab gains four
surface-local keys and none of the vocabulary's: `m` merge, `X` close or reopen
(one key, whichever the state allows, as `p` is pause or resume), `i` comment
and `ctrl+r` re-run the failed jobs — a modifier because `r` is retry. No §6
letter moves. `X`, `i` and `ctrl+r` mean other things on the triggers
takeover, the workflows list, the daemon view and a chat, which carry no term
for them and are never open beside a task workspace, so clause 2 holds without
an exception. While their confirmations are up they own the keyboard, the
footer and `?`.

*Amended 2026-09-17 (task 118, issue #412).* **The table above is the default
keymap, and `tui.keys` (§12.3) may move it.** What moves is an *operation*: one
of the vocabulary terms, one of §6's actions (`pause` is one operation for pause
and resume; `archive` is the term's id), or a global — the palette, help, their
text-field keys `ctrl+p` and `f1`, `!`, `M`, `q` and `n`, which is also the
chats board's `n`, because "make a new one here" is one gesture and must not
split under a keymap. An override rebinds the operation on **every** surface
that carries it, so a user's keymap still has one operation to one key, and the
footer, the palette, `?` and every label that names the key render the key in
force rather than the default. The handlers dispatch from the same registry, so
a key the help advertises is the key the handler answers. An override
**replaces** the default rather than adding an alias — task 093's "no aliasing",
kept — so `?` still explains one key per operation.

The three clauses hold the **effective** keymap, not only the shipped one: one
checker, `internal/keymap`'s, runs over the defaults in the registry tests and
over the defaults-with-overrides in `internal/config`. A key that already means
anything else — another operation, or a fixed key on any surface, even one
never open beside the operation — is refused. The deliberate exceptions listed
above are recorded against their exact default keys, so they cover the shipped
keymap and no other: an operation moved onto an exception's key does not
inherit it, and `refresh` moved to `r` is refused because `r` is retry. The §6
action letters still do not move **as defaults**; task 093 decision 2 governs
the shipped keymap, and a user moving one is a user's choice held to the same
clauses.

**The composer rule.** A key a text field would type — one character, or
`space`, without `ctrl` or `alt` — is refused for `palette_alt` and `help_alt`,
whose whole reason to exist is to work while a text field has the keyboard
(task 076 decision 7, task 114 decision 1), and for any operation answered on a
surface whose text field owns every printable key while it is up: the chat
workspace and the new-chat form.

**What is fixed, and why.** No override may name or take these, and naming one
in `tui.keys` is refused with the reason: the surface-local rows that carry no
term (`g`, `L`, `m`, `X`, `i`, `u`, `P`, `U`, `S`, …), because they appear once
and a vocabulary that named them would be a list of every key; the multi-key
sets — fold, page, `↑`/`↓`, `K`/`J`, `[`/`]` — because a set is not a shape one
key can describe (task 093 decision 6); `esc`, because it is the layer stack
above and every layer is closed by it; `ctrl+c`, because it must always kill
the TUI; `ctrl+v`, the terminal's paste fallback; `tab`/`shift+tab`, which move
focus everywhere; the popups' `y`/`n`, which answer the question the popup
prints; and the keys the handlers answer beside the registry — the vim aliases
`h j k l f b u G`, the output tab's `d` and `r` as retry-connecting. The daemon
publishes no config event, so a keymap edited outside the TUI's own
config editor takes effect at the TUI's next configuration read (§12.3).

*Amended 2026-09-17 (task 119, issue #472).* `chat` is a §6 action, so it is an
operation like the rest: `T` by default, moved by `tui.keys` on every surface
that offers task actions. `T` is also the triggers takeover's dry run, which
carries no term and offers no `available_actions`; that shared default is
recorded as an exception on `T` alone, so it does not travel with a moved
`chat`. The chat workspace's `ctrl+q` close is a fixed key, for the reason its
`ctrl+t` hand-off is: the composer owns every printable key there.

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
- **Agent text cannot drive the terminal (task 073, added 2026-09-01;
  extended 2026-09-01 by task 076).** Every record's text is stripped of ANSI
  escape sequences and of C0/C1 control characters before the output pane
  measures or draws it — newline and tab excepted, which the pane renders
  itself. **What is put on the clipboard is stripped on the same terms**: a
  clipboard is pasted into a terminal, which is precisely the boundary this
  rule exists to hold, so "copy the original Markdown" means the stored
  Markdown minus escape sequences and C0/C1 controls. The rendered/raw toggle
  is bound by it too — raw mode shows the agent's bytes as Markdown *source*,
  not as terminal input, and is an escape hatch for a surprising render rather
  than a hole in the one chokepoint. Only vincent's own styles are
  emitted. Without this an agent, or anything an agent chose to echo, could
  clear the screen, move the cursor, rewrite the window title or overwrite
  adjacent UI with a `\x1b[2J` or an OSC sequence in a tool result summary, a
  command's output body or an error message — none of which are Markdown, which
  is why the stripping is at the pane's one wrapping chokepoint rather than in
  the Markdown path. Raw HTML in assistant prose is never parsed, fetched or
  executed; it renders as the characters the agent sent. *Added 2026-09-01
  (task 075):* vincent emits **no OSC 8 hyperlink** and the pane opens nothing,
  so a link in agent prose is text a human may read and copy, never a thing the
  terminal can be made to act on — and an image is its alt text plus a printed
  source, never a fetch. *Amended 2026-09-17 (task 111, issue #404):* only
  vincent's own styles are emitted, plus — when `tui.hyperlinks` is on, which
  it is not by default — an OSC 8 wrapper around a destination that passed the
  hyperlink sanitizer. The sanitizer is one function and the only door to a
  link. A destination becomes a link only if it is at most 2048 bytes; every
  byte is printable ASCII 0x21–0x7E (refusing C0/C1 controls, DEL, ESC, BEL,
  the 8-bit ST, space and all non-ASCII, so a look-alike Unicode host is
  refused rather than percent-encoded); `net/url` parses it; its scheme is
  `http` or `https`; its host is non-empty; and it has no userinfo, so
  `https://github.com@evil.example` is refused. The URI inside the escape is
  the validated original string, byte-identical to the printed reference, and
  the link id is built by vincent from a digest of the document and the
  reference number, `[A-Za-z0-9-]` only, with no agent byte in it. The
  reference block stays on screen with hyperlinks on as the anti-spoofing
  disclosure: what a click opens is printed as text beside the message. The one opener in the TUI (`openURLCmd`, reached from
  the pull-request surfaces) still refuses every scheme but http and https, and
  the renderer never reaches it. *Amended 2026-09-17 (task 112):* the renderer
  still opens nothing and emits OSC 8 only as the task 111 amendment above
  allows, but an explicit pick in the §15
  link picker now reaches `openURLCmd`. Nothing about the opener is loosened:
  it refuses every scheme but http and https, the picker marks a refused
  destination `copy only` by asking the same check rather than a copy of it,
  and the URL — already stripped by the chokepoint above — is passed to the
  platform helper as one argv element, never through a shell.
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
  confinement below is the container's and does not yet reach them. *Amended
  2026-09-16 (task 062.1, issue #396): the seam landed as 062.1 (§9.1) with only
  a host launcher, so agent processes still run on the host; the container
  launcher is 062.2 (issue #397).* *Amended 2026-09-17 (task 062.2, issue
  #397): the container launcher has landed, so a containerized task's agent
  steps run inside the container and the confinement below reaches them. Chats
  still run on the host.* *Amended 2026-09-17 (task 119, issue #472): **free**
  chats still run on the host. A chat linked to a containerized task (§5.5)
  runs its turns inside **that task's container**, through the same launcher
  the task's agent steps and repairs use, because an operator who confined a
  worktree must not have an unconfined agent handed it by a conversation. If
  the task's container is gone the turn fails rather than running on the host.
  The CLI's session store persists between turns under `mount_agent_config`,
  and without it lives in the container, which lives as long as the task.* What that
  confines is real and is the point: the
  filesystem outside the two bind mounts — the project repository and the task's
  worktree, both at their own absolute paths — the shell, and whatever tooling
  the image carries. An agent that `rm -rf`s the wrong directory reaches the
  worktree and the repository and nothing else of the user's machine.
  What it does **not** confine is stated here with the same honesty §16 already
  applies to `/mcp/step/{run_id}` not being a boundary:
  - **Outbound network is open by default.** `container.network: false` closes
    it, and task 062 refuses it together with `mcp.wire_steps: true` because a
    container with no network cannot reach the daemon's per-step MCP endpoint.
    *Amended 2026-09-14 (issue #366): task 061 shipped that refusal and it is
    deferred to 062, because until then every agent runs on the host and
    reaches the endpoint from there, whatever the container's network is.*
    *Amended 2026-09-17 (task 062.2 decision 5): refused again, for a workflow
    with an agent step at any depth after include expansion; a command-only
    workflow still runs with no network (§12.3).*
  - **The agent's credentials are inside it.** `mount_agent_config` defaults to
    true and bind-mounts `~/.claude`, `~/.codex` and `~/.cursor` **read-write**,
    because subscription auth takes no key from the environment and cursor
    persists `--model` to its own config (§9.7). An agent in the container can
    therefore read the host's agent credentials and write to those directories.
    The knob turns it off; an agent CLI that then cannot authenticate is the
    documented consequence, not a bug. *Amended 2026-09-14 (issue #366): the
    default is **false** until task 062 moves the agent into the container and
    turns it back on. Until then nothing in the container reads those
    directories, so the default of true handed them, writable, to the image and
    to step code for no benefit; setting the key still mounts them.*
    *Amended 2026-09-17 (task 062.2 decision 3): the default is **true** again,
    and this bullet reads as first written — the agent now runs in the
    container and needs them. They are mounted beneath a vincent home at
    `/vincent-home` that every containerized step runs with as `HOME`, so a
    command step reaches them exactly as the agent does (§12.3). One credential
    does not carry over: claude on macOS keeps its OAuth login in the Keychain,
    so a Mac host supplies `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`
    through `environment`, and that value is then in the container's
    environment. Codex's `auth.json` and Linux claude's `.credentials.json` are
    files in the mounted directories.*
  - **The daemon is reachable.** Every container is created with
    `--add-host=host.docker.internal:host-gateway` whenever it has a network at
    all, so a process inside it can call the daemon's MCP surface — with the
    same per-run token scoping §13.4 already specifies, and the same caveat that
    the endpoint is not a boundary. The per-step endpoint's *host rewrite* to
    `host.docker.internal`, which is what makes a containerized **agent** step
    use it, is task 062. *Amended 2026-09-17 (task 062.2 decision 1): the
    rewrite has landed, and on a Linux host with native Docker Engine the
    endpoint is not reached on loopback at all. The daemon binds a step-only
    listener on the container network's gateway IP that serves
    `/mcp/step/{run_id}` and nothing else, guarded by the per-run secret
    (§13.1, §13.4). That listener is reachable by **anything on that bridge
    network**, not only the task's container: other containers on the same
    bridge can connect to the port, and the per-run secret is what stops them
    using it. Where the bind is impossible (Docker Desktop, rootless podman) the
    daemon's loopback port is used as before.*
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
- **Listing a chat's skills starts the chat's agent CLI before the human has
  sent anything** (*added 2026-09-19, task 124.9, issue #505*).
  `GET /v1/chats/{id}/skills` (§5.5) spawns the adapter's listing probe on the
  host, in the chat's directory, as the invoking user, whenever the cache has
  no fresh answer — on opening a chat, before any turn. The probe runs no
  prompt and asks for no model output, but the CLI starts in a worktree whose
  contents the human may not have read, and it reads that directory's own
  configuration the way a turn would. For claude the probe suppresses hooks
  and MCP servers so that listing runs none of a project's code (124.7 owns
  that suppression; this is the posture it is held to). codex's
  `skills/list` loads its skills without running one (§9.3). A
  container-run linked chat is never probed on the host.
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

  *Amended 2026-09-02 (task 082, issue #310): there are two places now.* The
  daemon view's `i` writes `statusLine.command` in `~/.claude/settings.json`,
  which is the only way claude's usage windows reach vincent at all (§9.2,
  §9.6) — the alternative, reading the OAuth token out of
  `~/.claude/.credentials.json`, is refused by the v0 T1.7 decision. Every
  property that made the cursor case tolerable is held here deliberately: the
  write is **user-initiated** from the TUI and happens never on daemon start,
  never silently, and never as a side effect of running a task; the exact JSON
  is shown before it is written (§15); and it is **reversible** — the displaced
  `statusLine` object is carried base64-encoded in the command vincent
  installs, so uninstall puts back what was there, or removes the key when
  there was nothing. The command installed is the vincent binary rather than a
  shell script precisely so the daemon's bearer token is discovered the way
  every other subcommand discovers it instead of being written into a file
  (§13.2). Starting a daemon leaves that file untouched, and a test asserts it
  rather than assuming it.

  *Amended 2026-09-10 (task 095): three, and the third is not vincent's own
  write.* `vincent skills install`, and the daemon view's `S`, run `npx skills
  add` (§9.8) — the published CLI puts one copy under `~/.agents/skills/` and
  links it into each agent's directory. Vincent writes none of those files
  itself, which is the point of shelling out: the install layout, symlink /
  `--copy` split included, stays that tool's business. The properties above
  hold anyway — user-initiated by a command or a keypress and never on daemon
  start, with the **exact argv shown before it runs** — and the one property
  that cannot is stated rather than claimed: an uninstall is that CLI's, not
  vincent's, so `S` does not undo the way `i` does. **Detection** adds nothing
  to this list at all: it reads `SKILL.md` out of a public CLI's documented
  install locations and writes nothing, which is why it is not the v0 T1.7
  refusal being reopened (§9.8).

*Added 2026-08-29 (task 057).* **An agent can now create and cancel vincent
tasks.** §13.4 serves MCP from the daemon and, by default
(`mcp.wire_steps: true`), wires vincent's own agent steps to it. In
blast-radius terms this is not a new privilege — the posture above already says
an agent step runs arbitrary commands as the user, and a full-auto agent can
read `{data_dir}/token` and `daemon.json` and drive `/v1` with curl today — but
it is a change worth stating rather than leaving to be discovered. Three
specifics:

- **The §13.4 exclusions are not tools.** An agent cannot stop, back up,
  garbage-collect or reconfigure the daemon supervising it. It also cannot
  rewrite its workflows or triggers, inject a trigger event, open a pull
  request, forge a quota reading, permanently delete a project, task or chat,
  import a task from a backup, or drive a chat. *(Amended 2026-09-14, issue
  #377; 2026-09-17, task 117 — the import.)*
- **`restricted` does not restrict what a step does to vincent** (§9.4). The
  allow-list carries `mcp__vincent__*` in full, so a restricted step can create
  and cancel tasks. It bounds the filesystem and the shell, and that is all it
  claims to bound.
- **The per-step endpoint is not a security boundary.** `/mcp/step/{run_id}`
  carries a secret minted for one step run, and it exists to make `task_wait`'s
  deadlock refusal correct and to attribute provenance — not to confine the
  agent. A full-auto agent can read the daemon token and reach `/mcp` directly.
  It must not be documented, or relied on, as a sandbox. *Amended 2026-09-17
  (task 062.2 decision 1, issue #397): it is still not a sandbox, and it is now
  also the one thing vincent serves off loopback. For a containerized agent
  step the daemon binds a listener on the container network's gateway IP that
  answers **one path**, `/mcp/step/{run_id}`, guarded by the per-run secret;
  `/v1` and `/mcp` are `404` there. Anything else on that bridge network can
  reach the port, so on that listener the per-run secret is the whole of the
  access control, not a convenience. The listener exists only while a
  containerized step holds it (§13.4).*

The cursor adapter additionally writes `.cursor/mcp.json` into the **task
worktree** (§9.7), which extends this section's existing note about vincent
writing to cursor's own config. It is workspace-scoped and per-task; the user's
global cursor config is untouched.

*Added 2026-09-13 (task 096; decision record rows 33 and 34).* **Event triggers
invert this section's premise.** Everything above is defensible because **a
human pressed the key**, or wrote the workflow that pressed it: full-auto by
default, arbitrary commands as the invoking user, and a worktree that isolates
collisions rather than privileges. A trigger breaks that chain. A third party
labelling an issue, opening a pull request or turning a CI build red causes
agents to run as you, with nobody at the keyboard. That is why a trigger's
defaults differ from every other default in the product, and the list below is
the whole of the posture, not a set of tips.

- **Off twice by default.** A file under `{config_dir}/triggers/` does nothing
  until its own `enabled: true` **and** `triggers.enabled` in `config.yaml` are
  both on (§12.3). The second key exists because there is no keypress to be the
  consent, in contrast to task 069 decision 2. Both are marked dangerous: the
  TUI asks before turning either on, and neither can be changed over MCP
  (§13.4).
- **`on_fire: propose` is the default, everywhere.** A task a trigger creates
  lands in `paused`, and so does a task it retries or follows up (§6), so a human
  admits each one with `resume`. `on_fire: create`, which runs unattended, is
  always an explicit line in the file. A `cancel` has no held form, so it must
  write that line.
- **Agent steps are clamped `restricted`.** A triggered task is created with
  `restricted: true` (§5.3, §9.4) unless the file says `permission: workflow`,
  which is itself a dangerous value. The clamp only tightens. It bounds what the
  agent CLI's own permission model bounds, and nothing a step does to vincent
  (§9.4, and the task 057 note above). `limits.max_task_cost_usd` caps spend
  where the adapter reports cost. `limits.max_per_hour` caps how many deliveries
  fire, and an event over it is recorded and dropped, never queued.
- **`overrun: cancel_previous` lets an inbound event destroy in-flight agent
  work** *(added 2026-09-18, task 122, issue #483)*. Every unfinished task in
  the event's concurrency group is cancelled before the new one is created; the
  cancelled tasks keep their branches and worktrees, so nothing is lost from
  disk, but an agent mid-run is killed by something no human pressed. It is one
  of the values a client must confirm before writing, beside `enabled: true`,
  `on_fire: create` and `permission: workflow`. This raises what
  `allowed_actors` is worth rather than changing what it does: on a GitHub
  source a trigger that can match an event an outsider authors already has to
  name the authors it accepts, and with this mode that requirement is what
  stands between a stranger's comment and a cancelled run. The other four modes
  create nothing a `parallel` trigger would not have created, and destroy
  nothing.
- **Untrusted events need an allowlist, and the allowlist is the author.** On
  `github_issues` and `github_prs`, some events are ones an outsider can cause
  on a public repository: `opened`, `reopened`, `closed`, `ready_for_review` and
  `review_requested`. A trigger that can match any of them (including any
  trigger with no `match.action`, which matches every event) is **refused at
  load** unless it names `allowed_actors`. The trusted events are issue
  `labeled`, `unlabeled` and `assigned`, which need triage, and pull-request
  `merged`, which needs write. A state diff has no actor (task 096 decision 10),
  so `allowed_actors` matches the issue's or pull request's **author**. It stops
  a stranger's issue from starting work. It does **not** say who applied a label,
  requested a review or closed anything. CODEOWNERS requests reviews on an
  outsider's pull request, which is why `review_requested` is untrusted. The key
  is refused on `command`, `http` and `schedule` sources, whose events carry no
  identity vincent can verify: a `command` source's trust is whatever its
  script filters, an `http` source's is its signature, and a `schedule`'s is
  the file itself — nobody outside the machine can make a clock strike.
- **Prompt injection becomes remote.** An issue body, a pull-request title or a
  CI log reaches a trigger's templates as `.Event`. Whatever the file renders
  into a title, description, field, prompt or branch reaches an agent. The
  exposure already existed through `.Issue` (row 26), but there a human chose
  the issue and read it. `.Event` is never snapshotted onto the task (§8.4), so
  an attacker's text reaches only the fields the trigger's author chose to
  render. It still reaches those. Container execution (task 061, above) is the
  real control for unattended work: point `action.workflow` at a workflow whose
  steps are containerized, and read the bullet above on what a container does
  not yet confine.
- **No project scope.** Triggers are global and live in `{config_dir}`, never in
  `.vincent/`, so merge rights on a repository cannot start agents on a
  maintainer's machine (task 096 decision 8). The accepted cost is that a
  trigger cannot be reviewed alongside the repository it serves.
- **A trigger file is code the daemon runs as you.** A `command` source's argv
  runs on an interval with `notify.command`'s posture: argv, never a shell
  string; the §12.3 environment; and a whole-tree kill at the timeout. It may
  carry a token, which is why every trigger file is written `0600` (§12.2).
  `POST /v1/triggers/{id}/poll` runs it too. That route is an MCP tool because
  it runs only a command the user already configured, which a full-auto agent
  could run anyway.
- **The ingress needs the daemon token.** `POST /v1/triggers/{id}/events`
  requires the bearer token **and** the trigger's HMAC signature (§13.1, row
  34), so only a caller on this machine that can read `{data_dir}/token` and
  holds the secret can deliver. The loopback boundary is unchanged. No tunnel
  reaches the route without a relay on this machine that holds the token, and
  such a relay is already something running as you. The secret lives in the
  daemon's environment, never in the file.
- **Built-ins author triggers; only a human arms one.** *Added 2026-09-14 (task
  098).* The `create-trigger` and `update-triggers` built-ins (§5.2) let an
  agent, in a task a human started, write trigger files. They install only
  through `vincent trigger apply` (§12.1), which refuses every change that
  **arms** a trigger relative to the file on disk, a new file comparing against
  absent: `enabled` to `true`, `on_fire` to `create`, `permission` to
  `workflow`. An already-armed value may be kept, disarming is always allowed,
  and there is no override flag. The fourth switch, `triggers.enabled`, is in
  `config.yaml`, which apply cannot touch. A human arms in the TUI, which asks
  first, or in `$EDITOR`. Apply also refuses a file for any project but the
  one it was given and a file that changed since the proposal read it, so a run
  in one project cannot rewrite another's triggers. `update-triggers` may
  rewrite a trigger that is already armed, and such a rewrite is live once
  written, so its proposal waits at a `manual` gate. The trigger write routes
  stay off MCP (§13.4). A proposal is staged in `{data_dir}` (§12.2), never in
  a worktree, because a trigger's argv can carry a token.

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
  burned it. *Amended 2026-09-17 (task 119, issue #472):* a chat **linked to a
  task** (§5.5) keeps its turns' tokens and cost on the chat, in `chat_turns`,
  so they are **not** in this rollup and do **not** count toward
  `max_task_cost_usd` — the trade the issue's rejected multi-turn repair would
  have avoided, accepted so that a conversation stays a chat.
  *Amended 2026-09-17 (task 116, issue #409): the figure also rolls
  up over a tree.* When `max_tree_cost_usd` (§12.3) is set, the engine sums it
  over the task's whole fan-out tree — the root and every descendant at any
  depth — at the same boundary and blocks `tree_cost_limit` (§18) once that sum
  is over. Archived descendants count, and so do the lanes a follow-up round
  spawned, because they are rows of the tree like any other. §13.2 serves the
  descendants' part on the parent as `children.cost_usd`, and the TUI's detail
  view adds the task's own to show a **tree cost** on any task with children.
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
  rather than by name. *Amended 2026-08-31 (task 067, issue #269):* an
  **ended chat**'s turn transcripts (§5.5) — *amended 2026-09-01 (task 074): both
  terminal states, `archived` and `handed_off`, measured from the same
  `updated_at`; a handed-off chat's transcripts live under `chat-{id}`, which the
  task that inherited its worktree never claims, so pruning them cannot reach
  task-owned state* — are pruned by the same pass under
  the same key, measured from when the chat was archived. The pruner walked
  archived tasks alone until then, so a chat's transcripts outlived every
  retention window. *Amended 2026-09-17 (task 119): all three terminal states
  now, `closed` included; a linked chat's transcripts live under `chat-{id}` like
  any chat's, never under the task's directory.* *Amended 2026-09-11 (task 096):* a second row exception —
  `trigger_deliveries` and `trigger_backlog` (§14) rows are pruned after a **fixed 30 days** by the
  same pass, on the same terms as `idempotency_keys`: no config knob, and
  independent of `transcript_retention_days`. A month answers "why did my
  trigger not fire last week?" while bounding a table that grows with every
  poll's events (task 096 decision 13). *Amended 2026-09-17 (task 117,
  issue #411):* a task imported from a backup (§13.2) has `archived_at` stamped
  at import, so its retention window restarts. Keeping the backed-up value would
  prune the transcripts of any task archived more than
  `transcript_retention_days` ago on the next pass — and the transcripts are the
  reason to import it.
- **Scheduled backups** (*added 2026-09-17, task 115*): a timer wired in
  `daemon.Run` beside the retention pass above takes the `vincent daemon backup`
  archive every `backup.interval` into `backup.dir`, and after each successful
  run deletes its own archives beyond the newest `backup.keep` (§12.3). It
  shares no knob with `transcript_retention_days`, and it never prunes rows or
  transcripts: it copies them. It is off until `backup.interval` is set. A
  written archive is logged at info with its path and size, a failed run at
  error, and a failed prune at warn. The last attempt is served as `GET
  /v1/doctor`'s `backup` group (§13.2).
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

  *Amended 2026-09-10 (task 095).* The report grows a `skills` group (§9.8): the
  agent skills this repository publishes, with the version shipped, the version
  installed in the global store and the agents each is linked into. It is a
  **row, not a problem** — the closed unhealthy set of task 006 decision 7 does
  not move for it, `vincent doctor` still exits 0 with every skill missing, and
  the repair lives in `vincent skills install` rather than in `--fix` because it
  is a client-side write into the user's own agent directories and every `--fix`
  repair is a daemon-owned one.

  *Amended 2026-09-17 (task 115): a failed scheduled backup is a problem, which
  amends task 006 decision 7.* The report grows a `backup` group (§12.3,
  §13.2), and when backups are on (`backup.interval > 0`) and **the most recent
  attempt failed**, `Report.Evaluate` adds a `backup` problem carrying the error
  and `vincent doctor` exits 1. The next successful attempt clears it. When
  backups are off there is never a problem, and neither is there on a local
  report with no daemon, which shows the group `known: false` as it shows the
  database group. This widens decision 7's closed unhealthy set on purpose, and
  it differs from the GitHub, update and skills rows, which never move the exit
  code. Those rows report defaults nobody chose. This one reports a feature the
  user switched on, so it cannot fire "on almost every machine", which is what
  decision 7 guards against, and a backup that fails silently is found out on
  the day it is needed. An info-only row was the alternative and would have paid
  exactly that cost. **Overdue alone is not a problem**: a daemon that was down
  missed a run but nothing failed, and it runs at the first check after it
  starts. A failed prune after a successful run is not a problem either.

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

**What triggers log (task 096, added 2026-09-13).** A trigger's durable record
is its ledger (§14) and its poll status on `GET /v1/triggers`, and the log
carries the rest.

- **Truncated catch-up is warn, once.** A poll that returned more events than
  the catch-up cap of 20 is logged at **warn**, naming the trigger, how many
  events the poll returned and how many were judged. It is logged **once per
  trigger per daemon run**, beside the four warn-once lines above: a trigger
  that is behind stays behind for many polls, and one line says so where a line
  per poll would bury everything else. The events past the cap are dropped, not
  deferred.
- **Poll failures.** A failing poll command (a non-zero exit, a timeout, or too
  much output) is logged at warn on every poll. A failing GitHub listing is
  recorded only in the poll status. Either way the transition is a
  `trigger.poll_changed` event (§13.3).
- **Other warn lines.** A seed event whose `dedupe_key` does not render, a
  command output line that is not an event, and a cursor or `seeded` row that
  could not be written are each logged at warn. A delivery that could not be
  recorded mid-poll is logged at error, and the poll reads failing.
- **Info lines.** A seed is logged at info with the count it saw, and a poll
  command's stderr at info, one record per line.

## 18. Edge cases and errors

| Case | Behavior |
|---|---|
| Workflow file edited mid-task | Irrelevant — execution uses the task's snapshot |
| Workflow deleted before task creation | Creation fails: `workflow_not_found` |
| Agent CLI missing at step start | Step fails (retry policy applies) with a `agent_unavailable` reason; typically → blocked |
| A fan-out lane's merge conflicts | The join stops at that lane and the task blocks `merge_conflict`, with the worktree left conflicted so a human resolves in place (*added 2026-08-17, task 014*). `on_conflict: agent` tries a resolver first, gated by its `check`, and falls back to the same block. Archive gets no special case: a conflicted worktree is dirty by construction and §10 already refuses a dirty worktree without confirmation |
| A fan-out lane ends without finishing | The join blocks `lane_failed` and merges **nothing** — a partial merge is indistinguishable downstream from a complete one. `retry` re-checks the lanes; the remedy is to fix the child, which is an ordinary task (*added 2026-08-17, task 014*). *Amended 2026-09-01 (task 080):* "nothing" is **nothing of that round**. Rounds an earlier wave already merged stay on the branch — they were there before this one was known to fail, and resetting them would destroy integrated work — and no further lane is spawned while in-flight ones are left to finish. Reachable only with `needs:`, since a lane list without it is a single round and is unchanged. *Amended 2026-09-02 (task 081):* under `schedule: eager` there is no round — the admission that finds the lane settled without finishing merges **nothing new**, and lanes merged by earlier admissions stay (§7.6, §11) |
| A workflow's includes are cyclic, unresolvable or too deep | Refused at task creation with a `400` naming the cycle path, the missing workflow, or `include.max_depth`. A callee bringing a step id the expansion already uses, and one whose `platforms:` excludes this host, are refused there too. Possible because expansion happens in the insert path (*added 2026-08-19, task 019*) |
| A fan-out tree is cyclic or too large | Refused at task creation with a `400` naming the cycle path or the bound crossed (`fan_out.max_depth`, `fan_out.max_tasks`). Possible because the whole tree's shape is static once lane lists are in the snapshot (*added 2026-08-17, task 014*). *Amended 2026-09-01 (task 080):* a **derived** lane list is not static, so it is `fan_out.max_tasks` alone that moves — to spawn, as the `fan_out_limit` row below. The cycle path and `fan_out.max_depth` are still refused here with the same `400`, because the lane's `workflow:` is still resolved at creation (§7.6) |
| Two sub-steps of a `parallel` group write the same file | Undefined: the group shares one worktree, and §10 isolates working trees between *tasks*, not processes within one. A workflow bug, documented as such rather than arbitrated (*added 2026-08-17, task 014*) |
| Option probe fails (help unparseable) | `GET /v1/agents` serves the curated catalog with `probe_error` set; selection and free text keep working (§9.6) |
| Model/effort unknown to the catalog | Validation warning only; the CLI is the final authority — a rejected value fails the step with the CLI's error (retry policy applies) |
| Model *in* the catalog but rejected at run time | Real, not hypothetical, on cursor (§9.7): the step fails with the stderr tail as the message, since no `result` event arrives. Catalog membership is advisory in both directions |
| Agent CLI installed but not authenticated | `logged_in: false` where the adapter can tell (§9.5); the new-task form flags it like an unavailable agent. Where it cannot (`null`), the step runs and fails. *Amended 2026-08-14 (task 003):* where the adapter recognizes the CLI's auth wording, that failure is now named `agent_unauthenticated` instead of surfacing as `nonzero_exit`/`agent_error`. Everything else about the row is unchanged and deliberately so — the step still runs, the attempt still fails, the §7.2 budget still applies, and the task still ends up blocked. There is no pre-flight refusal on `logged_in: false`. *Amended 2026-08-15 (task 005):* the "where it cannot (`null`)" set is now **claude alone** — codex probes `login status`, cursor probes `status` (§9.5). Every other clause of this row stands untouched, task 003 decision 4 included: making the state visible is not the same as blocking on it, and `vincent doctor` is where a user sees it before a task burns its retry budget. *Amended 2026-09-16 (task 107):* the "where it cannot (`null`)" set is now **empty by design** — claude probes `auth status` (§9.5). `null` is left to a probe that could not answer and to a claude outside `[2.1.41, 3.0.0)`, and for those this row's "the step runs and fails" still holds. Task 003 decision 4 stands |
| Agent stopped by a usage limit | *Added 2026-08-14 (task 003).* Where the adapter recognizes the wording, the attempt is recorded `interrupted` with reason `usage_limit`, consumes **no** retry (§7.2), and the task returns to `queued` with an admission hold (§11) — releasing its slot, so other work keeps running. The hold ends at the reset time the CLI reported, or `usage_limit_recheck_interval` after the stop when it reported none. Recovery is unattended: the scheduler re-admits and the step re-runs. The board says `queued` *with* its reason rather than `blocked` (§15). Where the adapter recognizes nothing — codex and cursor today (§9.1) — the run reads as `nonzero_exit`/`agent_error` exactly as before. *Amended 2026-08-24 (task 026):* the reset the engine acted on is additionally recorded per adapter (§14) and published on change (§13.3), so the fact outlives the hold — `admit_not_before` is cleared by the next transition out of `queued`, and until now the observation went with it. It is retired by the next successful agent step on that adapter, never by a timer. *Amended 2026-09-08:* the wording is only ever read from a run that **failed** (§9.1) — a step that succeeded while writing about quota stops is a success, not a wall. *Amended 2026-09-08 (task 091):* all of the above is what `usage_limit_auto_continue: always` — the default — does. Under `never`, and under `reported_only` when the CLI named no reset, the same stop **blocks** the task instead with `block_reason: usage_limit` (§7.2, §12.3): the attempt is still `interrupted` and still costs no retry, the cursor does not advance, and a human retry re-runs the step. *Amended 2026-09-16 (task 106):* in the modes that hold, every **other** task on the adapter is held too, at its next agent spawn, with no attempt recorded and no process started, until the observation's `resets_at`. When that passes the §11 walk releases them all under the normal caps |
| `effort` set on a step whose agent has no effort concept | Ignored by the adapter and documented as ignored (cursor, §9.7); a claude/codex effort value on a cursor step is already an §8.2 *error* — it belongs to another adapter's catalog |
| `restricted` step on an adapter that cannot restrict on this OS | Step fails to start with `restricted_unsupported` (cursor on Windows, §9.7), under the retry policy → typically blocked. Never downgraded to full-auto, and deliberately *not* `agent_unavailable`: the CLI is installed and healthy, so "not found" would send the user to reinstall what is already there. *Amended 2026-08-28 (task 041):* **task creation refuses these** with a `400` naming the step and the agent (§9.4), and `GET /v1/agents` publishes the `restricted_verdict` the gate uses. Reaching the engine anyway means the task and its daemon parted company — a data directory carried to Windows, or a workflow edited after the task was queued — so the reason above stays exactly as it is, as the backstop. Retries are not gated: enforcement is creation-time, and the backstop is what catches the rest. *Amended 2026-09-11 (task 096):* a task created with the `restricted` clamp (§9.4) makes **every** agent step a restricted one, so the same `400` refuses a clamped task whose agent steps resolve to such an adapter — even when its workflow says `full-auto` throughout |
| Step declaring `on_input: require` on an agent that cannot ask | *Added 2026-08-17 (task 013).* A workflow pinning an adapter with no control channel (codex, cursor) fails §8.2 validation outright. Otherwise creation is refused with a `400` naming the step and the agent, and the TUI's picker will not select that agent; `GET /v1/agents` publishes the `input_verdict` the gate uses. A task that reaches the engine anyway — claude upgraded past the §9.3 ceiling, a data directory moved — fails the attempt with `input_unsupported` under the §7.2 budget, before anything is spawned. Only a positive "cannot" refuses: an absent or unprobed binary is unknown, and unknown never blocks (§9.6) |
| Workflow restricted to platforms this host is not | *Added 2026-08-16 (task 010).* Creation is refused with a `400` naming the restriction and the host (§8.1.1); the entry stays listed and says why, and the TUI's picker will not select it. A task that *already* holds such a snapshot — the data directory moved to another OS, or the workflow narrowed after the task was queued — blocks at admission with `platform_unsupported`, before a worktree or any step. Not `invalid_snapshot`: the snapshot is valid, just not here |
| Runaway step output (agent or command) | Past `transcript_max_bytes` (§12.3) the process tree is killed and the attempt fails `transcript_limit`, under the retry policy. The line that trips the cap is written **whole** — a truncated line would turn a size failure into a parse failure for every later reader of the JSONL — and the partial transcript is kept with a closing `vincent.transcript_limit` annotation, because the lines that got there are what explain the runaway |
| A task spends past `max_task_cost_usd` | *Added 2026-08-26 (task 033).* The task goes `blocked` with `block_reason = cost_limit` and nothing further runs. It is a **block, not a step failure**: the finished `step_run` keeps its own state and its own reason, no retry is consumed (§7.2), and a retry that was already due does not run — retrying spends more money to arrive at the same wall, and that pre-empts `retry_backoff` too. The check happens at every **attempt boundary**, including inside a `loop` body and a `parallel` group, so the attempt that crossed the line ran to completion and the overshoot is at most one attempt: cost arrives on an agent run's terminal result line and nowhere else (§9.1), and there is no mid-run usage signal to poll. The remedy is to raise the cap (hot-reloaded, §12.3) and `retry`; a `retry` **without** raising it makes exactly one attempt of progress and blocks here again, which is idempotent and loses no work. `resume` is not the escape hatch — it is valid only from `paused` (§6). The cap counts one task, so each `fan_out` lane carries its own budget (*amended 2026-09-17, task 116:* the tree's shared budget is `max_tree_cost_usd`, the next row), and it is inert on codex and cursor, which report no cost at all (§9.3, §9.7). *Amended 2026-09-11 (task 096):* the cap is the **lower** of config's and the task's own `max_task_cost_usd` (§5.3, §12.3), and the block is `cost_limit` whichever side set it. A task cap cannot lift the global one. The task's value is fixed at creation — no route changes it — so when it is the lower side, raising config's does not move the wall, and `retry` makes one attempt of progress per press as above |
| A tree spends past `max_tree_cost_usd` | *Added 2026-09-17 (task 116, issue #409).* The task whose attempt took the tree's total over the cap goes `blocked` with `block_reason = tree_cost_limit`. The tree is the root and every descendant at any depth, archived ones included, and the total is the lifetime §17 rollup over all of them (§12.3). Everything the row above says about the boundary holds here: it is a block, not a step failure, the finished `step_run` keeps its own state and reason, no retry is consumed, a due retry and a `retry_backoff` hold are pre-empted, and the check runs inside a `loop` body and a `parallel` group. The blocking task is usually a lane, but can be the parent: its own steps before the fan-out, after the join, or a `merge.on_conflict: agent` run. A parent parked in `awaiting_children` makes no attempts, so it never blocks this way while parked; its join stays open on the `blocked` lane, which `children.blocked` names beside `children.cost_usd` (§13.2). **The overshoot is at most one attempt per task still working in the tree**, not one attempt: a lane running when the total crosses finishes the attempt it is on, a queued one makes one attempt when admitted, and each blocks at its own boundary. The remedy is to raise `max_tree_cost_usd` (hot-reloaded) and `retry`, either the parent, whose cascade (§6, task 090) re-admits every blocked lane in one call, or a single lane. Without raising it, a retry on a lane buys that lane one attempt and re-blocks, and a cascade retry buys **one attempt per blocked lane** per press. When both caps are over at the same boundary the block is `cost_limit`, because the per-task cap is the one raising this key cannot clear. A repair run still ends with the reason the task was blocked with (task 033 decision 3). Inert unless some step run in the tree reported a cost; a tree mixing claude with codex or cursor lanes counts only what was reported, and that undercount is never filled in from token counts (§9.3, §9.7) |
| A command emits a single line larger than one output record | *Added 2026-08-24 (#139).* Captured, not failed: the line becomes a run of `vincent.output` records marked `partial`, in order, on one stream, preserving phase, stream identity and live offsets. Minified JSON, a base64 blob and a `git diff` of a generated file all reach a megabyte on one line, so this is an ordinary command; failing it would only retry it into the same wall until the task blocked. It was previously a *silent success* — a line-bound reader stopped dead on the first such line, the rest of the stream went to `io.Discard`, and the attempt was judged from exit 0 alone |
| A transcript write, encode or close fails | *Added 2026-08-24 (#139).* The failure latches on the transcript and the attempt fails `transcript_io_error` under the §7.2 budget — disk full, a revoked permission, a short write, and ENOSPC surfaced at `Close`, which is where a buffered filesystem reports it. Never swallowed by `allow_failure:` (§7.2): vincent failing to record a step is not an outcome the step produced. Only a *success* is overridden — an attempt that already failed keeps the more useful reason. `transcript_max_bytes` is unaffected and stays the only size-based failure (§12.3) |
| An adapter cannot read its agent's stream to the end | *Added 2026-08-24 (#139).* The adapter latches its reader's error, drains the pipe so the CLI is not left blocked on it until the step timeout, and reports `agent.FailureStreamError`; the engine fails the attempt `agent_protocol_error` under the §7.2 budget. Deliberately not `agent_error`, which means "the CLI reported a failure" and would send a user to inspect a CLI that did nothing wrong — the reader that failed is vincent's. Deliberately not `input_protocol_error` either: that names a control message vincent could not render, and such a message arrived intact |
| Transcript of an archived task past retention | Deleted by the pruner at daemon start and every 24 h (§17). The **pruner** never deletes DB rows — a human's `DELETE /v1/tasks/{id}` does, and takes the transcript directory with it (§13.2, task 092, amended 2026-09-09); retention is measured from `archived_at`, so a long-running task archived yesterday is one day old. `transcript_retention_days: 0` disables pruning entirely |
| Base branch doesn't exist | Task creation fails fast |
| Branch already exists (or a ref hierarchy conflict blocks the name) | Rejected at creation with `400` where the name is known then; otherwise the task blocks with `branch_exists` at admission, which stays the authority. Never reused, never auto-renamed. Recover with `retry { branch_override }` (§10, task 001) |
| A pull request's head cannot be fetched | *Added 2026-08-30 (task 064).* A task created from a pull request runs on that pull request's head branch, so the fetch has nothing to fall back to. The task blocks with `pull_fetch_failed` at admission — deliberately unlike §10's base fetch, which is silent because a local base is always a valid answer |
| A branch push was rejected by the remote | *Added 2026-08-31 (task 069).* `push_rejected`. A non-fast-forward, a protected branch or a declined pre-receive hook — git reports all three by rejecting the ref, and vincent does not force past any of them, for the reason `pull_branch_diverged` gives: the local branch may hold commits nobody pushed. No pull request is created and nothing on the remote changes |
| A branch push could not authenticate | *Added 2026-08-31 (task 069).* `push_no_credential`. The push runs with `GIT_TERMINAL_PROMPT=0`, so a credential helper that wants a terminal fails here rather than parking a request handler on a prompt nobody can answer |
| A GitHub write's credential may not write | *Added 2026-09-15 (task 068.4).* `no_write_scope`: any HTTP 403 on a write that is not a spent rate limit — merge, close, reopen, comment, re-run, and pull-request creation, whose fallback carries it where it carried `forbidden` before. `forbidden` stays the read side's reason. A 404 stays `not_found`, though GitHub sometimes answers a missing write permission with one rather than reveal a private repository |
| A merge GitHub would refuse | *Added 2026-09-15 (task 068.4).* `not_mergeable`: the pull request is closed or already merged (which is what refuses a double merge), a draft, conflicted, or blocked by something other than a running check. Found by the preflight read, before anything is sent; GitHub's own refusal after an `UNKNOWN` merge state maps here too, and its text reaches only the daemon log |
| A merge blocked on running checks | *Added 2026-09-15 (task 068.4).* `checks_running`: the merge is blocked and a check on the head commit has not concluded. Nothing is sent; waiting is the fix |
| A merge whose branch is behind its base | *Added 2026-09-15 (task 068.4).* `branch_behind`: the repository requires the head to be up to date before merging. Nothing is sent |
| A merge whose head moved | *Added 2026-09-15 (task 068.4).* `head_changed`: the live head is not the `head_sha` the human confirmed. Refused by the preflight, and — for a push landing after it — by the pin on the send itself. Nothing is merged: that would be a green build for code nobody ran |
| A branch push failed for any other reason | *Added 2026-08-31 (task 069).* `push_failed`. Unreachable host, refused connection, or no answer inside `gitx.RemoteTimeout` |
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
| Scheduled backup fails | *Added 2026-09-17 (task 115).* Any failure of a timer run (§12.3) — the database copy, the tar, the rename into place, a `backup.dir` inside `{data_dir}/transcripts` or `{config_dir}/workflows` — is logged at error, removes its staging directory so no file ever carries a scheduled archive's name half-written, prunes nothing, and is retried **one hour later** rather than at the next check. The status carries the error as `last_error`, and `GET /v1/doctor` raises a `backup` problem, so `vincent doctor` exits 1 until an attempt succeeds (§17). No task is touched. A daemon killed mid-run leaves a `.vincent-backup-*` staging directory, which the timer sweeps when it next starts. A prune that fails after a successful run is logged at warn and reported as `prune_error`, not as a problem |
| Backup directory unwritable or disk full | *Added 2026-09-17 (task 115).* A failed attempt, handled exactly as the row above: a `backup.dir` that cannot be created `0700` or written, or a disk that fills during the copy or the tar, fails the run and removes the partial work with its staging directory. **Nothing already kept is lost**, because pruning runs only after a success. The default `{data_dir}/backups` is on the database's disk, so a disk that fills there also threatens `vincent.db` and transcripts, and it gives no protection against losing that disk: pointing `backup.dir` at another disk is the remedy for both |
| User wants back one deleted task | *Added 2026-09-17 (task 117, issue #411).* `vincent task import <archive.tar.gz> <task-id>` copies the task from a backup taken before the delete back into the running installation, with its id, its step runs and its transcripts, `archived`. It refuses rather than replacing or merging anything: a live task with that id, a task that was not archived in the backup, a fan-out lane whose parent is not here, a stray `transcripts/{id}/` directory, and a project that no longer matches by id and name unless `--project` names one. A whole-installation rollback is still `daemon restore` |
| DB corruption | Startup fails loudly, points at the file, never auto-deletes. *Amended 2026-08-25 (task 030):* what rescues this case is an **earlier** good copy, which is what `vincent daemon backup` is for; a fresh copy of the damage is not a remedy, and taking one is not offered as a cold-copy mode |
| Agent emits gigabytes of output | Transcript writes are streamed to disk; SSE output chunks are rate-limited/coalesced (~10 Hz); per-run transcript size cap (`transcript_max_bytes`, default 512 MB) fails the step past the cap with `transcript_limit` |
| Template references missing field | Step fails at render time (before any process starts) with the template error. *Amended 2026-08-28 (task 044):* this outcome is now reachable without creating a task — `vincent workflow render <file>` executes the same templates against the §8.4 preview context and names the step and the field |
| A step's `if:` does not render, or renders something that is not `true`/`false` | *Added 2026-08-18 (task 015).* The step blocks with `condition_error` and records one `failed` row naming it. The only reason in this table that does **not** run the §7.2 retry budget: a guard is evaluated before the step becomes an attempt, so there is no attempt to retry, and re-rendering an unchanged template cannot answer differently (§7.7). A human `retry` re-evaluates it |
| A guard skips a step | *Added 2026-08-18 (task 015).* A `skipped` row with `skip_reason: condition`, visible in `.Steps`; the workflow carries on. The same guard on a fan-out lane or a group sub-step subsets the set instead — the others still run |
| A `condition` step's guard is false | *Added 2026-08-18 (task 015).* The run ends there: one `stopped` row, the cursor moves to the end of the step list, the task is `done`. The steps after it record nothing, because they were never considered. *Amended 2026-08-18 (task 016):* inside a `loop` body the same step ends **that iteration** and the loop carries on — the sequence it ends is the body's (§7.8) |
| A `loop` cannot run within `max_iterations` | *Added 2026-08-18 (task 016).* The task blocks with `loop_limit`: a `for_each` list longer than the ceiling blocks before iteration 1 naming the count, and a `count:` the ceiling moved under (config lowered while the task was queued) blocks too. It does not truncate and does not advance — running out of tries is not a decision, and advancing would hand every downstream guard a `.Steps` that says the work is finished (§7.8) |
| A derived `fan_out` list is not a lane list | *Added 2026-09-01 (task 080).* The step blocks with `fan_out_invalid` having spawned nothing, naming the fault: an item that is not a JSON object (naming the line), an id that is not a slug, two items rendering to one id, or a `needs:` edge to a lane the step does not declare or a cycle among them. Every one of these is a **load-time** error for a declared `lanes:` list; a derived list has no ids until it is rendered, which is why the identical check blocks here |
| A derived `fan_out` list is too long | *Added 2026-09-01 (task 080).* The step blocks with `fan_out_limit` having spawned nothing: a list past the step's `max_lanes:`, or a tree that would pass `fan_out.max_tasks`. `fan_out.max_depth` is unaffected — it counts nesting, and a dynamic width does not nest. It is §7.8's `loop_limit` for the other dynamic step, and it exists because a derived width cannot be counted at task creation (§7.6) |
| A `break` step's guard is true | *Added 2026-08-18 (task 016).* The loop ends there and **succeeds**: one `stopped` row, the cursor advances past the loop step. A false guard records `succeeded` and the body carries on |
| A step's retry is paced by `retry_backoff` | *Added 2026-08-25 (task 028).* The attempt is recorded `failed` with its own reason and consumes a retry, and the task returns to `queued` with `queued_reason: retry_backoff` and an `admit_not_before` of `now + retry_backoff` (§7.2, §11) — releasing its slot, so other work keeps running. Recovery is unattended: the scheduler re-admits and the same step re-runs with the budget the recount says is left. Distinct from `usage_limit`, whose attempt is `interrupted` and costs nothing, so a reader can tell a quota wall from a flaky step. It never becomes a `block_reason`: when the budget *is* spent the task blocks with the step's own failure reason, with no wait first |
| A loop body step exhausts its retry budget | *Added 2026-08-18 (task 016).* The iteration fails and the task blocks with **that step's own** reason, not `loop_limit`. `allow_failure:` (§7.2) is how a probe's red result becomes data a `break` can read instead |
| The daemon dies mid-iteration | *Added 2026-08-18 (task 016).* §12.4 finalizes the running row as `interrupted`, and the re-admitted loop derives its position from the rows: body steps whose latest attempt succeeded are skipped, and it continues **mid-iteration**. *Amended 2026-09-15:* a `break` or `condition` is never skipped that way — its `succeeded` row is a guard's answer, and it is asked again. And once a body step runs again, every body step after it in that iteration runs again too (§7.8, task 016 decision 7 reopened). Iterations that already have rows keep the `for_each` item those rows recorded; only new iterations draw from a re-derived list (§7.8) |
| Every lane of a `fan_out` is guarded off | *Added 2026-08-18 (task 015).* A no-op success: the step records a row saying no lane was selected and advances. It must not park — a parent in `awaiting_children` with no children would be re-queued, spawn nothing and park again (§7.6) |
| `answer` posted when task isn't `awaiting_input` | `409` with the current state (standard invalid-transition handling) |
| Agent process dies while `awaiting_input` | Attempt fails with its exit code (retry policy applies); `pending_input` cleared |
| `input_timeout` expires | Process killed; attempt fails with reason `input_timeout`; normal retry/blocked policy (§7.2) |
| Unparseable/unknown control request from an agent | Transcripted verbatim; attempt fails with `input_protocol_error` (retry policy applies) — vincent never waits on a request it can't render |
| A chat's worktree is mid-merge or mid-rebase when it is handed off | *Added 2026-09-01 (task 074, issue #288).* `409 repo_operation_in_progress`, with `details.operation` naming which of merge, rebase (either backend), cherry-pick, revert or bisect — probed from the repository's own state files through the linked worktree's real git dir. Nothing is written: the chat stays `idle` and keeps its §10 claim. Ordinary dirty state is **not** refused — preserving it is the point of a handoff — and a chat with no `worktree_path` is a *different* `409`: it has nothing to hand over, and a task created with an empty path would have admission quietly cut a new worktree instead (§5.5) |
| A §6 action on a task an open linked chat has locked | *Added 2026-09-17 (task 119, issue #472).* `409 task_locked_by_chat` with `details.chat_id`, for every action but `cancel`, from the API, the CLI, the MCP `task_*` tools and trigger reactions alike. Nothing is written — the refusal is decided inside the compare-and-swap, and an action with a side effect ahead of its swap (`branch_override`'s rename, the decision row `skip`, `approve` and `reject` write, archive's worktree removal) is refused before it. Close the chat, then act. A fan-out parent's retry cascade skips such a lane rather than failing (§6) |
| A chat is opened on a task that has no worktree | *Added 2026-09-17 (task 119).* `409 task_has_no_worktree` — a task blocked on `branch_exists` or `base_branch_missing`, or aborted before admission. The daemon does not create the worktree for a chat: preparing it is the engine's, on an admission. `retry` (with `branch_override` where that is the cause) or `follow_up` gets the task a worktree, after which a chat can be opened. A turn of an open linked chat whose task has somehow lost its path fails the turn the same way |
| `archive`, `handoff` or `DELETE ?delete_branch=true` on a linked chat | *Added 2026-09-17 (task 119).* `409 chat_linked_to_task` with `details.task_id`, naming the task that owns the worktree and branch. Archive and hand-off are not in a linked chat's transition table (§5.5); the delete is refused because the chat's copy of the branch name is history, never an authority. Close the chat instead — a closed chat may be deleted without `delete_branch` |
| A linked-chat turn's task runs in a container that is gone | *Added 2026-09-17 (task 119).* The turn fails and the chat returns to `idle`, still open. It is never moved to the host: the operator confined that worktree (§16) |
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
  the pieces deliberately left out of the first cut. Two are now done:
  ~~**codex `exec resume <thread_id>`**~~ — **landed 2026-08-31 (task 070,
  issue #268)** — and ~~**cursor `--resume`**~~ — **landed 2026-08-31 (task
  072, issue #283)** — each pinned to a captured fixture from a named build,
  codex-cli 0.150.1 and cursor-agent 2026.08.11-e8db854, which is the
  condition this entry and task 063 decision 3 both attached (§9.3, §9.7). No
  shipped adapter is refused at chat creation any more; the
  `agent_cannot_resume` path itself is kept, is still the contract for the
  next adapter, and is proven against a stub. Still open: a
  **`notify.chat_on` key**, if
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
- ~~Dynamic per-item fan-out~~ — **promoted out of future work, 2026-09-01**
  (§7.6, task 080, issue #301): `for_each:` and a single `lane:` template on a
  `fan_out` step, each line a JSON object. The named trigger — an answer to
  "what replaces the creation-time bound" — is task 080 decision 6: for a
  *derived* lane list only, the task-count bound moves to spawn time, as the
  step's `max_lanes:`, the run-time `fan_out.max_tasks` check and the
  `fan_out_limit` block (§18). The cycle check and `fan_out.max_depth` stay at
  creation for a derived list too, and a static lane list keeps all of §7.6's
  creation-time checks.
  *Corrected 2026-09-14 (issue #378):* this entry still read as future work
  after task 080 landed.
- **A template FuncMap for §8.4** — `hasSuffix`, `contains`, `split`, `trim`,
  `default`. `text/template` builtins are all any template gets today, which
  `for_each:` makes felt: `.Loop.Item` is a string authors immediately want to
  test by extension or path segment, and the answer is to filter at the source
  (`git diff --name-only … | grep -v _test.go`). A FuncMap lands in *every*
  prompt, check, run and guard at once and invites the expression-language
  argument 015 decision 4 settled, so it earns its own task. The trigger is
  the first `for_each` that cannot filter at its source.
- ~~**Scheduled and recurring triggers**, as a `type: schedule` trigger source
  (task 096, *Explicitly not in scope*; recorded 2026-09-13)~~ — **promoted out
  of future work, 2026-09-18** (§12.2, §13.2, §15; decision record row 33,
  task 121, issue #480): the reopening condition this entry named came due, and
  the entry's own prediction held — it is one more `source.type`, not a
  subsystem. `match:`, `if:`, `dedupe_key:`, all four action types, the
  `propose` gate, the `restricted` clamp, `limits.max_per_hour`, global scope
  and the never-arm rule are reused verbatim. Task 096
  designed the `action:` block so that such a source reuses it verbatim, so the
  day it is built it is one more source type, not a subsystem. Two more pieces are deferred
  from the same task, each with its named trigger. A real **actor** on GitHub
  events, from the timeline API (task 096 decision 10), waits for the first
  need for a trustworthy actor. **Signature schemes** beyond
  `github_hmac_sha256` wait for the first sender that signs differently; the
  schema takes a scheme as one more value.
- LLM-as-judge verification as an optional third success layer.
- Multi-user / remote daemons / fleet view across hosts.
- Task templates & recurring tasks; ~~issue-tracker ingestion (Jira → task)~~ —
  **the GitHub half promoted out of future work, 2026-08-26** (§5.3, §8.4,
  §12.3, §13.2, §14, §15; decision record row 26, task 035): a task can be
  created *from* a GitHub issue, which prefills it and reaches templates as
  `.Issue`. **Issue ingestion writes nothing to GitHub** and still does not; the
  one write vincent has is pull-request creation, added by task 069 below — and the issue is snapshotted at creation rather
  than re-fetched, so the step path is unchanged. Jira and task templates stay
  deferred; nothing here was a decision *against* them, only a v1 scope line,
  and v1 shipped. *Retired 2026-09-13 (task 096, decision record row 33):* the
  ingestion line is done, and for more than Jira. Event triggers start work,
  with nobody picking, from three kinds of source: any system with a pollable
  API through a `type: command` source, GitHub issue and pull-request
  transitions, and a signed push from the same machine. Vincent still ships no
  per-vendor adapter and stores no vendor credential (§2). **Task templates and
  recurring tasks stay deferred** (the entry below names the trigger for the
  recurring half). *Amended 2026-09-18 (task 121, issue #480):* the recurring
  half is answered, by a `type: schedule` trigger source rather than by a
  second scheduler beside `internal/scheduler`. **Task templates stay
  deferred.** ~~**Pull requests** — checking, listing or reporting on them
  — are the intended next piece and are deliberately not built~~ — **promoted
  out of future work, 2026-08-29** (§5.3, §12.3, §13.2, §13.3, §14; decision
  record row 27, task 052): a project's open pull requests are listed, and a
  task is linked to the one opened from its branch by a daemon-side reconciler.
  It was **reading only** — and *that stopped being true on 2026-08-31*
  (task 069, issue #273): the "create a PR" affordance is no longer a
  constructed compare URL a human clicks, it is a route that pushes the branch
  and opens the pull request. Decision record rows 11 and 27 are amended there,
  narrowly: one human-initiated write, no merging (*until task 068.4, 2026-09-15, below*), workflow-owned delivery
  untouched for workflow runs, and the compare URL kept as the fallback. The one thing this paragraph predicted wrongly is the migration: the
  task column could *not* carry a PR shape, because `github_issue_json` is
  defined as "NULL = no linked issue" holding a bare `Issue`, so `github_pull_json`
  is a sibling column (migration 0018) rather than a widening.
  **Promoted further, 2026-08-30** (§5.3, §8.5, §10, §13.2, §15, §18; decision
  record row 27, task 064): a pull request is not only visible but **runnable** —
  a task can be created from one and its worktree is that pull request's head
  branch, checked out with an upstream, so the agent's commits reach the pull
  request when a workflow pushes. Reading only at the time: no write method, no
  `POST`, no mutating `gh` subcommand, and row 11 stood — until task 069 below, and task 068.4 rewrote it. The costs are recorded where
  they land rather than here — §10 gained a second worktree creation mode and an
  archive exception (vincent never deletes a branch it did not cut), §18 gained
  three block reasons, and the branch-name chain gained a level above the
  per-task literal.
  **Promoted further, 2026-08-31** (§10, §12.3, §13.2, §13.4, §15, §18; decision
  record rows **11 and 27**, task 069, issue #273): a pull request can now be
  **opened from vincent**. `POST /v1/tasks/{id}/github/pull/create` pushes the
  task's branch to `origin` — never forcing — creates the pull request through
  the same gate and credential as the read side, and writes the link
  immediately as `human`. This is vincent's first write to a forge and the
  second thing it has ever pushed; both decision rows say so in place. It is
  reachable only from a human: the route is in §13.4's exclusion list, because
  "the keypress is the consent" is only true while a human is the one pressing
  it. Merging stayed out of scope — until task 068.4, below. The costs land where they belong — §10 gained
  a non-forcing `PushBranch`, §18 gained three push block reasons, §13.2 gained
  the route and its two-shaped 200, and §15 gained a draft toggle in the
  workspace form plus a task picker on the takeover.
  **Promoted further, 2026-09-15** (§12.3, §13.2, §13.4, §18; decision record
  rows **11 and 27**, task 068.4, issue #386): a task's linked pull request can be
  **merged, closed, reopened and commented on, and its failed Actions jobs
  re-run**, from vincent. Row 11 is rewritten rather than narrowed — vincent
  delivers, human-triggered — and the reaffirmations above ("row 11 stood",
  "Merging stays out of scope") are dated by it. Nothing on the step path
  reaches GitHub, every write route is excluded from MCP, and a `merge` step type
  is still not licensed. The daemon routes and the `vincent github pr`
  subcommands land first; the Pull Request tab's confirmed actions follow in
  #387. *Amended 2026-09-16 (task 068.4, issue #387): they have landed — `m`,
  `X`, `i` and `ctrl+r` on the tab, each confirmed first (§15 view 2, §15
  Keys).*
- ~~Container/VM-sandboxed step execution~~ — **the container half is
  promoted out of future work, 2026-08-30** (§16, task 061, issue #256): a
  `container:` block names an image, and a task's step processes run inside one
  container created with its worktree and removed with it — command steps and
  checks as of task 061, agent steps once **062** lands the spawn seam the three
  adapters need. *Amended 2026-09-16 (task 062.1, issue #396): the seam landed
  as 062.1 with only a host launcher, so agent steps still run on the host until
  062.2 (issue #397) adds the container launcher.* *Amended 2026-09-17 (task
  062.2, issue #397): it has, and agent steps run in the container too. Two
  things it left out are named here: `vincent status` from inside a container,
  which would need a vincent binary in the image and a route to the daemon
  (062.2 decision 4; a containerized agent uses §13.4's `step_status` tool
  instead), and passing a command step's variables to `docker exec` by name,
  as agent steps now do, rather than as 061 shipped it.* The image
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
