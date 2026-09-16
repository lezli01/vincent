package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The Pull Request tab's confirmed writes (task 068.4, issue #387).

// daemonRequest is one request the fake daemon saw.
type daemonRequest struct {
	method, path, body string
}

// daemonLog records every request that reaches the fake daemon. The daemon
// is what writes to GitHub, so "no request reached it" is the property that
// makes "a mistyped key must not merge a pull request" true.
type daemonLog struct {
	mu   sync.Mutex
	reqs []daemonRequest
}

func (l *daemonLog) all() []daemonRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]daemonRequest(nil), l.reqs...)
}

func (l *daemonLog) posts() []daemonRequest {
	var out []daemonRequest
	for _, r := range l.all() {
		if r.method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

// writeTabFixture is pullTabFixture pointed at a recording daemon, with the
// first check failed so every write has somewhere to apply.
func writeTabFixture(t *testing.T) (*taskView, *daemonLog) {
	t.Helper()
	log := &daemonLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.reqs = append(log.reqs, daemonRequest{r.Method, r.URL.Path, string(body)})
		log.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	v := pullTabFixture(t)
	v.detail.client = apiclient.New(srv.URL, "test")
	v.pullTab.checks.Runs[0].State = "failure"
	return v, log
}

// drainAll runs a command and everything it batches, the way the runtime
// would. A command still running after a moment is a timer — a cursor blink —
// and is left to itself: a request to a local server has long since landed.
func drainAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				drainAll(c)
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// pressPull sends keys through the workspace's own routing and drains whatever
// each returns.
func pressPull(t *testing.T, v *taskView, keys ...string) {
	t.Helper()
	for _, k := range keys {
		drainAll(v.updateKey(registryKey(t, k)))
	}
}

func withPullState(v *taskView, state string, draft, merged bool) {
	p := *v.pull.Pull
	p.State, p.Draft, p.Merged = state, draft, merged
	pull := v.pull
	pull.Pull = &p
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
}

// TestPullWriteRejectedConfirmationSendsNothing is the one that matters: for
// every write, open its confirmation with a real key press, turn it down, and
// not one request of any kind reaches the daemon.
func TestPullWriteRejectedConfirmationSendsNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*taskView)
		keys  []string
	}{
		{name: "merge n", keys: []string{"m", "n"}},
		{name: "merge esc", keys: []string{"m", "esc"}},
		{name: "merge y before a method", keys: []string{"m", "y", "y", "esc"}},
		{name: "merge stray keys and enter", keys: []string{"m", "right", "q", "enter", "1", "c", "esc"}},
		{name: "close n", keys: []string{"X", "n"}},
		{name: "close esc", keys: []string{"X", "esc"}},
		{name: "reopen n", setup: func(v *taskView) { withPullState(v, "closed", false, false) }, keys: []string{"X", "n"}},
		{name: "reopen esc", setup: func(v *taskView) { withPullState(v, "closed", false, false) }, keys: []string{"X", "esc"}},
		{name: "comment esc after typing", keys: []string{"i", "h", "i", "enter", "esc", "esc"}},
		{name: "comment esc at once", keys: []string{"i", "esc", "esc"}},
		{name: "re-run n", keys: []string{"ctrl+r", "n"}},
		{name: "re-run esc", keys: []string{"ctrl+r", "esc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, log := writeTabFixture(t)
			if tc.setup != nil {
				tc.setup(v)
			}
			pressPull(t, v, tc.keys[0])
			if !v.capturesInput() {
				t.Fatalf("%s opened no confirmation", tc.keys[0])
			}
			pressPull(t, v, tc.keys[1:]...)
			if got := log.all(); len(got) != 0 {
				t.Fatalf("a turned-down confirmation reached the daemon: %+v", got)
			}
			if v.capturesInput() || v.pullMerge != nil || v.pullComment != nil || v.pullTab.confirm != nil {
				t.Fatal("the confirmation is still up after it was turned down")
			}
			if v.tab != taskTabPull {
				t.Fatalf("a key spent on the confirmation moved the workspace to %v", v.tab)
			}
		})
	}
}

// A key that cannot apply is absent from the hint line and the footer, and
// pressing it opens nothing — not present and refusing (task 068 decision 3,
// extended to all four writes).
func TestPullWriteKeysAreAbsentWhereTheyCannotApply(t *testing.T) {
	hint := func(v *taskView) string { return v.pullHintLine() }
	footer := func(v *taskView) string {
		var hints []string
		for _, b := range v.liveBindings(bindingsFor(ctxTaskPull)) {
			hints = append(hints, b.hint)
		}
		return strings.Join(hints, " · ")
	}
	assertAbsent := func(t *testing.T, v *taskView, key, word string) {
		t.Helper()
		for name, line := range map[string]string{"hint line": hint(v), "footer": footer(v)} {
			if strings.Contains(line, word) {
				t.Errorf("the %s names %q: %s", name, word, line)
			}
		}
		pressPull(t, v, key)
		if v.capturesInput() {
			t.Errorf("%s opened a confirmation where it cannot apply", key)
		}
	}
	assertPresent := func(t *testing.T, v *taskView, word string) {
		t.Helper()
		if !strings.Contains(hint(v), word) || !strings.Contains(footer(v), word) {
			t.Errorf("%q is not offered: hint %q, footer %q", word, hint(v), footer(v))
		}
	}

	t.Run("re-run follows the selected row", func(t *testing.T) {
		v, log := writeTabFixture(t)
		v.pullTab.checks.Runs = append(v.pullTab.checks.Runs,
			apiclient.GitHubCheckRun{Name: "ci/legacy", State: "failure", URL: "https://legacy.example/9"})
		assertPresent(t, v, "ctrl+r re-run")
		for i, why := range map[int]string{1: "a passing Actions row", 2: "a third-party check", 3: "a legacy commit status"} {
			v.pullTab.cursor = i
			t.Run(why, func(t *testing.T) { assertAbsent(t, v, "ctrl+r", "ctrl+r") })
		}
		v.pullTab.cursor = 0
		pressPull(t, v, "ctrl+r")
		if v.pullTab.confirm == nil {
			t.Fatal("ctrl+r on a failed Actions row asked nothing")
		}
		pressPull(t, v, "n")
		if len(log.all()) != 0 {
			t.Fatalf("the absent keys reached the daemon: %+v", log.all())
		}
	})

	for _, tc := range []struct {
		state         string
		draft, merged bool
	}{{"open", true, false}, {"closed", false, false}, {"closed", false, true}} {
		t.Run("merge is absent on "+pullStateWord(apiclient.GitHubPullRequest{State: tc.state, Draft: tc.draft, Merged: tc.merged}), func(t *testing.T) {
			v, _ := writeTabFixture(t)
			withPullState(v, tc.state, tc.draft, tc.merged)
			assertAbsent(t, v, "m", "m merge")
		})
	}

	t.Run("X reads close, reopen, or nothing", func(t *testing.T) {
		v, _ := writeTabFixture(t)
		assertPresent(t, v, "X close")
		withPullState(v, "open", true, false)
		assertPresent(t, v, "X close")
		withPullState(v, "closed", false, false)
		assertPresent(t, v, "X reopen")
		withPullState(v, "closed", false, true)
		assertAbsent(t, v, "X", "X ")
		assertPresent(t, v, "i comment")
	})

	t.Run("a reason instead of a pull request hides all four", func(t *testing.T) {
		v, log := writeTabFixture(t)
		pull := linkedPull()
		pull.Pull, pull.Reason = nil, "rate_limited"
		v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
		for key, word := range map[string]string{"m": "m merge", "X": "X ", "i": "i comment", "ctrl+r": "ctrl+r"} {
			assertAbsent(t, v, key, word)
		}
		if len(log.all()) != 0 {
			t.Fatalf("the absent keys reached the daemon: %+v", log.all())
		}
	})
}

// An accepted confirmation sends exactly one request carrying exactly what
// was confirmed, and a second press while it is in flight sends nothing.
func TestPullWriteAcceptedConfirmationSendsWhatWasConfirmed(t *testing.T) {
	decode := func(t *testing.T, body string) map[string]any {
		t.Helper()
		out := map[string]any{}
		if body != "" {
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("body %q: %v", body, err)
			}
		}
		return out
	}
	one := func(t *testing.T, log *daemonLog, path string) map[string]any {
		t.Helper()
		posts := log.posts()
		if len(posts) != 1 || posts[0].path != path {
			t.Fatalf("the daemon saw %+v, want one POST %s", posts, path)
		}
		return decode(t, posts[0].body)
	}

	t.Run("merge", func(t *testing.T) {
		v, log := writeTabFixture(t)
		pressPull(t, v, "m", "right", "right", "y", "y")
		body := one(t, log, "/v1/tasks/4/github/pull/merge")
		if body["method"] != "squash" || body["head_sha"] != "0b1d2e3f4a5b6c7d8e9f" {
			t.Fatalf("merge sent %v, want squash at the displayed head", body)
		}
		if out := ansi.Strip(v.render(120, 40)); !strings.Contains(out, "0b1d2e3f4a5b") {
			t.Fatalf("the popup does not show the head it pins:\n%s", out)
		}
	})
	t.Run("close", func(t *testing.T) {
		v, log := writeTabFixture(t)
		pressPull(t, v, "X")
		if !strings.Contains(v.pullTab.confirm.text, "close octo/api#41 without merging?") {
			t.Fatalf("the prompt reads %q", v.pullTab.confirm.text)
		}
		pressPull(t, v, "y", "X", "y")
		one(t, log, "/v1/tasks/4/github/pull/close")
	})
	t.Run("reopen", func(t *testing.T) {
		v, log := writeTabFixture(t)
		withPullState(v, "closed", false, false)
		pressPull(t, v, "X", "y")
		one(t, log, "/v1/tasks/4/github/pull/reopen")
	})
	t.Run("re-run", func(t *testing.T) {
		v, log := writeTabFixture(t)
		v.pullTab.checks.Runs[1].State = "timed_out"
		pressPull(t, v, "ctrl+r")
		if want := "re-run the failed jobs of Actions run 77 (build, lint)? (y/n)"; v.pullTab.confirm.text != want {
			t.Fatalf("the prompt reads %q, want %q", v.pullTab.confirm.text, want)
		}
		pressPull(t, v, "y", "ctrl+r", "y")
		if body := one(t, log, "/v1/tasks/4/github/pull/checks/rerun"); body["run_id"] != float64(77) {
			t.Fatalf("re-run sent %v, want run 77", body)
		}
	})
	t.Run("comment", func(t *testing.T) {
		v, log := writeTabFixture(t)
		pressPull(t, v, "i")
		drainAll(v.paste("Ship it.\n  Twice."))
		pressPull(t, v, "ctrl+s", "ctrl+s")
		if body := one(t, log, "/v1/tasks/4/github/pull/comment"); body["body"] != "Ship it.\n  Twice." {
			t.Fatalf("comment sent %q, want the body verbatim", body["body"])
		}
	})
	t.Run("a blank comment is refused in the client", func(t *testing.T) {
		v, log := writeTabFixture(t)
		pressPull(t, v, "i", "space", "ctrl+s")
		if len(log.all()) != 0 || v.pullComment == nil || v.pullComment.err == "" {
			t.Fatalf("a blank comment was sent or closed silently: %+v", log.all())
		}
	})
}

// The head a merge is pinned to is the rollup's, and while the pull row
// disagrees `y` does nothing.
func TestPullWriteMergeHeadFollowsTheRollup(t *testing.T) {
	v, log := writeTabFixture(t)
	pull := linkedPull()
	pull.Pull.HeadSHA = "ffffffffffffffffffff"
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
	pressPull(t, v, "m", "right", "y")
	if posts := log.posts(); len(posts) != 0 {
		t.Fatalf("a merge was sent while the head had moved: %+v", posts)
	}
	if !strings.Contains(ansi.Strip(v.render(120, 40)), "head moved") {
		t.Fatal("the popup does not say the head moved")
	}
	// The refetch lands and the two agree: now y sends, pinned to the rollup.
	v.pullTab.checks.Ref = "ffffffffffffffffffff"
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: v.pullTab.checks})
	pressPull(t, v, "y")
	posts := log.posts()
	if len(posts) != 1 || !strings.Contains(posts[0].body, `"head_sha":"ffffffffffffffffffff"`) {
		t.Fatalf("the merge sent %+v, want the refetched head", posts)
	}

	// With no rollup loaded, the pull row's head stands in.
	v2, _ := writeTabFixture(t)
	v2.pullTab.checks = apiclient.GitHubTaskChecks{}
	if head, moved := v2.mergeHead(); head != "0b1d2e3f4a5b6c7d8e9f" || moved {
		t.Fatalf("mergeHead = %q moved=%v with no rollup, want the pull row's", head, moved)
	}
}

// While a prompt or a popup is up, it owns the keyboard: q, a digit, a §6
// letter and tab do nothing but answer it, and the footer describes the
// confirmation rather than the tab underneath.
func TestPullWriteConfirmationsCaptureTheKeyboard(t *testing.T) {
	for _, tc := range []struct {
		open string
		ctx  bindingContext
	}{{"m", ctxPullMerge}, {"i", ctxPullComment}, {"X", ctxPullConfirm}, {"ctrl+r", ctxPullConfirm}} {
		for _, key := range []string{"q", "2", "r", "tab"} {
			t.Run(tc.open+" then "+key, func(t *testing.T) {
				v, log := writeTabFixture(t)
				pressPull(t, v, tc.open)
				if !v.capturesInput() {
					t.Fatalf("%s: the root would still read q as quit", tc.open)
				}
				if got := v.bindingContext(); got != tc.ctx {
					t.Fatalf("%s: the footer describes %q, want %q", tc.open, got, tc.ctx)
				}
				pressPull(t, v, key)
				if v.tab != taskTabPull {
					t.Fatalf("a key behind the confirmation moved the workspace to %v", v.tab)
				}
				if got := log.all(); len(got) != 0 {
					t.Fatalf("a key behind the confirmation reached the daemon: %+v", got)
				}
			})
		}
	}
}

// A refusal is the daemon's message on the note line, and a successful write
// refetches what it changed.
func TestPullWriteReportsTheDaemonsAnswer(t *testing.T) {
	v, _ := writeTabFixture(t)
	cmd := v.applyPullWrite(pullWriteMsg{
		taskID: v.detail.taskID, write: pullWriteMerge, subject: "octo/api#41",
		err: &apiclient.Error{Status: http.StatusConflict, Message: "could not merge octo/api#41: GitHub will not merge this pull request as it stands"},
	})
	if cmd != nil || !v.pullTab.noteBad ||
		v.pullTab.note != "could not merge octo/api#41: GitHub will not merge this pull request as it stands" {
		t.Fatalf("note = %q (bad=%v)", v.pullTab.note, v.pullTab.noteBad)
	}
	if cmd := v.applyPullWrite(pullWriteMsg{
		taskID: v.detail.taskID, write: pullWriteMerge, subject: "octo/api#41", method: "rebase",
	}); cmd == nil || v.pullTab.note != "merged octo/api#41 (rebase)" {
		t.Fatalf("a merge did not refetch or report: %q", v.pullTab.note)
	}
	if cmd := v.applyPullWrite(pullWriteMsg{
		taskID: v.detail.taskID, write: pullWriteRerun, subject: "octo/api#41", runID: 77,
	}); cmd == nil || v.pullTab.note != "re-run requested for Actions run 77" {
		t.Fatalf("a re-run did not refetch or report: %q", v.pullTab.note)
	}
}
