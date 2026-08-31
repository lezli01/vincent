package api

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// POST /v1/tasks/{id}/github/pull/create (§13.2, task 069) — the one route
// that writes to a forge, against the real handlers with the daemon's GitHub
// client pointed at cmd/fakegh and its `origin` pointed at a real bare
// repository.
//
// The two remotes are `remote.origin.url` and `remote.origin.pushurl`,
// deliberately: the handler derives the repository identity from the fetch
// URL (which must parse as github.com) and git pushes to the push URL (which
// must be a real repository this test can inspect). A stand-in for either
// half would prove only that this package believes itself.

// pushableOrigin gives the harness's project a bare remote to push to while
// its `origin` still reads as a github.com repository, and returns the bare
// repository's path.
func pushableOrigin(t *testing.T, h *githubHarness) string {
	t.Helper()
	bare := testrepo.InitBare(t)
	gitIn(t, h.repo, "config", "remote.origin.pushurl", bare)
	return bare
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedBranch cuts a branch with a commit on it in the project repository, so
// there is something to push.
func seedBranch(t *testing.T, repo, branch string) {
	t.Helper()
	gitIn(t, repo, "checkout", "-q", "-b", branch)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "work on "+branch)
	gitIn(t, repo, "checkout", "-q", "main")
}

func (h *githubHarness) createPull(t *testing.T, id int64, body map[string]any) (*http.Response, githubPullCreateResponse) {
	t.Helper()
	resp, raw := h.doJSON(t, http.MethodPost,
		"/v1/tasks/"+strconv.FormatInt(id, 10)+"/github/pull/create", body)
	var out githubPullCreateResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("create body: %v (%s)", err, raw)
		}
	} else {
		out.Reason = errorReason(t, raw)
	}
	return resp, out
}

// errorReason reads the `reason` out of the snake_case error envelope.
func errorReason(t *testing.T, raw []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("error envelope: %v (%s)", err, raw)
	}
	return env.Error.Details["reason"]
}

// The happy path: the branch reaches the remote, the pull request exists, and
// the link is written as `human` — not left to the reconciler's next tick,
// which is what would make a pull request just created from vincent read as
// unlinked for up to github.poll_interval.
func TestCreatePullPushesLinksAndPublishes(t *testing.T) {
	t.Setenv("FAKEGH_CREATED_FILE", filepath.Join(t.TempDir(), "created.json"))
	h := newGitHubHarness(t, nil, ghOrigin)
	bare := pushableOrigin(t, h)
	seedBranch(t, h.repo, "vincent/1-a-task")
	task := h.seedTask(t, "vincent/1-a-task")

	resp, out := h.createPull(t, task.ID, map[string]any{
		"title": "Open a pull request", "body": "Closes #273", "draft": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %d (%s)", resp.StatusCode, out.Reason)
	}
	if !out.Created || !out.Pushed || out.Pull == nil {
		t.Fatalf("create answered %+v", out)
	}
	if !out.Pull.Draft {
		t.Error("the draft toggle did not produce a draft pull request")
	}
	// The branch really is on the remote. This is the issue's second
	// complaint: a compare URL for a branch nobody pushed leads to a dead
	// page.
	if gitIn(t, bare, "rev-parse", "refs/heads/vincent/1-a-task") == "" {
		t.Fatal("the branch is not on the remote")
	}
	// The link is written, and it is a human's.
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !stored.GitHubPull.Linked() || stored.GitHubPull.Source != github.SourceHuman {
		t.Fatalf("stored link is %+v", stored.GitHubPull)
	}
	if stored.GitHubPull.Number != out.Pull.Number {
		t.Errorf("linked #%d, created #%d", stored.GitHubPull.Number, out.Pull.Number)
	}
	// SetTaskGitHubPull is the store call that publishes
	// task.github_pull_changed, so the workspace refreshes without polling.
	assertEventPublished(t, h, task.ID, "task.github_pull_changed")

	// And the second call is refused, which is how a double submission is
	// stopped without an idempotency key (decision 7).
	resp2, out2 := h.createPull(t, task.ID, map[string]any{"title": "again"})
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("a second create answered %d, want 409", resp2.StatusCode)
	}
	if out2.Reason != "pull_already_linked" {
		t.Errorf("second create reported %q", out2.Reason)
	}
}

// A create that fails after a successful push is a **200 with a compare URL**,
// not an error: the branch is on the remote, so GitHub's own page works and
// the client opens it exactly as it did before task 069. Nothing got worse.
func TestCreatePullFallsBackToCompareURL(t *testing.T) {
	t.Setenv("FAKEGH_SCENARIO", "forbidden")
	h := newGitHubHarness(t, nil, ghOrigin)
	bare := pushableOrigin(t, h)
	seedBranch(t, h.repo, "vincent/2-a-task")
	task := h.seedTask(t, "vincent/2-a-task")

	resp, out := h.createPull(t, task.ID, map[string]any{"title": "Open a pull request"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the fallback is not an error: got %d", resp.StatusCode)
	}
	if out.Created {
		t.Fatal("a forbidden credential created a pull request")
	}
	if !out.Pushed {
		t.Error("the push did not happen before the create was attempted")
	}
	if out.Reason != github.ReasonForbidden {
		t.Errorf("fallback reason is %q, want %q", out.Reason, github.ReasonForbidden)
	}
	// The branch is path-escaped in the URL, which is what github.CompareURL
	// has always done and is not this route's business to change.
	if !strings.Contains(out.CompareURL, "/compare/main...vincent%2F2-a-task") {
		t.Errorf("compare URL is %q", out.CompareURL)
	}
	// The branch was pushed, which is what makes that URL live rather than
	// the dead page the issue complains about.
	if gitIn(t, bare, "rev-parse", "refs/heads/vincent/2-a-task") == "" {
		t.Fatal("the branch is not on the remote, so the compare URL is dead")
	}
	// Nothing was linked: there is no pull request to link.
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.GitHubPull.Linked() {
		t.Fatalf("a failed create linked %+v", stored.GitHubPull)
	}
}

// A push that is rejected creates nothing at GitHub. The remote is diverged,
// vincent does not force past it, and the refusal carries the named reason —
// not a compare URL, because a page for a head the remote does not have is
// the dead page all over again.
func TestCreatePullRejectedPushCreatesNothing(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	bare := pushableOrigin(t, h)
	// Somebody else's commit on the same branch name, on the remote only.
	gitIn(t, h.repo, "checkout", "-q", "-b", "vincent/3-a-task")
	gitIn(t, h.repo, "commit", "-q", "--allow-empty", "-m", "theirs")
	gitIn(t, h.repo, "push", "-q", bare, "vincent/3-a-task:refs/heads/vincent/3-a-task")
	gitIn(t, h.repo, "checkout", "-q", "main")
	gitIn(t, h.repo, "branch", "-q", "-D", "vincent/3-a-task")
	// Ours, from main, sharing no history with theirs.
	seedBranch(t, h.repo, "vincent/3-a-task")
	before := gitIn(t, bare, "rev-parse", "refs/heads/vincent/3-a-task")
	task := h.seedTask(t, "vincent/3-a-task")

	before405 := h.ghCalls(t)
	resp, out := h.createPull(t, task.ID, map[string]any{"title": "Open a pull request"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a rejected push answered %d, want 409", resp.StatusCode)
	}
	if out.Reason != worktree.ReasonPushRejected {
		t.Errorf("rejected push reported %q, want %q", out.Reason, worktree.ReasonPushRejected)
	}
	// Nothing at GitHub: no `pr create` was ever run.
	if calls := strings.TrimPrefix(h.ghCalls(t), before405); strings.Contains(calls, "pr create") {
		t.Fatalf("a rejected push still called gh pr create: %q", calls)
	}
	// And nothing on the remote moved: this is why force-pushing is refused.
	if after := gitIn(t, bare, "rev-parse", "refs/heads/vincent/3-a-task"); after != before {
		t.Fatalf("the remote branch moved from %s to %s on a rejected push", before, after)
	}
}

// The §13.2 gate refuses with the same named reason the read side gives, and
// it is checked before anything is pushed.
func TestCreatePullGateRefusals(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		h := newGitHubHarness(t, func() config.Config {
			c := config.Default()
			c.GitHub.Enabled = false
			return c
		}, ghOrigin)
		pushableOrigin(t, h)
		seedBranch(t, h.repo, "vincent/4-a-task")
		task := h.seedTask(t, "vincent/4-a-task")
		resp, out := h.createPull(t, task.ID, map[string]any{"title": "T"})
		if resp.StatusCode != http.StatusConflict || out.Reason != github.ReasonDisabled {
			t.Fatalf("disabled answered %d/%q", resp.StatusCode, out.Reason)
		}
		if strings.Contains(h.ghCalls(t), "pr create") {
			t.Fatal("a disabled integration still called gh")
		}
	})
	t.Run("not github", func(t *testing.T) {
		h := newGitHubHarness(t, nil, "https://gitlab.com/octo/repo.git")
		seedBranch(t, h.repo, "vincent/5-a-task")
		task := h.seedTask(t, "vincent/5-a-task")
		resp, out := h.createPull(t, task.ID, map[string]any{"title": "T"})
		if resp.StatusCode != http.StatusConflict || out.Reason != github.ReasonNotGitHub {
			t.Fatalf("non-github origin answered %d/%q", resp.StatusCode, out.Reason)
		}
	})
	// There is deliberately no "task with no branch" case here: the store
	// refuses to insert one ("insert task: branch name is empty"), so the
	// state is unreachable over the API. The handler's guard for it stays as
	// a cheap defence, and the *reachable* half of that condition — a task
	// whose branch exists but whose workspace has not been created yet — is
	// what the TUI reports, and is covered there.
	t.Run("no title", func(t *testing.T) {
		h := newGitHubHarness(t, nil, ghOrigin)
		task := h.seedTask(t, "vincent/6-a-task")
		resp, _ := h.createPull(t, task.ID, map[string]any{"title": "  "})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("an empty title answered %d, want 400", resp.StatusCode)
		}
	})
}

// assertEventPublished checks the durable events table carries the type, which
// is what an SSE subscriber resumes from.
func assertEventPublished(t *testing.T, h *githubHarness, taskID int64, typ string) {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{TaskID: taskID, Limit: 100})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		if e.Type == typ {
			return
		}
	}
	t.Fatalf("no %s event was published for task %d", typ, taskID)
}
