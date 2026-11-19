# 154 — A chat hides the lines vincent cannot parse

**Status:** ✅ done (1/1) · **Opened:** 2026-11-19
**Spec:** §15 · **Branch:** `feat/chat-hide-unparsed`

The author found the chat workspace too noisy at its default level. They
switched verbosity at the start of every chat to get rid of the noise.

A look at their recent claude chats showed where it came from. The stream
carries `rate_limit_event`, `system`/`thinking_tokens`, `control_request` and
thinking-only `assistant` lines that no parser models. At `normal` each run of
them drew a `… N unrecognized line(s) (ctrl+r)` row, so one sat between nearly
every pair of messages.

Conventions for this file are in [the tasks README](README.md).

## Decisions

Taken 2026-11-19 by the author ("only show the parsed messages and nothing
that was not parseable").

1. **In a chat, an unrecognized line leaves no trace below verbose.** Quiet,
   compact and normal draw no count; verbose still shows the lines whole. This
   amends issue #321's "unrecognized lines stay behind their count at compact
   and normal" for the chat workspace only. *Beat:* opening chats at `quiet`.
   That would also drop the tool check lines, which are parsed and are what
   the author wants to see.
2. **The task output pane keeps the count.** A step's run is read for what the
   machine did, and the count is its offer to expand. The level is still one
   session value shared by both panes (task 071 decision 3), and the default
   is still `normal`. Only what the chat draws at a level changes, through
   `lineOpts.hideRaw`.

## Tasks

- [x] **154.1** `lineOpts.hideRaw`, set by the chat body; the chat verbosity
  test and the spec and TUI guide amended. ✓ 2026-11-19
