package api

import (
	"net/http"

	"github.com/lezli01/vincent/internal/worktree"
)

// Sample values a branch template is rendered against: a preview shows the shape
// of a name, so it needs a task that reads like one (task 152).
const (
	previewTaskID = 128
	previewTitle  = "cap the public API per tenant"
	previewBase   = "main"
)

type branchPreviewRequest struct {
	Template string `json:"template"`
}

// branchPreviewResponse is what a template makes. A template that does not render
// is an answer rather than a failure: the person is still typing it.
type branchPreviewResponse struct {
	Branch string `json:"branch,omitempty"`
	Error  string `json:"error,omitempty"`
	TaskID int64  `json:"task_id"`
	Title  string `json:"title"`
}

// handleBranchPreview renders a candidate branch_template with the daemon's own
// renderer, so a client never carries a second implementation of branch naming
// (PR L decision). It writes nothing.
func (s *Server) handleBranchPreview(w http.ResponseWriter, r *http.Request) {
	var req branchPreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	project := worktree.BranchProject{Name: "project", DefaultBranch: previewBase}
	c := worktree.NewBranchContext(previewTitle, previewBase, nil, project).WithID(previewTaskID)
	resp := branchPreviewResponse{TaskID: previewTaskID, Title: previewTitle}
	branch, err := worktree.RenderBranch(req.Template, c)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Branch = branch
	}
	writeJSON(w, http.StatusOK, resp)
}
