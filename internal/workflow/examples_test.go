package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
)

// TestShippedExamplesValidate parses every workflow in examples/ against the
// real catalogs, so a broken example fails the build rather than a reader.
//
// The done-when for the examples (T4.4, T5.6) names `vincent workflow
// validate` in CI, which is T4.2 and does not exist yet. This test is the
// same assertion one layer down — the CLI subcommand will call this very
// parser — so the examples are guarded from the day they ship rather than
// from the day the CLI lands.
func TestShippedExamplesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read examples/: %v", err)
	}

	// Curated catalogs only: this must not spawn a probe, and an example is
	// not allowed to depend on which CLIs the machine happens to have.
	reg := agent.NewRegistry(
		claude.New(func() string { return "" }),
		codex.New(func() string { return "" }),
		cursor.New(func() string { return "" }),
	)
	catalogs := make(agent.Catalogs, len(reg.Names()))
	for _, a := range reg.All() {
		catalogs[a.Name()] = a.Curated()
	}
	opts := Options{
		KnownAgents: reg.Names(),
		Catalogs:    func() agent.Catalogs { return catalogs },
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		found++
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			wf, warns, err := Parse(src, opts)
			if err != nil {
				t.Fatalf("does not validate: %v", err)
			}
			if len(warns) > 0 {
				// A shipped example is a template people copy; a catalog
				// warning in one teaches the warning along with the workflow.
				t.Errorf("validates with warnings: %v", warns)
			}
			if wf.Name == "" || len(wf.Steps) == 0 {
				t.Errorf("parsed to an empty workflow: %+v", wf)
			}
		})
	}
	if found == 0 {
		t.Fatal("examples/ contains no .yaml files; this test would pass vacuously")
	}
}
