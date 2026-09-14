package github

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Listings a poller can diff (task 096 decisions D and E): StateAll, a Since
// bound on both legs, and the fields the diff reads.
//
// The fixtures are captured from lezli01/vincent on 2026-09-13 with exactly
// the argv and query this package builds for StateAll, Limit 5 and a Since of
// 2026-09-10T00:00:00Z:
//
//	gh_2.100.0_pr_list_all.json      gh pr list --repo … --state all --limit 5 --search "updated:>=… sort:updated-desc" --json <ghPullFields>
//	gh_2.100.0_issue_list_all.json   gh issue list (same flags) --json <ghFields>
//	rest_2022-11-28_pulls_all.json   GET /repos/lezli01/vincent/pulls?state=all&sort=updated&direction=desc&per_page=5
//	rest_2022-11-28_issues_all.json  GET /repos/lezli01/vincent/issues?state=all&since=…&sort=updated&direction=desc&per_page=5
//
// Trimmed, and only trimmed: the pull-request pair keeps the same three rows
// on both legs (334, 360, 364) and the gh issue listing the three issues the
// REST page also returned (362, 363, 365 — the REST page keeps its two pull
// requests, which the leg must drop); every body is cut to its first line on
// both legs alike; and the gh rollups are emptied, because checks have their
// own fixtures and nothing here reads them.

func TestPullLegsAgreeOnStateAll(t *testing.T) {
	gh, err := parseGHPullList(readFixture(t, "gh_2.100.0_pr_list_all.json"), vincentRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse gh listing: %v", err)
	}
	rest, err := parseRESTPullList(readFixture(t, "rest_2022-11-28_pulls_all.json"), vincentRepo, fixedNow)
	if err != nil {
		t.Fatalf("parse rest listing: %v", err)
	}
	sortPulls(gh)
	sortPulls(rest)
	if !reflect.DeepEqual(gh, rest) {
		t.Fatalf("the two legs disagree:\n gh   = %+v\n rest = %+v", gh, rest)
	}
	byNumber := map[int]PullRequest{}
	for _, p := range gh {
		byNumber[p.Number] = p
	}
	if len(byNumber) != 3 {
		t.Fatalf("listing has %d rows, want 3", len(byNumber))
	}

	// A dependabot bump waiting on a review: the review_requested case.
	bump := byNumber[360]
	if !reflect.DeepEqual(bump.RequestedReviewers, []string{"lezli01"}) {
		t.Errorf("#360 requested reviewers = %v, want [lezli01]", bump.RequestedReviewers)
	}
	if !reflect.DeepEqual(bump.Labels, []string{"dependencies", "go"}) {
		t.Errorf("#360 labels = %v, want [dependencies go]", bump.Labels)
	}
	if bump.Author != "dependabot[bot]" || bump.State != StateOpen || bump.Draft || bump.Merged {
		t.Errorf("#360 = %+v, want an open, non-draft bump by dependabot[bot]", bump)
	}

	// Merged: in the listing at all only because it asked for StateAll.
	merged := byNumber[364]
	if !merged.Merged || merged.State != StateClosed || merged.Status() != "merged" {
		t.Errorf("#364 merged = %v, state = %q; want merged and closed", merged.Merged, merged.State)
	}
	if merged.RequestedReviewers != nil || merged.Labels != nil {
		t.Errorf("#364 reviewers = %v, labels = %v; want neither", merged.RequestedReviewers, merged.Labels)
	}

	release := byNumber[334]
	if release.State != StateOpen || release.HeadBranch == "" || release.UpdatedAt.IsZero() {
		t.Errorf("#334 = %+v, want an open pull request with a head branch and an update time", release)
	}
	if !reflect.DeepEqual(release.Labels, []string{"autorelease: pending"}) || release.RequestedReviewers != nil {
		t.Errorf("#334 labels = %v, reviewers = %v; want [autorelease: pending] and nobody asked",
			release.Labels, release.RequestedReviewers)
	}
}

func TestIssueLegsAgreeOnStateAll(t *testing.T) {
	gh, err := parseGHList(fixture(t, "gh_2.100.0_issue_list_all.json"), vincentRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parse gh listing: %v", err)
	}
	rest, err := parseRESTList(fixture(t, "rest_2022-11-28_issues_all.json"), vincentRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parse rest listing: %v", err)
	}
	sortIssues(gh)
	sortIssues(rest)
	if !reflect.DeepEqual(gh, rest) {
		t.Fatalf("the two legs disagree:\n gh   = %+v\n rest = %+v", gh, rest)
	}
	states := map[int]string{}
	for _, issue := range gh {
		states[issue.Number] = issue.State
		if issue.Author != "lezli01" || !reflect.DeepEqual(issue.Labels, []string{"enhancement"}) {
			t.Errorf("#%d author = %q, labels = %v; want lezli01 and [enhancement]",
				issue.Number, issue.Author, issue.Labels)
		}
	}
	want := map[int]string{362: StateClosed, 363: StateOpen, 365: StateOpen}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v, want %v: a closed issue is only listed under StateAll", states, want)
	}
}

// TestGHListArgsCarrySince pins the argv, including the parts of Since that
// must not reach the wire: a zone other than UTC and a sub-second.
func TestGHListArgsCarrySince(t *testing.T) {
	repo := Repo{Owner: "octo", Name: "repo"}
	since := time.Date(2026, 9, 10, 2, 30, 15, 999_000_000, time.FixedZone("CEST", 2*60*60))
	const search = "updated:>=2026-09-10T00:30:15Z sort:updated-desc"
	for _, tc := range []struct {
		name   string
		noun   string
		fields string
		opts   ListOptions
		want   []string
	}{
		{
			"issues, unbounded", "issue", ghFields,
			ListOptions{},
			[]string{"issue", "list", "--repo", "octo/repo", "--state", "open", "--limit", "50", "--json", ghFields},
		},
		{
			"issues since", "issue", ghFields,
			ListOptions{State: StateAll, Limit: 20, Since: since},
			[]string{
				"issue", "list", "--repo", "octo/repo", "--state", "all", "--limit", "20",
				"--search", search, "--json", ghFields,
			},
		},
		{
			"pulls since", "pr", ghPullFields,
			ListOptions{State: "ALL", Since: since},
			[]string{
				"pr", "list", "--repo", "octo/repo", "--state", "all", "--limit", "50",
				"--search", search, "--json", ghPullFields,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ghListArgs(tc.noun, repo, tc.opts, tc.fields); !slices.Equal(got, tc.want) {
				t.Errorf("argv =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// TestGHLegPassesSince drives both listings through fakegh, so the argv
// asserted is the one the leg really executed — and shows the client-side
// bound applies on this leg too, for a gh whose answer ignored the search.
func TestGHLegPassesSince(t *testing.T) {
	c, argv := ghClient(t, "success")
	repo := Repo{Owner: "octo", Name: "repo"}
	opts := ListOptions{State: StateAll, Since: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}

	issues, err := c.List(t.Context(), repo, opts)
	if err != nil || len(issues) == 0 {
		t.Fatalf("List = %d issues, %v; want the corpus", len(issues), err)
	}
	pulls, err := c.ListPulls(t.Context(), repo, opts)
	if err != nil {
		t.Fatalf("ListPulls: %v", err)
	}
	if !slices.ContainsFunc(pulls, func(p PullRequest) bool { return p.Merged }) {
		t.Errorf("listing under StateAll carries no merged pull request: %+v", pulls)
	}
	log := recordedArgv(t, argv)
	const search = "--search updated:>=2020-01-01T00:00:00Z sort:updated-desc"
	for _, want := range []string{
		"issue list --repo octo/repo --state all --limit 50 " + search + " --json " + ghFields,
		"pr list --repo octo/repo --state all --limit 50 " + search + " --json " + ghPullFields,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh was not invoked with %q; log:\n%s", want, log)
		}
	}

	future := ListOptions{State: StateAll, Since: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
	if issues, err := c.List(t.Context(), repo, future); err != nil || len(issues) != 0 {
		t.Errorf("List since 2099 = %d issues, %v; want none: every corpus row is older", len(issues), err)
	}
	if pulls, err := c.ListPulls(t.Context(), repo, future); err != nil || len(pulls) != 0 {
		t.Errorf("ListPulls since 2099 = %d pulls, %v; want none", len(pulls), err)
	}
}

// TestRESTLegPassesSince: issues carry `since` in the query; pulls cannot,
// so they are fetched in update order and the rows older than Since dropped.
func TestRESTLegPassesSince(t *testing.T) {
	var queries []url.Values
	c := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if strings.HasSuffix(r.URL.Path, "/issues") {
			_, _ = w.Write(fixture(t, "rest_2022-11-28_issues_all.json"))
			return
		}
		_, _ = w.Write(fixture(t, "rest_2022-11-28_pulls_all.json"))
	})
	since := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	opts := ListOptions{State: StateAll, Limit: 5, Since: since}

	issues, err := c.List(t.Context(), vincentRepo, opts)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 3 {
		t.Errorf("List = %d issues, want 3 (the page's two pull requests dropped)", len(issues))
	}
	wantIssues := url.Values{
		"state": {"all"}, "since": {"2026-09-11T00:00:00Z"}, "sort": {"updated"},
		"direction": {"desc"}, "per_page": {"5"},
	}
	if !reflect.DeepEqual(queries[0], wantIssues) {
		t.Errorf("issues query = %v, want %v", queries[0], wantIssues)
	}

	pulls, err := c.ListPulls(t.Context(), vincentRepo, opts)
	if err != nil {
		t.Fatalf("ListPulls: %v", err)
	}
	wantPulls := url.Values{
		"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"5"},
	}
	if !reflect.DeepEqual(queries[1], wantPulls) {
		t.Errorf("pulls query = %v, want %v: the collection has no since parameter", queries[1], wantPulls)
	}
	// #360 was last updated 2026-09-10T19:14:57Z, before the bound.
	var numbers []int
	for _, p := range pulls {
		numbers = append(numbers, p.Number)
	}
	if !slices.Equal(numbers, []int{364, 334}) {
		t.Errorf("pulls = %v, want [364 334]: #360 predates since", numbers)
	}

	// Unbounded, the picker's request is untouched.
	if _, err := c.ListPulls(t.Context(), vincentRepo, ListOptions{}); err != nil {
		t.Fatalf("ListPulls unbounded: %v", err)
	}
	if q := queries[2]; q.Get("sort") != "created" || q.Has("since") || q.Get("state") != StateOpen {
		t.Errorf("unbounded pulls query = %v, want state=open sort=created and no since", q)
	}
}

// TestKeepSinceIsInclusiveToTheSecond: the wire carries whole seconds, so a
// sub-second in Since must not drop the row updated in that same second.
func TestKeepSinceIsInclusiveToTheSecond(t *testing.T) {
	at := func(sec int) Issue {
		return Issue{Number: sec, UpdatedAt: time.Date(2026, 9, 11, 10, 48, sec, 0, time.UTC)}
	}
	rows := []Issue{at(28), at(29), at(30)}
	opts := ListOptions{Since: time.Date(2026, 9, 11, 10, 48, 29, 500_000_000, time.UTC)}
	kept := keepSince(slices.Clone(rows), opts.since(), func(i Issue) time.Time { return i.UpdatedAt })
	var numbers []int
	for _, i := range kept {
		numbers = append(numbers, i.Number)
	}
	if !slices.Equal(numbers, []int{29, 30}) {
		t.Errorf("kept %v, want [29 30]", numbers)
	}
	if got := keepSince(slices.Clone(rows), ListOptions{}.since(), func(i Issue) time.Time { return i.UpdatedAt }); len(got) != 3 {
		t.Errorf("a zero Since kept %d of 3 rows", len(got))
	}
}
