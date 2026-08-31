package taskrun

import (
	"encoding/json"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The live chunk shapes (§13.3) and the normalized transcript shapes (§13.2)
// are declared in different packages and must agree: a client renders the
// live tail and the fetched scrollback through one path, so a difference
// shows up as output that changes the moment a step finishes. These tests pin
// the chunk side; internal/api pins the transcript side, and the apiclient
// live tests pin the round trip.

func TestToolChunkShape(t *testing.T) {
	got := marshal(t, toolChunks([]agent.ToolUse{
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
	got := marshal(t, resultChunks([]agent.ToolResult{
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
	got := marshal(t, resultChunks([]agent.ToolResult{
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
// agent.tool_use's objects — one key cannot mean two shapes.
func TestHeaderChunkShape(t *testing.T) {
	got := marshal(t, headerChunk(&agent.RunHeader{
		WorkDir: `C:\work\repo`,
		Tools:   []string{"Task", "Bash"},
	}))
	want := `{"available_tools":["Task","Bash"],"work_dir":"C:\\work\\repo"}`
	if got != want {
		t.Errorf("header chunk =\n%s\nwant\n%s", got, want)
	}
	// A header with neither half still publishes: the record itself is the
	// statement that the run began.
	if got := marshal(t, headerChunk(&agent.RunHeader{})); got != "{}" {
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
