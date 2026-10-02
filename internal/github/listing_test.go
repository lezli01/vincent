package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github/fakeissues"
)

// The durable listings and the issue state write (task 130.5), held to one
// rule above all: the `gh` leg (cmd/fakegh) and the REST leg (httptest over
// the same fakeissues.Store) answer the same thing from the same corpus.
// Both fakes serve one Store.Serve, so a disagreement here is the client's.

// corpusFile writes a corpus of n issues — and a pull request after every
// tenth — updated a minute apart, and returns its path.
func corpusFile(t *testing.T, n int) string {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]map[string]any, 0, n+n/10)
	for i := 1; i <= n; i++ {
		at := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		rows = append(rows, map[string]any{
			"id": 9000 + i, "node_id": fmt.Sprintf("I_%d", i), "number": i,
			"title": fmt.Sprintf("issue %d", i), "state": "open", "state_reason": nil, "closed_at": nil,
			"created_at": at, "updated_at": at, "user": map[string]any{"login": "octocat"},
			"labels": []any{}, "assignees": []any{},
			"url":            fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", i),
			"repository_url": "https://api.github.com/repos/octo/repo",
			"html_url":       fmt.Sprintf("https://github.com/octo/repo/issues/%d", i),
		})
		if i%10 == 0 {
			rows = append(rows, map[string]any{
				"id": 50000 + i, "node_id": fmt.Sprintf("PR_%d", i), "number": 10000 + i,
				"title": "a pull", "state": "open", "created_at": at, "updated_at": at,
				"pull_request": map[string]any{"url": "https://api.github.com/repos/octo/repo/pulls/1"},
			})
		}
	}
	return writeCorpus(t, rows)
}

func writeCorpus(t *testing.T, rows any) string {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// legs returns the two clients over one corpus file: the `gh` leg through
// cmd/fakegh, and the REST leg through an httptest server serving the same
// Store. scenario applies to both.
func legs(t *testing.T, corpus, scenario string) map[string]*Client {
	t.Helper()
	gh, _ := ghClient(t, scenario)
	t.Setenv("FAKEGH_ISSUES_FILE", corpus)
	store := fakeissues.Store{Path: corpus, Now: func() time.Time { return fixtureNow }}
	rest := restClient(t, fakeRESTHandler(t, store, scenario))
	return map[string]*Client{ViaGH: gh, ViaToken: rest}
}

// fakeRESTHandler mounts Store.Serve as an HTTP server: the net/http leg of
// the fake.
func fakeRESTHandler(t *testing.T, store fakeissues.Store, scenario string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		resp, err := store.Serve(fakeissues.Request{
			Method: r.Method, Endpoint: r.URL.RequestURI(), Header: r.Header, Body: body, Scenario: scenario,
		})
		if err != nil {
			t.Errorf("fake REST: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
}

func TestListIssuesSinceLegsAgreeAcrossPages(t *testing.T) {
	corpus := corpusFile(t, 250)
	var answers []IssuePage
	for via, c := range legs(t, corpus, "success") {
		page, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{})
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		if len(page.Issues) != 250 || page.Truncated || page.Unchanged {
			t.Fatalf("%s: %d issues, truncated %v, unchanged %v; want all 250 in one walk",
				via, len(page.Issues), page.Truncated, page.Unchanged)
		}
		if page.ETag != "" {
			t.Errorf("%s: a three-page listing returned ETag %q; a page-1 304 proves nothing about it", via, page.ETag)
		}
		for i, issue := range page.Issues {
			if issue.Number != i+1 || issue.NodeID != fmt.Sprintf("I_%d", i+1) || issue.ID != int64(9001+i) {
				t.Fatalf("%s: row %d = #%d %s %d; want ascending update order, no pull requests, ids filled",
					via, i, issue.Number, issue.NodeID, issue.ID)
			}
		}
		answers = append(answers, page)
	}
	if !reflect.DeepEqual(answers[0], answers[1]) {
		t.Errorf("the two legs disagree")
	}
}

func TestListIssuesSinceResumesByWatermark(t *testing.T) {
	corpus := corpusFile(t, 250)
	for via, c := range legs(t, corpus, "success") {
		seen := map[string]int{}
		opts := ListSinceOptions{PageCap: 1}
		for calls := 0; ; calls++ {
			if calls > 10 {
				t.Fatalf("%s: the resume never finished", via)
			}
			page, err := c.ListIssuesSince(t.Context(), octoRepo, opts)
			if err != nil {
				t.Fatalf("%s: %v", via, err)
			}
			for _, issue := range page.Issues {
				seen[issue.NodeID]++
			}
			if !page.Truncated {
				break
			}
			if page.ETag != "" {
				t.Errorf("%s: a truncated walk returned an ETag", via)
			}
			opts.Since = page.ResumeSince
		}
		if len(seen) != 250 {
			t.Errorf("%s: after dedupe %d distinct issues, want every one of 250", via, len(seen))
		}
	}
}

func TestListIssuesSinceSinglePageETag(t *testing.T) {
	corpus := corpusFile(t, 3)
	for via, c := range legs(t, corpus, "success") {
		first, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{})
		if err != nil || first.ETag == "" || len(first.Issues) != 3 {
			t.Fatalf("%s: first = %+v, %v; want three issues and an ETag", via, first, err)
		}
		again, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{ETag: first.ETag})
		if err != nil {
			t.Fatalf("%s: a 304 is not an error: %v", via, err)
		}
		if !again.Unchanged || len(again.Issues) != 0 || again.ETag != first.ETag {
			t.Errorf("%s: conditional = %+v; want Unchanged with the matching ETag", via, again)
		}
		stale, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{ETag: `W/"stale"`})
		if err != nil || stale.Unchanged || len(stale.Issues) != 3 {
			t.Errorf("%s: stale ETag = %+v, %v; want the listing", via, stale, err)
		}
	}
}

func TestListIssueCommentsLegsAgree(t *testing.T) {
	corpus := writeCorpus(t, []map[string]any{
		{
			"id": 1, "node_id": "I_1", "number": 1, "title": "one", "state": "open",
			"created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
		},
		{
			"id": 101, "node_id": "IC_101", "issue_url": "https://api.github.com/repos/octo/repo/issues/1",
			"html_url": "https://github.com/octo/repo/issues/1#issuecomment-101", "body": "later",
			"user":       map[string]any{"login": "hubot"},
			"created_at": "2026-09-01T01:00:00Z", "updated_at": "2026-09-03T00:00:00Z",
		},
		{
			"id": 102, "node_id": "IC_102", "issue_url": "https://api.github.com/repos/octo/repo/issues/1",
			"html_url": "https://github.com/octo/repo/issues/1#issuecomment-102", "body": "earlier",
			"user":       map[string]any{"login": "app/dependabot"},
			"created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z",
		},
	})
	var answers []CommentPage
	for via, c := range legs(t, corpus, "success") {
		page, err := c.ListIssueComments(t.Context(), octoRepo, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), "")
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		if len(page.Comments) != 2 || page.Comments[0].ID != 102 || page.Comments[1].ID != 101 ||
			page.Comments[0].IssueNumber != 1 || page.Comments[0].Author != "dependabot[bot]" || page.ETag == "" {
			t.Fatalf("%s: comments = %+v", via, page)
		}
		again, err := c.ListIssueComments(t.Context(), octoRepo, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), page.ETag)
		if err != nil || !again.Unchanged {
			t.Errorf("%s: conditional = %+v, %v; want Unchanged", via, again, err)
		}
		answers = append(answers, page)
	}
	if !reflect.DeepEqual(answers[0], answers[1]) {
		t.Errorf("the two legs disagree:\n%+v\n%+v", answers[0], answers[1])
	}
}

// faultCorpus holds an open issue, a closed one, a deleted one and a
// transferred one.
func faultCorpus(t *testing.T) string {
	t.Helper()
	row := func(n int, state string) map[string]any {
		return map[string]any{
			"id": 7000 + n, "node_id": fmt.Sprintf("I_%d", n), "number": n, "title": fmt.Sprintf("issue %d", n),
			"state": state, "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
			"url":            fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", n),
			"repository_url": "https://api.github.com/repos/octo/repo",
			"html_url":       fmt.Sprintf("https://github.com/octo/repo/issues/%d", n),
		}
	}
	closed := row(2, "closed")
	closed["state_reason"], closed["closed_at"] = "completed", "2026-09-01T00:00:00Z"
	deleted := row(3, "open")
	deleted["_fake"] = map[string]any{"deleted": true}
	moved := row(4, "open")
	moved["_fake"] = map[string]any{"transferred_to": "octo/other#12"}
	return writeCorpus(t, []map[string]any{row(1, "open"), closed, deleted, moved, row(5, "open")})
}

func TestSetIssueStateRoundTrips(t *testing.T) {
	cases := []struct {
		number      int
		state       string
		reason      string
		duplicateOf int
		wantReason  string
	}{
		{1, StateClosed, IssueReasonCompleted, 0, "completed"},
		{5, StateClosed, IssueReasonNotPlanned, 0, "not_planned"},
		{1, StateOpen, "", 0, "reopened"},
		{1, StateClosed, IssueReasonDuplicate, 5, "duplicate"},
		{2, StateOpen, IssueReasonReopened, 0, "reopened"},
	}
	var answers [][]Issue
	for via, c := range legs(t, faultCorpus(t), "success") {
		var afters []Issue
		for _, tc := range cases {
			change, err := c.SetIssueState(t.Context(), octoRepo, tc.number, tc.state, tc.reason, tc.duplicateOf)
			if err != nil {
				t.Fatalf("%s: #%d → %s/%s: %v", via, tc.number, tc.state, tc.reason, err)
			}
			if change.Before.Number != tc.number || change.Before.NodeID == "" {
				t.Errorf("%s: preflight = %+v", via, change.Before)
			}
			after := change.After
			if after.State != tc.state || after.StateReason != tc.wantReason ||
				(tc.state == StateClosed) == after.ClosedAt.IsZero() {
				t.Errorf("%s: #%d after = %s/%s closed %v; want %s/%s",
					via, tc.number, after.State, after.StateReason, after.ClosedAt, tc.state, tc.wantReason)
			}
			afters = append(afters, after)
		}
		answers = append(answers, afters)
	}
	// Both legs write the one corpus in turn, so compare what does not depend
	// on which ran first.
	for i := range answers[0] {
		a, b := answers[0][i], answers[1][i]
		if a.Number != b.Number || a.State != b.State || a.StateReason != b.StateReason || a.NodeID != b.NodeID {
			t.Errorf("case %d: the legs disagree: %+v vs %+v", i, a, b)
		}
	}
}

func TestSetIssueStateFaults(t *testing.T) {
	for via, c := range legs(t, faultCorpus(t), "success") {
		_, err := c.SetIssueState(t.Context(), octoRepo, 3, StateClosed, IssueReasonCompleted, 0)
		if ReasonOf(err) != ReasonGone {
			t.Errorf("%s: deleted issue = %v, want gone", via, err)
		}
		_, err = c.SetIssueState(t.Context(), octoRepo, 4, StateClosed, IssueReasonCompleted, 0)
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonMoved ||
			e.Location != "https://api.github.com/repos/octo/other/issues/12" {
			t.Errorf("%s: transferred issue = %#v, want moved to octo/other#12", via, err)
		}
		_, err = c.SetIssueState(t.Context(), octoRepo, 99, StateClosed, IssueReasonCompleted, 0)
		if ReasonOf(err) != ReasonNotFound {
			t.Errorf("%s: missing issue = %v, want not_found", via, err)
		}
		_, err = c.SetIssueState(t.Context(), octoRepo, 1, StateClosed, IssueReasonDuplicate, 99)
		if ReasonOf(err) != ReasonBadRequest {
			t.Errorf("%s: duplicate of a missing issue = %v, want bad_request", via, err)
		}
	}
	// Nothing was written to the transferred issue's target or anywhere else.
	for via, c := range legs(t, faultCorpus(t), "read-only") {
		_, err := c.SetIssueState(t.Context(), octoRepo, 1, StateClosed, IssueReasonCompleted, 0)
		if ReasonOf(err) != ReasonNoWriteScope {
			t.Errorf("%s: read-only = %v, want no_write_scope", via, err)
		}
	}
}

func TestRateLimitCarriesTheReset(t *testing.T) {
	for via, c := range legs(t, faultCorpus(t), "rate-limited") {
		_, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonRateLimited {
			t.Fatalf("%s: %v, want rate_limited", via, err)
		}
		// The fake sends Retry-After: 60.
		if want := fixtureNow.Add(time.Minute).UTC(); !e.ResetAt.Equal(want) {
			t.Errorf("%s: ResetAt = %v, want %v", via, e.ResetAt, want)
		}
	}
}

func TestRateLimitResetFromHeaders(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		header http.Header
		want   time.Time
	}{
		{http.Header{"Retry-After": {"30"}}, now.Add(30 * time.Second)},
		{http.Header{"Retry-After": {"Fri, 02 Oct 2026 12:05:00 GMT"}}, now.Add(5 * time.Minute)},
		{http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}}, now.Add(time.Hour)},
		{http.Header{}, time.Time{}},
	}
	for _, tc := range cases {
		if got := rateLimitReset(tc.header, now); !got.Equal(tc.want) {
			t.Errorf("rateLimitReset(%v) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestRESTNeverFollowsARedirect: a 301 is `moved`, and its target is never
// requested — the trap fails the test if it is.
func TestRESTNeverFollowsARedirect(t *testing.T) {
	var srvURL string
	c := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/trap") {
			t.Errorf("the REST leg followed a redirect to %s", r.URL)
			return
		}
		w.Header().Set("Location", srvURL+"/trap/repos/octo/other/issues/12")
		w.WriteHeader(http.StatusMovedPermanently)
		_, _ = w.Write([]byte(`{"message":"Moved Permanently"}`))
	})
	srvURL = c.opts.BaseURL
	for _, call := range []func() error{
		func() error {
			_, err := c.SetIssueState(t.Context(), octoRepo, 4, StateClosed, IssueReasonCompleted, 0)
			return err
		},
		func() error { _, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{}); return err },
		func() error { _, err := c.Get(t.Context(), octoRepo, 4); return err },
	} {
		var e *Error
		if err := call(); !errors.As(err, &e) || e.Reason != ReasonMoved || !strings.HasSuffix(e.Location, "/trap/repos/octo/other/issues/12") {
			t.Errorf("redirect = %#v, want moved with its Location", err)
		}
	}
}

// TestSetIssueStateReadBackNeverFails: a PATCH whose answer does not parse
// is a write that happened, and is reported as what it made true.
func TestSetIssueStateReadBackNeverFails(t *testing.T) {
	store := fakeissues.Store{Path: faultCorpus(t), Now: func() time.Time { return fixtureNow }}
	serve := fakeRESTHandler(t, store, "success")
	c := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			_, _ = w.Write([]byte("{not json"))
			return
		}
		serve(w, r)
	})
	change, err := c.SetIssueState(t.Context(), octoRepo, 1, StateClosed, IssueReasonNotPlanned, 0)
	if err != nil {
		t.Fatalf("a write that happened was reported failed: %v", err)
	}
	if change.After.State != StateClosed || change.After.StateReason != IssueReasonNotPlanned || change.After.NodeID != "I_1" {
		t.Errorf("fallback = %+v", change.After)
	}
}

func TestSetIssueStateRefusesBeforeSending(t *testing.T) {
	c := restClient(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("a refused write sent %s %s", r.Method, r.URL)
	})
	cases := []struct {
		number        int
		state, reason string
		duplicateOf   int
	}{
		{0, StateClosed, IssueReasonCompleted, 0},
		{1, "merged", IssueReasonCompleted, 0},
		{1, StateClosed, "", 0},
		{1, StateClosed, IssueReasonReopened, 0},
		{1, StateOpen, IssueReasonCompleted, 0},
		{1, StateClosed, IssueReasonCompleted, 2},
		{1, StateClosed, IssueReasonDuplicate, 1},
		{1, StateClosed, IssueReasonDuplicate, -1},
	}
	for _, tc := range cases {
		_, err := c.SetIssueState(t.Context(), octoRepo, tc.number, tc.state, tc.reason, tc.duplicateOf)
		if ReasonOf(err) != ReasonBadRequest {
			t.Errorf("SetIssueState(%d, %q, %q, %d) = %v, want bad_request", tc.number, tc.state, tc.reason, tc.duplicateOf, err)
		}
	}
}

func TestGHLegSendsThePatchOnStdin(t *testing.T) {
	c, argv := ghClient(t, "success")
	t.Setenv("FAKEGH_ISSUES_FILE", faultCorpus(t))
	if _, err := c.SetIssueState(t.Context(), octoRepo, 1, StateClosed, IssueReasonCompleted, 0); err != nil {
		t.Fatal(err)
	}
	if got := recordedArgv(t, argv); !strings.Contains(got, "api -i -X PATCH --input - repos/octo/repo/issues/1") {
		t.Errorf("argv =\n%s\nwant a gh api PATCH with the body on stdin", got)
	}
}

// The captured answers (gh 2.100.0 and REST 2022-11-28, lezli01/vincent-test,
// 2026-10-02).

func TestParseGHIncludeCaptures(t *testing.T) {
	page1, ok := parseGHInclude(fixture(t, "gh_2.100.0_api_issues_page1.txt"))
	if !ok || page1.status != http.StatusOK || page1.header.Get("Etag") == "" {
		t.Fatalf("page 1 = %d %v", page1.status, page1.header)
	}
	next, err := nextLink(page1.header.Get("Link"))
	if err != nil || !strings.HasPrefix(next, "repositories/1401937766/issues?") || !strings.Contains(next, "after=") {
		t.Errorf("next = %q, %v; want GitHub's own cursor link, path and query only", next, err)
	}
	var rows []restIssue
	if err := json.Unmarshal(page1.body, &rows); err != nil || len(rows) != 2 || rows[0].NodeID == "" {
		t.Errorf("page 1 body = %d rows, %v", len(rows), err)
	}
	page2, _ := parseGHInclude(fixture(t, "gh_2.100.0_api_issues_page2.txt"))
	if next, _ := nextLink(page2.header.Get("Link")); !strings.Contains(next, "page=3") {
		t.Errorf("page 2 next = %q; a prev link must not be taken for it", next)
	}
	notModified, ok := parseGHInclude(fixture(t, "gh_2.100.0_api_issues_304.txt"))
	if !ok || notModified.status != http.StatusNotModified || len(strings.TrimSpace(string(notModified.body))) != 0 {
		t.Errorf("304 = %d %q", notModified.status, notModified.body)
	}
	deleted, ok := parseGHInclude(fixture(t, "gh_2.100.0_api_issue_deleted.txt"))
	if !ok || statusError(deleted.status, deleted.header, deleted.body, fixtureNow).Reason != ReasonGone {
		t.Errorf("410 = %d", deleted.status)
	}
}

// TestTransferLegsAgreeOnCaptures: REST's 301 and gh's followed 200 for the
// same transferred issue name the same Location.
func TestTransferLegsAgreeOnCaptures(t *testing.T) {
	asked := Repo{Owner: "lezli01", Name: "vincent-test"}
	const want = "https://api.github.com/repos/lezli01/vincent-test-second/issues/1"
	for _, name := range []string{"gh_2.100.0_api_issue_transferred.txt", "gh_2.100.0_api_issue_patch_transferred.txt"} {
		resp, ok := parseGHInclude(fixture(t, name))
		if !ok || resp.status != http.StatusOK {
			t.Fatalf("%s: status %d", name, resp.status)
		}
		_, err := parseAPIIssue(resp.body, asked, 4, fixtureNow)
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonMoved || e.Location != want {
			t.Errorf("%s: %#v, want moved to %s", name, err, want)
		}
	}
	rest, ok := parseGHInclude(fixture(t, "rest_2022-11-28_issue_transferred_301.txt"))
	if !ok {
		t.Fatal("the REST 301 capture did not parse")
	}
	if e := statusError(rest.status, rest.header, rest.body, fixtureNow); e.Reason != ReasonMoved || e.Location != want {
		t.Errorf("REST 301 = %#v, want moved to %s", e, want)
	}
	if e := statusError(http.StatusGone, nil, fixture(t, "rest_2022-11-28_issue_deleted_410.json"), fixtureNow); e.Reason != ReasonGone {
		t.Errorf("REST 410 = %v", e)
	}
	// The same issue in place is not moved.
	page1, _ := parseGHInclude(fixture(t, "gh_2.100.0_api_issues_page1.txt"))
	var rows []json.RawMessage
	_ = json.Unmarshal(page1.body, &rows)
	if _, err := parseAPIIssue(rows[0], asked, 1, fixtureNow); err != nil {
		t.Errorf("issue 1 in place: %v", err)
	}
}

func TestStateFieldsLegsAgree(t *testing.T) {
	asked := Repo{Owner: "lezli01", Name: "vincent-test"}
	gh, err := parseGHList(fixture(t, "gh_2.100.0_issue_list_state.json"), asked, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := parseRESTList(fixture(t, "rest_2022-11-28_issues_state.json"), asked, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh) != len(rest) || len(gh) == 0 {
		t.Fatalf("gh %d rows, rest %d", len(gh), len(rest))
	}
	for i := range gh {
		g, r := gh[i], rest[i]
		if g.NodeID == "" || g.NodeID != r.NodeID || g.StateReason != r.StateReason || !g.ClosedAt.Equal(r.ClosedAt) {
			t.Errorf("#%d: gh %s/%s/%v, rest %s/%s/%v", g.Number, g.NodeID, g.StateReason, g.ClosedAt,
				r.NodeID, r.StateReason, r.ClosedAt)
		}
		if g.ID != 0 || r.ID == 0 {
			t.Errorf("#%d: ID gh %d rest %d; the porcelain has no integer id and REST always does", g.Number, g.ID, r.ID)
		}
	}
}

// TestOldSnapshotsDecodeUnchanged: a github_issue_json written before task
// 130.5 decodes with the new fields empty, and encodes back byte for byte.
func TestOldSnapshotsDecodeUnchanged(t *testing.T) {
	const old = `{"repo":"octo/repo","number":200,"title":"t","body":"b","url":"https://github.com/octo/repo/issues/200","state":"open","labels":["enhancement"],"author":"octocat","assignee":"hubot","assignees":["hubot"],"milestone":"v0.2.0","milestone_number":4,"created_at":"2026-08-26T19:21:29Z","updated_at":"2026-08-26T19:30:00Z","fetched_at":"2026-08-26T20:00:00Z"}`
	var issue Issue
	if err := json.Unmarshal([]byte(old), &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Number != 200 || issue.NodeID != "" || issue.ID != 0 || issue.StateReason != "" || !issue.ClosedAt.IsZero() {
		t.Errorf("decoded = %+v", issue)
	}
	again, _ := json.Marshal(issue)
	if string(again) != old {
		t.Errorf("re-encoded =\n%s\nwant\n%s", again, old)
	}
}

// TestPackageStaysALeaf: internal/github imports nothing else from
// internal/ outside its tests.
func TestPackageStaysALeaf(t *testing.T) {
	for _, file := range goFiles(t, ".") {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(p, "github.com/lezli01/vincent/internal/") {
				t.Errorf("%s imports %s", file, p)
			}
		}
	}
}

// TestStepPathCallsNoWrite: internal/taskrun legitimately imports this
// package — it reads the Issue type — so an import-graph check cannot say
// "the step path never writes to GitHub". This does: no source in
// internal/taskrun or internal/workflow names a write method (task 068
// decision 1, task 130 decision 10).
func TestStepPathCallsNoWrite(t *testing.T) {
	writes := map[string]bool{
		"SetIssueState": true, "CreatePull": true, "MergePull": true, "ClosePull": true,
		"ReopenPull": true, "CommentPull": true, "RerunFailedJobs": true,
	}
	for _, dir := range []string{filepath.Join("..", "taskrun"), filepath.Join("..", "workflow")} {
		for _, file := range goFiles(t, dir) {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && writes[sel.Sel.Name] {
					t.Errorf("%s names %s: the step path must not write to GitHub", file, sel.Sel.Name)
				}
				return true
			})
		}
	}
}

// goFiles lists dir's non-test Go files, every build tag included.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return files
}
