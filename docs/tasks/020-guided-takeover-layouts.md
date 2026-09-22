# 020 — Guided takeover layouts for task, project, and workflow views

**Status:** ✅ done (7/7) · **Opened:** 2026-08-20 · **Closed:** 2026-09-22

The new-task, projects, and workflows takeovers have enough terminal space to
be useful work surfaces, but currently render their content as narrow, dense
lists in the upper-left corner. This task gives each a persistent navigation
rail and one focused work surface on wide terminals while retaining the
existing compact views at smaller sizes.

The design is deliberately a layout change, not a second interaction model.
Every existing key keeps its meaning and every value still comes from the
daemon API. The wider presentation makes selection, context, and the next
decision visible at the same time.

## Decisions

### 1. Wide takeovers use a rail plus one focused work surface

*2026-08-20.* At `128×24` and above, the three affected takeovers split into a
narrow navigation rail and a main pane. New task's rail is its six-stage
progress; projects and workflows keep the selected resource in their rail
while its details fill the main pane.

**Beat:** enlarging the existing tables and forms without changing their
information hierarchy. More whitespace around an all-at-once form still asks
the person to scan every field before knowing what matters now.

### 2. Compact rendering remains the responsive fallback

*2026-08-20.* Below either side of the breakpoint, the existing single-column
form or registry remains the contract. No field or action disappears on a
narrow terminal, and a resize may change composition without resetting the
cursor, filter, picker, editor, or graph state.

**Beat:** horizontal scrolling for the whole takeover. A terminal narrower
than the two-pane composition needs a different composition, not an off-screen
navigation rail.

### 3. New-task stages are derived from the existing field cursor

*2026-08-20.* The six stages are Project, Workflow, Task details, Git &
priority, Execution, and Review. They group the existing rows; they do not add
a second stage cursor or new validation gates. Arrow keys, tab, enter, pickers,
editing, and `ctrl+s` keep their current semantics.

**Beat:** a new wizard state machine with explicit Next and Back transitions.
That would duplicate cursor state, change keyboard behavior, and make server
validation errors harder to park on the field that owns them.

### 4. Resource takeovers keep real capabilities visible, not decorative tabs

*2026-08-20.* Projects show repository configuration, defaults, and current
workload for the selected project; their existing add/edit form occupies that
same main pane. Workflows show registry provenance, validation, platforms, and
resolved steps; `g` swaps the focused surface to the existing graph while the
registry rail stays visible. Labels do not imply tabs or actions the model does
not implement.

**Beat:** adding Overview/Tasks/Settings or Overview/Steps/Graph tab chrome
only to match a mock-up. Navigation that looks interactive but is not is worse
than a quieter hierarchy around the capabilities vincent already has.

## Work

- [x] **020.1 — Add the shared guided-layout contract and helpers.** ✓ 2026-08-20
  Done when the breakpoint, column sizing, rail rendering conventions, and
  compact fallback are centralized and covered by focused tests.
- [x] **020.2 — Group New task into six focused stages.** ✓ 2026-08-20
  Depends: 020.1. Done when the active stage shows only its relevant fields,
  pickers and errors remain attached to their field, and Review summarizes the
  complete request before Create.
- [x] **020.3 — Turn Projects into a focused master/detail surface.** ✓ 2026-08-20
  Depends: 020.1. Done when the selected project, its configuration and its
  current tasks occupy the main pane, and add/edit reuse it without changing
  project keyboard behavior.
- [x] **020.4 — Turn Workflows into a focused registry/detail surface.** ✓ 2026-08-20
  Depends: 020.1. Done when the selected workflow's provenance, validation and
  steps occupy the main pane and the graph opens there without hiding the rail.
- [x] **020.5 — Prove resize and keyboard continuity.** ✓ 2026-08-20
  Depends: 020.2–020.4. Done when tests cross the breakpoint with cursors,
  filters, forms, pickers and graph state intact, and existing view tests pass.
- [x] **020.6 — Amend the spec and TUI guide.** ✓ 2026-08-20
  Depends: 020.2–020.5. Done when §15 and the user guide describe the wide and
  compact compositions without promising capabilities the code lacks.
- [x] **020.7 — Run repository verification and review the final diff.** ✓ 2026-09-22
  Depends: 020.1–020.6. Done only when formatting, focused TUI tests and the
  repository's required checks have actually run; any unavailable check stays
  explicitly blocked rather than being inferred green.
  **Blocked 2026-09-14, closed 2026-09-22:** ~~the #153 diff review
  (2026-09-14) found a real defect: `ctrl+s` failed silently when a local check
  rejected a row on a stage the wide New task form was not showing. `49659a2`
  (#163) has since fixed it and every required check has run (see
  Verification), so closing needs only the owner's call on a review whose one
  defect is already fixed.~~ Closed 2026-09-22
  ([#572](https://github.com/lezli01/vincent/issues/572)); the review's one
  finding was fixed by `49659a2` (#163, `ee3c06d`, 2026-08-21) and is covered
  by tests at `2551b22f`, and the required checks have run on that tree (see
  Verification).

## Verification

Run 2026-08-20 with the pinned Go 1.26.6 toolchain:

- `go test ./internal/tui/...` — pass.
- `go test -race ./internal/tui/...` — pass.
- `go run mage.go lint` — pass, `0 issues`.
- `go run mage.go build` — pass.
- `GOOS=windows CGO_ENABLED=0 go build ./...` — pass.
- `GOOS=darwin CGO_ENABLED=0 go build ./...` — pass.
- `go run mage.go testrace` — the changed TUI packages pass; the repository
  run fails in existing `internal/procx` live-PID tests and the dependent
  `internal/taskrun` orphan-recovery tests because this environment's `/proc`
  view cannot find the test process. The same failures reproduce without
  `-race` in `go test ./internal/procx ./internal/taskrun -count=1`.

### Re-run 2026-09-14 (#379)

- PR #153's CI run
  [32349602452](https://github.com/lezli01/vincent/actions/runs/32349602452)
  at head `b11624b` (merged as `a4fdae6`, 2026-08-20) — `success`: `ci`
  (`mage lint`, `mage testrace`, `mage build`) and `gates` on ubuntu, macOS
  and windows. This is the run on the diff itself.
- Local run at `b885c53` (`master`) on macOS, 2026-09-14 — a run of today's
  tree, not of the 2026-08-20 diff. Every command passed:
  `go test ./internal/procx -count=1`,
  `go test ./internal/taskrun -run 'Orphan|Recover' -count=1`,
  `go run mage.go test`, `go run mage.go testrace`, `go run mage.go lint`
  (`0 issues.`), the host-built linter with `GOOS=windows`, `darwin` and
  `linux` (`0 issues.` each), and `go run mage.go build`.
- The `internal/procx` live-PID and `internal/taskrun` orphan-recovery failures
  recorded above were that workspace's `/proc` view; they do not reproduce on a
  host.
- Review of the #153 diff (`git diff a4fdae6^1 a4fdae6`, 19 files,
  +1033/−24) found one defect, since fixed: `submit()` recorded a local
  check's error on its row (no project, an invalid or wrong-platform workflow,
  an agent that cannot answer mid-run) and returned without moving the cursor,
  so on the wide layout the error could sit on a stage that was not showing and
  `ctrl+s` appeared to do nothing. `49659a2`, merged with #163 (`ee3c06d`,
  2026-08-21), moves the cursor to the first invalid row.
- Checked and not counted as a defect: while the first-run notice's error line
  shows (only when recording the acknowledgment failed), the body loses a row,
  so a terminal exactly 24 rows tall draws the compact composition; decision 2
  keeps every cursor and sub-layer across that change.

### Re-run 2026-09-22 (#572)

- Local run at `2551b22f` (`master`) on macOS, 2026-09-22, with `go1.27.1` —
  `go.mod`'s `toolchain go1.26.8` is a floor rather than a pin, so a newer host
  toolchain is the one that runs, and `go env GOVERSION` is what this records.
  Every command passed: `go run mage.go build`, `go run mage.go test`,
  `go run mage.go testrace`, `go run mage.go lint` (`0 issues.`), and the
  host-built linter with `GOOS=windows`, `darwin` and `linux` (`0 issues.`
  each). Neither standing local hazard fired: `internal/cli` — whose doctor
  tests are the flaky ones under `-race` — passed, as did the load-sensitive
  `internal/notify` and `internal/scheduler` packages.
- PR #583's CI run
  [35717414832](https://github.com/lezli01/vincent/actions/runs/35717414832) at
  `2551b22f`, 2026-09-22 — `success` on every job: `ci` (`mage lint`,
  `mage testrace`, `mage build`) and `gates` on ubuntu, macOS and windows, plus
  `packaging-config`. That run is the cross-platform evidence; the local run is
  the macOS host leg of it.
- Review of task 020's surfaces since the reviewed diff
  (`git diff a4fdae6..2551b22f -- internal/tui/guided*.go internal/tui/newtask*.go
  internal/tui/projects*.go internal/tui/workflows*.go`, 17 files,
  +3611/−182) found no defect. Decisions 1–4 all still hold at `2551b22f`:
  - Decision 1 — `internal/tui/guided.go` is untouched across the range, so the
    breakpoint still turns the guided composition on at 128×24, and
    `guided_test.go:10` still pins it.
  - Decision 2 — the compact rendering is still the other branch of every
    `guidedTakeover` call: `newtaskrender.go`, `projectrender.go`,
    `workflowrender.go` and `workflowgraphlayer.go`.
  - Decision 3 — still six stages carrying decision 3's labels
    (`newtaskrender.go:34–49`), and every row added since was folded into one
    of them by `ntStageForRow` (`:61–81`) rather than growing a seventh: the
    issue row (task 035) and the fields row (022) into Task details, the
    branch-name row (001) and the start row (096) into Git & priority.
  - Decision 4 — projects' add/edit form still renders through the guided
    surface (`projectrender.go:22–27`), so it occupies the main pane with the
    rail beside it, and `g` still swaps the workflows main pane to the graph
    with the registry rail intact (`workflowrender.go:60–65`).
- Checked and not counted as a defect: task 065's structured workflow editor
  and the create prompt (`i`, `a`, `f`) render as full takeovers rather than in
  the main pane (`workflowrender.go:19–25`), which is where the two resource
  takeovers now differ. Decision 4 names the add/edit form for projects and
  only the graph for workflows, so nothing it states is contradicted.
- The #153 review's one finding is closed. `49659a2`'s cursor move is live at
  `internal/tui/newtask.go:1100–1110` and covered at `2551b22f` by
  `TestNewTaskValidatesDeclaredFieldsBeforeSubmit`
  (`internal/tui/newtask_test.go:810`),
  `TestNewTaskParksDaemonFieldErrorsOnTheFieldsRow` (`:826`) and the branch-row
  case in `TestNewTaskParksTheDaemonsComplaintOnTheRowItNames` (`:636`) — each
  asserts the cursor lands on the offending row.
