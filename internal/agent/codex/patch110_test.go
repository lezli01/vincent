package codex

import (
	"path/filepath"
	"testing"
)

// TestNoPatch is codex's half of a positive statement (task 110): the
// `agent.patch` record joined the shared §13.2 vocabulary with claude as the
// only adapter that fills it. codex's `file_change` item is read for each
// change's path and kind, and for no hunks, so over every fixture no event
// carries one (§9.3).
func TestNoPatch(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("fixtures: %v, %v", names, err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		for i, ev := range parseFixture(t, name) {
			if ev.Patch != nil {
				t.Errorf("%s line %d: produced a patch", name, i)
			}
		}
	}
}
