package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// `tui.keys` (task 115). The daemon never uses a keymap; what it owes the TUI
// is that a keymap which breaks §15's vocabulary never loads — not at start,
// not on a hot reload, not through PATCH /v1/config — so a TUI only ever
// receives one it can apply.

// An absent or empty map is the shipped keymap, and neither is an error.
func TestTUIKeysDefaultIsTheShippedKeymap(t *testing.T) {
	if keys := Default().TUI.Keys; len(keys) != 0 {
		t.Errorf("default tui.keys = %v, want none", keys)
	}
	for name, content := range map[string]string{
		"absent":      "tui:\n  hyperlinks: false\n",
		"empty flow":  "tui:\n  keys: {}\n",
		"empty block": "tui:\n  keys:\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, content))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.TUI.Keys) != 0 {
				t.Errorf("tui.keys = %v, want none", cfg.TUI.Keys)
			}
			if len(cfg.TUI.Board.GroupBy) != 2 {
				t.Errorf("tui.keys dropped the default grouping: %v", cfg.TUI.Board.GroupBy)
			}
		})
	}
}

// A keymap the checker accepts loads as written, in either YAML style, and a
// swap of two operations in one file is one edit (decision 6).
func TestTUIKeysLoads(t *testing.T) {
	for name, content := range map[string]string{
		"flow":  "tui:\n  keys: {refresh: ctrl+e, help: f2}\n",
		"block": "tui:\n  keys:\n    refresh: ctrl+e\n    help: f2\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, content))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.TUI.Keys["refresh"] != "ctrl+e" || cfg.TUI.Keys["help"] != "f2" || len(cfg.TUI.Keys) != 2 {
				t.Errorf("tui.keys = %v, want refresh: ctrl+e and help: f2", cfg.TUI.Keys)
			}
		})
	}
	if _, err := Load(writeConfig(t, "tui:\n  keys: {pause: x, reject: p}\n")); err != nil {
		t.Errorf("swapping pause and reject was refused: %v", err)
	}
}

// Every refusal names the key path and the operation, in the house style of
// `tui.board.group_by: …`, so the log line on a rejected reload says what to
// fix.
func TestTUIKeysRefused(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          []string
	}{
		{"a key another operation has", "tui:\n  keys: {refresh: q}\n", []string{"refresh", `"q"`, "quit"}},
		{"a fixed operation", "tui:\n  keys: {group: G}\n", []string{"group", "not rebindable"}},
		{"an unknown operation", "tui:\n  keys: {reload: R}\n", []string{"reload", "unknown operation"}},
		{"a string no press produces", "tui:\n  keys: {refresh: ctrl+nope}\n", []string{"refresh", "ctrl+nope"}},
		{"a typed character on a text-field escape hatch", "tui:\n  keys: {palette_alt: x}\n", []string{"palette_alt"}},
		{"not a map", "tui:\n  keys: [refresh]\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.content))
			if err == nil {
				t.Fatalf("Load accepted %q", tc.content)
			}
			if tc.want == nil {
				return
			}
			msg := err.Error()
			for _, w := range append([]string{"tui.keys: "}, tc.want...) {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not mention %q", msg, w)
				}
			}
		})
	}
}

// A hot reload that would install a refused keymap is dropped, and the last
// good configuration — keymap included — is what stays delivered (§12.3).
func TestWatchDropsARefusedKeymap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tui:\n  keys: {refresh: ctrl+e}\n")

	reloads := make(chan Config, 16)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Watch(t.Context(), log, dir, new(sync.Mutex), func(c Config) { reloads <- c }); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	next := func() Config {
		t.Helper()
		select {
		case c := <-reloads:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for a config reload")
			return Config{}
		}
	}

	write("tui:\n  keys: {refresh: f5}\n")
	if got := next(); got.TUI.Keys["refresh"] != "f5" {
		t.Fatalf("reloaded tui.keys = %v, want refresh: f5", got.TUI.Keys)
	}

	// `q` is quit's: refused, so no callback. The sleep lets its debounce
	// window fire on its own before the next write, as in
	// TestWatchReloadsValidAndDropsInvalid.
	write("tui:\n  keys: {refresh: q}\n")
	time.Sleep(4 * debounce)
	write("max_parallel_tasks: 9\ntui:\n  keys: {refresh: f6}\n")
	// A save can fire more than once, so a late repeat of the f5 reload is
	// tolerated; the refused map is not.
	for {
		got := next()
		if got.TUI.Keys["refresh"] == "q" {
			t.Fatalf("watcher delivered a refused keymap: %v", got.TUI.Keys)
		}
		if got.TUI.Keys["refresh"] == "f6" {
			if got.MaxParallelTasks != 9 {
				t.Fatalf("max_parallel_tasks = %d, want 9", got.MaxParallelTasks)
			}
			return
		}
	}
}

// `tui.keys` ships as a commented block under an active `tui:`, the
// environment.set shape, so setting it uncomments the line where it stands and
// the file keeps its explanation.
func TestApplyUncommentsTUIKeysInPlace(t *testing.T) {
	got, err := Apply([]byte(defaultConfigYAML), []Set{
		{Path: "tui.keys", Value: RenderMap(map[string]string{"refresh": "ctrl+e", "help": "?"})},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "\n  keys: {help: \"?\", refresh: ctrl+e}\n") {
		t.Errorf("tui.keys was not uncommented in place:\n%s", out)
	}
	if a, b := strings.Count(out, "\n"), strings.Count(defaultConfigYAML, "\n"); a != b {
		t.Errorf("the template grew from %d lines to %d; tui.keys was appended instead of uncommented", b, a)
	}
	cfg, err := Decode(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.TUI.Keys["refresh"] != "ctrl+e" || cfg.TUI.Keys["help"] != "?" {
		t.Errorf("tui.keys = %v", cfg.TUI.Keys)
	}

	// An override back to nothing is the shipped keymap, written as `{}`.
	cleared, err := Apply(got, []Set{{Path: "tui.keys", Value: RenderMap(nil)}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(cleared), "\n  keys: {}\n") {
		t.Errorf("clearing tui.keys did not write {}:\n%s", cleared)
	}
	if cfg, err := Decode(cleared); err != nil || len(cfg.TUI.Keys) != 0 {
		t.Errorf("cleared tui.keys = %v, err %v; want the shipped keymap", cfg.TUI.Keys, err)
	}
}

// The example the template carries has to be one the checker accepts, or the
// first thing a reader tries is refused.
func TestTemplateKeysExampleLoads(t *testing.T) {
	var example []string
	inKeys := false
	for _, line := range strings.Split(defaultConfigYAML, "\n") {
		_, body, commented := virtual(line)
		switch {
		case commented && body == "keys:":
			inKeys = true
		case inKeys && commented && strings.Contains(body, ": "):
			example = append(example, "    "+body)
		case inKeys:
			inKeys = false
		}
	}
	if len(example) == 0 {
		t.Fatal("the template carries no commented tui.keys example")
	}
	content := "tui:\n  keys:\n" + strings.Join(example, "\n") + "\n"
	if _, err := Load(writeConfig(t, content)); err != nil {
		t.Errorf("the template's tui.keys example is refused: %v\n%s", err, content)
	}
}
