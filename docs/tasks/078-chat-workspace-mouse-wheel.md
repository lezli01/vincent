# 078 — Scroll the chat workspace conversation with the mouse wheel

**Status:** ✅ done (1/1)
**Issue:** #300
**Amends:** §15 view 9

The chat workspace was the one scrollable pane in the TUI the wheel did not
reach: `chatView.update` handled `tea.WindowSizeMsg` and `tea.KeyPressMsg` and
no mouse message, so a conversation could only be moved with `pgup`/`pgdown`.
It is the view where that is missed most — the composer holds the keyboard
almost always, which is why the scroll keys had to be `ctrl`-modified in the
first place.

The body now takes `tea.MouseWheelMsg`: one line per tick, `syncFollowToViewport()`
so a scroll back pauses follow and `ctrl+g` re-arms it, and
`fetchVisibleTranscripts()` so wheeling back into turns whose transcripts were
never fetched fills them in rather than scrolling into blank space (task 071
decision 6). That is `taskTabOutput`'s arm of `taskView.updateWheel`, verbatim,
which is the point: §15 view 9 makes this body that pane.

## Decisions

### 1. The wheel scrolls the conversation from anywhere in the chat view

The issue as filed required the wheel to act **only** while the pointer is over
the conversation body, with ticks over the header, footer or composer ignored,
and asked for a `chatView` field recording the body's Y range the way
`taskView.bodyY` does. That is not built, and the issue's own first alternative
— scroll from anywhere — is what shipped.

It contradicts a binding recorded decision. §15's Mouse section says "wheel to
scroll the focused panel"; PR S (`../history/v0-tasks.md`) records it as *"The
wheel scrolls the focused panel, not the hovered one — §15 verbatim. Hover
tracking is drag-adjacent machinery for a behavior §15 does not ask for"*; and
`TestShellWheelScrollsFocusedPanel` asserts it by name, moving the pointer over
an unfocused table to prove the tick does not follow it. No view gates the wheel
on pointer position today — `taskView.updateWheel` dispatches on the active tab,
`shell.updateWheel` on `s.focus` — so the gate would have made the chat
workspace the only position-gated surface in the TUI, the inverse of the issue's
own stated reason for rejecting this alternative.

The chat workspace has exactly one scrollable pane, so "the focused panel" is
that pane and the rule needs no exception to cover it. The cost is accepted
knowingly: a tick with the pointer in a draft longer than the composer's three
rows scrolls the conversation rather than the draft. §15's Mouse section is
therefore **unchanged** — this change makes the chat obey the rule already
written there rather than amending it.

Two alternatives are recorded so neither is re-proposed silently:

- **The hover gate**, above.
- **Hover-*routing* on the `detailsPane.scrollAt` precedent** (task 049 — over
  the sidebar it walks sections, over the content it scrolls the body).
  `scrollAt` is position-aware *routing* and is never a no-op; a positional
  no-op has no precedent here, and routing the wheel into the composer's own
  draft is behaviour nobody asked for.

### 2. The pre-existing footer clip is fixed in the same work

`chatView.render` computes `room := max(height-len(head)-len(foot), 1)`, but
`footerLines` appended `v.composer.View()` as a **single** slice element and the
composer is `SetHeight(3)`; bubbles' `viewport.View` pads its output to its
height, so that one element rendered three rows. The footer occupied five
rendered rows while `len(foot)` reported three, `render` returned `height+2`
lines, and `frame` keeps only the first `h-2` — so the in-view hint line
(`enter send · ctrl+x stop the turn · … pgup/pgdown scroll · ctrl+g live`) was
never drawn and the composer showed two of its three rows. The same bug fed a
string containing newlines to `render`'s per-line `ansi.Truncate` pass.

`footerLines` now returns one element per rendered line. `len(foot)` is honest,
`room` becomes `height-7`, `render` returns exactly `height` lines, and the
truncation pass sees single lines. The body loses two rows to the footer that
was always going to occupy them; that is the honest accounting, not a
regression.

It is in scope because the issue's fourth acceptance criterion names the footer
as a region the wheel must respect, and it was not on screen at all.

### 3. `taskView.updateWheel` gains the popup gate it was missing

`taskView.updateClick` returns early on `t.popup`; `updateWheel` did not, so
wheeling behind an open task popup scrolled the pane underneath it. PR S records
*"the palette and the answer popup ignore clicks"*, and the issue asks the chat
to honour the same rule for the wheel. Both have it now:
`chatView.updateWheel` returns `nil` while `v.form != nil`,
`taskView.updateWheel` while `t.popup`. "A popup owns the surface" is one rule
rather than two.

### 4. The tests live beside the chat's own scroll tests

The issue nominated `mouse_test.go` and `diffshell_test.go`. Both are built on
`newShellFixture`, which constructs a `shell` and cannot reach a `chatView`. The
chat's existing scroll coverage is `chatturns_test.go` with the `finishedChat`
fixture, which already drives `v.render(60, 14)` and a paging key, so the wheel
tests go in a new `chatmouse_test.go` beside it. The task workspace's popup gate
is tested there too, next to the chat gate it is the twin of.

## Shape

`internal/tui` alone. No daemon package, no API route, no store migration, no
wire change, and no key binding: the wheel is not a binding, so
`internal/tui/bindings.go` is untouched and the `ctxChat` rows for `pgup` and
`ctrl+g` are correct as they stand.

`internal/tui/root.go` is untouched as well — PR S already gives takeover
screens wheel delegation for free, and with decision 1 the chat never reads
`msg.Y`, so the header decrement there is irrelevant here and no geometry has to
agree with it. The chats board (§15 view 8) stays keyboard-only, as the issue
scopes it; it is its own issue if it is wanted.

## Tasks

- [x] **078.1** The wheel on the chat conversation, the footer clip and the task
  workspace's popup gate ✓ 2026-09-01

**Done when:** a wheel tick over an open chat moves the conversation one line;
a wheel-up pauses follow and `ctrl+g` re-arms it at the end; wheeling back past
the five turns fetched on open fetches the older ones; a tick behind either
workspace's popup moves nothing; `chatView.render(w, h)` returns exactly `h`
lines with the footer hint on screen; and §15 and `docs/guides/tui.md` say so.

## Tests

`internal/tui` is covered by no gate script, so the hermetic tests are the whole
assurance — task 073's position, unchanged.

- A tick moves `v.vp.YOffset()` by exactly one line, each way.
- A wheel-up on a following conversation clears `v.following`, and `ctrl+g`
  restores it with `v.vp.AtBottom()` true — the same pair `pgup` is held to.
- Wheeling to the top fetches turn 1, mirroring
  `TestChatFetchesOlderTurnsWhenScrolledTo` with ticks in place of pages, so the
  two scroll routes are proven to fetch identically.
- A tick behind the §7.4 popup leaves the offset unchanged in both workspaces,
  and closing the popup hands the wheel back — so neither gate is a test that
  nothing scrolls.
- `render(80, h)` returns exactly `h` lines at four heights, and the footer hint
  survives `frame` at the size `root.framedView` uses. Both fail against the
  pre-fix `footerLines`.

Position is deliberately **not** asserted: a test that fed a tick at composer
coordinates and expected no scroll would encode the design decision 1 rejects.

## Left open

- **The chats board's wheel** (§15 view 8). Out of scope by the issue's own
  wording.
- **No new screenshot.** The wheel is not visible in a still, and the footer fix
  restores a line the captures under `docs/assets/` were taken without — worth a
  re-run of `scripts/screenshots.sh` next time it is walked, not a reason to
  block this.
