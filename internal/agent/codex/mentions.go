package codex

import "github.com/lezli01/vincent/internal/agent"

// codex recognizes the syntax and does not expand it: through `codex exec` a
// mention arrives as prose, and with tools allowed the model reads the path
// itself in one tool call (§5.5, §9.3). That difference is stated, never
// emulated, and it does not stop the mention being worth sending (task 126
// decision 6).
var _ agent.FileMentioner = (*Adapter)(nil)

// FileMentionSyntax implements agent.FileMentioner (§9.1, §9.3, task 126):
// sigil `@`, anywhere in the message, and `Expands: false`. Observed on
// codex-cli 0.154.0 (issue #545's research) — §9.3's paragraph records why:
// at `rust-v0.154.0` the mention machinery lives entirely in the TUI crate
// and `codex-rs/exec/src/cli.rs` carries no file-reference flag.
func (a *Adapter) FileMentionSyntax() agent.FileMentionSyntax {
	return agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: false}
}

// FileMention implements agent.FileMentioner: `@` and the path, never quoted.
// Nothing was observed that quoting would help with, and a quoting rule
// invented for a CLI that does not read the mention at all would be a guess
// the model then has to see through.
//
// skills.go's linked `[$name](path)` form normalizes backslashes, but that is
// the *linked skill* path codex reads out of a markdown link
// (`codex-rs/tui/src/mention_codec.rs`) and it proves nothing about `@`.
//
// The path is passed through: workspace-relative, forward-slash, git's own
// bytes (task 124 decision 9, task 126 decisions 2 and 21). An empty path
// yields the bare `@`.
func (a *Adapter) FileMention(relPath string) string { return "@" + relPath }
