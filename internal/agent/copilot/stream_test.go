package copilot

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// splitLines returns a fixture's non-blank lines.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) != 0 {
			out = append(out, line)
		}
	}
	return out
}

// parseFixture runs one fixture through a fresh parser, holding it to
// losslessness on the way: one event per line, each carrying its line.
func parseFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	parse := New(nil).NewLineParser()
	var events []agent.Event
	for _, line := range splitLines(b) {
		ev := parse(line)
		if !bytes.Equal(ev.Raw, line) {
			t.Fatalf("%s: event %q does not carry its line verbatim", name, ev.Type)
		}
		events = append(events, ev)
	}
	return events
}

// resultOf is the fixture's terminal result.
func resultOf(t *testing.T, events []agent.Event) *agent.RunResult {
	t.Helper()
	last := events[len(events)-1]
	if last.Type != agent.EventResult || last.Result == nil {
		t.Fatalf("last event = %q, want the result", last.Type)
	}
	return last.Result
}

// TestSimpleFixture: the answer is the last assistant message, since the
// result line carries none, and the session id is the result line's.
func TestSimpleFixture(t *testing.T) {
	events := parseFixture(t, "simple_1.0.105.jsonl")
	var outputs []string
	for _, ev := range events {
		if ev.Type == agent.EventOutput {
			outputs = append(outputs, ev.Text)
		}
	}
	if strings.Join(outputs, "|") != "pong" {
		t.Errorf("outputs = %q, want the one coalesced message", outputs)
	}
	res := resultOf(t, events)
	if res.IsError || res.ResultText != "pong" {
		t.Errorf("result = %+v, want a clean pong", res)
	}
	if res.SessionID != "fd834e22-e2d2-4301-85ba-785576a1f0a1" {
		t.Errorf("SessionID = %q, want the result line's", res.SessionID)
	}
	if res.CostUSD != nil || res.NumTurns != 0 {
		t.Errorf("CostUSD=%v NumTurns=%d; premium requests are never cost, and turns are unreported", res.CostUSD, res.NumTurns)
	}
}

// TestShellFixture: a tool call becomes a tool_use summarized by its
// arguments, and its completion a tool_result under the same call id.
func TestShellFixture(t *testing.T) {
	var use agent.ToolUse
	var res agent.ToolResult
	for _, ev := range parseFixture(t, "shell_1.0.105.jsonl") {
		switch ev.Type {
		case agent.EventToolUse:
			use = ev.Tools[0]
		case agent.EventToolResult:
			res = ev.Results[0]
		}
	}
	if use.Name != "bash" || use.CallID == "" || use.Summary == "" {
		t.Errorf("tool_use = %+v, want bash with a call id and a summary", use)
	}
	if res.CallID != use.CallID || res.Name != "bash" || res.IsError {
		t.Errorf("tool_result = %+v, want bash's success under %q", res, use.CallID)
	}
	if strings.Contains(res.Summary, "\n") || res.Summary == "" {
		t.Errorf("tool_result summary = %q, want one line", res.Summary)
	}
}

// TestMCPFixture: copilot names a vincent MCP call `<server>-<tool>` and
// reports the split beside it; the split survives normalization, because a
// dash inside either name would make the joined name ambiguous to cut back
// (task 138.8). Built-in tools carry no split.
func TestMCPFixture(t *testing.T) {
	var use agent.ToolUse
	var res agent.ToolResult
	for _, ev := range parseFixture(t, "mcp_1.0.105.jsonl") {
		switch ev.Type {
		case agent.EventToolUse:
			use = ev.Tools[0]
		case agent.EventToolResult:
			res = ev.Results[0]
		}
	}
	want := agent.ToolUse{
		Name: "vincent-health", CallID: "call_PlL0Aqs0CwxvIqyowkCvlQ3a",
		MCPServer: "vincent", MCPTool: "health",
	}
	if use != want {
		t.Errorf("tool_use = %+v, want %+v", use, want)
	}
	if res.CallID != use.CallID || res.Name != "vincent-health" || res.IsError {
		t.Errorf("tool_result = %+v, want vincent-health's success under %q", res, use.CallID)
	}
	for _, ev := range parseFixture(t, "shell_1.0.105.jsonl") {
		if ev.Type == agent.EventToolUse && (ev.Tools[0].MCPServer != "" || ev.Tools[0].MCPTool != "") {
			t.Errorf("built-in tool_use = %+v, want no MCP split", ev.Tools[0])
		}
	}
}

// TestDeniedFixture: a denied call is a blocked tool result, never a run
// failure — the model answered around it and the run exited 0.
func TestDeniedFixture(t *testing.T) {
	events := parseFixture(t, "denied_1.0.105.jsonl")
	var res agent.ToolResult
	for _, ev := range events {
		if ev.Type == agent.EventToolResult {
			res = ev.Results[0]
		}
		if ev.Type == agent.EventError {
			t.Errorf("error event %s; a denied tool is not a run failure", ev.Raw)
		}
	}
	if !res.IsError || !res.Blocked || res.Name != "bash" {
		t.Errorf("tool_result = %+v, want a blocked bash failure", res)
	}
	if r := resultOf(t, events); r.IsError {
		t.Errorf("result = %+v, want success", r)
	}
}

// TestEditFixture: apply_patch's arguments are a bare string, which must
// neither fail the line nor invent a summary.
func TestEditFixture(t *testing.T) {
	for _, ev := range parseFixture(t, "edit_1.0.105.jsonl") {
		if ev.Type == agent.EventToolUse && ev.Tools[0].Name == "" {
			t.Errorf("tool_use without a name: %+v", ev.Tools[0])
		}
		if ev.Type == agent.EventToolResult && ev.Results[0].IsError {
			t.Errorf("edit failed: %+v", ev.Results[0])
		}
	}
}

// TestSigtermFixture: 1.0.105 ends a SIGTERM'd run `abort`, then
// `result{exitCode:0}`. That must not read as success.
func TestSigtermFixture(t *testing.T) {
	res := resultOf(t, parseFixture(t, "sigterm_1.0.105.jsonl"))
	if res.ExitCode != 0 {
		t.Fatalf("fixture exitCode = %d; the case under test is a zero one", res.ExitCode)
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "user_initiated") {
		t.Errorf("result = %+v, want an error naming the abort", res)
	}
}

// TestSessionErrorAndUnknownLines covers the shapes no capture has: a
// session.error (the package schema's, as the fake emits it), a result
// after one, an unrecognized type and a line that is not JSON.
func TestSessionErrorAndUnknownLines(t *testing.T) {
	parse := New(nil).NewLineParser()
	tests := []struct {
		line string
		want agent.EventType
	}{
		{`{"type":"something.new","data":{"x":1},"id":"a"}`, agent.EventUnknown},
		{`not json`, agent.EventUnknown},
		{`{"type":"session.error","data":{"errorType":"model_error","message":"it broke"},"id":"b"}`, agent.EventError},
		{`{"type":"result","sessionId":"s","exitCode":1}`, agent.EventResult},
	}
	var last agent.Event
	for _, tt := range tests {
		last = parse([]byte(tt.line))
		if last.Type != tt.want {
			t.Errorf("%s → %q, want %q", tt.line, last.Type, tt.want)
		}
	}
	if !last.Result.IsError || last.Result.ErrorMessage != "it broke" || last.Result.ExitCode != 1 {
		t.Errorf("result = %+v, want the session.error's message", last.Result)
	}
}

// TestExitCodeDecides: a session.error does not fail a run whose result
// reports exit 0 — a rate-limited turn may switch models and finish — and a
// subagent's session.error or abort (lines carrying agentId) never decides
// the parent run's outcome.
func TestExitCodeDecides(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  bool // IsError
	}{
		{"root session.error, exit 0", []string{
			`{"type":"session.error","data":{"errorType":"rate_limit","message":"slow down","eligibleForAutoSwitch":true}}`,
			`{"type":"result","sessionId":"s","exitCode":0}`,
		}, false},
		{"subagent session.error, exit 0", []string{
			`{"type":"session.error","data":{"message":"sub broke"},"agentId":"a1"}`,
			`{"type":"result","sessionId":"s","exitCode":0}`,
		}, false},
		{"subagent abort, exit 0", []string{
			`{"type":"abort","data":{"reason":"user_initiated"},"agentId":"a1"}`,
			`{"type":"result","sessionId":"s","exitCode":0}`,
		}, false},
		{"root abort, exit 0", []string{
			`{"type":"abort","data":{"reason":"user_initiated"}}`,
			`{"type":"result","sessionId":"s","exitCode":0}`,
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parse := New(nil).NewLineParser()
			var last agent.Event
			for _, l := range tt.lines {
				last = parse([]byte(l))
			}
			if last.Type != agent.EventResult || last.Result.IsError != tt.want {
				t.Errorf("result = %+v, want IsError=%v", last.Result, tt.want)
			}
		})
	}
}

// TestSubagentErrorDoesNotExplainTheExit: a nonzero exit takes the root
// agent's message, never a subagent's.
func TestSubagentErrorDoesNotExplainTheExit(t *testing.T) {
	parse := New(nil).NewLineParser()
	if ev := parse([]byte(`{"type":"session.error","data":{"message":"sub broke"},"agentId":"a1"}`)); ev.Type != agent.EventError || ev.Text != "sub broke" {
		t.Errorf("subagent session.error → %q %q, want error event", ev.Type, ev.Text)
	}
	res := parse([]byte(`{"type":"result","sessionId":"s","exitCode":2}`)).Result
	if !res.IsError || res.ErrorMessage != "copilot run failed (exit 2)" {
		t.Errorf("result = %+v, want the generic exit message", res)
	}
}

// TestDeltasCoalesce: a closing message without content takes the deltas'
// text, and deltas alone emit nothing.
func TestDeltasCoalesce(t *testing.T) {
	parse := New(nil).NewLineParser()
	for _, d := range []string{"po", "ng"} {
		if ev := parse([]byte(`{"type":"assistant.message_delta","data":{"messageId":"m","deltaContent":"` + d + `"}}`)); ev.Type != agent.EventUnknown {
			t.Errorf("delta → %q, want unknown", ev.Type)
		}
	}
	ev := parse([]byte(`{"type":"assistant.message","data":{"messageId":"m","content":""}}`))
	if ev.Type != agent.EventOutput || ev.Text != "pong" {
		t.Errorf("message → %q %q, want output pong", ev.Type, ev.Text)
	}
}

// usageOf is a result's account of itself, the fields task 138.7 fills.
type usageOf struct {
	In, Out, CacheRead, CacheWrite, Reasoning int64
	Models                                    []agent.ModelUsage
	Duration, APIDuration                     time.Duration
}

func usage(r *agent.RunResult) usageOf {
	return usageOf{
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
		r.ReasoningOutputTokens, r.ModelUsage, r.Duration, r.APIDuration,
	}
}

// TestUsageFromTheCheckpoint: tokens are the root usage checkpoint's
// tokenDetails — `input` the uncached share, as claude's InputTokens is
// (§9.2) — reasoning is modelMetrics' reasoningTokens summed, and the
// durations are the result line's. Every figure is the session's: the
// subagent fixture's include the subagent's spend, and the resume fixture's
// include the first run's (totalUserRequests 2; task 138.11 decides what a
// resumed run reports). Every completed capture carries one checkpoint.
func TestUsageFromTheCheckpoint(t *testing.T) {
	tests := []struct {
		fixture string
		want    usageOf
	}{
		{"simple_1.0.105.jsonl", usageOf{
			In: 11885, Out: 5, CacheRead: 1152,
			Models: []agent.ModelUsage{{
				Model: "mai-code-1.1-flash", InputTokens: 11885, OutputTokens: 5, CacheReadTokens: 1152,
			}},
			Duration: 4945 * time.Millisecond, APIDuration: 2116 * time.Millisecond,
		}},
		{"subagent_1.0.105.jsonl", usageOf{
			In: 15, Out: 323, CacheRead: 41889, CacheWrite: 8959, Reasoning: 60,
			Models: []agent.ModelUsage{{
				Model: "gpt-6-luna", InputTokens: 15, OutputTokens: 323,
				CacheReadTokens: 41889, CacheCreationTokens: 8959,
			}},
			Duration: 12491 * time.Millisecond, APIDuration: 8412 * time.Millisecond,
		}},
		{"resume_1.0.105.jsonl", usageOf{
			In: 13105, Out: 46, CacheRead: 13056,
			Models: []agent.ModelUsage{{
				Model: "mai-code-1.1-flash", InputTokens: 13105, OutputTokens: 46, CacheReadTokens: 13056,
			}},
			Duration: 12627 * time.Millisecond, APIDuration: 4529 * time.Millisecond,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			events := parseFixture(t, tt.fixture)
			if got := usage(resultOf(t, events)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestUsageCheckpointShapes: the last root checkpoint wins, one carrying an
// agentId is a sub-run's and never the run's, and per-model rows come out in
// model order with no cost.
func TestUsageCheckpointShapes(t *testing.T) {
	parse := New(nil).NewLineParser()
	cp := func(agentID string, in int, models string) []byte {
		line := `{"type":"session.usage_checkpoint","data":{"accountingSnapshot":{"tokenDetails":{"input":{"tokenCount":` +
			strconv.Itoa(in) + `},"output":{"tokenCount":1}},"modelMetrics":{` + models + `}}}`
		if agentID != "" {
			line += `,"agentId":"` + agentID + `"`
		}
		return []byte(line + "}")
	}
	model := func(name string, reasoning int) string {
		return `"` + name + `":{"usage":{"reasoningTokens":` + strconv.Itoa(reasoning) +
			`},"tokenDetails":{"input":{"tokenCount":2},"output":{"tokenCount":3}}}`
	}
	if ev := parse(cp("", 10, model("a", 1))); ev.Type != agent.EventUsage {
		t.Errorf("root checkpoint → %q, want usage", ev.Type)
	}
	if ev := parse(cp("", 20, model("b", 4)+","+model("a", 5))); ev.Type != agent.EventUsage {
		t.Errorf("second root checkpoint → %q, want usage", ev.Type)
	}
	if ev := parse(cp("sub", 99, model("c", 7))); ev.Type != agent.EventUnknown {
		t.Errorf("subagent checkpoint → %q, want unknown", ev.Type)
	}
	if ev := parse([]byte(`{"type":"session.usage_checkpoint","data":{"totalNanoAiu":1}}`)); ev.Type != agent.EventUnknown {
		t.Errorf("checkpoint without a snapshot → %q, want unknown", ev.Type)
	}
	res := parse([]byte(`{"type":"result","sessionId":"s","exitCode":0}`)).Result
	if res.InputTokens != 20 || res.ReasoningOutputTokens != 9 {
		t.Errorf("tokens = %d in, %d reasoning; want the last root checkpoint's 20 and 9", res.InputTokens, res.ReasoningOutputTokens)
	}
	if len(res.ModelUsage) != 2 || res.ModelUsage[0].Model != "a" || res.ModelUsage[1].Model != "b" {
		t.Errorf("ModelUsage = %+v, want a then b", res.ModelUsage)
	}
	for _, m := range res.ModelUsage {
		if m.CostUSD != nil || m.InputTokens != 2 || m.OutputTokens != 3 {
			t.Errorf("model row = %+v, want 2 in, 3 out, no cost", m)
		}
	}
	if res.Duration != 0 || res.APIDuration != 0 {
		t.Errorf("durations = %v/%v from a result without usage, want 0", res.Duration, res.APIDuration)
	}
}

// TestThinkingIsTheWholeBlock: assistant.reasoning's content is one thinking
// event; its deltas stay unknown and are never coalesced, because the closing
// line already carries the block.
func TestThinkingIsTheWholeBlock(t *testing.T) {
	for _, name := range []string{"reasoning_1.0.105.jsonl", "denied_1.0.105.jsonl", "subagent_1.0.105.jsonl"} {
		var thinking []string
		for _, ev := range parseFixture(t, name) {
			switch rawType(t, ev) {
			case "assistant.reasoning_delta":
				if ev.Type != agent.EventUnknown {
					t.Errorf("%s: reasoning delta → %q, want unknown", name, ev.Type)
				}
			case "assistant.reasoning":
				if ev.Type == agent.EventThinking {
					thinking = append(thinking, ev.Text)
				}
			}
		}
		if len(thinking) != 1 || !strings.HasPrefix(thinking[0], "**") {
			t.Errorf("%s: thinking = %q, want the one whole block", name, thinking)
		}
	}
}
