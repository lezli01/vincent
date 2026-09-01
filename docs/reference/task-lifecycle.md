# Task lifecycle

Every task moves through one state machine, defined in exactly one place in the
source. Both the API (which returns `409` on an invalid action) and the
execution engine consult it, so "what may happen next" has a single definition —
and every task representation carries `available_actions`, so no client ever
restates it.

This page is about **tasks**. A [chat](cli.md#vincent-chat) has its own, much
smaller vocabulary — `idle`, `running`, `awaiting_input`, `archived`,
`handed_off` — kept deliberately separate so no task query or board legend has
to decide whether it means chats too. It is documented with the [chat routes](api.md#chats).

- [The diagram](#the-diagram)
- [States](#states)
- [Human actions](#human-actions)
- [Step outcomes](#step-outcomes)
- [Failure reasons](#failure-reasons)
- [Interruption is not failure](#interruption-is-not-failure)

---

## The diagram

```
                 create
                   │
                   ▼
              ┌────────┐   slot free    ┌─────────┐  input request  ┌────────────────┐
   ┌─────────►│ queued ├───────────────►│ running │◄───────────────►│ awaiting_input │
   │          └────────┘  (scheduler)   └──┬──┬──┬┘    answer       └────────────────┘
   │   approve ▲   ▲ retry/skip            │  │  │
   │           │   │                       │  │  │ all steps
   │      ┌────┴───┴─┐   manual step       │  │  │ succeeded
   │      │ awaiting │◄────────────────────┘  │  ▼
   │      │  _gate   │                        │ ┌──────┐
   │      └────┬─────┘      step failed,      │ │ done │
   │           │ reject     retries exhausted ▼ └──┬───┘
   │           └─────────►┌─────────┐              │
   │                      │ blocked │              │ archive
   │ resume               └──┬──────┘              ▼
┌──┴─────┐  pause            │ abort         ┌──────────┐
│ paused │◄───────┐          ▼               │ archived │
└────────┘ (from  │      ┌─────────┐ archive └──────────┘
           queued/│      │ aborted ├──────────────▲
           running┘      └─────────┘
```

Tasks are `queued` immediately on creation. There is no draft state.

`done` and `aborted` have one more edge out of them that the diagram leaves off
to stay readable: `follow_up` re-queues either of them to run more work in the
task's existing worktree, and returns it to the state it came from. See
[human actions](#human-actions).

## States

| State | Meaning | Holds a slot? |
|---|---|---|
| `queued` | Ready to run; waiting for scheduler admission — or, when `queued_reason` is set, for something else (see below) | no |
| `running` | A step process is executing, or about to | **yes** |
| `awaiting_gate` | Stopped at a `manual` step, waiting for approval | no |
| `awaiting_input` | The running agent asked a question; its process is alive and idle on stdin | **yes** |
| `awaiting_children` | A `fan_out` step's lanes are running as child tasks; this task owns no process | no |
| `blocked` | A step failed and retries are exhausted; waiting for a human | no |
| `paused` | You asked it to hold; takes effect at the next step boundary | no |
| `done` | Every step succeeded. Worktree and branch retained for inspection. `follow_up` runs more work in them; `archive` tears them down | no |
| `aborted` | You cancelled, or rejected terminally. Worktree and branch retained, and open to `follow_up` on the same terms as `done` | no |
| `archived` | Terminal. Worktree removed, record kept. The branch is kept unless it has no commits past its base, in which case it is deleted ([`delete_empty_branch_on_archive`](configuration.md#delete_empty_branch_on_archive)) | no |

Three of these are worth dwelling on.

**`queued` covers two different waits.** Usually it means "waiting for a free
slot". But a task can also be `queued` waiting on a *clock* rather than on the
queue, and it says so through two fields: `queued_reason` and
`admit_not_before`, the instant vincent will try again. Either way it holds no
slot while it waits, keeps its place in the queue, and needs nothing from you —
when the wait is over the step runs on its own. The board shows the resume time
on the row (`queued → 14:20`) and the detail header names the reason.
Cancelling, pausing or otherwise acting on the task drops the wait immediately
— a human action always means go.

Two reasons produce that wait, and they are worth telling apart:

| `queued_reason` | What it means |
|---|---|
| `usage_limit` | The agent's usage quota for the window is spent. The attempt is recorded `interrupted` and costs **no** retry; the wait ends at the reset the CLI named, or after [`usage_limit_recheck_interval`](configuration.md#usage_limit_recheck_interval) when it named none |
| `retry_backoff` | The step failed and its next attempt is being paced by [`retry_backoff`](workflow-schema.md#step-fields). The attempt is recorded `failed` with its own reason and **does** consume a retry; the wait is the configured duration. When the budget runs out the task blocks with the step's own reason, with no wait first |

Both are `queued_reason` values only. Neither is ever a `block_reason`, and
`retry_backoff` is never a step's failure reason either — the step's row keeps
whatever actually failed.

**`awaiting_children` holds no slot, and offers only `cancel`.** A fan-out
parent waiting on its lanes owns no process, so keeping a slot would starve
the queue for hours — and it is what makes fan-out deadlock-free at any depth,
since a parent releases its slot before its children need one. It is not a
reuse of `awaiting_gate` because approve, reject and skip would all be
meaningless while children are still running: the states differ in what a
human can do about them, which is the only thing the lifecycle is for.

The parent resumes on its own once every lane has settled — finished or ended.
A lane that is `blocked`, at a gate, or paused holds the join open until you
deal with it; the `children` rollup on `GET /v1/tasks/{id}` (and the TUI's
`awaiting_children (2 blocked)` row) is where you see that.

**`awaiting_input` keeps its concurrency slot.** The agent process is alive
mid-step, idle on its stdin; killing or re-queueing it would lose the very
session the answer belongs to. So a forgotten question does occupy a slot, until
`input_timeout` (default 24h) expires.

**`blocked` is a resting state, not a failure.** Nothing is silently abandoned:
a step that exhausts its retries stops and waits for a person, and the task keeps
its worktree, its branch and its transcripts while it does.

**Repair when the worktree is what is wrong.** `retry` re-runs an unchanged
step, `edit + retry` can rewrite only that step's own prompt or command, and
`skip` advances past an unsatisfied check — none of them changes a file. When
the fix is in the worktree, `repair` runs one throwaway agent there: you write
what it should do, the daemon hands it the task's context and the blocked step's
failure (its rendered prompt or command, the reason and exit codes, the tail of
the failed attempt's transcript and where to read the rest), and it works on the
task's own branch in the task's own worktree.

It decides nothing. However the repair agent exits, the task comes back to
`blocked` at the same step with the same reason, so you read the diff and then
choose — retry, repair again, skip or cancel. It is recorded as a step run of
its own under the reserved step id `__repair` at the blocked step's index, which
is why the timeline shows it as its own entry and why it does not eat the
blocked step's retries: after any number of repairs, a `retry` gets exactly the
attempts it would have had with none.

Repair is offered from `blocked` whatever the reason. A task blocked before its
worktree existed re-blocks on the same reason without starting an agent, and one
blocked because its agent CLI is missing has its repair fail the same way — both
honest outcomes rather than a hidden filter.

**A finished task is not finished with until you archive it.** `done` and
`aborted` both keep the worktree, the branch and the commits, and `follow_up`
is how you do one more piece of work in them without leaving vincent. You supply
one of three things — an agent prompt, a shell command, or the name of a
workflow from the registry — and the daemon runs it on the task's own branch, in
the task's own worktree, recorded in the task's own ledger with a step run, a
transcript, events and cost accounting.

It decides nothing about the verdict: a done task comes back `done` and an
aborted one comes back `aborted`, whatever the run did. That is deliberate — a
command that exits 0 should not be able to reverse an abort you made on purpose.
Follow-ups are repeatable, and each is a **round**: round 1's rows sit one past
the workflow's last step index, round 2's one past that, so the timeline shows
`↳ follow-up 1`, `↳ follow-up 2` rather than pretending the workflow grew.

A follow-up step that fails blocks the task at the follow-up's own index. From
there `retry` re-runs the follow-up where it stopped, `repair` runs an ad-hoc
agent against *that* failure, `skip` abandons the follow-up and puts the task
back where it came from, and `cancel` aborts. Edit-and-retry is refused there:
an override rewrites a step in the task's snapshot, and a follow-up is
deliberately not in it.

One edge worth knowing about: because a follow-up's process is live while it
runs, `cancel` during one aborts the task — so `done → aborted` is reachable,
which it was not before follow-ups existed.

## Human actions

| Action | Valid from | Effect |
|---|---|---|
| `cancel` | queued, running, awaiting_input, awaiting_gate, awaiting_children, blocked, paused | Kills any running process — graceful termination, then a kill after 10s — and moves to `aborted`. From `awaiting_children` it cascades to every unfinished lane, whose branches and worktrees survive |
| `pause` | queued, running | From `running`, finishes the current step then holds. The request is persisted, so it survives a daemon crash; any other action clears it |
| `resume` | paused | → `queued` |
| `retry` | blocked | Re-runs the failed step as a fresh attempt with the retry counter reset → `queued` |
| `edit + retry` | blocked | Overrides the step's prompt or command **in this task's snapshot only**, then retries. The override is recorded on the step run |
| `repair` | blocked | Runs one ad-hoc agent, prompted by you, in the task's existing worktree and branch → `queued`, and back to `blocked` at the same step with the same reason when it exits. It does not consume the blocked step's retry budget |
| `skip` | blocked, awaiting_gate | Marks the step `skipped` and advances → `queued`. A step skipped this way carries no `skip_reason`, which is how it stays distinguishable from one an `if:` guard skipped |
| `answer` | awaiting_input | Delivers the answer into the live agent session → `running`, and the step clock resumes |
| `approve` | awaiting_gate | Gate `approved`, advance → `queued` |
| `reject` | awaiting_gate | Gate `rejected` → `blocked`, from which you can edit-and-retry an earlier step, skip, or abort |
| `set priority` | queued, paused | Reorders scheduler admission |
| `archive` | done, aborted | Removes the worktree → `archived`, then deletes the branch **only** if it has no commits past its base. Refuses on a dirty worktree unless forced — uncommitted work would be lost, and a refusal never reaches the branch |
| `follow_up` | done, aborted | Runs one more piece of work — an agent prompt, a shell command or a registry workflow — in the task's existing worktree and branch → `queued`, and back to the state it came from when it ends. Repeatable; it never changes the task's verdict and never spends the workflow's retry budgets |

In the TUI these are the action bar keys (`a`, `x`, `r`, `R`, `E`, `s`, `p`,
`c`, `A`, `F`); over the API they are `POST /v1/tasks/{id}/{action}`; from the
CLI, [`vincent task <action> <id>`](cli.md#vincent-task) — one subcommand each,
spelled in kebab-case (`follow_up` is `vincent task follow-up`). `set priority`
is the exception: it is a `PATCH`, and has no subcommand.

An action the current state does not allow returns `409` with `details.state`
set to the state actually found — so a client branches on a value rather than
parsing prose.

An action the state *does* allow never fails on a race. Each one is a
compare-and-swap on the state the request read, and the task can move under it
— the scheduler admitting a queued task is the usual way. When it does, the
action is re-applied once from the state actually found, provided the table
above allows it from there: `cancel` on a task the scheduler admits at that
instant aborts the now-running task rather than reporting a conflict, and
`pause` becomes the deferred kind that holds at the next step boundary.

## Step outcomes

Each attempt of each step is a **step run** row, and every attempt is kept —
that is what the TUI's timeline lists.

| Outcome | Meaning |
|---|---|
| `succeeded` | Exit 0, a terminal result, and any `check` passed |
| `failed` | A retryable failure; see the reasons below |
| `interrupted` | The daemon stopped mid-step. **Does not consume a retry** |
| `skipped` | You skipped it, **or** its `if:` guard was false. `skip_reason` is `condition` in the second case and empty in the first |
| `stopped` | A `condition` step whose guard was false: the run ended here, deliberately, and the task is `done` |
| `approved` / `rejected` | A manual gate's two outcomes |

A step succeeds only when *all* of its criteria hold:

- **agent** — the process exits 0, **and** the event stream produced a terminal
  result rather than an error, **and** any declared `check` exits 0.
- **command** — the command exits 0, **and** any declared `check` exits 0.
- **manual** — a person approves.

On a retryable failure the step re-runs, up to `max_retries` (default 1, so two
attempts). For agent steps the previous failure is appended to the retried prompt
as a structured block, so the agent is told exactly what went wrong rather than
asked to guess.

The re-run is immediate unless the step sets
[`retry_backoff`](workflow-schema.md#step-fields), in which case the task waits
that long in `queued` — holding no slot — and the step re-runs when the wait is
over. The budget is unchanged either way: the backoff decides *when* an attempt
happens, never *whether*.

## What a step says about itself

Beside the outcome, a step run carries two pieces of text that are **not**
vincent's words:

- **`status_message`** — a line the running step wrote about itself through
  [`vincent status`](../guides/workflows.md#56-reporting-status-from-a-step).
  While the step runs it is the live answer to "what is this doing"; the last
  value it set stays on the finished attempt. Empty unless the workflow asked
  for it, and empty always for the step types that run no process (`manual`,
  `parallel`, `fan_out`, `condition`, `loop`, `break`).
- **`result_summary`** — the agent's final result text, or the last 200 lines of
  a command step's stdout. Recorded on every attempt, readable from a later
  step's `{% raw %}{{ .Steps.<id>.Result }}{% endraw %}`.

Neither is a failure reason, and no surface renders either as one. The reasons
below are a closed set vincent authors; these two are whatever the step
produced, possibly long before it stopped.

## Failure reasons

The reason is recorded on the step run and on the task when it blocks. The
vocabulary is shared across the engine and the worktree layer: a reason means the
same thing wherever it originated.

| Reason | Meaning |
|---|---|
| `check_failed` | The `check` command exited non-zero |
| `nonzero_exit` | A command step exited non-zero |
| `agent_error` | The agent's event stream reported an error |
| `agent_unavailable` | The adapter's CLI could not be resolved or started |
| `agent_unauthenticated` | The agent CLI is installed but not logged in. Retries as usual, then blocks — log in and retry |
| `usage_limit` | The agent's usage quota for the window is spent. **Not a failure:** no retry is consumed, and the task waits `queued` until the window reopens |
| `retry_backoff` | Not a failure reason at all, and never appears on a step run — it is the `queued_reason` of a task waiting out a step's [`retry_backoff`](workflow-schema.md#step-fields) between two attempts. The attempt that triggered it keeps its own reason |
| `timeout` | The attempt exceeded its `timeout` and was killed |
| `input_timeout` | A mid-run question went unanswered past `input_timeout` |
| `input_protocol_error` | A control message the adapter could not parse — it fails, it never hangs |
| `template_error` | A template failed to render, before any process started |
| `condition_error` | A step's `if:` guard failed to render, or rendered something that is neither `true` nor `false`. The one reason that is **not** retried — a guard is evaluated before the step becomes an attempt, and re-rendering it cannot answer differently. Fix the workflow and retry |
| `loop_limit` | A `loop` step cannot run within `max_iterations`: a `for_each` list longer than the ceiling, or a `count:` the ceiling moved under. It blocks rather than truncating or advancing — running out of tries is not a decision the workflow made, and advancing would tell every later guard the work is finished. Raise `max_iterations:` on the step or `loop.max_iterations` in config, or narrow the list at its source |
| `restricted_unsupported` | The adapter cannot restrict on this platform (cursor on Windows). Task creation refuses these, so reaching it means the workflow or the machine changed after the task was queued |
| `mcp_unsupported` | The adapter or CLI build cannot be given vincent's [MCP server](../guides/mcp.md) for this step. The step fails rather than running an agent that silently has no vincent tools. Set [`mcp.wire_steps: false`](configuration.md#mcp) if the step does not need them |
| `transcript_limit` | The attempt hit `transcript_max_bytes` |
| `cost_limit` | The task has spent past [`max_task_cost_usd`](configuration.md#max_task_cost_usd). **Not a step failure:** the finished step run keeps its own state and reason, no retry is consumed, and a retry that was due does not run. Checked after each attempt, so expect to overshoot by up to one. Raise the cap — it is hot-reloaded — and retry; retrying without raising it buys exactly one more attempt. Inert on agents that report no cost |
| `transcript_io_error` | The attempt's transcript could not be written, encoded or closed — a full disk, a revoked permission, a short write. The step fails rather than reporting a success over a record that is missing the run it describes. Retries as usual (a new attempt writes a new file), then blocks. **Not** what an over-long line produces: those are captured in `partial` pieces |
| `agent_protocol_error` | Vincent could not read the agent's stream to the end, so the transcript is missing lines the CLI wrote. Not `agent_error` — the CLI may have behaved perfectly; the reader that failed is vincent's |
| `shell_unavailable` | The requested shell is not installed |
| `container_image_unavailable` | The task's [`container.image`](configuration.md#container) is not on this machine and could not be pulled. Blocks at **admission**, before a worktree, a branch or a retry is spent — the image check is deliberately not made when the task is created |
| `container_unavailable` | The container runtime answered when the task was created and does not now, or refused to create the container. A step the container was going to run is **never** moved to the host because of it — the task blocks instead |
| `rejected` | You rejected a manual gate |
| `canceled` | You cancelled the task |
| `invalid_snapshot` | The task's stored workflow snapshot is unusable |
| `platform_unsupported` | The workflow is restricted to platforms this host is not (`platforms:`). Only reachable for a task created on another OS |
| `input_unsupported` | The step declares `on_input: require` and its agent cannot take mid-run input. Refused at task creation, so only reachable when the agent changed underneath the task |
| `merge_conflict` | A fan-out lane's merge conflicted. The worktree is left conflicted on purpose: resolve it, stage the files, and retry — the join commits your resolution and merges the rest |
| `lane_failed` | A fan-out lane was cancelled or ended without finishing. **Nothing is merged** — a partial merge looks exactly like a complete one downstream. Fix the lane (it is an ordinary task), then retry the parent |
| `interrupted` | The daemon stopped mid-step — not a failure |
| `internal_error` | A bug. Please [report it](https://github.com/lezli01/vincent/issues/new/choose) |

Worktree-layer reasons: `project_path_missing`, `base_branch_missing`,
`branch_exists`, `branch_name_invalid`, `worktree_dirty`, `worktree_missing`,
`worktree_path_occupied`, `git_error`. A `branch_exists` block is recoverable
without losing the task: `POST /v1/tasks/{id}/retry` accepts a `branch_override`
that renames the branch and re-admits it. See
[Troubleshooting](../guides/troubleshooting.md#projects-and-worktrees).

Three more belong to a task created from a pull request
([`--github-pull`](cli.md#creating-a-task-from-a-pull-request)), whose branch is
the pull request's head rather than one vincent cut:

| Reason | What happened |
|---|---|
| `pull_fetch_failed` | The pull request's head could not be fetched. There is nothing to fall back to — the fetched commit is where the branch has to be — so unlike the base-branch fetch this blocks rather than degrading quietly |
| `pull_branch_diverged` | You have a local branch of that name carrying commits the pull request's head does not. Vincent will not discard them; merge or rebase yours, or delete it if it is stale, then retry |
| `pull_branch_checked_out` | That branch is already checked out in another worktree — vincent's or your own main checkout. Git cannot put one branch in two worktrees. Switch that checkout to another branch, then retry |

`branch_override` is **refused** on such a task: renaming its branch would
detach it from the pull request it was created for.

## Interruption is not failure

Vincent is **crash-first**: every transition is persisted before it is acted on.
When the daemon starts, recovery:

1. finalizes any step run still marked `running` as `interrupted`, keeping
   whatever status message that step had set — "what was it doing when the
   machine went down" is exactly what you want off that row;
2. kills verified orphan processes — the recorded PID must still exist **and**
   still hold the very process the run spawned, checked against a
   platform-native identity recorded at spawn (start ticks plus boot id on
   Linux, the kernel fork stamp on macOS, the creation time on Windows, each
   paired with the PID, because the operating system's stamp is a clock tick
   wide and processes started inside one share it). That
   is the guard against PID reuse killing an innocent process: anything vincent
   cannot prove is the same process is left running. A run recorded before
   vincent kept that identity — by an earlier version, or on a machine where
   the read is unavailable — falls back to the older check, a start time within
   five seconds of the recorded one;
3. re-queues the owning task and re-runs the interrupted step as a **fresh
   attempt that does not consume a retry**.

Tasks found in `awaiting_input` are treated identically: the pending request is
discarded with the process, and the fresh session may re-ask.

This is safe by construction because every agent step is a fresh session
operating on a worktree whose committed state survives. Workflow authors help by
having agents commit incrementally.

A graceful stop (`vincent daemon stop`, SIGTERM, a service stop) takes the same
path: a `daemon.shutting_down` event, admission stops, running processes get 15
seconds before being killed, and their runs are marked `interrupted`. The API —
SSE streams included — stays up through the grace period, so clients watch the
wind-down live.

---

## See also

- [Concepts](../getting-started/concepts.md) — how a step runs.
- [HTTP API](api.md) — the action endpoints and the event stream.
- [Using the TUI](../guides/tui.md#the-action-bar).
