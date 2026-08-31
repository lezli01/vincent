package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// The GitHub pull-request endpoints (spec §13.2, tasks 052 and 068).
// Read-only against GitHub throughout *as of 068.3*: the listing fetches and
// returns, the single fetch fetches and returns, the check rollup fetches and
// returns, and the two write routes touch only vincent's own
// `github_pull_json` column. Nothing here sends anything to GitHub yet — task
// 068 decision 1 settled that it will, and 068.4 is where merge, close,
// re-run and comment land beside these.

// githubPullResponse is one row of GET /v1/projects/{id}/github/pulls, and
// the `pull` half of GET /v1/tasks/{id}/github/pull.
//
// It embeds the normalized pull request for the reason githubIssueResponse
// embeds the issue: that shape is already the daemon's one spelling, and a
// second DTO beside it would be a third place for the names to drift.
type githubPullResponse struct {
	github.PullRequest
	// TaskID is the task this pull request is linked to, null when none is.
	// It is computed from the stored links rather than re-derived from the
	// head branch, so a human's link and a human's unlink are both visible in
	// the listing that offered them.
	TaskID *int64 `json:"task_id,omitempty"`
	// LinkSource is `auto` or `human` when TaskID is set, so a client can say
	// which kind of claim it is showing.
	LinkSource string `json:"link_source,omitempty"`
	// Prefill is what creating a task from this pull request would fill in,
	// computed server-side (035 decision 2, task 064). Present only when the
	// request named a workflow.
	Prefill *githubPrefill `json:"prefill,omitempty"`
}

// githubTaskPullResponse is GET /v1/tasks/{id}/github/pull: what this task
// knows about its pull request right now.
//
// The stored link is a pointer, so everything renderable is fetched live and
// carries its own FetchedAt. A task with no link answers 200 with Linked
// false and a CompareURL — "there is no pull request, and here is the page
// that would open one" is one fact, and two round trips to learn it would be
// two chances to disagree.
type githubTaskPullResponse struct {
	Linked bool `json:"linked"`
	// Repo and Number are the stored link, present whenever one exists —
	// including a suppressed one, so a client can say what a human refused.
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
	Source string `json:"source,omitempty"`
	// Suppressed is a human's sticky unlink. The reconciler will not
	// re-apply the link while it is set.
	Suppressed bool `json:"suppressed,omitempty"`
	// Pull is the live pull request, fetched on this request. Null when
	// nothing is linked, and null when the fetch failed — Reason then says
	// why, in the same named vocabulary every other GitHub route uses,
	// because a task workspace must still render when GitHub is unreachable.
	Pull   *github.PullRequest `json:"pull,omitempty"`
	Reason string              `json:"reason,omitempty"`
	// CompareURL is GitHub's "open a pull request" page for this task's
	// branch, prefilled with a title and body derived from the task. It is
	// **built, never fetched**: producing it makes no request to GitHub, and
	// vincent still writes nothing there — a human presses GitHub's own
	// button (decision record row 11).
	//
	// It is offered only for a task with no live link, because a task that
	// already has a pull request has nothing to open.
	CompareURL string `json:"compare_url,omitempty"`
}

// githubPullLinkRequest is POST /v1/tasks/{id}/github/pull's body: the human
// link, for a pull request the head-branch rule cannot find.
type githubPullLinkRequest struct {
	Number int `json:"number"`
}

// handleProjectGitHubPulls implements GET /v1/projects/{id}/github/pulls.
//
// Open by default. An open listing is the question the screen is usually
// asking — "which of my branches has a pull request" — and 052's objection to
// listing everything stands: a repository's whole pull-request history is not
// something to pull onto one screen to answer one question. `state` makes
// that a choice the human makes rather than a rule (task 064 decision 9),
// because redoing a reverted pull request and acting on a merged one are real
// cases and the takeover is now where a task is created from.
//
// `workflow` opts into the computed prefill per row, exactly as it does on
// the issue listing: the declared-field half of a prefill is a fact about a
// workflow rather than about a pull request.
//
// It is **pure**: it fetches, normalizes, sorts and returns, and persists
// nothing. Writing the link here would make it exist only for projects a
// human happened to open, and a GET that mutates rows is the one shape no
// other write in this API takes. The reconciler does that work.
func (s *Server) handleProjectGitHubPulls(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		writeGitHubUnavailable(w, gate)
		return
	}
	opts := github.ListOptions{State: r.URL.Query().Get("state")}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("limit must be a positive integer, got %q", raw))
			return
		}
		opts.Limit = n
	}
	pulls, err := s.deps.GitHub.ListPulls(ctx, gate.repo, opts)
	if err != nil {
		writeGitHubError(w, gate, err)
		return
	}
	// The matched task per number, read from the links vincent stores rather
	// than recomputed from head branches: the listing must agree with what a
	// human linked or unlinked, not with what the branch heuristic would have
	// said.
	linked := map[int]store.LinkCandidate{}
	candidates, err := s.deps.Store.LinkCandidates(ctx, project.ID)
	if err != nil {
		s.internalError(w, "list link candidates", err)
		return
	}
	for _, c := range candidates {
		if c.Pull.Linked() {
			linked[c.Pull.Number] = c
		}
	}
	// An unknown workflow name is a 400 rather than a silent "no prefill",
	// for the reason the issue listing gives: the caller asked for a preview
	// of something, and a preview of nothing would look like the pull request
	// simply had no metadata.
	var wf *workflow.Workflow
	if name := strings.TrimSpace(r.URL.Query().Get("workflow")); name != "" {
		entry, found := s.deps.Workflows.Lookup(project.ID, name)
		if !found || !entry.Valid() {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("workflow %q not found for project %d", name, project.ID))
			return
		}
		wf = entry.Workflow
	}
	out := make([]githubPullResponse, 0, len(pulls))
	for _, pull := range pulls {
		row := githubPullResponse{PullRequest: pull}
		if c, ok := linked[pull.Number]; ok && c.Pull.Repo == pull.Repo {
			id := c.TaskID
			row.TaskID, row.LinkSource = &id, c.Pull.Source
		}
		if wf != nil {
			prefill := pullPrefill(pull, wf)
			row.Prefill = &prefill
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTaskGitHubPull implements GET /v1/tasks/{id}/github/pull.
//
// This is the merged case's answer. The listing is open-only, so a pull
// request that has merged has dropped off it — and a merged pull request is
// precisely what the durable link exists to serve. Rendering the stored
// number alone was rejected for the reason a snapshot was: a merged, closed
// or renamed pull request would read exactly like an open one.
func (s *Server) handleTaskGitHubPull(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	out := githubTaskPullResponse{}
	if task.GitHubPull != nil {
		out.Repo, out.Number = task.GitHubPull.Repo, task.GitHubPull.Number
		out.Source, out.Suppressed = task.GitHubPull.Source, task.GitHubPull.Suppressed
		out.Linked = task.GitHubPull.Linked()
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		// Not a 409. A task workspace asks this on every open, and refusing
		// the whole row because GitHub is switched off would take the stored
		// link away from a client that can still render it. The reason rides
		// along instead, in the same named vocabulary.
		out.Reason = gate.avail.Reason
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !out.Linked {
		out.CompareURL = s.compareURLFor(gate.repo, task)
		writeJSON(w, http.StatusOK, out)
		return
	}
	pull, err := s.deps.GitHub.GetPull(ctx, gate.repo, task.GitHubPull.Number)
	if err != nil {
		out.Reason = github.ReasonOf(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Pull = &pull
	writeJSON(w, http.StatusOK, out)
}

// compareURLFor builds the prefilled "open a pull request" page for a task's
// branch. The title and body are a guess of the same kind task 035 decision 2
// already sanctions for task creation — visible before it is used, and fully
// editable by whoever opens it.
//
// No request is made to GitHub here. The URL is string construction over a
// Repo this daemon parsed from a git remote and text out of vincent's own
// database; nothing that arrived from GitHub unvalidated ever reaches it.
func (s *Server) compareURLFor(repo github.Repo, task *store.Task) string {
	if task.BranchName == "" || task.BaseBranch == "" {
		return ""
	}
	body := strings.TrimSpace(task.Description)
	// `Closes #N` only when the task carries an issue snapshot, and only for
	// an issue in the same repository: a cross-repository `Closes` reads as a
	// promise GitHub will not keep.
	if task.GitHubIssue != nil && task.GitHubIssue.Number > 0 && task.GitHubIssue.Repo == repo.String() {
		closes := fmt.Sprintf("Closes #%d", task.GitHubIssue.Number)
		if body == "" {
			body = closes
		} else {
			body += "\n\n" + closes
		}
	}
	return github.CompareURL(repo, task.BaseBranch, task.BranchName, task.Title, body)
}

// handleTaskGitHubPullLink implements POST /v1/tasks/{id}/github/pull: a
// human naming the pull request, for the cases the head-branch rule misses (a
// pull request opened from a branch vincent did not create) or gets wrong.
//
// It writes vincent's own column and nothing else — no call is made to
// GitHub, not even to check the number exists. Validating it would put a
// network failure in the way of a human correcting vincent, and the number is
// rendered live on every read anyway: a wrong one shows up as `not_found` the
// moment it is displayed.
func (s *Server) handleTaskGitHubPullLink(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	var req githubPullLinkRequest
	if !decodeJSONLimit(w, r, &req, maxRequestBytes) {
		return
	}
	if req.Number < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("number must be a positive pull request number, got %d", req.Number))
		return
	}
	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	// The repo has to come from somewhere, and it is derived here for the
	// same reason it is derived everywhere else (task 035 decision 5). A
	// project that is not GitHub-based has no repository to name, so this is
	// the one refusal that is a 409.
	repo, ok := s.githubRepo(ctx, project)
	if !ok {
		writeGitHubUnavailable(w, githubGate{
			enabled: s.deps.Config().GitHub.Enabled,
			avail:   unavailable(github.ReasonNotGitHub),
		})
		return
	}
	// A human link clears any earlier suppression: a person naming a pull
	// request is the strongest statement either side can make.
	updated, err := s.deps.Store.SetTaskGitHubPull(ctx, task.ID,
		store.LinkPull(repo.String(), req.Number, github.SourceHuman, time.Now().UTC()))
	if err != nil {
		s.internalError(w, "link pull request", err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot)))
}

// handleTaskGitHubPullUnlink implements DELETE /v1/tasks/{id}/github/pull.
//
// It does not clear the column: it marks the link suppressed, keeping the
// repo and number. That is what makes "a human unlink is not re-applied by
// the next tick" true — the reconciler has to be able to read the refusal,
// and an empty column would only tell it "never matched".
func (s *Server) handleTaskGitHubPullUnlink(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	updated, err := s.deps.Store.SetTaskGitHubPull(r.Context(), task.ID,
		store.SuppressPull(task.GitHubPull, time.Now().UTC()))
	if err != nil {
		s.internalError(w, "unlink pull request", err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot)))
}

// githubTaskChecksResponse is GET /v1/tasks/{id}/github/pull/checks: what CI
// says about the linked pull request's head commit right now (task 068).
//
// It is a separate route from the pull-request row rather than a field on it
// because the two have different costs and different lifetimes. The row is
// asked for on every workspace open; the rollup is asked for only while the
// Pull Request tab is up, and it is refetched on a timer that the row is not.
// Folding them together would make every task open pay for a check fetch
// nobody is looking at.
//
// It answers 200 with a Reason for every failure, for the reason the row
// does: a tab that refuses to render because GitHub is unreachable is worse
// than one that says so.
type githubTaskChecksResponse struct {
	Linked bool   `json:"linked"`
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
	// Ref is the head commit the rows belong to, echoed because a rollup is
	// only meaningful against the commit it was read for.
	Ref       string            `json:"ref,omitempty"`
	Runs      []github.CheckRun `json:"runs,omitempty"`
	State     string            `json:"state,omitempty"`
	FetchedAt time.Time         `json:"fetched_at,omitzero"`
	Reason    string            `json:"reason,omitempty"`
}

// handleTaskGitHubPullChecks implements GET /v1/tasks/{id}/github/pull/checks.
//
// Two GitHub reads, in order: the pull request, then its head commit's
// checks. The first is not redundant with the caller's own — the REST leg
// needs the head SHA and only GetPull knows it, and re-reading it here is
// what keeps the rollup's ref and the pull request it describes from coming
// from two different moments.
func (s *Server) handleTaskGitHubPullChecks(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	out := githubTaskChecksResponse{}
	if task.GitHubPull != nil {
		out.Repo, out.Number = task.GitHubPull.Repo, task.GitHubPull.Number
		out.Linked = task.GitHubPull.Linked()
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		out.Reason = gate.avail.Reason
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !out.Linked {
		// Not a 404. "This task has no pull request" is an answer, and the
		// same answer the row route gives; a client polling this while a
		// human unlinks must not start seeing errors.
		writeJSON(w, http.StatusOK, out)
		return
	}
	pull, err := s.deps.GitHub.GetPull(ctx, gate.repo, task.GitHubPull.Number)
	if err != nil {
		out.Reason = github.ReasonOf(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	rollup, err := s.deps.GitHub.Checks(ctx, gate.repo, pull)
	if err != nil {
		out.Reason = github.ReasonOf(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Ref, out.Runs, out.State, out.FetchedAt = rollup.Ref, rollup.Runs, rollup.State, rollup.FetchedAt
	writeJSON(w, http.StatusOK, out)
}
