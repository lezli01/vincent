# 050 — Cap the board TITLE column and wrap overflowing cells

**Status:** ✅ done (2/2)
**Spec:** amends §15 (view 1, and *Grouping*)
**Amends:** task 036 decision 9

## Problem

`TITLE` is the only flexible column on the board — every other one is a fixed
constant in `internal/tui/boardcols.go` — and `columnSet.titleWidth` hands it
*all* the width the fixed set leaves. So the wider the terminal, the more of
the extra space goes to a column that usually does not need it, while the
columns that are actually being cut stay at the width they had at 80 columns.
On a 200-column terminal with the default grouping that is 100 cells of title,
mostly trailing blanks, beside a `STEP` still cutting `3/7 green · loop 4/10`
at 18 and a `STATUS` still cut to a clause at 28.

Second, independent of the widths: the board gives every task exactly one line.
`charm.land/bubbles/v2`'s `table.renderRow` renders each cell `Inline(true)`
and truncates, so anything that does not fit is dropped with an ellipsis and
cannot be read without opening the task workspace. That is fine for `ELAPSED`
and `COST`. It is bad for the three cells carrying prose or a rollup —
`STATE` (`awaiting_children (2 blocked)`, `queued → 14:20`), `STEP` (step name
plus loop rollup) and `STATUS` (§5.4's own message).

Both halves land as one task and one PR, because §15 and the
`docs/assets/tui-*.png` captures should be amended once rather than twice.

## Decisions

**1. The cap is a hard ceiling on the title, and it is the same number for a
flat board and a grouped one.** *(2026-08-29)*

Task 036 decision 9 recorded that "at every width a grouped board's title stays
strictly wider than a flat board's, and a test asserts it". No title cap can
keep that invariant: once both boards reach the ceiling their rendered titles
are equal. Rather than carry two ceilings, decision 9 is **amended in place**
(dated, citing this task) to the weaker and truer form — a grouped board still
gains the width it frees by dropping `PROJECT` and `WORKFLOW`, and is never
worse off than a flat board at the same width, but that gain lands wherever the
allocation order in decision 3 puts it rather than necessarily in the title.
The part of decision 9 that mattered is undisturbed: a *new column* must not
silently re-spend the freed width, and `STATUS`'s gate is what enforces that.

**Beat:** a second, higher ceiling for grouped boards, which would have kept
the literal wording of decision 9 at the cost of two constants that must be
kept in a relationship nobody can state, for an invariant that is a proxy for
"grouping pays" rather than the thing itself.

**2. `maxTitle` replaces `minTitleWithStatus`, at the same value (64).**
*(2026-08-29)*

The cap and the status column's admission gate are one fact — the title has
cleared a comfortable width, so there is room to spend elsewhere — so they are
one constant. Expressing the gate as `titleWidth(width) < maxTitle` leaves
`columnsFor` byte-identical at every width, which means the shedding ladder,
`minTitle = 16` and the whole narrow end of the board are untouched by part 1.
That is the cheapest possible way to satisfy the issue's narrow-board
criterion: there is no code path where a narrow board can render differently,
so the existing ladder and gate tests are the proof.

**Beat:** a separate `maxTitle` alongside `minTitleWithStatus`, which would be
two names for one threshold and an invitation for them to drift apart.

**3. Allocation order: title to `maxTitle`, then `STEP` to `widthStepMax`,
then `STATUS` to `widthStatusMax`, then the remainder back to the title.**
*(2026-08-29)*

> **Superseded in part, 2026-09-03 — [task 083](083-loop-legibility.md).** The
> allocation order stands; the measurement it was taken against grew. The loop
> rollup now names its body step, so the sample is
> `3/7 green · loop 4/10 · repair 2/3` and `widthStepMax` is **34**. A rollup
> too wide for the cell **drops clauses from the tail** — body step, then item,
> then counter — rather than taking decision 4's uniform second line: three
> clauses outgrow every width below the ceiling, and a whole board grown a line
> so one cell can finish a counter spends that height on the least of what the
> rollup says. Decision 4 still governs every other cell.

`widthStepMax = 32` fits `3/7 green · loop 4/10` (21 cells) with room for a
step name longer than the sample's; `widthStatusMax = 96` is a couple of the
board's lines of prose, past which wrapping is the better answer than a column
nothing else can see around. `STATE` is deliberately not in the order: the
recorded reasoning that keeps a hold's reason out of that cell — it does not
fit, and widening a column for a rare state costs every board the columns that
shed first (§15, task 036) — stands, and decision 4 is what makes its overflow
readable.

The give-back is not a softening of decision 1, it is what stops the board
leaving dead cells. Without it the *default* grouping at 160 columns renders 12
blank cells on the right: `STATUS` is gated off there, `STEP` fills at +14, and
nothing else can use what is left. Giving the leftover back to the title makes
that board identical to today's, and only lets the title exceed the cap once
neither other column has any appetite — grouped, above roughly 230 columns.

**Beat:** dividing the surplus proportionally between the three, which puts a
few cells nowhere useful at every width instead of filling one column at a
time; and spending nothing beyond the ceiling, which is the dead-cells board.

**4. Uniform row height, computed per render, clamped to 3.** *(2026-08-29)*

Every row on a board is the same height `H`, where `H` is the tallest wrapped
row currently on screen, clamped to `[1, 3]`; anything still overflowing at the
third line is truncated with `…` as it is today. A board where nothing
overflows is `H = 1` and renders exactly as it renders now. Uniform height is
what makes the row arithmetic a fixed multiplier rather than a per-row lookup,
which is what decision 5 needs.

**Beat:** per-row heights, which are strictly better typography and would have
required a row→line index and its inverse at every call site that counts rows —
the cursor, the click, the viewport, `bubbles`' own paging — for a table whose
cells differ by one or two lines.

**5. Wrapping is delivered as continuation rows inside `charm.land/bubbles/v2`,
not by owning the table.** *(2026-08-29)*

Upstream's `renderRow` is unexported and forces `Inline(true)`, so a multi-line
cell cannot be handed to it; and its scroll model is row-count based throughout
(`MoveUp`/`MoveDown` do arithmetic on `YOffset` against row indices,
`UpdateViewport` windows on `cursor ± viewport.Height()`, `SetHeight` subtracts
the header). A task therefore becomes exactly `H` `table.Row`s and a group
header stays one, which keeps **line == row** true — and with it `clickRow`,
`firstRowLine`, the viewport paging and
`SetHeight(max(3, b.height-b.chromeLines()))` all correct as written. The
issue's claim that the `SetHeight` budget has to move is wrong under this
design: it is a line budget, and lines are still rows.

The accepted cost is the cursor: `bubbles` applies `styles.Selected` only to
`r == m.cursor`, so the selected row's block is shaded on its first line only.
Faking it per cell was examined and rejected — `styles.Cell` pads outside our
styled text, so the shading would come out with unshaded one-cell gutters,
which reads worse than an honest single shaded line.

**Beat:** forking or vendoring the table, which buys a fully shaded cursor
block at the price of owning a scroll model, a viewport and a renderer that
upstream maintains.

**6. Four cells wrap; the rest keep truncating.** *(2026-08-29)*

`TITLE`, `STATE`, `STEP` and `STATUS` wrap. `ID`, `ELAPSED`, `COST`, the mark
column and `PROJECT` / `WORKFLOW` do not: the first three cannot meaningfully
overflow, and the last two are identifiers used for scanning, which a 14-cell
wrap makes unreadable — under width pressure they are shed, which is the answer
the ladder already gives for them. The mark glyph is on a row's first line
only; a column of ticks down a wrapped row would read as three marked tasks.

**7. Wrapping applies at every width, including narrow boards.**
*(2026-08-29)*

The issue's criterion "a narrow board renders as it does today" is about the
*column set and widths*, which decision 2 leaves byte-identical below 64. It is
not about row height: 80–120 columns is where cells are cut worst, and the
issue rejects the widths-only alternative for exactly that reason.

**8. Wrap the plain text, then style the lines.** *(2026-08-29)*

`renderBoardState` and the mark cell returned styled strings. Following the
output pane's recorded precedent (v0 T4.16 — "the pane wraps itself … wrapping
plain text and styling after, so no ANSI-aware wrapping is involved"), a cell
is now built as `(text, style)` and wrapped before the style is applied.
`boardStateLabel` is the plain half of the state cell; `renderBoardState` is
still there as the two composed, for callers that want the whole cell.

## Alternatives considered

- **Widen `STEP`/`STATUS` only, no wrapping.** Cheap, no renderer work, and it
  removes most of the cutting on a wide terminal — but it does nothing at
  80–120 columns, where the cells are cut worst and the shedding ladder has
  already run.
- **Only the cursor row expands.** Keeps the row/line math almost intact, but
  the board still hides what every unselected row is saying, which is the case
  that matters when scanning.
- **Configurable column widths under `tui.board`.** Pushes the trade-off to the
  reader, at the price of new config keys, validation, a reference page and no
  good default — the default is what this task is about.
- **Leave it to the detail view.** That was the division of labour
  (`formatStatus` and `renderBoardState` both said so explicitly). The
  complaint is that it costs a view switch to read one clause.

## Risk

Uniform height plus wrap-at-every-width means a single long title at 80 columns
takes *every* row to three lines, and a 24-line terminal then shows about six
tasks. That follows from decisions 4 and 7 rather than contradicting them, and
the clamp bounds it. If it reads badly in the captures, the lever is the clamp,
not the design.

## Tasks

- [x] **050.1** `boardcols.go`: `maxTitle` in, `minTitleWithStatus` out at the
  same value; `widthStepMax` / `widthStatusMax`; the allocation order in
  `boardColumns`, with the give-back. `columnsFor` keeps its shape and its
  comments — the gate line changes constant name only.
  *Done-when:* the title stops at `maxTitle` and the surplus lands in `STEP`
  then `STATUS` at several widths under both groupings, including the give-back
  band; nothing is left unspent at any width; every width whose title is at or
  below the cap renders byte-identically to before, over all three groupings
  with and without the marker column. `TestStatusColumnDoesNotEatTheWidthGroupingFrees`
  and `TestGroupedColumnsAreDropped` carry the amended decision 9.
  ✓ 2026-08-29
- [x] **050.2** The wrap: `boardRow.line` and `selectable()`
  (`boardgroup.go`); `wrapCellLines` and `boardStateLabel` (`boardrows.go`);
  `boardCell`, `cellsFor`, `layoutCells`, `rowHeight` and the expanded `rows()`
  (`board.go`); `skipHeaders`, `clickRow`, `visible`, `restoreSelection` and
  `firstTaskRow` over continuations.
  *Done-when:* `H` is 1 when nothing overflows and the rows are what they were;
  it grows to 2 and 3 and never further, including for a 256-byte status
  message (task 036 decision 5); `3/7 green · loop 4/10` and
  `awaiting_children (2 blocked)` are both whole on a 200-column default-grouped
  board; a continuation carries the group indent, a blank mark cell and the same
  column count; a group header is one row at every `H`; `j`/`k` never rest on a
  continuation and `space`/`V` and the action bar's target are unaffected; a
  click on the second or third line of a wrapped row selects that row and a
  click below the rows still changes nothing. ✓ 2026-08-29
