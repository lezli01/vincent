package tui

import (
	"strings"
	"sync/atomic"

	"github.com/lezli01/vincent/internal/keymap"
)

// The effective keymap (task 118). Every handler of a rebindable operation
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

// setKeymap installs km and re-resolves the registry against it. A row km
// leaves without a key — an unbound operation, a shadowed fixed key (task
// 128) — keeps its label and loses its key and footer hint.
func setKeymap(km keymap.Keymap) {
	rows := make([]binding, len(bindings))
	for i, b := range bindings {
		switch {
		case b.op != "":
			if key := km.Key(b.op); key == "" {
				b.key, b.hint, b.unbound = "", "", true
			} else if key != b.key {
				b.hint = rebindHint(b.hint, b.key, key)
				b.key = key
			}
		case b.key != "" && rowShadowed(km, b):
			b.key, b.hint, b.unbound = "", "", true
		}
		rows[i] = b
	}
	currentKeys.Store(&activeKeys{km: km, rows: rows})
}

// rowShadowed reports whether every key a fixed row answers is shadowed on
// its surface.
func rowShadowed(km keymap.Keymap, b binding) bool {
	surface := keymap.Surface(b.context)
	if b.scope == scopeGlobal {
		surface = keymap.Global
	}
	keys := []string{b.key}
	if len(b.key) > 1 && strings.Contains(b.key, "/") {
		keys = strings.Split(b.key, "/")
	}
	for _, k := range keys {
		if !km.Shadowed(surface, k) {
			return false
		}
	}
	return true
}

// shadowedHere reports whether key is a fixed key the effective keymap
// shadows on the surface in front of the human (task 128 decision 3).
func (m *root) shadowedHere(key string) bool {
	km := currentKeys.Load().km
	return km.Shadowed(keymap.Surface(m.activeContext()), key)
}

// keyOwnerAnsweredOn reports whether the operation key is bound to is one
// ctx answers — so a shadowed fixed key still reaches the view for that
// operation's handler, and is dropped where only the fixed meaning would
// have heard it.
func keyOwnerAnsweredOn(key string, ctx bindingContext) bool {
	km := currentKeys.Load().km
	for _, info := range keymap.Catalog() {
		if km.Key(info.Op) != key {
			continue
		}
		for _, s := range info.Surfaces {
			// Global is not here: the root's globalKey has already had the
			// key by the time this is asked.
			if s == keymap.Surface(ctx) ||
				(s == keymap.Actions && keymap.ActionsLive(keymap.Surface(ctx))) {
				return true
			}
		}
	}
	return false
}

// applyKeys installs the keymap a config fetch carried, through the load
// path's lenient build (task 128 decision 2): an override on a key a default
// or a fixed key holds wins, and the warnings come back for the one-time
// notice. That covers version skew too — a newer TUI against an older daemon
// that served a keymap this release's defaults collide with. A hard error
// still keeps the keymap the TUI had rather than guessing (task 118 decision
// 9).
func applyKeys(overrides map[string]string) []string {
	km, warnings, err := keymap.BuildLenient(overrides)
	if err != nil {
		return nil
	}
	setKeymap(km)
	return warnings
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
