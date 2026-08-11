package tui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// escapes strips SGR sequences so a test can assert on what a reader sees.
var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = escapes.ReplaceAllString(l, "")
	}
	return out
}

// TestWrapLineHangingIndent is the reason the pane wraps itself instead of
// setting the viewport's SoftWrap: SoftWrap folds continuations to column 0,
// where a wrapped reasoning line is indistinguishable from assistant prose.
func TestWrapLineHangingIndent(t *testing.T) {
	pl := paneLine{
		gutter:      gutterThinking,
		gutterStyle: styleThinking,
		segs:        []segment{{text: "one two three four five six", style: styleThinking}},
	}
	got := plainLines(wrapLine(pl, 12))
	want := []string{"· one two", "  three four", "  five six"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapped = %q, want %q", got, want)
	}
}

// TestWrapLineNoTrailingSpace guards the separator handling: a space emitted
// before a word that turns out not to fit leaves the line ending in
// whitespace, which the terminal's own text selection then picks up.
func TestWrapLineNoTrailingSpace(t *testing.T) {
	pl := paneLine{
		gutter:      gutterNone,
		gutterStyle: lipgloss.NewStyle(),
		segs:        []segment{{text: "alpha beta gamma delta", style: lipgloss.NewStyle()}},
	}
	for _, line := range plainLines(wrapLine(pl, 14)) {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %q ends in whitespace", line)
		}
	}
}

// TestWrapLineHardSplitsLongWords covers the case that reintroduces clipping
// if it is missed: a path or a token with no spaces in it, wider than the
// pane.
func TestWrapLineHardSplitsLongWords(t *testing.T) {
	long := strings.Repeat("x", 25)
	pl := paneLine{
		gutter:      gutterNone,
		gutterStyle: lipgloss.NewStyle(),
		segs:        []segment{{text: long, style: lipgloss.NewStyle()}},
	}
	lines := plainLines(wrapLine(pl, 12))
	if len(lines) < 3 {
		t.Fatalf("lines = %q, want the word split across the pane", lines)
	}
	var joined string
	for _, l := range lines {
		if w := len([]rune(l)); w > 12 {
			t.Errorf("line %q is %d columns, wider than the pane", l, w)
		}
		joined += strings.TrimLeft(l, " ")
	}
	if joined != long {
		t.Errorf("split lost or added characters: %q", joined)
	}
}

// TestWrapLineKeepsMultiByteRunesWhole pins that widths count runes: a
// byte-wise split would cut a rune in half and emit invalid UTF-8.
func TestWrapLineKeepsMultiByteRunesWhole(t *testing.T) {
	pl := paneLine{
		gutter:      gutterNone,
		gutterStyle: lipgloss.NewStyle(),
		segs:        []segment{{text: strings.Repeat("é", 20), style: lipgloss.NewStyle()}},
	}
	for _, l := range plainLines(wrapLine(pl, 10)) {
		if !strings.HasPrefix(strings.TrimLeft(l, " "), "é") {
			t.Errorf("line %q does not begin on a rune boundary", l)
		}
	}
}

// TestWrapLineJoinsGutterToFirstWord pins that "▸ Edit" is contiguous text.
// A style boundary between the marker and the name puts an escape sequence
// mid-phrase: invisible to a reader, and it breaks anything that searches the
// rendered pane.
func TestWrapLineJoinsGutterToFirstWord(t *testing.T) {
	rendered := wrapLine(toolUsePane([]apiclient.TranscriptTool{
		{Name: "Edit", Summary: "internal/auth/token.go"},
	}), 80)
	if len(rendered) != 1 {
		t.Fatalf("lines = %q, want one", rendered)
	}
	if !strings.Contains(rendered[0], "▸ Edit") {
		t.Errorf("marker and name are not contiguous: %q", rendered[0])
	}
	// The subject is styled apart from the name, which is the point of
	// segments — but the space between them survives.
	if !strings.Contains(escapes.ReplaceAllString(rendered[0], ""), "▸ Edit internal/auth/token.go") {
		t.Errorf("rendered = %q, want name and subject separated by one space", rendered[0])
	}
}

// TestThinkingLevels covers the level matrix for reasoning: hidden, truncated
// after wrapping, then whole.
func TestThinkingLevels(t *testing.T) {
	text := strings.Repeat("reasoning words ", 30)

	if got := thinkingBlock(text, levelCompact, 40); got != nil {
		t.Errorf("compact rendered %d lines, want none", len(got))
	}

	normal := plainLines(thinkingBlock(text, levelNormal, 40))
	if len(normal) != thinkingLines+1 {
		t.Fatalf("normal = %d lines, want %d plus the count", len(normal), thinkingLines)
	}
	// Truncation is counted in *display* lines, so it means the same thing
	// for claude's paragraphs and cursor's one coalesced run.
	if !strings.Contains(normal[len(normal)-1], "… +") || !strings.Contains(normal[len(normal)-1], "(v)") {
		t.Errorf("last line = %q, want a count naming the key that expands it", normal[len(normal)-1])
	}

	verbose := thinkingBlock(text, levelVerbose, 40)
	if len(verbose) <= thinkingLines+1 {
		t.Errorf("verbose = %d lines, want the whole block", len(verbose))
	}
	for _, l := range plainLines(verbose) {
		if strings.Contains(l, "… +") {
			t.Errorf("verbose still truncated: %q", l)
		}
	}
}

// TestOutputLevelCycle pins the order the key advances through.
func TestOutputLevelCycle(t *testing.T) {
	got := []outputLevel{levelNormal}
	for range 3 {
		got = append(got, got[len(got)-1].next())
	}
	want := []outputLevel{levelNormal, levelVerbose, levelCompact, levelNormal}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cycle = %v, want %v", got, want)
		}
	}
}

// TestRawLinesReachableAtVerbose is the half of T4.16's done-when that the
// collapsed count alone did not satisfy: the lines have to be *reachable*,
// not merely counted.
func TestRawLinesReachableAtVerbose(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.raw", Line: `{"type":"system","subtype":"compact_boundary"}`},
		{Type: "agent.raw", Line: `{"type":"stream_event"}`},
	}

	d.level = levelNormal
	collapsed := strings.Join(plainLines(d.outputLines()), "\n")
	if !strings.Contains(collapsed, "… 2 unrecognized line(s) (v)") {
		t.Errorf("normal = %q, want the collapsed count naming the key", collapsed)
	}
	if strings.Contains(collapsed, "compact_boundary") {
		t.Errorf("normal leaked a raw line: %q", collapsed)
	}

	d.level = levelVerbose
	expanded := strings.Join(plainLines(d.outputLines()), "\n")
	for _, want := range []string{"compact_boundary", "stream_event"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("verbose = %q, want it to contain %q", expanded, want)
		}
	}
}

// TestUsageOnlyAtVerbose: the timeline row already carries the numbers, so
// usage stays out of the tail until the level means "show me the machine".
func TestUsageOnlyAtVerbose(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.usage", Raw: json.RawMessage(`{"input_tokens":10}`)},
	}
	for _, level := range []outputLevel{levelCompact, levelNormal} {
		d.level = level
		if got := d.outputLines(); len(got) != 0 {
			t.Errorf("%s rendered usage: %q", level, got)
		}
	}
	d.level = levelVerbose
	if got := strings.Join(plainLines(d.outputLines()), "\n"); !strings.Contains(got, "input_tokens") {
		t.Errorf("verbose = %q, want the adapter-native payload", got)
	}
}

// TestToolResultsIndentUnderTheirCall covers the gutter arrangement: an
// outcome is indented under the invocation, and a failure is marked as one.
func TestToolResultsIndentUnderTheirCall(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
			{Name: "Bash", Summary: "go test ./...", CallID: "t1"},
		}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{
			{CallID: "t1", Summary: "exit 1", IsError: true},
		}},
	}
	got := plainLines(d.outputLines())
	if len(got) != 2 {
		t.Fatalf("lines = %q, want the call and its outcome", got)
	}
	if got[0] != "▸ Bash go test ./..." {
		t.Errorf("call = %q", got[0])
	}
	if got[1] != "    ✗ exit 1" {
		t.Errorf("outcome = %q, want it indented under the call and marked failed", got[1])
	}
}

// TestToolResultWithoutSummaryStillSaysWhether covers the cursor case that
// has never been captured: a completion with no `success` payload reports
// failure with no detail rather than inventing one.
func TestToolResultWithoutSummaryStillSaysWhether(t *testing.T) {
	ok := plainLines(wrapLine(toolResultLine(apiclient.TranscriptToolResult{}), 40))
	if strings.TrimSpace(ok[0]) != "✓ done" {
		t.Errorf("bare success = %q", ok[0])
	}
	bad := plainLines(wrapLine(toolResultLine(apiclient.TranscriptToolResult{IsError: true}), 40))
	if strings.TrimSpace(bad[0]) != "✗ failed" {
		t.Errorf("bare failure = %q", bad[0])
	}
}

// TestTurnSeparation pins the blank line: assistant prose that follows
// anything else is a new turn, and consecutive prose is not.
func TestTurnSeparation(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "first"},
		{Type: "agent.output", Text: "still first"},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", CallID: "t1"}}},
		{Type: "agent.output", Text: "second turn"},
	}
	got := plainLines(d.outputLines())
	want := []string{"  first", "  still first", "▸ Bash", "", "  second turn"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// TestResultTextNotRepeated is the de-duplication rule and its exception.
// Cursor's result text is every assistant message of the turn concatenated,
// so printing it after the messages is the whole run twice.
func TestResultTextNotRepeated(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80

	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "the answer is 42"},
		{Type: "agent.result", ResultText: "the answer is 42"},
	}
	got := strings.Join(plainLines(d.outputLines()), "\n")
	if strings.Count(got, "the answer is 42") != 1 {
		t.Errorf("result text repeated:\n%s", got)
	}

	// Nothing else rendered — a codex turn with no agent_message — so the
	// result text is the only content there is and it stays.
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.result", ResultText: "the answer is 42"},
	}
	got = strings.Join(plainLines(d.outputLines()), "\n")
	if !strings.Contains(got, "the answer is 42") {
		t.Errorf("result text dropped with nothing else on screen:\n%s", got)
	}

	// An error keeps its text at all times: it is the error, and it may be
	// the only thing the run ever said.
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "trying"},
		{Type: "agent.result", IsError: true, Message: "model not found"},
	}
	got = strings.Join(plainLines(d.outputLines()), "\n")
	if !strings.Contains(got, "model not found") {
		t.Errorf("error message suppressed:\n%s", got)
	}
}

// TestVCyclesFromEitherFocus pins where the key is handled: `v` acts on the
// output pane, and requiring the pane to be focused first is a step nobody
// would guess at — the same reason `f` and `d` are handled before the focus
// switch.
func TestVCyclesFromEitherFocus(t *testing.T) {
	for _, focus := range []detailFocus{focusTimeline, focusOutput} {
		d := newTestDetail(t)
		d.focus = focus
		if d.level != levelNormal {
			t.Fatalf("fresh detail starts at %s, want normal", d.level)
		}
		d.updateKey(tea.KeyPressMsg{Code: 'v', Text: "v"})
		if d.level != levelVerbose {
			t.Errorf("focus %v: level = %s after v, want verbose", focus, d.level)
		}
		if !d.outputDirty {
			t.Errorf("focus %v: pane not marked stale, so the change would not repaint", focus)
		}
	}
}

// TestLevelSurvivesTaskSwitch is the requirement behind making the level
// session state: switching what you are looking at must not silently reset
// how much of it you asked to see.
func TestLevelSurvivesTaskSwitch(t *testing.T) {
	d := newTestDetail(t)
	d.updateKey(tea.KeyPressMsg{Code: 'v', Text: "v"})
	want := d.level
	d.open(d.taskID+1, "running")
	if d.level != want {
		t.Errorf("level = %s after switching task, want %s", d.level, want)
	}
}

// TestOutputTitleNamesNonDefaultLevel: `v` is the one key here whose effect
// can be invisible — on a run with no reasoning and no unrecognized lines it
// changes nothing on screen — so the title has to say the level changed.
func TestOutputTitleNamesNonDefaultLevel(t *testing.T) {
	d := newTestDetail(t)
	if got := escapes.ReplaceAllString(d.outputTitle(), ""); strings.Contains(got, "normal") {
		t.Errorf("title = %q, want the default level unnamed", got)
	}
	d.level = levelVerbose
	if got := escapes.ReplaceAllString(d.outputTitle(), ""); !strings.Contains(got, "verbose") {
		t.Errorf("title = %q, want it to name the level", got)
	}
}
