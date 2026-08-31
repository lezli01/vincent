package codex

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// planFixture is the 0.150.1 capture of a run that wrote itself a to-do list
// and ticked it over: item.started, two item.updated and item.completed for
// one todo_list, plus command_execution and file_change items.
const planFixture = "plan_0.150.1.jsonl"

// TestPlanEvents covers the item.updated arm, which had no case in the parse
// switch at all before task 070 — every one of them fell to EventUnknown and
// was dropped from live output.
func TestPlanEvents(t *testing.T) {
	var plans []*agent.Plan
	for _, ev := range parseFixture(t, planFixture) {
		if ev.Type == agent.EventPlan {
			if ev.Plan == nil {
				t.Fatal("plan event with no plan")
			}
			plans = append(plans, ev.Plan)
		}
	}
	// started, three updates, completed — the whole list on every one.
	if len(plans) != 5 {
		t.Fatalf("plan events = %d, want 5", len(plans))
	}
	for i, p := range plans {
		if p.CallID != "item_1" {
			t.Errorf("plan %d call id = %q, want item_1 — versions of one plan", i, p.CallID)
		}
		if len(p.Items) != 3 {
			t.Fatalf("plan %d has %d items, want the whole list of 3", i, len(p.Items))
		}
	}
	if plans[0].Items[0].Completed {
		t.Error("first plan version: item already complete")
	}
	if !plans[4].Items[2].Completed {
		t.Error("last plan version: final item not complete")
	}
	if got := plans[0].Items[1].Text; got != "Append `second` to notes.txt" {
		t.Errorf("plan item text = %q", got)
	}
}

// TestCommandOutput covers aggregated_output, which nothing read before: the
// body rides on the same event as the outcome, because codex reports both on
// one item.completed line.
func TestCommandOutput(t *testing.T) {
	var outputs []*agent.CommandOutput
	for _, ev := range parseFixture(t, planFixture) {
		if ev.Output != nil {
			if ev.Type != agent.EventToolResult {
				t.Errorf("output on a %s event", ev.Type)
			}
			outputs = append(outputs, ev.Output)
		}
	}
	if len(outputs) != 3 {
		t.Fatalf("command outputs = %d, want 3", len(outputs))
	}
	if !strings.Contains(outputs[0].Text, "notes.txt") {
		t.Errorf("first output = %q, want the `ls -la` listing", outputs[0].Text)
	}
	if outputs[0].CallID != "item_2" || outputs[0].Name != "command_execution" {
		t.Errorf("output = %+v, want it correlated to its call", outputs[0])
	}
	for i, o := range outputs {
		if o.Truncated {
			t.Errorf("output %d truncated; the fixture is far under the cap", i)
		}
	}
}

// TestCommandOutputTruncation proves the cap is applied and *visible*.
// Truncation a reader cannot see is indistinguishable from a command that
// printed exactly that much (task 070 decision 2).
func TestCommandOutputTruncation(t *testing.T) {
	long := strings.Repeat("x", agent.CommandOutputMax+500)
	it := &execItem{ID: "c1", Type: "command_execution", AggregatedOutput: long}
	out := commandOutput(it)
	if out == nil {
		t.Fatal("no output")
	}
	if !out.Truncated {
		t.Error("truncated = false")
	}
	if got := len([]rune(out.Text)); got != agent.CommandOutputMax {
		t.Errorf("output runes = %d, want %d", got, agent.CommandOutputMax)
	}
	if commandOutput(&execItem{ID: "c2", Type: "command_execution"}) != nil {
		t.Error("a command that printed nothing produced an output record")
	}
	if commandOutput(&execItem{ID: "w", Type: "web_search", AggregatedOutput: "x"}) != nil {
		t.Error("a non-command item produced a command output record")
	}
}

// TestFileChangeSummary: file_change keeps its subject in `changes[]`, an
// array of objects agent.ToolSummary cannot read at all, so the pane said
// `file_change` and stopped.
func TestFileChangeSummary(t *testing.T) {
	for _, ev := range parseFixture(t, planFixture) {
		for _, tool := range ev.Tools {
			if tool.Name != "file_change" {
				continue
			}
			if tool.Summary != "update notes.txt" {
				t.Errorf("file_change summary = %q, want %q", tool.Summary, "update notes.txt")
			}
			return
		}
	}
	t.Fatal("no file_change tool use in the fixture")
}

// TestChangeSummaryShapes covers the shapes one capture cannot show at once.
func TestChangeSummaryShapes(t *testing.T) {
	got := changeSummary([]execChange{
		{Path: `C:\repo\a.go`, Kind: "add"},
		{Path: "/repo/b.go", Kind: "delete"},
	})
	// A Windows path is split on its own separator, not left whole: the
	// adapter runs on all three platforms and a capture came from one.
	if got != "add a.go, delete b.go" {
		t.Errorf("summary = %q", got)
	}
	if got := changeSummary(nil); got != "" {
		t.Errorf("no changes = %q, want empty", got)
	}
	if got := changeSummary([]execChange{{Path: "/repo/x.go"}}); got != "x.go" {
		t.Errorf("kindless change = %q", got)
	}
}

// TestMCPToolCallSummary: server/tool are codex-shaped names that stay out of
// the shared key list, so the adapter builds this summary itself (task 070
// decision 4).
func TestMCPToolCallSummary(t *testing.T) {
	var summaries []string
	var results []agent.ToolResult
	for _, ev := range parseFixture(t, "mcp_0.150.1.jsonl") {
		for _, tool := range ev.Tools {
			if tool.Name == "mcp_tool_call" {
				summaries = append(summaries, tool.Summary)
			}
		}
		for _, r := range ev.Results {
			if r.Name == "mcp_tool_call" {
				results = append(results, r)
			}
		}
	}
	want := []string{"vincent/health", "vincent/task_get"}
	if len(summaries) != len(want) {
		t.Fatalf("mcp summaries = %v, want %v", summaries, want)
	}
	for i, w := range want {
		if summaries[i] != w {
			t.Errorf("mcp summary %d = %q, want %q", i, summaries[i], w)
		}
	}
	if len(results) != 2 {
		t.Fatalf("mcp results = %d, want 2", len(results))
	}
	// The failed call reports `status: "failed"` and `error: null` — the
	// server's explanation came back inside `result`. `error.message` has no
	// capture and is deferred (§9.3), so the outcome is still the status.
	if !results[1].IsError || results[1].Summary != "failed" {
		t.Errorf("failed mcp result = %+v", results[1])
	}
	if results[0].IsError {
		t.Errorf("successful mcp result marked as an error: %+v", results[0])
	}
}

// TestUsageFields: all five counters, with the two cache counts landing in
// the RunResult fields task 066 already defined rather than in new ones.
func TestUsageFields(t *testing.T) {
	var res *agent.RunResult
	for _, ev := range parseFixture(t, planFixture) {
		if ev.Type == agent.EventResult {
			res = ev.Result
		}
	}
	if res == nil {
		t.Fatal("no terminal result")
	}
	switch {
	case res.InputTokens != 133712:
		t.Errorf("input = %d", res.InputTokens)
	case res.OutputTokens != 937:
		t.Errorf("output = %d", res.OutputTokens)
	case res.CacheReadTokens != 113920:
		t.Errorf("cache read = %d, want cached_input_tokens", res.CacheReadTokens)
	case res.CacheCreationTokens != 0:
		t.Errorf("cache write = %d", res.CacheCreationTokens)
	case res.ReasoningOutputTokens != 314:
		t.Errorf("reasoning = %d", res.ReasoningOutputTokens)
	}
}

// TestThreadIDReachesResult is the parse half of the resume leg: the id
// arrives on thread.started, several lines before the result that carries it.
func TestThreadIDReachesResult(t *testing.T) {
	for _, name := range []string{planFixture, "resume_0.150.1.jsonl", "success.jsonl", "failure.jsonl"} {
		var res *agent.RunResult
		for _, ev := range parseFixture(t, name) {
			if ev.Type == agent.EventPlan || ev.Type == agent.EventUnknown {
				continue
			}
			if ev.Type == agent.EventResult {
				res = ev.Result
			}
		}
		if res == nil {
			t.Fatalf("%s: no terminal result", name)
		}
		if res.SessionID == "" {
			t.Errorf("%s: session id empty; a chat could not resume it", name)
		}
	}
}

// TestResumedTurnReportsSameThread is the evidence behind SupportsResume:
// the resumed turn's thread.started carries the *same* id it was handed, and
// its answer is about the previous turn.
func TestResumedTurnReportsSameThread(t *testing.T) {
	var first string
	for _, ev := range parseFixture(t, planFixture) {
		if ev.Type == agent.EventResult {
			first = ev.Result.SessionID
		}
	}
	for _, ev := range parseFixture(t, "resume_0.150.1.jsonl") {
		if ev.Type == agent.EventResult && ev.Result.SessionID != first {
			t.Errorf("resumed thread = %q, want %q", ev.Result.SessionID, first)
		}
	}
}

// TestUnmodelledShapesStayUnknown asserts the phase 1 tolerant-parsing
// decision rather than assuming it. It is what makes §9.3's deferred list
// safe: an item type or an event type vincent has no capture for is carried
// verbatim, not guessed at.
func TestUnmodelledShapesStayUnknown(t *testing.T) {
	s := &stream{}
	for _, line := range []string{
		`{"type":"item.completed","item":{"id":"i1","type":"collab_tool_call","payload":{"a":1}}}`,
		`{"type":"turn.interrupted","reason":"user"}`,
		`{"type":"item.updated","item":{"id":"i2","type":"command_execution","aggregated_output":"partial"}}`,
	} {
		ev := s.parse([]byte(line))
		if ev.Type != agent.EventUnknown {
			t.Errorf("%s → %s, want unknown", line, ev.Type)
		}
		if string(ev.Raw) != line {
			t.Errorf("raw = %q, want the line verbatim", ev.Raw)
		}
	}
}

// TestResumeArgv pins the shape verified against codex-cli 0.150.1. The
// prompt is never on argv: `codex exec resume <id>` with no PROMPT argument
// reads stdin, which is what RunSpec.Prompt's contract requires.
func TestResumeArgv(t *testing.T) {
	args := buildArgs(agent.RunSpec{ResumeSessionID: "01a0586e-5a43-7001-9628-cde66a53e993", Model: "gpt-5"})
	want := []string{
		"exec", "--json", "resume", "01a0586e-5a43-7001-9628-cde66a53e993",
		"--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", args, want)
	}
	if got := buildArgs(agent.RunSpec{}); strings.Contains(strings.Join(got, " "), "resume") {
		t.Errorf("a non-resuming run got a resume argv: %v", got)
	}
	if !New(nil).SupportsResume() {
		t.Error("SupportsResume = false")
	}
}
