package tui

import (
	"errors"
	"testing"
	"time"

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

// TestStaleReloadAfterSendKeepsTheComposer is review F1: a send's reload
// that read the chat before the send's draft PUT committed, and answers after
// it, must not put the sent prompt back into the composer — nor reset text
// typed since to an older save.
func TestStaleReloadAfterSendKeepsTheComposer(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)
	v := chatViewFixture()
	v.client = apiclient.New("http://127.0.0.1:1", "t")
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "fix the parser", HasDraft: true, DraftUpdatedAt: &t0})
	if got := v.composer.Value(); got != "fix the parser" {
		t.Fatalf("the stored draft was not restored: %q", got)
	}
	// The send is accepted: the composer empties and the PUT clearing the
	// stored draft goes out beside the reload.
	v.applySent(chatSentMsg{chatID: 1, sent: "fix the parser", result: &apiclient.ChatSend{}})
	if v.composer.Value() != "" || !v.draft.inFlight {
		t.Fatalf("after the send: composer %q, in flight %v", v.composer.Value(), v.draft.inFlight)
	}
	// The PUT answers first; the reload, which read the row before it,
	// answers last.
	v.applyDraftSaved(chatDraftSavedMsg{chatID: 1, updatedAt: &t1})
	v.applyLoaded(chatLoadedMsg{id: 1, chat: &apiclient.Chat{ID: 1, State: "running", Draft: "fix the parser", HasDraft: true, DraftUpdatedAt: &t0}})
	if got := v.composer.Value(); got != "" {
		t.Fatalf("a stale reload put %q back into the composer", got)
	}

	// The same reordering while typing: a save of "ab" is acknowledged, more
	// is typed and saved, and a read stamped before it arrives clean.
	w := chatViewFixture()
	w.client = apiclient.New("http://127.0.0.1:1", "t")
	w.applyLoadedDraft(&apiclient.Chat{ID: 1})
	w.composer.SetValue("ab")
	w.flushDraft()
	w.applyDraftSaved(chatDraftSavedMsg{chatID: 1, updatedAt: &t0})
	w.composer.SetValue("abc")
	w.flushDraft()
	w.applyDraftSaved(chatDraftSavedMsg{chatID: 1, updatedAt: &t1})
	w.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "ab", HasDraft: true, DraftUpdatedAt: &t0})
	if got := w.composer.Value(); got != "abc" {
		t.Fatalf("a stale read reset the composer to %q", got)
	}
	// A newer write from elsewhere is still followed.
	t2 := t1.Add(time.Second)
	w.applyLoadedDraft(&apiclient.Chat{ID: 1, Draft: "from elsewhere", HasDraft: true, DraftUpdatedAt: &t2})
	if got := w.composer.Value(); got != "from elsewhere" {
		t.Fatalf("a newer remote draft was not followed: %q", got)
	}
}

// TestFailedStashSaveIsRetried is review F2: alt+s against a daemon that
// cannot be reached empties the composer, and the next save must still carry
// the stash — nor may a reload replace it with the daemon's older one.
func TestFailedStashSaveIsRetried(t *testing.T) {
	v := chatViewFixture()
	v.client = apiclient.New("http://127.0.0.1:1", "t")
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Stash: "old"})
	v.composer.SetValue("keep me")
	if cmd := v.toggleStash(); cmd == nil {
		t.Fatal("the stash was not saved")
	}
	v.applyDraftSaved(chatDraftSavedMsg{chatID: 1, err: errors.New("connection refused")})
	v.applyLoadedDraft(&apiclient.Chat{ID: 1, Stash: "old"})
	if v.draft.stash != "keep me" {
		t.Fatalf("a reload replaced the unsaved stash with %q", v.draft.stash)
	}
	u := v.draftUpdate()
	if u == nil || u.Stash == nil || *u.Stash != "keep me" {
		t.Fatalf("the retry does not carry the stash: %+v", u)
	}
}

// TestHistoryNarrowsHint: the picker's "narrows from a word's third
// character" hint shows exactly when the daemon would list by recency — no
// word of three characters yet — not merely while the whole query is short
// (review F4).
func TestHistoryNarrowsHint(t *testing.T) {
	for q, want := range map[string]bool{"": false, "fi": false, "a b c d": false, "fix": true, "fix a": true, "a parser": true} {
		if got := historyNarrows(q); got != want {
			t.Errorf("historyNarrows(%q) = %v, want %v", q, got, want)
		}
	}
}
