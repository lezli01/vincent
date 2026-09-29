package apiclient_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The list row carries the pull request link (task 129.16), against the real
// handlers: a linked task comes back with it and an unlinked one with nil. The
// board marks its rows from exactly this, so a server that served the link on
// the detail alone would leave every row unmarked without failing anything
// else.
func TestListRowCarriesThePullLink(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	unlinked := h.snapshotTask(t)
	if _, err := h.st.SetTaskGitHubPull(t.Context(), h.taskID,
		store.LinkPull("octo/repo", 42, github.SourceHuman, time.Now())); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}

	tasks, err := c.ListTasks(t.Context(), apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	rows := map[int64]apiclient.Task{}
	for _, task := range tasks {
		rows[task.ID] = task
	}
	linked, ok := rows[h.taskID]
	if !ok {
		t.Fatalf("task %d missing from the list", h.taskID)
	}
	if linked.GitHubPull == nil || linked.GitHubPull.Number != 42 || linked.GitHubPull.Repo != "octo/repo" {
		t.Errorf("linked row GitHubPull = %+v, want octo/repo#42", linked.GitHubPull)
	}
	if got := rows[unlinked].GitHubPull; got != nil {
		t.Errorf("unlinked row GitHubPull = %+v, want nil", got)
	}

	// The detail still reads it, now by promotion from the embedded Task: a
	// field declared on both would be ambiguous and silently dropped.
	detail, err := c.GetTask(t.Context(), h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.GitHubPull == nil || detail.GitHubPull.Number != 42 {
		t.Errorf("detail GitHubPull = %+v, want #42", detail.GitHubPull)
	}
}

// A daemon from before the field was read off the list — or one that never
// served it — decodes to nil, which marks nothing (task 129.16 decision 6).
func TestListRowWithoutThePullLinkDecodesNil(t *testing.T) {
	var row apiclient.Task
	if err := json.Unmarshal([]byte(`{"id":7,"title":"old daemon","state":"done"}`), &row); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if row.GitHubPull != nil {
		t.Errorf("GitHubPull = %+v, want nil", row.GitHubPull)
	}
}
