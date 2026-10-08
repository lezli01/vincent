package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 134.16: the issue detail's lane fact, Main worktree section, role
// annotations and merge-back folding.

func i64p(n int64) *int64 { return &n }

// rolesIssueFixture is an in-progress issue whose main worktree #30 holds,
// with a side task #31 whose merge-back #33 is pending, and a side task #32
// whose source-less merge-back #34 lost its link.
func rolesIssueFixture() *issueView {
	v := issueFixture()
	v.issue.Lane, v.issue.Attention = "in_progress", true
	v.issue.MainWorktree = &apiclient.IssueMainWorktree{
		Branch: "vincent/30-dark", OccupantTaskID: i64p(30), OccupantState: strptr(stateRunning),
	}
	v.tasks = []apiclient.Task{
		{ID: 34, ProjectID: 2, Title: "Merge orphan", State: stateQueued, IssueWorktree: strptr("main")},
		{ID: 33, ProjectID: 2, Title: "Merge #31", State: stateQueued, IssueWorktree: strptr("main"), MergeSourceTaskID: i64p(31)},
		{
			ID: 32, ProjectID: 2, Title: "side two", State: stateRunning, IssueWorktree: strptr("side"),
			MergeBack: &apiclient.MergeBack{OnConflict: "block"},
		},
		{
			ID: 31, ProjectID: 2, Title: "side one", State: stateDone, IssueWorktree: strptr("side"),
			MergeBack: &apiclient.MergeBack{OnConflict: "agent"},
		},
		{ID: 30, ProjectID: 2, Title: "the main work", State: stateRunning, IssueWorktree: strptr("main")},
		{ID: 29, ProjectID: 2, Title: "before roles", State: stateDone},
	}
	return v
}

func TestIssueDetailShowsLaneAndMainWorktree(t *testing.T) {
	v := rolesIssueFixture()
	out := ansi.Strip(v.render(120, 200))
	for _, want := range []string{
		"lane      in progress  !", "Main worktree", "branch    vincent/30-dark",
		"#30  the main work  running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail is missing %q:\n%s", want, out)
		}
	}

	v.issue.MainWorktree.OccupantTaskID, v.issue.MainWorktree.OccupantState = nil, nil
	v.issue.State, v.issue.Lane, v.issue.Attention = "closed", "in_progress", false
	out = ansi.Strip(v.render(120, 200))
	if !strings.Contains(out, "occupant  free") {
		t.Errorf("a free main worktree does not say so:\n%s", out)
	}
	if !strings.Contains(out, "lane      done") {
		t.Errorf("a closed issue's lane is not done:\n%s", out)
	}

	v.issue.MainWorktree = nil
	if out := ansi.Strip(v.render(120, 200)); strings.Contains(out, "Main worktree") {
		t.Errorf("an issue with no main branch shows the section:\n%s", out)
	}
}

func TestIssueDetailEnterOpensTheOccupant(t *testing.T) {
	v := rolesIssueFixture()
	_, cmd := v.updateKey(registryKey(t, "enter"))
	if cmd == nil {
		t.Fatal("enter on the Main worktree section opened nothing")
	}
	if msg, ok := cmd().(selectTaskMsg); !ok || msg.id != 30 || msg.back != viewIssue {
		t.Fatalf("enter produced %#v, want the occupant #30 returning to the issue", cmd())
	}

	v.issue.MainWorktree.OccupantTaskID = nil
	if _, cmd := v.updateKey(registryKey(t, "enter")); cmd != nil {
		t.Fatalf("enter on a free main worktree produced %#v", cmd())
	}
}

func TestIssueDetailAnnotatesRolesAndFoldsMergeBacks(t *testing.T) {
	v := rolesIssueFixture()
	var order []int64
	children := map[int64]bool{}
	for _, row := range v.selectRows() {
		if row.mainWorktree {
			continue
		}
		order = append(order, row.task.ID)
		children[row.task.ID] = row.child
	}
	want := []int64{34, 32, 31, 33, 30, 29}
	if len(order) != len(want) {
		t.Fatalf("row order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("row order = %v, want %v", order, want)
		}
	}
	if !children[33] || children[34] || children[31] {
		t.Errorf("children = %v, want only #33 folded", children)
	}

	out := ansi.Strip(v.render(140, 200))
	for _, want := range []string{
		"#30  the main work  main · running",
		"#32  side two  side · merge manual · running",
		"#31  side one  side · merge agent · done",
		"└ ○ #33  Merge #31  merge-back of #31 · queued",
		"#34  Merge orphan  main · queued",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail is missing %q:\n%s", want, out)
		}
	}
	if line := lineWith(out, "#29"); strings.Contains(line, "main") || strings.Contains(line, "side") {
		t.Errorf("a task with no role is annotated: %q", line)
	}
}

// TestIssueDetailRereadsOnItsTasksEvents: a task event naming this issue —
// a new side task, a merge-back — re-reads it although the task is not
// listed yet.
func TestIssueDetailRereadsOnItsTasksEvents(t *testing.T) {
	v := rolesIssueFixture()
	ev := apiclient.Event{Type: "task.created", TaskID: i64p(99), Payload: []byte(`{"issue_id":5}`)}
	if cmd := v.updateNote(apiclient.EventNote{Event: ev}); cmd == nil {
		t.Fatal("a task event naming the issue did not re-read it")
	}
	v.refreshWait = false
	ev.Payload = []byte(`{"issue_id":6}`)
	if cmd := v.updateNote(apiclient.EventNote{Event: ev}); cmd != nil {
		t.Fatal("another issue's task event re-read this one")
	}
}

// seededForm is a new-task draft seeded from issue 5, answered with iss.
func seededForm(iss apiclient.Issue) *newTask {
	n := newNewTask()
	n.projectID, n.issueID, n.workflow = 2, 5, "fix"
	iss.ID = 5
	n.applyIssue(ntIssueMsg{projectID: 2, workflow: "fix", issueID: 5, issue: iss})
	return n
}

// TestNewTaskFormWorktreeRows is task 134.16 decision 4.
func TestNewTaskFormWorktreeRows(t *testing.T) {
	// No main branch: the task is the issue's first main task, nothing to
	// choose, and the submit carries no merge_back.
	n := seededForm(apiclient.Issue{})
	if n.rowVisible(ntWorktree) || n.rowVisible(ntMergeBack) || !n.rowVisible(ntBranchName) {
		t.Fatal("an issue with no main branch offers the worktree rows")
	}
	if n.request().MergeBack != nil {
		t.Fatal("a first main task sends merge_back")
	}

	// A free main worktree defaults to main and names no occupant.
	n = seededForm(apiclient.Issue{MainWorktree: &apiclient.IssueMainWorktree{Branch: "b"}})
	if !n.rowVisible(ntWorktree) || n.separate || n.rowVisible(ntMergeBack) {
		t.Fatalf("a free main worktree: worktree row %v, separate %v", n.rowVisible(ntWorktree), n.separate)
	}
	if strings.Contains(ansi.Strip(n.rowValue(ntWorktree)), "busy") {
		t.Errorf("a free main worktree names an occupant: %q", ansi.Strip(n.rowValue(ntWorktree)))
	}

	// Choosing separate hides and clears the branch rows, and the submit
	// carries merge_back — manual first, agent on the next enter.
	n.branchName.SetValue("typed/branch")
	n.cursor = ntWorktree
	n.activate()
	if !n.separate || n.rowVisible(ntBranch) || n.rowVisible(ntBranchName) || !n.rowVisible(ntMergeBack) {
		t.Fatal("separate does not swap the branch rows for the merge-back row")
	}
	req := n.request()
	if req.BranchName != nil || req.MergeBack == nil || req.MergeBack.OnConflict != "block" {
		t.Fatalf("separate request = branch %v, merge_back %+v; want no branch and block", req.BranchName, req.MergeBack)
	}
	n.cursor = ntMergeBack
	n.activate()
	if req := n.request(); req.MergeBack == nil || req.MergeBack.OnConflict != "agent" {
		t.Fatalf("merge_back = %+v, want agent", req.MergeBack)
	}

	// A busy main worktree preselects separate and names the occupant; a
	// later answer never overrides a human's choice.
	busy := apiclient.Issue{MainWorktree: &apiclient.IssueMainWorktree{Branch: "b", OccupantTaskID: i64p(30)}}
	n = seededForm(busy)
	if !n.separate || !strings.Contains(ansi.Strip(n.rowValue(ntWorktree)), "main worktree busy with #30") {
		t.Fatalf("busy: separate %v, row %q", n.separate, ansi.Strip(n.rowValue(ntWorktree)))
	}
	n.cursor = ntWorktree
	n.activate()
	n.applyIssue(ntIssueMsg{projectID: 2, workflow: "fix", issueID: 5, issue: busy})
	if n.separate {
		t.Error("a re-read put separate back over the human's choice of main")
	}
	if n.request().MergeBack != nil {
		t.Error("main sends merge_back")
	}
}
