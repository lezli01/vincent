# 005 — Reclaim orphaned worktrees: `vincent gc` and a report-only startup reconcile

**Status:** ✅ done (8/8) · **Opened:** 2026-08-15

A directory under `{data_dir}/worktrees` or `{data_dir}/transcripts` could outlive
every reference to it, and nothing in vincent would ever look at it again. `vincent gc`
reclaims those; daemon start reports them and deletes nothing.

Closes [#95](https://github.com/lezli01/vincent/issues/95).

## The problem

Two producers, neither reconciled by anything that already exists:

- **Project delete.** `internal/api/projects.go` removes each task's worktree
  best-effort — the T1.5 decision, spelled out in the comment — and
  `DeleteProjectCascade` drops the rows regardless. Removal genuinely fails in
  practice: a file locked by another process on Windows, a permissions problem, a
  shell sitting in the directory. `DeleteProjectCascade` is also **the only path that
  deletes task rows**, which makes it the only producer of a directory no row can ever
  name again.
- **The create/claim crash window.** `ensureWorktree` created the worktree and *then*
  wrote `worktree_path`. A crash in between left `{data_dir}/worktrees/{id}` on disk
  while the row survived claiming nothing — and the task's next admission failed
  `worktree_path_occupied`, telling the user to "remove it manually".

Nothing covered either. `Manager.prune` is `git worktree prune` inside the *project*
repo, reachable only from `Create` and `Remove`: it deletes no directory of ours and
never runs for a task whose row is gone. `taskrun.Recover` (§12.4) reconciles step-run
rows and processes. `TranscriptPruner` (§17) walks archived task *rows*. `Archive`
fails closed and is not a producer at all.

Consequences: disk grows silently, invisible to the board, the CLI and the API; and a
future task can be blocked by a directory belonging to a project that no longer exists.

## Decisions

### 1. An orphan is a directory **no task row claims** — by path, not by name

*2026-08-15.* The claim is `worktree_path`. The issue proposed the name-based
definition ("the name is not the id of an existing task row"), and that one misses the
create/claim crash window entirely: the directory there *is* named after a live row, so
a name-based scan calls it claimed and leaves the `worktree_path_occupied` block in
place forever — one of the two problems this work exists to end. One rule covers both
producers.

**Beat:** the name-based definition. It is simpler and it fails the second acceptance
criterion.

### 2. That definition creates a race, and it is guarded at the source

*2026-08-15.* Between `Manager.Create` returning and the claim being persisted, a live
task's worktree is momentarily unclaimed, and a concurrent scan would delete a running
task's working tree. So `Manager` grows an `RWMutex`: creation takes it shared across
create-**and**-claim, the reclaim scan takes it exclusively across scan-**and**-remove.
In practice `Create` gained a `claim func(path string) error` callback
(`CreateAndClaim`) that `ensureWorktree` fills with its `SetTaskProgress` call — the
callback is what makes the window lockable at all. `Archive`'s remove-then-clear window
takes the same shared lock (`RemoveAndRelease`), so the reverse-mismatch report cannot
transiently accuse a task that is being archived.

**Beat:** an mtime/age heuristic ("ignore anything younger than a minute"). A timing
guess in place of a lock, in the package whose entire subject is ownership.

### 3. Dirtiness git cannot determine is `dirty_unknown`, skipped without `--force`

*2026-08-15.* This is the **common** case, not the rare one. An orphan's `.git` file
points at `{repo}/.git/worktrees/{n}`; the repository being deleted — or
`git worktree prune` having run in it — makes `git status --porcelain` fail, so
`Manager.IsDirty` returns an error rather than true or false. Reported distinctly from
`worktree_dirty`, because "git says you have uncommitted work" and "nobody can tell
what is in here" are different facts, and the second one dominates in the field.

Consequence accepted deliberately: on a machine where the projects really are gone,
most orphans need `--force`, and a default run reclaims less than the issue's prose
implies. The alternative is deleting work nobody can vouch for.

The string stays `dirty_unknown` with no `worktree_` prefix: it is a gc *skip* reason
and never becomes a task `block_reason`, so it is not part of the block-reason
vocabulary §18 shares with `internal/worktree`.

### 4. gc reclaims orphaned transcripts as well as orphaned worktrees

*2026-08-15.* Same producer, same permanence: `DeleteProjectCascade` deletes the rows,
and the §17 pruner walks archived *rows*, so `{data_dir}/transcripts/{task_id}` for a
deleted row is pruned by no path, ever. It costs one more readdir against the same
task-id set — no git, no dirty check, nothing to be careful about — and it means one
command instead of two for one leak. §17 is amended alongside §10.

### 5. The command is `vincent gc`, over two maintenance endpoints

*2026-08-15.* `GET /v1/maintenance/orphans` and `POST /v1/maintenance/gc`
(`{force, dry_run}`). The name breaks §12.1's noun-verb pattern knowingly: `git gc` is
the idiom users already have, and the scope now spans two directory trees, so a
`worktree` noun would have been wrong on the day it shipped.

### 6. The unattended path never deletes; the explicit one deletes by default

*2026-08-15.* Daemon start scans, logs one warning per orphan, and leaves everything in
place — silently removing a directory that may hold an agent's uncommitted work is what
§18's "never auto-deletes" posture around DB corruption already rejects. `vincent gc`
deletes by default, and the dirty check, the containment rule and the printed byte
report are what make that acceptable with a human behind it.

**Beat:** deleting automatically at startup, and making project delete's removal
authoritative. The second is the issue's own rejected alternative and stays rejected:
it strands the user with an undeletable project because of a locked file, and does
nothing for orphans created by a crash.

### 7. The count goes on `/v1/info`, not `/v1/health`

*2026-08-15.* Health is deliberately `{status, version}` and is the one unauthenticated
endpoint (§13.1) — disk-shape facts about the user's machine do not belong on it. The
count is computed per request (readdir plus the id queries, **no** size walk), so it is
never stale after a gc run and `/v1/info` stays cheap. Sizes are walked only by the
list and reclaim endpoints, which is where the report needs them.

### 8. The TUI shows the count and offers no action

*2026-08-15.* §15 view 6: "the view reports, it does not act". The daemon view gains a
line with the count and the words `vincent gc`, shown only when the count is non-zero —
a permanent "orphans: 0" is noise on every healthy daemon. It does not run gc, for the
same reason it cannot stop the daemon.

### 9. Sizes are apparent file sizes, symlinks not followed

*2026-08-15.* `filepath.WalkDir` sums regular files, reusing the pruner's `dirSize`. A
byte figure that walked into a symlinked cache would over-report the reclaim.

## Tasks

- [x] **005.1** — `internal/worktree`: `Root()`, `Reclaim(path)` (today's containment
      check exported unchanged), `ReasonDirtyUnknown`, the `RWMutex` and
      `CreateAndClaim` / `RemoveAndRelease` / `WithReclaimLock`. ✓ 2026-08-15
- [x] **005.2** — `internal/store`: `ListWorktreeClaims` and `ListTaskIDs`, both across
      archived and non-archived rows. ✓ 2026-08-15
- [x] **005.3** — `internal/taskrun/reclaim.go`: `Scan`, `Reclaim`, `Count`; wiring in
      `engine.go` (`CreateAndClaim`) and `actions.go` (`RemoveAndRelease`).
      ✓ 2026-08-15
- [x] **005.4** — `internal/api/maintenance.go` + the two routes; `orphans` on
      `/v1/info`. ✓ 2026-08-15
- [x] **005.5** — `internal/apiclient/maintenance.go`; `Info.Orphans`. ✓ 2026-08-15
- [x] **005.6** — `vincent gc [--dry-run] [--force] [--json]`. ✓ 2026-08-15
- [x] **005.7** — Report-only startup reconcile in `internal/daemon`; orphan line in the
      TUI daemon view. ✓ 2026-08-15
- [x] **005.8** — Tests and the m1-gate leg; spec §10/§12.1/§12.4/§13.2/§15/§17/§18 and
      the user docs. ✓ 2026-08-15

## Out of scope

- **Repairing the reverse mismatch.** A row whose `worktree_path` is gone is reported
  and left alone. Clearing it is a state change to a task, which belongs to the FSM and
  to `retry` (§18: a retry recreates the worktree from the branch if it survives), not
  to a disk-reclaim command.
- **A scheduled gc.** Retention has a ticker because it is a policy with a configured
  window; gc deletes on a human's word, and a timer that deleted worktrees unattended
  would be decision 6 in reverse.
- **Deleting branches.** §10's standing rule, restated: vincent never deletes a branch,
  gc included. (*Note 2026-08-16, [task 008](008-archive-branch-cleanup.md):* archive
  now has one exception, for a branch carrying no commits past its base. gc still has
  none, and for a reason specific to orphans — no orphan has a branch that is both
  known and safe to delete.)

## Verification

- `go test ./...` and `go test -race ./internal/taskrun` green (2026-08-15, macOS).
- `go tool golangci-lint run ./...` clean for `GOOS=windows`, `darwin` and `linux`.
- `./scripts/m1-gate.sh` passes with its new gc leg: plant
  `{data_dir}/worktrees/999999`, assert it is listed with a size, assert `--dry-run`
  leaves it, reclaim it, and assert the real task's worktree and branch survived
  (2026-08-15, macOS).
