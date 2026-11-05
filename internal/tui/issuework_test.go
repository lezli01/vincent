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

// Task 144 (#933): the issue detail's `Issue | Work` strip. Pure model
// tests, one property each.

// workFixture is issueFixture with an archived fan-out parent and a chat in
// every state, opened on the issue or on one of its tasks.
func workFixture() *issueView {
	v := issueFixture()
	archived := time.Now()
	v.tasks = append(v.tasks, apiclient.Task{
		ID: 20, ProjectID: 2, Title: "fan it out", State: stateDone, ArchivedAt: &archived,
		Children: &apiclient.ChildrenRollup{Total: 3, Settled: 2},
	})
	issue, task := i64p(5), i64p(21)
	at := func(m int) time.Time { return time.Date(2026, 11, 1, 10, m, 0, 0, time.UTC) }
	v.chats = []apiclient.Chat{
		{ID: 40, ProjectID: 2, Title: "oldest", State: "closed", IssueID: issue, CreatedAt: at(0)},
		{ID: 41, ProjectID: 2, Title: "on the task", State: "closed", LinkedTaskID: task, CreatedAt: at(1)},
		{ID: 42, ProjectID: 2, Title: "shelved", State: "archived", IssueID: issue, CreatedAt: at(2)},
		{ID: 43, ProjectID: 2, Title: "passed on", State: "handed_off", IssueID: issue, HandoffTaskID: i64p(22), CreatedAt: at(3)},
		{ID: 44, ProjectID: 2, Title: "asking", State: "awaiting_input", LinkedTaskID: task, CreatedAt: at(4)},
		{ID: 45, ProjectID: 2, Title: "newest", State: "running", IssueID: issue, CreatedAt: at(5)},
	}
	return v
}

func TestIssueStripSwitchesWithEveryKeyAndWraps(t *testing.T) {
	v := workFixture()
	for _, c := range []struct {
		key  tea.KeyPressMsg
		want issueTab
	}{
		{tea.KeyPressMsg{Code: tea.KeyTab}, issueTabWork},
		{tea.KeyPressMsg{Code: tea.KeyTab}, issueTabIssue}, // wraps
		{key("]"), issueTabWork},
		{key("]"), issueTabIssue},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, issueTabWork}, // wraps back
		{key("["), issueTabIssue},
	} {
		v.update(c.key)
		if v.tab != c.want {
			t.Fatalf("%q: tab %v, want %v", c.key.String(), v.tab, c.want)
		}
	}
	// There are no digit keys (decision 2).
	v.update(key("2"))
	if v.tab != issueTabIssue {
		t.Errorf("2 moved the strip to %v", v.tab)
	}
}

func TestIssueStripClickSelectsATab(t *testing.T) {
	v := workFixture()
	lines := renderedLines(v, 120, 60)
	if got := ansi.Strip(lines[0]); !strings.HasPrefix(got, "  Issue │ Work") {
		t.Fatalf("the strip is not the first line: %q", got)
	}
	var work issueTabHit
	for _, h := range v.tabHits {
		if h.tab == issueTabWork {
			work = h
		}
	}
	v.update(tea.MouseClickMsg{X: work.x0, Y: issueFrameTop, Button: tea.MouseLeft})
	if v.tab != issueTabWork {
		t.Fatalf("a click on Work at x=%d left tab %v", work.x0, v.tab)
	}
	v.render(120, 60)
	v.update(tea.MouseClickMsg{X: v.tabHits[0].x0, Y: issueFrameTop, Button: tea.MouseLeft})
	if v.tab != issueTabIssue {
		t.Fatalf("a click on Issue left tab %v", v.tab)
	}
}

// The form and the prompt own the keyboard and the mouse: the strip stays.
func TestIssueStripStaysWhileAPromptOrTheFormIsOpen(t *testing.T) {
	v := workFixture()
	v.render(120, 60)
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyTab}, key("]"), key("[")} {
		v.w.act = newIssueDelete(v.client, v.issue)
		v.update(k)
		if v.tab != issueTabIssue {
			t.Fatalf("%q moved the strip to %v under the delete prompt", k.String(), v.tab)
		}
	}
	v.w.act = newIssueDelete(v.client, v.issue)
	v.update(tea.MouseClickMsg{X: v.tabHits[1].x0, Y: issueFrameTop, Button: tea.MouseLeft})
	if v.tab != issueTabIssue {
		t.Fatalf("a click moved the strip to %v under the delete prompt", v.tab)
	}
	v.w.act = nil
	iss := v.issue
	v.w.openForm(v.client, &iss, iss.ProjectID)
	if !v.capturesInput() {
		t.Fatal("fixture: the form is not open")
	}
	v.update(tea.KeyPressMsg{Code: tea.KeyTab})
	if v.tab != issueTabIssue {
		t.Fatalf("tab in the form moved the strip to %v", v.tab)
	}
}

func TestIssueTabEnterOnTheWorkLineSelectsWork(t *testing.T) {
	v := workFixture()
	v.issue.Body = "short"
	out := ansi.Strip(v.render(120, 60))
	if !strings.Contains(out, "3 tasks (2 active), 6 chats — tab for the work") {
		t.Fatalf("the Issue tab has no Work line:\n%s", out)
	}
	if strings.Contains(out, "fan it out") || strings.Contains(out, "chat #") {
		t.Errorf("the Issue tab lists rows the Work tab holds:\n%s", out)
	}
	rows := v.selectRows()
	if len(rows) != 1 || !rows[0].workLine {
		t.Fatalf("the Issue tab's rows = %+v, want the Work line alone", rows)
	}
	if _, cmd := v.updateKey(registryKey(t, "enter")); cmd != nil {
		t.Fatalf("enter on the Work line produced %#v", cmd())
	}
	if v.tab != issueTabWork {
		t.Fatalf("enter on the Work line left tab %v", v.tab)
	}
}

func TestIssueWorkTabRendersTasksAndChats(t *testing.T) {
	v := workFixture()
	v.setTab(issueTabWork)
	out := ansi.Strip(v.render(140, 60))
	for _, want := range []string{
		"Tasks  3 tasks, 2 active",
		"✓ #20  fan it out  done · archived · lanes 2/3",
		"Chats  6 chats",
		"● chat #45  newest  on the issue · running",
		"! chat #44  asking  on task #21 · waiting on you",
		"✓ chat #43  passed on  on the issue · handed off → #22",
		"■ chat #42  shelved  on the issue · archived",
		"■ chat #41  on the task  on task #21 · closed",
		"■ chat #40  oldest  on the issue · closed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the Work tab is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Description") {
		t.Errorf("the Work tab shows the description:\n%s", out)
	}
	// Newest first: #45 above #40.
	if strings.Index(out, "chat #45") > strings.Index(out, "chat #40") {
		t.Errorf("the chats are not newest first:\n%s", out)
	}
}

func TestIssueWorkTabEmptyGroupsSaySo(t *testing.T) {
	v := issueFixture()
	v.tasks = nil
	v.setTab(issueTabWork)
	out := ansi.Strip(v.render(120, 40))
	for _, want := range []string{"no task yet", "no chat yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty Work tab is missing %q:\n%s", want, out)
		}
	}
}

func TestIssueWorkTabEnterOpensEachKindOfRow(t *testing.T) {
	v := workFixture()
	v.setTab(issueTabWork)
	v.render(140, 60)
	rows := v.selectRows()
	seen := map[string]bool{}
	for i, row := range rows {
		v.cursor = i
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd == nil {
			t.Fatalf("enter on row %d opened nothing", i)
		}
		switch msg := cmd().(type) {
		case selectTaskMsg:
			if row.chat != nil || msg.id != row.task.ID || msg.back != viewIssue {
				t.Errorf("row %d: %#v, want task #%d returning to the issue", i, msg, row.task.ID)
			}
			seen["task"] = true
		case openChatMsg:
			if row.chat == nil || msg.id != row.chat.ID || msg.back != viewIssue || msg.projectID != 2 {
				t.Errorf("row %d: %#v, want its chat returning to the issue", i, msg)
			}
			seen["chat"] = true
		default:
			t.Errorf("row %d: enter produced %#v", i, msg)
		}
	}
	if !seen["task"] || !seen["chat"] {
		t.Errorf("rows opened %v, want both kinds", seen)
	}
}

// The tab survives a re-read of the same issue — esc back from a task or
// chat keeps the view — and resets when another issue is opened.
func TestIssueTabSurvivesAReloadAndResetsOnAnotherIssue(t *testing.T) {
	v := workFixture()
	v.setTab(issueTabWork)
	v.open(5)
	v.applyLoaded(issueLoadedMsg{id: 5, issue: v.issue, tasks: v.tasks, chats: v.chats})
	if v.tab != issueTabWork {
		t.Fatalf("a same-id reload reset the tab to %v", v.tab)
	}
	v.open(6)
	if v.tab != issueTabIssue || v.chats != nil {
		t.Fatalf("opening another issue left tab %v and %d chats", v.tab, len(v.chats))
	}
}

func TestIssueRefreshesOnItsChatsEvents(t *testing.T) {
	ev := func(payload string) apiclient.Note {
		return apiclient.EventNote{Event: apiclient.Event{Type: "chat.state_changed", Payload: []byte(payload)}}
	}
	for _, c := range []struct {
		name    string
		payload string
		want    bool
	}{
		{"a listed chat", `{"id":41,"state":"closed"}`, true},
		{"a chat linked to a listed task", `{"id":99,"state":"idle","linked_task_id":22}`, true},
		{"another chat", `{"id":99,"state":"idle"}`, false},
		{"a chat linked to another task", `{"id":99,"state":"idle","linked_task_id":7}`, false},
	} {
		v := workFixture()
		if got := v.updateNote(ev(c.payload)) != nil; got != c.want {
			t.Errorf("%s: refresh %v, want %v", c.name, got, c.want)
		}
	}
}

// A chat listing that failed degrades to a note in the tab, never an error
// over the issue.
func TestIssueFailedChatListingDegrades(t *testing.T) {
	v := workFixture()
	v.applyLoaded(issueLoadedMsg{id: 5, issue: v.issue, tasks: v.tasks, chatsErr: errors.New("boom")})
	if v.loadErr != nil || !v.loaded {
		t.Fatalf("a failed chat listing failed the screen: %v", v.loadErr)
	}
	v.issue.Body = "short"
	if out := ansi.Strip(v.render(120, 60)); !strings.Contains(out, "3 tasks (2 active), chats unavailable") {
		t.Errorf("the Work line does not say the chats are unavailable:\n%s", out)
	}
	v.setTab(issueTabWork)
	out := ansi.Strip(v.render(120, 60))
	if !strings.Contains(out, "chats unavailable") || strings.Contains(out, "boom") {
		t.Errorf("the Work tab does not degrade to a note:\n%s", out)
	}
	if !strings.Contains(out, "dark mode, first pass") {
		t.Errorf("the tasks went with the chats:\n%s", out)
	}
}

// Review F1: a click on the strip and on a Work-tab row lands where the root
// draws them. The root takes only its header rows off a click's Y, so the
// pane's own frame border is still in it; driving the click through
// root.Update against the drawn screen pins both at real coordinates.
func TestIssueClicksThroughTheRootHitTheDrawnScreen(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	v := issueFixture()
	m.views[viewIssue] = v
	m.active = viewIssue

	screen := strings.Split(ansi.Strip(content(m)), "\n")
	row := func(text string) (x, y int) {
		t.Helper()
		for i, line := range screen {
			if at := strings.Index(line, text); at >= 0 {
				return ansi.StringWidth(line[:at]), i
			}
		}
		t.Fatalf("%q is not on screen:\n%s", text, strings.Join(screen, "\n"))
		return 0, 0
	}
	x, y := row("Work")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if v.tab != issueTabWork {
		t.Fatalf("a click on Work at screen (%d,%d) left tab %v", x, y, v.tab)
	}

	screen = strings.Split(ansi.Strip(content(m)), "\n")
	x, y = row("dark mode, review")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	rows := v.selectRows()
	if v.cursor >= len(rows) || rows[v.cursor].task.ID != 22 {
		t.Fatalf("a click on task #22 at screen row %d left cursor %d", y, v.cursor)
	}

	x, y = row("Issue │")
	m.Update(tea.MouseClickMsg{X: x + 2, Y: y, Button: tea.MouseLeft})
	if v.tab != issueTabIssue {
		t.Fatalf("a click on Issue at screen row %d left tab %v", y, v.tab)
	}
}

// TestIssueWorkArchivedTaskSaysArchivedOnce is review F3: an archived task's
// state is `archived`, so the row does not repeat it as `archived · archived`.
func TestIssueWorkArchivedTaskSaysArchivedOnce(t *testing.T) {
	v := issueFixture()
	archived := time.Now()
	v.tasks = append(v.tasks, apiclient.Task{
		ID: 30, ProjectID: 2, Title: "put away", State: stateArchived, ArchivedAt: &archived,
	})
	v.setTab(issueTabWork)
	out := ansi.Strip(v.render(140, 60))
	if strings.Contains(out, "archived · archived") {
		t.Errorf("an archived task says archived twice:\n%s", out)
	}
	if !strings.Contains(out, "#30  put away  archived") {
		t.Errorf("the archived task's row is missing:\n%s", out)
	}
}
