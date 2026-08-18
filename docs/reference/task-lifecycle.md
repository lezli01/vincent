# Task lifecycle

Every task moves through one state machine, defined in exactly one place in the
source. Both the API (which returns `409` on an invalid action) and the
execution engine consult it, so "what may happen next" has a single definition —
and every task representation carries `available_actions`, so no client ever
restates it.

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
| `done` | Every step succeeded. Worktree and branch retained for inspection | no |
| `aborted` | You cancelled, or rejected terminally. Worktree and branch retained | no |
| `archived` | Terminal. Worktree removed, record kept. The branch is kept unless it has no commits past its base, in which case it is deleted ([`delete_empty_branch_on_archive`](configuration.md#delete_empty_branch_on_archive)) | no |

Three of these are worth dwelling on.

**`queued` covers two different waits.** Usually it means "waiting for a free
slot". But a task whose agent hit a usage limit is also `queued` — waiting on a
clock rather than on the queue — and says so through two fields: `queued_reason`
(`usage_limit`) and `admit_not_before`, the instant vincent will try again. It
holds no slot while it waits, keeps its place in the queue, and needs nothing
from you: when the window reopens, the step re-runs on its own. The board shows
the resume time on the row (`queued → 14:20`) and the detail header names the
reason. Cancelling, pausing or otherwise acting on the task drops the wait
immediately — a human action always means go.

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

## Human actions

| Action | Valid from | Effect |
|---|---|---|
| `cancel` | queued, running, awaiting_input, awaiting_gate, awaiting_children, blocked, paused | Kills any running process — graceful termination, then a kill after 10s — and moves to `aborted`. From `awaiting_children` it cascades to every unfinished lane, whose branches and worktrees survive |
| `pause` | queued, running | From `running`, finishes the current step then holds. The request is persisted, so it survives a daemon crash; any other action clears it |
| `resume` | paused | → `queued` |
| `retry` | blocked | Re-runs the failed step as a fresh attempt with the retry counter reset → `queued` |
| `edit + retry` | blocked | Overrides the step's prompt or command **in this task's snapshot only**, then retries. The override is recorded on the step run |
| `skip` | blocked, awaiting_gate | Marks the step `skipped` and advances → `queued`. A step skipped this way carries no `skip_reason`, which is how it stays distinguishable from one an `if:` guard skipped |
| `answer` | awaiting_input | Delivers the answer into the live agent session → `running`, and the step clock resumes |
| `approve` | awaiting_gate | Gate `approved`, advance → `queued` |
| `reject` | awaiting_gate | Gate `rejected` → `blocked`, from which you can edit-and-retry an earlier step, skip, or abort |
| `set priority` | queued, paused | Reorders scheduler admission |
| `archive` | done, aborted | Removes the worktree → `archived`, then deletes the branch **only** if it has no commits past its base. Refuses on a dirty worktree unless forced — uncommitted work would be lost, and a refusal never reaches the branch |

In the TUI these are the action bar keys (`a`, `x`, `r`, `E`, `s`, `p`, `c`,
`A`); over the API they are `POST /v1/tasks/{id}/{action}`; from the CLI,
`vincent task cancel`.

An action the current state does not allow returns `409` with `details.state`
set to the state actually found — so a client branches on a value rather than
parsing prose.

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
| `timeout` | The attempt exceeded its `timeout` and was killed |
| `input_timeout` | A mid-run question went unanswered past `input_timeout` |
| `input_protocol_error` | A control message the adapter could not parse — it fails, it never hangs |
| `template_error` | A template failed to render, before any process started |
| `condition_error` | A step's `if:` guard failed to render, or rendered something that is neither `true` nor `false`. The one reason that is **not** retried — a guard is evaluated before the step becomes an attempt, and re-rendering it cannot answer differently. Fix the workflow and retry |
| `restricted_unsupported` | The adapter cannot restrict on this platform (cursor on Windows) |
| `transcript_limit` | The attempt hit `transcript_max_bytes` |
| `shell_unavailable` | The requested shell is not installed |
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

## Interruption is not failure

vincent is **crash-first**: every transition is persisted before it is acted on.
When the daemon starts, recovery:

1. finalizes any step run still marked `running` as `interrupted`;
2. kills verified orphan processes — the recorded PID **and** its start time must
   both match, which is the guard against PID reuse killing an innocent process;
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
