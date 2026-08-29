# 009 — A configurable tasks view: grouped by project, then by workflow

**Status:** ✅ done (6/6) · **Opened:** 2026-08-16

The board's task table was one flat list, sorted by band, for any number of projects
and workflows. This makes the table's shape **configuration** — `tui.board.group_by` in
`config.yaml`, served on `GET /v1/config` — and changes the default to
`[project, workflow]`: projects outermost, the workflows of a project nested inside it.
`g` cycles the grouping for the session.

## The problem

Once more than one repository is registered, the board is read project by project — but
it was rendered as one list where two projects' tasks interleave by state, and the only
way to see one project at a time was to type its name into the filter, which hides
everything else including work waiting on a human. Within one project, the workflow is
what says what a task is *doing* (`survey` means nothing without knowing it belongs to
`docs-update`), and that too was a column to scan rather than a structure to read.

The shape that suits three projects with one workflow is not the shape that suits one
project with six, which is why this is a setting rather than a nicer fixed layout.

## Decisions

### 1. The grouping lives in `config.yaml`, under a `tui:` section the daemon only relays

*2026-08-16.* The daemon validates it, hot-reloads it with the rest of the file and
serves it on `GET /v1/config`; the TUI reads it from there and acts on it. The daemon
does nothing else with it.

**Beat:** a preferences file of the TUI's own. The TUI is a pure API client (§15) — it
reads no configuration from disk — so a second file would have been a second path per
platform, a second reload story, a second `vincent doctor` line and a second thing to
document, for one setting. Putting it in the file that already exists costs one relayed
field and keeps strict validation, hot reload and `GET /v1/config` for free.

The corollary is deliberate: a TUI that cannot reach a daemon renders the built-in
default, because the setting arrives over the API like everything else. Before a board
has any rows, that is invisible.

### 2. Grouping is a view over the existing sort, never a second ordering

*2026-08-16.* The tasks are sorted by band exactly as they always were, and each group
takes the position of its first task. So a group holding something that needs a human is
the first group, and §15's pinning rule — work waiting on a human is never below work
that is not — survives grouping untouched.

This was the constraint the feature was not allowed to cost. Sorting groups by name, or
by size, would have buried a blocked task under an alphabetically luckier project.

Group headers carry the task count and, when the group holds any, the needs-attention
badge and count: a header must never be the reason someone missed a task that is
waiting.

### 3. A grouped level costs no column

*2026-08-16.* The header names it, so grouping by project drops `PROJECT` and grouping
by workflow drops `WORKFLOW`, and the freed width goes to the title — which is where a
grouped board needs it, since the titles are indented under their headers. The §15
shedding order (cost → step name → workflow → project) is otherwise unchanged and still
derives its thresholds from the widths.

### 4. Headers are labels: the cursor steps over them, and nothing collapses

*2026-08-16.* A header has no task, so it has no state, no `available_actions` and
nothing for the detail panels to show. `↑`/`↓` therefore step over headers in the
direction of travel (turning around at either end), a click on one selects nothing, and
the cursor comes up on the first *task* rather than the first row.

Collapsing groups was rejected: a board whose whole job is showing you every task has no
business hiding rows, and a collapsed group holding an `awaiting_input` task is exactly
the failure the pinning rule and the bell exist to prevent. The `▾` glyph is a nesting
marker, not a disclosure control.

> **Superseded in part, 2026-08-29 — [task 054](054-collapsible-board-groups.md).**
> Groups fold. The half of this decision that stands is the cursor rule for an
> *open* header: it is still a label, it is still stepped over, and clicking it
> still selects nothing, for the reason given above — it has no task, so it has
> no state, no `available_actions` and nothing for the detail panels to show. A
> **collapsed** header stands in for tasks that are not on screen, so it is a
> row the cursor rests on; the same reasoning then says what it is *not*, and
> the §6 action keys, `space`, `enter` and `L` all do nothing on one.
>
> The half that is reversed is "nothing collapses". The argument that beat it:
> on a six-project installation the board's job is showing you every task you
> can *act on* rather than every task there is, and the two ways out today both
> cost something — `/` hides the work waiting on a human, `g` keeps every row.
> The concrete failure this decision named is answered three times over, and the
> third answer is what makes it unreachable rather than merely visible:
>
> - the header keeps its task count and its `! n` attention badge through a fold
>   — the machinery decision 2 built for exactly this;
> - `!` opens whatever group it lands in, so the key that exists for finding
>   waiting work always reaches it;
> - a collapsed group opens **by itself** the moment a task inside it enters
>   `awaiting_input`.
>
> Nothing is ever *refused* a collapse, which is what would have made the
> feature unpredictable on the busy board that wants it. See 050 for the fold
> rules, where the set lives and why that is not a reversal of decision 1.

### 5. `g` cycles for the session and never writes the config

*2026-08-16.* project›workflow → project → workflow → flat. The config is where the
board *starts*; the key is a quick look at the same board another way. A refetch on
reconnect therefore does not undo a press, and the Tasks panel title names the grouping
whenever it is not the configured one — the same rule the output pane's `v` follows.

`g` is taken from the table widget's own undocumented go-to-top alias; `home` still does
that, and the binding registry is what the help promises.

### 6. `project` and `workflow` are the whole vocabulary; `state` is deliberately absent

*2026-08-16.* Grouping by state would fight the band sort, which already orders by state
and pins what is waiting on a human — the one ordering rule decision 2 protects. Agent,
model and priority were not offered either: none of them is how anyone reads a board,
and each would be a level nobody can rename later without breaking a config file.

An unknown or repeated level fails the load and names the key; the TUI, being a client,
*ignores* one instead — a newer daemon may serve a level this build predates, and a
board with one level of a two-level grouping is still a board.

## Tasks

- [x] **009.1** — `internal/config`: the `tui.board.group_by` key, its vocabulary,
      validation and default; the generated `config.yaml` section. ✓ 2026-08-16
- [x] **009.2** — `internal/api` + `internal/apiclient`: the `tui` object on
      `GET /v1/config`, always an array so a flat table is not `null`. ✓ 2026-08-16
- [x] **009.3** — `internal/tui/boardgroup.go`: the grouping type, the row model, group
      building off the sorted list, header rendering. ✓ 2026-08-16
- [x] **009.4** — `internal/tui`: the board's config fetch, header-aware selection and
      cursor movement, `g` and its registry row, the grouped column set, the panel
      title, the daemon view's config line. ✓ 2026-08-16
- [x] **009.5** — Tests: grouping unit tests (nesting, group order under the band sort,
      counts, cursor skipping, cycling, columns, row/column shape), config tests, and
      live tests driving the real `/v1/config` handler both grouped and flat.
      ✓ 2026-08-16
- [x] **009.6** — Spec §12.3 and §15 amendments; `docs/reference/configuration.md`,
      `docs/reference/api.md` and `docs/guides/tui.md`. ✓ 2026-08-16

## Out of scope

- ~~**Collapsing groups** — decision 4.~~ *(2026-08-29: no longer out of scope —
  delivered by [task 054](054-collapsible-board-groups.md), which supersedes the
  "nothing collapses" half of decision 4. The reasoning is in the note on that
  decision above.)*
- **Grouping by state, agent, model or priority** — decision 6.
- **Configurable columns or sort order.** This task makes the table's *shape*
  configurable; which columns show is still derived from the terminal width (§15), and
  the sort is the band order the pinning rule depends on.
- **Writing the config from the TUI.** `g` is session state. The file is the daemon's,
  and a client that edited it would need a write endpoint, a merge story for concurrent
  edits and a reason the workflows view does not have one.
- **Per-project or per-view saved layouts.** One setting, one board.

## Verification

- `go test ./...` and `go test -race ./internal/tui ./internal/config ./internal/api`
  green (2026-08-16, Linux).
- `go tool golangci-lint run ./...` clean for `GOOS=linux`, `windows` and `darwin`.
- `TestBoardGroupsFromTheDaemonConfig` / `TestBoardHonoursAConfiguredFlatTable` drive
  the real API handler, so the config → wire → client → table path is covered end to
  end rather than by poking the model.
