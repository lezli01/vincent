# 117 — `vincent task import`: restore one archived task from a backup

**Status:** ✅ done (6/6)
**Opened:** 2026-09-17
**Issue:** [#411](https://github.com/lezli01/vincent/issues/411)
**Amends:** §12.1 — a `vincent task import` row, and a note that it is a thin
API client rather than a third exception to §4; §13.2 — `POST /v1/tasks/import`;
§13.3 — `task.restored`; §13.4 — the route joins the destructive-admin
exclusion; §14 — an import inserts with explicit ids and copies no `events`;
§17 — `archived_at` is stamped at import, so retention restarts; §18 — a "user
wants back one deleted task" row. No decision record row: beyond the §12.1
note, nothing here is cross-cutting.
**Keeps, without relitigating:** [030](030-daemon-backup-and-restore.md)
decision 5 — `daemon restore` is client-side because it opens nothing, and
stays the one stated exception of its kind; decision 9 — an archive is
untrusted input, checked entry by entry with the same code; decision 10 — no
gate script. [092](092-archived-boards-and-delete.md) decision 7 — a deleted
task's `events` rows stay, and `events.id` is the `Last-Event-ID` cursor.
[011](011-bulk-task-selection.md) — no bulk endpoint.

## Problem

`vincent daemon restore` (task 030) restores a whole installation, and only
into a stopped daemon with an empty or displaced destination. Task 092 added
`vincent task delete`, which removes an archived task, its step runs and its
transcripts for good. Its only undo was to restore the whole backup over
everything that happened since. 030 recorded single-task restore as "a
different feature with a different shape, and nothing here forecloses it", and
#411 tracked it.

The issue proposed a restore that runs "client-side like the existing restore"
and also inserts into the live database. Those two cannot both be true
(decision 1).

## What shipped

```
vincent task import <archive.tar.gz> <task-id> [--project <id>] [--json]
POST /v1/tasks/import   { path, task_id, project_id? }
```

The daemon reads the archive, copies one task row, its `step_runs` and its
`transcripts/{task_id}/` tree into the live installation, and appends one
durable `task.restored` event. The task comes back `archived` and read-only,
like any row on the archived board. The same call imports a task from another
installation's backup when its id is free here.

- The CLI resolves the archive to an absolute path and POSTs it. With no daemon
  it prints `import needs a running daemon: only the daemon opens the database,
  and only it can write a task back into it.` and exits 2. A refusal is exit 1
  with `Error: <message>` on stderr.
- Success is one line, `imported task 12 "<title>" into project 3: 4 step
  run(s), ids kept, transcripts 1.2KB` (`ids renumbered` when decision 3
  renumbered them). `--json` prints the response body:
  `{task_id, project_id, title, step_runs, step_runs_renumbered,
  transcript_files, transcript_bytes, archived_at, backup_schema_version,
  backup_created_at}`.
- Refusals use the §13.1 envelope. `400 validation_failed` for a path that is
  missing, relative or not an existing regular file, a `task_id` that is not
  positive, an archive that is not a vincent backup (unreadable gzip or tar, or
  no `manifest.json`), one with no `vincent.db`, an unsafe entry, and a schema
  newer than the binary (`details.reason: schema_too_new`). `404 not_found`
  with `details.reason` `task_not_in_backup` or `project_not_found`.
  `409 invalid_state` with `details: {action: "import", reason}`, where the
  reason is `not_archived` (with the backed-up `state`), `task_exists`,
  `project_mismatch`, `parent_missing` or `transcripts_present`.

No migration, no config key, no TUI change: the boards already refresh on any
`task.*` event, so an open archived board shows the imported row.

## Decisions

### 1. Daemon-side, not client-side — a deliberate departure from the issue (2026-09-17)

The issue asked for something "client-side like the existing restore" that
also inserts into the live database. 030 decision 5 and §12.1 make
`daemon restore` client-side **only because it opens nothing**: it probes the
lock, reads `manifest.json`, and moves files while the daemon is stopped.
Inserting rows means opening SQLite, and §4 says only the daemon does that. So
the CLI resolves the archive to an absolute path and POSTs it, the way 030
decision 3 has the daemon build the archive. The import needs a **running**
daemon and refuses without one in `backup`'s wording. §12.1 gains no third
stated exception, and 030 decision 5 stays as written.

**Beat:** a client-side import that opens the live `vincent.db` while the daemon
is stopped. It hands a second process a write handle to the file, which is the
exception §4 exists to prevent, to buy a command that cannot run beside the
work it restores into.

### 2. The task keeps its id and refuses on collision (2026-09-17)

The live database never reissues a deleted task's id (`AUTOINCREMENT`), so
undoing a delete always gets its id back. If a live task holds that id, the
import refuses with `task_exists` and names that task. This is the issue's
"refuses while the task exists". There is no fallback to a new id.

**Beat:** a fresh id on collision. The `events` rows 092 decision 7 kept, the
transcript directory and the default branch name all carry the id. A renumbered
task would be a different task that happens to carry the old one's step runs.

### 3. Step run ids: all kept if all are free, otherwise all renumbered (2026-09-17)

No column references `step_runs.id`, and transcript files are named by
`step_index`/`attempt`, so renumbering rewrites nothing else. After a
same-installation delete every original id is free. If any is taken, which is
normal for another installation's backup, every step run gets a fresh id **in
the original order**, so "newest by run id" (`vincent task transcript`'s
default) still picks the same attempt. The response reports it as
`step_runs_renumbered`.

**Beat:** renumbering only the colliding rows, which can reorder attempts by id.
**Beat:** refusing on any step run collision, which would make almost every
cross-installation import fail for a reason the user cannot fix.

### 4. Only a task that was `archived` in the backup can be imported (2026-09-17)

Any other state is refused with `not_archived`, naming the backed-up state.
This mirrors 092, where only an archived task can be deleted. A task that was
live when the backup was taken has `running` step runs with a pid and a process
identity, and §12.4 recovery would treat them as orphans at the next start and
re-run the step. Refusing keeps that out.

**Beat:** importing any state and forcing it to `archived`. That writes a state
the task never reached and step runs that claim to still be running.

### 5. `archived_at` is set to the import time (2026-09-17)

The §17 pruner removes transcripts once `archived_at` is older than
`transcript_retention_days`. Keeping the backup's value would delete the
restored transcripts of any task archived more than 90 days ago within a day.
`created_at`, `started_at`, `finished_at`, `updated_at` and every step run
timestamp stay as in the backup.

**Beat:** keeping `archived_at` for fidelity. The transcripts are the reason to
import the task at all.

### 6. Events are not copied; one `task.restored` event is appended (2026-09-17)

092 decision 7 keeps a deleted task's `events` rows, so after a
same-installation delete the history is still there and copying would
duplicate it. `events.id` is the SSE `Last-Event-ID` cursor, so old rows could
only be appended under new ids, and a client resuming a stream would replay
years-old state changes. `task.restored` works like `task.deleted`: a durable
§13.3 type with payload `{id, title}`, published post-commit through the
store's event hook. Unlike `task.deleted` it sets the event's `task_id` column,
because the row exists after the commit, so it also reaches the per-task
stream. It is not a §6 action and never appears in `available_actions`. Notify
and triggers do not react: notify fires on state entry, and an import enters no
state.

**Beat:** no event at all. Nothing else would tell an open client the row now
exists.

### 7. Project: same id and same name, otherwise `--project` (2026-09-17)

The backed-up `project_id` is used when a live project has that id **and** the
backed-up project's name. Otherwise the import refuses with `project_mismatch`
and names the backed-up project, unless `--project <id>` (`project_id` on the
wire) re-homes the task explicitly. A `project_id` naming no live project is
`404 project_not_found`. No project is ever created.

**Beat:** matching by id alone, which silently lands another installation's
task in an unrelated project. **Beat:** creating the project, which registers a
repository path nobody checked on this machine.

### 8. One task per call (2026-09-17)

A fan-out lane is refused with `parent_missing` unless its `parent_task_id` is
live. `parent_task_id` has no `ON DELETE` clause, so the guard gives a named
refusal where there would otherwise be a driver error. A parent can be imported
without its lanes, which then come back one call each. `created_by_task_id`
becomes NULL when that task is not live, which is the outcome its
`ON DELETE SET NULL` would have produced.

**Beat:** importing a parent with its lanes in one call. That is a bulk
operation, and task 011's rule stands.

### 9. Naming: `vincent task import`, `POST /v1/tasks/import` (2026-09-17)

`restore` already names a §6 action (`taskstate.Restore`, which ends a
follow-up), and `vincent daemon restore` is the stopped-daemon whole restore. A
`daemon restore --task` flag would give one command two opposite daemon
requirements. The route is a literal segment under `/v1/tasks`, and there is no
`POST /v1/tasks/{id}` for it to shadow.

## Derived rules

- **Branch and worktree are left alone.** `worktree_path` is written as NULL;
  `branch_name`, `base_branch` and `base_sha` are copied as they are. There are
  no git operations: backups exclude `worktrees/`, and an archived task has no
  worktree.
- **Rows that are not copied:** `events`, `idempotency_keys`,
  `trigger_deliveries`, chats and their `handoff_task_id` links, and every other
  table. A chat whose handoff link the delete cleared stays cleared. Task
  columns such as `github_issue_json`, `github_pull_json` and
  `workflow_origin_json` are copied as they are: they are pointers, not
  snapshots.
- **Transcript paths are rewritten.** `step_runs.transcript_path` is an absolute
  path into the *source* data directory. Each is rewritten to
  `{data_dir}/transcripts/{task_id}/<rest>` by locating its
  `transcripts/{task_id}/` segment with either separator, since the backup may
  come from Windows. A path with no such segment is left unchanged. A row whose
  file was pruned before the backup keeps its row and 404s like any pruned
  transcript.
- **A live `transcripts/{task_id}/` is a refusal** (`transcripts_present`),
  naming the path. It is a stray that `vincent gc` reports. Nothing is deleted
  or merged, matching §18.
- **Schema.** A manifest schema newer than the binary is refused
  (`schema_too_new`), as `daemon restore` refuses it. An older one imports: the
  daemon runs `store.Open`, which migrates, on the **staged copy** of the
  archive's `vincent.db`, never on the live file. That handle belongs to the
  daemon and points at another file, so the one-writer invariant on the live
  store is untouched.
- **Staging and atomicity.** One pass over the archive extracts `vincent.db` and
  the `transcripts/{task_id}/` subtree into a `.vincent-import-*` directory
  under `{data_dir}`, on the same volume so the final rename is atomic. Only
  `manifest.json`'s position is guaranteed, so nothing depends on any other
  entry order. The pass reuses 030 decision 9's entry-safety checks. Every
  refusal is checked against the staged copy and the live store before anything
  is placed. The transcript directory is then renamed into place inside the
  import transaction, which inserts the task and its step runs and appends
  `task.restored`. If the insert or the commit fails, the directory just placed
  is removed, which is safe because this import created it. Staging is removed
  on every exit path.
- **MCP.** `POST /v1/tasks/import` joins §13.4's destructive-admin exclusion
  beside `POST /v1/daemon/backup` and the permanent deletes. It reads an
  arbitrary file the caller names and writes rows no agent should be able to
  create.
- **No gate script**, for 030 decision 10's reason: the whole flow is asserted
  end to end in Go.

## Tasks

- [x] **117.1** `internal/backup`: `ExtractTask` stages `vincent.db` and one
  task's `transcripts/{id}/` subtree in a single pass, in any entry order,
  reusing the 030 entry-safety checks. ✓ 2026-09-17
- [x] **117.2** `internal/store`: `ExportTask`, read from the staged copy, and
  `ImportTask`, one transaction inserting the task and its step runs with
  explicit or renumbered ids, `archived_at` stamped, `worktree_path` NULL,
  `created_by_task_id` nulled when absent, and the `task.restored` event;
  `EventTaskRestored` beside `EventTaskDeleted`; the refusal checks.
  ✓ 2026-09-17
- [x] **117.3** `internal/api`: `taskimport.go`, the route in `server.go`'s
  table, path validation modelled on `backup.go`, staging, the transcript-path
  rewrite, and the placed directory's removal on a failed commit. ✓ 2026-09-17
- [x] **117.4** `internal/apiclient`: `ImportTask` and its wire types;
  `internal/cli`: `newTaskImportCmd` in the `task` tree. ✓ 2026-09-17
- [x] **117.5** `internal/mcp`: the route added to the exclusion list.
  ✓ 2026-09-17
- [x] **117.6** Docs: §12.1, §13.2, §13.3, §13.4, §14, §17 and §18 amended;
  `cli.md`, `api.md`, `files.md`, `guides/mcp.md`, `security-model.md`,
  `features.md`, `task-lifecycle.md`; `CHANGELOG.md`; the pointer from 030's
  "Restoring a single task" note. The TUI guide is unchanged: the boards already
  refresh on every `task.*` event. ✓ 2026-09-17

## Tests

- **`internal/store`:** an import round-trips every column of `tasks` and
  `step_runs`, with the expected set built from `PRAGMA table_info` so a future
  column fails the test instead of being dropped; `archived_at` is stamped,
  `worktree_path` is NULL, and `created_by_task_id` is nulled only when that
  task is absent; step run ids are kept when free and all renumbered, in order,
  when any is taken; each refusal leaves the database unchanged; `task.restored`
  is published post-commit and only on success.
- **`internal/backup`:** extraction takes exactly one task's subtree (not
  `transcripts/{id}0/`, which shares a prefix), works for any entry order, and
  rejects the 030 hostile-archive set.
- **`internal/api`**, live through `apiclient` against the real handler: every
  400, 404 and 409 above, including a lane with no live parent, a backed-up
  state other than `archived`, a project with the same id and another name, and
  the `project_id` override; an older-schema archive imports and a newer one is
  refused; a failed commit leaves no transcript directory behind; the route is
  absent from the MCP tool list.
- **`internal/cli` e2e**, real binary and real daemon: run a task, archive it,
  back up, `task delete` it, `task import` it back; `task show` has it archived
  with its original id and step runs, and `task transcript` prints the original
  bytes; a second import refuses with `task_exists`; a cross-installation import
  into a fresh data directory whose step run ids collide renumbers them and
  rewrites the transcript path; with no daemon the command exits 2.

## Out of scope, noted

- **Importing a single chat.** Chats have their own tables and handoff links; a
  separate feature if asked for.
- **Listing the tasks inside an archive.** The user supplies the id; a wrong one
  is `task_not_in_backup`.
- **Bulk import.** Task 011's rule: one call per task.
- **Scheduled backups** ([#410](https://github.com/lezli01/vincent/issues/410)).
