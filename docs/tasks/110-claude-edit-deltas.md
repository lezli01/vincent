# 110 — Show edit deltas in claude tool results

**Status:** 🔄 in progress (4/5)
**Issue:** [#402](https://github.com/lezli01/vincent/issues/402)
**Spec:** amends §9.1, §9.2, §9.3, §9.7, §13.2, §13.3, §15
**Follow-up to:** [066](066-claude-stream-json-surface.md), follow-up 2

## Problem

A claude `Edit` rendered its outcome as the tool's own sentence, cut at 120
runes: "The file /Users/…/x.go has been updated successfully. (file state is
current in your context…". It says which file changed, which the `▸ Edit
<path>` call line above it already said, and nothing about how much changed.

`ToolResult.Summary`'s doc comment promised a line delta from T4.14 until task
066 decision 3 withdrew the promise: the only fixture with a structured
`tool_use_result` was a `Write` of type `create`, whose `structuredPatch` is
always `[]`, and implementing `+N −M` against a shape no capture held is the
guess T4.17 refused. The hunk itself was reachable only through the Diff tab
and the raw transcript.

## Evidence

The issue asked for a capture. None was needed, for the reason task 109 needed
none: vincent's own recorded transcripts on the owner's machine hold the shape
at scale. Across `{data_dir}/transcripts`, claude 2.1.232 to 2.1.268:

- **2,342 `Edit` results carry a non-empty `structuredPatch`**, and their
  `tool_use_result` has **no `type`**. The keys are `filePath`, `oldString`,
  `newString`, `originalFile`, `structuredPatch`, `userModified` and
  `replaceAll`, so an `Edit` never reached the verb table.
- **52 `Write` results have `type: "update"`** and a non-empty patch (2 more an
  empty one). `update` had never been in the verb table.
- **981 `Write` results have `type: "create"`**, every one with
  `structuredPatch: []`.
- **Hunks** are `{oldStart, oldLines, newStart, newLines, lines[]}`, every line
  prefixed with `+`, `-` or a space. 126 results have more than one hunk.
  `replaceAll: true` appears 10 times; `userModified` is always `false`.
- **Size:** 13 lines per hunk at the median, 38 at p90, 633 at most. As a
  unified patch that is 814 runes at the median, 2,280 at p90, 12,403 at p99
  and 67,619 at most; 41 of 2,394 exceed 8,000 runes.
- **Subagent edits carry no `tool_use_result`.** 380 `Edit` and 48 `Write`
  results with a `parent_tool_use_id` have none; one has it.
- **One `tool_result` per line.** Every `user` line with a `structuredPatch`
  has exactly one `tool_result` block.
- **A failed edit** ("String to replace not found") carries a string
  `tool_use_result` and `is_error: true`.

## What shipped

- **`internal/agent`:** `Patch` (`CallID`, `Name`, `Text`, `Truncated`) and
  `Event.Patch`, riding the `EventToolResult` that reports the edit, with no
  event type of its own. `PatchMax` (8,000 runes) beside `CommandOutputMax`, and
  `TruncatePatch`. `LiveChunks` appends an `agent.patch` chunk after the
  result's. `ToolResult.Summary`'s doc names the delta again.
- **`internal/agent/claude`:** `structuredPatch` is read (the hunk fields only);
  `update → updated` joins the verb table; a non-error line reporting one
  result with a non-empty patch gets the `+N −M` summary and a capped unified
  patch.
- **`internal/api`:** `normalizedEvent` writes the `agent.patch` record after
  the result. **`internal/apiclient`:** `TranscriptRecord.Patch`.
- **`internal/tui`:** the `agent.patch` arm — `verbose` only, never nested,
  gutterless and preformatted, `+`/`-` in the Diff tab's colors, `@@` dim, the
  truncation stated.
- **`internal/cli`:** `vincent task transcript` skips `agent.patch`; its outcome
  line prints `< +13 −9` with no change of its own.
- **Fixture:** `internal/agent/claude/testdata/stream_edit_2.1.268.jsonl`.

## Decisions

Settled with the owner, 2026-09-17.

### 1. The hunk goes on a new shared record type, `agent.patch`

T4.16's "outcome only" rule for `ToolResult` stands, and a hunk is a body.
Task 070 decision 2 already solved this for codex's command output: the body
gets its own record, visible at `verbose` only, capped with the cut shown. This
is that answer again, not an exception to T4.16. Following task 070 decision 1,
it goes in the shared §13.2/§13.3 vocabulary with `call_id`, `name`, `patch`
and `truncated`. Claude fills it; codex and cursor fill none, and their tests
say so over every fixture.

**Beat:** reusing `agent.command_output`, which would label a patch "what a
command printed"; and a delta with no hunk, which would leave the hunk
reachable only through the Diff tab and the raw transcript.

### 2. The delta covers every result with a non-empty `structuredPatch`

That is `Edit` and `Write` `update`. A capture now shows `update`, so it joins
the verb table as `updated` under the T4.17 rule task 066 decision 3 applied,
and an overwrite renders `updated · +3 −1`.

- `create` is unchanged: it keeps `created` and its prose summary, and gets no
  delta and no patch. Its patch is always empty, and counting the lines of
  `content` would put a vincent-derived number where claude's own patch is read
  everywhere else.
- `Edit` gets no verb. The dialect sends no `type`, and inferring one from the
  payload's keys is the guessing 066 decision 3 refused.

**Beat:** "Edit only", which would leave overwrites as the one structured edit
still rendered as prose.

### 3. The summary is the delta alone, with no path

`+13 −9`, exactly cursor's form (§9.7), for the reason recorded there: the
`▸ Edit <path>` call line already names the file. N and M count the `+` and `-`
lines across all hunks — a hunk header's `oldLines`/`newLines` include context
— before any cap, so a truncated patch reports its true delta. An empty patch
yields no delta, and `+0 −0` is never invented. The delta renders wherever
outcomes render, from `compact` up, and it replaces a line rather than adding
one, so 066 decision 1's "compact does not grow" holds.

**Beat:** delta plus basename, which repeats the call line in the common case
and differs from cursor.

### 4. Fixture: trimmed from recorded runs

One call line and one result line per case, from 2.1.268 runs: a single-hunk
`Edit`, a two-hunk `Edit`, a `replace_all` `Edit`, a `Write` `update`, a
`Write` `create`, a failed `Edit` and a subagent's `Edit`. Paths are rewritten
under `C:\work\repo` the way the existing fixtures are, `originalFile`,
`oldString`, `newString` and `content` are replaced with placeholders (none is
read), and the hunks, line structure and field names are verbatim.

**Beat:** a fresh capture, which spends API quota to reproduce shapes already
on disk.

### 5. A patch is attributed only on a one-result line

`tool_use_result` belongs to the `user` line, not to a `tool_result` block, so a
line with several results would leave the patch's owner a guess. Every recorded
line with a patch has exactly one result, and a line with more gets no delta
and no patch rather than a guessed attribution.

### 6. The pane renders a patch preformatted

Added on landing. A patch's indentation is part of what changed, and the pane's
word wrapper collapses runs of spaces, so the patch lines go through the
preformatted path a fenced code block uses: every space kept, a line wider than
the pane continued on the next row at the gutter, never clipped.

**Beat:** word wrapping, as `agent.command_output` renders, which would show a
re-indented block as an unchanged one.

## Binding decisions this respects

- **T4.16:** `ToolResult` stays an outcome; the body rides its own record.
- **T4.17 / 066 decision 3:** a verb exists only for a type a capture shows.
- **066 decision 1:** compact gets no new lines.
- **066 decision 5:** the transcript record and the live chunk move together,
  result then patch, in the order task 070 set.
- **070 decisions 1–2:** shared vocabulary; a verbose-only body with a rune cap
  and visible truncation.
- **109 decision 2:** a nested record renders one level quieter, so a
  verbose-only `agent.patch` never renders nested.
- **§13.2 normalizes on read:** every claude run already on disk renders deltas
  and patches.

## Work

- [x] **110.1 — Wire and parser**: `agent.Patch`, `Event.Patch`, `PatchMax`;
  claude's `structuredPatch`, the `updated` verb, the delta and the capped
  patch; the `agent.patch` record, chunk and `TranscriptRecord.Patch`; the
  fixture; codex and cursor stated to produce no patch over every fixture.
  ✓ 2026-09-17
- [x] **110.2 — The pane**: the verbose-only, never-nested patch, styled and
  preformatted, with the truncation stated. ✓ 2026-09-17
- [x] **110.3 — The CLI**: `vincent task transcript` skips `agent.patch`;
  `--json` and `--raw` carry it. ✓ 2026-09-17
- [x] **110.4 — Documentation**: §9.1, §9.2, §9.3, §9.7, §13.2, §13.3 and §15
  amended, dated; `docs/reference/api.md`, `docs/guides/tui.md`,
  `CHANGELOG.md`; task 066's follow-up 2 annotated. No `docs/assets/tui-*.png`
  changes: `scripts/screenshots.sh` seeds through the fake agent, whose edits
  report no `structuredPatch`. ✓ 2026-09-17
- [ ] **110.5 — Owner walkthrough**: open a real claude run with edits at each
  of the four levels, and record the result below.

## What the tests prove

- `internal/agent/claude/edit110_test.go`, off the fixture: the exact `+N −M`
  for the single-hunk, two-hunk and `replace_all` edits, context lines not
  counted; the exact unified text of one patch and its `CallID`; `update` →
  `updated` with a delta and a patch; `create` → `created` with its prose and
  no patch; the failed and the subagent's edit keeping their prose with no
  patch; an over-cap patch truncated on a rune boundary with its delta counted
  whole; an empty patch inventing no delta; the three `2.1.226` fixtures
  producing no patch and no new verb.
- `internal/agent/codex` and `internal/agent/cursor` (`patch110_test.go`):
  over every fixture, no event carries a patch.
- `internal/agent/chunk_test.go`: the `agent.patch` chunk's keys, and a result
  with a patch publishing result then patch.
- `internal/api/transcript_patch_test.go`: over the fixture, every outcome and
  patch record equals the live chunk for the same line, in order; one line
  yields both records; a create yields no `agent.patch` and no `patch` key.
- `internal/tui/patch110_test.go`, through the real transcript handler and
  `apiclient`: the delta outcome at compact, normal and verbose and not at
  quiet; the patch at verbose only; compact, normal and quiet no longer with
  the patch records than without; a nested patch at no level; the Diff tab's
  styles, indentation kept, a long line wrapped at 80 columns without losing a
  character; the truncation line.
- `internal/cli/transcript_patch_test.go`: the outcome prints `< +N −M`, the
  body is skipped, and `--json` carries the four records.

As with T4.16, 066.5 and 109, these prove the parser and the wire, not whether
the pane is readable. 110.5 is the leg that closes it.

## Walkthrough record

| Date | By | Levels walked | Result |
|---|---|---|---|
| — | — | — | not yet walked |
