package github

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The pull-request half of the package (task 052). The claim these tests
// exist to hold is task 035 decision 1's, extended: **the two legs agree**.
// The same repository read through the `gh` shape and through the REST shape
// produces the same normalized list, in the same order, with the same reasons
// on the same failures — so a client can never tell which one answered.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

var pullRepo = Repo{Owner: "octo", Name: "repo"}

// fixedNow is the FetchedAt both legs are stamped with, so the comparison is
// about the wire shapes rather than about the clock.
var fixedNow = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// TestPullLegsAgreeOnListing is the central claim: `gh pr list --json` output
// and `GET /repos/o/n/pulls` output for the same two pull requests normalize
// to exactly the same []PullRequest, in the same order.
func TestPullLegsAgreeOnListing(t *testing.T) {
	gh, err := parseGHPullList(readFixture(t, "gh_2.98.0_pr_list.json"), pullRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse gh listing: %v", err)
	}
	rest, err := parseRESTPullList(readFixture(t, "rest_pulls.json"), pullRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse rest listing: %v", err)
	}
	sortPulls(gh)
	sortPulls(rest)
	if !reflect.DeepEqual(gh, rest) {
		t.Fatalf("the two legs disagree:\n gh   = %+v\n rest = %+v", gh, rest)
	}
	if len(gh) != 2 {
		t.Fatalf("listing has %d rows, want 2", len(gh))
	}
	// Newest first, applied after parsing rather than trusted from the wire.
	if gh[0].Number != 412 || gh[1].Number != 401 {
		t.Errorf("order = %d, %d; want 412 then 401", gh[0].Number, gh[1].Number)
	}
	want := PullRequest{
		Repo: "octo/repo", Number: 412,
		Title:      "List a GitHub project's open pull requests",
		Body:       "The delivery half of the loop is invisible today.",
		URL:        "https://github.com/octo/repo/pull/412",
		State:      StateOpen,
		HeadBranch: "vincent/231-list-open-pull-requests",
		HeadRepo:   "octo/repo",
		BaseBranch: "master",
		Author:     "octocat",
		CreatedAt:  time.Date(2026, 8, 26, 19, 21, 29, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 8, 27, 9, 2, 11, 0, time.UTC),
		FetchedAt:  fixedNow,
	}
	if !reflect.DeepEqual(gh[0], want) {
		t.Errorf("normalized row =\n %+v\nwant\n %+v", gh[0], want)
	}
	if !gh[1].Draft || gh[1].Status() != "draft" {
		t.Errorf("row 401 draft = %v, status = %q; want a draft", gh[1].Draft, gh[1].Status())
	}
}

// TestPullLegsAgreeOnMerged is the merged case, which is the whole reason the
// durable link exists: an open-only listing can never answer for it, so both
// legs' single fetch must. `gh` spells the state MERGED and the REST API does
// not spell it at all — one bool has to come out of both.
func TestPullLegsAgreeOnMerged(t *testing.T) {
	gh, err := parseGHPull(readFixture(t, "gh_2.98.0_pr_view_merged.json"), pullRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse gh view: %v", err)
	}
	rest, err := parseRESTPull(readFixture(t, "rest_pull_merged.json"), pullRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse rest fetch: %v", err)
	}
	if !reflect.DeepEqual(gh, rest) {
		t.Fatalf("the two legs disagree on a merged pull request:\n gh   = %+v\n rest = %+v", gh, rest)
	}
	if !gh.Merged {
		t.Error("merged = false, want true")
	}
	if gh.State != StateClosed {
		t.Errorf("state = %q, want %q: a merged pull request is closed everywhere in vincent", gh.State, StateClosed)
	}
	if gh.Status() != "merged" {
		t.Errorf("status = %q, want merged", gh.Status())
	}
}

// TestPullBadResponseIsItsOwnReason: neither leg lets a shape it cannot read
// pass as "unreachable", and neither one's own error text escapes.
func TestPullBadResponseIsItsOwnReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"gh list", func() error { _, err := parseGHPullList([]byte("not json"), pullRepo, fixedNow); return err }},
		{"gh view", func() error { _, err := parseGHPull([]byte("{"), pullRepo, fixedNow); return err }},
		{"rest list", func() error { _, err := parseRESTPullList([]byte("not json"), pullRepo, fixedNow); return err }},
		{"rest fetch", func() error { _, err := parseRESTPull([]byte("{"), pullRepo, fixedNow); return err }},
		{"rest fetch, no number", func() error {
			_, err := parseRESTPull([]byte(`{"title":"x"}`), pullRepo, fixedNow)
			return err
		}},
		{"gh view, no number", func() error {
			_, err := parseGHPull([]byte(`{"title":"x"}`), pullRepo, fixedNow)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasonOf(tc.run()); got != ReasonBadResponse {
				t.Errorf("reason = %q, want %q", got, ReasonBadResponse)
			}
		})
	}
}

// TestRESTPullLegErrorsMapToReasons pins that the REST leg's failures land on
// the same vocabulary the `gh` leg's do, with no HTTP body reaching the
// caller through anything but Detail.
func TestRESTPullLegErrorsMapToReasons(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, ReasonUnauthorized},
		{http.StatusForbidden, ReasonForbidden},
		{http.StatusNotFound, ReasonNotFound},
		{http.StatusTooManyRequests, ReasonRateLimited},
		{http.StatusInternalServerError, ReasonUnreachable},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"a body no client may ever see"}`))
			}))
			defer srv.Close()
			c := New(Options{
				BaseURL: srv.URL,
				Getenv:  func(string) string { return "t0ken" },
				GHPath:  filepath.Join(t.TempDir(), "definitely-not-gh"),
				Now:     func() time.Time { return fixedNow },
			})
			_, err := c.ListPulls(t.Context(), pullRepo, ListOptions{})
			if got := ReasonOf(err); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
			if msg := Message(ReasonOf(err)); msg == "" {
				t.Error("a named reason rendered as an empty message")
			}
		})
	}
}

// TestRESTPullLegAsksForOpenOnly pins the request the listing leg makes: the
// endpoint is open-only by design, and the merged case is GetPull's job.
func TestRESTPullLegAsksForOpenOnly(t *testing.T) {
	var got *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL
		_, _ = w.Write(readFixture(t, "rest_pulls.json"))
	}))
	defer srv.Close()
	c := New(Options{
		BaseURL: srv.URL,
		Getenv:  func(string) string { return "t0ken" },
		GHPath:  filepath.Join(t.TempDir(), "definitely-not-gh"),
		Now:     func() time.Time { return fixedNow },
	})
	pulls, err := c.ListPulls(t.Context(), pullRepo, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pulls) != 2 {
		t.Fatalf("listing has %d rows, want 2", len(pulls))
	}
	if got.Path != "/repos/octo/repo/pulls" {
		t.Errorf("path = %q, want /repos/octo/repo/pulls", got.Path)
	}
	if state := got.Query().Get("state"); state != StateOpen {
		t.Errorf("state = %q, want %q", state, StateOpen)
	}
}

// TestCompareURLMakesNoRequest is the acceptance criterion stated as a test
// rather than left to inspection: building the compare URL contacts nothing.
//
// The whole package is pointed at a server that fails the test if it is ever
// reached, and the URL is built while it is listening.
func TestCompareURLMakesNoRequest(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()
	// The transport every leg would use, so a call made by mistake could only
	// land on the server above.
	c := New(Options{BaseURL: srv.URL, HTTP: srv.Client()})
	_ = c

	got := CompareURL(pullRepo, "master", "vincent/231-list-a-thing",
		"List a project's pull requests", "Closes #231\n\nBody with an & and a ?.")
	if reached {
		t.Fatal("building a compare URL made a request to GitHub")
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("compare URL does not parse: %v", err)
	}
	if u.Host != Host {
		t.Errorf("host = %q, want %q", u.Host, Host)
	}
	if u.Path != "/octo/repo/compare/master...vincent/231-list-a-thing" {
		t.Errorf("path = %q, want the compare path", u.Path)
	}
	q := u.Query()
	if q.Get("expand") != "1" {
		t.Errorf("expand = %q, want 1", q.Get("expand"))
	}
	if q.Get("title") != "List a project's pull requests" {
		t.Errorf("title = %q, want the human's edited title", q.Get("title"))
	}
	// The body survives escaping intact — an `&` in it must not become a
	// second query parameter.
	if q.Get("body") != "Closes #231\n\nBody with an & and a ?." {
		t.Errorf("body = %q, want the human's edited body verbatim", q.Get("body"))
	}
}

// TestPullURLIsConstructed: the URL vincent opens is one it built from a
// parsed Repo and its own template, never a string that arrived from GitHub.
func TestPullURLIsConstructed(t *testing.T) {
	got := PullURL(Repo{Owner: "octo", Name: "repo"}, 412)
	if want := "https://github.com/octo/repo/pull/412"; got != want {
		t.Errorf("PullURL = %q, want %q", got, want)
	}
	// A hostile owner or name cannot leave the path: it is escaped, not
	// interpolated raw.
	got = PullURL(Repo{Owner: "octo/../evil", Name: "repo?x=1"}, 1)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("hostile input produced an unparseable URL: %v", err)
	}
	if u.Host != Host {
		t.Errorf("host = %q, want %q: a crafted repo must not redirect the opener", u.Host, Host)
	}
	if u.RawQuery != "" {
		t.Errorf("query = %q, want empty: a crafted name must not become a parameter", u.RawQuery)
	}
}

// TestPullLinkStates pins the three states migration 0018 needs and two would
// not give: never matched, linked, and matched-but-refused.
func TestPullLinkStates(t *testing.T) {
	var none *PullLink
	if none.Linked() {
		t.Error("a nil link reads as linked")
	}
	linked := &PullLink{Repo: "octo/repo", Number: 412, Source: SourceAuto}
	if !linked.Linked() {
		t.Error("an auto link reads as unlinked")
	}
	refused := &PullLink{Repo: "octo/repo", Number: 412, Source: SourceHuman, Suppressed: true}
	if refused.Linked() {
		t.Error("a suppressed link reads as linked: a refusal is not a link")
	}
	if refused.Number != 412 {
		t.Error("a suppression forgot which pull request was refused")
	}
}
