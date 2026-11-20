# 166 — Mention issues, tasks and pull requests in a chat, and file line ranges

**Status:** 🔄 in progress (3/6) · **Opened:** 2026-11-20 · **Issue:** #1021 (part of #1009)

A chat message can now name vincent's own objects — `#issue-N`, `#task-N`,
`#pr-N` — and a range of a file's lines, `@path#L20-45`, and the daemon puts a
bounded block for each in front of the message when the turn starts, on all
four adapters alike. It extends task 126 (file mentions), which it does not
reopen, and reuses the preamble seam of task 119 and task 140's
`opening_context`.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **166.1** Wire and expansion: `internal/chatrun/mentions.go` — the
  token parser, issue and task blocks through the store, `#pr-N` through a
  `Deps.PullRequest` closure the daemon wires to the API server's GitHub gate
  (`api.(*Server).MentionPull`), and the block joined after a first turn's
  opening context as the turn's preamble. Migration 0065 adds
  `chat_turns.mention_context`, written once before the agent starts and
  noted in the turn's transcript; the turn DTO, `apiclient.ChatTurn` and the
  export (`internal/chatexport`, all three formats) carry it. Spec §5.5, §14.
  ✓ 2026-11-20
- [x] **166.2** Line ranges: `@path#L20`, `@path#L20-45` and `@path#L20-L45`,
  read workspace-confined (no absolute path, no `..`, no symlink out), CRLF
  as LF, clamped at the end, capped, fenced past any backtick run inside.
  ✓ 2026-11-20
- [x] **166.3** The preview route `POST /v1/chats/{id}/expand` and
  `apiclient.ExpandChat`, off the MCP surface. Spec §13.2, §13.4; API
  reference, agents guide, features page. ✓ 2026-11-20
- [ ] **166.4** The TUI `#` picker on the `chatInlineList` core: the chat
  project's issues, tasks and pull requests, open first, fuzzy by title;
  opens on `#` plus a letter, never on a bare `#` (task 126 decision 8); and
  the line-range step after an `@` pick. Spec §15.
- [ ] **166.5** The composer note (`+2 mentions · ~1.4k tokens · alt+m
  preview`) fed by the expand route, and `alt+m` as a fixed chat key
  (`keymap.fixed` row, `internal/tui/bindings.go`) opening the expanded
  prompt. Spec §15; TUI key reference.
- [ ] **166.6** A gate (`scripts/166-gate.sh`) sending `#issue-N`,
  `#task-N`, `#pr-N` (fakegh) and `@file#L2-3` through a chat on the fake
  agent, wired into the lightest `gate-group`, committed executable; and the
  picker and preview screenshots through `scripts/screenshots.sh`.

## Decisions

Settled with the author on 2026-11-20, before work started. They are the
brief's, recorded here as binding.

1. **Line ranges are a vincent mention, expanded on every adapter.** Inlining
   a range only where `@` is not expanded would emulate claude's `@` on the
   other three, which §9 forbids; vincent owns the feature outright, as it
   owns `#issue-N`. claude also sees the typed `@path#L20-45` text; what it
   does with the suffix is neither probed nor relied on. A bare `@path` stays
   under task 124 decision 9 and task 126 decision 1. A path is resolved
   against `chatrun.Runner.Workspace` and confined to it; an absolute path or
   `..` is not read, and it, a missing file and an out-of-range start stay
   prose. The send is never refused.
2. **Expansion happens in the daemon when the turn starts, and is recorded
   as a snapshot** in `chat_turns.mention_context`; `chat_turns.prompt`
   stays the typed text. A queued message is expanded when its turn starts.
3. **Identity and scope.** `#issue-N` and `#task-N` are vincent ids; `#pr-N`
   is the GitHub number in the chat's project. Only the chat's project
   resolves. A token that does not resolve is prose, with no error. The
   grammar needs a boundary before the sigil.
4. **What a block carries:** title, state and kind, the body bounded like
   task 140's, and links — remote URL, the task's branch, the PR's head and
   base. One renderer, the same on every adapter; `FileMentioner` gains
   nothing.
5. **The preview key is `alt+m`**, a fixed chat key; `ctrl+o` and `alt+p`
   are not moved. The token count is chars ÷ 4, always shown with `~`.
6. **The preview comes from the daemon**, a read-only `POST
   /v1/chats/{id}/expand` running the same function as the turn. It holds no
   slot and is refused only on state.
7. **No adapter capability changes.** `GET /v1/agents`,
   `file_mention_expands` and `vincent agents`' note stay as they are.

### Recorded by the implementer (166.1–166.3)

8. **The bounds.** An issue's body, a task's description and a PR's
   description are each cut at 16 KiB (task 140's issue-chat body cap); a
   line range reads at most 16 KiB of lines. Both end in `…(truncated)` when
   cut. A message expands at most 20 distinct mentions; later ones stay
   prose. A range that *ends* past the file is clamped and its `lines`
   attribute says what was read — only a range that *starts* past the end
   stays prose, because the human plainly meant the file's tail.
9. **A binary file is prose**: a NUL byte in the range stops it being
   inlined, so a mention cannot paste bytes into a prompt.
10. **No `vincent chat expand` verb.** `vincent chat send` needs nothing,
    since the daemon expands, and the preview's consumer is the composer; a
    CLI verb can follow the route later without a wire change.
11. **The expand route's `prompt`** is the draft framed by
    `agent.RunSpec.JoinedPrompt` with the mention block alone; a linked or
    issue chat's first-turn opening context is not repeated in it, because
    that context is not the draft's and is already on the chat.

### Reconciled on the merge train (2026-11-20)

Delivered as task 164; master had meanwhile given 164 to chat drafts and
prompt history, so this is task **166**, and its migration is
`0065_chat_turn_mention_context` (master holds 0063, task 165 0064).
