package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 160: the conversation focus, search, prompt jumps, per-call expansion
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
	pressAll(t, v, "alt+G")
	v.render(100, 12)
	if v.newLines != 0 || v.edgeLabel() != "" {
		t.Fatalf("alt+G left the marker: %d", v.newLines)
	}
}

// clickScreen draws the chat view the way the root does at w×h — its own
// panes in the workspace, the root's frame around it otherwise — and returns
// the screen's plain lines.
func clickScreen(t *testing.T, v *chatView, w, h int) []string {
	t.Helper()
	v.update(tea.WindowSizeMsg{Width: w, Height: h})
	var out string
	if v.frameless() {
		out = v.render(w, h)
	} else {
		out = frame("", v.render(w-2, h-2), w, h, true)
	}
	return plainLines(strings.Split(out, "\n"))
}

// cellOf is the screen cell where text first appears.
func cellOf(t *testing.T, screen []string, text string) (x, y int) {
	t.Helper()
	for y, line := range screen {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, strings.Join(screen, "\n"))
	return 0, 0
}

// TestChatClickTogglesTheCallUnderThePointer is review F2: a click on a tool
// call's line toggles that call and no other — in the workspace with and
// without the totals line above the body, and in the narrow layout the root
// frames.
func TestChatClickTogglesTheCallUnderThePointer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		w      int
		totals bool
	}{
		{"workspace", 120, false},
		{"workspace with totals", 120, true},
		{"narrow", 90, false},
		{"narrow with totals", 90, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := chatFindFixture(t)
			v.side = chatWorkspaceFixture(t)
			if tc.totals {
				v.turns[1].InputTokens, v.turns[1].OutputTokens = 1200, 300
			}
			screen := clickScreen(t, v, tc.w, 40)
			if want := tc.w >= chatWorkspaceMinW; v.frameless() != want {
				t.Fatalf("frameless = %v at %d columns", v.frameless(), tc.w)
			}
			if got := strings.Contains(strings.Join(screen, "\n"), "tokens"); got != tc.totals {
				t.Fatalf("totals line drawn = %v, want %v", got, tc.totals)
			}
			x, y := cellOf(t, screen, "main.go")
			v.update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			b2, b3 := v.expandedFor(2)("call-b2", 0), v.expandedFor(3)("call-b3", 0)
			a2, a3 := v.expandedFor(2)("call-a2", 0), v.expandedFor(3)("call-a3", 0)
			if (!b2 && !b3) || a2 || a3 {
				t.Fatalf("clicking the Read call's line expanded a2 %v a3 %v b2 %v b3 %v, want only a Read call",
					a2, a3, b2, b3)
			}
		})
	}
}

// TestChatClickOnNewLinesFollows is review F2: while `↓ N new` shows, a click
// on it follows the conversation again — on the pane's bottom edge in the
// workspace, on the body's last row in the narrow layout.
func TestChatClickOnNewLinesFollows(t *testing.T) {
	for _, w := range []int{120, 90} {
		v := chatFindFixture(t)
		v.side = chatWorkspaceFixture(t)
		v.turns[2].State = "running"
		v.turns[1].InputTokens = 1200
		clickScreen(t, v, w, 16)
		v.vp.GotoTop()
		v.syncFollowToViewport()
		for i := range 3 {
			v.appendChunk(apiclient.OutputNote{Type: "agent.tool_use", TurnID: 3, Payload: []byte(
				`{"tools":[{"name":"Bash","summary":"ls","call_id":"live` + string(rune('0'+i)) + `"}]}`)})
			clickScreen(t, v, w, 16)
		}
		screen := clickScreen(t, v, w, 16)
		if v.newLines == 0 || v.following {
			t.Fatalf("%d columns: no `new` marker (newLines %d, following %v)", w, v.newLines, v.following)
		}
		x, y := cellOf(t, screen, "↓ "+strconv.Itoa(v.newLines)+" new")
		v.update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		if !v.following || v.newLines != 0 {
			t.Fatalf("%d columns: clicking `↓ N new` left following %v, newLines %d", w, v.following, v.newLines)
		}
	}
}
