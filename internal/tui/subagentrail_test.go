package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/apiclient"
)

// claudeRecords reads a captured claude run into the records the pane
// renders, through the adapter's own parser and the §13.3 chunk mapping the
// transcript route is held equal to (internal/api pins that parity). A line
// with no chunk shape becomes agent.raw, and the terminal result carries the
// fields the pane reads, which is all the pane is given either way.
func claudeRecords(t *testing.T, name string) []apiclient.TranscriptRecord {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "agent", "claude", "testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	parse := claude.New(func() string { return "" }).NewLineParser()
	var out []apiclient.TranscriptRecord
	add := func(fields map[string]any) {
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		rec.Raw = data
		out = append(out, rec)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ev := parse([]byte(line))
		switch {
		case agent.UnmodeledLine(ev):
			fields := map[string]any{"type": "agent.raw", "line": line}
			if ev.ParentCallID != "" {
				fields["parent_call_id"] = ev.ParentCallID
			}
			add(fields)
		case ev.Type == agent.EventResult && ev.Result != nil:
			add(map[string]any{
				"type": "agent.result", "result_text": ev.Result.ResultText, "is_error": ev.Result.IsError,
				"duration_ms": ev.Result.Duration.Milliseconds(), "num_turns": ev.Result.NumTurns,
				"cost_usd": ev.Result.CostUSD,
			})
		default:
			for _, c := range agent.LiveChunks(ev) {
				fields := map[string]any{"type": c.Type}
				for k, v := range c.Payload {
					fields[k] = v
				}
				add(fields)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return out
}

const (
	railAsync  = "stream_subagent_async_2.1.268.jsonl"
	railSync   = "stream_subagent_sync_2.1.263.jsonl"
	railFailed = "stream_subagent_failed_2.1.268.jsonl"
	firstAgent = "Verify 022 gate walkthrough claims"
)

var allLevels = []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose}

func railRender(t *testing.T, name string, level outputLevel, width int) []string {
	t.Helper()
	return plainLines(outputLines(claudeRecords(t, name), level, width, lineOpts{expandKey: "v"}))
}

// TestFlatCapturesRenderAsBefore is the promise that nesting changed nothing
// for a run that spawned no agent: the three 2.1.226 captures render, at
// every level, exactly what they rendered before task 109. The goldens were
// written by the renderer as it stood before the rail existed.
func TestFlatCapturesRenderAsBefore(t *testing.T) {
	for _, name := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_permission_deny_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		var b strings.Builder
		for _, l := range allLevels {
			b.WriteString("== " + l.String() + "\n")
			for _, line := range railRender(t, name, l, 80) {
				b.WriteString(line + "\n")
			}
		}
		want, err := os.ReadFile(filepath.Join("testdata", "flat_"+strings.TrimSuffix(name, ".jsonl")+".txt"))
		if err != nil {
			t.Fatalf("read golden: %v", err)
		}
		if got := b.String(); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Errorf("%s renders differently from before the rail:\n%s", name, got)
		}
	}
}

// TestSubagentRailByLevel is the one-level-quieter table over a real capture
// of two interleaved background agents.
func TestSubagentRailByLevel(t *testing.T) {
	rendered := map[outputLevel]string{}
	for _, l := range allLevels {
		rendered[l] = strings.Join(railRender(t, railAsync, l, 100), "\n")
	}

	if strings.Contains(rendered[levelQuiet], "┊") {
		t.Errorf("quiet shows subagent internals:\n%s", rendered[levelQuiet])
	}

	compact := rendered[levelCompact]
	for _, want := range []string{
		"┊ ↳ " + firstAgent,
		"┊   Sanitized text for this excerpt.",
		"┊ ✓ completed · " + firstAgent + " · 30 tool uses · 5m00s",
		"▸ Agent",
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact is missing %q:\n%s", want, compact)
		}
	}
	for _, gone := range []string{"┊ ▸ ", "┊     ✓", "┊ · "} {
		if strings.Contains(compact, gone) {
			t.Errorf("compact shows %q, a subagent's tool call or reasoning:\n%s", gone, compact)
		}
	}

	normal := rendered[levelNormal]
	for _, want := range []string{"┊ ▸ Bash", "┊     ✓ ", "✓ started in background"} {
		if !strings.Contains(normal, want) {
			t.Errorf("normal is missing %q:\n%s", want, normal)
		}
	}
	if strings.Contains(normal, "┊ · ") {
		t.Errorf("normal shows a subagent's reasoning:\n%s", normal)
	}
	if strings.Contains(normal, "Async agent launched") {
		t.Errorf("normal quotes claude's internal launch metadata:\n%s", normal)
	}

	if verbose := rendered[levelVerbose]; !strings.Contains(verbose, "┊ · Working out which check") {
		t.Errorf("verbose is missing the subagent's reasoning:\n%s", verbose)
	}
	// Progress lines are modeled, so they never become a count between a
	// subagent's records at the levels that count unrecognized lines.
	for _, l := range []outputLevel{levelCompact, levelNormal} {
		if strings.Contains(rendered[l], "┊   … ") {
			t.Errorf("%s counts a subagent's lines:\n%s", l, rendered[l])
		}
	}
}

// TestSubagentUnrecognizedLinesCountOnlyAtVerbose: a subagent's raw lines are
// never shown whole, and are counted one level quieter than the main loop's.
func TestSubagentUnrecognizedLinesCountOnlyAtVerbose(t *testing.T) {
	verbose := strings.Join(railRender(t, railSync, levelVerbose, 100), "\n")
	if !strings.Contains(verbose, "┊   … 1 unrecognized line(s) (v)") {
		t.Errorf("verbose does not count the subagent's raw lines:\n%s", verbose)
	}
	if strings.Contains(verbose, `┊   {"type"`) || strings.Contains(verbose, `┊ {"type"`) {
		t.Errorf("verbose shows a subagent's raw line whole:\n%s", verbose)
	}
	normal := strings.Join(railRender(t, railSync, levelNormal, 100), "\n")
	if strings.Contains(normal, "┊   … ") {
		t.Errorf("normal counts the subagent's raw lines:\n%s", normal)
	}
}

// TestSubagentLabelOnEverySwitch: a label names the subagent whenever the
// rendered stream moves onto a rail, and is always followed by a railed line
// that is not another label.
func TestSubagentLabelOnEverySwitch(t *testing.T) {
	for _, l := range []outputLevel{levelCompact, levelNormal, levelVerbose} {
		lines := railRender(t, railAsync, l, 100)
		prev := ""
		for i, line := range lines {
			if strings.HasPrefix(line, "┊ ↳ ") {
				if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "┊") || strings.HasPrefix(lines[i+1], "┊ ↳ ") {
					t.Errorf("%s: label %q dangles:\n%s", l, line, strings.Join(lines, "\n"))
				}
				if line == prev {
					t.Errorf("%s: label %q repeated with no switch between", l, line)
				}
				prev = line
				continue
			}
			if !strings.HasPrefix(line, "┊") && line != "" {
				prev = ""
			}
		}
	}
	normal := railRender(t, railAsync, levelNormal, 100)
	var first, second int
	for _, line := range normal {
		switch line {
		case "┊ ↳ " + firstAgent:
			first++
		case "┊ ↳ Verify 088 gate walkthrough claims":
			second++
		}
	}
	// The two agents interleave, so each is re-labelled when the stream
	// comes back to it.
	if first < 2 || second < 1 {
		t.Errorf("labels = %d first, %d second:\n%s", first, second, strings.Join(normal, "\n"))
	}
}

// TestSubagentLabelNeverDangles: a subagent record that does not render at
// the level leaves no label behind.
func TestSubagentLabelNeverDangles(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "main"},
		{Type: "agent.thinking", Text: "hidden reasoning", ParentCallID: "c1"},
		{Type: "agent.tool_use", ParentCallID: "c1", Tools: []apiclient.TranscriptTool{{Name: "Bash"}}},
	}
	got := strings.Join(plainLines(outputLines(recs, levelCompact, 60, lineOpts{})), "\n")
	if strings.Contains(got, "↳") || strings.Contains(got, "┊") {
		t.Errorf("compact labelled a subagent that rendered nothing:\n%s", got)
	}
}

// TestSubagentMainLoopRecordsAreNotRailed: a main-loop record between two
// of a subagent's ends the rail.
func TestSubagentMainLoopRecordsAreNotRailed(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.subagent_started", CallID: "c1", Description: "Scout"},
		{Type: "agent.tool_use", ParentCallID: "c1", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "a.go"}}},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", Summary: "go test"}}},
		{Type: "agent.tool_use", ParentCallID: "c1", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "b.go"}}},
	}
	got := plainLines(outputLines(recs, levelNormal, 60, lineOpts{}))
	want := []string{"┊ ↳ Scout", "┊ ▸ Read a.go", "▸ Bash go test", "┊ ↳ Scout", "┊ ▸ Read b.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rendered = %q, want %q", got, want)
	}
}

// TestSubagentCompletionLineByStatus: each observed status has its own mark,
// and an unobserved one gets a neutral mark and its own word.
func TestSubagentCompletionLineByStatus(t *testing.T) {
	for status, want := range map[string]string{
		"completed": "┊ ✓ completed · Scout · 1 tool use · 2.5s",
		"failed":    "┊ ✗ failed · Scout · 1 tool use · 2.5s",
		"stopped":   "┊ ■ stopped · Scout · 1 tool use · 2.5s",
		"paused":    "┊ · paused · Scout · 1 tool use · 2.5s",
	} {
		recs := []apiclient.TranscriptRecord{
			{Type: "agent.subagent_started", CallID: "c1", Description: "Scout"},
			{Type: "agent.subagent_finished", CallID: "c1", Status: status, ToolUses: 1, DurationMS: 2500},
		}
		if got := plainLines(outputLines(recs, levelCompact, 80, lineOpts{})); len(got) != 1 || got[0] != want {
			t.Errorf("%s: rendered %q, want %q", status, got, want)
		}
		if got := outputLines(recs, levelQuiet, 80, lineOpts{}); len(got) != 0 {
			t.Errorf("%s: quiet rendered %q, want nothing", status, plainLines(got))
		}
	}
	// A real failure, with no tally, names the agent from its spawning call's
	// start.
	failed := strings.Join(railRender(t, railFailed, levelCompact, 100), "\n")
	if !strings.Contains(failed, "┊ ✗ failed · Write vincent-triggers skill") {
		t.Errorf("failed capture:\n%s", failed)
	}
}

// TestSubagentLabelFallsBack: with no start in the window, the label is the
// spawning call's subject, and with neither it is a plain word.
func TestSubagentLabelFallsBack(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Agent", Summary: "look around", CallID: "c1"}}},
		{Type: "agent.output", Text: "found it", ParentCallID: "c1"},
		{Type: "agent.output", Text: "orphan", ParentCallID: "c2"},
	}
	got := strings.Join(plainLines(outputLines(recs, levelCompact, 60, lineOpts{})), "\n")
	for _, want := range []string{"┊ ↳ look around", "┊ ↳ subagent"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// TestSubagentProseNeverJoinsMainProse is the #291 rule's new boundary: the
// main loop's table header and a subagent's delimiter row are two documents.
func TestSubagentProseNeverJoinsMainProse(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "| a | b |"},
		{Type: "agent.output", Text: "|---|---|", ParentCallID: "c1"},
		{Type: "agent.output", Text: "| 1 | 2 |", ParentCallID: "c1"},
	}
	docs := assistantDocs(recs, nil)
	if len(docs) != 2 || docs[0].last != 0 || docs[1].first != 1 || docs[1].last != 2 {
		t.Fatalf("docs = %+v, want the main loop's and the subagent's", docs)
	}
}

// TestSubagentWrapKeepsTheRail: a wrapped continuation is still the
// subagent's, so it keeps the rail.
func TestSubagentWrapKeepsTheRail(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", ParentCallID: "c1", Tools: []apiclient.TranscriptTool{{
			Name: "Bash", Summary: "go test ./internal/tui ./internal/cli ./internal/api -run Subagent",
		}}},
		{Type: "agent.output", ParentCallID: "c1", Text: strings.Repeat("words that wrap ", 8)},
	}
	lines := plainLines(outputLines(recs, levelNormal, 30, lineOpts{}))
	if len(lines) < 5 {
		t.Fatalf("nothing wrapped: %q", lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "┊") {
			t.Errorf("line %q lost the rail in %q", line, lines)
		}
		if cols(line) > 30 {
			t.Errorf("line %q overflows the pane", line)
		}
	}
}

// TestChatDrawsTheSameRail: the chat workspace renders through the same
// function, so a subagent reads the same there.
func TestChatDrawsTheSameRail(t *testing.T) {
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done", Prompt: "ask"}}
	v.turnRecords[1] = claudeRecords(t, railAsync)
	v.level.set(levelNormal)
	body := strings.Join(plainLines(v.bodyLines(100)), "\n")
	for _, want := range []string{"┊ ↳ " + firstAgent, "┊ ▸ Bash", "┊ ✓ completed · " + firstAgent} {
		if !strings.Contains(body, want) {
			t.Errorf("chat body is missing %q:\n%s", want, body)
		}
	}
}
