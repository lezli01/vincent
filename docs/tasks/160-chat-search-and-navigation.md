# 160 — Search inside a chat, jump between prompts, expand one tool call

**Status:** 🔄 in progress (2/3) · **Opened:** 2026-11-20 · **Issue:** #1014 (part of #1009)

A long chat could only be read by scrolling. This adds a conversation focus
to the chat workspace — `ctrl+f` hands it the keyboard with a search line —
in which a line cursor moves, `n`/`N` step through every match in every turn
(the unloaded ones searched by the daemon), `{`/`}` jump between the `You`
cards, and `enter` expands one tool call to verbose without touching the
session level. A `↓ N new` marker says how much arrived while scrolled away.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **160.1** The server half: `GET /v1/chats/{id}/search?q=`, a
  case-insensitive plain-substring scan of every turn's prompt and normalized
  transcript text, falling back to `result_text` when the transcript has gone
  to retention; per-turn counts and no text; `q` bounded at 256 bytes and each
  turn's scan at 32 MiB (`truncated`). Off the MCP surface with the rest of
  the chat family; `apiclient.SearchChat`; `vincent chat find` for the
  TUI/CLI parity rule. ✓ 2026-11-20
- [x] **160.2** The TUI: conversation focus (`ctrl+f`, `j`/`k`, `/`, `n`/`N`,
  `{`/`}`, `enter`, `g`/`G`, `i`/`esc`), match highlighting with the current
  match distinct, the `3/17` count and `↓ N new` on the pane's bottom edge,
  the per-chat expanded set and `lineOpts.expanded`, clicks on a check line
  and on the marker. Spec §13.2 and §15 amended; API, TUI and key docs.
  ✓ 2026-11-20
- [ ] **160.3** Re-capture the chat screenshots with `scripts/screenshots.sh`
  and add one of a search with a highlighted match and an expanded call; the
  seed needs a chat long enough to have matches in turns not loaded yet.

## Decisions

Settled with the author on 2026-11-20.

1. **Conversation focus mode. Amends task 071 decision 4.** The composer
   keeps the keyboard by default. `ctrl+f` moves it to the conversation and
   opens a search line. In conversation focus a line cursor moves with
   `j`/`k`/`↑`/`↓`; `pgup`/`pgdown`, `g`/`G` and the alt keys still scroll;
   `/` opens the search line again; `n`/`N` go to the next or previous
   match; `{`/`}` jump to the previous or next `You` card; `enter` expands or
   collapses the tool call under the cursor; `esc` or `i` hands the keyboard
   back. `esc` peels one layer at a time — search line → conversation focus →
   composer — and never leaves the chat from conversation focus.
   `capturesInput()` is false only while the conversation has focus and no
   search line is open. Clicking a tool check line also enters conversation
   focus and toggles that call. The draft is untouched by all of it.
   *Rejected:* modifier-only keys (`alt+n`, `alt+[`, …) with no line cursor,
   because expanding one call needs something to point at.
2. **Unloaded turns are searched on the server.** `GET
   /v1/chats/{id}/search?q=` scans each turn's transcript text, using
   `result_text` when the transcript has gone to retention (§17), and
   returns the matching turn seqs with a count each and no rendered text. The
   TUI fetches just those turns through the transcript route, which counts
   against `maxRecords` like any other fetch, and finds the matches in its
   rendered lines. #1022 (search across every chat) is meant to reuse the
   scan. *Rejected:* client-side scanning of every transcript (one read per
   turn per search); a `result_text`-only search (misses tool output and
   intermediate messages). MCP gets no tool: the chat family is off the MCP
   surface whole (§13.4). The CLI does get a subcommand, because the TUI/CLI
   parity rule (`TestDocsClaimsTUIClientCallsHaveSubcommands`) holds every
   client call the TUI makes to a subcommand. *Amended 2026-11-20 (merge
   train):* it is `vincent chat find CHAT_ID TEXT`, not `chat search` —
   task 158 landed first with `vincent chat search QUERY...` across every
   chat, so the in-chat search took the name its TUI context (`chat find`)
   has too. Task 158 also brought its own index rather than reusing this
   scan.
3. **Search matches all of a turn's text**, not only what the current level
   draws. Landing on a match inside a folded tool call or text the level
   hides expands that one record (decision 4); the session level never
   changes. The count `3/17` counts every match and sits on the pane's edge;
   matches are highlighted, the current one in its own style
   (`chatlook.go`).
4. **Per-call expansion.** The view keeps an expanded set keyed by turn seq
   and the call's tool-use id, or — for a record with none — its position in
   the turn (the client's record ids are reassigned on every refetch). In
   memory, per chat, for the session; it survives switching chats and back.
   An expanded call renders as at verbose. The renderer change is
   `lineOpts.expanded`, so the task output pane could adopt it later; it is
   not wired there. `ctrl+r` and the shared level are unchanged (071
   decision 3). Check lines carry `▸`, an expanded call `▾`, and the hint
   names `enter` only while the conversation has focus (071 decision 7).
5. **`↓ N new`.** While not following and lines are appended below the
   viewport, the pane's bottom edge shows `↓ N new`, N the rendered lines
   added since follow stopped. Clicking it or `ctrl+g` re-arms follow and
   clears it. A rebuild for a level or width change does not count.

## Departures

- The brief asked for the new keys to be registered as rebindable
  `internal/keymap` operations. `/` in conversation focus is the existing
  `filter` operation (its surfaces now include `chat conversation`); every
  other new key is recorded as a fixed, surface-local key in `keymap.fixed`
  — the way the skill list's and file picker's keys are — so `tui.keys`
  cannot rebind them yet. The clause checker holds them against every
  operation, and `TestEveryMatchedKeyIsRegistered` passes.

## What the tests prove

- `internal/api/chatturnsearch_test.go`: seqs and counts, case-insensitive; the
  `result_text` fallback after the transcript is removed; empty and
  oversized queries are `validation_failed`; an unknown chat is 404.
- `internal/tui/chatfind_test.go`: `ctrl+f` moves focus and letters no
  longer reach the draft; `esc` peels and never leaves; `n`/`N` wrap and the
  count follows; a match in an unloaded turn fetches exactly that turn and
  lands on it; a match in a folded call expands only that call at an
  unchanged level; `{`/`}` land on `You` cards; `enter` toggles one call and
  the set survives a chat switch; `↓ N new` grows while scrolled up, is not
  inflated by a level change and resets on `ctrl+g`.
- `internal/tui/chatfind_live_test.go`: the search against the real
  handlers over httptest, landing in a turn the view never loaded.
- `internal/cli/chatfind_live_test.go`: `vincent chat find` against the
  real handler — counts, the result-text line, `--json`'s empty array, exit 1
  on an empty query.
- `panelKeyProbes` covers every new registry row.
