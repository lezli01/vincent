// Package config resolves platform-native config/data directories and
// loads, validates, and hot-reloads config.yaml (spec §12.2–12.3; T1.1).
//
// It imports exactly two internal packages, both leaves that import only the
// standard library, so the dependency direction stays one-way:
//
//   - internal/taskstate, to validate `notify.on` against §6's state
//     vocabulary rather than keeping a second copy of it (task 046 decision 4);
//   - internal/keymap, to validate `tui.keys` against §15's key vocabulary
//     with the same checker the TUI's registry tests run over the defaults.
//
// Task 046 decision 4 allowed taskstate and only taskstate; task 115
// decision 3 explicitly amends it to add keymap, on the same reasoning.
package config
