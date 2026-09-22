# Tasks 022 and 058 walkthrough — declared fields on New task

**Acceptance (task 022.7's manual walkthrough, and task 058's value picker):**
the rows New task draws for a workflow's `fields:` — every declared type, the
enum list and its `multiple` form — read correctly and edit correctly in a real
terminal.

This walkthrough has **no script**, deliberately. What is judged is whether a
form reads and edits correctly by eye, which is the reason M3 and
[017](017-workflow-graph.md) have none. The contract underneath is already
asserted: declarations, defaults and the value checks in
`internal/workflow/fields_test.go` and `internal/workflow/enumfields_test.go`,
the daemon's refusal at `POST /v1/tasks` in `internal/api/fields_test.go` and
`internal/api/enumfields_test.go`, and the form's rows, workflow switching and
local messages in `internal/tui/newtask_test.go` and
`internal/tui/newtaskenum_test.go`. A script would re-assert that over curl
and still not answer the only open question.

## Setup

A throwaway installation and one project. No agent is needed: nothing here has
to run.

```sh
go build -o bin/ ./cmd/vincent
W="$(mktemp -d)"
export VINCENT_CONFIG_DIR="$W/config" VINCENT_DATA_DIR="$W/data"
mkdir -p "$VINCENT_CONFIG_DIR/workflows" "$W/repo"
git -C "$W/repo" init -b main
git -C "$W/repo" commit --allow-empty -m seed
# save walk-fields.yaml below into $VINCENT_CONFIG_DIR/workflows/
./bin/vincent workflow validate "$VINCENT_CONFIG_DIR/workflows/walk-fields.yaml"
./bin/vincent daemon start
./bin/vincent project add "$W/repo"
./bin/vincent                                # n, then choose walk-fields
```

`walk-fields.yaml` declares one field of every kind, in this order:

```yaml
name: walk-fields
description: one of every kind of declared task field, for the New task walkthrough
fields:
  - name: ticket
    label: Ticket
    description: Issue tracker key, including its project prefix.
    type: string
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: retries
    label: Retries
    description: How many times to try again. Optional.
    type: integer
  - name: dry-run
    label: Dry run
    description: Say what would change without changing it.
    type: boolean
  - name: environment
    label: Environment
    description: Where it runs.
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: region
    label: Region
    description: Optional, with no default.
    type: enum
    values: [eu, us, ap]
  - name: reviewers
    label: Reviewers
    description: Any of them.
    type: enum
    multiple: true
    values: [ana, bo, cy]
    default: [ana, cy]
steps:
  - id: record
    type: command
    run: git log -1 --format=%s
```

## Legs

| # | Do | Expect |
|---|---|---|
| 1 | Press `n`, choose `walk-fields`, and read the Fields row; open it with `enter`, then move down the rows | Six rows in declaration order — Ticket, Retries, Dry run, Environment, Region, Reviewers — each labelled `Label (name)` with its type and required badges, e.g. `Ticket (ticket) [string · required]`. The row under the cursor also shows its description, and Ticket its regex help, `^OPS-[0-9]+$` |
| 2 | Move to Dry run with `↑`/`↓` (or `j`/`k`) and press `enter` twice | The value toggles between `true` and `false`. No text editor opens |
| 3 | On Environment press `→` three times; on Region press `→` until it comes round | Environment steps `prod`, `dev`, `staging` in place, with no list opening and no empty stop — it is required. Region steps `eu`, `us`, `ap`, then an empty stop shown as `(choose a value)`, then `eu` |
| 4 | `enter` on Environment, press `/`, type to filter, then `esc` twice; then `enter`, move to `prod`, `enter`. Open Region's list too | A scrollable list of the declared values that narrows as you type after `/`. The first `esc` clears the filter, the second closes the list and leaves the value as it was; `enter` commits `prod`. Region's list, being optional, starts with an `(unset)` row; Environment's has none |
| 5 | `enter` on Reviewers, add `bo` to the set with the list open, and close it; then press `←`/`→` on the row | The list toggles membership, and the row holds all three members. `←`/`→` do not step it |
| 6 | Leave New task, press `n` again and choose `walk-fields` | The rows are seeded from `default:` — Environment `staging`, Reviewers `ana` and `cy` — while Retries, Dry run and Region are empty |
| 7 | Press `d` on Ticket; press `a` and add a custom field `note` = `hello`; press `d` on `note` | `d` refuses Ticket, because a declared field cannot be removed, and removes `note`. A declared row's name is locked and only its value edits |
| 8 | Fill Ticket with `OPS-1`, add `note` = `hello` again, switch to another workflow, then back to `walk-fields` | Every value survives both switches, `note` included, though neither workflow declares it |
| 9 | With a title filled in, put `abc` in Retries and `nope` in Ticket, committing each with `enter` (an `esc` while typing discards the value); close the editor with `esc`, and press `ctrl+s` | Committing each value shows its message under the editor, one at a time: `Retries must be a base-10 integer`, `Ticket must match ^OPS-[0-9]+$`. `ctrl+s` creates nothing, and the Fields row carries the first bad field in declaration order, Ticket's |
| 10 | Still refused by the daemon: run `vincent task add --project <id> --workflow walk-fields --title x --field ticket=nope`; then, with `OPS-1` in the form, tighten `pattern:` in the file to `'^OPS-[0-9]{2,}$'`, save, and press `ctrl+s` | The command is refused, naming `ticket`, and no task is created. The open form does not re-read the workflow, so `OPS-1` passes its own check; the daemon, which has reloaded the file, refuses the task, and the Fields row says why, naming `ticket` |
| 11 | Repeat 1 and 9 under `NO_COLOR=1` | The badges, the regex help and the messages are all still readable as words |
| 12 | With Ticket `OPS-1`, put two spaces in Retries, commit with `enter`, close the editor and press `ctrl+s`; then `vincent task show <id> --json` | No message at all: a row holding only whitespace counts as unset, the way the daemon counts it (§8.1.2, amended 2026-09-22). The task is created, and its `fields` carry no `retries` key — the spaces are neither type-checked nor stored |

## Runs

| Date | Version | Platform | By | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk. A form that has never been walked on a platform is not
known to read correctly there.
