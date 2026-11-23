package tui

import "testing"

// #1083: the card board pages its column, and g/G are home/end there.
func init() {
	panelKeyProbes[ctxTasks]["pgdown"] = func(t *testing.T) {
		b := taskBoardFixture()
		first, _ := b.cardSelected()
		b.update(registryKey(t, "pgdown"))
		if id, _ := b.cardSelected(); id == first {
			t.Fatalf("pgdown left the cursor on #%d", first)
		}
		b.update(registryKey(t, "g"))
		if id, _ := b.cardSelected(); id != first {
			t.Fatalf("g left the cursor on #%d, want the column's first card #%d", id, first)
		}
		b.update(registryKey(t, "G"))
		if id, _ := b.cardSelected(); id == first {
			t.Fatal("G stayed on the column's first card")
		}
	}
}
