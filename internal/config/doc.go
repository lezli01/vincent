// Package config resolves platform-native config/data directories and
// loads, validates, and hot-reloads config.yaml (spec §12.2–12.3; T1.1).
//
// It imports exactly four internal packages, all leaves that import only the
// standard library, so the dependency direction stays one-way:
//
//   - internal/taskstate, to validate `notify.on` against §6's state
//     vocabulary rather than keeping a second copy of it (task 046 decision 4);
//   - internal/keymap, to validate `tui.keys` against §15's key vocabulary
//     with the same checker the TUI's registry tests run over the defaults;
//   - internal/chatstate, to validate `notify.chat_on` against its chat
//     moments (task 165), so `turn_interrupted` is refused in one place;
//   - internal/fileswap, to replace its files atomically the way
//     internal/workflow does (#969).
//
// Task 046 decision 4 allowed taskstate and only taskstate; task 118
// decision 3 explicitly amends it to add keymap, and task 165 adds
// chatstate, on the same reasoning.
package config
