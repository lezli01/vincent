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

// TestRunHeaderLevels covers the record task 066 added to the pane. It is the
// run's frame rather than something the agent said or did, so levelCompact —
// whose stated meaning is "what the agent said and did, nothing else" — does
// not grow, and it renders identically at normal and verbose.
func TestRunHeaderLevels(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{{
		Type:           "agent.run_header",
		WorkDir:        "/work/repo",
		AvailableTools: []string{"Task", "Bash", "Write"},
	}}

	d.level = levelCompact
	if got := d.outputLines(); len(got) != 0 {
		t.Errorf("compact rendered the run header: %q", got)
	}

	var atNormal string
	for _, level := range []outputLevel{levelNormal, levelVerbose} {
		d.level = level
		got := plainLines(d.outputLines())
		if len(got) != 1 {
			t.Fatalf("%s = %q, want one line", level, got)
		}
		want := "# /work/repo · 3 tools: Task, Bash, Write"
		if got[0] != want {
			t.Errorf("%s = %q, want %q", level, got[0], want)
		}
		if level == levelNormal {
			atNormal = got[0]
		} else if got[0] != atNormal {
			t.Errorf("verbose = %q, normal = %q: the header does not grow", got[0], atNormal)
		}
	}
}

// TestRunHeaderWrapsRatherThanClips is the case a real run produces: claude
// hands a step a dozen tools, and the list is past 80 columns before it is
// half printed. It must fold to the hanging indent, which is the whole reason
// this pane wraps its own lines.
func TestRunHeaderWrapsRatherThanClips(t *testing.T) {
	tools := []string{
		"Task", "AskUserQuestion", "Bash", "BashOutput", "Edit", "Glob",
		"Grep", "KillShell", "NotebookEdit", "Read", "TodoWrite", "WebFetch",
		"WebSearch", "Write",
	}
	d := newTestDetail(t)
	d.width = 80
	d.level = levelNormal
	d.records = []apiclient.TranscriptRecord{{
		Type:           "agent.run_header",
		WorkDir:        "/work/repo",
		AvailableTools: tools,
	}}
	got := plainLines(d.outputLines())
	if len(got) < 2 {
		t.Fatalf("lines = %q, want the tool list wrapped", got)
	}
	for i, line := range got {
		if cols(line) > 80 {
			t.Errorf("line %d is %d columns: %q", i, cols(line), line)
		}
		if i > 0 && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation %d = %q, want the hanging indent", i, line)
		}
	}
	// Nothing was dropped on the way: clipping is what this replaces.
	joined := strings.Join(got, " ")
	for _, tool := range tools {
		if !strings.Contains(joined, tool) {
			t.Errorf("tool %q clipped out of %q", tool, joined)
		}
	}
}

// TestResultMetadataByLevel is the level contract for the enriched result
// line (task 066). Compact is byte-identical to what it rendered before —
// the acceptance criterion that "compact does not grow" — normal carries the
// run's condensed account of itself, and the per-model and cache breakdown is
// verbose only.
func TestResultMetadataByLevel(t *testing.T) {
	cost := 0.02206225
	modelCost := 0.02206225
	rec := apiclient.TranscriptRecord{
		Type:             "agent.result",
		ResultText:       "all done",
		CostUSD:          &cost,
		DurationMS:       7324,
		APIDurationMS:    5706,
		NumTurns:         2,
		StopReason:       "end_turn",
		TerminalReason:   "completed",
		CacheReadTokens:  60280,
		CacheWriteTokens: 6835,
		ModelUsage: []apiclient.TranscriptModelUsage{{
			Model: "claude-haiku-4-5", InputTokens: 18, OutputTokens: 536,
			CacheReadTokens: 60280, CostUSD: &modelCost,
		}},
	}
	d := newTestDetail(t)
	d.width = 100
	// An assistant line first, so the result renders its outcome rather than
	// repeating its text (T4.16).
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: "all done"}, rec}

	d.level = levelCompact
	compact := plainLines(d.outputLines())
	if compact[len(compact)-1] != "✓ done · $0.02" {
		t.Errorf("compact = %q, want exactly what it rendered before task 066",
			compact[len(compact)-1])
	}

	d.level = levelNormal
	normal := plainLines(d.outputLines())
	if got := normal[len(normal)-1]; got != "✓ done · 7.3s · 2 turns · $0.02" {
		t.Errorf("normal = %q", got)
	}
	// The ordinary stop reasons stay off the line: every successful claude
	// run ends end_turn/completed, so printing them says nothing.
	for _, line := range normal {
		if strings.Contains(line, "end_turn") || strings.Contains(line, "completed") ||
			strings.Contains(line, "cache") || strings.Contains(line, "claude-haiku") {
			t.Errorf("normal carries a verbose-only detail: %q", line)
		}
	}

	d.level = levelVerbose
	verbose := strings.Join(plainLines(d.outputLines()), "\n")
	for _, want := range []string{
		"✓ done · 7.3s (5.7s api) · 2 turns · $0.02",
		"cache 60280 read / 6835 written",
		"claude-haiku-4-5 · 18 in / 536 out · 60280 cached · $0.02",
	} {
		if !strings.Contains(verbose, want) {
			t.Errorf("verbose missing %q:\n%s", want, verbose)
		}
	}
}

// TestResultNamesAnUnusualStop covers what the metadata is *for*: a run that
// hit a limit and one that finished read identically before task 066.
func TestResultNamesAnUnusualStop(t *testing.T) {
	d := newTestDetail(t)
	d.width = 100
	d.level = levelNormal
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "working"},
		{
			Type: "agent.result", StopReason: "max_tokens", TerminalReason: "interrupted",
			NumTurns: 1, PermissionDenials: []apiclient.TranscriptPermissionDenial{
				{ToolName: "Write", CallID: "toolu_02"},
			},
		},
	}
	got := plainLines(d.outputLines())
	last := got[len(got)-1]
	for _, want := range []string{"stop: max_tokens", "interrupted", "1 denied"} {
		if !strings.Contains(last, want) {
			t.Errorf("result = %q, want it to carry %q", last, want)
		}
	}
}

// TestBlockedToolResultHasItsOwnMark separates the two verdicts a reader acts
// on differently: a tool that ran and failed is the agent's problem, a tool a
// permission rule refused is the step's permission mode.
func TestBlockedToolResultHasItsOwnMark(t *testing.T) {
	blocked := plainLines(wrapLine(toolResultLine(apiclient.TranscriptToolResult{
		Summary: "permission denied", Blocked: true, IsError: true,
	}), 60))
	if strings.TrimSpace(blocked[0]) != "⊘ permission denied" {
		t.Errorf("blocked = %q, want the ⊘ mark", blocked[0])
	}
	bare := plainLines(wrapLine(toolResultLine(apiclient.TranscriptToolResult{
		Blocked: true, IsError: true,
	}), 60))
	if strings.TrimSpace(bare[0]) != "⊘ blocked" {
		t.Errorf("bare blocked = %q, want it to say so", bare[0])
	}
	// The verb leads the tool's own prose about what it did.
	verb := plainLines(wrapLine(toolResultLine(apiclient.TranscriptToolResult{
		Verb: "created", Summary: "File created successfully at: hello.txt",
	}), 80))
	if strings.TrimSpace(verb[0]) != "✓ created · File created successfully at: hello.txt" {
		t.Errorf("verb = %q", verb[0])
	}
}

// TestPlanFromNormalUp: a plan is what the agent *intends*, which is neither
// what it said nor what it ran, so levelCompact stays byte-identical to what
// it rendered before task 070 and the list appears from normal up.
func TestPlanFromNormalUp(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	plan := apiclient.TranscriptRecord{
		Type:       "agent.plan",
		PlanCallID: "item_1",
		Items: []apiclient.TranscriptPlanItem{
			{Text: "Run `ls -la`", Completed: true},
			{Text: "Append to notes.txt"},
		},
	}
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: "working"}, plan}

	d.level = levelCompact
	compact := plainLines(d.outputLines())
	if len(compact) != 1 || strings.TrimSpace(compact[0]) != "working" {
		t.Errorf("compact = %q, want only the agent's own words", compact)
	}
	for _, level := range []outputLevel{levelNormal, levelVerbose} {
		d.level = level
		got := plainLines(d.outputLines())
		if len(got) != 2 {
			t.Fatalf("%s = %q, want the output and the plan", level, got)
		}
		if !strings.HasPrefix(got[1], "☰ ✓ Run `ls -la`") ||
			!strings.HasSuffix(got[1], "○ Append to notes.txt") {
			t.Errorf("%s plan = %q, want done items ticked and pending ones not", level, got[1])
		}
	}
}

// TestLongPlanWraps: a plan longer than the pane wraps to the hanging indent
// under its gutter rather than clipping, which is the whole reason it is one
// paneLine rather than pre-joined text.
func TestLongPlanWraps(t *testing.T) {
	d := newTestDetail(t)
	d.width = 40
	d.level = levelNormal
	d.records = []apiclient.TranscriptRecord{{Type: "agent.plan", Items: []apiclient.TranscriptPlanItem{
		{Text: "read the whole specification carefully", Completed: true},
		{Text: "then rewrite the parser and its tests"},
	}}}
	got := plainLines(d.outputLines())
	if len(got) < 2 {
		t.Fatalf("lines = %q, want the plan wrapped across several", got)
	}
	for i, line := range got {
		if len([]rune(line)) > d.width {
			t.Errorf("line %d overflows the pane: %q", i, line)
		}
		if i > 0 && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation %d = %q, want the hanging indent", i, line)
		}
	}
	if !strings.Contains(strings.Join(got, ""), "rewrite the parser") {
		t.Errorf("plan clipped: %q", got)
	}
}

// TestCommandOutputOnlyAtVerbose: the output body is the one record a step
// running `go test ./...` could flood the pane with, so it is absent below
// verbose (task 070 decision 2). Truncation is stated when it happened —
// output that stops and says nothing is indistinguishable from a command
// that printed exactly that much.
func TestCommandOutputOnlyAtVerbose(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.command_output", CallID: "item_2", Output: "total 8\n", Truncated: true},
	}
	for _, level := range []outputLevel{levelCompact, levelNormal} {
		d.level = level
		if got := d.outputLines(); len(got) != 0 {
			t.Errorf("%s rendered a command's output body: %q", level, got)
		}
	}
	d.level = levelVerbose
	got := plainLines(d.outputLines())
	if len(got) != 2 || !strings.Contains(got[0], "total 8") {
		t.Fatalf("verbose = %q, want the body", got)
	}
	if !strings.Contains(got[1], "truncated") {
		t.Errorf("truncation is silent: %q", got)
	}
}
