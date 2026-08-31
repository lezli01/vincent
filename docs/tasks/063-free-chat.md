# 063 — Free chat: conversational agent sessions beside tasks

**Issue:** #255
**Status:** ✅ done (3/3)

## Why

Every agent run vincent owns today is a *task*: a workflow, a step ledger, a
verdict. There is no way to simply talk to an agent inside a project and keep
the result. People do it anyway, in a terminal beside vincent — and that
conversation has no worktree isolation, no transcript, no token or cost record,
and nothing to come back to.

A chat closes that: a titled conversation, scoped to a project, isolated in its
own worktree and branch, with every turn recorded the way a step run is.

## What it is

A **first-class entity beside Task**, never a task with a `kind` column. The
alternative was considered and rejected in the issue: a chat row in `tasks`
would force the board, admission and every §17 aggregate to decide whether they
mean chats too, and a chat carrying a `current_step` would be a lie to every
reader of the snapshot. Same reasoning keeps `chat_turns` out of `step_runs`:
`step_runs.task_id` stays `NOT NULL` and every query over it keeps its meaning.

Spec: §5.5 (the entity and its lifecycle), §7.3 (the chat-only amendment), §9.1
/ §9.2 / §9.3 / §9.7 (resume), §10 (worktrees and gc), §11 (the cap), §12.3,
§12.4 (interrupted turns), §13.2–§13.4, §14, §15, §20, decision record row 29.

## Decisions

1. **A chat turn is bounded by its own cap, and refused rather than queued.**
   `max_parallel_chats` (default 3) counts chats in `running` or
   `awaiting_input`. Over it, a send is `409` immediately — never queued, never
   through `internal/scheduler`. The issue asked for "no admission at all";
   that collides with §11's 2026-08-29 amendment (task 057, row 28), which
   rejected releasing a slot while an agent process is live and named the cost
   verbatim — *live-but-uncounted agent CLIs accumulating*. That reasoning does
   not stop applying because the noun changed. So the 057 amendment is
   **extended, not excepted**, and the foreground property the issue wanted is
   preserved anyway: a chat turn never waits behind batch work, it is refused
   in front of it. The scheduler invariant is untouched because a chat turn is
   never `queued`.

2. **No chat route is an MCP tool.** §13.4's exclusion list grows from five
   routes to five plus the chat family. An agent must not be able to start
   unqueued agent processes, and `mcp.max_depth`/`mcp.max_tasks` bound tasks by
   walking `created_by_task_id` — a chain a chat is not in, so adding chats
   would mean inventing depth semantics for a non-task. An agent that needs a
   conversation already has its own session. Task 057's assertion that the tool
   surface equals `Routes()` minus the exclusions is **extended**, not weakened.

3. **claude only in the first cut; codex and cursor are refused, not emulated.**
   `agent.Resumer` is an optional capability so a new adapter is still one
   implementation with zero core changes; all three shipped adapters implement
   it anyway, because §9.x states a missing capability positively. Chat creation
   refuses a non-resuming adapter with `400 agent_cannot_resume` — the same
   shape as `agent.ErrRestrictedUnsupported`. Replaying the log as prompt
   context stays rejected. codex `exec resume <thread_id>` and cursor
   `--resume` are follow-up work, each landing with a fixture captured against
   a named CLI version, the way every other adapter capability has.

   *Codex half satisfied 2026-08-31 (task 070, issue #268).* Not reversed —
   executed. The follow-up work this decision described is exactly what landed:
   a capture against a named build (codex-cli 0.150.1,
   `internal/agent/codex/testdata/resume_0.150.1.jsonl`) pins the argv, so
   `SupportsResume()` is true for codex and a codex chat is created rather than
   refused. The cursor half stands unchanged, and `m14`'s refusal leg states it
   over cursor now (§9.7, §20).

4. **A turn whose stored session is gone fails.** Reason `session_lost`,
   rendered against the turn; the chat stays usable and keeps its id, and the
   human decides whether to start fresh. A silent fresh session answers as if it
   had context it does not have, and a reader cannot tell that apart from a
   working one. The classifier only ever runs for a run that actually passed
   `--resume`, so no workflow step can be misdiagnosed.

5. **A turn interrupted by a daemon restart is finalized `interrupted` and is
   not re-run.** §12.4's auto-resume rule holds for steps because a fresh
   session over a surviving worktree is safe by construction; a chat turn is
   neither half of that. Re-running would re-send the human's message into a
   session that died with the process. The orphan kill is unchanged: same
   `procx.Identity` PID-reuse guard, same fail-closed atomic shape as
   `store.InterruptTask`. The chat returns to `idle`.

6. **Turns get their own `chat_turns` table** with `step_runs`' accounting
   columns and a transcript file per turn.

7. **`notify:` does nothing for chats in this cut.** It exists for unattended
   work finishing hours after the human left; a chat turn ends while its human
   is looking at it. `internal/config` gains no import of the chat state
   package, so task 046 decision 4's arrangement is untouched. `notify.chat_on`
   is the named trigger if `awaiting_input` on a long-open chat needs one.

8. **Mid-run input reuses the §7.4 flow.** A chat enters `awaiting_input`
   holding its process, `pending_input` on the chat row, answered through a chat
   answer route calling the same adapter `Respond()`. The structured options,
   multi-select and the permission allow/deny distinction are the reason not to
   degrade this into "the next message is the answer".

9. **Chat worktrees join the gc claim namespace** (found during review, not in
   the issue). `internal/taskrun/reclaim.go` builds its claim sets from *task*
   rows and treats anything unclaimed under the worktree root or
   `{data_dir}/transcripts` as an orphan, so a chat worktree would be deleted by
   `vincent gc` and inflate `GET /v1/info`'s orphan count meanwhile. Chats
   extend both claim sets, and `worktree.Manager` takes an `Owner` rather than a
   `taskID int64` so chat 7 and task 7 cannot collide on `{root}/7`. Keeping
   chat directories in a root the reclaimer does not scan was rejected: it
   trades a false positive for no gc coverage at all.

Unchanged and worth stating: §16 is untouched. Chats are full-auto by default
like tasks, the worktree isolates collisions and not privileges, and the
existing first-run acknowledgement already covers it. §6's task FSM,
`internal/taskstate` and the task board gain nothing.

## Sub-tasks

| ID | What | Status |
|---|---|---|
| 063.1 | The entity end to end: `internal/chatstate`, `internal/chatrun`, migration 0022, store CRUD + claims, `internal/agent` resume capability + claude `--resume`, the `/v1/chats` route family, `internal/apiclient`, `vincent chat`, daemon wiring, `max_parallel_chats`, gc claim sets, owner-named worktree directories, spec and docs | ✅ done |
| 063.2 | The chats view in the TUI — a chats board and a chat workspace, two `viewID`s of their own — and an end-to-end chat gate wired into `ci.yml`'s `gates` job on all three platforms. *The gate is `scripts/m14-gate.sh`, not `m13`: task 065 took that name for the workflow editor.* Closed by task 067 | [x] |
| 063.3 | Closed by task 067. The three gaps the 063.1 documentation audit found, none of which changes a decision above: a chat turn is bounded by **no clock at all** — `internal/chatrun` applies neither `input_timeout` nor `agent_timeout`, so a turn (and the `max_parallel_chats` slot it holds) runs until it is answered or cancelled, and §13.2 carries a dated correction saying so; `vincent chat` has **no `answer` and no `cancel`**, so a chat that enters `awaiting_input` can only be moved on over the API, and `vincent chat send` blocks meanwhile; and the transcript plumbing is task-scoped at both ends — `internal/taskrun`'s pruner walks archived *tasks*, so an **archived chat's transcripts are never reclaimed** by `transcript_retention_days`, and only `internal/taskrun/engine.go` calls `Writer.SetMax`, so a chat turn's transcript is written with **no `transcript_max_bytes` cap**. `vincent chat` also has no `--json`, which is why the blanket claim in the scripting guide was narrowed | [x] |

### Why 063.2 is separate

063.1 is the entity, and it is complete and usable — CLI, API, runner,
recovery, gc, docs. The TUI view and the end-to-end gate are the client and the
acceptance walk over it; neither changes a decision above, and both are held to
bars this repository takes seriously and does not want rushed: a view whose
screenshots come from `scripts/screenshots.sh` rather than a drawing, and a gate
written in the sh∩pwsh intersection that has actually been run on all three
platforms before it is claimed to pass on them. §15 carries a note saying the
view is not there yet, so the spec does not describe a screen that does not
exist.

*Closed 2026-08-31 by [task 067](067-chats-in-the-tui.md), issue #269*, which
landed 063.2 and 063.3 together — the 063.3 gaps are what make a chat drivable
without curl, and the view is the client that proves it. §15's note is gone
because the screens it described as absent are there.

## What the tests prove

- **Continuity** — `internal/chatrun`: turn 2 answers with what only turn 1
  supplied, against `cmd/fakeagent`, which was given a real session store of its
  own so the resume is the agent remembering rather than vincent staging it.
- **Refusal, not emulation** — codex and cursor are refused at creation with the
  typed reason, over the real handlers.
- **`session_lost`** — a stored id the CLI no longer knows fails the turn,
  leaves the chat idle and usable, keeps the id, and starts no fresh session
  (asserted against the fake CLI's own store being untouched).
- **Caps** — the `max_parallel_chats + 1`-th send is `409` and leaves no turn
  row behind: refused, not parked.
- **Recovery** — a turn journaled `running` by a dead daemon is `interrupted`,
  the chat is `idle`, no second turn exists and the message is not re-sent.
- **MCP** — the tool surface equals `Routes()` minus the five admin routes *and*
  the chat family.
- **gc** — a chat's worktree and transcripts are not strays.
- **Separation** — chats never appear in `ListTasks` or `GET /v1/tasks`.
- **FSM** — `internal/chatstate` over every action from every state, including
  the illegal ones.
