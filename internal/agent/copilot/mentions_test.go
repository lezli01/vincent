package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestFileMentionSyntax pins §9.9's mention syntax for copilot (task
// 138.13): sigil `@`, recognized anywhere in the message, and `Expands:
// false`. TestMentionIsNotExpanded is the capture behind the last field.
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

// TestFileMention pins copilot's answer over the same input list the other
// adapters' tables run, so the four are comparable row for row: copilot
// quotes none of them, as cursor does not.
func TestFileMention(t *testing.T) {
	a := New(func() string { return "" })
	for _, tc := range []struct{ name, in, want string }{
		{"no space", "notes.txt", "@notes.txt"},
		{"nested", "sub/deep.txt", "@sub/deep.txt"},
		// Passed through, not cleaned (task 124 decision 9).
		{"a leading ./", "./notes.txt", "@./notes.txt"},
		{"a space", "dir with space/q.txt", "@dir with space/q.txt"},
		{"a double quote", `say"hi".txt`, `@say"hi".txt`},
		{"both a quote and a space", `dir with space/say"hi".txt`, `@dir with space/say"hi".txt`},
		{"the empty path", "", "@"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.FileMention(tc.in); got != tc.want {
				t.Errorf("FileMention(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// fixtureLine is the part of a capture line these tests read.
type fixtureLine struct {
	Type string `json:"type"`
	Data struct {
		Content            string `json:"content"`
		TransformedContent string `json:"transformedContent"`
		ToolName           string `json:"toolName"`
		Arguments          struct {
			Skill string `json:"skill"`
		} `json:"arguments"`
	} `json:"data"`
}

func fixtureLines(t *testing.T, name string) []fixtureLine {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []fixtureLine
	for _, raw := range splitLines(b) {
		var l fixtureLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, l)
	}
	return out
}

// userMessage is the capture's one root user.message.
func userMessage(t *testing.T, name string, lines []fixtureLine) fixtureLine {
	t.Helper()
	var got []fixtureLine
	for _, l := range lines {
		if l.Type == "user.message" {
			got = append(got, l)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%s: %d user.message lines, want 1", name, len(got))
	}
	return got[0]
}

// TestMentionIsNotExpanded backs `Expands: false` with
// mention_1.0.105.jsonl: the prompt reaches the model with `@README.md` as
// typed and none of the file's content — the README's first line is
// `Zebra marmalade lighthouse 4417` — no tool ran, and the answer says the
// file could not be read. Recapture it when testedVersions moves.
func TestMentionIsNotExpanded(t *testing.T) {
	const name = "mention_1.0.105.jsonl"
	lines := fixtureLines(t, name)
	msg := userMessage(t, name, lines)
	if !strings.Contains(msg.Data.TransformedContent, "@README.md") {
		t.Errorf("transformedContent = %q, want the mention left as typed", msg.Data.TransformedContent)
	}
	for _, l := range lines {
		if strings.Contains(l.Data.TransformedContent, "Zebra") || strings.Contains(l.Data.Content, "Zebra") {
			t.Errorf("%s: the file's content reached the conversation (%s line)", name, l.Type)
		}
		if l.Type == "tool.execution_start" {
			t.Errorf("%s: a tool ran (%s)", name, l.Data.ToolName)
		}
	}
	if out := outputText(t, name); !strings.Contains(out, "without reading the file") {
		t.Errorf("answer = %q, want copilot saying it could not read the file", out)
	}
}

// outputText is the capture's normalized output, joined.
func outputText(t *testing.T, name string) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range parseFixture(t, name) {
		if ev.Type == agent.EventOutput {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}
