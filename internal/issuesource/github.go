package issuesource

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// ProviderGitHub is the provider column's value for every GitHub row.
// It is the registry's id (internal/issueprovider), which owns the value.
const ProviderGitHub = issueprovider.GitHub

// GitHub is the GitHub source: *github.Client behind the seam. It also
// implements RepoCommentLister and PlaceholderAdopter.
type GitHub struct {
	client *github.Client
	git    *gitx.Git
}

var (
	_ Source             = (*GitHub)(nil)
	_ RepoCommentLister  = (*GitHub)(nil)
	_ PlaceholderAdopter = (*GitHub)(nil)
)

// NewGitHub wraps client. git reads a project's origin for Bind; a nil git
// binds nothing, which is how a caller with no git runner sees no project
// as GitHub-based. Calls through a nil client panic: a caller gates on
// having one first.
func NewGitHub(client *github.Client, git *gitx.Git) *GitHub {
	return &GitHub{client: client, git: git}
}

// Provider is ProviderGitHub.
func (*GitHub) Provider() string { return ProviderGitHub }

// AdoptsPlaceholders marks GitHub as the provider whose rows may be backfill
// placeholders.
func (*GitHub) AdoptsPlaceholders() bool { return true }

// Bind derives the project's repository from its `origin` remote at the
// point of use — the API's derivation, with its known narrowness: an SSH
// alias, or a GitHub remote not named `origin`, is simply not GitHub-based
// (task 035 decision 5, unreversed by task 052).
func (g *GitHub) Bind(ctx context.Context, project store.Project) (Binding, bool) {
	repo, ok := RepoOf(ctx, g.git, project)
	if !ok {
		return Binding{}, false
	}
	return BindRepo(repo), true
}

// RepoOf is Bind's derivation for the callers that want the github.Repo
// itself — the pull request half and the GitHub triggers. A nil git binds
// nothing.
func RepoOf(ctx context.Context, git *gitx.Git, project store.Project) (github.Repo, bool) {
	if git == nil {
		return github.Repo{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	remote, err := git.Run(ctx, project.Path, "remote", "get-url", "origin")
	if err != nil {
		return github.Repo{}, false
	}
	return github.ParseRemote(remote)
}

// BindRepo is repo's binding.
func BindRepo(repo github.Repo) Binding { return NewBinding(repo.String(), repo) }

// Locate is the repository a remote row names. A row with no owner/name or
// no issue number cannot be addressed on GitHub.
func (*GitHub) Locate(rem store.IssueRemote) (Binding, bool) {
	repo, ok := splitRepo(rem.Repo)
	if !ok || rem.Number < 1 {
		return Binding{}, false
	}
	return BindRepo(repo), true
}

// ListIssues is github.Client.ListIssuesSince.
func (g *GitHub) ListIssues(ctx context.Context, b Binding, opts ListOptions) (Page, error) {
	state := github.StateAll
	if opts.OpenOnly {
		state = github.StateOpen
	}
	page, err := g.client.ListIssuesSince(ctx, repoOf(b), github.ListSinceOptions{
		State: state, Since: opts.Since, ETag: opts.ETag, PageCap: opts.PageCap,
	})
	if err != nil {
		return Page{}, err
	}
	out := Page{
		Unchanged: page.Unchanged, ETag: page.ETag,
		Truncated: page.Truncated, ResumeSince: page.ResumeSince,
	}
	for i := range page.Issues {
		if page.Issues[i].NodeID == "" {
			// Not addressable by the store's key; the listing skips it, as
			// the importer always has.
			continue
		}
		out.Issues = append(out.Issues, issueOf(&page.Issues[i]))
	}
	return out, nil
}

// GetIssue is github.Client.GetIssue for rem's number in b.
func (g *GitHub) GetIssue(ctx context.Context, b Binding, rem store.IssueRemote) (Issue, error) {
	is, err := g.client.GetIssue(ctx, repoOf(b), rem.Number)
	if err != nil {
		return Issue{}, err
	}
	return issueOf(&is), nil
}

// ListComments is github.Client.ListCommentsOfIssue.
func (g *GitHub) ListComments(ctx context.Context, b Binding, is *Issue) ([]Comment, error) {
	thread, err := g.client.ListCommentsOfIssue(ctx, repoOf(b), is.Remote.Number)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(thread))
	for i := range thread {
		out = append(out, commentOf(&thread[i]))
	}
	return out, nil
}

// ListRepoComments is github.Client.ListIssueComments: one conditional
// listing of every issue comment in b's repository.
func (g *GitHub) ListRepoComments(ctx context.Context, b Binding, q CommentQuery) (CommentPage, error) {
	page, err := g.client.ListIssueComments(ctx, repoOf(b), q.Since, q.ETag)
	if err != nil {
		return CommentPage{}, err
	}
	out := CommentPage{
		Unchanged: page.Unchanged, ETag: page.ETag,
		Truncated: page.Truncated, ResumeSince: page.ResumeSince,
	}
	for i := range page.Comments {
		out.Comments = append(out.Comments, commentOf(&page.Comments[i]))
	}
	return out, nil
}

// SetIssueState is github.Client.SetIssueState.
func (g *GitHub) SetIssueState(ctx context.Context, b Binding, rem store.IssueRemote, v store.IssueStateValue) error {
	_, err := g.client.SetIssueState(ctx, repoOf(b), rem.Number, v.State, v.Reason, v.DuplicateOf)
	return err
}

// CreateComment is github.Client.CreateIssueComment; GitHub's integer
// comment id, in decimal, is the remote key.
func (g *GitHub) CreateComment(ctx context.Context, b Binding, rem store.IssueRemote, body string) (string, error) {
	posted, err := g.client.CreateIssueComment(ctx, repoOf(b), rem.Number, body)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(posted.ID, 10), nil
}

// Classify is github.ReasonOf, with the rate limit's reset and a moved
// issue's location.
func (*GitHub) Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	f := Failure{Reason: github.ReasonOf(err)}
	var gerr *github.Error
	if errors.As(err, &gerr) {
		f.ResetAt = gerr.ResetAt
		f.Location = gerr.Location
	}
	return f
}

// repoOf is the github.Repo a binding this source made carries.
func repoOf(b Binding) github.Repo {
	repo, _ := b.Ref().(github.Repo)
	return repo
}

// splitRepo parses a remote row's "owner/name".
func splitRepo(s string) (github.Repo, bool) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return github.Repo{}, false
	}
	return github.Repo{Owner: owner, Name: name}, true
}

// commentOf maps a GitHub comment onto the store's shape. GitHub's integer
// comment id is the remote key: it is what the repository listing and the
// per-issue read agree on, so a comment both report is one row.
func commentOf(c *github.IssueComment) Comment {
	return Comment{
		IssueNumber: c.IssueNumber,
		Remote: store.RemoteIssueComment{
			RemoteKey: strconv.FormatInt(c.ID, 10), Author: c.Author, Body: c.Body,
			CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
		},
	}
}

// remoteJSON is what an imported issue keeps of GitHub's that the issue
// row has no column for. State is GitHub's own open/closed, kept beside the
// row's: the issue detail's source section shows it as the state GitHub last
// reported (the API's remote_state), which the local state need not match.
type remoteJSON struct {
	State           string     `json:"state,omitempty"`
	Assignees       []string   `json:"assignees,omitempty"`
	Milestone       string     `json:"milestone,omitempty"`
	MilestoneNumber int        `json:"milestone_number,omitempty"`
	Author          string     `json:"author,omitempty"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	StateReason     string     `json:"state_reason,omitempty"`
}

// issueOf maps a GitHub issue onto the seam's shape.
func issueOf(is *github.Issue) Issue {
	return Issue{Remote: remoteIssue(is), StateValue: stateValue(is), Comments: is.Comments}
}

// remoteIssue maps a GitHub issue onto the store's provider-neutral shape.
// Kind is left empty: kind is vincent's, and GitHub has none to give.
func remoteIssue(is *github.Issue) store.RemoteIssue {
	extra := remoteJSON{
		State: is.State, Assignees: assignees(is), Milestone: is.Milestone, MilestoneNumber: is.MilestoneNumber,
		Author: is.Author, StateReason: is.StateReason,
	}
	if !is.ClosedAt.IsZero() {
		closed := is.ClosedAt.UTC()
		extra.ClosedAt = &closed
	}
	raw, _ := json.Marshal(extra)
	ri := store.RemoteIssue{
		Provider: ProviderGitHub, RemoteKey: is.NodeID,
		Repo: is.Repo, Number: is.Number, URL: is.URL, RemoteJSON: string(raw),
		Title: is.Title, Body: is.Body, Author: is.Author,
		State: issuestate.Open, Labels: is.Labels,
	}
	if !is.UpdatedAt.IsZero() {
		updated := is.UpdatedAt.UTC()
		ri.RemoteUpdatedAt = &updated
	}
	if is.State == github.StateClosed {
		ri.State = issuestate.Closed
		// GitHub's state_reason is issuestate's vocabulary already; anything
		// else (none, or "reopened" on a closed row) closes as completed.
		reason, err := issuestate.ResolveReason(issuestate.RemoteClosed, issuestate.Reason(is.StateReason))
		if err != nil {
			reason = issuestate.Completed
		}
		ri.CloseReason = reason
	}
	return ri
}

// stateValue is GitHub's issue as the outbox compares it.
func stateValue(is *github.Issue) store.IssueStateValue {
	if is.State != github.StateClosed {
		return store.IssueStateValue{State: string(issuestate.Open)}
	}
	reason := is.StateReason
	if !issuestate.ValidReason(issuestate.Reason(reason)) {
		reason = string(issuestate.Completed)
	}
	return store.IssueStateValue{State: string(issuestate.Closed), Reason: reason}
}

func assignees(is *github.Issue) []string {
	if len(is.Assignees) > 0 {
		return is.Assignees
	}
	if is.Assignee != "" {
		return []string{is.Assignee}
	}
	return nil
}
