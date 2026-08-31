package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// POST /v1/tasks/{id}/github/pull/create (spec §13.2, task 069).
//
// The one route in vincent that writes to a forge. It pushes the task's
// branch to `origin` and creates its pull request, and it exists because the
// alternative was a browser and a terminal: the compare page 052 hands off to
// is dead for a branch nobody pushed, and the push was a manual step in the
// worktree.
//
// Decision record rows 11 and 27 are amended for exactly this and nothing
// else. Workflow-owned delivery is unchanged for workflow runs — no step
// type, no default workflow and no automatic behaviour reaches this — and
// merging stays out of scope. What changed is that a human pressing a key in
// vincent may now do what they previously did in a browser.
//
// The route is excluded from the MCP tool surface (§13.4, decision 3): "the
// keypress is the consent" (decision 2) is only true while a human is the one
// pressing it. Nothing is lost — a step's agent already has a full-auto shell
// in its own worktree and can push and run `gh pr create` there, which is row
// 11's original path and stays open.

// githubPullCreateRequest is the body: what the human edited in the popup.
// Nothing else is accepted — the head and base branches come from the task
// row, because a caller choosing its own head is a caller pushing a branch
// this task does not own.
type githubPullCreateRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Draft opens the pull request as a draft. It is the toggle in the popup
	// and has no default beyond false: a pull request created without asking
	// is ready, which is what GitHub's own form does.
	Draft bool `json:"draft"`
}

// githubPullCreateResponse is the answer, and it has two shapes behind one
// 200 (decision: the fallback is not an error).
//
// Created is the happy path: the pull request exists, the link is written as
// `human`, and Pull carries it. Otherwise the push succeeded but the create
// did not, and CompareURL is GitHub's own page for a branch that is now on
// the remote — which is the issue's second complaint fixed even on the
// unhappy path. Reason names why in the same vocabulary every other GitHub
// route uses.
//
// A *push* failure is not this shape at all: it is a 409, because nothing was
// attempted at GitHub and offering a compare URL for a branch that is not
// there would be the dead page all over again.
type githubPullCreateResponse struct {
	Created bool `json:"created"`
	// Pushed says the branch reached `origin` on this call. True on both
	// shapes: the fallback is only reachable after the push succeeded.
	Pushed bool `json:"pushed"`
	// Branch and Remote are what was pushed.
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
	// Pull is the created pull request, normalized. Null on the fallback.
	Pull *github.PullRequest `json:"pull,omitempty"`
	// Task is the task with its link written, so a client needs no second
	// round trip to render what it just created. Null on the fallback.
	Task *taskResponse `json:"task,omitempty"`
	// CompareURL and Reason are the fallback: the page to open, and the named
	// reason the create could not be made here.
	CompareURL string `json:"compare_url,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
}

// handleTaskGitHubPullCreate implements POST /v1/tasks/{id}/github/pull/create.
//
// The order is the order the refusals matter in, and it stops at the first
// "no": the §13.2 gate, then the already-linked check, then the push, then
// the create. It is synchronous in the handler — handleTaskDiff already runs
// git inside a request and internal/worktree's fetch already bounds a network
// git call at gitx.RemoteTimeout, so a background job would be new machinery
// for a call that is already bounded twice (decision 6).
//
// Double submission is refused at the source rather than with an idempotency
// key (decision 7): the link is written the moment the pull request exists,
// so the second call sees a live link and is refused 409, and GitHub itself
// refuses a second pull request for the same head and base as the backstop.
func (s *Server) handleTaskGitHubPullCreate(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	var req githubPullCreateRequest
	if !decodeJSONLimit(w, r, &req, maxRequestBytes) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"title is required: GitHub's own form is unusable without one")
		return
	}
	// A task that already names a live pull request has nothing to create.
	// Suppressed does not count as linked, deliberately: a human who unlinked
	// a pull request may open a new one (decision 4).
	if task.GitHubPull.Linked() {
		writeConflict(w, fmt.Sprintf(
			"task %d already has pull request %s#%d — unlink it first",
			task.ID, task.GitHubPull.Repo, task.GitHubPull.Number),
			map[string]string{"reason": "pull_already_linked"})
		return
	}
	if task.BranchName == "" || task.BaseBranch == "" {
		writeConflict(w, "this task has no branch yet, so there is nothing to push",
			map[string]string{"reason": worktree.ReasonBranchNameInvalid})
		return
	}

	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		writeGitHubUnavailable(w, gate)
		return
	}
	if s.deps.Worktrees == nil {
		s.internalError(w, "push branch", errors.New("no worktree manager wired"))
		return
	}

	// The push comes first and its failure is terminal. Nothing is attempted
	// at GitHub, because a pull request for a head the remote does not have
	// is the dead page this task exists to remove.
	push, err := s.deps.Worktrees.PushBranch(ctx, project.Path, task.BranchName)
	if err != nil {
		reason := worktree.ReasonOf(err)
		s.deps.Logger.Warn("github pull create: push failed",
			"task", task.ID, "branch", task.BranchName, "reason", reason, "detail", err)
		writeConflict(w,
			fmt.Sprintf("could not push %s to %s, so no pull request was created",
				task.BranchName, push.Remote),
			map[string]string{"reason": reason})
		return
	}

	out := githubPullCreateResponse{
		Pushed: true, Branch: push.Branch, Remote: push.Remote,
	}
	pull, err := s.deps.GitHub.CreatePull(ctx, gate.repo, github.CreateOptions{
		Base:  task.BaseBranch,
		Head:  task.BranchName,
		Title: req.Title,
		Body:  req.Body,
		Draft: req.Draft,
	})
	if err != nil {
		// The fallback, and a 200 rather than an error: the branch is on the
		// remote now, so GitHub's own page works, and the client opens it
		// exactly as it does today. Nothing got worse than it was.
		out.Reason = github.ReasonOf(err)
		out.Message = github.Message(out.Reason)
		out.CompareURL = s.compareURLFor(gate.repo, task)
		writeJSON(w, http.StatusOK, out)
		return
	}

	// The link is written here rather than left to the reconciler's next
	// github.poll_interval tick: a pull request just created from vincent
	// would otherwise read as unlinked for up to that interval. `human`,
	// because 052 decision 2's reconciler never overwrites a human link — and
	// a person who pressed this key is the strongest statement either side
	// can make.
	updated, err := s.deps.Store.SetTaskGitHubPull(ctx, task.ID,
		store.LinkPull(gate.repo.String(), pull.Number, github.SourceHuman, time.Now().UTC()))
	if err != nil {
		s.internalError(w, "link created pull request", err)
		return
	}
	resp := toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot))
	out.Created, out.Pull, out.Task = true, &pull, &resp
	writeJSON(w, http.StatusOK, out)
}
