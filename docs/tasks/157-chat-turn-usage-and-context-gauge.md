# 157 — Per-turn usage and a context gauge in the chat

**Status:** 🔄 in progress (3/4) · **Opened:** 2026-11-19 · **Issue:** #1015
(part of the chat UX epic, #1009)

A chat turn's heading said only who answered, which turn it was and how long
ago; what the turn cost and how full the conversation's context had grown were
nowhere on screen. This records each turn's cache counts and context figures,
prints the turn's usage in its heading, puts a context gauge on the
conversation pane's edge, and — on claude — offers `/compact` past the gauge's
threshold.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **157.1** Store and runner: migration `0060_chat_turn_context.sql`
  (`cache_read_tokens`, `cache_creation_tokens`, `context_tokens`,
  `context_window`, and the raw `session_cache_*` totals the copilot share
  needs), `RunResult.ContextTokens`/`ContextWindow` filled by the claude
  adapter, `chatrun` persisting them with the cache counts and extending
  `turnShare` to the cache, `agent.Compactor` (claude's `/compact`) and
  `agent.CacheInInput` (codex). ✓ 2026-11-19
- [x] **157.2** The wire: the four `ChatTurn` fields on `GET /v1/chats/{id}`
  and `compact_command` on `GET /v1/agents` (and so on the MCP `agent_list`
  tool), with the spec amended in §5.5, §9.1, §9.2, §9.3, §9.6, §9.7, §9.9,
  §13.2, §14 and §15. ✓ 2026-11-19
- [x] **157.3** The TUI: the turn heading's usage, the context gauge, the
  totals line below 150 columns, and the rebindable compact key behind a
  confirmation. ✓ 2026-11-19
- [ ] **157.4** A `cmd/fakeagent` scenario whose claude-shaped result reports
  `contextWindow` and a final assistant usage, seeded by
  `scripts/screenshots.sh` into a chat, and the chat shots re-captured so the
  gauge appears in them.

## Decisions

Confirmed by the author on 2026-11-19.

1. **The context window comes from the run's own report.** Claude's adapter
   reads `modelUsage[*].contextWindow` for the model the run actually used —
   the main loop's, never a subagent's haiku — and surfaces it as
   `RunResult.ContextWindow`. This **amends** task 066's decision (§9.2) to
   leave that field unread: a catalog of aliases cannot tell a 200k model from
   a 1M one, or know what the default model is, while the run's report is
   exact. No window table is hard-coded anywhere.
2. **The gauge measures the last model call's prompt size, not totals.**
   `RunResult.ContextTokens` is the last top-level assistant call's input plus
   cache-read plus cache-write tokens. Codex, cursor and copilot report no
   per-call figure in their captured `testdata`, so they leave it 0 and §9.3,
   §9.7 and §9.9 say so. It is never estimated from totals.
3. **Heading tokens include the cache.** The heading's `↑` is input plus
   cache-read plus cache-write, `↓` is output. Copilot's cumulative cache
   counts take task 138.11's share subtraction under the same rules.
4. **Compact is a key that sends `/compact`, behind a confirmation.** A new
   adapter capability, published on `GET /v1/agents` as the flat sibling
   `compact_command` (task 041's rule): `/compact` for claude, `""` for every
   other adapter. Never emulated; adapters without it get neither the key nor
   the hint.

### Settled while implementing

5. **The share needs the raw session cache totals.** Task 138.11 subtracts
   the previous turn's *raw* totals, so the cache share needs raw cache totals
   beside the raw input and output. Migration 0060 therefore adds
   `session_cache_read_tokens` and `session_cache_creation_tokens` beside the
   four columns the plan named. A turn written before 0060 answers 0 for them,
   which makes the next turn's cache share the whole session's — the
   first-turn rule.
6. **Codex records no cache counts.** Codex's `input_tokens` already include
   `cached_input_tokens` (OpenAI's usage shape; every captured run has
   `cached ≤ input`), so decision 3's sum would count the cache twice. The
   adapter implements `agent.CacheInInput` and a codex turn records null cache
   counts; its heading's `↑` is its input, which is already the whole prompt.
7. **A `<synthetic>` assistant line is no model call.** Claude writes one,
   with all-zero usage, as a forked skill's stand-in reply
   (`stream_skill_fork_2.1.277.jsonl`); taking it as the last call would zero
   the gauge.
