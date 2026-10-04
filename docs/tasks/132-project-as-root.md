# 132 — Project as root: the TUI scoped to one selected project

**Status:** ⏳ not started (0/17)

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
decisions 16–19 settle the contradictions between #693's research reports. Each
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

- [ ] **132.1** ([#695](https://github.com/lezli01/vincent/issues/695))
  `GET /v1/projects?stats=true`, `taskstate.NeedsHuman`, counts in
  `vincent project ls`, the API and MCP docs.
- [ ] **132.2** ([#696](https://github.com/lezli01/vincent/issues/696))
  `projectSel` on the root, the `projectScoped` interface, the cached project
  list, the header segment. Depends: this document.
- [ ] **132.3** ([#697](https://github.com/lezli01/vincent/issues/697)) The
  startup precedence, `tui.default_project`, `selected_project` in `tui.json`,
  `vincent --project`. Depends: 132.2.
- [ ] **132.4** ([#698](https://github.com/lezli01/vincent/issues/698)) The
  `project` op on `@`, the picker popup with stats, the palette row. Depends:
  132.1, 132.2.
- [ ] **132.5** ([#699](https://github.com/lezli01/vincent/issues/699)) The
  `{project, seq}` load stamp, the client-side event filter. Depends: 132.2.
- [ ] **132.6** ([#700](https://github.com/lezli01/vincent/issues/700)) The
  view is kept across a switch; detail views fall back to their list; forms
  re-target or ask; an open follows the object's project. Depends: 132.2.
- [ ] **132.7** ([#701](https://github.com/lezli01/vincent/issues/701)) Zero
  projects, a deleted or renamed selection, reloads on reconnect, the first
  project added. Depends: 132.3, 132.5.
- [ ] **132.8** ([#702](https://github.com/lezli01/vincent/issues/702)) The
  board filtered in memory; the archived tasks board filtered on the server;
  per-project fold pruning. Depends: 132.5.
- [ ] **132.9** ([#703](https://github.com/lezli01/vincent/issues/703)) The
  `project` level of `tui.board.group_by` deprecated, the default `[workflow]`.
  Depends: 132.8.
- [ ] **132.10** ([#704](https://github.com/lezli01/vincent/issues/704)) The
  chats and archived chats boards, flat and scoped. Depends: 132.5.
- [ ] **132.11** ([#705](https://github.com/lezli01/vincent/issues/705)) The
  issues list and the pull-requests takeover scoped. Depends: 132.5.
- [ ] **132.12** ([#706](https://github.com/lezli01/vincent/issues/706))
  Resolved workflows; triggers filtered client-side, with the "unassigned"
  band. Depends: 132.5.
- [ ] **132.13** ([#707](https://github.com/lezli01/vincent/issues/707))
  Locked project fields on forms; `projectHinting` removed. Depends: 132.2.
- [ ] **132.14** ([#708](https://github.com/lezli01/vincent/issues/708)) The
  chrome badge, the global bell, the project-crossing `!`, the per-project
  board header. Depends: 132.6, 132.8.
- [ ] **132.15** ([#709](https://github.com/lezli01/vincent/issues/709)) The
  overview replaces view 4 (Projects). Depends: 132.4.
- [ ] **132.16** ([#710](https://github.com/lezli01/vincent/issues/710))
  Project-aware `scripts/screenshots.sh` and a full recapture. Depends:
  132.2–132.15; #692 (merged, so already met).
- [ ] **132.17** ([#711](https://github.com/lezli01/vincent/issues/711)) The
  human walkthrough record, and the m3 and task 129 amendments. Depends:
  132.16.

The scoping items (132.8, 132.10–132.13) may merge in any order once their
dependencies land, and each one carries its own view's tests, guide section and
dated spec amendment.
