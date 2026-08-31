package claude

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestNoPlanOrCommandOutput is the mirror of codex's TestUsageFields: task
// 070 added `agent.plan` and `agent.command_output` to the *shared* §13.2
// vocabulary, and claude fills neither. It is stated positively over every
// fixture, the way TestNoRunHeaderOrResultMetadata states codex's absences,
// so "unreported" is asserted rather than inferred from a gap.
//
// claude does have a plan — `TodoWrite` — but it is a tool call, not a
// dedicated stream item, and turning one into a Plan would be emulation
// (§9.2). It renders as the tool call it is.
func TestNoPlanOrCommandOutput(t *testing.T) {
	for _, name := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_permission_deny_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		events, _ := fixtureEvents(t, name)
		for i, ev := range events {
			if ev.Type == agent.EventPlan || ev.Plan != nil {
				t.Errorf("%s line %d: produced a plan", name, i)
			}
			if ev.Type == agent.EventCommandOutput || ev.Output != nil {
				t.Errorf("%s line %d: produced a command output body", name, i)
			}
			if ev.Result != nil && ev.Result.ReasoningOutputTokens != 0 {
				t.Errorf("%s line %d: reasoning tokens = %d, want unreported",
					name, i, ev.Result.ReasoningOutputTokens)
			}
		}
	}
}
