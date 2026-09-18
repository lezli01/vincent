# 115 — Scheduled daemon backups with retention

**Status:** ✅ done (6/6)
**Opened:** 2026-09-17
**Issue:** [#410](https://github.com/lezli01/vincent/issues/410)
**Spec:** amends §12.1, §12.2, §12.3, §13.2, §14, §15, §17 and §18; amends
[task 006](006-vincent-doctor.md) decision 7

[Task 030](030-daemon-backup-and-restore.md) added the manual `vincent daemon
backup` and `restore`, and deliberately left out scheduling and retention (its
"Noted, deliberately not folded in" paragraph points at this issue). No task
document, spec section or v0 decision rules scheduling out, and neither §2 nor
§20 mentions backups. §20's `type: schedule` trigger source is a different
thing: it would start tasks, not copy the daemon's state. So this work builds on
task 030 and contradicts nothing, except the one amendment decision 4 makes on
purpose.

It adds a daemon-side timer. The timer takes the same archive `vincent daemon
backup` writes, on an interval, into a backup directory, and after each
successful run it prunes its own archives past a count. How backups are going is
a `backup` group on `GET /v1/doctor`, so `vincent doctor`, the MCP `doctor` tool
and the TUI daemon view show it too. A failing backup is a doctor problem. A new
`backup:` block in `config.yaml` sets it up, and it can be edited through task
060's config editor, `vincent config get|set` and `PATCH /v1/config`.

There is no new endpoint, no new CLI command, no migration and no gate script.
Task 030 decision 10's reasoning still applies: `internal/cli`'s e2e tests
already build the real binary and drive a real daemon.

## Decisions

These were settled with the author on 2026-09-17, except where a decision says
it was taken during implementation.

### 1. Off by default; the directory defaults to under the data dir (2026-09-17)

```yaml
backup:                        # scheduled daemon backups (task 115)
  interval: 0                  # 0 (default) = off; e.g. 24h. Negative, or under 1h, is refused
  keep: 7                      # timer-written archives kept; 0 = keep everything
  dir: ""                      # "" = {data_dir}/backups
```

`backup.interval` is the only switch: setting it is the one step that turns
backups on. `backup.dir: ""` resolves to `{data_dir}/backups`, created `0700` on
first use. Archives stay `0600`, as `backup.Create` already writes them. The
default `config.yaml` ships the block commented out, per task 060 decision 7.

**Beat:** on by default. Every archive carries transcripts (task 030 decision 1,
unchanged here), which can run to gigabytes. Turning it on for every existing
install would quietly use up to `keep` times that much disk.

**Beat:** making `dir` required. One key to turn the feature on is worth more
than making the user pick a location first.

The default location is **on the same disk as the database**, and the
documentation says so plainly. It protects against corruption and human
mistakes, not against losing the disk. Anyone who wants that protection points
`backup.dir` somewhere else.

*Decided at implementation time (2026-09-17):* a non-empty `backup.dir` must be
an **absolute path**, and a relative one refuses the file. This is the reason
`POST /v1/daemon/backup` refuses a relative path: the daemon would resolve it
against its own working directory, which nobody chose. **Beat:** resolving a
relative `dir` against the data or config directory, which would be a second
path rule that the manual command does not have.

### 2. The schedule is an interval, counted from the newest archive on disk (2026-09-17)

`backup.interval` is a `config.Duration`, in the same style as
`github.poll_interval` and `update.poll_interval`.

**Beat:** a time of day (`at: "03:00"`), which brings time-zone and DST cases
and allows only one run a day. **Beat:** a cron expression, which needs a parser
dependency for something cron and Task Scheduler already do outside vincent.

*Narrowed 2026-09-18 ([121](121-scheduled-triggers.md) decision 1, issue
#480).* The cron half of that second **Beat** no longer holds everywhere, and
the reasoning above is left as it was written. Task 121's `type: schedule`
trigger source takes a cron expression, because neither ground transfers to it:
the parser is **written** rather than taken, five fields in
`internal/trigger/schedule.go` with no addition to `go.mod`, and a crontab line
calling `vincent task create` is not the thing outside vincent that already
does the job — it skips the `match:` filter, the dedupe ledger, the propose
gate, the hourly limit and the delivery ledger, which is the whole of what
`internal/trigger` is. `backup.interval` is untouched: it stays a
`config.Duration` counted from the newest archive on disk, and this task's
everything else stands.

- **"Last success" is the newest timer-written archive name in the directory**,
  not a value in memory. A restart does not reset the clock, and nothing is
  persisted beyond the archives themselves. An install whose newest archive is
  older than the interval, or that has none, runs at the first check after
  startup, so an overdue backup runs right after the daemon starts.
- **A failed attempt is retried one hour later**, not at the next check.
  Without that delay a failure leaves the newest archive stale and the timer
  loops hot, once a minute.
- *Decided during implementation, not by the author:* **an interval above 0 but
  under 1h is refused.** Each run holds the store's only connection for the
  length of `VACUUM INTO` (task 030 decision 7), then re-tars every transcript.
  Below an hour that is a load generator, not a backup, and cron covers anything
  tighter. A negative `interval` or `keep` is refused, as `update.poll_interval`
  is. **Beat:** accepting any positive interval. It is recorded here so review
  can challenge it.
- **The timer reads the current config on every check**, the way
  `TranscriptPruner` does. An edit to `interval`, `keep` or `dir` takes effect
  at the next check, which runs about once a minute, with no restart and no
  hook into the config applier.

### 3. Retention prunes only the timer's own archives, by count (2026-09-17)

The timer names what it writes `vincent-backup-<UTC YYYYMMDDTHHMMSSZ>.tar.gz`.
The timestamp is fixed-width UTC, so sorting by name sorts by time. **That name
pattern is the only thing that makes a file count toward `keep` or be
prunable.** A manual `vincent daemon backup` archive in the same directory is
never counted and never deleted, and neither is any other file. `keep: 0` keeps
everything, as `transcript_retention_days: 0` does.

**Beat:** an extra `keep_days` age limit, which is a second key to document and
defend. **Beat:** counting every vincent archive in the directory, which could
delete a hand-made backup.

These rules come from task 030 and §18's never-auto-delete stance:

- **Prune only after a successful run.** A failure never lowers the number of
  good archives.
- **An archive never appears under its final name until it is complete.** The
  timer writes the database copy and the archive into a `.vincent-backup-*`
  staging directory inside `backup.dir`, then renames the archive into place in
  the same directory. Otherwise a daemon killed mid-write would leave a truncated
  file that matches the pattern, counts as the newest success, and is kept by
  retention. A manual backup keeps writing its destination directly, as before.
- **Leftover staging directories are swept at timer start only.** At that
  moment no manual backup can be running, because a manual backup goes through
  this same daemon.
- **A prune failure is logged at warn** and recorded on the status as
  `prune_error`. It is not a doctor problem, because the backup itself
  succeeded.
- **A `backup.dir` inside `{data_dir}/transcripts` or `{config_dir}/workflows`
  fails the run**, because the archive walks both trees and would read itself —
  and every later run would carry all the ones before it. `POST
  /v1/daemon/backup` refuses the transcript half with a 400 (task 030 decision
  9); the workflows half was noticed while writing this task and is refused by
  the timer only, since the manual endpoint is outside this scope.
- On Windows, every file handle is closed before the rename and before a
  remove.

### 4. A failed backup is a doctor problem — this amends task 006 decision 7 (2026-09-17)

When backups are on (`interval > 0`) and **the most recent attempt failed**,
`Report.Evaluate` adds a problem in group `backup` with the message `the last
scheduled backup failed: <error>`, and `vincent doctor` exits 1. The next
successful attempt clears it. When backups are off there is never a problem. The
same holds for a local report with no daemon: the group shows `known: false`, as
the database group does.

This deliberately widens task 006 decision 7's closed unhealthy set, and it
differs from the GitHub, update and skills rows (tasks 035, 055 and 095), which
never change the exit code. Those rows report defaults nobody chose. This one
reports a feature the user switched on, so it cannot fire "on almost every
machine", which is the thing decision 7 guards against. A backup that fails
silently is also found out on the day it is needed.

**Beat:** an info-only row, which leaves decision 7 untouched and pays exactly
that cost.

"Overdue" alone, because the daemon was down, is **not** a problem. Only an
attempt that ran and failed is. A prune failure is not a problem either
(decision 3).

### 5. Where the code lives (2026-09-17, decided during implementation)

- **`backup.Take` holds the "stage a `VACUUM INTO` copy, then `backup.Create`"
  sequence** that used to live in `internal/api/backup.go`, and both
  `POST /v1/daemon/backup` and the timer call it. `internal/backup` stays a leaf
  package: it receives the database copy as a
  `func(ctx context.Context, dst string) error` and does not import `store`. Its
  `Atomic` field selects the stage-then-rename write of decision 3, which only
  the timer asks for. `handleBackup` keeps its behavior and messages, and its
  existing tests pass unchanged. **Beat:** a second copy of the sequence in the
  timer, which would leave two staging rules and two cleanup paths to drift.
- **A new package, `internal/backupsched`**, holds the timer, the next-due
  calculation, pruning, the staging sweep and an in-memory `Status` behind a
  mutex. Its `doc.go` cites §12.3 and §17. The clock and the database copier are
  passed in, and each pass can be called on its own (`Check(ctx, now)`), the
  same shape as `TranscriptPruner.Prune(ctx, now)`. It is wired in `daemon.Run`
  beside the pruner, and `api.Deps` gains `BackupStatus func()
  backupsched.Status`, following `UpdateStatus`. **Beat:** the timer as a file
  in `internal/daemon` beside `updatecheck.go`. Both `internal/api` and
  `internal/doctor` need the `Status` type, `daemon` imports `api`, and
  `doctor` must not import `daemon` (task 006 decision 6). That is the reason
  task 055 put `release.Status` in its own package.
- **The doctor `backup` group** has the fields `known`, `enabled`, `dir`,
  `interval`, `keep`, `last_success_at`, `last_attempt_at`, `last_error`
  (omitted when empty), `next_due_at`, `last_bytes`, `retained` and
  `prune_error` (omitted when empty). Timestamps are `null` when unknown. The
  settings come from `config.yaml`, so a local report still shows them.

## Design settled without a question

- **The config surface follows task 060.** These change together:
  `config.Config` with its validation and defaults; `defaultConfigYAML`, with
  the commented-out `backup:` block; `configResponse`/`configPatch` in
  `internal/api/config.go`, which the drift tests enforce;
  `apiclient.Config`/`ConfigPatch`; `internal/tui/configkeys.go`;
  `internal/cli/config.go`; and `scripts/m11-gate.sh`'s served-key list.
- *Decided during implementation:* **`backup.dir` is marked dangerous** in the
  TUI editor. An archive contains `config.yaml`, with its `environment.set`
  values and `notify.command`, and every transcript. Pointing the directory at a
  synced or shared folder decides what the daemon exposes, and that is the rule
  the confirmation guards. `interval` and `keep` are not marked dangerous. None
  of the keys is secret, so the MCP `config_get` redaction does not change.
- **The MCP `doctor` tool needs no change.** It replays `GET /v1/doctor`, so it
  returns the new group with the rest of the report. Taking a backup is still
  not an MCP tool.

## Tasks

- [x] **115.1** `internal/config`: the `backup:` block (`Backup`, `Enabled`,
  `ResolveDir`), its defaults and validation (a negative interval, one above 0
  and under `1h`, a negative `keep`, and a relative `dir` all refused), and the
  commented-out block in `defaultConfigYAML`. The task 060 surface:
  `internal/api/config.go`, `apiclient`, the TUI config editor with `backup.dir`
  marked dangerous, `vincent config`, and `scripts/m11-gate.sh`'s served keys.
  ✓ 2026-09-17
- [x] **115.2** `internal/backup`: `Take` and `Snapshot` (with `Atomic`) and the
  exported `StagingPrefix`; `handleBackup` moved onto `Take` with its tests
  unchanged. ✓ 2026-09-17
- [x] **115.3** `internal/backupsched`: the timer, next-due from the newest
  archive name, the 1h retry, pruning by name pattern, the staging sweep at
  start, and `Status`. Wired in `daemon.Run` beside the transcript pruner.
  ✓ 2026-09-17
- [x] **115.4** Reporting: the `doctor.Backup` group and its problem in
  `Report.Evaluate`; `api.Deps.BackupStatus`; the `apiclient.DoctorBackup`
  alias; the `BACKUP` group in `vincent doctor`; the backup row, with its error,
  in the TUI daemon view. ✓ 2026-09-17
- [x] **115.5** Tests. Config: the defaults, every refusal, `dir: ""` resolving
  to `{data_dir}/backups`, the editor uncommenting the block in place, and the
  060 drift tests. `backupsched`, with an injected clock and a fake copier and
  no sleeping: off runs nothing; an empty directory runs at the first check; a
  new instance over existing archives does not run early; a failure sets
  `last_error`, waits 1h, and a later success clears it; a config change applies
  at the next check; pruning keeps the newest `keep` matching archives and never
  touches a manual archive or a non-matching file; `keep: 0` keeps everything; a
  failed run prunes nothing; a copy that fails mid-write leaves nothing under
  the final name; stale staging directories are swept at start; a produced
  archive passes `backup.ReadManifest`. API and doctor: the group is present,
  the problem appears if and only if backups are on and the last attempt failed,
  and the local report shows the group unknown with no problem. An `apiclient`
  live test for the group, a TUI live test for the daemon view row and its
  error, and an `internal/cli` e2e test: a real daemon with backups on and an
  empty directory writes an archive at startup, and `vincent daemon restore`
  restores it into a clean installation. ✓ 2026-09-17
- [x] **115.6** Docs: §12.1, §12.2, §12.3, §13.2, §14, §15, §17 and §18 amended,
  dated; `configuration.md`, `files.md`, `cli.md`, `api.md`,
  `security-model.md`, `features.md`; `CHANGELOG.md`; the link from task 030's
  "Noted" paragraph and the pointer on task 006 decision 7. No screenshot
  changed: no `docs/assets/tui-*.png` captures the daemon view. ✓ 2026-09-17
