# 059 — A task-details tab inside the answer, repair and follow-up popups

**Status:** ✅ done (5/5)
**Spec:** amends §15 (the three form popups, and the `esc` stack)
**Issue:** #251

## Problem

When an agent raises a §7.4 input request the task parks in `awaiting_input`
and the TUI offers the answer popup. Deciding what to answer usually needs the
task's own context — the original prompt, which workflow and step is asking,
the agent and model, the linked GitHub issue — and while the popup is open none
of it is reachable. The popup owns the keyboard (`taskView.capturesInput`
returns true while `popup` is set, and `updateKey` returns early into
`updatePopupKey` before the `tab` / `1`–`5` cases), and it is drawn at nearly
the full body width, so the panels it floats over carry little of the answer.

The only escape was `esc`, and what it costs differs per popup. The answer form
keeps `detail.form`, so the picks come back on the next `enter` — a round trip
through a closed question. The repair (`R`) and follow-up (`F`) forms discard
the draft outright, so looking something up means retyping the prompt, and in
practice people decide without looking. A step can sit in `awaiting_input` for
`input_timeout` (24 h by default) while someone works this out.

Two premises from the issue did not survive reading the code, and are corrected
in decision 1 and decision 7 below.

## Decisions

**1. Scope is `taskView` only — three popups, not two surfaces.**
*(2026-08-30)*

The issue asks for "both surfaces": `shell.overlayPopup` on the board home
screen and `taskView`'s popup path. There is only one. Since task 049,
`views.go` sets `home.boardOnly = true` and `updateBoardOnly` sends `enter` out
as a `selectTaskMsg` and drops `R`/`F` into the board, so `shell.popup` is never
set on the shipped path: `shell.updateKey`'s three popup branches,
`shell.capturesInput`'s popup arm and `shell.paste`'s popup arm are reachable
only from the focused component tests in `shell_test.go`. The issue's "on the
board home screen there is no Details tab to arrive at once you are out" no
longer describes anything — there is no way to be in a popup there.

That routing is left exactly as it is. This change does not clean up that
surface, and the create-PR form (task 052.6) does not get the tab either, even
though its `esc` discards a draft for the same reason. It is a separable
follow-up if it is ever wanted.

**Beat:** deleting the shell's dead popup path in the same change. It is a
second reviewable thing with its own risk of taking a live test with it.

**2. The Details tab is the sidebar renderer, from an extracted sub-model.**
*(2026-08-30)*

`renderDetails` is a two-pane layout — a 16–24 column section sidebar against an
independently scrolled content pane (task 049.8) — and held its state in seven
fields on `taskView`. Those fields and their methods are now `detailsPane`
(`internal/tui/detailspane.go`), and the workspace tab and each open popup own
an instance. One implementation, identical navigation in both places.

The pane is *fed* its lines rather than owning the data: `detailLines` and
`pullSectionLines` stay on `taskView`, because the "GitHub pull request" section
reads `t.pull`, which the pane has no business fetching.

**Beat:** rendering a second, summarized details body for the popup. A summary
is a second thing to keep in sync and reintroduces "leave to see the rest",
which is the whole complaint.

**3. A popup with tabs takes the full height budget.** *(2026-08-30)*

`ph = max(bodyH-4, 6)`, replacing `min(height(inner)+2, max(bodyH-4, 6))` for
these three. No frame jitter across a `ctrl+t`, and the Details tab gets every
line it can. This does change how the three popups look: a two-line question
now floats in a full-height box. Accepted as the cost of a frame that does not
resize under the reader. The compare-URL editor, outside this change, keeps
sizing to its content.

**4. `ctrl+t` switches the tabs, intercepted before the form.** *(2026-08-30)*

It is unbound anywhere in `bindings.go`, non-printable, and passes through
every sub-mode these popups have: the answer form's free-text `textarea`, the
repair/follow-up prompt `textarea` (which spends `enter` on newlines), and the
agent/model/effort `picker`. It matches `ctrl+s` as the modifier convention
these popups already use. `taskView.updatePopupKey` takes it **before** the form
sees it — that seam is the one thing that makes it survive a focused editor —
and the forms themselves stay unaware that they have tabs.

**5. `esc` on the Task details tab returns to the form tab.** *(2026-08-30)*

One layer per press, which is 017 decision 13's rule as task 053 restated it. It
does not close the popup and it does not touch the draft. `esc` on the form tab
keeps its existing per-popup meaning. §15's `esc` stack gains this inner layer.

**6. The Task details tab is read-only, and more strictly than the workspace
tab is.** *(2026-08-30)*

`updateDetailsKey`'s `default` arm forwards unhandled keys to
`t.detail.update(msg)` so task actions stay live on the workspace tab. Inside
the popup that arm is not taken, and `o` (open the pull request) and `P` (open
the create-PR form) are not offered: a popup that can raise a second popup is
not a reference surface.

**7. `overlayPopup` becomes a free function, because it is already shared.**
*(2026-08-30)*

`shell.overlayPopup` *is* live, despite decision 1: `taskView.render` builds a
throwaway `shell{detail: t.detail, bodyW: t.width, bodyH: t.height}` and calls
it, borrowing the renderer rather than duplicating it. That throwaway is
constructed fresh every frame and can carry no tab state, so `overlayPopup` now
takes the popup state it draws — `overlayPopup(bg, bodyW, bodyH, popupOverlay)`
— and the tab and its pane live on the view that owns the popup. The three
forms reach it through a small `popupForm` interface (`height`, `render`),
which is all three already had in common.

## A gap this closed on the way

`taskView.bindingContext()` returned `ctxCreatePR` when the create-PR form was
open but had no arm for the other three popups — it fell through to the current
tab's context. So while the answer, repair or follow-up popup owned the
keyboard, `?` and the footer described the *Steps* tab, and the `ctxForm` /
`ctxRepairForm` / `ctxFollowUpForm` rows added in tasks 025 and 027 were
unreachable from the footer on the shipped surface. (`help.go` printed the first
two unconditionally for home contexts, which is why this stayed invisible — and
it never printed a follow-up section at all.) Both are fixed here: the three
arms are added, and `help.go` prints all three popups' sections.

## Screenshots

No re-capture was needed: none of the eight `docs/assets/tui-*.png` shows any of
the three popups — `tui-multi-select.png` is the board's bulk selection, not the
answer form's multi-select. A shot of the new tab would need a new tape in
`scripts/screenshots.sh` and is optional.

## Tasks

- [x] **059.1** Extract `detailsPane` from `taskView`'s seven `details*` fields
  and four methods, keeping the existing `taskview_test.go` cases passing
  against it. ✓ 2026-08-30
- [x] **059.2** Turn `shell.overlayPopup` into a free function over a
  `popupOverlay`, drawing the two-tab strip as the popup's first body line and
  sizing per decision 3. ✓ 2026-08-30
- [x] **059.3** Give `taskView` a per-popup tab index and details pane, with
  `ctrl+t` and the details-tab `esc` intercepted in `updatePopupKey` ahead of
  the form, and the tab read-only per decision 6. ✓ 2026-08-30
- [x] **059.4** Register `ctrl+t` in `ctxForm`, `ctxRepairForm` and
  `ctxFollowUpForm` with probes; add the three `bindingContext` arms and the
  follow-up section to the `?` sheet; add each form's hint. ✓ 2026-08-30
- [x] **059.5** Spec §15 amendment, `docs/guides/tui.md`, and the tests that
  prove the draft survives the round trip — including a live one against the
  real handlers. ✓ 2026-08-30
