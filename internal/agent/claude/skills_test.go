package claude

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestClaudeListsSkills pins decision 13's static bool for claude: it
// implements SkillLister (task 124.7), so GET /v1/agents reports
// `supports_skill_listing: true` whatever build is installed.
func TestClaudeListsSkills(t *testing.T) {
	if !agent.CanListSkills(New(nil)) {
		t.Error("CanListSkills(claude) = false, want true")
	}
}

// TestSkillSyntax pins §9.2's invocation syntax (task 124 decision 18): the
// sigil and position published as `skill_sigil` and `skill_position` on
// GET /v1/agents, and the text a client inserts for a plain and a plugin
// skill — the CLI's own name after the sigil, nothing translated.
func TestSkillSyntax(t *testing.T) {
	a := New(func() string { return "" })
	if !agent.CanInvokeSkills(a) {
		t.Fatal("CanInvokeSkills = false, want true")
	}
	want := agent.SkillSyntax{Sigil: "/", Position: agent.SkillLeading}
	if got := a.SkillSyntax(); got != want {
		t.Errorf("SkillSyntax() = %+v, want %+v", got, want)
	}
	among := []agent.Skill{{Name: "review"}, {Name: "fmt:check", Plugin: "fmt"}}
	for i, want := range []string{"/review", "/fmt:check"} {
		if got := a.Invocation(among[i], among); got != want {
			t.Errorf("Invocation(%q) = %q, want %q", among[i].Name, got, want)
		}
	}
}
