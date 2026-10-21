package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/lezli01/vincent/internal/apiclient"
)

// The tests in this file hold issue #801: the chat workspace's notes,
// failures and header at the pane's edges, its one key line, its composer,
// and the chats board's columns and new-chat form.

// stripLines is a rendered frame with its styling removed, one element per
// screen line.
func stripLines(frame string) []string {
	return strings.Split(ansi.Strip(frame), "\n")
}

// squeeze drops every whitespace rune, so wrapped text can be compared with
// the string it was wrapped from.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// TestIssue801ClosePromptKeepsItsAnswerKeys: a linked chat with a running
// turn asks a sentence longer than the pane, and the `(y/n)` is the part a
// reader needs (F2). It wraps rather than being cut at either width.
func TestIssue801ClosePromptKeepsItsAnswerKeys(t *testing.T) {
	for _, width := range []int{80, 120} {
		v := chatViewFixture()
		v.chat.LinkedTaskID = ptr(int64(42))
		v.chat.State = "running"
		v.turns = []apiclient.ChatTurn{{ID: 1, ChatID: 1, Seq: 1, Prompt: "go", State: "running", StartedAt: v.now()}}
		v.askClose()
		if !v.closing {
			t.Fatal("ctrl+q raised no close prompt")
		}
		out := ansi.Strip(v.render(width, 24))
		if !strings.Contains(out, "(y/n)") {
			t.Errorf("at %d columns the close prompt lost its (y/n):\n%s", width, out)
		}
	}
}

// TestIssue801ChatsArchivePromptKeepsItsAnswerKeys: the chats board quotes
// the whole title in its archive question, and a long one pushed `(y/n)` off
// the line (F2).
func TestIssue801ChatsArchivePromptKeepsItsAnswerKeys(t *testing.T) {
	v := newChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.project = projectSel{id: 7, name: "repo"}
	v.applyLoaded(chatsLoadedMsg{chats: []apiclient.Chat{
		testChat(1, "idle", strings.Repeat("a very long chat title ", 6)),
	}})
	v.updateKey(registryKey(t, "A"))
	if v.confirm == nil {
		t.Fatal("A raised no archive prompt")
	}
	out := ansi.Strip(v.render(80, 24))
	if !strings.Contains(out, "(y/n)") {
		t.Fatalf("the archive prompt lost its (y/n) at 80 columns:\n%s", out)
	}
}

// TestIssue801FailedTurnErrorWraps: a failed turn's reason and message were
// one line, cut at the pane's edge (F3). Nothing of the message is lost.
func TestIssue801FailedTurnErrorWraps(t *testing.T) {
	msg := "claude exited with status 1: Error: the API returned 529 overloaded_error after 3 retries; " +
		"your request was not processed and no tokens were billed, so retrying later is safe and the " +
		"session can be resumed from here"
	if len(msg) < 200 {
		t.Fatalf("the fixture message is %d characters, want at least 200", len(msg))
	}
	for _, width := range []int{80, 120} {
		v := chatViewFixture()
		end := v.now()
		v.turns = []apiclient.ChatTurn{{
			ID: 1, ChatID: 1, Seq: 1, Prompt: "go", State: "failed",
			FailReason: "agent_error", ErrorMessage: msg, StartedAt: end, EndedAt: &end,
		}}
		v.bodyDirty = true
		out := ansi.Strip(v.render(width, 40))
		if !strings.Contains(squeeze(out), squeeze(msg)) {
			t.Errorf("at %d columns the failure message is clipped:\n%s", width, out)
		}
	}
}

// TestIssue801HeaderKeepsStateBehindALongTitle: the title came first and
// padBetween never shrinks it, so a long one pushed the state, the branch,
// `on task #N` and the turn count off the line (F4).
func TestIssue801HeaderKeepsStateBehindALongTitle(t *testing.T) {
	v := chatViewFixture()
	v.chat.Title = strings.Repeat("t", 80)
	v.chat.State = "awaiting_input"
	v.chat.Branch = "vincent/9-br"
	v.chat.LinkedTaskID = ptr(int64(42))
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "succeeded"}, {ID: 2, Seq: 2, State: "succeeded"}}
	v.following = false
	head := stripLines(v.render(80, 24))[0]
	for _, want := range []string{chatStateLabel("awaiting_input"), "vincent/9-br", "on task #42", "2 turns", "⏸"} {
		if !strings.Contains(head, want) {
			t.Errorf("the 80-column header lost %q: %q", want, head)
		}
	}
}

// TestIssue801WorkspaceDrawsNoHintLineOfItsOwn: the root footer is the
// registry, and the workspace's literal hint line under the composer was a
// second, disagreeing one (F5).
func TestIssue801WorkspaceDrawsNoHintLineOfItsOwn(t *testing.T) {
	v := chatViewFixture()
	out := ansi.Strip(v.render(200, 24))
	if strings.Contains(out, "esc back to the chats board") || strings.Contains(out, "enter send · ctrl+j newline") {
		t.Fatalf("the chat workspace draws its own hint line:\n%s", out)
	}
}

// TestIssue801LiveBindingsDropDeadKeys: the footer offered keys that do
// nothing in the chat's current state (F5).
func TestIssue801LiveBindingsDropDeadKeys(t *testing.T) {
	has := func(v *chatView, key string) bool {
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			if b.context == ctxChat && b.key == key {
				return true
			}
		}
		return false
	}

	idle := chatViewFixture()
	if has(idle, "ctrl+x") {
		t.Error("ctrl+x stop is offered with no running turn")
	}

	ended := chatViewFixture()
	ended.chat.State = "archived"
	if has(ended, "tab") {
		t.Error("tab skills is offered on a terminal chat")
	}

	closing := chatViewFixture()
	closing.chat.LinkedTaskID = ptr(int64(42))
	closing.askClose()
	if has(closing, "enter") {
		t.Error("enter send is offered while the close prompt owns the keyboard")
	}
}

// TestIssue801LoadErrorIsInTheBody: a failed GetChat read as an empty chat,
// the error only in the dim header and the body inviting a message (F9).
func TestIssue801LoadErrorIsInTheBody(t *testing.T) {
	v := chatViewFixture()
	v.chat = nil
	v.loadErr = "connection refused"
	v.bodyDirty = true
	lines := stripLines(v.render(80, 24))
	body := strings.Join(lines[1:], "\n")
	if strings.Contains(body, "Nothing said yet") {
		t.Errorf("a chat that failed to load reads as an empty one:\n%s", body)
	}
	if !strings.Contains(body, "connection refused") {
		t.Errorf("the body does not say why the chat is not shown:\n%s", body)
	}
}

// TestIssue801ComposerGrowsWithTheDraft: the composer was fixed at three
// rows, so a six-line draft showed half of itself with no sign of the rest
// (F6).
func TestIssue801ComposerGrowsWithTheDraft(t *testing.T) {
	v := chatViewFixture()
	v.composer.SetValue("line1\nline2\nline3\nline4\nline5\nline6")
	out := ansi.Strip(v.render(80, 40))
	for i := 1; i <= 6; i++ {
		want := "line" + string(rune('0'+i))
		if !strings.Contains(out, want) {
			t.Errorf("a six-line draft hides %q:\n%s", want, out)
		}
	}
}

// TestIssue801ChatsBoardColumnsAlign: the state cell was padded with %-15s
// after styling, so escape bytes and the spinner's multi-byte frame shifted
// every later column (F11). The title starts at one display column for every
// state.
func TestIssue801ChatsBoardColumnsAlign(t *testing.T) {
	v := chatsFixture()
	col := map[string]int{}
	for _, state := range []string{"idle", "running", "awaiting_input", "archived", "handed_off", "closed"} {
		c := testChat(3, state, "TITLEMARK")
		line := ansi.Strip(v.rowLine(chatRow{chat: &c}, false, 120))
		i := strings.Index(line, "TITLEMARK")
		if i < 0 {
			t.Fatalf("the %s row has no title: %q", state, line)
		}
		col[state] = ansi.StringWidth(line[:i])
	}
	want := col["idle"]
	for state, got := range col {
		if got != want {
			t.Errorf("the %s row's title starts at column %d, the idle row's at %d", state, got, want)
		}
	}
}

// TestIssue801ChatsHeaderKeepsUpdatedAt80: the scope label for the all
// listing pushed `updated hh:mm:ss` off an 80-column header (F11).
func TestIssue801ChatsHeaderKeepsUpdatedAt80(t *testing.T) {
	v := chatsFixture()
	v.scope = apiclient.ArchivedAll
	v.lastLoad = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	head := stripLines(v.render(80, 24))[0]
	if !strings.Contains(head, "updated 12:00:00") {
		t.Fatalf("the 80-column header lost its updated stamp: %q", head)
	}
}

// TestIssue801NewChatPickerVisibleAtTenRows: the form windowed on the
// focused row's index rather than its line, and the open picker was appended
// after every row, so at 80×10 no option of the agent list was on screen
// (F13).
func TestIssue801NewChatPickerVisibleAtTenRows(t *testing.T) {
	f := newNewChatForm(nil, 7)
	f.applyFields(newChatFieldsMsg{agents: []apiclient.Agent{
		{Name: "claude", SupportsResume: ptr(true)},
		{Name: "elder", SupportsResume: ptr(true)},
	}})
	openAt(t, f, ncAgent)
	if f.pick == nil {
		t.Fatal("enter on the agent row opened no list")
	}
	out := ansi.Strip(f.render(80, 10))
	// The agent row's value is "claude", so "elder" is only ever the list's.
	if !strings.Contains(out, "elder") {
		t.Errorf("at 80×10 the open agent list shows no option:\n%s", out)
	}
	if !strings.Contains(out, "esc close the list") {
		t.Errorf("at 80×10 the open agent list's key hint is cut:\n%s", out)
	}
}

// TestIssue801NewChatErrorWraps: a create error was one unwrapped line,
// wider than the pane (F13).
func TestIssue801NewChatErrorWraps(t *testing.T) {
	f := newNewChatForm(nil, 7)
	f.err = "create failed: " + strings.Repeat("the daemon refused the request for a reason worth reading ", 3)
	for _, line := range stripLines(f.render(80, 24)) {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("a new-chat line is %d columns wide in an 80-column pane: %q", w, line)
		}
	}
}
