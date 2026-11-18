package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The wide new-task and new-chat screens (task 150.11). Pure model tests, one
// property each.

func wideTask(t *testing.T) *newTask {
	t.Helper()
	n := newNewTask()
	n.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	n.selected = 1
	n.applyLoaded(ntLoadedMsg{
		projects: []apiclient.Project{{ID: 1, Name: "api", DefaultBranch: "main"}},
		workflows: []apiclient.WorkflowEntry{
			{
				Name: "feature-pr", Scope: "project", Description: "Plan, build and open a pull request.",
				Steps: []apiclient.WorkflowEntryStep{
					{ID: "plan", Type: "agent", Agent: "claude"},
					{ID: "review", Type: "manual"},
				},
				Fields: []apiclient.WorkflowField{{Name: "ticket", Type: apiclient.WorkflowFieldString, Required: true}},
			},
			{Name: "hotfix", Scope: "global", Description: "A small fix."},
			{Name: "adhoc", Scope: "builtin", Description: "One agent step."},
		},
		agents: apiclient.Agents{
			{Name: "claude", Available: true, Efforts: []apiclient.AgentOption{{Value: "high"}}},
			{Name: "cursor", Available: true},
		},
	})
	n.setWorkflow("feature-pr")
	return n
}

// Everything the draft decides is on one screen, exactly the size it was given.
func TestNewTaskWideShowsTheWholeDraft(t *testing.T) {
	n := wideTask(t)
	lines := strings.Split(n.render(180, 44), "\n")
	if len(lines) != 44 {
		t.Fatalf("drew %d lines, want 44", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 180 {
			t.Errorf("line %d is %d wide", i, got)
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{
		"Workflow", "This project", "Global", "Built-in", "feature-pr", "2 steps · 1 gate",
		"Title", "Description", "Fields", "ticket", "Branch", "Start", "What will run",
		"waits for you", "ticket is required", "2 to fill",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "of 6") {
		t.Errorf("the wide screen still draws the guided stages:\n%s", out)
	}
}

// A terminal too small for three panes keeps the guided form.
func TestNewTaskNarrowKeepsTheGuidedForm(t *testing.T) {
	n := wideTask(t)
	n.update(tea.WindowSizeMsg{Width: mgmtWideMinW - 1, Height: 44})
	if n.frameless() {
		t.Fatal("a narrow terminal asked for the wide form")
	}
	if out := ansi.Strip(n.render(mgmtWideMinW-1, 44)); !strings.Contains(out, "of 6") {
		t.Errorf("the narrow form lost its stages:\n%s", out)
	}
}

// The section the cursor is in has the thick border.
func TestNewTaskWideMarksTheFocusedSection(t *testing.T) {
	n := wideTask(t)
	n.cursor = ntTitle
	out := ansi.Strip(n.render(180, 44))
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Title") && strings.Contains(line, "┏") {
			return
		}
	}
	t.Errorf("the focused title section is not thick:\n%s", out)
}

// The list's cursor walks the cards in the order they are drawn, whatever order
// the registry answered in.
func TestNewTaskWorkflowsStepInDrawnOrder(t *testing.T) {
	n := wideTask(t)
	n.workflows = []apiclient.WorkflowEntry{
		{Name: "adhoc", Scope: "builtin"},
		{Name: "hotfix", Scope: "global"},
		{Name: "feature-pr", Scope: "project"},
		{Name: "default", Scope: "builtin"},
		{Name: "broken", Scope: "global", Errors: []apiclient.WorkflowFinding{{Message: "bad"}}},
	}
	n.workflow, n.cursor = "feature-pr", ntWorkflow
	for _, want := range []string{"hotfix", "adhoc", "default", "default"} {
		press(n, "down")
		if n.workflow != want {
			t.Fatalf("down landed on %q, want %q", n.workflow, want)
		}
	}
	for _, want := range []string{"adhoc", "hotfix", "feature-pr", "feature-pr"} {
		press(n, "up")
		if n.workflow != want {
			t.Fatalf("up landed on %q, want %q", n.workflow, want)
		}
	}
	if n.cursor != ntWorkflow {
		t.Errorf("choosing a workflow moved the cursor to %v", n.cursor)
	}
}

// The workflow list is to the left of the form, and the arrows say so.
func TestNewTaskArrowsMoveBetweenTheListAndTheForm(t *testing.T) {
	n := wideTask(t)
	n.cursor = ntDescription
	press(n, "left")
	if n.cursor != ntWorkflow {
		t.Fatalf("← left the cursor on %v", n.cursor)
	}
	press(n, "right")
	if n.cursor != ntDescription {
		t.Fatalf("→ came back to %v, want the field ← left", n.cursor)
	}
	n.cursor = ntTitle
	press(n, "up")
	if n.cursor != ntTitle {
		t.Errorf("↑ from the top of the form moved to %v", n.cursor)
	}
	n.cursor = ntWorkflow
	n.formRow = ntProject
	press(n, "enter")
	if n.cursor != ntTitle || n.mode != ntNavigating {
		t.Errorf("enter on the list went to %v in mode %v, want the title", n.cursor, n.mode)
	}
	n.cursor = ntTitle
	press(n, "shift+tab")
	if n.cursor != ntWorkflow {
		t.Errorf("shift+tab went to %v: tab still walks every field", n.cursor)
	}
}

// / starts narrowing the list straight away.
func TestNewTaskSlashFiltersTheWorkflowList(t *testing.T) {
	n := wideTask(t)
	n.cursor = ntWorkflow
	press(n, "/")
	if n.mode != ntPicking || n.pick == nil || !n.pick.filtering {
		t.Fatalf("/ left mode %v, pick %+v", n.mode, n.pick)
	}
}

// A narrow form keeps its keys: the arrows there mean what they always meant.
func TestNewTaskNarrowKeepsItsArrows(t *testing.T) {
	n := wideTask(t)
	n.update(tea.WindowSizeMsg{Width: mgmtWideMinW - 1, Height: 44})
	n.cursor = ntWorkflow
	press(n, "down")
	if n.cursor == ntWorkflow || n.workflow != "feature-pr" {
		t.Errorf("↓ on a narrow form left row %v on workflow %q", n.cursor, n.workflow)
	}
	for _, b := range n.liveBindings(bindingsFor(ctxNewTask)) {
		if b.wideForm {
			t.Errorf("a narrow form offers %q", b.key)
		}
	}
}

// An agent with no effort levels has no effort row to walk onto.
func TestNewTaskHidesEffortForAnAgentWithoutOne(t *testing.T) {
	n := wideTask(t)
	n.agent = "claude"
	if !n.rowVisible(ntEffort) {
		t.Error("claude has effort levels and lost the row")
	}
	n.agent = "cursor"
	if n.rowVisible(ntEffort) {
		t.Error("cursor has no effort levels and kept the row")
	}
	n.agent = ""
	if !n.rowVisible(ntEffort) {
		t.Error("with no agent chosen the workflow decides, so the row stays")
	}
}

func wideChat(t *testing.T) *chatsView {
	t.Helper()
	v := chatWorkspaceFixture(t)
	v.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	v.create = newNewChatForm(nil, 1)
	loggedIn := true
	v.create.agents = apiclient.Agents{
		{
			Name: "claude", Available: true, LoggedIn: &loggedIn, Version: "2.1.4",
			Models:  []apiclient.AgentOption{{Value: "opus"}, {Value: "sonnet"}},
			Efforts: []apiclient.AgentOption{{Value: "low"}, {Value: "high"}},
		},
		{Name: "cursor", Available: true, LoggedIn: &loggedIn, Models: manyModels(40)},
		{Name: "codex", Available: true, LoggedIn: &loggedIn},
		{Name: "copilot", Available: true},
	}
	return v
}

func manyModels(n int) []apiclient.AgentOption {
	out := make([]apiclient.AgentOption, n)
	for i := range out {
		out[i] = apiclient.AgentOption{Value: "model-" + string(rune('a'+i%26))}
	}
	return out
}

// The form sits between the chat list and what the chat would get.
func TestNewChatWideLayout(t *testing.T) {
	v := wideChat(t)
	if !v.frameless() {
		t.Fatal("the wide form asked for the root's frame")
	}
	lines := strings.Split(v.render(180, 44), "\n")
	if len(lines) != 44 {
		t.Fatalf("drew %d lines, want 44", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 180 {
			t.Errorf("line %d is %d wide", i, got)
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{
		"A new conversation", "Waiting on you", "Title", "Agent", "✓ signed in", "sign-in unknown",
		"Model", "opus", "Effort", "A new branch", "A branch that exists", "This chat", "full-auto",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
}

// The draft's card stays at the top of a list too long for the pane.
func TestNewChatDraftCardStaysOnScreen(t *testing.T) {
	v := wideChat(t)
	v.update(tea.WindowSizeMsg{Width: 180, Height: formWideMinH})
	out := ansi.Strip(v.render(180, formWideMinH))
	for _, want := range []string{"✦ New chat", "A new conversation"} {
		if !strings.Contains(strings.Join(strings.Split(out, "\n")[:4], "\n"), want) {
			t.Errorf("the top of the list is missing %q:\n%s", want, out)
		}
	}
}

// Model and effort say only what the selected agent can use.
func TestNewChatChipsFollowTheAgent(t *testing.T) {
	v := wideChat(t)
	f := v.create
	f.setAgent("cursor")
	out := ansi.Strip(v.render(180, 44))
	if strings.Contains(out, "Effort") {
		t.Errorf("cursor has no effort levels and the form offers them:\n%s", out)
	}
	if !strings.Contains(out, "40 models") {
		t.Errorf("a long catalog is not behind a list:\n%s", out)
	}
	f.focus = ncModel
	f.moveFocus(1)
	if f.focus == ncEffort {
		t.Error("tab walked onto an effort row that is not drawn")
	}
	f.setAgent("codex")
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "enter to type a model") {
		t.Errorf("an agent that lists no models lost the way to name one:\n%s", out)
	}
}

// Left and right walk a short catalog's chips, through the agent's default.
func TestNewChatArrowsStepTheChips(t *testing.T) {
	f := wideChat(t).create
	f.focus = ncModel
	for _, want := range []string{"opus", "sonnet", "", "opus"} {
		f.cycle(1)
		if f.model != want {
			t.Fatalf("the model stepped to %q, want %q", f.model, want)
		}
	}
	f.setAgent("cursor")
	f.focus = ncModel
	f.cycle(1)
	if f.model != "" {
		t.Errorf("a long catalog stepped to %q", f.model)
	}
}

// A narrow terminal keeps the plain form.
func TestNewChatNarrowKeepsThePlainForm(t *testing.T) {
	v := wideChat(t)
	v.update(tea.WindowSizeMsg{Width: mgmtWideMinW - 1, Height: 44})
	if v.frameless() {
		t.Fatal("a narrow terminal asked for the wide form")
	}
	if out := ansi.Strip(v.render(mgmtWideMinW-1, 44)); !strings.Contains(out, "new chat") {
		t.Errorf("the plain form is gone:\n%s", out)
	}
}

// The list says how far a branch has moved from the project's default.
func TestBranchNoteSaysCommitsAhead(t *testing.T) {
	three, none := 3, 0
	if got := branchNote(apiclient.Branch{Name: "fix", Ahead: &three, AheadOf: "main"}); got != "3 commits ahead of main" {
		t.Errorf("the note is %q", got)
	}
	for _, b := range []apiclient.Branch{{Name: "main", Ahead: &none, AheadOf: "main"}, {Name: "old"}} {
		if got := branchNote(b); got != "" {
			t.Errorf("%s says %q", b.Name, got)
		}
	}
}
