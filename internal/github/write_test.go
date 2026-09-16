package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pull-request writes (task 068.4). Each is held to the bar CreatePull
// is: exactly the argv (gh) or method, path and body (REST) it is supposed to
// send, one normalized answer from both legs, named reasons — and, for a
// merge, a preflight that refuses before anything is sent.

var octoRepo = Repo{Owner: "octo", Name: "repo"}

// fakeHead is the head commit cmd/fakegh reports for #412.
const fakeHead = "d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f"

// ghWriteClient is ghClient with the fake's state and stdin records wired, so
// a write-then-read sequence answers the new state.
func ghWriteClient(t *testing.T, scenario string) (*Client, string) {
	t.Helper()
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	return ghClient(t, scenario)
}

// The preflight table, as a pure function: every merge state both legs
// report, in either spelling.
func TestMergeRefusal(t *testing.T) {
	open := PullRequest{Number: 412, State: StateOpen, HeadSHA: "abc"}
	draft := open
	draft.Draft = true
	closed := open
	closed.State = StateClosed
	merged := closed
	merged.Merged = true
	for _, tc := range []struct {
		name    string
		pull    PullRequest
		state   string
		sha     string
		running bool
		want    string
	}{
		{"clean", open, "CLEAN", "abc", false, ""},
		{"clean rest spelling", open, "clean", "abc", false, ""},
		{"unstable is a non-required check failing", open, "UNSTABLE", "abc", false, ""},
		{"has hooks", open, "has_hooks", "abc", false, ""},
		{"unknown is still computing", open, "UNKNOWN", "abc", false, ""},
		{"absent state", open, "", "abc", false, ""},
		{"behind", open, "BEHIND", "abc", false, ReasonBranchBehind},
		{"behind rest spelling", open, "behind", "abc", false, ReasonBranchBehind},
		{"blocked with a running check", open, "BLOCKED", "abc", true, ReasonChecksRunning},
		{"blocked otherwise", open, "blocked", "abc", false, ReasonNotMergeable},
		{"dirty", open, "DIRTY", "abc", false, ReasonNotMergeable},
		{"draft state", open, "draft", "abc", false, ReasonNotMergeable},
		{"draft pull request", draft, "CLEAN", "abc", false, ReasonNotMergeable},
		{"closed", closed, "CLEAN", "abc", false, ReasonNotMergeable},
		{"already merged", merged, "UNKNOWN", "abc", false, ReasonNotMergeable},
		{"head moved", open, "CLEAN", "def", false, ReasonHeadChanged},
		{"head moved beats behind", open, "BEHIND", "def", false, ReasonHeadChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeRefusal(tc.pull, tc.state, tc.sha, tc.running); got != tc.want {
				t.Fatalf("mergeRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// Never --delete-branch, --auto or --admin, and always the pin.
func TestGHMergeArgs(t *testing.T) {
	want := []string{"pr", "merge", "412", "-R", "octo/repo", "--rebase", "--match-head-commit", "abc"}
	if got := ghMergeArgs(octoRepo, 412, MergeOptions{Method: MergeMethodRebase, HeadSHA: "abc"}); !slices.Equal(got, want) {
		t.Fatalf("merge argv is %v, want %v", got, want)
	}
}

func TestMergePullGH(t *testing.T) {
	c, argv := ghWriteClient(t, "success")
	pull, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodSquash, HeadSHA: fakeHead})
	if err != nil {
		t.Fatalf("MergePull: %v", err)
	}
	if !pull.Merged || pull.State != StateClosed || pull.Number != 412 {
		t.Fatalf("merged pull request reads %+v", pull)
	}
	if got := recordedArgv(t, argv); !strings.Contains(got,
		"pr merge 412 -R octo/repo --squash --match-head-commit "+fakeHead+"\n") {
		t.Fatalf("gh argv does not carry the pinned merge:\n%s", got)
	}

	// A second merge is refused by the preflight — the pull request is
	// closed now — and sends nothing.
	_, err = c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodSquash, HeadSHA: fakeHead})
	assertReasonAndNoLeak(t, err, ReasonNotMergeable, "Merged")
	if n := strings.Count(recordedArgv(t, argv), "pr merge"); n != 1 {
		t.Fatalf("a double merge sent %d merges, want 1", n)
	}
}

// Every refusal the preflight can make on the gh leg, and none of them runs
// `gh pr merge`.
func TestMergePullGHPreflightSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		number   int
		sha      string
		want     string
	}{
		{"behind", 412, fakeHead, ReasonBranchBehind},
		{"blocked-running", 412, fakeHead, ReasonChecksRunning},
		{"blocked", 412, fakeHead, ReasonNotMergeable},
		{"dirty", 412, fakeHead, ReasonNotMergeable},
		{"success", 412, "0000000000000000000000000000000000000000", ReasonHeadChanged},
		{"success", 377, fakeHead, ReasonNotMergeable},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.scenario, tc.number), func(t *testing.T) {
			c, argv := ghWriteClient(t, tc.scenario)
			_, err := c.MergePull(t.Context(), octoRepo, tc.number, MergeOptions{Method: MergeMethodMerge, HeadSHA: tc.sha})
			assertReasonAndNoLeak(t, err, tc.want, "mergeStateStatus")
			if got := recordedArgv(t, argv); strings.Contains(got, "pr merge") {
				t.Fatalf("a refused preflight still ran gh pr merge:\n%s", got)
			}
		})
	}
}

// A push landing between the preflight and the send is refused by the pin,
// not merged.
func TestMergePullGHPinRefusesAMovedHead(t *testing.T) {
	c, argv := ghWriteClient(t, "head-moved")
	_, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodMerge, HeadSHA: fakeHead})
	assertReasonAndNoLeak(t, err, ReasonHeadChanged, "Head branch was modified")
	if got := recordedArgv(t, argv); !strings.Contains(got, "--match-head-commit "+fakeHead) {
		t.Fatalf("the send was not pinned:\n%s", got)
	}
}

func TestMergePullRefusesMissingValues(t *testing.T) {
	c := New(Options{Getenv: func(string) string { return "" }, GHPath: filepath.Join(t.TempDir(), "no-gh-here")})
	for _, opts := range []MergeOptions{{HeadSHA: "abc"}, {Method: "fast-forward", HeadSHA: "abc"}, {Method: MergeMethodMerge}} {
		if _, err := c.MergePull(t.Context(), octoRepo, 412, opts); ReasonOf(err) != ReasonBadRequest {
			t.Errorf("MergePull(%+v) reported %q, want %q", opts, ReasonOf(err), ReasonBadRequest)
		}
	}
}

func TestCloseAndReopenPullGH(t *testing.T) {
	c, argv := ghWriteClient(t, "success")
	pull, err := c.ClosePull(t.Context(), octoRepo, 412)
	if err != nil {
		t.Fatalf("ClosePull: %v", err)
	}
	if pull.State != StateClosed || pull.Merged {
		t.Fatalf("closed pull request reads %+v", pull)
	}
	pull, err = c.ReopenPull(t.Context(), octoRepo, 412)
	if err != nil {
		t.Fatalf("ReopenPull: %v", err)
	}
	if pull.State != StateOpen {
		t.Fatalf("reopened pull request reads %+v", pull)
	}
	got := recordedArgv(t, argv)
	for _, want := range []string{"pr close 412 -R octo/repo\n", "pr reopen 412 -R octo/repo\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh argv does not carry %q:\n%s", want, got)
		}
	}
}

// The body travels on stdin, never in argv.
func TestCommentPullGH(t *testing.T) {
	c, argv := ghWriteClient(t, "success")
	stdin := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("FAKEGH_STDIN_FILE", stdin)
	body := "Looks good.\n\nShip it."
	link, err := c.CommentPull(t.Context(), octoRepo, 412, body)
	if err != nil {
		t.Fatalf("CommentPull: %v", err)
	}
	if link != "https://github.com/octo/repo/pull/412#issuecomment-1" {
		t.Errorf("comment URL is %q", link)
	}
	got := recordedArgv(t, argv)
	if !strings.Contains(got, "pr comment 412 -R octo/repo --body-file -\n") || strings.Contains(got, "Ship it") {
		t.Fatalf("gh argv is wrong, or carries the body:\n%s", got)
	}
	if b, _ := os.ReadFile(stdin); string(b) != body {
		t.Fatalf("stdin carried %q, want %q", b, body)
	}
}

func TestCommentPullRefusesEmptyBody(t *testing.T) {
	c := New(Options{Getenv: func(string) string { return "" }, GHPath: filepath.Join(t.TempDir(), "no-gh-here")})
	if _, err := c.CommentPull(t.Context(), octoRepo, 412, " \n"); ReasonOf(err) != ReasonBadRequest {
		t.Fatalf("an empty comment reported %q, want %q", ReasonOf(err), ReasonBadRequest)
	}
}

// Run 5150 backs #412's failed `build` row in the fake; 9999 backs nothing.
func TestRerunFailedJobsGH(t *testing.T) {
	c, argv := ghWriteClient(t, "success")
	if err := c.RerunFailedJobs(t.Context(), octoRepo, 412, 5150); err != nil {
		t.Fatalf("RerunFailedJobs: %v", err)
	}
	if got := recordedArgv(t, argv); !strings.Contains(got, "run rerun 5150 --failed -R octo/repo\n") {
		t.Fatalf("gh argv does not carry the re-run:\n%s", got)
	}
	err := c.RerunFailedJobs(t.Context(), octoRepo, 412, 9999)
	assertReasonAndNoLeak(t, err, ReasonBadRequest, "9999")
	if got := recordedArgv(t, argv); strings.Contains(got, "run rerun 9999") {
		t.Fatalf("a run outside the rollup was re-run:\n%s", got)
	}
}

// Only a failed, Actions-backed row makes a run re-runnable.
func TestRerunnable(t *testing.T) {
	rollup := CheckRollup{Runs: []CheckRun{
		{Name: "green", State: CheckSuccess, RunID: 1},
		{Name: "running", State: CheckInProgress, RunID: 2},
		{Name: "third-party", State: CheckFailure},
		{Name: "red", State: CheckTimedOut, RunID: 3},
	}}
	for id, want := range map[int64]bool{1: false, 2: false, 0: false, 3: true, 4: false} {
		if got := rerunnable(rollup, id); got != want {
			t.Errorf("rerunnable(%d) = %v, want %v", id, got, want)
		}
	}
}

// A 403 on every write is no_write_scope under a credential that reads fine —
// the preflights pass, the writes do not.
func TestWritesForbiddenAreNoWriteScopeGH(t *testing.T) {
	c, _ := ghWriteClient(t, "read-only")
	leak := "not accessible"
	_, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodMerge, HeadSHA: fakeHead})
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.ClosePull(t.Context(), octoRepo, 412)
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.ReopenPull(t.Context(), octoRepo, 412)
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.CommentPull(t.Context(), octoRepo, 412, "hi")
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	err = c.RerunFailedJobs(t.Context(), octoRepo, 412, 5150)
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.CreatePull(t.Context(), octoRepo, CreateOptions{Base: "main", Head: "topic", Title: "T"})
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)

	// And the same 403 on a read is still forbidden.
	f, _ := ghClient(t, "forbidden")
	_, err = f.GetPull(t.Context(), octoRepo, 412)
	assertReasonAndNoLeak(t, err, ReasonForbidden, leak)
}

// restWriteFake is a REST API for one pull request, #412, that records every
// non-GET it is sent.
type restWriteFake struct {
	t *testing.T

	mu         sync.Mutex
	mergeState string
	running    bool
	state      string
	merged     bool
	// forbidWrites answers every non-GET 403; writeStatus answers the merge
	// with that status instead of 200.
	forbidWrites bool
	writeStatus  int
	writes       []string
	bodies       []map[string]any
}

func (f *restWriteFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, body)
		if f.forbidWrites {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/repo/pulls/412":
		_, _ = w.Write(f.pullJSON())
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/commits/abc/check-runs"):
		runs := []map[string]any{{
			"name": "build", "status": "completed", "conclusion": "failure",
			"html_url": "https://github.com/octo/repo/actions/runs/77/job/1",
		}}
		if f.running {
			runs = append(runs, map[string]any{
				"name": "test", "status": "in_progress", "conclusion": nil,
				"html_url": "https://github.com/octo/repo/actions/runs/78/job/2",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": runs})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/commits/abc/status"):
		_, _ = w.Write([]byte(`{"statuses":[]}`))
	case r.Method == http.MethodPut && r.URL.Path == "/repos/octo/repo/pulls/412/merge":
		if f.writeStatus != 0 {
			w.WriteHeader(f.writeStatus)
			_, _ = w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))
			return
		}
		f.merged, f.state = true, "closed"
		_, _ = w.Write([]byte(`{"merged":true,"message":"Pull Request successfully merged"}`))
	case r.Method == http.MethodPatch && r.URL.Path == "/repos/octo/repo/pulls/412":
		f.state = fmt.Sprint(f.bodies[len(f.bodies)-1]["state"])
		_, _ = w.Write(f.pullJSON())
	case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/repo/issues/412/comments":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"html_url":"https://github.com/octo/repo/pull/412#issuecomment-9"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/repo/actions/runs/77/rerun-failed-jobs":
		w.WriteHeader(http.StatusCreated)
	default:
		f.t.Errorf("unexpected REST call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *restWriteFake) pullJSON() []byte {
	var mergedAt any
	if f.merged {
		mergedAt = "2026-09-15T10:00:00Z"
	}
	b, _ := json.Marshal(map[string]any{
		"number": 412, "title": "Add a thing", "html_url": "https://github.com/octo/repo/pull/412",
		"state": f.state, "draft": false, "merged_at": mergedAt,
		"head":            map[string]any{"ref": "topic", "sha": "abc", "repo": map[string]any{"full_name": "octo/repo"}},
		"base":            map[string]any{"ref": "main"},
		"user":            map[string]any{"login": "octocat"},
		"mergeable_state": f.mergeState,
	})
	return b
}

func (f *restWriteFake) recorded() ([]string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes), slices.Clone(f.bodies)
}

func restWriteClient(t *testing.T, f *restWriteFake) *Client {
	t.Helper()
	f.t = t
	if f.state == "" {
		f.state = "open"
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return New(Options{
		BaseURL: srv.URL,
		Getenv:  func(string) string { return "tok" },
		GHPath:  filepath.Join(t.TempDir(), "no-gh-here"),
		Now:     func() time.Time { return fixtureNow },
	})
}

func TestMergePullREST(t *testing.T) {
	f := &restWriteFake{mergeState: "clean"}
	c := restWriteClient(t, f)
	pull, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodRebase, HeadSHA: "abc"})
	if err != nil {
		t.Fatalf("MergePull: %v", err)
	}
	if !pull.Merged || pull.State != StateClosed {
		t.Fatalf("merged pull request reads %+v", pull)
	}
	writes, bodies := f.recorded()
	if !slices.Equal(writes, []string{"PUT /repos/octo/repo/pulls/412/merge"}) {
		t.Fatalf("writes are %v", writes)
	}
	if bodies[0]["merge_method"] != "rebase" || bodies[0]["sha"] != "abc" {
		t.Fatalf("merge body is %v", bodies[0])
	}
}

func TestMergePullRESTPreflightSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		running bool
		sha     string
		want    string
	}{
		{"behind", "behind", false, "abc", ReasonBranchBehind},
		{"blocked running", "blocked", true, "abc", ReasonChecksRunning},
		{"blocked", "blocked", false, "abc", ReasonNotMergeable},
		{"dirty", "dirty", false, "abc", ReasonNotMergeable},
		{"draft", "draft", false, "abc", ReasonNotMergeable},
		{"head moved", "clean", false, "def", ReasonHeadChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &restWriteFake{mergeState: tc.state, running: tc.running}
			c := restWriteClient(t, f)
			_, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodMerge, HeadSHA: tc.sha})
			assertReasonAndNoLeak(t, err, tc.want, "mergeable_state")
			if writes, _ := f.recorded(); len(writes) != 0 {
				t.Fatalf("a refused preflight still sent %v", writes)
			}
		})
	}
}

// GitHub's own refusal after the preflight passed: 405 is not mergeable, 409
// is the pinned sha no longer being the head.
func TestMergePullRESTRefusalStatuses(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusMethodNotAllowed: ReasonNotMergeable,
		http.StatusConflict:         ReasonHeadChanged,
	} {
		f := &restWriteFake{mergeState: "unknown", writeStatus: status}
		c := restWriteClient(t, f)
		_, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodMerge, HeadSHA: "abc"})
		assertReasonAndNoLeak(t, err, want, "successfully")
	}
}

func TestCloseReopenCommentRerunREST(t *testing.T) {
	f := &restWriteFake{mergeState: "clean"}
	c := restWriteClient(t, f)
	pull, err := c.ClosePull(t.Context(), octoRepo, 412)
	if err != nil || pull.State != StateClosed {
		t.Fatalf("ClosePull = %+v, %v", pull, err)
	}
	pull, err = c.ReopenPull(t.Context(), octoRepo, 412)
	if err != nil || pull.State != StateOpen {
		t.Fatalf("ReopenPull = %+v, %v", pull, err)
	}
	link, err := c.CommentPull(t.Context(), octoRepo, 412, "Ship it.")
	if err != nil || link != "https://github.com/octo/repo/pull/412#issuecomment-9" {
		t.Fatalf("CommentPull = %q, %v", link, err)
	}
	if err := c.RerunFailedJobs(t.Context(), octoRepo, 412, 77); err != nil {
		t.Fatalf("RerunFailedJobs: %v", err)
	}
	// Run 78 is not in this rollup at all (nothing is running), so it is
	// refused before a request is made.
	assertReasonAndNoLeak(t, c.RerunFailedJobs(t.Context(), octoRepo, 412, 78), ReasonBadRequest, "78")

	writes, bodies := f.recorded()
	want := []string{
		"PATCH /repos/octo/repo/pulls/412",
		"PATCH /repos/octo/repo/pulls/412",
		"POST /repos/octo/repo/issues/412/comments",
		"POST /repos/octo/repo/actions/runs/77/rerun-failed-jobs",
	}
	if !slices.Equal(writes, want) {
		t.Fatalf("writes are %v, want %v", writes, want)
	}
	if bodies[0]["state"] != "closed" || bodies[1]["state"] != "open" || bodies[2]["body"] != "Ship it." {
		t.Fatalf("write bodies are %v", bodies)
	}
}

func TestWritesForbiddenAreNoWriteScopeREST(t *testing.T) {
	f := &restWriteFake{mergeState: "clean", forbidWrites: true}
	c := restWriteClient(t, f)
	leak := "not accessible"
	_, err := c.MergePull(t.Context(), octoRepo, 412, MergeOptions{Method: MergeMethodMerge, HeadSHA: "abc"})
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.ClosePull(t.Context(), octoRepo, 412)
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.ReopenPull(t.Context(), octoRepo, 412)
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	_, err = c.CommentPull(t.Context(), octoRepo, 412, "hi")
	assertReasonAndNoLeak(t, err, ReasonNoWriteScope, leak)
	assertReasonAndNoLeak(t, c.RerunFailedJobs(t.Context(), octoRepo, 412, 77), ReasonNoWriteScope, leak)
}
