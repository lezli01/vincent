package fakeissues

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnreachable is the `unreachable` scenario's answer: there is no HTTP
// response at all. cmd/fakegh prints gh's network-failure wording for it; an
// httptest leg drops the connection.
var ErrUnreachable = errors.New("fakeissues: api.github.com unreachable")

// Request is one REST call, the way gh api or an http.Request carries it.
type Request struct {
	// Method is GET when empty.
	Method string
	// Endpoint is the path and query, with or without a leading slash:
	// `repos/o/r/issues?state=all`.
	Endpoint string
	// Header carries If-None-Match, the one request header the fake reads.
	Header http.Header
	// Body is a PATCH's JSON object.
	Body []byte
	// Scenario is the invocation's scenario (see Scenario).
	Scenario string
	// FollowRedirects answers a transferred issue the way `gh api` does:
	// gh's net/http client follows the 301 by itself — a GET's, and a
	// PATCH's too, which it turns into a GET of the target, so nothing is
	// written — and prints the target's 200 (observed with gh 2.100.0
	// against lezli01/vincent-test, 2026-10-02). cmd/fakegh sets it; the
	// net/http leg, which never follows, leaves it false and sees the 301.
	FollowRedirects bool
}

// Response is the answer: what an HTTP server would write.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// StatusLine is the line `gh api -i` prints first: `HTTP/2.0 304 Not
// Modified`.
func (r Response) StatusLine() string {
	return fmt.Sprintf("HTTP/2.0 %d %s", r.Status, http.StatusText(r.Status))
}

// Message is the error body's `message`, which gh prints on stderr as
// `gh: <message> (HTTP n)`; empty for a body without one (a 304's).
func (r Response) Message() string {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Message
}

const (
	rateLimit     = 5000
	rateRemaining = 4999
	docsIssues    = "https://docs.github.com/rest/issues/issues"
	docsComments  = "https://docs.github.com/rest/issues/comments"
	apiBase       = "https://api.github.com/"
)

// Serve answers one request against the corpus.
func (s Store) Serve(req Request) (Response, error) {
	switch req.Scenario {
	case "unreachable":
		return Response{}, ErrUnreachable
	case "rate-limited":
		resp := s.errorResponse(http.StatusForbidden,
			"API rate limit exceeded for user ID 1.",
			"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api")
		resp.Header.Set("X-Ratelimit-Remaining", "0")
		resp.Header.Set("X-Ratelimit-Used", strconv.Itoa(rateLimit))
		resp.Header.Set("Retry-After", "60")
		return resp, nil
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	u, err := url.Parse("/" + strings.TrimPrefix(req.Endpoint, "/"))
	if err != nil {
		return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues), nil
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) < 4 || seg[0] != "repos" || seg[3] != "issues" {
		return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues), nil
	}
	rows, err := s.Load()
	if err != nil {
		return Response{}, err
	}
	var resp Response
	switch {
	case len(seg) == 4 && method == http.MethodGet:
		resp = s.listIssues(rows, u)
	case len(seg) == 5 && seg[4] == "comments" && method == http.MethodGet:
		resp = s.listComments(rows, u)
	case len(seg) == 5 && (method == http.MethodGet || method == http.MethodPatch):
		n, err := strconv.Atoi(seg[4])
		if err != nil {
			return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues), nil
		}
		if method == http.MethodGet {
			resp = s.getIssue(rows, n, req.FollowRedirects)
			break
		}
		if req.Scenario == "read-only" {
			return s.errorResponse(http.StatusForbidden,
				"Resource not accessible by integration", docsIssues+"#update-an-issue"), nil
		}
		resp, err = s.patchIssue(rows, n, req.Body, req.FollowRedirects)
		if err != nil {
			return Response{}, err
		}
	default:
		return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues), nil
	}
	if method == http.MethodGet && resp.Status == http.StatusOK {
		resp = notModified(resp, req.Header)
	}
	return resp, nil
}

// notModified turns a 200 into gh 2.100.0's observed 304 when the request's
// If-None-Match is the page's current etag: status line, the etag and the
// rate-limit headers, no body, and the remaining count the 200 reported
// (GitHub does not charge a conditional hit).
func notModified(resp Response, h http.Header) Response {
	tag := resp.Header.Get("Etag")
	if h == nil || h.Get("If-None-Match") != tag {
		return resp
	}
	out := Response{Status: http.StatusNotModified, Header: http.Header{}}
	for _, k := range []string{"Etag", "X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset", "X-Ratelimit-Resource", "X-Ratelimit-Used"} {
		if v := resp.Header.Get(k); v != "" {
			out.Header.Set(k, v)
		}
	}
	return out
}

func (s Store) headers() http.Header {
	reset := s.now().Truncate(time.Hour).Add(time.Hour).Unix()
	return http.Header{
		"X-Ratelimit-Limit":     {strconv.Itoa(rateLimit)},
		"X-Ratelimit-Remaining": {strconv.Itoa(rateRemaining)},
		"X-Ratelimit-Reset":     {strconv.FormatInt(reset, 10)},
		"X-Ratelimit-Resource":  {"core"},
		"X-Ratelimit-Used":      {strconv.Itoa(rateLimit - rateRemaining)},
	}
}

// ok is a 200 carrying v and a weak etag over the encoded body, so any
// corpus change that reaches a page gives that page a new etag and nothing
// else does.
func (s Store) ok(v any) Response {
	body, _ := json.Marshal(v)
	sum := sha256.Sum256(body)
	h := s.headers()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Etag", `W/"`+hex.EncodeToString(sum[:])+`"`)
	return Response{Status: http.StatusOK, Header: h, Body: body}
}

func (s Store) errorResponse(status int, message, docs string) Response {
	body, _ := json.Marshal(map[string]any{
		"message":           message,
		"documentation_url": docs,
		"status":            strconv.Itoa(status),
	})
	h := s.headers()
	h.Set("Content-Type", "application/json; charset=utf-8")
	return Response{Status: status, Header: h, Body: body}
}

func (s Store) listIssues(rows []Row, u *url.URL) Response {
	q := u.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	since, bad := sinceParam(q)
	if bad {
		return s.errorResponse(http.StatusUnprocessableEntity, "Validation Failed", docsIssues+"#list-repository-issues")
	}
	// Pull request rows stay in: GitHub's issues listing includes them, and
	// excluding them is the client's job.
	matched := make([]Row, 0, len(rows))
	for _, row := range rows {
		if !isIssue(row) || marked(row) {
			continue
		}
		if state != "all" && str(row, "state") != state {
			continue
		}
		if !since.IsZero() && timeField(row, "updated_at").Before(since) {
			continue
		}
		matched = append(matched, row)
	}
	by := q.Get("sort")
	if by != "updated" && by != "comments" {
		by = "created"
	}
	sortRows(matched, by, q.Get("direction") == "asc")
	return s.page(matched, u)
}

// listComments answers the repository's comment listing. Without a sort it
// is ascending by creation, which is what GitHub answers there; with one,
// direction defaults to desc as it does on the issues listing.
func (s Store) listComments(rows []Row, u *url.URL) Response {
	q := u.Query()
	since, bad := sinceParam(q)
	if bad {
		return s.errorResponse(http.StatusUnprocessableEntity, "Validation Failed", docsComments)
	}
	matched := make([]Row, 0, len(rows))
	for _, row := range rows {
		if isIssue(row) {
			continue
		}
		if !since.IsZero() && timeField(row, "updated_at").Before(since) {
			continue
		}
		matched = append(matched, row)
	}
	by, asc := q.Get("sort"), true
	if by != "" {
		asc = q.Get("direction") == "asc"
	}
	if by != "updated" {
		by = "created"
	}
	sortRows(matched, by, asc)
	return s.page(matched, u)
}

func sinceParam(q url.Values) (time.Time, bool) {
	raw := q.Get("since")
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	return t, err != nil
}

// page cuts one page out of rows and carries the Link header GitHub sends
// when there is more than one: prev, next, last, first, in GitHub's order,
// each the request's own query with page replaced.
func (s Store) page(rows []Row, u *url.URL) Response {
	q := u.Query()
	per := 30
	if n, err := strconv.Atoi(q.Get("per_page")); err == nil && n > 0 {
		per = min(n, 100)
	}
	pg := 1
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
		pg = n
	}
	last := max(1, int(math.Ceil(float64(len(rows))/float64(per))))
	out := make([]Row, 0, per)
	for i := (pg - 1) * per; i < len(rows) && i < pg*per; i++ {
		out = append(out, public(rows[i]))
	}
	resp := s.ok(out)
	if last > 1 {
		link := func(n int, rel string) string {
			lq := u.Query()
			lq.Set("page", strconv.Itoa(n))
			return fmt.Sprintf(`<%s%s?%s>; rel="%s"`, apiBase, strings.TrimPrefix(u.Path, "/"), lq.Encode(), rel)
		}
		var links []string
		if pg > 1 {
			links = append(links, link(min(pg-1, last), "prev"))
		}
		if pg < last {
			links = append(links, link(pg+1, "next"), link(last, "last"))
		}
		if pg > 1 {
			links = append(links, link(1, "first"))
		}
		resp.Header.Set("Link", strings.Join(links, ", "))
	}
	return resp
}

// find is the issue (or pull request) row numbered n.
func find(rows []Row, n int) (Row, bool) {
	for _, row := range rows {
		if got, ok := intField(row, "number"); ok && got == n && isIssue(row) {
			return row, true
		}
	}
	return nil, false
}

// fault answers a row's `_fake` marker: 410 for a deleted issue, and for a
// transferred one a 301 whose Location and body `url` name the target — or,
// when the request follows redirects as `gh api` does, the target's own 200.
//
// Verified in #664 against lezli01/vincent-test (gh 2.100.0, REST
// 2022-11-28): a deleted issue's 410 carries "This issue was deleted"; a
// transferred issue's REST 301 carries the target's API URL in Location and
// as `url`; `gh api` follows it, GET and PATCH alike, and answers 200 with
// the issue in its new repository, under a new number and a new node id. An
// issue converted to a discussion could not be produced — GitHub has no API
// for the conversion and the test repository has discussions off — so it has
// no marker here.
func (s Store) fault(row Row, follow bool) (Response, bool) {
	f := fake(row)
	if f == nil {
		return Response{}, false
	}
	if f["deleted"] == true {
		return s.errorResponse(http.StatusGone, "This issue was deleted", docsIssues+"#get-an-issue"), true
	}
	to, _ := f["transferred_to"].(string)
	if to == "" {
		return Response{}, false
	}
	repo, num, _ := strings.Cut(to, "#")
	location := apiBase + "repos/" + repo + "/issues/" + num
	if follow {
		return s.ok(transferred(row, repo, num)), true
	}
	body, _ := json.Marshal(map[string]any{
		"message":           "Moved Permanently",
		"url":               location,
		"documentation_url": "https://docs.github.com/v3/#http-redirects",
	})
	h := s.headers()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Location", location)
	return Response{Status: http.StatusMovedPermanently, Header: h, Body: body}, true
}

// transferred is row as its new repository answers it: the target's number,
// its API and web URLs, and a node id of its own (GitHub gives a transferred
// issue a new one). The integer id is moved off the original's too, so
// nothing about the answer reads as the issue that was asked for.
func transferred(row Row, repo, num string) Row {
	out := public(row)
	moved := make(Row, len(out))
	for k, v := range out {
		moved[k] = v
	}
	moved["number"] = json.Number(num)
	moved["url"] = apiBase + "repos/" + repo + "/issues/" + num
	moved["repository_url"] = apiBase + "repos/" + repo
	moved["html_url"] = "https://github.com/" + repo + "/issues/" + num
	moved["node_id"] = str(row, "node_id") + "_moved"
	if id, ok := intField(row, "id"); ok {
		moved["id"] = json.Number(strconv.Itoa(id + 1))
	}
	return moved
}

// hasID reports an issue row whose integer id is v.
func hasID(rows []Row, v any) bool {
	want := fmt.Sprint(v)
	for _, row := range rows {
		if id, ok := intField(row, "id"); ok && isIssue(row) && strconv.Itoa(id) == want {
			return true
		}
	}
	return false
}

func (s Store) getIssue(rows []Row, n int, follow bool) Response {
	row, ok := find(rows, n)
	if !ok {
		return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues+"#get-an-issue")
	}
	if resp, ok := s.fault(row, follow); ok {
		return resp
	}
	return s.ok(public(row))
}

// patchIssue applies a state write the way GitHub does: closing stamps
// closed_at and defaults state_reason to completed, reopening clears
// closed_at and sets state_reason to reopened, and every write moves
// updated_at forward — strictly, so two writes inside one second still
// change it.
func (s Store) patchIssue(rows []Row, n int, body []byte, follow bool) (Response, error) {
	row, ok := find(rows, n)
	if !ok {
		return s.errorResponse(http.StatusNotFound, "Not Found", docsIssues+"#update-an-issue"), nil
	}
	if resp, ok := s.fault(row, follow); ok {
		return resp, nil
	}
	var patch map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if len(bytes.TrimSpace(body)) > 0 {
		if err := dec.Decode(&patch); err != nil {
			return s.errorResponse(http.StatusBadRequest, "Problems parsing JSON", docsIssues+"#update-an-issue"), nil
		}
	}
	invalid := s.errorResponse(http.StatusUnprocessableEntity, "Validation Failed", docsIssues+"#update-an-issue")
	reason, hasReason := patch["state_reason"]
	if hasReason && reason != nil {
		switch reason {
		case "completed", "not_planned", "reopened", "duplicate":
		default:
			return invalid, nil
		}
	}
	now := s.now()
	stamp := now.Format(time.RFC3339)
	wasClosed := str(row, "state") == "closed"
	switch patch["state"] {
	case nil:
		if hasReason {
			row["state_reason"] = reason
		}
	case "closed":
		row["state"] = "closed"
		if !wasClosed {
			row["closed_at"] = stamp
		}
		row["state_reason"] = "completed"
		if hasReason && reason != nil {
			row["state_reason"] = reason
		}
	case "open":
		row["state"] = "open"
		row["closed_at"] = nil
		if wasClosed {
			row["state_reason"] = "reopened"
		}
	default:
		return invalid, nil
	}
	// duplicate_issue_id is the other issue's integer id — not its number:
	// GitHub answers a number with this 422 (verified in #664) — and is not
	// stored, because GitHub records it as a timeline event, not as a field
	// of the issue.
	if dup, ok := patch["duplicate_issue_id"]; ok && dup != nil && !hasID(rows, dup) {
		return s.errorResponse(http.StatusUnprocessableEntity, "Issue not found for duplicate_issue_id.",
			docsIssues+"#update-an-issue"), nil
	}
	if prev := timeField(row, "updated_at"); !now.After(prev) {
		stamp = prev.Add(time.Second).Format(time.RFC3339)
	}
	row["updated_at"] = stamp
	if err := s.Save(rows); err != nil {
		return Response{}, err
	}
	return s.ok(public(row)), nil
}
