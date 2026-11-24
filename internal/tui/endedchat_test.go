package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
