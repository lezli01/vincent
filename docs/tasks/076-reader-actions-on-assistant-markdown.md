# 076 — Reader actions on assistant Markdown

**Status:** ✅ done (1/1)
**Issue:** #292
**Amends:** §15's task 073 amendment, §15's command-palette and popup
paragraphs, and §16's terminal-injection item
**Follow-up to:** [073](073-assistant-markdown-in-output.md), whose decision 4
parked this work by name

Rendered Markdown improves readability, but a terminal conversation also needs
a way to inspect and act on the original: an ANSI-styled pane is a poor
clipboard format, and a surprising render needs an immediate source escape
hatch. Task 073's §15 amendment said as much — "a raw/rendered toggle key and a
copy-raw action are both plausible and both belong in the follow-up issues
already scoped — a toggle in particular needs its own decision, because the
chat composer holds every letter key". This is that follow-up, and this is its
decision.

The work is entirely client-side and lands in `internal/tui` alone: no daemon
package, no API route, no store migration — the same shape task 073 argued for
and for the same reason. §13.3's chunks and the JSONL transcript already keep
the agent's exact bytes, and width, theme and clipboard are things only the
client knows.

## Decisions

### 1. Link actions are not in this task

Copy-link-destination, inspect-destination and open-link move to a follow-up.

The reason changed while this was in flight and the decision did not. It was
written against a renderer with no link construct at all, whose parsing
**#290** owned; #290 has since landed as [075](075-rich-markdown-blocks.md),
so a link *is* an entity now — a label carrying a dim `[n]`, resolved by a
reference block at the end of the message. What is still missing is the half
this task would have had to build either way: a way to say *which* `[n]` a
reader means. That is the same targeting problem decision 6 answers for
documents and fenced blocks with a captured-at-pick-time popup, and task 075
named it in §15 in those words — "the numbering is what a later reader action
would name". Adding a third row type to the picker for it is a small change on
top of what lands here, and it is a change with its own decisions: whether a
destination is offered per message or per link, and what an action does with a
scheme `openURL` refuses.

The infrastructure that follow-up needs already exists and is untouched:
`openurl.go` + `openurl_{darwin,unix,windows}.go` validate the scheme
(`http`/`https` only), pass the URL as a separate argv element and never
through a shell, bound the helper at 10 s, and fail visibly. That is already
the issue's "open allowed HTTP(S) links with a platform-native launcher" and
"unsupported URI schemes remain visible but cannot be launched", written for
task 052.6.

**Consequence for the issue's acceptance criteria:** criteria 4 and 5 (link
inspection and opening; unsupported schemes) and the link half of criteria 3
and 8 are not discharged here. They move to the follow-up.

What this task does owe #290 is that the payloads carry what the pane shows:
the plain-text payload keeps a link's `[n]` and ends with the same `[n] dest`
reference block, because a destination stripped of its punctuation and of its
reference would be a destination deleted (decision 5).

### 2. Raw is a session-level value, held the way the verbosity level is

A `rawHolder` created in `newViews` beside the existing `levelHolder`, pointed
at by both `newDetail` and `newChatView` — [071](071-chat-workspace-verbosity.md)
decision 3's shape exactly. Nothing is persisted: no `tui.json` entry, no
`tui:` config key, matching what §15 already reasons for the level. Toggling in
one workspace is visible in the other.

The flag rides into the renderer on `lineOpts`, which already exists as
`outputLines`' carrier for pane-specific display choices (071 decision 2). Two
call sites branch on it, through one `assistantLines` helper: the `agent.output`
record in `outputlines.go`, and §17's retention fallback in `chatrender.go`.
Nothing else changes, because nothing else was ever interpreted (073
decision 5).

Raw mode is presentation only. It does not touch `records`, `nextOffset`, the
verbosity level, the transcript, follow mode, or any task or chat state.

*Beat:* a per-document override. §15's reason for the level being session state
— moving around should not reset what a reader chose to see — applies unchanged,
and per-document state needs the identity model #291 owns.

### 3. Raw still goes through the pane's sanitizer

Raw renders the stored Markdown through the existing literal path —
`wrapLine`/`wrapPre`'s segment emission, where §16's ANSI/C0/C1 stripping
lives — one pane line per source line, preformatted so leading whitespace
survives.

"Exact stored Markdown" means the agent's bytes as Markdown *source*, not the
agent's bytes as terminal input. Raw mode is the escape hatch for a surprising
*render*; it is not a hole in the one chokepoint §16 was added to guarantee.

### 4. Clipboard payloads are sanitized too, and carry no pane wrapping

The issue asks for both "copy an assistant document as its original Markdown"
and "never place terminal ANSI sequences on the clipboard". Those conflict for
an agent that emitted `\x1b[2J` inside its prose, and §16 settles it: every
clipboard payload goes through the same `sanitizeText` chokepoint, in
`writeClipboardCmd` rather than at each call site. A clipboard is pasted into a
terminal, which is precisely the boundary §16 exists to hold. "Original
Markdown" therefore means the stored Markdown minus escape sequences and C0/C1
controls — a definition the whole feature is already committed to.

Payloads are also built from the **source**, never from rendered lines, so a
copy made at width 40 and one made at width 200 are byte-identical. The pane's
hard wraps are a property of the pane.

### 5. Three copy payloads, and one parse feeds three emitters

- **Markdown** — the record's stored text, sanitized.
- **Plain text** — the parsed block tree emitted unstyled and unwrapped:
  headings as their own text, list items with the pane's own `• `/`◦ `/`▪ `
  markers and their nesting indent, blockquotes prefixed, fenced code as its
  interior lines. This is "rendered structure without Markdown punctuation"
  read as *the structure the pane draws*, minus lipgloss and minus width.
  Task 075's two new blocks follow that reading rather than getting a rule of
  their own: a **table** emits as the stacked `column: value` records
  `renderMDTableStacked` draws, because that is the form that does not depend
  on a width, and a **link** keeps the `[n]` the pane gave it with the same
  `[n] dest` block closing the payload.
- **Fenced code block** — the block's interior lines verbatim, with no fence
  and no info string, whitespace and tabs preserved.

`mdBlock` was extended to carry its **source** (depth, marker, text) beside the
laid-out `paneLine`s, so that all three emitters and the picker's fenced-block
scan read one parse. A block that had *only* become styled segments could serve
the pane alone: recovering "the text without its punctuation" from a rendered
line means unpicking lipgloss, which is the wrong direction. Layout stays at
parse time rather than moving into the emitters because parse order is where
task 075 numbers a link's destination, and a message's numbering has to follow
document order — a table, whose cells are resolved to segments at parse time,
would otherwise be numbered before prose that precedes it. Inline spans are the
same one scanner — `inlineSegments` already emits emphasis with its marks gone
and a link as its label plus `[n]`, so the only thing left to undo is the code
span's backticks, which a new `segment.code` flag marks.

*Beat:* a second inline parser for the plain payload. Two answers to "is this
emphasis" is exactly the drift the single registry and the single renderer exist
to prevent.

### 6. Targets are picked from a popup, and captured at pick time

The output pane is a `viewport.Model` over rendered `[]string`. It has no
cursor, no selection and no block identity — `markdownLines` returns strings and
`apiclient.TranscriptRecord` carries no id.

So `ctrl+y` opens a popup listing what can be copied from the records currently
loaded, newest first: two rows per assistant document and one per fenced block,
with a search input over them. It follows `palette.go` and its `m.palette != nil`
handling in `root.go` — a short-lived popup that owns the keyboard, top of the
§15 esc stack — and is dormant until asked for.

Each row **captures its source text when the popup is built**, rather than
holding an index into `records`. That answers the issue's "targets remain stable
across resize, transcript reload and incremental rendering" outright, with no
identity model: there is no index left to drift when a chunk arrives, a resize
re-renders, or the chat's `maxRecords` cap prunes the front of the slice.

*Beat:* a semantic `(turn, document, block)` identity — which is what **#291**
is for. When #291 lands its document model the picker's rows become references
to it; nothing in the copy payloads changes.

*Beat:* a block cursor with `tab`/`shift+tab` in the pane — rejected by the
issue's own alternatives ("make every code block permanently focused … adds
heavy navigation state to ordinary reading").

### 7. The palette gets `ctrl+p`, because it does not exist in a chat today

The issue says to prefer the command palette in chat. There was no palette in
chat: `chatView.capturesInput()` is true whenever the composer is focused, and
`root.updateKey` hands a capturing view every key but `ctrl+c`, so `:` types a
colon into the draft and opens nothing (071 decision 4).

`ctrl+p` is therefore hoisted above the input-capture gate in `root.updateKey`,
alongside the `ctrl+v` paste that is already hoisted for the same reason, and
opens the palette in both workspaces. This is a real fix beyond this issue — the
palette is §15's "what can be done right now" surface, and the chat workspace
has been unable to reach it since task 067 — and it is what discharges the
issue's "every action is reachable through keyboard help/palette in both
contexts".

Two direct keys sit on top, registered in `ctxOutput` and `ctxChat`:
**`ctrl+o`** toggles rendered/raw and **`ctrl+y`** opens the copy picker. Both
are ctrl-modified in both contexts rather than a letter in `ctxOutput` and a
ctrl twin in `ctxChat` (the `v`/`ctrl+r` split task 071 chose) — one action
should have one name in the help overlay and the palette, and the existing split
is a cost, not a model. `ctrl+o` and `ctrl+y` were free in both contexts;
`ctrl+o` is bound only in the create-PR form, which is a popup neither key can
be pressed under.

### 8. Two clipboard transports, and only one of them can be believed

`github.com/atotto/clipboard` is already a direct dependency (`clipboard.go`
reads with it) and returns a real error, which is what the "visible
success/failure notice" criterion needs. It is also wrong over SSH — with
`xclip` installed on the remote box it succeeds against the *remote* display.
`tea.SetClipboard` is OSC 52: terminal-native, correct destination over SSH, no
helper binary, and fire-and-forget with no failure signal.

So: `WriteAll` first, OSC 52 on its error, and a notice that says which
happened — "copied" for the verified path, "sent to the terminal" for the
unverified one.

**Amendment to the brief.** The brief asked for a third notice, "failure of both
naming the error". OSC 52 cannot report failure — the brief says so itself — so
that state is not representable, and a notice claiming it would be a guess. The
fallback's notice **names the system clipboard's error inline** instead
("… sent to the terminal — the system clipboard refused (exec: "xclip": not
found)"), which carries the same information at the moment it is known. The
third outcome that *is* real — a payload that sanitizes away to nothing — is an
error notice rather than a silent no-op, following `openurl.go`'s posture that a
key press a human made must never fail silently.

The write is indirected through a package variable the way `openURL` is, so the
seam is asserted without a system clipboard.

## Tasks

- [x] **076.1** Rendered/raw toggle, copy picker, `ctrl+p` palette hoist ✓ 2026-09-01

**Done when:** `ctrl+o` toggles raw in both workspaces off one shared session
value; `ctrl+y` picks and copies Markdown, plain text and fenced blocks with a
visible notice; `ctrl+p` opens the palette while the chat composer holds focus
and the composer still receives every printable key; §15, §16 and
`docs/guides/tui.md` say so.

## What the tests prove

`internal/tui` is covered by no gate script, so the hermetic tests are the whole
assurance — task 073's position, unchanged. `internal/tui/readerpicker_test.go`
holds them, plus four registry probes in `bindings_test.go`:

- The same assistant document renders identically raw in the task pane and the
  chat at several widths, keeps its punctuation, and wraps within the pane at
  narrow widths.
- Toggling raw mutates no `apiclient.TranscriptRecord`, does not move
  `nextOffset`, does not disturb the verbosity level and does not drop follow.
- Raw is one shared session value: toggling in one view is visible in the other,
  and it survives opening a fresh view.
- Injection fixtures produce no ESC and no C0/C1 in raw mode, and no line wider
  than the pane.
- Markdown copy is the record's stored text; plain-text copy carries no `#`, `*`
  or backtick and no ANSI; code-block copy has neither fence nor info string and
  keeps interior whitespace and tabs; every payload is sanitized.
- Every payload is identical at width 40 and width 200.
- The clipboard seam: the verified notice, `WriteAll` failing over to OSC 52
  with a notice naming its error, and an empty payload reported rather than
  dropped. Both workspaces turn a result into their own notice.
- The picker lists every assistant document and fenced block newest first; two
  documents with identical opening text stay distinguishable by ordinal; a
  document with no fence contributes no code row; a reasoning record contributes
  nothing; and a pick copies what was on screen when the popup opened even after
  further records arrive and the pane is resized.
- `ctrl+p` opens the palette while the chat composer holds focus, and the
  composer still receives every letter, both arrows and a `:` typed into a
  draft; the whole `ctrl+y` → popup → pick → clipboard → notice path runs
  through the root model; the footer hints name the ctrl keys, including the
  chat's hand-written footer line.

The existing `TestPaletteReachesEveryRegistryEntry` covers the new rows'
palette reachability in both contexts without a new test.

## Left open

- **All link actions**, to the follow-up. #290 landed as task 075 while this
  was in flight, so the blocker is no longer the renderer: it is naming a
  reference, which decision 1 leaves to that task. Issue criteria 4 and 5, and
  the link parts of 3 and 8, are theirs.
- **Semantic identities.** Captured text is the stability mechanism until #291
  has a document model to reference.
- **`?` in a chat.** Still swallowed by the composer. The palette answers the
  issue's reachability criterion; moving help is a separate call.
- **No new screenshot.** Raw is a display state of an existing panel and the
  picker is a panel no capture shows; the chat views have never been captured at
  all (task 071, "Not done here").
