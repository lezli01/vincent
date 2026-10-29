package copilot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// subagentCall is subagent_1.0.105.jsonl's `task` call that spawned the
// explore subagent.
const subagentCall = "call_QsZsx9R4Ejvfr1Bo1qxMLfSp"

// TestSubagentLifecycle: subagent.started and subagent.completed are the
// root agent's account of the sub-run (task 109), keyed by the spawning
// call, and themselves carry no ParentCallID. subagent.selected and
// .configured stay unknown, and there is no progress event.
func TestSubagentLifecycle(t *testing.T) {
	var started, finished *agent.Subagent
	for _, ev := range parseFixture(t, "subagent_1.0.105.jsonl") {
		switch ev.Type { //nolint:exhaustive // only the subagent events matter here
		case agent.EventSubagentStarted:
			started = ev.Subagent
		case agent.EventSubagentFinished:
			finished = ev.Subagent
		case agent.EventSubagentProgress:
			t.Errorf("progress event %s; copilot reports none", ev.Raw)
		}
		switch rawType(t, ev) {
		case "subagent.started", "subagent.completed":
			if ev.ParentCallID != "" {
				t.Errorf("%s stamped with %q, want the main loop's", rawType(t, ev), ev.ParentCallID)
			}
		case "subagent.selected", "subagent.configured":
			if ev.Type != agent.EventUnknown {
				t.Errorf("%s → %q, want unknown", rawType(t, ev), ev.Type)
			}
		}
	}
	wantStart := agent.Subagent{CallID: subagentCall, AgentType: "explore", Description: "Find hello file"}
	if started == nil || *started != wantStart {
		t.Errorf("started = %+v, want %+v", started, wantStart)
	}
	wantEnd := agent.Subagent{CallID: subagentCall, ToolUses: 1, TotalTokens: 8067, Duration: 3803 * time.Millisecond}
	if finished == nil || *finished != wantEnd {
		t.Errorf("finished = %+v, want %+v", finished, wantEnd)
	}
}

// TestSubagentLinesAreNested: every line the subagent produced after it
// started — its output, its tool use and result — carries the spawning
// call as its ParentCallID, and no root line does. The subagent's report is
// never the run's answer.
func TestSubagentLinesAreNested(t *testing.T) {
	events := parseFixture(t, "subagent_1.0.105.jsonl")
	nested := map[agent.EventType]int{}
	for _, ev := range events {
		var line streamLine
		if err := json.Unmarshal(ev.Raw, &line); err != nil {
			t.Fatal(err)
		}
		sub := line.AgentID != "" && line.Type != "subagent.started" && line.Type != "subagent.completed"
		if sub != (ev.ParentCallID == subagentCall) {
			t.Errorf("%s (agentId %q) → ParentCallID %q", line.Type, line.AgentID, ev.ParentCallID)
		}
		if ev.ParentCallID != "" && ev.Type != agent.EventUnknown {
			nested[ev.Type]++
		}
	}
	want := map[agent.EventType]int{agent.EventOutput: 1, agent.EventToolUse: 1, agent.EventToolResult: 1}
	if !equalCounts(nested, want) {
		t.Errorf("nested modeled events = %v, want %v", nested, want)
	}
	if res := resultOf(t, events); res.ResultText == "/tmp/copilot-fixture/repo/notes.txt" {
		t.Errorf("ResultText = %q, the subagent's report rather than the run's answer", res.ResultText)
	}
}

// TestNoSubagentOutsideTheSubagentFixture: no other capture produces a
// subagent event or a nested line.
func TestNoSubagentOutsideTheSubagentFixture(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if name == "subagent_1.0.105.jsonl" {
			return
		}
		if ev.Subagent != nil || ev.ParentCallID != "" {
			t.Errorf("%s line %d: subagent event %q", name, i, ev.Type)
		}
	})
}
