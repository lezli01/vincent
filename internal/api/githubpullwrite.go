package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/store"
)

// The pull-request write routes (spec §13.2, task 068.4):
//
//	POST /v1/tasks/{id}/github/pull/merge         { method, head_sha }
//	POST /v1/tasks/{id}/github/pull/close
//	POST /v1/tasks/{id}/github/pull/reopen
//	POST /v1/tasks/{id}/github/pull/comment       { body }
//	POST /v1/tasks/{id}/github/pull/checks/rerun  { run_id }
//
// They act on the task's **linked** pull request and on nothing else: a task
// with no live link — a suppressed one included — is refused 409
// `pull_not_linked`, the mirror of task 069's `pull_already_linked`. The
// number comes from the stored link and the repository from the project's
// origin, so a caller cannot aim a write at a pull request this task does not
// name.
//
// Decision record row 11 as rewritten: every write is a human's act. All five
// routes are excluded from the shared MCP tool surface (§13.4), because "the
// keypress is the consent" only holds while a human presses it. A step's
// agent keeps its own path — `gh pr merge` in its full-auto worktree.
// *Narrowed 2026-10-28 (task 137 decision 6, 137.7):* every route first asks
// the one forge-write guard (issues.ForgeWrite), before any `gh` call. A
// caller reading as an agent — an MCP call, a step's or a chat's marker — is
// refused 409 forge_write_needs_human unless it is a live handoff's task
// whose human-given grant names the route's capability and whose scope
// covers this task; such a write is simply allowed. Before 137.7 a step's
// `vincent github pr merge` was accepted as a human's.
//
// There is no task-state guard and no idempotency key (task 069 decisions 4
// and 7 carry over). A running task's pull request may be merged; a double
// merge is refused by the preflight as `not_mergeable`; a double comment
// posts twice, and the client's submit-disable is the defence. No event is
// published: the link does not change, and the response carries the new
// state.

type githubPullMergeRequest struct {
	// Method is merge, squash or rebase, and required: there is no default
	// and no config key (task 068 decision 4).
	Method string `json:"method"`
	// HeadSHA is the head commit the human confirmed. The merge is refused
	// `head_changed` when the live head differs, and pinned to it when sent.
	HeadSHA string `json:"head_sha"`
}

type githubPullCommentRequest struct {
	Body string `json:"body"`
}

type githubPullCommentResponse struct {
	// URL is the created comment's web page.
	URL string `json:"url"`
}

// githubPullRerun is both the re-run body and its answer: the run whose
// failed jobs were re-requested.
type githubPullRerun struct {
	RunID int64 `json:"run_id"`
}

// handleTaskGitHubPullMerge implements POST /v1/tasks/{id}/github/pull/merge.
// It answers 200 with the pull request re-read after the merge.
func (s *Server) handleTaskGitHubPullMerge(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if !s.pullWriteAllowed(w, r, task, store.CapPullMerge) {
		return
	}
	var req githubPullMergeRequest
	if !decodeJSONLimit(w, r, &req, maxRequestBytes) {
		return
	}
	if !github.ValidMergeMethod(req.Method) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("method is required and must be merge, squash or rebase, got %q", req.Method))
		return
	}
	headSHA := strings.TrimSpace(req.HeadSHA)
	if headSHA == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"head_sha is required: it is the head commit the merge was confirmed for")
		return
	}
	gate, ok := s.githubPullWriteGate(w, r, task)
	if !ok {
		return
	}
	pull, err := s.deps.GitHub.MergePull(r.Context(), gate.repo, task.GitHubPull.Number,
		github.MergeOptions{Method: req.Method, HeadSHA: headSHA})
	if err != nil {
		writeGitHubRefusal(w, "merge", task, err)
		return
	}
	writeJSON(w, http.StatusOK, pull)
}

// handleTaskGitHubPullClose implements POST /v1/tasks/{id}/github/pull/close.
func (s *Server) handleTaskGitHubPullClose(w http.ResponseWriter, r *http.Request) {
	s.setTaskGitHubPullState(w, r, "close", (*github.Client).ClosePull)
}

// handleTaskGitHubPullReopen implements POST /v1/tasks/{id}/github/pull/reopen.
func (s *Server) handleTaskGitHubPullReopen(w http.ResponseWriter, r *http.Request) {
	s.setTaskGitHubPullState(w, r, "reopen", (*github.Client).ReopenPull)
}

// setTaskGitHubPullState is close and reopen: no body, and a 200 carrying the
// pull request as the write left it.
func (s *Server) setTaskGitHubPullState(w http.ResponseWriter, r *http.Request, verb string,
	write func(*github.Client, context.Context, github.Repo, int) (github.PullRequest, error),
) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if !s.pullWriteAllowed(w, r, task, store.CapPullClose) {
		return
	}
	gate, ok := s.githubPullWriteGate(w, r, task)
	if !ok {
		return
	}
	pull, err := write(s.deps.GitHub, r.Context(), gate.repo, task.GitHubPull.Number)
	if err != nil {
		writeGitHubRefusal(w, verb, task, err)
		return
	}
	writeJSON(w, http.StatusOK, pull)
}

// handleTaskGitHubPullComment implements POST /v1/tasks/{id}/github/pull/comment.
func (s *Server) handleTaskGitHubPullComment(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if !s.pullWriteAllowed(w, r, task, store.CapPullComment) {
		return
	}
	var req githubPullCommentRequest
	if !decodeJSONLimit(w, r, &req, maxRequestBytes) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "body is required: a comment cannot be empty")
		return
	}
	gate, ok := s.githubPullWriteGate(w, r, task)
	if !ok {
		return
	}
	link, err := s.deps.GitHub.CommentPull(r.Context(), gate.repo, task.GitHubPull.Number, req.Body)
	if err != nil {
		writeGitHubRefusal(w, "comment on", task, err)
		return
	}
	writeJSON(w, http.StatusOK, githubPullCommentResponse{URL: link})
}

// handleTaskGitHubPullRerun implements
// POST /v1/tasks/{id}/github/pull/checks/rerun: re-run the failed jobs of one
// Actions run on the linked pull request's head. The run id is validated
// against the live rollup inside internal/github, before anything is sent.
func (s *Server) handleTaskGitHubPullRerun(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if !s.pullWriteAllowed(w, r, task, store.CapChecksRerun) {
		return
	}
	var req githubPullRerun
	if !decodeJSONLimit(w, r, &req, maxRequestBytes) {
		return
	}
	if req.RunID < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("run_id must be a positive GitHub Actions run id, got %d", req.RunID))
		return
	}
	gate, ok := s.githubPullWriteGate(w, r, task)
	if !ok {
		return
	}
	if err := s.deps.GitHub.RerunFailedJobs(r.Context(), gate.repo, task.GitHubPull.Number, req.RunID); err != nil {
		writeGitHubRefusal(w, "re-run the failed checks of", task, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// pullWriteAllowed asks the forge-write guard whether the caller may make a
// pull-request write of capability on task, and writes the refusal when it
// may not (task 137.7). It runs before the gate and the body: a refused
// caller learns nothing about the integration, and nothing reaches `gh`.
// reopen shares close's capability, as the grant vocabulary has it.
func (s *Server) pullWriteAllowed(w http.ResponseWriter, r *http.Request, task *store.Task, capability store.Capability) bool {
	_, err := issues.New(s.deps.Store).ForgeWrite(r.Context(), forgeCaller(r), capability, issues.Target{TaskID: task.ID})
	switch {
	case err == nil:
		return true
	case errors.Is(err, issues.ErrForgeWriteNeedsHuman):
		writeConflict(w, err.Error(), map[string]string{"reason": issueReasonForgeWrite})
	default:
		s.internalError(w, "check forge write", err)
	}
	return false
}

// githubPullWriteGate runs the §13.2 gate and then the link check, and writes
// the refusal when either says no. The gate comes first: "the integration is
// off" is the more fundamental answer, and it is what every other GitHub
// route gives before looking at anything else.
func (s *Server) githubPullWriteGate(w http.ResponseWriter, r *http.Request, task *store.Task) (githubGate, bool) {
	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return githubGate{}, false
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		writeGitHubUnavailable(w, gate)
		return gate, false
	}
	if !task.GitHubPull.Linked() {
		writeConflict(w, fmt.Sprintf("task %d has no linked pull request to act on", task.ID),
			map[string]string{"reason": "pull_not_linked"})
		return gate, false
	}
	return gate, true
}

// writeGitHubRefusal is writeGitHubError's envelope — 409, `details.reason`
// from the vocabulary — with a message that says GitHub refused a write
// rather than that it is unavailable. The error's Detail is not read: the
// daemon logged it, and only the named reason reaches a client (decision 1).
func writeGitHubRefusal(w http.ResponseWriter, verb string, task *store.Task, err error) {
	reason := github.ReasonOf(err)
	writeConflict(w, fmt.Sprintf("could not %s %s#%d: %s",
		verb, task.GitHubPull.Repo, task.GitHubPull.Number, github.Message(reason)),
		map[string]string{"reason": reason})
}
