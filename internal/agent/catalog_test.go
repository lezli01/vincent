package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCatalogs mirrors the shipped curated catalogs: claude and codex share
// low/medium/high, each has exclusive efforts, and only claude curates
// models (§9.3: codex model ids are account-dependent).
func testCatalogs() Catalogs {
	opt := func(vs ...string) []Option {
		out := make([]Option, 0, len(vs))
		for _, v := range vs {
			out = append(out, Option{Value: v, Source: SourceCurated})
		}
		return out
	}
	return Catalogs{
		"claude": {Models: opt("sonnet", "opus", "haiku"), Efforts: opt("low", "medium", "high", "xhigh", "max")},
		"codex":  {Models: opt(), Efforts: opt("minimal", "low", "medium", "high", "xhigh")},
	}
}

// TestCatalogsCheck pins the §8.2 cross-catalog rule: own catalog = valid,
// another adapter's catalog = error, no catalog = warning.
func TestCatalogsCheck(t *testing.T) {
	c := testCatalogs()
	tests := []struct {
		name      string
		sel       Selection
		wantErrs  int
		wantWarns int
	}{
		{name: "empty values check nothing", sel: Selection{Agent: "claude"}},
		{name: "own catalog is valid", sel: Selection{Agent: "claude", Model: "sonnet", Effort: "max"}},
		{name: "shared effort is valid on both", sel: Selection{Agent: "codex", Effort: "low"}},
		{name: "xhigh is valid on codex too", sel: Selection{Agent: "codex", Effort: "xhigh"}},
		{name: "claude model on codex is an error", sel: Selection{Agent: "codex", Model: "sonnet"}, wantErrs: 1},
		{name: "claude-only effort on codex is an error", sel: Selection{Agent: "codex", Effort: "max"}, wantErrs: 1},
		{name: "codex-only effort on claude is an error", sel: Selection{Agent: "claude", Effort: "minimal"}, wantErrs: 1},
		{name: "unknown model warns and passes", sel: Selection{Agent: "codex", Model: "gpt-5.6-sol"}, wantWarns: 1},
		{name: "unknown effort warns and passes", sel: Selection{Agent: "claude", Effort: "turbo"}, wantWarns: 1},
		{name: "error and warning can coexist", sel: Selection{Agent: "codex", Model: "opus", Effort: "ultra"}, wantErrs: 1, wantWarns: 1},
		{name: "unknown agent is someone else's check", sel: Selection{Agent: "gemini", Model: "sonnet", Effort: "max"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, warns := c.Check(tt.sel)
			if len(errs) != tt.wantErrs || len(warns) != tt.wantWarns {
				t.Errorf("Check(%+v) = %d errors %d warnings, want %d/%d\nerrs: %v\nwarns: %v",
					tt.sel, len(errs), len(warns), tt.wantErrs, tt.wantWarns, errs, warns)
			}
		})
	}

	// The error names the owning adapter so the message teaches the fix.
	errs, _ := c.Check(Selection{Agent: "codex", Model: "sonnet"})
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "claude") {
		t.Errorf("cross-catalog error %v must name the owning adapter", errs)
	}
}

// stubAdapter is a probe-counting Adapter for cache tests; its binary is a
// plain temp file so identity checks exercise real stat calls.
type stubAdapter struct {
	name    string
	path    string
	pathErr error
	av      Availability
	opts    Options
	optsErr error
	curated Options
	detects int
	options int
}

func (s *stubAdapter) Name() string { return s.name }

func (s *stubAdapter) Detect(context.Context) (Availability, error) {
	s.detects++
	return s.av, nil
}

func (s *stubAdapter) Options(context.Context) (Options, error) {
	s.options++
	return s.opts, s.optsErr
}

func (s *stubAdapter) Path() (string, error) { return s.path, s.pathErr }

func (s *stubAdapter) Curated() Options { return s.curated }

func (s *stubAdapter) NewLineParser() LineParser {
	return func(raw []byte) Event { return Event{Type: EventUnknown, Raw: raw} }
}

func (s *stubAdapter) Start(context.Context, RunSpec) (RunHandle, error) {
	return nil, errors.New("stub adapter cannot run")
}

func fakeBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-bin")
	if err := os.WriteFile(path, []byte("v1"), 0o755); err != nil { //nolint:gosec // test binary stand-in
		t.Fatal(err)
	}
	return path
}

func cachedOpts(vs ...string) Options {
	out := Options{}
	for _, v := range vs {
		out.Models = append(out.Models, Option{Value: v, Source: SourceCLI})
	}
	return out
}

func TestCatalogCacheHitAndInvalidation(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "claude", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "1"},
		opts: cachedOpts("sonnet"),
	}
	c := NewCatalogCache(NewRegistry(stub))

	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 1 || stub.options != 1 {
		t.Fatalf("probes = %d/%d after two requests, want one probe (cache hit)", stub.detects, stub.options)
	}

	// Same path, new mtime = a swapped binary: the next request re-probes.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(bin, future, future); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 2 {
		t.Fatalf("detects = %d after binary swap, want re-probe", stub.detects)
	}

	// refresh=true re-probes even with an unchanged identity.
	if _, ok := c.Entry(t.Context(), "claude", true); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 3 {
		t.Fatalf("detects = %d after ?refresh, want a forced probe", stub.detects)
	}

	if _, ok := c.Entry(t.Context(), "nope", false); ok {
		t.Error("Entry returned ok for an unregistered adapter")
	}
}

func TestCatalogCacheMissingBinaryDegrades(t *testing.T) {
	stub := &stubAdapter{
		name:    "codex",
		pathErr: errors.New("codex not found on PATH"),
		av:      Availability{Error: "codex not found on PATH"},
		opts:    Options{Efforts: []Option{{Value: "low", Source: SourceCurated}}},
		optsErr: errors.New("codex not found on PATH"),
	}
	c := NewCatalogCache(NewRegistry(stub))
	e, ok := c.Entry(t.Context(), "codex", false)
	if !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if e.Availability.Found {
		t.Error("Found = true for a missing binary")
	}
	if e.ProbeError == "" {
		t.Error("ProbeError empty; want the Options failure surfaced")
	}
	if len(e.Options.Efforts) == 0 {
		t.Error("Options empty; probe failure must still serve the curated catalog (§9.6)")
	}
	// A still-missing binary is a stable identity: no re-probe per request.
	if _, ok := c.Entry(t.Context(), "codex", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 1 {
		t.Fatalf("detects = %d, want 1 (missing binary cached)", stub.detects)
	}
}

func TestCatalogsPrimedOrCurated(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "claude", path: bin,
		av:      Availability{Found: true, Path: bin},
		opts:    cachedOpts("sonnet", "brand-new-cli-model"),
		curated: cachedOpts("sonnet"),
	}
	c := NewCatalogCache(NewRegistry(stub))

	// Unprimed: the curated catalog, and crucially no probe subprocess.
	before := c.Catalogs()
	if got := len(before["claude"].Models); got != 1 {
		t.Errorf("unprimed catalog has %d models, want curated-only 1", got)
	}
	if stub.detects != 0 && stub.options != 0 {
		t.Error("Catalogs() probed; validation paths must never spawn")
	}

	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	after := c.Catalogs()
	if got := len(after["claude"].Models); got != 2 {
		t.Errorf("primed catalog has %d models, want the probed 2", got)
	}
}
