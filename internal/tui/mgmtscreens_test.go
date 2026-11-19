package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The management screens' wide layouts (task 150.7). Pure model tests, one
// property each (task 129 decision 7).

// assertFills holds a layout to exactly the screen it was given.
func assertFills(t *testing.T, out string, width, height int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != height {
		t.Errorf("drew %d lines, want %d", len(lines), height)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > width {
			t.Errorf("line %d is %d wide, over %d", i, got, width)
		}
	}
}

func wideProjects(t *testing.T) *projectsView {
	t.Helper()
	p := newProjectsView()
	p.now = func() time.Time { return taskBoardNow }
	p.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	for i, name := range []string{"api", "web", "docs-portal", "platform-infra", "mobile-app"} {
		p.projects = append(p.projects, apiclient.Project{ID: int64(i + 1), Name: name})
	}
	p.tasks = []apiclient.Task{{ID: 6, ProjectName: "api", Title: "verify the checksums", State: stateBlocked}}
	p.loaded = true
	return p
}

func TestProjectsWideDrawsCards(t *testing.T) {
	p := wideProjects(t)
	out := p.render(180, 44)
	assertFills(t, out, 180, 44)
	plain := ansi.Strip(out)
	for _, want := range []string{"◆ Projects", "◆ api", "◆ platform-infra", "! Needs you, across projects", "api #6", "verify the checksums"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the screen is missing %q:\n%s", want, plain)
		}
	}
}

// The arrows walk the grid: sideways by one card, up and down by a row.
func TestProjectsWideArrowsWalkTheGrid(t *testing.T) {
	p := wideProjects(t)
	p.render(180, 44)
	if p.perRow < 2 {
		t.Fatalf("a wide terminal fits %d cards a row", p.perRow)
	}
	p.updateKey(keyPress("right"))
	if got := p.tbl.Cursor(); got != 1 {
		t.Fatalf("right moved to %d, want 1", got)
	}
	p.updateKey(keyPress("left"))
	p.updateKey(keyPress("down"))
	if got := p.tbl.Cursor(); got != p.perRow {
		t.Fatalf("down moved to %d, want the next row's %d", got, p.perRow)
	}
}

func TestProjectsWideClickSelectsACard(t *testing.T) {
	p := wideProjects(t)
	p.render(180, 44)
	hit := p.hits[1]
	p.update(tea.MouseClickMsg{X: hit.x0, Y: hit.y0, Button: tea.MouseLeft})
	if got := p.tbl.Cursor(); got != 1 {
		t.Fatalf("the click selected %d, want 1", got)
	}
}

// A narrow terminal and an open form keep the layouts they have always had.
func TestProjectsWideGivesWayToTheForm(t *testing.T) {
	p := wideProjects(t)
	if !p.frameless() {
		t.Fatal("the wide layout asked for the root's frame")
	}
	p.form = newProjectForm(nil, nil)
	if p.frameless() {
		t.Error("the form lost the root's frame")
	}
	p.form = nil
	p.update(tea.WindowSizeMsg{Width: mgmtWideMinW - 1, Height: 44})
	if p.frameless() {
		t.Error("a narrow terminal drew the wide layout")
	}
}

func widePulls(t *testing.T) *pullRequestsView {
	t.Helper()
	claimed := int64(2)
	v := newPullRequestsView()
	v.client = offlineClient()
	v.now = func() time.Time { return taskBoardNow }
	v.project = projectSel{id: 1, name: "web"}
	v.probes = []githubProject{{project: apiclient.Project{ID: 1}, status: apiclient.GitHubStatus{Available: true}}}
	v.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	v.pulls = []apiclient.GitHubPullRequest{
		{Number: 401, Title: "rework the header", State: "open", Draft: true, HeadBranch: "rework", BaseBranch: "main"},
		{Number: 412, Title: "bump the tokens", State: "open", HeadBranch: "bump", BaseBranch: "main", TaskID: &claimed},
		{Number: 355, Title: "fix a typo", State: "open", HeadBranch: "typo", BaseBranch: "main"},
	}
	v.tasks = []apiclient.Task{{ID: 2, Title: "bump the design tokens", State: stateDone}}
	v.loaded = true
	return v
}

func TestPullRequestsWideSplitsByClaim(t *testing.T) {
	v := widePulls(t)
	out := v.render(180, 44)
	assertFills(t, out, 180, 44)
	plain := ansi.Strip(out)
	for _, want := range []string{"▶ Linked to a task", "○ Unclaimed", "#412", "⎇ bump → main", "▶ task #2"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the screen is missing %q:\n%s", want, plain)
		}
	}
}

// ↑ and ↓ stay in a column, and ← and → cross it.
func TestPullRequestsWideKeys(t *testing.T) {
	v := widePulls(t)
	v.cursor = 0 // #401, unclaimed
	v.updateKey(keyPress("down"))
	if got := v.rows()[v.cursor].pull.Number; got != 355 {
		t.Fatalf("down went to #%d, want #355", got)
	}
	v.updateKey(keyPress("left"))
	if got := v.rows()[v.cursor].pull.Number; got != 412 {
		t.Fatalf("left went to #%d, want #412", got)
	}
}

// Checks are read through the task that claims a pull request, once the
// selection rests there.
func TestPullRequestPreviewReadsChecks(t *testing.T) {
	v := widePulls(t)
	v.cursor = 0
	if v.checksCmd() != nil {
		t.Fatal("an unclaimed pull request armed a read")
	}
	if out := ansi.Strip(strings.Join(v.pullPreview(50, 30), "\n")); !strings.Contains(out, "Link a task to read its checks.") {
		t.Errorf("an unclaimed preview does not say why it has no checks:\n%s", out)
	}
	v.cursor = 1
	if v.checksCmd() == nil {
		t.Fatal("a claimed pull request armed no read")
	}
	v.applyChecks(pullChecksMsg{taskID: 2, runs: []apiclient.GitHubCheckRun{{Name: "build", State: "success"}, {Name: "e2e", State: "failure"}}})
	out := ansi.Strip(strings.Join(v.pullPreview(50, 30), "\n"))
	for _, want := range []string{"✓ build", "✗ e2e", "#2 bump the design tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview is missing %q:\n%s", want, out)
		}
	}
	if v.checksCmd() != nil {
		t.Fatal("checks already held were read again")
	}
}

func TestPullRequestPreviewSaysWhenChecksFail(t *testing.T) {
	v := widePulls(t)
	v.cursor = 1
	v.applyChecks(pullChecksMsg{taskID: 2, err: errors.New("github is unreachable")})
	if out := ansi.Strip(strings.Join(v.pullPreview(50, 30), "\n")); !strings.Contains(out, "github is unreachable") {
		t.Errorf("a failed read is not reported:\n%s", out)
	}
}

func wideTriggers(t *testing.T) *triggersView {
	t.Helper()
	v := newTriggersView()
	v.now = func() time.Time { return taskBoardNow }
	v.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	v.list = apiclient.TriggerList{Enabled: true, Dir: "/config/triggers", Triggers: []apiclient.TriggerSummary{
		{ID: "ci-red", Valid: true, Enabled: true, Armed: true, SourceType: "command", ActionType: "create_task", OnFire: "propose"},
		{ID: "nightly", Valid: true, SourceType: "schedule", ActionType: "create_task"},
	}}
	v.loaded = true
	return v
}

func TestTriggersWideDrawsCardsAndDetails(t *testing.T) {
	v := wideTriggers(t)
	out := v.render(180, 44)
	assertFills(t, out, 180, 44)
	plain := ansi.Strip(out)
	for _, want := range []string{"⚡ Triggers", "⚡ ci-red", "● armed", "command → create_task · propose", "⚡ nightly", "deliveries · ci-red"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the screen is missing %q:\n%s", want, plain)
		}
	}
}

// The archive's two halves are one screen: tab crosses between them.
func TestArchiveTabSwitchesBetweenTasksAndChats(t *testing.T) {
	b := testArchivedBoard()
	_, cmd := b.updateKey(keyPress("tab"))
	if msg, ok := drain(cmd).(selectViewMsg); !ok || msg.id != viewArchivedChats {
		t.Fatalf("tab on archived tasks produced %#v", drain(cmd))
	}
	v := archivedChatsFixture()
	_, cmd = v.updateKey(keyPress("tab"))
	if msg, ok := drain(cmd).(selectViewMsg); !ok || msg.id != viewArchived {
		t.Fatalf("tab on archived chats produced %#v", drain(cmd))
	}
}

// enter opens an archived task, and esc comes back to the archive.
func TestArchiveEnterOpensTheTask(t *testing.T) {
	b := testArchivedBoard()
	b.project = projectSel{id: 1, name: "api"}
	b.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	b.tasks = []apiclient.Task{{ID: 14, ProjectID: 1, Title: "retire the v1 reference", State: "archived"}}
	_, cmd := b.updateKey(keyPress("enter"))
	if msg, ok := drain(cmd).(selectTaskMsg); !ok || msg.id != 14 || msg.back != viewArchived {
		t.Fatalf("enter produced %#v, want task #14 opened from the archive", drain(cmd))
	}
}

// The wide archive is cards grouped by issue, with nothing folded away.
func TestArchiveWideDrawsCards(t *testing.T) {
	b := testArchivedBoard()
	b.project = projectSel{id: 1, name: "api"}
	b.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	b.tasks = []apiclient.Task{
		{ID: 14, ProjectID: 1, Title: "retire the v1 reference", State: "archived", Workflow: "docs-refresh"},
		{ID: 12, ProjectID: 1, Title: "roll back the change", State: "archived", Issue: &apiclient.TaskIssue{ID: 11, Title: "DNS broke"}},
	}
	b.loaded = true
	if !b.frameless() {
		t.Fatal("the wide archive asked for the root's frame")
	}
	out := ansi.Strip(b.render(180, 44))
	for _, want := range []string{"▣ Archived tasks", "▶ Tasks", "◌ Chats", "▦ #11 DNS broke", "retire the v1 reference", "roll back the change"} {
		if !strings.Contains(out, want) {
			t.Errorf("the archive is missing %q:\n%s", want, out)
		}
	}
}

func wideArchive(t *testing.T) *board {
	t.Helper()
	b := testArchivedBoard()
	b.project = projectSel{id: 1, name: "api"}
	b.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	b.tasks = []apiclient.Task{
		{ID: 14, ProjectID: 1, Title: "retire the v1 reference", State: "archived"},
		{ID: 13, ProjectID: 1, Title: "drop the old shim", State: "archived"},
		{ID: 10, ProjectID: 1, Title: "trim the changelog", State: "archived"},
		{ID: 12, ProjectID: 1, Title: "roll back the change", State: "archived", Issue: &apiclient.TaskIssue{ID: 11, Title: "DNS broke"}},
	}
	b.loaded = true
	b.render(180, 44)
	return b
}

// The arrows walk the archive's grid as it is drawn.
func TestArchiveWideArrowsWalkTheGrid(t *testing.T) {
	b := wideArchive(t)
	if b.cardPerRow != 2 {
		t.Fatalf("a wide archive fits %d cards a row", b.cardPerRow)
	}
	var free []taskStop
	for _, s := range cardStops(b.cardGroups())[taskColumnFinished] {
		if s.key == groupNoIssue {
			free = append(free, s)
		}
	}
	if len(free) != 3 {
		t.Fatalf("the No issue group holds %d stops, want 3", len(free))
	}
	selected := func() int64 {
		id, _ := b.selected()
		return id
	}
	b.restOn(free[0])
	b.updateKey(keyPress("right"))
	if got := selected(); got != free[1].id {
		t.Fatalf("right moved to #%d, want #%d", got, free[1].id)
	}
	b.updateKey(keyPress("left"))
	b.updateKey(keyPress("down"))
	if got := selected(); got != free[2].id {
		t.Fatalf("down moved to #%d, want the next row's #%d", got, free[2].id)
	}
}

func TestArchiveWideNamesEveryWindow(t *testing.T) {
	out := ansi.Strip(wideArchive(t).render(180, 44))
	for _, w := range archivedWindows {
		if !strings.Contains(out, w.label) {
			t.Errorf("the switch is missing %q:\n%s", w.label, out)
		}
	}
}

func TestProjectsWideBadgesTheCurrentProject(t *testing.T) {
	p := wideProjects(t)
	if strings.Contains(ansi.Strip(p.render(180, 44)), " current ") {
		t.Fatal("a badge was drawn with no project selected")
	}
	p.setCurrentProject(2)
	if out := ansi.Strip(p.render(180, 44)); !strings.Contains(out, "◆ web  current") {
		t.Errorf("the selected project has no badge:\n%s", out)
	}
}
