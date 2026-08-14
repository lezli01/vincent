# HTTP API

The daemon serves REST + SSE on loopback. Every client — the TUI, the
`vincent` subcommands, your script — uses this and nothing else.

- [Transport and auth](#transport-and-auth)
- [Errors](#errors)
- [Daemon](#daemon)
- [Projects](#projects)
- [Workflows](#workflows)
- [Tasks](#tasks)
- [Transcripts and diffs](#transcripts-and-diffs)
- [Events (SSE)](#events-sse)

---

## Transport and auth

- HTTP/1.1 + JSON on `127.0.0.1` only. **No TLS** — it is loopback.
- Every request needs `Authorization: Bearer {token}`, where the token is the
  contents of `{data_dir}/token` (created `0600`). This is what keeps other
  local users and drive-by browser requests out; CORS is additionally disabled.
- **Discovery:** read `{data_dir}/daemon.json` for the port, then
  `GET /v1/health`.
- **Versioning:** path-prefixed (`/v1`), additive changes only within a version.

```sh
DATA_DIR=${VINCENT_DATA_DIR:-$HOME/.local/share/vincent}
PORT=$(jq -r .port "$DATA_DIR/daemon.json")
TOKEN=$(cat "$DATA_DIR/token")
curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT/v1/info" | jq
```

## Errors

```json
{ "error": { "code": "invalid_state",
             "message": "task 7 is running, not queued",
             "details": { "state": "running" } } }
```

Codes are stable `snake_case` strings; HTTP status codes are used properly.
`details` is optional and carries values a client should branch on rather than
parse out of prose — an invalid state transition is always `409` with
`details.state` set to the state actually found. It is omitted when empty.

---

## Daemon

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/health` | Liveness → `{ status, version }`. **Unauthenticated** |
| `GET` | `/v1/info` | Version, uptime, agent availability, caps in effect |
| `GET` | `/v1/config` | The effective global config, read-only |
| `GET` | `/v1/agents` | Per-adapter availability plus model/effort options. `?refresh=true` forces a re-probe |
| `POST` | `/v1/daemon/stop` | Graceful shutdown → `202`, then the daemon exits |

`GET /v1/agents` is the option catalog the TUI's pickers render:

```json
{ "agents": [ {
    "name": "claude", "available": true, "path": "…", "version": "2.1.224",
    "supports_input": true, "logged_in": null,
    "models":  [ { "value": "sonnet", "source": "cli" } ],
    "efforts": [ { "value": "max",    "source": "cli" } ],
    "default_model": "", "default_effort": "",
    "probed_at": "2026-08-07T10:00:00Z", "probe_error": null } ] }
```

`source` is provenance: `cli` was discovered from the installed binary,
`curated` comes from vincent's own floor. Results are cached by **binary
identity** (path + mtime + version), so upgrading a CLI invalidates the cache by
construction. `logged_in` is `null` where the adapter has no cheap
authentication probe (claude, codex) and a definite boolean where it does
(cursor) — because an installed-but-unauthenticated CLI probes as healthy and
then fails every run.

## Projects

| Method | Path | Body / notes |
|---|---|---|
| `GET` | `/v1/projects` | List |
| `POST` | `/v1/projects` | `{ path, name?, default_branch?, default_workflow?, max_parallel_tasks? }` |
| `GET` | `/v1/projects/{id}` | |
| `PATCH` | `/v1/projects/{id}` | Any mutable field, including re-pointing `path` |
| `DELETE` | `/v1/projects/{id}` | Hard-deletes the project and its task rows |

`DELETE` succeeds only when no non-archived tasks remain. `?force` archives them
first (force-removing worktrees), and is refused while any task is running.
**Branches are never deleted.**

## Workflows

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/workflows?project_id=` | The merged registry: built-in + global + that project's, with shadowing applied |
| `POST` | `/v1/workflows/validate` | `{ yaml }` → `{ valid, errors[], warnings[] }` |
| `POST` | `/v1/resolve` | `{ workflow, project_id?, agent?, model?, effort?, title?, fields?, base_branch?, branch_name? }` → resolution per step, plus the previewed branch name |

Registry entries carry
`{ name, scope, project_id, file, description, steps[], errors[]?, warnings[]?, error? }`.

`POST /v1/resolve` applies the [resolution order](workflow-schema.md#resolution-order)
to every step under a candidate task-level override, returning `{ value, source }`
per field — `source` being the winning level (`step`, `task`, `workflow`,
`adapter`). Non-agent steps keep their index with null fields, so a client can zip
the two lists positionally.

When the request names a `project_id` it also returns `branch`, the name this draft
task would get:

```json
{ "value": "feat/OPS-123-retry-logic", "source": "project", "placeholder": false }
```

`source` is the winning level of the branch chain (`default`, `config`, `project`,
`task`). `placeholder: true` means `value` carries a literal `<id>` where the task id
will go, because the id does not exist until the task is created — the daemon does not
guess the next one. This is why a client should not render branch templates itself:
resolution stays server-side so there is only ever one implementation of the
precedence.

An empty value with source `adapter` means the adapter names no default of its
own and the CLI decides at run time — which the TUI renders as "CLI default"
rather than inventing a model name.

**Resolution is server-side only.** Clients report it; they never re-derive it.

## Tasks

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/tasks?project_id=&state=&archived=&limit=&offset=` | List |
| `POST` | `/v1/tasks` | `{ project_id, workflow, title, description?, fields?, base_branch?, branch_name?, priority?, agent?, model?, effort? }` — `branch_name` is used verbatim and wins over any template |
| `GET` | `/v1/tasks/{id}` | Full task |
| `PATCH` | `/v1/tasks/{id}` | `{ priority }` — queued/paused only |
| `GET` | `/v1/tasks/{id}/steps` | Every step run, every attempt |

Human actions, all `POST /v1/tasks/{id}/…`:

| Path | Valid from | Body |
|---|---|---|
| `/cancel` | most states | |
| `/pause` | queued, running | |
| `/resume` | paused | |
| `/retry` | blocked | `{ prompt_override?, run_override?, branch_override? }` — `branch_override` renames the branch before re-admission, which is how a `branch_exists` block is recovered |
| `/skip` | blocked, awaiting_gate | |
| `/approve` | awaiting_gate | |
| `/reject` | awaiting_gate | |
| `/answer` | awaiting_input | `{ answers?, allow? }` |
| `/archive` | done, aborted | `{ force? }` or `?force` |

Anything else returns `409` with `details.state`. See
[Task lifecycle](task-lifecycle.md).

Four details worth knowing:

- **A `queued` task may be waiting on a clock, not a slot.** Every task
  representation carries `queued_reason` and `admit_not_before` (RFC3339, or
  `null`). Both are `null` for an ordinarily queued task; `usage_limit` with a
  timestamp means the agent's usage window is spent and vincent will try again
  then, unattended. They are separate from `block_reason`, which still means
  only "stopped, needs a human" — the task is not blocked.

- **List rows carry the board fields** — `project_name`, `step_total`,
  `step_name`, and `cost_usd` / `input_tokens` / `output_tokens` rolled up across
  every attempt — so a board renders without an N+1. Those are list-only;
  `GET /v1/tasks/{id}` serves the same numbers per attempt in `steps[]`.
- **`?archived=` defaults to false.** `true` selects only archived tasks, `all`
  returns both.
- **Every task representation carries `available_actions`** (the actions valid
  right now) and `pause_requested`, so clients never restate the state machine.
  The detail response adds `workflow_steps[]` — this task's snapshot, which is
  what edit-and-retry prefills an editor with, reflecting any earlier edit.

Task creation validates the agent/model/effort override: a known-invalid value is
`400`, a catalog-unknown one is reported in `warnings[]` on the `201` body.

## Transcripts and diffs

```
GET /v1/tasks/{id}/steps/{run_id}/transcript?offset=&tail=&format=
GET /v1/tasks/{id}/diff
```

The transcript is the attempt's JSONL file, ranged:

- `offset=` (bytes) and `tail=` (last N bytes) are mutually exclusive.
- `tail` opens at the **start of the record its byte count lands in**, so a
  window narrower than the last record still returns that record rather than
  nothing. `offset` is taken as given.
- The body always ends on a complete line, and `X-Next-Offset` reports that
  boundary — never mid-record, so a follow-up fetch on a file still being
  appended to resumes cleanly.
- `format=normalized` maps every line through the owning adapter's parser into
  the live-output shapes plus `agent.result`, `agent.error`, the `vincent.*`
  kinds, and `agent.raw` for anything unrecognized. That is one render path for
  live tail and scrollback alike. Absent, you get the raw file byte for byte.

Because normalization runs **on read**, enriching a parser improves transcripts
already on disk.

`GET …/diff` is a unified diff of the worktree against the merge-base with the
base branch, including uncommitted changes. Untracked files are excluded — a
documented limitation.

## Events (SSE)

```
GET /v1/events?types=&project_id=
GET /v1/tasks/{id}/events
```

Two kinds of stream, with deliberately different guarantees.

### State events — durable

Persisted to the events table with a monotonic id and emitted with `id:` set, so
a client reconnecting with `Last-Event-ID` misses nothing.

```
task.created            task.state_changed      task.priority_changed
task.step_advanced      project.*               workflow.registry_changed
daemon.shutting_down
```

Payloads carry ids and the new state, not full objects — clients re-fetch what
they need.

- **A connection without `Last-Event-ID` starts live at the next committed
  event.** The stream never replays history unasked: catch-up is a REST snapshot
  first, then the stream.
- There is no separate `task.archived` or `task.awaiting_input` type. Both are
  `task.state_changed` with the appropriate `to`; the `awaiting_input` payload
  additionally carries the request kind and a one-line summary, which is the
  alert clients key off. The full request comes from `GET /v1/tasks/{id}`.
- `task.step_advanced` carries `{ current_step }` when the engine moves the
  cursor without a state change, so a board's `k/n` tracks a run instead of
  freezing.

### Live output — ephemeral

`agent.output`, `agent.tool_use`, `agent.tool_result`, `agent.thinking`,
`agent.usage` and `command.output` chunks stream on the **per-task** stream only
and are **not** written to the events table. Their durable copy is the transcript
file.

Each chunk is one SSE event, flushed on a ~100 ms coalescing timer, and carries:

- `run_id` — the step-run row that produced it, and
- `offset` — the byte position in that attempt's transcript file *after* its line
  was written.

Together those make the catch-up seam exact: fetch the transcript, then discard
buffered chunks whose `run_id` matches the attempt you fetched and whose `offset`
is at or before the fetch's `X-Next-Offset`. `run_id` is load-bearing on its own,
because offsets restart at zero in every attempt's file.

`Last-Event-ID` on the per-task stream resumes its **durable** events only; live
output is not replayable.

### Back-pressure

The two kinds fail differently on purpose. A slow subscriber has **live output
chunks dropped** — the transcript is the durable copy. A slow subscriber to
**durable state events is disconnected** instead, so it reconnects and resumes
from the events table via `Last-Event-ID`. Fan-out is post-commit only: the
store publishes after the database has recorded the event.

---

## See also

- [Scripting vincent](../guides/scripting.md) — worked examples.
- [Task lifecycle](task-lifecycle.md) — what the action endpoints do.
- Spec [§13](../spec.md) — the normative definition.
