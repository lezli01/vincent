package claude

import (
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
