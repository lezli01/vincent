package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1070: a failed turn says what it means and what to do next, and alt+l
// sends its prompt again.
func TestFailedTurnSaysWhatNextAndRetries(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "summarise the diff", State: "failed", FailReason: "usage_limit"}}
	v.bodyDirty = true
	got := ansi.Strip(v.render(120, 40))
	for _, want := range []string{"usage limit reached (usage_limit)", "quota", "alt+l sends the same prompt again"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failed turn misses %q:\n%s", want, got)
		}
	}
	if cmd := v.retryFailedTurn(); cmd == nil || !v.sending {
		t.Fatal("alt+l did not send the failed prompt")
	}
	// A second send while that one is in flight says so.
	v.composer.SetValue("and another")
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(v.note, "still being sent") || v.composer.Value() != "and another" {
		t.Fatalf("enter during a send: note %q, draft %q", v.note, v.composer.Value())
	}
}

// #1070: ctrl+x with nothing running says so.
func TestStopWithNothingRunningSaysSo(t *testing.T) {
	v := chatViewFixture()
	v.updateKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if v.note != "no turn is running — nothing to stop" {
		t.Fatalf("ctrl+x said %q", v.note)
	}
}

// #1149: a chat turn's failure is worded for a turn, not a task — a stopped
// turn says the turn was stopped — and the chat-only reasons have a meaning.
func TestFailedTurnMeaningIsAChatTurns(t *testing.T) {
	for reason, want := range map[string]string{
		"canceled":          "turn was stopped",
		"session_lost":      "no longer knows this conversation",
		"checkpoint_failed": "could not snapshot",
		"agent_unavailable": "could not be found or started",
	} {
		v := chatViewFixture()
		v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "p", State: "failed", FailReason: reason}}
		v.bodyDirty = true
		got := strings.Join(strings.Fields(ansi.Strip(v.render(120, 40))), " ")
		if !strings.Contains(got, want) {
			t.Errorf("%s: the failed turn misses %q:\n%s", reason, want, got)
		}
		if strings.Contains(got, "the task") {
			t.Errorf("%s: a chat turn's failure talks about a task:\n%s", reason, got)
		}
	}
}

// #1149: a second alt+l after the send was taken but before the reload shows
// the new turn does not send the prompt again; a refused send frees it.
func TestRetryFailedTurnOncePerResend(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "again", State: "failed", FailReason: "agent_error"}}
	if v.retryFailedTurn() == nil {
		t.Fatal("the first alt+l sent nothing")
	}
	v.applySent(chatSentMsg{chatID: v.chatID, sent: "again", result: &apiclient.ChatSend{}})
	if cmd := v.retryFailedTurn(); cmd != nil {
		t.Fatal("a second alt+l before the reload sent the prompt again")
	}
	if !strings.Contains(v.note, "already") {
		t.Errorf("the ignored alt+l said %q", v.note)
	}
	// The reload shows the resent turn: a later failure can be retried.
	v.applyLoaded(chatLoadedMsg{id: v.chatID, chat: v.chat, turns: []apiclient.ChatTurn{
		{Seq: 1, Prompt: "again", State: "failed", FailReason: "agent_error"},
		{Seq: 2, Prompt: "again", State: "failed", FailReason: "agent_error"},
	}})
	if v.retryFailedTurn() == nil {
		t.Fatal("alt+l after the reload sent nothing")
	}
	// A refused send frees it at once.
	v.applySent(chatSentMsg{chatID: v.chatID, sent: "again", err: errors.New("refused")})
	if v.retryFailedTurn() == nil {
		t.Fatal("alt+l after a refused resend sent nothing")
	}
}
