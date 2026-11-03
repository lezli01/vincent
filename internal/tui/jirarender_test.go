package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/jira/render"
)

// The Jira converter (internal/jira/render) promises Markdown inside this
// package's subset. These tests hold it to that promise against the parser
// itself, so a shape one side changes cannot quietly stop parsing.

// renderInline converts html and runs the result through the pane's inline
// scanner, returning the segments and the link registry.
func renderInline(t *testing.T, src string) (string, []segment, *mdRefs) {
	t.Helper()
	md := render.HTMLToMarkdown(src, nil)
	refs := &mdRefs{}
	return md, inlineSegments(md, lipgloss.NewStyle(), 0, refs), refs
}

// TestJiraEmphasisNestingParses is review F8: bold inside italic and italic
// inside bold both come out with every mark consumed, and the nested text
// carries both styles.
func TestJiraEmphasisNestingParses(t *testing.T) {
	for _, tc := range []struct {
		in     string
		styled string // the text that must be bold and italic
	}{
		{"<b><i>x</i></b>", "x"},
		{"<i><b>x</b></i>", "x"},
		{"<strong>a <em>b</em> c</strong>", "b"},
		{"<em>a <strong>b</strong> c</em>", "b"},
		{"<b>a <i>b</i></b>", "b"},
		{"<i>a <b>b</b></i>", "b"},
		{"<b><b>x</b></b>", ""},
		{"<i><i>x</i></i>", ""},
	} {
		md, segs, _ := renderInline(t, tc.in)
		found := tc.styled == ""
		for _, s := range segs {
			if strings.ContainsAny(s.text, "*_") {
				t.Errorf("%s → %q: a mark survived as text in %q", tc.in, md, s.text)
			}
			if s.text == tc.styled && s.style.GetBold() && s.style.GetItalic() {
				found = true
			}
		}
		if !found {
			t.Errorf("%s → %q: %q is not bold and italic in %+v", tc.in, md, tc.styled, segs)
		}
	}
}
