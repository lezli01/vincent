# 129 — The TUI answers what is happening, why it failed and what it delivered

**Status:** 🔄 in progress (12/19)

Issue [#591](https://github.com/lezli01/vincent/issues/591), part of
[#589](https://github.com/lezli01/vincent/issues/589). Spec §15 (TUI) above
all; §5.3/§5.4 (the fields a card reads), §8 (workflow structure a card
summarises), §12.3 (`tui.keys`, `tui.output.level`) and §13.2 (the commits
route) where an item touches them. Every item amends the spec in its own pull
request, never this one (see the per-item checklist).

## What this is

The TUI (`internal/tui`, spec §15) grew one feature at a time. Each piece makes
sense alone; together they make a user learn many places to answer three
questions:

1. **What is happening right now?** Live monitoring, across tasks and inside
   one task.
2. **Where and why did it fail, and what do I do about it?**
3. **What did a successful task deliver?** Changes, commits, pull request, the
   agent's own summary, cost, checks.

The requirement behind it (task 407), verbatim as #589 quotes it: *"I want to
enhance the UX in the TUI about how the different tasks, their steps can be
monitored, real time analysed. Currently it seems too complex and noisy, it
should be intuitive, easy to understand with a good learning curve to master it
and to easily find the different aspects, such as where and why something
failed, or what had been delivered in a successful task. Thing this through
thoroughly."*

From the board to the answer, as measured by #589's research at 2026-09-23:

| Question | Keys | Tabs visited |
|---|---|---|
| What is happening now | 2 | 2 |
| Where and why it failed | 4–6 | 3 |
| What it delivered | 7+ | 4–5 |

Every path starts on Steps & Attempts because opening a task always sets
`t.tab = taskTabSteps` (`internal/tui/taskview.go:301`, task 049 decision 2),
and the strip reads in build order — Steps, Task Details, Output, Diff,
Workflow, Step Details (`6`), Pull Request (`7`) — not question order
(`taskViewTab`, `taskview.go:21-41`). The goal is one key from the board for
each question: `enter` lands on a surface that matches the task's state, the
default view shows less, and the detail is one deliberate step away.

This document is the plan and the decision record; it changes no code. Each
`129.n` item below is one sub-issue of #589 and roughly one pull request.

## Decisions

Recorded 2026-09-29. #589's goals require that "every binding decision that is
relitigated is superseded by name, with a date, in the new task doc", and the
sub-issues ask this document to record each one (#590, #599, #603, #604,
#608). Each decision below is **taken now; its effect lands with its item.**
The older decision keeps governing the code until that item's pull request
merges, and that pull request writes the dated in-place note into the older
record and the matching §15 amendment into the spec. No older record is edited
here.

### 1. Overview at `0`, not Steps & Attempts, is the landing tab — supersedes task 049 decision 2's "(default)" clause, via 129.12

Task 049 decision 2 (`049-full-screen-task-workspace.md:27`) makes Steps &
Attempts the default tab. 129.12 (#599) adds a state-aware **Overview** tab
bound to `0` and opens every task on it: running shows what it is doing now,
blocked/aborted what failed and why, done what it delivered. No other tab is
renumbered.

*Alternative beaten:* cards pinned on top of Steps & Attempts, which
failure-diagnosis and delivery-summary each assumed. They would compete with
the attempt timeline for the 24-row height budget and give each question a
different home; Overview is one home for all three, and Steps keeps its job as
the timeline. A *dedicated* "Failure" tab one step away was rejected too:
Overview is the landing surface itself, not a place to go.

Only the "(default)" clause is superseded. The rest of decision 2 stands: one
detail sub-model holds the attempt cursor, `tab`/`shift+tab` primary with
`[`/`]` aliases and digit jumps, Diff fetched only when its tab opens. The strip
had already grown past decision 2's four tabs — Workflow (task 051), Pull
Request (task 068), Step Details at `6` with Pull Request moved to `7` (task
088 decision 1) — without a note in 049; 129.12's note should record that too.

### 2. Task 118 decision 3 — already amended in part by task 128 (129.1); nothing further here

Task 118 decision 3 (`118-user-configurable-tui-keymap.md:89`: the daemon
refuses a bad keymap) was amended in part on 2026-09-28 by task 128, which is
129.1: lenient on load, strict on write, a user override wins over an
upgrade-introduced default. The amendment is in 118's record (`118-…:120`) and
in task 128 (`128-keymap-upgrade-tolerance.md:3-4`, decision 2). This document
cites it and writes no second amendment. Task 118 decisions 6 (an override
replaces, never aliases — task 128 keeps it explicitly) and 8 (every place that
names a rebindable key renders the effective one) are kept.

*Why it came first:* a new fixed or default key refuses any user config that
already binds that key, and the daemon refuses to start on a bad keymap. So
every item that adds a key — 129.12 and 129.18 — depends on 129.1.

### 3. STATUS is kept at 120 columns — amends task 036 decision 9 and task 050 decisions 1–3, via 129.16

Task 036 decision 9 (`036-step-status-message.md:151`) sheds the STATUS column
first and gates it on the title's width — `minTitleWithStatus` then, `maxTitle`
(64) since task 050 decision 2 — which keeps it off below 164 columns on the
default project/workflow-grouped board and below 196 on a flat one
(`internal/tui/boardcols.go:198-200`, `columnsFor` at HEAD). It was already amended once, 2026-08-29, by task
050 decision 1 (`036-…:167`). Task 050 decisions 1–3 (`050-…:31`, `:50`,
`:65`) set the title ceiling, `maxTitle` and the allocation order; decision 3
was already superseded in part 2026-09-03 by task 083 (`050-…:69`). 129.16
(#604) amends all four so STATUS — the board's only "now" signal — survives at
120 columns, an all-empty COST column is shed, and delivered rows carry a pull
request marker.

*Alternative beaten:* leaving STATUS as the first column shed, which hides the
one changing signal at the most common width while constant columns stay.

Task 050 decision 4 (`050-…:99`, uniform row height clamped to 3) is **kept**;
variable per-row height is deferred (see Open questions).

### 4. A group level with one distinct value draws no header — amends task 054 decision 3 and task 009 decision 3 in part, via 129.15

#589's list says "009 d6 / 054 d3 amended"; the source says otherwise for 009.
129.15 (#603) proposes that a grouping level whose tasks share one value
renders no header. That amends:

- **task 054 decision 3** in part (`054-collapsible-board-groups.md:85`: "The
  header carries the `! n` attention badge") — with no header, the `! n`
  badge moves to the remaining header or to the rows;
- **task 009 decision 3** in part (`009-configurable-tasks-view.md:55`: "The
  header names it", so the grouped column is dropped) — with no header the
  value would be named nowhere, so 129.15 must say where it is shown.

**Task 009 decision 6** (no grouping by `state`) is **kept**: the level
vocabulary does not change.

*Alternative beaten:* two group-header lines above a single task, text that
says nothing the row does not.

### 5. The board's in-frame action line goes — amends v0 T3.4 "Actions live on the board too", via 129.15

The PR K decision "Actions live on the board too" (`docs/history/v0-tasks.md:353`)
renders `available_actions` as a dimmed footer line on the board. #589's list
omits it, but #603 asks this document to record it. 129.15 removes the in-frame
action line that duplicates the footer; actions stay on the board through the
footer, which task 094 decision 2 already budgets first. The v0 ledger is
frozen, so the note lands in this document and in 129.15's pull request, not in
`v0-tasks.md`.

*Alternative beaten:* keeping both lines, which shows the same keys twice.

### 6. The Output verbosity level is persisted — supersedes task 071 decision 3's "never persisted" clause, via 129.11

Task 071 decision 3 (`071-chat-workspace-verbosity.md:67`) keeps one shared
level and persists nothing. 129.11 (#608) persists it as `tui.output.level` in
`config.yaml`. The "one shared value across task and chat workspaces" half is
**kept**.

*Alternative beaten:* re-cycling the level on every launch, which makes a user
who reads at `quiet` pay the same keys each session.

### 7. Everything else is kept

| Decision | Where | Why it stays |
|---|---|---|
| The footer never wraps; one binding registry; keys are unhidden, not cut | `docs/history/v0-tasks.md:492-494` | Every new hint goes through the registry and the width budget; no key is cut to make room. |
| No teatest, no golden frames | `v0-tasks.md:295`, `:496` | Items test with pure model tests and `*live_test.go`, as the checklist says. |
| Refresh is snapshot-then-events, never polling (T3.2) | `v0-tasks.md:312` | The now-line and cards render from the same snapshot and events. |
| No "following" badge on a terminal run (T3.3) | `v0-tasks.md:340` | 129.8 draws follow state on the Output tab and must honour it. |
| Diff fetched on tab activation, never from events (T3.4) | `v0-tasks.md:358` | The outcome card's diff stat follows the same rule. |
| `!` is the attention-jump key (T3.11) | `v0-tasks.md:525` | 129.18 adds a next-failure key beside it; what `!` does inside a workspace is an open question. |
| Home is the board; opening is a routed transition (049 d1) | `049-…:16` | Overview is a tab of the workspace, not a board pane; the peek pane is deferred. |
| Task Details complete and read-only (049 d3); the detail sub-model stays intact (049 d4) | `049-…:38`, `:46` | Overview reads the same sub-model; Task Details keeps the raw codes for copy-paste. |
| `tab` means "next tab" on Workflow (051 d5); the overlay derives from the held rows (051 d7) | `051-…:87`, `:106` | Unchanged by any item. |
| Step Details at `6`, Pull Request at `7` (088 d1); shared attempt cursor (088 d7); own attempts only (088 d9) | `088-…:38`, `:124`, `:142` | Overview takes `0`, so nothing is renumbered. |
| One 120 ms tick (089 d1); indicator placements (089 d5); braille everywhere (089 d8) | `089-…:47`, `:97`, `:131` | 129.8 closes the gap 089 records at `089-…:189-197` — a note, not a decision — by drawing the indicator on the workspace Output tab. |
| The key rule is three clauses (093 d1) | `093-…:40` | Every new key is checked by the same clause checker. |
| Width decides admitted hints (094 d1); `N` counts this surface's keys (094 d3) and is computed from live rows (094 d5) | `094-…:42`, `:63`, `:95` | 129.6 fixes stale and duplicated hints inside these rules. |
| An override replaces, not aliases (118 d6); effective keys rendered everywhere (118 d8) | `118-…:197`, `:222` | See decision 2. |
| A step's status message is neutral (036 d6) | `036-…:117` | Never shown as a failure cause; every new card keeps status and failure apart. |
| Uniform row height (050 d4) | `050-…:99` | Variable height is deferred. |
| Descendants excluded; the rollup is derived (014 d13) | `014-workflow-fan-out.md:313` | 129.10 renders `by_state` from the existing rollup. |
| `iteration` is a column; the loop owns no row (016 d7, amended 2026-09-15 at `016-…:243`) | `016-workflow-loops.md:228` | 129.10's iteration strip reads the rows. |
| The board keeps 014 d13, the parent row is a disclosure (084 d1); Output shows one lane at a time (084 d5) | `084-…:72`, `:142` | 129.13 generalises lane blame without changing either. |
| No grouping by `state` (009 d6) | `009-…:112` | See decision 4. |
| `quiet` is a fourth level (085 d1) | `085-…:30` | 129.11 bounds command output at `quiet`/`compact` without redefining them. |
| One step-state palette (097 d2); off-graph attempts get words first (097 d3) | `097-…:37`, `:44` | 129.10's glyphs join the same palette, words beside colour. |
| Diff stats are computed client-side (100 d2) | `100-cli-task-diff.md:44` | No daemon-computed outcome column (#589 decision 4); 129.5 adds commits only. |

### 8. Lane breakdowns, loop strips and step pips reuse the step-state glyphs — via 129.10

Settled with the author on #605. None of these amends a recorded decision:
014 d13 and 084 d1 are kept (lanes stay out of the list and the counts), 016
d7 is kept (the loop strip is a line, never a loop row), and 097 d2/d3 are
honoured by reusing the one glyph set and palette.

1. **One glyph vocabulary**, `attemptStateGlyph`'s: `●` running, `✓` done, `×`
   blocked/failed, `!` waiting on a human (the board's attention badge), `○`
   not started, `■` stopped, `–` skipped. The issue's `▶` and `⏸` are not used
   — `⏸` would be a second word for paused. `approve` reads `✓` and `reject`
   `×`, as stepStateStyle already paired them. Lane clauses take task-state
   colours; step and iteration glyphs take `stepStateStyle`'s.
2. **The fan-out breakdown is in the board's STATE cell**,
   `awaiting_children (×1 !1 ●1 ✓2)`, ordered blocked, human wait, running,
   done, then the rest (a glyph-less state is spelled out, `1 paused`). Zero
   clauses are dropped. The cell's wrap-then-cut sheds from the tail, so the
   blocked clause survives at 80 columns. The same helper feeds the Steps tab's
   `round N · …` field. `ChildrenRollup.Summary` is retired; the TUI was its
   only reader. *As built:* list rows still carry no `children` (see 084's
   open note), so on the live board the cell shows the breakdown only where a
   row carries the rollup. Serving it on list rows is an API change this item
   did not take.
3. **Lane rows read `lane <lane_id>`**, falling back to the title without one.
4. **A loop iteration strip** — newest ten, oldest first, `…+k` for the cut —
   in the workspace header's loop clause and on one dim line under a `loop`
   step's header on the Steps tab. A pass is its worst newest-attempt body row.
   Multi-round `fan_out` tiers get none.
5. **Step pips**: in the workspace header one per top-level step from the step
   runs (a composite step is one pip); on the board derived from
   `current_step`/`step_total`/state and appended to STEP only with width to
   spare — never widening, wrapping or displacing STATE, and not changing
   129.16's allocation.

## Tasks

The sequencing: phase 0 preconditions, phase 1 vocabulary and labels, phase 2
new surfaces behind existing entry points, phase 3 default-view changes, phase
4 removals. Items are numbered in dependency order; anything without a
`Depends:` can proceed in parallel.

**Phase 0 — preconditions** (keymap safety, the docs-sync test, the data the
cards need):

- [x] **129.1** ([#590](https://github.com/lezli01/vincent/issues/590)) Keymap
  upgrade tolerance — delivered as [task 128](128-keymap-upgrade-tolerance.md),
  PR #616. ✓ 2026-09-28
- [x] **129.2** ([#593](https://github.com/lezli01/vincent/issues/593)) The
  reason catalogue — delivered as [task 127](127-reason-catalogue.md), PR #614.
  ✓ 2026-09-28
- [x] **129.3** ([#594](https://github.com/lezli01/vincent/issues/594)) A
  persisted `block_detail` and a tail-kept `result_summary` — PR #615.
  ✓ 2026-09-28
- [x] **129.4** ([#592](https://github.com/lezli01/vincent/issues/592)) A test
  tying the guide's key tables and screenshot references to the registry, and
  tab-label test pins derived from the enum — `internal/tui/docs_claims_test.go`
  and `taskViewTab.String()`. ✓ 2026-09-29
- [x] **129.5** ([#601](https://github.com/lezli01/vincent/issues/601))
  `GET /v1/tasks/{id}/commits`, read from the branch so it survives archive.
  ✓ 2026-09-29

**Phase 1 — vocabulary and labels:**

- [x] **129.6** ([#595](https://github.com/lezli01/vincent/issues/595)) The
  workspace's stale, duplicated and hidden key hints fixed; `R` repair and `E`
  edit & retry surfaced on blocked tasks. ✓ 2026-09-29
- [ ] **129.7** ([#596](https://github.com/lezli01/vincent/issues/596))
  Plain-language states and reasons, one glossary. Depends: 129.2. Ordering:
  see "Ordering with the open walkthroughs" if its wording reaches Output
  lines.

**Phase 2 — new surfaces behind existing entry points:**

- [ ] **129.8** ([#597](https://github.com/lezli01/vincent/issues/597)) Follow
  state, level and liveness on the Output tab; failed attempts open at the
  failure. Ordering: see "Ordering with the open walkthroughs".
- [x] **129.9** ([#598](https://github.com/lezli01/vincent/issues/598))
  Breadcrumb, a single task title, a live now-line. *Done 2026-09-29.* The
  breadcrumb stops at the tab; crumbs are ids (`#id`, `lane #id name`) and
  truncate from the left. The now-line reads a small side buffer of the live
  attempt's chunks, so it follows the stream without a fetch; the Overview's
  running frame drops `latest status`. Screenshots are left to #609.
- [x] **129.10** ([#605](https://github.com/lezli01/vincent/issues/605)) Lane
  breakdowns, loop iteration outcomes, step pips as glyphs (decision 8). ✓
  2026-09-29
- [ ] **129.11** ([#608](https://github.com/lezli01/vincent/issues/608)) A
  persisted `tui.output.level`, and bounded command output at `quiet` and
  `compact` (decision 6). Ordering: see "Ordering with the open walkthroughs".

**Phase 3 — default-view changes:**

- [x] **129.12** ([#599](https://github.com/lezli01/vincent/issues/599)) The
  state-aware Overview tab at `0`, the landing tab (decision 1). Depends: 129.1.
  *Done 2026-09-29.* Only fresh opens land on it: a back-stack pop restores
  the tab left, and the pull requests takeover's create route lands on Pull
  Request. The strip is drawn in two groups, and tab/⇧tab walk that drawn
  order; digits keep their tabs. The Steps & Attempts tab is relabelled
  Steps. `!` is left as it was (see Open questions). The Steps and loop
  tapes now press `1` after `enter`; re-capturing the Steps picture and adding
  a tape for the Overview are deferred to #609.
- [x] **129.13** ([#600](https://github.com/lezli01/vincent/issues/600)) The
  failure card on Overview. Depends: 129.12, 129.2, 129.3.
  *Done 2026-09-29.* Blocked and aborted tasks get the card; awaiting_input
  and awaiting_gate keep the plain needs frame. The anchor is the newest
  non-succeeded attempt at the current step, shared with the `3`/`6` links.
  Evidence is one normalized `tail=` fetch per failing attempt, never on an
  event. Lane blame is copied onto the card; the Steps tab keeps its copy
  until a later item removes it, and the Steps tab is not folded (see Open
  questions). The snapshot carries no retry budget, so the first line reads
  `attempt k` without "of n". No `D` key opens the daemon view, so the
  no-evidence line names the palette instead. Re-capturing the blocked
  Overview is deferred to #609.
- [x] **129.14** ([#602](https://github.com/lezli01/vincent/issues/602)) The
  outcome card on Overview. Depends: 129.12, 129.5.
  *Done 2026-09-29.* The card renders on done, archived and aborted (under
  the failure card, collapsing first). The result is the newest succeeded
  agent attempt's summary, else the newest non-empty one, else the task's
  status message. `3` could not also serve the result on aborted, so the
  result jump is a new `result` operation, default `w`, registered on the
  Overview. The diff and the commits are one fetch each per open; archived
  fetches no diff and says whether the branch was kept. The PR line reads the
  Pull Request tab's row and checks, asking that tab's checks fetch once.
  The Steps-tab `↳` preview is left out; screenshots are deferred to #609.
- [x] **129.15** ([#603](https://github.com/lezli01/vincent/issues/603)) A
  quieter board (decisions 4 and 5). ✓ 2026-09-29
- [x] **129.16** ([#604](https://github.com/lezli01/vincent/issues/604)) STATUS
  kept at 120 columns, an empty COST column shed, a pull request marker on rows
  (decision 3).

**Phase 2, after their phase-3 dependencies:**

- [x] **129.17** ([#606](https://github.com/lezli01/vincent/issues/606))
  Orienting help, and palette "go to tab" entries. Depends: 129.12.
  *Done 2026-09-29.* Help opens on a "This screen" header: the workspace's
  strip in drawn order with each tab's purpose, or the board's `enter`, `!`
  and `:`. It scrolls with keys local to the overlay (`↑`/`↓`, `pgup`/`pgdown`,
  `home`, `end`), which are not registry rows. The workspace palette gains a
  tabs group whose rows replay the tab's digit. Decisions: (1) no first-run
  orientation card, the §16 notice stays the only first-run screen; (2) help
  shows the whole action vocabulary, split into "Actions now" and a dimmed
  "Not available now", while the palette keeps omitting invalid actions;
  (3) Overview's jump links stay Overview-only and are not copied into the
  tabs group. No tape shows help or the palette, so pictures are deferred to
  #609.
- [ ] **129.18** ([#607](https://github.com/lezli01/vincent/issues/607)) An
  in-task next-failure key and a board attention-only filter. Depends: 129.1,
  129.13, 129.8.

**Closing:**

- [ ] **129.19** ([#609](https://github.com/lezli01/vincent/issues/609)) The
  three journeys in the TUI guide, and a full screenshot sweep. Depends:
  129.13, 129.14, 129.15, 129.17.

**Phase 4 — removals:** none proposed. A removal would need its own item, and
the deprecation-window question below answered first.

## Prior art

Each borrowed pattern, its source, and the item that borrows it, so a reviewer
can check the pattern against where it came from:

| Pattern | Source | Borrowed by |
|---|---|---|
| Failed steps expand on open | [GitHub Actions run logs](https://docs.github.com/en/actions/how-tos/monitor-workflows/use-workflow-run-logs) | 129.8, 129.13 |
| `f` cycles failures | [Buildkite build page](https://buildkite.com/docs/pipelines/build-page) | 129.18 |
| Errors on top | [Dagger observability](https://docs.dagger.io/features/observability/) | 129.13 |
| An outcome summary first, logs one step away | [`$GITHUB_STEP_SUMMARY`](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands), [Codex cloud](https://openai.com/index/introducing-codex/) | 129.14 |
| Crumbs; an errors-only filter | [k9s commands](https://k9scli.io/topics/commands/) | 129.9; 129.18 |
| An agent overview, and a peek pane | [Claude Code agent view](https://code.claude.com/docs/en/agent-view) | 129.12; the deferred peek pane |
| At most two disclosure tiers | [NN/g progressive disclosure](https://www.nngroup.com/articles/progressive-disclosure/) | The Overview-then-detail shape |
| Plain errors that name the next action | [NN/g error-message guidelines](https://www.nngroup.com/articles/error-message-guidelines/) | 129.7, 129.13 |
| Never colour alone | [WCAG 2.2 SC 1.4.1](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html) | 129.10's glyphs, words beside colour |

## Ordering with the open walkthroughs

Owner walkthroughs [#578](https://github.com/lezli01/vincent/issues/578) and
[#579](https://github.com/lezli01/vincent/issues/579) — 109.5 in
[task 109](109-claude-subagent-nesting.md) and 110.5 in
[task 110](110-claude-edit-deltas.md), both open — walk the Output tab at all
four verbosity levels. Any item that changes the Output line model or the
levels — 129.8, 129.11, and 129.7 if its wording reaches Output lines — lands
after both are walked, or its pull request says the walkthrough must be
re-walked at all four levels.

## Per-item checklist

Every `129.n` pull request, verbatim from #591:

- spec §15 dated amendment (plus §5/§8/§12/§13 where an item touches them);
- `docs/guides/tui.md`, `docs/features.md` (`:302-386`) and `docs/reference/configuration.md` updated where affected;
- screenshots named and re-captured with `scripts/screenshots.sh`, or explicitly deferred to #609;
- keymap: new keys listed, ids unchanged;
- tests: pure model tests, a `*live_test.go` for any new API field, a registry probe for any new binding;
- cross-platform: ≤80 columns, no-colour (`TestShellColourDowngrade`-style), Windows CI green;
- version skew: any new API field degrades at its zero value.

On screenshots: an item re-captures the shots it changes in its own pull
request, or names them and defers them to 129.19 (#609) explicitly in that pull
request. A `tui-*.png` left showing a screen that no longer exists, with
neither, is never acceptable.

## Open questions

Carried from #589, each for the item that must settle it:

- **`!` from the board vs in a workspace** (129.18): should `!` open the
  next needs-you task on Overview, or only move the board cursor? 129.12 left
  `!` exactly as it was — it moves the board cursor, and the `enter` after it
  lands on the Overview — so the in-workspace question is 129.18's alone.
- **Folding succeeded steps while blocked**: should the Steps tab collapse
  the steps that succeeded while a task is blocked, now that the failure card
  carries the failure? 129.13 left the Steps tab body unchanged, and its
  lane-blame copy with it; removing that duplicate is a later item.
- **Archived tasks** (129.14): is branch + pull request + commits enough, or
  must vincent keep a diff stat after archive? *Answered by 129.14:* branch,
  pull request and commits are enough; no diff stat is kept.
- **Deprecation window** (phase 4): a one-release overlap before any surface is
  removed?
- **Deferred investigations**, none filed: a daemon-derived per-step "current
  activity" field; serving the failure digest `writeFailureBlock` builds as
  `GET /v1/tasks/{id}/failure`; a workflow-declared `summary:` step marker;
  folding Step Details into Steps (would supersede 088 d1); a board peek pane
  (would relitigate 049 d1); variable board row height (would relitigate 050
  d4); band-first ordering across groups or a hide-settled toggle; a
  multiplexed live view for `parallel` branches.

Settled:

- **`block_detail` redaction** — by 129.3, [PR #615](https://github.com/lezli01/vincent/pull/615).
- **Screenshot cadence** — by the per-item checklist above.
- **Calm board** — by 129.15 (#603): the calm board is the only board, with no
  `tui.board.density` key and no escape hatch; an uninstalled adapter is
  omitted, neither dim nor red, and the doctor and daemon view keep listing the
  catalog.
