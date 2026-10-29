package copilot

import "github.com/lezli01/vincent/internal/agent"

// copilot recognizes the syntax and does not expand it: on stdin a mention
// arrives as prose, and with tools allowed the model reads the path itself
// in one tool call (§5.5, §9.9). That difference is stated, never emulated,
// and it does not stop the mention being worth sending (task 126 decision 6).
var _ agent.FileMentioner = (*Adapter)(nil)

// FileMentionSyntax implements agent.FileMentioner (§9.1, §9.9, task 138.13):
// sigil `@`, anywhere in the message, and `Expands: false`. Observed on
// 1.0.105 (mention_1.0.105.jsonl): the `user.message` echo's
// `transformedContent` is the prompt with `@README.md` left as typed and no
// file content beside it, and the model, told not to use tools, answered
// that it could not read the file. `@` path expansion is documented for the
// interactive session alone
// (https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli),
// and https://docs.github.com/en/copilot/how-tos/copilot-cli/automate-copilot-cli/run-cli-programmatically
// does not mention it.
func (a *Adapter) FileMentionSyntax() agent.FileMentionSyntax {
	return agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: false}
}

// FileMention implements agent.FileMentioner: `@` and the path, never quoted,
// for the reason cursor's does not quote either — nothing was observed that a
// quoting rule would help with on a CLI that does not read the mention, and
// copilot's docs give none. A Windows path was not observed (#557).
//
// The path is passed through: workspace-relative, forward-slash, git's own
// bytes (task 124 decision 9, task 126 decisions 2 and 21). An empty path
// yields the bare `@`.
func (a *Adapter) FileMention(relPath string) string { return "@" + relPath }
