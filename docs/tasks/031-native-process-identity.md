# 031 — Exact native process identity for the §12.4 PID-reuse guard

**Status:** ✅ done (5/5)
**Opened:** 2026-08-26
**Issue:** [#149](https://github.com/lezli01/vincent/issues/149)

Recovery journaled `time.Now()` — the daemon's own wall clock, read just after
spawn — into `step_runs.proc_started_at`, and `killOrphan` compared it with
`procx.StartTime(pid)`, which reads kernel bookkeeping. Two clocks, one loose
comparison, with `startTimeTolerance = 5 * time.Second` between them. In a
narrow crash / PID-reuse / timing window a different process could fall inside
the window and be killed as an orphan.

This exists because a P3 defense-in-depth path is exactly where the invariant
should be strongest: the cost of a wrong "yes" is killing a stranger's process
on the user's machine.

## Decisions

### 1. The PR D tolerance is superseded, not quietly changed (2026-08-26)

±5 s is a **recorded decision** — the PR D grill session of 2026-08-08,
`docs/history/v0-tasks.md`: *"within ±5 s of the journaled spawn time — covers
tick/btime conversion error while making a reuse collision practically
impossible"* — and spec §12.4 encodes it as "within a small tolerance". Issue
149 overturns it, so it is overturned in writing: §12.4 carries a dated
in-place amendment and the PR D bullet carries a superseding note, in the same
style as the ledger's existing *"(Amended by the PR F grill: …)"* annotation
and never as a deletion. Both land in this PR, with the code that makes them
true.

The tolerance itself survives — as the fallback, in one place, described as
what it now is.

### 2. Identity is an opaque, versioned, per-OS token in a new column (2026-08-26)

Migration `0013_proc_identity.sql` adds `step_runs.proc_identity TEXT`. `procx`
gains `Identity(pid int) (string, error)`, whose contract is *compare, never
parse*:

- **Linux** — `/proc/<pid>/stat` field 22 raw (start ticks, USER_HZ = 100, so
  10 ms precision) joined with `/proc/sys/kernel/random/boot_id`. Deliberately
  **not** `btime`: keeping the tick count out of absolute time is what makes
  the token immune to an NTP step and to suspend/resume, and the boot id is
  what makes a reboot a guaranteed mismatch rather than an arithmetic
  coincidence.
- **macOS** — `kinfo_proc.Proc.P_starttime` `sec` and `usec` (1 µs), a stamp
  taken once at fork that a later clock adjustment does not move.
- **Windows** — the creation `FILETIME` from `GetProcessTimes` in its raw
  100 ns unit, not converted to a `time.Time`. The unit is finer than the
  value — the system clock updates on a ~15 ms tick — so for one PID a
  collision would need a reuse inside a single tick.

Each token carries a scheme prefix (`linux1:`, `darwin1:`, `windows1:`) so a
future format change cannot be mistaken for a match: an old token stops
comparing equal, which fails the safe way.

**Beat:** re-using `proc_started_at` to hold `procx.StartTime`'s output and
comparing *that* exactly — no migration, but nothing would then distinguish a
legacy wall-clock row from a new OS-read row, and on Linux an absolute time
derived from `btime` is precisely the value that can move under the process.
`proc_started_at` stays exactly as it is: the human-readable stamp, and the
legacy fallback's input.

### 3. Absent identity falls back to today's guard (2026-08-26)

A row whose `proc_identity` is NULL — written before the migration, or by a
spawn where the identity read failed — is judged by the existing ±5 s
wall-clock comparison, unchanged. No host gets worse than it is now.

That second case is real, not hypothetical: task 022's verification record
already documents a workspace where `StartTime(self)` returns `process not
found`. A failure to read identity *during* recovery, when one was journaled,
is never a kill — the identity branch does not fall back to the tolerance,
because a row that *has* an identity has already answered the question the
tolerance was approximating.

### 4. A mismatch stays a log warning (2026-08-26)

`log.Warn` and move on; the task re-queues normally, as it does today. No new
`Reason*` constant, no new block reason, no doctor problem. The issue asks for
defense in depth on a P3 path, and widening the blast radius to task state was
rejected: an unkilled orphan is a stray process, not a corrupt task.

### 5. One test seam, for the branch that has no other way in (2026-08-26)

`taskrun` holds an unexported `identityOf = procx.Identity` that a test swaps
for a failing stub. "Cannot prove, do not kill" matters most exactly where the
proof is unavailable, and that branch is otherwise unreachable deterministically
— an untested invariant on the path that protects a stranger's process is not
much of an invariant.

### 6. No API, TUI or gate change (2026-08-26)

`proc_started_at` is not on the wire and `proc_identity` will not be either.
`internal/cli/crash_e2e_test.go:TestCrashRecoveryE2E` already drives the real
binary through a hard kill and a restart, so the identity path gets its
end-to-end proof from a test that exists; nothing here needs a
`scripts/*-gate.sh`.

## Tasks

- [x] **031.1** `internal/procx`: `Identity` plus `identity_linux.go`,
  `identity_darwin.go`, `identity_windows.go`; the Linux stat-field parse and
  the darwin/windows OS reads factored out of the `starttime_*.go` files and
  shared rather than duplicated. ✓ 2026-08-26
- [x] **031.2** `internal/store`: migration 0013, `StepRun.ProcIdentity`, the
  column through `stepRunColumns`/insert/update/scan, and
  `terminalizeOpenStepRuns` clearing it beside `pid`. ✓ 2026-08-26
- [x] **031.3** `internal/taskrun`: both journal sites persist the identity in
  the same `UpdateStepRun` as the PID; `killOrphan` splits into
  `identityStillHolds` and the legacy `startTimeStillHolds`; the
  `startTimeTolerance` comment says what it now is. ✓ 2026-08-26
- [x] **031.4** Tests: `procx` identity stability, distinctness, self, gone,
  and the Linux boot-id component; `recover_test.go`'s matching identity,
  same-PID-different-identity, foreign-boot token, dead PID with an identity,
  unreadable identity, and the two legacy-tolerance cases kept as they were;
  `store`'s round trip, terminalize-clears, and a 0012 database migrated with a
  legacy row reading back NULL. ✓ 2026-08-26
- [x] **031.5** Docs: spec §12.4 amendment and the §14 column, the superseding
  note on PR D in `docs/history/v0-tasks.md`, this document, and the two public
  pages that stated the old "PID **and** start time must match" rule —
  `docs/reference/task-lifecycle.md` and `docs/faq.md` — plus the crash-first
  bullet in `CLAUDE.md`. No `CHANGELOG.md` edit: the file deliberately carries
  no `Unreleased` section (RELEASING.md step 3), so this change's prose belongs
  in the Release Please pull request. ✓ 2026-08-26

## Noted, deliberately not folded in

**A `pidfd`/handle-based guard.** Linux `pidfd_open` and a retained Windows
process handle identify a process without any timestamp at all, but both need
the *original* daemon to still hold the descriptor — which is the one thing a
crash guarantees it does not. A journaled token is the only form of identity
that survives the process that took it.

**Reporting a mismatch anywhere but the log.** Decision 4 keeps it a warning.
If orphan-sparing ever turns out to be common enough to act on, `GET
/v1/doctor` is where it would surface, and that is a separate decision with a
separate surface.
