# 077 — Keep assistant Markdown stable while output streams

**Status:** ✅ done (1/1)
**Issue:** #291
**Amends:** §15's task 073, 075 and 076 amendments
**Follow-up to:** [076](076-reader-actions-on-assistant-markdown.md), whose
decision 6 shipped no identity model and named this issue as the one that
brings one

The issue asked for a stable-prefix parser: classify the assistant document
into a settled prefix and an unfinished tail, render the prefix, and promote
the tail at a block boundary. Three things underneath that ask are real, and
one of them is work task 076 parked here by name. The mechanism itself is not,
and this task does not build it.

## Decisions

### 1. The stable-prefix parser is not built

`agent.output` never carries a partial Markdown document. Assistant text
reaches a client one whole message at a time, on every adapter:

- **claude** runs message-level `stream-json` with **no
  `--include-partial-messages`** — the T1.7 decision, recorded at
  `docs/history/v0-tasks.md:142` ("message-level only … token-level deltas
  revisited at T3.3 if the tail feels chunky") and cited in
  `internal/agent/claude/claude.go`. T3.3 never revisited it. `parseAssistant`
  joins a message's `text` blocks into one `EventOutput`.
- **codex** emits an `agent_message` only on `item.completed`
  (`internal/agent/codex/stream.go`).
- **cursor** delivers assistant content blocks whole — "not as deltas"
  (`internal/agent/cursor/stream.go`). Only *thinking* arrives as deltas, and
  the adapter accumulates those before emitting anything.

So the pane is never holding half a fence, a table missing its delimiter row
or a dangling emphasis run in an `agent.output` record. The classifier would
have no stream shape to run against, and its tests could only assert against
splits the daemon cannot emit. If token-level deltas are ever wanted,
reopening T1.7 is its own issue, and this issue's mechanism is what that issue
would build.

Several of the issue's acceptance criteria were already true and already held
by tests — the `X-Next-Offset` seam, one renderer for live and cold, one
renderer for both workspaces, reparse from source rather than from ANSI, and
§16's sanitize chokepoint. They become regression tests here, not new
machinery.

### 2. Consecutive assistant records are one document

`outputLines` treated one `agent.output` record as one Markdown document.
Consecutive records already rendered with no blank line between them, so they
*looked* like one message and parsed as several: a table, list or fence split
across two of them rendered as two broken documents.

A run of consecutive `agent.output` records now accumulates into one document,
joined at the newline a record ends on. Any other record — reasoning, tool
use, tool result, command output, `agent.raw`, a result line — closes it, and
prose after it opens the next. A run or turn boundary closes it too, which
falls out for free: the task pane's `outputLines` is called with one attempt's
records and the chat's is called per turn. The rule is independent of the
verbosity level, so a level that hides a record cannot change how the prose
around it parses.

This is a parse-scope change, not a change to the blank-line rule between a
document and a record that is not prose. It does have one visible consequence
for ordinary prose, and it is the same change seen from the other side: two
records that each carry part of a paragraph now reflow into that paragraph
rather than staying two lines. `TestTurnSeparation` pins it.

Two shipped decisions follow the document rather than the record:

- **Link reference numbering.** Task 075 numbers destinations "per rendered
  message"; the message is now the joined document, so one destination named
  in two records of one run gets one number and one `[n] dest` line, and the
  reference block closes the document.
- **Copy-picker rows.** `copyDocsFromRecords` offered one document per record;
  it offers one per joined document, and "message 1" is the newest document.

### 3. A joined document re-parses whole, and its reflow is bounded rather than eliminated

Joining reintroduces a growing tail at *record* granularity: a live run is a
concatenation whose last record is the newest, so a table header in one record
and its delimiter in the next renders as a paragraph and then becomes a table.
That is accepted, and the bound is asserted instead of the classifier decision
1 refused being built.

The property held by test is the weaker, true one: a record boundary is a
message boundary, the parse is deterministic from the accumulated source, and
**nothing above the last block of the previous document moves** when a record
arrives. Mid-block splits across two assistant messages are possible and rare;
guaranteeing more would mean rendering the newest record one record late, or
re-deriving the classifier under another name.

### 4. Documents and blocks get identities, and the anchor and picker use them

Task 076 decision 6 shipped no identity model on purpose — the picker captured
a row's text when the popup opened — and named this issue as the one that
brings one. `apiclient.TranscriptRecord` carries no id and no offset, and the
wire format does not change, so identity is client-assigned on ingest: a
monotonic `seq` stamped when a record is fetched or appended from a chunk —
**not** an index into the slice, so it survives the `maxRecords` front-prune. A
document is identified by the seq of its first record; a block is its ordinal
within the document.

- **Paused scrolling** captures the identity of the topmost visible block and
  restores it after any rebuild. That fixes the four things that actually move
  a paused reader, none of which is streaming reflow: a resize re-wraps, a
  front-prune shifts every line up, and the verbosity and raw toggles rebuild
  wholesale. Follow mode is untouched — `d.following`/`v.following` and
  `GotoBottom` keep the bottom anchor. `anchorIndex` resolves in three tiers,
  exact line then block then document, which is what carries a reader across
  the raw toggle, where the rendered block ordinals do not exist.
- **The copy picker's rows become references** to a document, resolved at pick
  time. One behaviour change falls out and is deliberate: picking a document
  that has grown since the popup opened copies it as it is now, because "copy
  this message" should yield the whole message. A finished document — the
  common case — is unaffected. A reference that no longer resolves, because
  the prune took its records, falls back to the text captured when the popup
  was built, so a pick can never fail. `TestCopyPickerCapturesAtPickTime`'s
  assertion is amended to that rule rather than deleted.

### 5. Rendering is memoized per document

Every chunk re-rendered every record: `appendChunk` set `outputDirty` and
`renderOutputPane` re-ran `outputLines` over up to 5000 records, at the
daemon's ~10 Hz coalescing rate (§13.3). The cache key is **(digest of the
document's source, width, verbosity level, raw)** — the issue's "theme" and
"completion state" are dropped, because this TUI has no theme concept and,
with whole records, no partial state to key on. A chunk re-renders one
document instead of the whole pane; a growing document re-renders itself,
which is O(document) rather than O(pane) and is the correct cost of decision
3. No client-side throttle and no second timer: the daemon already coalesces.
The memo is swept per render pass rather than bounded by count — what a pane
holds is already bounded by `maxRecords`, and an entry no pass touched belongs
to a document that left the screen.

## Shape

Entirely client-side, in `internal/tui` alone — the shape tasks 073, 075 and
076 each argued for, and for the same reason. **No daemon package, no API
route, no store migration, no wire change.** New: `assistantdoc.go` (the
document model, `seq` stamping, anchors) and `markdowncache.go` (the keyed
memo). Changed: `outputlines.go`, `markdown.go`, `detail.go`,
`detailrender.go`, `chatview.go`, `chatrender.go`, `readerpicker.go`,
`root.go`.

## Tests

`internal/tui` is covered by no gate script, so the hermetic tests are the
whole assurance — task 073's position, unchanged.

- A document split at every line boundary, across two records and three,
  renders identically to the same source arriving in one record — paragraphs,
  nested lists, blockquotes, tables and fences.
- Nothing above the last block of the previous document changes when a record
  arrives — decision 3's bound, asserted line for line.
- A reasoning, tool, command, raw or result record between two prose records
  keeps them two documents; a window boundary does the same.
- One destination named in two records of one run gets one number and one
  `[n] dest` line.
- An unclosed fence, an incomplete table delimiter, a dangling emphasis marker
  and multibyte text across a split produce no panic, no ESC, no C0/C1 and no
  line wider than the pane, rendered and raw.
- The paused anchor survives a resize, a `maxRecords` prune, a level cycle and
  a raw toggle, in both workspaces; follow mode still lands on the bottom.
- The cache: identical source at one width, level and raw renders once; each
  of the three invalidates; a chunk extending one document re-renders one
  document. Renders are counted, not timed.
- A live regression test streams a table whose header, delimiter and body
  arrive as three chunks against the real store, broker, handlers and client,
  and asserts the pane renders the table and the seam neither duplicates a
  record nor loses one. It needed an agent registry on the board harness so a
  step run that names an agent has its transcript normalized out of the
  agent's own dialect (§13.2) rather than surfacing as `agent.raw`.

## Left open

- **Token-level deltas.** Reopening T1.7 is its own issue. If it ever lands,
  the classifier this task declines is what it needs, and decision 3's bound
  is what it would tighten.
- **Link reader actions** remain task 076 decision 1's follow-up. This task
  gives them the thing they were waiting for — a document and block identity a
  reference can be named against — but binds no key and adds no picker row for
  them.
- **No new screenshot.** Nothing here changes a panel's shape; what changed is
  what a split message parses as, which no capture would show.
