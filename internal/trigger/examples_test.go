package trigger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/workflow"
)

// TestShippedExampleTriggers holds every trigger under docs/examples/triggers
// to what the handoff guide promises of it (task 137.12, decision 3): it
// parses and validates exactly as `vincent trigger validate` would, it starts
// a handoff of a workflow this repository ships with `role: handoff` — an
// example under examples/ or one of its own .vincent/workflows — and it is
// shipped unarmed, because a person copies and arms it (the never-arm rule).
func TestShippedExampleTriggers(t *testing.T) {
	root := filepath.Join("..", "..")
	paths, err := filepath.Glob(filepath.Join(root, "docs", "examples", "triggers", "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("docs/examples/triggers contains no .yaml files; this test would pass vacuously")
	}
	handoffs := shippedHandoffWorkflows(t, root)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			d, errs := Parse(src, Stem(path))
			if len(errs) > 0 {
				t.Fatalf("does not validate: %v", errs)
			}
			if d.Enabled {
				t.Error("ships armed (enabled: true); an example is copied and armed by a person")
			}
			if d.Action.Type != ActionStartHandoff {
				t.Errorf("action.type = %q, want %q", d.Action.Type, ActionStartHandoff)
			}
			if !handoffs[d.Action.Workflow] {
				t.Errorf("action.workflow %q is not a shipped role: handoff workflow (have %v)", d.Action.Workflow, handoffs)
			}
		})
	}
}

// shippedHandoffWorkflows names every `role: handoff` workflow under
// examples/ and .vincent/workflows/. Only the two keys are read: whether a
// workflow validates is the workflow package's tests' question.
func shippedHandoffWorkflows(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, dir := range []string{"examples", filepath.Join(".vincent", "workflows")} {
		paths, err := filepath.Glob(filepath.Join(root, dir, "*.yaml"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, path := range paths {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var head struct {
				Name string `yaml:"name"`
				Role string `yaml:"role"`
			}
			if err := yaml.Unmarshal(src, &head); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			if head.Role == workflow.RoleHandoff {
				out[head.Name] = true
			}
		}
	}
	return out
}
