package agent_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// stubMentions is the true leg of CanMentionFiles: StubNoMentions' inert body
// with the capability added, which is the whole difference the two legs turn
// on. The false leg is agenttest.StubNoMentions itself, and neither leg is
// proven against a shipped adapter — all three implement the capability
// today, and a refusal pinned to a real CLI inverts itself the day that CLI
// changes (the Resumer lesson, §9.1).
//
// It lives here rather than in agenttest because nothing outside this test
// needs it: the capability is static per adapter and spawns nothing, so a
// consumer testing against it would be testing a constant.
type stubMentions struct{ agenttest.StubNoMentions }

func (stubMentions) Name() string { return "stubmentions" }

func (stubMentions) FileMentionSyntax() agent.FileMentionSyntax {
	return agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: true}
}

func (stubMentions) FileMention(relPath string) string { return "@" + relPath }

// TestCanMentionFilesReportsInterfaceSatisfaction pins §9.1's file-mention
// capability (task 126): CanMentionFiles answers whether the adapter
// implements FileMentioner, and nothing more.
func TestCanMentionFilesReportsInterfaceSatisfaction(t *testing.T) {
	for _, tc := range []struct {
		a    agent.Adapter
		want bool
	}{
		{agenttest.StubNoMentions{}, false},
		{stubMentions{}, true},
	} {
		if got := agent.CanMentionFiles(tc.a); got != tc.want {
			t.Errorf("CanMentionFiles(%s) = %v, want %v", tc.a.Name(), got, tc.want)
		}
	}
}
