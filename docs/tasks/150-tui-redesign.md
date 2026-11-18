# 150 — A ground-up redesign of the TUI

**Status:** ✅ done (11/11) · **Opened:** 2026-11-18
**Spec:** §15 · **Branch:** `feat/tui-redesign`

The author asked for the TUI to be redesigned from the ground up. Four problems
drive it: the screens look flat and dated, chrome text competes with content,
sparse screens waste the terminal, and moving between views is hard to discover.
[gh-dash](https://github.com/dlvhdr/gh-dash) is the reference for the feel:
verbose, clearly separated by borders, informative icons, welcoming.

Conventions for this file are in [the tasks README](README.md).

## Decisions

Taken 2026-11-18 by the author, from mock-ups of the issues board and the chats
workspace.

1. **A full replacement, finished on one branch.** The new UI is completed on
   `feat/tui-redesign` and is the only UI once that branch merges. *Beat:* an
   opt-in config flag with views moved over one at a time, which would keep two
   UIs alive through a long rebuild.
2. **Top tabs with counts are the navigation.** A rounded filter box sits under
   them and a one-line status bar shows only the main keys. *Beat:* a left
   sidebar, because the issue columns need the width.
3. **The issues view is a board of columns.** Open, In progress, Hand-off and
   Done are columns, each with an icon, a color and a count. This supersedes
   task 134 decision 6, which drew the lanes as stacked, foldable sections.
4. **An issue is a card.** It shows the id, kind, age, a two-line title, labels,
   source and one status line. The selected card has a thick border, so
   selection does not depend on color.
5. **Lanes stay derived.** Task 134 decision 1 and task 145 stand: `m` runs the
   action a lane is derived from, and no lane is stored.
6. **A preview pane on wide terminals.** The selected issue is previewed on the
   right at 150 columns and wider. Enter still opens the full issue.
7. **Chats become one workspace.** Chat cards grouped into Waiting on you,
   Running and Idle sit beside the open conversation and a details pane. An
   agent's question is a card pinned where the message box normally sits.
8. **Plain Unicode icons by default.** Nobody needs a special font. Nerd Font
   icons are an opt-in.
9. **ANSI colors, not a fixed palette.** §15's Colour rule requires the palette
   to degrade under `NO_COLOR` and on 16-color terminals. Named ANSI colors also
   follow the terminal's own light or dark theme. The mock-ups' hex palette
   illustrated the layout only.
10. **Done is a column from the start.** The accepted board shows all four
    lanes, so the listing no longer filters to open issues by default. `s` hides
    the column. This reverses task 134 decision 5's default.
11. **The status bar offers the main keys only.** At most six palette-reachable
    keys show, however wide the terminal, and `+N` stands for the rest. This
    reverses task 094's rule that the width alone decides, because a long row of
    keys was one of the four problems this redesign exists to solve.
12. **Chats is a default tab.** `tui.tabs` defaults to `[issues, tasks, chats]`,
    so the three project boards are all one key away.
13. **The task board is columns grouped by issue.** Needs you, Running, Queued
    and Finished are columns, and every column groups its cards by issue, with
    Finished folded to start. This retires `tui.board.group_by` for the live board.
14. **The wide task workspace shows its steps and details as panes.** Steps, Task
    Details and Step Details leave the tab strip from 130 columns.

15. **The management screens get wide layouts over their existing models.** From
    130 columns they use cards and panes; a narrower terminal keeps its tables.
16. **The two archives are one screen.** `tab` crosses between tasks and chats.
17. **A chat turn follows the accepted mock in both panes.** The prompt is a
    `You` card, the answer has a heading, and tool calls are check lines at
    `compact` and `normal`. This supersedes the `›` bubble and narrows task 071's
    shared vocabulary for tool lines in this view; `verbose` keeps the output
    pane's lines. *Beat:* keeping the task pane's two lines per call, which the
    mock replaced.
18. **New task and new chat are one screen from 130 columns.** The accepted mocks
    are in `150-mocks/`. They redraw the same rows and keys, so narrower terminals
    keep the existing forms. Model and effort appear only where the agent can use
    them: an agent with no effort levels has no effort control, a long model
    catalog is a list, and an agent that lists no models keeps a typed model id
    because its CLI still accepts one. *Beat:* the six-stage guided form at every
    width.

## Tasks

- [x] **150.1** Design tokens: tones, glyphs and the rounded and thick boxes.
- [x] **150.2** The issues board: columns, cards, lane and card navigation.
- [x] **150.3** The issue preview pane.
- [x] **150.4** The shell: top tabs with counts, filter box and status bar.
- [x] **150.5** The chats workspace.
- [x] **150.6** The task board and task workspace.
- [x] **150.7** The management views: projects, workflows, daemon, pull requests,
  triggers and the archives.
- [x] **150.8** The Nerd Font icon opt-in: `tui.icons`.
- [x] **150.9** §15, the user guides, the key tables and every `tui-*.png`
  recaptured with `scripts/screenshots.sh`.
- [x] **150.10** The opened issue's wide layout: a title band, the conversation
  pane with boxed comments, and a details pane, from 120 columns and 18 rows.
- [x] **150.11** The wide new-task and new-chat screens, and each branch's commits
  ahead of the project's default branch on `GET /v1/projects/{id}/branches`.
