package workflow

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// checklistItem is a numbered checklist line as a version-coupled built-in
// renders it, or one of its continuation lines, indented three or four
// spaces to sit under the item's text.
var checklistItem = regexp.MustCompile(`^(\d+\. | {3,4}\S)`)

// The version-coupled checklists stay wrapped at 80 columns as the agent
// reads them (review F7): an item edited in place without a reflow lands in
// the rendered prompt — and the modernize golden — as a ragged line.
func TestBuiltinChecklistsStayWrapped(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		fields     map[string]string
	}{
		{UpdateWorkflowsName, "modernize", nil},
		{UpdateWorkflowsName, "modernize", map[string]string{"global": "true"}},
		{UpdateTriggersName, "propose", nil},
	} {
		prompt, err := Render("prompt", stepByID(t, builtins()[tc.name].Workflow.Steps, tc.step).Prompt, projectRunContext(tc.fields))
		if err != nil {
			t.Fatalf("Render(%s/%s) error = %v", tc.name, tc.step, err)
		}
		inList := false
		for line := range strings.SplitSeq(prompt, "\n") {
			if strings.HasPrefix(line, "1. ") {
				inList = true
			}
			if strings.HasPrefix(line, "#") {
				inList = false
			}
			if inList && checklistItem.MatchString(line) && utf8.RuneCountInString(line) > 80 {
				t.Errorf("%s/%s (fields %v): checklist line is %d columns, wrap it at 80:\n%s",
					tc.name, tc.step, tc.fields, utf8.RuneCountInString(line), line)
			}
		}
	}
}
