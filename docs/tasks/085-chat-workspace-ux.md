# 085 — A quieter level, a distinct user turn, and a bounded composer

**Status:** ✅ done (1/1)
**Issue:** #321
**Amends:** §15 (the `v` paragraph, the task 066 result-line amendment, view 9)

Task 071 put the chat workspace's body through the output pane's own renderer,
and that was the right call for fidelity: an `agent.tool_use` reads the same in
a chat as in a task, and there is one verbosity vocabulary rather than two. It
left three things wrong for *reading a conversation*, and this task fixes all
three without reopening that decision.

**There was no level quiet enough.** `compact` already means "what the agent
said and did, nothing else" — and tool calls are what it *did*, so they stay.
In a chat that put a two-sentence answer behind a column of file reads, greps
and a count of lines nobody asked about. `quiet` goes in **below** compact
rather than redefining it.

**The user's own words looked like the agent's.** A prompt was
`wrapCellLines("› "+t.Prompt, width-2, 6)`: default foreground, flush left at
the assistant's own margin, with the `›` on the first line only and gone on
every wrapped line after it. It now renders as a right-aligned bubble.

**The composer had no boundary.** The last line of an answer and the first line
of a draft were two adjacent rows of the same colour. It now sits in a titled
box drawn with `shell.go`'s `frame`.

## Decisions

### 1. A fourth level, not a redefinition of `compact`, and not a chat-only one

*2026-09-03.* `outputLevel` gains `levelQuiet` as the new zero value and the
other three shift up. Three alternatives were rejected in the issue and are
recorded here because each would have superseded a task 071 decision:

- **Redefining `compact`** contradicts §15's stated meaning of that level and
  silently changes the task output pane for every existing user.
- **A chat-only level** would supersede task 071 decision 3 and §15's amendment
  that the level is one session value visible in both panes: walking from a chat
  to a task workspace would reset what a reader chose to see.
- **A chat-only *rendering* of a shared value** — the value cycles four ways but
  the task pane draws `quiet` as `compact` — is two panes rendering one named
  level differently, which is the second vocabulary task 071 decision 2 removed.

`newLevelHolder()` still starts at `levelNormal`, so no existing first screen
moves; the level is still persisted nowhere (no `tui.json` entry, no `tui:`
key), which is precisely why renumbering the constants costs nothing. `next()`
is a four-cycle wrapping verbose → quiet, so one press is still "one louder,
wrap to the quietest" and the gesture did not change.

The renumbering made every `level == levelCompact` guard a decision rather than
a constant: a guard meaning "compact only" (`resultOutcome`'s short form) and one
meaning "compact and below" (`agent.run_header`, `agent.plan`, `thinkingBlock`)
are now different expressions, and the second became `level <= levelCompact`.
Getting one wrong is a silent regression at an existing level, which is why the
tests compare whole rendered output at `compact`, `normal` and `verbose` rather
than spot-checking a line.

### 2. `quiet` suppresses the result line's outcome branch and only that branch

*2026-09-03.* `renderResult` has three branches. The issue's prose ("a failure
is never hidden by a display level") and its acceptance criterion ("no
`agent.result` line") disagreed about two of them; settled in favour of the
prose, and §15 amended to say where `quiet` sits.

- `rec.IsError` (`✗ …`) still renders. In a step run there is no other line
  carrying the failure — the chat has its own fail-reason line, a step run does
  not — so a display level that hid it would erase a failure outright.
- `!sawOutput` (`✓ <result text>`) still renders. It is the fallback for a turn
  that produced no assistant prose at all, which is what a codex turn with no
  `agent_message` looks like; suppressing it would draw such a turn as a bare
  `── turn N ──` with nothing under it.
- the `✓ done · …` outcome is what `quiet` drops.

§15 records the first two as meanings of the record rather than as level rules,
which is the reason they survive a level that is otherwise about hiding. And
`sawOutput` is set only by the assistant-document path — no `renderRecord` arm
sets `isOutput` — so hiding the tool lines cannot perturb which branch is taken:
the fallback fires at `quiet` exactly when it fires everywhere else.
Mechanically `renderResult` grew a `bool` return and `renderRecord`'s
`case "agent.result"` stopped returning an unconditional `true`.

### 3. An unrecognized line leaves no trace at `quiet`

*2026-09-03.* This is the one written rule the task **changes** rather than adds
below, so it is amended in §15 in place rather than quietly outgrown.
`levelCompact`'s doc comment said unrecognized lines "stay behind their count at
every level below verbose". That is now true of `compact` and `normal`.
Everywhere else the count is an offer — *there are lines here, `v` shows them* —
and `quiet` is the level that makes no offers, so `flushRaw` resets its run and
appends nothing rather than drawing a row of arithmetic about itself.

### 4. A foreground accent and a per-line `›`, not a background band

*2026-09-03.* The issue asked for a "tinted band … in an accent colour". Drawn
instead as an existing 16-colour **foreground**, bold, with `›` on every line of
the bubble.

Every style in `internal/tui` is a 16-colour foreground; the package's only
background is `styleSelected`'s 256-colour grey 237, which means "this row is
selected" and collapses to nothing on a 16-colour terminal. §15's Colour section
requires the palette to degrade under `NO_COLOR` and at 16 colours, and the
issue's own requirement is that the distinction survive monochrome. Right
alignment plus a per-line marker carry it with the colour stripped entirely; a
band would either reuse the selection colour for something unselected or invent a
palette role §15 does not have. `stylePrompt` is composed from `styleFocus`'s
colour 6 rather than given a colour of its own: the accent already in the
palette is the accent.

### 5. The prompt wraps in full and keeps its own line breaks

*2026-09-03.* The old call collapsed `\n`/`\r`/`\t` into spaces and capped the
prompt at six lines with an ellipsis. Both are gone.

The composer is multi-line by design — task 071 decision 4 keeps `↑`/`↓` for
editing a draft — so a pasted snippet or a numbered list is prompt content, not
incidental whitespace. And a truncated prompt puts the tail of what a *human*
asked out of reach on a body that already scrolls, which is the same call task
073 decision 5 made when it dropped the 40-line cap from the §17 retention
fallback, and the same posture §15's #299 amendment states for fields being
typed into. Tabs are expanded to four spaces so every remaining cell is one the
width arithmetic can count.

The bubble pads by its *actual* height, so prompt lines still carry the zero
anchor and `v.turnAt[t.Seq]` still points at the turn separator: `lineAnchor` /
`anchorIndex` restore behaviour is unchanged.

### 6. The border is spent out of the height budget

*2026-09-03.* `footerLines` reuses `frame(title, content, w, h, focused)` from
`shell.go` — the package has exactly one box-drawing routine and a second would
be a second answer to the same question. Titled `message`, `focused=false`: the
chat composer holds the keyboard nearly always, so a permanently lit focus glyph
would say nothing.

`footerLines` returns **one element per rendered line**, so `frame`'s output is
split on `\n` the way the composer's own `View()` already is. This is issue
#299's regression and the standing comment in `footerLines` is its warning: a
joined multi-line string makes `render`'s `room := height - len(head) -
len(foot)` arithmetic wrong *and* feeds the per-line `ansi.Truncate` pass a
string it measures as the sum of its rows — `ansi.StringWidth` treats `\n` as
zero-width and never resets — which collapses the box to its top edge. The
comment was extended to cover the frame rather than restated somewhere else, and
`TestPaneWidthChatComposerKeepsEveryWrappedRow` grew widths and heights rather
than gaining a sibling.

The two rows the frame costs come out of the body, which §15's #299 amendment
already sanctions: "a composer that grew is a body that shrank, not a frame that
overflowed." `chatComposerWidth` is one function because two callers size the
composer — `footerLines` on every render and `chatview.go`'s `WindowSizeMsg`
handler — and two copies of that arithmetic drift.

## Not done here

- **No chat screenshot.** `scripts/screenshots.sh` seeds no chat and has no chat
  tape, the gap task 071 recorded under its own "Not done here", so no
  `docs/assets/tui-*.png` shows this view and none went stale. No existing
  capture shows a non-default level either: the pane title names a level only
  when it is not the default, and every tape runs at `normal`.
- **No gate.** The level is client-only session state that never crosses the
  wire, and the chrome is client-side layout, so `scripts/m14-gate.sh` is
  unaffected and no new script is warranted.
