# 108 — Read cursor's run header and result metadata

**Status:** ✅ done (4/4)
**Issue:** [#400](https://github.com/lezli01/vincent/issues/400)
**Spec:** amends §9.1, §9.2 (a note), §9.7

## Problem

Task 066 added a shared run header (`agent.run_header`: `work_dir`,
`available_tools`) and the result line's own account of a run — durations,
turns, stop and terminal reasons, cache counts, per-model usage, permission
denials — to the adapter-neutral vocabulary, and filled it for claude only.
Task 070 filled codex's share. Cursor was left out on purpose, and §9.7 said so
as scope rather than absence: "the shared vocabulary was designed so cursor can
fill these later without another wire change".

The data was already on disk. Every cursor fixture — `success_2026.08.04`,
`tools_2026.08.11`, `resume_2026.08.11`, `resume_unknown_2026.08.11`, all real
captures — has `cwd` on its `system`/`init` line and `duration_ms`,
`duration_api_ms`, `usage.cacheReadTokens` and `usage.cacheWriteTokens` on its
`result` line. `internal/agent/cursor/stream.go` sent `system` lines to
`EventUnknown` and read only `inputTokens`/`outputTokens` off `result`.

No recorded decision stands in the way. Task 066 decision 4 (nothing persisted,
no migration) still holds, the phase 1 tolerant-parsing rule still holds —
unmodelled lines and `system` subtypes stay `EventUnknown` with `Raw` intact —
and T1.7 (no state-file parsing) is unaffected, because only stdout stream lines
are read. No client changes: the TUI's `runHeaderLine` already renders a header
with a directory and no tool list, the CLI's `agent.run_header` arm does too,
and both result renderers key on non-zero fields.

## Decisions

All dated 2026-09-17, settled with the author, and binding.

1. **A fresh capture against cursor-agent `2026.08.25-3e8eec8`, which then
   counts as a tested build.** It is the build installed and logged in on the
   owner's machine. It joins `testedVersions`, as task 070 added codex 0.150.1
   (task 041's table). The 2026.08.04 and 2026.08.11 fixtures stay and are not
   re-captured; the new tests run over the old captures and the new ones.
   **Beat:** reading the old captures alone, which would pin the parser to
   builds nobody runs any more without saying whether the current one still
   has the fields.
2. **No owner walkthrough leg.** No TUI or CLI rendering code changes: cursor
   starts filling records claude already fills, and both clients already render
   them. Fixture-driven tests at the parser, API, CLI and TUI layers are the
   acceptance, and the task closes when the pull request merges.
3. **No wire change, no client code change, no migration.** Every field cursor
   fills already exists in `agent.RunHeader`, `agent.RunResult`, §13.2's records
   and §13.3's chunks. Durations and cache counts are still not persisted on
   `step_runs` (task 066 decision 4).
4. **The run header carries `WorkDir` and no tools.** Cursor's init line has no
   tool list, so `RunHeader.Tools` stays nil and nothing builds one from tool
   calls seen later. A `system` line with subtype `init` produces
   `EventRunHeader`, the way claude's `parseInit` does; any other `system`
   subtype stays `EventUnknown`. **Beat:** a tool list assembled from the
   `tool_call` lines, which would describe what the run used rather than what
   it could reach, and arrive long after the header.
5. **What cursor reports that is still not read.** On the init line, `model`
   (`"Auto"`), `permissionMode` and `apiKeySource`: `RunHeader` has no field for
   any of them, the issue rules out a wire change, and `apiKeySource` concerns
   authentication and has no place in a transcript record. On the result line,
   `request_id`.
6. **What stays zero for cursor, and nothing emulates.** `NumTurns`,
   `StopReason`, `TerminalReason`, `ModelUsage`, `PermissionDenials`,
   `ReasoningOutputTokens`, `ToolResult.Verb`/`.Blocked` and `ParentCallID`
   (§9.x rule).
7. **Cache counts are copied as reported.** `usage.cacheReadTokens` goes into
   `RunResult.CacheReadTokens` and `usage.cacheWriteTokens` into
   `.CacheCreationTokens`, with no arithmetic. `InputTokens`/`OutputTokens`
   stay as they are and cache traffic is never folded into them. The captures
   show no fixed relation between `inputTokens` and `cacheReadTokens` — the
   resume fixture has 92 against 35318, the 2026.08.04 success fixture 8274
   against 8000 — so neither the spec nor a doc comment says whether one
   includes the other. Task 070 gave codex's `cached_input_tokens` the same
   as-reported treatment. **Beat:** deriving an uncached input count, which
   would assert a relation no capture supports.
8. **Error results carry the metadata too.** Durations and cache counts are
   read whatever the result subtype, as claude's `parseResult` does. No cursor
   error-result capture exists, so this is tested on a synthetic line, and the
   §9.7 bullet saying errors arrive on stderr still stands.

## Out of scope

- A model field on the run header, or any other wire change.
- Subagent nesting (#401) and edit deltas (#402).
- Persisting result metadata on `step_runs`.
- Classifying cursor's usage-limit or unauthenticated wording (task 003).
- A cursor error-result capture.

## Tasks

- [x] **108.1 — Parser and capture**: `internal/agent/cursor/stream.go` reads
  `cwd` into the run header and the durations and cache counts into the
  result; `2026.08.25-3e8eec8` joins `testedVersions`; new fixtures
  `success_2026.08.25.jsonl` and `tools_2026.08.25.jsonl`, captured with the
  adapter's argv in throwaway git repos and scrubbed (`session_id` and
  `conversationId` → `SESSION`, `request_id` and `requestId` → `REQ`,
  `model_call_id` → `MC`, the repo path → `/tmp/wt`), durations and token
  counts as captured. The capture showed no renamed or missing field, and the
  thinking, `tool_call` and result arms all hold on it. Tests: the header and
  the metadata over every fixture, the other `system` subtype, the synthetic
  error result, `TestUnreportedMetadataStaysZero` (was
  `TestNoRunHeaderOrResultMetadata`, narrowed to decision 6), the tools test
  over both tool captures, the new build judged tested. ✓ 2026-09-17
- [x] **108.2 — The fake**: `cmd/fakeagent/cursor.go`'s init line carries the
  process's `cwd`, and its result carries `duration_api_ms` beside
  `duration_ms`. `m5` and `m14` pass unchanged. ✓ 2026-09-17
- [x] **108.3 — The other layers**: `internal/api` normalizes a cursor fixture
  to a `work_dir`-only header and a result carrying the durations and cache
  count with every unreported key omitted; `internal/cli` renders `# /tmp/wt`
  and `= done (2.0s)`, and codex alone stays in
  `TestTranscriptNoRunHeaderForAdaptersThatReportNone`; `internal/tui` renders
  a cursor fixture served by the real handler — compact unchanged, the header
  and elapsed time at normal, the API split and cache counts at verbose.
  ✓ 2026-09-17
- [x] **108.4 — Documentation**: §9.1's `RunResult` comment, a note on §9.2's
  duration paragraph, and §9.7's run-metadata, usage and version-verdict
  bullets, dated; `internal/agent/agent.go` doc comments;
  `docs/guides/agents.md`, `docs/guides/tui.md`, `docs/reference/cli.md`,
  `docs/reference/api.md`, `docs/features.md`, `CHANGELOG.md`. No
  `docs/assets/tui-*.png` changes: the seeded installation does run cursor
  steps, but no captured shot shows the output pane.
  ✓ 2026-09-17
