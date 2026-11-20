# 162 — Notify on chats, mark unread chats, switch chats without the list

**Status:** 🔄 in progress (5/6) · **Opened:** 2026-11-20

Part of the chat UX epic (#1009); closes issue #1019. A chat asking for input
or finishing a turn had no way to reach someone who had looked away: the
daemon's `notify:` hook was silent on chats (task 063 decision 7), the TUI's
attention count and `!` were task-only (task 067 decision 4), nothing marked a
chat whose answer nobody had read, and reaching another chat meant going back
to the list.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **162.1** `notify.chat_on`: `chatstate.NotifyOn`, config validation, the
  notifier's chat events and the envelope's `kind` and `chat_id`; Settings
  edits the key. ✓ 2026-11-20
- [x] **162.2** Unread marks: migration `0062_chat_last_viewed`,
  `POST /v1/chats/{id}/viewed`, `last_viewed_at` and `unread` on the chat
  body, `chat.viewed`, the `●` mark, and the route off the MCP surface.
  ✓ 2026-11-20
- [x] **162.3** Attention: `awaiting_input` chats in the count, the tally and
  `!`. ✓ 2026-11-20
- [x] **162.4** The focus-gated bell, shared by tasks and chats at the root.
  ✓ 2026-11-20
- [x] **162.5** Chat switching: `next_chat`, `prev_chat`, `chat_1`…`chat_9`,
  and `]`/`[` on the chats board. ✓ 2026-11-20
- [~] **162.6** Docs and pictures: the feature, guide and reference pages,
  and re-captured chats screenshots with a `●` mark and a chat in the
  attention count. The pages are written (2026-11-20); the screenshots wait
  on a re-run of `scripts/screenshots.sh`.

## Decisions

Confirmed by the author on 2026-11-20, in the brief for #1019.

1. **`notify.chat_on`, not a wider `notify.on`** — amends task 063
   decision 7. Its vocabulary is `chatstate.NotifyOn`: `awaiting_input`,
   `turn_done`, `turn_failed`. `turn_interrupted` is refused: it only happens
   on a daemon restart, which the human caused. It reuses `notify.command`.
   `internal/config` imports `chatstate`, a stdlib-only leaf (widening task
   046 decision 4 as task 118 decision 3 and #969 did).
2. **The envelope gains `kind` and `chat_id`**, additively: `"task"` on every
   task envelope, `"chat"` on a chat's.
3. **One `last_viewed_at` per chat**, stored by the daemon: the installation
   has no per-user identity, so "the same in every client" means one value.
   It never moves backwards. `unread` is derived on read: the latest
   terminal turn ended after it.
4. **Supersedes task 067 decision 4.** Only `awaiting_input` chats count
   towards attention; an unread finished turn is only the `●` mark. Row 29
   holds: chats join the count and `!`, never the task board's columns.
5. **The bell is focus-gated for tasks and chats alike**, with one
   once-per-event-id rule and rate limit at the root. Focus starts true, so a
   terminal that never reports focus never rings; the TUI does not guess.
   The TUI never runs `notify.command`.
6. **Chat switching** follows the chats board's order for the selected
   project (waiting on you, running, idle), wraps, does nothing past the
   end, and keeps the draft of the chat it leaves. `]`/`[` are fixed aliases
   on the chats board only, where nothing is typed.
