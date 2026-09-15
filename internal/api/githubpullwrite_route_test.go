package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The pull-request write routes (§13.2, task 068.4), against the real
// handlers with the daemon's GitHub client pointed at cmd/fakegh.

const fakeGHHead = "d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f"

func (h *githubHarness) pullWrite(t *testing.T, id int64, action string, body any) (*http.Response, []byte) {
	t.Helper()
	return h.doJSON(t, http.MethodPost,
		"/v1/tasks/"+strconv.FormatInt(id, 10)+"/github/pull/"+action, body)
}

// linkedTask seeds a task linked to #412 by a human.
func (h *githubHarness) linkedTask(t *testing.T, branch string) *store.Task {
	t.Helper()
	task := h.seedTask(t, branch)
	if _, err := h.store.SetTaskGitHubPull(t.Context(), task.ID,
		store.LinkPull("octo/repo", 412, github.SourceHuman, task.CreatedAt)); err != nil {
		t.Fatalf("link: %v", err)
	}
	return task
}

// writeActions is every route with a body that passes validation.
var writeActions = []struct {
	action string
	body   any
}{
	{"merge", map[string]any{"method": "merge", "head_sha": fakeGHHead}},
	{"close", nil},
	{"reopen", nil},
	{"comment", map[string]any{"body": "hi"}},
	{"checks/rerun", map[string]any{"run_id": 5150}},
}

func TestPullWriteMergeAnswersTheMergedPull(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")

	resp, raw := h.pullWrite(t, task.ID, "merge", map[string]any{"method": "squash", "head_sha": fakeGHHead})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merge: %d %s", resp.StatusCode, raw)
	}
	var pull github.PullRequest
	if err := json.Unmarshal(raw, &pull); err != nil {
		t.Fatalf("merge body: %v (%s)", err, raw)
	}
	if !pull.Merged || pull.Number != 412 {
		t.Fatalf("merge answered %+v", pull)
	}
	if !strings.Contains(h.ghCalls(t), "pr merge 412 -R octo/repo --squash --match-head-commit "+fakeGHHead) {
		t.Fatalf("gh was not asked for the pinned merge:\n%s", h.ghCalls(t))
	}

	// The second merge is refused by the preflight, and sends nothing.
	resp, raw = h.pullWrite(t, task.ID, "merge", map[string]any{"method": "squash", "head_sha": fakeGHHead})
	if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != github.ReasonNotMergeable {
		t.Fatalf("a double merge answered %d %s", resp.StatusCode, raw)
	}
	if n := strings.Count(h.ghCalls(t), "pr merge"); n != 1 {
		t.Fatalf("a double merge ran gh pr merge %d times", n)
	}
}

func TestPullWriteCloseReopenCommentRerun(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")

	for action, want := range map[string]string{"close": github.StateClosed, "reopen": github.StateOpen} {
		resp, raw := h.pullWrite(t, task.ID, action, nil)
		var pull github.PullRequest
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &pull) != nil || pull.State != want {
			t.Fatalf("%s answered %d %s", action, resp.StatusCode, raw)
		}
	}

	resp, raw := h.pullWrite(t, task.ID, "comment", map[string]any{"body": "Ship it."})
	var comment githubPullCommentResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &comment) != nil ||
		comment.URL != "https://github.com/octo/repo/pull/412#issuecomment-1" {
		t.Fatalf("comment answered %d %s", resp.StatusCode, raw)
	}

	resp, raw = h.pullWrite(t, task.ID, "checks/rerun", map[string]any{"run_id": 5150})
	var rerun githubPullRerun
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &rerun) != nil || rerun.RunID != 5150 {
		t.Fatalf("rerun answered %d %s", resp.StatusCode, raw)
	}

	// A run that is not a failed Actions row of the live rollup is refused
	// before gh is asked.
	resp, raw = h.pullWrite(t, task.ID, "checks/rerun", map[string]any{"run_id": 9999})
	if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != github.ReasonBadRequest {
		t.Fatalf("an unknown run answered %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(h.ghCalls(t), "run rerun 9999") {
		t.Fatal("an unknown run was re-run")
	}
}

// No link and a suppressed link are both `pull_not_linked`, and neither
// reaches a gh write.
func TestPullWriteRefusesAnUnlinkedTask(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	unlinked := h.seedTask(t, "vincent/1-a-task")
	suppressed := h.linkedTask(t, "vincent/2-a-task")
	if _, err := h.store.SetTaskGitHubPull(t.Context(), suppressed.ID,
		store.SuppressPull(suppressed.GitHubPull, suppressed.CreatedAt)); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	for _, id := range []int64{unlinked.ID, suppressed.ID} {
		for _, a := range writeActions {
			resp, raw := h.pullWrite(t, id, a.action, a.body)
			if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != "pull_not_linked" {
				t.Errorf("task %d %s answered %d %s", id, a.action, resp.StatusCode, raw)
			}
		}
	}
	for _, write := range []string{"pr merge", "pr close", "pr reopen", "pr comment", "run rerun"} {
		if strings.Contains(h.ghCalls(t), write) {
			t.Fatalf("an unlinked task still ran gh %s", write)
		}
	}
}

func TestPullWriteGateRefusesWhenDisabled(t *testing.T) {
	h := newGitHubHarness(t, func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")
	for _, a := range writeActions {
		resp, raw := h.pullWrite(t, task.ID, a.action, a.body)
		if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != github.ReasonDisabled {
			t.Errorf("%s answered %d %s", a.action, resp.StatusCode, raw)
		}
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Fatalf("a disabled integration still called gh:\n%s", calls)
	}
}

func TestPullWriteValidatesBodies(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")
	for _, tc := range []struct {
		action string
		body   any
	}{
		{"merge", map[string]any{"head_sha": fakeGHHead}},
		{"merge", map[string]any{"method": "fast-forward", "head_sha": fakeGHHead}},
		{"merge", map[string]any{"method": "merge"}},
		{"comment", map[string]any{"body": "  "}},
		{"checks/rerun", map[string]any{"run_id": 0}},
	} {
		resp, raw := h.pullWrite(t, task.ID, tc.action, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %v answered %d %s, want 400", tc.action, tc.body, resp.StatusCode, raw)
		}
	}
	if calls := h.ghCalls(t); strings.Contains(calls, "pr merge") || strings.Contains(calls, "pr comment") {
		t.Fatalf("an invalid body still reached gh:\n%s", calls)
	}
}

// A read-scoped credential: every write is `no_write_scope`, and no part of
// gh's stderr reaches the response.
func TestPullWriteNoWriteScopeLeaksNothing(t *testing.T) {
	t.Setenv("FAKEGH_SCENARIO", "read-only")
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")
	for _, a := range writeActions {
		resp, raw := h.pullWrite(t, task.ID, a.action, a.body)
		if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != github.ReasonNoWriteScope {
			t.Errorf("%s answered %d %s", a.action, resp.StatusCode, raw)
		}
		for _, leak := range []string{"Resource not accessible", "HTTP 403", "gh:"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s leaked %q: %s", a.action, leak, raw)
			}
		}
	}
}

// The preflight's refusal is named, and GitHub's merge-state word never
// appears in the response.
func TestPullWriteMergePreflightRefusal(t *testing.T) {
	t.Setenv("FAKEGH_SCENARIO", "behind")
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.linkedTask(t, "vincent/1-a-task")
	resp, raw := h.pullWrite(t, task.ID, "merge", map[string]any{"method": "merge", "head_sha": fakeGHHead})
	if resp.StatusCode != http.StatusConflict || errorReason(t, raw) != github.ReasonBranchBehind {
		t.Fatalf("a behind branch answered %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "BEHIND") {
		t.Fatalf("the response carries GitHub's own merge state: %s", raw)
	}
	if strings.Contains(h.ghCalls(t), "pr merge") {
		t.Fatal("a refused preflight still ran gh pr merge")
	}
}
