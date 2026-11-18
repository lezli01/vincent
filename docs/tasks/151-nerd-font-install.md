# 151 — Installing the Nerd Font behind `tui.icons: nerd`

**Status:** ✅ done (3/3) · **Opened:** 2026-11-18
**Spec:** §12.3 · **Branch:** `feat/tui-redesign`

`tui.icons: nerd` (task 150.8) needs a Nerd Font, and vincent already offers to
install agent skills. The author asked for the font to be installable the same way,
on Linux and macOS.

Conventions for this file are in [the tasks README](README.md).

## Decisions

Taken 2026-11-18 by the author.

1. **macOS and Linux only for now.** On Windows the commands say it is unsupported.
2. **One pinned font, not a choice.** JetBrainsMono Nerd Font Mono: the Mono variant
   keeps every icon to one cell, which is what the TUI's layouts measure.
3. **Download and verify, with no package manager.** The archive and each face are
   pinned by SHA-256. *Beat:* shelling out to `brew`, which Linux does not share.
4. **The hashes are the record of what vincent wrote.** Uninstall removes a file only
   when its content is exactly the pinned face. *Beat:* a manifest that can drift.
5. **Icons turn on only after someone sees them.** A program cannot read its terminal's
   font, so the flow shows real icons and asks.
6. **Not offered over SSH, or where the terminal already has the icons.** A font on the
   remote machine never reaches the screen; Ghostty and WezTerm include these icons.

## Tasks

- [x] **151.1** `internal/nerdfont`: pinned release, install, uninstall, terminal advice.
- [x] **151.2** `vincent fonts status`, `install` and `uninstall`.
- [x] **151.3** The daemon view's `U` flow and its offer line.
