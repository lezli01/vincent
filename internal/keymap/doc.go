// Package keymap is the catalog of the TUI's rebindable operations and the
// checker that holds a keymap to §15's key vocabulary (task 115).
//
// It is the one definition of three things that used to live only in the
// TUI's binding registry and its tests: which operations a `tui.keys` override
// in config.yaml (§12.3) may move, the key each one has by default, and the
// three §15 clauses a keymap must satisfy —
//
//  1. a key may be shared only when it means the same operation;
//  2. a key may mean two things only where the surfaces never co-exist, which
//     the catalog records as exceptions with the decision that made each one;
//  3. a key already carrying an operation may not be given a second meaning.
//
// The same Check runs over the shipped defaults (internal/tui's registry tests)
// and over a user's effective keymap (internal/config, at load, on hot reload
// and on PATCH /v1/config), so the defaults can never pass a rule an override
// is refused by.
//
// What an operation is (task 115 decision 1): one of §15's vocabulary terms,
// one of §6's actions, or a piece of global chrome. Everything else — a
// surface-local row, a multi-key set such as fold or page, esc, ctrl+c, the
// popups' y/n — is fixed, and the catalog records every key the TUI answers
// for those so that an override can be refused before it shadows one.
//
// An override replaces the default; it is not an alias (decision 6). And an
// operation that works while a text field has the keyboard may only be bound
// to a key the text field would not type (decision 5).
//
// keymap is a leaf: it imports only the standard library. internal/config and
// internal/tui both import it; the fact that an archived board offers no §6
// action is proved against internal/taskstate by the TUI's tests rather than
// by an import here.
package keymap
