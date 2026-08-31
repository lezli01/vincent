# 066 — Surface more of claude's stream-json in the output view

**Status:** 🔄 in progress (4/5) · **Issue:**
[#267](https://github.com/lezli01/vincent/issues/267)
· **Spec:** §9.2, §9.3, §9.7, §13.2, §13.3, §15

`internal/agent/claude/stream.go` read a deliberately narrow subset of claude's
`stream-json`. `parseLine` recognized three line types; the `system`/`init` line
fell through to `EventUnknown`; `parseResult` read five of the result line's ~20
fields; and `parseToolResults` read the `tool_result` blocks inside
`message.content` and never touched the sibling `tool_use_result` /
`tool_result_meta` objects.

The consequence was visible in the detail view's output pane. A reader watching
a run could not see how long it took, how many turns it burned, why it stopped,
what it was refused permission to do, or how much of the token spend was cache
reads. At `verbose` those lines existed only as raw JSON behind `agent.raw`,
which is a debugging affordance rather than a rendering.

Everything the issue said was thrown away was in fact thrown away, and the
fixtures already carried it. Each of the three
`internal/agent/claude/testdata/stream_*_2.1.226.jsonl` files is one
`system/init`, four `assistant`, one `control_request`, one `user` and one
`result/success`. `stream_permission_deny_2.1.226.jsonl` additionally carries a
one-element `permission_denials[]` and a `tool_result_meta` of
`[{"id":…,"non_execution_kind":"permission-rule"}]`.

Nothing here contradicts a recorded decision. It is the deliberate
*continuation* of two of them: T4.14's tool subjects and T4.16's
outcomes-not-bodies rule, both of which are respected — the tool's output body
stays out of the normalized stream, and the transcript remains the durable copy.

## Decisions (2026-08-31)

**1. The new records appear at `normal` and `verbose`; `compact` does not
grow.** The issue said the run header and result metadata "belong at `normal` or
below" and *also* that "`compact` does not grow" — and compact *is* below
normal. The acceptance criterion wins. `levelCompact` keeps its stated meaning
("what the agent said and did, nothing else"), the run header and a condensed
result line render from `levelNormal` up, and the per-model and cache-token
breakdown is `levelVerbose` only. `TestResultMetadataByLevel` asserts the
compact line byte for byte against what it rendered before this task.

The alternative — one more level, or a fourth record class — was rejected: `v`
cycles three levels and "show me more" is one intention (T4.16).

**2. `parent_tool_use_id` is carried on the wire; subagent nesting is not built
here.** The field is `null` on all eight lines of all three captured fixtures,
so there is no evidence to test a nesting renderer against, and nesting would
amend §15's flat two-column gutter model — the one thing T4.16 built the pane
around. The parser reads the field, it reaches the normalized record and the
live chunk, and the pane keeps rendering flat. Capturing a `Task` run and
designing the tree is follow-up work in its own document; because §13.2
re-normalizes on every read, transcripts recorded before that lands will render
under it.

**3. Tool outcomes get a verb, not a diff delta.** `ToolResult.Summary`'s doc
comment promised "created (+1 −0)", but the only fixture with a structured
`tool_use_result` is a *create* whose `structuredPatch` is `[]`; no capture in
the repo contains an edit with hunks. Implementing `+N −M` against the
documented shape is precisely what T4.17 rejected — a wrong guess fails
silently, the delta simply never appears, and nothing distinguishes that from a
file that did not change.

So the verb comes from `tool_use_result.type`, through a table holding exactly
the types a capture has shown (`create` → `created`); an unobserved type yields
no verb rather than a guessed past tense. A
`tool_result_meta[].non_execution_kind` of `permission-rule` renders as a
blocked call (`⊘`) rather than as an error string (`✗`). Deltas wait for a
fixture that has one. The doc comment that made the unkept promise was corrected
in the same change, in `internal/agent/agent.go` and in §9.1's type listing.

**4. No migration; nothing is persisted on `step_runs`.** The durable half is
split out, to be opened when a consumer asks for it. Three reasons, and the
first decides it:

- Migrations are append-only by convention, so a speculative column is expensive
  to take back.
- `step_runs` already carries `started_at`/`finished_at`, so a persisted
  `duration_ms` would be a *second* duration that disagrees with vincent's own
  (claude's excludes what §7.4 input waits add to ours).
- The columns would be claude-only in a table that is otherwise adapter-neutral,
  read by no named client — the pane reads the transcript, which §13.2
  normalizes on read.

This task is parser, wire and pane. There is consequently **no §14 amendment**.

**5. Both mappings move together.** The issue listed `internal/api/transcript.go`
and the TUI. There is a second mapping: `Runner.publishAgentEvent`
(`internal/taskrun/steps.go`) produces the §13.3 live chunks, and the spec
records that the two are kept in agreement deliberately — a difference shows up
as output that changes when a step finishes. The run header is published there
too, and `toolChunks`/`resultChunks` gained the same fields `toolLines`/
`resultLines` did.

`publishAgentEvent` deliberately drops `EventResult` (results surface as step
outcomes), so the enriched result metadata reaches the pane on scrollback only.
That asymmetry is pre-existing and this task does not change it.

**6. `modelUsage` carries what the run spent, not what the model is.** Claude's
payload also names `contextWindow`, `maxOutputTokens`, `canonicalModel` and
`provider`. Those describe the model rather than this run, and §9.6's option
catalog is where a fact about a model belongs, so they are read by nothing. A
field on the wire that no client can use is a field that will be wrong later.

## Task list

- [x] **066.1** `internal/agent`: `EventRunHeader` and `RunHeader`;
  `Event.ParentCallID`; `ToolResult.Verb`/`.Blocked`; `RunResult`'s durations,
  turns, stop/terminal reason, cache counts, `ModelUsage` and
  `PermissionDenials`. Every one documented as empty-for-adapters-that-do-not-
  report-it, in the shape `RunResult.SessionID` and `.Failure` already use.
- [x] **066.2** `internal/agent/claude/stream.go`: the `system`/`init` arm, the
  result line's metadata, the structured tool outcome (probed as object *or*
  string — the deny fixture's is the bare string `"Error: no user is available;
  permission denied"`), and `parent_tool_use_id`. Codex and cursor untouched.
- [x] **066.3** The wire, in one commit: `internal/api/transcript.go`'s
  `agent.run_header` record and enriched `agent.result`,
  `internal/taskrun/steps.go`'s matching live chunks, and
  `internal/apiclient/transcript.go` so `vincent task transcript` renders them.
- [x] **066.4** `internal/tui`: the run-header line, the level-aware
  `resultOutcome`, the `⊘` blocked mark, and the verbose-only breakdown — all
  inside §15's two-column gutter scheme, with no timestamps.
- [ ] **066.5** Owner walkthrough against a real claude run, recorded below.

## What the tests prove

- `internal/agent/claude/stream_test.go`, off the existing `2.1.226` fixtures:
  the result line's duration, API duration, turns, stop and terminal reason,
  cache counts, per-model usage and the deny fixture's single
  `permission_denials` entry; the init line normalizing to a run header carrying
  `cwd` and its five tools, exactly once per run; the allow fixture's
  `tool_use_result` yielding the `created` verb; the deny fixture's
  `non_execution_kind: permission-rule` yielding a blocked outcome rather than
  an error string, *and* its string-shaped `tool_use_result` not derailing the
  object decode; an unobserved `tool_use_result.type` yielding no verb;
  `parent_tool_use_id` read as empty across all three captures, which is what
  they contain, and read correctly on a synthetic line that has one.
- A `system` line with an unknown `subtype` and a line with an unmodelled
  `type`, both still `EventUnknown` with `Raw` intact — the phase 1
  tolerant-parsing decision, asserted rather than assumed.
- `internal/taskrun/chunks_test.go` — the header chunk's shape and the result
  chunk's new keys, extending the parity test T4.16 wrote for exactly this.
- `internal/api/transcript_test.go` — the new wire names round-trip, *and* an
  adapter that reports none of them emits none of the keys, so a client can
  tell "unreported" from "zero".
- `internal/tui/outputlines_test.go` — the new records at all three levels,
  including that `levelCompact`'s result line is byte-identical to what it was,
  and that a header whose tool list overflows 80 columns wraps to the hanging
  indent rather than clipping.
- `internal/agent/codex` and `internal/agent/cursor` —
  `TestNoRunHeaderOrResultMetadata` states positively, over every existing
  fixture, that neither adapter produces a run header or fills any new field.

The unit tests are necessary and **not sufficient.** T4.16's own record is
explicit that every test it wrote proves a *parser* and none of them proves a
*pane*, and its acceptance was the owner walking the TUI against a real claude
run — the same reason `scripts/m3-gate.sh` seeds instead of asserting. This
change adds two record shapes to that pane, so 066.5 is the leg that closes it.

## Walkthrough record

| Date | By | Result |
|---|---|---|
| — | — | not yet walked |

## Follow-up work this task creates

1. **Subagent nesting.** Capture a claude run that uses the `Task` tool, design
   the pane's tree within §15's model, amend §15. The wire field is already
   there and already recorded.
2. **Edit deltas.** Capture a run containing an `Edit` with a non-empty
   `structuredPatch`, then make `ToolResult.Summary` keep the promise its doc
   comment made from T4.14 until this task removed it.
3. **Durable result metadata on `step_runs`**, if and when a client needs it off
   `/v1/tasks` rather than off a transcript (decision 4).
