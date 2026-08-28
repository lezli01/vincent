# 049 — Board-only home and full-screen task workspace

**Status:** ✅ done (5/5)
**Spec:** amends §15

## Problem

The home screen fused the task table, attempt timeline, and output/diff into an
accordion. That made the board compete with the task being inspected, and made
metadata, transcripts, and diffs fit into partial-width panes even though only
one of those surfaces is normally being read at a time. Basic task data — most
notably the description and declared fields — had no complete TUI surface.

## Decisions

**1. Home is the task board; opening is a routed screen transition.**
*(2026-08-28)*

The board keeps its existing table, grouping, filtering, bulk selection and
live refresh behavior, at full size. `enter` sends the selected id through the
root router to a dedicated task workspace; `esc` returns. Task creation lands
in the same workspace because creation begins watching that task.

**Beat:** retaining the fused shell and merely changing panel ratios, which
would still make task inspection subordinate to board navigation.

**2. Four tabs, each owning the whole task body.** *(2026-08-28)*

The order is Steps & Attempts (default), Task Details, Output, Diff. The attempt
cursor stays in one detail sub-model, so choosing an attempt on the first tab
still selects the transcript on the third without duplicating state. `tab` and
`shift+tab` are primary, `[`/`]` are compact aliases, and `1`–`4` are direct
jumps. `enter` on an attempt jumps directly to Output, whose selector can move
between attempts with `←`/`→` or `h`/`l`. Diff is fetched only when its tab
opens, preserving the existing rule that live output cannot launch a git
subprocess.

**3. Task Details is complete and read-only.** *(2026-08-28)*

It renders every user-meaningful field already returned by `GET /v1/tasks/{id}`:
title, description, declared fields, identity and execution metadata, workflow
origin and snapshot, lifecycle, holds and blocks, pending input, fan-out and
loop rollups, captured GitHub issue, warnings and available actions. It scrolls
locally and introduces no daemon state or edit path.

**4. The existing detail sub-model stays intact.** *(2026-08-28)*

Timeline selection, transcript catch-up, live streaming, forms, task actions and
diff folding remain one model. The new task screen only routes input and chooses
which renderer gets the viewport. This preserves the live-output seam and means
moving between tabs cannot select a different attempt.

## Tasks

- [x] **049.1** Route Board and Task as separate screens; make board the only
  home content and preserve board filtering, grouping, bulk actions and refresh.
  ✓ 2026-08-28
- [x] **049.2** Add the four-tab full-screen task workspace with keyboard and
  mouse tab selection, defaulting to Steps & Attempts. ✓ 2026-08-28
- [x] **049.3** Add the scrollable Task Details inspector over the complete
  detail response. ✓ 2026-08-28
- [x] **049.4** Move answer/repair/follow-up popups, task actions, Output and
  Diff interaction into the task screen; update focused and live tests.
  ✓ 2026-08-28
- [x] **049.5** Amend §15 and the TUI-facing guides, update the screenshot
  capture flow, regenerate affected assets, and pass the full verification
  suite. ✓ 2026-08-28
- [x] **044.6** Add the Output attempt selector and let `enter` open the
  timeline's selected attempt there. ✓ 2026-08-28
- [x] **044.7** Refine Steps & Attempts and Task Details with clearer visual
  hierarchy, responsive fact groups, breathing room, and lossless wrapping for
  long metadata. ✓ 2026-08-28

## What the tests prove

- The routed home renders board rows and none of the four task surfaces.
- Enter opens the selected task, Steps & Attempts is first, all four tabs cycle
  in order, and `esc` returns to the board.
- Enter on a selected attempt opens its Output, where left/right selection
  changes both the named attempt and the displayed transcript.
- Step groups remain visibly separated, detail facts use two columns only when
  they fit, and narrow or unbroken values wrap without losing content.
- Task Details exposes the description, fields, workflow provenance, branch,
  lifecycle and workflow snapshot and scrolls when it exceeds the terminal.
- The real transcript/API/SSE seam still joins catch-up and live output without
  a duplicate or gap after Output became a separate tab.
- Answer, repair, follow-up, task actions, diff folding and footer/palette
  bindings operate from the task workspace.
