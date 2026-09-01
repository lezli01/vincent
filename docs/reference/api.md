{% raw %}

# HTTP API

The daemon serves REST + SSE on loopback. Every client — the TUI, the
`vincent` subcommands, your script — uses this and nothing else.

- [Transport and auth](#transport-and-auth)
- [Errors](#errors)
- [Request bodies](#request-bodies)
- [Daemon](#daemon)
- [Doctor](#doctor)
- [Projects](#projects)
- [Workflows](#workflows)
- [Tasks](#tasks)
- [Chats](#chats)
- [Transcripts and diffs](#transcripts-and-diffs)
- [Events (SSE)](#events-sse)
- [MCP](#mcp)

---

## Transport and auth

- HTTP/1.1 + JSON on `127.0.0.1` only. **No TLS** — it is loopback.
- Every request needs `Authorization: Bearer {token}`, where the token is the
  contents of `{data_dir}/token` (created `0600`). This is what keeps other
  local users and drive-by browser requests out; CORS is additionally disabled.
- **Discovery:** read `{data_dir}/daemon.json` for the port, then
  `GET /v1/health`.
- **Versioning:** path-prefixed (`/v1`), additive changes only within a version.
- **`Idempotency-Key`** (optional, `POST /v1/tasks` only) makes a create
  replayable: if the daemon committed the task but you never saw the response,
  re-sending the *same body* under the *same key* returns that task instead of
  making a second one. Up to 255 bytes of printable ASCII; anything else is
  `400 validation_failed`. Keys are scoped to the route and kept for **24
  hours**, which is fixed rather than configurable. Every other mutating route
  is already replay-safe and ignores the header — see
  [`POST /v1/tasks`](#tasks) for what a replay returns.

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

Every `409` carries the code `invalid_state`; what varies is `details`. A
conflict that is not about a task's state names itself in `details.reason` —
`idempotency_key_reused` when an `Idempotency-Key` is re-sent with a different
body, and the GitHub integration's reasons on the issue routes. Branch on
`details`, not on a per-case code.

## Request bodies

Three rules apply to every request body, before the endpoint sees it.

**One JSON document.** A body is one JSON value followed only by whitespace.
Two concatenated documents — a retry that rewrites the body, a `jq -c` loop
piped into one `curl -d @-` — are `400 invalid_json`. Nothing after the first
document is ever acted on, and nothing is silently discarded.

**Bounded.** Bodies are read up to a fixed limit and no further. Over it is
`413 payload_too_large` with a message naming the limit; the body is never
echoed back.

| Limit | Bytes | Applies to |
|---|---|---|
| Ordinary request body | 64 KiB | every route not listed below |
| Large request body | 4 MiB | `POST /v1/tasks`, `POST /v1/resolve`, `POST /v1/tasks/{id}/retry`, `/repair`, `/answer`, `POST /v1/workflows/validate`, `PATCH /v1/workflows` — the bodies that carry a prompt or a workflow source |
| `yaml` in `POST /v1/workflows/validate` | 1 MiB | the same bound a workflow file gets when the registry loads it |

Individual fields are bounded too — over one is `400 validation_failed` naming
the field and the limit:

| Field | Bytes / count |
|---|---|
| `title` | 1 KiB |
| `description` | 64 KiB |
| project `name`, `branch_name`, `base_branch`, `branch_override` | 512 B |
| `prompt`, `prompt_override` | 1 MiB |
| `run_override` | 16 KiB |
| one `fields` key | 256 B |
| one `answers` key | 64 KiB |
| one `fields` / `answers` value | 64 KiB |
| `fields` / `answers` entries, values per answer | 100 |
| `ops` entries in one `PATCH /v1/workflows` | 512 |

The two keys differ because they are different kinds of thing. A `fields` key is
a short identifier you choose. An `answers` key is not yours to choose at all: it
is the agent's question text, which the answer is keyed by and which the daemon
hands back to the CLI unchanged, so it is bounded like the prose it is.

These are fixed constants, not configuration: a body larger than one of them is
a buggy client rather than a workload to tune for.

**Labelled JSON, leniently.** Send `Content-Type: application/json`. A body with
*no* `Content-Type` is accepted (so `curl --data-binary @file.json` with no
header still works), as is any `*/json` or `*+json` type with any parameters. A
non-empty body labelled something clearly not JSON — `text/html`, or the
`application/x-www-form-urlencoded` that a plain `curl -d` sends without `-H` —
is `415 unsupported_media_type`.

The server also bounds how long a request may take to arrive (read-header, whole
request, and idle-connection timeouts). Responses are not bounded: SSE streams
are long-lived by contract and no write deadline is set.

---

## Daemon

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/health` | Liveness → `{ status, version }`. **Unauthenticated** |
| `GET` | `/v1/info` | Version, uptime, agent availability, caps in effect, `orphans`, the database's byte footprint, and the container runtime |
| `GET` | `/v1/config` | The effective global config — **every** key in `config.yaml`, including the `tui` section the daemon only relays |
| `PATCH` | `/v1/config` | Partial, snake_case, mirroring the read shape. Validates, writes `config.yaml` comment-preservingly, and applies the result before answering → the config in force. An invalid patch is `400 validation_failed` with the file byte-identical |
| `GET` | `/v1/agents` | Per-adapter availability plus model/effort options. `?refresh=true` forces a re-probe |
| `GET` | `/v1/doctor` | The whole diagnostic report. Read-only. `?probe=false` skips the forced adapter re-probe — see [Doctor](#doctor) |
| `POST` | `/v1/doctor/fix` | Removes orphaned worktrees and compacts the database |
| `GET` | `/v1/update` | The daemon's **cached** release check — see [Update check](#update-check). Never polls |
| `POST` | `/v1/daemon/stop` | Graceful shutdown → `202`, then the daemon exits |
| `POST` | `/v1/daemon/backup` | `{ path }` — writes a `.tar.gz` of daemon state to `path`. See [Backup](#backup) |
| `GET` | `/v1/maintenance/orphans` | Directories under the data dir no task claims, with sizes. Removes nothing |
| `POST` | `/v1/maintenance/gc` | `{ force?, dry_run? }` — reclaims them. Same body shape as the list |

`GET /v1/agents` is the option catalog the TUI's pickers render:

```json
{ "agents": [ {
    "name": "claude", "available": true, "path": "…", "version": "2.1.224",
    "supports_input": true, "input_verdict": "supported", "logged_in": null,
    "supports_resume": true,
    "version_verdict": "tested", "tested_versions": "2.1.224, 2.1.226",
    "restricted_verdict": "supported",
    "models":  [ { "value": "sonnet", "source": "cli" } ],
    "efforts": [ { "value": "max",    "source": "cli" } ],
    "default_model": "", "default_effort": "",
    "probed_at": "2026-08-07T10:00:00Z", "probe_error": null,
    "quota": null } ] }
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

Adapter health is five separate facets, and each has exactly one field:

| Facet | Field | Notes |
|---|---|---|
| installed | `available`, `path` | |
| authenticated | `logged_in` | `null` = the adapter cannot tell; never a guess |
| protocol-compatible | `supports_input`, `version_verdict` | |
| permission-compatible | `restricted_verdict` | |
| model-catalog | `probe_error` | non-null = you are reading the curated catalog |

`version_verdict` is `tested`, `untested`, `incompatible`, or `""` when nothing
is installed to judge, and `tested_versions` is the build list it was compared
against — whole-string equality, since cursor's calver-plus-sha version admits
no range. It is **advisory**: no endpoint refuses anything on account of it, and
`untested` is the normal answer for a current CLI.

`restricted_verdict` is `supported`, `unsupported` or `unknown`, and it is the
one verdict here that refuses something: `POST /v1/tasks` answers `400
validation_failed` when a step running `permission_mode: restricted` resolves to
an adapter reported `unsupported` (cursor on Windows). It needs no installed
binary — it is a fact about the adapter and the operating system — so it is
answered even for a CLI that is missing.

`version_verdict`, `tested_versions` and `restricted_verdict` also ride
`GET /v1/info`'s and `GET /v1/doctor`'s `agents[]`, beside `supports_input`.

`supports_resume` is whether the adapter can resume its own session, and so
whether it may hold a [chat](cli.md#vincent-chat): `true` for all three shipped
adapters. It is the same answer `POST /v1/chats` gates on, so a
picker built on it and the `agent_cannot_resume` refusal cannot disagree, and
like `restricted_verdict` it is answered for a CLI that is missing. It is
`null` — never `false` — when the daemon has no adapter registry to ask, and a
daemon that predates the field omits it: both mean "no judgement", and nothing
may be filtered out on either. It rides this route only.

### Backup

`POST /v1/daemon/backup` writes one archive of everything the daemon owns and
answers with what it wrote:

```json
{ "path": "/home/you/vincent-2026-08-25.tar.gz",
  "bytes": 1503238553,
  "database_bytes": 8601600,
  "transcript_bytes": 1494360064,
  "schema_version": 13,
  "created_at": "2026-08-25T14:05:00.000000000Z" }
```

- `path` must be **absolute** and must not exist. The daemon resolves it
  against its own working directory, not the caller's, and it never overwrites
  a file that is by construction somebody's backup. A path inside
  `{data_dir}/transcripts` is refused too — the archive would read itself.
  Every one of these is a `400 validation_failed`.
- The **daemon** assembles the whole archive: the database copy *and* the two
  directory trees. That keeps exactly one process walking daemon-owned state,
  and it is why there is no cold-copy mode — only the daemon opens SQLite.
- The database copy is `VACUUM INTO`, which runs in a read transaction, so a
  backup may be taken **while tasks are running**. It costs the store's single
  connection for the duration of the copy: other queries queue behind it,
  bounded by the size of the database.
- The archive layout, what it excludes, and the restore rules are in
  [Files](files.md#backup-and-restore). There is no restore endpoint —
  `vincent daemon restore` runs client-side, because the daemon it would
  overwrite has to be down.

### Usage quota

`quota` is what the daemon has **watched happen** to that adapter's usage
window. It is an observation, not a measurement: none of `claude`, `codex` or
`cursor` can report remaining quota from a non-interactive invocation, so
vincent reports the `usage_limit` stops it has seen for itself rather than a
number nothing can produce.

```json
"quota": {
  "spent": true,
  "used_percent": null,
  "window": null,
  "observed_at": "2026-08-24T14:05:00Z",
  "resets_at": "2026-08-24T14:20:00Z",
  "resets_at_reported": true,
  "source": "observed"
}
```

- `null` — never a zeroed block — means nothing has ever been observed for that
  adapter, which is the normal state. A zero would read as "empty quota".
- `spent` is derived per request (`now < resets_at`). A window that has reset
  does **not** delete the observation: `spent: false` with `observed_at` and
  `resets_at` intact is how "this adapter ran out at 14:05 and has since
  recovered" is said.
- `resets_at_reported` separates a fact from an estimate. `true` means the CLI
  named the reset; `false` means
  [`usage_limit_recheck_interval`](configuration.md) supplied it, and a client
  must not render a computed guess as something the CLI stated.
- `used_percent` and `window` are permanently `null`. They are declared so a
  client written against this shape keeps working the day a vendor ships a
  quota surface, at which point they fill in and `source` changes.
- `source` is `observed` for everything written today.
- An observation is **retired by evidence**: the next successful agent step on
  that adapter deletes it. Nothing sweeps it on a timer.

The same block rides `GET /v1/info` per adapter, so a client rendering a badge
from `/v1/info` needs no second fetch. Both are served from one read, so the two
endpoints can never disagree. Changes are announced by the
[`agent.quota_changed`](#events-sse) event.

## Update check

`GET /v1/update` serves the release check
[`update`](configuration.md#update) governs:

```json
{
  "enabled": true,
  "current_version": "v0.4.1",
  "latest_version": "v0.5.0",
  "update_available": true,
  "published_at": "2026-08-21T09:31:07Z",
  "release_url": "https://github.com/lezli01/vincent/releases/tag/v0.5.0",
  "checked_at": "2026-08-29T10:00:00Z"
}
```

It serves **the cache and nothing else**. There is deliberately no `?refresh`:
`update.check: false` promises the daemon makes no outbound request, and a
parameter that made one on demand would hand any client the ability to break
that. [`vincent update --check`](cli.md#vincent-update) queries the release feed
itself instead, which is also why it works before the first poll and with no
daemon running.

- `checked_at` is `null` and `latest_version` empty until a poll succeeds. That
  is the **never-polled** state and it is not the same answer as "no update
  available" — a daemon that started ten seconds ago does not know yet.
- `update_available` is the daemon's verdict, computed server-side, so every
  client agrees. A `dev` build is never reported as behind.
- `current_version` is the **daemon's** build. The binary that made the request
  may be a newer one, which is exactly what `vincent daemon status` reports
  after an update.
- `error` carries why the last poll failed and is **absent** when it worked, as
  above. A quietly failing check would otherwise look identical to one with
  nothing to report.
- A prerelease never appears here.

## Doctor

`GET /v1/doctor` is one read-only body carrying every group
[`vincent doctor`](cli.md#vincent-doctor) renders:

```json
{
  "generated_at": "2026-08-15T10:00:00Z",
  "paths":    { "config_dir": "…", "data_dir": "…", "config_file": "…",
                "config_file_exists": true, "config_parses": true,
                "config_permissions": [] },
  "daemon":   { "status": "running", "pid": 4021, "port": 51234,
                "started_at": "2026-08-15T09:00:00Z", "uptime_seconds": 3600,
                "version": "0.1.1" },
  "log":      { "path": "…", "exists": true, "size_bytes": 18244,
                "mod_time": "…", "tail": ["…"] },
  "database": { "path": "…", "known": true, "size_bytes": 262144,
                "wal_bytes": 4136960, "shm_bytes": 32768,
                "total_bytes": 4431872,
                "schema_version": 13, "newest_migration": 13,
                "integrity_check": "ok",
                "table_rows": { "events": 91234, "step_runs": 812, "tasks": 140,
                                "projects": 7, "agent_quota": 0,
                                "schema_migrations": 13 },
                "oldest_event_at": "2025-06-02T09:11:04Z",
                "workflow_snapshot_bytes": 2179072 },
  "agents":   [ { "name": "codex", "available": true, "path": "…",
                  "version": "0.147.0", "logged_in": true,
                  "supports_input": false, "version_verdict": "tested",
                  "tested_versions": "0.142.5, 0.147.0, 0.150.1",
                  "restricted_verdict": "supported" } ],
  "storage":  { "worktrees_dir": "…", "disk_free_bytes": 127310651392,
                "disk_total_bytes": 494384795648,
                "worktree_count": 3, "worktree_bytes": 8412736,
                "orphans_known": true, "orphans": [] },
  "tasks":    { "known": true, "total": 14,
                "counts": { "queued": 1, "running": 1, "blocked": 12, "…": 0 },
                "unreconciled": [] },
  "problems": []
}
```

- **`problems[]` is the daemon's verdict**, not something a client re-derives:
  it is the closed set that makes the CLI exit `1` (config that does not parse,
  an unresponsive daemon, a failed `integrity_check`, a schema newer than the
  binary, orphaned worktrees, or an unreconciled task). A missing or logged-out
  agent CLI and any number of blocked tasks are reported and never appear here.
- **`paths.config_permissions[]` is a warning, not a verdict.** Each entry is a
  config path whose mode grants group or other access — `{ "path", "mode",
  "expected_mode", "remediation" }`, where `remediation` is the exact `chmod`.
  It never reaches `problems[]` and never changes the CLI's exit code: the
  daemon re-tightens both paths on every start, so an entry means no daemon has
  started on this config or something widened it since. Always empty on Windows,
  where modes carry no access control.
- **`tasks.unreconciled[]`** is the §12.4 contradiction: a task holding a step
  run still marked `running` while sitting in a state that cannot be executing
  one — `queued`, `done`, `aborted` or `archived`. Each entry carries `task_id`,
  `state` and `open_step_runs`. Such a task is refused by admission and will not
  run until crash recovery reconciles it, so it also raises a `tasks` problem.
  The waiting states are deliberately absent: an open run is correct under
  `awaiting_input` and `awaiting_gate`.
- **Agent availability is re-probed by default**, unlike `GET /v1/agents`.
  Authentication is not a function of the binary, so a cached `logged_in: false`
  would survive the user logging in — which would break the endpoint in the loop
  it exists for. Pass **`?probe=false`** to be served from the same cache
  `/v1/agents` uses instead: it is for a caller that is not in that loop and
  wants the rest of the report cheaply — the TUI's daemon panel, which opens on
  a keypress, is the one in the tree. `vincent doctor` always forces.
- **The database group measures growth** and changes nothing about it.
  `total_bytes` is the file plus its WAL and SHM sidecars, which is the honest
  figure: the store runs in WAL mode, so `size_bytes` alone understates the
  footprint between checkpoints, and a missing sidecar counts as zero.
  `table_rows` is enumerated from the schema itself rather than from a fixed
  list, so its key set describes the database this binary is talking to and a
  later migration's table appears with no client change. `oldest_event_at` is
  `null` on an install that has not recorded an event yet.
  `workflow_snapshot_bytes` totals the per-task workflow YAML — the second
  growth driver beside `events`, reported separately because one byte total
  cannot tell "many small events" from "a few enormous snapshots". Nothing here
  prunes, warns, or moves the exit code.
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

`container` on `GET /v1/info` reports the container runtime the way `agents[]`
reports the adapters — presence, never a verdict on an image:

```json
{ "container": { "enabled": true, "image": "ghcr.io/acme/dev:latest",
                 "runtime": "docker", "available": true } }
```

`enabled` is whether `container.image` names an image at all; `available` is
whether that runtime binary answered, which is probed either way so a client can
say "this would work if you turned it on". A failure adds `error` with the
runtime's own words, and on a Windows daemon `available` is `false` with an
`error` saying containerized tasks are not supported there. Whether a particular
image exists is **not** here: that is a registry pull, and it happens when a task
is admitted.

`database` on `GET /v1/info` carries the byte figures and **only** those:

```json
{ "database": { "path": "…/vincent.db", "size_bytes": 262144,
                "wal_bytes": 4136960, "shm_bytes": 32768,
                "total_bytes": 4431872 } }
```

Three `os.Stat` calls per request, which is the same cheapness rule that admits
`orphans` here. The row counts, the retention span and the workflow-snapshot
total are scans and are on `GET /v1/doctor` instead — this endpoint is polled by
the board, the projects view and the daemon view on every debounced refresh, and
a `COUNT(*)` over a multi-million-row `events` table does not belong on that
path. Nothing is cached, so nothing is stale. Like `orphans`, none of it goes on
`/v1/health`.

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
| `GET` | `/v1/projects/{id}/github` | Can this project's GitHub issues be read? |
| `GET` | `/v1/projects/{id}/github/issues` | Its issues, newest first — `?state=`, `?limit=`, `?workflow=` |
| `GET` | `/v1/projects/{id}/github/pulls` | Its pull requests, newest first — `?state=`, `?limit=`, `?workflow=` |

`DELETE` succeeds only when no non-archived tasks remain. `?force` archives them
first (force-removing worktrees), and is refused while any task is running.
Before the rows go, every branch that has no commits past its base is deleted —
archived rows included, because the cascade erases the branch names for good.
**A branch carrying a commit is never deleted**, and no remote is ever touched
here; failures are logged and the delete proceeds. Set
[`delete_empty_branch_on_archive: false`](configuration.md#delete_empty_branch_on_archive)
to disable the sweep.

### GitHub issues

`GET /v1/projects/{id}/github` is the capability probe. It is a separate call
rather than three fields on the project object because the board lists projects
constantly, and answering it there would run `gh auth status` per project on
every refresh:

```json
{ "enabled": true, "repo": "lezli01/vincent", "available": true, "via": "gh" }
```

`enabled` is [`github.enabled`](configuration.md#github). `repo` is derived from
the project's `origin` remote at the moment you ask, and is absent when that
remote is not a github.com URL. `via` is `gh` or `token`. When `available` is
false the body carries a `reason` and a human-readable `message`:

| `reason` | Meaning |
|---|---|
| `disabled` | `github.enabled` is false |
| `not_github` | No `origin`, or one that is not a github.com repository |
| `no_credential` | `gh` is absent or logged out, and neither `GITHUB_TOKEN` nor `GH_TOKEN` is set |
| `unauthorized` | GitHub rejected the credential |
| `forbidden` | Authenticated, but not permitted to read this repository's issues or pull requests |
| `not_found` | No such repository, issue or pull request |
| `rate_limited` | The API rate limit is spent |
| `timeout` | GitHub did not answer in time |
| `unreachable` | The call failed, or the API answered something with no more specific meaning |
| `bad_response` | The answer arrived and did not parse |

Those reasons are the whole client-facing vocabulary. `gh`'s stderr and the
API's response body never appear in any of these fields — they go to the daemon
log.

`GET /v1/projects/{id}/github/issues` lists the repository's issues, newest
first. Pull requests are never included. `?state=` takes `open` (the default),
`closed` or `all`; `?limit=` caps the rows. There is no `?q=` — narrow the list
client-side, the way the TUI's picker does.

```json
[
  {
    "repo": "lezli01/vincent", "number": 200,
    "title": "GitHub integration: select a GitHub issue when creating a task",
    "body": "### Problem\n\n…", "url": "https://github.com/lezli01/vincent/issues/200",
    "state": "open", "labels": ["enhancement"], "author": "lezli01",
    "created_at": "2026-08-26T19:21:29Z", "updated_at": "2026-08-26T19:30:00Z",
    "fetched_at": "2026-08-26T20:04:11Z"
  }
]
```

Adding `?workflow=<name>` attaches a `prefill` object to every row — the
daemon's own answer to "what would creating a task from this issue fill in":

```json
{ "prefill": { "title": "#200 …", "description": "…\n\nGitHub issue #200: https://…",
               "fields": { "issue": "200", "labels": "enhancement" } } }
```

`title` is the issue title prefixed `#N`. `fields` holds only the declared
fields named exactly `issue`, `labels`, `assignee` or `milestone` whose declared
type and pattern accept the value — `issue` being the issue number, which is how
a `command` step reads it, since step bodies see the environment rather than the
template context.

`POST /v1/tasks` computes exactly the same prefill from the same code, so a
preview a human accepted and a create call that names only the issue produce the
same task. An unknown workflow name is `400 validation_failed`.

### GitHub pull requests

`GET /v1/projects/{id}/github/pulls` lists the repository's pull requests,
newest first. `?state=` is `open` (the default), `closed` or `all`; `?limit=`
caps the rows; `?workflow=` adds a computed `prefill` per row — the same shape
the issue listing carries, and the same one `POST /v1/tasks` applies when you
name a pull request. It goes through the same capability gate as the issue
listing, so a disabled integration or a project whose `origin` is not a
github.com repository makes no call at all.

The default stays open-only on purpose: it is the question the screen usually
asks, and pulling a repository's whole pull-request history to answer it would
be paid for by everyone. Reaching a closed or merged one is a choice you make.

It is a pure read: it fetches, normalizes, sorts and returns, and **persists
nothing**. Linking is the daemon's own job (below).

```json
[
  {
    "repo": "lezli01/vincent", "number": 412,
    "title": "List a GitHub project's open pull requests",
    "url": "https://github.com/lezli01/vincent/pull/412",
    "state": "open", "draft": false, "merged": false,
    "head_branch": "vincent/231-list-open-pull-requests", "base_branch": "master",
    "author": "lezli01",
    "created_at": "2026-08-26T19:21:29Z", "updated_at": "2026-08-27T09:02:11Z",
    "fetched_at": "2026-08-29T12:00:00Z",
    "task_id": 61, "link_source": "auto"
  }
]
```

`state` is `open` or `closed`; a merged pull request is closed and carries
`merged: true`. `task_id` and `link_source` are present on the rows a task
claims — `auto` when the daemon matched the head branch, `human` when a person
said so.

Nothing about a pull request is ever stored. What a task keeps is a *pointer* —
`github_pull`, holding `{repo, number, source, suppressed, linked_at, branch,
fork}` — and everything renderable is re-read on every request, because draft,
state and merged status are live by nature and a snapshot of them would read
exactly like a current one while being wrong. `branch` and `fork` are not
renderable either: `branch: true` says this task's `branch_name` **is** this pull
request's head branch, because the task was created from it
([`github_pull` on `POST /v1/tasks`](#tasks)), and `fork: true` that the head
lives in another repository, so the branch carries no upstream and nothing can be
pushed back. Both are absent on a link a reconciler or a human made.

| Method | Path | Body / notes |
|---|---|---|
| `GET` | `/v1/tasks/{id}/github/pull` | This task's pull request, fetched live |
| `POST` | `/v1/tasks/{id}/github/pull` | `{ number }` — link by hand |
| `DELETE` | `/v1/tasks/{id}/github/pull` | Unlink, and remember the refusal |
| `GET` | `/v1/tasks/{id}/github/pull/checks` | The live check rollup for the linked pull request's head commit |
| `POST` | `/v1/tasks/{id}/github/pull/create` | `{ title, body, draft }` — push the branch and open the pull request |

```json
{
  "linked": true, "repo": "lezli01/vincent", "number": 412, "source": "auto",
  "pull": { "number": 412, "state": "closed", "merged": true, "…": "…" }
}
```

`GET /v1/tasks/{id}/github/pull/checks` answers with one normalized row per
check on the pull request's **head commit**:

```json
{
  "linked": true, "repo": "lezli01/vincent", "number": 412,
  "ref": "d3adb33f…", "state": "failure",
  "fetched_at": "2026-08-31T09:14:02Z",
  "runs": [
    { "name": "test", "state": "in_progress",
      "url": "https://github.com/lezli01/vincent/actions/runs/5150/job/72",
      "run_id": 5150 },
    { "name": "license/cla", "state": "success", "url": "https://cla.example/…" }
  ]
}
```

`state` is one word, on the rollup and on each row: `queued`, `in_progress`,
`success`, `failure`, `cancelled`, `skipped`, `neutral`, `timed_out`,
`action_required` or `stale`. Failure wins over running on the rollup — a pull
request with one failed job and five still going is failed, and calling it in
progress would hide the only fact worth acting on. GitHub's check runs and its
older commit statuses are folded onto the same shape, so the two credential
legs cannot be told apart. `run_id` is the GitHub Actions workflow run behind a
row and is **absent** for a third-party check run or a legacy commit status —
that is what distinguishes a row an operation like re-run could honestly be
offered on. A row also carries `started_at` and `completed_at` when GitHub
reported them. Nothing here is cached or stored: a check result a minute old
reads exactly like a current one while being wrong, so `fetched_at` is the only
honest answer to how old this one is.

Both `GET`s answer **200 whatever GitHub says**. A task workspace asks it on every
open, and refusing the whole row because the integration is off would take a
fact vincent owns away from a client that can still render it — so the stored
link is always served, and `reason` carries the named vocabulary above when the
live fetch could not be made. It fetches by number rather than looking in the
listing, which is what lets a task still name a pull request that has since
**merged** and dropped off an open-only listing.

For a task with no link, the response carries `compare_url` instead: GitHub's
own "open a pull request" page for the task's branch, prefilled with the task's
title and description plus `Closes #N` when the task carries an issue snapshot
from the same repository. It is **built, never fetched** — producing it makes no
request to GitHub — and it is the fallback behind
`POST /v1/tasks/{id}/github/pull/create` below.

### Opening a pull request

`POST /v1/tasks/{id}/github/pull/create` is the **one route in vincent that
writes to GitHub**. It pushes the task's branch to `origin` and creates its pull
request:

```sh
curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Add rate limiting","body":"Closes #12","draft":true}' \
  http://127.0.0.1:8765/v1/tasks/61/github/pull/create
```

It is gated by [`github.enabled`](configuration.md#github) and the same
capability probe every read is, and there is no second switch: the consent is a
person asking for it. It is deliberately **not** an
[MCP tool](#mcp) — an agent that wants a pull request has a shell in its own
worktree and can run `git push` and `gh pr create` there.

The push uses `--set-upstream` and **never** `--force`. Only committed work
reaches it: anything uncommitted in the task's worktree is not in the pull
request.

There are three answers, and only one of them is an error.

**Created** — 200, with the pull request and the task whose link was written
immediately as `human`, so nothing has to wait for the reconciler's next tick:

```json
{ "created": true, "pushed": true, "branch": "vincent/61-add-rate-limiting",
  "remote": "origin", "pull": { "number": 412, "draft": true, "…": "…" },
  "task": { "id": 61, "…": "…" } }
```

**Pushed, not created** — also 200, and also not a failure. There was no
credential with write scope, or GitHub refused the create; the branch is on the
remote, so GitHub's own page works and a client opens it exactly as it would
have before:

```json
{ "created": false, "pushed": true, "branch": "vincent/61-add-rate-limiting",
  "remote": "origin", "reason": "forbidden",
  "message": "the credential may not do this in this repository",
  "compare_url": "https://github.com/octo/repo/compare/main...vincent%2F61-add-rate-limiting?expand=1&title=…" }
```

**Push refused** — 409, and nothing was attempted at GitHub, because a pull
request for a head the remote does not have would be a dead page. The reason is
`push_rejected` (a non-fast-forward, a protected branch, a declined hook),
`push_no_credential` or `push_failed`. A task that already has a live link is
also 409, with `pull_already_linked`: unlink it first.

`POST` is the human link, for a pull request the head-branch rule misses (one
opened from a branch vincent did not create) or gets wrong. It writes vincent's
own column and makes no GitHub call, not even to check the number exists —
validating it would put a network failure in the way of a person correcting
vincent, and a wrong number shows as `not_found` the moment it is displayed. A
human link is never overwritten by the daemon.

`DELETE` does not clear the column: it marks the link **suppressed**, keeping
the repo and number. That is what makes a human unlink stick — the daemon has to
be able to read the refusal, and an empty column would only say "never matched".

`POST` and `DELETE` both return the updated task, as does a create that
succeeded.

Linking otherwise happens in the background. Every
[`github.poll_interval`](configuration.md#github) the daemon lists each
GitHub-based project's open pull requests and links the ones whose head branch
equals a task's branch, marking them `auto`. It never overwrites a `human` link
and never un-suppresses one. Set the interval to `0` to switch it off.

The two listings, `POST /v1/tasks` naming a `github_issue`, `POST
/v1/tasks/{id}/github/pull` and `POST /v1/tasks/{id}/github/pull/create` answer
**409** when the integration is not usable, carrying the reason a client can
branch on:

```json
{ "error": { "code": "invalid_state",
             "message": "GitHub is not available for this project: …",
             "details": { "reason": "no_credential" } } }
```

## Workflows

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/workflows?project_id=` | The merged registry: built-in + global + that project's, with shadowing applied |
| `GET` | `/v1/workflows/definition?name=&project_id=` | One workflow's whole recursive structure, with the same shadowing applied |
| `GET` | `/v1/workflows/schema` | The workflow schema as data: which fields are legal on which step type, and where each type may be nested |
| `POST` | `/v1/workflows` | `{ scope, project_id?, name, from?, from_project_id? }` → the written file and its version. Creates a workflow; `from` forks an existing entry, keeping its `name:` so the copy shadows it |
| `PATCH` | `/v1/workflows?name=&project_id=` | `{ version, ops[] }` → the same shape. Applies edit operations to a workflow file, preserving every byte outside the region they touch |
| `POST` | `/v1/workflows/validate` | `{ yaml }` → `{ valid, errors[], warnings[] }` |
| `POST` | `/v1/resolve` | `{ workflow, project_id?, agent?, model?, effort?, title?, fields?, base_branch?, branch_name? }` → resolution per step, plus the previewed branch name |

Registry entries carry
`{ name, scope, project_id, file, description, fields[], steps[], platforms[]?, platform_supported, requires_input, includes[]?, version?, errors[]?, warnings[]?, error? }`.

`fields[]` is the selected workflow's ordered
[`fields:` declaration](workflow-schema.md#fields). Each entry is
`{ name, label?, description?, type, required, pattern?, values[]?, multiple?, default? }`;
`type` is always explicit (`string` when the YAML omitted it). An empty list
means the workflow publishes no task-input contract, not that task fields are
forbidden.

`values[]` is an `enum` field's members in declared order, `multiple` says it
accepts more than one of them (joined with `,` in that order), and `default` is
the value that applies when the caller omits the key. All three are absent when
the declaration does not carry them. A client older than `enum` sees an unknown
`type`, falls back to a free-text row, and relies on `POST /v1/tasks` to reject
a non-member.

`platforms[]` is the entry's [platform restriction](workflow-schema.md#platforms)
as the file declares it, and `platform_supported` is **the daemon's own verdict**
on it — the daemon is the process that would run the steps, so clients report
that flag rather than comparing the list to their own OS. An entry with
`platform_supported: false` is listed like any other, but `POST /v1/tasks`
rejects a task naming it with a `400`.

`requires_input` marks an entry with a step declaring
[`on_input: require`](workflow-schema.md#type-agent) that leaves its agent to
the task — the agent chosen for a task on it must be one that can stop and ask
mid-run, or `POST /v1/tasks` rejects it with a `400` naming the step. Each
adapter's `input_verdict` in `GET /v1/agents` (`supported`, `unsupported`,
`unknown`) is the verdict that gate uses; only `unsupported` refuses anything,
so an agent that is not installed never blocks a task.

`includes[]` names the workflows this one splices in with
[`type: include`](workflow-schema.md#type-include). Whether those names resolve
is **not** answered here: which file a name reaches depends on the project's
registry, so an unresolvable or cyclic include is a `400` from `POST /v1/tasks`
rather than an error on the entry.

`version` is the token a write of this entry must carry, described under
[Writing a workflow](#writing-a-workflow). It is absent for a built-in, which
has no file to be stale against.

### One workflow's full definition

`GET /v1/workflows/definition?name=&project_id=` returns the registry entry
above — the same derived fields — plus `definition`, the workflow's whole
recursive structure. The list endpoint's `steps[]` carries only
`{ id, name, type, agent }`, which is right for a registry listing and not
enough to draw a graph: nested `steps`, fan-out `lanes`, `merge`, guards and
loop drivers are gone before a client sees them.

```
GET /v1/workflows/definition?name=feature-pr&project_id=3
```

```json
{
  "name": "feature-pr",
  "scope": "project",
  "project_id": 3,
  "file": "/src/app/.vincent/workflows/feature-pr.yaml",
  "platform_supported": true,
  "requires_input": false,
  "definition": {
    "name": "feature-pr",
    "fields": [
      { "name": "ticket", "label": "Ticket", "type": "string",
        "required": true, "pattern": "^OPS-[0-9]+$" },
      { "name": "environment", "type": "enum", "required": true,
        "values": ["dev", "staging", "prod"], "default": "staging" }
    ],
    "defaults": { "agent": "claude", "model": "sonnet" },
    "steps": [
      { "id": "plan", "type": "agent", "prompt": "…", "check": "go build ./..." },
      { "id": "spread", "type": "fan_out",
        "lanes": [ { "id": "api", "steps": [ … ] },
                   { "id": "web", "workflow": "web-feature", "if": "…" } ],
        "merge": { "on_conflict": "agent", "agent": { "id": "fixup", … } } }
    ]
  }
}
```

Three things about this contract are deliberate.

**The name is a query parameter, not a path segment.** A registry name is
neither URL-safe nor unique. An entry whose file fails to parse is still listed,
under a name taken from an unvalidated `name:` field or the filename — it may
contain anything. And the loser of a duplicate name is listed beside the winner.
The endpoint serves the shadowing winner and reports the `scope` and `file` it
came from, so you can tell which entry you got.

**A workflow that does not parse is a `200`,** carrying its `errors[]` and
`definition: null` — the same rule the list follows in showing a broken file
rather than hiding it. A `404` means no entry of that name in that project's
view of the registry at all.

**Steps are reported as authored.** Workflow `defaults` stay in their own block
and are never folded into the steps that inherit them, so `"agent": "claude"`
written on a step and the same value inherited stay distinguishable — the
distinction the [resolution order](workflow-schema.md#resolution-order) rests on,
and the one anything that round-trips a workflow needs. For the *resolved*
answer, use `POST /v1/resolve`.

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

**Resolution has one implementation.** Clients report it; they never
re-implement the precedence. `vincent workflow render` resolves a **file** —
one this endpoint cannot serve, since it takes a workflow *name* the registry
has frequently not picked up yet — by calling that one implementation directly,
and reports the same `{value, source}`.

### Writing a workflow

`POST /v1/workflows` creates a file, `PATCH /v1/workflows` edits one, and
neither carries YAML in either direction.

A create names a scope and a `name` — lowercase letters, digits, `-`, `_` or
`.`, starting with a letter or digit — and the daemon resolves the path itself:
`{config_dir}/workflows/` for `global`, the project's `.vincent/workflows/` for
`project`, created when it does not exist yet, with `{name}.yaml` inside it. The
client never sends a path. Without `from`, the file is the starter template with
its `name:` set to `name`. With `from`, it is a copy of an existing entry's
bytes and `name` decides only the file name: **the copy keeps the source's own
`name:`**, because keeping it is what makes it shadow the original.
`from_project_id` says whose registry `from` is resolved against, when that is
not the project being written to. Built-ins have no file of their own, so
`scope: builtin` is a `400` — forking one into a scope that does have files is
the only way to change a built-in. New files are written `0644` — a project
workflow is meant to be committed and shared — and an existing file keeps the
mode it has.

A `PATCH` carries edit operations, which the daemon applies to the file's own
bytes line by line. Comments, key order, blank lines, block scalars and CRLF
endings outside the region an operation touches come back byte-identical. That
is why the wire carries operations rather than a document: a client that had to
send the whole file would have had to discard its comments to build it, and the
daemon would then have nothing left to preserve.

```json
{ "version": "…",
  "ops": [
    { "op": "set", "path": "steps[2].prompt", "value": "…", "block": true },
    { "op": "insert", "path": "steps[1]",
      "item": [ { "key": "id", "value": "review" },
                { "key": "type", "value": "agent" } ] },
    { "op": "remove", "path": "steps[0].model" },
    { "op": "move", "path": "steps[0]", "to": 2 } ] }
```

`path` is dotted with list indices — `steps[2].prompt`,
`steps[3].lanes[0].merge.on_conflict`, `fields[1].values`. `block` writes the
value as a `|` block scalar, which is what a prompt, a multi-line `run:` or an
`instructions` body needs. `insert`'s path carries the index the entry lands at,
and `item` is its keys in the order they should be written; the daemon renders
the YAML. `remove` takes either a key path or an indexed one — `steps[0].model`
drops the key, `steps[3].lanes[0]` drops the lane — and dropping a key is not
the same as writing an empty one, since an absent `model:` inherits the
workflow's `defaults` and `model: ""` does not. `move` reorders within one
sequence.

A create answers `201` and a patch `200`, both with
`{ name, scope, file, version, errors[], warnings[] }` — the parse verdict on
the bytes now on disk. The two differ in what they will let you produce. A
`PATCH` the daemon cannot apply, or whose result does not parse, is a `400`
naming the findings and **nothing is written**: the file is byte-identical to
what it was before the request, the same rule
[`PATCH /v1/config`](#daemon) follows. A result over the 1 MiB a workflow source
is bounded to is a `413`, and equally writes nothing. A create can still answer
with a populated `errors[]`, because a fork copies bytes verbatim and the entry
you forked may already be broken — which is the same reason the registry lists a
broken file instead of hiding it.

**`version` is a precondition.** It is an opaque token for a file's current
contents, served on `GET /v1/workflows` and `GET /v1/workflows/definition` and
returned by every write. A `PATCH` carrying a stale one is a `409` with the
current token in `details.version`, and nothing is written. This is scoped to
workflows deliberately: [`PATCH /v1/config`](#daemon) carries no precondition
because there the race is a human against themselves, while a workflow file has
writers who are not the person at the keyboard — the `create-workflow` built-in
writes the live registry directory from an agent run, and `$EDITOR` is one key
away in the same view. A create is refused the same way when the destination
file already exists, or when another file in the target scope already declares
that `name:` — the duplicate the registry would otherwise have to pick between.

`GET /v1/workflows/schema` is the [workflow schema](workflow-schema.md) served
as data: the top-level, `defaults`, `fields[]`, lane and merge rows, and per
step type the fields it accepts, which of the common fields it accepts, and
which contexts (`body`, `parallel`, `loop`, `merge`) it may appear in. It is
generated from the same table the parser validates against, so a client renders
forms from it instead of carrying a second copy of the rules — which is how the
two used to drift. Each row names a `control` telling a client what to draw
(`string`, `text`, `enum`, `bool`, `int`, `duration`, `list`, `map`, `agent`,
`model`, `effort`, `steps`, `lanes`, `lane`, `merge`, `workflow`, `fields`,
`template`); an unrecognized one is drawn as a text row, which is what keeps an
older client usable against a newer daemon. Agent, model and effort value sets
are **not** here — those come from [`GET /v1/agents`](#daemon).

## Tasks

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/tasks?project_id=&state=&archived=&limit=&offset=&parent_id=&include_children=` | List. Fan-out lanes are **excluded** by default — `parent_id` lists one parent's lanes in merge order, `include_children=true` the flat everything |
| `POST` | `/v1/tasks` | `{ project_id, workflow, title, description?, fields?, base_branch?, branch_name?, priority?, agent?, model?, effort?, github_issue?, github_pull? }` — `branch_name` is used verbatim and wins over any template, **except** on a `github_pull` task, whose branch is the pull request's head. Accepts an optional `Idempotency-Key` header |
| `GET` | `/v1/tasks/{id}` | Full task |
| `PATCH` | `/v1/tasks/{id}` | `{ priority }` — queued/paused only |
| `GET` | `/v1/tasks/{id}/steps` | Every step run, every attempt, in position order. `state` may be `stopped` (a `condition` step ended the run, or a `break` ended its loop), and a `skipped` row carries `skip_reason: "condition"` when a guard skipped it and `null` when you did. A row inside a `loop` (§7.8) carries `iteration` (1-based) and, for `for_each`, `loop_item` — a loop's body steps share the loop's `step_index`, so those are what tell two of them apart. A `fan_out` step with `needs:` between its lanes puts its rounds on the same `iteration` column (0-based, so a flat lane list still reads `0`), which is the one other place a non-zero `iteration` appears; the two cannot be confused because a `fan_out` is not valid inside a loop body |
| `POST` | `/v1/tasks/{id}/steps/{step_id}/status` | `{ message }` → `{ message }` as stored. What the **running** step is doing, in its own words. Called by that step's own process — see [Step status](#step-status) |
| `GET` | `/v1/tasks/{id}/workflow` | This task's own workflow **snapshot** as a full definition — what ran, not what the registry says now. See [The task's workflow](#the-tasks-workflow) |

### The task's workflow

`GET /v1/tasks/{id}/workflow` serves the task's own workflow snapshot with the
same structure `GET /v1/workflows/definition` serves for a registry entry — one
DTO describes both. It is what the TUI's **Workflow** tab draws.

It is deliberately not the registry entry of the same name. The snapshot is what
the task actually ran: [`include`](workflow-schema.md#type-include) steps are
already spliced flat, and an `edit + retry` rewrite is reflected. The registry's
copy is whatever the file says right now, which for a running task may be a
different workflow entirely.

```sh
curl -sS "http://127.0.0.1:$PORT/v1/tasks/12/workflow" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "task_id": 12,
  "name": "ship-it",
  "error": null,
  "definition": {
    "name": "ship-it",
    "fields": [],
    "defaults": { "agent": "claude" },
    "steps": [
      { "id": "plan", "type": "agent", "prompt": "plan it" },
      { "id": "lint", "type": "command", "run": "go vet ./...",
        "resolved_from": ["go-checks"] }
    ]
  }
}
```

`resolved_from` names the chain of workflows a step was spliced through,
outermost first — after a splice there is no `include` step left to attribute
it to, so the step carries it.

The envelope carries no `scope`, `file`, `platforms` or `platform_supported`:
those are registry facts a snapshot has none of. Where the task's definition
came from is `workflow_origin` on `GET /v1/tasks/{id}`.

A snapshot that does not parse is a **200** with `errors[]` and
`"definition": null`, never a `4xx` — the same rule
[`/v1/workflows/definition`](#workflows) follows.

### Replaying a create

`POST /v1/tasks` is the one route where re-sending a request does something
twice: it inserts a task, claims a branch and wakes the scheduler. Send an
`Idempotency-Key` header to make the retry safe.

```sh
KEY=$(uuidgen)
curl -sS -X POST "http://127.0.0.1:$PORT/v1/tasks" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: $KEY" \
  -d '{"project_id":1,"title":"Add login page"}'
# lost the response? send exactly that again — same key, same body.
```

- **Same key, same body** → `201` with the task the first request created. The
  body is the task **as it is now**, not a recording of the original response,
  so a task the scheduler has already admitted replays as `"state": "running"`
  under a `201`. The task exists, and this is it.
- **Same key, a different body** → `409 invalid_state` with
  `details.reason: "idempotency_key_reused"`, and no task is created. Use a new
  key for a new operation.
- **No key** → exactly the behaviour vincent has always had. Two identical
  sends make two tasks, which is what a person pressing enter twice means.

The body is compared by a digest of the decoded request, so reformatting your
JSON or reordering its keys between the two sends is not a difference. It is
taken before the `github_issue` prefill runs, so an issue edited in between does
not turn a genuine retry into a conflict.

Keys live for 24 hours and are then pruned; they are also deleted along with the
task they name, so a key whose task was destroyed by a forced project delete
creates a fresh task on the next send. `vincent doctor` counts them under
`database.table_rows.idempotency_keys`.

The CLI and the TUI do not send the header: neither retries a create, so a
failed create is reported to you and what to do next is your call.

On `POST /v1/tasks`, the daemon validates the selected root workflow's
declared fields before inserting the task. Missing required values, invalid
types or patterns, and values outside an `enum`'s `values[]` return
`400 validation_failed`. The `fields` object remains open: additional names that
the workflow did not declare are accepted, stored, and returned with the task.

Two things happen before that validation. A `multiple: true` enum value is
normalized — split on `,`, trimmed, emptied entries dropped, deduplicated,
reordered to the declared order and rejoined — so `"reviewers": "cy, ana"` is
stored as `"ana,cy"` and every client produces the same task row for the same
selection; a rejection names the element that failed. And a **required** field's
`default:` is substituted for an omitted key, so a caller that omits it gets a
task rather than a 400, with the applied value recorded on the row. An
**optional** field's default is never substituted: it is published in
`GET /v1/workflows` for clients to seed, and an optional key you omit stays
absent from the task's `fields`. A key sent with an empty value is never
defaulted.

`github_issue` is an issue **number**. The daemon fetches that issue, computes
the [prefill](#github-issues), and fills in whatever this request left unset —
**anything you send explicitly wins.** For `fields` and `description` that is
decided by *presence*: a key sent with an empty value is a row somebody cleared
on purpose and stays cleared, so `"description": ""` creates a task with no
description rather than the issue body. Only `title` keys on emptiness as well
as absence — there is no such thing as deliberately creating an untitled task —
so `title` becomes optional when `github_issue` is given.

The issue is stored on the task and served back on every task representation as
`github_issue`, in the same shape the listing returns. It is a **snapshot**: it
is never re-read, so editing the issue on GitHub afterwards does not change what
a later step renders through
[`.Issue`](workflow-schema.md#template-context). A request that names no
`github_issue` makes no GitHub call at all. An unusable integration is the same
409 with `details.reason` the GitHub endpoints return.

`github_pull` is a pull-request **number**, and it resolves through the same one
implementation on the same presence/blank rules — the daemon fetches the pull
request, prefills `title` (`#N ` and the pull request title), `description` (its
body plus a trailing `GitHub pull request #N: <url>` line) and a declared field
named exactly `pull` carrying the bare number, and anything sent explicitly wins.
Naming both `github_issue` and `github_pull` is `400 validation_failed`: they
would prefill the same title and description from two sources.

What is different is the branch. **The task's `branch_name` is the pull request's
head branch** — the top of the [branch chain](configuration.md#branch_template),
above even a literal `branch_name`, which is ignored — and its worktree is that
branch checked out with an upstream, so a workflow that pushes lands its commits
on the pull request. Four consequences follow:

- The creation-time branch-collision check is **skipped**: the branch is expected
  to exist. The in-transaction claim check still runs, so two live tasks on one
  head branch remain a `400`.
- The `github_pull` link is written **at creation** with `source: "human"`, so the
  pull request reads as claimed immediately rather than on the next reconciler
  tick, and the reconciler will not overwrite it. The link carries `branch: true`
  — this task's branch came from this pull request — and `fork: true` when the head
  lives in another repository, in which case the branch has no upstream and nothing
  can be pushed back.
- Archiving deletes **neither** branch leg; the outcome is `not_ours` (below).
- `POST /v1/tasks/{id}/retry` refuses `branch_override` with a `409`.

Fetching the head is admission's job, not creation's: `POST /v1/tasks` runs no
git at all. A fetch that fails, a diverged local branch of that name, or one
already checked out elsewhere block the task with
[`pull_fetch_failed`, `pull_branch_diverged` or `pull_branch_checked_out`](task-lifecycle.md#failure-reasons).

Every task representation also carries `workflow_origin`: which definition the
`workflow` name resolved to at creation.

```json
"workflow": "adhoc",
"workflow_origin": {
  "scope": "project",
  "file": ".vincent/workflows/adhoc.yaml",
  "digest": "sha256:0f4a1c1e…"
}
```

A project or global workflow **shadows** a built-in of the same name, including
the `adhoc` a task created without a `workflow` falls back to. That is by
design, and this field is how you see it happened: `scope` is `builtin`,
`global`, `project` or `derived`, `file` is the source path relative to that
scope's root (absent for a built-in), and `digest` is a SHA-256 of the source
bytes as the registry loaded them — for a built-in, the copy compiled into the
binary.

It is frozen at creation and never recomputed, so editing the workflow file
afterwards does not rewrite an existing task's origin — and the digest names the
file the task came from, not the (expanded, possibly `edit + retry`-rewritten)
`workflow_snapshot` the engine executes. A `fan_out` lane reports
`{"scope": "derived", "parent_task_id": 41}`: its steps came from its parent's
snapshot, not from a registry file. A task created before vincent recorded this
has `"workflow_origin": null`, which means *not recorded* — vincent will not
look the name up again to invent one.

The `task.created` event carries the same object under `workflow_origin`.

Human actions, all `POST /v1/tasks/{id}/…`:

| Path | Valid from | Body |
|---|---|---|
| `/cancel` | most states | |
| `/pause` | queued, running | |
| `/resume` | paused | |
| `/retry` | blocked | `{ prompt_override?, run_override?, branch_override? }` — `branch_override` renames the branch before re-admission, which is how a `branch_exists` block is recovered. **`409`** on a task created from a pull request: renaming its branch would detach it from that pull request |
| `/repair` | blocked | `{ prompt, agent?, model?, effort? }` — runs one ad-hoc agent in the task's existing worktree, then returns the task to `blocked` at the same step with the same reason |
| `/skip` | blocked, awaiting_gate | |
| `/approve` | awaiting_gate | |
| `/reject` | awaiting_gate | |
| `/answer` | awaiting_input | `{ answers?, allow? }` |
| `/archive` | done, aborted | `{ force? }` or `?force` |
| `/follow_up` | done, aborted | `{ prompt? \| run? \| workflow?, agent?, model?, effort? }` — exactly one of the three; runs it in the task's existing worktree, then returns the task to the state it came from |

Anything else returns `409` with `details.state`. See
[Task lifecycle](task-lifecycle.md).

`/repair` runs one throwaway agent against a blocked task's worktree — the
escape hatch for a block that `retry` cannot clear because the worktree itself
is wrong. `prompt` is required and is **literal text**, not a template: it is
prose, and the daemon assembles the failure context around it (the task, the
blocked step's rendered prompt or command, the reason and exit codes, the last
200 lines of the failed attempt's transcript and the path to the rest). An empty
or whitespace-only prompt is `400 validation_failed`.

The optional `agent` / `model` / `effort` apply to that one run and take
precedence over the task's overrides and the workflow's `defaults:`; they are
validated exactly as `POST /v1/tasks` validates a task's, so an unregistered
agent or a known-invalid model is a `400` and a value no catalog recognizes
comes back in `warnings`:

```json
{ "id": 7, "state": "queued", "…": "…", "warnings": [] }
```

The repair decides nothing about the blocked step. Whatever the agent exits
with, the task goes back to `blocked` at the same step with the same
`block_reason`, and you retry, repair again, skip or cancel. Its attempt is
recorded as an ordinary step run under the reserved step id `__repair` at the
blocked step's index, so `GET /v1/tasks/{id}/steps` returns it with its own
transcript, tokens and cost — and the blocked step's retry budget is untouched
by it.

`/follow_up` runs one more piece of work in a **finished** task's worktree and
branch, before you archive it. Exactly one of three fields says what to run:

| Field | Runs |
|---|---|
| `prompt` | an agent, with this as its instructions |
| `run` | a shell command, under the daemon's shell (`/bin/sh`, or `pwsh` on Windows) |
| `workflow` | a workflow from the registry, against this task's worktree instead of a new one |

Naming none of them, or more than one, is `400 validation_failed`. `prompt` and
`run` are **literal text**, not templates — the daemon escapes them when it
compiles the one-step workflow it runs, so a `{{` you type is two characters. If
you want templating, put it in a workflow and name that.

A `workflow` name is resolved through the registry now, not at admission: an
unknown name, a workflow that cannot run on this host, one that fails validation
once its `include`s are expanded, or a fan-out tree past `fan_out.max_depth`
from this task's own depth are all `400`s. What validates is stored on the task
and is what runs, so editing the file afterwards does not change the run in
flight.

The optional `agent` / `model` / `effort` behave exactly as `/repair`'s do,
except that an explicit agent field on a step of a named workflow still wins —
that is what a step field means. The response is the task, now `queued`, plus
`warnings`.

The run returns the task to the state it came from: `done` to `done`, `aborted`
to `aborted`, whatever it exits with. A follow-up never changes a task's
verdict. It is repeatable, and each run is a **round**: round *n* of a task
whose workflow has *k* steps records its rows at `step_index = k + n - 1`, so
`GET /v1/tasks/{id}/steps` returns them past the workflow's last index and
`step_total` does not change. A row with `step_index >= step_total` is a
follow-up row; render it as its own round rather than as a step of the workflow.

A follow-up step that fails blocks the task at that index. `/retry` there
re-runs the follow-up where it stopped, `/repair` runs an ad-hoc agent against
that failure, `/skip` abandons the follow-up and restores the task's original
state, and `/cancel` aborts — which means `done → aborted` is reachable while a
follow-up is running. `/retry` with `prompt_override` or `run_override` is
`400`: an override rewrites a step in the task's snapshot, and a follow-up is
not in it.

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

`result` is `deleted` (no commits past its base), `has_commits` (kept),
`not_ours` (kept — the branch came from a pull request, so vincent did not cut it
and never deletes it, and the remote leg does not run either), `unknown` (git
could not judge it — base branch renamed away, repository gone) or `error` (the
delete itself failed), with git's message in `error` for the last two. The
`remote` object appears only when
[`delete_remote_branch_on_archive`](configuration.md#delete_remote_branch_on_archive)
is on and the local delete succeeded; its `result` is `deleted`, `no_upstream` or
`error`. The whole `branch` object is **absent** when nothing was checked — the
cleanup is off, or the task never had a branch of its own.

None of it affects the status code: a branch problem never fails an archive.

Four details worth knowing:

- **A `queued` task may be waiting on a clock, not a slot.** Every task
  representation carries `queued_reason` and `admit_not_before` (RFC3339, or
  `null`). Both are `null` for an ordinarily queued task. Two reasons set them,
  and vincent tries again at that timestamp unattended in both cases:
  `usage_limit`, the agent's usage window being spent, and `retry_backoff`, a
  step's failed attempt being paced by its
  [`retry_backoff`](workflow-schema.md#step-fields). They are separate from
  `block_reason`, which still means only "stopped, needs a human" — the task is
  not blocked. Treat the set as open: a client should render whatever string it
  is given rather than switching on the two it knows.

- **List rows carry the board fields** — `project_name`, `step_total`,
  `step_name`, `status_message`, and `cost_usd` / `input_tokens` /
  `output_tokens` rolled up across every attempt — so a board renders without an
  N+1. Those are list-only; `GET /v1/tasks/{id}` serves the same numbers per
  attempt in `steps[]`.

Every task shape carries `parent_task_id`, `lane_id` and `lane_order`, all null
for a root task. `GET /v1/tasks/{id}` additionally carries `children` whenever
the task has lanes:

```json
"children": {
  "total": 4, "settled": 2,
  "by_state": {"done": 2, "blocked": 1, "running": 1},
  "blocked": [17], "awaiting_gate": []
}
```

It covers the **whole subtree**, not just direct lanes, and is computed per
request from one recursive CTE rather than stored — a counter would be a second
truth that drifts from the rows it counts. `blocked` and `awaiting_gate` are
ids: fetch the ones you decide to show. This is what pays for hiding lanes from
the list, since a blocked lane would otherwise be invisible.

Both the list and the detail endpoint carry `loop` while a task's **current**
step is a `loop` (§7.8), and omit it otherwise:

```json
"loop": { "driver": "for_each", "iteration": 4, "max_iterations": 10, "item": "internal/store" }
```

`iteration` is the pass in progress (0 before the first one starts) and
`max_iterations` is the largest it could reach — the `count:` itself, or the
ceiling a `for_each` is bounded by, whose real length is only known at run
time. It is on the list endpoint too, so a board can render `loop 4/10` without
a request per row. Like `children`, it is derived per request from the step
rows rather than stored: a persisted loop cursor would be a second truth that
recovery would have to reconcile. There is deliberately **no** step-lifecycle
event for iterations — ten passes of a four-step body would put forty durable
events on the stream to say what forty rows already say.
- **`?archived=` defaults to false.** `true` selects only archived tasks, `all`
  returns both.
- **Every task representation carries `available_actions`** (the actions valid
  right now) and `pause_requested`, so clients never restate the state machine.
  The detail response adds `workflow_steps[]` — this task's snapshot, which is
  what edit-and-retry prefills an editor with, reflecting any earlier edit. A
  step spliced in by [`type: include`](workflow-schema.md#type-include) carries
  `resolved_from[]`, the chain of workflows it came through, outermost first.

Task creation validates the agent/model/effort override: a known-invalid value is
`400`, a catalog-unknown one is reported in `warnings[]` on the `201` body. It is
also where a workflow's [includes](workflow-schema.md#type-include) are
resolved into the snapshot, so an include that cycles, names a workflow this
project cannot see, nests past `include.max_depth`, brings a step id already in
use, or is restricted to another platform is a `400` here.

## Chats

A **chat** is a titled conversation with an agent, scoped to a project, running
in its own git worktree and `vincent/{id}-{slug}` branch. Each turn resumes the
agent CLI's own session, so turn N has turns 1..N-1 in context. Chats are a
separate family from tasks: they never appear in `GET /v1/tasks` or on the
board, and tasks never appear here.

```
GET    /v1/chats?project_id=&state=&archived=
                                      newest first; state may repeat
POST   /v1/chats                      create, with a worktree and a branch
GET    /v1/chats/{id}                 { chat, turns[] } — the whole conversation
POST   /v1/chats/{id}/send            start a turn
POST   /v1/chats/{id}/answer          answer a mid-run request
POST   /v1/chats/{id}/cancel          stop the live turn
POST   /v1/chats/{id}/archive         remove the worktree; terminal
POST   /v1/chats/{id}/handoff         give the worktree and branch to a new task; terminal
GET    /v1/chats/{id}/events          SSE: this chat's events plus its live output
GET    /v1/chats/{id}/turns/{seq}/transcript
                                      one turn's transcript, with ?offset= / ?tail=
```

None of these is an [MCP tool](#the-mcp-endpoint) — the whole family is
excluded, the stream and the transcript included. `handoff` is on that list for
a reason worth stating: it creates a task, and `task_create`'s bounds
(`mcp.max_depth`, `mcp.max_tasks`) are walked over `created_by_task_id`, which
a chat is not in.

`archived=false|true|all` is `GET /v1/tasks`' parameter, spelled and defaulted
the same way: terminal chats are hidden unless you ask for them. It covers
**both** terminal states — `archived` and `handed_off` alike — which its name
does not say; an explicit `state=` wins over it, and anything but
`false|true|all` is a `400 validation_failed`.

`POST /v1/chats/{id}/archive` is legal from `idle` alone, so its `409` names
the state that blocked it: an already-archived chat is told so, and a
handed-off one that the task owns its worktree now.

`POST /v1/chats/{id}/handoff` takes `POST /v1/tasks`' body and is validated by
the same code, so it accepts exactly the task the create route accepts.
`project_id`, `base_branch` and `branch_name` are the chat's and are ignored.
It answers `201 { "task": {...}, "chat": {...} }`: the task carries
`source_chat_id`, and the chat comes back `handed_off` with `handoff_task_id`
set and its `worktree_path` cleared — the claim moved, in one transaction with
the task row, the link, both durable events (`task.created` and
`chat.handed_off`) and the transition.

Everything is validated before anything is written, so a refusal leaves the
chat exactly as it was: `400` when the task does not validate, `409` when the
chat is not idle, has no worktree to give, or its worktree is partway through a
git operation (code `repo_operation_in_progress`, with `details.operation`
naming it). Ordinary uncommitted work is preserved, never refused and never
committed.

### Creating one

```bash
curl -sS -X POST http://127.0.0.1:PORT/v1/chats   -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json'   -d '{"project_id": 1, "title": "poke at the parser"}'
```

`agent` is optional and defaults to the first registered adapter that **can
resume** — there is no `defaults.agent` key, and a chat's whole premise is
continuity. An adapter that cannot resume is refused:

```json
{"error": {"code": "agent_cannot_resume",
           "message": "agent \"someagent\" cannot resume its own session, so it cannot hold a conversation; vincent refuses this rather than replaying the log as prompt context"}}
```

All three shipped adapters can resume, so today nothing hits this. It is kept
because it is the contract for the next adapter: faking continuity by replaying
the log into the prompt is exactly what the refusal exists to prevent. Ask
[`GET /v1/agents`](#get-v1agents) for `supports_resume` rather than assuming a
list.

### States

`idle` → `running` → `idle`, with `awaiting_input` in the middle when the agent
asks something, and two terminal states: `archived`, and `handed_off` for a chat
whose worktree and branch now belong to a task. Anything outside that table is a
`409` — sending to an archived chat, answering one that asked nothing, handing
off one that has already been handed off. There is no pause: a paused chat is an
idle one nobody has sent to.

### Sending a turn

```bash
curl -sS -X POST http://127.0.0.1:PORT/v1/chats/3/send   -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json'   -d '{"message": "what does buildArgs do with an empty model?"}'
```

`202` with the new turn. Poll `GET /v1/chats/3` or follow `GET /v1/events` for
`chat.turn_changed`. A chat has no live-output stream over HTTP: the turn's
normalized output goes to the turn's transcript file, and `result_text` on the
finished turn is the answer.

Over `max_parallel_chats` it is **`409 chat_cap_reached`, immediately** — never
queued. A chat is a foreground reply, and waiting behind batch work is the thing
chats exist to avoid, so you get an error you can act on instead of a spinner.
The chat is left exactly as it was: still `idle`, with no turn row behind it.

### Turns

Each turn carries the accounting a step run does — `input_tokens`,
`output_tokens`, `cost_usd`, `exit_code`, `duration_ms` — plus `session_id`, the
session it actually ran in, and its own transcript at
`{data_dir}/transcripts/chat-{chat_id}/{seq}.jsonl`. A turn is `running`, then
`done`, `failed` or `interrupted`, forever.

```bash
curl -sS "http://127.0.0.1:PORT/v1/chats/3/turns/2/transcript?offset=0" \
  -H "Authorization: Bearer $TOKEN" -D -
```

The turn is named by its **`seq`**, not by a run id: a chat turn is its own run.
`?offset=` and `?tail=` are mutually exclusive, the body always covers whole
records, and `X-Next-Offset` names where the next fetch resumes — the same
contract [a step's transcript](#transcripts-and-diffs) has. Add
`?format=normalized` for vincent's own vocabulary instead of the agent's
dialect.

### Following one

```bash
curl -N http://127.0.0.1:PORT/v1/chats/3/events -H "Authorization: Bearer $TOKEN"
```

This chat's durable `chat.*` events interleaved with its live output — the
[per-task stream](#events-sse)'s shape for a chat. Another chat's events do not
arrive on it and neither do a task's. `Last-Event-ID` resumes the durable
events; live output is ephemeral and is never replayed, so a reconnect catches
up by re-fetching the running turn's transcript and discarding every chunk whose
`offset` is at or before the `X-Next-Offset` it reported. Chunks carry
`chat_id`, `turn_id`, `offset`, the same normalized fields a task's chunks carry
under the same type names (`agent.output`, `agent.tool_use`, `agent.tool_result`,
`agent.run_header`, `agent.thinking`, `agent.usage`), **and** the agent's own
`raw` line beside them.

A chat's stream carries one type a task's does not: **`agent.raw`**, for a line
vincent's parsers do not model. A task leaves those to its transcript, but a
chat has no timeline of steps beside its output, so a turn whose stream is all
unmodeled lines would show nothing at all while it runs. `agent.result` and
`agent.error` are not published live on either: they reach you as the turn's
own state and in its transcript.

Two failure reasons are worth knowing:

- **`session_lost`** — the CLI no longer knows the stored session. The turn
  fails, the chat stays usable and keeps its id, and nothing starts a fresh
  session behind your back: an agent answering with none of the conversation in
  context reads exactly like one that has it, so starting over is a decision you
  make explicitly. claude and codex both report this; **cursor cannot** — it
  adopts an unknown session id and answers — so a cursor chat is the one place
  that reading is unavoidable, and it is a property of the CLI rather than a
  choice vincent makes.
- **`interrupted`** — the daemon restarted under a live turn. It is **not**
  re-run, unlike a task's step: re-running would re-send your message into a
  session that died with the process. The chat returns to `idle`.
- **`timeout` / `input_timeout`** — the turn ran past
  [`defaults.agent_timeout`](configuration.md#defaults), or the chat sat in
  `awaiting_input` past `defaults.input_timeout`. The process tree is killed,
  the chat returns to `idle` and its `max_parallel_chats` slot is released. The
  two clocks are the ones a workflow step gets, with no chat-specific key and
  no per-turn override.

## Step status

A running step can say what it is doing, in its own words:

```
POST /v1/tasks/{id}/steps/{step_id}/status
{ "message": "3 tests red in internal/store" }
```

The answer is `{ "message": … }` — the value **as stored**, so you can see what
a reader will see.

The caller is the step's own process. It addresses itself with two of the
[`VINCENT_*` variables](../guides/workflows.md#the-vincent-environment) the
daemon sets on every agent and command step, `VINCENT_TASK_ID` and
`VINCENT_STEP_ID`, and the usual way to call it is not curl but
[`vincent status`](cli.md#vincent-status), which reads both and needs no
arguments beyond the message.

The path names a **step id**, not a `step_runs` row id, because a step knows
which step it is and cannot know its row. It names a step rather than only the
task because a `parallel` group's sub-steps share one task and run at the same
time; within one task a step id has at most one running row.

What the daemon does with the message:

- **Bounds it rather than validating it.** It is flattened to a single line,
  stripped of control characters and truncated to 256 bytes. Over-long text is
  never a `400` — a step reporting progress should not fail because it was
  wordy. An empty message clears the status.
- **Refuses a step that is not running**, with `409 invalid_state`. An unknown
  task is `404`. A write is never silently dropped, so a script still narrating
  after its step was killed finds out.
- **Paces writes without rejecting them.** Two writes for one step run inside
  one second coalesce to the later value, which lands when the second is up.
  The first write after a quiet period is always immediate.
- **Announces the change** as the durable
  [`task.status_changed`](#state-events--durable) event — but only when the
  stored value actually changed.

Where it shows up: `status_message` on every step-run object from
`GET /v1/tasks/{id}` and `GET /v1/tasks/{id}/steps`, and on each row of
`GET /v1/tasks`, denormalized from the task's **newest** step run so a board
never fetches step rows for it. It is `null` when the step said nothing, which
is the ordinary case — only `agent` and `command` steps run a process, and one
only speaks if its prompt or script was written to.

**It is not a failure reason.** `failure_reason` is a closed set of
daemon-authored constants and is vincent's own verdict; `status_message` is free
text the step chose, possibly long before it died. Render it as the step's last
status, not as the cause of anything.

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
- `agent.run_header` carries `work_dir` and `available_tools` — what the CLI
  announced about the run before starting it. `agent.result` additionally
  carries `duration_ms`, `api_duration_ms`, `num_turns`, `stop_reason`,
  `terminal_reason`, `cache_read_tokens`, `cache_write_tokens`, `model_usage`
  and `permission_denials` where the adapter reports them; `agent.tool_result`
  entries carry `verb` and `blocked`; and any record may carry `parent_call_id`,
  the tool call a subagent's line belongs to. **Every one of those keys is
  omitted when unreported**, so absent and zero are distinguishable — only
  [Claude Code](../guides/agents.md#claude-code) fills them today.
- `agent.plan` carries `items` (`[{text, completed}]`) and `plan_call_id` — the
  agent's running to-do list, **whole on every record** rather than as a delta,
  so a client that joins mid-run learns where the agent is. `agent.command_output`
  carries `output`, `truncated`, `call_id` and `name` — what a command printed,
  which `agent.tool_result` deliberately never carries, capped with the cut
  reported rather than silent. `agent.result` additionally carries
  `reasoning_tokens`. Only [Codex](../guides/agents.md#codex) fills these today,
  and the same omitted-when-unreported rule applies.
- One stream line can produce **two** records: codex reports a command's outcome
  and the body it printed on a single event, and they are separate records
  because clients show them at different verbosity levels. Read the NDJSON as a
  record stream, not one record per source line.

Because normalization runs **on read**, enriching a parser improves transcripts
already on disk.

`GET …/diff` is a unified diff of the worktree against the merge-base with the
commit the task was cut from — its recorded `base_sha`, and the base branch by
name for a task that has none — including uncommitted changes. Untracked files
are excluded — a documented limitation.

## Events (SSE)

```
GET /v1/events?types=&project_id=
GET /v1/tasks/{id}/events
GET /v1/chats/{id}/events
```

Two kinds of stream, with deliberately different guarantees. The per-chat one is
the per-task one's twin, over a [chat](#chats) instead of a task, and is
documented there.

### State events — durable

Persisted to the events table with a monotonic id and emitted with `id:` set, so
a client reconnecting with `Last-Event-ID` misses nothing.

```
task.created            task.state_changed      task.priority_changed
task.step_advanced      task.status_changed     task.children_changed
task.github_pull_changed
chat.created            chat.state_changed      chat.turn_changed
chat.archived           chat.handed_off
project.*               workflow.registry_changed
agent.quota_changed     daemon.shutting_down
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
- `task.children_changed` carries `{ task_id, child_id, to_state }` and is
  emitted on **every** fan-out ancestor when a descendant is created or
  transitions — re-fetch the `children` rollup when you see one. It exists
  because the per-task stream filters on `task_id` alone, so a root's stream
  would otherwise never see a depth-2 transition.
- `task.status_changed` carries `{ task_id, step_id, message }` when a running
  step changes what it says about itself — see [Step status](#step-status). It
  is on the durable side deliberately, so a client that blinks recovers the
  message through `Last-Event-ID`. It is emitted only when the stored value
  actually changed, so a step re-asserting the same line does not wake you.
- `agent.quota_changed` carries `{ agent, spent, resets_at, source }` and no
  `task_id`: the fact is about an adapter, not about any one task. It is
  emitted when a `usage_limit` stop is observed and when a successful run
  retires an observation — never on a re-observation identical to what is
  already stored, and never merely because a window lapsed. Re-fetch
  [`quota`](#usage-quota) from `/v1/agents` or `/v1/info` when you see one.
- The `chat.*` events belong to the [chat](#chats) family and carry the chat's
  `project_id`, so `?project_id=` filters them the way it filters a task's.
  Each payload is `{ id, title, state }`; `chat.turn_changed` adds `turn_id`,
  `turn_seq`, `turn_state` and — when the turn failed — `fail_reason`, and
  `chat.handed_off` adds `handoff_task_id`, so a follower can link the chat to
  its task without a fetch.
  Re-fetch `GET /v1/chats/{id}` when you see one. There is **no per-chat event
  stream and no live-output route**: a chat's normalized output is written to
  the turn's transcript file, and over HTTP a finished turn's `result_text` is
  what you read.
- `task.github_pull_changed` carries `{ repo, number, source, suppressed }` —
  empty when the link was cleared — and says a task's pull-request link changed:
  the daemon's reconciler matched one, or a human linked or unlinked one. It is
  **not** a transition: the task's state is unchanged and its `updated_at` is
  untouched, because the link is a fact about GitHub rather than about the
  task's own progress. Re-fetch
  [`/v1/tasks/{id}/github/pull`](#github-pull-requests) when you see one.

### Live output — ephemeral

`agent.output`, `agent.tool_use`, `agent.tool_result`, `agent.thinking`,
`agent.run_header`, `agent.plan`, `agent.command_output`, `agent.usage` and
`command.output` chunks stream on the
**per-task** stream only and are **not** written to the events table. Their
durable copy is the transcript file.

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

## MCP

The daemon also speaks the **Model Context Protocol** at `POST /mcp`, on this
same listener behind this same bearer token. It exists so an AI coding agent can
drive vincent with tool discovery, argument schemas and typed errors instead of
hand-rolled curl. Full guide: **[Driving vincent from an agent](../guides/mcp.md)**.

Every route on this page is a tool, with these exceptions:

| Not a tool | Why |
|---|---|
| `POST /v1/daemon/stop` | An agent must not stop the daemon supervising it |
| `POST /v1/daemon/backup` | Destructive admin |
| `DELETE /v1/projects/{id}` | Destructive admin |
| `POST /v1/maintenance/gc` | Destructive admin |
| `POST /v1/doctor/fix` | Destructive admin |
| `PATCH /v1/config` | An agent must not reconfigure the daemon supervising it — a patch changes the argv it spawns, what its children inherit, and whether steps get MCP at all |
| `POST /v1/workflows` | Same line: a workflow file is what that daemon runs. `GET /v1/workflows/schema` is an ordinary tool |
| `PATCH /v1/workflows` | Same |
| `POST /v1/tasks/{id}/github/pull/create` | The one route that writes to a forge. Nothing gates it behind the keypress it exists for — no config key, no confirmation the daemon can check — so an agent-callable version would be consent nobody gave. An agent that wants a pull request runs `git push` and `gh pr create` in its own worktree |
| `GET /v1/events` | A tool call is request/response; use `task_wait` |
| `GET /v1/tasks/{id}/events` | Same |
| every `/v1/chats` route | Two reasons, either sufficient: a chat turn starts an agent CLI *without* going through admission, so a tool that could send one would let an agent start unqueued agent processes — the exact thing `mcp.max_tasks` bounds; and the recursion bounds walk `created_by_task_id`, a chain a chat is not in, so exposing chats would mean inventing depth semantics for a non-task. An agent that needs a conversation already has its own session |

`task_create` additionally takes an optional `idempotency_key` string, which
becomes the `Idempotency-Key` header — a tool call has no header surface, and
replay protection exists for exactly the client an agent is.

A tool call is dispatched by replaying its arguments against the very handler
this page documents, so the request bounds, the validation and the error
envelopes above are the same ones a tool sees — a `409` reaches an MCP client
still carrying `details.state`. One tool result is capped at 256 KiB; use a
route's own `offset`/`limit` query parameters to page.

`config_get` is the one tool whose **body** differs from its route's: it masks
`environment.set`'s values and `notify.command`'s argv, keeping the names,
because a tool result lands in the model's context and in the step's transcript.
`GET /v1/config` itself serves them.

`task_wait` is the one tool with no route behind it: it blocks until a task
reaches `done`, `aborted`, `archived`, `awaiting_input`, `blocked` or
`awaiting_gate`, or until its timeout (default 5 minutes, hard ceiling 30).

```
POST /mcp                       shared endpoint; Authorization: Bearer {token}
POST /mcp/step/{run_id}         per-step endpoint; the daemon wires this to its own agent steps
```

The per-step endpoint carries a secret minted for one step run. It is **not** a
security boundary — see the [security model](../security-model.md).

---

## See also

- [Driving vincent from an agent](../guides/mcp.md) — the MCP surface.
- [Scripting vincent](../guides/scripting.md) — worked examples.
- [Task lifecycle](task-lifecycle.md) — what the action endpoints do.
- Spec [§13](https://github.com/lezli01/vincent/blob/master/docs/spec.md) — the normative definition.

{% endraw %}
