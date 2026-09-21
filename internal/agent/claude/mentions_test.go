package claude

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestFileMentionSyntax pins §5.5's mention syntax for claude (task 126):
// sigil `@`, recognized anywhere in the message, and the one adapter whose
// CLI expands a mention itself. Observed on claude 2.1.278.
//
// Position is `anywhere` while SkillSyntax' is `leading` — the two sigils do
// not share a rule, which is why MentionPosition is its own type (task 126
// decision 22).
func TestFileMentionSyntax(t *testing.T) {
	a := New(func() string { return "" })
	if !agent.CanMentionFiles(a) {
		t.Fatal("CanMentionFiles = false, want true")
	}
	want := agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: true}
	if got := a.FileMentionSyntax(); got != want {
		t.Errorf("FileMentionSyntax() = %+v, want %+v", got, want)
	}
}

// TestFileMention pins claude's quoting rule against §5.5's path-form table:
// `@"dir with space/q.txt"` expanded on 2.1.278, the bare and the
// backslash-escaped spellings did not, so a space and only a space earns the
// quotes.
//
// The inputs are the list codex's and cursor's tables run too, so the three
// adapters' answers are comparable row for row: claude quotes exactly the two
// space-carrying rows, and those two are the only ones where its answer
// differs from the other two adapters'.
func TestFileMention(t *testing.T) {
	a := New(func() string { return "" })
	for _, tc := range []struct{ name, in, want string }{
		{"no space", "notes.txt", "@notes.txt"},
		{"nested", "sub/deep.txt", "@sub/deep.txt"},
		// Passed through, not cleaned: cleaning is a rewrite, and task 124
		// decision 9 is pass-through only.
		{"a leading ./", "./notes.txt", "@./notes.txt"},
		{"a space", "dir with space/q.txt", `@"dir with space/q.txt"`},
		// claude was never probed with a quote in a filename, and §5.5
		// records that backslash escaping is exactly what does *not* work
		// after its `@`, so `\"` would be a rule invented ahead of the
		// observation (task 126 decision 24). Unquoted is the pass-through.
		{"a double quote", `say"hi".txt`, `@say"hi".txt`},
		// The unprobed gap, pinned as the rule's consequence rather than as
		// a guarantee: claude will very likely mis-parse this. #557 does not
		// settle it either — it is a claude parsing question, not a Windows
		// one.
		{"both a quote and a space", `dir with space/say"hi".txt`, `@"dir with space/say"hi".txt"`},
		// A guard returning "" would be the daemon judging a path (task 124
		// decision 9); the bare sigil is what pass-through means here.
		{"the empty path", "", "@"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.FileMention(tc.in); got != tc.want {
				t.Errorf("FileMention(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
