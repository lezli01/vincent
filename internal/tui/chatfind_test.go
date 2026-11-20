package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 157: the conversation focus, search, prompt jumps, per-call expansion
// and the `↓ N new` marker.

// chatFindFixture is a three-turn chat: turn 1 not loaded (only the daemon
// can search it), turns 2 and 3 loaded, each with two folded tool calls.
func chatFindFixture(t *testing.T) *chatView {
	t.Helper()
	v := chatViewFixture()
	v.client = offlineClient()
	v.turns = []apiclient.ChatTurn{
		{ID: 1, Seq: 1, State: "done", Prompt: "first question", ResultText: "an old answer"},
		{ID: 2, Seq: 2, State: "done", Prompt: "second question"},
		{ID: 3, Seq: 3, State: "done", Prompt: "third question"},
	}
	v.fetched[1] = false
	for _, seq := range []int{2, 3} {
		a, b := "call-a"+string(rune('0'+seq)), "call-b"+string(rune('0'+seq))
		v.turnRecords[seq] = []apiclient.TranscriptRecord{
			{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", Summary: "go test", CallID: a}}},
			{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: a, Verb: "ran"}}},
			{Type: "agent.command_output", CallID: a, Output: "ok  needle-pkg  0.1s"},
			{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "main.go", CallID: b}}},
			{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: b, Verb: "read"}}},
			{Type: "agent.output", Text: "all done"},
		}
	}
	v.level.set(levelCompact)
	v.render(100, 40)
	return v
}

// convFocused is the fixture with the conversation holding the keyboard.
func convFocused(t *testing.T) *chatView {
	t.Helper()
	v := chatFindFixture(t)
	v.focusConversation()
	v.render(100, 40)
	return v
}

func pressAll(t *testing.T, v *chatView, keys ...string) {
	t.Helper()
	for _, k := range keys {
		v.updateKey(registryKey(t, k))
	}
}

func bodyText(v *chatView) string {
	v.render(100, 40)
	return strings.Join(plainLines(v.lines), "\n")
}

func TestChatCtrlFTakesTheKeyboardFromTheDraft(t *testing.T) {
	v := chatFindFixture(t)
	v.composer.SetValue("half a draft")
	pressAll(t, v, "ctrl+f")
	if !v.convFocus || !v.search.typing || !v.capturesInput() {
		t.Fatalf("ctrl+f: focus %v, search line %v, capturing %v", v.convFocus, v.search.typing, v.capturesInput())
	}
	if v.bindingContext() != ctxChatFind {
		t.Fatalf("context = %q, want the search line's", v.bindingContext())
	}
	pressAll(t, v, "x", "y")
	if v.search.buf != "xy" {
		t.Fatalf("the search line holds %q", v.search.buf)
	}
	pressAll(t, v, "esc")
	if v.capturesInput() || v.bindingContext() != ctxChatConversation {
		t.Fatal("esc on the search line should leave the conversation focused, capturing nothing")
	}
	pressAll(t, v, "j", "k", "n", "q")
	if got := v.composer.Value(); got != "half a draft" {
		t.Fatalf("letters reached the draft: %q", got)
	}
	// esc peels: conversation → composer, and never leaves the chat.
	_, cmd := v.updateKey(registryKey(t, "esc"))
	if cmd != nil {
		if _, left := cmd().(selectViewMsg); left {
			t.Fatal("esc in conversation focus left the chat")
		}
	}
	if v.convFocus || !v.composer.Focused() || v.bindingContext() != ctxChat {
		t.Fatal("esc in conversation focus should give the keyboard back to the composer")
	}
	pressAll(t, v, "ctrl+f", "esc", "i")
	if v.convFocus {
		t.Fatal("i should give the keyboard back to the composer")
	}
}

func TestChatSearchStepsAndWraps(t *testing.T) {
	v := chatFindFixture(t)
	v.runSearch("question")
	if got := len(v.search.matches); got != 3 {
		t.Fatalf("matches = %d, want one per prompt", got)
	}
	start := v.search.current
	for i := range 3 {
		v.stepMatch(1)
		if v.search.current != (start+i+1)%3 {
			t.Fatalf("n moved to %d", v.search.current)
		}
	}
	v.stepMatch(-1)
	if v.search.current != (start+2)%3 {
		t.Fatalf("N did not wrap back: %d", v.search.current)
	}
	label := ansi.Strip(v.edgeLabel())
	if !strings.Contains(label, "/3") {
		t.Fatalf("edge label %q has no count", label)
	}
	if !strings.Contains(strings.ToLower(ansi.Strip(v.lines[v.cursor])), "question") {
		t.Fatalf("the cursor is on %q, not a match", ansi.Strip(v.lines[v.cursor]))
	}
}

func TestChatSearchFetchesExactlyTheUnloadedTurn(t *testing.T) {
	v := chatFindFixture(t)
	v.runSearch("needle")
	before := len(v.search.matches)
	v.applySearch(chatSearchMsg{chatID: 1, query: "needle", result: &apiclient.ChatSearch{
		Query: "needle", Total: 4, Turns: []apiclient.ChatSearchTurn{{Seq: 1, Matches: 2}, {Seq: 2, Matches: 1}, {Seq: 3, Matches: 1}},
	}})
	if got := len(v.search.matches); got != before+2 {
		t.Fatalf("matches = %d, want the daemon's two in turn 1 added to %d", got, before)
	}
	if !v.search.matches[0].unloaded {
		t.Fatal("turn 1's match should be the daemon's")
	}
	v.fetched = map[int]bool{}
	v.search.current = 0
	if cmd := v.landOn(0); cmd == nil {
		t.Fatal("landing on an unloaded match fetched nothing")
	}
	if len(v.fetched) != 1 || !v.fetched[1] {
		t.Fatalf("fetched %v, want exactly turn 1", v.fetched)
	}
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "a needle and another needle"},
	}})
	if v.search.land != nil {
		t.Fatal("the landing is still pending after the turn arrived")
	}
	if a := anchorAt(v.anchors, v.cursor); a.turn != 1 {
		t.Fatalf("the cursor is in turn %d, want 1", a.turn)
	}
	if !strings.Contains(ansi.Strip(v.lines[v.cursor]), "needle") {
		t.Fatalf("the cursor is on %q", ansi.Strip(v.lines[v.cursor]))
	}
}

func TestChatSearchExpandsOnlyTheFoldedCall(t *testing.T) {
	v := chatFindFixture(t)
	if strings.Contains(bodyText(v), "needle-pkg") {
		t.Fatal("compact drew the command output it should fold")
	}
	v.runSearch("needle-pkg")
	if len(v.search.matches) != 2 {
		t.Fatalf("matches = %d, want one per loaded turn", len(v.search.matches))
	}
	v.landOn(0)
	if v.level.get() != levelCompact {
		t.Fatal("landing changed the session level")
	}
	set := v.expanded[v.chatID]
	if len(set) != 1 {
		t.Fatalf("expanded = %v, want exactly the one call", set)
	}
	body := bodyText(v)
	if strings.Count(body, "needle-pkg") != 1 {
		t.Fatalf("want the output of the one expanded call, got:\n%s", body)
	}
	if !strings.Contains(ansi.Strip(v.lines[v.cursor]), "needle-pkg") {
		t.Fatalf("cursor on %q", ansi.Strip(v.lines[v.cursor]))
	}
}

func TestChatPromptJumpsLandOnYouCards(t *testing.T) {
	v := chatFindFixture(t)
	pressAll(t, v, "ctrl+f", "esc", "g")
	pressAll(t, v, "}")
	if a := anchorAt(v.anchors, v.cursor); !a.you || a.turn != 2 {
		t.Fatalf("} landed on %+v, want turn 2's card", a)
	}
	pressAll(t, v, "}")
	if a := anchorAt(v.anchors, v.cursor); !a.you || a.turn != 3 {
		t.Fatalf("} landed on %+v, want turn 3's card", a)
	}
	pressAll(t, v, "{")
	if a := anchorAt(v.anchors, v.cursor); !a.you || a.turn != 2 {
		t.Fatalf("{ landed on %+v, want turn 2's card", a)
	}
}

func TestChatEnterTogglesOneCallAndTheSetSurvivesASwitch(t *testing.T) {
	v := chatFindFixture(t)
	pressAll(t, v, "ctrl+f", "esc")
	check := -1
	for y, a := range v.anchors {
		if a.call == "call-a2" {
			check = y
			break
		}
	}
	if check < 0 {
		t.Fatal("no check line for call-a2")
	}
	if !strings.Contains(v.lines[check], "▸") {
		t.Fatalf("the folded check line has no ▸: %q", ansi.Strip(v.lines[check]))
	}
	v.moveCursor(check)
	pressAll(t, v, "enter")
	body := bodyText(v)
	if strings.Count(body, "needle-pkg") != 1 {
		t.Fatalf("enter should expand call-a2 alone:\n%s", body)
	}
	if !strings.Contains(body, "▾") {
		t.Fatal("the expanded call has no ▾")
	}
	// Away to another chat and back: the set is the chat's.
	v.open(2)
	if v.expanded[1] == nil || !v.expanded[1][chatCallKey{turn: 2, call: "call-a2"}] {
		t.Fatal("switching chats dropped the expanded set")
	}
	v.open(1)
	if !v.expandedFor(2)("call-a2", 0) || v.expandedFor(2)("call-b2", 0) {
		t.Fatal("the expanded set did not come back with the chat")
	}
}

func TestChatNewLinesMarker(t *testing.T) {
	v := chatFindFixture(t)
	v.turns[2].State = "running"
	v.render(100, 12)
	v.vp.GotoTop()
	v.syncFollowToViewport()
	for i := range 3 {
		v.appendChunk(apiclient.OutputNote{Type: "agent.tool_use", TurnID: 3, Payload: []byte(
			`{"tools":[{"name":"Bash","summary":"ls","call_id":"live` + string(rune('0'+i)) + `"}]}`)})
		v.render(100, 12)
	}
	if v.newLines == 0 || !strings.Contains(ansi.Strip(v.edgeLabel()), "new") {
		t.Fatalf("newLines = %d, label %q", v.newLines, ansi.Strip(v.edgeLabel()))
	}
	n := v.newLines
	v.level.cycle()
	v.render(100, 12)
	if v.newLines != n {
		t.Fatalf("a level change moved the count from %d to %d", n, v.newLines)
	}
	pressAll(t, v, "ctrl+g")
	v.render(100, 12)
	if v.newLines != 0 || v.edgeLabel() != "" {
		t.Fatalf("ctrl+g left the marker: %d", v.newLines)
	}
}
