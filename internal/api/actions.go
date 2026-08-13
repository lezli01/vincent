package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// CodeWorktreeDirty is the `details.reason` of the 409 archiving returns when
// it would discard uncommitted work and force was not given (§13.2). It is
// the worktree package's own reason vocabulary, re-exported so the API's
// stable strings are all declared in one place.
const CodeWorktreeDirty = worktree.ReasonWorktreeDirty

// taskAction is one human action from spec §6, as the runner implements it.
type taskAction func(id int64) (*store.Task, error)

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Cancel(r.Context(), id)
	})
}

func (s *Server) handleTaskPause(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Pause(r.Context(), id)
	})
}

func (s *Server) handleTaskResume(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Resume(r.Context(), id)
	})
}

func (s *Server) handleTaskSkip(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Skip(r.Context(), id)
	})
}

func (s *Server) handleTaskApprove(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Approve(r.Context(), id)
	})
}

func (s *Server) handleTaskReject(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Reject(r.Context(), id)
	})
}

// retryRequest is the §13.2 body of POST /v1/tasks/{id}/retry. Either field
// overrides the failed step in this task's snapshot only (§5.3).
type retryRequest struct {
	PromptOverride string `json:"prompt_override"`
	RunOverride    string `json:"run_override"`
	// BranchOverride renames the task's branch before the retry re-admits it.
	// It is what makes a `branch_exists` block recoverable at all (task 001):
	// nothing else in the API can change a branch name, so without it a blocked
	// task would be permanently dead and its transcripts orphaned.
	BranchOverride string `json:"branch_override"`
}

func (s *Server) handleTaskRetry(w http.ResponseWriter, r *http.Request) {
	var req retryRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if branch := strings.TrimSpace(req.BranchOverride); branch != "" {
		if !s.renameBranchForRetry(w, r, branch) {
			return
		}
	}
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		t, err := s.deps.Runner.Retry(r.Context(), id,
			store.Override{Prompt: req.PromptOverride, Run: req.RunOverride})
		if err == nil && (req.PromptOverride != "" || req.RunOverride != "") {
			// edit+retry rewrote this task's snapshot (§6), which is the one
			// thing that can make a cached parse wrong.
			s.snaps.forget(id)
		}
		return t, err
	})
}

// archiveRequest is the §13.2 body of POST /v1/tasks/{id}/archive. `force`
// is also accepted as a query parameter, matching DELETE /v1/projects/{id}.
type archiveRequest struct {
	Force bool `json:"force"`
}

func (s *Server) handleTaskArchive(w http.ResponseWriter, r *http.Request) {
	var req archiveRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	force := req.Force || hasForce(r)
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Archive(r.Context(), id, force)
	})
}

// answerRequest is the §13.2 body of POST /v1/tasks/{id}/answer (§7.4).
// Answer values accept a bare string or an array of strings, so
// single-select answers need no array ceremony.
type answerRequest struct {
	Answers map[string]answerValues `json:"answers"`
	Allow   *bool                   `json:"allow"`
}

// answerValues decodes a string or an array of strings.
type answerValues []string

func (v *answerValues) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*v = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("answer values must be a string or an array of strings")
	}
	*v = many
	return nil
}

func (s *Server) handleTaskAnswer(w http.ResponseWriter, r *http.Request) {
	var req answerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := taskrun.AnswerInput{Allow: req.Allow}
	if req.Answers != nil {
		in.Answers = make(map[string][]string, len(req.Answers))
		for text, vals := range req.Answers {
			in.Answers[text] = vals
		}
	}
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Answer(r.Context(), id, in)
	})
}

// patchTaskRequest is the §13.2 body of PATCH /v1/tasks/{id}. Priority is
// the only mutable field in v1.
type patchTaskRequest struct {
	Priority *int `json:"priority"`
}

func (s *Server) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	var req patchTaskRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Priority == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"priority is the only mutable task field")
		return
	}
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.SetPriority(r.Context(), id, *req.Priority)
	})
}

// runAction resolves {id}, applies the action, and renders the task or the
// §13.1 error the action produced.
func (s *Server) runAction(w http.ResponseWriter, r *http.Request, action taskAction) {
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	task, err := action(id)
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot)))
}

// writeActionError maps the errors an action can produce onto §13.1 status
// codes. A 409 always reports what was actually found, so a client can
// re-issue against the state it did not expect.
func (s *Server) writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())

	case isInvalidAction(err):
		e, _ := taskrun.AsInvalidAction(err)
		writeConflict(w, e.Error(),
			map[string]string{"state": string(e.State)})

	case isStateConflict(err):
		e, _ := store.AsStateConflict(err)
		writeConflict(w, e.Error(),
			map[string]string{"state": string(e.Got)})

	case isOverrideMismatch(err):
		e, _ := taskrun.AsOverrideMismatch(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// A structurally mismatched answer is untranslatable to the live agent
	// session; the request never reaches the task (§7.4, §13.2).
	case isAnswerValidation(err):
		e, _ := taskrun.AsAnswerValidation(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// Archive removes the worktree before transitioning, so a refusal here
	// means the task is untouched — `worktree_dirty` is the one a client
	// resolves by re-sending with force (§13.2).
	case worktree.ReasonOf(err) != "":
		writeConflict(w, err.Error(),
			map[string]string{"reason": worktree.ReasonOf(err)})

	default:
		s.internalError(w, "task action", err)
	}
}

func isInvalidAction(err error) bool {
	_, ok := taskrun.AsInvalidAction(err)
	return ok
}

func isStateConflict(err error) bool {
	_, ok := store.AsStateConflict(err)
	return ok
}

func isOverrideMismatch(err error) bool {
	_, ok := taskrun.AsOverrideMismatch(err)
	return ok
}

func isAnswerValidation(err error) bool {
	_, ok := taskrun.AsAnswerValidation(err)
	return ok
}
