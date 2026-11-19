# 152 — A Settings screen

**Status:** ✅ done (3/3) · **Opened:** 2026-11-18
**Spec:** §12.3, §15 · **Branch:** `feat/tui-redesign`

The author asked for configuration to leave its small box on the Daemon screen and
have a screen of its own. The accepted mocks are `150-mocks/settings-*.png`.

Conventions for this file are in [the tasks README](README.md).

## Decisions

Taken 2026-11-18 by the author, from the mocks.

1. **Settings is a screen, not a tab.** It opens from the palette and the Daemon
   screen. *Beat:* a main tab, because settings are visited rather than lived in.
2. **A change applies when it is made.** The daemon already hot-reloads the file,
   so a save step would add nothing.
3. **Every path to a dangerous key confirms.** The arrows and reset stop at the
   same confirmation as the editor (task 060).
4. **The daemon renders the branch preview.** A client never carries a second
   implementation of branch naming (PR L decision).
5. **`backspace` resets a setting.** The mock's `x` already means reject, and §15
   gives one key one meaning.

## Tasks

- [x] **152.1** The Settings screen, its catalogue and its keys.
- [x] **152.2** `POST /v1/config/branch-preview` and `config_path` on `GET /v1/info`.
- [x] **152.3** The Daemon screen keeps a digest that opens Settings.
