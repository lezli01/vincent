# 114 — Help while a text field has the keyboard, and the wheel on the list boards

**Status:** ✅ done (4/4)
**Issue:** [#405](https://github.com/lezli01/vincent/issues/405)
**Spec:** amends §15 Discovery, Keys, view 8 and view 10

## Problem

Two gaps left open by earlier chat work.

**Help could not be opened from a chat.** `chatView.capturesInput()` is
`composer.Focused()`, the composer is focused when the chat opens and never
blurred, so `root.updateKey` handed every key but `ctrl+c` to the view and `?`
was typed into the draft. [076](076-reader-actions-on-assistant-markdown.md)
decision 7 hoisted `ctrl+p` above that gate and left help out on purpose
("moving help is a separate call"). The other routes were broken the same way:

- Running "toggle this help" from the palette replayed `?` through
  `updateKey(synthKey("?"))`, back through the capture gate, into the draft.
  The palette's quit, mouse toggle, `!` and new-task rows did the same.
- The footer's pinned `: commands  ? help  q quit` was drawn in the chat too.
  Pressing those keys typed them; clicking one replayed it into the draft.

**The help overlay did not own the keyboard.** `helpFooter` and
`root.footerLine`'s comment both said it did, but `updateKey` handled only `?`
and `esc` for it, after the capture gate. On the board, `enter`, the arrows and
`q` still acted behind the sheet.

**`synthKey` could not replay a function key.** Its default arm returned
`rune(key[0])`, so `f1` would come back as `f`.

**Only the home board took the wheel.** `chatsView` (the live and archived
chats boards, §15 views 8 and 10) handled no `tea.MouseWheelMsg`, and neither
did `board.update`, which backs the archived tasks board. The home board's wheel
lives in `shell.updateBoardOnly`.

Nothing here contradicts a recorded decision. 076 decision 7 is the precedent
this follows. [078](078-chat-workspace-mouse-wheel.md) decisions 1 and 3 (the
wheel scrolls the focused panel; a popup owns the surface, wheel included) are
applied as written. Task 093's "no aliasing" was about keeping retired keys
beside their replacements; a key for text-field surfaces is the shape `ctrl+p`
already has, and `?` does not move.

## Decisions

### 1. `f1` opens help wherever a text field has the keyboard

It is checked in `root.updateKey` beside `ctrl+p`, above the input-capture gate,
for `ctrl+p`'s reason. So it works on every text-field surface — the chat
composer, the board and chats filters, the new-task, new-chat and answer forms,
the workflow editor — not only in the chat. It also toggles help where `?`
works. `?` is unchanged everywhere it worked before.

`f1` reads the same on Windows, macOS and Linux. Its costs are recorded rather
than worked around: MacBook keyboards need `fn`, and VS Code's integrated
terminal takes F1 unless its settings hand it over.

Rejected:

- **`ctrl+/`.** Legacy terminals send it as `ctrl+_`, and it depends on the
  keyboard layout.
- **`?` on an empty composer.** The key's meaning would depend on the draft, a
  message could not start with `?`, and filters and forms would still have no
  help.

### 2. The help overlay owns the keyboard while it is open, on every surface

The palette's rule: every key but `ctrl+c` goes to the overlay. `?`, `esc` and
`f1` close it, and everything else is swallowed. The check runs before the
`ctrl+p`/`f1` checks and before the capture gate, which makes `helpFooter`'s
text and `footerLine`'s comment true.

Accepted behaviour change on the board: keys pressed under the sheet no longer
act on the board behind it. Mouse and paste were already ignored while
`m.help` is set, so they are unchanged.

### 3. Global rows from the palette or a footer click take effect in a text field

A replayed `scopeGlobal` row — help, quit, the mouse toggle, `!`, and the `n`
nav row — goes to the root's own global handling and is not typed into the
field. Panel rows and task actions still replay through the normal route; the
chat's own rows are ctrl keys the view already handles.

The mark travels as a `global` field on `paletteEntry` and `footerHit`, and
`root.replayKey` routes on it. The root's single-key globals moved out of
`updateKey` into `root.globalKey`, which reports whether it consumed the key,
so a keypress and a replay run the same code and no view is special-cased. A
global key the root does not consume in its current state (`!` while
disconnected, `n` on the new-task form) is dropped rather than delegated when a
text field has the keyboard, for the same reason.

### 4. The footer's pinned part names the keys that work on the current surface

While `activeCapturesInput()` is true it reads `ctrl+p commands  f1 help  ctrl+c
quit`; otherwise it keeps `: commands  ? help  q quit`. Each span is still
clickable and fires its key, which is why `synthKey` gains the function-key
case. The pinned part is still never truncated, and task 094's width budget
still measures it first; its growth comes out of the hints' budget.

### 5. The wheel moves the cursor on all three list boards

Live chats, archived chats and archived tasks: one selectable row per tick, as
on the home board. The chats boards use `chatsView.moveCursor(±1)`, which skips
project headings and remembers the selection; the archived tasks board uses
`board.wheelMove`. The wheel is ignored while something owns the keyboard (078
decision 3):

- chats boards: the new-chat form, the archive confirmation, the delete
  confirmation;
- archived tasks board: the delete confirmation.

An open filter does not stop it, matching the home board. The wheel never turns
an archived page: paging is a fetch and stays on its keys. Clicking a row to
select it on the chats boards is out of scope.

The home shell answers the wheel itself and never forwards it to
`board.update`, so the home board's behaviour is unchanged.

## Work

- [x] **114.1 — Help keys**: `helpAltKey = "f1"` and its `noPalette` global row
  in `bindings.go`; the modal overlay (`updateHelpKey`) and the `f1` hoist in
  `root.updateKey`; `synthKey`'s `f<n>` case. Tests in `helpalt_test.go`:
  `TestF1OpensHelpFromAChat`, `TestHelpOverChatOwnsTheKeyboard`,
  `TestF1TogglesHelpOnTheBoard`, `TestHelpOverBoardSwallowsKeys`,
  `TestF1WorksInABoardFilter`, `TestSynthKeyRoundTripsEveryRegistryKey`,
  `TestHelpListsF1UnderGlobalKeys`. ✓ 2026-09-17
- [x] **114.2 — Global replay and the footer**: `root.globalKey` and
  `root.replayKey`; `global` on `paletteEntry`, `footerSeg` and `footerHit`;
  `footerPinnedSegs` picking the pinned part by capture state. Tests:
  `TestPaletteGlobalRowsActFromAChat`, `TestFooterPinnedNamesTheKeysThatWork`,
  `TestFooterClickReplaysGlobalKeysPastTheField`,
  `TestFooterTextFieldPinnedNeverTruncates`. ✓ 2026-09-17
- [x] **114.3 — The wheel on the list boards**: `chatsView.updateWheel` and a
  wheel case in `board.update`. Tests in `listwheel_test.go`: the live and
  archived chats boards (a heading skipped each way, held under each layer and
  moving again once it closes, not held by a filter), the archived tasks board
  under its delete confirmation, and one tick through `root.Update`.
  ✓ 2026-09-17
- [x] **114.4 — Documentation**: §15 Discovery, Keys, view 8 and view 10
  amended, dated; `docs/guides/tui.md` (the palette, every key, the footer's
  pinned part, chats, chat workspace, archived); `CHANGELOG.md`; closed-by
  notes on the #405 bullets in 076 and 078. The screenshots taken while a text
  field has the keyboard (likely `tui-new-task` and `tui-workflow-editor`,
  whose pinned footer changes) were **not** re-captured:
  `scripts/screenshots.sh seed` stopped at "task 4 never reached
  awaiting_input" on 2026-09-17, in the daemon-driven seed before any capture,
  so the committed images are unchanged and wait for the next run.
  `internal/tui` is covered by no gate script, so the tests are the whole
  assurance, as they were for 073, 076 and 078. ✓ 2026-09-17
