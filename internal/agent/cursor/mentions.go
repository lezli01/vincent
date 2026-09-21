package cursor

import "github.com/lezli01/vincent/internal/agent"

// cursor recognizes the syntax and does not expand it: through `-p` a mention
// arrives as prose, and with tools allowed the model reads the path itself in
// one tool call (§5.5, §9.7). That difference is stated, never emulated, and
// it does not stop the mention being worth sending (task 126 decision 6).
var _ agent.FileMentioner = (*Adapter)(nil)

// FileMentionSyntax implements agent.FileMentioner (§9.1, §9.7, task 126):
// sigil `@`, anywhere in the message, and `Expands: false`. Observed on
// cursor-agent 2026.09.18 (issue #545's research) — §9.7's paragraph records
// why: `@` is documented for the interactive session alone, under "Selecting
// context" in <https://cursor.com/docs/cli/using.md>, and
// `cli/reference/parameters.md` does not mention it.
func (a *Adapter) FileMentionSyntax() agent.FileMentionSyntax {
	return agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: false}
}

// FileMention implements agent.FileMentioner: `@` and the path, never quoted,
// for the reason codex's does not quote either — nothing was observed that a
// quoting rule would help with on a CLI that does not read the mention.
//
// The path is passed through: workspace-relative, forward-slash, git's own
// bytes (task 124 decision 9, task 126 decisions 2 and 21). An empty path
// yields the bare `@`.
func (a *Adapter) FileMention(relPath string) string { return "@" + relPath }
