package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1082: the chats list says why it is empty and which key changes that.
func TestChatsEmptyStateSaysWhy(t *testing.T) {
	v := chatsFixture()
	v.filter.SetValue("zzz")
	if got := v.emptyChatsText(); !strings.Contains(got, `No chat matches "zzz"`) || strings.Contains(got, "Press n") {
		t.Errorf("a filter with no match reads %q", got)
	}
	v.filter.SetValue("")
	v.scope = apiclient.ArchivedOnly
	if got := v.emptyChatsText(); !strings.Contains(got, "No archived") || strings.Contains(got, "Press n") {
		t.Errorf("an empty ended scope reads %q", got)
	}
	v.scope = apiclient.ArchivedExclude
	v.chats = nil
	if got := v.emptyChatsText(); !strings.Contains(got, "Press n to start one") {
		t.Errorf("a project with no chats reads %q", got)
	}
}

// #1082: with nothing registered, @ opens the add form on the overview.
func TestProjectKeyAddsWhenNoneIsRegistered(t *testing.T) {
	m := connectedRoot(t)
	m.startup.done = true
	m.projects = nil
	m.updateKey(keyPress("@"))
	if m.active != viewProjects {
		t.Fatalf("@ with no projects went to %v, want the projects overview", m.active)
	}
	if p := m.views[viewProjects].(*projectsView); p.form == nil {
		t.Fatal("@ with no projects did not open the add form")
	}
	if !strings.Contains(noProjectsEmpty(), "press @ to add one") {
		t.Errorf("the empty state reads %q", noProjectsEmpty())
	}
}

// #1082: the first project's save names the next key.
func TestFirstProjectSaveNamesTheNextKey(t *testing.T) {
	p := newProjectsView()
	p.loaded = true
	p.applySaved(projectSavedMsg{project: apiclient.Project{ID: 1, Name: "api"}})
	out := ansi.Strip(p.render(100, 20))
	if !strings.Contains(out, "api is registered") || !strings.Contains(out, "n starts its first task") {
		t.Fatalf("the first save names no next key:\n%s", out)
	}
	p.updateKey(keyPress("j"))
	if p.firstRun != "" {
		t.Error("a key did not clear the note")
	}
}
