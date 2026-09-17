# 109 — Render claude subagent runs nested in the output pane

**Status:** 🔄 in progress (4/5)
**Issue:** [#401](https://github.com/lezli01/vincent/issues/401)
**Spec:** amends §9.1, §9.2, §9.3, §9.7, §13.2, §13.3, §15
**Follow-up to:** [066](066-claude-stream-json-surface.md), follow-up 1

## Problem

Task 066 decision 2 put claude's `parent_tool_use_id` on the wire as
`Event.ParentCallID`, and on the §13.2 record and the §13.3 live chunk as
`parent_call_id`. It chose not to render it: none of the three `2.1.226`
fixtures had a subagent line, so there was nothing to test a tree against. §9.1
said the field was "**not rendered**", and §15 said nesting was "its own design
problem with its own capture".

So nothing rendered nesting. The claude parser read the field,
`internal/agent/chunk.go` attached it to chunks, `internal/api/transcript.go`
emitted it and `internal/apiclient` decoded it, but no consumer in
`internal/tui` or `internal/cli` read it. A subagent's prose, reasoning and
tool calls rendered flat, as if the main agent had produced them.

This does not relitigate a recorded decision: task 066 decision 2 deferred the
work to this follow-up. Nesting does amend T4.16's flat two-column gutter model
(`docs/history/v0-tasks.md`). That is why §15 is amended explicitly and dated
here rather than changed silently. The owner agreed to the amendment.

## Evidence

The issue asked for a capture of a run using the `Task` tool. No fresh capture
was needed: vincent's own recorded transcripts on the owner's machine held 16
real claude runs with subagents, from 2.1.251 to 2.1.268. They corrected
several assumptions.

- **The spawning tool is named `Agent`, not `Task`,** in every captured call,
  although the `system`/`init` line's tool list still names it `Task`.
- **Subagents run concurrently, and their lines interleave** with each other and
  with the main loop, which keeps working while background agents run. Several
  runs show two or three agents alternating line by line.
- **Two completion shapes exist.**
  - *Synchronous* (2.1.251, 2.1.263, `run_in_background: false`): the `Agent`
    call's `tool_result` arrives after the children. Its `tool_use_result` has
    `status: "completed"`, the report, `totalToolUseCount` and
    `totalDurationMs`.
  - *Async* (the 2.1.268 default): the `tool_result` arrives immediately with
    `tool_use_result.status: "async_launched"`. Its text is "Async agent
    launched successfully. (This tool result is internal metadata — never
    quote…)", which is what the pane showed as the outcome.
- **claude emits `system` task lines,** which vincent dropped as `agent.raw`:
  - `task_started` has `task_id`, `tool_use_id`, `description`,
    `subagent_type`, `is_backgrounded`, `spawn_depth` and
    `task_type: "local_agent"`.
  - `task_progress` arrives after every one to three child lines, with running
    `usage` (`total_tokens`, `tool_uses`, `duration_ms`) and `last_tool_name`.
  - `task_notification` comes for **both** sync and async agents, with
    `status`, `summary` (the subagent's whole final report) and `usage`.
  - The same subtypes also carry `task_type: "local_bash"` background shells,
    the far more common case, including shells a subagent started
    (`owned_by_subagent: true`).
- **Only `task_started` names its `task_type`.** A subagent's `task_progress`
  carries `subagent_type` (no shell's progress line has been seen). A
  subagent's `task_notification` never carries `task_type`, and it carries
  `usage` when `completed` but **no `usage` when `failed`**. A failed
  subagent's notification therefore looks exactly like a background shell's.
  This finding was made during implementation and is decision 7.
- **Observed statuses.** A subagent's notification has been `completed` or
  `failed`. `stopped` was observed on background-shell task lines only.
- **`spawn_depth` is 1 in every observed run.** No subagent was seen spawning
  another.

## What shipped

- `internal/agent/agent.go`: `EventSubagentStarted` (`subagent_started`),
  `EventSubagentProgress` (`subagent_progress`) and `EventSubagentFinished`
  (`subagent_finished`), each carrying `Event.Subagent`, an `agent.Subagent`
  with `CallID`, `Description`, `AgentType`, `Background`, `Status`, `Summary`,
  `ToolUses`, `TotalTokens`, `Duration` and `LastTool`. They are empty for an
  adapter that does not report subagents. Only claude produces them.
- `internal/agent/claude/stream.go`: a per-stream parser that normalizes the
  three `local_agent` task lines (decision 7), the `async_launched` outcome
  (verb `started in background`, no summary), and a finish summary capped at
  one line of 120 runes.
- The wire, both mappings together (task 066 decision 5):
  `internal/api/transcript.go`'s records, `internal/agent/chunk.go` and
  `internal/taskrun/steps.go` `publishAgentEvent`'s live chunks, and
  `internal/apiclient/transcript.go`'s `TranscriptRecord` fields
  (`Description`, `SubagentType`, `Background`, `Status`, `ToolUses`,
  `TotalTokens`, `LastTool`).
- `internal/tui/outputlines.go` and `internal/tui/assistantdoc.go`: the rail,
  the switch label, the one-level-quieter rule, the completion line, and the
  Markdown document boundary. Both workspaces render through `outputLinesAt`,
  so the chat pane has the same rail.
- `internal/cli/transcript.go`: the ASCII rail, label and completion line for
  `vincent task transcript` and `vincent chat transcript`.
- Fixtures, trimmed from recorded runs, with paths under `C:\work\…` and
  prompts and summaries sanitized, the line structure and field names
  verbatim, and no fresh API spend:
  - `internal/agent/claude/testdata/stream_subagent_async_2.1.268.jsonl`: two
    parallel async agents whose children interleave with each other and with
    main-loop lines, with `task_started`, `task_progress`, `task_notification`,
    the `async_launched` results, and a subagent-owned `local_bash` start and
    notification.
  - `stream_subagent_sync_2.1.263.jsonl`: one synchronous agent whose
    `tool_result` carries `status: "completed"`, with a subagent-owned
    background shell and a `task_updated` line.
  - `stream_subagent_failed_2.1.268.jsonl`: an async agent whose notification
    is `failed` and carries no `usage`.

## Decisions

All dated 2026-09-17, settled with the owner, and binding.

### 1. Layout: a chronological rail

Each subagent record stays exactly where it arrived. It is drawn behind a
two-column rail, `┊ `, with the record's own gutter composed after it: a nested
tool call is `┊ ▸ `, its outcome `┊     ✓ `, and nested prose `┊ ` followed by
the prose. Wrapped continuation lines keep the rail.

When the rendered stream moves into a subagent, or from one subagent to another,
a label line `┊ ↳ <description>` names which one. The description comes from
`agent.subagent_started`, falling back to the spawning `agent.tool_use` call's
summary, then to a plain `subagent`. The label is emitted only if the child
record after it renders at the current level, so a level never leaves a
dangling label. Any rendered main-loop line ends the rail, so the next child
line gets a label again.

**Beat:** grouping children under their spawning `▸ Agent` line. With
interleaved concurrent agents, live lines would land above the tail, and
`vincent task transcript --follow` would have to buffer each subagent until it
finished.

**Beat:** collapsing a subagent to one summary line, which gives up the nesting
the issue asks for.

### 2. Levels: one level quieter

A record carrying `parent_call_id` renders at level L only where a main-loop
record of its type renders at L−1:

| Level | A subagent shows |
|---|---|
| `quiet` | nothing of its internals |
| `compact` | its prose and its errors |
| `normal` | also its tool calls and their outcomes |
| `verbose` | also its truncated reasoning, its plan, and a count of its unrecognized lines |

Two consequences are stated rather than left implicit. A subagent's
`agent.command_output` is `verbose`-only at top level, so it **never renders
nested**; the transcript, `e`, `--raw` and `--json` keep it. A subagent's
`agent.raw` lines are **never shown whole** in the pane.

The subagent's spawn line and completion line are main-loop records. They
follow the top-level tool-call rules: hidden at `quiet`, shown from `compact`
up. `vincent task transcript` has no levels and prints the pane's `normal`
content, as it already does for the run header (issue #371): nested prose, tool
calls, outcomes and errors, on an ASCII rail with a label.

**Beat:** the main loop's own rules, under which subagent prose would still
read as the agent's at `quiet`.

**Beat:** internals only at `verbose`, under which the CLI would show none of
them.

### 3. Wire: model the `local_agent` task lines

The claude parser normalizes `task_started`, `task_progress` and
`task_notification` into the three new event types, keyed by the spawning
call's id (`tool_use_id`):

- *started*: description, subagent type, backgrounded.
- *progress*: tool uses, tokens, duration, last tool.
- *finished*: status, usage, and a summary capped at the adapter to one line of
  120 runes.

The final report body stays out of the normalized stream (T4.16
outcomes-not-bodies); the transcript holds it verbatim. Both mappings move
together, as task 066 decision 5 requires. The §13.2 records and §13.3 chunks
are `agent.subagent_started`, `agent.subagent_progress` and
`agent.subagent_finished`. None carries `parent_call_id`: they are main-loop
lines about a sub-run.

How each renders:

- *started*: no line of its own. It supplies the rail label's description.
- *progress*: **never renders a line**, in the pane or the CLI. It is
  normalized only so that a progress line after every few child records does
  not become an `… N unrecognized line(s)` note between them at `compact` and
  `normal`. A visible progress line was rejected for the same flood reason.
- *finished*: the completion line, on the rail, with status, description, tool
  uses and duration where reported: `┊ ✓ completed · <description> · 14 tool
  uses · 5m0s`. `failed` is `┊ ✗ failed · …`. `stopped` is `┊ ■ stopped · …`,
  because "the agent was stopped" and "the agent failed" send a reader to
  different places, the same reasoning as `⊘`.

The async launch outcome renders as `✓ started in background`, from
`tool_use_result.status == "async_launched"`, instead of claude's internal
metadata text. Only observed status values get wording; any other status
renders as `┊ · <status> · …` with no special wording (the T4.17 rule).
`local_bash` task lines stay `agent.raw`, out of scope and unchanged.

**Beat:** `parent_call_id` only, under which an async subagent would have no
end and no status.

**Beat:** all task lines generically, which widens scope to background shells
nobody asked for.

### 4. Fixtures: trimmed from recorded real runs

The fixtures are minimal excerpts of the owner's recorded vincent transcripts,
which are real `claude -p` stream-json, named with the CLI version. Paths,
prompts and summaries are sanitized the way the existing fixtures use
`C:\work\repo`. The line structure and field names stay verbatim.

**Beat:** a fresh capture, which spends API quota to reproduce shapes already on
disk.

### 5. Depth is one

Only `spawn_depth: 1` has been captured. A record whose parent is itself a
child call renders on the same single rail, not a deeper one. A second rail
waits for a capture that has one.

**Beat:** a rail per depth, designed against a shape no capture has shown.

### 6. Tool names are never matched

Neither `Task` nor `Agent` appears in parsing or rendering logic. Attribution is
`parent_call_id` and the task lines' `tool_use_id`. The captures justify this:
the init line's tool list says `Task` while every call says `Agent`.

**Beat:** keying on the tool name the issue expected, which the first capture
already contradicted.

### 7. The claude parser is stateful per stream

Found while implementing decision 3. Only `task_started` names its `task_type`,
and a failed subagent's `task_notification` carries neither `task_type` nor
`usage`, so on its face it is indistinguishable from a background shell's. The
parser therefore remembers, per stream, every call id it has seen acting as a
subagent: a `local_agent` `task_started`, a `task_progress` (seen only for
subagents, carrying `subagent_type`), and any line's `parent_tool_use_id`. It
treats a `task_notification` as a subagent's when its call id is remembered or
when it carries `usage`. `NewLineParser` hands back a fresh parser per call;
before this the dialect was stateless and it returned one pure function.

The cost is stated rather than hidden. A transcript range fetched with `tail=`
or `offset=` that opens after every line of a subagent can leave that
subagent's `failed` notification as `agent.raw`. A `completed` one is still
recognized by its `usage`. Live chunks are unaffected: one parser reads the
whole run.

**Beat:** keying on `task_type`, which only the start line carries, so no
progress or finish line could be recognized.

**Beat:** treating every notification with a `tool_use_id` as a subagent's,
which reports background shells as subagents finishing and widens scope the way
decision 3 rejected.

**Beat:** recognizing a notification by `usage` alone, under which a failed
subagent, the ending a reader most needs to see, never gets a completion line.

## Work

- [x] **109.1 — Wire and parser**: the event types and `agent.Subagent`; the
  claude parser's task-line arms, per-stream memory, capped summary and
  `async_launched` outcome; the three fixtures; the §13.2 records, §13.3 chunks
  and `apiclient.TranscriptRecord` fields; codex and cursor stated, over every
  existing fixture, to produce no subagent event. ✓ 2026-09-17
- [x] **109.2 — The pane**: rail and label composition, the one-level-quieter
  rule (including the `agent.raw` run count and the thinking block), the
  completion line, the Markdown document boundary on a change of
  `parent_call_id`, and main-loop-only prose counting toward the result line's
  de-duplication, in both workspaces. ✓ 2026-09-17
- [x] **109.3 — The CLI**: the ASCII rail, label and completion line in
  `vincent task transcript` and `vincent chat transcript`, with `--json` and
  `--raw` unchanged. ✓ 2026-09-17
- [x] **109.4 — Documentation**: §9.1, §9.2, §9.3, §9.7, §13.2, §13.3 and §15
  amended, dated; `docs/guides/tui.md`, `docs/reference/api.md`,
  `docs/reference/cli.md`, `docs/guides/agents.md`, `docs/features.md`,
  `CHANGELOG.md`; task 066's follow-up 1 annotated. No
  `docs/assets/tui-*.png` changes: `scripts/screenshots.sh` seeds through the
  fake agent, and no shot shows a subagent run. ✓ 2026-09-17
- [ ] **109.5 — Owner walkthrough**: watch a real claude run with parallel
  background agents in both the task workspace and the chat workspace, at each
  of the four levels, and record the result below.

## What the tests prove

- `internal/agent/claude/subagent109_test.go`, off the new fixtures: children
  carry the spawning call's id, main-loop lines carry none, and the two
  background agents interleave; the three task lines normalize with
  description, usage, status and the capped summary; the failed notification
  without `usage` is recognized by its remembered call and stays raw on a cold
  parser; `local_bash` lines and other `system` subtypes stay `EventUnknown`
  with `Raw` intact; the async result yields `started in background` and the
  sync result does not. `stream_test.go`'s assertion that the three `2.1.226`
  fixtures carry no parent still holds.
- `internal/agent/codex` and `internal/agent/cursor` (`subagent109_test.go`):
  over every existing fixture, neither produces a subagent event or a parent.
- Wire parity: `internal/agent/chunk_test.go` pins the three chunk shapes, and
  `internal/api/transcript_subagent_test.go` shows, over all three captures,
  that every subagent and child record the transcript route writes equals the
  live chunk published for the same line, pins the record names, and shows an
  adapter reporting none emits none of the keys.
- `internal/tui/subagentrail_test.go`: the rail and label at all four levels;
  a label on every switch between concurrent subagents and never without a
  rendered child after it; the completion line in each status; main-loop
  records between children not railed; child and main prose never one Markdown
  document; wrapping keeping the rail; a subagent's raw lines counted only at
  verbose; the `2.1.226` captures rendering byte for byte (styling stripped) as
  the renderer did before the rail, against goldens it wrote
  (`internal/tui/testdata/flat_*.txt`). One chat-renderer test shows the same
  rail.
- `internal/cli/transcript_subagent_test.go`, through the real handlers: the
  ASCII rail, label and completion line off the captures, the failed mark, and
  `--json` / `--raw` unchanged.
- `internal/tui/subagentlive_test.go`, through the real handlers and broker: a
  fetched subagent and one published live both reach the pane with
  `parent_call_id` and the subagent fields intact.

As with T4.16 and 066.5, these prove a parser or a layout, not whether the pane
is readable. 109.5 is the leg that closes it.

## Walkthrough record

| Date | By | Workspace | Levels walked | Result |
|---|---|---|---|---|
| — | — | task | — | not yet walked |
| — | — | chat | — | not yet walked |
