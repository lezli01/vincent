package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// tokenText builds a sentence of n distinct, two-digit-numbered words, so a
// test can prove every word reached the screen without caring where a wrap
// put the line breaks or what a guided rail drew beside them (#791).
func tokenText(prefix string, n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("%s%02d", prefix, i)
	}
	return strings.Join(words, " ")
}

// longDescription is the issue's 380-odd-character description, and
// longError its 300-character validation error.
var (
	longDescription = tokenText("dsc", 64)
	longError       = tokenText("err", 50)
)

// assertFullyReadable fails for every word of text that did not render, for
// any line wider than the screen — the compact new-task form hands the root
// over-wide lines and the root cuts them, so a word present here is not yet
// a word on screen — and for any line that carries a word of it and ends in
// an ellipsis. The picker's own option rows ("▸ broken  invalid: …") are one
// row each by design and outside #791, so neither rule holds them.
func assertFullyReadable(t *testing.T, what, out, text string, width int) {
	t.Helper()
	plain := ansi.Strip(out)
	for _, line := range strings.Split(plain, "\n") {
		if w := ansi.StringWidth(line); w > width && !strings.Contains(line, "invalid:") {
			t.Errorf("%s: a line is %d cells wide on a %d-cell screen: %q", what, w, width, line)
		}
	}
	var missing []string
	for _, word := range strings.Fields(text) {
		if !strings.Contains(plain, word) {
			missing = append(missing, word)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %d of %d words never rendered (first %q):\n%s",
			what, len(missing), len(strings.Fields(text)), missing[0], plain)
	}
	prefix := strings.Fields(text)[0][:3]
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimRight(line, " │")
		if strings.Contains(line, prefix) && !strings.Contains(line, "invalid:") &&
			strings.HasSuffix(trimmed, "…") {
			t.Errorf("%s: a line of it is cut with an ellipsis: %q", what, line)
		}
	}
}

// The workflows screen draws a description as one row, cut at the pane edge
// at every width (#791, WF-1 and WF-3). The selected workflow's description
// must wrap instead, in the compact list and in the guided overview.
func TestWorkflowsWrapTheSelectedDescription(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {250, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			w := newWorkflowsView()
			e := globalEntry("review")
			e.Description = longDescription
			loadedWorkflows(w, e)
			out := w.render(size[0], size[1])
			assertFullyReadable(t, "description", out, longDescription, size[0])
		})
	}
}

// The same for a validation error: the compact list joins it onto the
// entry's one line, and the overview's findings are one row each.
func TestWorkflowsWrapTheSelectedValidationError(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {250, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			w := newWorkflowsView()
			e := globalEntry("busted")
			e.Errors = []apiclient.WorkflowFinding{{Message: longError}}
			e.Error = &longError
			loadedWorkflows(w, e)
			out := w.render(size[0], size[1])
			assertFullyReadable(t, "validation error", out, longError, size[0])
		})
	}
}

// An unselected row that does not fit says so with an ellipsis rather than
// stopping dead at the edge.
func TestWorkflowsEllipsizeACutUnselectedRow(t *testing.T) {
	w := newWorkflowsView()
	first := globalEntry("alpha")
	second := globalEntry("beta")
	second.Description = longDescription
	loadedWorkflows(w, first, second)
	out := ansi.Strip(w.render(80, 24))
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "beta") {
			if !strings.HasSuffix(strings.TrimRight(line, " "), "…") {
				t.Errorf("unselected cut row does not end in …: %q", line)
			}
			return
		}
	}
	t.Fatalf("beta row not rendered:\n%s", out)
}

// A long name pushes the invalid badge off the rail and the list (WF-1,
// WF-5): under NO_COLOR nothing else says the entry is broken.
func TestWorkflowsKeepTheInvalidBadgeBesideALongName(t *testing.T) {
	name := strings.Repeat("n", 64)
	bad := "steps: at least one step is required"
	for _, size := range [][2]int{{80, 24}, {250, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			w := newWorkflowsView()
			e := globalEntry(name)
			e.Errors = []apiclient.WorkflowFinding{{Message: bad}}
			e.Error = &bad
			loadedWorkflows(w, e)
			out := ansi.Strip(w.render(size[0], size[1]))
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "nnnnnnnn") && strings.Contains(line, "invalid") {
					return
				}
			}
			t.Errorf("no row carries both the name and its invalid badge:\n%s", out)
		})
	}
}

// longForm is loadedForm with a workflow whose description, error and step
// name are each too long for one row.
func longForm(t *testing.T) *newTask {
	t.Helper()
	n := loadedForm(t)
	n.workflows = append(n.workflows,
		apiclient.WorkflowEntry{
			Name: "verbose", Scope: "global", Description: longDescription,
			Steps: []apiclient.WorkflowEntryStep{{
				ID: "plan", Name: "Plan the change with the architect agent",
				Type: "agent", Agent: "claude",
			}},
		},
		apiclient.WorkflowEntry{
			Name: "broken", Scope: "global",
			Errors: []apiclient.WorkflowFinding{{Message: longError}},
			Error:  &longError,
		},
	)
	return n
}

// The new-task form appends the description and each ✗ line with no width
// (N1): the pane cuts them. Both the committed workflow's detail and the
// picker's preview must wrap, compact and guided.
func TestNewTaskWrapsTheWorkflowDetail(t *testing.T) {
	for _, size := range [][2]int{{80, 40}, {250, 60}} {
		for _, tc := range []struct{ workflow, what, text string }{
			{"verbose", "description", longDescription},
			{"broken", "validation error", longError},
		} {
			name := fmt.Sprintf("%dx%d/%s", size[0], size[1], tc.workflow)
			t.Run(name+"/detail", func(t *testing.T) {
				n := longForm(t)
				n.projectID = 1
				n.workflow = tc.workflow
				moveTo(n, ntWorkflow)
				out := n.render(size[0], size[1])
				assertFullyReadable(t, tc.what, out, tc.text, size[0])
			})
			t.Run(name+"/picker", func(t *testing.T) {
				n := longForm(t)
				n.projectID = 1
				moveTo(n, ntWorkflow)
				press(n, "enter")
				if n.mode != ntPicking || n.pick == nil {
					t.Fatal("workflow picker did not open")
				}
				for range len(n.pick.options) {
					if opt, ok := n.pick.current(); ok && opt.value == tc.workflow {
						break
					}
					press(n, "down")
				}
				if opt, ok := n.pick.current(); !ok || opt.value != tc.workflow {
					t.Fatalf("picker never reached %s", tc.workflow)
				}
				out := n.render(size[0], size[1])
				assertFullyReadable(t, tc.what, out, tc.text, size[0])
			})
		}
	}
}

// A step name over 24 characters runs straight into its type column.
func TestNewTaskSeparatesALongStepNameFromItsType(t *testing.T) {
	n := longForm(t)
	out := ansi.Strip(strings.Join(n.renderWorkflowDetail("verbose"), "\n"))
	if strings.Contains(out, "agentagent") || strings.Contains(out, "agent agent") {
		t.Errorf("step name runs into its type column:\n%s", out)
	}
}
