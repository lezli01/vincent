# 161 — Start a chat from its first message, rename chats, choose the agent for a chat on a task or issue

**Status:** ✅ done (6/6) · **Opened:** 2026-11-20

Part of the chat UX epic (#1009), tier 1; closes issue #1016. Starting a chat
used to take a title first and a message second — two requests from the CLI,
two steps in the TUI. A chat's title could not be changed afterwards, and a
chat opened on a task or an issue always took the default agent in the TUI,
although the API and the CLI already accepted one. This makes the first message
the way a chat starts, makes the title optional and derived from it, adds a
rename, and gives `T` a sibling that asks for the agent.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **161.1** API: `message` on `POST /v1/chats` (free and issue chats) and
  on `POST /v1/tasks/{id}/chat`, sent as turn 1 in the same request;
  `first_turn` on the 201; a refused send keeps the chat and says so in
  `warnings`; the one title-derivation helper; `PATCH /v1/chats/{id}` with
  `title`, the `chat.updated` event, and the route on `internal/mcp`'s
  exclusion list. ✓ 2026-11-20
- [x] **161.2** CLI: `vincent chat start [<title>]` sends the message in the
  create; `vincent chat rename`; `--message`/`--message-file` on
  `vincent task chat`. ✓ 2026-11-20
- [x] **161.3** TUI: the new-chat form's message row, focused on open, `enter`
  creates and sends; the not-sent path reopens with the message in the
  composer. ✓ 2026-11-20
- [x] **161.4** TUI: the `rename` and `chat_with` operations, their palette
  commands, and the `chat_with` form. ✓ 2026-11-20
- [x] **161.5** Docs and gate: §5.5, §13.2, §13.3 and §15 amendments, the API,
  CLI and key references, the chats guide, `CHANGELOG.md`, and a
  create-with-message and a rename step in `m14`. ✓ 2026-11-20
- [x] **161.6** Re-capture the new-chat form and chats board screenshots with
  `scripts/screenshots.sh` (VHS): the form gained a message row.

## Decisions

Confirmed by the author on 2026-11-20, in the brief for #1016.

1. **First message on create.** `POST /v1/chats` accepts an optional
   `message`. After the chat is created exactly as before, the handler sends
   it, in the same request, and the 201 carries `first_turn` (null when no
   message was given or the send failed). **A failed send keeps the chat**:
   nothing is rolled back, the answer is still 201, `first_turn` is null and a
   `warnings` entry says the first message was not sent and why. Rolling back
   was the alternative; it would discard a worktree the user may already be
   looking at, for a refusal (the chat cap) that clears by itself. The message
   is not stored by the daemon — that is #1012's. The same `message` is
   accepted on `POST /v1/tasks/{id}/chat`, where the linked chat's opening
   context is prepended to turn 1 as to any first turn.
2. **Optional title, derived from the message.** An empty title (after
   trimming) is the first non-empty line of the message, whitespace collapsed,
   cut at a word boundary to 72 runes with `…`. The branch slug follows the
   derived title. Both empty is still a 400 ("a title or a message is
   required"). One helper serves every route that creates a chat; the
   task-linked route keeps "default: the task's title" ahead of derivation. An
   agent-written title was declined as out of scope (a possible follow-up).
3. **CLI.** `<title>` is optional on `vincent chat start`, which refuses
   locally with neither a title nor a message, sends the message in the
   create instead of a second `send`, and still waits for the answer. A 201
   whose message was not sent prints the chat and the warning and exits
   non-zero.
4. **Rename.** `PATCH /v1/chats/{id}` with `{title}`: non-empty after
   trimming, at most 72 runes, legal in every state including terminal ones
   (the title is display only). It **never renames the branch**: the slug is
   fixed at creation. It publishes `chat.updated`. The handler is shaped so
   #1011 can add `model`/`effort`/`permission_mode`; none is accepted here,
   and an unknown field is a 400. CLI: `vincent chat rename <chat-id> <title>`.
5. **New-chat form.** A multi-line message row, and the form opens focused on
   it with every other row at its default. `enter` in the message row submits
   one create carrying the message. There is no separate quick-chat key: `n`,
   type, `enter` *is* the quick chat. When the first message was not sent,
   the conversation opens with the message back in the composer and the
   warning shown, so nothing typed is lost.
6. **Rename in the TUI.** A rebindable operation `rename` (default `r`) on the
   chats boards opens a one-line prompt prefilled with the title, and a
   palette command "rename chat" does the same, reachable from the
   conversation through `ctrl+p`. No fixed key inside the workspace: the
   composer owns every printable key.
7. **Agent choice for a chat on a task or issue.** `T` is unchanged: instant,
   default agent. A rebindable operation `chat_with` (default `alt+t`) on the
   surfaces `chat` is on, plus a palette command "chat with…", opens a small
   form — agent, model, effort (resumable adapters only) and an optional first
   message. When the target already has an open chat it behaves like `T` and
   navigates there, because a linked chat's agent is fixed. `shift+T` from the
   issue could not be a second key: `T` already is shift+t.
