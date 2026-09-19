package cursor

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestSkillSyntax pins §9.7's invocation syntax (task 124 decision 18): the
// sigil and position published as `skill_sigil` and `skill_position` on
// GET /v1/agents, and the text a client inserts for a plain and a plugin
// skill — the CLI's own name after the sigil, nothing translated.
func TestSkillSyntax(t *testing.T) {
	a := New(func() string { return "" })
	if !agent.CanInvokeSkills(a) {
		t.Fatal("CanInvokeSkills = false, want true")
	}
	want := agent.SkillSyntax{Sigil: "/", Position: agent.SkillAnywhere}
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
