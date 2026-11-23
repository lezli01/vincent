package tui

import (
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
