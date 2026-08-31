package github

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The fixtures are captured, per the adapter-parsing convention the issue
// fixtures already follow: `gh pr view --json ...statusCheckRollup` on one
// side, `/commits/{sha}/check-runs` plus `/commits/{sha}/status` on the
// other, for the *same* pull request.

var checksNow = time.Date(2026, 8, 31, 7, 30, 0, 0, time.UTC)

const checksRef = "3f1c8a0d9e2b4c6a8d0f1e2b3c4d5e6f70819a2b"

// TestBothLegsAgreeOnChecks is the invariant this package exists to hold: a
// client cannot tell which leg answered. The fixture deliberately carries a
// legacy commit status beside its check runs, which is where the two shapes
// differ most — `gh` folds it into `statusCheckRollup` and the REST leg has
// to fetch it from a second endpoint.
func TestBothLegsAgreeOnChecks(t *testing.T) {
	ghRollup, err := parseGHChecks(fixture(t, "gh_2.98.0_pr_view_checks.json"), checksNow)
	if err != nil {
		t.Fatalf("parse gh checks: %v", err)
	}
	restRollup, err := parseRESTChecks(
		fixture(t, "rest_pr_check_runs.json"),
		fixture(t, "rest_pr_status.json"),
		checksRef, checksNow)
	if err != nil {
		t.Fatalf("parse rest checks: %v", err)
	}
	if !reflect.DeepEqual(ghRollup, restRollup) {
		t.Fatalf("the two legs disagree:\n gh: %+v\nrest: %+v", ghRollup, restRollup)
	}
	if ghRollup.Ref != checksRef {
		t.Fatalf("rollup ref %q, want %q", ghRollup.Ref, checksRef)
	}
	if ghRollup.State != CheckFailure {
		t.Fatalf("rollup state %q, want failure — one row failed", ghRollup.State)
	}
	if ghRollup.FetchedAt != checksNow {
		t.Fatalf("rollup fetched at %v, want %v", ghRollup.FetchedAt, checksNow)
	}
}

// Re-run is offered only where an Actions run backs the row (decision 3), so
// the provenance has to survive both legs identically.
func TestCheckActionsProvenance(t *testing.T) {
	rollup, err := parseGHChecks(fixture(t, "gh_2.98.0_pr_view_checks.json"), checksNow)
	if err != nil {
		t.Fatalf("parse gh checks: %v", err)
	}
	byName := map[string]CheckRun{}
	for _, run := range rollup.Runs {
		byName[run.Name] = run
	}
	for name, want := range map[string]int64{
		"test (ubuntu-latest)":   9911223344,
		"gates (windows-latest)": 9911223344,
		// A third-party check run owns its own page; re-run has no meaning.
		"license/cla": 0,
		// A legacy commit status has no run behind it at all.
		"ci/legacy-builder": 0,
	} {
		got := byName[name]
		if got.RunID != want {
			t.Errorf("%s reports run %d, want %d", name, got.RunID, want)
		}
		if got.Actions() != (want > 0) {
			t.Errorf("%s reports Actions=%v, want %v", name, got.Actions(), want > 0)
		}
	}
}

// A URL that merely contains GitHub's Actions path must not hand vincent a
// run id to re-run on github.com.
func TestActionsRunIDRefusesForeignHosts(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want int64
	}{
		{"https://github.com/o/r/actions/runs/42/job/7", 42},
		{"https://github.com/o/r/actions/runs/42", 42},
		{"https://www.github.com/o/r/actions/runs/42/job/7", 42},
		{"https://evil.example.test/o/r/actions/runs/42/job/7", 0},
		{"https://github.com/o/r/checks/42", 0},
		{"", 0},
		{"not a url at all", 0},
	} {
		if got := actionsRunID(tc.url); got != tc.want {
			t.Errorf("actionsRunID(%q) = %d, want %d", tc.url, got, tc.want)
		}
	}
}

// The rollup is ordered here rather than trusted from the wire, so the order
// is not a difference a user can see: unfinished first, then failed.
func TestChecksAreOrderedUnfinishedThenFailed(t *testing.T) {
	rollup, err := parseGHChecks(fixture(t, "gh_2.98.0_pr_view_checks.json"), checksNow)
	if err != nil {
		t.Fatalf("parse gh checks: %v", err)
	}
	var names []string
	for _, run := range rollup.Runs {
		names = append(names, run.Name)
	}
	want := []string{"gates (windows-latest)", "ci/legacy-builder", "license/cla", "test (ubuntu-latest)"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("check order %v, want %v", names, want)
	}
}

// Failure wins over running: a pull request with one failed job and five
// still going is failed, and reporting it as in progress would hide the only
// fact worth acting on.
func TestRollupState(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs []CheckRun
		want string
	}{
		{"none", nil, ""},
		{"all green", []CheckRun{{State: CheckSuccess}, {State: CheckSkipped}}, CheckSuccess},
		{"one running", []CheckRun{{State: CheckSuccess}, {State: CheckQueued}}, CheckInProgress},
		{"failure beats running", []CheckRun{{State: CheckQueued}, {State: CheckFailure}}, CheckFailure},
		{"neutral is not a failure", []CheckRun{{State: CheckNeutral}}, CheckSuccess},
	} {
		if got := rollupState(tc.runs); got != tc.want {
			t.Errorf("%s: rollup state %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An unknown conclusion is a failure, never a silent green: GitHub adds
// conclusions, and a new red one rendering as passing is the one wrong answer
// here.
func TestNormalizeCheckState(t *testing.T) {
	for _, tc := range []struct{ status, conclusion, want string }{
		{"COMPLETED", "SUCCESS", CheckSuccess},
		{"completed", "timed_out", CheckTimedOut},
		{"completed", "", CheckNeutral},
		{"completed", "something_new", CheckFailure},
		{"QUEUED", "", CheckQueued},
		{"waiting", "", CheckQueued},
		{"IN_PROGRESS", "", CheckInProgress},
		{"", "", CheckQueued},
	} {
		if got := normalizeCheckState(tc.status, tc.conclusion); got != tc.want {
			t.Errorf("normalizeCheckState(%q, %q) = %q, want %q", tc.status, tc.conclusion, got, tc.want)
		}
	}
	// A legacy status's `error` folds onto failure rather than becoming a
	// seventh state only one leg can produce.
	for _, tc := range []struct{ state, want string }{
		{"success", CheckSuccess},
		{"failure", CheckFailure},
		{"error", CheckFailure},
		{"pending", CheckInProgress},
	} {
		if got := normalizeStatusState(tc.state); got != tc.want {
			t.Errorf("normalizeStatusState(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

// The REST leg asks about a commit, so a pull request with no head SHA is a
// bad_response rather than a call against an unnamed ref.
func TestChecksRefusesAPullRequestWithNoHead(t *testing.T) {
	// A `gh` that cannot be executed forces the token leg, which is the one
	// that needs a commit: this must not depend on whether the machine
	// running the test happens to have `gh` logged in.
	c := New(Options{
		GHPath:  filepath.Join(t.TempDir(), "gh-that-is-not-there"),
		Getenv:  func(string) string { return "t0ken" },
		BaseURL: "http://127.0.0.1:1",
	})
	_, err := c.Checks(t.Context(), Repo{Owner: "octo", Name: "api"}, PullRequest{Number: 7})
	if got := ReasonOf(err); got != ReasonBadResponse {
		t.Fatalf("reason %q, want %q", got, ReasonBadResponse)
	}
}
