# 089 — An in-progress indicator while a turn or an attempt is running

**Status:** ✅ done (1/1)
**Issue:** #330
**Amends:** §15 (view 2, view 8, view 9)

While an agent produces a turn, vincent's clients could go completely silent.
The chat workspace was the sharpest case: a `running` turn with no records yet
rendered as *literally nothing* — the reader's own prompt bubble and then blank
space — and `chatView` had no `tea.Tick` at all, so with the stream quiet the
frame was genuinely frozen. That is indistinguishable from a wedged TUI.

The gap was never only the pre-first-chunk window, which is why the fix is not
the one the issue's own rejected alternative describes. At `quiet` the output
pane drops tool calls and results, so a turn busy running tools for minutes
renders nothing new the whole time; even at `normal`, a long tool stretch
between two pieces of prose is a silent gap. The same silence existed on the
chats board (a static `running` label, no tick), in the task workspace (a
running attempt's output pane, with no elapsed clock anywhere on the screen)
and in `vincent chat send`, which polled every 500 ms and printed nothing at
all until the turn ended.

## Tasks

- [x] **089.1** — One shared indicator across the chat workspace, the chats
  board, the task workspace and `vincent chat send`. ✓ 2026-09-05

## What was already true

Three facts made this smaller than the issue assumed, and all three are worth
recording because each removed a seam somebody would otherwise have built:

1. **All three views already had an injectable clock.** `chatView`, `chatsView`
   and `detail` each carry `now func() time.Time`, pinned by their tests. The
   elapsed half needed no new seam.
2. **The chats board already had the right number on screen.** `chatActivity`
   renders `formatElapsed(now - chat.UpdatedAt)`, and the store touches
   `chats.updated_at` only on a state change — never per output chunk — so for a
   `running` chat that cell already *was* time-since-the-turn-started. It simply
   never advanced, because nothing repainted. No DTO, endpoint or column
   changed.
3. **The wire already carried the rest.** `apiclient.ChatTurn` has `StartedAt`;
   `apiclient.StepRun` has `StartedAt` and a `Live()` predicate.

## Decisions

### 1. One tick at 120 ms; the clock is derived, not ticked

*2026-09-05.* A single `SpinnerTick = 120ms` advances the frame, and the
elapsed string is computed from the view's `now()` on each render, so it still
reads in whole seconds.

The issue proposed a 1 s tick, matching `board.go`'s `elapsedTick`. Rejected: a
glyph that moves once per second **is** the frozen screen this feature exists to
rule out. The cost is affordable precisely because the expensive work is already
gated — `chatView.bodyView` rebuilds only on `bodyDirty || builtWidth != width`,
and `detail.renderOutputPane` only on `outputDirty` — so an indicator-only tick
repaints the frame without re-rendering any Markdown.

### 2. One pure renderer, not `bubbles/spinner`

*2026-09-05.* `internal/tui/progress.go` exports a function of
`(frame int, since time.Duration) string`: no `tea.Model`, no tick of its own,
no message identity to route.

`bubbles`' spinner is a model with its own ticker, so three surfaces would mean
three models and three tick identities. A pure function is how `outputlines.go`,
`formatElapsed` and every other renderer in this package are built, and it is
table-testable without running a program. It is exported for one caller outside
the package — `vincent chat send` — rather than copied there; `cli` → `tui` is
the sanctioned direction and the alternative was a second frame table and a
second spelling of the duration.

### 3. Elapsed reuses `formatElapsed`, not the issue's `0:14`

*2026-09-05.* The TUI has one duration vocabulary — `14s`, `2m03s`, `1h04m` —
and a second spelling of the same fact, three views over, is worth more than
matching an illustrative `e.g.` in an issue body. A negative duration clamps to
zero rather than printing `-1s`: the client's clock and the daemon's are two
clocks.

### 4. Ticking stops by not re-arming, and every view clears its guard

*2026-09-05.* `tea.Tick` cannot be cancelled (recorded in
`docs/history/v0-tasks.md` and honoured by `internal/tui/daemon.go`'s log poll),
so a stray tick always outlives the state that armed it and must be a no-op.
Each of the three views clears its `ticking` guard on the tick and re-arms only
from one place — `update`'s trailing `armTick()` — so a send, a load, a stream
note and a cancel cannot each miss a site.

**`board.go` is deliberately not copied.** Its `b.ticking` is set once and never
cleared, which is correct *there*: the task board always has an elapsed column to
advance. A chat with no running turn has nothing moving, and issue #330's own
acceptance criterion demands it arm no tick. The divergence is intentional and is
commented in all four files, so a later reader does not "unify" it.

### 5. Placements: the chat footer, the board's state cell, the pane title

*2026-09-05.* Three placements, each chosen for the same property — visible at
every scroll position:

- **Chat workspace:** in `footerLines`, above the composer and beside the note.
  Not inline at the end of the running turn's body, which scrolls: a reader who
  has scrolled up is exactly the reader who needs to be told to keep waiting.
  This also puts it outside the `bodyDirty` gate, which is what makes it work at
  `quiet`.
- **Chats board:** *beside* the `running` label, not instead of it. The state
  cell is a fixed-width column rendered through the shared `chatStateLabel`
  vocabulary, and replacing the word with a glyph breaks that vocabulary for one
  state only. The issue left this open.
- **Task workspace:** in `detail.outputTitle`, beside the `output│diff` tab
  strip, the level and the follow state. That title is already this view's home
  for live state and the only place in the workspace visible at every scroll
  position. Output tab only, never `tabDiff`.

### 6. The detail gate is `StepRun.Live()`, not the step's type

*2026-09-05.* The issue says "a running agent step". A long `command:` step is
silent for exactly the same reason and for exactly as long, so what earns the
indicator is that the attempt is still producing output. A deliberate, small
widening of the issue's letter in service of its stated purpose.

### 7. Only `running` animates on the chats board

*2026-09-05.* The issue's list of non-animating states omitted
`awaiting_input`; it does not animate. By this workspace's own criterion an
`awaiting_input` chat is waiting on the human rather than working, and the
header already badges it. This matches the chat workspace, where the indicator
is gone at `done`, `failed`, `interrupted` **and** `awaiting_input`.

### 8. Braille everywhere, no ASCII fallback

*2026-09-05.* `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`, unconditionally on all three platforms. The
issue's "plain-ASCII fallback if the braille frames do not render" is dropped as
**undetectable**: a program can probe whether it is attached to a terminal, never
whether that terminal has a glyph. This TUI already ships `⏸`, `▸`, `▾` and
box-drawing on the same terms, and a terminal that cannot draw braille cannot
draw the frame around it either.

### 9. TTY detection promotes `github.com/charmbracelet/x/term`

*2026-09-05.* There was no TTY detection anywhere in this repository, so the
issue was right that this was worth deciding rather than falling into.
`golang.org/x/term` as a new direct module was rejected: `charmbracelet/x/term`
is **already** in `go.sum` via `charm.land/bubbletea/v2`, so promoting it to the
direct require block adds no module to the graph and no new supply-chain
surface, and it is the library the TUI already leans on transitively for exactly
this question. It owns the platform difference, so `internal/cli/tty.go` needs no
`_windows.go` twin.

The predicate is `*os.File` **and** `term.IsTerminal`. The type assertion is
load-bearing on its own: cobra's test writers are `*bytes.Buffer`, so every
command test is non-TTY *structurally* rather than by mocking, which is what
makes "a redirected send is byte-identical" a property of the code.

### 10. The CLI leg stays single-goroutine

*2026-09-05.* `runChatTurn`'s `select` over `ctx.Done()` and a 500 ms
`time.After` becomes a select over `ctx.Done()`, a 500 ms poll ticker and a
120 ms frame ticker, both `defer`-stopped. A writer goroutine drawing frames
while the main one prints the answer would race on stderr for no gain; three
channels in one `select` cost nothing and leave `-race` nothing to find. With
the indicator suppressed the frame channel is left nil, so a redirected send
arms no ticker at all.

The line is erased before **every** exit path — the answer, both error returns
and `ctx.Err()` — with `\r`, spaces and `\r` rather than an ANSI
erase-to-end-of-line: a Windows console without
`ENABLE_VIRTUAL_TERMINAL_PROCESSING` prints escape bytes literally, and garbage
on the line this exists to keep clean is worse than a few spaces.

## Not done here

- **No screenshots were regenerated, and none went stale.** `scripts/screenshots.sh`
  seeds no chat and has no chat tape — the gap task 071 recorded and task 085
  re-recorded — so no `docs/assets/tui-*.png` shows view 8 or view 9. The one
  task-workspace capture, `tui-diff.png`, is of the **Diff** tab, where this
  indicator deliberately does not draw. If a chat tape or an Output-tab tape is
  ever added, it will show one arbitrary spinner frame, which is why the elapsed
  clock beside it is what such a picture would actually prove.
- **No gate.** The indicator is client-side rendering over data already on the
  wire: no endpoint, DTO, column or block reason changed, so no gate script has
  anything new to assert.
- **No "show it only after N seconds of silence".** The issue lists it as worth
  revisiting if the always-on version reads as noisy in practice. It is not
  implemented: it needs a threshold to tune and a state machine to explain, and
  neither is worth carrying before anyone has found the always-on version noisy.
