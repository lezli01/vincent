package cursor

import "github.com/lezli01/vincent/internal/agent"

// cursor invokes skills and deliberately does not list them (§9.7, task 124
// decision 2): the `-p` dialect vincent drives has no listing, and ACP's
// `available_commands_update` disagrees with what a `-p` turn loads. Whether
// to adopt ACP is #514's to decide. CanListSkills answers false for it.
var _ agent.SkillInvoker = (*Adapter)(nil)

// SkillSyntax implements agent.SkillInvoker (§9.1, §9.7, task 124 decision
// 18): cursor-agent recognizes `/name` anywhere in the message, as a token.
// Observed on cursor-agent 2026.09.18 (issue #496's research;
// https://cursor.com/docs/skills.md).
func (a *Adapter) SkillSyntax() agent.SkillSyntax {
	return agent.SkillSyntax{Sigil: "/", Position: agent.SkillAnywhere}
}

// Invocation implements agent.SkillInvoker: `/` and the CLI's own name.
func (a *Adapter) Invocation(s agent.Skill, _ []agent.Skill) string { return "/" + s.Name }
