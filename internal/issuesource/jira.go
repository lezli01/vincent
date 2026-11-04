package issuesource

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/jiramap"
	"github.com/lezli01/vincent/internal/jira/render"
	"github.com/lezli01/vincent/internal/store"
)

// ErrJiraWriteBackNotImplemented is what the Jira source's write methods
// return (task 142 decision 7): the write-back is #909's, and until it lands
// the outbox is never given this source, so reaching one is a mis-wiring
// that must fail loudly rather than drop a write.
var ErrJiraWriteBackNotImplemented = errors.New("jira write-back is not implemented until #909")

// jiraDefaultPageCap bounds an incremental listing given no PageCap. The
// client's page is 100 issues; ten of them is a generous tick.
const jiraDefaultPageCap = 10

// Jira is the Jira Cloud source (issue #908, task 142 decision 9): a
// *jira.Client per bound site behind the seam. It also implements
// BulkGetter and BindingResolver, and deliberately not RepoCommentLister
// (Jira has no project-wide comment feed, so the importer reads each listed
// issue's thread) nor PlaceholderAdopter (no Jira row is a backfill
// placeholder).
type Jira struct {
	newClient func(site string) *jira.Client
}

var (
	_ Source          = (*Jira)(nil)
	_ BulkGetter      = (*Jira)(nil)
	_ BindingResolver = (*Jira)(nil)
)

// NewJira is the Jira source. newClient builds the client for a binding's
// site; it is called on every Bind and must not cache a credential (the
// client's Creds is read per request).
func NewJira(newClient func(site string) *jira.Client) *Jira {
	return &Jira{newClient: newClient}
}

// jiraRef is the handle a Jira binding carries.
type jiraRef struct {
	client  *jira.Client
	key     string // the bound project key
	jql     string // the binding's extra JQL, "" for none
	mapping jiramap.Config
	// projectID is the stable project id ResolveBinding read; "" before.
	projectID string
}

// Provider is issueprovider.Jira.
func (*Jira) Provider() string { return issueprovider.Jira }

// Bind reads project's Jira binding; it makes no call. The binding's key is
// the project key, which is also what every imported row stores in repo
// (task 142 decision 9.4's dated note), so a rebind to another key is a new
// binding. A mapping that does not parse falls back to the defaults: the API
// refuses one at write time, so this is only a defence.
func (j *Jira) Bind(_ context.Context, project store.Project) (Binding, bool) {
	src := project.IssueSource
	if src.Provider != store.IssueProviderJira || strings.TrimSpace(src.JiraSite) == "" ||
		strings.TrimSpace(src.JiraProjectKey) == "" || j.newClient == nil {
		return Binding{}, false
	}
	mapping, err := jiramap.ParseConfig([]byte(src.JiraMappingJSON))
	if err != nil {
		mapping = jiramap.Config{}
	}
	ref := &jiraRef{
		client: j.newClient(src.JiraSite), key: src.JiraProjectKey,
		jql: strings.TrimSpace(src.JiraJQL), mapping: mapping,
	}
	return NewBinding(src.JiraProjectKey, ref), true
}

// ResolveBinding reads b's project (GET project/{key}) for its stable id,
// which every later query names instead of the key.
func (*Jira) ResolveBinding(ctx context.Context, b Binding) (Binding, string, error) {
	ref := jiraRefOf(b)
	if ref == nil {
		return b, "", &jira.Error{Op: "resolve binding", Reason: jira.ReasonBadRequest, Err: errors.New("not a jira binding")}
	}
	p, err := ref.client.Project(ctx, ref.key)
	if err != nil {
		return b, "", err
	}
	next := *ref
	next.projectID = p.ID
	return NewBinding(b.String(), &next), p.ID, nil
}

// Locate is the binding a Jira row names: its project key. It carries no
// client — the site is the project's binding, not the row's — so only the
// sweep's addressability check reads it.
func (*Jira) Locate(rem store.IssueRemote) (Binding, bool) {
	if rem.RemoteKey == "" || rem.Repo == "" {
		return Binding{}, false
	}
	return NewBinding(rem.Repo, nil), true
}

// ListIssues is one search/jql walk. OpenOnly is the initial import's
// id-ordered walk of the not-done issues, resumed by After; otherwise the
// incremental walk of everything updated since opts.Since less the client's
// overlap, resumed by time.
func (*Jira) ListIssues(ctx context.Context, b Binding, opts ListOptions) (Page, error) {
	ref := jiraRefOf(b)
	if ref == nil {
		return Page{}, &jira.Error{Op: "list issues", Reason: jira.ReasonBadRequest, Err: errors.New("not a jira binding")}
	}
	pageCap := opts.PageCap
	if pageCap <= 0 {
		pageCap = jiraDefaultPageCap
	}
	var jql string
	if opts.OpenOnly {
		jql = jira.InitialJQL(ref.projectRef(), ref.jql, opts.After)
	} else {
		jql = jira.IncrementalJQL(ref.projectRef(), ref.jql, opts.Since)
	}
	res, err := ref.client.Search(ctx, jql, pageCap)
	if err != nil {
		return Page{}, err
	}
	out := Page{Truncated: res.Truncated}
	for i := range res.Issues {
		is := &res.Issues[i]
		if is.ID == "" {
			continue
		}
		out.Issues = append(out.Issues, ref.issueOf(is))
	}
	if res.Truncated && len(res.Issues) > 0 {
		last := &res.Issues[len(res.Issues)-1]
		if opts.OpenOnly {
			out.ResumeAfter = last.ID
		} else {
			out.ResumeSince = last.Fields.UpdatedAt()
		}
	}
	return out, nil
}

// GetIssue reads rem's issue by its id. One that left b's project is
// returned mapped where it is now, with an error Classify reads as
// ReasonMoved at its new browse URL. A 404 is not_found — Jira answers
// deleted, archived and unreadable alike — and never gone (decision 6).
func (*Jira) GetIssue(ctx context.Context, b Binding, rem store.IssueRemote) (Issue, error) {
	ref := jiraRefOf(b)
	if ref == nil {
		return Issue{}, &jira.Error{Op: "get issue", Reason: jira.ReasonBadRequest, Err: errors.New("not a jira binding")}
	}
	is, err := ref.client.GetIssue(ctx, rem.RemoteKey)
	if err != nil {
		return Issue{}, err
	}
	mapped := ref.issueOf(&is)
	if ref.moved(&is) {
		return mapped, &jiraMovedError{location: mapped.Remote.URL}
	}
	return mapped, nil
}

// GetIssues reads rems through bulkfetch, BulkGetMax at a time. An issue
// that left b's project is reported with MovedTo. One still in it that the
// binding's JQL now excludes is left out — absent, which the importer marks
// missing — so a JQL-narrowed binding sweeps the same set it lists.
func (*Jira) GetIssues(ctx context.Context, b Binding, rems []store.IssueRemote) (map[string]BulkIssue, error) {
	ref := jiraRefOf(b)
	if ref == nil {
		return nil, &jira.Error{Op: "get issues", Reason: jira.ReasonBadRequest, Err: errors.New("not a jira binding")}
	}
	out := make(map[string]BulkIssue, len(rems))
	for start := 0; start < len(rems); start += jira.BulkFetchMax {
		batch := rems[start:min(start+jira.BulkFetchMax, len(rems))]
		ids := make([]string, 0, len(batch))
		for _, rem := range batch {
			if rem.RemoteKey != "" {
				ids = append(ids, rem.RemoteKey)
			}
		}
		if len(ids) == 0 {
			continue
		}
		got, err := ref.client.BulkFetch(ctx, ids)
		if err != nil {
			return nil, err
		}
		var here []string
		for i := range got {
			is := &got[i]
			if is.ID == "" {
				continue
			}
			bi := BulkIssue{Issue: ref.issueOf(is)}
			if ref.moved(is) {
				bi.MovedTo = bi.Issue.Remote.URL
			} else {
				here = append(here, is.ID)
			}
			out[is.ID] = bi
		}
		if err := ref.dropExcluded(ctx, here, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// dropExcluded removes from out the ids the binding's extra JQL no longer
// matches. Without extra JQL every id still in the project matches.
func (ref *jiraRef) dropExcluded(ctx context.Context, ids []string, out map[string]BulkIssue) error {
	if ref.jql == "" || len(ids) == 0 {
		return nil
	}
	jql := "project = " + ref.projectRef() + " AND (" + ref.jql + ") AND id in (" + strings.Join(ids, ", ") + ") ORDER BY id ASC"
	res, err := ref.client.Search(ctx, jql, (len(ids)+jira.BulkFetchMax-1)/jira.BulkFetchMax+1)
	if err != nil {
		return err
	}
	match := make(map[string]bool, len(res.Issues))
	for i := range res.Issues {
		match[res.Issues[i].ID] = true
	}
	for _, id := range ids {
		if !match[id] {
			delete(out, id)
		}
	}
	return nil
}

// ListComments reads is's whole thread. A restricted comment — one with a
// visibility set — is dropped, always (decision 4): it is visible only to a
// role or group, and mirroring it would show it to everyone who reads the
// issue here. The author is the display name; the account id is not kept.
func (*Jira) ListComments(ctx context.Context, b Binding, is *Issue) ([]Comment, error) {
	ref := jiraRefOf(b)
	if ref == nil {
		return nil, &jira.Error{Op: "list comments", Reason: jira.ReasonBadRequest, Err: errors.New("not a jira binding")}
	}
	thread, err := ref.client.Comments(ctx, is.Remote.RemoteKey)
	if err != nil {
		return nil, err
	}
	base := ref.base()
	out := make([]Comment, 0, len(thread))
	for i := range thread {
		c := &thread[i]
		if c.Visibility != nil || c.ID == "" {
			continue
		}
		out = append(out, Comment{Remote: store.RemoteIssueComment{
			RemoteKey: c.ID, Author: c.Author.DisplayName,
			Body:      render.HTMLToMarkdown(c.RenderedBody, base),
			CreatedAt: c.Created.UTC(), UpdatedAt: c.Updated.UTC(),
		}})
	}
	return out, nil
}

// SetIssueState is #909's.
func (*Jira) SetIssueState(context.Context, Binding, store.IssueRemote, store.IssueStateValue) error {
	return ErrJiraWriteBackNotImplemented
}

// CreateComment is #909's.
func (*Jira) CreateComment(context.Context, Binding, store.IssueRemote, string) (string, error) {
	return "", ErrJiraWriteBackNotImplemented
}

// Classify maps the client's reasons, which are the seam's strings, onto a
// Failure, with a 429's Retry-After as ResetAt. A moved issue is
// ReasonMoved at its new browse URL. Never ReasonGone: a Jira miss is
// always missing, never a delete (decision 6).
func (*Jira) Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	var moved *jiraMovedError
	if errors.As(err, &moved) {
		return Failure{Reason: ReasonMoved, Location: moved.location}
	}
	f := Failure{Reason: jira.ReasonOf(err)}
	var jerr *jira.Error
	if errors.As(err, &jerr) {
		f.ResetAt = jerr.RetryAt
	}
	if f.Reason == "" {
		f.Reason = ReasonUnreachable
	}
	return f
}

// jiraMovedError is GetIssue's "it is elsewhere now".
type jiraMovedError struct{ location string }

func (e *jiraMovedError) Error() string { return "jira issue moved to " + e.location }

func jiraRefOf(b Binding) *jiraRef {
	ref, _ := b.Ref().(*jiraRef)
	if ref == nil || ref.client == nil {
		return nil
	}
	return ref
}

// projectRef is what a query names the project by: its stable id once
// resolved, the quoted key before.
func (ref *jiraRef) projectRef() string {
	if ref.projectID != "" {
		return ref.projectID
	}
	return strconv.Quote(ref.key)
}

// moved is whether is has left the bound project.
func (ref *jiraRef) moved(is *jira.Issue) bool {
	if ref.projectID != "" && is.Fields.Project.ID != "" {
		return is.Fields.Project.ID != ref.projectID
	}
	p, _ := splitJiraKey(is.Key)
	return p != "" && !strings.EqualFold(p, ref.key)
}

func (ref *jiraRef) base() *url.URL {
	u, err := url.Parse(ref.client.Site())
	if err != nil {
		return nil
	}
	return u
}

// issueOf maps a Jira issue onto the seam (task 142 decision 9): the id is
// the remote key, the project key the repo, the key's suffix the number,
// the browse URL — never `self` — the URL. The assignee is kept in
// remote_json only.
func (ref *jiraRef) issueOf(is *jira.Issue) Issue {
	return mapJiraIssue(is, ref.client.BrowseURL(is.Key), ref.base(), ref.mapping)
}

func mapJiraIssue(is *jira.Issue, browseURL string, base *url.URL, mapping jiramap.Config) Issue {
	project, number := splitJiraKey(is.Key)
	if is.Fields.Project.Key != "" {
		project = is.Fields.Project.Key
	}
	state, reason, _ := jiramap.FromJira(is.Fields.Fields, mapping)
	labels, _ := jiramap.Labels(is.Fields.Labels)
	author := ""
	if is.Fields.Reporter != nil {
		author = is.Fields.Reporter.DisplayName
	}
	ri := store.RemoteIssue{
		Provider: issueprovider.Jira, RemoteKey: is.ID,
		Repo: project, Number: number, URL: browseURL,
		RemoteJSON: string(jiramap.RemoteJSON(is.Key, is.Fields.Fields)),
		Title:      is.Fields.Summary,
		Body:       render.HTMLToMarkdown(is.RenderedFields.Description, base),
		Author:     author,
		State:      state, CloseReason: reason, Labels: labels,
	}
	if is.Fields.IssueType != nil {
		ri.Kind = jiramap.KindSeed(is.Fields.IssueType.Name)
	}
	if t := is.Fields.UpdatedAt(); !t.IsZero() {
		updated := t.UTC()
		ri.RemoteUpdatedAt = &updated
	}
	sv := store.IssueStateValue{State: string(issuestate.Open)}
	if state == issuestate.Closed {
		sv = store.IssueStateValue{State: string(issuestate.Closed), Reason: string(reason)}
	}
	comments := 0
	if is.Fields.Comment != nil {
		comments = is.Fields.Comment.Total
	}
	return Issue{Remote: ri, StateValue: sv, Comments: comments}
}

// splitJiraKey splits PROJ-12 on its last "-" into the project key and the
// number; a key without a positive numeric suffix has number 0.
func splitJiraKey(key string) (project string, number int) {
	i := strings.LastIndexByte(key, '-')
	if i < 1 {
		return key, 0
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n < 1 {
		return key, 0
	}
	return key[:i], n
}
