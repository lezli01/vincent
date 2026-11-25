package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1163: the task Diff and the chat diff answer the same keys. ctrl+r reads
// either again, and `r` is the chat diff's no longer.
func TestChatDiffReloadsOnCtrlR(t *testing.T) {
	v := chatDiffFixture(t)
	if _, cmd := v.updateKey(registryKey(t, "r")); cmd != nil || v.diff.fetching {
		t.Fatal("r still reads the chat diff again")
	}
	if _, cmd := v.updateKey(registryKey(t, "ctrl+r")); cmd == nil || !v.diff.fetching {
		t.Fatal("ctrl+r did not read the chat diff again")
	}
}

// chatDiffSearch commits a search for q on the chat diff.
func chatDiffSearch(t *testing.T, v *chatView, q string) {
	t.Helper()
	v.updateKey(registryKey(t, "/"))
	if !v.diff.pane.find.typing || !v.capturesInput() {
		t.Fatal("/ on the chat diff did not open a search that takes the keys")
	}
	for _, r := range q {
		v.updateKey(keyPress(string(r)))
	}
	v.updateKey(namedKey("enter"))
}

// #1163: `/` searches the chat diff with the task Diff's own search — every
// file opened, n and N walking the matches, esc ending the search before it
// leaves the pane, and leaving the pane ending it too (#1154).
func TestChatDiffSearch(t *testing.T) {
	v := chatDiffFixture(t)
	chatDiffSearch(t, v, "e")
	if len(v.diff.pane.find.hits) < 2 {
		t.Fatalf("a search for e found %d lines", len(v.diff.pane.find.hits))
	}
	if v.bindingContext() != ctxPaneFind {
		t.Fatalf("a running search is described as %q", v.bindingContext())
	}
	if screen := chatDiffScreen(v); !strings.Contains(screen, "match 1/") {
		t.Fatalf("the chat diff draws no search line:\n%s", screen)
	}
	v.updateKey(registryKey(t, "n"))
	if v.diff.pane.find.cur != 1 {
		t.Fatalf("n left the search on match %d", v.diff.pane.find.cur)
	}
	v.updateKey(registryKey(t, "esc"))
	if v.diff.pane.find.active() || !v.diff.open {
		t.Fatalf("esc on a search: search %v, pane open %v", v.diff.pane.find.active(), v.diff.open)
	}
	chatDiffSearch(t, v, "e")
	v.updateKey(registryKey(t, "alt+d"))
	v.updateKey(registryKey(t, "alt+d"))
	if v.diff.pane.find.active() {
		t.Fatal("the search outlived leaving the chat diff")
	}
}

// #1163: ctrl+y on the chat diff copies the hunk under the cursor first,
// as the task Diff's does (#1076).
func TestChatDiffCopiesTheHunk(t *testing.T) {
	v := chatDiffFixture(t)
	v.updateKey(registryKey(t, "]"))
	_, cmd := v.updateKey(registryKey(t, "ctrl+y"))
	msg, ok := drain(cmd).(openCopyPickerMsg)
	if !ok || len(msg.items) == 0 || msg.items[0].label != "this hunk" {
		t.Fatalf("ctrl+y on the chat diff produced %#v", msg)
	}
}

// #1163 under #1162's rule: the hunk-reject question owns n and esc over a
// committed search.
func TestChatDiffRejectOwnsAnswersOverSearch(t *testing.T) {
	for _, key := range []string{"n", "N", "esc"} {
		v := chatDiffFixture(t)
		chatDiffSearch(t, v, "e")
		v.updateKey(registryKey(t, "]"))
		v.updateKey(registryKey(t, "x"))
		if v.diff.rejectAsk == nil {
			t.Fatal("x did not ask")
		}
		cur := v.diff.pane.find.cur
		v.updateKey(registryKey(t, key))
		if v.diff.rejectAsk != nil {
			t.Fatalf("%s went to the search, not the question", key)
		}
		if !v.diff.pane.find.active() || v.diff.pane.find.cur != cur || !v.diff.open {
			t.Fatalf("%s answered the question and moved the search too", key)
		}
	}
}

// #1163 under #1162's rule: a pending task confirmation owns y/Y/n/N/esc
// over a committed search on Output or Diff.
func TestTaskConfirmationOwnsAnswersOverSearch(t *testing.T) {
	for _, tab := range []taskViewTab{taskTabOutput, taskTabDiff} {
		for _, key := range []string{"n", "N", "esc"} {
			v := outputWithLines(t, "one needle", "two", "three needle")
			v.setTab(tab)
			find := &v.detail.find
			if tab == taskTabDiff {
				v.detail.diff = *loadedDiff(t, twoFileDiff)
				v.detail.diff.render(100, 30)
				find = &v.detail.diff.find
			}
			v.updateKey(registryKey(t, "/"))
			typeQuery(v, map[taskViewTab]string{taskTabOutput: "needle", taskTabDiff: "+"}[tab])
			if find.query == "" {
				t.Fatal("no search was committed (the test proves nothing)")
			}
			cur := find.cur
			v.detail.actions.pending, v.detail.actions.pendingID = apiclient.ActionCancel, 4
			cmd := v.updateKey(registryKey(t, key))
			if v.detail.actions.pending != "" {
				t.Fatalf("tab %v: %s went to the search, not the confirmation", tab, key)
			}
			if !find.active() || find.cur != cur {
				t.Fatalf("tab %v: %s answered the confirmation and moved the search too", tab, key)
			}
			if cmd != nil {
				if _, popped := drain(cmd).(navPopMsg); popped {
					t.Fatalf("tab %v: %s left the task", tab, key)
				}
			}
		}
	}
}
