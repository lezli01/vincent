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
	d := newDetail(testCtx(t), level)
	v := newChatView(level)
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
	// And back the other way.
	d.cycleLevel()
	if v.level.get() != levelCompact {
		t.Fatalf("cycling in the task pane left the chat on %s, want compact", v.level.get())
	}
	// compact → normal → verbose → compact, three presses back to the start.
	for range 3 {
		v.updateKey(registryKey(t, "ctrl+r"))
	}
	if v.level.get() != levelCompact {
		t.Fatalf("three presses landed on %s, want compact again", v.level.get())
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
	for _, l := range []outputLevel{levelCompact, levelVerbose} {
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
	for _, level := range []outputLevel{levelCompact, levelNormal, levelVerbose} {
		opts := lineOpts{expandKey: chatExpandKey}
		want := plainLines(outputLines(fetched, level, 80, opts))
		got := plainLines(outputLines(streamed, level, 80, opts))
		if strings.Join(want, "\n") != strings.Join(got, "\n") {
			t.Errorf("%s: the stream and the refetch disagree\nrefetched:\n%s\nlive:\n%s",
				level, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
	}
}
