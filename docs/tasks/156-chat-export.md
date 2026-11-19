# 156 — Export a chat as Markdown, HTML or JSON

**Status:** ✅ done (4/4) · **Opened:** 2026-11-19 · **Issue:** #1023 (chat UX epic #1009)

Nothing exported a chat. `vincent chat show` prints each turn's prompt and
`result_text` and `vincent chat transcript` (task 103) one turn's transcript,
but there was no way to keep or share a whole conversation. No earlier decision
ruled an export out — tasks 063, 067 and 103 never decided against one — and a
read-only render is not a §6 human action (task 047 decision 9).

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **156.1** `internal/chatexport`: the document model (`Export`, JSON
  `"version": 1`), the Markdown, HTML (goldmark, raw HTML disabled, inline
  CSS, no scripts, images turned into links) and JSON renderings, the
  normalized-transcript tool reader, and `ToolSummary`, the one definition of
  a tool call's one-line form, which `vincent task transcript` / `chat
  transcript` now call. Golden tests per format. ✓ 2026-11-19
- [x] **156.2** `GET /v1/chats/{id}/export?format=&diff=`: the retention
  fallback, the diff of the directory the chat works in today through
  `diffMergeBase`/`diffFrom`, factored out of `handleTaskDiff` (whose output
  is unchanged), and `apiclient.(*Client).ChatExport`. ✓ 2026-11-19
- [x] **156.3** `vincent chat export <id> [--format] [--with-diff] [-o]`, with
  `--json` as a synonym for `--format json`, writing `-o` atomically through
  the new `fileswap.WriteFile`. ✓ 2026-11-19
- [x] **156.4** The TUI's rebindable "export chat" operation on the chat
  workspace and the chats board, in the palette, opening the export form;
  copying the path or the Markdown afterwards. ✓ 2026-11-19

## Decisions

Confirmed by the author on 2026-11-19.

1. **The diff is the base-to-tip diff of the directory the chat works in
   today**, computed the way `GET /v1/tasks/{id}/diff` computes its own: a
   free chat's (or an issue main worktree chat's) `worktree_path` against the
   chat's base, a linked chat's task worktree against the task's base. With no
   directory left (handed off, archived with its worktree removed, a path that
   is gone) the export says `diff unavailable: <reason>` and still succeeds.
   Per-turn commit attribution was considered and not taken: it needs per-turn
   SHA bookkeeping and a migration.
2. **HTML goes through goldmark**, a direct dependency, with raw HTML left
   disabled (goldmark's default, kept on purpose) so agent output cannot inject
   markup. The page is one file with inline CSS and no scripts; an image in an
   answer becomes a link, so opening the page loads nothing.
3. **One line per tool call** — name, key argument, failed marker — reusing
   the one-line summary the transcript renderers already print. Tool output is
   never included. Calls a subagent made are left out, as the chat's compact
   level leaves them out of the turn's own line.
4. **The TUI** gets a rebindable keymap operation, also in the palette, which
   opens a form for format, diff and path (prefilled
   `./chat-<id>-<slug>.<ext>` in the TUI's working directory) and afterwards
   offers to copy the path or the Markdown.
5. **The daemon renders; clients write.** The CLI never invents a filename —
   the route's `Content-Disposition` name is for the TUI. `--json` on `chat
   export` is a synonym for `--format json` and refused beside another format;
   task 047 decision 1 and task 103 decision 1 concern transcript renderers,
   not a document with three formats.
6. **Not in scope:** a share link or `--gist`, an MCP tool, exporting tasks.
   No gate script: like task 103 this is a read-only client surface proven
   in-process against the real handlers.
