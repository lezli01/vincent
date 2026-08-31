package agent

import (
	"encoding/json"
	"testing"
)

// The live chunk shapes (§13.3) and the normalized transcript shapes (§13.2)
// are declared in different packages and must agree: a client renders the
// live tail and the fetched scrollback through one path, so a difference
// shows up as output that changes the moment a step finishes. These tests pin
// the chunk side; internal/api pins the transcript side, and the apiclient
// live tests pin the round trip.

func TestToolChunkShape(t *testing.T) {
	got := marshal(t, toolChunks([]ToolUse{
		{Name: "Bash", Summary: "git status", CallID: "toolu_01"},
		// A call whose arguments yielded no subject omits the field rather
		// than sending an empty string, so a client can tell "no subject"
		// from "the subject is blank".
		{Name: "TodoWrite"},
	}))
	want := `[{"call_id":"toolu_01","name":"Bash","summary":"git status"},{"name":"TodoWrite"}]`
	if got != want {
		t.Errorf("tool chunks =\n%s\nwant\n%s", got, want)
	}
}

func TestResultChunkShape(t *testing.T) {
	got := marshal(t, resultChunks([]ToolResult{
		{CallID: "toolu_01", Name: "command_execution", Summary: "exit 0"},
		{CallID: "toolu_02", Summary: "exit 1", IsError: true},
		// Cursor's uncaptured failure shape: no detail at all, which still
		// answers the question a reader is asking.
		{CallID: "toolu_03", IsError: true},
	}))
	want := `[{"call_id":"toolu_01","name":"command_execution","summary":"exit 0"},` +
		`{"call_id":"toolu_02","is_error":true,"summary":"exit 1"},` +
		`{"call_id":"toolu_03","is_error":true}]`
	if got != want {
		t.Errorf("result chunks =\n%s\nwant\n%s", got, want)
	}
}

// TestResultChunkCarriesVerbAndBlock extends the parity above onto the two
// fields task 066 added: the dialect's structured verb, and a call a
// permission rule refused — which is a different verdict from one that ran
// and failed, and renders with its own mark.
func TestResultChunkCarriesVerbAndBlock(t *testing.T) {
	got := marshal(t, resultChunks([]ToolResult{
		{CallID: "toolu_01", Summary: "File created successfully", Verb: "created"},
		{CallID: "toolu_02", Summary: "permission denied", Blocked: true, IsError: true},
	}))
	want := `[{"call_id":"toolu_01","summary":"File created successfully","verb":"created"},` +
		`{"blocked":true,"call_id":"toolu_02","is_error":true,"summary":"permission denied"}]`
	if got != want {
		t.Errorf("result chunks =\n%s\nwant\n%s", got, want)
	}
}

// TestHeaderChunkShape pins the run header's live shape (task 066). The tool
// list rides on `available_tools` rather than `tools`, which is
// tool_use's objects — one key cannot mean two shapes.
func TestHeaderChunkShape(t *testing.T) {
	got := marshal(t, headerChunk(&RunHeader{
		WorkDir: `C:\work\repo`,
		Tools:   []string{"Task", "Bash"},
	}))
	want := `{"available_tools":["Task","Bash"],"work_dir":"C:\\work\\repo"}`
	if got != want {
		t.Errorf("header chunk =\n%s\nwant\n%s", got, want)
	}
	// A header with neither half still publishes: the record itself is the
	// statement that the run began.
	if got := marshal(t, headerChunk(&RunHeader{})); got != "{}" {
		t.Errorf("empty header chunk = %s, want {}", got)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestPlanChunkShape pins the agent.plan live chunk against the wire record
// internal/api writes for the same event (task 070). The whole list rides on
// every chunk: a client that joins mid-run needs where the agent *is*, and a
// delta would leave it guessing.
func TestPlanChunkShape(t *testing.T) {
	got := marshal(t, planChunk(&Plan{
		CallID: "item_1",
		Items: []PlanItem{
			{Text: "Run `ls -la`", Completed: true},
			// `completed` is omitted rather than sent false, matching the
			// record's omitempty: absent and false mean the same thing.
			{Text: "Append to notes.txt"},
		},
	}))
	want := `{"items":[{"completed":true,"text":"Run ` + "`ls -la`" +
		`"},{"text":"Append to notes.txt"}],"plan_call_id":"item_1"}`
	if got != want {
		t.Errorf("plan chunk =\n%s\nwant\n%s", got, want)
	}
}

// TestCommandOutputChunkShape pins agent.command_output. The key is `output`
// and not `text`: `text` is agent.output's prose, and a reader must be able
// to tell what the agent said from what a command printed.
func TestCommandOutputChunkShape(t *testing.T) {
	got := marshal(t, outputChunk(&CommandOutput{
		CallID: "item_2", Name: "command_execution", Text: "total 8\n", Truncated: true,
	}))
	want := `{"call_id":"item_2","name":"command_execution","output":"total 8\n","truncated":true}`
	if got != want {
		t.Errorf("output chunk =\n%s\nwant\n%s", got, want)
	}
	// Untruncated output omits the flag, so a client can tell a cut body
	// from a whole one without comparing lengths.
	got = marshal(t, outputChunk(&CommandOutput{Text: "hi"}))
	if got != `{"output":"hi"}` {
		t.Errorf("untruncated chunk = %s", got)
	}
}

// TestToolResultWithOutputSplits pins the one event that becomes two chunks.
// codex reports a command's outcome and the body it printed on a single line;
// internal/api's transcript route already splits that into two records, so the
// live tail has to publish the same two in the same order — a client renders
// both through one path, and a tail that emitted one record where the refetch
// has two would change on screen the moment the step finished.
func TestToolResultWithOutputSplits(t *testing.T) {
	chunks := LiveChunks(Event{
		Type:    EventToolResult,
		Results: []ToolResult{{CallID: "item_2", Name: "command_execution"}},
		Output:  &CommandOutput{CallID: "item_2", Text: "total 8\n"},
	})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want the result and its output body: %+v", len(chunks), chunks)
	}
	if chunks[0].Type != "agent.tool_result" || chunks[1].Type != "agent.command_output" {
		t.Errorf("types = %q, %q; want agent.tool_result then agent.command_output",
			chunks[0].Type, chunks[1].Type)
	}
	// A result with no body stays one chunk: an empty agent.command_output
	// would be a line the transcript route never hands back.
	if got := LiveChunks(Event{Type: EventToolResult}); len(got) != 1 {
		t.Errorf("bodiless result = %d chunks, want 1", len(got))
	}
}

// TestParentCallIDRidesEveryChunk is why LiveChunks attaches it once rather
// than each arm doing it: a split line's second chunk is as nested as its
// first, and a client that indents by parent_call_id would otherwise put the
// body back at the top level.
func TestParentCallIDRidesEveryChunk(t *testing.T) {
	chunks := LiveChunks(Event{
		Type:         EventToolResult,
		ParentCallID: "call_1",
		Results:      []ToolResult{{CallID: "item_2"}},
		Output:       &CommandOutput{Text: "hi"},
	})
	for _, c := range chunks {
		if c.Payload["parent_call_id"] != "call_1" {
			t.Errorf("%s dropped parent_call_id: %+v", c.Type, c.Payload)
		}
	}
}
