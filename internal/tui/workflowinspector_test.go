package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// longStepName is a step name wider than half an 80-column screen — the one
// that used to push the run state off the joined inspector line.
const longStepName = "step-00-run-the-entire-integration-suite-against-every-supported-database"

// inspectorTaskView is a task on its Workflow tab with one agent step that ran
// three attempts, the last one green, and a long name and a two-line prompt.
func inspectorTaskView(t *testing.T, width, height int) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{
		attempt(1, 0, 1, "integration", "failed", false),
		attempt(2, 0, 2, "integration", "failed", false),
		attempt(3, 0, 3, "integration", "succeeded", false),
	})
	v := newTaskView(d)
	v.width, v.height = width, height
	v.setTab(taskTabWorkflow)
	v.update(taskWorkflowMsg{taskID: 4, def: apiclient.TaskWorkflow{
		TaskID: 4, Name: "fixture", Definition: &apiclient.WorkflowBody{
			Name: "fixture", Steps: []apiclient.WorkflowStepDef{{
				ID: "integration", Name: longStepName, Type: "agent",
				Prompt: "Run the integration suite.\nFix whatever fails.",
			}},
		},
	}})
	return v
}

// TestWorkflowInspectorFillsTheTab (#799 W10): at 80×24 and 120×40 the
// inspector is the last thing on the tab — no blank rows under it — and a
// long step name and its run state are each readable on a row of their own.
func TestWorkflowInspectorFillsTheTab(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			v := inspectorTaskView(t, w, h)
			frame := ansi.Strip(v.render(w, h))
			lines := strings.Split(frame, "\n")
			if len(lines) != h {
				t.Fatalf("the tab drew %d lines at height %d:\n%s", len(lines), h, frame)
			}
			rule := -1
			for i, line := range lines {
				if strings.HasPrefix(line, "────") {
					rule = i
				}
			}
			if rule < 0 {
				t.Fatalf("no inspector rule:\n%s", frame)
			}
			under := lines[rule+1:]
			if len(under) != workflowInspectorRows-1 {
				t.Errorf("%d rows under the rule, want %d:\n%s", len(under), workflowInspectorRows-1, frame)
			}
			for i, line := range under {
				if strings.TrimSpace(line) == "" {
					t.Errorf("inspector row %d is blank:\n%s", i, frame)
				}
				if got := ansi.StringWidth(line); got > w {
					t.Errorf("inspector row %d is %d columns wide at width %d", i, got, w)
				}
			}
			rows := strings.Join(under, "\n")
			// Clipped to the width with a stated ellipsis, so what survives is
			// a prefix of the name followed by "…" — or the name whole.
			name := ansi.Truncate("name: "+longStepName, w-3, "")
			if !strings.Contains(rows, name) || len(name) < len("name: step-00-run-the-entire") {
				t.Errorf("the step name is not readable at %d columns:\n%s", w, rows)
			}
			if !strings.Contains(rows, "run: ") || !strings.Contains(rows, "attempt 3") {
				t.Errorf("the run state is not readable at %d columns:\n%s", w, rows)
			}
			if !strings.Contains(rows, "prompt: Run the integration suite. …") {
				t.Errorf("the prompt's first line, marked as cut, is not on the strip:\n%s", rows)
			}
		})
	}
}
