package claude

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

func TestParseHelpFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "help_2.1.224.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models, efforts := parseHelp(string(raw))
	if !slices.Equal(models, []string{"sonnet", "opus", "haiku"}) {
		t.Errorf("models = %v, want the documented aliases", models)
	}
	if !slices.Equal(efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("efforts = %v, want the --effort enum", efforts)
	}
}

func TestParseHelpUnparseable(t *testing.T) {
	models, efforts := parseHelp("Usage: mystery\n\nNo options documented here.\n")
	if len(models) != 0 || len(efforts) != 0 {
		t.Errorf("got models=%v efforts=%v from garbage help", models, efforts)
	}
	// The merged catalog degrades to curated-only.
	opts := mergeOptions(models, efforts)
	for _, o := range append(opts.Models, opts.Efforts...) {
		if o.Source != agent.SourceCurated {
			t.Errorf("option %q source = %q, want curated-only fallback", o.Value, o.Source)
		}
	}
	if len(opts.Models) != len(curatedModels) || len(opts.Efforts) != len(curatedEfforts) {
		t.Errorf("fallback catalog incomplete: %+v", opts)
	}
}

func TestMergeOptionsProvenance(t *testing.T) {
	opts := mergeOptions([]string{"sonnet", "brand-new"}, []string{"low"})
	find := func(list []Option, v string) *Option {
		for i := range list {
			if list[i].Value == v {
				return &list[i]
			}
		}
		return nil
	}
	if o := find(opts.Models, "sonnet"); o == nil || o.Source != agent.SourceCLI {
		t.Errorf("probed value must win with source cli, got %+v", o)
	}
	if o := find(opts.Models, "brand-new"); o == nil || o.Source != agent.SourceCLI {
		t.Errorf("cli-only value missing, got %+v", o)
	}
	if o := find(opts.Models, "haiku"); o == nil || o.Source != agent.SourceCurated {
		t.Errorf("curated value must fill in, got %+v", o)
	}
	if o := find(opts.Efforts, "max"); o == nil || o.Source != agent.SourceCurated {
		t.Errorf("unprobed efforts must fall back to curated, got %+v", o)
	}
}

func TestOptionsAgainstFakeagent(t *testing.T) {
	opts, err := fakeAdapter(t).Options(t.Context())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	for _, want := range []string{"sonnet", "opus", "haiku"} {
		found := false
		for _, o := range opts.Models {
			if o.Value == want && o.Source == agent.SourceCLI {
				found = true
			}
		}
		if !found {
			t.Errorf("model %q with source cli missing from %+v", want, opts.Models)
		}
	}
	if len(opts.Efforts) != 5 {
		t.Errorf("efforts = %+v, want the 5 probed values", opts.Efforts)
	}
}

func TestOptionsMissingBinary(t *testing.T) {
	a := New(func() string { return "/nonexistent/claude-not-here" })
	opts, err := a.Options(t.Context())
	if err == nil {
		t.Error("Options returned nil error for a missing binary")
	}
	if len(opts.Models) == 0 || len(opts.Efforts) == 0 {
		t.Errorf("curated catalog must still be served on probe failure, got %+v", opts)
	}
}
