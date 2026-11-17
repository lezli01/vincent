package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// BulkFetchMax is the most ids one POST issue/bulkfetch takes.
const BulkFetchMax = 100

// Overlap is how far an incremental search reaches back before its last
// high-water mark. The comparison itself is exact: a real Cloud site honours
// epoch-ms `updated >= N` to the millisecond (task 142.3, "Facts verified"
// 3). The reach-back is for search lag — search/jql is eventually
// consistent, and an edit was seen there 300–410 ms after its write on an
// idle site (fact 6), a lower bound rather than a budget — so a window that
// only touched the mark could miss an edit not yet indexed when it ran.
// Re-reading a few minutes is idempotent, so the margin is generous.
const Overlap = 3 * time.Minute

// IssueFieldNames is the explicit `fields` list every issue read sends:
// search/jql's default is the id alone, and naming every field read keeps the
// answer to what the mapping needs.
var IssueFieldNames = []string{
	"summary", "description", "status", "resolution", "issuetype", "priority",
	"assignee", "reporter", "parent", "issuelinks", "components", "labels",
	"created", "updated", "resolutiondate", "project", "comment",
}

// ProjectRef is an issue's project.
type ProjectRef struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// CommentSummary is the part of an issue's `comment` field read: how many
// comments it has. The thread itself is read with Comments.
type CommentSummary struct {
	Total int `json:"total"`
}

// IssueFields is an issue's `fields`. It embeds jiramap.Fields, so
// jiramap.FromJira, Labels, KindSeed and RemoteJSON take .Fields directly.
type IssueFields struct {
	jiramap.Fields
	Summary string          `json:"summary"`
	Updated string          `json:"updated"`
	Project ProjectRef      `json:"project"`
	Comment *CommentSummary `json:"comment,omitempty"`
}

// UpdatedAt is `updated` parsed; zero when absent or unparseable.
func (f IssueFields) UpdatedAt() time.Time { return parseTime(f.Updated) }

// RenderedFields is expand=renderedFields: Jira's HTML rendering.
type RenderedFields struct {
	Description string `json:"description"`
}

// Issue is one Jira issue as every issue read answers it.
type Issue struct {
	ID             string         `json:"id"`
	Key            string         `json:"key"`
	Self           string         `json:"self"`
	Fields         IssueFields    `json:"fields"`
	RenderedFields RenderedFields `json:"renderedFields"`
}

// timeLayouts are Jira's date-time shapes: millisecond precision with a
// colon-less zone, and RFC 3339 for good measure.
var timeLayouts = []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339Nano}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Scope is one Jira project a sync reads, and the binding's optional JQL
// narrowing it (task 146). Project is the numeric id, which survives a
// rename of the key, or a quoted key before the id is known.
type Scope struct {
	Project string
	JQL     string
}

// scopeClause is what a query says about the projects it reads, as a head
// that opens the query and a tail that follows the state clause. One scope
// keeps the shape a single-project binding has always sent — `project = X`,
// with its JQL as the tail — so its queries are unchanged. Several are one
// head: `project in (...)` when none narrows, otherwise a parenthesised OR
// of `(project = X AND (jql))` for each that does and the `project = Y` or
// `project in (...)` of those that do not. Each scope's JQL is parenthesised
// on its own, so an OR inside it cannot reach another project.
func scopeClause(scopes []Scope) (head, tail string) {
	if len(scopes) == 1 {
		if extra := strings.TrimSpace(scopes[0].JQL); extra != "" {
			tail = " AND (" + extra + ")"
		}
		return "project = " + scopes[0].Project, tail
	}
	var (
		plain []string
		terms []string
	)
	for _, sc := range scopes {
		if extra := strings.TrimSpace(sc.JQL); extra != "" {
			terms = append(terms, "(project = "+sc.Project+" AND ("+extra+"))")
			continue
		}
		plain = append(plain, sc.Project)
	}
	switch len(plain) {
	case 0:
	case 1:
		terms = append(terms, "project = "+plain[0])
	default:
		terms = append(terms, "project in ("+strings.Join(plain, ", ")+")")
	}
	if len(terms) == 1 {
		return terms[0], ""
	}
	return "(" + strings.Join(terms, " OR ") + ")", ""
}

// InitialJQL is InitialJQLFor one project.
func InitialJQL(projectID, extraJQL, afterID string) string {
	return InitialJQLFor([]Scope{{Project: projectID, JQL: extraJQL}}, afterID)
}

// InitialJQLFor is the first import's query: the open issues of every
// scope in id order, resuming after afterID when set. Jira's issue ids are
// unique across a site, so one id cursor walks every project at once.
func InitialJQLFor(scopes []Scope, afterID string) string {
	head, tail := scopeClause(scopes)
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(" AND statusCategory != Done")
	b.WriteString(tail)
	if afterID != "" {
		fmt.Fprintf(&b, " AND id > %s", afterID)
	}
	b.WriteString(" ORDER BY id ASC")
	return b.String()
}

// IncrementalJQL is IncrementalJQLFor one project.
func IncrementalJQL(projectID, extraJQL string, since time.Time, afterID string) string {
	return IncrementalJQLFor([]Scope{{Project: projectID, JQL: extraJQL}}, since, afterID)
}

// IncrementalJQLFor is a later sync's query: everything in the scopes
// updated since the high-water mark less Overlap — closed issues included,
// since a close is a change to mirror — as unquoted epoch milliseconds,
// which JQL compares exactly whatever the site's time zone. A zero since
// omits the updated clause.
//
// It is ordered by id, not by updated, and resumes after afterID when set:
// a walk a page cap stopped continues from the same since and the last id
// read. Resuming by time instead cannot get past a window holding more
// changes than one walk reads — one bulk edit is enough — because every
// restart lands inside the overlap and reads the same first issues again
// (review F1 on #943).
func IncrementalJQLFor(scopes []Scope, since time.Time, afterID string) string {
	head, tail := scopeClause(scopes)
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(tail)
	if !since.IsZero() {
		fmt.Fprintf(&b, " AND updated >= %d", since.Add(-Overlap).UnixMilli())
	}
	if afterID != "" {
		fmt.Fprintf(&b, " AND id > %s", afterID)
	}
	b.WriteString(" ORDER BY id ASC")
	return b.String()
}

// searchPage is one search/jql answer. There is no total.
type searchPage struct {
	Issues        []Issue `json:"issues"`
	NextPageToken string  `json:"nextPageToken"`
	IsLast        bool    `json:"isLast"`
}

// SearchResult is one Search call's issues. Truncated says maxPages stopped
// it with a page still outstanding; the caller resumes with its own JQL
// cursor, never with a page token.
type SearchResult struct {
	Issues    []Issue
	Truncated bool
}

// Search runs jql over GET /rest/api/3/search/jql — never the removed
// /rest/api/3/search — with IssueFieldNames and expand=renderedFields,
// following nextPageToken for at most maxPages pages (<= 0 is 1) within this
// call alone.
func (c *Client) Search(ctx context.Context, jql string, maxPages int) (SearchResult, error) {
	if maxPages <= 0 {
		maxPages = 1
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("fields", strings.Join(IssueFieldNames, ","))
	q.Set("expand", expandRenderedFields)
	q.Set("maxResults", strconv.Itoa(searchPageSize))
	p := c.searchPager()
	p.first(q)
	var res SearchResult
	for page := 0; ; page++ {
		var sp searchPage
		if err := c.do(ctx, "search", http.MethodGet, pathSearchJQL, q, nil, &sp); err != nil {
			return SearchResult{}, err
		}
		res.Issues = append(res.Issues, sp.Issues...)
		more := p.next(&sp, q)
		if !more {
			return res, nil
		}
		if page+1 >= maxPages {
			res.Truncated = true
			return res, nil
		}
	}
}

// GetIssue reads one issue by id or key (a key the issue had before a move
// resolves too), with the same fields and expand as Search. 404 — deleted or
// not visible, which Jira does not tell apart — is not_found.
func (c *Client) GetIssue(ctx context.Context, idOrKey string) (Issue, error) {
	q := url.Values{}
	q.Set("fields", strings.Join(IssueFieldNames, ","))
	q.Set("expand", expandRenderedFields)
	var is Issue
	if err := c.do(ctx, "get issue", http.MethodGet, pathIssue(idOrKey), q, nil, &is); err != nil {
		return Issue{}, err
	}
	return is, nil
}

// bulkFetchRequest is POST issue/bulkfetch's body.
type bulkFetchRequest struct {
	IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
	Fields         []string `json:"fields"`
	Expand         []string `json:"expand"`
	FieldsByKeys   bool     `json:"fieldsByKeys"`
}

// bulkFetchAnswer is its answer. issueErrors lists only retriable failures;
// an id deleted or not visible is simply absent from issues.
type bulkFetchAnswer struct {
	Issues []Issue `json:"issues"`
}

// BulkFetch reads up to BulkFetchMax issues by id or key in one POST
// issue/bulkfetch, with the same fields and expand as Search. Ids absent from
// the answer are simply absent. More than BulkFetchMax ids is a programming
// error, answered without a request.
func (c *Client) BulkFetch(ctx context.Context, ids []string) ([]Issue, error) {
	if len(ids) > BulkFetchMax {
		return nil, &Error{Op: "bulk fetch", Reason: ReasonBadRequest, Err: fmt.Errorf("%d ids, at most %d per call", len(ids), BulkFetchMax)}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	body := bulkFetchRequest{IssueIDsOrKeys: ids, Fields: IssueFieldNames, Expand: []string{expandRenderedFields}}
	var ans bulkFetchAnswer
	if err := c.do(ctx, "bulk fetch", http.MethodPost, pathBulkFetch, nil, body, &ans); err != nil {
		return nil, err
	}
	return ans.Issues, nil
}
