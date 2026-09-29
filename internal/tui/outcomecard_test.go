package tui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// summaryAttempt is a succeeded attempt that reported summary.
func summaryAttempt(id int64, step int, name, typ, summary string) apiclient.StepRun {
	return apiclient.StepRun{
		ID: id, StepIndex: step, StepID: name, StepName: name, StepType: typ,
		Attempt: 1, State: stepSucceeded, ResultSummary: summary, TranscriptPath: ptr("/tmp/t.jsonl"),
	}
}

func TestSummaryRunPicksTheResult(t *testing.T) {
	agent := summaryAttempt(1, 0, "implement", "agent", "implemented it")
	command := summaryAttempt(2, 1, "test", "command", "ok 42 tests")
	followUp := summaryAttempt(3, 2, "__follow_up", "agent", "followed up")
	for _, c := range []struct {
		name     string
		steps    []apiclient.StepRun
		status   *string
		wantID   int64
		fallback string
	}{
		{"agent last", []apiclient.StepRun{command, agent}, nil, 1, ""},
		{"command after an agent", []apiclient.StepRun{agent, command}, nil, 1, ""},
		{"a follow-up is the latest agent", []apiclient.StepRun{agent, command, followUp}, nil, 3, ""},
		{"no agent step", []apiclient.StepRun{command}, nil, 2, ""},
		{"every summary empty", []apiclient.StepRun{
			summaryAttempt(2, 1, "test", "command", " "),
		}, ptr("shipped"), 0, "shipped"},
		{"nothing at all", nil, nil, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			task := apiclient.TaskDetail{Task: apiclient.Task{StatusMessage: c.status}, Steps: c.steps}
			run, fallback, ok := summaryRun(task)
			if ok != (c.wantID != 0) || run.ID != c.wantID || fallback != c.fallback {
				t.Fatalf("summaryRun = (%d, %q, %v), want (%d, %q)", run.ID, fallback, ok, c.wantID, c.fallback)
			}
		})
	}
}

// outcomeTask is a finished task with an implement step that reported a
// result and a test step after it.
func outcomeTask(state string) apiclient.TaskDetail {
	implement := summaryAttempt(1, 0, "implement", "agent", "## Done\n\nAdded the **thing**.")
	implement.Attempt = 2
	return apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 9, Title: "outcome task", State: state, CurrentStep: 2, StepTotal: 2,
			BranchName: "vincent/9-outcome", StatusMessage: ptr("all green"),
		},
		Steps: []apiclient.StepRun{implement, summaryAttempt(2, 1, "test", "command", "ok")},
	}
}

func outcomeView(t *testing.T, task apiclient.TaskDetail) *taskView {
	t.Helper()
	v := cardView(t, task)
	v.detail.active = true
	v.syncOutcome()
	return v
}

func TestOutcomeCardRendersWhereItShould(t *testing.T) {
	for _, state := range []string{stateDone, stateAborted, stateArchived} {
		v := outcomeView(t, outcomeTask(state))
		got := ansi.Strip(v.render(100, 40))
		if !strings.Contains(got, "Result  from step 1 implement · attempt 2") {
			t.Errorf("%s: the card is missing or unlabelled:\n%s", state, got)
		}
	}
	for _, state := range []string{
		stateRunning, stateQueued, statePaused, stateAwaitingInput, stateAwaitingGate,
		stateAwaitingChildren, stateBlocked,
	} {
		task := outcomeTask(state)
		if state == stateBlocked {
			task.BlockReason = ptr("check_failed")
		}
		v := outcomeView(t, task)
		if got := ansi.Strip(v.render(100, 40)); strings.Contains(got, "Result  from") || v.showsOutcomeCard() {
			t.Errorf("%s: the outcome card renders:\n%s", state, got)
		}
	}
}

func TestOutcomeCardStacksUnderTheFailureCardOnAborted(t *testing.T) {
	v := outcomeView(t, outcomeTask(stateAborted))
	got := ansi.Strip(v.render(100, 40))
	failure, outcome := strings.Index(got, "■ "), strings.Index(got, "incomplete — what was delivered")
	if failure < 0 || outcome < 0 || failure > outcome {
		t.Fatalf("aborted: want the failure card, then the outcome card:\n%s", got)
	}
	if !strings.Contains(got, "3 output of attempt") {
		t.Errorf("aborted: 3 is no longer the failure card's link:\n%s", got)
	}
	// A short terminal loses the outcome card first; the failure card keeps
	// its collapsed line.
	got = ansi.Strip(v.render(100, cardCollapseRows-1))
	if strings.Contains(got, "incomplete") || !strings.Contains(got, "■ ") {
		t.Errorf("a short aborted overview:\n%s", got)
	}
}

func TestOutcomeResultJumpOpensOutputAtTheEnd(t *testing.T) {
	v := outcomeView(t, outcomeTask(stateAborted))
	v.detail.selectedRun = 2
	var offered bool
	for _, b := range v.liveBindings(bindingsFor(ctxTaskOverview)) {
		offered = offered || b.op == keymap.Result
	}
	if !offered {
		t.Fatal("the Overview does not offer the result jump")
	}
	v.updateKey(synthKey(opKey(keymap.Result)))
	if v.tab != taskTabOutput || v.detail.selectedRun != 1 || !v.detail.following {
		t.Fatalf("jump: tab %v, cursor %d, following %v; want Output on attempt 1 at its end",
			v.tab, v.detail.selectedRun, v.detail.following)
	}
	// `3` on aborted is still the failure card's anchor.
	v.tab = taskTabOverview
	v.updateKey(synthKey("3"))
	if want, _ := cardRun(v.detail.task); v.detail.selectedRun != want.ID {
		t.Fatalf("3 on aborted landed on %d, want the failure card's %d", v.detail.selectedRun, want.ID)
	}
	// No result, no key.
	running := outcomeView(t, outcomeTask(stateRunning))
	for _, b := range running.liveBindings(bindingsFor(ctxTaskOverview)) {
		if b.op == keymap.Result {
			t.Error("the result jump is offered on a running task")
		}
	}
}

func TestOutcomeResultFallsBackToTheStatusDimmed(t *testing.T) {
	task := outcomeTask(stateDone)
	task.Steps = nil
	v := outcomeView(t, task)
	got := ansi.Strip(v.render(100, 40))
	if !strings.Contains(got, "Result  from the task's status message") || !strings.Contains(got, "all green") {
		t.Fatalf("fallback result:\n%s", got)
	}
}

// outcomeServer counts the card's two fetches and answers them.
type outcomeServer struct {
	diffs, commits atomic.Int32
	commitsStatus  int
	commitsBody    string
}

func (s *outcomeServer) start(t *testing.T) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/diff"):
			s.diffs.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sections":[{"diff":"diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n"}]}`))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			s.commits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if s.commitsStatus != 0 {
				w.WriteHeader(s.commitsStatus)
			}
			_, _ = w.Write([]byte(s.commitsBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL, "token")
}

func (s *outcomeServer) run(v *taskView) {
	for _, msg := range flatten(v.syncOutcome()) {
		v.update(msg)
	}
}

func TestOutcomeFetchesOncePerOpen(t *testing.T) {
	srv := &outcomeServer{commitsBody: `[{"sha":"aaaaaaaaaa","subject":"first"},{"sha":"bbbbbbbbbb","subject":"second"}]`}
	v := outcomeView(t, outcomeTask(stateDone))
	v.detail.client = srv.start(t)
	srv.run(v)
	got := ansi.Strip(v.render(100, 40))
	for _, want := range []string{"± 1 file · +2 −1 · branch vincent/9-outcome", "● 2 commits", "bbbbbbb second", "4 diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	// A task event arrives: the snapshot changes, the fetches do not repeat.
	v.detail.task.StatusMessage = ptr("changed")
	srv.run(v)
	srv.run(v)
	if d, c := srv.diffs.Load(), srv.commits.Load(); d != 1 || c != 1 {
		t.Fatalf("fetches: %d diffs, %d commits; want one each", d, c)
	}
	// Leaving the workspace ends the open and asks nothing while away; the
	// next open fetches again.
	v.update(viewDeactivatedMsg{id: viewTask})
	srv.run(v)
	if d := srv.diffs.Load(); d != 1 {
		t.Fatalf("a closed workspace fetched: %d diffs in all", d)
	}
	v.detail.active = true
	srv.run(v)
	if d, c := srv.diffs.Load(), srv.commits.Load(); d != 2 || c != 2 {
		t.Fatalf("a reopen fetched %d diffs and %d commits in all, want 2 each", d, c)
	}
}

func TestOutcomeArchivedFetchesNoDiff(t *testing.T) {
	srv := &outcomeServer{commitsBody: `[{"sha":"aaaaaaaaaa","subject":"first"}]`}
	v := outcomeView(t, outcomeTask(stateArchived))
	v.detail.client = srv.start(t)
	srv.run(v)
	if d := srv.diffs.Load(); d != 0 {
		t.Fatalf("an archived task fetched %d diffs", d)
	}
	got := ansi.Strip(v.render(100, 40))
	for _, want := range []string{"worktree removed — branch vincent/9-outcome kept", "● 1 commit"} {
		if !strings.Contains(got, want) {
			t.Errorf("archived card misses %q:\n%s", want, got)
		}
	}
}

func TestOutcomeArchivedBranchGone(t *testing.T) {
	srv := &outcomeServer{
		commitsStatus: http.StatusConflict,
		commitsBody:   `{"error":{"code":"conflict","message":"branch vincent/9-outcome does not exist"}}`,
	}
	v := outcomeView(t, outcomeTask(stateArchived))
	v.detail.client = srv.start(t)
	srv.run(v)
	got := ansi.Strip(v.render(100, 40))
	if !strings.Contains(got, "branch vincent/9-outcome gone") || strings.Contains(got, "●") {
		t.Fatalf("a deleted branch:\n%s", got)
	}
}

func TestOutcomeOlderDaemonHidesCommits(t *testing.T) {
	v := outcomeView(t, outcomeTask(stateDone))
	v.outcome.commitsAsked, v.outcome.diffAsked = true, true
	v.applyOutcomeCommits(outcomeCommitsMsg{taskID: 9, err: apiclient.ErrCommitsUnsupported})
	if got := ansi.Strip(v.render(100, 40)); strings.Contains(got, "●") || strings.Contains(got, "commits") {
		t.Fatalf("an older daemon's missing route shows a commits line:\n%s", got)
	}
}

func TestOutcomePullLineReadsThePullTab(t *testing.T) {
	task := outcomeTask(stateDone)
	task.GitHubPull = &apiclient.GitHubPullLink{Repo: "octo/repo", Number: 123}
	v := outcomeView(t, task)
	v.detail.client = apiclient.New("http://127.0.0.1:1", "token")
	v.pull = apiclient.GitHubTaskPull{
		Linked: true, Repo: "octo/repo", Number: 123,
		Pull: &apiclient.GitHubPullRequest{Repo: "octo/repo", Number: 123, State: "open"},
	}
	// The card asks the tab's own checks fetch once, and never a pull row.
	if cmd := v.syncOutcome(); cmd == nil || !v.outcome.checksAsked {
		t.Fatal("the card did not ask for the tab's check rollup")
	}
	if cmd := v.syncOutcome(); cmd != nil {
		t.Fatal("the card asked a second time")
	}
	v.applyChecks(taskChecksMsg{taskID: 9, checks: apiclient.GitHubTaskChecks{
		Linked: true, State: "success",
		Runs: []apiclient.GitHubCheckRun{{Name: "a", State: "success"}, {Name: "b", State: "success"}},
	}})
	got := ansi.Strip(v.render(100, 40))
	for _, want := range []string{"⇡ #123 open · checks 2/2 ✓ passing", "7 PR"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
}

func TestOutcomeCardCostAndRollups(t *testing.T) {
	task := outcomeTask(stateDone)
	task.Steps[0].CostUSD = ptr(1.5)
	task.Steps[1].Iteration = 3
	task.Children = &apiclient.ChildrenRollup{Total: 2, ByState: map[string]int{stateDone: 2}, CostUSD: ptr(2.0)}
	v := outcomeView(t, task)
	got := ansi.Strip(v.render(100, 40))
	for _, want := range []string{"tree cost $3.50", "↻ 2 lanes merged · loop ran 3 iterations"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
}

func TestOutcomeCardFitsAndSurvivesBadSummaries(t *testing.T) {
	task := outcomeTask(stateDone)
	task.Steps[0].ResultSummary = "tail kept \xff\xfe mid-rune\n```go\nfunc x() {\n" +
		strings.Repeat("a very long line that has to wrap at eighty columns without spilling ", 4) +
		"\n1\n2\n3\n4\n5\n6\n7\n8"
	v := outcomeView(t, task)
	frame := v.render(80, 40)
	for _, line := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("line is %d wide at 80 columns: %q", w, ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(frame), "    …") {
		t.Errorf("a long summary is not cut:\n%s", ansi.Strip(frame))
	}
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Result  from step 1", "± reading the diff", "$ cost"} {
		if !strings.Contains(ansi.Strip(buf.String()), want) {
			t.Errorf("NO_COLOR loses %q:\n%s", want, buf.String())
		}
	}
}
