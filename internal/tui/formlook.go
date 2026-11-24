package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The pieces the wide new-task and new-chat screens share (task 150.11): a form
// is a stack of titled sections, and the one the keyboard is in has the thick
// border, so where you are does not depend on colour.

// formWideMinH is the shortest terminal that gets the wide forms.
const formWideMinH = 24

// formSection is one titled group of a form.
func formSection(title, right string, body []string, w int, focused bool) []string {
	if focused {
		return drawBox(styleFocus.Bold(true).Render(title), right, body, w, len(body)+2, edgesThick, styleFocus)
	}
	return drawBox(styleTitle.Render(title), right, body, w, len(body)+2, edgesRounded, styleDim)
}

// formChip is one choice of a row of chips. Reverse video marks the chosen one,
// so it survives NO_COLOR.
func formChip(label string, on bool) string {
	if on {
		return styleTabActive.Render(" " + label + " ")
	}
	return styleDim.Render(" " + label + " ")
}

// formPill says whether a draft can be created, for the form's top edge.
func formPill(ready bool, label string) string {
	style := styleWarn
	if ready {
		style = styleOK
	}
	return style.Bold(true).Reverse(true).Render(" " + label + " ")
}

// formRowMark is the cursor in front of a section's row.
func formRowMark(focused bool) string {
	if focused {
		return styleFocus.Render("› ")
	}
	return "  "
}

// formStack piles sections up and remembers where the focused one is, so a form
// taller than its pane keeps that section on screen.
type formStack struct {
	lines    []string
	from, to int
	// tags is, per line, what a click on it is about — a setting's path —
	// and "" for borders and gaps (#1065). Only a stack built with tags
	// keeps it in step with lines.
	tags []string
}

// addTagged is add with a tag for each line of block.
func (s *formStack) addTagged(block, tags []string, focused bool) {
	s.add(block, focused)
	s.tags = append(s.tags, tags...)
	for len(s.tags) < len(s.lines) {
		s.tags = append(s.tags, "")
	}
}

// viewStart is the first line view shows.
func (s *formStack) viewStart(height int) int {
	return windowRangeStart(len(s.lines), s.from, s.to, height)
}

// add appends a block, marking it as the focused one when it is.
func (s *formStack) add(block []string, focused bool) {
	if focused {
		s.from, s.to = len(s.lines), len(s.lines)+len(block)
	}
	s.lines = append(s.lines, block...)
}

// extend appends a block that belongs to the focused one, so both stay in view.
func (s *formStack) extend(block []string) {
	s.lines = append(s.lines, block...)
	for len(s.tags) < len(s.lines) {
		s.tags = append(s.tags, "")
	}
	s.to = len(s.lines)
}

// view is the stack cut to height around the focused section.
func (s *formStack) view(height int) []string { return windowRange(s.lines, s.from, s.to, height) }

// formBeside lays two blocks of equal width side by side with a one-cell gap.
// The shorter is padded so the columns line up.
func formBeside(left, right []string) []string {
	out := make([]string, max(len(left), len(right)))
	lw := 0
	if len(left) > 0 {
		lw = ansi.StringWidth(left[0])
	}
	for i := range out {
		l, r := strings.Repeat(" ", lw), ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = l + " " + r
	}
	return out
}

// formEqualRows pads two section bodies to one height, so the boxes drawn from
// them end on the same row.
func formEqualRows(a, b []string) (left, right []string) {
	for len(a) < len(b) {
		a = append(a, "")
	}
	for len(b) < len(a) {
		b = append(b, "")
	}
	return a, b
}
