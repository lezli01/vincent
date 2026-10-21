package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The tests in this file cover what issue801_test.go does not: where the
// §7.4 popup goes and how it comes back (F7), the line scroll and the turn
// cue (F8), a send keeping the finished turns' records (F10), and the inline
// lists' `n/N` (F14).

// awaitingChat is chatViewFixture with a question pending whose eight long
// options are taller than half an 80×24 pane.
func awaitingChat(t *testing.T) *chatView {
	t.Helper()
	v := finishedChat(t, 4)
	opts := make([]string, 8)
	for i := range opts {
		opts[i] = fmt.Sprintf(`"option %d, which says a good deal more than a word or two about what picking it does"`, i)
	}
	v.applyLoaded(chatLoadedMsg{id: 1, chat: &apiclient.Chat{
		ID: 1, ProjectID: 7, Title: "a chat", State: "awaiting_input", Agent: "claude",
		PendingInput: []byte(`{"kind":"question","questions":[{"text":"Which one?","options":[` +
			strings.Join(opts, ",") + `]}]}`),
	}, turns: v.turns})
	if v.form == nil {
		t.Fatal("the awaiting_input chat opened no popup")
	}
	return v
}

// TestChatAnswerPopupTakesTheComposersPlace: the popup is drawn instead of
// the composer, within half the pane, its key line pinned at the bottom and
// without the task workspace's ctrl+t (F7).
func TestChatAnswerPopupTakesTheComposersPlace(t *testing.T) {
	v := awaitingChat(t)
	const w, h = 80, 24
	lines := stripLines(v.render(w, h))
	if len(lines) != h {
		t.Fatalf("render drew %d lines, want %d", len(lines), h)
	}
	out := strings.Join(lines, "\n")
	if strings.Contains(out, "message…") || strings.Contains(out, "─ message") {
		t.Errorf("the composer is drawn under the popup that owns the keyboard:\n%s", out)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "esc leave the form") {
		t.Errorf("the popup's key line is not pinned at its bottom: %q", last)
	}
	if strings.Contains(out, "ctrl+t task details") {
		t.Errorf("the chat's popup offers the task workspace's ctrl+t:\n%s", out)
	}
	if got := len(v.formLines(w, h)); got > h/2+1 {
		t.Errorf("the popup took %d lines of a %d-line pane", got, h)
	}
	for _, b := range v.liveBindings(bindingsFor(ctxForm)) {
		if b.key == "ctrl+t" {
			t.Error("the chat's footer offers ctrl+t task details")
		}
	}
}

// TestChatAnswerPopupEscAndReopen: esc puts the question away with a note
// that stays, a reload does not bring it back on its own, and alt+a does,
// with what was picked (F7).
func TestChatAnswerPopupEscAndReopen(t *testing.T) {
	v := awaitingChat(t)
	v.updateKey(registryKey(t, "down"))
	v.updateKey(registryKey(t, "space"))
	picked := fmt.Sprint(v.form.answers)
	v.updateKey(registryKey(t, "esc"))
	if v.form != nil || v.formAway == nil {
		t.Fatal("esc did not put the popup away")
	}
	if out := ansi.Strip(v.render(80, 24)); !strings.Contains(out, "alt+a reopens the question") {
		t.Errorf("nothing says how to get the question back:\n%s", out)
	}
	live := false
	for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
		live = live || b.key == "alt+a"
	}
	if !live {
		t.Error("the footer does not offer alt+a while the question is away")
	}

	// A chat.* reload of the same question leaves it where esc put it.
	v.applyLoaded(chatLoadedMsg{id: 1, chat: v.chat, turns: v.turns})
	if v.form != nil {
		t.Fatal("a reload reopened the question esc put away")
	}
	if out := ansi.Strip(v.render(80, 24)); !strings.Contains(out, "alt+a reopens the question") {
		t.Errorf("the reopen note did not persist:\n%s", out)
	}

	v.updateKey(synthKey("alt+a"))
	if v.form == nil {
		t.Fatal("alt+a did not reopen the question")
	}
	if got := fmt.Sprint(v.form.answers); got != picked {
		t.Errorf("reopening lost the picks: %s, want %s", got, picked)
	}
	for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
		if b.key == "alt+a" {
			t.Error("alt+a is offered with no question put away")
		}
	}
}

// TestChatLineScrollAndTurnCue: alt+↑/↓ move one line, alt+g goes to the
// start, and a paused window says which turn it is showing (F8).
func TestChatLineScrollAndTurnCue(t *testing.T) {
	v := finishedChat(t, 9)
	v.bodyDirty = true
	v.render(80, 20)
	bottom := v.vp.YOffset()
	if bottom == 0 {
		t.Fatal("the fixture conversation fits the pane; nothing to scroll")
	}
	v.updateKey(synthKey("alt+up"))
	if got := v.vp.YOffset(); got != bottom-1 || v.following {
		t.Fatalf("alt+up moved to %d (following %v), want %d and paused", got, v.following, bottom-1)
	}
	v.updateKey(synthKey("alt+down"))
	if got := v.vp.YOffset(); got != bottom || !v.following {
		t.Fatalf("alt+down moved to %d (following %v), want %d and following", got, v.following, bottom)
	}
	v.updateKey(synthKey("alt+g"))
	if v.vp.YOffset() != 0 || v.following {
		t.Fatalf("alt+g left the window at %d", v.vp.YOffset())
	}
	head := stripLines(v.render(80, 20))[0]
	if !strings.Contains(head, "turn 1/9") || !strings.Contains(head, "⏸") {
		t.Errorf("the paused header has no position cue: %q", head)
	}
	v.updateKey(registryKey(t, "ctrl+g"))
	if head := stripLines(v.render(80, 20))[0]; strings.Contains(head, "turn 1/9") {
		t.Errorf("the cue outlived the pause: %q", head)
	}
}

// TestChatSendKeepsFinishedRecords: a send resets the live seam and nothing
// else, so the finished turns keep the records already fetched (F10).
func TestChatSendKeepsFinishedRecords(t *testing.T) {
	v := finishedChat(t, 2)
	recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: "kept"}}
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: recs})
	v.fetched[1] = true
	v.liveFrom, v.liveTurn = 40, 7
	v.composer.SetValue("next")
	v.applySent(chatSentMsg{chatID: 1, sent: "next"})
	if len(v.turnRecords[1]) != 1 || !v.fetched[1] {
		t.Fatalf("a send dropped turn 1's records: %v (fetched %v)", v.turnRecords[1], v.fetched[1])
	}
	if v.liveFrom != 0 || v.liveTurn != 0 {
		t.Errorf("a send kept the old live seam: %d/%d", v.liveFrom, v.liveTurn)
	}
}

// TestChatSendRefusedBeforeLoad: a chat that has not loaded takes no send,
// and the draft stays (F9).
func TestChatSendRefusedBeforeLoad(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.chat = nil
	v.composer.SetValue("hello")
	if cmd := v.sendCmd(); cmd != nil {
		t.Fatal("a chat that has not loaded sent a message")
	}
	if v.composer.Value() != "hello" || !v.noteBad {
		t.Errorf("the refusal lost the draft or said nothing: %q, %q", v.composer.Value(), v.note)
	}
}

// TestChatComposerMarksTheHiddenDraft: a draft taller than the composer may
// grow says how much of it is out of sight, and the frame still fits (F6).
func TestChatComposerMarksTheHiddenDraft(t *testing.T) {
	v := chatViewFixture()
	draft := make([]string, 14)
	for i := range draft {
		draft[i] = fmt.Sprintf("line%d", i)
	}
	v.composer.SetValue(strings.Join(draft, "\n"))
	const h = 24
	lines := stripLines(v.render(80, h))
	if len(lines) != h {
		t.Fatalf("render drew %d lines, want %d", len(lines), h)
	}
	if out := strings.Join(lines, "\n"); !strings.Contains(out, "+6 lines") {
		t.Errorf("a 14-line draft in an 8-row composer does not say +6 lines:\n%s", out)
	}
}

// TestChatInlineListPositionAndKeys: the list says `n/N` uncapped, arrows
// the side it hides rows on, and draws its keys on their own line (F14).
func TestChatInlineListPositionAndKeys(t *testing.T) {
	var l chatSkillList
	l.open, l.data = true, manySkills(20)
	l.build()
	l.cursor = 0
	const paneHeight, width = 24, 80
	out := stripLines(strings.Join(l.render(width, paneHeight, testNow), "\n"))
	if !strings.Contains(out[0], "1/20 ↓") {
		t.Errorf("the title has no position cue: %q", out[0])
	}
	if strings.Contains(out[0], "tab insert") {
		t.Errorf("the keys still ride on the title: %q", out[0])
	}
	if last := out[len(out)-1]; !strings.Contains(last, "tab insert") {
		t.Errorf("the keys are not on a line of their own: %q", last)
	}
	if win := l.window(paneHeight); win < 3 {
		t.Errorf("an 80×24 pane shows %d rows of the list", win)
	}
	l.cursor = 19
	if title := ansi.Strip(l.render(width, paneHeight, testNow)[0]); !strings.Contains(title, "20/20 ↑") {
		t.Errorf("at the last row the title says %q", title)
	}
}
