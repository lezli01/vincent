# 157 — Write a chat message in `$EDITOR`, open the conversation as Markdown

**Status:** 🔄 in progress (2/3) · **Opened:** 2026-11-20 · **Issue:** #1013 (part of #1009)

A chat's composer is three lines tall, and a long prompt is easier to write
in the editor a person already uses. A conversation is also easier to read,
search and quote as one document than through the workspace's viewport. This
adds `ctrl+g`, which writes the draft in `$EDITOR`, and `alt+v`, which opens
the whole conversation as Markdown, rendered by the daemon and served at
`GET /v1/chats/{id}/export?format=md`.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **157.1** The daemon half: `internal/chatexport` renders a chat from its
  turns' normalized records, `GET /v1/chats/{id}/export?format=md` serves it
  (`400 unsupported_format` for anything else, off the MCP surface), and
  `apiclient.ChatExportMarkdown` returns the body. Spec §13.2, API
  reference. ✓ 2026-11-20
- [x] **157.2** The TUI half: `compose_editor` (`ctrl+g`) and `chat_markdown`
  (`alt+v`) as rebindable `Typing` operations, follow moved to `alt+G`,
  `$EDITOR` split quote-aware for every caller, `cmd/fakeeditor` for a real
  round trip on every platform. Spec §15 view 9, task 071 decision 5
  amended; TUI guide and configuration reference. ✓ 2026-11-20
- [ ] **157.3** Re-capture every `docs/assets/tui-*.png` whose chat footer
  still reads `ctrl+g live` with `scripts/screenshots.sh`.

## Decisions

Settled with the author on 2026-11-19, before work started.

1. **`ctrl+g` opens the editor, and follow moves to `alt+G`.** Most terminal
   tools use `ctrl+g` for the editor, which is worth the cost of moving a
   binding. This deliberately amends task 071 decision 5. `alt+G` sits beside
   `alt+g`: lower case goes to the start of the conversation and upper case to
   the live end.
2. **The editor is a rebindable operation**, `compose_editor`, on the `chat`
   surface and `Typing`, because the composer has the keyboard. `ctrl+g` leaves
   the fixed `chat` rows; `alt+G` joins them. Neither key collides with
   anything, so the checker needed no new exception.
3. **The composer round trip.** The draft goes to a temp `.md` file named for
   the chat through `editTextCmd`; what comes back replaces the draft, cursor
   at the end, the inline pickers re-synced. It never sends. An emptied or
   byte-identical file leaves the draft alone, a failure goes to the note
   line and keeps the draft, and the single trailing newline an editor adds
   is dropped — only when the draft did not already end in one. While `↑` has
   a queued message (task 156) in the composer, the editor edits that text
   and `enter` still saves it; a session that began on a different queued
   message is dropped rather than written over the current one.
4. **Quote-aware `$EDITOR` splitting** for every caller: double and single
   quotes, backslash escapes outside single quotes on POSIX only (a backslash
   is a path separator on Windows), and an unbalanced quote falls back to
   splitting on whitespace.
5. **The conversation as Markdown: `alt+v` plus a palette row**,
   `chat_markdown`, `Typing`. A bare `v` would be typed into the draft.
6. **The daemon renders the Markdown.** `GET /v1/chats/{id}/export?format=md`,
   `text/markdown; charset=utf-8`; any other format is a `400
   unsupported_format`. A header (title, agent, model, branch, worktree, linked
   task or issue), then each turn: the prompt as a block quote, the
   assistant's prose as written, each tool call folded to one line. A turn
   whose transcript is gone falls back to its `result_text`. The renderer is
   `internal/chatexport`, fed the normalized records the transcript route's
   `format=normalized` produces, so no dialect is parsed twice. It is **not**
   an MCP tool: every chat read is excluded under task 063 decision 2, and
   making this the family's first tool would reopen that decision.
7. **The view opens in the editor, read-only by convention**: a `0400` temp
   file named for the chat, opened through `openEditorPath`, removed when the
   editor exits (made writable first, since Windows will not delete a
   read-only file). Edits are discarded; `$PAGER` is not used. A fetch failure
   goes to the note line and opens nothing.

## What the tests prove

- `internal/chatexport`: the header for a free chat, a chat on a task and a
  chat on an issue; prompt and answer turns; folded tool calls with command
  output and subagent records left out; a pruned turn falling back to its
  result text; a failed turn's errors.
- `internal/api`: the route over a real chat turn returns Markdown with the
  right content type, defaults to `md`, falls back once the transcript file is
  removed, answers `400 unsupported_format` and `404` for an unknown chat.
- `internal/tui`: `ctrl+g` against the real `cmd/fakeeditor` process found
  through a quoted `$VISUAL` in a directory whose name holds a space — a
  replaced draft, an unchanged file, an emptied file, an editor that exits
  non-zero; the queued-message edit; the splitter's table; `alt+v` against
  the real handler, asserting the opened file's content and mode and that it
  is gone afterwards; the registry probes for `ctrl+g`, `alt+G` and `alt+v`.
- `internal/keymap`: the catalog checker passes with both operations; the
  existing `Typing` clause refuses a printable key for either.

## Not done here

Issue #1023 keeps the rest of the export: the `vincent chat export` CLI
subcommand, the `html` and `json` formats, per-turn usage and the diff.
