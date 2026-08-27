# 020 — Guided takeover layouts for task, project, and workflow views

**Status:** ⚠ verification blocked (6/7) · **Opened:** 2026-08-20

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
- [!] **020.7 — Run repository verification and review the final diff.** — the
  container's PID namespace makes existing `procx` live-process tests report
  their own PID as missing; the failures reproduce without `-race` and lie
  outside the packages changed here.
  Depends: 020.1–020.6. Done only when formatting, focused TUI tests and the
  repository's required checks have actually run; any unavailable check stays
  explicitly blocked rather than being inferred green.

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
