package codex

import (
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestNoSkillAndNoInputEcho is codex's half of task 124.2, stated as a test
// rather than left as an absence: `exec --json` has no skill item — a model
// reading a SKILL.md is a command like any other, and normalizes as
// agent.command_output — and codex echoes nothing of the prompt it was
// handed, so over every fixture no event is a skill load or an input echo
// (§9.3).
func TestNoSkillAndNoInputEcho(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("fixtures: %v, %v", names, err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		for i, ev := range parseFixture(t, name) {
			if ev.Type == agent.EventSkill || ev.Skill != nil {
				t.Errorf("%s line %d: produced a skill load", name, i)
			}
			if ev.Type == agent.EventInputEcho {
				t.Errorf("%s line %d: produced an input echo", name, i)
			}
		}
	}
}
