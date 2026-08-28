package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The detail header names the workflow *and* where its definition came from
// (task 043). A name on its own cannot tell a project `adhoc.yaml` from the
// built-in it shadows, which is the whole reason the origin is recorded.
func TestDetailHeaderNamesTheWorkflowOrigin(t *testing.T) {
	parent := int64(41)
	for _, tc := range []struct {
		name   string
		origin *apiclient.WorkflowOrigin
		want   string
	}{
		{"unrecorded", nil, "adhoc (unknown)"},
		{"builtin", &apiclient.WorkflowOrigin{Scope: "builtin", Digest: "sha256:beef"}, "adhoc (built-in)"},
		{
			"shadowed",
			&apiclient.WorkflowOrigin{Scope: "project", File: ".vincent/workflows/adhoc.yaml"},
			"adhoc (project .vincent/workflows/adhoc.yaml)",
		},
		{
			"lane",
			&apiclient.WorkflowOrigin{Scope: "derived", ParentTaskID: &parent},
			"adhoc (derived from task 41)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 42
			d.applyLoaded(detailLoadedMsg{
				id: d.taskID,
				task: apiclient.TaskDetail{Task: apiclient.Task{
					ID: d.taskID, Title: "detail task", State: stateRunning,
					Workflow: "adhoc", WorkflowOrigin: tc.origin,
				}},
			})
			got := d.headerLine()
			if !strings.Contains(got, tc.want) {
				t.Errorf("header = %q, want it to carry %q", got, tc.want)
			}
		})
	}
}
