# 045 — Task fields from a file or stdin in `vincent task add`

**Status:** ✅ done (4/4)
**Issue:** [#144](https://github.com/lezli01/vincent/issues/144)
**Spec:** amends §12.1

## Problem

Issue #144 was filed on 2026-08-19 against baseline `7b621865`, when `vincent
task add` had no way to supply `.Task.Fields` at all: a workflow that takes
inputs could be launched from the TUI or from raw `curl`, but not from the
supported scripting interface.

Most of it was answered two days later. Task 022 landed on 2026-08-21 and
022.4 shipped repeatable `--field name=value`, daemon-authoritative validation
of the declared field contract, the §13.1 bounds that report a limit without
echoing a value, and the flag's row in the CLI reference. Acceptance criteria
1, 2 and 4 were met on that day, and 3 and 5 were met for the value-safety
half.

What was left, and what this task is:

- no file/stdin form, so a value carrying newlines or quotes still has to be
  survived by the shell, and a generated document has to be exploded into flags;
- no human confirmation of what the created task actually carries;
- no test anywhere proving a supplied field reaches a step **template** —
  `TestCommandsAgainstLiveDaemon` points every adapter at `/nonexistent/claude`
  and never runs a step, so it proves storage and nothing past it;
- nothing in the scripting guide, which is the page a person writing the
  `jq | vincent task add` pipeline reads.

## What shipped

`--fields-file PATH` on `vincent task add`, `-` for standard input. It decodes
one JSON object whose values are all JSON strings, merges under `--field`, and
is bounded client-side at the API's own 4 MiB large-body limit.

Creation without `--json` now confirms the recorded fields on an indented line
under `task N created:` — `fields: owner, ticket (2)` — sorted names and a
count, never a value.

| Path | Change |
|---|---|
| `internal/cli/task.go` | The flag, `readFieldsFile`, `mergeFields`, `fieldsSummary` |
| `internal/cli/task_fields_test.go` | The parser, the merge precedence, every rejection, the summary |
| `internal/cli/fields_e2e_test.go` | Real binary, real daemon: a command step's template renders a field supplied on stdin |
| `docs/reference/cli.md`, `docs/guides/scripting.md`, `docs/guides/workflows.md` | The flag, the pipeline, the precedence rule |
| `docs/spec.md` §12.1 | Dated amendment |

## Decisions

**1. The scope is the gaps, not the issue as filed.** *(2026-08-28)*

`--field` is not re-derived, and `parseFieldFlags` is not touched. The issue's
"repeatable `--field key=value`, split on the first equals sign" bullet
describes code that already exists and is already tested; rebuilding it would
have been churn with a regression risk and no user-visible result.

**Beat:** implementing the issue front to back as written.

**2. `--fields-file` combines with `--field`, and `--field` wins per name.**
*(2026-08-28)*

The file supplies the base map; each `--field` overrides its own name. This is
the same last-wins rule `--field` already documents, extended one level out.

**Beat:** mutual exclusion, which the issue offered as the simpler option. It
forces a script that wants to vary one input to regenerate the whole JSON
document, and the precedence rule it saves is one sentence.

**Beat:** file-wins, which inverts the usual expectation that the more specific
flag on the same command line is the one that takes effect.

**3. Undeclared fields stay accepted; no strict mode is added.**
*(2026-08-28)*

The issue asks to "reject fields not accepted by the resolved workflow". That
contradicts **task 022 decision 3** (2026-08-21): declaring `fields:` does not
close the map, because closing it turns "add form guidance to a workflow" into
a breaking change for every existing script and metadata convention. That
decision stands and is not relitigated here.

`internal/workflow/fields.go` and `Workflow.ValidateTaskFields` are untouched,
there is no `--strict` flag, and no warning is printed for a name the workflow
never declared. The bullet is recorded as beaten so the next reader does not
re-open it.

**4. The confirmation line carries names and a count, never values.**
*(2026-08-28)*

A field can hold a ticket key, a customer name or a token, and the line lands
in scrollback, screenshots and CI logs. It is suppressed under `--json` — where
the caller asked for the task, values and all — and omitted entirely when the
map is empty.

It is read off the **response**, not off what was sent, so a field the daemon
prefilled from `--github-issue` (task 035) is confirmed alongside the typed
ones.

**Beat:** a bare count. It cannot catch a mistyped name, which is the whole
reason to confirm; `fields: (2)` looks identical whether the workflow got
`ticket` or `tikcet`.

**5. A local input error exits 1, not a new code.** *(2026-08-28)*

A malformed `--fields-file` is the same class of thing as today's malformed
`--field`: a plain `error` returned from `RunE`, which `asExitCode` maps to 1.
Exit 2 keeps its single meaning — no daemon answered — so a script can still
tell "start the daemon" from "fix your request" without parsing stderr (PR U
decision).

**6. The read is bounded client-side at the API's 4 MiB body limit.**
*(2026-08-28)*

Standard input can be an unbounded pipe. Reading through an `io.LimitReader`
and failing with a message naming the limit gives the caller the answer the
daemon would have given them, sooner, and without buffering an arbitrary file
into memory first. It is the only bound the client applies: per-field key and
value sizes, the 100-entry cap and the declared field contract stay
daemon-authoritative, because the CLI is not the only client.

**7. Values must be JSON strings; a duplicate name inside the object is
documented, not detected.** *(2026-08-28)*

`.Task.Fields` is a `map[string]string` (§8.1.2). Stringifying a number or a
boolean would make `{"retries": 3}` and `{"retries": "3"}` the same document
while a workflow declaring `type: integer` can tell them apart, so a non-string
value is an error — naming the **key only**, for the same reason the
confirmation line names no values. A JSON `null` is rejected explicitly rather
than being allowed to unmarshal into `""`.

A name repeated inside the object resolves last-wins, which is what
`encoding/json` does and what `--field` already promises; it is written down
rather than detected. Keys are used **verbatim** — only an empty or
all-whitespace name is refused — because trimming them would make
`{"ticket": …, " ticket": …}` collide, and which of the two survived would then
depend on Go's map iteration order.

**8. The end-to-end proof is a Go test, not a gate script.** *(2026-08-28)*

The E2E test creates a project carrying a project-scoped workflow with a
`command` step that renders `{{ index .Task.Fields "ticket" }}`, runs it to
completion, and reads the effect out of the task's worktree. Its step body is
`git config -f fields.ini …`, spelled in the intersection of `/bin/sh` and
`pwsh` (§8.3, CLAUDE.md) so it runs unchanged on all three platforms.

`go test ./...` already runs on Linux, macOS and Windows, so this is the
cheaper home than an eighth gate script; no gate covers fields today either
way.

## Tasks

- [x] **045.1** `--fields-file PATH` / `-`: bounded read, one JSON object of
  strings, merge under `--field`.
- [x] **045.2** The field confirmation line on human output, from the response.
- [x] **045.3** Unit tests for the parser, the precedence, every rejection and
  the summary; a real-binary E2E test proving template consumption.
- [x] **045.4** `docs/reference/cli.md`, `docs/guides/scripting.md`,
  `docs/guides/workflows.md`, and the §12.1 amendment.
