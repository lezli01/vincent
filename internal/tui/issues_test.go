package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Pure model tests for the issue screens (§15 views 12 and 13, task 130.9).
// No golden frames (task 129 decision 7): each asserts one property.

// issuesFixture is the list on project "api" with three issues in the
// daemon's order: a local one and an imported one in the open section, and
// a second local one in progress, whose task waits on a human. The cursor
// starts where a fresh list puts it, on the first issue.
func issuesFixture() *issuesView {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	v.issues = []apiclient.Issue{
		{ID: 3, ProjectID: 1, Title: "Crash on start", State: "open", Lane: "open", Kind: "bug", Labels: []string{"p1"}, TaskCount: 2},
		{
			ID: 5, ProjectID: 1, Title: "Dark mode", State: "open", Lane: "open", Kind: "feature",
			Source: &apiclient.IssueSource{Provider: "github", Repo: "octo/web", Number: 41, URL: "https://github.com/octo/web/issues/41"},
		},
		{ID: 4, ProjectID: 1, Title: "Docs typo", State: "open", Lane: "in_progress", Labels: []string{"docs"}, TaskCount: 1, Active: true, Attention: true},
	}
	v.loaded = true
	v.clampCursor()
	return v
}

// issueFixture is the detail on an imported issue with two active tasks and
// a description carrying one link.
func issueFixture() *issueView {
	v := newIssueView(nil, nil)
	v.client = offlineClient()
	v.id = 5
	v.issue = apiclient.Issue{
		ID: 5, ProjectID: 2, Title: "Dark mode", State: "open", Kind: "feature", Priority: 2, Author: "human",
		Body:   "# Why\n\nSee [the mock](https://example.com/mock) for **details**.\n" + strings.Repeat("\nmore\n", 30),
		Source: &apiclient.IssueSource{Provider: "github", Repo: "octo/web", Number: 41, URL: "https://github.com/octo/web/issues/41", RemoteState: "open"},
		Tasks:  apiclient.IssueTasks{Count: 3, ActiveIDs: []int64{21, 22}},
	}
	v.tasks = []apiclient.Task{
		{ID: 21, ProjectID: 2, Title: "dark mode, first pass", State: stateRunning},
		{ID: 22, ProjectID: 2, Title: "dark mode, review", State: stateAwaitingGate},
	}
	v.loaded = true
	return v
}

// With no project selected the list fetches nothing and says why, rather
// than showing every project's issues (task 132.11).
func TestIssuesWithNoProjectLoadNothing(t *testing.T) {
	v := newIssuesView()
	v.client = offlineClient()
	if cmd := v.loadCmd(); cmd != nil {
		t.Fatal("a list with no project selected issued a load")
	}
	v.setProjects(nil)
	out := ansi.Strip(v.render(120, 20))
	if !strings.Contains(out, "No projects registered") || !strings.Contains(out, "overview") {
		t.Errorf("the empty selection does not point at the overview:\n%s", out)
	}
}

// A switch empties the list before the reload lands, and says which project
// it is loading (task 132.11).
func TestIssuesSwitchClearsTheOldProjectsRows(t *testing.T) {
	v := issuesFixture()
	if cmd := v.setProject(projectSel{id: 2, name: "web"}); cmd == nil {
		t.Fatal("a switch did not reload")
	}
	if len(v.rows()) != 0 {
		t.Fatalf("the previous project's %d rows survived the switch", len(v.rows()))
	}
	if out := ansi.Strip(v.render(120, 20)); !strings.Contains(out, "loading web…") {
		t.Errorf("the header does not name the project being loaded:\n%s", out)
	}
}

func TestIssuesFilterMatchesEveryField(t *testing.T) {
	cases := map[string][]int64{
		"#4":    {4},       // id
		"crash": {3},       // title
		"docs":  {4},       // label
		"featu": {5},       // kind
		"api":   nil,       // the project's name is no longer a term
		"zzz":   nil,       // nothing
		"":      {3, 5, 4}, // no filter, in section order
	}
	for q, want := range cases {
		v := issuesFixture()
		v.filter.SetValue(q)
		var got []int64
		for _, row := range v.rows() {
			got = append(got, row.issue.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("filter %q matched %v, want %v", q, got, want)
		}
	}
}

func TestIssuesBrowserOnlyForImported(t *testing.T) {
	opened := withFakeOpener(t, nil)
	v := issuesFixture() // cursor on #3, local
	if _, cmd := v.updateKey(registryKey(t, "o")); cmd != nil {
		drain(cmd)
	}
	if len(*opened) != 0 {
		t.Fatalf("o on a local issue opened %v", *opened)
	}
	if !v.noteBad || !strings.Contains(v.note, "local") {
		t.Fatalf("o on a local issue said %q, want a note that it is local", v.note)
	}
}

// TestIssueRowTaskSummary is task 134.16 decision 3: the card's right cell
// per lane, from the row DTO alone.
func TestIssueRowTaskSummary(t *testing.T) {
	occ, running := int64(12), "running"
	busy := &apiclient.IssueMainWorktree{Branch: "b", OccupantTaskID: &occ, OccupantState: &running}
	free := &apiclient.IssueMainWorktree{Branch: "b"}
	for _, c := range []struct {
		name string
		iss  apiclient.Issue
		want string
	}{
		{"open, no tasks", apiclient.Issue{Lane: "open"}, "no tasks"},
		{"open, aborted only", apiclient.Issue{Lane: "open", TaskCount: 2}, "2 tasks · cancelled"},
		{
			"in progress, occupant",
			apiclient.Issue{Lane: "in_progress", TaskCount: 1, Active: true, MainWorktree: busy},
			"● #12 running",
		},
		{"in progress, side and merging", apiclient.Issue{
			Lane: "in_progress", TaskCount: 4, Active: true, MainWorktree: busy, SideActive: 2, MergeBacksPending: 1,
		}, "● #12 running · +2 side · 1 merging"},
		{
			"in progress, no occupant",
			apiclient.Issue{Lane: "in_progress", TaskCount: 2, Active: true, MainWorktree: free},
			"● 2 tasks",
		},
		{"in progress, no main branch", apiclient.Issue{Lane: "in_progress", TaskCount: 1, Active: true}, "● 1 task"},
		{"hand-off", apiclient.Issue{Lane: "hand_off", TaskCount: 3}, "✓ 3 done"},
		{"done", apiclient.Issue{Lane: "done", State: "closed", TaskCount: 2}, "2 tasks"},
		{"done, live", apiclient.Issue{Lane: "done", State: "closed", TaskCount: 1, Active: true}, "1 task · ● live"},
		{"done, no tasks", apiclient.Issue{Lane: "done", State: "closed"}, "no tasks"},
		{"no lane", apiclient.Issue{TaskCount: 2, Active: true}, "● 2 tasks"},
	} {
		if got := issueTaskSummary(c.iss); got != c.want {
			t.Errorf("%s: summary = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestIssueRowFitsAtTheFloor is review F2 on #773: an in-progress card with
// labels, a kind and a source badge stays within the 80-column floor, and
// what it gives up first is the occupant's state word, never the side and
// merging counts.
func TestIssueRowFitsAtTheFloor(t *testing.T) {
	occ, state := int64(123), "awaiting_input"
	iss := apiclient.Issue{
		ID: 12, State: "open", Title: "Crash on cold start", Labels: []string{"bug"}, Kind: "bug",
		Lane: "in_progress", TaskCount: 4, Active: true,
		MainWorktree:      &apiclient.IssueMainWorktree{Branch: "b", OccupantTaskID: &occ, OccupantState: &state},
		SideActive:        1,
		MergeBacksPending: 1,
	}
	for _, c := range []struct {
		name   string
		source *apiclient.IssueSource
	}{
		{"local", nil},
		{"imported", &apiclient.IssueSource{Repo: "octo-org/web-frontend", Number: 4127}},
	} {
		iss.Source = c.source
		for _, width := range []int{80, 76} {
			line := ansi.Strip(issueLine(iss, width, true))
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("%s at %d columns: the row is %d wide:\n%s", c.name, width, got, line)
			}
			if !strings.Contains(line, "● #123 · +1 side · 1 merging") {
				t.Errorf("%s at %d columns: the row lost its counts:\n%s", c.name, width, line)
			}
		}
	}
	// With room, nothing is given up.
	iss.Source = nil
	if line := ansi.Strip(issueLine(iss, 160, false)); !strings.Contains(line, "● #123 awaiting_input · +1 side · 1 merging") ||
		!strings.Contains(line, "[bug] bug") {
		t.Errorf("a wide row dropped a part:\n%s", line)
	}
}

func TestIssueRowShowsBadges(t *testing.T) {
	v := issuesFixture()
	out := ansi.Strip(v.render(160, 30))
	for _, want := range []string{"octo/web#41", "[p1]", "bug", "● 1 task", "open"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing %q:\n%s", want, out)
		}
	}
	closed := apiclient.Issue{State: "closed", CloseReason: "not_planned"}
	if got := issueStateBadge(closed); got != "closed · not planned" {
		t.Errorf("closed badge = %q", got)
	}
	dup := int64(9)
	if got := issueStateBadge(apiclient.Issue{State: "closed", CloseReason: "duplicate", DuplicateOf: &dup}); got != "closed · duplicate of #9" {
		t.Errorf("duplicate badge = %q", got)
	}
}

func TestIssuePriorityLabels(t *testing.T) {
	want := map[int]string{0: "none", 1: "urgent", 2: "high", 3: "medium", 4: "low"}
	for p, w := range want {
		if got := issuePriorityLabel(p); got != w {
			t.Errorf("priority %d = %q, want %q", p, got, w)
		}
	}
}

func TestIssueRawToggle(t *testing.T) {
	v := issueFixture()
	rendered := ansi.Strip(v.render(100, 60))
	if strings.Contains(rendered, "**details**") || !strings.Contains(rendered, "details") {
		t.Fatalf("the rendered view shows the Markdown punctuation:\n%s", rendered)
	}
	v.updateKey(registryKey(t, "ctrl+o"))
	raw := ansi.Strip(v.render(100, 60))
	if !strings.Contains(raw, "**details**") {
		t.Fatalf("the raw view lost the source:\n%s", raw)
	}
}

func TestIssueDetailShowsSourceAndTasks(t *testing.T) {
	v := issueFixture()
	v.issue.Body = "short"
	out := ansi.Strip(v.render(120, 60))
	for _, want := range []string{
		"#5", "Dark mode", "octo/web#41", "high", "3 tasks, 2 active",
		"#21", "dark mode, first pass", "https://github.com/octo/web/issues/41",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the detail is missing %q:\n%s", want, out)
		}
	}
}

func TestIssueDetailEscReturnsToTheList(t *testing.T) {
	v := issueFixture()
	_, cmd := v.updateKey(registryKey(t, "esc"))
	if cmd == nil {
		t.Fatal("esc did nothing")
	}
	if msg, ok := cmd().(selectViewMsg); !ok || msg.id != viewIssues {
		t.Fatalf("esc produced %#v, want the issues list", cmd())
	}
}

// A load issued for the project just left must not land once the selection
// is no project at all (review F1 on PR #720): the list would show another
// project's issues under no name.
func TestIssuesDeselectingDropsTheLoadInFlight(t *testing.T) {
	v := newIssuesView()
	v.client = deadClient()
	v.project = projectSel{id: 1, name: "api"}
	cmd := v.loadCmd()
	if cmd == nil {
		t.Fatal("a selected project issued no load")
	}
	inFlight, ok := cmd().(issuesLoadedMsg)
	if !ok {
		t.Fatal("the load did not answer with an issuesLoadedMsg")
	}
	inFlight.err = nil
	inFlight.issues = []apiclient.Issue{{ID: 1, Title: "stale"}}

	if cmd := v.setProject(projectSel{}); cmd != nil {
		t.Fatal("deselecting the project issued a load")
	}
	v.applyLoaded(inFlight)
	if len(v.issues) != 0 || v.loaded {
		t.Errorf("a load for the project just left was installed: %+v", v.issues)
	}
}

// The list re-lists on issue events, issue.lane_changed among them, and on a
// task event naming a shown issue — but not on a step advancing, nor on a
// task event for an issue it does not show or a task with none (task 134.6
// decision 6).
func TestIssuesListReListsOnlyForItsRows(t *testing.T) {
	pid := int64(1)
	ev := func(typ, payload string) apiclient.Note {
		return apiclient.EventNote{Event: apiclient.Event{Type: typ, ProjectID: &pid, Payload: []byte(payload)}}
	}
	for _, c := range []struct {
		name string
		note apiclient.Note
		want bool
	}{
		{"lane changed", ev("issue.lane_changed", `{"id":3,"from":"open","to":"in_progress","task_id":9}`), true},
		{"task event for a shown issue", ev("task.state_changed", `{"from":"running","to":"done","issue_id":4}`), true},
		{"task created for a shown issue", ev("task.created", `{"state":"queued","issue_id":5}`), true},
		{"project event", ev("project.updated", `{}`), true},
		{"step advanced", ev("task.step_advanced", `{"current_step":2}`), false},
		{"task event for another issue", ev("task.state_changed", `{"from":"running","to":"done","issue_id":99}`), false},
		{"task event with no issue", ev("task.state_changed", `{"from":"running","to":"done"}`), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := issuesFixture()
			if got := v.updateNote(c.note) != nil; got != c.want {
				t.Errorf("re-listed = %v, want %v", got, c.want)
			}
		})
	}
}
