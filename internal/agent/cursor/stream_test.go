package cursor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The fixtures are captured from real cursor-agent 2026.08.04-aaa8809 runs
// with absolute paths, session ids and account identifiers scrubbed. In
// tools_2026.08.04.jsonl the two `tool_call/completed` payloads and the
// closing messages are reconstructed to their documented success shape: the
// capture machine had a user-level Cursor hook that rejected every tool call,
// which is an artifact of that machine, not of the CLI. The `started` lines —
// the only ones this adapter normalizes — are verbatim.

func parseFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var events []agent.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		events = append(events, parse(line))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return events
}

func TestParseSuccessFixture(t *testing.T) {
	events := parseFixture(t, "success_2026.08.04.jsonl")
	counts := map[agent.EventType]int{}
	for _, ev := range events {
		counts[ev.Type]++
		if len(ev.Raw) == 0 {
			t.Errorf("event %q lost its raw line", ev.Type)
		}
	}
	if counts[agent.EventOutput] != 1 {
		t.Errorf("output events = %d, want 1 (the single assistant message)", counts[agent.EventOutput])
	}
	// system, user, thinking×3, thinking/completed — transcripted, not
	// normalized (§9.7).
	if counts[agent.EventUnknown] != 6 {
		t.Errorf("unknown events = %d, want 6 (system, user, 4 thinking)", counts[agent.EventUnknown])
	}
	last := events[len(events)-1]
	if last.Type != agent.EventResult || last.Result == nil {
		t.Fatalf("last event = %q, want a result", last.Type)
	}
	res := last.Result
	if res.IsError {
		t.Errorf("IsError = true for subtype=success")
	}
	if res.ResultText != "OK" {
		t.Errorf("ResultText = %q, want %q", res.ResultText, "OK")
	}
	if res.InputTokens != 8274 || res.OutputTokens != 39 {
		t.Errorf("tokens = %d/%d, want 8274/39 from the camelCase usage keys",
			res.InputTokens, res.OutputTokens)
	}
	if res.CostUSD != nil {
		t.Error("CostUSD is set; cursor reports no cost (§9.7)")
	}
}

func TestParseToolsFixture(t *testing.T) {
	events := parseFixture(t, "tools_2026.08.04.jsonl")
	var tools []string
	for _, ev := range events {
		if ev.Type == agent.EventToolUse {
			for _, tu := range ev.Tools {
				tools = append(tools, tu.Name)
			}
		}
	}
	// Only `started` normalizes; `completed` would double-count. The names
	// come from the tool-shaped key, not from the sibling bookkeeping keys
	// (`toolCallId`, `startedAtMs`, `hookAdditionalContexts`) the payload also
	// carries — sorted first, `hookAdditionalContexts` precedes
	// `shellToolCall` and would misname every shell invocation.
	if strings.Join(tools, ",") != "edit,shell" {
		t.Errorf("tools = %v, want [edit shell]", tools)
	}
	// T4.14: the subject lives one level down under `args`, and the call id
	// is the line-level `call_id` shared by started and completed.
	var uses []agent.ToolUse
	for _, ev := range events {
		if ev.Type == agent.EventToolUse {
			uses = append(uses, ev.Tools...)
		}
	}
	if len(uses) != 2 {
		t.Fatalf("tool uses = %d, want 2", len(uses))
	}
	if uses[0].Summary != "/tmp/wt/hi.txt" || uses[0].CallID != "tool_1" {
		t.Errorf("edit call = %+v, want the edited path and tool_1", uses[0])
	}
	// The shell call carries both `command` and `description`; the command
	// is the subject a reader wants.
	if uses[1].Summary != "git status" || uses[1].CallID != "tool_2" {
		t.Errorf("shell call = %+v, want the command and tool_2", uses[1])
	}
	// The result text is every assistant message concatenated, not the last.
	last := events[len(events)-1]
	if last.Result == nil {
		t.Fatal("last event is not a result")
	}
	if !strings.Contains(last.Result.ResultText, "Creating `hi.txt`") ||
		!strings.Contains(last.Result.ResultText, "Created `hi.txt`") {
		t.Errorf("ResultText = %q, want every assistant message concatenated (§9.7)", last.Result.ResultText)
	}
}

func TestParseTable(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    agent.EventType
		text    string
		tool    string
		isError bool
	}{
		{
			name: "assistant text normalizes to output",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
			want: agent.EventOutput, text: "hi",
		},
		{
			name: "multiple text blocks join",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
			want: agent.EventOutput, text: "ab",
		},
		{
			name: "empty assistant message is not an output event",
			line: `{"type":"assistant","message":{"role":"assistant","content":[]}}`,
			want: agent.EventUnknown,
		},
		{
			name: "tool_call started names the tool",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"readToolCall":{},"toolCallId":"x"}}`,
			want: agent.EventToolUse, tool: "read",
		},
		{
			name: "tool_call completed is not a tool_use",
			line: `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{}}}`,
			want: agent.EventUnknown,
		},
		{
			name: "tool_call with no tool-shaped key is not a tool_use",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"toolCallId":"x","startedAtMs":"1"}}`,
			want: agent.EventUnknown,
		},
		{
			name: "thinking is transcripted but never normalized",
			line: `{"type":"thinking","subtype":"delta","text":"pondering"}`,
			want: agent.EventUnknown,
		},
		{
			name: "result error subtype is an error even without the flag",
			line: `{"type":"result","subtype":"error","result":"boom"}`,
			want: agent.EventResult, isError: true,
		},
		{
			name: "is_error alone is an error",
			line: `{"type":"result","subtype":"success","is_error":true,"result":"boom"}`,
			want: agent.EventResult, isError: true,
		},
		{
			name: "unparseable json is unknown, never a panic",
			line: `{not json at all`,
			want: agent.EventUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := parse([]byte(tt.line))
			if ev.Type != tt.want {
				t.Fatalf("type = %q, want %q", ev.Type, tt.want)
			}
			if tt.text != "" && ev.Text != tt.text {
				t.Errorf("text = %q, want %q", ev.Text, tt.text)
			}
			if tt.tool != "" {
				if len(ev.Tools) != 1 || ev.Tools[0].Name != tt.tool {
					t.Errorf("tools = %v, want [%s]", ev.Tools, tt.tool)
				}
			}
			if tt.want == agent.EventResult {
				if ev.Result == nil {
					t.Fatal("result event carries no RunResult")
				}
				if ev.Result.IsError != tt.isError {
					t.Errorf("IsError = %v, want %v", ev.Result.IsError, tt.isError)
				}
				if tt.isError && ev.Result.ErrorMessage == "" {
					t.Error("ErrorMessage is empty on an error result")
				}
			}
			if string(ev.Raw) != tt.line {
				t.Errorf("Raw = %q, want the verbatim line", ev.Raw)
			}
		})
	}
}

// TestNewLineParserIsStateless documents why cursor's parser needs no
// per-file instance the way codex's does: the terminal result carries the
// whole result text, so replaying a transcript out of order cannot corrupt it.
func TestNewLineParserIsStateless(t *testing.T) {
	a := New(nil)
	line := []byte(`{"type":"result","subtype":"success","result":"done","usage":{"inputTokens":1,"outputTokens":2}}`)
	first := a.NewLineParser()(line)
	second := a.NewLineParser()(line)
	if first.Result.ResultText != second.Result.ResultText ||
		first.Result.InputTokens != second.Result.InputTokens {
		t.Error("two parser instances disagreed on the same line")
	}
}
