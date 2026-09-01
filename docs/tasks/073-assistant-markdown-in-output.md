# 073 — Assistant Markdown in the output pane

**Status:** ✅ done (1/1)
**Issue:** #289
**Amends:** §15's "output pane's line model (T4.16)" paragraph and §16
**Follow-up to:** [071](071-chat-workspace-verbosity.md), which pointed both
workspaces at one renderer and so gave this defect two surfaces

Assistant prose reached the output pane as literal text: a heading kept its
`#`, a fenced block kept its backticks, a list lost its indentation. The pane
also measured width by counting runes, so an emoji, a CJK glyph or a combining
mark mis-filled every line it appeared on — a defect rich layout would only
amplify.

The work is entirely client-side and lands in `internal/tui` alone: no daemon
package, no API route, no store migration. That is not a scoping convenience,
it is the reason the feature is allowed to exist in this shape. §13.3's chunks
and the JSONL transcript keep the agent's bytes exactly, and width, theme and
colour profile are things only the client knows.

## Decisions

### 1. The parser is hand-rolled, over a written-down subset

A line-oriented block scanner plus an inline span splitter, emitting the
existing `[]segment` / `paneLine` model directly. No new module in `require`.

This follows the posture recorded twice already — [017](017-workflow-visualization.md)
decision 2 refusing a graph widget ("a large integration surface and
binary-size cost" for a small amount of drawing code) and
[055](055-release-check-and-self-update.md) decision 1 refusing `sigstore-go` ("a very
large dependency tree in a project whose runtime `require` block is fifteen
lines"). The supported constructs are a fixed list rather than CommonMark:
headings, paragraphs, emphasis, strong, ordered and unordered lists, nested
lists, blockquotes, inline code, fenced code, horizontal rules.

*Beat:* `goldmark` plus a custom AST walker — correct parsing for free, but a
new direct dependency and an AST layer that still has to be written, for a
subset this size.

*Beat, harder:* `charmbracelet/glamour`. It emits a pre-styled ANSI block with
its own word wrap and margins. That cannot be folded into the two-column
activity gutter, it breaks the wrap-plain-then-style invariant below, and it
drags in chroma, termenv and a probable lipgloss v1/v2 clash against the v2
this repo is on.

**The subset boundary is documented, not implicit.** Anything outside it —
tables, links, reference links, HTML blocks, footnotes, setext headings,
backslash escapes — renders as safe literal text, and
`TestMarkdownOutsideTheSubsetStaysLiteral` is where that is held. Tables and
link interaction are the stated follow-ups.

### 2. Cell width, and the wrap-plain-then-style invariant is kept

Read literally, the issue's "replace rune-count width calculations in this path
with ANSI-aware terminal cell width" would supersede **task 050 decision 8**
and T4.16's recorded reasoning, which state the opposite: text is wrapped while
plain and each produced line is styled afterwards, "so no wrapping here has to
be ANSI-aware". Confirmed with the author: **the invariant stands unamended.**

What was actually wrong is the measurement. `cols()` counted runes, so an
emoji, a CJK glyph or a combining mark was mis-measured and the pane over- or
under-filled a line. It is now `ansi.StringWidth` — already a direct dependency,
already what `wrapCellLines` and twenty other TUI sites use. Nothing in the wrap
path ever sees an escape sequence, because the styles are still applied to
already-wrapped plain text.

Two consequences that are easy to miss, and are both in scope:

- `splitWords`' hard split for over-long words sliced `runes[:avail]`. Rune-
  indexed, so it was wrong for wide runes for the same reason `cols` was, and
  it cut ZWJ sequences in half. It is now a cell-width walk.
- `cols` is shared with `answerform.go`. Fixing it fixed the answer form's
  wrapping too, and that gets its own test rather than being silently
  inherited.

Because the invariant holds, the segment model already does the work: a
paragraph with inline `code` and **strong** spans is one `paneLine` with several
`segment`s, and `wrapLine` measures each token plain and styles runs after. No
new wrapping machinery for inline Markdown.

### 3. Fenced code hard-wraps at the cell boundary

A code line longer than the pane continues on the next line at the block's own
rail — no word breaking, no ellipsis, nothing unreachable. This is what the pane
already does for a long path or a base64 blob, and it is the policy T4.16
adopted when it replaced clipping.

*Beat:* truncate with `…`, which keeps a code block rectangular but puts the
tail of a long line out of reach of the TUI entirely.

*Beat:* no wrap, let the viewport clip — precisely the defect T4.16 fixed for
prose.

Fenced code cannot go through `splitWords`, which collapses runs of whitespace
via `strings.Fields`. It has a `pre` path that hard-splits at the cell boundary
and preserves every space. A tab is expanded to the four columns lipgloss draws
it as: `ansi.StringWidth` calls a tab zero columns wide, so measuring the tab
and rendering the spaces would disagree by exactly the block's indentation.

### 4. "Keep raw Markdown copyable" means the transcript, and ships nothing new

The criterion had no owner. The TUI has no copy-out action at all —
`clipboard.go` only reads, for paste — and terminal drag-select copies rendered
glyphs, so a heading's `#` is gone the moment it renders. There is no way for
this issue to make the *rendered* pane yield raw Markdown without inventing a
reader action.

So the criterion is discharged as the property that is already true and now has
a test: rendering is derived, never stored. The JSONL transcript and every API
payload keep the agent's exact bytes, and no render path mutates a
`TranscriptRecord`. A raw/rendered toggle key and a copy-raw action are both
plausible and both belong in the follow-up issues already scoped — a toggle in
particular needs its own decision, because the chat composer holds every letter
key ([071](071-chat-workspace-verbosity.md) decision 4).

### 5. What stays literal, precisely

Markdown interpretation applies to exactly two sites: `agent.output` records,
in both views since both reach them through `outputLines`, and the chat's
retained-away answer (§17), which renders `turn.ResultText` when retention has
taken the transcript away.

Everything else keeps its current literal rendering and its current gutter:
`agent.thinking`, `agent.tool_use`, `agent.tool_result`, `agent.run_header`,
`agent.plan`, `agent.command_output`, `agent.usage`, `agent.raw`,
`command.output`, `vincent.*`, and every `agent.error`.

**`agent.result` stays literal too**, including the case where `renderResult`
falls back to `ResultText` because the attempt rendered no output. That is a
`✓ `/`✗ `-marked line — an event, not prose — and the issue's own rule is that
Markdown must never make prose look like a tool event. The `result_text` the
issue names is the chat's retention fallback, which is prose with a blank
gutter.

### 6. Markdown structure lives inside the assistant content column

The two-column activity gutter is untouched: assistant prose still gets
`gutterNone` and sits flush left. A list marker, a blockquote bar or a code
block's rail is composed *after* the gutter, exactly as `toolResultLine`
composes `gutterResult + mark` into a four-column gutter. Nesting depth grows
that prefix; `wrapLine`'s hanging indent then falls out for free, which is what
the issue asks for on wrapped list items.

One mechanical extension was needed. `wrapLine` indents continuations with
*spaces* of the gutter's width, so a blockquote's `│` would appear on the first
line of a wrapped quote and vanish on the rest — a content gutter that is not a
gutter. `paneLine` gained an optional `contPrefix`, defaulting to the current
spaces; blockquotes and code blocks set it to their own rail. Every existing
caller is unchanged.

Every marker is a glyph rather than a colour, which is the rule §15 already
holds the activity gutters to: a monochrome terminal or an SSH session that
lost its profile must keep every distinction. Inline code keeps its backticks
for the same reason.

### 7. Control sequences are neutralized at one chokepoint

The issue scopes stripping to the Markdown path. It belongs wider, and it is
cheap there: agent text reached the pane unfiltered, so an agent that emits
`\x1b[2J` or an OSC title sequence could already clobber the TUI, in records
that have nothing to do with Markdown — a tool result summary, a command's
output body, an error message. Sanitizing inside `wrapLine`'s segment emission
covers every record type at one site and cannot be bypassed by a record type
added later.

Escape sequences and C0/C1 controls are removed *before* measurement, so they
can never influence the width arithmetic either. Only vincent-owned styles are
emitted. Raw HTML is not parsed and never fetched or executed; it renders as
literal text.

This fixes a pre-existing defect. It is folded in rather than split out because
adding rich layout on top of unsanitized input is what would turn a cosmetic
bug into a pane-corrupting one. §16 had no terminal-injection item before this.

## One behaviour change worth calling out

The chat's retention fallback rendered through `wrapCellLines(t.ResultText,
width-2, 40)` — a **40-line cap** with an ellipsis, and a two-column narrowing
the `outputLines` path does not apply. Routing it through the shared renderer
drops both. That is required by the first acceptance criterion (the same
Markdown must render identically in both views at the same width) and it is a
strict improvement: the retained-away answer is the only content that turn has
left, and capping it hid the tail.

## What the tests prove

`internal/tui` is covered by no gate script, so the tests are the whole
assurance. Construct fidelity per width; the two views agreeing at every level;
nothing but assistant prose being interpreted, each other record type keeping
its gutter; every produced line measuring within the pane over ASCII,
combining marks, emoji, ZWJ sequences and CJK at narrow widths; reflow being
from the source rather than from rendered ANSI; injection fixtures leaving no
control character and no widened line; code blocks keeping their whitespace and
hard-wrapping whole; monochrome keeping every structural distinction; and the
rendered pane not mutating the records it rendered.
