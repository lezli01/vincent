package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestTriggerGrantAsksOnAdd: a start_handoff's grant asks before an edit that
// adds a capability, and writes one that only removes (task 137.9).
func TestTriggerGrantAsksOnAdd(t *testing.T) {
	row := trigRow{
		wfEditRow: wfEditRow{
			field: apiclient.WorkflowSchemaField{Name: "grant", Control: apiclient.WorkflowControlList},
			path:  "action.grant", value: "issue.close, pull.merge",
		},
		dangerousOnAdd: "grant lets the handoff's agent write to GitHub",
	}
	for _, tc := range []struct {
		value string
		ask   bool
	}{
		{"issue.close, pull.merge, issue.comment", true},
		{"issue.comment", true},
		{"pull.merge, issue.close", false},
		{"issue.close", false},
		{unsetMarker, false},
	} {
		v := newTriggersView()
		v.form = &trigFormLayer{rows: []trigRow{row}, rowErr: map[string]string{}, def: map[string]any{}}
		v.formCommit(row, tc.value)
		if got := v.form.confirm != nil; got != tc.ask {
			t.Errorf("%q: asked = %v, want %v", tc.value, got, tc.ask)
			continue
		}
		if tc.ask && !strings.Contains(strings.Join(v.form.confirm.warning, " "), "adds: issue.comment") {
			t.Errorf("%q: warning = %v", tc.value, v.form.confirm.warning)
		}
	}
}
