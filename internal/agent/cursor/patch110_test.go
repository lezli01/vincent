package cursor

import (
	"path/filepath"
	"testing"
)

// TestNoPatch is cursor's half of the statement codex's file makes (task
// 110): an edit reports its `+N −M` delta from `linesAdded`/`linesRemoved`,
// which is the form claude's delta now shares, and no hunks — so over every
// fixture no event carries an `agent.patch` body (§9.7).
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
