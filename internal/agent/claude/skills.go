package claude

import "github.com/lezli01/vincent/internal/agent"

// claude both invokes and lists its skills: listing is the stream-json
// `initialize` request (list.go, §9.2, task 124.7), so CanListSkills answers
// true for it. A build below the listing floor answers ErrSkillsUnsupported
// at list time (task 124 decision 13).
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
