package codex

import "github.com/lezli01/vincent/internal/agent"

// codex invokes skills but does not list them yet: listing through the
// app-server's `skills/list` is task 124's codex item (#504). Until then
// CanListSkills answers false for it, which is today's truth.
var _ agent.SkillInvoker = (*Adapter)(nil)

// SkillSyntax implements agent.SkillInvoker (§9.1, §9.3, task 124 decision
// 18): codex recognizes `$name` anywhere in the message. Observed on
// codex-cli 0.154.0 (issue #496's research; `codex-rs/skills/src/mentions.rs`
// at `rust-v0.154.0`).
func (a *Adapter) SkillSyntax() agent.SkillSyntax {
	return agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}
}

// Invocation implements agent.SkillInvoker: `$` and the CLI's own name.
//
// codex selects a skill by a plain `$name` only when the name is unambiguous
// (`codex-rs/skills/src/selection.rs`); two skills sharing a name need the
// linked form `[$name](path)`. That form is #504's, which is why among is in
// the signature and unread here.
func (a *Adapter) Invocation(s agent.Skill, _ []agent.Skill) string { return "$" + s.Name }
