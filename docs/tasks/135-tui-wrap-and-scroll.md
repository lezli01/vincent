# 135 — TUI wrap and scroll: one wrap helper, one scrolling pane, screen by screen

**Status:** 🔄 in progress (1/2)

The umbrella record for [#784](https://github.com/lezli01/vincent/issues/784):
text that "can not be read" across the TUI, and an opened issue that cannot be
scrolled. Spec §15 "Text fields wrap" and "Wrap or truncate, never clip".

## What this is

Every audit under #784 traced most of its symptoms to one cause. The TUI had
no shared wrap helper and no shared scrolling pane, so screens emitted prose as
one line and left it to `frame()` (`internal/tui/shell.go`) to cut silently
with `…`, and content taller than its pane was clipped to its first rows with
no cue. Five local wrap helpers each got a different subset right.

135.1 adds the primitive pair and writes the convention down, with the help
sheet as its first adopter. Each screen issue of #784 then migrates its own
surface onto them as **135.n**, appended here when it is taken. The rule is
not new: it is spec §15's "Text fields wrap" (#299), task 070 decision 2
("truncation is stated rather than silent") and task 050 decision 8 ("wrap
plain text, then style"), made true for every surface.

## The convention

Written into `internal/tui/doc.go` and spec §15:

- Views wrap prose with `wrapText` and never leave it to `frame()`.
- List and table rows stay one line, truncated with `…`; the focused row's
  full text is shown wrapped elsewhere (as `linkpicker.go` does).
- Exact identifiers hard-wrap where they are the screen's subject.
- Content taller than its pane lives in a `scrollPane` with a cue; a static
  first-N clip is a bug.
- `frame()`'s truncation stays as the last-resort guard, and
  `assertNoFrameClip` (a test helper) fails a screen that reaches it.

## Tasks

- [x] **135.1** Shared `wrapText` and `scrollPane`, the convention, the help
  sheet first (#786). `wrapText` replaces `wrapPlain`, `wrapTaskDetailText`,
  `appendWrapped`, `appendWrappedIndented` and `wrappedHeaderValue` at every
  call site. The help overlay's labels wrap with a hanging indent at the
  label column, its key column is measured in cells and capped, its footer
  fits its width with the close keys never cut, and it scrolls in a
  `scrollPane`, the wheel included. The root's wheel path subtracts
  `chromeTop()`, as clicks do. ✓ 2026-10-20
- [~] **135.2** ([#797](https://github.com/lezli01/vincent/issues/797)) The
  new-task form. Done (2026-10-21): its title, branch and priority rows, open
  pickers and Fields editor input are sized from the pane the form is drawn
  in, not the terminal (N2), so no wrapped row reaches `frame()`; and
  `ctrl+s` creates from every mode but the discard prompt, keeping what is
  open first (N6). Open: N3–N5 and N7–N11 of the issue — row errors wrapped
  under their row, two-line Fields rows, windowing on the expansion's focus
  line, the Review stage's hints, marking every failed stage, the
  description's line numbers, wrapped collapsed rows, the empty-picker
  explanation — and per-mode footer contexts.
- [~] **135.3** The workflow editor, the workflow overview and the trigger
  screens (#802). Done (2026-10-21): the schema form (`layoutFormRows`,
  shared by the workflow editor and the triggers form) windows its rows
  around the cursor with the error and key hint pinned below (WF-6, TR-2);
  the cursor row's value and help wrap under the value column and other
  rows truncate with `…` (WF-7, WF-8); in-place input is indented under the
  value column and the label column is sized to the widest label, capped at
  24 (WF-9, WF-10); refusal notes go on their own line (TR-3); the full-pane
  editor draws every line in the height left to it (TR-1); the step modal
  owns the mouse (WF-12); the overview and modal hints use `opKey` (WF-13);
  the triggers list puts POLL after ARMED so it shows at 80 columns, and its
  banner wraps and its head lines are cut to the width (TR-7, TR-8). Still
  open: the overview, expanded entry and graph errors in a `scrollPane`
  (WF-2, WF-4, WF-11, WF-14); the dry run scrolling with its footer pinned
  (TR-4); the ledger's wrapped delivery error and stated 50-row cap (TR-5,
  TR-6); facts and findings past `trigDetailLines` (TR-9); and
  pgup/pgdn/home/end in the triggers list and ledger (PL-9).

## Decisions

1. **Narrow widths are honoured** (2026-10-20, #786). `wrapText` never
   returns a line wider than its width, clamped to 1; below 8 it hard-splits
   by display width. The one exception is a single grapheme wider than the
   width — a wide rune in a one-cell column — which has no narrower form.
   *Beat:* `wrapPlain`'s floor at 8, which existed only to hand the overflow
   to `frame()`, the very cut this convention calls a bug. The one test that
   pinned the floor (`TestAnswerFormWrapsInCells`) was widened to narrow
   widths on purpose.
2. **Recorded as task 135** (2026-10-20, #786), with #786 as 135.1 and the
   sibling screen issues of #784 as 135.n as they are taken.
3. **The issue's design, accepted as stated** (2026-10-20, #786). `wrapText`
   takes plain text and returns lines, never a joined string — a `\n` inside
   a measured line is the #299 width hazard. `scrollPane` wraps
   `viewport.Model` as the scroll engine but owns its keys and never forwards
   one to the viewport's keymap, whose `j`/`k`/`f`/`b`/`u`/`d` collide with
   app keys. *Beat:* `lipgloss.Style.Width`, which wraps styled text by
   cutting through it and measures its own padding, and the viewport's
   `SoftWrap`, for the same reason.
4. **No line ends in whitespace** (2026-10-20, 135.1). A blank paragraph is
   the lead it would have had, trailing spaces trimmed: an indent alone never
   makes a line, and a label with no value is still drawn. This is what the
   replaced helpers did between them — `appendWrappedIndented` drew blank
   paragraphs empty, `wrappedHeaderValue` drew an empty value's label.
