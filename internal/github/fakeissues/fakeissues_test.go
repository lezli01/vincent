package fakeissues

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// fixture is a corpus covering every row kind: open and closed issues with
// distinct created, updated and comment counts, a pull request row, two
// comments, and one row of each fault marker.
const fixture = `[
  {"id": 1, "number": 1, "title": "one", "state": "open", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-05T00:00:00Z", "comments": 2, "user": {"login": "a"}, "html_url": "https://github.com/octo/repo/issues/1"},
  {"id": 2, "number": 2, "title": "two", "state": "closed", "state_reason": "completed", "closed_at": "2026-01-04T00:00:00Z", "created_at": "2026-01-02T00:00:00Z", "updated_at": "2026-01-04T00:00:00Z", "comments": 0, "user": {"login": "b"}, "html_url": "https://github.com/octo/repo/issues/2"},
  {"id": 3, "number": 3, "title": "three", "state": "open", "created_at": "2026-01-03T00:00:00Z", "updated_at": "2026-01-03T00:00:00Z", "comments": 5, "user": {"login": "c"}, "html_url": "https://github.com/octo/repo/issues/3"},
  {"id": 4, "number": 4, "title": "a pull", "state": "open", "created_at": "2026-01-06T00:00:00Z", "updated_at": "2026-01-06T00:00:00Z", "comments": 1, "pull_request": {"url": "https://api.github.com/repos/octo/repo/pulls/4"}},
  {"id": 5, "number": 5, "title": "moved", "state": "open", "created_at": "2026-01-07T00:00:00Z", "updated_at": "2026-01-07T00:00:00Z", "_fake": {"transferred_to": "octo/other#12"}},
  {"id": 6, "number": 6, "title": "gone", "state": "open", "created_at": "2026-01-08T00:00:00Z", "updated_at": "2026-01-08T00:00:00Z", "_fake": {"deleted": true}},
  {"id": 101, "issue_url": "https://api.github.com/repos/octo/repo/issues/1", "body": "first", "created_at": "2026-01-01T01:00:00Z", "updated_at": "2026-01-09T00:00:00Z"},
  {"id": 102, "issue_url": "https://api.github.com/repos/octo/repo/issues/1", "body": "second", "created_at": "2026-01-02T01:00:00Z", "updated_at": "2026-01-02T01:00:00Z"}
]`

func newStore(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return Store{Path: path, Now: func() time.Time { return fixedNow }}
}

func serve(t *testing.T, s Store, req Request) Response {
	t.Helper()
	resp, err := s.Serve(req)
	if err != nil {
		t.Fatalf("Serve(%+v): %v", req, err)
	}
	return resp
}

func get(t *testing.T, s Store, endpoint string) Response {
	t.Helper()
	return serve(t, s, Request{Endpoint: endpoint})
}

// ids reads the id of every row of a listing.
func ids(t *testing.T, resp Response) []int {
	t.Helper()
	if resp.Status != http.StatusOK {
		t.Fatalf("status %d: %s", resp.Status, resp.Body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		t.Fatalf("decode %s: %v", resp.Body, err)
	}
	out := []int{}
	for _, row := range rows {
		if _, ok := row["_fake"]; ok {
			t.Errorf("_fake reached the answer: %v", row)
		}
		out = append(out, int(row["id"].(float64)))
	}
	return out
}

func TestListIssuesParameters(t *testing.T) {
	s := newStore(t)
	for _, tc := range []struct {
		endpoint string
		want     []int
	}{
		// The default is open, created, desc — and the pull request row is
		// there, and the marked rows are not.
		{"repos/octo/repo/issues", []int{4, 3, 1}},
		{"/repos/octo/repo/issues?state=closed", []int{2}},
		{"repos/octo/repo/issues?state=all", []int{4, 3, 2, 1}},
		{"repos/octo/repo/issues?state=all&direction=asc", []int{1, 2, 3, 4}},
		{"repos/octo/repo/issues?state=all&sort=updated", []int{4, 1, 2, 3}},
		{"repos/octo/repo/issues?state=all&sort=updated&direction=asc", []int{3, 2, 1, 4}},
		{"repos/octo/repo/issues?state=all&sort=comments", []int{3, 1, 4, 2}},
		{"repos/octo/repo/issues?state=all&since=2026-01-04T00:00:00Z", []int{4, 2, 1}},
		{"repos/octo/repo/issues?state=all&per_page=3", []int{4, 3, 2}},
		{"repos/octo/repo/issues?state=all&per_page=3&page=2", []int{1}},
		{"repos/octo/repo/issues?state=all&per_page=3&page=3", []int{}},
		{"repos/octo/repo/issues/comments", []int{101, 102}},
		{"repos/octo/repo/issues/comments?sort=created", []int{102, 101}},
		{"repos/octo/repo/issues/comments?sort=updated&direction=asc", []int{102, 101}},
		{"repos/octo/repo/issues/comments?since=2026-01-05T00:00:00Z", []int{101}},
		{"repos/octo/repo/issues/comments?per_page=1&page=2", []int{102}},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			if got := ids(t, get(t, s, tc.endpoint)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
		})
	}
	if resp := get(t, s, "repos/octo/repo/issues?since=yesterday"); resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("bad since: status %d", resp.Status)
	}
}

func TestPerPageCapsAtOneHundred(t *testing.T) {
	rows := make([]string, 0, 120)
	for n := 1; n <= 120; n++ {
		rows = append(rows, fmt.Sprintf(`{"id": %d, "number": %d, "state": "open", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}`, n, n))
	}
	path := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(path, []byte("["+strings.Join(rows, ",")+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := get(t, Store{Path: path}, "repos/octo/repo/issues?per_page=500")
	if got := len(ids(t, resp)); got != 100 {
		t.Errorf("rows = %d, want 100", got)
	}
	if !strings.Contains(resp.Header.Get("Link"), `page=2&per_page=500>; rel="last"`) {
		t.Errorf("Link = %s", resp.Header.Get("Link"))
	}
}

func TestLinkAcrossPages(t *testing.T) {
	s := newStore(t)
	base := "https://api.github.com/repos/octo/repo/issues?"
	link := func(page int, rel string) string {
		return fmt.Sprintf(`<%spage=%d&per_page=1&state=all>; rel="%s"`, base, page, rel)
	}
	for _, tc := range []struct {
		page int
		want string
	}{
		{1, link(2, "next") + ", " + link(4, "last")},
		{2, link(1, "prev") + ", " + link(3, "next") + ", " + link(4, "last") + ", " + link(1, "first")},
		{4, link(3, "prev") + ", " + link(1, "first")},
	} {
		resp := get(t, s, fmt.Sprintf("repos/octo/repo/issues?state=all&per_page=1&page=%d", tc.page))
		if got := resp.Header.Get("Link"); got != tc.want {
			t.Errorf("page %d Link =\n%s\nwant\n%s", tc.page, got, tc.want)
		}
	}
	// One page needs no Link, the way GitHub sends none.
	if got := get(t, s, "repos/octo/repo/issues").Header.Get("Link"); got != "" {
		t.Errorf("single page Link = %s", got)
	}
}

func TestEtagAnd304(t *testing.T) {
	s := newStore(t)
	const list = "repos/octo/repo/issues?state=all&sort=updated&direction=desc"
	first := get(t, s, list)
	tag := first.Header.Get("Etag")
	if !strings.HasPrefix(tag, `W/"`) {
		t.Fatalf("Etag = %q", tag)
	}
	if again := get(t, s, list).Header.Get("Etag"); again != tag {
		t.Errorf("unchanged corpus moved the etag: %s → %s", tag, again)
	}

	hit := serve(t, s, Request{Endpoint: list, Header: http.Header{"If-None-Match": {tag}}})
	if hit.Status != http.StatusNotModified || len(hit.Body) != 0 {
		t.Fatalf("304 = %d %q", hit.Status, hit.Body)
	}
	if hit.StatusLine() != "HTTP/2.0 304 Not Modified" {
		t.Errorf("status line = %q", hit.StatusLine())
	}
	if hit.Header.Get("Etag") != tag ||
		hit.Header.Get("X-Ratelimit-Remaining") != first.Header.Get("X-Ratelimit-Remaining") {
		t.Errorf("304 headers = %v", hit.Header)
	}
	miss := serve(t, s, Request{Endpoint: list, Header: http.Header{"If-None-Match": {`W/"stale"`}}})
	if miss.Status != http.StatusOK {
		t.Errorf("stale etag answered %d", miss.Status)
	}

	serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/3", Body: []byte(`{"state":"closed"}`)})
	after := serve(t, s, Request{Endpoint: list, Header: http.Header{"If-None-Match": {tag}}})
	if after.Status != http.StatusOK || after.Header.Get("Etag") == tag {
		t.Errorf("a write left the etag: %d %s", after.Status, after.Header.Get("Etag"))
	}
}

func TestPatchPersists(t *testing.T) {
	s := newStore(t)
	reload := func(n int) Row {
		rows, err := Store{Path: s.Path}.Load()
		if err != nil {
			t.Fatal(err)
		}
		row, _ := find(rows, n)
		return row
	}
	stamp := fixedNow.Format(time.RFC3339)

	resp := serve(t, s, Request{
		Method: "PATCH", Endpoint: "repos/octo/repo/issues/1",
		Body: []byte(`{"state":"closed","state_reason":"duplicate","duplicate_issue_id":3}`),
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("PATCH = %d %s", resp.Status, resp.Body)
	}
	row := reload(1)
	if row["state"] != "closed" || row["state_reason"] != "duplicate" || row["closed_at"] != stamp || row["updated_at"] != stamp {
		t.Errorf("closed row = %v", row)
	}

	serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/2", Body: []byte(`{"state":"open"}`)})
	row = reload(2)
	if row["state"] != "open" || row["state_reason"] != "reopened" || row["closed_at"] != nil || row["updated_at"] != stamp {
		t.Errorf("reopened row = %v", row)
	}

	// A second write in the same second still moves updated_at.
	serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/2", Body: []byte(`{"state":"closed"}`)})
	if got := reload(2)["updated_at"]; got != fixedNow.Add(time.Second).Format(time.RFC3339) {
		t.Errorf("updated_at = %v", got)
	}
	if reload(2)["state_reason"] != "completed" {
		t.Errorf("default close reason = %v", reload(2)["state_reason"])
	}

	// Every other field survives a write byte for byte.
	if reload(1)["html_url"] != "https://github.com/octo/repo/issues/1" {
		t.Errorf("a write dropped a field: %v", reload(1))
	}

	for body, want := range map[string]int{
		`{"state":"merged"}`:       http.StatusUnprocessableEntity,
		`{"state_reason":"bored"}`: http.StatusUnprocessableEntity,
		`{`:                        http.StatusBadRequest,
	} {
		if got := serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/3", Body: []byte(body)}).Status; got != want {
			t.Errorf("PATCH %s = %d, want %d", body, got, want)
		}
	}
}

func TestPorcelainSharesTheCorpus(t *testing.T) {
	s := newStore(t)
	numbers := func(state string, limit int) []int {
		issues, err := s.List(state, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := []int{}
		for _, i := range issues {
			out = append(out, i["number"].(int))
		}
		return out
	}
	// No pull request, no comment, no marked row; newest first.
	if got := numbers("", 0); !reflect.DeepEqual(got, []int{3, 1}) {
		t.Errorf("open = %v", got)
	}
	if got := numbers("all", 2); !reflect.DeepEqual(got, []int{3, 2}) {
		t.Errorf("all, limit 2 = %v", got)
	}
	serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/3", Body: []byte(`{"state":"closed"}`)})
	if got := numbers("closed", 0); !reflect.DeepEqual(got, []int{3, 2}) {
		t.Errorf("closed after a write = %v", got)
	}
	issue, ok, err := s.Issue(3)
	if err != nil || !ok || issue["state"] != "CLOSED" || issue["updatedAt"] != fixedNow.Format(time.RFC3339) {
		t.Errorf("view = %v %v %v", issue, ok, err)
	}
	for _, n := range []int{4, 5, 6, 99} {
		if _, ok, _ := s.Issue(n); ok {
			t.Errorf("view %d answered", n)
		}
	}
}

func TestBuiltinCorpus(t *testing.T) {
	issues, err := Store{}.List("open", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{
			"number": 200,
			"title":  "GitHub integration: select a GitHub issue when creating a task",
			"body":   "Most work in a GitHub-hosted repo starts life as a GitHub issue.",
			"url":    "https://github.com/octo/repo/issues/200",
			"state":  "OPEN",
			"labels": []map[string]any{
				{"name": "enhancement"},
				{"name": "area/api"},
			},
			"author":    map[string]any{"login": "octocat"},
			"assignees": []map[string]any{{"login": "hubot"}},
			"milestone": map[string]any{"number": 4, "title": "v0.2.0"},
			"createdAt": "2026-08-26T19:21:29Z",
			"updatedAt": "2026-08-26T19:30:00Z",
			// The fields ghFields gained in #664.
			"id":          "I_kwDOAAAAAc4AAAAAAAAAyA",
			"stateReason": "",
			"closedAt":    nil,
		},
		{
			"number":      41,
			"title":       "Board header truncates on narrow terminals",
			"body":        "",
			"url":         "https://github.com/octo/repo/issues/41",
			"state":       "OPEN",
			"labels":      []map[string]any{},
			"author":      map[string]any{"login": "hubot"},
			"assignees":   []map[string]any{},
			"milestone":   nil,
			"createdAt":   "2026-07-01T08:00:00Z",
			"updatedAt":   "2026-07-02T08:00:00Z",
			"id":          "I_kwDOAAAAAc4AAAAAAAAAKQ",
			"stateReason": "",
			"closedAt":    nil,
		},
	}
	// Compared as JSON: the porcelain emits exactly this.
	got, _ := json.Marshal(issues)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("builtin porcelain =\n%s\nwant\n%s", got, exp)
	}

	// A set but missing file is seeded from it, rehomed.
	path := filepath.Join(t.TempDir(), "issues.json")
	s := Store{Path: path, Repo: "acme/web"}
	if ids := ids(t, get(t, s, "repos/acme/web/issues")); !reflect.DeepEqual(ids, []int{2000200, 2000041}) {
		t.Errorf("seeded ids = %v", ids)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("seed not written: %v", err)
	}
	if strings.Contains(string(raw), "octo/repo") || !strings.Contains(string(raw), "https://github.com/acme/web/issues/200") {
		t.Errorf("seed not rehomed: %s", raw)
	}
}

func TestFaultMarkers(t *testing.T) {
	s := newStore(t)
	moved := get(t, s, "repos/octo/repo/issues/5")
	if moved.Status != http.StatusMovedPermanently ||
		moved.Header.Get("Location") != "https://api.github.com/repos/octo/other/issues/12" {
		t.Errorf("transferred = %d %v", moved.Status, moved.Header)
	}
	if gone := get(t, s, "repos/octo/repo/issues/6"); gone.Status != http.StatusGone || gone.Message() != "This issue was deleted" {
		t.Errorf("deleted = %d %s", gone.Status, gone.Body)
	}
	if resp := get(t, s, "repos/octo/repo/issues/99"); resp.Status != http.StatusNotFound || resp.Message() != "Not Found" {
		t.Errorf("unknown = %d %s", resp.Status, resp.Body)
	}
	// The good issue beside them answers, without the marker key.
	ok := get(t, s, "repos/octo/repo/issues/1")
	if ok.Status != http.StatusOK || strings.Contains(string(ok.Body), "_fake") {
		t.Errorf("good issue = %d %s", ok.Status, ok.Body)
	}
	if resp := serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/6", Body: []byte(`{"state":"closed"}`)}); resp.Status != http.StatusGone {
		t.Errorf("PATCH deleted = %d", resp.Status)
	}
}

func TestScenarios(t *testing.T) {
	s := newStore(t)
	const one = "repos/octo/repo/issues/1"
	patch := Request{Method: "PATCH", Endpoint: one, Body: []byte(`{"state":"closed"}`)}

	readOnly := patch
	readOnly.Scenario = "read-only"
	if resp := serve(t, s, readOnly); resp.Status != http.StatusForbidden || resp.Message() != "Resource not accessible by integration" {
		t.Errorf("read-only PATCH = %d %s", resp.Status, resp.Body)
	}
	if resp := serve(t, s, Request{Endpoint: one, Scenario: "read-only"}); resp.Status != http.StatusOK {
		t.Errorf("read-only GET = %d", resp.Status)
	}
	if row, _, _ := s.Issue(1); row["state"] != "OPEN" {
		t.Errorf("a refused write was kept: %v", row)
	}

	for _, req := range []Request{{Endpoint: one}, patch} {
		req.Scenario = "rate-limited"
		resp := serve(t, s, req)
		if resp.Status != http.StatusForbidden || resp.Message() != "API rate limit exceeded for user ID 1." ||
			resp.Header.Get("X-Ratelimit-Remaining") != "0" || resp.Header.Get("Retry-After") == "" ||
			resp.Header.Get("X-Ratelimit-Reset") != fmt.Sprint(fixedNow.Add(time.Hour).Unix()) {
			t.Errorf("rate-limited %s = %d %v %s", req.Method, resp.Status, resp.Header, resp.Body)
		}
	}

	if _, err := s.Serve(Request{Endpoint: one, Scenario: "unreachable"}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable err = %v", err)
	}
}

// The scenario file is read on every call, over the env var, and a missing
// or empty one falls back to it.
func TestScenarioFileFlips(t *testing.T) {
	file := filepath.Join(t.TempDir(), "scenario")
	t.Setenv("FAKEGH_SCENARIO", "read-only")
	t.Setenv("FAKEGH_SCENARIO_FILE", file)
	if got := Scenario(); got != "read-only" {
		t.Errorf("missing file = %q", got)
	}
	for _, want := range []string{"unreachable", "success"} {
		if err := os.WriteFile(file, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Scenario(); got != want {
			t.Errorf("Scenario() = %q, want %q", got, want)
		}
	}
	if err := os.WriteFile(file, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Scenario(); got != "read-only" {
		t.Errorf("empty file = %q", got)
	}
}

// TestFollowRedirectsAnswersTheTarget is `gh api`'s observed behaviour on a
// transferred issue (#664): a 200 about the issue in its new repository, for
// a GET and for a PATCH, which writes nothing.
func TestFollowRedirectsAnswersTheTarget(t *testing.T) {
	s := newStore(t)
	for _, req := range []Request{
		{Endpoint: "repos/octo/repo/issues/5", FollowRedirects: true},
		{Method: "PATCH", Endpoint: "repos/octo/repo/issues/5", Body: []byte(`{"state":"closed"}`), FollowRedirects: true},
	} {
		resp := serve(t, s, req)
		var row map[string]any
		if err := json.Unmarshal(resp.Body, &row); err != nil || resp.Status != http.StatusOK {
			t.Fatalf("%s followed = %d %s", req.Method, resp.Status, resp.Body)
		}
		if row["number"] != float64(12) || row["repository_url"] != "https://api.github.com/repos/octo/other" ||
			row["url"] != "https://api.github.com/repos/octo/other/issues/12" || row["state"] != "open" {
			t.Errorf("%s followed = %v", req.Method, row)
		}
	}
}

// TestDuplicateIssueIDIsAnID: GitHub's duplicate_issue_id takes the other
// issue's integer id, and answers anything else 422 (verified in #664).
func TestDuplicateIssueIDIsAnID(t *testing.T) {
	s := newStore(t)
	resp := serve(t, s, Request{
		Method: "PATCH", Endpoint: "repos/octo/repo/issues/1",
		Body: []byte(`{"state":"closed","state_reason":"duplicate","duplicate_issue_id":999}`),
	})
	if resp.Status != http.StatusUnprocessableEntity || resp.Message() != "Issue not found for duplicate_issue_id." {
		t.Errorf("unknown duplicate id = %d %s", resp.Status, resp.Body)
	}
}
