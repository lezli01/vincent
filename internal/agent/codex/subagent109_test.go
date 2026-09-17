package codex

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestNoSubagentEvents states codex's side of task 109 positively: the
// subagent events are claude's alone, and no captured codex run produces one.
func TestNoSubagentEvents(t *testing.T) {
	for _, name := range []string{
		"failure.jsonl", "mcp_0.150.1.jsonl", "plan_0.150.1.jsonl", "reasoning_0.147.0.jsonl",
		"resume_0.150.1.jsonl", "success.jsonl", "tooluse.jsonl",
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
