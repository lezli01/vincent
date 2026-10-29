package copilot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// rawTypes are every `type` the 1.0.105 captures carry, each with the
// normalized types its lines may become. assistant.message is the one with
// two: a message that only requests tools has no text.
var rawTypes = map[string][]agent.EventType{
	"assistant.message":       {agent.EventOutput, agent.EventUnknown},
	"tool.execution_start":    {agent.EventToolUse},
	"tool.execution_complete": {agent.EventToolResult},
	"session.error":           {agent.EventError},
	"result":                  {agent.EventResult},
	// Everything else is unknown in 138.5. Reasoning, subagents and usage
	// are task 138.7's; the deltas are coalesced into assistant.message.
	"abort": {agent.EventUnknown}, "agent.interrupted": {agent.EventUnknown},
	"assistant.idle": {agent.EventUnknown}, "assistant.message_delta": {agent.EventUnknown},
	"assistant.message_start": {agent.EventUnknown}, "assistant.reasoning": {agent.EventUnknown},
	"assistant.reasoning_delta": {agent.EventUnknown}, "assistant.tool_call_delta": {agent.EventUnknown},
	"assistant.turn_end": {agent.EventUnknown}, "assistant.turn_start": {agent.EventUnknown},
	"model.call_final_result": {agent.EventUnknown}, "model.call_finished": {agent.EventUnknown},
	"model.call_start": {agent.EventUnknown}, "prompt_cache_break": {agent.EventUnknown},
	"session.auto_mode_resolved": {agent.EventUnknown}, "session.background_tasks_changed": {agent.EventUnknown},
	"session.indexed_search": {agent.EventUnknown}, "session.info": {agent.EventUnknown},
	"session.mcp_server_status_changed": {agent.EventUnknown}, "session.mcp_server_tools_loaded": {agent.EventUnknown},
	"session.mcp_servers_loaded": {agent.EventUnknown}, "session.tools_updated": {agent.EventUnknown},
	"session.usage_checkpoint": {agent.EventUnknown}, "subagent.completed": {agent.EventUnknown},
	"subagent.configured": {agent.EventUnknown}, "subagent.selected": {agent.EventUnknown},
	"subagent.started": {agent.EventUnknown}, "tool.execution_partial_result": {agent.EventUnknown},
	"tool.shell_output": {agent.EventUnknown}, "user.message": {agent.EventUnknown},
}

// TestFixtureEventTypesArePinned holds every 1.0.105 capture to the events it
// normalizes to: the count of each modeled type per fixture, every raw type
// to its pinned normalization, and no usage event anywhere (task 138.7's).
// A fixture or a raw type nobody pinned fails the test.
func TestFixtureEventTypesArePinned(t *testing.T) {
	type counts map[agent.EventType]int
	want := map[string]counts{
		"denied_1.0.105.jsonl":                {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"edit_1.0.105.jsonl":                  {"output": 1, "tool_use": 2, "tool_result": 2, "result": 1},
		"mcp_1.0.105.jsonl":                   {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"reasoning_1.0.105.jsonl":             {"output": 1, "result": 1},
		"restricted_1.0.105.jsonl":            {"output": 1, "tool_use": 9, "tool_result": 9, "result": 1},
		"resume_1.0.105.jsonl":                {"output": 1, "result": 1},
		"session_id_1.0.105.jsonl":            {"output": 1, "result": 1},
		"session_id_unknown_1.0.105.jsonl":    {"output": 1, "result": 1},
		"shell_1.0.105.jsonl":                 {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"sigterm_1.0.105.jsonl":               {"tool_use": 2, "tool_result": 1, "result": 1},
		"simple_1.0.105.jsonl":                {"output": 1, "result": 1},
		"skill_1.0.105.jsonl":                 {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"skill_store_1.0.105.jsonl":           {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"skill_store_linked_1.0.105.jsonl":    {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"skill_store_relocated_1.0.105.jsonl": {"output": 1, "tool_use": 1, "tool_result": 1, "result": 1},
		"subagent_1.0.105.jsonl":              {"output": 2, "tool_use": 3, "tool_result": 3, "result": 1},
		"unavailable_model_1.0.105.jsonl":     {},
		"unsupported_effort_1.0.105.jsonl":    {},
	}
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		exp, ok := want[name]
		if !ok {
			t.Errorf("%s: no pinned event types; add the capture here", name)
			continue
		}
		got := counts{}
		for _, ev := range parseFixture(t, name) {
			var line struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(ev.Raw, &line); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			allowed, ok := rawTypes[line.Type]
			if !ok {
				t.Errorf("%s: raw type %q is not pinned", name, line.Type)
			} else if !containsType(allowed, ev.Type) {
				t.Errorf("%s: %q normalized to %q, want one of %v", name, line.Type, ev.Type, allowed)
			}
			if ev.Type == agent.EventUsage {
				t.Errorf("%s: usage event; usage is task 138.7's", name)
			}
			if ev.Type != agent.EventUnknown {
				got[ev.Type]++
			}
		}
		if !equalCounts(got, exp) {
			t.Errorf("%s: modeled events %v, want %v", name, got, exp)
		}
	}
}

func containsType(ts []agent.EventType, t agent.EventType) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

func equalCounts(a, b map[agent.EventType]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
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
	if res.InputTokens != 0 || res.OutputTokens != 0 || res.CostUSD != nil {
		t.Errorf("usage reported: %+v; premium requests are never cost, and tokens are 138.7's", res)
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
