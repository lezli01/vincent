package cursor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The fixtures are captured from real cursor-agent runs with absolute paths,
// session ids and account identifiers scrubbed, each named for the CLI version
// it came from: success_2026.08.04.jsonl from 2026.08.04-aaa8809,
// tools_2026.08.11.jsonl from 2026.08.11-e8db854.
//
// tools_2026.08.11.jsonl replaces a 2026.08.04 capture whose `completed`
// payloads had to be reconstructed to their documented shape, because that
// machine rejected every tool call: Cursor imports Claude Code's hooks and —
// when MSYSTEM is set — runs them through bash after composing them for
// PowerShell, so each one errors and a hook that errors blocks the call
// (T5.7). Re-captured from a shell without MSYSTEM, so every line is now
// verbatim, both outcomes included. That is not cosmetic: the reconstruction
// carried only the edit's result, and a real run also completes the *shell*
// call, which is now covered below.

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
	parse := (&stream{}).parse
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
	// system, user and the three thinking deltas stay unknown — the deltas
	// are swallowed into the buffer and are still unmodeled lines, which is
	// what a reader asking for raw lines should see (§9.7, amended by T4.16).
	if counts[agent.EventUnknown] != 5 {
		t.Errorf("unknown events = %d, want 5 (system, user, 3 thinking deltas)",
			counts[agent.EventUnknown])
	}
	// The deltas coalesce into exactly one thinking event, emitted when
	// `completed` closes the block. Per-delta events are what §9.7 refused,
	// and this is the assertion that would fail if they came back.
	if counts[agent.EventThinking] != 1 {
		t.Fatalf("thinking events = %d, want exactly 1 coalesced block", counts[agent.EventThinking])
	}
	for _, ev := range events {
		if ev.Type != agent.EventThinking {
			continue
		}
		if want := `The user requested the exact word "OK" without using any tools.`; ev.Text != want {
			t.Errorf("thinking text = %q, want the deltas joined verbatim (%q)", ev.Text, want)
		}
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
	events := parseFixture(t, "tools_2026.08.11.jsonl")
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
	// T4.16: the edit's `completed` reports what it did, correlated to the
	// call by id. `+1 −0` rather than the path, which the call line already
	// showed — an outcome has to say something the invocation did not.
	var results []agent.ToolResult
	for _, ev := range events {
		if ev.Type == agent.EventToolResult {
			results = append(results, ev.Results...)
		}
	}
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want 2 (both calls complete in a real run)", len(results))
	}
	if results[0].CallID != "tool_1" || results[0].IsError || results[0].Summary != "+1 −0" {
		t.Errorf("edit result = %+v, want tool_1 succeeding with +1 −0", results[0])
	}
	// The shell outcome falls through to ToolSummary, whose first preference is
	// `command` — so it repeats the invocation rather than reporting what came
	// of it. The real payload carries `exitCode`, `stdout` and `stderr`, none of
	// which reach the summary because ToolSummary only considers string fields
	// and `command` wins the order. Asserted as-is because it is what today's
	// code does, and pinned here so a change to that is a deliberate one: this
	// is the one place the "an outcome must say something the invocation did
	// not" rule above is not honoured.
	if results[1].CallID != "tool_2" || results[1].IsError || results[1].Summary != "git status" {
		t.Errorf("shell result = %+v, want tool_2 succeeding, summarised by its command", results[1])
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
			// completed is the outcome, never a second invocation —
			// normalizing it as a tool_use would double-count every call.
			// With no `result.success` it reports failure with no detail
			// rather than inventing a failure shape nobody has captured.
			name: "tool_call completed is a tool_result, not a tool_use",
			line: `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{}}}`,
			want: agent.EventToolResult,
		},
		{
			name: "tool_call with no tool-shaped key is not a tool_use",
			line: `{"type":"tool_call","subtype":"started","tool_call":{"toolCallId":"x","startedAtMs":"1"}}`,
			want: agent.EventUnknown,
		},
		{
			// A delta on its own emits nothing: it is buffered, and the
			// block surfaces only once `completed` closes it. This is the
			// half of §9.7 that survived — no per-fragment events.
			name: "a thinking delta emits nothing on its own",
			line: `{"type":"thinking","subtype":"delta","text":"pondering"}`,
			want: agent.EventUnknown,
		},
		{
			// A completed with nothing buffered — a transcript opened
			// mid-block, or a run killed and resumed — has no text to emit.
			name: "thinking completed with an empty buffer stays unknown",
			line: `{"type":"thinking","subtype":"completed"}`,
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
			ev := (&stream{}).parse([]byte(tt.line))
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

// TestResultNeedsNoState documents what is still stateless about cursor's
// parser: the terminal result carries the whole result text (unlike codex,
// whose result is the last agent_message it saw), so replaying a transcript
// out of order cannot corrupt it. Thinking is the one thing that does carry
// state, and TestNewLineParserIsolatesThinking covers that.
func TestResultNeedsNoState(t *testing.T) {
	a := New(nil)
	line := []byte(`{"type":"result","subtype":"success","result":"done","usage":{"inputTokens":1,"outputTokens":2}}`)
	first := a.NewLineParser()(line)
	second := a.NewLineParser()(line)
	if first.Result.ResultText != second.Result.ResultText ||
		first.Result.InputTokens != second.Result.InputTokens {
		t.Error("two parser instances disagreed on the same line")
	}
}

// TestNewLineParserIsolatesThinking is why NewLineParser stopped returning a
// shared function: a half-accumulated reasoning block leaking into another
// file would attribute one run's reasoning to a different run.
func TestNewLineParserIsolatesThinking(t *testing.T) {
	a := New(nil)
	first, second := a.NewLineParser(), a.NewLineParser()
	first([]byte(`{"type":"thinking","subtype":"delta","text":"belongs to the first run"}`))

	if ev := second([]byte(`{"type":"thinking","subtype":"completed"}`)); ev.Type != agent.EventUnknown {
		t.Errorf("second parser emitted %q from the first parser's buffer: %q", ev.Type, ev.Text)
	}
	ev := first([]byte(`{"type":"thinking","subtype":"completed"}`))
	if ev.Type != agent.EventThinking || ev.Text != "belongs to the first run" {
		t.Errorf("first parser lost its own block: %q / %q", ev.Type, ev.Text)
	}
	// The buffer is spent, so closing again emits nothing.
	if ev := first([]byte(`{"type":"thinking","subtype":"completed"}`)); ev.Type != agent.EventUnknown {
		t.Errorf("block re-emitted after completion: %q", ev.Text)
	}
}

// TestCoalescedThinkingRawIsTheClosingLine pins the documented exception to
// Event.Raw: a coalesced block's text came from the delta lines, so its Raw
// is the line that closed it. The transcript is written from Raw, and the
// offsets that pair live chunks with fetched scrollback are byte positions
// in that file — the closing line is the one that had just been written.
func TestCoalescedThinkingRawIsTheClosingLine(t *testing.T) {
	parse := (&stream{}).parse
	parse([]byte(`{"type":"thinking","subtype":"delta","text":"reasoning"}`))
	closing := `{"type":"thinking","subtype":"completed","timestamp_ms":1}`
	ev := parse([]byte(closing))
	if ev.Type != agent.EventThinking {
		t.Fatalf("type = %q, want thinking", ev.Type)
	}
	if string(ev.Raw) != closing {
		t.Errorf("Raw = %s, want the closing line verbatim", ev.Raw)
	}
}

// TestNoRunHeaderOrResultMetadata states positively what task 066 did *not*
// do to this adapter. The shared vocabulary grew a run header, a structured
// tool verb and the result's own account of a run; cursor reports no structured
// tool outcome, and the equivalents its dialect *does* carry — `cwd` on the
// init line, `duration_ms`/`duration_api_ms` and the cache token split on the
// result — are deliberately not read: task 066 widened one adapter, and each
// dialect deserves its own fixtures. Every new field therefore stays zero here,
// and nothing emulates a value (§9.7).
func TestNoRunHeaderOrResultMetadata(t *testing.T) {
	for _, name := range []string{"success_2026.08.04.jsonl", "tools_2026.08.11.jsonl"} {
		for i, ev := range parseFixture(t, name) {
			if ev.Type == agent.EventRunHeader || ev.Header != nil {
				t.Errorf("%s line %d: produced a run header", name, i)
			}
			if ev.ParentCallID != "" {
				t.Errorf("%s line %d: parent = %q", name, i, ev.ParentCallID)
			}
			for _, r := range ev.Results {
				if r.Verb != "" || r.Blocked {
					t.Errorf("%s line %d: result = %+v, want no verb and no block", name, i, r)
				}
			}
			if res := ev.Result; res != nil {
				if res.Duration != 0 || res.APIDuration != 0 || res.NumTurns != 0 ||
					res.StopReason != "" || res.TerminalReason != "" ||
					res.CacheReadTokens != 0 || res.CacheCreationTokens != 0 ||
					res.ModelUsage != nil || res.PermissionDenials != nil {
					t.Errorf("%s line %d: result carries claude-only metadata: %+v", name, i, res)
				}
			}
		}
	}
}
