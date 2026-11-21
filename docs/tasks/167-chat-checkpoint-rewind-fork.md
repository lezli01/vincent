# 167 — Checkpoint a chat's worktree every turn, with rewind and fork

**Status:** 🔄 in progress (9/10) · **Opened:** 2026-11-20

Part of the chat UX epic (#1009); issue #1017. Nothing in vincent
checkpointed, rewound or forked a chat: an agent turn that went wrong left the
worktree and the conversation where it left them. This checkpoints the
worktree before every turn, then adds rewinding the code, the conversation or
both to a checkpoint, and forking a new free chat from one.

It interacts with task 119 (linked chats), task 140 (issue chats), task 156
(the message queue), task 162 (per-turn settings) and §12.4 crash-first; the
decisions below say how each is respected.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **167.1** `internal/gitx`: `Snapshot` (temporary index, `read-tree
  HEAD`/`add -A`/`write-tree`/`commit-tree`), `Restore` (reset to the
  snapshot's parent, `clean -fd`, `read-tree -u --reset` of the snapshot's
  tree, mixed reset), `UpdateRef` and `DeleteRefs`, with tests for modified,
  deleted, new, nested untracked and ignored files, an untouched index, and
  tree reuse on an unchanged worktree. ✓ 2026-11-20
- [x] **167.2** Migration `0066_chat_checkpoints.sql` (`checkpoint_sha`,
  `head_before`, `head_after` on `chat_turns`), a checkpoint written as
  `refs/vincent/<install id>/chats/<id>/turn-<seq>` and recorded on the row before
  `adapter.Start`, `head_after` captured when the process ends, and the
  `checkpoint_failed` reason. Tested on all four dialects, and a failed
  checkpoint never spawns. ✓ 2026-11-20
- [x] **167.3** Code rewind: `POST /v1/chats/{id}/rewind` (`mode: code`),
  `vincent chat rewind`, the MCP tool, the `rewound-<k>` ref of the
  pre-rewind state, the shared-worktree rule (decision 3) as
  `409 chat_rewind_foreign_commits`, and the live-turn `409`. The turn DTO
  gains the checkpoint fields here. ✓ 2026-11-21
- [x] **167.4** The claude resume-at-point capability
  (`--resume-session-at --fork-session`, detected, with a captured fixture),
  the per-turn `resume_anchor` the claude parser records, and the seeded
  fallback (`RunSpec.Preamble`) for codex, cursor and copilot.
  Depends: 167.3. ✓ 2026-11-21
- [x] **167.5** Conversation and both rewinds; `rewound_at`/`rewind_id`/
  `seeded` on `chat_turns`; rewound turns hidden by default in the TUI, CLI
  transcript and export, still in cost totals; the rewound prompt back in the
  task 164 draft. Depends: 167.4. ✓ 2026-11-21
- [x] **167.6** Fork: a new free chat on its own branch from the
  checkpoint's HEAD, the snapshot's dirty state applied, the source's agent
  and settings copied. Depends: 167.4. ✓ 2026-11-21
- [x] **167.7** TUI: the `rewind` keymap operation, the "rewind…" and "fork
  from turn…" palette entries, the turn picker with disabled options and
  their reasons, and the rewound-turn toggle. ✓ 2026-11-21
- [x] **167.8** Pruning: delete `refs/vincent/<install id>/chats/<id>/` with the chat
  (permanent delete, archive that removes the branch, `delete_branch=true`;
  not on hand-off), and a daemon sweep for refs whose chat row is gone.
  Measure disk cost (see below). ✓ 2026-11-21
- [x] **167.9** `scripts/167-gate.sh`, executable, wired into `gate-group`
  in the lightest group. ✓ 2026-11-21
- [~] **167.10** Remaining spec amendments (§9.x capability, §10 refs and
  pruning, §13.2 endpoints, §15 key), the API/CLI/TUI references,
  `features.md` and screenshots. Everything but the screenshots ✓ 2026-11-21; see Delivery notes.

## Decisions

Decisions 1–4 were settled with the author on 2026-11-20, in the brief for
#1017. The rest were made in that brief and stand unless the author
overrules them.

1. **Conversation rewind: claude resumes natively, the others are seeded.**
   claude resumes with `--resume <session> --resume-session-at <assistant
   message uuid> --fork-session`. The flag is undocumented (in the 2.1.x
   binary, not in `--help`), so it sits behind a detected capability in the
   `CanChangeOnResume` pattern (§9.1), with a captured fixture; a claude
   without it falls back to seeding. codex, cursor and copilot cannot resume
   from a point (`codex exec fork` forks the whole session), so they start a
   fresh session seeded with turns 1..N-1 — each prompt and its final reply,
   bounded like a linked chat's opening context — through `RunSpec.Preamble`,
   never fused into the message. The turn, the API and the TUI say "seeded,
   not resumed"; it is never faked.
   *Amended 2026-11-21 (task 171 decision 3, issue #1020, with the author):*
   every seeded context — a seeded rewind, a same-agent seeded fork and a
   cross-agent fork — now also carries, under each turn's prompt, a `<tools>`
   block of one `chatexport.ToolSummary` line per tool call, read from the
   turn's normalized transcript. Tool output is still excluded, the bound is
   unchanged, and over it whole tool blocks go before any prompt or reply.
2. **Code rewind resets the branch and keeps a ref.** The branch moves to
   the checkpoint's HEAD and the index and worktree are restored exactly,
   untracked files included. The pre-rewind tip and a snapshot of its dirty
   state are saved first as `refs/vincent/<install id>/chats/<id>/rewound-<k>`, so a later
   rewind can go forward to it.
3. **Shared worktrees refuse unless only this chat has committed.** A code
   rewind to turn N needs `head_before(k+1) == head_after(k)` for every
   k ≥ N and the current HEAD equal to the last turn's `head_after`;
   otherwise `409 chat_rewind_foreign_commits`, with no force. Free chats run
   the same check and refuse too — one rule everywhere. Conversation-only
   rewind and fork stay available.
4. **TUI: a rebindable `rewind` key and the palette, not `esc esc`.** `esc`
   is the chat workspace's fixed "back" key, so a double `esc` would leave on
   the first press.
5. **Gitignored files are not snapshotted.** This matches git.
6. **The snapshot uses a temporary index**, beating a second tree recording
   the index. `GIT_INDEX_FILE` points under the data directory; the real
   index and HEAD are untouched. The staged/unstaged split is not kept: a
   restore resets the index to the checkpoint's HEAD and leaves the
   snapshot's differences unstaged. Keeping a second tree for the index would
   double the work per turn to preserve a distinction agents rarely make.
   *Amended 2026-11-20 (review F2):* the temporary index is seeded with a
   copy of the worktree's real index (its mtime kept, for git's racy-entry
   check), falling back to `read-tree HEAD` without one. The tree is the same;
   a read-tree index has no stat data, so `add -A` rehashed every tracked
   file per turn (0.79 s against 0.02 s on this repository).
7. **Crash-first.** The ref is written and its SHA stored on the turn row
   before `adapter.Start`; a failure fails the turn `checkpoint_failed` and
   the agent never runs unprotected. Recovery needs nothing new. A drained
   queued turn is checkpointed like any other. *Implementation note
   (2026-11-20, 167.2):* the brief named `internal/reasons` as the home of
   `checkpoint_failed`, but that package covers task and step reasons only
   and its doc says chat reasons stay with `internal/chatrun`; the constant
   lives beside the other turn reasons there, and is documented with them in
   the API reference. A stop that lands while git runs fails `canceled`, not
   `checkpoint_failed`; a daemon shutdown there ends it `interrupted`, as it
   would a live turn (review F4).
   *Amended 2026-11-20 (review F1, settled with the author):* a file git
   cannot read is not a checkpoint failure. `add -A --ignore-errors` leaves
   it out, `gitx.Snapshot` reports it in `Skipped`, and the turn runs with a
   `vincent.checkpoint_skipped` transcript record naming it — beating
   "refuse and name the file", which would stop every later turn of the chat
   until a human fixed it. 167.3 must therefore not let `Restore`'s
   `clean -fd` delete a path the checkpoint skipped.
   *Amended 2026-11-21 (PR #1044 review F2):* the clean's `-e` spares only an
   untracked path. A skipped path that HEAD, the target's `head_before` or the
   checkpoint's tree tracks is rewritten by `reset --hard` or `read-tree -u`
   — and the pre-rewind save holds the index's blob for it, not the edit —
   so `gitx.Restore` refuses such a restore (`UnsavedPathsError`) and a code
   rewind checks first, refusing with `409 chat_rewind_unsaved_files` naming
   the paths before any ref is written or file moved. Refusing beats moving
   the files aside: it keeps the rule "a rewind never destroys what it could
   not save" without a second, unsaved copy to lose on a crash.
8. **Rewind and fork refuse while a turn is live** (`running`,
   `awaiting_input`, or a queue draining) with `409`, as a guard, not a §5.5
   transition. Not offered on terminal chats.
9. **Rewound turns are kept**, marked `rewound_at` and grouped by
   `rewind_id`, hidden by default with a toggle, never deleted, and counted in
   §17 costs. New turns continue `seq`. The rewound prompt returns to the
   composer through task 164's daemon-held draft.
10. **`session_id` after a conversation rewind** is the forked or seeded
    session's, written by the rewind (claude) or the next turn (seeded).
    *Amended 2026-11-21 (#1041, brief decision A, settled with the author):*
    read as "decided by the rewind, made real by the next turn". The rewind
    runs no process: it records `chats.resume_at` (claude with the
    capability) or `pending_preamble` (seeded), and the next turn runs
    `--resume <session> --resume-session-at <uuid> --fork-session` or starts
    seeded, recording the session id it reports. claude can fork a session
    only by running a process, so this is how rewind stays an MCP tool for
    both modes, not code alone.
11. **Fork is always a new free chat** on its own branch, cut from the
    checkpoint's HEAD with the snapshot's dirty state applied. It does not
    inherit a link, an issue or a hold on a worktree; it copies agent, model,
    effort and permission mode, with task 162's `restricted` checks.
    *Amended 2026-11-21 (task 171 decision 2, issue #1020, with the author):*
    a fork may take an `agent` other than its source's. Such a fork is always
    seeded (no session crosses adapters), its model and effort are the
    request's or that adapter's defaults, never the source's, and the
    source's permission mode is checked against the new adapter unless the
    request passes `permission_mode`. "Copies agent" now holds only when no
    other agent is asked for.
12. **Pruning.** The refs go with the chat (permanent delete, an archive that
    removes the branch, `delete_branch=true`), survive a hand-off, and a
    daemon sweep deletes refs whose chat row no longer exists.
    *Amended 2026-11-21 (PR #1044 review F4, settled with the author):* the
    refs live under `refs/vincent/<install id>/chats/<id>/`, scoped by a
    per-database installation id (migration 0072). Unscoped, the sweep
    deleted the refs of every other installation registered on the same
    repository — their chat ids have no row here — and two installations'
    equal chat ids shared refs. Chosen over dropping the sweep (a crashed
    delete would leak refs) and over documenting the limit. Refs a master
    build wrote under the unscoped namespace before this are neither read
    nor swept; the feature was unreleased, so there is no migration of them.
13. **Disk cost is measured, not assumed.** git deduplicates objects, so a
    checkpoint's only new storage is its commit object, the trees on the
    changed paths, and the blobs of changed files not already stored.
    `TestSnapshotUnchangedReusesTree` proves an unchanged worktree reuses
    the previous checkpoint's tree. The measured number is 167.8's to record.
    *Measured 2026-11-21 (167.8), on this repository:* a checkpoint of an
    unchanged worktree adds one commit object, ~150 bytes loose; one modified
    file (a 590-line Go file) and one new file added six objects, ~12 KB
    loose, almost all of it the modified file's blob. Recorded in spec §10.

## Delivery notes (2026-11-21, #1041)

- 167.3–167.10 landed together in one pull request (brief decision B).
  Task 063 decision 2 is amended there: `chat_rewind` and `chat_fork` are the
  only chat routes that are MCP tools (brief decision A).
- Not done in that pull request: re-capturing `docs/assets/tui-*.png` with
  `scripts/screenshots.sh`, and seeding a rewound turn for the picker and
  toggle shots. The TUI and docs change without new pictures until then.
