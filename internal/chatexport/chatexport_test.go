package chatexport

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden exports under testdata")

func ptr[T any](v T) *T { return &v }

// fixture is one chat covering every case the brief names: a multi-turn
// claude exchange with tool calls, a failed turn, a pruned transcript, an
// agent_unavailable turn, a copilot turn with no cost, and a running turn.
func fixture(t *testing.T) *Export {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "turn1.normalized.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tools, err := ToolsFromNormalized(f)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new ```\n"
	return &Export{
		Version:    Version,
		ExportedAt: created.Add(time.Hour),
		Chat: Chat{
			ID: 7, Title: "Fix the parser", ProjectID: 1, Project: "demo",
			Agent: "claude", Model: "opus", Effort: "high", PermissionMode: "full_auto",
			State: "idle", Branch: "vincent/chat-7-fix-the-parser", BaseBranch: "main",
			BaseSHA: "abc123", LinkedTaskID: ptr[int64](3), IssueID: ptr[int64](9), CreatedAt: created,
		},
		Turns: []Turn{
			{
				Seq: 1, State: "done", Prompt: "Why does `Parse` fail?", Tools: tools,
				Answer: "Fixed it.\n\n- one\n- two", InputTokens: 1200, OutputTokens: 340,
				CostUSD: ptr(0.0123), DurationMS: ptr[int64](12345), StartedAt: created,
			},
			{
				Seq: 2, State: "failed", Prompt: "Run it again", Tools: []ToolCall{},
				InputTokens: 10, OutputTokens: 0, FailReason: "agent_error", ErrorMessage: "rate limited",
				StartedAt: created,
			},
			{
				Seq: 3, State: "done", Prompt: "Summarize", Tools: []ToolCall{}, ToolsNote: NotePruned,
				Answer: "All good.", InputTokens: 5, OutputTokens: 6, CostUSD: ptr(0.0), StartedAt: created,
			},
			{
				Seq: 4, State: "failed", Prompt: "Hello?", Tools: []ToolCall{}, ToolsNote: NoteNeverWritten,
				FailReason: "agent_unavailable", StartedAt: created,
			},
			{
				Seq: 5, State: "done", Prompt: "copilot turn", Tools: []ToolCall{{Name: "view", Arg: "README.md"}},
				Answer: "Read it.", InputTokens: 50, OutputTokens: 20, DurationMS: ptr[int64](900), StartedAt: created,
			},
			{
				Seq: 6, State: "running", Prompt: "Keep going", Tools: []ToolCall{{Name: "Edit", Arg: "x.go"}},
				StartedAt: created,
			},
		},
		Diff: &diff,
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	// Git may check the golden out with CRLF on Windows.
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Errorf("%s differs from the golden file; run go test ./internal/chatexport -update and review.\ngot:\n%s", name, got)
	}
}

func TestGoldenExports(t *testing.T) {
	for _, f := range []Format{FormatMarkdown, FormatHTML, FormatJSON} {
		t.Run(string(f), func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, fixture(t), f); err != nil {
				t.Fatal(err)
			}
			golden(t, "chat."+f.Ext(), buf.Bytes())
		})
	}
}

func TestToolsFromNormalized(t *testing.T) {
	got := fixture(t).Turns[0].Tools
	want := []ToolCall{
		{Name: "Read", Arg: "internal/parse/parse.go"},
		{Name: "Grep", Arg: "func Parse"},
		{Name: "Task", Arg: "explore"},
		{Name: "Bash", Arg: "go test ./internal/parse", Failed: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %+v\nwant %+v", got, want)
	}
}

func TestMarkdownCarriesEveryCase(t *testing.T) {
	md := Markdown(fixture(t))
	for _, want := range []string{
		"# Chat 7: Fix the parser",
		"- **linked task:** #3",
		"- **issue:** #9",
		"- `Bash go test ./internal/parse (failed)`",
		"**Failed:** agent_error: rate limited",
		"_" + NotePruned + "_",
		"_" + NoteNeverWritten + "_",
		"_Usage: 1200 in · 340 out · $0.0123 · 12.3s_",
		"_Usage: 50 in · 20 out · 900ms_", // copilot: no cost, not $0
		"## Turn 6 · running",
		"````diff\n", // the diff holds ``` so its fence is longer
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if strings.Contains(md, "secret output") {
		t.Error("tool output leaked into the export")
	}
	// The order inside a turn is prompt, tools, answer, usage.
	turn := md[strings.Index(md, "## Turn 1"):strings.Index(md, "## Turn 2")]
	last := -1
	for _, h := range []string{"### Prompt", "### Tool calls", "### Answer", "_Usage:"} {
		i := strings.Index(turn, h)
		if i <= last {
			t.Fatalf("%q out of order in turn 1:\n%s", h, turn)
		}
		last = i
	}
}

func TestHTMLIsSafeAndSelfContained(t *testing.T) {
	e := fixture(t)
	e.Turns[0].Answer = "Look: <script>alert(1)</script> and <b onclick=\"x()\">bold</b>\n\n" +
		"<iframe src=\"https://evil.example\"></iframe>\n\n" +
		"![pic](https://example.com/p.png) [bad](javascript:alert(1))"
	e.Turns[0].Prompt = "<img src=x onerror=alert(1)>"
	e.Turns[0].Tools = []ToolCall{{Name: "Bash", Arg: "echo '<script>'"}}
	var buf bytes.Buffer
	if err := Render(&buf, e, FormatHTML); err != nil {
		t.Fatal(err)
	}
	page := strings.ToLower(buf.String())
	for _, bad := range []string{"<script", "<iframe", "onclick", "onerror", "<img", "javascript:", "<link", "@import"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if !strings.Contains(page, "echo &#39;&lt;script&gt;&#39;") {
		t.Error("tool line was not escaped")
	}
	// The only URLs on the page are the answer's own links, never a src.
	if strings.Contains(page, "src=") {
		t.Error("page loads something")
	}
	if !strings.Contains(page, `<a href="https://example.com/p.png">pic</a>`) {
		t.Errorf("image was not turned into a link:\n%s", page)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	e := fixture(t)
	var buf bytes.Buffer
	if err := Render(&buf, e, FormatJSON); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != float64(1) {
		t.Fatalf("version = %v, want 1", raw["version"])
	}
	var back Export
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, e) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", back, *e)
	}
}

func TestJSONDiffUnavailable(t *testing.T) {
	e := fixture(t)
	e.Diff, e.DiffUnavailable = nil, "the chat has no worktree"
	var buf bytes.Buffer
	if err := Render(&buf, e, FormatJSON); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if v, ok := raw["diff"]; !ok || v != nil {
		t.Errorf("diff = %v (present %v), want null", v, ok)
	}
	if raw["diff_unavailable"] != "the chat has no worktree" {
		t.Errorf("diff_unavailable = %v", raw["diff_unavailable"])
	}
	if md := Markdown(e); !strings.Contains(md, "_diff unavailable: the chat has no worktree_") {
		t.Errorf("markdown lacks the unavailable note:\n%s", md)
	}
}

func TestParseFormatAndFilename(t *testing.T) {
	for in, want := range map[string]Format{"": FormatMarkdown, "md": FormatMarkdown, "html": FormatHTML, "json": FormatJSON} {
		if got, err := ParseFormat(in); err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseFormat("pdf"); err == nil {
		t.Error("ParseFormat(pdf) succeeded")
	}
	for _, c := range []struct {
		title string
		f     Format
		want  string
	}{
		{"Fix the parser!", FormatMarkdown, "chat-7-fix-the-parser.md"},
		{"   ", FormatHTML, "chat-7.html"},
		{"Ünïcode / path\\sep", FormatJSON, "chat-7-n-code-path-sep.json"},
		{strings.Repeat("abcdefgh ", 10), FormatMarkdown, "chat-7-abcdefgh-abcdefgh-abcdefgh-abcdefgh.md"},
	} {
		if got := Filename(7, c.title, c.f); got != c.want {
			t.Errorf("Filename(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestToolSummary(t *testing.T) {
	if got := ToolSummary("Read", "a.go"); got != "Read a.go" {
		t.Errorf("got %q", got)
	}
	if got := ToolSummary("TodoWrite", ""); got != "TodoWrite" {
		t.Errorf("got %q", got)
	}
}
