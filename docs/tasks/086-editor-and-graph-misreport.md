# 086 — The editor and the graph misreport a workflow

**Status:** ✅ done (2/2)
**Issue:** #320 (with [087](087-editor-cannot-write.md), which is the write half)
**Amends:** nothing. No asserted behaviour changes — this is the read path
telling the truth about a file it was already showing
**Keeps, without relitigating:** [065](065-workflow-editor-in-the-tui.md)
decision 3 (the forms are rendered from `GET /v1/workflows/schema`, never from a
second copy of §8.2 in the client) and 065's *"editing from the graph stays out
of scope"*

The structured editor's value column was a 17-case switch over field name.
Twelve fields the served descriptor publishes had no case and rendered
`(unset)` however the file spelled them — `max_retries`, `retry_backoff`,
`timeout`, `allow_failure`, `input_timeout`, `check_timeout`, `env`,
`max_parallel`, `count`, `for_each`, `max_iterations`, `schedule`. The
shipped `github-resolve-issue-dag` sets two of them on its first step, so the
form misreported the very file it was most likely to be opened on.

The graph had the same fault from the other end. `apiclient.WorkflowStepDef`
carried `Lanes` but neither `Lane` nor `MaxLanes`, which the API had served
since [080](080-fan-out-dag.md); `diagram.go` sourced its columns from
`st.Lanes` alone, so a fan-out that declares a single `lane:` template drew a
heavy frame with nothing inside it — a picture that says the step spawns
nothing.

## Decisions

1. **The value column is a table keyed by schema field name, not a switch.**
   *2026-09-03.* A switch is a list of what someone remembered; a table can be
   walked. A test walks the served descriptor — every `TopLevel`, `Common` and
   per-type field — and fails when a published field has no reader, so the next
   field added to §8.2 cannot render blank in silence. The structural controls
   (`steps`, `lanes`, `lane`, `merge`, `fields`, `container`) carry a **named
   exemption** rather than being skipped by shape: "no reader" is then a
   decision on the record and not an oversight.

2. **The client's wire types are checked against the parser's, by reflection.**
   *2026-09-03.* `internal/api`'s DTO restates `internal/workflow` and
   `TestWorkflowDefinitionCoversEveryField` holds *that* copy honest; nothing
   walked the second hop to `internal/apiclient`, which is exactly how `lane:`
   and `max_lanes:` reached the wire and never reached a client. The new guard
   reflects over `internal/workflow` directly rather than over the server's
   unexported DTO — the model is the authority for both hops, and the server's
   DTO is unexported precisely so it stays the server's own. Alternative
   rejected: pinning keys by hand in a decode test, which is what
   `TestStepDefDecodesTheLaneDAG` already did and what missed `lane:`.

3. **A `lane:` template is drawn as one column, and never as a count.**
   *2026-09-03.* The alternative — leaving the frame empty until the step runs —
   is what the bug was. The column stands for the list the template becomes;
   the template's own `needs:` is dropped from the drawn copy, never from the
   DTO, because the lanes it would point at do not exist yet. The frame is
   captioned `templated from …` rather than `derived from …`: which of the two
   a reader is looking at is the half they cannot recover from anywhere else on
   the picture. `max_lanes:` is named because it is the one bound the file
   states; no count is invented, for the reason `for_each` on a loop gets none.

## Phases

- **086.1 — The editor reads and addresses every block.** ✅ The keyed reader
  table (`internal/tui/workfloweditorvalues.go`), the descriptor-walking
  coverage test with its named exemptions, `Lane`/`MaxLanes`/`Container` on
  `apiclient.WorkflowStepDef` and `WorkflowDefaults`, and the reflection parity
  guard (`internal/apiclient/workflowparity_test.go`).
- **086.2 — The graph draws a derived fan-out.** ✅ `fanOutNote`/`templateNote`,
  the single-column render, the `lane` row in the step detail, and the merge
  node's "every lane the step derives".
