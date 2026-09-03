package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The board's STEP column under a loop rollup (issue #317). formatStep's own
// drop order is pinned in detailloop_test.go; what these prove is that a real
// board, at the widths that actually produce each STEP width, renders that
// order — and that a task carrying no rollup is untouched by any of it.

// loopedBoard is a flat board holding one running task on step 3 of 7, in a
// loop the caller describes.
func loopedBoard(rollup *apiclient.LoopRollup) *board {
	b := testBoard()
	t := task(1, stateRunning, withStep("green", 2, 7))
	t.Loop = rollup
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{t}})
	return b
}

// TestBoardStepColumnDropsFromTheTail walks the three widths a flat board
// gives the STEP column — widthStepLong, a middle tier bought with surplus,
// and its ceiling — and pins what survives at each. The clause dropped first
// is the body step, then the `for_each` item, then the counter.
func TestBoardStepColumnDropsFromTheTail(t *testing.T) {
	rollup := &apiclient.LoopRollup{
		Driver: "for_each", Iteration: 4, Total: 10, MaxIterations: 10,
		Item: "alpha", BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
	}
	for _, tt := range []struct {
		name  string
		width int
		want  string
		// gone is what the width could not afford, which must not appear
		// anywhere on the board — not wrapped onto a second line either.
		gone []string
	}{
		{
			name: "at the column's base width only k/n and the name fit",
			// 18 cells: `3/7 green · loop 4/10` is 21, so even the counter goes.
			width: 110, want: "3/7 green", gone: []string{"loop 4/10", "alpha", "repair"},
		},
		{
			name:  "a middle tier buys the counter",
			width: 170, want: "3/7 green · loop 4/10", gone: []string{"alpha", "repair"},
		},
		{
			name:  "the ceiling buys the item too",
			width: 190, want: "3/7 green · loop 4/10 · alpha", gone: []string{"repair"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := ansi.Strip(loopedBoard(rollup).render(tt.width, 20))
			if !strings.Contains(out, tt.want) {
				t.Errorf("a %d-column board does not show %q:\n%s", tt.width, tt.want, out)
			}
			for _, absent := range tt.gone {
				if strings.Contains(out, absent) {
					t.Errorf("a %d-column board shows %q, which does not fit its STEP column:\n%s",
						tt.width, absent, out)
				}
			}
		})
	}

	// A `count:` loop has no item clause, so the same ceiling reaches the
	// body step — the tier widthStepMax is measured off.
	out := ansi.Strip(loopedBoard(&apiclient.LoopRollup{
		Driver: "count", Iteration: 4, Total: 10, MaxIterations: 10,
		BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
	}).render(190, 20))
	if want := "3/7 green · loop 4/10 · repair 2/3"; !strings.Contains(out, want) {
		t.Errorf("a 190-column board does not show %q:\n%s", want, out)
	}
}

// TestBoardWithoutALoopIsUnchanged: the fitting is dead code for every task
// not in a loop, which is nearly every task. A board of them must render byte
// for byte as it did before the clauses existed — at every width, including
// the ones the loop tiers are bought at.
func TestBoardWithoutALoopIsUnchanged(t *testing.T) {
	for _, width := range []int{80, 110, 170, 190, 200} {
		before := loopedBoard(nil).render(width, 20)
		// A rollup with no iteration yet reports nothing, so it is the same
		// board: that is what keeps a loop's first moments from flickering a
		// half-built clause onto the row.
		after := loopedBoard(&apiclient.LoopRollup{Driver: "count", MaxIterations: 10}).render(width, 20)
		if before != after {
			t.Errorf("width %d: a rollup with no iteration changed the board:\n%s\n---\n%s",
				width, before, after)
		}
		if strings.Contains(ansi.Strip(before), "loop ") {
			t.Errorf("width %d: a task with no loop shows a loop clause:\n%s", width, before)
		}
	}
	// Guard the assertion itself: an empty render would satisfy everything
	// above.
	if out := ansi.Strip(loopedBoard(nil).render(110, 20)); !strings.Contains(out, "3/7 green") {
		t.Fatalf("the fixture board renders no step cell at all:\n%s", out)
	}
}
