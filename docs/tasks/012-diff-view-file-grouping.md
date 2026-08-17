# 012 — A diff tab grouped by file, folded shut

Status: **done** (5/5)

The diff tab rendered `GET /v1/tasks/{id}/diff` exactly as git wrote it: one
stream of lines, the first file's hunks filling the pane, every following file
somewhere below the fold. That answers "what is in this file" before it answers
the question the tab is opened with — *what did this task touch, and which file
do I read first?* An agent's change over fifteen files is read by scrolling
through fourteen of them.

This groups the diff **by file**: a list of foldable rows, each with its path and
its added/removed counts, every one collapsed to begin with, plus expand-all and
collapse-all.

## Decisions

- **Collapsed by default, and the fold state is not configuration**
  *(2026-08-17)*. The first screen of the tab is the file list — that is the
  overview the tab lacked, and it is worth more than the first eighty lines of
  whichever file git happened to write first. Nothing is persisted and no config
  key is added: a fold is a way of looking at *one* diff, unlike
  `tui.board.group_by`, which describes a board shape a human keeps. The
  alternative considered — auto-expanding a single-file diff — was rejected for
  making the tab's shape depend on its content, so `enter` sometimes opens the
  first file and sometimes closes it.
- **↑/↓ move between files; the pager keys scroll lines** *(2026-08-17)*. The
  pane's axis is now the file list, and with everything folded there is nothing
  for a line-scroll key to do — a tab whose ↑/↓ did nothing on the screen it
  opens with is a dead key. Line scrolling stays available on `pgup`/`pgdn`,
  `f`/`b`, `u` and the wheel, which is what a reader inside a long expanded file
  reaches for anyway.
- **The diff tab is its own binding context** *(2026-08-17)*. `ctxOutput` could
  not carry it: the registry is keyed by context *and* key, and `↑/↓` means
  "scroll the tail, dropping follow" on one tab and "walk the files" on the
  other. One row cannot describe both, and a footer offering the wrong one is
  the exact failure the single registry exists to prevent (T4.18). `]` is
  duplicated into the new context so the way back off the tab stays on screen.
- **`O` and `C` for expand-all / collapse-all** *(2026-08-17)*. Every
  lower-case candidate is taken by a §6 action key or a global (`a` approve, `c`
  cancel, `e` transcript, `v` verbosity, `n` new task), and those are checked
  before the pane sees a key. Uppercase is the registry's existing convention for
  the less-frequent variant of a key (`V`, `E`, `A`, `R`). Vim's `zR`/`zM` were
  the other candidate and would have been the TUI's first chord.
- **Folds are keyed by path, not by position** *(2026-08-17)*. Re-entering the
  tab re-fetches, because the endpoint runs git per call and following the event
  stream would mean a subprocess per event (§15, unchanged). Between two fetches
  a file can gain hunks or gain neighbours above it, so an index-keyed fold would
  shut the file the reader was in and open one they were not. Entries for files
  the new diff no longer has are dropped, and moving to another task clears the
  lot: another task's files are not these files.
- **The header row replaces the four lines that repeated it** *(2026-08-17)*.
  `diff --git`, `index`, `---` and `+++` come out of the body, since the row
  above now names the file — three to four lines per file is most of what a
  fifteen-file diff makes you scroll past. Everything else git writes before the
  first hunk stays: a mode change, `rename from`/`rename to`,
  `Binary files … differ` are facts about the file, and the body is the last
  place they can be read. Nothing is reinterpreted beyond that — an expanded file
  is git's own lines.
- **The paths come from the `---`/`+++` markers, not from `diff --git`**
  *(2026-08-17)*. `diff --git a/X b/Y` is one line holding two paths separated by
  a space, which is also a legal character in a path — `a/foo bar.txt b/foo
  bar.txt` cannot be split reliably. Each marker holds exactly one path (git
  appends a tab when it contains a space), `/dev/null` names the side that does
  not exist, so a deletion reads from the old side and an addition from the new,
  and a rename renders as `old → new` from the rename pair. The `diff --git` line
  is the fallback for a section with no markers at all — a binary file, a
  mode-only change — and is *checked* (`a/X` and `b/Y` must agree) rather than
  guessed at.
- **A binary file says `binary` where its counts would go** *(2026-08-17)*. Its
  ± counts are both zero and `+0 -0` reads as "unchanged" rather than "not a text
  diff". The summary line follows the same rule and omits totals of zero, which
  is what a change made only of renames and mode bits produces.
- **The 5000-line cap is unchanged** *(2026-08-17)*. It now bounds the parse as
  well as the render, so a truncated diff's last file shows the counts of the
  part that arrived — the notice already says the whole change is on the branch,
  and re-reading a capped stream to make one number exact would be work spent on
  the case the notice exists for.

## Tasks

- [x] **012.1** Parse a unified diff into per-file sections: paths (renames,
  deletions, names with spaces), ± counts inside hunks, binary detection, and the
  lines before the first file kept rather than dropped.
  *Done when:* a diff holding an edit, a name with a space, an addition, a
  deletion, a rename and a binary file parses to six correctly named sections
  with the right counts, and the ± *markers* count as neither. ✓ 2026-08-17 —
  `internal/tui/diffgroup.go`; `TestParseDiffFilesReadsPathsAndCounts`,
  `TestDiffFileMarkersAreNotCountedAsChanges`,
  `TestDiffFileBodyDropsOnlyRepetition`,
  `TestParseDiffFilesKeepsWhatPrecedesTheFirstFile`.
- [x] **012.2** The fold itself: collapsed-by-default file rows with counts, a
  file cursor, `enter`/`space`/`→`/`←` on one file, `O`/`C` on all of them, and a
  pinned summary line.
  *Done when:* the tab opens on the file list with no body visible, each fold key
  moves the same state, and `O`/`C` move the whole list. ✓ 2026-08-17 —
  `TestDiffOpensWithEveryFileCollapsed`, `TestDiffTogglesTheFileUnderTheCursor`,
  `TestDiffCursorWalksFiles`, `TestDiffExpandAllAndCollapseAll`,
  `TestDiffBinaryFileSaysSoInsteadOfCountingNothing`.
- [x] **012.3** Fold lifetime: kept across a refresh by path, pruned to the files
  the diff still has, cleared for another task.
  *Done when:* a file expanded before a refetch that added a file above it is
  still open, with the cursor still on it. ✓ 2026-08-17 —
  `TestDiffFoldsSurviveARefresh`, `TestDiffForgetsFoldsOfFilesThatWentAway`,
  `TestDiffCollapsesAgainForAnotherTask`.
- [x] **012.4** Discovery and the mouse: `ctxDiff` in the registry with probes,
  the footer/`?`/palette following the live tab, a click folding the header it
  lands on, and the wheel scrolling the tab that is actually on screen.
  *Done when:* `TestEveryPanelKeyIsHandled` covers every new row, the footer over
  a diff names files and folds, and a click derived from the rendered frame folds
  the file it points at. ✓ 2026-08-17 — probes for `]`, `down`, `enter`, `O`, `C`;
  `TestDiffTabIsItsOwnSurface`, `TestDiffClickFoldsThroughTheShell`,
  `TestDiffWheelScrollsTheDiff`, `TestDiffClickFoldsTheHeaderItLandsOn`.
- [x] **012.5** Docs: the spec §15 amendment and the TUI guide's diff section and
  key table.
  *Done when:* the spec describes the tab as shipped and the guide's key table
  lists the fold keys. ✓ 2026-08-17 — spec §15 (view 2 and *Diff tab*),
  `docs/guides/tui.md`.
