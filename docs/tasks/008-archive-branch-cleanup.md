# 008 — Archive cleanup: delete a task's branch when it has no commits past its base

**Status:** ✅ done (7/7) · **Opened:** 2026-08-16

Archiving a task removed its worktree and stopped there, so every task ever run left a
`vincent/*` ref behind — including the workflows that never write to the repository at
all. This adds one narrow exception to §10's standing rule: a branch with **no commits
past its recorded base** is deleted at archive time. Nothing that carries a commit
object is ever touched.

Closes [#105](https://github.com/lezli01/vincent/issues/105).

## The problem

A workflow that files a GitHub issue, posts a summary or runs a read-only review still
gets a worktree and a branch, because `git worktree add -b` makes both or neither. It
writes nothing, so the branch it was given never receives a commit. `Runner.Archive`
removed the worktree and released the claim; §10 then said, in as many words, that "the
branch is **never** deleted by vincent".

The remedy was manual, and `docs/getting-started/installation.md` said so: list the
archived tasks, read the branch column, delete them by hand. Branch names are
configurable since task 001, so there is not even a `vincent/*` glob that reliably
finds them.

## Decisions

### 1. The test is `git rev-list -n 1 {base}..{branch}` producing no output

*2026-08-16.* The tip is an ancestor of the base. Both fields are on the task row since
`0001_init.sql`, so nothing has to be inferred or stored. It costs one cheap git call,
it stays correct when the base moves forward *after* the task started, and it is exact:
a branch that passes holds no commit object anybody could want back.

Both refs are named in full (`refs/heads/…`). An ambiguous short name — a tag sharing
the branch's name, a deleted base with a surviving remote-tracking ref — must read as
*cannot judge* rather than quietly resolve to something else and answer confidently.

**Beat:** deleting when the *net diff* against base is empty (commits that cancel out).
That destroys real commit objects, for a case rarer than the one this is about. It was
the issue's own rejected alternative and stays rejected.

### 2. Any git failure means *cannot judge*, and keeps the branch

*2026-08-16.* Base branch renamed or deleted, repository gone, git unhappy for a reason
nobody predicted: the outcome is `unknown`, the branch survives, the error is logged.
Deliberately distinct from `has_commits`, the same way task 005 separated
`dirty_unknown` from `worktree_dirty` — "it has work in it" and "nobody can tell" are
different facts, and only one of them is a judgement.

`git branch -d`, never `-D`, for the same posture: its own merged check is a second
belt behind the rev-list, and its refusal is what covers a branch checked out in
another worktree — exactly the case where a force delete corrupts somebody's working
tree.

### 3. The remote leg gets its own key, defaults to false, and runs only on an attended archive

*2026-08-16.* `delete_remote_branch_on_archive`, honoured only by
`POST /v1/tasks/{id}/archive`. `DELETE /v1/projects/{id}?force` never touches a remote.

Vincent has no push, no remote refs and no credential handling anywhere in the tree
today; the single mention of a remote is `origin/HEAD` detection at project
registration. Deleting a branch on a forge the user shares with other people is
unrecoverable and outward-facing — strictly further-reaching than the local directories
task 005's "the unattended path never deletes" was written about. Opt-in and
attended-only *extends* that rule; a local-style default-true would have contradicted
it.

It runs only after a local delete that succeeded, and only when the branch has a
configured upstream (`branch.{name}.remote` **and** `branch.{name}.merge`). No upstream
means nothing was pushed as far as vincent knows, so nothing is attempted — and
guessing a remote name from a local one is exactly the inference that deletes the wrong
ref on somebody else's forge. The upstream is read from git *config*, not from
`@{upstream}`: a remote-tracking ref can be pruned while the configuration that named
it survives, and the question here is "did vincent ever push this".

**Beat:** the issue's "skip any branch with an upstream" — the remote counterpart is
deletable, just not by default and not unattended.

### 4. `vincent gc` and `vincent doctor --fix` are out of scope; task 005's rule stands unamended

*2026-08-16.* The issue lists them. No orphan gc sees has a branch that is both **known**
and **safe to delete**:

- A row-less orphan — the project-delete leftover — has no `base_branch`, no
  `branch_name`, and usually no reachable repository to run `rev-list` in. The
  ancestor-of-base test has no input there.
- The crash-window orphan is named after a **live** task row, whose branch must
  survive.

So "branches are never deleted here either" (§10, task 005) keeps its full force, gc
gains neither a deletion path nor a report line that would read the same on every row,
and `docs/reference/cli.md`'s "It never deletes a branch" stays true as written.

### 5. `DELETE /v1/projects/{id}?force` sweeps every row it drops, archived ones included

*2026-08-16.* The issue describes that endpoint as archiving its tasks; it does not. It
force-removes worktrees best-effort and then hard-deletes the rows through
`DeleteProjectCascade`. After the cascade the branch names are gone forever, so this is
the last moment they exist — an archived row whose branch survived (the setting was off
when it was archived) is reachable from nowhere else afterwards. The sweep therefore
covers the whole `tasks` slice the handler already holds, not just the non-archived
ones.

Local-only and best-effort, exactly as the worktree removal beside it already is: a
failure is logged and the cascade proceeds.

### 6. The outcome rides the archive response; every other path logs

*2026-08-16.* `archived` is terminal and a `block_reason` would be a lie on it, and no
client renders event rows today — the TUI and CLI use SSE only to trigger refreshes. So
`POST /v1/tasks/{id}/archive` returns `branch: { name, result, error?, remote? }` beside
the task, and the TUI prints one line where a human asked for the action. Project delete
and anything else writes `daemon.log` only. No new event type (§13.3 keeps its PR D
shape) and no migration.

The object is **absent**, not null-with-empty-strings, when the branch step did not run,
so a client that predates the field sees exactly what it saw before.

### 7. Ordering is worktree removal → transition → branch, and a branch problem never fails an archive

*2026-08-16.* The branch is checked out in the worktree until the worktree is gone, so
the branch work happens *after* `RemoveAndRelease` returns and never inside its
callback. By then the archive has committed, and it must not be reversible by a git
problem: every failure is logged and reported, and the task stays `archived`. The
corollary is load-bearing in the other direction too — a dirty worktree refused without
`force` never reaches the branch step at all.

### 8. Two plain `bool` keys, and a warning rather than a validation error

*2026-08-16.* `Load` unmarshals into `Default()`, so an absent key keeps its default and
an explicit `false` restores the pre-008 behaviour exactly — no tri-state pointer is
needed. A bool has no invalid value, so `validate()` gains nothing.

The *pair* does: `delete_remote_branch_on_archive: true` with
`delete_empty_branch_on_archive: false` asks for something that cannot happen, since
the remote leg runs only after a local delete. That is a new `Config.Warnings()`,
logged at startup and on any reload that changes it — separate from `validate()` on
purpose, because a key that is merely unreachable is not an invalid one, and failing
the load over it would revert every unrelated edit in the same save.

### 9. `gitx.RemoteTimeout`, a third timeout constant

*2026-08-16.* Neither existing one fits a network call. `QueryTimeout` (30 s) is sized
for a local object-database read and would fail a push over a slow link; `WorktreeTimeout`
(5 min) would park the archive's caller behind an unreachable host. 60 s, and a remote
that does not answer inside it is logged while the branch stays deleted locally.

## Tasks

- [x] **008.1** — `internal/worktree/branch_delete.go`: `DeleteEmptyBranch`, the
      outcome vocabulary, upstream resolution and `push --delete`; `gitx.RemoteTimeout`;
      `testrepo.InitBare`. ✓ 2026-08-16
- [x] **008.2** — `internal/config`: both keys, `Default()`, `Config.Warnings()`, the
      generated `config.yaml` comments, and the daemon logging them at start and on
      reload. ✓ 2026-08-16
- [x] **008.3** — `internal/taskrun`: `Archive` returns a `worktree.BranchOutcome` and
      runs the branch step after `RemoveAndRelease`. ✓ 2026-08-16
- [x] **008.4** — `internal/api`: archive's own write path and response shape, the
      pre-cascade sweep in `handleProjectDelete`, both keys on `GET /v1/config`.
      ✓ 2026-08-16
- [x] **008.5** — `internal/apiclient`: `Archive`'s second return, `BranchOutcome` and
      its `Summary()`; the TUI status line and the daemon view's config lines.
      ✓ 2026-08-16
- [x] **008.6** — Tests: worktree table (empty, commits, base moved, base gone, checked
      out elsewhere, ref-hierarchy neighbour, the three remote legs), taskrun, api,
      config. ✓ 2026-08-16
- [x] **008.7** — `scripts/m2-gate.sh` scenario 6; spec §6/§10/§12.3/§13.2/§18 and the
      user docs. ✓ 2026-08-16

## Out of scope

- **`vincent gc` and `vincent doctor --fix`** — decision 4.
- **Pushing, or any other remote write.** The one remote call this adds is a delete of a
  ref vincent's own configuration points at, behind a key that is off by default.
- **A per-archive flag beside `force`.** This is a standing policy, not a per-action
  judgement, and the project-delete path has no human to ask. (The issue's own rejected
  alternative.)
- **Reclaiming branches for tasks archived before this shipped.** They keep their
  branches; only `DELETE /v1/projects/{id}?force` reaches an already-archived row, and
  only because the cascade is about to erase its name.

## Verification

- `go test ./...` green (2026-08-16, macOS); `go test -race ./internal/taskrun
  ./internal/worktree ./internal/api` green.
- `go tool golangci-lint run ./...` clean for `GOOS=windows`, `darwin` and `linux`.
- `VINCENT_GATE_SCENARIO=6 ./scripts/m2-gate.sh` passes (2026-08-16, macOS): a
  fake-agent task that commits nothing loses its branch and the response says
  `deleted`; one that commits keeps its branch at the same commit and the response says
  `has_commits`; `delete_empty_branch_on_archive: false` hot-reloads and the branch
  survives.
