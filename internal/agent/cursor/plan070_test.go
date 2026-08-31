package cursor

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestNoPlanOrCommandOutput is cursor's half of the same positive statement
// claude's file makes: the `agent.plan` and `agent.command_output` records
// task 070 added to the shared vocabulary are filled by codex alone today,
// and nothing here synthesizes one (§9.7).
func TestNoPlanOrCommandOutput(t *testing.T) {
	for _, name := range []string{"success_2026.08.04.jsonl", "tools_2026.08.11.jsonl"} {
		for i, ev := range parseFixture(t, name) {
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
