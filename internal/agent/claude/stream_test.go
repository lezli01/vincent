package claude

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want func(t *testing.T, ev agent.Event)
	}{
		{
			name: "assistant text becomes output",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventOutput || ev.Text != "hello" {
					t.Errorf("got %q/%q, want output/hello", ev.Type, ev.Text)
				}
			},
		},
		{
			name: "assistant tool_use becomes tool_use",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{}}]}}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventToolUse || len(ev.Tools) != 1 || ev.Tools[0].Name != "Edit" {
					t.Errorf("got %q tools=%v, want tool_use Edit", ev.Type, ev.Tools)
				}
			},
		},
		{
			// Verbatim from testdata/stream_permission_allow_2.1.226.jsonl:
			// the subject and the id claude actually sends (T4.14). `content`
			// is deliberately not the summary — the preference list picks the
			// file being written, not the bytes going into it.
			name: "tool_use carries its subject and id",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use",` +
				`"id":"toolu_01PSqBeA6sKydYaELf8NTXHH","name":"Write",` +
				`"input":{"file_path":"C:\\work\\repo\\hello.txt","content":"hi"},` +
				`"caller":{"type":"direct"}}]}}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventToolUse || len(ev.Tools) != 1 {
					t.Fatalf("got %q tools=%v, want one tool_use", ev.Type, ev.Tools)
				}
				tu := ev.Tools[0]
				if tu.Name != "Write" || tu.Summary != `C:\work\repo\hello.txt` ||
					tu.CallID != "toolu_01PSqBeA6sKydYaELf8NTXHH" {
					t.Errorf("tool = %+v, want Write on the written path with its id", tu)
				}
			},
		},
		{
			name: "mixed content is output with tools attached",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"editing"},{"type":"tool_use","name":"Write"}]}}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventOutput || len(ev.Tools) != 1 {
					t.Errorf("got %q tools=%v, want output with 1 tool", ev.Type, ev.Tools)
				}
			},
		},
		{
			name: "success result with usage and cost",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"all done","total_cost_usd":1.5,"usage":{"input_tokens":10,"output_tokens":20}}`,
			want: func(t *testing.T, ev agent.Event) {
				r := ev.Result
				if ev.Type != agent.EventResult || r == nil {
					t.Fatalf("got %q, want result", ev.Type)
				}
				if r.IsError || r.ResultText != "all done" || r.InputTokens != 10 ||
					r.OutputTokens != 20 || r.CostUSD == nil || *r.CostUSD != 1.5 {
					t.Errorf("result = %+v", r)
				}
			},
		},
		{
			name: "error result",
			line: `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"ran out"}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Result == nil || !ev.Result.IsError || ev.Result.ErrorMessage != "ran out" {
					t.Errorf("result = %+v, want error 'ran out'", ev.Result)
				}
			},
		},
		{
			name: "error subtype without is_error flag still errors",
			line: `{"type":"result","subtype":"error_during_execution","result":""}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Result == nil || !ev.Result.IsError || ev.Result.ErrorMessage != "error_during_execution" {
					t.Errorf("result = %+v, want subtype as error message", ev.Result)
				}
			},
		},
		{
			name: "result without cost keeps CostUSD nil",
			line: `{"type":"result","subtype":"success","result":"ok"}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Result == nil || ev.Result.CostUSD != nil {
					t.Errorf("CostUSD = %v, want nil when unreported", ev.Result.CostUSD)
				}
			},
		},
		{
			// A `system` line is only normalized for the one subtype vincent
			// models. Anything else — compact boundaries among them — stays
			// raw, which is the phase 1 tolerant-parsing rule asserted
			// rather than assumed (task 066).
			name: "unknown system subtype is tolerated",
			line: `{"type":"system","subtype":"compact_boundary","model":"x"}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventUnknown {
					t.Errorf("got %q, want unknown", ev.Type)
				}
			},
		},
		{
			name: "unmodelled type is tolerated",
			line: `{"type":"control_response","request_id":"r1"}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventUnknown {
					t.Errorf("got %q, want unknown", ev.Type)
				}
			},
		},
		{
			name: "system init becomes the run header",
			line: `{"type":"system","subtype":"init","cwd":"C:\\work\\repo",` +
				`"tools":["Task","Bash"]}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventRunHeader || ev.Header == nil {
					t.Fatalf("got %q header=%v, want a run header", ev.Type, ev.Header)
				}
				if ev.Header.WorkDir != `C:\work\repo` {
					t.Errorf("cwd = %q", ev.Header.WorkDir)
				}
				if len(ev.Header.Tools) != 2 || ev.Header.Tools[0] != "Task" {
					t.Errorf("tools = %v, want the list claude was given", ev.Header.Tools)
				}
			},
		},
		{
			name: "non-JSON line is tolerated",
			line: `this is not json`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventUnknown {
					t.Errorf("got %q, want unknown", ev.Type)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := parseLine([]byte(tt.line))
			if string(ev.Raw) != tt.line {
				t.Errorf("Raw = %q, want the verbatim line", ev.Raw)
			}
			tt.want(t, ev)
		})
	}
}

// TestParseThinkingAndToolResultsFixture runs a captured permission-mode run
// and asserts on the two shapes the parser ignored through M4 (T4.16). Both
// were in this fixture the whole time.
func TestParseThinkingAndToolResultsFixture(t *testing.T) {
	events, _ := fixtureEvents(t, "stream_permission_allow_2.1.226.jsonl")

	var thinking []agent.Event
	var results []agent.ToolResult
	for _, ev := range events {
		switch ev.Type {
		case agent.EventThinking:
			thinking = append(thinking, ev)
		case agent.EventToolResult:
			results = append(results, ev.Results...)
		}
	}

	if len(thinking) == 0 {
		t.Fatal("no thinking events: claude's reasoning blocks are in this capture and were dropped")
	}
	for _, ev := range thinking {
		if ev.Text == "" {
			t.Error("thinking event with no text")
		}
		// The block also carries `signature`, an opaque attestation blob.
		// It is not text and must never reach a pane.
		if strings.Contains(ev.Text, "signature") || strings.Contains(ev.Text, "EucCCpMB") {
			t.Errorf("thinking text carries the signature blob: %q", ev.Text)
		}
	}

	if len(results) == 0 {
		t.Fatal("no tool results: claude replays them on `user` lines and they were dropped")
	}
	// The captured Write reports where it wrote, and correlates back to the
	// tool_use block by id rather than by position.
	var found bool
	for _, r := range results {
		if r.CallID != "toolu_01PSqBeA6sKydYaELf8NTXHH" {
			continue
		}
		found = true
		if r.IsError {
			t.Errorf("Write result marked an error: %+v", r)
		}
		if !strings.Contains(r.Summary, "File created successfully") {
			t.Errorf("summary = %q, want the tool's reported outcome", r.Summary)
		}
	}
	if !found {
		t.Errorf("no result correlated to the Write call; got %+v", results)
	}
}

// TestToolResultSummaryShapes covers claude sending a tool_result's content
// as either a bare string or an array of blocks, which varies by tool.
func TestToolResultSummaryShapes(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "string content",
			line: `{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"tool_use_id":"t1","content":"wrote 3 lines"}]}}`,
			want: "wrote 3 lines",
		},
		{
			name: "block array content",
			line: `{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"tool_use_id":"t1","content":[{"type":"text","text":"first"},` +
				`{"type":"text","text":"second"}]}]}}`,
			want: "first second",
		},
		{
			// Neither shape: no guess, and the outcome still renders from
			// its error flag alone.
			name: "unrecognized content",
			line: `{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"tool_use_id":"t1","content":{"weird":true}}]}}`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := parseLine([]byte(tc.line))
			if ev.Type != agent.EventToolResult || len(ev.Results) != 1 {
				t.Fatalf("got %q with %d results, want one tool_result", ev.Type, len(ev.Results))
			}
			if ev.Results[0].Summary != tc.want {
				t.Errorf("summary = %q, want %q", ev.Results[0].Summary, tc.want)
			}
		})
	}
}

// TestUserLineWithoutToolResultsStaysUnknown guards the other `user` line
// claude sends — the replayed prompt — from becoming an empty result event.
func TestUserLineWithoutToolResultsStaysUnknown(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"text","text":"do the thing"}]}}`
	if ev := parseLine([]byte(line)); ev.Type != agent.EventUnknown {
		t.Errorf("type = %q, want unknown for a user line carrying no tool results", ev.Type)
	}
}

// TestErrorToolResultIsFlagged pins that a denied or failed tool surfaces as
// an error rather than a quiet outcome — the §7.4 deny path produces exactly
// this line.
func TestErrorToolResultIsFlagged(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result",` +
		`"content":"no user is available; permission denied","is_error":true,` +
		`"tool_use_id":"toolu_01NfZSDqXQKXwt59MWGhoqgw"}]}}`
	ev := parseLine([]byte(line))
	if ev.Type != agent.EventToolResult || len(ev.Results) != 1 {
		t.Fatalf("got %q, want a tool_result", ev.Type)
	}
	if !ev.Results[0].IsError {
		t.Errorf("result = %+v, want IsError", ev.Results[0])
	}
}

// TestParseResultMetadataFixture reads the result line every captured run
// ends with. Each field was in the stream the whole time and was discarded
// (task 066): a reader could not see how long a run took, how many turns it
// burned, why it stopped, or how much of the spend was cache reads.
func TestParseResultMetadataFixture(t *testing.T) {
	events, _ := fixtureEvents(t, "stream_permission_deny_2.1.226.jsonl")
	res := lastResult(t, events)

	if res.Duration != 7324*time.Millisecond {
		t.Errorf("duration = %s, want the run's own 7324ms", res.Duration)
	}
	if res.APIDuration != 5706*time.Millisecond {
		t.Errorf("api duration = %s, want 5706ms", res.APIDuration)
	}
	if res.NumTurns != 2 {
		t.Errorf("turns = %d, want 2", res.NumTurns)
	}
	if res.StopReason != "end_turn" || res.TerminalReason != "completed" {
		t.Errorf("stop/terminal = %q/%q, want end_turn/completed",
			res.StopReason, res.TerminalReason)
	}
	// The cache counts are not part of input_tokens: this run reported 18
	// input tokens against 60280 cache reads, and reporting only the former
	// makes a real spend look like nothing happened.
	if res.InputTokens != 18 || res.CacheReadTokens != 60280 ||
		res.CacheCreationTokens != 6835 {
		t.Errorf("tokens = %d in / %d cache-read / %d cache-write",
			res.InputTokens, res.CacheReadTokens, res.CacheCreationTokens)
	}

	if len(res.ModelUsage) != 1 {
		t.Fatalf("model usage = %+v, want one model", res.ModelUsage)
	}
	u := res.ModelUsage[0]
	if u.Model != "claude-haiku-4-5-20251001" || u.InputTokens != 18 ||
		u.OutputTokens != 536 || u.CacheReadTokens != 60280 ||
		u.CacheCreationTokens != 6835 || u.CostUSD == nil {
		t.Errorf("model usage = %+v", u)
	}

	// The denial the fixture was captured for: full-auto with no user to
	// approve a Write.
	if len(res.PermissionDenials) != 1 {
		t.Fatalf("denials = %+v, want the one this run recorded", res.PermissionDenials)
	}
	if d := res.PermissionDenials[0]; d.ToolName != "Write" ||
		d.CallID != "toolu_01NfZSDqXQKXwt59MWGhoqgw" {
		t.Errorf("denial = %+v, want the refused Write and its call id", d)
	}
}

// TestParseRunHeaderFixture covers the `system`/`init` line each capture
// opens with — the one that fell through to EventUnknown entirely.
func TestParseRunHeaderFixture(t *testing.T) {
	events, _ := fixtureEvents(t, "stream_permission_allow_2.1.226.jsonl")
	if events[0].Type != agent.EventRunHeader || events[0].Header == nil {
		t.Fatalf("first event = %q, want the run header", events[0].Type)
	}
	h := events[0].Header
	if h.WorkDir != `C:\work\repo` {
		t.Errorf("cwd = %q, want the directory claude reported", h.WorkDir)
	}
	want := []string{"Task", "AskUserQuestion", "Bash", "Read", "Write"}
	if !slices.Equal(h.Tools, want) {
		t.Errorf("tools = %v, want %v in the order claude listed them", h.Tools, want)
	}
	// Exactly one: a header per run, not per line.
	var headers int
	for _, ev := range events {
		if ev.Type == agent.EventRunHeader {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("got %d run headers, want exactly one", headers)
	}
}

// TestParseToolUseResultVerb covers the structured outcome that rides beside
// a `user` line's message rather than inside it (task 066).
func TestParseToolUseResultVerb(t *testing.T) {
	events, _ := fixtureEvents(t, "stream_permission_allow_2.1.226.jsonl")
	var found bool
	for _, ev := range events {
		for _, r := range ev.Results {
			if r.CallID != "toolu_01PSqBeA6sKydYaELf8NTXHH" {
				continue
			}
			found = true
			if r.Verb != "created" {
				t.Errorf("verb = %q, want created from tool_use_result.type", r.Verb)
			}
			if r.Blocked {
				t.Errorf("allowed Write marked blocked: %+v", r)
			}
		}
	}
	if !found {
		t.Error("no result correlated to the captured Write call")
	}
}

// TestParseBlockedToolResult covers the deny capture's two halves at once: a
// permission-blocked call is reported as blocked rather than as an ordinary
// error string, and the *string*-shaped `tool_use_result` on that same line
// does not derail the object decode the verb comes from.
func TestParseBlockedToolResult(t *testing.T) {
	events, _ := fixtureEvents(t, "stream_permission_deny_2.1.226.jsonl")
	var results []agent.ToolResult
	for _, ev := range events {
		results = append(results, ev.Results...)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want the one refused Write", results)
	}
	r := results[0]
	if !r.Blocked {
		t.Errorf("result = %+v, want Blocked from non_execution_kind permission-rule", r)
	}
	if !r.IsError {
		t.Errorf("result = %+v: a blocked call is still an error to the model", r)
	}
	// `tool_use_result` here is the bare string
	// "Error: no user is available; permission denied" — the object probe
	// must decline it rather than fail the line.
	if r.Verb != "" {
		t.Errorf("verb = %q, want none: a string payload carries no type", r.Verb)
	}
	if !strings.Contains(r.Summary, "permission denied") {
		t.Errorf("summary = %q, want the tool's reported outcome", r.Summary)
	}
}

// TestUnobservedToolUseResultTypeHasNoVerb is the T4.17 rule asserted: a
// structured outcome whose type no capture has shown yields no verb rather
// than a guessed past tense, because a wrong verb is indistinguishable from
// a tool that reported nothing.
func TestUnobservedToolUseResultTypeHasNoVerb(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result",` +
		`"tool_use_id":"t1","content":"ok"}]},` +
		`"tool_use_result":{"type":"some_future_shape"}}`
	ev := parseLine([]byte(line))
	if len(ev.Results) != 1 || ev.Results[0].Verb != "" {
		t.Errorf("results = %+v, want no verb for an unobserved type", ev.Results)
	}
}

// TestParentToolUseIDIsEmptyInCaptures pins what the fixtures actually
// contain: `parent_tool_use_id` is null on every line of all three, so the
// field is carried and there is nothing here to nest. A capture of a `Task`
// run is what unblocks the renderer, and it is follow-up work.
func TestParentToolUseIDIsEmptyInCaptures(t *testing.T) {
	for _, name := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_permission_deny_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		events, _ := fixtureEvents(t, name)
		for i, ev := range events {
			if ev.ParentCallID != "" {
				t.Errorf("%s line %d: parent = %q, want empty", name, i, ev.ParentCallID)
			}
		}
	}
}

// TestParentToolUseIDIsRead is the other half: the field is read when it is
// there. It has to be asserted synthetically because no captured run has one.
func TestParentToolUseIDIsRead(t *testing.T) {
	line := `{"type":"assistant","parent_tool_use_id":"toolu_parent",` +
		`"message":{"content":[{"type":"text","text":"from a subagent"}]}}`
	if ev := parseLine([]byte(line)); ev.ParentCallID != "toolu_parent" {
		t.Errorf("parent = %q, want the spawning call id", ev.ParentCallID)
	}
}

func lastResult(t *testing.T, events []agent.Event) *agent.RunResult {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == agent.EventResult && events[i].Result != nil {
			return events[i].Result
		}
	}
	t.Fatal("no result event in the capture")
	return nil
}
