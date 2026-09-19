package codex

import "github.com/lezli01/vincent/internal/agent"

// codex both lists its skills (skilllist.go, `skills/list`) and invokes them.
var _ agent.SkillInvoker = (*Adapter)(nil)

// SkillSyntax implements agent.SkillInvoker (§9.1, §9.3, task 124 decision
// 18): codex recognizes `$name` anywhere in the message. Observed on
// codex-cli 0.154.0 (issue #496's research; `codex-rs/skills/src/mentions.rs`
// at `rust-v0.154.0`).
func (a *Adapter) SkillSyntax() agent.SkillSyntax {
	return agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}
}

// Invocation implements agent.SkillInvoker: `$` and the CLI's own name, or
// the linked form `[$name](path)` when that name is not unique among.
//
// codex selects a skill by a plain `$name` only when exactly one enabled
// skill carries that exact name (`codex-rs/skills/src/selection.rs`,
// `name_counts.rs` at `rust-v0.154.0`); two sharing it need the link, whose
// path codex reads up to the first `)`, trims, and normalizes backslashes in
// (`mentions.rs`) — so a worktree under `Application Support` and a Windows
// path both survive verbatim. The count is exact and case-sensitive, as
// codex's is. A skill with no path cannot be linked and keeps `$name`.
//
// codex also refuses a plain `$name` whose lowercased form equals an enabled
// app connector's slug, and `skills/list` cannot reveal connectors: that is
// §9.3's known gap (task 124 decision 30), not a reason to link every name.
func (a *Adapter) Invocation(s agent.Skill, among []agent.Skill) string {
	plain := "$" + s.Name
	if s.Path == "" {
		return plain
	}
	n := 0
	for _, o := range among {
		if o.Name == s.Name {
			n++
		}
	}
	if n < 2 {
		return plain
	}
	return "[" + plain + "](" + s.Path + ")"
}
