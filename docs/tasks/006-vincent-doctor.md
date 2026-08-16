# 006 — `vincent doctor`

One command that answers "why is nothing running?", fed by a daemon-side
endpoint and degrading to a local report when no daemon answers.

Issue [#94](https://github.com/lezli01/vincent/issues/94).

Nothing in `docs/spec.md`, `docs/history/v0-tasks.md` or `docs/tasks/` recorded
a decision against a diagnostic command, and §20's future-work list did not
mention one. The single binding decision this work brushes against — task 003
decision 4, "no pre-flight refusal on `logged_in: false`" — is kept, explicitly
and unchanged.

## Why

Answering "why is nothing running?" took five surfaces, and the sequence was
written down nowhere: `vincent daemon status`; reading `daemon.json` by hand;
opening the TUI's daemon view for the log tail (the one pane that deliberately
works without a daemon — and the only way to reach it is an interactive
program); `curl /v1/agents` with a hand-extracted bearer token; and finding the
config file and data dir yourself. `docs/guides/troubleshooting.md` covered each
symptom individually, but nothing started from "nothing is happening" and
nothing produced a single pasteable capture for a bug report.

Three facts had no surface at all: the database's size and integrity, the disk
free under the data dir, and whether a worktree directory still belonged to a
task. And **agent authentication was worse than undocumented — it was unknowable
for two of three adapters**, so "the CLI is installed but your session expired"
stayed invisible until a task burned its retry budget.

## Tasks

- [x] **006.1** `internal/doctor`: report types, composition, disk-free split
      (`_unix`/`_windows`), the worktree scan and orphan classifier. ✓ 2026-08-15
- [x] **006.2** Store accessors: `CountTasksByState`, `LiveTaskIDs`,
      `SchemaVersion`, `NewestMigration`, `IntegrityCheck`, `Vacuum`, `Path`.
      No migration — nothing about the schema changes. ✓ 2026-08-15
- [x] **006.3** `GET /v1/doctor` and `POST /v1/doctor/fix`, plus the `Deps` the
      report needs (dirs, log path, log tailer). ✓ 2026-08-15
- [x] **006.4** `apiclient.Doctor` / `DoctorFix` and the wire types. ✓ 2026-08-15
- [x] **006.5** `vincent doctor` (`--json`, `--fix`, `--force`), with the
      degraded no-daemon path and the three exit codes. ✓ 2026-08-15
- [x] **006.6** codex `logged_in`: a `login status` probe in `Detect`, plus the
      fakeagent dialect and the four-leg parse table. ✓ 2026-08-15
- [x] **006.7** Spec amendments (§9.3, §9.5, §9.6, §9.7, §10, §12.1, §13.2,
      §17, §18) and the user-facing docs derived from the source. ✓ 2026-08-15
- [x] **006.8** A doctor assertion in `scripts/m2-gate.sh`, over curl against a
      running daemon, including the orphan reclaim. ✓ 2026-08-15

## Decisions

All dated 2026-08-15 and binding.

### 1. Codex gains an auth probe; claude stays `null`

The issue asserted that both adapters gain "a cheap authentication probe".
Codex has one — `codex login status`. Claude does not: the captured `--help`
(`internal/agent/claude/testdata/help_2.1.224.txt`, 2.1.224) carries no
`login`/`auth`/`status` surface, and the only definite answer available is a
real prompt round-trip, which costs API tokens and seconds on a cold cache and
contradicts §9.6's "always dynamic, never slow".

So `codex.Detect` gained a `loggedIn` probe built exactly like `cursor.loggedIn`:
non-zero exit is `false`, an explicit negative is `false`, an explicit positive
is `true`, and a timeout or a failure to spawn is `nil` rather than a guess. The
timeout rule is not optional — T4.22 records that a probe killed on Windows
exits 1, which the naive exit-status reading turns into a false accusation
against a logged-in account.

Claude keeps `logged_in: null`, which also keeps the v0 T1.7 decision
(`docs/history/v0-tasks.md:145`) intact. §9.5 now records *why* rather than
leaving the claim bare.

**Beat:** a doctor-local probe leaving `Detect` alone — the board and the
new-task form would keep showing "unknown", so the Monday-morning failure stays
invisible everywhere except one command the user has to know to run. Also beat:
probing claude with a minimal `claude -p` round-trip, which bills the user for a
health check.

### 2. Doctor always forces a re-probe

`logged_in` is served from the §9.6 binary-identity cache, and auth state is
*not* a pure function of the binary. A cached `false` therefore survives the
user logging in, until the CLI is upgraded or `?refresh=true` arrives — which
would break doctor in the exact loop it exists for: run doctor, log in, run
doctor again, still told you are logged out.

The doctor endpoint asks the catalog with `refresh=true` unconditionally. The
cost is one probe per adapter per invocation of a command the user ran
deliberately, bounded by the existing probe timeouts, and it needs no surgery on
`catalog.go`.

**Beat:** giving `logged_in` its own short TTL inside the cache. It fixes every
surface rather than one, but splits a cache line that is currently one clean
rule and adds a second probe cadence; it is the better follow-up if the board's
staleness ever becomes a complaint of its own.

### 3. An orphan is gc's orphan — doctor owns no second definition

*Superseded 2026-08-16, on the rebase onto task 005.* This decision originally
read: **a directory under `{data_dir}/worktrees/` whose `{task_id}` matches no
non-archived task**, with a directory whose name is not a task id reported but
never removed. Task 005 landed `vincent gc` on `master` first with a different
and better rule — **an entry under a data root that no task row *claims* by
`worktree_path`** — and explicitly rejected the name-based reading, because the
crash between `git worktree add` and the claim write leaves a directory named
after a *live* task that nothing owns. A name-based scan calls that directory
claimed and leaves the task failing `worktree_path_occupied` forever.

Two definitions of "orphan" in one binary is a defect on its own, so doctor now
reads gc's scan and `--fix` calls gc's `Reclaim`: one classifier
(`internal/taskrun/reclaim.go`), one removal path, one containment check. What
doctor keeps is the *reporting* — orphans beside the disk figures in the one
command that answers "why is nothing running?" — and an explicit "orphans
unknown" with no daemon, since the claim set lives in a database only the daemon
opens (§4).

Scope stays local to the data dir. `git worktree prune` in the user's repos is
**not** run, so a stale registration can survive in a project repo after the
directory goes; the report names that and points at `git worktree prune`, rather
than reaching into a repository it was not asked to touch.

Dirty orphans (`git status --porcelain` non-empty, the same rule
`internal/worktree` and §10 already use) are counted and reported but not
removed without a second explicit `--force`, mirroring the archive path. If the
dirty check cannot run at all — the main repo is gone, so the directory is just
files — the orphan is treated as dirty. Conservative in the one direction that
matters: nothing is deleted on the strength of a check that did not happen.

This amended §10, which said cleanup happens "only on `archive`".

### 4. `--fix` refuses to compact while work is in flight

The daemon holds SQLite open on one WAL connection, and a full `VACUUM` rewrites
the file under an exclusive lock. When any task holds a slot — `running` or
`awaiting_input` (§11) — `--fix` reports `skipped — N task(s) in flight` and
does not compact; otherwise it runs a real `VACUUM`. Being honest about the
stall beats imposing it on a step mid-write.

**Beat:** `PRAGMA wal_checkpoint(TRUNCATE)` only — safe under load, but it does
not reclaim pages freed by transcript pruning or task deletion, which is the
growth the issue is about.

### 5. Repair is a POST, not a side effect of the GET

The issue said the writes happen "behind the same endpoint". They do not share a
method: `GET /v1/doctor` is read-only, and `POST /v1/doctor/fix` performs the
repair and returns what it did plus a fresh report. The router allow-lists
methods per route, and a GET that deletes directories would be wrong in a table
clients read as a contract. The daemon still performs every write, so the
one-writer invariant holds and repair is simply unavailable when no daemon
answers — exactly as the issue requires.

### 6. Composition lives in `internal/doctor`; the TUI is out of scope

A degraded report has to be composed client-side regardless, since the endpoint
needs a daemon and doctor must run without one. Rather than two composers that
drift, `internal/doctor` owns the report types and every probe that needs no
database: paths, config parse, adapter detection, log stat and tail, disk free,
and the worktree scan. The API handler calls it and adds the rows only the
daemon can serve; the CLI calls it directly when no daemon answers.

**`internal/doctor` must not import `internal/daemon`.** `daemon` imports `api`,
so `api → doctor → daemon` would close a cycle. The daemon-liveness rows are
therefore supplied by the caller, and so are the log path *and the log tailer* —
`daemon.TailFile` stays where it is, and both callers pass it in rather than a
second copy of a routine with subtleties (a window starting mid-line, a rotation
renaming the file underneath the reader) growing here.

Because `internal/doctor` owns the report types, `internal/apiclient` declares
them as **aliases** rather than copies — the one exception to that package's
"client owns its wire types" rule, and for the rule's own reason: two
declarations of the same document is precisely the drift the shared package
exists to prevent. The live test still proves the JSON round-trips.

The TUI daemon view is untouched. The issue's "one report" goal is served by the
shared package and the endpoint, which is what makes the TUI follow-up cheap;
folding it in now means reworking a view that has a live no-daemon path and a
poll loop, for a diff that is already wide.

### 7. Exit 1 means broken, not imperfect

`0` healthy · `1` problems found · `2` no daemon answered — following
`vincent daemon status` and the PR U decision that exit 2 means the request was
never made.

Unhealthy is a **closed set**: `config.yaml` exists and fails to parse, the
daemon is alive but unresponsive, `integrity_check` is not `ok`, the database's
applied migration version is ahead of the binary's newest embedded migration, or
orphaned worktrees are present. A missing or logged-out agent CLI is reported
plainly and does **not** set the exit code — most users install one of three
adapters, and a doctor that exits 1 on almost every machine is useless in a
script. Task counts never affect the exit code: twelve blocked tasks is
information, not a defect.

When no daemon answers, exit 2 wins over any exit-1 finding in the local report.
The report is still printed in full.

## Out of scope

- **Pre-flight refusal on `logged_in: false`.** Task 003 decision 4 beat it
  deliberately; this work makes the state visible, not blocking.
- **The TUI daemon view.** Decision 6 — a follow-up the endpoint exists to make
  cheap.
- **`git worktree prune` in user repos.** Decision 3.
- **A claude auth probe.** Decision 1; revisit if the CLI ever grows a
  non-interactive auth surface.
