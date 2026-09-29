package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// commitsWorktree gives a queued task the worktree admission would have made
// and returns its path.
func commitsWorktree(t *testing.T, h *taskHarness, id int64) string {
	t.Helper()
	stored, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	created, err := h.wt.CreateAndClaim(t.Context(), h.repo, worktree.TaskOwner(id),
		stored.BranchName, stored.BaseBranch, true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), id, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	return created.Path
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	testrepo.WriteFile(t, dir, name, msg+"\n")
	testrepo.Run(t, dir, "add", ".")
	testrepo.Run(t, dir, "commit", "-q", "-m", msg)
}

// taskCommitsOK fetches the commit list, requiring a 200.
func taskCommitsOK(t *testing.T, h *taskHarness, id int64) []commitResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commits: %d %s", resp.StatusCode, body)
	}
	var out []commitResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func subjects(cs []commitResponse) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Subject
	}
	return out
}

// TestTaskCommitsListsTheTasksOwnCommitsInOrder: oldest first, each with its
// sha and an RFC3339 UTC author time, and no lane fields on a plain commit.
func TestTaskCommitsListsTheTasksOwnCommitsInOrder(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	wt := commitsWorktree(t, h, task.ID)
	commitFile(t, wt, "one.txt", "first")
	commitFile(t, wt, "two.txt", "second")

	got := taskCommitsOK(t, h, task.ID)
	if s := strings.Join(subjects(got), ","); s != "first,second" {
		t.Fatalf("subjects = %q, want first,second", s)
	}
	head := strings.TrimSpace(testrepo.Run(t, wt, "rev-parse", "HEAD"))
	if got[1].SHA != head {
		t.Errorf("newest sha = %q, want HEAD %q", got[1].SHA, head)
	}
	for _, c := range got {
		at, err := time.Parse(time.RFC3339, c.AuthorTime)
		if err != nil || at.Location() != time.UTC {
			t.Errorf("author_time %q is not RFC3339 UTC (%v)", c.AuthorTime, err)
		}
		if c.LaneID != "" || c.ChildTaskID != 0 {
			t.Errorf("a plain commit carries lane fields: %+v", c)
		}
	}
}

// TestTaskCommitsCreditsEachLaneOnce: a fan-out parent's lane merges carry the
// lane, and the commits made inside a lane — on the merge's second-parent
// side — are not the parent's and do not appear.
func TestTaskCommitsCreditsEachLaneOnce(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	laneDiffFixture(t, h, task.ID)

	got := taskCommitsOK(t, h, task.ID)
	want := []struct {
		subject string
		lane    string
		child   int64
	}{
		{"setup", "", 0},
		{"Merge lane 'alpha' of task 4001", "alpha", 4001},
		{"Merge lane 'beta' of task 4002", "beta", 4002},
		{"post-join", "", 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %d commits", subjects(got), len(want))
	}
	for i, w := range want {
		if got[i].Subject != w.subject || got[i].LaneID != w.lane || got[i].ChildTaskID != w.child {
			t.Errorf("commit %d = %+v, want %q lane %q of task %d", i, got[i], w.subject, w.lane, w.child)
		}
	}

	// The wire omits the lane fields on a plain commit rather than sending
	// zero values a client would have to know to ignore.
	_, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", task.ID), nil)
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := raw[0]["lane_id"]; has {
		t.Errorf("a plain commit carries lane_id: %s", body)
	}
}

// TestTaskCommitsUsesTheRecordedBase: a task cut from a tip ahead of its local
// base branch lists only what it made. A merge-base against the base branch's
// name would resolve to the stale local commit and claim the upstream work.
func TestTaskCommitsUsesTheRecordedBase(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	wt := commitsWorktree(t, h, task.ID)
	commitFile(t, wt, "upstream.txt", "upstream work")
	tip := strings.TrimSpace(testrepo.Run(t, wt, "rev-parse", "HEAD"))
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, nil, &tip); err != nil {
		t.Fatalf("record base sha: %v", err)
	}
	commitFile(t, wt, "mine.txt", "mine")

	if s := strings.Join(subjects(taskCommitsOK(t, h, task.ID)), ","); s != "mine" {
		t.Errorf("subjects = %q, want only the task's own commit", s)
	}
}

// TestTaskCommitsSurviveArchive is the reason the route reads the branch and
// not the worktree: archive removes the worktree (§3 row 17) and keeps a
// branch that has commits, and the list must still answer.
func TestTaskCommitsSurviveArchive(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	wt := commitsWorktree(t, h, task.ID)
	commitFile(t, wt, "kept.txt", "kept")
	setState(t, h, task.ID, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/archive", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there after archive (stat err %v)", err)
	}
	if s := strings.Join(subjects(taskCommitsOK(t, h, task.ID)), ","); s != "kept" {
		t.Errorf("subjects after archive = %q, want kept", s)
	}
}

// TestTaskCommitsConflicts: a task never admitted has no branch, and a
// deleted branch is gone whatever deleted it. Both are 409s that say which.
func TestTaskCommitsConflicts(t *testing.T) {
	h := newActionHarness(t)

	never := queuedTask(t, h)
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", never.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(decodeError(t, body).Message, "no branch yet") {
		t.Errorf("never admitted: %d %s, want 409 no branch yet", resp.StatusCode, body)
	}

	gone := queuedTask(t, h)
	wt := commitsWorktree(t, h, gone.ID)
	stored, err := h.store.GetTask(t.Context(), gone.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	testrepo.Run(t, h.repo, "worktree", "remove", "--force", wt)
	testrepo.Run(t, h.repo, "branch", "-D", stored.BranchName)
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", gone.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(decodeError(t, body).Message, "no longer exists") {
		t.Errorf("deleted branch: %d %s, want 409 no longer exists", resp.StatusCode, body)
	}

	// Archive's own cleanup of a branch with no commits (task 008) is the
	// same answer: the daemon does not record who deleted a branch.
	cleaned := queuedTask(t, h)
	commitsWorktree(t, h, cleaned.ID)
	setState(t, h, cleaned.ID, store.TaskDone)
	if resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/archive", cleaned.ID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", cleaned.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(decodeError(t, body).Message, "no longer exists") {
		t.Errorf("archive-cleaned branch: %d %s, want 409 no longer exists", resp.StatusCode, body)
	}
}

// TestTaskCommitsEmptyIsAnEmptyList: nothing past the base is `[]`, never
// `null` — "no commits" is an answer, not an absence of one.
func TestTaskCommitsEmptyIsAnEmptyList(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	commitsWorktree(t, h, task.ID)

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/commits", task.ID), nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("empty branch: %d %s, want 200 []", resp.StatusCode, body)
	}
}

// TestTaskCommitsLaneMergeWithCRSubject: a merge message whose line ends in CR
// is still recognised as a lane, and the subject comes back without the CR —
// the case that would otherwise fail on Windows alone.
func TestTaskCommitsLaneMergeWithCRSubject(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	wt := commitsWorktree(t, h, task.ID)
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	testrepo.Run(t, wt, "checkout", "-q", "-b", "lane-cr")
	commitFile(t, wt, "cr.txt", "lane work")
	testrepo.Run(t, wt, "checkout", "-q", stored.BranchName)
	testrepo.Run(t, wt, "merge", "-q", "--no-ff", "--no-commit", "lane-cr")
	testrepo.Run(t, wt, "commit", "-q", "--cleanup=verbatim", "-m", "Merge lane 'cr' of task 77\r")

	got := taskCommitsOK(t, h, task.ID)
	if len(got) != 1 {
		t.Fatalf("got %q, want the merge alone", subjects(got))
	}
	if got[0].Subject != "Merge lane 'cr' of task 77" || got[0].LaneID != "cr" || got[0].ChildTaskID != 77 {
		t.Errorf("CR-terminated lane merge = %+v", got[0])
	}
}

// TestTaskCommitsOverMCP: `task_commits` is a read, exposed like `task_diff`,
// and replays the route — the same handler answers the tool.
func TestTaskCommitsOverMCP(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	wt := commitsWorktree(t, h, task.ID)
	commitFile(t, wt, "tool.txt", "via the tool")

	httpClient := h.ts.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: testToken}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(),
		&sdk.StreamableClientTransport{Endpoint: h.ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "task_commits", Arguments: map[string]any{"id": task.ID},
	})
	if err != nil {
		t.Fatalf("tools/call task_commits: %v", err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("task_commits = %+v, want the route's body", res.Content)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	if !strings.Contains(text.Text, `"subject":"via the tool"`) {
		t.Errorf("task_commits = %s, want the task's commit", text.Text)
	}
}
