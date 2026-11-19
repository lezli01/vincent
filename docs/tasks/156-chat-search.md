# 156 — Search across every chat, live and archived

**Status:** ✅ done (5/5) · **Opened:** 2026-11-19 · **Issue:** #1022 (epic #1009)

The chats board's `/` filters by title, agent and branch only. Nothing found a
chat by what was said in it, and archived chats were out of reach entirely.
This adds a full-text index over every chat's title, prompts and final answers,
exposed as one read-only route, a `vincent chat search` subcommand and a TUI
search view whose results open the chat on the matching turn.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **156.1** Store: migration `0059_chat_search.sql` — two external-content
  FTS5 tables, trigram-tokenized, over `chats.title` and
  `chat_turns(prompt, result_text)`, kept in step by triggers and backfilled
  with `rebuild` — and `Store.SearchChats`, which quotes every term, scopes by
  project and the archived window, ranks by `bm25`, groups by chat and caps
  chats and matches. ✓ 2026-11-19
- [x] **156.2** API: `GET /v1/chats/search`, the `query_too_short` code, the
  route listed as not-an-MCP-tool, and `apiclient.SearchChats`. ✓ 2026-11-19
- [x] **156.3** CLI: `vincent chat search`. ✓ 2026-11-19
- [x] **156.4** TUI: the chat search view, the "Search all chats" palette
  entry and `S` on the chats screen; `enter` on a match opens the chat on that
  turn, `esc` returns to the search. ✓ 2026-11-19
- [x] **156.5** Capture `docs/assets/tui-chat-search.png` with
  `scripts/screenshots.sh`. ✓ 2026-11-19

## Decisions

Confirmed by the author on 2026-11-19.

1. **FTS5, no `LIKE` fallback.** The pinned `modernc.org/sqlite` has FTS5
   compiled in; `TestFTS5IsCompiledIn` fails loudly if a driver bump drops it.
2. **Tokenizer: `trigram`.** Matching is by substring, so `429` finds
   `status429Body`. A term shorter than three characters cannot use a trigram
   index, so the API refuses it with `400 query_too_short` rather than scanning.
   The larger index is the accepted cost.
3. **Scope defaults to everything.** The route takes `GET /v1/chats`'
   `archived=false|true|all` with the same meaning, but defaults to `all`,
   because finding a chat that is done with is the point of searching.
   `project_id` is optional.
4. **TUI entry: a palette entry plus `S` on the chats screen.** `/` keeps
   filtering the list; `ctrl+f` is left for #1014's in-conversation search.
5. **Not an MCP tool.** Spec decision row 29 keeps the whole chat route family
   off the MCP surface, and this route follows it.
6. **Two external-content tables, not one.** An external-content FTS5 table
   reads its text from one content table, and the text lives in two. One
   table with a kind column would have to store its own copy of every prompt
   and answer; two store only the index. `snippet()` works against external
   content, so no copy is needed for it either.
7. **Highlights are data.** The route returns each snippet as plain text and
   as `segments` marked `match`; no client parses markup or ANSI from the
   server.

## Out of scope

In-conversation search and `n`/`N`, `{`/`}` (#1014); searching tool output in
transcripts; task and issue search.
