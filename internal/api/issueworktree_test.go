package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// Task 134.10: a task's issue-worktree role on POST /v1/tasks, `merge_back`,
// the issue's main branch bound at creation, the creation hint, and
// `main_worktree` on the issue.

func (h *taskHarness) issue(t *testing.T) *store.Issue {
	t.Helper()
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{
		ProjectID: h.projectID, Title: "Share one branch", Body: "b",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

// TestIssueTaskIsMainByDefault: a task with issue_id is a main task, and a
// second one runs on the first one's branch (task 134 decisions 3, 9). A
// task without an issue has no role.
func TestIssueTaskIsMainByDefault(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	iss := h.issue(t)

	first := h.createTask(t, map[string]any{"issue_id": iss.ID})
	if first.IssueWorktree == nil || *first.IssueWorktree != store.IssueWorktreeMain || first.MergeBack != nil {
		t.Fatalf("first task role = %v / %v, want main with no merge_back", first.IssueWorktree, first.MergeBack)
	}
	second := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "again"})
	if second.BranchName != first.BranchName {
		t.Errorf("second main task's branch = %q, want the issue's main branch %q", second.BranchName, first.BranchName)
	}
	// It joins the branch rather than adopting it (task 134.12): it receives
	// the first task's directory at admission.
	if second.AdoptedBranch {
		t.Error("a joining main task is bound as an adopted branch")
	}
	plain := h.createTask(t, map[string]any{"title": "no issue"})
	if plain.IssueWorktree != nil {
		t.Errorf("a task with no issue has role %q", *plain.IssueWorktree)
	}
}

// TestMergeBackValidation is task 134 decision 6, one refusal per rule.
func TestMergeBackValidation(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	iss := h.issue(t)
	testrepo.Run(t, h.repo, "branch", "already/there")
	block := map[string]any{"on_conflict": "block"}

	if msg := h.createTaskRejected(t, map[string]any{"title": "x", "merge_back": block}); !strings.Contains(msg, "requires issue_id") {
		t.Errorf("merge_back without issue_id: %q", msg)
	}
	if msg := h.createTaskRejected(t, map[string]any{"issue_id": iss.ID, "merge_back": block}); !strings.Contains(msg, "no main branch yet") {
		t.Errorf("merge_back before a main branch: %q", msg)
	}
	main := h.createTask(t, map[string]any{"issue_id": iss.ID})
	// Bound but not cut yet: the first main task has not been admitted, so a
	// side task has no branch to start from (task 134.13 decision 2).
	if msg := h.createTaskRejected(t, map[string]any{"issue_id": iss.ID, "merge_back": block}); !strings.Contains(msg, "does not resolve to a local branch") {
		t.Errorf("merge_back before the main branch is cut: %q", msg)
	}
	testrepo.Run(t, h.repo, "branch", main.BranchName)
	for name, extra := range map[string]map[string]any{
		"branch_name":     {"branch_name": "side/typed"},
		"existing_branch": {"branch_name": "already/there", "existing_branch": true},
	} {
		body := map[string]any{"issue_id": iss.ID, "merge_back": block}
		for k, v := range extra {
			body[k] = v
		}
		if msg := h.createTaskRejected(t, body); !strings.Contains(msg, "cannot be combined") {
			t.Errorf("merge_back with %s: %q", name, msg)
		}
	}
	if msg := h.createTaskRejected(t, map[string]any{
		"issue_id": iss.ID, "merge_back": map[string]any{"on_conflict": "rebase"},
	}); !strings.Contains(msg, "on_conflict") {
		t.Errorf("bad on_conflict: %q", msg)
	}

	side := h.createTask(t, map[string]any{"issue_id": iss.ID, "merge_back": map[string]any{"on_conflict": "agent"}})
	if side.IssueWorktree == nil || *side.IssueWorktree != store.IssueWorktreeSide ||
		side.MergeBack == nil || side.MergeBack.OnConflict != store.MergeOnConflictAgent {
		t.Fatalf("side task role = %v / %v", side.IssueWorktree, side.MergeBack)
	}
	if side.BranchName == main.BranchName {
		t.Errorf("side task runs on the main branch %q", side.BranchName)
	}
	// An empty on_conflict is fan_out's default.
	dflt := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "d", "merge_back": map[string]any{}})
	if dflt.MergeBack == nil || dflt.MergeBack.OnConflict != store.MergeOnConflictBlock {
		t.Errorf("default merge_back = %v, want block", dflt.MergeBack)
	}
	// And issue_id beside github_pull is still refused.
	if msg := h.createTaskRejected(t, map[string]any{"issue_id": iss.ID, "github_pull": 3}); !strings.Contains(msg, "github_pull") {
		t.Errorf("issue_id with github_pull: %q", msg)
	}
}

// TestSideTaskBaseIsTheMainBranch is task 134.13 decisions 3 and 4: a side
// task's base is the issue's main branch, whether base_branch is omitted or
// names it, and base_branch naming any other branch is a 400.
func TestSideTaskBaseIsTheMainBranch(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	iss := h.issue(t)
	main := h.createTask(t, map[string]any{"issue_id": iss.ID})
	testrepo.Run(t, h.repo, "branch", main.BranchName)
	testrepo.Run(t, h.repo, "branch", "elsewhere")

	omitted := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "omitted", "merge_back": map[string]any{}})
	if omitted.BaseBranch != main.BranchName {
		t.Errorf("side base with base_branch omitted = %q, want the main branch %q", omitted.BaseBranch, main.BranchName)
	}
	named := h.createTask(t, map[string]any{
		"issue_id": iss.ID, "title": "named", "merge_back": map[string]any{}, "base_branch": main.BranchName,
	})
	if named.BaseBranch != main.BranchName {
		t.Errorf("side base naming the main branch = %q", named.BaseBranch)
	}
	for _, other := range []string{"elsewhere", "main"} {
		msg := h.createTaskRejected(t, map[string]any{
			"issue_id": iss.ID, "title": "other", "merge_back": map[string]any{}, "base_branch": other,
		})
		if !strings.Contains(msg, "a side task is cut from it") {
			t.Errorf("side base_branch %q: %q", other, msg)
		}
	}
	// The side task cuts its own branch, never the main one.
	stored, err := h.store.GetTask(t.Context(), omitted.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.AdoptedBranch || stored.BranchName == main.BranchName {
		t.Errorf("side task = (%q, adopted %v), want a branch of its own", stored.BranchName, stored.AdoptedBranch)
	}
}

// TestSideTaskDiffSurvivesItsMergeBack is task 134.13 decision 6: a side
// task's recorded base_sha is the main branch's tip at the cut, so merging
// its commits back into the main branch leaves its diff as it was — against
// base_branch alone, the merge-base would move to the side's own tip.
func TestSideTaskDiffSurvivesItsMergeBack(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	iss := h.issue(t)
	main := h.createTask(t, map[string]any{"issue_id": iss.ID})
	mainDir := filepath.Join(t.TempDir(), "main")
	testrepo.Run(t, h.repo, "worktree", "add", "-q", "-b", main.BranchName, mainDir, "main")
	side := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "side", "merge_back": map[string]any{}})
	stored, err := h.store.GetTask(t.Context(), side.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	created, err := h.wt.CreateSideAndClaim(t.Context(), h.repo, worktree.TaskOwner(side.ID),
		stored.BranchName, stored.BaseBranch, nil)
	if err != nil {
		t.Fatalf("CreateSideAndClaim: %v", err)
	}
	if tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+main.BranchName); created.BaseSHA != tip {
		t.Fatalf("base_sha = %q, want the main branch's tip %s", created.BaseSHA, tip)
	}
	if err := h.store.SetTaskProgress(t.Context(), side.ID, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	testrepo.WriteFile(t, created.Path, "side.txt", "side work\n")
	testrepo.Run(t, created.Path, "add", ".")
	testrepo.Run(t, created.Path, "commit", "-q", "-m", "side work")

	diff := func() string {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", side.ID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("diff: %d %s", resp.StatusCode, body)
		}
		return string(body)
	}
	before := diff()
	if !strings.Contains(before, "side.txt") {
		t.Fatalf("side diff does not carry its work:\n%s", before)
	}
	testrepo.Run(t, mainDir, "merge", "-q", "--no-ff", "-m", "merge back", stored.BranchName)
	if after := diff(); after != before {
		t.Errorf("diff changed after the merge-back:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestExplicitBranchOnAMainTask is task 134 decision 5: branch_name or
// existing_branch becomes the main branch when there is none, may name it
// once there is, and may not name another.
func TestExplicitBranchOnAMainTask(t *testing.T) {
	t.Run("branch_name", func(t *testing.T) {
		h := newTaskHarness(t, 0, false)
		iss := h.issue(t)
		first := h.createTask(t, map[string]any{"issue_id": iss.ID, "branch_name": "issue/line"})
		if first.BranchName != "issue/line" {
			t.Fatalf("first branch = %q", first.BranchName)
		}
		// The branch exists by now in a real run; naming it is accepted.
		testrepo.Run(t, h.repo, "branch", "issue/line")
		same := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "same", "branch_name": "issue/line"})
		if same.BranchName != "issue/line" {
			t.Errorf("same branch = %q", same.BranchName)
		}
		msg := h.createTaskRejected(t, map[string]any{"issue_id": iss.ID, "title": "other", "branch_name": "issue/other"})
		if !strings.Contains(msg, "main branch is") {
			t.Errorf("different branch: %q", msg)
		}
	})
	t.Run("existing_branch", func(t *testing.T) {
		h := newTaskHarness(t, 0, false)
		iss := h.issue(t)
		testrepo.Run(t, h.repo, "branch", "adopt/me")
		testrepo.Run(t, h.repo, "branch", "adopt/other")
		first := h.createTask(t, map[string]any{"issue_id": iss.ID, "branch_name": "adopt/me", "existing_branch": true})
		if first.BranchName != "adopt/me" || !first.AdoptedBranch {
			t.Fatalf("first = %q adopted=%v", first.BranchName, first.AdoptedBranch)
		}
		iw, err := h.store.GetIssueMainWorktree(t.Context(), iss.ID)
		if err != nil || iw.Branch != "adopt/me" {
			t.Fatalf("main branch = %q (%v), want the adopted branch", iw.Branch, err)
		}
		if same := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "same", "branch_name": "adopt/me", "existing_branch": true}); same.AdoptedBranch {
			t.Error("a joining main task naming the adopted branch is itself adopted")
		}
		msg := h.createTaskRejected(t, map[string]any{
			"issue_id": iss.ID, "title": "other", "branch_name": "adopt/other", "existing_branch": true,
		})
		if !strings.Contains(msg, "main branch is") {
			t.Errorf("different adopted branch: %q", msg)
		}
	})
}

// TestMainWorktreeOccupantHint is task 134 decision 10's creation hint and
// decision 8's `main_worktree` on the issue: a main task created while an
// admitted one holds the main worktree names it.
func TestMainWorktreeOccupantHint(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	iss := h.issue(t)
	first := h.createTask(t, map[string]any{"issue_id": iss.ID})
	if first.MainWorktreeOccupantTaskID != nil {
		t.Errorf("first main task names occupant %d", *first.MainWorktreeOccupantTaskID)
	}
	queued := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "queued"})
	if queued.MainWorktreeOccupantTaskID != nil {
		t.Errorf("a queued predecessor is named as occupant %d", *queued.MainWorktreeOccupantTaskID)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), first.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	next := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "next"})
	if next.MainWorktreeOccupantTaskID == nil || *next.MainWorktreeOccupantTaskID != first.ID {
		t.Errorf("hint = %v, want task %d", next.MainWorktreeOccupantTaskID, first.ID)
	}
	testrepo.Run(t, h.repo, "branch", first.BranchName)
	side := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "side", "merge_back": map[string]any{}})
	if side.MainWorktreeOccupantTaskID != nil {
		t.Errorf("a side task carries the hint %d", *side.MainWorktreeOccupantTaskID)
	}

	resp, out := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get issue = %d: %s", resp.StatusCode, out)
	}
	var body issueBody
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	mw := body.MainWorktree
	if mw == nil || mw.Branch != first.BranchName || mw.OccupantTaskID == nil || *mw.OccupantTaskID != first.ID {
		t.Errorf("issue main_worktree = %+v, want %q held by %d", mw, first.BranchName, first.ID)
	}
	resp, out = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues?project_id=%d", h.projectID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list issues = %d: %s", resp.StatusCode, out)
	}
	var rows []issueRowBody
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MainWorktree == nil || rows[0].MainWorktree.Branch != first.BranchName {
		t.Errorf("issue rows = %+v", rows)
	}
	// An issue no task came from has no main worktree, and says nothing.
	other := h.issue(t)
	_, out = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", other.ID), nil)
	if strings.Contains(string(out), "main_worktree") {
		t.Errorf("an issue with no main branch serves main_worktree: %s", out)
	}
}

// TestMergeBackDigestIsAdditive: a body without merge_back digests exactly
// as it did before the field existed, so a key an older daemon recorded
// still replays; one with it is a different operation.
func TestMergeBackDigestIsAdditive(t *testing.T) {
	id := int64(4)
	req := taskCreateRequest{ProjectID: 1, Title: "t", IssueID: &id}
	b, err := json.Marshal(req.digestShape())
	if err != nil {
		t.Fatal(err)
	}
	const before = `{"project_id":1,"workflow":null,"title":"t","description":null,"fields":null,` +
		`"base_branch":null,"branch_name":null,"priority":null,"agent":null,"model":null,"effort":null,` +
		`"github_issue":null,"github_pull":null,"issue_id":4}`
	if string(b) != before {
		t.Errorf("digest shape =\n %s\nwant\n %s", b, before)
	}
	req.MergeBack = &mergeBackBody{OnConflict: "block"}
	withField, err := idempotencyDigest(req.digestShape())
	if err != nil {
		t.Fatal(err)
	}
	req.MergeBack = nil
	without, err := idempotencyDigest(req.digestShape())
	if err != nil {
		t.Fatal(err)
	}
	if withField == without {
		t.Error("merge_back does not change the digest")
	}
}

// TestHandoffFollowsTheIssueRole is task 134 decision 7: a handoff on an
// issue with no main branch is main and makes the chat's branch the main
// branch; the next one is side, merged back on block. merge_back itself is
// not accepted on the handoff yet.
func TestHandoffFollowsTheIssueRole(t *testing.T) {
	h := newChatHarness(t)
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.projectID, Title: "Carry on"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	stored := func(body map[string]any) *store.Task {
		t.Helper()
		task, _ := body["task"].(map[string]any)
		id, _ := task["id"].(float64)
		got, err := h.store.GetTask(t.Context(), int64(id))
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		return got
	}

	first, chat := handoffFixture(t, h)
	code, body := h.handoff(t, first, map[string]any{"issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("first handoff = %d (%v)", code, body)
	}
	main := stored(body)
	if main.IssueWorktree != store.IssueWorktreeMain || main.BranchName != chat["branch"] {
		t.Fatalf("first handoff = (%q, %q), want main on the chat's branch %v", main.IssueWorktree, main.BranchName, chat["branch"])
	}
	if iw, err := h.store.GetIssueMainWorktree(t.Context(), iss.ID); err != nil || iw.Branch != main.BranchName {
		t.Errorf("main branch = %q (%v), want the chat's", iw.Branch, err)
	}

	second, chat2 := handoffFixture(t, h)
	if code, body := h.handoff(t, second, map[string]any{"issue_id": iss.ID, "merge_back": map[string]any{}}); code != http.StatusBadRequest {
		t.Errorf("handoff with merge_back = %d (%v), want 400", code, body)
	}
	code, body = h.handoff(t, second, map[string]any{"issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("second handoff = %d (%v)", code, body)
	}
	side := stored(body)
	if side.IssueWorktree != store.IssueWorktreeSide || side.MergeOnConflict != store.MergeOnConflictBlock ||
		side.BranchName != chat2["branch"] {
		t.Errorf("second handoff = (%q, %q, %q), want side/block on its chat's branch",
			side.IssueWorktree, side.MergeOnConflict, side.BranchName)
	}
}
