# 112 — Open and copy links from assistant Markdown

**Status:** ✅ done (3/3)
**Issue:** [#403](https://github.com/lezli01/vincent/issues/403)
**Spec:** amends §15 (task 075 and task 076 amendments, the popup list), §16
**Follow-up to:** [073](073-assistant-markdown-in-output.md) decision 4,
[075](075-rich-markdown-blocks.md) decision 2,
[076](076-reader-actions-on-assistant-markdown.md) decision 1,
[077](077-stable-assistant-markdown-while-streaming.md) "Left open"

## Problem

Task 075 gave assistant prose links: a label carrying a dim `[n]`, and a
`[n] dest` reference block closing the document, "the numbering is what a
later reader action would name". Task 076 decision 1 moved every link action —
copy, inspect, open — to a follow-up, and task 077 supplied document identities
a reference can be named against but bound no key. The renderer, the numbering,
the document model, the clipboard path (076 decision 8) and the safe opener
(`openURLCmd`, task 052.6) all existed. What was missing was a way to say which
`[n]` a reader means, and the key that raises it. Issue #292's criteria 4 and 5,
and the link halves of 3 and 8, are what this discharges.

Nothing here contradicts a recorded decision. It keeps 071 decision 4 (the
composer owns every letter, so ctrl chords only), 076 decision 7 (one ctrl key
per action, the same key in both contexts), 075 decision 3 (no OSC 8, and the
renderer never reaches the opener), 093's key vocabulary, and §16's scheme
refusal in `openURLCmd`. It is entirely client-side, in `internal/tui` alone:
no daemon package, no API route, no migration, no wire change.

## Decisions

1. **A link picker of its own, on `ctrl+l`.** Not extra rows in the `ctrl+y`
   copy picker: that popup is titled "copy" and its `enter` copies, a link needs
   two actions, and the search line takes every letter, so the second action
   must be a non-letter key. Inside the picker `enter` opens, `ctrl+y` copies
   (the same operation as the outer `ctrl+y`), `esc` closes. `ctrl+l` is free
   in every registry context and in bubbles' `textarea` and `textinput`
   keymaps, so it reaches the chat past the composer the way `ctrl+o`/`ctrl+y`
   do, with no root hoist. The registry rows carry **no vocabulary term**, for
   `copyPickKey`'s reason: the key raises a picker. Tagging it `termBrowser`
   would make `o` and `ctrl+l` one operation and fail 093's one-key-per-term
   test.
2. **Scope: every loaded document, newest first.** The pane has no cursor, so
   "the selected assistant block" has no referent. The picker uses the copy
   picker's scope and `MESSAGE n` ordinals, and a document with no links keeps
   its ordinal without showing a group, so one document has one name in both
   popups. Rows are references `(document seq, n)`, resolved at pick time with
   the destination captured at build time as the fallback; when the document is
   gone or its `[n]` names a different destination, the captured one is used —
   `copyPayload`'s rule that a row delivers what it said it would.
3. **One row per `[n]`; refused destinations are listed and copy-only.** The
   same `mdRefs` numbering, so identical destinations share a row and image
   sources are included. Each row shows `[n]`, the label of the destination's
   first occurrence (alt text for an image; the destination when there is no
   label) and the destination. A destination `openURLCmd` would refuse is
   marked `copy only`; `enter` on it opens nothing and produces a visible
   error naming the reason. Refusal is decided by `openableURL`, factored out
   of `openURLCmd` and called by both — never a second copy of the rule.
4. **Inspection is the full destination of the cursor row**, stripped and
   hard-wrapped like a reference line, between a rule and a one-line key hint.
   `enter` is the explicit action; no confirmation is added on top of it.
5. **Outcomes are notices in the workspace that asked, never the PR note.**
   Copy goes through `writeClipboardCmd`, labelled `link [n]`. Opening does not
   reuse `openedURLMsg` — every view sees it and `taskview.go` writes it into
   the pull-request note, and the chat does not handle it at all — so
   `openLinkCmd` rewraps the result as `linkOpenedMsg`, which the root delivers
   to the active view only: `detail.go` sets the pane's status, `chatview.go`
   the note. A success says `opened <url>`.
6. **One parse feeds every emitter.** `mdRefs` keeps the first label and image
   flag per destination, and `markdownLinks` reads the registry after
   `parseMarkdown(sanitizeText(text), refs)` — the parse the reference block is
   drawn from, with no second inline scanner. Raw mode does not change the list.

## Tasks

- [x] **112.1** `markdownLinks` over `mdRefs`, and `openableURL` split out of `openURLCmd` ✓ 2026-09-17
- [x] **112.2** The link picker, `ctrl+l` in both workspaces, the root popup slot and `linkOpenedMsg` ✓ 2026-09-17
- [x] **112.3** §15/§16 amendments, the TUI guide, features, 076/077 "Left open" ✓ 2026-09-17

## What the tests prove

`internal/tui` has no gate script, so the hermetic tests are the whole
assurance (073's position). `internal/tui/linkpicker_test.go`:

- `markdownLinks` numbers exactly as the pane's reference block — asserted
  against `markdownBlockLines`' own reference lines — across headings,
  paragraphs, list items, quotes and table cells, with a repeated destination
  sharing a number, images included, the first label winning, and a
  destination named in two records of one joined document getting one row.
- Rows are newest first, grouped by the copy picker's ordinals, one per `[n]`;
  a linkless document shows no group, and reasoning and tool records contribute
  nothing. Search matches label, destination and group.
- `mailto:`, `file:`, `javascript:`, a relative path and a malformed URL are
  listed and marked `copy only`, and the mark agrees with `openableURL`.
- Through the root, in the chat with the composer focused and in the task
  workspace's Output tab: `ctrl+l` opens the popup, a letter searches, `esc`
  closes; `enter` on an http(s) row calls the `openURL` seam with exactly the
  destination and the notice lands in the chat's note or the pane's status —
  success and opener failure — and never in the pull-request note; `enter` on a
  refused row never calls the seam and names the scheme; `ctrl+y` copies the
  destination with a `link [n] copied` notice.
- Control bytes inside a destination reach neither the clipboard, the popup nor
  the opener's argument.
- A pick after more records arrive, after a resize and raw toggle, and after a
  `maxRecords` front-prune delivers the destination the row showed, falling
  back to the captured one when the document is gone or `[n]` changed.
- Rendered and raw offer the same rows; the full destination wraps inside the
  popup at 24, 40 and 64 columns with no line wider than the frame.
- The palette's `synthKey` replay opens the picker in both contexts.

`bindings_test.go` gains `ctrl+l` probes for `ctxOutput` and `ctxChat`, and
`readerpicker_test.go`'s hint and footer tests name the third key.

## Left open

- **OSC 8 hyperlinks** stay [#404](https://github.com/lezli01/vincent/issues/404)'s.
- **Reference links, autolinks and bare URLs** stay literal (075 decision 4), so
  they have no `[n]` and no row. Widening the subset is its own issue.
- **A per-link cursor in the pane** stays rejected (076 decision 6, #292's own
  alternatives).
- **No screenshot.** The picker is a popup no capture shows, the position 076
  took.
