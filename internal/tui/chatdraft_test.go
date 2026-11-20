package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func upKey() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp} }
func downKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }

// TestUpEditsTheQueueBeforeRecalling is task 164 decision 4: with a queued
// message `↑` edits it, as task 156 decision 5 does; with none it recalls.
func TestUpEditsTheQueueBeforeRecalling(t *testing.T) {
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "earlier"}}
	v.queue = &apiclient.ChatQueue{Messages: []apiclient.ChatQueuedMessage{{ID: 9, Prompt: "queued"}}}
	v.updateKey(upKey())
	if v.editingQueued != 9 || v.composer.Value() != "queued" || v.recall.active() {
		t.Fatalf("with a queue: editing %d %q recall=%v", v.editingQueued, v.composer.Value(), v.recall.active())
	}

	v = chatViewFixture()
	v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "earlier"}}
	v.updateKey(upKey())
	if v.composer.Value() != "earlier" || !v.recall.active() {
		t.Fatalf("without a queue: %q recall=%v", v.composer.Value(), v.recall.active())
	}
}

// TestRecallWalksPromptsInOrder: `↑` goes back newest first and stops at the
// oldest, `↓` comes forward and ends on an empty composer, and editing a
// recalled prompt ends recall without touching the turn it came from.
func TestRecallWalksPromptsInOrder(t *testing.T) {
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{Seq: 1, Prompt: "one"}, {Seq: 2, Prompt: "two"}, {Seq: 3, Prompt: "three"}}
	var got []string
	for range 4 {
		v.updateKey(upKey())
		got = append(got, v.composer.Value())
	}
	if want := []string{"three", "two", "one", "one"}; !equalStrings(got, want) {
		t.Fatalf("↑ walked %q, want %q", got, want)
	}
	got = nil
	for range 3 {
		v.updateKey(downKey())
		got = append(got, v.composer.Value())
	}
	if want := []string{"two", "three", ""}; !equalStrings(got, want) {
		t.Fatalf("↓ walked %q, want %q", got, want)
	}
	if v.recall.active() {
		t.Fatal("recall still active on an empty composer")
	}

	v.updateKey(upKey())
	v.updateKey(tea.KeyPressMsg{Code: '!', Text: "!"})
	if v.recall.active() || v.composer.Value() != "three!" {
		t.Fatalf("an edit left recall=%v composer=%q", v.recall.active(), v.composer.Value())
	}
	if v.turns[2].Prompt != "three" {
		t.Fatalf("editing a recalled prompt changed the turn: %q", v.turns[2].Prompt)
	}
}

// TestDraftEchoLosesNoKeystroke: a chat.draft_changed reload that echoes an
// older save — of text typed before the last keystrokes — never resets the
// composer, while a remote draft on a clean composer is taken.
func TestDraftEchoLosesNoKeystroke(t *testing.T) {
	v := chatViewFixture()
	v.client = apiclient.New("http://127.0.0.1:1", "t")
	v.applyLoadedDraft(&apiclient.Chat{ID: 1})
	for _, r := range "ab" {
		v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if cmd := v.flushDraft(); cmd == nil || !v.draft.inFlight {
		t.Fatal("no save went out")
	}
	v.updateKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	// The echo of "ab" arrives while that save is in flight, then after it.
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "ab", HasDraft: true})
	v.applyDraftSaved(chatDraftSavedMsg{chatID: 1})
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "ab", HasDraft: true})
	if got := v.composer.Value(); got != "abc" {
		t.Fatalf("an echo reset the composer to %q", got)
	}
	// Even a different remote draft cannot replace unsaved text.
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "from elsewhere", HasDraft: true})
	if got := v.composer.Value(); got != "abc" {
		t.Fatalf("a remote draft replaced unsaved text: %q", got)
	}

	// A clean composer follows another client's write.
	w := chatViewFixture()
	w.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "x"})
	w.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "from elsewhere"})
	if got := w.composer.Value(); got != "from elsewhere" {
		t.Fatalf("a clean composer did not follow: %q", got)
	}
}
