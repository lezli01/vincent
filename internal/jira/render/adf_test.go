package render

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarkdownToADF(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", `{"type":"doc","version":1,"content":[{"type":"paragraph"}]}`},
		{"blank lines only", "\n \n\t\n", `{"type":"doc","version":1,"content":[{"type":"paragraph"}]}`},
		{"one paragraph", "hello", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}`},
		{
			"paragraphs",
			"a\n\n\nb",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]},{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}`,
		},
		{
			"hard breaks",
			"one\ntwo",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}]}]}`,
		},
		{
			"crlf",
			"one\r\ntwo\r\n\r\nthree\r\n",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}]},{"type":"paragraph","content":[{"type":"text","text":"three"}]}]}`,
		},
		{
			"fenced code with language",
			"before\n```go\nfunc f() {}\n\n// x\n```\nafter",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"before"}]},{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"func f() {}\n\n// x"}]},{"type":"paragraph","content":[{"type":"text","text":"after"}]}]}`,
		},
		{
			"fenced code without language",
			"```\nx\n```",
			`{"type":"doc","version":1,"content":[{"type":"codeBlock","content":[{"type":"text","text":"x"}]}]}`,
		},
		{
			"empty fence has no empty text node",
			"```\n```",
			`{"type":"doc","version":1,"content":[{"type":"codeBlock"}]}`,
		},
		{
			"unterminated fence runs to end",
			"```sh\necho a\n\necho b",
			`{"type":"doc","version":1,"content":[{"type":"codeBlock","attrs":{"language":"sh"},"content":[{"type":"text","text":"echo a\n\necho b"}]}]}`,
		},
		{
			"shorter fence does not close",
			"````\n```\n````",
			`{"type":"doc","version":1,"content":[{"type":"codeBlock","content":[{"type":"text","text":"` + "```" + `"}]}]}`,
		},
		{
			"inline markdown stays literal",
			"# Title\n*em* `code` [l](https://x) <b>",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"# Title"},{"type":"hardBreak"},{"type":"text","text":"*em* ` + "`code`" + ` [l](https://x) <b>"}]}]}`,
		},
		{
			"inline backticks are not a fence",
			"``` not `a fence",
			`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"` + "``` not `a fence" + `"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MarkdownToADF(tc.in)
			if string(got) != tc.want {
				t.Errorf("MarkdownToADF(%q)\n got: %s\nwant: %s", tc.in, got, tc.want)
			}
			checkADF(t, got)
		})
	}
}

// checkADF holds a doc to the shape Jira's validator requires: a doc of
// version 1 with content, and no text node that is the empty string.
func checkADF(t *testing.T, raw json.RawMessage) node {
	t.Helper()
	var doc node
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if doc.Type != "doc" || doc.Version != 1 || len(doc.Content) == 0 {
		t.Fatalf("bad root: %s", raw)
	}
	var walk func(node)
	walk = func(n node) {
		if n.Type == "text" && n.Text == "" {
			t.Fatalf("empty text node in %s", raw)
		}
		for _, k := range n.Content {
			walk(k)
		}
	}
	walk(doc)
	return doc
}

type node struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Attrs   struct {
		Language string `json:"language"`
	} `json:"attrs"`
	Content []node `json:"content"`
	Text    string `json:"text"`
}

// plain reads a doc back to the Markdown it came from: paragraphs and code
// blocks separated by a blank line, hardBreaks as newlines.
func plain(doc node) string {
	var blocks []string
	for _, b := range doc.Content {
		var s strings.Builder
		for _, k := range b.Content {
			switch k.Type {
			case "text":
				s.WriteString(k.Text)
			case "hardBreak":
				s.WriteString("\n")
			}
		}
		if b.Type == "codeBlock" {
			blocks = append(blocks, "```"+b.Attrs.Language+"\n"+s.String()+"\n```")
		} else {
			blocks = append(blocks, s.String())
		}
	}
	return strings.Join(blocks, "\n\n")
}

// TestMarkdownToADFRoundTripsAHandoffComment is the acceptance criterion's
// round trip: a comment shaped like the ones vincent posts reads back from
// the ADF as the same lines.
func TestMarkdownToADFRoundTripsAHandoffComment(t *testing.T) {
	comment := strings.Join([]string{
		"**vincent** handed this issue to task #42 (`fix-the-importer`).",
		"Branch: vincent/42-fix-the-importer",
		"",
		"Summary of the change:",
		"- the importer keeps the first line",
		"- a test pins it",
		"",
		"```sh",
		"go test ./internal/jira/...",
		"```",
		"",
		"Reply here to steer it.",
	}, "\n")
	for _, in := range []string{comment, strings.ReplaceAll(comment, "\n", "\r\n")} {
		doc := checkADF(t, MarkdownToADF(in))
		if got := plain(doc); got != comment {
			t.Errorf("round trip changed the comment\n got:\n%s\nwant:\n%s", got, comment)
		}
	}
}

func FuzzMarkdownToADF(f *testing.F) {
	for _, s := range []string{"", "a\nb", "```\n", "```go\nx\n```\n\ny", "\r\n\r\n", "    ```"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := MarkdownToADF(in)
		if string(MarkdownToADF(in)) != string(got) {
			t.Fatalf("not deterministic for %q", in)
		}
		checkADF(t, got)
	})
}
