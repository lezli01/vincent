package apiclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestTaskCommitsAgainstRealHandlers is the drift guard for the commit list
// (§13.2, issue #601): the client decodes what the real handler encodes,
// lane fields included.
func TestTaskCommitsAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "checkout", "-q", "-b", "feature")
	testrepo.Run(t, repo, "checkout", "-q", "-b", "lane-x")
	testrepo.WriteFile(t, repo, "lane.txt", "lane\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "lane work")
	testrepo.Run(t, repo, "checkout", "-q", "feature")
	testrepo.WriteFile(t, repo, "own.txt", "own\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "own work")
	testrepo.Run(t, repo, "merge", "-q", "--no-ff", "-m", "Merge lane 'x' of task 31", "lane-x")

	p := &store.Project{Name: "commits", Path: repo, DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "commits", WorkflowName: "adhoc",
		WorkflowSnapshot: snapshotWorkflow, BaseBranch: "main",
		BranchName: "feature", WorktreePath: repo, State: store.TaskRunning,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := h.client().TaskCommits(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("TaskCommits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want own work and the lane merge", got)
	}
	if got[0].Subject != "own work" || got[0].LaneID != "" || got[0].SHA == "" || got[0].AuthorTime == "" {
		t.Errorf("first commit = %+v", got[0])
	}
	if got[1].LaneID != "x" || got[1].ChildTaskID != 31 {
		t.Errorf("lane merge = %+v, want lane x of task 31", got[1])
	}
}

// TestTaskCommitsConflictIsAnError: a task that was never admitted is a 409
// *Error, not an empty list — "no branch yet" is not "no commits".
func TestTaskCommitsConflictIsAnError(t *testing.T) {
	h := newHarness(t)
	_, err := h.client().TaskCommits(context.Background(), h.taskID)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("TaskCommits on an unadmitted task: err = %v, want a 409 *apiclient.Error", err)
	}
}

// TestTaskCommitsUnknownTaskIsNotUnsupported: an unknown task and an unknown
// route are both `not_found`; only the router's own message means the daemon
// predates the route.
func TestTaskCommitsUnknownTaskIsNotUnsupported(t *testing.T) {
	h := newHarness(t)
	_, err := h.client().TaskCommits(context.Background(), 999999)
	if errors.Is(err, apiclient.ErrCommitsUnsupported) {
		t.Fatal("an unknown task was reported as an older daemon")
	}
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 *apiclient.Error", err)
	}
}

// TestTaskCommitsOnAnOlderDaemon: a daemon without the route answers its
// router's 404, which is ErrCommitsUnsupported. The body is the one every
// released daemon's catch-all writes.
func TestTaskCommitsOnAnOlderDaemon(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such endpoint"}}`))
	}))
	t.Cleanup(ts.Close)

	_, err := apiclient.New(ts.URL, testToken).TaskCommits(context.Background(), 1)
	if !errors.Is(err, apiclient.ErrCommitsUnsupported) {
		t.Fatalf("err = %v, want ErrCommitsUnsupported", err)
	}
}
