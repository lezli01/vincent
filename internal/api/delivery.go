package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// maxProofBytes bounds a delivery's `proof`: what proved the change, a few
// lines of test names or a reason there were none, never a log.
const maxProofBytes = 16 << 10

// deliveryRequest is the body of POST /v1/tasks/{id}/delivery (§13.2, task
// 137.2): what a run delivered, in its own words. The daemon fills the
// branch and the base from the task, and the head from the branch tip when
// the request names none, because clients never touch git.
type deliveryRequest struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Summary     string `json:"summary"`
	HeadSHA     string `json:"head_sha"`
	CloseReason string `json:"close_reason"`
	Proof       string `json:"proof"`
}

// deliveryResponse is the wire shape of a delivery (§5.3). The summary form —
// every list, and an issue row — leaves out the three fields written at
// length, as a task list leaves out the workflow snapshot.
type deliveryResponse struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title,omitempty"`
	Body        string    `json:"body,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	HeadSHA     string    `json:"head_sha,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Base        string    `json:"base,omitempty"`
	CloseReason string    `json:"close_reason,omitempty"`
	Proof       string    `json:"proof,omitempty"`
	Implicit    bool      `json:"implicit"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// taskDeliveryResponse is what both delivery routes answer: the task and its
// delivery, null when it has none.
type taskDeliveryResponse struct {
	TaskID   int64             `json:"task_id"`
	Delivery *deliveryResponse `json:"delivery"`
}

func toDeliveryResponse(d *store.Delivery, full bool) *deliveryResponse {
	if d == nil {
		return nil
	}
	out := &deliveryResponse{
		Kind: string(d.Kind), Title: d.Title, HeadSHA: d.HeadSHA, Branch: d.Branch, Base: d.Base,
		CloseReason: d.CloseReason, Implicit: d.Implicit, RecordedAt: d.RecordedAt,
	}
	if full {
		out.Body, out.Summary, out.Proof = d.Body, d.Summary, d.Proof
	}
	return out
}

// boundDelivery applies §13.1's field bounds to a delivery body.
func boundDelivery(req *deliveryRequest) string {
	for _, b := range []string{
		boundString("title", req.Title, maxTitleBytes),
		boundString("body", req.Body, maxDescriptionBytes),
		boundString("summary", req.Summary, maxDescriptionBytes),
		boundString("head_sha", req.HeadSHA, maxNameBytes),
		boundString("proof", req.Proof, maxProofBytes),
	} {
		if b != "" {
			return b
		}
	}
	return ""
}

// handleTaskDelivery answers GET /v1/tasks/{id}/delivery: the task's
// delivery in full, or null before it has one.
func (s *Server) handleTaskDelivery(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, taskDeliveryResponse{TaskID: task.ID, Delivery: toDeliveryResponse(task.Delivery, true)})
}

// handleTaskDeliver answers POST /v1/tasks/{id}/delivery: it replaces the
// task's delivery (task 137.2). A step records one while the task runs,
// through `vincent task deliver` or the `task_deliver` tool; a human may
// correct one on a `done` task. Anything else is 409, because a delivery
// describes a run: there is none before it starts, and an aborted run
// delivered nothing.
//
// It is not a write to a forge. It is local state a handoff later reads, so
// a step's call is accepted like any other step call.
func (s *Server) handleTaskDeliver(w http.ResponseWriter, r *http.Request) {
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	var req deliveryRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if msg := boundDelivery(&req); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	kind := store.DeliveryKind(req.Kind)
	if kind == "" {
		kind = store.DeliveryChange
	}
	if !store.ValidDeliveryKind(kind) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("kind must be one of %v (got %q)", store.DeliveryKinds, req.Kind))
		return
	}
	if req.CloseReason != "" && !issuestate.ValidReason(issuestate.Reason(req.CloseReason)) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("close_reason must be one of %v (got %q)", issuestate.Reasons, req.CloseReason))
		return
	}
	ctx := r.Context()
	task, err := s.deps.Store.GetTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return
	}
	// Refused before any git work: the answer cannot change what the state
	// already decides. The store checks again inside its transaction.
	if !store.DeliverableState(task.State) {
		writeConflict(w, fmt.Sprintf("task %d is %s: a delivery is recorded while the task runs or once it is done",
			id, task.State), map[string]string{"state": string(task.State)})
		return
	}
	head, msg, err := s.deliveryHead(r, task, req.HeadSHA)
	switch {
	case err != nil:
		s.internalError(w, "resolve delivery head", err)
		return
	case msg != "":
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	d := &store.Delivery{
		Kind: kind, Title: req.Title, Body: req.Body, Summary: req.Summary,
		HeadSHA: head, Branch: task.BranchName, Base: task.BaseBranch,
		CloseReason: req.CloseReason, Proof: req.Proof, RecordedAt: time.Now().UTC(),
	}
	updated, err := s.deps.Store.SetTaskDelivery(ctx, id, d)
	if err != nil {
		var se *store.DeliveryStateError
		switch {
		case errors.As(err, &se):
			writeConflict(w, err.Error(), map[string]string{"state": string(se.State)})
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		default:
			s.internalError(w, "set task delivery", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, taskDeliveryResponse{TaskID: id, Delivery: toDeliveryResponse(updated.Delivery, true)})
}

// deliveryHead is the commit a recorded delivery names: the request's own,
// which must be a commit on the task's branch, or the branch tip. A branch
// that cannot be read yet leaves it empty, for the `→ done` to fill. msg is a
// validation failure to answer 400 with.
func (s *Server) deliveryHead(r *http.Request, task *store.Task, rev string) (head, msg string, err error) {
	if s.deps.Worktrees == nil || task.BranchName == "" {
		if rev != "" {
			return "", "head_sha cannot be checked: the task has no branch to check it against", nil
		}
		return "", "", nil
	}
	project, err := s.deps.Store.GetProject(r.Context(), task.ProjectID)
	if err != nil {
		return "", "", err
	}
	if rev == "" {
		tip, tipErr := s.deps.Worktrees.BranchTip(r.Context(), project.Path, task.BranchName)
		if tipErr != nil {
			return "", "", nil
		}
		return tip, "", nil
	}
	sha, on, err := s.deps.Worktrees.CommitOnBranch(r.Context(), project.Path, rev, task.BranchName)
	switch {
	case err != nil:
		return "", "", err
	case sha == "":
		return "", fmt.Sprintf("head_sha %q is not a commit in the project's repository", rev), nil
	case !on:
		return "", fmt.Sprintf("head_sha %s is not on the task's branch %s", sha, task.BranchName), nil
	}
	return sha, "", nil
}
