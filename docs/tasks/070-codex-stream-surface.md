# 070 — Extend codex output parsing to the full exec event schema, and support thread resume

Issue [#268](https://github.com/lezli01/vincent/issues/268). Spec §9.1, §9.3,
§13.2, §13.3, §15, §20.

The codex counterpart of task 066 / issue #267, and deliberately its shape:
parser, wire, pane, no `step_runs` migration, and an owner walkthrough as the
leg that closes it. `internal/agent/codex/stream.go` read the subset of
`codex exec --json` real captures existed for; the upstream schema had grown
past it, so a codex step's pane was thinner than a claude step's and thinner
than what codex actually reported.

## Decisions

1. **The new record types join the shared §13.2/§13.3 vocabulary; codex is
   simply the first adapter to fill them.** The issue proposed codex-only
   record types and recorded the cost; that is overturned. Task 066 landed
   `agent.run_header` three days earlier as a shared type only claude fills,
   with `TestNoRunHeaderOrResultMetadata` stating positively over every codex
   and cursor fixture that neither produces one — the project already has a
   settled answer for "one dialect reports something the others do not", and it
   is not a per-adapter namespace. The two new types are `agent.plan` and
   `agent.command_output`; claude and cursor gained the mirror negative.
2. **Command output renders at `verbose` only, under a cap carried on the
   record.** `aggregated_output` is the tool's output body — the exact thing
   `ToolResult` refuses to carry. It rides on its own record type, and the cap
   (`agent.CommandOutputMax`) lives in `internal/agent` the way `toolSummaryMax`
   does, so every client inherits the guard. Truncation is visible, never
   silent. A codex step running `go test ./...` must not be able to flood the
   pane at the level most readers use.
3. **Two pull requests under one task document.** The parsing/wire/pane half
   and the resume half share one capture session, which is why they share a
   document. *Amended on landing:* both halves landed in **one** PR — the work
   ran in a single branch, and splitting a branch into two PRs after the fact
   buys a reviewer nothing the two commits do not. The decision's substance —
   that the resume half is the only one that changes behaviour outside the
   adapter — is recorded in the PR body instead.
4. **`file_change` and `mcp_tool_call` summaries are built by the codex
   adapter, not by widening `toolSummaryKeys`.** `changes` is an array of
   objects, which `ToolSummary` cannot read at all, and `server`/`tool` are
   codex-shaped names in a list whose stated design is "names that converge
   because the underlying tools do". Adding them would make that comment false.
5. **Acceptance for the resume leg is a gate leg *and* an owner walkthrough.**
   Fakeagent can prove vincent hands the thread id back as argv; it cannot
   prove codex resumed anything. So `m14`'s refusal leg moved to cursor, a
   fakeagent codex-resume scenario proves the plumbing on all three platforms,
   and the real-CLI capture (`testdata/resume_0.150.1.jsonl`) is the recorded
   walkthrough: the resumed turn reports the **same** `thread_id` and answers a
   question about the previous turn.
6. **Capture against 0.150.1, and defer what cannot be captured.** §9.3's
   verified-build list grew to `0.142.5, 0.147.0, 0.150.1`. Item types no real
   run could be made to produce are **not** implemented from the upstream
   schema; they are named in §9.3 as deferred with the fixture requirement
   attached — the rule that kept codex reasoning unimplemented until
   `testdata/reasoning_0.147.0.jsonl` existed. Three legs deferred that way on
   landing: `item.updated` on a running `command_execution` (0.150.1 goes
   `started → completed` even for a half-minute command),
   `mcp_tool_call.error.message` (the capture reports `error: null` on a call
   the same line marks `status: "failed"` — the server's explanation came back
   inside `result`), and `collab_tool_call` / `web_search.action`.

## Tasks

- [x] **070.1** `internal/agent`: `EventPlan`/`Plan`, `EventCommandOutput`/
  `CommandOutput`, `Event.Output`, `RunResult.ReasoningOutputTokens`,
  `CommandOutputMax` and `TruncateOutput`.
- [x] **070.2** `internal/agent/codex`: the `item.updated` arm, `todo_list`,
  adapter-built `file_change` and `mcp_tool_call` summaries,
  `aggregated_output`, the three remaining `usage` fields, `thread.started`
  into `RunResult.SessionID`. Captures: `plan_0.150.1.jsonl`,
  `mcp_0.150.1.jsonl`, `resume_0.150.1.jsonl`.
- [x] **070.3** Wire: `internal/api/transcript.go` (one line may now yield two
  records), `internal/taskrun/steps.go` live chunks, `internal/apiclient`,
  `internal/cli/transcript.go`, `internal/tui`.
- [x] **070.4** Resume: `exec resume <id>` argv, `SupportsResume()` true,
  fakeagent's codex dialect, `scripts/m14-gate.sh` (refusal leg to cursor, new
  codex resume leg).
- [x] **070.5** Docs: §9.1, §9.3, §13.2, §13.3, §15, §20; task 063 decision 3's
  dated note; `CHANGELOG.md`; `docs/reference/api.md`.
