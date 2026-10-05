# 132 — Project as root: the TUI scoped to one selected project

**Status:** 🔄 in progress (13/18)

Issue [#694](https://github.com/lezli01/vincent/issues/694), part of
[#693](https://github.com/lezli01/vincent/issues/693). Spec §3 (the new row
37) in this document's pull request; §12.3, §13.2 and §15 in the pull request
of each item whose code makes them true.

#693 and its sub-issues #694–#711 were filed calling this work "task 131" and
its items "131.n". Task 131 was taken the same day by the versioned
documentation site (#712, #713), so this is task 132 and its items are
`132.1`–`132.17` (decision 15). Read "131.n" in those issues as "132.n".

## What this is

The requirement, as #693 records it:

> Modify vincent tui so its root is always the selected project. It can have a default selected project. Tasks issues prs and whatsoever is always only shown for the currently selected project. I do not want anymore views where I see details of multiple projects altogether. The project selection should show important details of the projects like active tasks, number of issues. There should be a new view which is project overview which is the only view where multiple projects are shown, here the user can see aggregated details and stats of all the registered projects. Otherwise the usecase of vincent must be refactored and changed so user always managing one selected project and can switch between projects anytime. When project is changed the currently selected view must remain and be shown for the newly selected project. In other usecases where there is ambigousity give me options with your recommendations, always highlight your recommended option.

The model:

- The TUI holds exactly one **selected project**. It is client-side TUI view
  state and never daemon state, because two TUIs may watch different projects.
- Every project-bearing view shows only the selected project. The **project
  overview** is the only multi-project view.
- The daemon, its REST/SSE API, the CLI and MCP stay installation-wide.
- The daemon changes only to add per-project counts (132.1), the data the
  picker and the overview need and the TUI cannot cheaply get today.

This document is the plan and the decision record; it changes no code. Each
`132.n` item is one sub-issue of #693 and roughly one pull request. The
research behind it is summarised in #693.

## Prior art

The same direction was filed as #637, with #640 (selector), #649 (board,
archived boards, chats, grouping level), #650 (pull requests, workflows) and
#657 (screenshots). All five were closed `NOT_PLANNED` on 2026-10-02, and task
130 then recorded the opposite as binding (see Supersedes). The author reversed
that call on 2026-10-04 (decision 1), and the designs of #640, #649 and #650
are reused here.

One correction to #640: `W` is no longer a free key. It is the issue screen's
`comment` op (`internal/keymap/keymap.go:156-158`), so the selector cannot
take it; decision 3's switch key is `@`.

## Decisions

Recorded 2026-10-04. Decisions 1–14 are the author's answers to #693's open
questions, each the option #693 recommended; decision 15 is the renumbering;
decisions 16–19 settle the contradictions between #693's research reports;
decisions 20–22 were taken while delivering 132.1, decisions 23–24 while
delivering 132.2, decisions 25–28 were settled with the author for 132.5
on 2026-10-05, decisions 29–32 were taken while delivering 132.3,
decision 33 while delivering 132.4, decisions 34–35 while delivering
132.8, decisions 36–38 were settled with the author for 132.6, and
decisions 39–41 were settled with the author while delivering 132.12,
and decisions 42–45 were taken with the author while delivering 132.15. Each
decision is **taken now; its effect lands with its item.** The older record
keeps governing the code until that item's pull request merges.

### 1. The requirement supersedes the 2026-10-02 closures and task 130 decision 16.1 (2026-10-04)

The requirement supersedes the `NOT_PLANNED` closures of #637, #640, #649,
#650 and #657 and task 130 decision 16.1, for tasks, issues, pull requests,
chats, workflows and triggers alike. "Tasks issues prs and whatsoever" reads as
every project-bearing view.

*Alternative beaten:* scoping only the board.

### 2. Attention stays cross-project (2026-10-04)

A badge counts attention tasks in **other** projects, the bell rings for every
project, and `!` switches project when the selected one has nothing left that
needs a human (132.14). This rests on §15's rule that "a filter must not hide
that something needs you" (`internal/tui/board.go:1536-1540`): a project
selection is a filter, and it must not hide a question waiting in another
project. The badge is a count, not a view of another project's details.

*Alternatives beaten:* (B) `!`, the count and the bell all per project;
(C) a per-project `!` and count with a global bell only.

### 3. Startup precedence (2026-10-04)

At startup the selection is decided by the first of: the `--project` flag, the
working directory (the registered project containing it), `tui.default_project`,
the last-used project (`selected_project` in `{data_dir}/tui.json`), and the
first project by name (132.3). The switch at run time is a new global keymap
op `project` on `@` and a palette row (132.4).

*Alternatives beaten:* last used only; `tui.default_project` only; #640's
working directory → last used → first.

### 4. The working-directory rule is kept, and named (2026-10-04)

Launching `vincent` from inside a registered project selects it. The startup
notice names which rule picked the project, so a launch from an unrelated
repository is not a silent surprise.

*Alternative beaten:* dropping the rule.

### 5. The overview replaces §15 view 4 in place (2026-10-04)

The project overview replaces view 4 (Projects) in place (132.15). It keeps
view 4's add, edit and remove flows and its management keys, every citation of
"view 4" stays valid, and there are still thirteen views.

*Alternatives beaten:* a new view 14 beside a retired view 4; keeping view 4
as a second multi-project screen, which contradicts the requirement.

### 6. The `project` level of `tui.board.group_by` is deprecated (2026-10-04)

Under scoping the `project` level has one value and draws no header (task 129
decision 4). It is accepted on load with a warning doctor can see, refused on
write, and the default becomes `[workflow]` (132.9).

*Alternatives beaten:* removing it outright, which breaks every bootstrapped
config on a cold start — unknown levels are an error and the bootstrap
template has shipped `[project, workflow]` (`internal/config/bootstrap.go:346`);
keeping it as a documented no-op.

### 7. The workflows view shows what the selected project resolves (2026-10-04)

Builtin, global and project entries, with §5.2 shadowing applied, as
`?project_id=` already returns them (132.12). Create and fork target global or
the selected project.

*Alternatives beaten:* an "all scopes" toggle (#650's plan, which breaks the
requirement); the project's own entries only, which hides shadowing.

### 8. Triggers are filtered client-side, with an "unassigned" band (2026-10-04)

Every valid trigger names exactly one `source.project`, so the takeover shows
the selected project's triggers. An invalid file whose project cannot be read
lands in an "unassigned" band, so it stays repairable from the TUI (132.12).
Trigger files stay global (task 096 decision 8 is kept).

*Alternatives beaten:* staying global (#650, which breaks the requirement);
filtering and hiding unassigned files, which makes them unrepairable from the
TUI.

### 9. Form project fields are locked to the selection (2026-10-04)

A form's project field shows the selection read-only (132.13). `projectHinting` (`internal/tui/views.go`),
today's guess from the cursor, is removed.

*Alternatives beaten:* a picker defaulted to the selection that switches on
create; no row at all.

### 10. A newly registered project does not change a selection (2026-10-04)

A project added while one is selected leaves the selection alone. It is
auto-selected only when nothing is selected — the first project added to an
empty installation (132.7).

*Alternatives beaten:* always switching to the new project; offering it in a
notice.

### 11. The board header's slot clause is per project (2026-10-04)

The header shows the selected project's running count over its cap, with the
daemon's global count dimmed: `2 running · cap 3 · daemon 5/8` (132.14).

*Alternative beaten:* keeping the global count only.

### 12. The picker's "issues" means open issues (2026-10-04)

The picker counts open issues. The overview shows imported issues separately
(132.1, 132.4, 132.15).

*Alternative beaten:* open issues nobody is working on.

### 13. Screenshots: new tapes with their features, one sweep at the end (2026-10-04)

A new tape lands in the pull request of the feature it shows, and 132.16 makes
`scripts/screenshots.sh` project-aware and recaptures everything once.

*Alternatives beaten:* recapturing in every pull request; one sweep at the end
only.

### 14. Spec §3 records this ahead of its code (2026-10-04)

Spec §3 row 37 is added **in this document's pull request**, before any item
lands. That is an explicit exception to this directory's rule that a behaviour
change lands in the spec "in the same pull request as the code that makes them
true — never ahead of it" (`README.md`), the same exception task 130 decision
13 made (`130-issues-pillar.md:358`). It covers row 37 only, and row 37 says
in its own note that it records a decision ahead of its code and names the
items that make it true. §12.3, §13.2 and §15 are amended by each item in its
own pull request.

*Alternative beaten:* adding row 37 with the first code item (#696).

### 15. The task number is 132, not 131 (2026-10-04)

#693 predicted the race: 131 was the next free number when it was written, and
the versioned documentation site (#712, merged as #713) took it the same day.

*Alternative beaten:* none — the number is a fact, not a choice; it is recorded
so that "131.n" in #693–#711 is not mistaken for task 131's items.

### 16. One global event stream, filtered in the client (2026-10-04)

The root keeps its one unfiltered event stream and each view drops notes for
other projects (132.5).

*Note (2026-10-05):* the board is the exception. Its live listing stays
global by decision 17 and feeds the attention count, `!` and `H`, so it
drops no note: a task event from any project refetches it (review F1 on
#718).
A `project.*` event passes every view's filter whatever project it names,
since it describes the project list every view renders whole, and the
triggers takeover lets `trigger.*` through until 132.12 scopes it (review F2
on #718). *Corrected 2026-10-05:* the scoping item is 132.12, not 132.11; it
has landed, and a `trigger.*` event for another project is now dropped like
any other, while `trigger.poll_changed`, which carries no project, still
re-reads.

*Alternative beaten:* resubscribing with `?project_id=` on each switch. It
loses the events that carry no project (`task.github_pull_changed`,
`task.children_changed`, `agent.quota_changed`, `workflow.registry_changed`,
`trigger.poll_changed`, `daemon.shutting_down`), the cross-project bell of
decision 2, and the events committed while the stream reopens.

### 17. The live board listing stays global; the archived board is filtered on the server (2026-10-04)

The board keeps fetching every live task and filters in memory, so a switch
costs no fetch and the attention badge and `!` of decision 2 keep their source.
The paged archived tasks board sends the project to the server (132.8).

*Alternative beaten:* sending `ProjectID` in the board's live listing.

### 18. A `{project, seq}` load stamp, not a per-switch epoch (2026-10-04)

Every list load is stamped with the project it was issued for and a sequence
number, and a response whose stamp is stale is dropped (132.5). A stamp also
tells an old filter's response apart, and reads better in tests.

*Alternative beaten:* an epoch on the selection, bumped on each switch. It may
still be kept as a cheap "selection changed" counter.

### 19. Form fields: locked, re-targeted when pristine, asked about when dirty (2026-10-04)

Decision 9's lock and 132.6's re-targeting are compatible: the project field is
read-only and shows the selection; a switch re-targets a pristine form and asks
before dropping a dirty or seeded one (132.6, 132.13).

*Alternative beaten:* none — the two research positions did not conflict, so
neither beat the other.

### 20. An opt-in `stats` parameter on the existing project routes (2026-10-04)

`GET /v1/projects?stats=true` and `GET /v1/projects/{id}?stats=true` add a
`stats` object to each row (option A2 of the #695 research), the data
decision 12's picker and the overview read (132.1). The others lost:

- **A1, counting in the client:** it needs every non-archived task with its
  rollups, every open issue with its body, and every chat, and it puts the
  definition of each figure in every client — the #324 bug.
- **A3, a separate `/v1/projects/stats` route:** one more route, MCP tool and
  client join for figures that belong on the rows already being listed.
- **A4, stats on every response:** the projects view refreshes the list
  constantly, and most callers would pay several `GROUP BY`s they ignore.

Absent or `stats=false` is byte-identical to the default shape. The counts
are a fixed number of `GROUP BY project_id` statements however many projects
are registered (`store.projectStatsPasses`), and a failed count degrades to
`"stats": null` with a warning, the `slots_used` precedent. The seeded
benchmark (20 projects, 4,000 tasks, 2,000 issues) reads in about 3 ms, so no
`tasks(project_id, state)` index was added.

### 21. Task counts cover every row, lanes included (2026-10-04)

The issue recommended root tasks only. The author chose all rows instead, so
`tasks.by_state`, `tasks.active` and `tasks.attention` line up with
`slots_used`, §11's all-rows figure. A picker reading "2 active" beside
"3/4 slots" while lanes run would otherwise look wrong.

- `active` is every non-archived task that is not settled
  (`taskstate.Settled`). It is therefore **not** an issue's `active`, which
  is about the issue and counts an unsettled *root* task; that definition
  is unchanged. Both API docs say so.
- `attention` is every task in a `taskstate.NeedsHuman` state. An
  `awaiting_children` parent is not counted as well: its lane that needs
  someone is already counted, so the rollup subtree query is not used.
- `by_state` omits zero states and never carries `archived`.
- Chat attention (`chats.awaiting_input`) stays a separate figure (spec
  decision row 29).

### 22. The CLI is the first client; no `GetProject` yet (2026-10-04)

`vincent project ls --stats` adds count columns and, with `--json`, the
`stats` object. `apiclient.ListProjects` takes a variadic `WithStats()`
option, so its existing callers are unchanged. No `apiclient.GetProject` is
added, because nothing calls one; 132.4 or 132.15 adds it if needed.

### 23. The header segment is drawn, not clicked, until 132.4 (2026-10-04)

132.2 draws `◆ <project>` (or `◆ no project`) on the existing header line,
after the version and any connection badge and before the view tag. It is not
a click target yet: making it open the picker lands in 132.4 with the picker
itself, and §15's click scope is amended there. A click on the header row does
what it did before.

### 24. The header sheds the tag, then the version, then the name (2026-10-04)

The chrome stays one header line, so `shellChromeH` and §15's floors do not
move. When the line does not fit, the view tag (or the task workspace's
breadcrumb) truncates down to a floor of eight cells and is then dropped;
then the version number goes, leaving `vincent`; last, the project name
truncates behind an ellipsis. The connection badge is never shed, because it
is drawn only while it is news.

The root walks `projectScoped` views directly, never by broadcast, and again
right after the `setClient` walk on every connect. Until 132.3, the first
selection is the first project by name, made only while nothing is selected
(decision 10). A selected project that vanishes was deleted, and 132.7
replaces it (decisions 46–49). `n` opens the new-task form on the active view's hint first and on the
selection only when the view hints none (review F1 of the 132.2 train). Until
132.8–132.13 scope the views their rows span every project, and the projects
view is never project-bearing, so the selection overriding the cursor would
open the form on a project the user is not pointing at. Decision 9 retires the
hint in 132.13, when the two agree.

`projectScoped` took the name of a `workflows.go` helper, which became
`ownEntries`, and of the new-task form's `setProject`, which became
`chooseProject`.

### 25. A switch reloads every stamped view (2026-10-05)

On every stamped, project-bearing view, `setProject` reloads when the
selected project's id changes: `projectScope` runs the loader the embedding
view supplied (`reload`), which stamps the new project. So whenever a stamp
drops a response issued for the old project, a fresh load is already on its
way, and a view never sits on rows it did not re-fetch. The fetch itself
stays unfiltered; what each view fetches is 132.8's and 132.10–132.12's. A
call that does not change the id — the walk after each connect, a rename —
does not reload, since `setClient` has just loaded.

*Alternative beaten:* stamping without reloading, which leaves a view
showing the previous project's rows until its next event.

### 26. The projects view takes a seq-only stamp (2026-10-05)

The projects view stamps every load with project 0. It is still not
`projectScoped` (decision 5) and does not filter events; the stamp only
gives it the ordering guard it lacked.

*Alternative beaten:* leaving it unstamped, the one list a slow response
could still overwrite.

### 27. The mode stamps stay beside the load stamp (2026-10-05)

`loadStamp` carries only `{project, seq}`. The boards' and lanes' `archived`
tag and `addressed()`, the issues list's `state`, the chats boards'
`archived` and the detail view's `id`+`seq` guard stay as they were and are
checked next to it. The board's and lanes' own `seq` fields are replaced by
the board's one sequence; the lanes order it per parent and take only the
project check from the stamp.

*Alternative beaten:* folding every mode into the stamp, which would make
it a different type per view.

### 28. No daemon change for the filter (2026-10-05)

`task.github_pull_changed` and `task.children_changed` keep carrying no
`project_id`, and spec §13.3 is unchanged. The client filter is correct
without them because a note with no project passes every view.

*Alternative beaten:* attributing those two events in the daemon in the same
item, which the filter does not need.

### 29. `--project` matches a name before an id (2026-10-05)

`vincent --project <value>` tries an exact project name first, and only then
an all-digit value as an id. Project names may be numeric (`validateName`
checks only non-empty and length), and name-first keeps a project named `3`
reachable. It is also how `tui.default_project` behaves, which is by name
only. A signed or spaced value is a name.

### 30. The startup notice: the working directory, or a fallthrough (2026-10-05)

Decision 4's "the notice names the rule" is applied as one line under the
header, raised in exactly two cases, and cleared by the next key like the
keymap notice. A pick by the working directory says `◆ web — from the working
directory`. Any fallthrough names every rule that failed and what won:
``tui.default_project `api` is not registered — showing `web` (last used)``.
A pick by flag, config, last used or first by name is silent, because the
header segment already shows the project.

### 31. `selected_project` is written on every selection change (2026-10-05)

`tui.json`'s `selected_project` `{id, name}` is written through
`mergeTUIState` from `selectProject`, the one place the selection changes, so
the startup pick itself is written too: a `--project` or working-directory
launch makes that project the last used. 132.4's `@` switch reuses it. The
write runs off the update loop and a failure is not reported, for the board
folds' reason: the selection holds on screen, and the only cost is that the
next launch falls through to a rule below last used. With several TUIs, the
last writer wins.

### 32. The m11 gate carries `tui.default_project` (2026-10-05)

`scripts/m11-gate.sh` scenario 9: `PATCH /v1/config` refuses an empty or
over-long `tui.default_project` with the validation envelope and the file
byte-identical, and a valid value round-trips through `GET /v1/config` and
`vincent config get`. The key is syntax-checked only — config is a leaf and
looks nothing up — and is adopted by the TUI from its first config answer;
a later answer never moves the selection.

The working-directory comparison moved into a new leaf, `internal/pathx`
(`SameDir`, `Contains`), which `internal/worktree` and `internal/api` now
delegate to, so the TUI matches paths without importing a git-running
package. `GET /v1/tasks` always served `worktree_path` on the list row;
`apiclient.Task` now decodes it there rather than on `TaskDetail` alone.

### 33. The picker marks the current project only; the default marker is 132.3's (2026-10-05)

Taken with the author while delivering 132.4. `tui.default_project` arrives
with 132.3 (#697), which is in flight on its own branch and is not a
dependency of 132.4. So 132.4's picker marks only the current selection, and
132.3's pull request adds the default marker when it introduces the setting;
neither blocks the other.

*Note (2026-10-05):* 132.3 shipped without the marker, so it moves to its own
item, 132.18, rather than staying owed by a closed one (review F4 on #718).

Two smaller calls were made beside it. A project with a nil
`max_parallel_tasks` has no cap of its own, only the global one, so its row
reads `N running` with no denominator; showing the global cap would imply a
per-project limit that does not exist. A capped project reads `N/cap
running`. And `@` is the default because nothing else answers it: the chat
composer's file picker reads `@` from the draft and never matches it as a key
(`internal/keymap/fixed.go`), `ctrl+e` is the m11 gate's accepted rebind, and
`ctrl+k`/`ctrl+w`/`ctrl+n` are bubbles' text-editing keys. The op is not a
typing key, so a text field still types `@`, and the palette's "switch
project" row is the way in from one.

*Note 2026-10-05:* 132.3 and 132.4 were delivered in parallel and merged
together, and 132.3 was written before the picker existed, so it did not add
the marker. The picker still marks only the current project; the default
marker remains open against 132.3's row.

### 34. The interim attention clause is global and labelled (2026-10-05)

Taken with the author while delivering 132.8. Until 132.14 (#708) brings the
badge and the project-crossing `!`, the board header's `! N need attention`
keeps counting every project's tasks: a selection is a filter and must not
hide a question (decision 2). When some of that count is in projects other
than the selected one, the clause reads ` (all projects)`, the way
` (all tasks)` already marks a committed filter; when both apply, the one
` (all projects)` covers both, being the wider statement. The footer's
`! next attention (N)` takes the same label, as `(N, all projects)`. `!`
(`shell.jumpAttention`) walks `visible()` and is therefore per project in
this item; that is accepted deliberately, and 132.14 replaces both the
clause and the jump.

*Alternatives beaten:* scoping the count now, which hides another project's
question and so breaks decision 2; leaving the mismatch unlabelled.

### 35. Folds are kept per project (2026-10-05)

Taken with the author while delivering 132.8. The task board's fold set is
keyed by project id, so a board's folds are independent per project whatever
`group_by` says — which matters most once 132.9 makes `[workflow]` the
default, because a shared `["build"]` fold would collapse `build` in every
project. In memory the board holds `map[int64]foldSet`; the render, the four
fold keys and `!`'s auto-expand read and write the selected project's set. In
`{data_dir}/tui.json` the sets live in `board_folds_by_project`
(`{"<id>": [[...], ...]}`), written through the same merge as before.

The legacy `board_folds` list is read once and migrated when the root's
project list is known, then dropped on the next write; until then it is held
unmigrated and not written back. A legacy path whose first segment names a
registered project moves under that project's id; a path that was only the
project segment (a folded project header, which a one-project board no
longer draws) is dropped; a path whose first segment names no project — a
`[workflow]` grouping's — is dropped too, because copying it into every
project would recreate the sharing this removes.

A successful live load prunes each project's set against that project's own
tasks from the global list, and a project with no live tasks keeps its set
(the existing "an empty list prunes nothing" rule). An archived load prunes
nothing, because a page and a date window say nothing about which groups
exist; the archived board still shares the sets (task 054 decision 1). A
removed project's set is dropped when it leaves the cached project list.

*Amended 2026-10-05 (review of the 526–530 train, F10):* keying by id does
not by itself survive a rename, because a stored path keeps the project's
name as a segment (the delivery note below). The board remembers the name
each project's paths were last seen under and, when the project list or a
task load reports a new one, rewrites that segment before pruning. A rename
made while no TUI was running is not seen, and that project's folds go.

*Note (2026-10-05), at delivery:* the decision as taken said a migrated
path is stored with its project segment stripped, on the premise that a
scoped board's paths no longer carry one. They do: a level `shownLevels`
skips still contributes its value to every header path under it (task 129
decision 4, `headerPaths`), so under today's default `[project, workflow]`
the scoped board's `build` header is `["api", "build"]`. A migrated path
therefore keeps its segment, or it would never match the header it was made
on. Whether paths drop the project level is 132.9's, with the grouping.

*Amended 2026-10-05 (132.9):* they do now — see decision 51's delivery note.
The F10 rename rewrite above is removed with the level.

*Alternatives beaten:* shared label paths, which leak a `[workflow]` fold
across projects; prefixing stored paths with the project *name*, which breaks
on a rename.

### 36. One root confirmation for drafts (2026-10-05)

Settled with the author while scoping 132.6. Draft-holding views implement a
`switchGuard` interface, and the root holds the pending switch and draws a
single y/n confirmation. Cancelling it cancels the switch, and any open
waiting on it. *Beaten:* giving each form its own prompt, with
`ntConfirming` reused and five new confirm states to keep consistent.

### 37. A confirmed discard lands on a fresh form for the new project (2026-10-05)

The view kind is kept, as the requirement says. A seeded form becomes a blank
one, and forms inside a detail view leave with it. *Beaten:* falling back to
a list.

### 38. Every open carries its project; an unknown one is fetched first (2026-10-05)

Every source sets `projectID` from the row it holds, and the ledger resolves
its trigger's `source.project`. A zero or unresolvable project makes the root
GET the object before it switches and routes, so the order is always switch,
then route. *Beaten:* switching once the detail loads, which needs an
exemption from the fallback rule and briefly draws the wrong header; and
making the field mandatory with 0 meaning "do not switch".

### 39. The workflows view loads with two calls, not one (2026-10-05)

Settled with the author while delivering 132.12. Decision 7's "as
`?project_id=` already returns them" cannot show shadowing:
`Registry.List(projectID)` merges by name, so a global or builtin entry the
project overrides is missing from that response. The view issues
`ListWorkflows(0)` (builtin + global) and `ListWorkflows(selected)` and
compares them in the client; `ListProjects`, the per-project fan-out and the
per-scope blocks are gone. The acceptance criterion becomes "at most two
listing calls per load, none for any other project".

*Alternatives beaten:* a `shadows` field on `GET /v1/workflows`, which changes
the API, MCP and §13.2 against this task's daemon-unchanged model; one call
with no shadow note, which is the "hides shadowing" option decision 7 already
rejected.

### 40. An overridden global or builtin entry stays listed, dimmed (2026-10-05)

Settled with the author while delivering 132.12. It is marked "shadowed here
by `<project>`" beside the project entry marked "shadows global X" (or
"shadows builtin X"), and the overridden global file can still be opened and
edited from the view, under the "affects every project" warning every global
row carries.

*Alternative beaten:* showing only the winning entry, which makes the global
file unreachable from any project that overrides it.

### 41. The unassigned trigger band shows in every project's triggers view (2026-10-05)

Settled with the author while delivering 132.12: option (a) of #706's open
question, and what decision 8's "stays repairable from the TUI" already
implies. Cross-project content leaks only for files whose project cannot be
read, and that leak is accepted.

*Amended 2026-10-05 (review of the 526–530 train, F2–F4):* `GET
/v1/triggers` now carries the project an invalid file still names, so only a
file with no readable project lands in the band. A trigger whose project was
removed — valid, and possibly still enabled — joins the band too, rather
than vanishing from every view, and its form keeps the project picker so it
can be reassigned.

*Alternative beaten:* showing them only in the overview (132.15), which leaves
no repair path in the triggers view.

### 42. Enter on an overview row selects and returns to the last scoped view (2026-10-05)

Taken with the author while delivering 132.15. Enter on a project row of the
overview selects that project and returns to the last project-scoped view
that was active, or to the board when there is none. The root keeps no view
history otherwise, so it gains one field — the last `projectScoped` view,
recorded in `switchTo` as that view is left. Enter on a "needs you" row
selects the task's project and opens the task, also through
`selectProject`, with `esc` back to the overview. A view showing one record —
a task, a chat, an issue — belongs to the project it was opened in, so after
a switch it gives way to its own list (board, chats board, issues list)
rather than show another project's record under the new selection.

*Alternatives beaten:* always the board (E2); select and stay on the
overview (E3).

### 43. The overview keeps a detail surface on wide terminals (2026-10-05)

Taken with the author while delivering 132.15. The table and the "needs you,
across projects" list are the view; on a wide terminal the highlighted
project's repository and execution defaults still show beside them, and the
add/edit form still takes the focused surface as it did. On a narrow terminal
the detail pane is shed first, then columns. This departs from task 020
decision 1's rail-plus-focus shape (already listed as superseded below) while
keeping the at-a-glance configuration it gave. The old focus pane's
client-filtered "Current workload" is dropped: the row's own figures and the
attention list replace it.

*Alternative beaten:* the table and the attention list only, which hides the
defaults behind the edit form.

### 44. Open pull-request counts are a follow-up (2026-10-05)

Taken with the author while delivering 132.15. The overview's GitHub cell
shows only the root's existing per-project §13.2 probe — `✓ owner/repo`, the
probe's reason, or `—`. Lazily loaded `ListGitHubPulls(state=open)` counts
are filed as a new issue after 132.15 lands.

### 45. Spend is deferred (2026-10-05)

Taken with the author while delivering 132.15. The overview has no spend
column. It is revisited only when asked, after timing the `step_runs` ×
`tasks` scan on a store of at least 100k step runs; chat cost stays apart by
spec decision row 29.

### 46. A deleted selection falls to the default project, then the first by name (2026-10-05)

Taken with the author while delivering 132.7. When the selected project is
deleted, the replacement is `tui.default_project` if it names a
still-registered project, otherwise the first project by name, otherwise
nothing (`◆ no project`). The `--project` flag and the working directory are
launch facts and do not apply mid-session; last-used is the project just
deleted. Honoring the default is consistent with decision 32: it is the
user's stated preference applied to a selection that no longer exists, not a
later config answer moving a live one. The rule is the tail of the startup
chain (`reselectAfterDelete` runs `resolveStartupProject` with only the
default and the list), and a one-line notice names both projects and the rule:
``project `api` was deleted — showing `web` (default project)``, or
`(first by name)`, or ``— no projects remain``.

*Alternative beaten:* the first by name only.

### 47. A dirty draft on delete asks, with no way to stay (2026-10-05)

Taken with the author while delivering 132.7. Decision 36's root y/n
confirmation is raised as for any switch, its prompt saying the project was
deleted, so the human may copy text out first. Only `y` answers it: the
target of "stay" is gone. The same holds when nothing remains. A switch to no
project from a deleted one is still a switch, so 132.6's detail fallback and
form re-targeting run.

*Alternatives beaten:* discarding the draft silently; carrying the draft to
the new project.

### 48. A reconnect reloads only the views whose last load failed (2026-10-05)

Taken with the author while delivering 132.7. Each stamped view records
whether its last accepted load failed (`projectScope.loadFailed`), and on a
`ConnectedNote` after a drop the root walks the views directly, as it does
for `setProject`, and reloads those. A switch made while offline issues loads
that fail (decision 25), so it needs no tracking of its own. A clean
reconnect adds no fetch beyond the projects relist and the GitHub re-probe.
The picker opens while reconnecting, on the cached list, with a dim "offline —
list may be stale" line.

*Alternative beaten:* reloading every scoped view on every reconnect.

### 49. A delete is detected from the project list, not the event (2026-10-05)

Taken with the author while delivering 132.7. `updateProjectList` treats a
selection the refreshed list no longer carries as deleted. `project.*`
events and reconnects already relist, so one path covers a live delete, one
made by another client, and one made during an outage. The apiclient's
cursor-at-zero replay gap (`events.go`, a stream that never saw an event
resumes live rather than replaying) is known and out of scope: with
detection on the list, a lost `project.deleted` is harmless for the
selection. A rename of the selection is also written to tui.json's
`selected_project` (decision 31).

*Alternative beaten:* acting on `project.deleted` alone, which misses
deletes made during an outage.

### 50. A write refuses `project` only when it sets `tui.board.group_by` itself (2026-10-05)

Taken with the author while delivering 132.9. `PATCH /v1/config` decodes the
whole candidate file (task 128 decision 2), and almost every installation's
bootstrapped file carries `[project, workflow]`, so refusing the whole file
would block every unrelated PATCH on nearly every upgraded install —
`vincent config set log_level debug`, and every save from the TUI config
editor. So the check splits: the candidate file is decoded with the
deprecation handled **leniently** (`project` stripped, the bytes outside the
patched key untouched), and the PATCH is refused with `validation_failed`
when the **patch's own** `tui.board.group_by` value contains `project`, the
file byte-identical and the error naming the key and why. This narrows task
128 decision 2's whole-file posture for this deprecation only (see
Supersedes); the `tui.keys` posture is unchanged.

*Alternatives beaten:* whole-file strict, which blocks every PATCH on legacy
files; stripping on any write, which rewrites a key the caller never asked to
touch.

### 51. `[project]` alone strips to `[]` (2026-10-05)

Taken with the author while delivering 132.9. The strip is literal, not a
reset to the default: under scoping `[project]` already renders a flat board,
so nothing visible changes. `[workflow, project]` strips to `[workflow]`. A
duplicated `project` is still the "listed twice" error — malformed, not
legacy.

*Delivery note (2026-10-05):* with the level gone, a fold path carries no
project segment, so decision 35's F10 rename rewrite had nothing left to
rewrite and was removed, and the legacy `board_folds` migration now strips
the project segment — decision 35's original premise, which its delivery note
deferred to this item. Per-project sets written under the old grouping lead
with a project name, name nothing, and are pruned; no migration of them was
made.

## Supersedes

Every binding record this work overturns, departs from, refines or keeps, by
file and decision number. Each one keeps governing the code until the named
item's pull request merges.

- **Task 130's opener**, "the cross-project TUI navigation is kept (unlike
  #649)" (`130-issues-pillar.md:26-30`) — **superseded** (decision 1).
- **Task 130 decision 16.1**, "The list is cross-project and grouped by
  project" (`130-issues-pillar.md:444`, in decision 16 at `:439-447`), and spec
  §15 view 12 — **superseded** by 132.11.
- **Task 009**'s `[project, workflow]` default and its "read project by
  project" rationale (`009-configurable-tasks-view.md:13`), and spec §15
  Grouping — **departed from**: the `project` level is deprecated and the
  default becomes `[workflow]` (decision 6, 132.9).
- **Task 067 decision 6**, "The chats board groups by project and by nothing
  else" (`067-chats-in-the-tui.md:61`), and spec §15 view 8 — **superseded**:
  the board is flat and scoped (132.10).
- **Task 052.6** and spec §15 view 7: "Every available project's **open** pull
  requests, grouped by project", "answers the cross-project question" —
  **superseded** (132.11).
- **Task 096 decision 14**, the triggers takeover framed installation-wide
  (`096-event-triggers.md:408`) — **departed from**: the takeover is filtered
  client-side (decision 8, 132.12). **Task 096 decision 8** (`:307`, "Triggers
  are global-scope only") is **kept**: trigger *files* stay global, with no
  `.vincent/triggers/`, because only the display is filtered. Decision 14's own
  "Beaten" note cites decision 8, which is untouched.
- **Spec §15's attention clause**, "`! N need attention (all tasks)`, the
  count staying global" — **refined, not dropped**: the count stays global
  through the badge (decision 2, 132.14).
- **Spec §15 view 4 (Projects)** and its design record, **task 020 decision 1**
  (`020-guided-takeover-layouts.md:18`, the rail plus one focused surface) and
  the #324 amendment — **superseded in place** by the overview (decision 5,
  132.15).
- **Task 128 decision 2** (`128-keymap-upgrade-tolerance.md:29`, a PATCH
  decodes the whole candidate file strictly) — **refined** for the deprecated
  `project` level of `tui.board.group_by` only: the candidate file strips it,
  and a PATCH is refused only when it sets the level itself (decision 50,
  132.9). Its `tui.keys` posture is kept.
- **Task 129 decision 4** (`129-tui-monitoring-redesign.md:119`, a group level
  with one distinct value draws no header) — **kept**; it is why the `project`
  level goes inert under scoping (132.9).
- **Task 054 decision 1** (`054-collapsible-board-groups.md:27`, folds live in
  `{data_dir}/tui.json`) — **kept**, but it constrains the work: pruning
  becomes per project (132.8), and `selected_project` joins `tui.json` (132.3).
- **Task 009 decision 1** (TUI configuration lives in `config.yaml`, relayed by
  the daemon; restated at `internal/tui/firstrun.go:17-19`) — **kept**:
  `tui.default_project` is config, and the last-used project is view state in
  `tui.json`.
- The 2026-10-02 `NOT_PLANNED` closures of #637, #640, #649, #650 and #657 —
  **reversed** by the author on 2026-10-04 (decision 1).

## Tasks

In #693's delivery order. Items without a `Depends:` can proceed in parallel.
Each item amends the spec sections and public pages its code makes true, in
its own pull request.

- [x] **132.1** ([#695](https://github.com/lezli01/vincent/issues/695))
  `GET /v1/projects?stats=true` and `GET /v1/projects/{id}?stats=true`,
  `taskstate.NeedsHuman`, `store.ProjectStats`, `vincent project ls --stats`,
  the API and MCP docs, spec §11 and §13.2 (decisions 20–22). ✓ 2026-10-04
- [x] **132.2** ([#696](https://github.com/lezli01/vincent/issues/696))
  `projectSel` on the root, the `projectScoped` interface, the cached project
  list, the header segment, spec §15 Layout (decisions 23–24). Depends: this
  document. ✓ 2026-10-04
- [x] **132.3** ([#697](https://github.com/lezli01/vincent/issues/697)) The
  startup precedence, `tui.default_project`, `selected_project` in `tui.json`,
  `vincent --project` (decisions 29–32). Depends: 132.2. ✓ 2026-10-05
  The project picker's default-project marker, which decision 33 assigned
  here, did not land with it and is now 132.18's.
- [x] **132.4** ([#698](https://github.com/lezli01/vincent/issues/698)) The
  `project` op on `@`, the picker popup with stats, the palette row, the
  header segment as its click target, spec §15 Discovery, Layout, Keys and
  Mouse (decisions 23, 33). Depends: 132.1, 132.2. ✓ 2026-10-05
- [x] **132.5** ([#699](https://github.com/lezli01/vincent/issues/699)) The
  `{project, seq}` load stamp, the client-side event filter, the chats
  board's refetch debounce, spec §15 (decisions 25–28). Depends: 132.2.
  ✓ 2026-10-05
- [x] **132.6** ([#700](https://github.com/lezli01/vincent/issues/700)) The
  view is kept across a switch; detail views fall back to their list; forms
  re-target or ask; an open follows the object's project; spec §15
  (decisions 36–38). Depends: 132.2. ✓ 2026-10-05
- [x] **132.7** ([#701](https://github.com/lezli01/vincent/issues/701)) Zero
  projects, a deleted or renamed selection, reloads on reconnect, the first
  project added (decisions 46–49). Depends: 132.3, 132.5. ✓ 2026-10-05
- [x] **132.8** ([#702](https://github.com/lezli01/vincent/issues/702)) The
  board filtered in memory; the archived tasks board filtered on the server;
  per-project fold pruning; no PROJECT column; the `/` filter without the
  project name (decisions 34, 35). Depends: 132.5. ✓ 2026-10-05
- [x] **132.9** ([#703](https://github.com/lezli01/vincent/issues/703)) The
  `project` level of `tui.board.group_by` deprecated, the default `[workflow]`
  (decisions 6, 50, 51). Depends: 132.8. ✓ 2026-10-05
- [x] **132.10** ([#704](https://github.com/lezli01/vincent/issues/704)) The
  chats and archived chats boards, flat and scoped. Depends: 132.5.
  ✓ 2026-10-05
- [x] **132.11** ([#705](https://github.com/lezli01/vincent/issues/705)) The
  issues list and the pull-requests takeover scoped; the pull-requests gate
  follows the selected project's probe; spec §15 views 7 and 12.
  Depends: 132.5. ✓ 2026-10-05
- [x] **132.12** ([#706](https://github.com/lezli01/vincent/issues/706))
  Resolved workflows; triggers filtered client-side, with the "unassigned"
  band; spec §15 views 5 and 11 (decisions 7, 8, 39–41). Depends: 132.5.
  ✓ 2026-10-05
- [ ] **132.13** ([#707](https://github.com/lezli01/vincent/issues/707))
  Locked project fields on forms; `projectHinting` removed. Depends: 132.2.
- [ ] **132.14** ([#708](https://github.com/lezli01/vincent/issues/708)) The
  chrome badge, the global bell, the project-crossing `!`, the per-project
  board header. Depends: 132.6, 132.8.
- [x] **132.15** ([#709](https://github.com/lezli01/vincent/issues/709)) The
  overview replaces view 4 (Projects). Depends: 132.4. Decisions 42–45.
- [ ] **132.16** ([#710](https://github.com/lezli01/vincent/issues/710))
  Project-aware `scripts/screenshots.sh` and a full recapture. Depends:
  132.2–132.15, 132.18; #692 (merged, so already met).
- [ ] **132.17** ([#711](https://github.com/lezli01/vincent/issues/711)) The
  human walkthrough record, and the m3 and task 129 amendments. Depends:
  132.16.
- [ ] **132.18** The project picker's `tui.default_project` marker, which
  decision 33 assigned to 132.3 and which shipped without it; spec §15's
  picker note and the TUI guide's picker section in the same pull request.
  No issue of its own yet. Depends: 132.3, 132.4.

The scoping items (132.8, 132.10–132.13) may merge in any order once their
dependencies land, and each one carries its own view's tests, guide section and
dated spec amendment.
