# 168 — A diff pane in the chat with per-hunk accept and reject

**Status:** 🔄 in progress (9/11) · **Opened:** 2026-11-20

Part of the chat UX epic (#1009); issue #1018. A chat had no diff view and no
diff route: the only chat diff was the export's `diff=true`, which shares the
task diff's tracked-changes-only helper. This gives a chat a diff of its own —
untracked files included, since the chat's base or since its last turn — and a
review of it one hunk at a time: reject reverts the hunk in the working tree
and tells the agent, accept marks it reviewed.

It builds on task 167's per-turn checkpoint (`gitx.Snapshot`,
`chat_turns.checkpoint_sha`) and task 164's daemon-held draft, and respects
task 119 (linked chats), task 140 (issue chats), task 160 (conversation
focus) and task 063 decision 2 (chat writes stay off MCP).

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **168.1** `internal/gitx`: `SnapshotTree` (the tree half of `Snapshot`,
  sharing its temporary index; no commit, no ref), `DiffTrees` (structured,
  `--raw -z` for paths and `-p` for text, every output option pinned),
  `DiffTreesStat`, and `ReverseHunk` (a one-hunk `git apply -R`, working tree
  only), with tests for modified, deleted, new untracked, nested untracked,
  ignored and CRLF files, a hunk in a committed change, a stale hunk and a
  conflicting one, and an untouched index and HEAD. ✓ 2026-11-20
- [x] **168.2** Migration `0067_chat_hunk_reviews.sql`, the accepted-mark
  CRUD, and `RecordChatHunkReject` — the mark's deletion and the draft note in
  one transaction, through the one draft write `UpdateChatDraft` uses.
  ✓ 2026-11-20
- [x] **168.3** `GET /v1/chats/{id}/diff?scope=base|turn` and
  `GET /v1/chats/{id}/diff/stat`, the directory and base resolution factored
  out of `chatExportDiff`, the `apiclient` types and methods, and
  `vincent chat diff [--scope] [--stat]`. ✓ 2026-11-20
- [x] **168.4** `POST …/diff/reject`, `…/accept` and `…/unaccept`, the draft
  note, the `chat_hunk_stale`, `chat_hunk_conflict`,
  `chat_hunk_not_rejectable`, `chat_turn_live` and `chat_diff_unavailable`
  codes, `chatrun.Runner.WhileNoTurn`, the MCP exclusions, and
  `vincent chat diff reject` / `accept [--undo]`. ✓ 2026-11-20
- [x] **168.5** The hunk level in the shared renderer (`diffhunk.go`): hunks
  parsed from `@@` lines, a hunk cursor, marks, and `]`/`[` on the task
  workspace's Diff tab. ✓ 2026-11-20
- [x] **168.6** The chat diff pane: the `chat_diff` operation (`alt+d`), the
  palette row, the fixed pane keys, the scope switch, reject and accept, and
  the composer adopting a reject's draft. ✓ 2026-11-20
- [x] **168.7** The Details pane's `changes` row, in the open chat and the
  chats board's preview. ✓ 2026-11-20
- [x] **168.8** `scripts/168-gate.sh`, executable, wired into `gate-group`
  (group 2) on all three platforms, with the fake agent's
  `FAKEAGENT_EDIT_LINES` so one turn leaves two hunks. ✓ 2026-11-20
- [x] **168.9** Spec amendments (§5.5, §12.1, §12.3, §13.2, §13.4, §14, §15),
  the API, CLI, configuration and TUI references, `features.md` and the
  changelog. ✓ 2026-11-20
- [ ] **168.10** Side-by-side view. Recorded, not built (decision 4).
- [ ] **168.11** Screenshots: seed a chat with uncommitted changes in
  `scripts/screenshots.sh` and add a `tui-chat-diff` tape; re-capture
  `tui-diff` for the hunk marker. Needs VHS, as 164.6 did.

## Decisions

Decisions 1–4 were settled with the author on 2026-11-20, in the brief for
#1018. The rest were made in that brief and stand unless the author overrules
them.

1. **The chat diff includes untracked files.** A deliberate, chat-only
   departure from the phase 1 "untracked files excluded" limitation; the task
   diff and the chat export's `diff=true` keep the phase 1 rule. The chat diff
   is tree to tree: the daemon snapshots the working tree through the
   checkpoint machinery (temporary index seeded from the real one,
   `add -A --ignore-errors`, `write-tree`; no commit and no ref) and diffs
   that tree against the scope's start. New files appear in both scopes, and
   rejecting a new file's hunk removes the file. Ignored files stay out, as
   in checkpoints (task 167 decision 5).
2. **All review state lives in the daemon.** Accepted marks are rows keyed by
   chat, path and a hash of the hunk's content, so a hunk whose content
   changes loses its mark and a mark survives a restart or another client.
   The reject route itself prepends the rejection note to the chat's
   daemon-held draft (task 164). The note is then ordinary composer text:
   visible, editable, deletable, and sent as part of the next message. An API
   or CLI reject leaves the same note. There is no separate note field and no
   send-path rule.
3. **The "Diff tab" is a pane swap on a key, not a tab strip.** A rebindable
   `chat_diff` operation, default `alt+d`, and a palette entry swap the
   conversation pane for the diff. The diff then holds the keyboard as
   conversation focus does; `esc` returns to the conversation and the draft
   is untouched. Inside it: `]`/`[` next and previous hunk, `x` reject, `a`
   accept, `s` switches scope, and the fold, scroll and pan keys keep
   working. The issue may call the feature "the Diff tab"; the spec describes
   a pane.
4. **Hunks now, split view later.** The hunk level is added to the shared
   renderer, so the task workspace's Diff tab gains hunk navigation and
   nothing else: no reject, no accept there. The view is unified only;
   side-by-side is 168.10. Both scopes ship in this task.
   *Implementation note (2026-11-20):* on the task's Diff tab `]`/`[` had been
   `tab`'s aliases; they walk the hunks there now, and `tab`/`shift+tab` and
   the digits still switch tabs. The board's preview keeps them as its
   Output/Diff toggle.
5. **Scopes.** `base` is the merge-base with the chat's recorded base (a
   linked chat uses its task's base and worktree, as the export does). `turn`
   is the `checkpoint_sha` of the chat's latest turn — the state before that
   turn's agent started. With no turn carrying a checkpoint the `turn` scope
   answers `409` with a reason and the TUI shows it as an empty state; it is
   never faked from the base.
6. **Reject reverse-applies one hunk to the working tree only.** The daemon
   regenerates the scope's diff, finds the hunk by its content hash, builds a
   one-hunk patch and runs `git apply -R` in the worktree. It never touches
   the index, HEAD or history, so rejecting a hunk the agent already
   committed leaves an uncommitted reversal. A hash that no longer matches is
   `409 chat_hunk_stale` (the TUI refetches); a patch git will not apply is
   `409 chat_hunk_conflict`. Binary files, renames and mode-only changes are
   listed but not rejectable per hunk (`409 chat_hunk_not_rejectable`), and
   the pane says so.
7. **Reject and accept refuse while a turn is live** (`running`,
   `awaiting_input`, or a queue draining) with `409 chat_turn_live`, as a
   guard and not a §5.5 transition, matching task 167 decision 8. Reading the
   diff is always allowed where a directory exists. Terminal chats can read
   when the directory remains and can never reject; with no directory the
   read answers the export's reasons.
   *Implementation note (2026-11-20):* the guard is unraced, not a state
   check alone. `chatrun.Runner.WhileNoTurn` holds a per-chat start lock that
   `SubmitWith` and a queue resume also take, so a send arriving during the
   `git apply` waits for it instead of starting an agent in the same files.
8. **The note format.** One block at the top of the draft, headed "The user
   reverted these hunks; do not reapply them:", then one line per hunk with
   path and `@@` header. A second reject extends the existing block in place
   when the draft still starts with it; otherwise a new block is prepended. A
   draft that would exceed the 64 KiB cap still gets the reject, and the
   response carries a warning that the note was not added.
9. **Accept is only a mark.** It changes nothing in git. `a` on an accepted
   hunk clears the mark. Accepted hunks are drawn dimmed with a check glyph
   (a glyph, not colour alone), and the file row shows reviewed/total. Marks
   are deleted with the chat row (`ON DELETE CASCADE`).
10. **The Details `changes` row** shows the file count and `+/-` since the
    base, from a dedicated stat call (`GET /v1/chats/{id}/diff/stat`) fetched
    with the chat and with the board's preview, never from the event stream.
    It refreshes when a turn ends and after a reject.
11. **Wire shape.** The diff is served structured (files, each with path,
    `+/-` counts and hunks carrying a stable hash, header, body and accepted
    flag) so no client re-derives hunk identity. `apiclient` owns the client
    types. The routes are not MCP tools (task 063 decision 2 keeps chat
    writes off MCP); they are in `internal/mcp`'s exclusion list.
    *Implementation note (2026-11-20):* a hunk's hash leaves the `@@` line
    numbers out, so rejecting one hunk does not invalidate the ones below it;
    identical content repeated in one file is told apart by ordinal. The CLI
    accepts a unique prefix of a hash and resolves it itself — the daemon
    takes full hashes only.
