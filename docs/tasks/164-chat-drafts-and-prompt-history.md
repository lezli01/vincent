# 164 — Keep chat drafts in the daemon and recall earlier prompts

**Status:** 🔄 in progress (5/6) · **Opened:** 2026-11-20

Part of the chat UX epic (#1009); closes issue #1012. Opening a chat cleared
the composer, so a half-written message was lost to a chat switch, a TUI
restart or another client, and there was no way to reuse a prompt already
sent. This keeps each chat's draft and one stash slot in the daemon, recalls a
chat's earlier prompts on `↑`, and adds a picker over every prompt the
project's chats have sent.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **164.1** Store: migration `0063_chat_drafts.sql` (`chats.draft`,
  `draft_updated_at`, `stash`), `SetChatDraft`/`SetChatStash`/
  `UpdateChatDraft` with the 64 KiB cap, `ListChatPrompts`, the
  `chat.draft_changed` event, and clearing both on archive, close and
  hand-off. ✓ 2026-11-20
- [x] **164.2** API and client: `GET`/`PUT /v1/chats/{id}/draft`,
  `GET /v1/chats/prompts`, `draft`/`stash` on the chat read and `has_draft` on
  every chat body, the routes on `internal/mcp`'s exclusion list, and
  `apiclient` methods with a live test. ✓ 2026-11-20
- [x] **164.3** Keymap: the `stash` (`alt+s`) and `prompt_history` (`alt+h`)
  operations, the recall `↓` row, and the amended `↑` meaning. ✓ 2026-11-20
- [x] **164.4** TUI: restore on open, debounced save and flush on leaving or
  quitting, clear on an accepted send, `↑`/`↓` recall, the stash, the
  history picker, and the board's `✎ draft` mark. ✓ 2026-11-20
- [x] **164.5** Docs and gate: §5.5, §13.2, §13.3, §14 and §15 amendments, the
  API, configuration and TUI references, `features.md`, `CHANGELOG.md`, and a
  draft round trip across a daemon restart in `m14`. ✓ 2026-11-20
- [ ] **164.6** Re-capture the chats board and chat screenshots with
  `scripts/screenshots.sh` (VHS), seeding one chat with a draft so the board
  shows `✎ draft`. Not done in the implementing PR: it needs VHS, which the
  implementing environment did not run.

## Decisions

Confirmed by the author on 2026-11-20, in the brief for #1012.

1. **The daemon owns the draft and one stash slot per chat**, as columns on
   `chats`. A draft and a stash survive switching chats, restarting the TUI
   and moving to another client. Columns rather than a table: one of each per
   chat, read with it.
2. **Stash is `alt+s`, not `ctrl+s`.** `ctrl+s` stays `steer` (task 156
   decision 5). On a non-empty composer `stash` stores the draft and clears
   the composer; on an empty one it pops the stash. A stash on a full slot
   replaces it — one slot, as the issue asked.
3. **Prompt search is `alt+h`, not `ctrl+r`.** `ctrl+r` stays the chat's
   verbosity cycle. `enter` inserts the chosen prompt without sending it;
   `esc` leaves the draft untouched.
4. **`↑` on an empty composer edits the queue first, then recalls.** With
   queued messages it edits the newest, as task 156 decision 5 does; only
   with none does it walk this chat's prompts, newest first. `↓` walks back
   and ends on an empty composer. Recall copies; any edit ends it.
5. **Search covers archived chats too.**
6. **Search reuses task 158's trigram index.** Matching is by substring. A
   query under three characters is answered by recency rather than refused,
   and identical prompt texts collapse to their newest occurrence.

Implementation choices recorded here:

7. **Terminal transitions clear the draft and stash**, in the same
   transaction that discards the queue (task 156 decision 6): a finished chat
   can never send them. A terminal chat refuses a draft write with `409`.
8. **The event carries no text.** `chat.draft_changed` carries `has_draft`,
   `has_stash` and `draft_updated_at`; an open chat re-reads the draft. The
   TUI takes a remote draft only while its composer holds exactly the text it
   last wrote and no save is in flight, so an echo of its own write — or of an
   older one — cannot reset what was typed since. Saves are serialized, one
   PUT at a time, so an older one never lands last. Reads are ordered too
   (review F1): the chat read carries `draft_updated_at`, and the TUI ignores
   a read stamped before the `updated_at` its last acknowledged PUT returned
   — a reload that read the row before a send's PUT and answers after it
   would otherwise put the sent prompt back into the composer.
9. **The size cap is the send route's**, 64 KiB: `POST /v1/chats/{id}/send`
   reads an ordinary request body, so a stored draft is always one the daemon
   would accept as a message.
10. **The stored draft is cleared by the client**, on the send's acceptance,
    not by the send route: a message sent from the CLI must not wipe a draft
    being typed in the TUI.
11. **No CLI command.** `vincent chat draft` was optional and is left out.
