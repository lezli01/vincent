package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// testIssue creates one issue in projectID for a task to link to.
func testIssue(t *testing.T, s *Store, projectID int64, title string) *Issue {
	t.Helper()
	is, err := s.CreateIssue(t.Context(), NewIssue{ProjectID: projectID, Title: title}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return is
}

func linkedSnapshot(id int64) *IssueSnapshot {
	return &IssueSnapshot{
		ID: id, Title: "Link tasks to issues", State: "open",
		Kind: "feature", Priority: 2, Labels: []string{"store", "taskrun"},
		URL: "https://example.invalid/issues/1",
	}
}

// TestTaskIssueLinkRoundTrips (task 130 decision 5): both halves of the link
// — the issues.id pointer and the snapshot — are written by CreateTask and
// read back by GetTask and ListTasks; a task with neither reads back nil.
func TestTaskIssueLinkRoundTrips(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	linked := newTask(p.ID, "linked", TaskQueued)
	linked.IssueID = &is.ID
	linked.Issue = linkedSnapshot(is.ID)
	plain := newTask(p.ID, "plain", TaskQueued)
	for _, tk := range []*Task{linked, plain} {
		if err := s.CreateTask(ctx, tk, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", tk.Title, err)
		}
	}

	got, err := s.GetTask(ctx, linked.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.IssueID == nil || *got.IssueID != is.ID {
		t.Errorf("IssueID = %v, want %d", got.IssueID, is.ID)
	}
	if !reflect.DeepEqual(got.Issue, linkedSnapshot(is.ID)) {
		t.Errorf("Issue = %+v, want %+v", got.Issue, linkedSnapshot(is.ID))
	}

	list, err := s.ListTasks(ctx, TaskFilter{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	seen := 0
	for _, tk := range list {
		switch tk.ID {
		case linked.ID:
			seen++
			if tk.IssueID == nil || *tk.IssueID != is.ID || !reflect.DeepEqual(tk.Issue, linkedSnapshot(is.ID)) {
				t.Errorf("listed linked task = %v %+v", tk.IssueID, tk.Issue)
			}
		case plain.ID:
			seen++
			if tk.IssueID != nil || tk.Issue != nil {
				t.Errorf("listed plain task carries a link: %v %+v", tk.IssueID, tk.Issue)
			}
		}
	}
	if seen != 2 {
		t.Errorf("ListTasks returned %d of the 2 tasks", seen)
	}

	// SQL NULL, not the JSON text "null", for the task with no snapshot.
	var raw *string
	if err := s.db.QueryRowContext(ctx, `SELECT issue_json FROM tasks WHERE id = ?`, plain.ID).Scan(&raw); err != nil {
		t.Fatalf("read issue_json: %v", err)
	}
	if raw != nil {
		t.Errorf("issue_json = %q, want NULL", *raw)
	}
}

// TestTaskCreatedCarriesIssueID: task.created names the issue when there is
// one, and omits the key rather than writing null when there is not.
func TestTaskCreatedCarriesIssueID(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	linked := newTask(p.ID, "linked", TaskQueued)
	linked.IssueID = &is.ID
	linked.Issue = linkedSnapshot(is.ID)
	plain := newTask(p.ID, "plain", TaskQueued)
	for _, tk := range []*Task{linked, plain} {
		if err := s.CreateTask(ctx, tk, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", tk.Title, err)
		}
	}

	for _, tc := range []struct {
		task *Task
		want *int64
	}{{linked, &is.ID}, {plain, nil}} {
		evs, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskCreated}, TaskID: tc.task.ID})
		if err != nil || len(evs) != 1 {
			t.Fatalf("task.created for %s = %d, %v", tc.task.Title, len(evs), err)
		}
		var payload map[string]any
		if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		v, ok := payload["issue_id"]
		switch {
		case tc.want == nil && ok:
			t.Errorf("%s: payload carries issue_id %v, want it omitted", tc.task.Title, v)
		case tc.want != nil && (!ok || v != float64(*tc.want)):
			t.Errorf("%s: payload issue_id = %v (present %v), want %d", tc.task.Title, v, ok, *tc.want)
		}
	}
}

// TestDeletingAnIssueKeepsItsTasksSnapshot (task 130 decision 6): the pointer
// goes NULL with the issue, the snapshot stays, and the task is untouched
// otherwise.
func TestDeletingAnIssueKeepsItsTasksSnapshot(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")

	tk := newTask(p.ID, "linked", TaskQueued)
	tk.IssueID = &is.ID
	tk.Issue = linkedSnapshot(is.ID)
	if err := s.CreateTask(ctx, tk, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := s.DeleteIssue(ctx, is.ID, issuestate.Human); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	got, err := s.GetTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTask after the issue's deletion: %v", err)
	}
	if got.IssueID != nil {
		t.Errorf("IssueID = %d, want nil once the issue is gone", *got.IssueID)
	}
	if !reflect.DeepEqual(got.Issue, linkedSnapshot(is.ID)) {
		t.Errorf("Issue = %+v, want the snapshot intact", got.Issue)
	}
}

func TestIssueSnapshotCloneIsDeep(t *testing.T) {
	var none *IssueSnapshot
	if none.Clone() != nil {
		t.Error("Clone of nil is not nil")
	}
	orig := linkedSnapshot(1)
	c := orig.Clone()
	if c == orig || !reflect.DeepEqual(c, orig) {
		t.Fatalf("Clone = %p %+v, want an equal copy of %p", c, c, orig)
	}
	c.Labels[0] = "mutated"
	if orig.Labels[0] == "mutated" {
		t.Error("Clone shares the Labels backing array")
	}
}
