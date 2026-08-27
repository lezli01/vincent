package taskrun

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// `.Issue` (§8.4, task 035). The property under test throughout is that
// rendering reads the **task row** — the snapshot the daemon captured at
// creation — and never anything live. There is no seam here for a network
// call to enter through, which is exactly the point: §8.4 promises a step
// render cannot fail for an external reason.

func storedIssue() *github.Issue {
	return &github.Issue{
		Repo:            "lezli01/vincent",
		Number:          200,
		Title:           "Select a GitHub issue when creating a task",
		Body:            "The body as it was when the task was created.",
		URL:             "https://github.com/lezli01/vincent/issues/200",
		State:           github.StateOpen,
		Labels:          []string{"enhancement", "area/api"},
		Author:          "lezli01",
		Assignee:        "hubot",
		Milestone:       "v0.2.0",
		MilestoneNumber: 4,
	}
}

func TestIssueContextCarriesTheSnapshot(t *testing.T) {
	got := issueContext(storedIssue())
	want := workflow.IssueContext{
		Number: 200, Repo: "lezli01/vincent",
		Title:  "Select a GitHub issue when creating a task",
		Body:   "The body as it was when the task was created.",
		URL:    "https://github.com/lezli01/vincent/issues/200",
		State:  github.StateOpen,
		Labels: []string{"enhancement", "area/api"},
		Author: "lezli01", Assignee: "hubot",
		Milestone: "v0.2.0", MilestoneNumber: 4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("issueContext =\n %+v\nwant\n %+v", got, want)
	}
}

// TestIssueContextOfAnUnlinkedTask: the zero value, which is what makes
// `{{ if .Issue.Number }}` the test a shared template writes — the same
// convention `.Loop`'s `Index: 0` already uses (decision 8).
func TestIssueContextOfAnUnlinkedTask(t *testing.T) {
	if got := issueContext(nil); !reflect.DeepEqual(got, workflow.IssueContext{}) {
		t.Errorf("issueContext(nil) = %+v, want the zero value", got)
	}
}

// TestIssueRendersInAPromptAndACommand: the five fields the acceptance
// criteria name, in both of the places §8.4 templates reach.
func TestIssueRendersInAPromptAndACommand(t *testing.T) {
	rc := workflow.RenderContext{Issue: issueContext(storedIssue())}
	prompt := `Fix #{{ .Issue.Number }}: {{ .Issue.Title }}
{{ .Issue.Body }}
See {{ .Issue.URL }}
Labels:{{ range .Issue.Labels }} {{ . }}{{ end }}`
	got, err := workflow.Render("prompt", prompt, rc)
	if err != nil {
		t.Fatalf("render prompt: %v", err)
	}
	for _, want := range []string{
		"Fix #200: Select a GitHub issue when creating a task",
		"The body as it was when the task was created.",
		"See https://github.com/lezli01/vincent/issues/200",
		"Labels: enhancement area/api",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered prompt is missing %q:\n%s", want, got)
		}
	}

	run, err := workflow.Render("run", `gh issue comment {{ .Issue.Number }}`, rc)
	if err != nil {
		t.Fatalf("render run: %v", err)
	}
	if run != "gh issue comment 200" {
		t.Errorf("rendered command = %q", run)
	}
}

// TestIssueGuardWorksOnBoth: one template, both kinds of task. This is the
// acceptance criterion that a workflow shared between linked and unlinked
// tasks does not have to be forked.
func TestIssueGuardWorksOnBoth(t *testing.T) {
	const body = `{{ if .Issue.Number }}Work on #{{ .Issue.Number }}.{{ else }}No linked issue.{{ end }}`
	for _, tc := range []struct {
		name  string
		issue *github.Issue
		want  string
	}{
		{"linked", storedIssue(), "Work on #200."},
		{"unlinked", nil, "No linked issue."},
	} {
		got, err := workflow.Render("prompt", body,
			workflow.RenderContext{Issue: issueContext(tc.issue)})
		if err != nil {
			t.Fatalf("%s: render: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: rendered %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRenderReadsTheSnapshotNotTheIssue is the freshness claim (§5.3): an
// issue edited on GitHub after creation does not change what a later step
// renders, because the row is the only thing rendering can see. The "edit" is
// modelled by changing the value nobody wrote back to the row.
func TestRenderReadsTheSnapshotNotTheIssue(t *testing.T) {
	stored := storedIssue()
	rc := workflow.RenderContext{Issue: issueContext(stored)}

	// Somebody retitles and closes the issue on GitHub. Nothing re-fetches.
	edited := *stored
	edited.Title, edited.State = "Retitled on GitHub", github.StateClosed
	if edited.Title == stored.Title {
		t.Fatal("the test did not model an edit")
	}

	got, err := workflow.Render("prompt", `{{ .Issue.Title }} ({{ .Issue.State }})`, rc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "Select a GitHub issue when creating a task (open)"
	if got != want {
		t.Errorf("rendered %q, want the snapshot %q", got, want)
	}
}

// TestLaneInheritsTheParentsIssue (decision 9): a lane already inherits the
// parent's Fields, and a lane prompt that could read `.Task.Fields` but not
// `.Issue` would be an arbitrary hole. The copy is deep, so the two rows are
// independent tasks rather than two views of one slice.
func TestLaneInheritsTheParentsIssue(t *testing.T) {
	r := &Runner{}
	parent := &store.Task{
		ID: 7, ProjectID: 1, Title: "root", BranchName: "vincent/7-root",
		GitHubIssue: storedIssue(),
	}
	env := &stepEnv{
		task: parent,
		wf:   &workflow.Workflow{Name: "root"},
		step: workflow.Step{ID: "build", Type: workflow.StepFanOut},
	}
	lane := workflow.Lane{
		ID:    "api",
		Steps: []workflow.Step{{ID: "work", Type: workflow.StepCommand, Run: "exit 0"}},
	}
	child, err := r.laneTask(env, lane, 0)
	if err != nil {
		t.Fatalf("laneTask: %v", err)
	}
	if child.GitHubIssue == nil {
		t.Fatal("the lane inherited no issue")
	}
	if !reflect.DeepEqual(*child.GitHubIssue, *storedIssue()) {
		t.Errorf("lane issue =\n %+v\nwant the parent's\n %+v", *child.GitHubIssue, *storedIssue())
	}
	if child.GitHubIssue == parent.GitHubIssue {
		t.Error("the lane shares the parent's issue pointer; the copy must be its own")
	}
	child.GitHubIssue.Labels[0] = "mutated"
	if parent.GitHubIssue.Labels[0] == "mutated" {
		t.Error("the lane shares the parent's label slice")
	}
}

// TestUnlinkedParentGivesUnlinkedLanes: nothing is invented for a lane whose
// parent has no issue.
func TestUnlinkedParentGivesUnlinkedLanes(t *testing.T) {
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
	if child.GitHubIssue != nil {
		t.Errorf("lane carries an issue its parent never had: %+v", child.GitHubIssue)
	}
}
