package fakeissues

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// commentCount reads issue number's `comments` from the issue listing.
func commentCount(t *testing.T, s Store, number int) int {
	t.Helper()
	resp := get(t, s, "repos/octo/repo/issues?state=all&per_page=100")
	var rows []struct {
		Number   int `json:"number"`
		Comments int `json:"comments"`
	}
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Number == number {
			return r.Comments
		}
	}
	t.Fatalf("#%d not listed", number)
	return 0
}

func TestIssueCommentsRoute(t *testing.T) {
	s := newStore(t)
	// Oldest first, whatever the rows' update order: 101 was edited last.
	if got := ids(t, get(t, s, "repos/octo/repo/issues/1/comments")); !reflect.DeepEqual(got, []int{101, 102}) {
		t.Errorf("thread of #1 = %v", got)
	}
	if got := ids(t, get(t, s, "repos/octo/repo/issues/2/comments")); len(got) != 0 {
		t.Errorf("thread of #2 = %v, want empty", got)
	}
	page2 := get(t, s, "repos/octo/repo/issues/1/comments?per_page=1&page=2")
	if got := ids(t, page2); !reflect.DeepEqual(got, []int{102}) {
		t.Errorf("page 2 = %v", got)
	}
	if link := page2.Header.Get("Link"); !strings.Contains(link, `rel="prev"`) || strings.Contains(link, `rel="next"`) {
		t.Errorf("last page Link = %q", link)
	}
	if link := get(t, s, "repos/octo/repo/issues/1/comments?per_page=1").Header.Get("Link"); !strings.Contains(link,
		"repos/octo/repo/issues/1/comments?page=2&per_page=1>; rel=\"next\"") {
		t.Errorf("first page Link = %q", link)
	}
	// The same REST shape the repository listing answers.
	var thread, repoWide []map[string]any
	_ = json.Unmarshal(get(t, s, "repos/octo/repo/issues/1/comments").Body, &thread)
	_ = json.Unmarshal(get(t, s, "repos/octo/repo/issues/comments?sort=created&direction=asc").Body, &repoWide)
	if !reflect.DeepEqual(thread, repoWide) {
		t.Errorf("thread %v\nrepo-wide %v", thread, repoWide)
	}
	for endpoint, want := range map[string]int{
		"repos/octo/repo/issues/99/comments": http.StatusNotFound,
		"repos/octo/repo/issues/6/comments":  http.StatusGone,
		"repos/octo/repo/issues/5/comments":  http.StatusMovedPermanently,
	} {
		if got := get(t, s, endpoint).Status; got != want {
			t.Errorf("%s = %d, want %d", endpoint, got, want)
		}
	}
	if resp := serve(t, s, Request{Endpoint: "repos/octo/repo/issues/5/comments", FollowRedirects: true}); resp.Status != http.StatusOK {
		t.Errorf("followed transfer = %d", resp.Status)
	}
}

func TestCommentCountFollowsTheRows(t *testing.T) {
	s := newStore(t)
	s.Now = func() time.Time { return fixedNow }
	if got := commentCount(t, s, 1); got != 2 {
		t.Errorf("#1 comments = %d, want 2", got)
	}
	// #3's file says 5 and it has no rows.
	if got := commentCount(t, s, 3); got != 0 {
		t.Errorf("#3 comments = %d, want 0", got)
	}
	id, err := s.AddComment(3, "octocat", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id != 103 {
		t.Errorf("new id = %d, want one past the largest", id)
	}
	if got := commentCount(t, s, 3); got != 1 {
		t.Errorf("#3 comments after add = %d, want 1", got)
	}
	var issue struct {
		Comments int `json:"comments"`
	}
	_ = json.Unmarshal(get(t, s, "repos/octo/repo/issues/3").Body, &issue)
	if issue.Comments != 1 {
		t.Errorf("GET #3 comments = %d", issue.Comments)
	}
	var thread []struct {
		ID       int64  `json:"id"`
		Body     string `json:"body"`
		IssueURL string `json:"issue_url"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	_ = json.Unmarshal(get(t, s, "repos/octo/repo/issues/3/comments").Body, &thread)
	if len(thread) != 1 || thread[0].ID != id || thread[0].Body != "hello" || thread[0].User.Login != "octocat" ||
		thread[0].IssueURL != "https://api.github.com/repos/octo/repo/issues/3" {
		t.Errorf("thread = %+v", thread)
	}
	// A pull request's conversation comments count on it, as on GitHub.
	if _, err := s.AddComment(4, "hubot", "on the pull"); err != nil {
		t.Fatal(err)
	}
	if got := commentCount(t, s, 4); got != 1 {
		t.Errorf("#4 comments = %d, want 1", got)
	}
	if err := s.RemoveComment(id); err != nil {
		t.Fatal(err)
	}
	if got := commentCount(t, s, 3); got != 0 {
		t.Errorf("#3 comments after remove = %d, want 0", got)
	}
	if _, err := s.AddComment(99, "x", "y"); err == nil {
		t.Error("a comment on an absent issue was filed")
	}
	if err := s.EditComment(999, "x"); err == nil {
		t.Error("an absent comment was edited")
	}
	if _, err := (Store{}).AddComment(1, "x", "y"); !errors.Is(err, ErrNoCorpusFile) {
		t.Errorf("no corpus file: %v", err)
	}
}

// TestCommentListingEtag: the repository's comment listing revalidates on
// its own — an issue write leaves it 304, a comment edit makes it 200, and
// the edit leaves the issue listing 304.
func TestCommentListingEtag(t *testing.T) {
	s := newStore(t)
	s.RequestLog = filepath.Join(t.TempDir(), "requests.log")
	const (
		comments = "repos/octo/repo/issues/comments?sort=updated&direction=asc&per_page=100"
		issues   = "repos/octo/repo/issues?state=all&sort=updated&direction=asc&per_page=100"
	)
	conditional := func(endpoint, tag string) Response {
		return serve(t, s, Request{Endpoint: endpoint, Header: http.Header{"If-None-Match": {tag}}})
	}
	ctag := get(t, s, comments).Header.Get("Etag")
	itag := get(t, s, issues).Header.Get("Etag")
	if !strings.HasPrefix(ctag, `W/"`) || ctag == itag {
		t.Fatalf("comment etag %q, issue etag %q", ctag, itag)
	}
	if got := conditional(comments, ctag).Status; got != http.StatusNotModified {
		t.Errorf("unchanged comments = %d", got)
	}

	serve(t, s, Request{Method: "PATCH", Endpoint: "repos/octo/repo/issues/2", Body: []byte(`{"state":"open"}`)})
	if got := conditional(comments, ctag).Status; got != http.StatusNotModified {
		t.Errorf("an issue write moved the comment listing: %d", got)
	}
	itag = get(t, s, issues).Header.Get("Etag")

	if err := s.EditComment(102, "second, edited"); err != nil {
		t.Fatal(err)
	}
	after := conditional(comments, ctag)
	if after.Status != http.StatusOK || after.Header.Get("Etag") == ctag {
		t.Errorf("a comment edit left the comment listing: %d", after.Status)
	}
	if got := ids(t, after); !reflect.DeepEqual(got, []int{101, 102}) {
		t.Errorf("after edit, update order = %v, want the edited one last", got)
	}
	if got := conditional(issues, itag).Status; got != http.StatusNotModified {
		t.Errorf("a comment edit moved the issue listing: %d", got)
	}

	// The request log counts each listing apart.
	for _, tc := range []struct {
		path   string
		status int
		want   int
	}{
		{"repos/octo/repo/issues/comments", 0, 4},
		{"repos/octo/repo/issues/comments", http.StatusNotModified, 2},
		{"repos/octo/repo/issues", 0, 3},
		{"/repos/octo/repo/issues", http.StatusNotModified, 1},
	} {
		got, err := CountRequests(s.RequestLog, "get", tc.path, tc.status)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("CountRequests(%s, %d) = %d, want %d", tc.path, tc.status, got, tc.want)
		}
	}
	if n, _ := CountRequests(s.RequestLog, "PATCH", "repos/octo/repo/issues/2", 200); n != 1 {
		t.Errorf("PATCH count = %d", n)
	}
	if reqs, err := Requests(filepath.Join(t.TempDir(), "never")); err != nil || reqs != nil {
		t.Errorf("unwritten log = %v, %v", reqs, err)
	}
}
