# 094 — The footer fills its width, and says what it hides

**Status:** ✅ done (5/5)
**Issue:** #352
**Amends:** §15 — the footer paragraph, in place with a dated note. Decision
record row 32.
**Supersedes:** the phase 3 refactor decision and the PR R / T3.12 decision in
[the v0 ledger](../history/v0-tasks.md) — "focused-panel keys (max 5,
priority-ordered)" and "priority orders the five that fit". Only the number.
What those decisions were protecting is one line that never wraps and never
truncates the pinned escape hatch, and that invariant is unchanged here.
**Keeps, without relitigating:**
[054](054-collapsible-board-groups.md) decision 5 — the footer never names a
press that does nothing, so `shell.liveBindings` still gates the board's fold
rows, and a count of them would be naming them.
[049](049-full-screen-task-workspace.md) — `:` is the escape hatch that makes
every other key optional, and it stays pinned and untruncated.

## The problem

Both halves of the issue were real, and they were the same half twice.

`maxFooterHints = 5` dropped everything past the fifth hinted row in silence.
Eleven of the twenty-one binding contexts declare six to nine hinted keys, so
on more than half the surfaces one to four keys never reached the footer. On
the board the five survivors were `enter open`, `/ filter`, `↑/↓ select`,
`g group` and `space select`; `L lanes`, `←/→ fold` and `C/O fold all` were
cut, and nothing on screen suggested they existed. Two more classes were
invisible for the same reason: rows carrying no `hint:` at all, and — on a
narrow terminal — whatever the left-truncation `…` had eaten.

Meanwhile `pad := max(width-lw-pw, 2)` turned every column between the left
segments and the pinned segment into blank space. At 200 columns there was
room for every dropped key and nothing in it.

The palette is where those keys live, and the footer never said so. The pinned
`: commands` is always present, but it reads as a generic label rather than as
"three keys are hidden behind this".

## Decisions

**1 (2026-09-10). Width decides how many hinted keys are admitted, and the
count is exact rather than iterated.** For each candidate admission count `k`
— zero through the number of live hinted rows, taken as a strict prefix of
registry priority order — the whole rendered width is computed, including a
`+N` sized for the `N` that `k` implies, and the largest `k` that fits wins.
This settles the issue's first edge case outright: there is no "admitted
because `+9` shrank to `+8`" state to detect after layout, because every
candidate was measured against its own `N`. A prefix rather than a best fit
because priority means priority: a wide row is never skipped to squeeze in a
narrow one behind it. The registry lists at most a dozen rows per context, so
the sweep is free.

**2 (2026-09-10). The hint budget reserves the segments that follow it.** The
line truncates from the *left*, so hints are what a full line loses first.
Admitting them against the whole `avail` would let the task actions, `!`,
`r retry` and the action-bar status push the just-admitted hints straight back
off the line. The composed right-of-hints segments are measured first and their
width — plus the separators and the `+N` — comes out of the budget before any
hint is admitted. When they alone exceed `avail`, no hint is admitted,
truncation eats into them, and `N` reports what was lost.

**3 (2026-09-10). `N` counts this surface's own keys, not everything the
palette lists.** Counting every palette-reachable entry would include the five
global rows and the eight view-navigation entries, putting `N` around fourteen
on every board and never at zero — which contradicts the issue's own "no `+N`
when nothing is left over". `N` counts, against the rows `buildFooter` was
handed:

- panel-context rows the palette would list (`noPalette` rows excluded — the
  form and popup contexts are entirely `noPalette`, so `+N` is absent there,
  which is correct: `ctrl+p` opens a palette that lists none of those keys)
  whose key is not advertised on the rendered line, whether it lost the width
  contest, carries no `hint:` at all, or was cut by the `…`;
- task-action segments the `…` removed.

Global rows are never counted: the pinned `: commands  ? help  q quit` is what
stands for them. The reconnect `r retry` hint has no registry row at all — the
only `r` row is the §6 retry action — so it is not palette-reachable and never
counts. The action bar's status segment is unclickable text and never counts.

**4 (2026-09-10). Alias rows are declared in the registry, not parsed out of
hint text.** Four unhinted rows are already advertised by a sibling's hint:
`right` and `O` on the board sit behind `←/→ fold` and `C/O fold all`, and `C`
does the same on the timeline and on the diff. Counting them as hidden would
put a permanent `+2` on a grouped board with nothing actually hidden. `binding`
grows `aliased`, and `TestAliasRowsAreDeclared` asserts it in both directions
against what the hints actually say — so a new alias pair fails a test rather
than inflating a count, and a stale mark fails it rather than quietly dropping
a key from the count. Splitting the hint on `/` and mapping `↑↓←→` back to key
names *at run time* was rejected: it is text parsing over a human-written field
that would break silently the first time a hint is worded differently. The test
does that parse, where breaking is the point.

**5 (2026-09-10). `N` is computed from the footer's live rows.**
`root.footerLine` already hands `buildFooter` a set filtered by `withoutGitHub`
and `shell.liveBindings`, so with `group_by: []` the board's inert fold keys
are absent and are not counted — task 054 decision 5 says the footer never
names a press that does nothing, and a count of them is naming them.
`paletteEntries` never had the fold gate and still lists those three rows; that
mismatch is left alone here and is its own issue, not this one's to widen into.

**6 (2026-09-10). What does not change.** `bar.capturing()` replaces the left
side outright and gets no `+N` — the pending `y/n` owns the keyboard, so
nothing else is actionable. `helpFooter` is a separate line. `width <= 0` and
the below-floor path (the pinned segment alone, never truncated) keep their
returns. `+N` sits after the action segments, so `actionOrder`'s stability is
untouched, and it is a normal `footerSeg` keyed `:`, which gives it its click
and its "a cut span cannot be clicked" behaviour for free.

## Tasks

- [x] **094.1 — Width decides.** ✓ 2026-09-10 `maxFooterHints` deleted;
  `footerAdmit` sweeps the candidate counts against a budget that already holds
  the right-of-hints segments and the `+N`.
- [x] **094.2 — The `+N` segment.** ✓ 2026-09-10 `footerMoreSeg`,
  `footerCountable`, and the fixed point that settles the count against what
  the `…` removed.
- [x] **094.3 — Alias rows.** ✓ 2026-09-10 `binding.aliased` plus the four
  rows that carry it, and `TestAliasRowsAreDeclared`.
- [x] **094.4 — Tests.** ✓ 2026-09-10 `TestFooterFiveKeyCap` replaced by seven
  cases asserting against widths and against the registry, never against a new
  constant.
- [x] **094.5 — Docs.** ✓ 2026-09-10 §15 amended in place, decision record row
  32, the TUI guide's key section, `CHANGELOG.md`.
