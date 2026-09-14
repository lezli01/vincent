# 099 — Fresh base: fast-forward the local base branch and show what a task started from

**Status:** ✅ done (6/6)
**Opened:** 2026-09-14

*Issue [#430](https://github.com/lezli01/vincent/issues/430).*

Task [056](056-fetch-base-branch.md) made a task's worktree start from its base
branch's fetched upstream tip. It left four gaps. The human's local base branch
never moved, so `git log master` in their checkout drifted behind what tasks
built on. `base_sha` was stored but kept off the task DTO (056 decision 4), so
no client could show what a task started from. A failed fetch fell back to the
local ref with one log line and nothing on the task. And `POST /v1/chats` passed
`fetch=true` literally, so `fetch_base_branch: false` never reached a chat.

This task closes all four. The issue names its two reversals itself — §10's
"nothing local is mutated" bullet and 056 decision 4 — so neither is a silent
relitigation. It keeps 056 decisions 1, 2, 3, 5 and 6 (fetch at first admission,
no guessed `origin`, mandatory `--no-track`, the narrow `-D`, one global key),
§10's "a base fetch never blocks" and §26's "no step fails for a network
reason". Scope is the moment a worktree is **first created**: an existing
worktree is never refreshed on retry, resume or a later step.

## Decisions

**1. "Dirty" is the T1.5/T1.6 rule.** *(2026-09-14)*

A checkout holding `<base>` is clean only when `git status --porcelain` is empty:
tracked edits, staged changes and untracked non-ignored files all skip the
fast-forward. That is `Manager.IsDirty`, unchanged. The known cost: a stray
scratch file in the human's checkout means it never moves, and every task records
that skip.

**2. No hooks: plumbing, not porcelain.** *(2026-09-14)*

A checked-out working tree moves with `git read-tree -m -u <old> <new>` in that
worktree, then a compare-and-swap `git update-ref refs/heads/<base> <new> <old>`.
Never `merge --ff-only`, `pull` or `checkout`, so no post-merge or post-checkout
hook of the human's runs in their checkout during an admission, under the
per-repo lock other admissions wait on. `reference-transaction` is the one
exception — git fires it on every ref update, including the `worktree add -b`
vincent already ran — and the spec says so rather than claiming "no hooks at all".

**3. Chats record the outcome too, and a handoff copies it.** *(2026-09-14)*

A chat stores the same record beside its `base_sha` and serves it on the chat
DTO. `POST /v1/chats/{id}/handoff` copies it to the task with `base_sha`, so a
handed-off task shows what it started from. The TUI chat workspace does not
change.

**4. The order and refusal rules of the fast-forward.** *(2026-09-14)*

It runs in `Manager.create` only when `fetchBase` resolved a commit, inside the
lock `create` already holds, before `git worktree add`. With `old =
refs/heads/<base>` and `new` the fetched SHA:

- `old == new` → `up_to_date`.
- `new` is an ancestor of `old` → skipped, `local_ahead`. Tested first, because an
  ahead branch also fails the next test and "ahead" is the narrower answer.
- `old` is not an ancestor of `new` → skipped, `diverged`.
- `<base>` checked out in some worktree (`branchCheckedOut`, main checkout
  included): an operation in progress (`InProgressOp`) → skipped,
  `checkout_busy`, asked before dirtiness because a stopped merge is almost
  always dirty too and "busy" is the more useful thing to say; `IsDirty` →
  skipped, `checkout_dirty`; otherwise `read-tree`, then the compare-and-swap. If
  the swap loses a race, `read-tree -m -u new old` switches the tree back — a
  moved tree under an unmoved ref reads as every upstream change reverted and
  staged — and the result is skipped, `error`. A `read-tree` that refuses (a file
  changed after the check, a file Windows holds locked) is skipped, `error`, with
  nothing else run.
- Not checked out: the compare-and-swap alone; a lost race is skipped, `error`.
- Success → `advanced`, with the checkout path when a working tree moved.

With no fetched commit (key off, `no_upstream`, fetch `error`) the result is
`not_attempted`. None of these blocks, fails or adds a `block_reason`; an
`error` is recorded, never returned from `create`; the task always starts from
`new`. The values are snake_case results on the `Fetch*` convention, not
`Reason*` failures. `merge-base --is-ancestor` gets its own wrapper
(`ancestorOf`) that keeps exit 1 ("no") apart from other failures, because
`isAncestor` folds both into false and would turn an unreadable object into a
confident `diverged`.

**5. Carrier: stored on the row, not a task event.** *(2026-09-14)*

One nullable TEXT column, `base_refresh`, holding JSON on both `tasks` and
`chats` (migration `0031_base_refresh.sql`), written in the same statement as
`worktree_path` and `base_sha` — `Store.ClaimTaskWorktree` and
`Store.ClaimChatWorktree`. It survives a restart, sits on the row the DTO already
renders, and is untouched by events retention. It is written only where
`base_sha` is, so a pre-existing worktree is never re-recorded. NULL means no
worktree was created since the migration; `disabled` is recorded explicitly, so
"key off" and "predates the record" stay distinct. A malformed value decodes to
nil rather than failing the scan: the record is display-only, and it must never
make a task or chat unreadable. `SetTaskProgress` and `SetChatWorktree` keep their
signatures, and the full-row writes carry the column through unchanged.

`store.BaseFetch` and `store.BaseFastForward` mirror `worktree.FetchOutcome` and
`worktree.FastForwardOutcome` field for field, so callers convert with a plain
type conversion and a drift on either side is a compile error. The store still
does not import `internal/worktree`.

**6. Wire shape.** *(2026-09-14)*

The task DTO and the chat DTO both carry `base_sha` (`omitempty`) and
`base_refresh` (`null` when absent, never omitted):

```json
"base_refresh": {
  "fetch": {"result": "fetched|no_upstream|error|disabled", "remote": "origin", "ref": "refs/heads/master", "error": "…"},
  "fast_forward": {"result": "advanced|up_to_date|skipped|not_attempted", "reason": "diverged|local_ahead|checkout_dirty|checkout_busy|error", "worktree": "/path", "error": "…"}
}
```

`apiclient` mirrors both, and puts the rendering on the type —
`BaseRefresh.Warning()` and `TaskDetail.BaseDisplay()` — so the TUI's
`base` / `base refresh` rows and `task show`'s `base` / `refresh` rows cannot
drift. Only a degraded refresh (fetch `error`, fast-forward `skipped`) gets a
row; a healthy one says nothing. The MCP task tools replay the handlers and pick
the fields up with no change.

A pull-request task (task 064's second creation mode) records NULL: the fetch it
runs is the head's, not the base's, and no base fast-forward is attempted, so any
record would claim something that did not happen.

**7. Chats read the key per creation.** *(2026-09-14)*

`handleChatCreate` reads `s.deps.Config().FetchBaseBranch` for each request, the
way `ensureWorktree` does per admission. With `false`, no fetch runs, no
`base_sha` is recorded, and the record reads `disabled` / `not_attempted`.

**8. Logging stays out of `internal/worktree`.** *(2026-09-14)*

`taskrun`'s `logBaseRefresh` (formerly `logBaseFetch`) covers the fast-forward:
`advanced` at Info, since it changed something in the human's repository;
`up_to_date` and `local_ahead` at Debug, the ordinary state of a developer's
checkout; the other skips at Info with the reason; `error` at Warn with git's
message. Fetch logging is unchanged. The chat handler logs only a failed fetch
and a fast-forward `error`, at Warn — everything else is already on the row.

## Work

- [x] **099.1 — `internal/worktree`: `fastForwardBase`, `FastForwardOutcome`, `Created.FastForward`.** Built on `branchCheckedOut`, `InProgressOp` and `IsDirty`; `createPull` leaves the zero value. ✓ 2026-09-14
- [x] **099.2 — `internal/store`: migration 0031, `BaseRefresh` on tasks and chats, `ClaimTaskWorktree` / `ClaimChatWorktree`, the handoff copy.** ✓ 2026-09-14
- [x] **099.3 — `internal/apiclient` wire fields and the TUI / CLI `base` and `base refresh` rows.** ✓ 2026-09-14
- [x] **099.4 — `internal/taskrun`: persist the record at admission, log the fast-forward.** Depends: 099.1, 099.2. ✓ 2026-09-14
- [x] **099.5 — `internal/api`: `base_sha` and `base_refresh` on the task and chat DTOs, chats honour `fetch_base_branch`, handoff copies the record.** Depends: 099.1, 099.2, 099.3. ✓ 2026-09-14
- [x] **099.6 — Spec amendments (§5.3, §5.5, §10, §12.3, §13.2, §14), the configuration, API and CLI reference pages, 056's dated notes, `CHANGELOG.md`.** ✓ 2026-09-14

## What the tests prove

Hermetic throughout, on `internal/testrepo`'s real bare remote. `internal/worktree`
covers a clean checked-out base advancing ref, index and files together; a
tracked edit and an untracked-only file each leaving ref, index and tree
byte-identical as `checkout_dirty`; a stopped merge as `checkout_busy`; a base
not checked out advancing its ref alone; `diverged`, `local_ahead` and
`up_to_date`; `autoSetupMerge = always` still leaving the task branch without
upstream config after an advance; a failed fetch, the key off and no upstream all
`not_attempted` with nothing moved; a lost compare-and-swap race (through an
unexported test hook, since `gitx.Git` has no fake) ending `error` with the
checkout's HEAD and tree still agreeing; and the pull-request mode's zero value.
056's `TestCreateFetchLeavesTheLocalBaseAlone` asserted the rule this reverses
and is gone; `fetch_test.go`'s fixture keeps the local base behind with an
untracked file, which is now a skipped fast-forward.
`internal/store` round-trips the record on both tables, across a reopen, as NULL
for a pre-0031 row, through `UpdateTask` and `SetChatWorktree`, through
`HandoffChat`, and past a malformed value. `internal/taskrun` admits a task against
a fast-forwardable base, one against an unreachable remote (admitted, not
blocked, `fetch.result=error`), and a `fan_out` lane (`no_upstream`, nothing
moved). `internal/api` serves both fields, proves a chat with the key off fetches
nothing and records `disabled`, and that a handoff copies the record;
`internal/apiclient`'s live test decodes every field from the real handlers.

No new gate script, for 056's reason: the behaviour is observable from unit tests,
and a network-remote scenario is expensive to express in the sh∩pwsh
intersection.
