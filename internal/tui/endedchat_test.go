package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1054: an ended chat draws what still works where the composer was, and
// takes no text, typed or pasted.
func TestEndedChatHasNoComposer(t *testing.T) {
	v := chatViewFixture()
	v.chat.State = "closed"
	foot := ansi.Strip(strings.Join(v.footerLines(100, 30), "\n"))
	for _, want := range []string{"this chat is closed and takes no more turns", "ctrl+y copy", "ctrl+f search"} {
		if !strings.Contains(foot, want) {
			t.Errorf("ended chat's footer misses %q:\n%s", want, foot)
		}
	}
	if strings.Contains(foot, "message") {
		t.Errorf("ended chat still draws the composer:\n%s", foot)
	}
	v.updateKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if v.composer.Value() != "" {
		t.Fatalf("an ended chat took typed text: %q", v.composer.Value())
	}
	if !strings.Contains(v.note, "nothing typed here can be sent") {
		t.Errorf("typing said %q", v.note)
	}
	v.pasteText("pasted")
	if v.composer.Value() != "" {
		t.Fatalf("an ended chat took a paste: %q", v.composer.Value())
	}

	live := chatViewFixture()
	live.updateKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if live.composer.Value() != "h" {
		t.Fatalf("a live chat lost typed text: %q", live.composer.Value())
	}
}

// #1135: an ended chat's footer offers no composer key, and a composer key
// pressed there says why it does nothing rather than filling the hidden
// composer — ctrl+g's $EDITOR text above all.
func TestEndedChatOffersNoComposerKeys(t *testing.T) {
	composerOnly := []string{"alt+s stash", "alt+h history", "ctrl+j newline", "ctrl+g editor", "alt+p model", "shift+tab mode", "ctrl+q close", "ctrl+t hand off"}
	hints := func(v *chatView) string {
		var out []string
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			out = append(out, b.hint)
		}
		return strings.Join(out, "|")
	}
	live := chatViewFixture()
	liveHints := hints(live)
	for _, h := range []string{"alt+s stash", "alt+h history", "ctrl+j newline", "ctrl+g editor", "alt+p model", "shift+tab mode", "ctrl+t hand off"} {
		if !strings.Contains(liveHints, h) {
			t.Errorf("an idle chat's footer misses %q: %s", h, liveHints)
		}
	}
	for _, state := range []string{"closed", "archived", "handed_off"} {
		v := chatViewFixture()
		v.chat.State = state
		task := int64(3)
		v.chat.LinkedTaskID = &task
		got := hints(v)
		for _, h := range composerOnly {
			if strings.Contains(got, h) {
				t.Errorf("%s chat's footer offers %q: %s", state, h, got)
			}
		}
		for _, h := range []string{"ctrl+y copy", "ctrl+f search", "esc back"} {
			if !strings.Contains(got, h) {
				t.Errorf("%s chat's footer lost %q: %s", state, h, got)
			}
		}
	}

	for _, tc := range []struct{ key, op string }{
		{"ctrl+g", "editor"},
		{"alt+h", "history"},
		{"ctrl+j", "newline"},
		{"up", "recall"},
	} {
		v := chatViewFixture()
		v.chat.State = "closed"
		v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "an earlier prompt", State: "succeeded"}}
		_, cmd := v.updateKey(registryKey(t, tc.key))
		if cmd != nil {
			t.Errorf("%s on an ended chat returned a command", tc.key)
		}
		if want := tc.op + ": not available — the chat has ended"; v.note != want {
			t.Errorf("%s said %q, want %q", tc.key, v.note, want)
		}
		if v.composer.Value() != "" || v.history.open {
			t.Errorf("%s on an ended chat reached the composer: %q (history open %v)", tc.key, v.composer.Value(), v.history.open)
		}
	}

	// An editor session that outlives the chat: what comes back is dropped,
	// and said so, rather than put into a composer that is no longer drawn.
	v := chatViewFixture()
	v.chat.State = "closed"
	v.applyEditorDraft(chatEditorMsg{chatID: 1, text: "written after it ended"})
	if v.composer.Value() != "" {
		t.Fatalf("the editor's text reached an ended chat's composer: %q", v.composer.Value())
	}
	if !strings.Contains(v.note, "the chat has ended") {
		t.Errorf("the dropped text said %q", v.note)
	}
}
