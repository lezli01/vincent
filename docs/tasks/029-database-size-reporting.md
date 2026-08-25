# 029 — Reporting the database's footprint, row counts and retention span

**Status:** ✅ done (6/6)
**Opened:** 2026-08-25

Spec §17 decides retention deliberately: transcripts of archived tasks are
pruned after `transcript_retention_days`, and **DB rows are kept indefinitely**
because "rows are small, history is valuable". That decision is sound and this
work does not argue with it.

What was missing is the measurement. `events` gets one row per state change and
is never pruned (`0001_init.sql`); `tasks.workflow_snapshot` stores the whole
workflow YAML per task; the only pruner in the tree deletes transcript *files*
(`internal/taskrun/prune.go`) and never touches a table. So "rows are small" was
an assumption nobody could check on their own machine after six months of use.

`vincent doctor` (task 006) already reported the database's path, size, applied
schema version and `integrity_check` — `os.Stat` on the main file alone. This
task adds the WAL/SHM half of the byte total, the per-table row counts, the
workflow-snapshot total and the retention span, and nothing else:
[#98](https://github.com/lezli01/vincent/issues/98) asks for a measurement, not
a policy.

## Tasks

- [x] **029.1** `internal/store`: `FileSizes`, `TableRows`, `OldestEventAt`,
      `WorkflowSnapshotBytes`. No migration — nothing about the schema changes.
      ✓ 2026-08-25
- [x] **029.2** `internal/doctor`: the `Database` group grows `wal_bytes`,
      `shm_bytes`, `total_bytes`, `table_rows`, `oldest_event_at` and
      `workflow_snapshot_bytes`, all unset when `known` is false. ✓ 2026-08-25
- [x] **029.3** `internal/api`: `fillDatabase` fills them; `/v1/info` gains a
      nested `database` object carrying **only** the byte figures; `/v1/doctor`
      gains `?probe=false`. ✓ 2026-08-25
- [x] **029.4** `internal/apiclient` and `internal/cli`: `Info.Database`,
      `Doctor(ctx, probe)`, and the new rows under `vincent doctor`'s DATABASE
      group. ✓ 2026-08-25
- [x] **029.5** `internal/tui`: the daemon view's database block, fetched with
      `probe=false`, with its three named empty states. ✓ 2026-08-25
- [x] **029.6** Gate, spec amendments (§9.6, §13.2, §15 view 6, §17) and the
      derived pages. ✓ 2026-08-25

## Decisions

### 1. The split is by cost: bytes on `/v1/info`, scans on `/v1/doctor` (2026-08-25)

The issue proposed putting every figure on `GET /v1/info`. That is the one
structural point it got wrong for the tree as it stands, and the split is
deliberate.

§13.2 admits `orphans` onto `/v1/info` with an explicit rationale — "computed
per request from a readdir plus the id queries — no size walk, no git — so it is
cheap". Three `os.Stat` calls are cheap in that sense. A `COUNT(*)` over a
multi-million-row `events` table, on the daemon's single SQLite connection, on
every debounced board and projects refresh, is not. So the byte figures ride
`/v1/info` and the scans ride `/v1/doctor`, which is already the cold path — it
forces three adapter probes, runs `integrity_check` and walks the worktree roots,
so the counts cost it nothing new and need no cache anywhere in this change.

**Beat:** the issue's own suggestion of everything on `/v1/info` behind a TTL
cache. It works, but it makes the endpoint the TUI polls most the owner of an
unbounded scan and buys a cache-invalidation question the split does not have.
**Also beat:** doctor-only for everything, which is tidier but leaves the byte
total unavailable to any surface not already paying doctor's price.

### 2. Row counts are enumerated, not listed (2026-08-25)

`sqlite_master` decides which tables are counted, so `agent_quota` and
`schema_migrations` appear today and a future migration's table appears for free.

The cost is a key set that shifts between versions. That is correct for a
diagnostic: it describes *this binary's* database rather than promising a fixed
contract. It also avoids a live failure mode — the issue's own hardcoded list
named an `actions` table, and **there is none**: actions are columns on `tasks`
(`0003_actions.sql`). A curated list is a list that goes wrong quietly.

### 3. `workflow_snapshot` gets its own figure (2026-08-25)

One aggregate over `tasks`, alongside the row counts. "412 MB and 1.2M events"
and "412 MB and 900 fat snapshots" point at different future decisions, and
telling them apart is the whole reason the issue rejects reporting a single byte
count.

The `CAST(workflow_snapshot AS BLOB)` is not optional: SQLite's `LENGTH()` counts
characters on a TEXT value and bytes only on a BLOB, so without it any non-ASCII
snapshot is under-reported by a figure that claims to be bytes. There is a test
that fails against the uncast query.

### 4. Doctor's forced re-probe narrows from "the endpoint" to "the command" (2026-08-25)

Task 006 decision 2 makes `GET /v1/doctor` re-probe every adapter
unconditionally, because a cached `logged_in: false` breaks the loop doctor
exists for — run doctor, log in, run doctor again, still told you are logged out.

That reasoning is about **a human running a command**, and it is kept exactly for
that path: `vincent doctor` still forces. A TUI panel that opens on a keypress is
not that loop, and making `6` spawn three subprocesses every time would be a real
regression in a view that is currently cheap. So the endpoint gains
`?probe=false`, defaulting to the current behaviour, and the TUI passes it.

The decision's outcome is unchanged for every caller it was written about. This
records the narrowing rather than performing it silently.

### 5. `vincent daemon status` is not touched (2026-08-25)

The issue proposes switching it from `/v1/health` to an authenticated
`/v1/info` so it can print the figures. Declined.

`daemon status` is a one-line liveness answer with 0/1/2 exit codes (T1.3/T1.4
decision, "Status truth"). `vincent doctor` is now the pasteable report and
already owns the Database group. Moving `status` onto an authenticated route
would add a token-read failure mode to the command people run *because*
something is wrong.

The issue's real constraint is honoured in full: nothing is added to the
auth-exempt `/v1/health`, and a test asserts its body stays exactly
`{status, version}`.

### 6. Nothing prunes and nothing warns (2026-08-25)

Rejected explicitly, as the issue asks: no `events` retention window, no size
threshold, no non-zero exit, no deletion, no `VACUUM` (doctor's `--fix` already
owns compaction, task 006 decision 4). §17's retention decision stands untouched.
Measure first; a later decision to prune would be the amendment.

## What the tests prove

- **store** — counts match a seeded database, and the enumeration is checked
  against the tables parsed out of the embedded migrations, so a new migration
  needs no test edit to be covered. `OldestEventAt` is nil on an empty table and
  equal to the first event's `ts` after inserts. The snapshot sum counts bytes,
  which a non-ASCII fixture proves and a `LENGTH`-without-`CAST` implementation
  fails. `FileSizes` reports a total that is the sum of the three files and does
  not error when the sidecars are absent.
- **api** — `/v1/info` carries the byte figures **and no row counts**, asserted
  against the raw body as an absence, which is the regression guard for the hot
  path. `/v1/doctor` carries all of them. `/v1/health` is still exactly
  `{status, version}`. `?probe=false` does not invoke the adapter probe and the
  default still does, counted through a wrapper around the real adapter over the
  real fake binary rather than assumed.
- **doctor / cli** — the degraded report leaves `Database.Known` false with every
  figure unset **while a real populated database sits at the path it names**, and
  the CLI renders unknown rather than zero.
- **tui** — unit tests for the block's three named empty states and for the
  `probe=false` query, plus a live test against the real handlers asserting the
  view renders the row counts the daemon actually returned.
- **apiclient** — the live round-trip proves the widened wire types survive JSON
  in both directions, which is what keeps the type aliases honest.
- **gate** — `scripts/m2-gate.sh` asserts the doctor report's `table_rows`,
  `total_bytes >= size_bytes` and a non-null `oldest_event_at` after a real
  scenario, plus the `/v1/info` and `/v1/health` shapes.
