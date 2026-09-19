package claude

import "github.com/lezli01/vincent/internal/agent"

// claude invokes skills but does not list them yet: listing through the
// stream-json `initialize` request is task 124's claude item (#503). Until
// then CanListSkills answers false for it, which is today's truth.
var _ agent.SkillInvoker = (*Adapter)(nil)

// SkillSyntax implements agent.SkillInvoker (§9.1, §9.2, task 124 decision
// 18): claude expands `/name` only when it starts the message — under
// stream-json input, the message's last text block. Observed on claude
// 2.1.277 (issue #496's research; https://code.claude.com/docs/en/headless).
func (a *Adapter) SkillSyntax() agent.SkillSyntax {
	return agent.SkillSyntax{Sigil: "/", Position: agent.SkillLeading}
}

// Invocation implements agent.SkillInvoker: `/` and the CLI's own name,
// `plugin:skill` included. Any arguments are the human's to type after it.
func (a *Adapter) Invocation(s agent.Skill, _ []agent.Skill) string { return "/" + s.Name }
