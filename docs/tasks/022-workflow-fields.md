# 022 — Workflow-declared task fields

**Status:** ⚠ verification blocked (6/7) · **Opened:** 2026-08-21

Task fields were previously only a free-form `map[string]string`. That is
useful for ad-hoc metadata, but a workflow could not tell a client which values
it expected before the task was created. This task adds an additive field contract
to workflow YAML, renders that contract in New task, and keeps the daemon as the
authoritative validation boundary.

## Decisions

### 1. Declarations are an ordered list and values remain strings

*2026-08-21.* A workflow declares `fields:` as an ordered list of definitions
with `name`, optional `label` and `description`, `type`, `required`, and an
optional string `pattern`. The declared order is the form order. Task storage,
the REST body, `.Task.Fields`, branch templates, and lane inheritance remain
`map[string]string`; the type is a validation and editing contract, not a new
runtime value system.

**Beat:** changing task fields to heterogeneous JSON values. That would break
existing templates, snapshots, branch-name inputs, API clients, and persisted
tasks for no execution benefit.

### 2. The first type vocabulary is string, integer, number, and boolean

*2026-08-21.* `string` is the default. Integers are base-10 whole numbers,
numbers are finite decimal values, and booleans are exactly `true` or `false`.
`pattern` is a Go RE2 expression and belongs only to strings; it is compiled at
workflow load so a broken expression makes the workflow visibly invalid.

**Beat:** an open-ended type name plus a regex for every field. A small enum
gives the TUI meaningful controls and gives every client identical validation;
regex remains the escape hatch for domain-specific strings.

### 3. Declared fields do not close the existing map

*2026-08-21.* Additional, undeclared fields remain accepted, recorded on the
task, inherited by lanes, and available to templates exactly as before. The TUI
keeps its add/delete route for those custom pairs. Only declared fields receive
required, type, and pattern validation.

**Beat:** treating the presence of `fields:` as `additionalProperties: false`.
That would make adding form guidance a breaking change for existing scripts and
metadata conventions.

### 4. The selected root workflow owns the public field contract

*2026-08-21.* Declarations apply when that workflow is selected for task
creation. Included workflows and named fan-out lanes do not implicitly merge
their declarations into the caller. A composing workflow re-declares inputs it
exposes; lane `fields:` continues to bind internal values.

**Beat:** recursively unioning contracts. Lane-provided values and two
workflows declaring the same name with different types or patterns make that
union ambiguous; the root is the one stable public boundary the human picked.

### 5. The daemon validates; clients may validate for placement and speed

*2026-08-21.* `POST /v1/tasks` validates the selected workflow's declarations
before anything is inserted. The TUI performs the same pure validation locally
so an error stays on the Fields row, but the server check is the gate for the
CLI, curl, older clients, and races with workflow reload.

**Beat:** validating only in New task. Vincent's clients are intentionally thin
and interchangeable; a TUI-only constraint would not be a workflow contract.

## Work

- [x] **022.1 — Add and validate the workflow field schema.** ✓ 2026-08-21
  Done when strict YAML decoding accepts the ordered definitions, structural
  mistakes carry source paths, and valid workflows expose a pure task-field
  validator.
- [x] **022.2 — Publish and enforce the contract through the API.** ✓ 2026-08-21
  Depends: 022.1. Done when `GET /v1/workflows` carries definitions and
  `POST /v1/tasks` rejects missing, mistyped, or pattern-mismatched declared
  values without rejecting extra fields.
- [x] **022.3 — Pre-render declared fields in New task.** ✓ 2026-08-21
  Depends: 022.2. Done when selected workflows seed ordered, locked field names,
  required/type/help text is visible, booleans use a selector, shared values
  survive workflow switches, and custom rows still work.
- [x] **022.4 — Accept fields from the CLI.** ✓ 2026-08-21
  Depends: 022.2. Done when repeatable `vincent task add --field name=value`
  sends both declared and additional fields and reports daemon validation.
- [x] **022.5 — Cover the schema, API, TUI, and CLI.** ✓ 2026-08-21
  Depends: 022.1–022.4. Done when focused tests cover valid and invalid
  definitions, authoritative task creation, form rendering/switching, and CLI
  transport including additional fields.
- [x] **022.6 — Amend the spec and user documentation.** ✓ 2026-08-21
  Depends: 022.1–022.5. Done when the workflow schema, API, CLI, workflow guide,
  and TUI guide describe the shipped contract and its open-map compatibility.
- [!] **022.7 — Run repository verification and review the final diff.**
  Depends: 022.1–022.6. Done only when formatting, tests, race tests, lint,
  cross-platform builds, and the relevant manual TUI walkthrough have actually
  run; unavailable checks remain explicitly blocked.
  **Blocked 2026-08-21:** this managed workspace cannot report process start
  times even for its own PID, so the full and full-race suites fail only in
  `internal/procx` and the two downstream `internal/taskrun` recovery tests.
  It also cannot provide the human terminal needed to grade a manual TUI
  walkthrough; the focused render and navigation tests pass.

## Verification

- `go run mage.go build` — pass (2026-08-21).
- `go test ./internal/workflow ./internal/api ./internal/apiclient ./internal/tui ./internal/cli` — pass (2026-08-21).
- `go test -race ./internal/workflow ./internal/api ./internal/apiclient ./internal/tui ./internal/cli` — pass (2026-08-21).
- `go run mage.go lint` plus host-built lint with `GOOS=linux`, `darwin`, and
  `windows` — pass, zero findings (2026-08-21).
- `go build ./...` with `GOOS=linux`, `darwin`, and `windows` — pass
  (2026-08-21).
- `go run mage.go test` and `go run mage.go testrace` — all packages pass
  except `internal/procx` (`StartTime(self): process not found`) and the two
  `internal/taskrun` orphan-recovery tests that depend on that process lookup;
  blocked by the workspace process environment (2026-08-21).
- Focused TUI render, editing, workflow-switch, validation, and daemon-error
  routing tests — pass; a human visual walkthrough is unavailable in this
  workspace (2026-08-21).
