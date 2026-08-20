package tui

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func globalEntry(name string) apiclient.WorkflowEntry {
	return apiclient.WorkflowEntry{
		Name: name, Scope: "global", File: "/cfg/workflows/" + name + ".yaml",
		Description: name + " workflow",
	}
}

// projectEntry builds an entry owned by project 1, which is the only
// project these tests need.
func projectEntry(name string) apiclient.WorkflowEntry {
	pid := int64(1)
	return apiclient.WorkflowEntry{
		Name: name, Scope: scopeProject, ProjectID: &pid,
		File: "/repos/app/.vincent/workflows/" + name + ".yaml",
	}
}

func loadedWorkflows(w *workflowsView, blocks ...wfBlock) {
	w.update(workflowsLoadedMsg{blocks: blocks})
}

// A global entry a project also sees comes back from both calls. Keeping
// only the project-scoped rows from a project's response is what stops it
// rendering twice.
func TestWorkflowsMergeKeepsAGlobalEntryOnce(t *testing.T) {
	response := []apiclient.WorkflowEntry{
		globalEntry("review"),
		projectEntry("release"),
	}
	own := projectScoped(response)
	if len(own) != 1 || own[0].Name != "release" {
		t.Fatalf("projectScoped = %+v, want only the project's own entry", own)
	}
}

// A project entry with a global entry's name hides it (§5.2), and the row
// says so rather than looking like a duplicate.
func TestWorkflowsFlagAProjectEntryThatShadowsAGlobalOne(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w,
		wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}},
		wfBlock{name: "app", projectID: 1, entries: []apiclient.WorkflowEntry{
			projectEntry("review"), projectEntry("release"),
		}},
	)
	var shadowed, plain string
	for _, line := range w.lines() {
		if line.entry == nil {
			continue
		}
		if line.entry.Scope != scopeProject {
			continue
		}
		if line.entry.Name == "review" {
			shadowed = line.shadows
		}
		if line.entry.Name == "release" {
			plain = line.shadows
		}
	}
	if shadowed != "review" {
		t.Errorf("shadows = %q, want the global entry named", shadowed)
	}
	if plain != "" {
		t.Errorf("shadows = %q on an entry with no global twin, want empty", plain)
	}
	// Both rows exist exactly once each.
	if got := countEntryLines(w); got != 3 {
		t.Errorf("entry lines = %d, want 3 (one global, two project)", got)
	}
}

// Scope is the outer grouping and entries are alphabetical inside it — an
// invalid entry stays where its name puts it.
func TestWorkflowsGroupByScopeAndSortWithinIt(t *testing.T) {
	w := newWorkflowsView()
	entries := []apiclient.WorkflowEntry{globalEntry("zeta"), brokenEntry("alpha"), globalEntry("mid")}
	sortEntries(entries)
	loadedWorkflows(w,
		wfBlock{name: "global", entries: entries},
		wfBlock{name: "app", projectID: 1, entries: []apiclient.WorkflowEntry{projectEntry("own")}},
	)
	var order []string
	for _, line := range w.lines() {
		if line.header != "" {
			order = append(order, "#"+line.header)
			continue
		}
		order = append(order, line.entry.Name)
	}
	want := []string{"#global", "alpha", "mid", "zeta", "#app", "own"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func brokenEntry(name string) apiclient.WorkflowEntry {
	e := globalEntry(name)
	e.Errors = []apiclient.WorkflowFinding{{Line: 4, Message: "steps is required"}}
	return e
}

// A broken entry is listed in place with its own finding, not hidden and not
// floated to the top.
func TestWorkflowsRenderABrokenEntryInPlace(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{brokenEntry("busted")}})
	out := w.render(120, 24)
	if !strings.Contains(out, "busted") {
		t.Fatalf("render = %q, want the broken entry listed", out)
	}
	if !strings.Contains(out, "steps is required") {
		t.Errorf("render = %q, want the entry's own finding", out)
	}
}

// A workflow this host cannot run (§8.1, task 010) is listed like any other,
// with the platforms it needs — the registry view is where a human goes to
// find out why a workflow is missing from the new-task picker.
func TestWorkflowsRenderAPlatformRestrictedEntry(t *testing.T) {
	e := globalEntry("posix-tools")
	no := false
	e.Platforms, e.PlatformSupported = []string{"linux", "darwin"}, &no
	w := newWorkflowsView()
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{e}})
	out := w.render(120, 24)
	if !strings.Contains(out, "posix-tools") {
		t.Fatalf("render = %q, want the restricted entry listed", out)
	}
	if !strings.Contains(out, "not on this platform") {
		t.Errorf("render = %q, want the restriction stated", out)
	}
	if !strings.Contains(out, "linux, darwin") {
		t.Errorf("render = %q, want the platforms it needs", out)
	}
}

func TestWorkflowsGuidedLayoutPairsTheRegistryWithDetails(t *testing.T) {
	entry := globalEntry("review")
	entry.Steps = []apiclient.WorkflowEntryStep{
		{ID: "plan", Name: "Plan", Type: "agent", Agent: "claude"},
		{ID: "check", Name: "Check", Type: "command"},
	}
	w := newWorkflowsView()
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{entry}})
	out := w.render(160, 32)
	for _, want := range []string{
		"Registry", "Overview · review", "Availability", "2 top-level steps",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guided workflows render is missing %q:\n%s", want, out)
		}
	}
	pressView(w, "enter")
	out = w.render(160, 32)
	for _, want := range []string{"Plan", "Check", "claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded guided workflow is missing %q:\n%s", want, out)
		}
	}
	w.render(120, 24)
	if !w.expanded {
		t.Error("crossing to the compact registry closed the step expansion")
	}
}

func TestWorkflowsCompactFallbackKeepsTheFlatRegistry(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
	out := w.render(120, 24)
	if !strings.Contains(out, "review workflow") {
		t.Errorf("compact workflow registry lost its row detail:\n%s", out)
	}
	if strings.Contains(out, "Availability") {
		t.Errorf("compact workflow registry contains the guided overview:\n%s", out)
	}
}

// One unreadable project degrades its own block and nothing else.
func TestWorkflowsIsolateAFailedProjectFetch(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w,
		wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}},
		wfBlock{name: "broken-project", projectID: 2, err: errors.New("project path missing")},
		wfBlock{name: "app", projectID: 1, entries: []apiclient.WorkflowEntry{projectEntry("release")}},
	)
	out := w.render(120, 30)
	if !strings.Contains(out, "registry unavailable") {
		t.Errorf("render = %q, want the failed block to say so", out)
	}
	for _, want := range []string{"review", "release"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lost %q when another block failed", want)
		}
	}
}

// A failed global fetch keeps the last-good registry behind the warning.
func TestWorkflowsKeepLastGoodWhenTheGlobalFetchFails(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
	w.update(workflowsLoadedMsg{err: errors.New("connection refused")})
	out := w.render(120, 24)
	if !strings.Contains(out, "review") {
		t.Errorf("render = %q, want the last-good rows kept", out)
	}
	if !strings.Contains(out, "refresh failed") {
		t.Errorf("render = %q, want the failure surfaced", out)
	}
}

// The built-in adhoc has no file, so there is nothing for `e` to open.
func TestWorkflowsRefuseToEditAnEntryWithNoFile(t *testing.T) {
	w := newWorkflowsView()
	builtin := apiclient.WorkflowEntry{Name: "adhoc", Scope: "global"}
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{builtin}})
	var ran bool
	w.exec = func(*exec.Cmd, tea.ExecCallback) tea.Cmd {
		ran = true
		return nil
	}
	pressView(w, "e")
	if ran {
		t.Error("an editor was launched for an entry with no file")
	}
	if !strings.Contains(w.err, "built in") {
		t.Errorf("err = %q, want it to say there is no file", w.err)
	}
	if !strings.Contains(w.render(120, 24), "built-in") {
		t.Error("the row does not say the entry is built in")
	}
}

// `e` opens the entry's real path and then waits: what the file now says
// arrives through the registry reload, the same path an external editor
// takes. Nothing is read back here.
func TestWorkflowsEditOpensTheRealFileAndWaitsForTheReload(t *testing.T) {
	w := newWorkflowsView()
	entry := globalEntry("review")
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{entry}})
	var opened []string
	w.exec = func(c *exec.Cmd, _ tea.ExecCallback) tea.Cmd {
		opened = c.Args
		return nil
	}
	pressView(w, "e")
	if len(opened) == 0 || opened[len(opened)-1] != entry.File {
		t.Fatalf("editor args = %v, want it to end at %q", opened, entry.File)
	}
	if w.err != "" {
		t.Errorf("err = %q, want none", w.err)
	}

	// The editor exiting changes nothing on its own.
	before := w.render(120, 24)
	if _, cmd := w.update(workflowEditedMsg{}); cmd != nil {
		t.Error("the editor exiting triggered a fetch; the reload event is what refetches")
	}
	if after := w.render(120, 24); after != before {
		t.Error("the view changed on editor exit rather than on the reload")
	}

	// A failed editor is reported.
	w.update(workflowEditedMsg{err: errors.New("exit status 1")})
	if !strings.Contains(w.err, "editor") {
		t.Errorf("err = %q, want the editor failure surfaced", w.err)
	}
}

func TestWorkflowsRefetchOnActivationAndOnRegistryEvents(t *testing.T) {
	w := newWorkflowsView()
	w.client = &apiclient.Client{}
	if _, cmd := w.update(viewActivatedMsg{id: viewWorkflows}); cmd == nil {
		t.Error("activation did not refetch")
	}
	if _, cmd := w.update(viewActivatedMsg{id: viewHome}); cmd != nil {
		t.Error("another view's activation triggered a fetch")
	}
	for _, evType := range []string{eventWorkflowRegistryChanged, "project.created"} {
		w.refreshPending = false
		_, cmd := w.update(noteMsg{note: apiclient.EventNote{
			Event: apiclient.Event{Type: evType, Payload: json.RawMessage("{}")},
		}})
		if cmd == nil {
			t.Errorf("%s did not schedule a refresh", evType)
		}
	}
	// A task event moves nothing in this view.
	w.refreshPending = false
	if _, cmd := w.update(noteMsg{note: apiclient.EventNote{
		Event: apiclient.Event{Type: "task.state_changed", Payload: json.RawMessage("{}")},
	}}); cmd != nil {
		t.Error("a task event refetched the registry")
	}
}

// The cursor skips headers and per-block errors, so `e` never points at one.
func TestWorkflowsCursorSkipsHeaders(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w,
		wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}},
		wfBlock{name: "app", projectID: 1, entries: []apiclient.WorkflowEntry{projectEntry("release")}},
	)
	line, ok := w.currentLine()
	if !ok || line.entry.Name != "review" {
		t.Fatalf("initial line = %+v, want the first entry", line)
	}
	pressView(w, "j")
	line, ok = w.currentLine()
	if !ok || line.entry.Name != "release" {
		t.Fatalf("after j = %+v, want the next block's entry", line)
	}
	if got := w.hintedProject(); got != 1 {
		t.Errorf("hintedProject() = %d, want the block's project", got)
	}
	pressView(w, "j")
	if line, _ = w.currentLine(); line.entry.Name != "release" {
		t.Error("the cursor walked off the last entry")
	}
}

func TestWorkflowsEmptyStatesAreDistinct(t *testing.T) {
	w := newWorkflowsView()
	body, ok := w.emptyBody(nil)
	if !ok || !strings.Contains(body, "loading") {
		t.Errorf("before the first load: %q", body)
	}
	loadedWorkflows(w)
	body, ok = w.emptyBody(nil)
	if !ok || !strings.Contains(body, "adhoc") {
		t.Errorf("empty registry: %q, want the built-in named", body)
	}
	// A project with no workflows of its own is a block, not an empty view.
	loadedWorkflows(w,
		wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}},
		wfBlock{name: "app", projectID: 1},
	)
	out := w.render(120, 24)
	if !strings.Contains(out, "no workflows of its own") {
		t.Errorf("render = %q, want the empty project block named", out)
	}
}

func countEntryLines(w *workflowsView) int {
	n := 0
	for _, line := range w.lines() {
		if line.entry != nil {
			n++
		}
	}
	return n
}
