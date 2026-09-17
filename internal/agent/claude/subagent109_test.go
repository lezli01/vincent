package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// The subagent captures (task 109) are trimmed from recorded real runs: two
// background agents whose lines interleave with each other and with the main
// loop on 2.1.268, one agent the main loop waited on on 2.1.263, and one
// background agent that failed on 2.1.268.
const (
	asyncFixture  = "stream_subagent_async_2.1.268.jsonl"
	syncFixture   = "stream_subagent_sync_2.1.263.jsonl"
	failedFixture = "stream_subagent_failed_2.1.268.jsonl"
)

// The spawning calls in the async capture.
const (
	asyncFirst  = "toolu_01VxaW1YHEhXY4R1c7fvbXLF"
	asyncSecond = "toolu_015ULCiXx2FP8kNzrbkiLFjU"
)

// TestSubagentChildrenCarryTheirSpawningCall is the attribution the pane
// nests by: every line a subagent wrote names the call that spawned it, the
// main loop's lines name none, and both agents' lines interleave.
func TestSubagentChildrenCarryTheirSpawningCall(t *testing.T) {
	events, _ := fixtureEvents(t, asyncFixture)
	children := map[string]int{}
	var main int
	for _, ev := range events {
		switch ev.ParentCallID {
		case "":
			main++
		case asyncFirst, asyncSecond:
			children[ev.ParentCallID]++
		default:
			t.Errorf("event %q has an unexpected parent %q", ev.Type, ev.ParentCallID)
		}
	}
	if children[asyncFirst] == 0 || children[asyncSecond] == 0 || main == 0 {
		t.Fatalf("children = %v, main = %d: want both agents and the main loop", children, main)
	}
	// The interleaving is what rules out grouping: the second agent's first
	// line lands between two of the first agent's.
	var order []string
	for _, ev := range events {
		if ev.ParentCallID != "" && (len(order) == 0 || order[len(order)-1] != ev.ParentCallID) {
			order = append(order, ev.ParentCallID)
		}
	}
	if len(order) < 3 {
		t.Errorf("children switch agents %d times, want the capture's interleaving", len(order)-1)
	}
}

// TestSubagentTaskLinesNormalize covers the three `local_agent` task lines.
func TestSubagentTaskLinesNormalize(t *testing.T) {
	events, _ := fixtureEvents(t, asyncFixture)
	byType := map[agent.EventType][]*agent.Subagent{}
	for _, ev := range events {
		switch ev.Type { //nolint:exhaustive // only the subagent events matter here
		case agent.EventSubagentStarted, agent.EventSubagentProgress, agent.EventSubagentFinished:
			if ev.Subagent == nil {
				t.Fatalf("%s carries no Subagent", ev.Type)
			}
			if ev.ParentCallID != "" {
				t.Errorf("%s has parent %q: a task line is the main loop's", ev.Type, ev.ParentCallID)
			}
			byType[ev.Type] = append(byType[ev.Type], ev.Subagent)
		}
	}

	started := byType[agent.EventSubagentStarted]
	if len(started) != 2 {
		t.Fatalf("started = %d, want the two agents", len(started))
	}
	if s := started[0]; s.CallID != asyncFirst || s.Description != "Verify 022 gate walkthrough claims" ||
		s.AgentType != "general-purpose" || !s.Background {
		t.Errorf("first start = %+v", s)
	}

	progress := byType[agent.EventSubagentProgress]
	if len(progress) != 3 {
		t.Fatalf("progress = %d, want the capture's three", len(progress))
	}
	if p := progress[0]; p.CallID != asyncFirst || p.LastTool != "Bash" || p.ToolUses != 1 ||
		p.TotalTokens == 0 || p.Duration == 0 {
		t.Errorf("first progress = %+v, want a running tally", p)
	}

	finished := byType[agent.EventSubagentFinished]
	if len(finished) != 2 {
		t.Fatalf("finished = %d, want both agents' notifications", len(finished))
	}
	f := finished[0]
	if f.CallID != asyncFirst || f.Status != "completed" || f.ToolUses != 30 ||
		f.TotalTokens != 110215 || f.Duration != 300797*time.Millisecond {
		t.Errorf("first finish = %+v", f)
	}
	// The report is a body: one line, capped, never the paragraphs.
	if strings.Contains(f.Summary, "\n") || len([]rune(f.Summary)) > resultSummaryMax ||
		!strings.HasPrefix(f.Summary, "Done.") {
		t.Errorf("summary = %q, want the report's capped first line", f.Summary)
	}
}

// TestFailedSubagentIsRecognizedByItsCall is why the parser remembers calls:
// a failed agent's notification carries no usage and no task_type, and only
// the start before it says whose it is.
func TestFailedSubagentIsRecognizedByItsCall(t *testing.T) {
	events, _ := fixtureEvents(t, failedFixture)
	last := events[len(events)-1]
	if last.Type != agent.EventSubagentFinished || last.Subagent == nil {
		t.Fatalf("last event = %q, want the failed notification normalized", last.Type)
	}
	if last.Subagent.Status != "failed" || last.Subagent.ToolUses != 0 {
		t.Errorf("finish = %+v, want failed with no tally", last.Subagent)
	}

	// The same line with nothing before it is indistinguishable from a
	// background shell's, and stays raw rather than being guessed at.
	if ev := parseLine(last.Raw); ev.Type != agent.EventUnknown {
		t.Errorf("a cold failed notification = %q, want unknown", ev.Type)
	}
}

// TestShellTaskLinesStayUnknown: `local_bash` lines — including one a
// subagent owns — and every other system subtype are out of scope and keep
// their raw line.
func TestShellTaskLinesStayUnknown(t *testing.T) {
	for _, name := range []string{asyncFixture, syncFixture} {
		events, _ := fixtureEvents(t, name)
		var shells int
		for i, ev := range events {
			raw := string(ev.Raw)
			if !strings.Contains(raw, `"type":"system"`) {
				continue
			}
			isShell := strings.Contains(raw, `"local_bash"`) ||
				(strings.Contains(raw, `"task_notification"`) && !strings.Contains(raw, `"usage"`) &&
					!strings.Contains(raw, asyncFirst) && !strings.Contains(raw, asyncSecond) &&
					!strings.Contains(raw, "toolu_01SccLMcQ3heAyKbreuZ4axP"))
			other := strings.Contains(raw, `"task_updated"`) || strings.Contains(raw, `"background_tasks_changed"`)
			if !isShell && !other {
				continue
			}
			shells++
			if ev.Type != agent.EventUnknown || len(ev.Raw) == 0 {
				t.Errorf("%s line %d = %q, want unknown with its raw line", name, i, ev.Type)
			}
		}
		if shells == 0 {
			t.Errorf("%s: no shell or other system line checked", name)
		}
	}
	line := `{"type":"system","subtype":"task_future","tool_use_id":"toolu_x","task_type":"local_agent"}`
	if ev := parseLine([]byte(line)); ev.Type != agent.EventUnknown {
		t.Errorf("unmodelled task subtype = %q, want unknown", ev.Type)
	}
}

// TestBackgroundLaunchOutcome: an async launch reports its verb instead of
// claude's internal metadata text, and a call the main loop waited on keeps
// the report's first line and gets no verb.
func TestBackgroundLaunchOutcome(t *testing.T) {
	result := func(name, callID string) agent.ToolResult {
		t.Helper()
		events, _ := fixtureEvents(t, name)
		for _, ev := range events {
			for _, r := range ev.Results {
				if r.CallID == callID {
					return r
				}
			}
		}
		t.Fatalf("%s: no result for %s", name, callID)
		return agent.ToolResult{}
	}
	if r := result(asyncFixture, asyncFirst); r.Verb != "started in background" || r.Summary != "" {
		t.Errorf("async result = %+v, want the background verb and no metadata text", r)
	}
	r := result(syncFixture, "toolu_01SccLMcQ3heAyKbreuZ4axP")
	if r.Verb != "" || r.Summary == "" || strings.Contains(r.Summary, "Async agent") {
		t.Errorf("sync result = %+v, want the report's first line and no verb", r)
	}
}

// TestSyncSubagentFixture: an agent the main loop waited on starts in the
// foreground and finishes before its call's result arrives.
func TestSyncSubagentFixture(t *testing.T) {
	events, _ := fixtureEvents(t, syncFixture)
	var sawStart, sawFinish bool
	for _, ev := range events {
		switch ev.Type { //nolint:exhaustive // only the subagent events matter here
		case agent.EventSubagentStarted:
			sawStart = true
			if ev.Subagent.Background {
				t.Errorf("start = %+v, want a foreground agent", ev.Subagent)
			}
		case agent.EventSubagentFinished:
			sawFinish = true
			if ev.Subagent.Status != "completed" || ev.Subagent.ToolUses != 25 {
				t.Errorf("finish = %+v", ev.Subagent)
			}
		}
	}
	if !sawStart || !sawFinish {
		t.Errorf("start = %v, finish = %v, want both", sawStart, sawFinish)
	}
}

// TestNoSubagentEventsInOlderCaptures: the 2.1.226 captures ran no agent.
func TestNoSubagentEventsInOlderCaptures(t *testing.T) {
	for _, name := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_permission_deny_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		events, _ := fixtureEvents(t, name)
		for i, ev := range events {
			if ev.Subagent != nil {
				t.Errorf("%s line %d: produced a subagent event", name, i)
			}
		}
	}
}
