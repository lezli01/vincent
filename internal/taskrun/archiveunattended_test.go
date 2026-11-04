package taskrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// issueClosed is the marker the auto-archive worker passes (task 143).
var issueClosed = map[string]any{"archived_by": "issue_closed"}

// withRemote gives the harness's repository a bare remote and turns the
// remote leg on, and returns the remote's path.
func (h *actionHarness) withRemote(t *testing.T) string {
	t.Helper()
	h.cfg.DeleteRemoteBranchOnArchive = true
	remote := testrepo.InitBare(t)
	testrepo.Run(t, h.repo, "remote", "add", "origin", remote)
	testrepo.Run(t, h.repo, "push", "-q", "origin", "main")
	return remote
}

// pushedWorktree creates the task's worktree and pushes its still-empty
// branch with an upstream, which is what makes the remote leg eligible.
func (h *actionHarness) pushedWorktree(t *testing.T, task *store.Task) {
	t.Helper()
	path := h.worktree(t, task)
	testrepo.Run(t, path, "push", "-q", "--set-upstream", "origin", task.BranchName)
}

// remoteHas asks the bare remote itself whether it still carries branch.
func remoteHas(t *testing.T, remote, branch string) bool {
	t.Helper()
	return testrepo.Run(t, remote,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/"+branch) != ""
}

// archivePayload is the payload of the task's `to: archived` state event.
func (h *actionHarness) archivePayload(t *testing.T, id int64) map[string]any {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{
		Types: []string{store.EventTaskStateChanged}, TaskID: id,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range events {
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode payload %s: %v", e.Payload, err)
		}
		if p["to"] == string(store.TaskArchived) {
			return p
		}
	}
	t.Fatalf("task %d has no archive event", id)
	return nil
}

// TestUnattendedArchiveNeverRunsTheRemoteLeg: task 008 decision 3 makes the
// remote leg attended-only, and an unattended archive enforces it whatever
// delete_remote_branch_on_archive says. The attended control proves the
// fixture really reaches the remote leg.
func TestUnattendedArchiveNeverRunsTheRemoteLeg(t *testing.T) {
	h := newActionHarness(t)
	remote := h.withRemote(t)

	attended := h.task(t, store.TaskDone)
	h.pushedWorktree(t, attended)
	_, branch, err := h.runner.Archive(t.Context(), attended.ID, ArchiveOptions{})
	if err != nil {
		t.Fatalf("Archive(attended): %v", err)
	}
	if branch.Remote == nil || remoteHas(t, remote, attended.BranchName) {
		t.Fatalf("attended archive: remote outcome %+v, remote ref kept %v — the fixture never reached the remote leg",
			branch.Remote, remoteHas(t, remote, attended.BranchName))
	}

	unattended := h.task(t, store.TaskDone)
	h.pushedWorktree(t, unattended)
	got, branch, err := h.runner.Archive(t.Context(), unattended.ID,
		ArchiveOptions{Unattended: true, EventPayload: issueClosed})
	if err != nil {
		t.Fatalf("Archive(unattended): %v", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if branch.Result != worktree.BranchDeleted || h.branchExists(t, unattended.BranchName) {
		t.Errorf("local leg: outcome %+v — the empty local branch is still deleted unattended", branch)
	}
	if branch.Remote != nil {
		t.Errorf("remote outcome = %+v, want nil: the remote leg never runs unattended", branch.Remote)
	}
	if !remoteHas(t, remote, unattended.BranchName) {
		t.Errorf("remote ref %s was deleted by an unattended archive", unattended.BranchName)
	}
}

// TestUnattendedArchiveNeverForces: a dirty worktree refuses an unattended
// archive and leaves the task as it was — with Force passed alongside too,
// because the mode overrides it.
func TestUnattendedArchiveNeverForces(t *testing.T) {
	for _, force := range []bool{false, true} {
		h := newActionHarness(t)
		task := h.task(t, store.TaskDone)
		path := h.worktree(t, task)
		if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("wip"), 0o600); err != nil {
			t.Fatalf("dirty the worktree: %v", err)
		}
		_, _, err := h.runner.Archive(t.Context(), task.ID,
			ArchiveOptions{Unattended: true, Force: force, EventPayload: issueClosed})
		if worktree.ReasonOf(err) != worktree.ReasonWorktreeDirty {
			t.Fatalf("force=%v: err = %v, want a dirty-worktree refusal", force, err)
		}
		got := h.get(t, task.ID)
		if got.State != store.TaskDone || got.WorktreePath != path {
			t.Errorf("force=%v: task = (%s, %q), want (done, %q) untouched",
				force, got.State, got.WorktreePath, path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("force=%v: worktree removed by a refused archive: %v", force, err)
		}
	}
}

// TestUnattendedArchiveCascadesItsMode: a fan-out parent's lanes inherit the
// root's whole options — no lane's remote ref goes, and every lane's archive
// event carries the root's marker.
func TestUnattendedArchiveCascadesItsMode(t *testing.T) {
	h := newActionHarness(t)
	remote := h.withRemote(t)
	parent := h.task(t, store.TaskDone)
	h.pushedWorktree(t, parent)
	lanes := []*store.Task{h.lane(t, parent, store.TaskDone), h.lane(t, parent, store.TaskDone)}
	for _, lane := range lanes {
		h.pushedWorktree(t, lane)
	}

	if _, _, err := h.runner.Archive(t.Context(), parent.ID,
		ArchiveOptions{Unattended: true, EventPayload: issueClosed}); err != nil {
		t.Fatalf("Archive(parent): %v", err)
	}
	for _, task := range append([]*store.Task{parent}, lanes...) {
		if got := h.get(t, task.ID); got.State != store.TaskArchived {
			t.Errorf("task %d = %s, want archived", task.ID, got.State)
		}
		if !remoteHas(t, remote, task.BranchName) {
			t.Errorf("task %d: remote ref %s deleted under an unattended root", task.ID, task.BranchName)
		}
		if by := h.archivePayload(t, task.ID)["archived_by"]; by != "issue_closed" {
			t.Errorf("task %d: archived_by = %v, want issue_closed", task.ID, by)
		}
	}
}

// TestArchivePayload: the marker reaches the archive event beside the
// task's issue_id, and an archive given no options writes exactly the keys
// it always has.
func TestArchivePayload(t *testing.T) {
	h := newActionHarness(t)
	iss, err := h.store.CreateIssue(t.Context(),
		store.NewIssue{ProjectID: h.projectID, Title: "closed upstream"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	marked := &store.Task{
		ProjectID: h.projectID, Title: "issue task",
		WorkflowName: "test", WorkflowSnapshot: actionSnapshot,
		BaseBranch: "main", State: store.TaskDone,
		IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, marked.Title), nil }
	if err := h.store.CreateTask(t.Context(), marked, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h.worktree(t, marked)
	if _, _, err := h.runner.Archive(t.Context(), marked.ID,
		ArchiveOptions{Unattended: true, EventPayload: issueClosed}); err != nil {
		t.Fatalf("Archive(marked): %v", err)
	}
	p := h.archivePayload(t, marked.ID)
	if p["archived_by"] != "issue_closed" {
		t.Errorf("archived_by = %v, want issue_closed", p["archived_by"])
	}
	if id, _ := p["issue_id"].(float64); int64(id) != iss.ID {
		t.Errorf("issue_id = %v, want %d", p["issue_id"], iss.ID)
	}

	plain := h.task(t, store.TaskDone)
	h.worktree(t, plain)
	if _, _, err := h.runner.Archive(t.Context(), plain.ID, ArchiveOptions{}); err != nil {
		t.Fatalf("Archive(plain): %v", err)
	}
	var keys []string
	for k := range h.archivePayload(t, plain.ID) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"current_step", "from", "to"}; !slices.Equal(keys, want) {
		t.Errorf("plain archive payload keys = %v, want %v", keys, want)
	}
}
