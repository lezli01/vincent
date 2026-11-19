package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chats workspace (task 150.5). Pure model tests, one property each (task
// 129 decision 7).

var chatWorkspaceNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func chatWorkspaceFixture(t *testing.T) *chatsView {
	t.Helper()
	pending, err := json.Marshal(apiclient.InputRequest{Kind: "question", Questions: []apiclient.InputQuestion{{
		Text: "How should the adapter get its models?", Options: []string{"Cache them", "Probe every start"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	issue := int64(9)
	v := newChatsView()
	v.client = offlineClient()
	v.now = func() time.Time { return chatWorkspaceNow }
	v.project = projectSel{id: 1, name: "api"}
	v.chats = []apiclient.Chat{
		{ID: 4, ProjectID: 1, Title: "should the adapter probe models?", State: "awaiting_input", Agent: "claude", PendingInput: pending, UpdatedAt: chatWorkspaceNow.Add(-2 * time.Minute)},
		{ID: 3, ProjectID: 1, Title: "walk me through the failover", State: "running", Agent: "codex", UpdatedAt: chatWorkspaceNow.Add(-time.Minute)},
		{ID: 2, ProjectID: 1, Title: "what would tenant_id break?", State: "running", Agent: "claude", IssueID: &issue, UpdatedAt: chatWorkspaceNow.Add(-3 * time.Minute)},
		{ID: 1, ProjectID: 1, Title: "where is the rate limit applied?", State: "idle", Agent: "claude", Branch: "vincent/1-rate", BaseBranch: "master", Model: "opus", CreatedAt: chatWorkspaceNow.Add(-time.Hour), UpdatedAt: chatWorkspaceNow.Add(-time.Hour)},
	}
	v.loaded = true
	v.restoreSelection()
	return v
}

// previewKeyOfChat is the read the selection calls for.
func previewKeyOfChat(t *testing.T, v *chatsView) chatPreviewKey {
	t.Helper()
	c, ok := v.current()
	if !ok {
		t.Fatal("nothing is selected")
	}
	return chatPreviewKey{c.ID, c.UpdatedAt.UnixNano()}
}

// The list says what each chat needs from you, in that order.
func TestChatWorkspaceGroupsTheCards(t *testing.T) {
	v := chatWorkspaceFixture(t)
	out := ansi.Strip(v.render(160, 44))
	at := -1
	for _, want := range []string{"! Waiting on you 1", "▶ Running 2", "○ Idle 1"} {
		i := strings.Index(out, want)
		if i < 0 || i < at {
			t.Fatalf("the list is missing %q in order:\n%s", want, out)
		}
		at = i
	}
	for _, want := range []string{"#4", "◈ claude", "◷ 2m", "! has a question", "▶ working", "○ idle", "▦ #9"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing %q:\n%s", want, out)
		}
	}
}

// Every width draws exactly the screen it was given.
func TestChatWorkspaceFitsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{200, 40}, {150, 30}, {120, 24}, {100, 24}, {80, 24}, {60, 15}} {
		v := chatWorkspaceFixture(t)
		lines := strings.Split(v.render(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: drew %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, got)
			}
		}
	}
}

// The panes appear as the terminal can afford them.
func TestChatWorkspacePanesFollowTheWidth(t *testing.T) {
	v := chatWorkspaceFixture(t)
	if out := ansi.Strip(v.render(chatWorkspaceMinW-1, 30)); strings.Contains(out, "is asking") || strings.Contains(out, "Details") {
		t.Errorf("a narrow terminal drew more than the list:\n%s", out)
	}
	out := ansi.Strip(v.render(chatWorkspaceMinW, 30))
	if !strings.Contains(out, "is asking") || strings.Contains(out, "Details") {
		t.Errorf("a medium terminal should draw the conversation alone:\n%s", out)
	}
	if out := ansi.Strip(v.render(chatDetailsMinW, 30)); !strings.Contains(out, "Details") {
		t.Errorf("a wide terminal drew no details:\n%s", out)
	}
}

// The selected card stays on screen however long the list is.
func TestChatWorkspaceScrollsToTheSelectedCard(t *testing.T) {
	v := chatWorkspaceFixture(t)
	for range 3 {
		v.moveCursor(1)
	}
	if out := ansi.Strip(v.render(160, 16)); !strings.Contains(out, "┏") || !strings.Contains(out, "#1") {
		t.Errorf("the selected card is off screen:\n%s", out)
	}
}

// An agent's question is pinned where it cannot scroll away.
func TestChatWorkspacePinsTheQuestion(t *testing.T) {
	v := chatWorkspaceFixture(t)
	out := ansi.Strip(v.render(160, 40))
	for _, want := range []string{"! claude is asking", "How should the adapter get its models?", "○ Cache them", "enter to answer", "waiting on you"} {
		if !strings.Contains(out, want) {
			t.Errorf("the conversation is missing %q:\n%s", want, out)
		}
	}
}

func TestChatPreviewShowsTheConversation(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.selectedID = 1
	v.restoreSelection()
	cost := 0.21
	v.applyPreview(chatPreviewMsg{key: previewKeyOfChat(t, v), updated: chatWorkspaceNow.Add(-time.Hour), turns: []apiclient.ChatTurn{{
		Seq: 1, Prompt: "where is it?", ResultText: "In **limits.go**.", InputTokens: 1000, OutputTokens: 200, CostUSD: &cost,
	}}})
	out := ansi.Strip(v.render(170, 40))
	for _, want := range []string{
		"where is it?", "turn 1", "In limits.go.", "enter continues this chat",
		"opus", "vincent/1-rate", "master", "1.2k", "$0.21", "1h ago", "no issue or task",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the workspace is missing %q:\n%s", want, out)
		}
	}
}

// A conversation is text an agent wrote: an escape in it never reaches the
// terminal.
func TestChatPreviewSanitizes(t *testing.T) {
	v := chatWorkspaceFixture(t)
	key := previewKeyOfChat(t, v)
	v.applyPreview(chatPreviewMsg{key: key, turns: []apiclient.ChatTurn{{Seq: 1, Prompt: "hi\x1b[2J", ResultText: "yo\x1b]0;owned\x07"}}})
	got := v.previews[key.id].turns[0]
	if strings.ContainsRune(got.Prompt+got.ResultText, '\x1b') {
		t.Fatalf("a kept turn carries an escape: %+v", got)
	}
}

func TestChatPreviewSaysWhenItCannotRead(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.selectedID = 1
	v.restoreSelection()
	if out := ansi.Strip(v.render(170, 40)); !strings.Contains(out, "Loading…") {
		t.Errorf("an unread chat does not say it is loading:\n%s", out)
	}
	v.applyPreview(chatPreviewMsg{key: previewKeyOfChat(t, v), updated: chatWorkspaceNow.Add(-time.Hour), err: errors.New("refused")})
	if out := ansi.Strip(v.render(170, 40)); !strings.Contains(out, "Could not read this chat: refused") {
		t.Errorf("a failed read is not reported:\n%s", out)
	}
}

// Holding an arrow key reads only the chat the selection rests on.
func TestChatPreviewReadsOnlyWhereTheSelectionRests(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.render(170, 40)
	first := previewKeyOfChat(t, v)
	if v.previewCmd() == nil {
		t.Fatal("selecting a chat armed no read")
	}
	if v.previewCmd() != nil {
		t.Fatal("a read armed twice for one chat")
	}
	v.moveCursor(1)
	if v.previewCmd() == nil {
		t.Fatal("moving to another chat armed no read")
	}
	if v.previewDue(first) != nil {
		t.Fatal("a wait for a chat since left read it")
	}
	if v.previewDue(previewKeyOfChat(t, v)) == nil {
		t.Fatal("the chat the selection rests on was not read")
	}
}

// A kept read is good until the chat changes.
func TestChatPreviewReadsAgainWhenTheChatChanges(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.render(170, 40)
	c, _ := v.current()
	v.applyPreview(chatPreviewMsg{key: previewKeyOfChat(t, v), updated: c.UpdatedAt})
	if v.previewCmd() != nil {
		t.Fatal("a kept chat was read again")
	}
	v.chats[0].UpdatedAt = chatWorkspaceNow
	if v.previewCmd() == nil {
		t.Fatal("a chat that changed was not read again")
	}
}

// The list alone reads nothing it cannot show.
func TestChatPreviewReadsNothingWhenHidden(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.render(80, 24)
	if v.previewCmd() != nil {
		t.Fatal("a hidden conversation armed a read")
	}
}

func TestChatWorkspaceClickSelectsACard(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.render(170, 44)
	var target chatCardHit
	for _, h := range v.cardHits {
		if h.id == 1 {
			target = h
		}
	}
	if target.id == 0 {
		t.Fatalf("chat #1 was not drawn: %+v", v.cardHits)
	}
	v.update(tea.MouseClickMsg{X: target.x0, Y: target.y0, Button: tea.MouseLeft})
	if c, _ := v.current(); c.ID != 1 {
		t.Fatalf("the click selected #%d, want #1", c.ID)
	}
}

// The archive keeps its table, and the forms keep the root's frame.
func TestChatWorkspaceIsTheLiveBoardOnly(t *testing.T) {
	if newArchivedChatsView().frameless() {
		t.Error("the archived board lost the root's frame")
	}
	v := chatWorkspaceFixture(t)
	if !v.frameless() {
		t.Error("the workspace asked for the root's frame")
	}
	v.create = newNewChatForm(nil, 1)
	if v.frameless() {
		t.Error("the new-chat form lost the root's frame")
	}
}

// Opening a chat keeps the list and the details around the conversation, and a
// chat opened from elsewhere keeps the whole screen.
func TestOpenChatStaysInTheWorkspace(t *testing.T) {
	side := chatWorkspaceFixture(t)
	v := awaitingChat(t)
	v.side = side
	v.now = side.now
	v.chatID = 1
	v.update(tea.WindowSizeMsg{Width: 170, Height: 40})
	if !v.frameless() {
		t.Fatal("the open conversation asked for the root's frame")
	}
	lines := strings.Split(v.render(170, 40), "\n")
	if len(lines) != 40 {
		t.Fatalf("drew %d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 170 {
			t.Errorf("line %d is %d wide", i, got)
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"◌ Chats", "Details", "is asking", "esc put away"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workspace is missing %q:\n%s", want, out)
		}
	}
	v.back = viewIssue
	if v.frameless() || strings.Contains(ansi.Strip(v.render(170, 40)), "◌ Chats") {
		t.Error("a chat opened from an issue drew the chats list")
	}
}

// A click on another card moves the conversation to that chat.
func TestOpenChatClickSwitchesChats(t *testing.T) {
	side := chatWorkspaceFixture(t)
	v := awaitingChat(t)
	v.form = nil
	v.side, v.chatID = side, 4
	v.update(tea.WindowSizeMsg{Width: 170, Height: 44})
	v.render(170, 44)
	var target chatCardHit
	for _, h := range side.cardHits {
		if h.id == 1 {
			target = h
		}
	}
	_, cmd := v.update(tea.MouseClickMsg{X: target.x0, Y: target.y0, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("a click on another card did nothing")
	}
	if msg, ok := cmd().(openChatMsg); !ok || msg.id != 1 || msg.back != viewChats {
		t.Fatalf("the click produced %#v, want chat #1 opened", cmd())
	}
}

// A call and its outcome are one line, and the mark says how it went.
func TestToolChecksPairCallsWithOutcomes(t *testing.T) {
	records := []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "internal/agent/catalog.go", CallID: "a"}}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: "a"}}},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", Summary: "go test", CallID: "b"}}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: "b", IsError: true, Summary: "exit 1"}}},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Edit", Summary: "main.go", CallID: "c"}}},
	}
	got := plainLines(outputLines(records, levelNormal, 80, lineOpts{toolChecks: true}))
	want := []string{"✓ read internal/agent/catalog.go", "✗ bash go test · exit 1", "◌ edit main.go"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the checks are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if out := strings.Join(plainLines(outputLines(records, levelVerbose, 80, lineOpts{toolChecks: true})), "\n"); strings.Contains(out, "✓ read") {
		t.Errorf("verbose lost the separate call and result lines:\n%s", out)
	}
}

// The preview reads the same way the open conversation does.
func TestChatPreviewDrawsTheTurnLikeTheConversation(t *testing.T) {
	v := chatWorkspaceFixture(t)
	v.selectedID = 1
	v.restoreSelection()
	v.chats[3].TurnCount, v.chats[3].PermissionMode = 2, "full_auto"
	ended := chatWorkspaceNow.Add(-2 * time.Minute)
	v.applyPreview(chatPreviewMsg{
		key: previewKeyOfChat(t, v), updated: chatWorkspaceNow.Add(-time.Hour),
		turns: []apiclient.ChatTurn{{Seq: 3, Prompt: "where is it?", ResultText: "In limits.go.", StartedAt: chatWorkspaceNow.Add(-4 * time.Minute), EndedAt: &ended}},
		tools: map[int][]apiclient.TranscriptRecord{3: {
			{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "limits.go", CallID: "a"}}},
			{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: "a"}}},
		}},
	})
	out := ansi.Strip(v.render(170, 44))
	for _, want := range []string{"─ You ─", "◷ 4m", "◈ claude  ·  turn 3  ·  ◷ 2m", "✓ read limits.go", "○ idle · 2 turns", "full-auto"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workspace is missing %q:\n%s", want, out)
		}
	}
}

// The pinned question offers its options and a way to write your own.
func TestChatQuestionCardOffersAnotherAnswer(t *testing.T) {
	out := ansi.Strip(chatWorkspaceFixture(t).render(160, 40))
	for _, want := range []string{"! waiting on you", "✎ Type another answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the conversation is missing %q:\n%s", want, out)
		}
	}
}

// An open question is answered in the card, with the mock's dots and keys.
func TestOpenChatQuestionIsTheCard(t *testing.T) {
	v := awaitingChat(t)
	out := ansi.Strip(v.render(120, 40))
	for _, want := range []string{"› ○", "✎ Type another answer", "space select  ·  enter submit  ·  esc put away"} {
		if !strings.Contains(out, want) {
			t.Errorf("the card is missing %q:\n%s", want, out)
		}
	}
	v.form.pick()
	if out := ansi.Strip(v.render(120, 40)); !strings.Contains(out, "› ◉") {
		t.Errorf("the chosen option is not filled:\n%s", out)
	}
}

// The status bar carries the board's state on a wide terminal, and the filter
// box's edge keeps it on a narrow one.
func TestChatSummaryMovesToTheStatusBar(t *testing.T) {
	v := chatWorkspaceFixture(t)
	wide := ansi.Strip(v.footerSummary(chatFooterSummaryMinW))
	for _, want := range []string{"4 chats", "! 1 waiting on you"} {
		if !strings.Contains(wide, want) {
			t.Errorf("the status bar's summary %q is missing %q", wide, want)
		}
	}
	if edge := ansi.Strip(v.chatsFilterBox(chatFooterSummaryMinW)[0]); strings.Contains(edge, "waiting on you") {
		t.Errorf("the summary is drawn twice: %q", edge)
	}
	if v.footerSummary(chatFooterSummaryMinW-1) != "" {
		t.Error("a narrow status bar spent its room on the summary")
	}
	if edge := ansi.Strip(v.chatsFilterBox(chatFooterSummaryMinW - 1)[0]); !strings.Contains(edge, "waiting on you") {
		t.Errorf("a narrow terminal lost the summary: %q", edge)
	}
}

// The open conversation's top edge names the chat and its state, under the
// board's filter.
func TestOpenChatPaneEdgeNamesTheChat(t *testing.T) {
	side := chatWorkspaceFixture(t)
	v := awaitingChat(t)
	v.side, v.chatID, v.now = side, 4, side.now
	v.update(tea.WindowSizeMsg{Width: 170, Height: 40})
	lines := strings.Split(ansi.Strip(v.render(170, 40)), "\n")
	if !strings.Contains(lines[1], "filter by title") {
		t.Errorf("the filter box is not above the panes: %q", lines[1])
	}
	if !strings.Contains(lines[3], "#4 ") || !strings.Contains(lines[3], "! waiting on you") {
		t.Errorf("the pane's top edge is %q", lines[3])
	}
}
