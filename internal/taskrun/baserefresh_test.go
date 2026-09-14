package taskrun

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// The base-refresh record (§10, task 099): admission persists what fetching
// the base branch and fast-forwarding the local one did, in the same write
// that claims the worktree. Every assertion reads the task back through
// GetTask rather than trusting the engine's in-memory copy, because the row is
// what a restarted daemon, the API and the TUI will serve.

// refreshSnapshot is one command step that succeeds on both shells — the
// admission is under test, not the step.
func refreshSnapshot() string {
	return "name: refresh\nsteps:\n" + commandStep("noop", "exit 0")
}

// TestAdmissionRecordsAnAdvancedBaseRefresh: a clean project whose remote main
// is one commit ahead. The worktree is cut from the fetched tip, the project's
// own main is moved to it, and the row says both.
func TestAdmissionRecordsAnAdvancedBaseRefresh(t *testing.T) {
	h := newEngineHarness(t)
	remoteTip := advanceRemoteMain(t, h.repo)
	h.start(t)
	task := h.createTask(t, refreshSnapshot())

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if done.BaseSHA != remoteTip {
		t.Errorf("base_sha = %q, want the fetched tip %s", done.BaseSHA, remoteTip)
	}
	r := done.BaseRefresh
	if r == nil {
		t.Fatal("base_refresh is NULL after an ordinary admission")
	}
	if r.Fetch.Result != worktree.FetchDone || r.Fetch.Remote != "origin" || r.Fetch.Ref == "" {
		t.Errorf("fetch = %+v, want result %q from origin with a ref", r.Fetch, worktree.FetchDone)
	}
	if r.FastForward.Result != worktree.FastForwardAdvanced {
		t.Errorf("fast_forward = %+v, want %q", r.FastForward, worktree.FastForwardAdvanced)
	}
	if got := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main"); got != remoteTip {
		t.Errorf("project refs/heads/main = %s, want it fast-forwarded to %s", got, remoteTip)
	}
}

// TestAdmissionRecordsAFailedBaseFetch: the base has an upstream whose URL
// goes nowhere. That degrades to the local ref (task 056) — the task is
// admitted and runs, never blocked — and the record carries git's error and
// says no fast-forward was attempted, because there was nothing to move to.
func TestAdmissionRecordsAFailedBaseFetch(t *testing.T) {
	h := newEngineHarness(t)
	advanceRemoteMain(t, h.repo)
	testrepo.Run(t, h.repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	localMain := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main")
	h.start(t)
	task := h.createTask(t, refreshSnapshot())

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone || done.BlockReason != "" {
		t.Fatalf("state = %s (block_reason %q), want done — a failed fetch never blocks", done.State, done.BlockReason)
	}
	r := done.BaseRefresh
	if r == nil {
		t.Fatal("base_refresh is NULL after an ordinary admission")
	}
	if r.Fetch.Result != worktree.FetchFailed || r.Fetch.Error == "" {
		t.Errorf("fetch = %+v, want result %q with git's error", r.Fetch, worktree.FetchFailed)
	}
	if r.FastForward.Result != worktree.FastForwardNotAttempted {
		t.Errorf("fast_forward = %+v, want %q", r.FastForward, worktree.FastForwardNotAttempted)
	}
	if got := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main"); got != localMain {
		t.Errorf("project refs/heads/main moved to %s after a failed fetch, want %s", got, localMain)
	}
}

// TestPullRequestAdmissionRecordsNoBaseRefresh: a task created from a pull
// request fetches its *head*, not its base, and never fast-forwards anything
// (task 064). Its row keeps base_refresh NULL rather than a record that would
// claim a base refresh happened.
func TestPullRequestAdmissionRecordsNoBaseRefresh(t *testing.T) {
	h := newEngineHarness(t)
	remote := testrepo.InitBare(t)
	testrepo.Run(t, h.repo, "remote", "add", "origin", remote)
	testrepo.Run(t, h.repo, "push", "-q", "-u", "origin", "main")
	// The head exists only on the remote, as somebody else's pull request does.
	testrepo.Run(t, h.repo, "checkout", "-q", "-b", "feature")
	testrepo.Run(t, h.repo, "commit", "-q", "--allow-empty", "-m", "their change")
	testrepo.Run(t, h.repo, "push", "-q", "origin", "feature:refs/heads/feature")
	testrepo.Run(t, h.repo, "checkout", "-q", "main")
	testrepo.Run(t, h.repo, "branch", "-q", "-D", "feature")
	h.start(t)

	task := &store.Task{
		ProjectID: h.projectID, Title: "from a pull request", Description: "a task",
		WorkflowName: "refresh", WorkflowSnapshot: refreshSnapshot(),
		BaseBranch: "main", BranchName: "feature", State: store.TaskQueued,
		GitHubPull: &github.PullLink{Repo: "o/r", Number: 7, Source: github.SourceHuman, Branch: true},
	}
	resolve := func(int64) (string, error) { return "feature", nil }
	if err := h.store.CreateTask(t.Context(), task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if done.WorktreePath == "" {
		t.Error("worktree_path not persisted for a pull-request admission")
	}
	if done.BaseRefresh != nil {
		t.Errorf("base_refresh = %+v, want NULL — a head fetch is not a base refresh", *done.BaseRefresh)
	}
}

// TestLogBaseRefreshLevels pins the level each fast-forward outcome logs at:
// the level is the signal of who has to act, so a change to one is a change
// to what a human is told.
func TestLogBaseRefreshLevels(t *testing.T) {
	cases := []struct {
		name string
		ff   worktree.FastForwardOutcome
		want string // the level=… of the one fast-forward line; "" for none
		has  string
	}{
		{"advanced", worktree.FastForwardOutcome{Result: worktree.FastForwardAdvanced, Worktree: "/p"}, "INFO", "worktree=/p"},
		{"up to date", worktree.FastForwardOutcome{Result: worktree.FastForwardUpToDate}, "DEBUG", ""},
		{"local ahead", worktree.FastForwardOutcome{Result: worktree.FastForwardSkipped, Reason: worktree.SkipLocalAhead}, "DEBUG", ""},
		{"diverged", worktree.FastForwardOutcome{Result: worktree.FastForwardSkipped, Reason: worktree.SkipDiverged}, "INFO", "reason=diverged"},
		{"dirty", worktree.FastForwardOutcome{Result: worktree.FastForwardSkipped, Reason: worktree.SkipCheckoutDirty}, "INFO", "reason=checkout_dirty"},
		{"busy", worktree.FastForwardOutcome{Result: worktree.FastForwardSkipped, Reason: worktree.SkipCheckoutBusy}, "INFO", "reason=checkout_busy"},
		{"error", worktree.FastForwardOutcome{Result: worktree.FastForwardSkipped, Reason: worktree.SkipError, Error: "boom"}, "WARN", "error=boom"},
		{"not attempted", worktree.FastForwardOutcome{Result: worktree.FastForwardNotAttempted}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			// An empty fetch result logs nothing, so every line is the fast-forward's.
			logBaseRefresh(log, "main", worktree.FetchOutcome{}, tc.ff)
			out := strings.TrimSpace(buf.String())
			if tc.want == "" {
				if out != "" {
					t.Fatalf("logged %q, want nothing", out)
				}
				return
			}
			if strings.Count(out, "\n") != 0 || !strings.Contains(out, "level="+tc.want) {
				t.Fatalf("logged %q, want one line at %s", out, tc.want)
			}
			if !strings.Contains(out, tc.has) {
				t.Errorf("logged %q, want it to carry %q", out, tc.has)
			}
		})
	}
}
