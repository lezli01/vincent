package codex

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestSkillSyntax pins §9.3's invocation syntax (task 124 decision 18): the
// sigil and position published as `skill_sigil` and `skill_position` on
// GET /v1/agents, and the text a client inserts for a plain and a plugin
// skill — the CLI's own name after the sigil, nothing translated.
//
// The linked form for a name two skills share is TestInvocation's.
func TestSkillSyntax(t *testing.T) {
	a := New(func() string { return "" })
	if !agent.CanInvokeSkills(a) {
		t.Fatal("CanInvokeSkills = false, want true")
	}
	want := agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}
	if got := a.SkillSyntax(); got != want {
		t.Errorf("SkillSyntax() = %+v, want %+v", got, want)
	}
	among := []agent.Skill{{Name: "review"}, {Name: "fmt:check", Plugin: "fmt"}}
	for i, want := range []string{"$review", "$fmt:check"} {
		if got := a.Invocation(among[i], among); got != want {
			t.Errorf("Invocation(%q) = %q, want %q", among[i].Name, got, want)
		}
	}
}

// TestInvocation pins the linked form (§9.3, task 124.8, decision 30):
// `[$name](path)` exactly when the name occurs more than once among the
// listed skills, compared exactly as codex counts, and `$name` otherwise.
func TestInvocation(t *testing.T) {
	a := New(func() string { return "" })
	repo := "/Users/you/Library/Application Support/vincent/data/worktrees/7/.agents/skills/review/SKILL.md"
	user := "/Users/you/.agents/skills/review/SKILL.md"
	winPath := `C:\Users\you\.agents\skills\fmt\SKILL.md`
	among := []agent.Skill{
		{Name: "review", Scope: "repo", Path: repo},
		{Name: "review", Scope: "user", Path: user},
		{Name: "Deploy", Scope: "repo", Path: "/r/Deploy/SKILL.md"},
		{Name: "deploy", Scope: "user", Path: "/u/deploy/SKILL.md"},
		{Name: "pdf:pdf", Scope: "user", Plugin: "pdf@openai-primary-runtime", Path: "/p/pdf/SKILL.md"},
		{Name: "fmt", Scope: "user", Path: winPath},
		{Name: "fmt", Scope: "repo", Path: "/r/fmt/SKILL.md"},
		{Name: "nopath"},
		{Name: "nopath"},
	}
	for _, tc := range []struct {
		name string
		s    agent.Skill
		want string
	}{
		{"a unique name", agent.Skill{Name: "imagegen", Path: "/s/imagegen/SKILL.md"}, "$imagegen"},
		{"a plugin skill", among[4], "$pdf:pdf"},
		{"a repeated name links the repo skill, space and all", among[0], "[$review](" + repo + ")"},
		{"a repeated name links the user skill by its own path", among[1], "[$review](" + user + ")"},
		{"a name differing only in case is not a duplicate", among[2], "$Deploy"},
		{"nor is its twin", among[3], "$deploy"},
		{"a Windows path is kept verbatim", among[5], "[$fmt](" + winPath + ")"},
		{"a skill with no path cannot be linked", among[7], "$nopath"},
	} {
		if got := a.Invocation(tc.s, among); got != tc.want {
			t.Errorf("%s: Invocation(%q) = %q, want %q", tc.name, tc.s.Name, got, tc.want)
		}
	}
}
