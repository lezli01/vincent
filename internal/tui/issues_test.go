package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Pure model tests for the issue screens (§15 views 12 and 13, task 130.9).
// No golden frames (task 129 decision 7): each asserts one property.

// issuesFixture is the list on project "api" with three issues: a local one,
// an imported one, and a second local one, in the daemon's order.
func issuesFixture() *issuesView {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	v.issues = []apiclient.Issue{
		{ID: 3, ProjectID: 1, Title: "Crash on start", State: "open", Kind: "bug", Labels: []string{"p1"}, TaskCount: 2, Active: true},
		{
			ID: 5, ProjectID: 1, Title: "Dark mode", State: "open", Kind: "feature",
			Source: &apiclient.IssueSource{Provider: "github", Repo: "octo/web", Number: 41, URL: "https://github.com/octo/web/issues/41"},
		},
		{ID: 4, ProjectID: 1, Title: "Docs typo", State: "open", Labels: []string{"docs"}, TaskCount: 1},
	}
	v.loaded = true
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

func TestIssuesScopeCycles(t *testing.T) {
	v := issuesFixture()
	var seen []string
	for range 4 {
		v.updateKey(registryKey(t, "s"))
		seen = append(seen, v.state)
	}
	if got := strings.Join(seen, ","); got != "closed,all,open,closed" {
		t.Fatalf("s walked %s, want closed,all,open,closed", got)
	}
}

// The list is flat (task 132.11): the daemon's order, no project headings.
func TestIssuesListIsFlatInTheDaemonsOrder(t *testing.T) {
	v := issuesFixture()
	var got []string
	for _, row := range v.rows() {
		got = append(got, "#"+strconv.FormatInt(row.issue.ID, 10))
	}
	if want := "#3,#5,#4"; strings.Join(got, ",") != want {
		t.Fatalf("rows = %v, want %s", got, want)
	}
	out := ansi.Strip(v.render(140, 30))
	if strings.Contains(out, "across") || strings.Contains(out, "\n api") {
		t.Errorf("the list still groups by project:\n%s", out)
	}
	if !strings.Contains(out, "3 issues") {
		t.Errorf("the header does not count the project's issues:\n%s", out)
	}
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
	if !strings.Contains(out, "No project selected") || !strings.Contains(out, "project overview") {
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
		"":      {3, 5, 4}, // no filter
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

func TestIssueRowTaskSummary(t *testing.T) {
	for _, c := range []struct {
		count  int
		active bool
		want   string
	}{
		{0, false, "no tasks"},
		{1, false, "1 task"},
		{2, true, "● 2 tasks"},
	} {
		if got := issueTaskSummary(apiclient.Issue{TaskCount: c.count, Active: c.active}); got != c.want {
			t.Errorf("summary(%d, %v) = %q, want %q", c.count, c.active, got, c.want)
		}
	}
}

func TestIssueRowShowsBadges(t *testing.T) {
	v := issuesFixture()
	out := ansi.Strip(v.render(160, 30))
	for _, want := range []string{"octo/web#41", "[p1]", "bug", "● 2 tasks", "open"} {
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
