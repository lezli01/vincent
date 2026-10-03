package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The durable listings (task 130.5): ListIssuesSince and ListIssueComments.
//
// They differ from List in what they are for. List answers a picker — one
// page, newest first, through `gh issue list` — and a poller that only needs
// the recent window. These answer a sync that must not lose a row: they walk
// every page in ascending update order, keep the node id an issue is stored
// under, and make a conditional request so an unchanged repository costs a
// 304 rather than a page.
//
// Both legs speak REST here, the `gh` leg through `gh api -i` (task 035
// decision 1's preference for gh is kept) and the token leg through net/http,
// and both parse with the same restIssue — so the two legs return the same
// rows by construction rather than by two normalizers agreeing.

// DefaultPageCap is how many pages one durable listing walks when the caller
// names no cap: 1000 rows at 100 a page. A walk the cap stops is Truncated,
// and the caller resumes from ResumeSince.
const DefaultPageCap = 10

// listPageSize is the per_page every durable listing asks for: GitHub's
// maximum, so a cap of N pages is N×100 rows.
const listPageSize = 100

// ListSinceOptions bound a durable listing.
type ListSinceOptions struct {
	// State is StateOpen, StateClosed or StateAll; empty is StateAll, because
	// a sync that asked for open issues only could not tell "closed" from
	// "never changed".
	State string
	// Since keeps rows updated at or after it (GitHub's `since` is
	// inclusive); zero is no bound.
	Since time.Time
	// ETag is the ETag a previous call returned. When it still matches,
	// the call answers Unchanged and costs GitHub nothing against the rate
	// limit.
	ETag string
	// PageCap bounds the pages walked; <= 0 means DefaultPageCap.
	PageCap int
}

func (o ListSinceOptions) state() string {
	switch strings.ToLower(strings.TrimSpace(o.State)) {
	case StateOpen:
		return StateOpen
	case StateClosed:
		return StateClosed
	default:
		return StateAll
	}
}

func pageCap(n int) int {
	if n <= 0 {
		return DefaultPageCap
	}
	return n
}

// IssuePage is one durable issue listing.
type IssuePage struct {
	// Issues are the listing's issues in ascending update order. Pull
	// requests, which GitHub's issues collection includes, are dropped.
	Issues []Issue
	// Unchanged is a 304: the ETag still matched, nothing changed, and
	// Issues is empty. It is not an error.
	Unchanged bool
	// ETag is what to send next time, and is set **only** for a listing that
	// was complete in one page. In ascending update order a newly updated row
	// lands on the last page, so a page-1 304 says nothing about a listing of
	// several; a multi-page listing returns none, and the next call is
	// unconditional. On Unchanged it is the ETag that matched.
	ETag string
	// Truncated says PageCap stopped the walk before the last page. Resume by
	// calling again with Since = ResumeSince. GitHub's `since` is inclusive,
	// so the boundary rows repeat and the caller dedupes by NodeID — the
	// watermark is the cursor, and no opaque page cursor is returned.
	Truncated bool
	// ResumeSince is the updated_at of the last row walked, pull requests
	// included — a page of pull requests still advances it, so a resume
	// cannot stall on a window holding no issues. Zero when nothing was
	// walked.
	ResumeSince time.Time
}

// ListIssuesSince walks repo's issues updated since opts.Since, oldest
// update first, across pages up to opts.PageCap.
func (c *Client) ListIssuesSince(ctx context.Context, repo Repo, opts ListSinceOptions) (IssuePage, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return IssuePage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("state", opts.state())
	q.Set("sort", "updated")
	q.Set("direction", "asc")
	q.Set("per_page", strconv.Itoa(listPageSize))
	if !opts.Since.IsZero() {
		q.Set("since", opts.Since.UTC().Truncate(time.Second).Format(time.RFC3339))
	}
	endpoint := fmt.Sprintf("repos/%s/%s/issues?%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode())
	walked, err := c.walk(ctx, cred, endpoint, opts.ETag, pageCap(opts.PageCap))
	if err != nil {
		c.logf("github durable issue list failed", "repo", repo.String(),
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return IssuePage{}, err
	}
	page := IssuePage{Unchanged: walked.unchanged, ETag: walked.etag, Truncated: walked.truncated}
	now := c.now()
	for _, body := range walked.bodies {
		var raw []restIssue
		if err := json.Unmarshal(body, &raw); err != nil {
			return IssuePage{}, newError(ReasonBadResponse, "decode issue list: %v", err)
		}
		for _, r := range raw {
			page.ResumeSince = r.UpdatedAt
			if r.PullRequest != nil {
				continue
			}
			issue := r.normalize(repo, now)
			issue.Comments = r.Comments
			page.Issues = append(page.Issues, issue)
		}
	}
	return page, nil
}

// IssueComment is one comment on an issue, as the repository's comment
// listing reports it. GitHub's issue comments include a pull request's
// conversation comments, and the listing does not say which is which:
// IssueNumber is the number either way.
type IssueComment struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	IssueNumber int       `json:"issue_number"`
	Author      string    `json:"author,omitempty"`
	Body        string    `json:"body,omitempty"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// CommentPage is one durable comment listing, with IssuePage's rules for
// Unchanged, ETag, Truncated and ResumeSince.
type CommentPage struct {
	Comments    []IssueComment
	Unchanged   bool
	ETag        string
	Truncated   bool
	ResumeSince time.Time
}

type restComment struct {
	ID       int64  `json:"id"`
	NodeID   string `json:"node_id"`
	HTMLURL  string `json:"html_url"`
	IssueURL string `json:"issue_url"`
	Body     string `json:"body"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListIssueComments walks repo's issue comments updated since since, oldest
// update first, sharing ListIssuesSince's pagination, watermark,
// single-page ETag and 304 rules, with DefaultPageCap pages.
func (c *Client) ListIssueComments(ctx context.Context, repo Repo, since time.Time, etag string) (CommentPage, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return CommentPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("sort", "updated")
	q.Set("direction", "asc")
	q.Set("per_page", strconv.Itoa(listPageSize))
	if !since.IsZero() {
		q.Set("since", since.UTC().Truncate(time.Second).Format(time.RFC3339))
	}
	endpoint := fmt.Sprintf("repos/%s/%s/issues/comments?%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode())
	walked, err := c.walk(ctx, cred, endpoint, etag, DefaultPageCap)
	if err != nil {
		c.logf("github issue comment list failed", "repo", repo.String(),
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return CommentPage{}, err
	}
	page := CommentPage{Unchanged: walked.unchanged, ETag: walked.etag, Truncated: walked.truncated}
	page.Comments, err = decodeComments(walked.bodies)
	if err != nil {
		return CommentPage{}, err
	}
	if n := len(page.Comments); n > 0 {
		page.ResumeSince = page.Comments[n-1].UpdatedAt
	}
	return page, nil
}

// ListCommentsOfIssue reads issue number's whole thread, oldest first —
// GitHub's only order on `issues/{n}/comments` — walking every page. It is
// the one-off backfill of an issue a sync imports for the first time (task
// 130 decision 24, 130.16): ListIssueComments is bounded by the sync's
// watermark, so an issue whose comments predate it would otherwise import
// with half a thread. It is unconditional and uncapped because it runs
// once per issue, its answer is never revalidated, and stopping early would
// leave exactly the hole the backfill exists to close. Every comment's
// IssueNumber is number, whatever its issue_url says, so a transferred
// issue's thread stays filed under the issue that was asked about.
func (c *Client) ListCommentsOfIssue(ctx context.Context, repo Repo, number int) ([]IssueComment, error) {
	if number <= 0 {
		return nil, newError(ReasonBadRequest, "issue number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(listPageSize))
	endpoint := fmt.Sprintf("repos/%s/%s/issues/%d/comments?%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number, q.Encode())
	walked, err := c.walk(ctx, cred, endpoint, "", math.MaxInt)
	if err != nil {
		c.logf("github issue thread read failed", "repo", repo.String(), "number", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return nil, err
	}
	comments, err := decodeComments(walked.bodies)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		comments[i].IssueNumber = number
	}
	return comments, nil
}

// decodeComments parses the pages of either comment listing, in the order
// they were walked. Both answer the same REST comment shape, so the two
// cannot drift into reading one comment two ways.
func decodeComments(bodies [][]byte) ([]IssueComment, error) {
	var out []IssueComment
	for _, body := range bodies {
		var raw []restComment
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, newError(ReasonBadResponse, "decode issue comments: %v", err)
		}
		for _, r := range raw {
			out = append(out, IssueComment{
				ID:          r.ID,
				NodeID:      r.NodeID,
				IssueNumber: trailingNumber(r.IssueURL),
				Author:      normalizeLogin(r.User.Login),
				Body:        r.Body,
				URL:         r.HTMLURL,
				CreatedAt:   r.CreatedAt,
				UpdatedAt:   r.UpdatedAt,
			})
		}
	}
	return out, nil
}

// trailingNumber is the last path segment of an API URL as a number, 0 when
// it is not one.
func trailingNumber(u string) int {
	i := strings.LastIndexByte(u, '/')
	n, err := strconv.Atoi(u[i+1:])
	if err != nil {
		return 0
	}
	return n
}

// walked is what a paginated walk read.
type walked struct {
	bodies    [][]byte
	unchanged bool
	etag      string
	truncated bool
}

// walk reads endpoint and every `rel="next"` page after it, up to that many pages.
// Only the first request is conditional: a later page's ETag is never kept
// (see IssuePage.ETag), so there is nothing to condition it on.
func (c *Client) walk(ctx context.Context, cred credential, endpoint, etag string, pages int) (walked, error) {
	var out walked
	var header http.Header
	if etag != "" {
		header = http.Header{"If-None-Match": {etag}}
	}
	for {
		resp, err := c.apiCall(ctx, cred, http.MethodGet, endpoint, header, nil)
		if err != nil {
			return walked{}, err
		}
		if resp.status == http.StatusNotModified {
			if len(out.bodies) == 0 {
				return walked{unchanged: true, etag: etag}, nil
			}
			// Only the first request carries If-None-Match, so a 304 later
			// is GitHub answering something that was not asked.
			return walked{}, newError(ReasonBadResponse, "304 on an unconditional page: %s", endpoint)
		}
		out.bodies = append(out.bodies, resp.body)
		if len(out.bodies) == 1 {
			out.etag = resp.header.Get("Etag")
		}
		next, err := nextLink(resp.header.Get("Link"))
		if err != nil {
			return walked{}, err
		}
		if next == "" {
			break
		}
		if len(out.bodies) >= pages {
			out.truncated = true
			break
		}
		endpoint, header = next, nil
	}
	if len(out.bodies) != 1 || out.truncated {
		// Only a listing complete in one page may be revalidated.
		out.etag = ""
	}
	return out, nil
}

// nextLink is the `rel="next"` target of a Link header, reduced to a path
// and query relative to the API root. GitHub's next link is not this
// package's own URL with page bumped — observed against gh 2.100.0, it names
// `repositories/{id}/issues` and carries an `after` cursor — so it is
// followed as given. Only its path and query are kept: the host is the
// leg's own (the configured base on REST, gh's host on `gh api`), so a Link
// header can never send the credential anywhere else.
func nextLink(link string) (string, error) {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		target = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(target), "<"), ">")
		u, err := url.Parse(target)
		if err != nil || u.Path == "" {
			return "", newError(ReasonBadResponse, "unreadable next link %q", target)
		}
		rel := strings.TrimPrefix(u.EscapedPath(), "/")
		if u.RawQuery != "" {
			rel += "?" + u.RawQuery
		}
		return rel, nil
	}
	return "", nil
}

// apiResponse is one REST answer, from either leg.
type apiResponse struct {
	status int
	header http.Header
	body   []byte
}

// apiCall sends one REST call over whichever leg cred names and maps any
// status but 2xx and 304 onto the reason vocabulary through statusError — on
// both legs, since `gh api -i` reports the same status line and headers the
// REST leg sees. endpoint is relative to the API root
// (`repos/o/r/issues?…`); payload is a JSON body or nil.
func (c *Client) apiCall(ctx context.Context, cred credential, method, endpoint string, header http.Header, payload []byte) (apiResponse, error) {
	var (
		resp apiResponse
		err  error
	)
	if cred.via == ViaGH {
		resp, err = c.ghAPI(ctx, cred.ghPath, method, endpoint, header, payload)
	} else {
		resp, err = c.restAPI(ctx, cred, method, endpoint, header, payload)
	}
	if err != nil {
		return apiResponse{}, err
	}
	if resp.status == http.StatusNotModified || (resp.status >= 200 && resp.status <= 299) {
		return resp, nil
	}
	if method != http.MethodGet {
		return apiResponse{}, writeStatusError(resp.status, resp.header, resp.body, c.now(), nil)
	}
	return apiResponse{}, statusError(resp.status, resp.header, resp.body, c.now())
}

func (c *Client) restAPI(ctx context.Context, cred credential, method, endpoint string, header http.Header, payload []byte) (apiResponse, error) {
	base := c.opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/"+strings.TrimPrefix(endpoint, "/"), reqBody)
	if err != nil {
		return apiResponse{}, newError(ReasonUnreachable, "build request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "vincent")
	req.Header.Set("Authorization", "Bearer "+cred.token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return apiResponse{}, newError(ReasonTimeout, "%v", err)
		}
		return apiResponse{}, newError(ReasonUnreachable, "%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return apiResponse{}, newError(ReasonUnreachable, "read response: %v", err)
	}
	return apiResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// ghAPI is `gh api -i`. gh exits 1 for every non-2xx — a 304 included, with
// `gh: HTTP 304` on stderr (captured: gh_2.100.0_api_issues_304.txt) — but
// still prints the status line and headers on stdout, so the status is read
// from there, before ghError, and a 304 is not an error. Only a failure that
// printed no status line (gh missing, logged out, the network down) is
// mapped from stderr by ghError.
//
// The request body travels on stdin through `--input -`, for the reason a
// pull request's body does: argv is under Windows' 32 KiB limit.
func (c *Client) ghAPI(ctx context.Context, path, method, endpoint string, header http.Header, payload []byte) (apiResponse, error) {
	args := []string{"api", "-i", "-X", method}
	for k, vs := range header {
		for _, v := range vs {
			args = append(args, "-H", k+": "+v)
		}
	}
	var stdin io.Reader
	if payload != nil {
		args = append(args, "--input", "-")
		stdin = bytes.NewReader(payload)
	}
	args = append(args, strings.TrimPrefix(endpoint, "/"))
	out, stderr, runErr := execGH(ctx, path, stdin, args...)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if runErr == nil {
			runErr = ctxErr
		}
		return apiResponse{}, ghError(runErr, stderr, ctxErr)
	}
	resp, ok := parseGHInclude(out)
	if !ok {
		if runErr != nil {
			return apiResponse{}, ghError(runErr, stderr, nil)
		}
		return apiResponse{}, newError(ReasonBadResponse, "gh api printed no status line")
	}
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return apiResponse{}, ghError(runErr, stderr, nil)
	}
	return resp, nil
}

// parseGHInclude reads `gh api -i` output: a status line ending in LF
// (`HTTP/2.0 200 OK`), header lines ending in CRLF, a blank line, then the
// body as received (captured: gh_2.100.0_api_issues_page1.txt). LF-only
// header lines are accepted too, so a checkout that normalized the fixture's
// line endings still parses.
func parseGHInclude(out []byte) (apiResponse, bool) {
	r := bufio.NewReader(bytes.NewReader(out))
	line, err := r.ReadString('\n')
	if err != nil {
		return apiResponse{}, false
	}
	proto, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	if !strings.HasPrefix(proto, "HTTP/") {
		return apiResponse{}, false
	}
	code, _, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(code)
	if err != nil {
		return apiResponse{}, false
	}
	header := http.Header{}
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		if err != nil {
			break
		}
	}
	body, _ := io.ReadAll(r)
	return apiResponse{status: status, header: header, body: body}, true
}
