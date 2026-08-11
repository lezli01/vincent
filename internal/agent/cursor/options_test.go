package cursor

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// modelsOutput is the shape of `cursor-agent models` (captured from
// 2026.08.04-aaa8809): a heading, `id - Display Name` lines, and a trailing
// tip paragraph that mentions --model and a bracketed example — the two
// things a naive parser would swallow as model ids.
const modelsOutput = `Available models

auto - Auto (current, default)
gpt-5.3-codex-high - Codex 5.3 High
claude-sonnet-5-thinking-xhigh - Sonnet 5 1M Extra High Thinking
gemini-3.6-flash-low - Gemini 3.6 Flash Low

Tip: use --model <id> (or /model <id> in interactive mode) to switch. Parameterized models also accept quoted overrides, e.g. --model 'claude-opus-4-8[context=1m,effort=high,fast=false]'.
`

func TestParseModels(t *testing.T) {
	got := parseModels(modelsOutput)
	want := []string{
		"auto", "gpt-5.3-codex-high",
		"claude-sonnet-5-thinking-xhigh", "gemini-3.6-flash-low",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseModels = %v, want %v", got, want)
	}
	for _, m := range got {
		if strings.HasPrefix(m, "Tip") || strings.HasPrefix(m, "--") {
			t.Errorf("parsed %q out of the tip paragraph", m)
		}
	}
}

func TestParseModelsUnparseable(t *testing.T) {
	if got := parseModels("something went wrong\n"); len(got) != 0 {
		t.Errorf("parseModels = %v, want nothing for unparseable output", got)
	}
}

// TestCuratedFloor pins the §9.7 catalog shape: `auto` alone as the curated
// floor, no efforts at all, and a non-empty adapter default — cursor is the
// first adapter that has one, which is what lets /v1/resolve name level 4.
func TestCuratedFloor(t *testing.T) {
	opts := New(nil).Curated()
	if len(opts.Efforts) != 0 {
		t.Errorf("Efforts = %v, want empty: cursor has no effort flag (§9.7)", opts.Efforts)
	}
	if opts.DefaultEffort != "" {
		t.Errorf("DefaultEffort = %q, want empty", opts.DefaultEffort)
	}
	if opts.DefaultModel != "auto" {
		t.Errorf("DefaultModel = %q, want auto — what buildArgs actually passes", opts.DefaultModel)
	}
	if len(opts.Models) != 1 || opts.Models[0].Value != "auto" ||
		opts.Models[0].Source != agent.SourceCurated {
		t.Errorf("Models = %v, want exactly the curated auto", opts.Models)
	}
}

func TestOptionsProbe(t *testing.T) {
	opts, err := fakeAdapter(t).Options(t.Context())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	byValue := map[string]string{}
	for _, m := range opts.Models {
		byValue[m.Value] = m.Source
	}
	// `auto` is in both the probe and the curated floor; the probe wins, so
	// it must not be duplicated and must not be labelled curated.
	if len(opts.Models) != 3 {
		t.Errorf("Models = %v, want 3 deduplicated entries", opts.Models)
	}
	if byValue["auto"] != agent.SourceCLI {
		t.Errorf("auto source = %q, want cli — the probe outranks the floor", byValue["auto"])
	}
	if byValue["fake-model-high"] != agent.SourceCLI {
		t.Errorf("fake-model-high missing or mislabelled: %v", opts.Models)
	}
	if len(opts.Efforts) != 0 {
		t.Errorf("Efforts = %v, want empty even after a successful probe", opts.Efforts)
	}
}

// TestOptionsDegradesToCurated is the §9.6 contract that matters more for
// cursor than for any other adapter: its probe is an authenticated network
// call, so "offline" and "logged out" are ordinary states, not corners.
func TestOptionsDegradesToCurated(t *testing.T) {
	a := New(func() string { return "/nonexistent/cursor-agent-not-here" })
	opts, err := a.Options(t.Context())
	if err == nil {
		t.Error("Options succeeded with no binary; want the probe error reported")
	}
	if len(opts.Models) != 1 || opts.Models[0].Value != "auto" {
		t.Errorf("Models = %v, want the curated floor served alongside the error", opts.Models)
	}
	if opts.DefaultModel != "auto" {
		t.Errorf("DefaultModel = %q, want auto even on a failed probe", opts.DefaultModel)
	}
}

// TestOptionsProbeUsesTheModelsSubcommand guards the invocation itself: the
// fake binary answers `models` and nothing else, so a wrong subcommand shows
// up here rather than as an empty catalog in production.
func TestOptionsProbeUsesTheModelsSubcommand(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	opts, err := New(func() string { return path }).Options(t.Context())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	found := false
	for _, m := range opts.Models {
		if m.Value == "fake-model-low" {
			found = true
		}
	}
	if !found {
		t.Errorf("Models = %v, want the fake models list", opts.Models)
	}
}
