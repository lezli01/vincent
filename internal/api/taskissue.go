package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// A task created from a vincent issue (spec §5.6, §13.2, task 130.7): the
// `issue_id` create field, the task DTO's `issue` object, and the
// `github_issue` the DTO derives from an imported issue's snapshot.

// issueProviderGitHub is issue_remotes.provider for a GitHub import; the
// constant internal/issues spells the same value with is unexported.
const issueProviderGitHub = "github"

// taskIssueResponse is the issue a task was created from, as the task DTO
// carries it (§5.3). It is read from the live issue while the link holds and
// from the task's frozen snapshot once the issue is deleted and issue_id is
// NULL (task 130 decision 6), so a task never loses the name of what it was
// for.
type taskIssueResponse struct {
	ID     int64            `json:"id"`
	Title  string           `json:"title"`
	State  string           `json:"state"`
	Source *issueSourceBody `json:"source,omitempty"`
}

// snapshotIssueResponse renders the snapshot half: what toTaskResponse puts
// on every task, before a live read replaces it.
func snapshotIssueResponse(snap *store.IssueSnapshot) *taskIssueResponse {
	if snap == nil {
		return nil
	}
	out := &taskIssueResponse{ID: snap.ID, Title: snap.Title, State: snap.State}
	if rem := snap.Remote; rem != nil {
		out.Source = &issueSourceBody{
			Provider: rem.Provider, Repo: rem.Repo, Number: rem.Number, URL: rem.URL,
			RemoteState: rem.State,
		}
	}
	return out
}

// liveIssueResponse renders the issue as it is now.
func liveIssueResponse(iss *store.Issue) *taskIssueResponse {
	row := renderIssueRow(iss)
	return &taskIssueResponse{ID: row.ID, Title: row.Title, State: row.State, Source: row.Source}
}

// taskGitHubIssue is the DTO's `github_issue` (task 130 decision 7): the
// legacy snapshot when the row has one, otherwise derived from the issue
// snapshot's GitHub reference, so `.github_issue.number` consumers keep
// working for a task created from an imported issue. A local issue has no
// GitHub number and derives nothing.
func taskGitHubIssue(t *store.Task) *github.Issue {
	if t.GitHubIssue != nil {
		return t.GitHubIssue
	}
	snap := t.Issue
	if snap == nil || snap.Remote == nil || snap.Remote.Provider != issueProviderGitHub || snap.Remote.Number < 1 {
		return nil
	}
	rem := snap.Remote
	assignee := ""
	if len(rem.Assignees) > 0 {
		assignee = rem.Assignees[0]
	}
	return &github.Issue{
		Repo:            rem.Repo,
		Number:          rem.Number,
		Title:           snap.Title,
		Body:            snap.Body,
		URL:             rem.URL,
		State:           firstNonBlank(rem.State, snap.State),
		Labels:          snap.Labels,
		Author:          snap.Author,
		Assignee:        assignee,
		Assignees:       rem.Assignees,
		Milestone:       rem.Milestone,
		MilestoneNumber: rem.MilestoneNumber,
		FetchedAt:       snap.CapturedAt,
	}
}

// taskGitHubRef is the GitHub issue a task closes when its pull request
// merges: the issue snapshot's reference first, the legacy snapshot second.
// ok is false for a task with neither, including one from a local issue.
func taskGitHubRef(t *store.Task) (repo string, number int, ok bool) {
	if snap := t.Issue; snap != nil && snap.Remote != nil {
		if snap.Remote.Provider == issueProviderGitHub && snap.Remote.Number > 0 {
			return snap.Remote.Repo, snap.Remote.Number, true
		}
		return "", 0, false
	}
	if gi := t.GitHubIssue; gi != nil && gi.Number > 0 {
		return gi.Repo, gi.Number, true
	}
	return "", 0, false
}

// applyVincentIssue resolves a create request's `issue_id` (task 130.7): it
// loads the issue, freezes the snapshot, and folds vincentIssuePrefill into
// the request through foldPrefill — explicit wins, exactly as for
// `github_issue` (task 035 decision 2). The prefill reads only the snapshot,
// so this makes no provider call even for an imported issue.
//
// A closed issue is not a refusal: one issue backs many tasks, and follow-up
// work on a closed one is legitimate. It comes back as a warning for the
// response's `warnings` instead.
//
// It writes its own 400 and reports false when it did.
func (s *Server) applyVincentIssue(
	ctx context.Context, w http.ResponseWriter,
	project *store.Project, wf *workflow.Workflow, req *taskCreateRequest,
) (snap *store.IssueSnapshot, warning string, ok bool) {
	if req.IssueID == nil {
		return nil, "", true
	}
	id := *req.IssueID
	if id < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue_id must be a positive issue id, got %d", id))
		return nil, "", false
	}
	iss, err := s.deps.Store.GetIssue(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue_id %d not found", id))
		return nil, "", false
	}
	if err != nil {
		s.internalError(w, "get issue", err)
		return nil, "", false
	}
	if iss.ProjectID != project.ID {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue_id %d belongs to project %d, not project %d", id, iss.ProjectID, project.ID))
		return nil, "", false
	}
	snap = store.NewIssueSnapshot(iss, time.Now())
	if msg := foldPrefill(req, vincentIssuePrefill(snap, wf)); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, "", false
	}
	if issuestate.Normalize(iss.State) == issuestate.Closed {
		warning = fmt.Sprintf("issue %d is closed; the task was created from it anyway", id)
	}
	return snap, warning, true
}

// overlayLiveIssue replaces resp's snapshot-derived `issue` with the issue as
// it is now, while the link holds. A failed read keeps the snapshot rather
// than failing a response that is otherwise complete.
func (s *Server) overlayLiveIssue(ctx context.Context, resp *taskResponse, t *store.Task) {
	if t.IssueID == nil {
		return
	}
	iss, err := s.deps.Store.GetIssue(ctx, *t.IssueID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.deps.Logger.Warn("get task issue", "task", t.ID, "issue", *t.IssueID, "error", err)
		}
		return
	}
	resp.Issue = liveIssueResponse(iss)
}

// liveIssues reads every linked issue of a page of tasks in one query, for
// toListResponse's "a board never fans out per row".
func (s *Server) liveIssues(ctx context.Context, tasks []store.Task) (map[int64]*store.Issue, error) {
	seen := map[int64]bool{}
	var ids []int64
	for i := range tasks {
		if id := tasks[i].IssueID; id != nil && !seen[*id] {
			seen[*id] = true
			ids = append(ids, *id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := s.deps.Store.ListIssues(ctx, store.IssueFilter{IDs: ids})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*store.Issue, len(list))
	for _, iss := range list {
		out[iss.ID] = iss
	}
	return out, nil
}
