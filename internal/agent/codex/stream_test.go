package codex

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// parseFixture runs every line of a captured `codex exec --json` stream
// through one stream instance, mirroring readLoop.
func parseFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	st := &stream{}
	var events []agent.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ev := st.parse([]byte(line))
		if len(ev.Raw) == 0 {
			t.Errorf("event %q has empty Raw; transcripts would lose it", ev.Type)
		}
		events = append(events, ev)
	}
	return events
}

// terminal returns the single EventResult of a fixture stream.
func terminal(t *testing.T, events []agent.Event) *agent.RunResult {
	t.Helper()
	var res *agent.RunResult
	for _, ev := range events {
		if ev.Type == agent.EventResult {
			if res != nil {
				t.Fatal("more than one result event in the stream")
			}
			res = ev.Result
		}
	}
	if res == nil {
		t.Fatal("no result event in the stream")
	}
	return res
}

func TestParseSuccessFixture(t *testing.T) {
	events := parseFixture(t, "success.jsonl")
	types := make([]agent.EventType, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	want := []agent.EventType{
		agent.EventUnknown, // thread.started
		agent.EventUnknown, // turn.started
		agent.EventOutput,  // agent_message "hello"
		agent.EventResult,  // turn.completed
	}
	for i := range want {
		if i >= len(types) || types[i] != want[i] {
			t.Fatalf("event types = %v, want %v", types, want)
		}
	}
	res := terminal(t, events)
	if res.IsError {
		t.Errorf("IsError = true (%s), want success", res.ErrorMessage)
	}
	if res.ResultText != "hello" {
		t.Errorf("ResultText = %q, want the last agent_message", res.ResultText)
	}
	if res.InputTokens != 14312 || res.OutputTokens != 32 {
		t.Errorf("tokens = %d/%d, want 14312/32 (input_tokens verbatim)", res.InputTokens, res.OutputTokens)
	}
	if res.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", *res.CostUSD)
	}
}

func TestParseToolUseFixture(t *testing.T) {
	events := parseFixture(t, "tooluse.jsonl")
	var toolNames []string
	for _, ev := range events {
		if ev.Type == agent.EventToolUse {
			for _, tu := range ev.Tools {
				toolNames = append(toolNames, tu.Name)
			}
		}
	}
	// tool_use fires on item.started only — the matching item.completed is a
	// tool_result, not a second invocation (T4.16), so the count stays 1.
	if len(toolNames) != 1 || toolNames[0] != "command_execution" {
		t.Errorf("tool uses = %v, want exactly one command_execution (from item.started)", toolNames)
	}
	// T4.14: the subject is read out of the item's own fields, which differ
	// per item type — the item is kept raw rather than modeled per type.
	for _, ev := range events {
		if ev.Type != agent.EventToolUse {
			continue
		}
		if got := ev.Tools[0].Summary; got != `pwsh -Command 'echo vincent-fixture'` {
			t.Errorf("summary = %q, want the executed command", got)
		}
		if got := ev.Tools[0].CallID; got != "item_0" {
			t.Errorf("call id = %q, want item_0", got)
		}
	}
	// T4.16: the completion reports the exit code, correlated to the call by
	// the shared item id — the fixture's command exits 0.
	var results []agent.ToolResult
	for _, ev := range events {
		if ev.Type == agent.EventToolResult {
			results = append(results, ev.Results...)
		}
	}
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1 from item.completed", len(results))
	}
	if results[0].CallID != "item_0" || results[0].Summary != "exit 0" || results[0].IsError {
		t.Errorf("result = %+v, want item_0 exiting 0 without error", results[0])
	}
	res := terminal(t, events)
	if res.IsError {
		t.Errorf("IsError = true (%s), want success", res.ErrorMessage)
	}
	if !strings.Contains(res.ResultText, "vincent-fixture") {
		t.Errorf("ResultText = %q, want the final agent_message", res.ResultText)
	}
	if res.InputTokens != 28858 || res.OutputTokens != 196 {
		t.Errorf("tokens = %d/%d, want 28858/196", res.InputTokens, res.OutputTokens)
	}
}

func TestParseFailureFixture(t *testing.T) {
	events := parseFixture(t, "failure.jsonl")
	errorEvents := 0
	for _, ev := range events {
		if ev.Type == agent.EventError {
			errorEvents++
			if ev.Message == "" {
				t.Error("EventError with empty Message")
			}
		}
	}
	// One advisory error item + one error line precede the failed turn.
	if errorEvents != 2 {
		t.Errorf("error events = %d, want 2 (advisory item + error line)", errorEvents)
	}
	res := terminal(t, events)
	if !res.IsError {
		t.Error("IsError = false for a turn.failed stream")
	}
	if !strings.Contains(res.ErrorMessage, "requires a newer version of Codex") {
		t.Errorf("ErrorMessage = %q, want the turn.failed message", res.ErrorMessage)
	}
}

func TestParseUnparseableLine(t *testing.T) {
	st := &stream{}
	ev := st.parse([]byte("not json at all"))
	if ev.Type != agent.EventUnknown {
		t.Errorf("Type = %q, want unknown for an unparseable line", ev.Type)
	}
	if string(ev.Raw) != "not json at all" {
		t.Error("Raw must carry the verbatim line for the transcript")
	}
}

func TestParseTurnFailedWithoutMessage(t *testing.T) {
	st := &stream{}
	ev := st.parse([]byte(`{"type":"turn.failed"}`))
	if ev.Type != agent.EventResult || ev.Result == nil {
		t.Fatalf("Type = %q, want a result event", ev.Type)
	}
	if !ev.Result.IsError || ev.Result.ErrorMessage == "" {
		t.Errorf("got IsError=%v msg=%q, want a placeholder error message", ev.Result.IsError, ev.Result.ErrorMessage)
	}
}

func TestOptionsCuratedOnly(t *testing.T) {
	// A missing binary changes nothing: the catalog is curated, no probe runs.
	a := New(func() string { return "/nonexistent/codex-not-here" })
	opts, err := a.Options(t.Context())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if len(opts.Models) != 0 {
		t.Errorf("Models = %v, want none (account-dependent ids are never advertised, §9.3)", opts.Models)
	}
	wantEfforts := []string{"minimal", "low", "medium", "high", "xhigh"}
	if len(opts.Efforts) != len(wantEfforts) {
		t.Fatalf("Efforts = %v, want %v", opts.Efforts, wantEfforts)
	}
	for i, want := range wantEfforts {
		if opts.Efforts[i].Value != want || opts.Efforts[i].Source != agent.SourceCurated {
			t.Errorf("Efforts[%d] = %+v, want {%s curated}", i, opts.Efforts[i], want)
		}
	}
	if opts.DefaultModel != "" || opts.DefaultEffort != "" {
		t.Error("defaults must stay empty — the CLI decides")
	}
}

// TestParseReasoningFixture is T4.17, against a real `codex exec --json` run
// (codex-cli 0.147.0) at `-c model_reasoning_effort=high`. The task stayed
// open for exactly one reason — no capture in this repo contained a reasoning
// item, and normalizing a documented-but-unobserved shape fails silently: the
// reasoning simply never appears, which is indistinguishable from a model that
// did not reason.
//
// The capture settles the shape. Reasoning arrives as `item.completed` with
// item type `reasoning`, carrying whole `text`, four times in one turn — no
// `item.started` to correlate against and no deltas to accumulate. That is
// claude's shape rather than cursor's, which is why this needs none of the
// cursor parser's buffering.
func TestParseReasoningFixture(t *testing.T) {
	events := parseFixture(t, "reasoning_0.147.0.jsonl")

	var thinking []string
	for _, ev := range events {
		if ev.Type == agent.EventThinking {
			thinking = append(thinking, ev.Text)
		}
	}
	if len(thinking) != 4 {
		t.Fatalf("thinking events = %d, want 4 (one per reasoning item in the capture)", len(thinking))
	}
	for i, text := range thinking {
		if text == "" {
			t.Errorf("thinking event %d has no text", i)
		}
	}
	if !strings.Contains(thinking[0], "Designing shortest subarray sum algorithm") {
		t.Errorf("first reasoning block = %q, want the captured text verbatim", thinking[0])
	}

	// Reasoning is not output: it must not become the result text, and it
	// must not be counted as an assistant message.
	res := terminal(t, events)
	if res.IsError {
		t.Errorf("IsError = true (%s), want success", res.ErrorMessage)
	}
	if !strings.HasPrefix(res.ResultText, "Let `P[i]`") {
		t.Errorf("ResultText = %.40q, want the agent_message, not a reasoning block", res.ResultText)
	}
	for _, ev := range events {
		if ev.Type == agent.EventOutput && strings.HasPrefix(ev.Text, "**Designing") {
			t.Error("a reasoning block was normalized as assistant output")
		}
	}
	if res.InputTokens != 17792 || res.OutputTokens != 3333 {
		t.Errorf("tokens = %d/%d, want 17792/3333", res.InputTokens, res.OutputTokens)
	}
	// Every reasoning line still carries its raw bytes, so a transcript keeps
	// the record even where a renderer chooses to hide it.
	for _, ev := range events {
		if ev.Type == agent.EventThinking && len(ev.Raw) == 0 {
			t.Error("a thinking event lost its raw line")
		}
	}
}

// A reasoning item with no text is not an event: emitting an empty thinking
// block would put a blank gutter line in the pane for nothing.
func TestParseReasoningWithoutText(t *testing.T) {
	st := &stream{}
	ev := st.parse([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":""}}`))
	if ev.Type != agent.EventUnknown {
		t.Errorf("empty reasoning normalized to %v, want unknown", ev.Type)
	}
}
