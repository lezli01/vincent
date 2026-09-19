package cursor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// The fixtures are captured from real cursor-agent runs with absolute paths,
// session ids and account identifiers scrubbed, each named for the CLI version
// it came from: success_2026.08.04.jsonl from 2026.08.04-aaa8809,
// tools_2026.08.11.jsonl from 2026.08.11-e8db854, and success_2026.08.25.jsonl
// and tools_2026.08.25.jsonl from 2026.08.25-3e8eec8 (task 108). The last two
// are scrubbed the same way — `session_id` and `conversationId` to SESSION,
// `request_id` and `requestId` to REQ, `model_call_id` to MC, the throwaway
// repo to /tmp/wt — and keep their tool call ids, durations and token counts
// exactly as captured, because the tests below assert those values.
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
	// The three thinking deltas stay unknown — they are swallowed into the
	// buffer and are still unmodeled lines, which is what a reader asking for
	// raw lines should see (§9.7, amended by T4.16). The system/init line is
	// the run header since task 108, and the echoed `user` line an input echo
	// since task 124: it was the fourth unknown line before that.
	if counts[agent.EventUnknown] != 3 {
		t.Errorf("unknown events = %d, want 3 (the thinking deltas)",
			counts[agent.EventUnknown])
	}
	if counts[agent.EventInputEcho] != 1 {
		t.Errorf("input echo events = %d, want 1 (the echoed user line)", counts[agent.EventInputEcho])
	}
	if counts[agent.EventRunHeader] != 1 {
		t.Errorf("run header events = %d, want 1 (the system/init line)", counts[agent.EventRunHeader])
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

// TestParseToolsFixture runs over both tool captures: the 2026.08.25 one is
// what makes that build a tested one (task 108 decision 1), so every arm the
// older capture pins — tool names, subjects, call correlation, outcomes and
// the concatenated result text — has to hold for it too. The builds differ in
// their call ids, which 2026.08.25 reports as two ids joined by a newline.
func TestParseToolsFixture(t *testing.T) {
	for _, tc := range []struct {
		fixture         string
		editID, shellID string
	}{
		{"tools_2026.08.11.jsonl", "tool_1", "tool_2"},
		{
			"tools_2026.08.25.jsonl",
			"call-fc8df3e2-e81f-4ce4-9156-1a6166d8a792-0\nfc_899ae317-65c9-9d65-abdd-e079d14a17e9_0",
			"call-fc8df3e2-e81f-4ce4-9156-1a6166d8a792-1\nfc_899ae317-65c9-9d65-abdd-e079d14a17e9_1",
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			parseToolsFixture(t, tc.fixture, tc.editID, tc.shellID)
		})
	}
}

func parseToolsFixture(t *testing.T, fixture, editID, shellID string) {
	t.Helper()
	events := parseFixture(t, fixture)
	// Both captures think once before the calls and once after them.
	if n := countType(events, agent.EventThinking); n != 2 {
		t.Errorf("thinking events = %d, want 2 coalesced blocks", n)
	}
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
	if uses[0].Summary != "/tmp/wt/hi.txt" || uses[0].CallID != editID {
		t.Errorf("edit call = %+v, want the edited path and %q", uses[0], editID)
	}
	// The shell call carries both `command` and `description`; the command
	// is the subject a reader wants.
	if uses[1].Summary != "git status" || uses[1].CallID != shellID {
		t.Errorf("shell call = %+v, want the command and %q", uses[1], shellID)
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
	if results[0].CallID != editID || results[0].IsError || results[0].Summary != "+1 −0" {
		t.Errorf("edit result = %+v, want %q succeeding with +1 −0", results[0], editID)
	}
	// The shell outcome falls through to ToolSummary, whose first preference is
	// `command` — so it repeats the invocation rather than reporting what came
	// of it. The real payload carries `exitCode`, `stdout` and `stderr`, none of
	// which reach the summary because ToolSummary only considers string fields
	// and `command` wins the order. Asserted as-is because it is what today's
	// code does, and pinned here so a change to that is a deliberate one: this
	// is the one place the "an outcome must say something the invocation did
	// not" rule above is not honoured.
	if results[1].CallID != shellID || results[1].IsError || results[1].Summary != "git status" {
		t.Errorf("shell result = %+v, want %q succeeding, summarised by its command", results[1], shellID)
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

func countType(events []agent.Event, typ agent.EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
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

// allFixtures is every cursor capture, old and new. The run header and the
// result metadata are read off all of them: the 2026.08.04 and 2026.08.11
// captures already carried `cwd`, the durations and the cache split, which
// is why task 108 needed no re-capture of them.
var allFixtures = []string{
	"success_2026.08.04.jsonl",
	"tools_2026.08.11.jsonl",
	"resume_2026.08.11.jsonl",
	"resume_unknown_2026.08.11.jsonl",
	"success_2026.08.25.jsonl",
	"tools_2026.08.25.jsonl",
}

// TestRunHeaderAndResultMetadata is task 108: cursor fills its share of the
// vocabulary task 066 added. The init line's `cwd` becomes the run header —
// with no tool list, because cursor's init line has none and nothing builds
// one from the calls seen later — and the result line's durations and cache
// counts are copied as captured, with no arithmetic (§9.7).
func TestRunHeaderAndResultMetadata(t *testing.T) {
	want := map[string]struct {
		workDir             string
		duration, api       time.Duration
		cacheRead, cacheCre int64
		inputTokens         int64
	}{
		"success_2026.08.04.jsonl":        {"/tmp/wt", 2002 * time.Millisecond, 2002 * time.Millisecond, 8000, 0, 8274},
		"tools_2026.08.11.jsonl":          {"/tmp/wt", 16902 * time.Millisecond, 16902 * time.Millisecond, 17088, 0, 17161},
		"resume_2026.08.11.jsonl":         {"/tmp/worktree", 4741 * time.Millisecond, 4741 * time.Millisecond, 35318, 0, 92},
		"resume_unknown_2026.08.11.jsonl": {"/tmp/worktree", 2667 * time.Millisecond, 2667 * time.Millisecond, 5746, 0, 10946},
		"success_2026.08.25.jsonl":        {"/tmp/wt", 13153 * time.Millisecond, 13153 * time.Millisecond, 3840, 0, 15149},
		"tools_2026.08.25.jsonl":          {"/tmp/wt", 15079 * time.Millisecond, 15079 * time.Millisecond, 24192, 0, 14033},
	}
	for _, name := range allFixtures {
		t.Run(name, func(t *testing.T) {
			w, ok := want[name]
			if !ok {
				t.Fatalf("no expectation for fixture %s", name)
			}
			events := parseFixture(t, name)
			var headers []*agent.RunHeader
			var res *agent.RunResult
			for i, ev := range events {
				if ev.Type == agent.EventRunHeader {
					if ev.Header == nil {
						t.Fatalf("line %d: run header event carries no header", i)
					}
					headers = append(headers, ev.Header)
				}
				if ev.Type == agent.EventResult {
					res = ev.Result
				}
			}
			if len(headers) != 1 {
				t.Fatalf("run headers = %d, want exactly 1", len(headers))
			}
			if events[0].Type != agent.EventRunHeader {
				t.Errorf("first event = %q, want the run header", events[0].Type)
			}
			if headers[0].WorkDir != w.workDir {
				t.Errorf("WorkDir = %q, want %q", headers[0].WorkDir, w.workDir)
			}
			if headers[0].Tools != nil {
				t.Errorf("Tools = %v, want nil: cursor's init line lists none", headers[0].Tools)
			}
			if res == nil {
				t.Fatal("no result event")
			}
			if res.Duration != w.duration || res.APIDuration != w.api {
				t.Errorf("durations = %v (%v api), want %v (%v api)",
					res.Duration, res.APIDuration, w.duration, w.api)
			}
			if res.CacheReadTokens != w.cacheRead || res.CacheCreationTokens != w.cacheCre {
				t.Errorf("cache = %d read / %d written, want %d / %d",
					res.CacheReadTokens, res.CacheCreationTokens, w.cacheRead, w.cacheCre)
			}
			// Cache traffic is never folded into the plain count (decision 7).
			if res.InputTokens != w.inputTokens {
				t.Errorf("InputTokens = %d, want %d as reported", res.InputTokens, w.inputTokens)
			}
		})
	}
}

// TestRunHeaderAndResultMetadataLines covers the lines no capture has: a
// system subtype other than init, and an error result, which carries its
// timing and cache counts the way a success does (task 108 decision 8).
func TestRunHeaderAndResultMetadataLines(t *testing.T) {
	other := `{"type":"system","subtype":"status","cwd":"/tmp/wt"}`
	if ev := (&stream{}).parse([]byte(other)); ev.Type != agent.EventUnknown ||
		ev.Header != nil || string(ev.Raw) != other {
		t.Errorf("system/status = %+v, want unknown with its raw line intact", ev)
	}

	errLine := `{"type":"result","subtype":"error","is_error":true,"result":"boom",` +
		`"duration_ms":1200,"duration_api_ms":900,` +
		`"usage":{"inputTokens":5,"outputTokens":6,"cacheReadTokens":7,"cacheWriteTokens":8}}`
	ev := (&stream{}).parse([]byte(errLine))
	if ev.Type != agent.EventResult || ev.Result == nil || !ev.Result.IsError {
		t.Fatalf("error result = %+v, want an error RunResult", ev)
	}
	res := ev.Result
	if res.Duration != 1200*time.Millisecond || res.APIDuration != 900*time.Millisecond {
		t.Errorf("durations = %v (%v api), want 1.2s (900ms api)", res.Duration, res.APIDuration)
	}
	if res.CacheReadTokens != 7 || res.CacheCreationTokens != 8 {
		t.Errorf("cache = %d / %d, want 7 / 8", res.CacheReadTokens, res.CacheCreationTokens)
	}
}

// TestUnreportedMetadataStaysZero states positively what cursor still does
// not report. Task 108 taught this adapter the run header and the result's
// durations and cache counts; turns, stop and terminal reasons, per-model
// usage, permission denials, a structured tool verb or block, and subagent
// attribution have no equivalent on any cursor line, so they stay zero
// whatever the fixture — nothing emulates a value (§9.7).
func TestUnreportedMetadataStaysZero(t *testing.T) {
	for _, name := range allFixtures {
		for i, ev := range parseFixture(t, name) {
			if ev.ParentCallID != "" {
				t.Errorf("%s line %d: parent = %q", name, i, ev.ParentCallID)
			}
			for _, r := range ev.Results {
				if r.Verb != "" || r.Blocked {
					t.Errorf("%s line %d: result = %+v, want no verb and no block", name, i, r)
				}
			}
			if res := ev.Result; res != nil {
				if res.NumTurns != 0 || res.StopReason != "" || res.TerminalReason != "" ||
					res.ModelUsage != nil || res.PermissionDenials != nil ||
					res.ReasoningOutputTokens != 0 {
					t.Errorf("%s line %d: result carries metadata cursor does not report: %+v", name, i, res)
				}
			}
		}
	}
}
