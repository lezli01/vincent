package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolSummary(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		// One preference list serves three dialects because the argument
		// names converge (§9.1, T4.14).
		{"claude bash", `{"command":"go test ./...","description":"Run the tests"}`, "go test ./..."},
		{"claude write", `{"file_path":"/repo/hello.txt","content":"hi"}`, "/repo/hello.txt"},
		{"claude grep", `{"pattern":"TODO","path":"internal"}`, "TODO"},
		{"claude fetch", `{"url":"https://example.test/x","prompt":"summarize"}`, "https://example.test/x"},
		{"cursor edit args", `{"path":"/tmp/wt/hi.txt","streamContent":"hello\n"}`, "/tmp/wt/hi.txt"},
		{
			"codex command item", `{"type":"command_execution","command":"pwsh -Command 'echo x'"}`,
			"pwsh -Command 'echo x'",
		},

		// Ordering is the design: the command beats the sentence written
		// about it, which is what cursor's shell calls carry alongside.
		{
			"command wins over description",
			`{"description":"Show git working tree status","command":"git status"}`, "git status",
		},

		// A summary is a line. A heredoc or a multi-line command must not
		// decide how many rows a record occupies.
		{"newlines collapse", `{"command":"a \n\n  b\tc"}`, "a b c"},

		// Nothing recognizable is not an error — the caller renders the
		// bare tool name, exactly as it did before summaries existed.
		{"unknown keys", `{"todos":[{"content":"x"}]}`, ""},
		{"empty value", `{"command":"   "}`, ""},
		{"non-string value", `{"command":42}`, ""},
		{"not an object", `"nope"`, ""},
		{"unparseable", `{`, ""},
		{"absent", ``, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolSummary(json.RawMessage(tc.args)); got != tc.want {
				t.Errorf("ToolSummary(%s) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestToolSummaryTruncates pins the cap at the producing end: every client
// would otherwise need the same guard against a pathological argument.
func TestToolSummaryTruncates(t *testing.T) {
	long, err := json.Marshal(map[string]string{"command": strings.Repeat("x", toolSummaryMax+50)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := ToolSummary(long)
	if n := len([]rune(got)); n != toolSummaryMax+1 {
		t.Errorf("summary length = %d runes, want %d plus the ellipsis", n, toolSummaryMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("summary = %q, want a trailing ellipsis so truncation is visible", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := OneLine("  a  b \n c ", 0); got != "a b c" {
		t.Errorf("OneLine with no cap = %q, want the flattened text", got)
	}
	// Truncation counts runes, not bytes: a cap applied to bytes would split
	// a multi-byte rune and emit invalid UTF-8 into the transcript.
	if got := OneLine("héllo wörld", 5); got != "héllo…" {
		t.Errorf("OneLine = %q, want 5 runes plus the ellipsis", got)
	}
}
