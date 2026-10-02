package taskrun

import (
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestLaneInheritsTheIssueLink (task 130 decision 5, keeping task 035
// decision 9): a lane carries both halves of its parent's issue link — the
// issues.id pointer and the frozen snapshot — as copies of its own.
func TestLaneInheritsTheIssueLink(t *testing.T) {
	r := &Runner{}
	issueID := int64(42)
	snap := &store.IssueSnapshot{
		ID: 42, Title: "Link tasks to issues", State: "open",
		Kind: "feature", Priority: 2, Labels: []string{"store", "taskrun"},
	}
	parent := &store.Task{
		ID: 7, ProjectID: 1, Title: "root", BranchName: "vincent/7-root",
		IssueID: &issueID, Issue: snap,
	}
	env := &stepEnv{
		task: parent,
		wf:   &workflow.Workflow{Name: "root"},
		step: workflow.Step{ID: "build", Type: workflow.StepFanOut},
	}
	child, err := r.laneTask(env, workflow.Lane{
		ID:    "api",
		Steps: []workflow.Step{{ID: "work", Type: workflow.StepCommand, Run: "exit 0"}},
	}, 0)
	if err != nil {
		t.Fatalf("laneTask: %v", err)
	}
	if child.IssueID == nil || *child.IssueID != 42 {
		t.Fatalf("lane issue_id = %v, want 42", child.IssueID)
	}
	if child.IssueID == parent.IssueID {
		t.Error("the lane shares the parent's issue_id pointer")
	}
	if child.Issue == nil || !reflect.DeepEqual(*child.Issue, *snap) {
		t.Fatalf("lane issue snapshot = %+v, want the parent's %+v", child.Issue, snap)
	}
	if child.Issue == parent.Issue {
		t.Error("the lane shares the parent's snapshot pointer")
	}
	child.Issue.Labels[0] = "mutated"
	if parent.Issue.Labels[0] == "mutated" {
		t.Error("the lane shares the parent's label slice")
	}
}

// TestUnlinkedParentGivesNoIssueLink: nothing is invented for a lane whose
// parent came from no issue.
func TestUnlinkedParentGivesNoIssueLink(t *testing.T) {
	r := &Runner{}
	env := &stepEnv{
		task: &store.Task{ID: 7, ProjectID: 1, Title: "root", BranchName: "b"},
		wf:   &workflow.Workflow{Name: "root"},
		step: workflow.Step{ID: "build", Type: workflow.StepFanOut},
	}
	child, err := r.laneTask(env, workflow.Lane{
		ID:    "api",
		Steps: []workflow.Step{{ID: "work", Type: workflow.StepCommand, Run: "exit 0"}},
	}, 0)
	if err != nil {
		t.Fatalf("laneTask: %v", err)
	}
	if child.IssueID != nil || child.Issue != nil {
		t.Errorf("lane carries an issue link its parent never had: %v %+v", child.IssueID, child.Issue)
	}
}
