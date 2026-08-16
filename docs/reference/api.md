# HTTP API

The daemon serves REST + SSE on loopback. Every client — the TUI, the
`vincent` subcommands, your script — uses this and nothing else.

- [Transport and auth](#transport-and-auth)
- [Errors](#errors)
- [Daemon](#daemon)
- [Doctor](#doctor)
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
| `GET` | `/v1/info` | Version, uptime, agent availability, caps in effect, and `orphans` |
| `GET` | `/v1/config` | The effective global config, read-only |
| `GET` | `/v1/agents` | Per-adapter availability plus model/effort options. `?refresh=true` forces a re-probe |
| `GET` | `/v1/doctor` | The whole diagnostic report. Read-only — see [Doctor](#doctor) |
| `POST` | `/v1/doctor/fix` | Removes orphaned worktrees and compacts the database |
| `POST` | `/v1/daemon/stop` | Graceful shutdown → `202`, then the daemon exits |
| `GET` | `/v1/maintenance/orphans` | Directories under the data dir no task claims, with sizes. Removes nothing |
| `POST` | `/v1/maintenance/gc` | `{ force?, dry_run? }` — reclaims them. Same body shape as the list |

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
authentication probe (**claude**, whose CLI exposes no non-interactive auth
surface) and a definite boolean where it does (**codex** via `login status`,
**cursor** via `status`) — because an installed-but-unauthenticated CLI probes
as healthy and then fails every run. It is never guessed: a probe that times out
or cannot be spawned reports `null`, not `false`.

## Doctor

`GET /v1/doctor` is one read-only body carrying every group
[`vincent doctor`](cli.md#vincent-doctor) renders:

```json
{
  "generated_at": "2026-08-15T10:00:00Z",
  "paths":    { "config_dir": "…", "data_dir": "…", "config_file": "…",
                "config_file_exists": true, "config_parses": true },
  "daemon":   { "status": "running", "pid": 4021, "port": 51234,
                "started_at": "2026-08-15T09:00:00Z", "uptime_seconds": 3600,
                "version": "0.1.1" },
  "log":      { "path": "…", "exists": true, "size_bytes": 18244,
                "mod_time": "…", "tail": ["…"] },
  "database": { "path": "…", "known": true, "size_bytes": 262144,
                "schema_version": 6, "newest_migration": 6,
                "integrity_check": "ok" },
  "agents":   [ { "name": "codex", "available": true, "path": "…",
                  "version": "0.147.0", "logged_in": true } ],
  "storage":  { "worktrees_dir": "…", "disk_free_bytes": 127310651392,
                "disk_total_bytes": 494384795648,
                "worktree_count": 3, "worktree_bytes": 8412736,
                "orphans_known": true, "orphans": [] },
  "tasks":    { "known": true, "total": 14,
                "counts": { "queued": 1, "running": 1, "blocked": 12, "…": 0 } },
  "problems": []
}
```

- **`problems[]` is the daemon's verdict**, not something a client re-derives:
  it is the closed set that makes the CLI exit `1` (config that does not parse,
  an unresponsive daemon, a failed `integrity_check`, a schema newer than the
  binary, or orphaned worktrees). A missing or logged-out agent CLI and any
  number of blocked tasks are reported and never appear here.
- **Agent availability is re-probed unconditionally**, unlike `GET /v1/agents`.
  Authentication is not a function of the binary, so a cached `logged_in: false`
  would survive the user logging in — which would break the endpoint in the loop
  it exists for.
- `known: false` on `database` or `tasks` means the report was composed without
  a daemon (the CLI's degraded path); over this endpoint they are always `true`.
- An **orphan** is an entry under a data root that no task row claims — the same
  set `GET /v1/maintenance/orphans` returns, from the same scan. Each carries
  `kind` (`worktree` or `transcript`), `task_id`, `size_bytes`, and `skip` when
  gc would leave it alone (`worktree_dirty`, `dirty_unknown`, `not_a_directory`).
  `orphans_known` is `false` only when the daemon has no reclaimer wired.

`POST /v1/doctor/fix` takes `{ "force": true }` or `?force` and answers with
what it did plus a report taken afterwards:

```json
{ "actions": [
    { "action": "remove_worktree", "target": "…/worktrees/41",
      "status": "done", "freed_bytes": 2113536,
      "detail": "run `git worktree prune` in the project repo to clear its stale registration" },
    { "action": "compact_database", "target": "…/vincent.db",
      "status": "skipped", "detail": "2 task(s) in flight; a VACUUM would stall them mid-step" } ],
  "report": { "…": "…" } }
```

`status` is `done`, `skipped` or `failed`, and a skip always carries its reason.
It is a separate method from the `GET` on purpose: a call that deletes
directories is a different promise from a report.

`orphans` on `GET /v1/info` counts directories under `{data_dir}/worktrees` and
`{data_dir}/transcripts` that no task row claims. It is computed per request from
a readdir and one id query — no size walk, no git — so it is cheap and drops the
moment `gc` runs. It is deliberately **not** on `/v1/health`, which stays
`{ status, version }` and is the one unauthenticated endpoint.

The two maintenance endpoints share one body, so a dry run and a real run are
compared field by field:

```json
{ "orphans": [ { "path": "/home/u/.local/share/vincent/worktrees/41",
                 "kind": "worktree", "task_id": 41, "bytes": 13010000,
                 "skip_reason": "dirty_unknown", "removed": false } ],
  "mismatches": [ { "task_id": 58, "path": "…/worktrees/58", "state": "blocked" } ],
  "bytes": 13010000, "reclaimed": 0, "reclaimed_bytes": 0,
  "dry_run": false, "force": false }
```

`kind` is `worktree` or `transcript`. `task_id` is `null` when the directory's
name is not an id — the claim decides, not the name. `skip_reason` is why gc
declined (`worktree_dirty`, `dirty_unknown`, `not_a_directory`); `error` is a
removal that was attempted and failed, and the run continues past it, so
`reclaimed` and `reclaimed_bytes` count only what actually went. `mismatches[]`
is the reverse case — rows whose `worktree_path` points at a directory that is
gone — reported only; gc modifies no row and deletes nothing outside the two data
roots.

See [`vincent gc`](cli.md#vincent-gc) for the command over these endpoints.

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
Before the rows go, every branch that has no commits past its base is deleted —
archived rows included, because the cascade erases the branch names for good.
**A branch carrying a commit is never deleted**, and no remote is ever touched
here; failures are logged and the delete proceeds. Set
[`delete_empty_branch_on_archive: false`](configuration.md#delete_empty_branch_on_archive)
to disable the sweep.

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

`/archive` is the one action whose response is not just the task. When it looks
at the branch, it adds a `branch` object beside the task fields:

```json
{
  "id": 7, "state": "archived", "…": "…",
  "branch": {
    "name": "vincent/7-file-an-issue",
    "result": "deleted",
    "remote": { "remote": "origin", "ref": "refs/heads/vincent/7-file-an-issue", "result": "deleted" }
  }
}
```

`result` is `deleted` (no commits past its base), `has_commits` (kept), `unknown`
(git could not judge it — base branch renamed away, repository gone) or `error`
(the delete itself failed), with git's message in `error` for the last two. The
`remote` object appears only when
[`delete_remote_branch_on_archive`](configuration.md#delete_remote_branch_on_archive)
is on and the local delete succeeded; its `result` is `deleted`, `no_upstream` or
`error`. The whole `branch` object is **absent** when nothing was checked — the
cleanup is off, or the task never had a branch of its own.

None of it affects the status code: a branch problem never fails an archive.

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
