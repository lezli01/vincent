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
// high-water mark: search/jql is eventually consistent (an issue written a
// moment ago can still answer its old `updated`), and `updated` compares at
// minute granularity on some sites, so a window that only touched the mark
// would miss an edit. Re-reading a few minutes is idempotent.
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

// InitialJQL is the first import's query: the project's open issues in id
// order, resuming after afterID when set, and narrowed by the binding's extra
// JQL when set. The project is its numeric id, which survives a rename of
// the key.
func InitialJQL(projectID, extraJQL, afterID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project = %s AND statusCategory != Done", projectID)
	if extra := strings.TrimSpace(extraJQL); extra != "" {
		fmt.Fprintf(&b, " AND (%s)", extra)
	}
	if afterID != "" {
		fmt.Fprintf(&b, " AND id > %s", afterID)
	}
	b.WriteString(" ORDER BY id ASC")
	return b.String()
}

// IncrementalJQL is a later sync's query: everything in the project updated
// since the high-water mark less Overlap — closed issues included, since a
// close is a change to mirror — as unquoted epoch milliseconds, which JQL
// compares exactly whatever the site's time zone. A zero since omits the
// updated clause.
func IncrementalJQL(projectID, extraJQL string, since time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project = %s", projectID)
	if extra := strings.TrimSpace(extraJQL); extra != "" {
		fmt.Fprintf(&b, " AND (%s)", extra)
	}
	if !since.IsZero() {
		fmt.Fprintf(&b, " AND updated >= %d", since.Add(-Overlap).UnixMilli())
	}
	b.WriteString(" ORDER BY updated ASC, id ASC")
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
