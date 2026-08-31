# 071 — Give the chat workspace the output pane's verbosity levels

**Status:** ✅ done (1/1) · **Issue:**
[#282](https://github.com/lezli01/vincent/issues/282)
· **Spec:** §13.3, §15 (view 9 and the verbosity paragraph), §17, decision
record row 29

The chat workspace printed raw agent JSON at a reader mid-conversation and
offered no way to turn it down. The issue was right about the symptom and
understated the cause.

`internal/chatrun.record` published **one** chunk type — `output` — whose only
content was `agent.Event.Raw`, the agent CLI's verbatim stream line. The task
path never did this: `taskrun.publishAgentEvent` maps each normalized event onto
§13.3's typed chunks, which is why `tui.recordFromChunk` can rebuild a
`TranscriptRecord` from a chunk by field name. `chatrender.go`'s `outputNoteLine`
assumed the chat's `raw` *was* one of those normalized records — its comment said
so — and unmarshalled it as one. It never was: claude's stream-json line types are
`system`, `assistant`, `user`, `result`, `stream_event`, none of them
`agent.output`, so `transcriptLine` hit its `default:` arm on every line and
every chunk fell to the dimmed 200-character raw fallback. The chat's live tail
was not "noisy for unmodeled events"; it was raw JSON end to end.

The existing tests hid it: `chatview_test.go` and `stream_chat_test.go` both fed
a `raw` of `{"type":"agent.output","text":"hi"}`, a shape no adapter emits. Those
fixtures were part of the defect and are replaced here with real dialect lines.

The issue's other two defects were real as written. `transcriptLine`'s `default:`
arm dropped `agent.tool_result`, `agent.usage`, `agent.run_header` and
`agent.result` outright, and it disagreed with `outputNoteLine` about
unrecognized lines — so a reconnect changed what was on screen.

## Decisions

1. **The chat's live chunks are normalized in the daemon.** `chatrun.record`
   publishes §13.3's typed chunks the way `taskrun.publishAgentEvent` does; it
   already holds the parsed `agent.Event` at the call site, so this is a
   mapping, not new parsing. `raw` stays in the payload alongside the normalized
   fields, so the change is additive and no existing consumer breaks. This
   overrides the issue's "nothing changes daemon-side": normalization is §13.3's
   job, and doing it in the client would mean a second copy of it, a new
   `tui → agent` dependency edge, and a client that has to know which dialect a
   chat speaks. *Alternatives rejected:* parsing the dialect in the TUI; leaving
   the tail raw and rendering it as `agent.raw` (a running turn would show almost
   nothing below verbose).

   The mapping itself lives in `internal/agent` (`LiveChunk`, `UnmodeledLine`),
   shared by both runners. `chatrun → taskrun` would be a new edge against the
   one-way dependency direction, and a duplicated switch would be two definitions
   of a wire format whose entire purpose is that both sides agree.

   `agent.result` and `agent.error` are **not** published live, mirroring the
   task path. They normalize to their own record types in the transcript, so a
   live chunk for them is one the refetch would contradict; the turn's outcome
   reaches a client as the turn's own state. `agent.raw` **is** published for the
   three events §13.3 also surfaces raw, which is what keeps the two sides
   line-for-line identical.

2. **One renderer, reached by both views.** `detail.outputLines` and
   `detail.renderRecord` moved out of `detailrender.go` and are free functions in
   `outputlines.go` over `(records, level, width, lineOpts)`. `chatrender.go`'s
   `transcriptLines`, `transcriptLine` and `outputNoteLine` are deleted; the chat
   rebuilds records from chunks with the same `recordFromChunk` the task pane
   uses. This is what makes a live line and a refetched line identical at a
   level, and it is not a separable change.

3. **The verbosity level is one shared value.** `detail.level` is a pointer to a
   `levelHolder` created in `newViews` and handed to both `newDetail` and
   `newChatView`. Nothing is persisted: no `tui.json` entry, no `tui:` config
   key, exactly as §15 already reasons for the task pane. Cycling in either view
   is visible in the other.

4. **Keys.** `ctrl+r` cycles compact → normal → verbose → compact in `ctxChat`.
   `pgup`/`pgdown` scroll the conversation and `ctrl+g` jumps to the live end and
   re-arms follow. None of the four has meaning in a three-line textarea, so the
   composer keeps `↑`/`↓` for editing a multi-line draft and keeps every letter —
   which is why `v`, `f` and `G` are unavailable here: `chatView.capturesInput()`
   is true whenever the composer is focused. `ctrl+r` was unbound in every
   context.

5. **The chat body gets a viewport.** At verbose the body grows several-fold, and
   a bottom-anchored window with no way to scroll would put the newly revealed
   content out of reach as fast as it appears. The chat takes the output pane's
   follow model: following means showing the end, a manual scroll pauses it,
   `ctrl+g` re-arms it.

6. **Finished turns render from their transcripts — newest five eagerly.** §15
   view 9 already required this; the client only ever fetched the running turn.
   On open, the five newest finished turns' transcripts are fetched; older turns
   fetch when scrolled to. A total record cap (`maxRecords`) with the "earlier
   output truncated" marker bounds memory on a long conversation. A transcript
   gone to retention still falls back to `ResultText` (§17), silently, as it did.

7. **The "(v)" hints are parameterized.** `thinkingBlock`'s `… +N lines` and the
   collapsed-raw `… N unrecognized line(s)` name the key that expands them, so
   the chat says `(ctrl+r)`. A hint naming a key that types a letter into the
   composer would be worse than no hint.

## Tasks

- [x] **071.1** — Normalize a chat's live chunks in the daemon, render the chat
  body through the output pane's renderer at a session-wide level, and give the
  workspace `ctrl+r`, `pgup`/`pgdown` and `ctrl+g`. Spec §13.3, §15 view 9, §15's
  verbosity paragraph and decision row 29 amended in the same PR.

## What the tests prove

- `internal/chatrun` — a finished turn's published chunks carry §13.3's typed
  names and normalized fields, never the pre-071 `output` catch-all, and every
  one still carries `raw` (the additive half of decision 1).
- `internal/tui` — the three levels mean what §15 says inside a chat, including
  the records the old switch dropped; the collapsed-content hints name `ctrl+r`;
  a live chunk and a refetched record render identically at every level; the
  level is one value shared with the task pane and survives leaving and
  reopening a chat; the composer still receives every letter and both arrows;
  the newest five finished turns are fetched on open and older ones on scroll;
  a pruned transcript falls back to `ResultText` with no banner; the record cap
  emits its marker.
- `internal/apiclient` — the chat stream's fixture is a real claude stream-json
  line under a typed chunk, replacing the `{"type":"agent.output"}` shape no
  adapter emits.
- `scripts/m14-gate.sh` — over the wire: the chat event stream carries typed
  `agent.*` chunk names, never `output`, and a chunk carries both a normalized
  field and its raw line.

## Not done here

The screenshots. The chat workspace's appearance changed, but there is nothing
stale to re-make: **the chat views have never been captured.**
`scripts/screenshots.sh` seeds no chat and has no chat tape, so no
`docs/assets/tui-*.png` shows this panel — the gap task 067 recorded when it
added the two views is still open, now covering a body that renders through the
output pane. Closing it is a seeded VHS run on a macOS or Linux workstation
(VHS, ttyd, ffmpeg; CI does not run it) that first grows a chat tape. Until
then `docs/guides/tui.md` describes the panel in prose and a key table, and
nothing here is hand-drawn to stand in for a capture.
