package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
)

// stepStatusRequest is the body of POST /v1/tasks/{id}/steps/{step_id}/status
// (§13.2, task 033): the one line the step wants to say about itself.
type stepStatusRequest struct {
	Message string `json:"message"`
}

// handleStepStatus records a running step's own status message.
//
// The producer is this endpoint and not a sentinel line in the step's output
// (task 033 decision 1). A marker printed on stdout would force a
// strip-or-keep choice over the transcript and `result_summary` with no good
// answer, would miss the obvious agent spelling entirely — an agent running
// `echo` through its Bash tool produces a tool-use event, not the output
// event a parser would be watching — and would make every step's stdout a
// control channel, so any program that happened to print the marker would
// change daemon state. An API call has none of those properties, works
// identically for `agent` and `command` steps, and reuses the auth,
// transport and error envelope that already exist.
//
// The step addresses itself with §8.5's VINCENT_TASK_ID and VINCENT_STEP_ID,
// which is why the path is keyed by step id and not by run id: a step's
// process knows which step it is and has no way to learn its row's id. It is
// keyed by step id rather than by task alone because a `parallel` group's
// sub-steps share one task and run at the same time (§7.5).
func (s *Server) handleStepStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.Runner == nil {
		s.internalError(w, "step status", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	stepID := r.PathValue("step_id")
	if strings.TrimSpace(stepID) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "step id is required")
		return
	}
	var req stepStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// An empty message is a clear, not a validation failure: a step that has
	// finished the thing it was narrating should be able to stop saying it
	// without a second endpoint.
	if err := s.deps.Runner.SetStepStatus(r.Context(), id, stepID, req.Message); err != nil {
		switch {
		case errors.Is(err, store.ErrStepNotRunning):
			// 409 and not 404: the step exists in the workflow, it is simply
			// past the point where it can speak. A client re-reads the task
			// and sees why.
			writeConflict(w, err.Error(), map[string]string{"step_id": stepID})
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		default:
			s.internalError(w, "set step status", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		// The normalized text, so a caller sees exactly what was stored —
		// flattened to one line and truncated at taskrun.StatusMessageLimit.
		"message": taskrun.NormalizeStatusMessage(req.Message),
	})
}
