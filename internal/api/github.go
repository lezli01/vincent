package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// The GitHub issue endpoints (spec §13.2, task 035). Everything GitHub-facing
// in the daemon enters through here and through the `github_issue` field on
// POST /v1/tasks — clients never talk to GitHub, per the ownership invariant,
// and no other route reaches internal/github.

// githubResponse is GET /v1/projects/{id}/github: the capability probe
// (decision 4).
//
// It is its own endpoint rather than three fields on the project DTO because
// the board lists projects constantly, and answering "is this GitHub, and can
// we reach it" there would probe `gh auth` per project on every refresh. Nor
// is it inferred from a failed issue list: that would surface the reason only
// after the call it exists to prevent.
type githubResponse struct {
	// Enabled is the config.yaml toggle (§12.3). It is reported separately
	// from Available so a client can distinguish "you turned this off" from
	// "this project is not on GitHub".
	Enabled bool `json:"enabled"`
	// Repo is `owner/name` when the project's origin identified one.
	Repo string `json:"repo,omitempty"`
	// Available says the daemon can list this repository's issues right now.
	Available bool `json:"available"`
	// Reason is the named unavailability reason (internal/github's
	// vocabulary), empty when Available. Message is that reason rendered for
	// a human. Neither ever carries `gh` stderr or an HTTP body (decision 1).
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Via is `gh` or `token` when available, so `vincent doctor` can name the
	// credential that answered without probing again.
	Via string `json:"via,omitempty"`
}

// githubIssueResponse is one row of GET /v1/projects/{id}/github/issues.
//
// It embeds the normalized issue rather than restating it: that shape is
// already the daemon's one spelling of an issue — the snapshot persisted on
// the task and the value `.Issue` renders from — and a second DTO beside it
// would be a third place for the field names to drift.
type githubIssueResponse struct {
	github.Issue
	// Prefill is what creating a task from this issue would fill in, computed
	// server-side (decision 2). Present only when the request named a
	// workflow, because the declared-field half of a prefill is a fact about
	// a workflow rather than about an issue.
	Prefill *githubPrefill `json:"prefill,omitempty"`
}

// githubPrefill is the daemon's computed prefill. The TUI drops it into its
// editable rows, so every guess is visible before creation; POST /v1/tasks
// recomputes exactly the same thing from the same code, which is what makes
// "the CLI flag and the TUI produce the same stored task" a testable claim
// rather than a coincidence (decision 2).
type githubPrefill struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// githubGate is the resolved answer to "may this project be asked about
// GitHub issues, and can it be reached". Every GitHub-facing handler and the
// create path go through it, so the three refusals are spelled once.
type githubGate struct {
	enabled bool
	repo    github.Repo
	avail   github.Availability
}

func (g githubGate) response() githubResponse {
	return githubResponse{
		Enabled:   g.enabled,
		Repo:      g.avail.Repo,
		Available: g.avail.Available,
		Reason:    g.avail.Reason,
		Message:   g.avail.Message,
		Via:       g.avail.Via,
	}
}

// githubGateFor resolves the gate for a project: the config toggle, then the
// derived repository identity, then the credential probe. Each step's failure
// is a named reason, and the first one that fails is the answer — a project
// whose origin is not GitHub is never probed, which is what makes "with
// integration disabled, or on a non-GitHub project, no GitHub call is made"
// true rather than merely intended.
func (s *Server) githubGateFor(ctx context.Context, project *store.Project) githubGate {
	if !s.deps.Config().GitHub.Enabled {
		return githubGate{avail: unavailable(github.ReasonDisabled)}
	}
	repo, ok := s.githubRepo(ctx, project)
	if !ok {
		return githubGate{enabled: true, avail: unavailable(github.ReasonNotGitHub)}
	}
	if s.deps.GitHub == nil {
		// No client wired (a test server without one). Reported as the same
		// "no credential" the daemon would report, never as a 500: the
		// integration is simply not usable here.
		avail := unavailable(github.ReasonNoCredential)
		avail.Repo = repo.String()
		return githubGate{enabled: true, repo: repo, avail: avail}
	}
	return githubGate{enabled: true, repo: repo, avail: s.deps.GitHub.Probe(ctx, repo)}
}

func unavailable(reason string) github.Availability {
	return github.Availability{Reason: reason, Message: github.Message(reason)}
}

// githubRepo derives the project's GitHub identity from its `origin` remote
// at the point of use (decision 5). Nothing is stored: a `github_repo` column
// would be a migration for a fact the remote already carries, and it is held
// in reserve for PR checking, which needs a durable identity.
//
// A project whose origin is an SSH alias, or whose GitHub remote is not named
// `origin`, is simply not GitHub-based — that is the known narrowness this
// decision accepts.
func (s *Server) githubRepo(ctx context.Context, project *store.Project) (github.Repo, bool) {
	if s.deps.Git == nil {
		return github.Repo{}, false
	}
	remote, err := s.git(ctx, project.Path, "remote", "get-url", "origin")
	if err != nil {
		return github.Repo{}, false
	}
	return github.ParseRemote(remote)
}

func (s *Server) handleProjectGitHub(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.githubGateFor(r.Context(), project).response())
}

// handleProjectGitHubIssues implements GET /v1/projects/{id}/github/issues.
//
// `state` and `limit` narrow the listing; `workflow` opts into the computed
// prefill per row. The picker needs no `q` parameter: it filters what it is
// given locally, the way every other picker in §15 does.
func (s *Server) handleProjectGitHubIssues(w http.ResponseWriter, r *http.Request) {
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
	issues, err := s.deps.GitHub.List(ctx, gate.repo, opts)
	if err != nil {
		writeGitHubError(w, gate, err)
		return
	}
	// The prefill's declared-field half needs a workflow. An unknown name is
	// a 400 rather than a silent "no prefill": the caller asked for a
	// preview of something, and answering with a preview of nothing would
	// look like the issue simply had no metadata.
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
	out := make([]githubIssueResponse, 0, len(issues))
	for _, issue := range issues {
		row := githubIssueResponse{Issue: issue}
		if wf != nil {
			prefill := issuePrefill(issue, wf)
			row.Prefill = &prefill
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// writeGitHubUnavailable answers a request that needed the integration when
// it is not usable. It is a 409 with the reason in `details`, which is
// §13.1's shape for "the precondition this request needs does not hold" —
// clients branch on the code, not on the prose.
func writeGitHubUnavailable(w http.ResponseWriter, gate githubGate) {
	writeConflict(w, "GitHub is not available for this project: "+gate.avail.Message,
		map[string]string{"reason": gate.avail.Reason})
}

// writeGitHubError maps a failed GitHub call onto the same envelope. The
// error's own Detail — `gh` stderr, an HTTP body — is deliberately not read
// here: the daemon logged it, and only the named reason reaches a client
// (decision 1).
func writeGitHubError(w http.ResponseWriter, gate githubGate, err error) {
	reason := github.ReasonOf(err)
	gate.avail.Reason, gate.avail.Message = reason, github.Message(reason)
	writeGitHubUnavailable(w, gate)
}

// issuePrefill computes what creating a task from this issue would fill in
// (decision 7). It is the **one** implementation: the list endpoint previews
// it and POST /v1/tasks applies it, so a preview a human accepted and a
// create call that names only the issue produce the same task.
//
// Declared fields are matched by exact name only — no aliases, no fuzzy
// matching, no case folding — and a candidate is offered only when the
// declaration would accept it. Anything that would fail validation is left
// empty rather than pre-filling a value the create call would then 400 on.
// Undeclared names are never invented: issue metadata reaches templates
// through `.Issue`.
func issuePrefill(issue github.Issue, wf *workflow.Workflow) githubPrefill {
	out := githubPrefill{Title: github.Title(issue), Description: github.Description(issue)}
	if wf == nil {
		return out
	}
	for _, definition := range wf.Fields {
		value, ok := github.Candidate(issue, github.FieldDecl{Name: definition.Name, Type: definition.Type})
		if !ok || definition.Validate(value) != "" {
			continue
		}
		if out.Fields == nil {
			out.Fields = map[string]string{}
		}
		out.Fields[definition.Name] = value
	}
	return out
}

// applyIssuePrefill resolves a create request's `github_issue` (task 035).
// It fetches the issue, folds the computed prefill into the request wherever
// the caller left a value unset, and returns the snapshot to persist on the
// task. A request without `github_issue` is left untouched and no GitHub call
// is made — which is what "with the integration disabled, or on a non-GitHub
// project, no GitHub call is made" means in the create path too.
//
// Precedence is **explicit wins**, keyed on presence rather than on
// emptiness for the fields map: a caller that sends `labels: ""` has cleared
// a prefilled row on purpose, and re-filling it would make the form's
// "nothing is locked" promise false. Title and description key on
// blank/absent instead, because there is no such thing as deliberately
// creating a task with an empty title, and an absent description is how every
// client spells "you decide".
//
// It writes its own error response and reports false when it did.
func (s *Server) applyIssuePrefill(
	ctx context.Context, w http.ResponseWriter,
	project *store.Project, wf *workflow.Workflow, req *taskCreateRequest,
) (*github.Issue, bool) {
	if req.GitHubIssue == nil {
		return nil, true
	}
	number := *req.GitHubIssue
	if number < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("github_issue must be a positive issue number, got %d", number))
		return nil, false
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		writeGitHubUnavailable(w, gate)
		return nil, false
	}
	issue, err := s.deps.GitHub.Get(ctx, gate.repo, number)
	if err != nil {
		writeGitHubError(w, gate, err)
		return nil, false
	}
	prefill := issuePrefill(issue, wf)
	if strings.TrimSpace(req.Title) == "" {
		req.Title = prefill.Title
	}
	if req.Description == nil {
		description := prefill.Description
		req.Description = &description
	}
	for name, value := range prefill.Fields {
		if _, present := req.Fields[name]; present {
			continue
		}
		if req.Fields == nil {
			req.Fields = map[string]string{}
		}
		req.Fields[name] = value
	}
	// Re-bound the request now that it carries text vincent fetched rather
	// than text the caller typed. An issue body at GitHub's own size limit is
	// larger than §13.1's description bound, and it has to fail the same way
	// a pasted one would rather than be silently truncated into the row.
	if msg := boundTaskFields(req.Title, ptrValue(req.Description), req.Fields); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, false
	}
	return &issue, true
}

// pullPrefill computes what creating a task from this pull request would fill
// in (task 064 decision 9). It is issuePrefill's counterpart and the **one**
// implementation of the pull-request half: the listing previews it, POST
// /v1/tasks applies it, and the TUI renders what the daemon computed — which
// is what makes "the CLI flag, the API and the TUI produce the same stored
// task" a testable claim rather than a coincidence (035 decision 2).
//
// The only declared field a pull request fills is `pull`, matched by exact
// name and offered only when the declaration would accept it. There is no
// `.Pull` template variable for anything else to reach (064 decision 11), so
// this is the whole of what a workflow learns about the pull request.
func pullPrefill(pull github.PullRequest, wf *workflow.Workflow) githubPrefill {
	out := githubPrefill{Title: github.PullTitle(pull), Description: github.PullDescription(pull)}
	if wf == nil {
		return out
	}
	for _, definition := range wf.Fields {
		value, ok := github.PullCandidate(pull, github.FieldDecl{Name: definition.Name, Type: definition.Type})
		if !ok || definition.Validate(value) != "" {
			continue
		}
		if out.Fields == nil {
			out.Fields = map[string]string{}
		}
		out.Fields[definition.Name] = value
	}
	return out
}

// applyPullPrefill resolves a create request's `github_pull` (task 064). It
// mirrors applyIssuePrefill exactly — same fetch-then-fold shape, same
// explicit-wins precedence keyed on presence for fields and on blankness for
// title and description, same re-bounding afterwards — and differs in what it
// returns: not a snapshot to persist, but the live pull request, which the
// caller turns into a branch name, a `human` link and nothing else.
//
// A request without `github_pull` is left untouched and no GitHub call is
// made, which is what keeps "a task created without a pull request makes no
// GitHub call at all" true in the create path.
//
// It writes its own error response and reports false when it did.
func (s *Server) applyPullPrefill(
	ctx context.Context, w http.ResponseWriter,
	project *store.Project, wf *workflow.Workflow, req *taskCreateRequest,
) (*github.PullRequest, github.Repo, bool) {
	if req.GitHubPull == nil {
		return nil, github.Repo{}, true
	}
	number := *req.GitHubPull
	if number < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("github_pull must be a positive pull request number, got %d", number))
		return nil, github.Repo{}, false
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available {
		writeGitHubUnavailable(w, gate)
		return nil, github.Repo{}, false
	}
	pull, err := s.deps.GitHub.GetPull(ctx, gate.repo, number)
	if err != nil {
		writeGitHubError(w, gate, err)
		return nil, github.Repo{}, false
	}
	// The head branch *is* the task's branch (decision 1), so a pull request
	// that does not name one cannot become a task at all. Refused here rather
	// than at admission: the human asking is right here to be told why, and a
	// task that can never run should not reach the board.
	if strings.TrimSpace(pull.HeadBranch) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("pull request #%d names no head branch, so there is no branch to run the task on", number))
		return nil, github.Repo{}, false
	}
	prefill := pullPrefill(pull, wf)
	if strings.TrimSpace(req.Title) == "" {
		req.Title = prefill.Title
	}
	if req.Description == nil {
		description := prefill.Description
		req.Description = &description
	}
	for name, value := range prefill.Fields {
		if _, present := req.Fields[name]; present {
			continue
		}
		if req.Fields == nil {
			req.Fields = map[string]string{}
		}
		req.Fields[name] = value
	}
	// Re-bound now that the request carries text vincent fetched rather than
	// text the caller typed, for the reason applyIssuePrefill does.
	if msg := boundTaskFields(req.Title, ptrValue(req.Description), req.Fields); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, github.Repo{}, false
	}
	return &pull, gate.repo, true
}
