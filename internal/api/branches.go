package api

import (
	"net/http"

	"github.com/lezli01/vincent/internal/worktree"
)

// branchListResponse is GET /v1/projects/{id}/branches (§13.2, task 125): the
// project's local branches, so a client filling in the new-task or new-chat
// form can offer them instead of asking the user to remember a name.
//
// It is a listing, not a validator. Free text is still accepted on both forms,
// and a name that is not in this list is the cut-a-new-branch mode exactly as
// it always was — which is why nothing here says "valid".
type branchListResponse struct {
	Branches []branchBody `json:"branches"`
}

type branchBody struct {
	Name string `json:"name"`
	// CheckedOutIn is the working tree holding this branch, or "" when none
	// does. Set to the project's own path, it is the one fact a user
	// choosing a branch most needs: adopting it will run the task **in that
	// checkout** rather than in a worktree (§10, task 125 decision 3).
	CheckedOutIn string `json:"checked_out_in,omitempty"`
	// MainCheckout says CheckedOutIn is the project path itself, computed
	// server-side so a client never has to compare two paths git and the
	// project record may spell differently.
	MainCheckout bool `json:"main_checkout,omitempty"`
	// Current marks the branch the project's main checkout has at HEAD.
	Current bool `json:"current,omitempty"`
}

// handleProjectBranches lists the project's local branches.
//
// Local only, and deliberately: a remote-tracking ref is not something
// `git worktree add` can adopt, so offering one would hand the user a name
// that fails at admission.
func (s *Server) handleProjectBranches(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	branches, err := s.deps.Worktrees.ListBranches(r.Context(), project.Path)
	if err != nil {
		// A project path that has gone missing is the caller's repository
		// problem, not the daemon's, and it is the same reason every other
		// route reports for it.
		if worktree.ReasonOf(err) == worktree.ReasonProjectPathMissing {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return
		}
		s.internalError(w, "list branches", err)
		return
	}
	resp := branchListResponse{Branches: make([]branchBody, 0, len(branches))}
	for _, b := range branches {
		resp.Branches = append(resp.Branches, branchBody{
			Name:         b.Name,
			CheckedOutIn: b.CheckedOutIn,
			MainCheckout: b.CheckedOutIn != "" && worktree.SameDir(b.CheckedOutIn, project.Path),
			Current:      b.Current,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
