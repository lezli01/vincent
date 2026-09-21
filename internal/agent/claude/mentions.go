package claude

import (
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// claude is the one shipped adapter that *expands* a mention: the CLI puts
// the file in front of the model before the model sees the message, without a
// tool call (§5.5, §9.2).
var _ agent.FileMentioner = (*Adapter)(nil)

// FileMentionSyntax implements agent.FileMentioner (§9.1, §5.5, task 126):
// claude recognizes `@path` as a token anywhere in the message — unlike its
// *skill* invocation, which is leading-only (skills.go). The two sigils do
// not share a rule and neither is evidence about the other. Observed on
// claude 2.1.278 (issue #545's research); §9.1's 2.1.277 pin elsewhere is not
// widened by this.
func (a *Adapter) FileMentionSyntax() agent.FileMentionSyntax {
	return agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: true}
}

// FileMention implements agent.FileMentioner: `@` and the path, double-quoted
// after the sigil when the path contains a space.
//
// §5.5's path-form table is what picks the quoting rule, observed on claude
// 2.1.278: `@"dir with space/q.txt"` expanded, while the bare
// `@dir with space/unq.txt` and the backslash-escaped
// `@dir\ with\ space/bs.txt` both did not. Backslash escaping is exactly what
// does *not* work here.
//
// A double quote inside the filename is therefore left alone unless the path
// also carries a space (task 126 decision 24): claude was never probed with
// one, and `\"` would be a rule invented ahead of the observation. A path
// carrying both yields `@"a "b".txt"`, which claude will very likely
// mis-parse — that is the rule's observed consequence, not a guarantee, and
// it is pinned in the test table rather than papered over.
//
// The path is passed through: workspace-relative, forward-slash, git's own
// bytes, nothing cleaned (task 124 decision 9, task 126 decisions 2 and 21).
// An empty path yields the bare `@`.
func (a *Adapter) FileMention(relPath string) string {
	if strings.Contains(relPath, " ") {
		return `@"` + relPath + `"`
	}
	return "@" + relPath
}
