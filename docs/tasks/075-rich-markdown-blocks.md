# 075 — Tables, links and code blocks in the output pane

**Status:** ✅ done (1/1)
**Issue:** #290
**Amends:** §15's task-073 Markdown amendment, and §16
**Follow-up to:** [073](073-assistant-markdown-in-output.md), which shipped the
Markdown renderer over a written-down subset and named tables and link
interaction as its stated follow-ups

Task 073 left tables, links and images outside the subset, held there as safe
literal text by `TestMarkdownOutsideTheSubsetStaysLiteral`, and captured a
fence's content while discarding its info string. This is that follow-up: three
constructs join the subset and fenced blocks gain a language header and a tint.

It contradicts no recorded decision. 073 decision 2's wrap-plain-then-style
invariant, decision 5's "only assistant prose is interpreted" and decision 7's
one-chokepoint sanitizing all survive it unamended, and the two parts of the
issue that would have strained them — chroma-grade highlighting and OSC 8 —
were resolved with the author before the work started and are recorded below.

The work is entirely client-side and lands in `internal/tui` alone: no daemon
package, no API route, no store migration. §13.3's chunks and the JSONL
transcript keep the agent's bytes exactly, and width, theme and colour profile
are things only the client knows. `agent.output` in both workspaces and the
chat's §17 retention fallback remain the only two doors, unchanged from 073
decision 5.

## Decisions

### 1. The highlighter is vincent's own, not chroma

A coarse token scanner — comment, string, number, keyword, punctuation, plain —
over a written-down language list, with a plain fallback for everything not on
it, in `internal/tui/markdownhighlight.go`.

This is the same posture as 073 decision 1's hand-rolled parser,
[017](017-workflow-visualization.md) decision 2's refused graph widget and
[055](055-release-check-and-self-update.md) decision 1's refused `sigstore-go`. The runtime
`require` block stays seventeen lines; chroma is present today only as a
linter's indirect dependency and stays there.

The cost is accepted and stated rather than hidden: this is not real parsing.
Scanning is per line and carries no state between lines, so a string or a block
comment spanning two lines is tinted as two independent lines, and a
nested-comment dialect will be mis-tinted. Nothing depends on the tint being
right, which is what makes that acceptable — see decision 3's property.

**Beat:** `alecthomas/chroma/v2` as a direct runtime dependency. Correct lexing
for ~250 languages, at a large dependency and several MB of binary in a project
that has twice recorded a refusal of exactly that trade.

### 2. A destination is discoverable through per-message numbered references

An inline link renders its label as prose carrying a dim `[n]`, and the message
ends with a block of `[n] dest` lines — one per distinct destination, numbered
per rendered message, identical destinations sharing a number. The exact
destination is therefore always on screen without OSC 8 and without a
keybinding, and the numbering is what a later picker action would name.

**Beat:** an inline host suffix — `label (example.com)` — which is compact but
is not the exact destination: two paths on one host render identically.
**Beat:** storing the URL as segment metadata and shipping nothing visible,
which fails the issue's own criterion the moment OSC 8 is absent.
**Beat:** a reader action shipped alongside. It needs its own key decision,
because the chat composer owns every letter key ([071](071-chat-workspace-verbosity.md)
decision 4, which is why verbosity is `ctrl+r` there), and the pane has no
per-record cursor to hang a picker on.

### 3. No OSC 8 hyperlink is emitted, in this task or by default

There is no reliable capability probe. The payload would carry an
agent-supplied URL inside an escape sequence, which is the one thing §16 as
amended by 073 decision 7 says the pane does not do. And a link spanning a wrap
boundary would have to be closed and reopened per line, under an invariant that
deliberately keeps escape sequences out of wrapping.

The reference block satisfies every acceptance criterion without it. OSC 8
becomes its own issue if it is wanted, with the escaping rules and the gating as
its subject.

The same reasoning gives the highlighter its safety property, asserted by
`TestHighlightingEmitsStylesNeverCharacters`: highlighting emits **styles only,
never characters**, so stripping the escapes from a rendered block reproduces
the fence's content byte for byte — tab expansion and the existing hard wrap
aside. "Highlighting must not change copying or transcript contents" is
therefore checkable rather than asserted.

### 4. Only inline links and inline images join the subset

Reference links (`[label][ref]`), autolinks (`<https://…>`), bare URLs and
titled links (`[label](url "title")`) stay literal, and §15's boundary list is
amended to say so rather than shrinking silently. A link whose destination
contains whitespace does not match, which is what makes the titled form fall out
without a rule of its own.

The consequence is stated rather than papered over: the issue's "give bare URLs
a compact presentation" is discharged as **literal rendering**. A bare URL
displays as exactly itself — the strongest reading of "without silently
changing them" — but gains no reference number and no styling.

### 5. The wrap-plain-then-style invariant is not touched by any of this

Table cells, highlight runs and link labels are all measured as plain text and
styled after layout. The issue's "measure columns using ANSI-aware terminal cell
width" is read as 073 decision 2 already settled it: `ansi.StringWidth` is the
measurement, and nothing in the wrap path ever sees an escape sequence.

The one mechanical consequence is that `wrapPre` became multi-segment. It
rendered `pl.segs[0]` alone, which a highlighted code line and a reference line
both outgrow; it now lays every segment out as one continuous stream of cells,
still measuring plain and re-opening a style run on each produced line.

### 6. A table's layout is arithmetic, and its degradation is a stated width

Two widths per column — *natural* (the widest cell) and *minimum* (the widest
unbreakable token; a cell that is a single inline-code span is unbreakable
whole, which is the issue's "preserving literal/code cells"). Then, against the
content column with two spaces between columns and no borders:

| available | outcome |
|---|---|
| Σ natural ≤ available | natural widths, drawn aligned |
| Σ minimum ≤ available < Σ natural | minimum widths, surplus shared in proportion to each column's remaining demand (natural − minimum) |
| Σ minimum > available | the stacked form |

Stating the middle case as arithmetic is what makes "allocate surplus width by
content rather than giving it all to one column" reproducible at every width,
and it is what `TestTableSurplusGoesWhereTheDemandIs` asserts directly. The
switch to the stacked form is likewise a computed threshold in
`TestTableDegradesToStackedAtItsStatedWidth`, not an observed one.

**Beat:** a clipped pipe table, which loses the cells the reader came for.
**Beat:** a horizontal scroll inside the pane, which is a second scrolling axis
and a keyboard/mouse ambiguity this pane has never had. The issue rejects both,
and the arithmetic exists so neither is ever needed.

A table cannot go through `wrapLine`, which is a one-dimensional layout of one
`paneLine`, so it has its own `mdTable` block kind and its own renderer emitting
composed lines directly, the way `mdRule` already renders without lines.

## What shipped

- `internal/tui/markdown.go` — info-string capture, table block scanning, link
  and image inline handling, the per-message reference registry, block dispatch.
- `internal/tui/markdowntable.go` — column measurement, surplus allocation,
  aligned rendering, the stacked fallback.
- `internal/tui/markdownhighlight.go` — the token scanner, its language table
  and the vincent-owned palette.
- `internal/tui/outputlines.go` — `wrapPre` is multi-segment.
- Tests: `markdowntable_test.go` and `markdownhighlight_test.go` are new, and
  `TestMarkdownOutsideTheSubsetStaysLiteral` lost its `table`, `table rule`,
  `inline link` and `image` cases and gained `autolink`, `bare url` and
  `titled link` in their place. `internal/tui` is covered by no gate script, so
  the tests are the whole assurance, as they were for 073.

## Not done here

A raw/rendered toggle, a copy-raw action and a link picker remain the follow-ups
073 decision 4 scoped; the reference numbering is what a picker would address.
OSC 8 is its own issue per decision 3. Reference links, autolinks and bare-URL
styling are out per decision 4, and are the obvious content of a later
subset-widening issue if agent output makes the case for them.

`scripts/screenshots.sh` was **not** re-run: the seeded assistant text contains
no table, link or fenced block, so the panel the existing shots photograph is
unchanged. Extending the seed to show the feature would mean regenerating the
shots with the script, never drawing them.
