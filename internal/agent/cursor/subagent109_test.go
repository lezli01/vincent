package cursor

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestNoSubagentEvents states cursor's side of task 109 positively: the
// subagent events are claude's alone, and no captured cursor run produces one.
func TestNoSubagentEvents(t *testing.T) {
	for _, name := range []string{
		"resume_2026.08.11.jsonl", "resume_unknown_2026.08.11.jsonl",
		"success_2026.08.04.jsonl", "tools_2026.08.11.jsonl",
	} {
		for i, ev := range parseFixture(t, name) {
			if isSubagentEvent(ev) {
				t.Errorf("%s line %d: produced a subagent event %q", name, i, ev.Type)
			}
		}
	}
}

func isSubagentEvent(ev agent.Event) bool {
	switch ev.Type { //nolint:exhaustive // only the subagent events matter here
	case agent.EventSubagentStarted, agent.EventSubagentProgress, agent.EventSubagentFinished:
		return true
	}
	return ev.Subagent != nil || ev.ParentCallID != ""
}
