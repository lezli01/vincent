package tui

import (
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatDiffFiles is a two-file diff with one file that cannot be rejected per
// hunk, as the daemon would answer it.
func chatDiffFiles() *apiclient.ChatDiff {
	return &apiclient.ChatDiff{
		Scope: apiclient.ChatDiffScopeBase, Added: 3, Removed: 1,
		Files: []apiclient.ChatDiffFile{
			{Path: "a.go", Status: "modified", Rejectable: true, Added: 2, Removed: 1, Hunks: []apiclient.ChatDiffHunk{
				{Hash: "h1", Header: "@@ -1,2 +1,2 @@", Body: " keep\n-old\n+new\n", Added: 1, Removed: 1},
				{Hash: "h2", Header: "@@ -9,1 +9,2 @@", Body: " tail\n+more\n", Added: 1},
			}},
			{Path: "b.go", Status: "added", Rejectable: true, Added: 1, Hunks: []apiclient.ChatDiffHunk{
				{Hash: "h3", Header: "@@ -0,0 +1 @@", Body: "+fresh\n", Added: 1},
			}},
			{Path: "logo.png", Status: "modified", Binary: true},
		},
	}
}

// chatDiffFixture is a chat with its diff pane open on chatDiffFiles, opened
// with the registry's own key.
func chatDiffFixture(t *testing.T) *chatView {
	t.Helper()
	v := chatViewFixture()
	v.client = offlineClient()
	v.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v.updateKey(synthKey(opKey("chat_diff")))
	if !v.diff.open {
		t.Fatal("the chat_diff key did not open the diff pane")
	}
	v.applyDiff(chatDiffMsg{chatID: v.chatID, scope: apiclient.ChatDiffScopeBase, diff: chatDiffFiles()})
	v.render(100, 30)
	return v
}

func chatDiffScreen(v *chatView) string { return ansi.Strip(v.render(100, 30)) }

// TestChatDiffKeySwapsThePane: the key swaps the conversation for the diff,
// the pane then holds the keyboard (task 169 decision 3), and esc gives it
// back with the draft byte-identical.
func TestChatDiffKeySwapsThePane(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "ask", ResultText: "the answer"}}
	v.composer.SetValue("half a  thought\n  with trailing space ")
	// As the composer holds it: that is what must come back byte for byte.
	draft := v.composer.Value()
	v.draft.loaded, v.draft.saved = true, draft
	if !strings.Contains(chatDiffScreen(v), "the answer") {
		t.Fatal("the conversation is not on screen before the key")
	}

	v.updateKey(registryKey(t, "alt+d"))
	if !v.diff.open || v.capturesInput() || v.bindingContext() != ctxChatDiff {
		t.Fatalf("after alt+d: open %v, captures %v, context %q", v.diff.open, v.capturesInput(), v.bindingContext())
	}
	v.applyDiff(chatDiffMsg{chatID: v.chatID, scope: apiclient.ChatDiffScopeBase, diff: chatDiffFiles()})
	screen := chatDiffScreen(v)
	if strings.Contains(screen, "the answer") || !strings.Contains(screen, "a.go") || !strings.Contains(screen, "since the base") {
		t.Fatalf("the diff did not take the conversation's place:\n%s", screen)
	}
	// Letters are the pane's, not the draft's.
	for _, k := range []string{"j", "O", "]", "["} {
		v.updateKey(registryKey(t, k))
	}
	if v.composer.Value() != draft {
		t.Fatalf("a pane key reached the draft: %q", v.composer.Value())
	}

	v.updateKey(registryKey(t, "esc"))
	if v.diff.open || !v.capturesInput() || v.bindingContext() != ctxChat {
		t.Fatalf("after esc: open %v, captures %v, context %q", v.diff.open, v.capturesInput(), v.bindingContext())
	}
	if v.composer.Value() != draft {
		t.Fatalf("esc changed the draft: %q, want %q", v.composer.Value(), draft)
	}
	if !strings.Contains(chatDiffScreen(v), "the answer") {
		t.Fatal("esc did not bring the conversation back")
	}
}

// TestChatDiffKeyRebinds: a rebound chat_diff opens the pane on its new key,
// and the default no longer does — it is typed nowhere, and opens nothing.
func TestChatDiffKeyRebinds(t *testing.T) {
	withKeymap(t, map[string]string{"chat_diff": "alt+z"})
	v := chatViewFixture()
	v.client = offlineClient()
	v.updateKey(synthKey("alt+d"))
	if v.diff.open {
		t.Fatal("the default key still opens the pane after a rebind")
	}
	v.updateKey(synthKey("alt+z"))
	if !v.diff.open {
		t.Fatal("the rebound key did not open the pane")
	}
	v.diff.stat = &apiclient.ChatDiffStat{Files: 2, Added: 3, Removed: 1}
	v.chat.Branch = "vincent/chat-1"
	pane := ansi.Strip(strings.Join(chatDetailsPane(*v.chat, true, nil, v.changes(), v.now(), 34, 30), "\n"))
	if !strings.Contains(pane, "alt+z opens the diff") || strings.Contains(pane, "alt+d") {
		t.Fatalf("the changes row does not name the rebound key:\n%s", pane)
	}
	// And it closes it again, as the key that opened it.
	v.updateKey(synthKey("alt+z"))
	if v.diff.open {
		t.Fatal("the rebound key did not close the pane")
	}
}

// TestChatDiffHunkKeys walks the hunks across files and holds what x and a
// ask the daemon for to the hunk under the cursor.
func TestChatDiffHunkKeys(t *testing.T) {
	v := chatDiffFixture(t)
	var got []string
	for range 4 {
		v.updateKey(registryKey(t, "]"))
		ref, ok := v.diff.pane.selectedHunk()
		if !ok {
			t.Fatal("] selected no hunk")
		}
		got = append(got, ref.id)
	}
	// The binary file has no hunk to land on, so the walk stops at h3.
	if want := "h1 h2 h3 h3"; strings.Join(got, " ") != want {
		t.Fatalf("] walked %v, want %s", got, want)
	}
	v.updateKey(registryKey(t, "["))
	if ref, _ := v.diff.pane.selectedHunk(); ref.id != "h2" || ref.path != "a.go" {
		t.Fatalf("[ landed on %+v, want a.go's h2", ref)
	}
	if _, cmd := v.updateKey(registryKey(t, "x")); cmd == nil || !v.diff.busy {
		t.Fatal("x on a selected hunk asked the daemon for nothing")
	}
	// One at a time: a second press while the first is in flight is spent.
	if _, cmd := v.updateKey(registryKey(t, "x")); cmd != nil {
		t.Fatal("a second x queued behind the first")
	}
}

// TestChatDiffNotRejectableSaysWhy is decision 6: x on a binary file does
// nothing but say why, and the pane names the file as not rejectable.
func TestChatDiffNotRejectableSaysWhy(t *testing.T) {
	v := chatDiffFixture(t)
	v.updateKey(registryKey(t, "]")) // a hunk is selected, in a.go
	for v.diff.pane.cursorPath != "logo.png" {
		before := v.diff.pane.cursorPath
		v.updateKey(registryKey(t, "down"))
		if v.diff.pane.cursorPath == before {
			t.Fatalf("the file cursor stopped at %q before the binary file", before)
		}
	}
	if _, cmd := v.updateKey(registryKey(t, "x")); cmd != nil || v.diff.busy {
		t.Fatal("x on a binary file sent a reject")
	}
	if !strings.Contains(v.note, "not rejectable per hunk") {
		t.Fatalf("x on a binary file said %q", v.note)
	}
	if screen := chatDiffScreen(v); !strings.Contains(screen, "binary file — not rejectable per hunk") {
		t.Fatalf("the pane does not name the binary file as not rejectable:\n%s", screen)
	}
}

// TestChatDiffRefusals is decision 7: a live turn is a plain message, a stale
// hunk re-reads the diff, a conflict is shown.
func TestChatDiffRefusals(t *testing.T) {
	conflict := func(code, msg string) error {
		return &apiclient.Error{Status: http.StatusConflict, Code: code, Message: msg}
	}
	v := chatDiffFixture(t)
	v.diff.busy = true
	if cmd := v.applyHunkRejected(chatHunkRejectedMsg{chatID: v.chatID, err: conflict(apiclient.CodeChatTurnLive, "live")}); cmd != nil {
		t.Fatal("a live turn re-read something")
	}
	if v.diff.busy || !strings.Contains(v.note, "a turn is running") {
		t.Fatalf("chat_turn_live: busy %v, note %q", v.diff.busy, v.note)
	}
	if cmd := v.applyHunkMarked(chatHunkMarkedMsg{chatID: v.chatID, err: conflict(apiclient.CodeChatHunkStale, "gone")}); cmd == nil || !v.diff.fetching {
		t.Fatal("chat_hunk_stale did not re-read the diff")
	}
	v.applyHunkRejected(chatHunkRejectedMsg{chatID: v.chatID, err: conflict(apiclient.CodeChatHunkConflict, "git would not reverse it")})
	if v.note != "git would not reverse it" || !v.noteBad {
		t.Fatalf("chat_hunk_conflict shown as %q", v.note)
	}
}

// TestChatDiffTurnScopeUnavailable is decision 7's other half: the turn scope
// with no checkpoint is an empty state carrying the daemon's reason, and the
// base scope's files are not shown under its name.
func TestChatDiffTurnScopeUnavailable(t *testing.T) {
	v := chatDiffFixture(t)
	if _, cmd := v.updateKey(registryKey(t, "s")); cmd == nil || v.diffScope() != apiclient.ChatDiffScopeTurn {
		t.Fatalf("s did not switch to the turn scope (scope %q)", v.diffScope())
	}
	v.applyDiff(chatDiffMsg{chatID: v.chatID, scope: apiclient.ChatDiffScopeTurn, err: &apiclient.Error{
		Status: http.StatusConflict, Code: apiclient.CodeChatDiffUnavailable, Message: "turn 3 has no checkpoint to diff from",
	}})
	screen := chatDiffScreen(v)
	if !strings.Contains(screen, "since the last turn") || !strings.Contains(screen, "turn 3 has no checkpoint to diff from") {
		t.Fatalf("the empty state does not show the daemon's reason:\n%s", screen)
	}
	if strings.Contains(screen, "a.go") || v.diffScope() != apiclient.ChatDiffScopeTurn {
		t.Fatalf("the pane fell back to the base scope:\n%s", screen)
	}
	// A late answer for the scope the reader left is dropped.
	v.applyDiff(chatDiffMsg{chatID: v.chatID, scope: apiclient.ChatDiffScopeBase, diff: chatDiffFiles()})
	if strings.Contains(chatDiffScreen(v), "a.go") {
		t.Fatal("a base-scope answer was drawn under the turn scope")
	}
}

// TestChatDiffRejectDraft is decision 8 against task 164: the reject's draft
// replaces a clean composer, and is prepended to an unsent edit rather than
// taking it back.
func TestChatDiffRejectDraft(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		v := chatDiffFixture(t)
		v.draft.loaded, v.draft.saved = true, "stored"
		v.composer.SetValue("stored")
		v.applyHunkRejected(chatHunkRejectedMsg{chatID: v.chatID, res: &apiclient.ChatHunkReject{Draft: "NOTE\n\nstored", NoteAdded: true}})
		if v.composer.Value() != "NOTE\n\nstored" || v.draft.saved != "NOTE\n\nstored" || v.noteBad {
			t.Fatalf("composer %q, saved %q, note %q", v.composer.Value(), v.draft.saved, v.note)
		}
	})
	t.Run("unsent edit", func(t *testing.T) {
		v := chatDiffFixture(t)
		v.draft.loaded, v.draft.saved = true, "stored"
		v.composer.SetValue("stored and typed since")
		v.applyHunkRejected(chatHunkRejectedMsg{chatID: v.chatID, res: &apiclient.ChatHunkReject{Draft: "NOTE\n\nstored", NoteAdded: true}})
		if got := v.composer.Value(); got != "NOTE\n\nstored and typed since" {
			t.Fatalf("the unsent edit was not kept under the note: %q", got)
		}
		if v.draftClean() {
			t.Fatal("the merged draft is not marked for saving")
		}
	})
	t.Run("no note", func(t *testing.T) {
		v := chatDiffFixture(t)
		v.draft.loaded, v.draft.saved = true, "stored"
		v.composer.SetValue("stored")
		v.applyHunkRejected(chatHunkRejectedMsg{chatID: v.chatID, res: &apiclient.ChatHunkReject{Draft: "stored", Warning: "the draft is full"}})
		if !strings.Contains(v.note, "the draft is full") || !v.noteBad || v.composer.Value() != "stored" {
			t.Fatalf("note %q (bad %v), composer %q", v.note, v.noteBad, v.composer.Value())
		}
	})
}

// TestChatDiffAcceptedHunksAreFed is decision 9: the marks the daemon sends
// reach the renderer, which draws the check and the reviewed count.
func TestChatDiffAcceptedHunksAreFed(t *testing.T) {
	v := chatDiffFixture(t)
	d := chatDiffFiles()
	d.Files[0].Hunks[0].Accepted = true
	v.applyDiff(chatDiffMsg{chatID: v.chatID, scope: apiclient.ChatDiffScopeBase, diff: d})
	v.updateKey(registryKey(t, "O"))
	screen := chatDiffScreen(v)
	if !strings.Contains(screen, "1/2") || !strings.Contains(screen, "✓") {
		t.Fatalf("the accepted hunk is not drawn as reviewed:\n%s", screen)
	}
	v.updateKey(registryKey(t, "]"))
	if _, cmd := v.updateKey(registryKey(t, "a")); cmd == nil {
		t.Fatal("a on an accepted hunk asked for nothing")
	}
	v.applyHunkMarked(chatHunkMarkedMsg{chatID: v.chatID, path: "a.go", id: "h1", accepted: false})
	if ref, _ := v.diff.pane.selectedHunk(); ref.accepted || v.diff.busy {
		t.Fatalf("the mark did not clear: %+v", ref)
	}
}

// TestChatDiffNarrow: the pane and the changes row hold at narrow widths —
// no line wider than the pane, and nothing dropped that says where one is.
func TestChatDiffNarrow(t *testing.T) {
	v := chatDiffFixture(t)
	v.updateKey(registryKey(t, "O"))
	for _, w := range []int{24, 40, 80} {
		v.update(tea.WindowSizeMsg{Width: w, Height: 20})
		out := v.render(w, 20)
		lines := strings.Split(out, "\n")
		if len(lines) > 20 {
			t.Errorf("width %d: %d lines in a 20-line pane", w, len(lines))
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("width %d: a line is %d wide: %q", w, got, ansi.Strip(line))
			}
		}
		if !strings.Contains(ansi.Strip(out), "Diff") {
			t.Errorf("width %d: the pane no longer says it is the diff:\n%s", w, ansi.Strip(out))
		}
	}
	stat := chatChanges{stat: &apiclient.ChatDiffStat{Files: 12, Added: 340, Removed: 25}, key: "alt+d"}
	for _, line := range chatDetailsPane(apiclient.Chat{Agent: "claude", Branch: "b"}, true, nil, stat, v.now(), 27, 30) {
		if got := ansi.StringWidth(line); got > 27 {
			t.Errorf("details line is %d wide at 27: %q", got, ansi.Strip(line))
		}
	}
}

// TestTaskDiffTabMovesByHunk is task 169 decision 4: on the task workspace's
// Diff tab `]` and `[` walk the hunks, tab and shift+tab still switch tabs,
// and every other tab keeps the brackets as tab's aliases.
func TestTaskDiffTabMovesByHunk(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDiff)
	v.detail.diff.openTask(v.detail.taskID)
	v.detail.diff.apply(diffLoadedMsg{taskID: v.detail.taskID, text: twoFileDiff})
	v.updateKey(registryKey(t, "]"))
	first, ok := v.detail.diff.selectedHunk()
	if v.tab != taskTabDiff || !ok {
		t.Fatalf("] on the Diff tab: tab %v, hunk selected %v", v.tab, ok)
	}
	v.updateKey(registryKey(t, "]"))
	second, _ := v.detail.diff.selectedHunk()
	if second == first || v.tab != taskTabDiff {
		t.Fatalf("a second ] did not move on: %+v then %+v (tab %v)", first, second, v.tab)
	}
	v.updateKey(registryKey(t, "["))
	if back, _ := v.detail.diff.selectedHunk(); back != first {
		t.Fatalf("[ landed on %+v, want %+v", back, first)
	}
	// No reject and no accept on a task's diff: the hunk carries no id to
	// send, and the tab offers no key for either.
	if first.id != "" {
		t.Fatalf("a task's hunk carries an id: %+v", first)
	}

	v.updateKey(registryKey(t, "tab"))
	if v.tab != taskTabStepDetails {
		t.Fatalf("tab on the Diff tab moved to %v, want Step details", v.tab)
	}
	v.updateKey(registryKey(t, "shift+tab"))
	if v.tab != taskTabDiff {
		t.Fatalf("shift+tab moved to %v, want back on Diff", v.tab)
	}
	v.updateKey(registryKey(t, "shift+tab"))
	if v.tab != taskTabOutput {
		t.Fatalf("shift+tab on the Diff tab moved to %v, want Output", v.tab)
	}
	v.updateKey(registryKey(t, "]"))
	if v.tab != taskTabDiff {
		t.Fatalf("] on the Output tab moved to %v, want Diff — it is still tab's alias there", v.tab)
	}
}
