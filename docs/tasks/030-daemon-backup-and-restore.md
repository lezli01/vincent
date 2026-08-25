# 030 — `vincent daemon backup` / `restore`

**Status:** ✅ done (6/6)
**Opened:** 2026-08-25
**Issue:** [#99](https://github.com/lezli01/vincent/issues/99)

`vincent.db` holds every project, task, step run, durable event, cost record and
transcript pointer, and `docs/reference/files.md` said plainly that losing it
means "everything is gone". Nothing documented how to copy it, and no command
did. `rg -i backup` over `internal/` and `docs/` returned only log rotation's
`MaxBackups` and task 027's task-state `Restore` action. Backups appeared in
neither §2's non-goals nor §20's future work, and `docs/history/v0-tasks.md`
carries no decision on them — a gap rather than a deferral.

The workaround a user would reach for is actively unsafe. Under WAL a committed
row lives in `vincent.db-wal` until a checkpoint, so `cp vincent.db` while the
daemon runs copies a file missing recent commits, and copying `.db`/`-wal`/`-shm`
separately gives a non-atomic set that can restore into a torn database. §18
already promised the right posture on corruption — *"startup fails loudly, points
at the file, never auto-deletes"* — and that posture assumes the user has
something to restore from.

Two commands, one endpoint, one new leaf package. No scheduling, no retention,
no new config key, no migration.

## Decisions

### 1. The archive carries everything, transcripts included (2026-08-25)

`vincent.db`, `transcripts/`, `config/config.yaml`, `config/workflows/`,
`manifest.json` — one artifact that restores full fidelity. Transcripts are not
opt-in and there is no `--exclude-transcripts` flag in this task.

The size consequence is met by **reporting** rather than by trimming: the
response carries `bytes`, `database_bytes` and `transcript_bytes` separately,
the command prints all three, and `docs/reference/files.md` says plainly that a
data directory with large transcripts produces a large archive. A vincent
installation is megabytes of database beside gigabytes of transcripts, and a
user surprised by the total is owed the breakdown, not a smaller lie.

Excluded, and documented as excluded: `worktrees/` (disposable, and the branches
survive in the repositories), `token`, `daemon.json`, `daemon.lock`, `logs/`,
`tui.json`. A restored installation mints a fresh token at next start, so every
client re-reads it — said in the docs rather than discovered.

### 2. Backup needs a live daemon and refuses without one (2026-08-25)

No cold-copy fallback and no `--cold` flag. This follows task 006's rule
verbatim — *"every repair is a write, and only the daemon writes"*
(`docs/spec.md` §12.1, `internal/cli/doctor.go`) — and the refusal text echoes
`doctor --fix`'s wording so the two read as one policy.

The `--fix`-style refusal is not a hardship in §18's corrupt-database case: what
rescues a corrupt database is an *earlier* good copy, not a fresh copy of the
damage. The documentation keeps "stop the daemon, then copy `vincent.db`,
`vincent.db-wal` and `vincent.db-shm` together" as the no-binary fallback, which
is also the honest answer for a daemon that will not start.

**Beat:** a cold mode that opens the database read-only from the CLI. It hands a
second process a handle to the file and breaks the one-writer invariant to buy a
worse copy.

### 3. The daemon assembles the whole archive (2026-08-25)

The CLI resolves the destination to an absolute path client-side, POSTs it, and
prints what came back. The daemon runs `VACUUM INTO` into a staging directory
beside the destination, writes the `.tar.gz`, removes the staging directory, and
returns the byte counts. The CLI stays the thin API client §12.1 describes for
every other data subcommand, and exactly one process walks daemon-owned state.

**Beat:** daemon copies the database, CLI tars the rest. It puts two processes
in the data directory and leaves the staging file without a clear owner.

**Beat:** the daemon streams the archive back over HTTP and the CLI writes it.
That would matter if the daemon were ever remote; the API is loopback-only
(§16), so it only adds a copy and a second path-resolution story.

### 4. `POST /v1/daemon/backup`, `vincent daemon backup` / `restore` (2026-08-25)

Chosen over `/v1/maintenance/backup` and a top-level `vincent backup`. This is
the daemon's own state, and it reads correctly beside `daemon stop`;
`/v1/maintenance/*` is the family that reconciles what is on disk against what
the rows say (§10), which a backup is not.

The consequence, accepted knowingly and written into the §13.2 amendment rather
than left for a reader to notice: `/v1/daemon/*` now holds more than process
lifecycle. Its meaning widens from "the daemon process" to "the daemon itself".

### 5. Restore is client-side, and that is a stated exception (2026-08-25)

§4's "clients never touch the DB" holds because the daemon is the process that
*opens SQLite*. Restore opens nothing: it probes the single-instance lock,
refuses unless the daemon is down, reads `manifest.json` for the schema version
— never the database — and then moves files. It cannot be an endpoint for the
same reason it is safe: the daemon whose files it replaces has to be gone first.

The exception is written into §12.1 explicitly. An unremarked exception to an
ownership invariant is how the invariant erodes.

### 6. Restore's conflict rule extends to the config directory — and to the WAL (2026-08-25)

The issue specified `vincent.db` and `transcripts/`. `config.yaml` and
`workflows/` get the same treatment: written when absent, refused when present
without `--force`, moved aside as `config.yaml.bak-<ts>` / `workflows.bak-<ts>`
with it. Nothing is deleted on any path, matching §18's never-auto-delete
posture — and `moveAside` uniquifies a colliding `.bak-<ts>` name, because
`os.Rename` onto an existing path would replace it and replacing a displaced
file *is* a delete.

`vincent.db-wal` and `vincent.db-shm` are in the conflict set with the database
they belong to. That is not tidiness: leaving the old installation's write-ahead
log beside a freshly restored `vincent.db` is how a good backup becomes a corrupt
database at first open.

### 7. Backup does not adopt task 005 decision 4's "skip while work is in flight" (2026-08-25)

That decision governs `VACUUM`, which rewrites the live file under an exclusive
lock and would stall a step mid-write. `VACUUM INTO` writes elsewhere under a
read transaction, and the issue's acceptance criteria require a backup **while a
task runs**.

The real cost is different and is named here rather than discovered: the store
holds one connection (`SetMaxOpenConns(1)`, phase 1 decision), so every other
daemon query queues behind the copy for its duration. Bounded by database size,
and a vincent database is megabytes where its transcripts are gigabytes.

### 8. Two live-data hazards are handled, not hoped about (2026-08-25)

- **A transcript being appended while the tar is written** makes the entry's
  declared size wrong, and `archive/tar` rejects both ends of that — overrun and
  underrun. `copyExactly` stats first, copies at most that many bytes, and pads a
  short read. The tail written after the stat is simply not in this backup.
- **The pruner (§17) or a project delete can remove a directory mid-walk**, so
  `os.ErrNotExist` during the walk is a skip, not a failure.

A failure part-way through removes the partial archive: an unrestorable archive
is worse than none, because it is the one a user reaches for on the day they
need it.

### 9. An archive is untrusted input, even one vincent wrote (2026-08-25)

Entry names are relative, forward-slashed, with no leading `/` and no `..`.
Restore rejects any entry that escapes the destination after cleaning — checked
twice, once on the name and once on the joined result — any entry that is not a
regular file or a directory (a symlink is exactly how an archive reaches outside
the directory it was told to write to), and any top-level name outside the known
layout. The last one is strict on purpose: a newer vincent that adds an entry
produces an archive this binary cannot restore faithfully, and saying so beats
restoring most of it in silence.

The destination guard is the mirror image: a backup path under
`{data_dir}/transcripts` is refused, because the archive would otherwise feed
itself.

### 10. No gate script (2026-08-25)

The acceptance here is assertable end to end in Go — `internal/cli`'s e2e tests
already build the real binary and drive real daemons — which is what
`scripts/*-gate.sh` exists to avoid duplicating.

## Tasks

- [x] **030.1** `internal/store`: `BackupTo(ctx, dst)` wrapping `VACUUM INTO ?`,
  beside `Vacuum` and `IntegrityCheck`. ✓ 2026-08-25
- [x] **030.2** `internal/backup`: the archive layout as a leaf package —
  `Manifest`, `Create`, `ReadManifest`, `Occupied`, `Restore`, `copyExactly`,
  the entry-safety checks and `moveAside`. ✓ 2026-08-25
- [x] **030.3** `internal/api`: `backup.go`, the route in `server.go`'s table,
  destination validation, the staging directory. No new `Deps` — `Dirs` carries
  the §12.2 directories and `Store` carries the rest. ✓ 2026-08-25
- [x] **030.4** `internal/apiclient`: `Backup` plus its wire types;
  `internal/cli`: `newDaemonBackupCmd` and `newDaemonRestoreCmd`, wired into
  `daemon.go`'s `AddCommand`. ✓ 2026-08-25
- [x] **030.5** Tests: the store copy's self-containment; the archive layout,
  exclusions and forward-slashed names; `copyExactly` both ways plus a real
  concurrent appender; the hostile-archive set; every refusal on both commands;
  `--force` displacement asserted on the *bytes*; an `apiclient` live test
  against the real handler; and an `internal/cli` e2e round trip that backs up
  **while a task is running** and restores into a clean installation.
  ✓ 2026-08-25
- [x] **030.6** Docs: §12.1 row and the client-side-restore amendment, §13.2
  route and the `/v1/daemon/*` note, §14's `VACUUM INTO` amendment, §18's two
  rows; `files.md`, `cli.md`, `api.md`, `security-model.md`; `CHANGELOG.md`.
  ✓ 2026-08-25

## Noted, deliberately not folded in

**Scheduled backups with retention.** Deferred, as the issue proposed: cron and
Task Scheduler cover the scheduling, and a daemon-side timer with a retention
policy is its own decision with its own configuration surface. Manual command
first.

**Restoring a single task.** The archive is whole-installation. Pulling one
task's rows and transcripts out of a backup is a different feature with a
different shape, and nothing here forecloses it.
