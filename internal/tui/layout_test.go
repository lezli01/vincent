package tui

import "testing"

// TestLayoutThreePane covers the accordion across focus targets: the focused
// band expands, the other collapses, and the task table never drops below
// its five-row floor.
func TestLayoutThreePane(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		focus panelID
		tasks int // wanted outer height of the tasks box
	}{
		{"tasks focused", 120, 32, panelTasks, 32 - collapsedBandH},
		{"timeline focused", 120, 32, panelTimeline, tasksFloorH},
		{"output focused", 120, 32, panelOutput, tasksFloorH},
		{"tasks focused at the floor", 80, minAreaH3, panelTasks, minAreaH3 - collapsedBandH},
		{"output focused at the floor", 80, minAreaH3, panelOutput, tasksFloorH},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			boxes := layout(tc.w, tc.h, tc.focus)
			if len(boxes) != 3 {
				t.Fatalf("layout(%d, %d) returned %d boxes, want 3", tc.w, tc.h, len(boxes))
			}
			tasks, tl, out := boxes[0], boxes[1], boxes[2]
			if tasks.id != panelTasks || tl.id != panelTimeline || out.id != panelOutput {
				t.Fatalf("box order = %v %v %v, want tasks timeline output", tasks.id, tl.id, out.id)
			}
			if tasks.h != tc.tasks {
				t.Errorf("tasks box height = %d, want %d", tasks.h, tc.tasks)
			}
			// The task table is the navigation spine: full width, on top.
			if tasks.x != 0 || tasks.y != 0 || tasks.w != tc.w {
				t.Errorf("tasks box = %+v, want full-width at the origin", tasks)
			}
			// The bottom band tiles the rest exactly: side by side, no gap,
			// no overlap, flush with both edges.
			if tl.y != tasks.h || out.y != tasks.h {
				t.Errorf("band y = %d/%d, want %d", tl.y, out.y, tasks.h)
			}
			if tl.h != tc.h-tasks.h || out.h != tc.h-tasks.h {
				t.Errorf("band heights = %d/%d, want %d", tl.h, out.h, tc.h-tasks.h)
			}
			if tl.x != 0 || out.x != tl.w || tl.w+out.w != tc.w {
				t.Errorf("band widths: timeline %+v output %+v do not tile width %d", tl, out, tc.w)
			}
			// The timeline is the index, the output the content.
			if tl.w >= out.w {
				t.Errorf("timeline width %d >= output width %d", tl.w, out.w)
			}
		})
	}
}

// TestLayoutFloors pins §15's stated floors, shifted by the shell chrome:
// below 80 wide or the three-pane area floor the shell falls to a single
// panel; below 60 wide or the hard area floor it renders nothing but the
// size line.
func TestLayoutFloors(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		boxes int
	}{
		{"three-pane at the floor", minShellW, minAreaH3, 3},
		{"one column too narrow", minShellW - 1, minAreaH3, 1},
		{"one row too short", minShellW, minAreaH3 - 1, 1},
		{"single-panel at the floor", minTermW, minAreaH1, 1},
		{"below the hard width floor", minTermW - 1, minAreaH1, 0},
		{"below the hard height floor", minTermW, minAreaH1 - 1, 0},
		{"tiny", 10, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			boxes := layout(tc.w, tc.h, panelTasks)
			if len(boxes) != tc.boxes {
				t.Fatalf("layout(%d, %d) returned %d boxes, want %d", tc.w, tc.h, len(boxes), tc.boxes)
			}
		})
	}
}

// TestLayoutSinglePanel asserts single-panel mode shows exactly the focused
// panel, full area — tab swapping which is the shell's job, so layout must
// honor every focus target.
func TestLayoutSinglePanel(t *testing.T) {
	for _, focus := range []panelID{panelTasks, panelTimeline, panelOutput} {
		boxes := layout(70, 12, focus)
		if len(boxes) != 1 {
			t.Fatalf("layout(70, 12, %v) returned %d boxes, want 1", focus, len(boxes))
		}
		b := boxes[0]
		if b.id != focus {
			t.Errorf("single panel = %v, want the focused %v", b.id, focus)
		}
		if b.x != 0 || b.y != 0 || b.w != 70 || b.h != 12 {
			t.Errorf("single panel box = %+v, want the full 70×12 area", b)
		}
	}
}

// TestLayoutTasksRowFloor asserts the five-row promise concretely: the
// collapsed tasks box leaves enough content height for the board's chrome,
// the table header, and five task rows.
func TestLayoutTasksRowFloor(t *testing.T) {
	boxes := layout(100, 40, panelOutput)
	if len(boxes) != 3 {
		t.Fatalf("layout returned %d boxes, want 3", len(boxes))
	}
	content := boxes[0].h - 2 // borders
	// The board spends chromeLines() on its header and action lines; the
	// bubbles table spends one more on the column header row.
	rows := content - 3 - 1
	if rows < 5 {
		t.Fatalf("collapsed tasks box shows %d rows, want at least 5", rows)
	}
}
