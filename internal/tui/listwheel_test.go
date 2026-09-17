package tui

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// twoProjectChats loads v with one chat in each of two projects, so the two
// selectable rows have a project heading between them: a wheel tick that
// landed on the heading would be a tick that selected nothing.
func twoProjectChats(v *chatsView, state string) {
	a := testChat(1, state, "first")
	b := testChat(2, state, "second")
	b.ProjectID = 8
	v.applyLoaded(chatsLoadedMsg{
		chats:          []apiclient.Chat{a, b},
		names:          map[int64]string{7: "repo", 8: "other"},
		projectsListed: true,
	})
}

// chatRowsForWheel returns the two selectable rows' indices and fails unless
// a heading sits between them, then puts the cursor on the first.
func chatRowsForWheel(t *testing.T, v *chatsView) (first, second int) {
	t.Helper()
	var sel []int
	for i, r := range v.rows() {
		if r.selectable() {
			sel = append(sel, i)
		}
	}
	if len(sel) != 2 || sel[1]-sel[0] != 2 || !v.rows()[sel[0]+1].header {
		t.Fatalf("fixture rows %+v: want two chats with a heading between them", v.rows())
	}
	v.cursor = sel[0]
	v.rememberSelection()
	return sel[0], sel[1]
}

// assertChatsWheelWalks is one tick down and one tick up, each moving exactly
// one chat and skipping the heading.
func assertChatsWheelWalks(t *testing.T, v *chatsView, first, second int) {
	t.Helper()
	v.update(wheelTick(false))
	if v.cursor != second {
		t.Fatalf("a tick down left the cursor on row %d, want %d", v.cursor, second)
	}
	v.update(wheelTick(true))
	if v.cursor != first {
		t.Fatalf("a tick up left the cursor on row %d, want %d", v.cursor, first)
	}
}

// assertChatsWheelHeld is a tick in each direction that moves nothing.
func assertChatsWheelHeld(t *testing.T, v *chatsView, at int, layer string) {
	t.Helper()
	for _, up := range []bool{false, true} {
		v.update(wheelTick(up))
		if v.cursor != at {
			t.Fatalf("a tick under the %s moved the cursor to row %d, want %d", layer, v.cursor, at)
		}
	}
}

// TestChatsBoardWheelMovesTheCursor is task 112 decision 5 on the live chats
// board: one chat per tick, headings skipped, and nothing while a layer that
// owns the keyboard is up — proven by the same tick moving the cursor again
// once that layer closes.
func TestChatsBoardWheelMovesTheCursor(t *testing.T) {
	v := newChatsView()
	v.now = func() time.Time { return testNow }
	twoProjectChats(v, "idle")
	first, second := chatRowsForWheel(t, v)
	assertChatsWheelWalks(t, v, first, second)

	t.Run("new-chat form", func(t *testing.T) {
		v.update(registryKey(t, "n"))
		if v.create == nil {
			t.Fatal("n did not open the new-chat form")
		}
		assertChatsWheelHeld(t, v, first, "new-chat form")
		v.update(registryKey(t, "esc"))
		if v.create != nil {
			t.Fatal("esc did not close the new-chat form")
		}
		assertChatsWheelWalks(t, v, first, second)
	})

	t.Run("archive confirmation", func(t *testing.T) {
		v.update(registryKey(t, "A"))
		if v.confirm == nil {
			t.Fatal("A did not ask before archiving")
		}
		assertChatsWheelHeld(t, v, first, "archive confirmation")
		v.update(registryKey(t, "n"))
		if v.confirm != nil {
			t.Fatal("n did not dismiss the archive confirmation")
		}
		assertChatsWheelWalks(t, v, first, second)
	})

	t.Run("open filter", func(t *testing.T) {
		// A filter narrows the rows the wheel walks; it does not stop it,
		// matching the home board.
		v.update(registryKey(t, "/"))
		if !v.filtering {
			t.Fatal("/ did not open the filter")
		}
		assertChatsWheelWalks(t, v, first, second)
	})
}

// TestArchivedChatsBoardWheelMovesTheCursor is the same on view 10's chats
// half, whose own layer is the permanent delete's confirmation.
func TestArchivedChatsBoardWheelMovesTheCursor(t *testing.T) {
	v := newArchivedChatsView()
	v.now = func() time.Time { return testNow }
	twoProjectChats(v, "archived")
	first, second := chatRowsForWheel(t, v)
	assertChatsWheelWalks(t, v, first, second)

	v.update(registryKey(t, "D"))
	if v.delPrompt == nil {
		t.Fatal("D did not ask before deleting")
	}
	assertChatsWheelHeld(t, v, first, "delete confirmation")
	v.update(registryKey(t, "n"))
	if v.delPrompt != nil {
		t.Fatal("n did not dismiss the delete confirmation")
	}
	assertChatsWheelWalks(t, v, first, second)
}

// TestArchivedTasksBoardWheelMovesTheCursor is view 10's tasks half: a bare
// board, which the home shell's wheel handling never wraps.
func TestArchivedTasksBoardWheelMovesTheCursor(t *testing.T) {
	b := testArchivedBoard()
	b.render(120, 20) // the table pages by the height it was laid out at
	first, ok := b.selected()
	if !ok {
		t.Fatal("fixture board selects no task")
	}
	b.update(wheelTick(false))
	second, _ := b.selected()
	if second == first {
		t.Fatalf("a tick down left the cursor on task %d", first)
	}
	b.update(wheelTick(true))
	if got, _ := b.selected(); got != first {
		t.Fatalf("a tick up left the cursor on task %d, want %d", got, first)
	}

	b.updateKey(registryKey(t, "D"))
	if b.delPrompt == nil {
		t.Fatal("D did not ask before deleting")
	}
	for _, up := range []bool{false, true} {
		b.update(wheelTick(up))
		if got, _ := b.selected(); got != first {
			t.Fatalf("a tick under the delete confirmation moved the cursor to task %d", got)
		}
	}
	b.updateKey(registryKey(t, "n"))
	if b.delPrompt != nil {
		t.Fatal("n did not dismiss the delete confirmation")
	}
	b.update(wheelTick(false))
	if got, _ := b.selected(); got != second {
		t.Fatalf("once the confirmation closed, a tick left the cursor on task %d, want %d", got, second)
	}
}

// TestChatsBoardWheelThroughRoot delivers the tick the way the terminal does:
// through root.Update, its header offset and its active-view routing.
func TestChatsBoardWheelThroughRoot(t *testing.T) {
	m := connectedChatsRoot(t)
	v := m.views[viewChats].(*chatsView)
	before, ok := v.current()
	if !ok {
		t.Fatal("fixture board selects no chat")
	}
	m.Update(wheelTick(false))
	after, _ := v.current()
	if after.ID == before.ID {
		t.Fatalf("a tick through the root left the cursor on chat %d", before.ID)
	}
	m.Update(wheelTick(true))
	if got, _ := v.current(); got.ID != before.ID {
		t.Fatalf("a tick up through the root left the cursor on chat %d, want %d", got.ID, before.ID)
	}
}
