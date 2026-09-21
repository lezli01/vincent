package cursor

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestFileMentionSyntax pins §9.7's mention syntax for cursor (task 126):
// sigil `@`, recognized anywhere in the message, and `Expands: false` —
// through `-p` a mention arrives as prose, and the model reads the path
// itself in one tool call. Observed on cursor-agent 2026.09.18.
func TestFileMentionSyntax(t *testing.T) {
	a := New(func() string { return "" })
	if !agent.CanMentionFiles(a) {
		t.Fatal("CanMentionFiles = false, want true")
	}
	want := agent.FileMentionSyntax{Sigil: "@", Position: agent.MentionAnywhere, Expands: false}
	if got := a.FileMentionSyntax(); got != want {
		t.Errorf("FileMentionSyntax() = %+v, want %+v", got, want)
	}
}

// TestFileMention pins cursor's answer over the same input list claude's and
// codex's tables run, so the three adapters are comparable row for row:
// cursor quotes none of them, including the two claude quotes.
func TestFileMention(t *testing.T) {
	a := New(func() string { return "" })
	for _, tc := range []struct{ name, in, want string }{
		{"no space", "notes.txt", "@notes.txt"},
		{"nested", "sub/deep.txt", "@sub/deep.txt"},
		// Passed through, not cleaned: cleaning is a rewrite, and task 124
		// decision 9 is pass-through only.
		{"a leading ./", "./notes.txt", "@./notes.txt"},
		{"a space", "dir with space/q.txt", "@dir with space/q.txt"},
		{"a double quote", `say"hi".txt`, `@say"hi".txt`},
		{"both a quote and a space", `dir with space/say"hi".txt`, `@dir with space/say"hi".txt`},
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
