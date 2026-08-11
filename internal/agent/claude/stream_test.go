package claude

import (
	"strings"
	"testing"

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
			name: "unknown type is tolerated",
			line: `{"type":"system","subtype":"init","model":"x"}`,
			want: func(t *testing.T, ev agent.Event) {
				if ev.Type != agent.EventUnknown {
					t.Errorf("got %q, want unknown", ev.Type)
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
