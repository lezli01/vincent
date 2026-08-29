package api

import (
	"errors"
	"net/http"

	"github.com/lezli01/vincent/internal/workflow"
)

// GET /v1/tasks/{id}/workflow serves one task's own workflow — its §5.3
// snapshot — as the structure a graph is drawn from (task 051).
//
// It is a different endpoint from GET /v1/workflows/definition, and
// deliberately so. That one is defined by Registry.Lookup and §5.2 shadowing
// (task 017 decisions 10 and 11), which a snapshot does not go through: the
// snapshot is what *ran*, with includes already spliced (§7.9) and any
// `edit + retry` rewrite (§6) reflected, while the registry entry of the same
// name is whatever the file says right now. Drawing the registry copy for a
// task would show a different workflow than the one that ran the moment the
// file is edited, deleted, or shadowed differently.
//
// Two properties carry over from decision 11, and one does not:
//
//   - A snapshot that does not parse is a 200 with findings and a null
//     definition, never a 4xx. It is the same contract the definition
//     endpoint has, and a client that renders one renders the other.
//   - Steps are reported as the snapshot spells them, defaults left in their
//     own block (decision 12).
//   - The registry envelope's `scope`, `file`, `platforms` and
//     `platform_supported` are *not* here. They are registry facts a snapshot
//     has none of, and sending them empty would be four fields inviting a
//     wrong reading (task 051 decision 6). A task's provenance already has
//     its own field, `workflow_origin` on GET /v1/tasks/{id}.
type taskWorkflowResponse struct {
	TaskID   int64            `json:"task_id"`
	Name     string           `json:"name"`
	Errors   []workflow.Error `json:"errors,omitempty"`
	Warnings []workflow.Error `json:"warnings,omitempty"`
	Error    *string          `json:"error"`
	// Definition is null when the snapshot did not parse. Its findings are in
	// Errors, which is the whole reason this is a 200.
	Definition *workflowDefinition `json:"definition"`
}

// handleTaskWorkflow serves GET /v1/tasks/{id}/workflow.
func (s *Server) handleTaskWorkflow(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	out := taskWorkflowResponse{TaskID: t.ID, Name: t.WorkflowName}
	// Parsed here rather than out of the snapshot cache: that cache holds the
	// flat summary the board and the detail view read on every refresh, and
	// widening its entry to carry a whole parsed workflow would grow the
	// resident cost of every listed task to pay for a graph one task at a
	// time opens. `edit + retry` already forgets the cached entry, so either
	// place would have been correctly invalidated — this one simply costs
	// nothing when nobody is looking.
	wf, warnings, err := workflow.Parse([]byte(t.WorkflowSnapshot), workflow.Options{})
	out.Warnings = warnings
	if err != nil {
		var errs workflow.Errors
		if errors.As(err, &errs) {
			out.Errors = errs
		} else {
			out.Errors = workflow.Errors{{Message: err.Error()}}
		}
		msg := err.Error()
		out.Error = &msg
	}
	if wf != nil {
		out.Name = wf.Name
		out.Definition = toWorkflowDefinition(wf)
	}
	writeJSON(w, http.StatusOK, out)
}
