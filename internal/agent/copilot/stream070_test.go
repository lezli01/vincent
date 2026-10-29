package copilot

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestShellCommandOutput: the shell tool's completion carries the command's
// output on the same line as its outcome, so it rides on the tool_result as
// codex's does (task 070) — the body with copilot's trailing
// `<shellId: N completed with exit code E>` stripped, the exit code as the
// outcome. Only `bash` is captured; the live chunks before the completion
// (tool.shell_output, tool.execution_partial_result) stay unknown, because
// the completion is the durable copy.
func TestShellCommandOutput(t *testing.T) {
	var outputs []*agent.CommandOutput
	var res agent.ToolResult
	for _, ev := range parseFixture(t, "shell_1.0.105.jsonl") {
		switch rawType(t, ev) {
		case "tool.shell_output", "tool.execution_partial_result":
			if ev.Type != agent.EventUnknown || ev.Output != nil {
				t.Errorf("live chunk → %q %+v, want unknown", ev.Type, ev.Output)
			}
		}
		if ev.Output != nil {
			outputs = append(outputs, ev.Output)
			res = ev.Results[0]
		}
	}
	if len(outputs) != 1 {
		t.Fatalf("command outputs = %d, want the one completion's", len(outputs))
	}
	want := agent.CommandOutput{CallID: res.CallID, Name: "bash", Text: "3106f2a init"}
	if *outputs[0] != want {
		t.Errorf("output = %+v, want %+v", *outputs[0], want)
	}
	if res.Summary != "exit 0" || res.IsError {
		t.Errorf("tool_result = %+v, want exit 0", res)
	}
}

// TestCommandOutputOnlyFromAShellThatRan: no tool but the shell carries an
// output body, and a denied shell call carries none.
func TestCommandOutputOnlyFromAShellThatRan(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if ev.Type == agent.EventCommandOutput {
			t.Errorf("%s line %d: a command_output event; copilot's output rides on its tool_result", name, i)
		}
		if ev.Output != nil && (ev.Type != agent.EventToolResult || ev.Results[0].Name != "bash" || name != "shell_1.0.105.jsonl") {
			t.Errorf("%s line %d: output on %q %+v", name, i, ev.Type, ev.Results)
		}
	})
	parse := New(nil).NewLineParser()
	parse([]byte(`{"type":"tool.execution_start","data":{"toolCallId":"c","toolName":"bash","arguments":{"command":"ls"}}}`))
	ev := parse([]byte(`{"type":"tool.execution_complete","data":{"toolCallId":"c","success":false,"error":{"message":"denied","code":"denied"}}}`))
	if ev.Output != nil || !ev.Results[0].Blocked {
		t.Errorf("denied shell → %+v, want blocked with no output", ev)
	}
}

// TestShellTrailerAndTruncation: the trailer goes wherever the body ends,
// an output that is only the trailer is no output, and the body is capped.
func TestShellTrailerAndTruncation(t *testing.T) {
	long := make([]byte, agent.CommandOutputMax+10)
	for i := range long {
		long[i] = 'x'
	}
	tests := []struct {
		content   string
		want      string
		truncated bool
		none      bool
	}{
		{content: `a\nb\n<shellId: 3 completed with exit code 1>`, want: "a\nb"},
		{content: `<shellId: 3 completed with exit code 0>`, none: true},
		{content: string(long) + `\n<shellId: 0 completed with exit code 0>`, want: string(long[:agent.CommandOutputMax]), truncated: true},
	}
	for _, tt := range tests {
		parse := New(nil).NewLineParser()
		parse([]byte(`{"type":"tool.execution_start","data":{"toolCallId":"c","toolName":"bash","arguments":{"command":"x"}}}`))
		ev := parse([]byte(`{"type":"tool.execution_complete","data":{"toolCallId":"c","success":true,"shellExecution":{"exitCode":1},"result":{"content":"` + tt.content + `"}}}`))
		switch {
		case tt.none:
			if ev.Output != nil {
				t.Errorf("%.30q → %+v, want no output", tt.content, ev.Output)
			}
		case ev.Output == nil || ev.Output.Text != tt.want || ev.Output.Truncated != tt.truncated:
			t.Errorf("%.30q → %+v, want %.30q truncated=%v", tt.content, ev.Output, tt.want, tt.truncated)
		}
		if ev.Results[0].Summary != "exit 1" {
			t.Errorf("summary = %q, want exit 1", ev.Results[0].Summary)
		}
	}
}

// TestNoPlan: no capture carries a to-do tool or item, so copilot produces
// no plan event (§9.9), and nothing synthesizes one.
func TestNoPlan(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if ev.Type == agent.EventPlan || ev.Plan != nil {
			t.Errorf("%s line %d: produced a plan", name, i)
		}
	})
}
