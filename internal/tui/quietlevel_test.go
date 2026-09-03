package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The fourth output level (issue #321). `quiet` sits below `compact` and is
// the reading level: assistant prose and anything that went wrong, with
// nothing the agent narrated about its own machinery.
//
// Two things are under test here. What quiet drops, in *both* panes — the
// level is one holder shared by pointer, so a rule that held in a task
// workspace and not in a chat would be a second vocabulary. And what quiet
// did *not* disturb: the constants renumbered underneath the other three, so
// the loud levels are held to a golden of their whole rendered output rather
// than to a spot check, because an off-by-one in a `==` guard is exactly the
// regression that would otherwise pass unnoticed.

var quietCost = 0.0125

// levelFixture covers every record type §15 gives a per-level rule for, plus
// the ones it gives none — command output and the vincent.* frame — so the
// golden below says something about each.
var levelFixture = []apiclient.TranscriptRecord{
	{Type: "agent.run_header", WorkDir: "/w/tree", AvailableTools: []string{"Edit", "Bash"}},
	{Type: "agent.plan", Items: []apiclient.TranscriptPlanItem{
		{Text: "read the file", Completed: true},
		{Text: "write the fix"},
	}},
	{Type: "agent.thinking", Text: strings.Repeat("reasoning words ", 20)},
	{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
		{Name: "Edit", Summary: "main.go", CallID: "c1"},
	}},
	{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{
		{CallID: "c1", Verb: "wrote", Summary: "main.go"},
	}},
	{Type: "agent.command_output", Output: "ok\n"},
	{Type: "agent.usage", Raw: json.RawMessage(`{"input_tokens":10}`)},
	{Type: "agent.raw", Line: `{"type":"stream_event"}`},
	{Type: "agent.raw", Line: `{"type":"system","subtype":"compact_boundary"}`},
	{Type: "agent.output", Text: "here is the **answer**"},
	{Type: "vincent.command_started", Raw: json.RawMessage(`{"command":"go test ./..."}`)},
	{Type: "command.output", Text: "PASS"},
	{Type: "command.output", Text: "a warning", Stream: "stderr"},
	{Type: "agent.error", Message: "the model refused"},
	{
		Type:          "agent.result",
		ResultText:    "here is the answer",
		DurationMS:    1200,
		APIDurationMS: 900,
		NumTurns:      3,
		StopReason:    "end_turn",
		CostUSD:       &quietCost,
	},
}

// levelGolden is levelFixture rendered at eighty columns, one entry per
// level. The three loud ones were captured from the code as it stood before
// quiet existed and must not move; quiet's is the new rule.
var levelGolden = map[outputLevel]string{
	// Nothing the agent narrated about itself: no tool call, no outcome for
	// it, no arithmetic about lines vincent could not parse, and no `done ·`
	// summary of a run that succeeded. The failure stays, and so does the
	// command step's own output — a command step narrates nothing, so its
	// pane is byte-identical to compact's.
	levelQuiet: "  here is the answer\n" +
		"$ go test ./...\n" +
		"  PASS\n" +
		"  a warning\n" +
		"✗ the model refused",

	levelCompact: "▸ Edit main.go\n" +
		"    ✓ wrote · main.go\n" +
		"  … 2 unrecognized line(s) (v)\n" +
		"\n" +
		"  here is the answer\n" +
		"$ go test ./...\n" +
		"  PASS\n" +
		"  a warning\n" +
		"✗ the model refused\n" +
		"✓ done · $0.01",

	levelNormal: "# /w/tree · 2 tools: Edit, Bash\n" +
		"☰ ✓ read the file · ○ write the fix\n" +
		"· reasoning words reasoning words reasoning words reasoning words reasoning\n" +
		"  words reasoning words reasoning words reasoning words reasoning words\n" +
		"  reasoning words reasoning words reasoning words reasoning words reasoning\n" +
		"  … +2 lines (v)\n" +
		"▸ Edit main.go\n" +
		"    ✓ wrote · main.go\n" +
		"  … 2 unrecognized line(s) (v)\n" +
		"\n" +
		"  here is the answer\n" +
		"$ go test ./...\n" +
		"  PASS\n" +
		"  a warning\n" +
		"✗ the model refused\n" +
		"✓ done · 1.2s · 3 turns · $0.01",

	levelVerbose: "# /w/tree · 2 tools: Edit, Bash\n" +
		"☰ ✓ read the file · ○ write the fix\n" +
		"· reasoning words reasoning words reasoning words reasoning words reasoning\n" +
		"  words reasoning words reasoning words reasoning words reasoning words\n" +
		"  reasoning words reasoning words reasoning words reasoning words reasoning\n" +
		"  words reasoning words reasoning words reasoning words reasoning words\n" +
		"  reasoning words reasoning words\n" +
		"▸ Edit main.go\n" +
		"    ✓ wrote · main.go\n" +
		"  ok\n" +
		"  {\"input_tokens\":10}\n" +
		"  {\"type\":\"stream_event\"}\n" +
		"  {\"type\":\"system\",\"subtype\":\"compact_boundary\"}\n" +
		"\n" +
		"  here is the answer\n" +
		"$ go test ./...\n" +
		"  PASS\n" +
		"  a warning\n" +
		"✗ the model refused\n" +
		"✓ done · 1.2s (0.9s api) · 3 turns · $0.01",
}

// renderAt is levelFixture through the task workspace's own pane.
func renderAt(t *testing.T, level outputLevel) string {
	t.Helper()
	d := newTestDetail(t)
	d.width = 80
	d.records = levelFixture
	d.level.set(level)
	return strings.Join(plainLines(d.outputLines()), "\n")
}

// TestLoudLevelsRenderWhatTheyAlwaysDid is the renumbering's regression net.
// Adding a level below compact shifted every constant up by one, so a guard
// that meant "compact only" and a guard that meant "compact and quieter" are
// now different code — and a whole-output golden is what tells them apart,
// which a spot check for one substring would not.
func TestLoudLevelsRenderWhatTheyAlwaysDid(t *testing.T) {
	for _, level := range []outputLevel{levelCompact, levelNormal, levelVerbose} {
		if got := renderAt(t, level); got != levelGolden[level] {
			t.Errorf("%s moved\n got:\n%s\nwant:\n%s", level, got, levelGolden[level])
		}
	}
}

// TestQuietRendersOnlyProseAndFailures holds the new level's whole output in
// the task workspace.
func TestQuietRendersOnlyProseAndFailures(t *testing.T) {
	got := renderAt(t, levelQuiet)
	if got != levelGolden[levelQuiet] {
		t.Fatalf("quiet\n got:\n%s\nwant:\n%s", got, levelGolden[levelQuiet])
	}
	for _, gone := range []string{"▸ Edit", "wrote", "unrecognized line(s)", "done ·"} {
		if strings.Contains(got, gone) {
			t.Errorf("quiet still shows %q:\n%s", gone, got)
		}
	}
}

// TestQuietInAChatHidesTheSameThings is the pointer-shared holder's other
// half: the chat body is the output pane, so quiet has to mean there exactly
// what it means in a task workspace.
func TestQuietInAChatHidesTheSameThings(t *testing.T) {
	v := chatWithRecords(t)
	v.level.set(levelQuiet)
	body := strings.Join(plainLines(v.bodyLines(80)), "\n")

	hidden := []string{
		"Edit", "wrote", "unrecognized line(s)", "done ·",
		"/w/tree", "reasoning words", "hook_created",
	}
	for _, gone := range hidden {
		if strings.Contains(body, gone) {
			t.Errorf("quiet in a chat still shows %q:\n%s", gone, body)
		}
	}
	// The turn frame and the answer are what is left.
	for _, want := range []string{"── turn 1 ──", "here is the answer"} {
		if !strings.Contains(body, want) {
			t.Errorf("quiet in a chat dropped %q:\n%s", want, body)
		}
	}
}

// TestQuietLeavesNoTraceOfAnUnrecognizedLine is the one rule quiet changes
// rather than adds below: everywhere else an unparsed line leaves a count,
// because the count is the offer to expand. Quiet makes no offers, so an
// agent.raw record contributes nothing whatsoever — not the line, not a
// number, not a blank row where it would have gone.
func TestQuietLeavesNoTraceOfAnUnrecognizedLine(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "before"},
		{Type: "agent.raw", Line: `{"type":"stream_event"}`},
		{Type: "agent.raw", Line: `{"type":"system"}`},
	}
	without := []apiclient.TranscriptRecord{{Type: "agent.output", Text: "before"}}

	for _, opts := range []lineOpts{{expandKey: "v"}, {expandKey: chatExpandKey}} {
		got := plainLines(outputLines(recs, levelQuiet, 80, opts))
		want := plainLines(outputLines(without, levelQuiet, 80, opts))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: the raw records left a trace\n got: %q\nwant: %q",
				opts.expandKey, got, want)
		}
	}
}

// TestQuietKeepsFailuresAndSilentTurns pins the settled decision about
// renderResult: only the success outcome goes. A display level does not hide
// a failure — in a step run there is no other line carrying it — and it does
// not reduce a turn that produced no prose at all to a bare separator.
func TestQuietKeepsFailuresAndSilentTurns(t *testing.T) {
	body := func(recs []apiclient.TranscriptRecord) string {
		return strings.Join(plainLines(
			outputLines(recs, levelQuiet, 80, lineOpts{expandKey: "v"})), "\n")
	}

	if got := body([]apiclient.TranscriptRecord{
		{Type: "agent.error", Message: "context deadline exceeded"},
	}); !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("quiet hid an agent.error: %q", got)
	}

	// A failed result, with nothing else in the run to carry the reason.
	if got := body([]apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "working on it"},
		{Type: "agent.result", IsError: true, Message: "the run failed hard"},
	}); !strings.Contains(got, "the run failed hard") {
		t.Errorf("quiet hid a failed result: %q", got)
	}

	// A codex turn with no agent_message: the result text is the only thing
	// the turn ever said, and hiding it would leave the turn blank.
	if got := body([]apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Edit"}}},
		{Type: "agent.result", ResultText: "all done, nothing to report"},
	}); !strings.Contains(got, "all done, nothing to report") {
		t.Errorf("quiet hid the fallback result text: %q", got)
	}

	// And the outcome form, which is the one that goes.
	if got := body([]apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "the answer"},
		{Type: "agent.result", ResultText: "the answer", DurationMS: 1200},
	}); strings.Contains(got, "done") {
		t.Errorf("quiet kept the success outcome: %q", got)
	}
}

// TestQuietKeepsAChatTurnsFailureLine covers the other place a chat says
// something went wrong: the turn row's own FailReason, drawn beside the
// records rather than among them. It survives quiet for the reason a failed
// result does.
func TestQuietKeepsAChatTurnsFailureLine(t *testing.T) {
	v := chatViewFixture()
	v.level.set(levelQuiet)
	v.turns = []apiclient.ChatTurn{{
		ID: 9, Seq: 1, State: "failed", Prompt: "ask",
		FailReason: "agent_error", ErrorMessage: "cursor-agent exited 1",
	}}
	body := strings.Join(plainLines(v.bodyLines(80)), "\n")
	for _, want := range []string{"agent_error", "cursor-agent exited 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("quiet hid a turn's failure line, want %q:\n%s", want, body)
		}
	}
}

// TestQuietDoesNotTouchACommandStep: quiet is a rule about what the *agent*
// narrated, and a command step narrates nothing, so its pane is the same
// bytes at quiet as at compact.
func TestQuietDoesNotTouchACommandStep(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "vincent.command_started", Raw: json.RawMessage(`{"command":"go build ./..."}`)},
		{Type: "command.output", Text: "building"},
		{Type: "command.output", Text: "vet: bad news", Stream: "stderr"},
		{Type: "vincent.output", Text: "done"},
	}
	quiet := plainLines(outputLines(recs, levelQuiet, 80, lineOpts{expandKey: "v"}))
	compact := plainLines(outputLines(recs, levelCompact, 80, lineOpts{expandKey: "v"}))
	if strings.Join(quiet, "\n") != strings.Join(compact, "\n") {
		t.Errorf("a command step differs by level\nquiet:   %q\ncompact: %q", quiet, compact)
	}
	if !strings.Contains(strings.Join(quiet, "\n"), "vet: bad news") {
		t.Errorf("quiet dropped a command step's stderr: %q", quiet)
	}
}

// TestBothPanesOpenAtNormalAndNameQuiet: the default did not move — a fourth
// level below the old floor must not change what anyone's first screen shows
// — and quiet, being a level other than the default, is named where compact
// and verbose already are.
func TestBothPanesOpenAtNormalAndNameQuiet(t *testing.T) {
	d := newTestDetail(t)
	v := chatViewFixture()
	if d.level.get() != levelNormal || v.level.get() != levelNormal {
		t.Fatalf("panes opened at %s (task) and %s (chat), want normal",
			d.level.get(), v.level.get())
	}
	title := func() string { return plainLines([]string{d.outputTitle()})[0] }
	header := func() string { return plainLines([]string{v.headerLine(80)})[0] }
	if strings.Contains(title(), "quiet") || strings.Contains(header(), "quiet") {
		t.Fatal("a pane named quiet while on normal")
	}
	d.level.set(levelQuiet)
	v.level.set(levelQuiet)
	if !strings.Contains(title(), "quiet") {
		t.Errorf("the output pane's title does not name quiet: %q", title())
	}
	if !strings.Contains(header(), "quiet") {
		t.Errorf("the chat header does not name quiet: %q", header())
	}
}

// TestCopyDocsIgnoreTheLevel: what a reader may copy is the assistant
// documents in the window, and that has never depended on how much of them
// is on screen (task 076). Quiet, which hides more than any other level,
// is the strongest statement of it.
func TestCopyDocsIgnoreTheLevel(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = levelFixture
	var want []copyItem
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
		d.level.set(level)
		_ = d.outputLines()
		got := copyDocs(copyDocsFromRecords(d.records, d.recordSeqs()))
		if len(got) == 0 {
			t.Fatalf("%s: no assistant document is offered for copying", level)
		}
		if want == nil {
			want = got
			continue
		}
		if len(got) != len(want) {
			t.Errorf("%s offers %d documents, want %d", level, len(got), len(want))
		}
	}
}
