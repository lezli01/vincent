# 054 — Collapsible groups on the task board

**Status:** ✅ done (1/1)
**Spec:** amends §15 (the grouping amendment's header rule, and the
pure-API-client sentence), §12.2 (`tui.json`)
**Supersedes:** [009](009-configurable-tasks-view.md) decision 4, in part

## Problem

The board nests its rows under group headers, `[project, workflow]` by default
since [009](009-configurable-tasks-view.md) — but every row under every header
is always rendered. A board carrying several projects and a long tail of
finished tasks pushes the project you are actually working in off the bottom of
the screen, and grouping does nothing about it: `g` changes the *shape* of the
table, never the number of rows in it.

The two ways out today both cost something. `/` filters to one project but hides
everything else, including work waiting on a human — the case the filter is
explicitly not allowed to be used for. `g` to a coarser grouping keeps every row.

The headers already carry what you need to decide a group is not interesting
right now — the task count, and the `! n` attention badge when the group holds
any — so the information is on screen; there was just no way to act on it.

## Decisions

**1. The fold set lives in `{data_dir}/tui.json`, under a new key.**
*(2026-08-29)*

`{data_dir}/tui.json` has existed since v0 (`internal/tui/firstrun.go`, spec
§12.2, `docs/reference/files.md`, `docs/security-model.md`), and the v0 record
that created it says why in as many words: *"an open JSON object so later prefs
join without a format change"* (`docs/history/v0-tasks.md`). The write already
merges into whatever the file holds rather than rewriting it, so a field one
build does not know about survives being read by it.

So there is **no second path per platform, no second reload story, no new
`vincent doctor` line and no new page**: the file is already resolved, already
permissioned (`internal/config/permissions.go`), already excluded from backup
and restore (task 030), and already documented on all three pages. The whole
cost is one key on an object that exists.

**009 decision 1 is not reversed.** It governs *configuration the TUI reads* —
the thing that made it not a pure API client — and `tui.board.group_by` still
arrives over `GET /v1/config` exactly as it did. A fold set is view state the
TUI owns, which is the same category as the first-run acknowledgment that file
was built for. §15's "holds no state the daemon doesn't have" gains a dated
amendment naming the distinction; it is not weakened.

**Beat:** session-only folds (cheapest, but re-collapsing the same five projects
at every launch is what makes a feature like this go unused); a separate prefs
file (a second TUI-owned file next to `tui.json` with no reason for the split);
a daemon-side `/v1/view-state` endpoint (an endpoint, a migration and a rule for
two disagreeing TUIs, to store a gesture).

**2. A collapsed header is selectable; an expanded one stays a label.**
*(2026-08-29)*

If the cursor never lands on a header, then `→` — "expand the group the cursor's
*task* belongs to" — can never address a group that is already collapsed,
because collapsing moved the cursor to a task outside it. `O` would be the only
way back, and there would be no way to reach an outer level at all.

The rule instead: **a collapsed header stands in for its tasks, so it is a row;
an expanded header names rows that are present, so it stays a label.** `↑`/`↓`
stop on a collapsed header and step over an expanded one. `←` on a task
collapses its innermost group and leaves the cursor on the header it just
closed; `←` again folds the parent; `→` expands one level and the cursor moves
onto the first thing that level now shows — a task, or a sub-header still
folded. This is ordinary tree behaviour, it addresses every nesting level
without a "nearest above" heuristic, and it is what `enter` already means in the
diff pane.

009 decision 4's reasoning still governs the rest: a collapsed header has no
task, so it has no state, no `available_actions` and nothing for the detail
panels to show. The §6 action keys, `space`, `enter` and `L` therefore do
nothing on one, and the detail panels hold their last task rather than blanking.

**Beat:** `→` expanding the nearest collapsed group above the cursor
(unpredictable on a busy board, and still cannot reach an outer level);
accepting the hole and letting `O` be the way back (makes `→` nearly useless);
headers fully selectable with `enter` folding (rejected for 009 decision 4's
reason).

**3. Decision 4's failure is made impossible, not merely visible.**
*(2026-08-29)*

009 decision 4 named one concrete failure: a collapsed group holding an
`awaiting_input` task. Three things answer it, and the third is beyond what the
issue asked for:

- The header carries the `! n` attention badge and the task count already — that
  machinery was built in 009 decision 2 for exactly this, and it keeps working
  through a fold. So does the bulk-selection count, so `V` reaching into a fold
  (task 011: the selection is not a view) is never invisible.
- `!` (jump to the next task needing a human) expands whatever group it lands
  in. The expansion is a real fold change and is persisted like any other.
- **A collapsed group auto-expands the moment a task inside it *enters*
  `awaiting_input`** — read off the same `task.state_changed` event that rings
  the bell, but read separately, because the bell is rate-limited into one
  interruption and an expansion has to happen every time.

Folds stay predictable in the direction that matters — nothing is ever *refused*
a collapse, which is what would make the feature unpredictable on the busy board
that wants it — while the board state decision 4 was protecting against cannot
be reached.

**Beat:** refusing to collapse a group that holds attention work, which
preserves the concern literally and makes the fold a coin toss.

**4. Pruning is against task values, not the current grouping.** *(2026-08-29)*

The issue's two acceptance criteria conflict as written: folds must survive `g`
cycling away and back, but a path whose label "no longer appears" is dropped —
and cycling to `project`-only grouping makes every `[project, workflow]` path
stop appearing.

A saved path is kept while each segment is still a project name or a workflow
name occurring somewhere in the **unfiltered** task list, independent of which
levels are currently rendered. `g` cycling therefore leaves the set alone, a
filter leaves it alone, and a project that is archived away drops out. Pruning
runs on the task list the board holds and only on a *successful* load, so a
disconnected TUI prunes nothing rather than forgetting everything.

**Beat:** pruning once at load (a long-lived TUI accumulates dead paths); never
pruning with an LRU cap (a stale path silently re-collapses a project that comes
back months later).

**5. Flat grouping has no folds.** *(2026-08-29)*

With `group_by: []` there are no headers, so the four keys are inert and their
footer hints are dropped (`shell.liveBindings`). The saved set is **not**
cleared — cycling back to a grouped view restores what was folded.

## Tasks

- [x] **054.1** Fold the board's groups: `foldPath`/`foldSet`, `applyFolds` and
  the four key handlers in `internal/tui/boardfold.go`; `←`/`→`/`C`/`O` in the
  binding registry; the cursor rule for a collapsed header and the count,
  attention badge and marked count on its cell; `!` and the incoming
  `awaiting_input` transition opening a fold; the set persisted, pruned and
  fail-open through `{data_dir}/tui.json`; the §15 and §12.2 amendments and
  009's superseding note. ✓ 2026-08-29

## Implementation notes

Everything is in `internal/tui` plus documentation. No daemon, API, store,
config or migration change, which is the strongest argument for this shape over
the `/v1/view-state` alternative.

| File | Change |
|---|---|
| `internal/tui/boardfold.go` | New. `foldPath`/`foldSet`, `applyFolds`, pruning, the four key handlers, the `tui.json` read/write. A sibling of `boardgroup.go` and `boardmark.go` rather than more of `boardgroup.go`, following the file convention the board package already has |
| `internal/tui/boardgroup.go` | `boardRow` gains `path`, `collapsed` and `marked`; `groupRows` fills the path; `▸`/`▾` by state; the header cell gains the marked count |
| `internal/tui/board.go` | `←`/`→`/`C`/`O`; `rows()` = `applyFolds(allRows())`; `visible()` reads `allRows()` and so stays filter-scoped; `skipHeaders` stops on a collapsed header; `selected()` answers "no task" on one; cursor restore by path; the prune on load; the auto-expand |
| `internal/tui/firstrun.go` | The `tui.json` helpers generalized to `readTUIState`/`mergeTUIState`; the merge-preserving write was already there |
| `internal/tui/bindings.go` | Four `ctxTasks` rows, marked `fold:` so a flat board can drop them |
| `internal/tui/shell.go` | `!` expands the group it lands in; `liveBindings` |
| `internal/tui/boardmark.go` | Unchanged by design — `visible()` is filter-scoped, not fold-scoped, so `V` marks inside collapsed groups for free. Covered by a test rather than an edit |

## Out of scope

- **A daemon-side view-state endpoint.** Decision 1.
- **Remembering which task the cursor was on across restarts.** The fold set is
  the one piece of board view state that is worth a file; a cursor is not.
- **Folding in the diff pane or the workflow graph.** Both already have their
  own fold rules (§15, task 012; task 017), and they are session-only on
  purpose.

## Verification

- `go test ./...`, `go test -race ./internal/tui`, `go run mage.go lint` for
  `GOOS=darwin`, `linux` and `windows`.
- No gate script. This is a judgement about a TUI, which is why 009 had none
  either. The board screenshots did not change shape — a fresh install has
  nothing folded — so `scripts/screenshots.sh` was not re-run.
