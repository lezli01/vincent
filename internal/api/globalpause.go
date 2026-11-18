package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/taskrun"
)

// maxPauseReason bounds the optional sentence a pause carries. It is shown
// on one header line and one CLI line; anything longer is a note, not a
// reason, and belongs somewhere else.
const maxPauseReason = 200

// pauseRequest is the optional body of POST /v1/daemon/pause.
type pauseRequest struct {
	Reason string `json:"reason"`
}

// pauseResponse is the global pause as every route reports it (task 149,
// §13.2). `state` is the one field a client switches on; the counts say what
// a `pausing` is still waiting for and what a `paused` is holding.
type pauseResponse struct {
	State string `json:"state"`
	// PausedAt is when the pause was taken; null while active.
	PausedAt *string `json:"paused_at"`
	Reason   string  `json:"reason"`
	// Running is the tasks still executing a step; each parks at its next
	// step boundary. A pause with any is `pausing`.
	Running int `json:"running"`
	// AwaitingInput is the tasks holding an agent idle on a question. They
	// are not waited on: they park at the step boundary after the answer.
	AwaitingInput int `json:"awaiting_input"`
	// Queued is the tasks admission is holding back.
	Queued int `json:"queued"`
	// Held is the tasks the pause parked, which `continue` resumes.
	Held int `json:"held"`
	// Resumed is how many tasks this `continue` resumed; absent elsewhere.
	Resumed *int `json:"resumed,omitempty"`
}

func toPauseResponse(st taskrun.GlobalPauseStatus) pauseResponse {
	out := pauseResponse{
		State:         st.State,
		Running:       st.Tally.Running,
		AwaitingInput: st.Tally.AwaitingInput,
		Queued:        st.Tally.Queued,
		Held:          st.Tally.Paused,
	}
	if st.Pause != nil {
		at := st.Pause.PausedAt.UTC().Format(time.RFC3339)
		out.PausedAt = &at
		out.Reason = st.Pause.Reason
	}
	return out
}

// handlePauseGet serves GET /v1/daemon/pause.
func (s *Server) handlePauseGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireRunner(w) {
		return
	}
	st, err := s.deps.Runner.GlobalPauseState(r.Context())
	if err != nil {
		s.internalError(w, "global pause", err)
		return
	}
	writeJSON(w, http.StatusOK, toPauseResponse(st))
}

// handlePause serves POST /v1/daemon/pause: take the global pause. Pausing
// a paused daemon is a 200 that keeps the original time and reason.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if !s.requireRunner(w) {
		return
	}
	var req pauseRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if strings.ContainsAny(reason, "\r\n") || utf8.RuneCountInString(reason) > maxPauseReason {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"reason must be a single line of at most 200 characters")
		return
	}
	st, err := s.deps.Runner.PauseAll(r.Context(), reason)
	if err != nil {
		s.internalError(w, "global pause", err)
		return
	}
	writeJSON(w, http.StatusOK, toPauseResponse(st))
}

// handleContinue serves POST /v1/daemon/continue: lift the global pause and
// resume the tasks it parked. Continuing an active daemon is a 200 that
// resumes nothing.
func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	if !s.requireRunner(w) {
		return
	}
	st, resumed, err := s.deps.Runner.ContinueAll(r.Context())
	if err != nil {
		s.internalError(w, "global continue", err)
		return
	}
	out := toPauseResponse(st)
	out.Resumed = &resumed
	writeJSON(w, http.StatusOK, out)
}

// requireRunner answers 500 when the server was built without a task
// runner, as runAction does for the per-task actions.
func (s *Server) requireRunner(w http.ResponseWriter) bool {
	if s.deps.Runner == nil {
		s.internalError(w, "global pause", errors.New("no task runner is configured"))
		return false
	}
	return true
}
