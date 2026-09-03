package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's verbosity (task 071). What is under test is that the
// conversation body *is* the output pane — same records, same renderer, same
// level — and that the key which cycles it is one the composer does not want.

// longRaw is an unmodeled dialect line well past the 200 columns the old chat
// renderer clipped every raw line at.
var longRaw = `{"type":"hook_created","payload":"` + strings.Repeat("x", 300) + `"}`

// unwrap folds a wrapped pane back into one string, so a test can assert that
// a long line survived whole without asserting where it broke.
func unwrap(s string) string { return strings.Join(strings.Fields(s), "") }

// chatWithRecords is a chat holding one finished turn whose records cover the
// record types §15 gives per-level rules for.
func chatWithRecords(t *testing.T) *chatView {
	t.Helper()
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done", Prompt: "ask"}}
	v.turnRecords[1] = []apiclient.TranscriptRecord{
		{Type: "agent.run_header", WorkDir: "/w/tree", AvailableTools: []string{"Edit"}},
		{Type: "agent.thinking", Text: strings.Repeat("reasoning words ", 40)},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Edit", Summary: "main.go"}}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{Verb: "wrote", Summary: "main.go"}}},
		{Type: "agent.raw", Line: longRaw},
		{Type: "agent.output", Text: "here is the answer"},
		{Type: "agent.result", ResultText: "here is the answer", DurationMS: 1200},
	}
	return v
}

// TestChatLevelsMeanWhatTheSpecSays holds §15's three levels inside a chat.
// The records the old chatrender.go switch dropped outright — tool results,
// the run header, the result line — are reachable here at the levels §15
// already specifies for each.
func TestChatLevelsMeanWhatTheSpecSays(t *testing.T) {
	v := chatWithRecords(t)
	body := func(l outputLevel) string {
		v.level.set(l)
		return strings.Join(plainLines(v.bodyLines(80)), "\n")
	}

	compact := body(levelCompact)
	if strings.Contains(compact, "reasoning words") {
		t.Error("compact showed reasoning")
	}
	if strings.Contains(compact, "hook_created") {
		t.Error("compact showed an unrecognized line")
	}
	if strings.Contains(compact, "/w/tree") {
		t.Error("compact showed the run header")
	}
	// What the agent said and did, and nothing else — but that much, always.
	for _, want := range []string{"here is the answer", "Edit", "wrote"} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact dropped %q:\n%s", want, compact)
		}
	}
	// The count is the affordance that says there is more, and it names the
	// key that shows it — ctrl+r here, never `v` (decision 7).
	if !strings.Contains(compact, "unrecognized line(s) (ctrl+r)") {
		t.Errorf("compact did not offer ctrl+r for the collapsed lines:\n%s", compact)
	}

	normal := body(levelNormal)
	if !strings.Contains(normal, "/w/tree") || !strings.Contains(normal, "Edit") {
		t.Errorf("normal dropped the run header:\n%s", normal)
	}
	if !strings.Contains(normal, "reasoning words") {
		t.Errorf("normal dropped reasoning entirely:\n%s", normal)
	}
	if !strings.Contains(normal, "lines (ctrl+r)") {
		t.Errorf("normal did not truncate reasoning behind a count:\n%s", normal)
	}

	verbose := body(levelVerbose)
	// The line is wrapped, not clipped: the old renderer cut every raw line
	// at 200 columns and there was no level that showed the rest.
	if !strings.Contains(unwrap(verbose), unwrap(longRaw)) {
		t.Errorf("verbose clipped the unrecognized line instead of showing it whole:\n%s", verbose)
	}
	if strings.Contains(verbose, "lines (ctrl+r)") {
		t.Errorf("verbose still collapsed reasoning:\n%s", verbose)
	}
}

// TestChatLevelIsTheOutputPanesLevel is decision 3: one holder, two views. A
// reader who asked for verbose in a task keeps it in a chat and the other way
// round, and leaving a chat and returning changes nothing.
func TestChatLevelIsTheOutputPanesLevel(t *testing.T) {
	level := newLevelHolder()
	raw := newRawHolder()
	d := newDetail(testCtx(t), level, raw)
	v := newChatView(level, raw)
	v.chatID = 1

	if _, cmd := v.updateKey(registryKey(t, "ctrl+r")); cmd != nil {
		drain(cmd)
	}
	if v.level.get() != levelVerbose {
		t.Fatalf("ctrl+r left the chat at %s, want verbose", v.level.get())
	}
	if d.level.get() != levelVerbose {
		t.Fatalf("the task pane is on %s, want the level the chat cycled to", d.level.get())
	}
	// And back the other way. Verbose wraps to the quietest level, which is
	// quiet since issue #321.
	d.cycleLevel()
	if v.level.get() != levelQuiet {
		t.Fatalf("cycling in the task pane left the chat on %s, want quiet", v.level.get())
	}
	// quiet → compact → normal → verbose → quiet, four presses back to the
	// start, and the two panes agree at every stop along the way.
	want := []outputLevel{levelCompact, levelNormal, levelVerbose, levelQuiet}
	for _, w := range want {
		v.updateKey(registryKey(t, "ctrl+r"))
		if v.level.get() != w || d.level.get() != w {
			t.Fatalf("a press landed on %s (chat) and %s (task), want %s",
				v.level.get(), d.level.get(), w)
		}
	}
	// Leaving and reopening the chat keeps it: the holder outlives the view's
	// per-chat state.
	v.level.set(levelVerbose)
	v.open(2)
	if v.level.get() != levelVerbose {
		t.Fatalf("reopening a chat reset the level to %s", v.level.get())
	}
}

// TestChatHeaderNamesANonDefaultLevel: ctrl+r on a conversation with nothing
// to reveal changes nothing on screen, so the header has to say the key did
// something.
func TestChatHeaderNamesANonDefaultLevel(t *testing.T) {
	v := chatWithRecords(t)
	header := func() string { return plainLines([]string{v.headerLine(80)})[0] }
	if strings.Contains(header(), "normal") {
		t.Error("the header names the default level, which is noise")
	}
	for _, l := range []outputLevel{levelQuiet, levelCompact, levelVerbose} {
		v.level.set(l)
		if !strings.Contains(header(), l.String()) {
			t.Errorf("the header does not name the %s level", l)
		}
	}
}

// TestChatComposerKeepsItsKeys is decision 4: the four keys the workspace
// takes are ones a three-line textarea has no use for, and everything else
// still reaches the draft.
func TestChatComposerKeepsItsKeys(t *testing.T) {
	v := chatWithRecords(t)
	for i := range 200 {
		v.turnRecords[1] = append(v.turnRecords[1],
			apiclient.TranscriptRecord{Type: "agent.output", Text: fmt.Sprintf("line %d", i)})
	}
	v.bodyDirty = true
	v.render(60, 30)

	v.updateKey(registryKey(t, "pgup"))
	if v.following {
		t.Fatal("pgup did not pause follow")
	}
	before := v.vp.YOffset()
	v.updateKey(registryKey(t, "pgdown"))
	if v.vp.YOffset() <= before {
		t.Fatal("pgdn did not scroll forward")
	}
	v.updateKey(registryKey(t, "ctrl+g"))
	if !v.following || !v.vp.AtBottom() {
		t.Fatal("ctrl+g did not jump to the live end and re-arm follow")
	}
	if v.composer.Value() != "" {
		t.Fatalf("a scroll key was typed into the draft: %q", v.composer.Value())
	}

	// Letters and the arrows are the composer's: `v`, `f` and `G` would all
	// be perfectly good keys in a pane with no text field, and none of them
	// is available here.
	for _, key := range []string{"v", "f", "G", "e", "q"} {
		v.updateKey(registryKey(t, key))
	}
	if got := v.composer.Value(); got != "vfGeq" {
		t.Fatalf("the composer received %q, want every letter", got)
	}
	v.updateKey(registryKey(t, "up"))
	v.updateKey(registryKey(t, "down"))
	if v.composer.Value() != "vfGeq" {
		t.Fatal("an arrow key disturbed the draft")
	}
}

// TestChatLiveAndRefetchedAgree is the acceptance criterion the two divergent
// switch statements made impossible: a line delivered as a live chunk and the
// same line refetched from the turn transcript render identically at every
// level.
//
// The two shapes here are the daemon's own — internal/chatrun publishes the
// chunk (TestChatChunksAreNormalized holds that end) and internal/api's
// transcript route returns the record — and this is the client half: one
// renderer, reached by one record type, whichever door the line came in.
func TestChatLiveAndRefetchedAgree(t *testing.T) {
	fetched := []apiclient.TranscriptRecord{
		{Type: "agent.run_header", WorkDir: "/w/tree", AvailableTools: []string{"Edit", "Bash"}},
		{Type: "agent.thinking", Text: strings.Repeat("thought ", 30)},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Edit", Summary: "main.go", CallID: "c1"}}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: "c1", Verb: "wrote", Summary: "main.go"}}},
		{Type: "agent.output", Text: "done and dusted"},
	}
	live := []apiclient.OutputNote{
		{Type: "agent.run_header", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":1,"work_dir":"/w/tree","available_tools":["Edit","Bash"],"raw":"{}"}`)},
		{Type: "agent.thinking", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":2,"text":"` + strings.Repeat("thought ", 30) + `","raw":"{}"}`)},
		{Type: "agent.tool_use", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":3,"tools":[{"name":"Edit","summary":"main.go","call_id":"c1"}],"raw":"{}"}`)},
		{Type: "agent.tool_result", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":4,"results":[{"call_id":"c1","verb":"wrote","summary":"main.go"}],"raw":"{}"}`)},
		{Type: "agent.output", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":5,"text":"done and dusted","raw":"{}"}`)},
	}

	streamed := make([]apiclient.TranscriptRecord, 0, len(live))
	for _, note := range live {
		streamed = append(streamed, recordFromChunk(note))
	}
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
		opts := lineOpts{expandKey: chatExpandKey}
		want := plainLines(outputLines(fetched, level, 80, opts))
		got := plainLines(outputLines(streamed, level, 80, opts))
		if strings.Join(want, "\n") != strings.Join(got, "\n") {
			t.Errorf("%s: the stream and the refetch disagree\nrefetched:\n%s\nlive:\n%s",
				level, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
	}
}

// mdFixture is assistant prose covering the whole supported subset, used to
// hold the two workspaces to the same rendering.
const mdFixture = "# Findings\n\nThe **fix** is in `internal/tui`, and it is *small*.\n\n" +
	"- one, long enough to wrap at eighty columns without any help at all\n" +
	"  - nested\n1. first\n\n> a quotation\n\n```go\nfunc main() {}\n```\n\n---\n\nDone."

// TestChatAndTaskPaneRenderMarkdownIdentically is the first acceptance
// criterion of task 073, and it extends the equivalence pattern above: the
// same assistant Markdown, at the same width and the same level, is the same
// lines in a task workspace and in a chat. There is one renderer, and this is
// what says so.
func TestChatAndTaskPaneRenderMarkdownIdentically(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Edit", CallID: "c1"}}},
		{Type: "agent.output", Text: mdFixture},
	}
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
		for _, width := range []int{40, 80} {
			d := newTestDetail(t)
			d.width = width
			d.level.set(level)
			d.records = recs
			pane := strings.Join(plainLines(d.outputLines()), "\n")

			v := chatViewFixture()
			v.level.set(level)
			v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done", Prompt: "ask"}}
			v.turnRecords[1] = recs
			body := strings.Join(plainLines(v.bodyLines(width)), "\n")

			if !strings.Contains(body, pane) {
				t.Errorf("%s at width %d: the chat and the task pane disagree\n"+
					"pane:\n%s\nchat:\n%s", level, width, pane, body)
			}
			if !strings.Contains(pane, "▌ Findings") {
				t.Errorf("%s at width %d: the fixture did not render as Markdown:\n%s",
					level, width, pane)
			}
		}
	}
}

// TestChatRetentionFallbackIsRenderedProse holds decision 5's second site.
// §17 leaves a turn nothing but its answer, and that answer is assistant
// prose: it goes through the same renderer the records do, at the pane's full
// width and with no line cap — the cap this used to apply hid the tail of the
// only content the turn had left.
func TestChatRetentionFallbackIsRenderedProse(t *testing.T) {
	v := finishedChat(t, 1)
	v.turns[0].ResultText = mdFixture + "\n\n" + strings.Repeat("tail line\n\n", 30)
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: nil})
	body := plainLines(v.bodyLines(80))
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "▌ Findings") || !strings.Contains(joined, "• one") {
		t.Errorf("the retained-away answer was not rendered as Markdown:\n%s", joined)
	}
	if strings.Count(joined, "tail line") != 30 {
		t.Errorf("the answer lost its tail to a cap: %d of 30 lines survived:\n%s",
			strings.Count(joined, "tail line"), joined)
	}
	if strings.Contains(joined, "…") {
		t.Errorf("the fallback still truncates with an ellipsis:\n%s", joined)
	}
}
