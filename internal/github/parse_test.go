package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The fixtures under testdata/ are **captured**, per the adapter-parsing
// convention: `gh_2.98.0_*.json` is what `gh 2.98.0 --json <ghFields>`
// printed, and `rest_*.json` is what `GET /repos/{o}/{r}/issues` answered for
// the same repositories. Nothing here is hand-written, which is the point —
// the two legs are held to shapes GitHub actually produces rather than to
// shapes this package hoped for.
//
// `rest_issues.json` is a listing that contains pull requests, because the
// REST `/issues` collection does and `gh issue list` does not. That
// difference is the one thing that could make the two legs disagree about
// what an issue *is*, so it has its own fixture and its own test.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

var (
	fixtureNow  = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	vincentRepo = Repo{Owner: "lezli01", Name: "vincent"}
	goRepo      = Repo{Owner: "golang", Name: "go"}
)

func TestGHListParsesCapturedOutput(t *testing.T) {
	issues, err := parseGHList(fixture(t, "gh_2.98.0_issue_list.json"), vincentRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parseGHList: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("the captured listing parsed to no issues")
	}
	first := issues[0]
	if first.Number != 200 {
		t.Errorf("first issue number = %d, want 200", first.Number)
	}
	if first.State != StateOpen {
		t.Errorf("state = %q, want the normalized %q (gh reports OPEN)", first.State, StateOpen)
	}
	if first.Repo != "lezli01/vincent" {
		t.Errorf("repo = %q, want lezli01/vincent", first.Repo)
	}
	if first.Author == "" {
		t.Error("author did not parse out of gh's nested author object")
	}
	if !reflect.DeepEqual(first.Labels, []string{"enhancement"}) {
		t.Errorf("labels = %v, want [enhancement] as a real list", first.Labels)
	}
	if first.URL == "" || first.Title == "" || first.Body == "" {
		t.Errorf("issue is missing url/title/body: %+v", first)
	}
	if !first.FetchedAt.Equal(fixtureNow) {
		t.Errorf("fetched_at = %v, want the fetch instant %v", first.FetchedAt, fixtureNow)
	}
}

func TestGHViewParsesCapturedOutput(t *testing.T) {
	issue, err := parseGHIssue(fixture(t, "gh_2.98.0_issue_view.json"), vincentRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parseGHIssue: %v", err)
	}
	if issue.Number != 200 {
		t.Errorf("number = %d, want 200", issue.Number)
	}
	if issue.State != StateOpen {
		t.Errorf("state = %q, want %q", issue.State, StateOpen)
	}
}

// TestBothLegsAgree is decision 1's contract: `gh` and the REST API are
// alternatives, not variants. The same issue read through each leg must
// normalize to the same value, or a client could tell which credential the
// daemon happened to have.
func TestBothLegsAgree(t *testing.T) {
	viaGH, err := parseGHIssue(fixture(t, "gh_2.98.0_issue_metadata.json"), goRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parseGHIssue: %v", err)
	}
	viaREST, err := parseRESTIssue(fixture(t, "rest_issue_same.json"), goRepo, viaGH.Number, fixtureNow)
	if err != nil {
		t.Fatalf("parseRESTIssue: %v", err)
	}
	if !reflect.DeepEqual(viaGH, viaREST) {
		t.Errorf("the two legs normalized the same issue differently:\n gh:   %+v\n rest: %+v",
			viaGH, viaREST)
	}
	// And the fixture is worth having only if it exercises the metadata the
	// prefill maps, so assert it does.
	if viaGH.Assignee == "" || viaGH.MilestoneNumber == 0 || viaGH.Milestone == "" ||
		len(viaGH.Labels) == 0 {
		t.Fatalf("the agreement fixture carries no assignee/milestone/labels to compare: %+v", viaGH)
	}
}

// TestRESTListDropsPullRequests: the REST `/issues` collection includes pull
// requests and `gh issue list` does not. Filtering them is what makes the two
// legs return the same list — and PRs are decision 10's explicit non-goal.
func TestRESTListDropsPullRequests(t *testing.T) {
	raw := fixture(t, "rest_issues.json")
	issues, err := parseRESTList(raw, vincentRepo, fixtureNow)
	if err != nil {
		t.Fatalf("parseRESTList: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("the captured listing parsed to no issues at all")
	}
	// The fixture is only meaningful if it actually contains pull requests.
	var rows []restIssue
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	prs := 0
	for _, r := range rows {
		if r.PullRequest != nil {
			prs++
		}
	}
	if prs == 0 {
		t.Fatal("rest_issues.json carries no pull requests, so it cannot prove they are dropped")
	}
	if len(issues) != len(rows)-prs {
		t.Errorf("parsed %d rows from %d entries of which %d are pull requests",
			len(issues), len(rows), prs)
	}
	for _, issue := range issues {
		if issue.URL == "" {
			t.Errorf("issue #%d has no url; the REST leg reads html_url", issue.Number)
		}
	}
}

// TestSortNewestFirst: the picker opens on "the recent issues", and the order
// is applied after parsing rather than trusted from either leg (issue.go).
func TestSortNewestFirst(t *testing.T) {
	issues := []Issue{
		{Number: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Number: 9, CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		{Number: 4, CreatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	sortIssues(issues)
	if got := []int{issues[0].Number, issues[1].Number, issues[2].Number}; !reflect.DeepEqual(
		got, []int{9, 4, 1}) {
		t.Errorf("order = %v, want newest first [9 4 1]", got)
	}
}
