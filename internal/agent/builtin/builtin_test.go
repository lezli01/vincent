package builtin_test

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/lezli01/vincent/internal/agent/builtin"
	"github.com/lezli01/vincent/internal/config"
)

func noPath(string) string { return "" }

// TestNewOrder pins the registry order every caller relies on: doctor reports
// and the daemon probes the adapters in it.
func TestNewOrder(t *testing.T) {
	got := builtin.New(noPath).Names()
	want := []string{"claude", "codex", "cursor"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

// TestConfigPathsIsTotal sets a distinct path on every config.Agents field and
// requires each registered adapter to get its own back, so an adapter added to
// New without its ConfigPaths arm fails here.
func TestConfigPathsIsTotal(t *testing.T) {
	cfg := config.Default()
	cfg.Agents.Claude.Path = "/opt/claude"
	cfg.Agents.Codex.Path = "/opt/codex"
	cfg.Agents.Cursor.Path = "/opt/cursor-agent"
	want := map[string]string{
		"claude": "/opt/claude",
		"codex":  "/opt/codex",
		"cursor": "/opt/cursor-agent",
	}
	paths := builtin.ConfigPaths(func() config.Config { return cfg })
	names := builtin.New(noPath).Names()
	if len(names) != len(want) {
		t.Fatalf("registry has %v, test covers %v", names, want)
	}
	for _, name := range names {
		w, ok := want[name]
		if !ok {
			t.Fatalf("adapter %q has no expected path in this test", name)
		}
		if got := paths(name); got != w {
			t.Errorf("ConfigPaths(%q) = %q, want %q", name, got, w)
		}
	}
	if got := paths("nope"); got != "" {
		t.Errorf("ConfigPaths(unknown) = %q, want empty", got)
	}
}

// TestPathsFollowReload swaps the configuration under a built registry: the
// adapter's next path read sees the new value without a rebuild, and nothing is
// read at construction.
func TestPathsFollowReload(t *testing.T) {
	var current atomic.Pointer[config.Config]
	first := config.Default()
	first.Agents.Codex.Path = "/old/codex"
	current.Store(&first)

	var seen []string
	paths := builtin.ConfigPaths(func() config.Config { return *current.Load() })
	reg := builtin.New(func(name string) string {
		p := paths(name)
		seen = append(seen, name+"="+p)
		return p
	})
	if len(seen) != 0 {
		t.Fatalf("New read paths eagerly: %v", seen)
	}
	codex, ok := reg.Get("codex")
	if !ok {
		t.Fatal("no codex adapter")
	}

	// Path resolution may fail — neither path exists — but the accessor is
	// what is under test, and it must be consulted on every read.
	_, _ = codex.Path()
	second := config.Default()
	second.Agents.Codex.Path = "/new/codex"
	current.Store(&second)
	_, _ = codex.Path()

	want := []string{"codex=/old/codex", "codex=/new/codex"}
	if !slices.Equal(seen, want) {
		t.Fatalf("accessor reads = %v, want %v", seen, want)
	}
}
