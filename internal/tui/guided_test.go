package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestGuidedTakeoverBreakpointMatchesA128By24Terminal(t *testing.T) {
	if guidedTakeover(guidedTakeoverMinWidth-1, guidedTakeoverMinHeight) {
		t.Error("guided layout opened one column below the breakpoint")
	}
	if guidedTakeover(guidedTakeoverMinWidth, guidedTakeoverMinHeight-1) {
		t.Error("guided layout opened one row below the breakpoint")
	}
	if !guidedTakeover(guidedTakeoverMinWidth, guidedTakeoverMinHeight) {
		t.Error("guided layout stayed compact at the breakpoint")
	}
}

func TestGuidedPaneWidthsConsumeTheSurfaceExactly(t *testing.T) {
	for _, width := range []int{guidedTakeoverMinWidth, 160, 240} {
		rail, main := guidedPaneWidths(width)
		if rail+main != width {
			t.Errorf("width %d split into %d + %d", width, rail, main)
		}
		if rail < guidedRailMinWidth || rail > guidedRailMaxWidth {
			t.Errorf("width %d gave the rail %d cells", width, rail)
		}
	}
}

func TestGuidedSurfaceNeverLeaksPastItsTakeover(t *testing.T) {
	const width, height = guidedTakeoverMinWidth, guidedTakeoverMinHeight
	out := guidedSurface(width, height, "Rail", "one\ntwo", "Focus", "detail")
	lines := strings.Split(out, "\n")
	if len(lines) != height {
		t.Fatalf("surface has %d rows, want %d", len(lines), height)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Errorf("row %d is %d columns, want %d", row, got, width)
		}
	}
}
