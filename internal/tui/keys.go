package tui

import (
	"strings"
	"sync/atomic"

	"github.com/lezli01/vincent/internal/keymap"
)

// The effective keymap (task 115). Every handler of a rebindable operation
// asks opKey for the key it answers, and every surface that names one renders
// the same answer, so a `tui.keys` override moves the handler and the help
// together — the help advertising a key the handler ignores is the defect task
// 093 closed, and the registry being the dispatch source is what keeps it
// closed (decision 2).
//
// It is package state rather than a field threaded through forty views for
// the reason the registry itself is: there is one keymap per TUI process, the
// root is the only writer (on every config fetch and after its own editor's
// PATCH), and every reader runs on Bubble Tea's update goroutine. The atomic
// pointer is for the tests that read it from a live program's goroutine.
type activeKeys struct {
	km   keymap.Keymap
	rows []binding
}

var currentKeys atomic.Pointer[activeKeys]

func init() { setKeymap(keymap.Default()) }

// opKey is the key op is bound to in the effective keymap.
func opKey(op keymap.Op) string { return currentKeys.Load().km.Key(op) }

// setKeymap installs km and re-resolves the registry against it.
func setKeymap(km keymap.Keymap) {
	rows := make([]binding, len(bindings))
	for i, b := range bindings {
		if b.op != "" {
			if key := km.Key(b.op); key != b.key {
				b.hint = rebindHint(b.hint, b.key, key)
				b.key = key
			}
		}
		rows[i] = b
	}
	currentKeys.Store(&activeKeys{km: km, rows: rows})
}

// applyKeys installs the keymap a config fetch carried. The daemon refuses a
// `tui.keys` that fails keymap.Build before it is ever served, so a refusal
// here means a daemon older or newer than this client; the TUI keeps the
// keymap it had rather than guessing (decision 9).
func applyKeys(overrides map[string]string) {
	km, err := keymap.Build(overrides)
	if err != nil {
		return
	}
	setKeymap(km)
}

// registry is the binding registry with the effective keymap applied: what `?`,
// the palette and the footer render and what a replayed row presses.
func registry() []binding { return currentKeys.Load().rows }

// rebindHint rewrites a footer hint's key part. Every hint on a row that
// carries an operation is "<default key> <word>", which bindings_test.go
// asserts, so the word is kept and only the key is swapped.
func rebindHint(hint, from, to string) string {
	if rest, ok := strings.CutPrefix(hint, from+" "); ok {
		return to + " " + rest
	}
	return hint
}
