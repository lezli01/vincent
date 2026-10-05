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

// loadedWorkflows loads a registry with no project selected: the global
// listing alone.
func loadedWorkflows(w *workflowsView, global ...apiclient.WorkflowEntry) {
	w.update(workflowsLoadedMsg{global: global})
}

// loadedProjectWorkflows selects app (project 1) and loads both listings.
func loadedProjectWorkflows(w *workflowsView, global, own []apiclient.WorkflowEntry) {
	w.project = projectSel{id: 1, name: "app"}
	w.update(workflowsLoadedMsg{global: global, own: own})
}

// A global entry a project also sees comes back from both calls. Keeping
// only the project-scoped rows from a project's response is what stops it
// rendering twice.
func TestWorkflowsMergeKeepsAGlobalEntryOnce(t *testing.T) {
	response := []apiclient.WorkflowEntry{
		globalEntry("review"),
		projectEntry("release"),
	}
	own := ownEntries(response)
	if len(own) != 1 || own[0].Name != "release" {
		t.Fatalf("ownEntries = %+v, want only the project's own entry", own)
	}
}

// A project entry with a global entry's name hides it (§5.2). Both rows stay
// listed: the project's says what it shadows, and the global one is dimmed
// and says by whom, so the global file is still reachable (task 132
// decision 40).
func TestWorkflowsFlagAProjectEntryThatShadowsAGlobalOne(t *testing.T) {
	w := newWorkflowsView()
	builtin := apiclient.WorkflowEntry{Name: "adhoc", Scope: "builtin"}
	loadedProjectWorkflows(w,
		[]apiclient.WorkflowEntry{builtin, globalEntry("review")},
		[]apiclient.WorkflowEntry{projectEntry("adhoc"), projectEntry("review"), projectEntry("release")},
	)
	notes := map[string]string{}
	for _, line := range w.lines() {
		notes[line.entry.Scope+"/"+line.entry.Name] = shadowNote(line)
	}
	want := map[string]string{
		"project/review":  "shadows global review",
		"global/review":   "shadowed here by app",
		"project/adhoc":   "shadows builtin adhoc",
		"builtin/adhoc":   "shadowed here by app",
		"project/release": "",
	}
	for k, v := range want {
		if got, ok := notes[k]; !ok || got != v {
			t.Errorf("%s: note = %q (listed %v), want %q", k, got, ok, v)
		}
	}
	if got := countEntryLines(w); got != 5 {
		t.Errorf("entry lines = %d, want 5 (both sides of each override)", got)
	}
	out := w.render(120, 24)
	for _, s := range []string{"shadows global review", "shadowed here by app", "shadows builtin adhoc"} {
		if !strings.Contains(out, s) {
			t.Errorf("render is missing %q:\n%s", s, out)
		}
	}
}

// The resolved list is one list sorted by name, the project entry ahead of
// the global one it shadows. An invalid entry stays where its name puts it.
func TestWorkflowsResolvedListSortsByName(t *testing.T) {
	w := newWorkflowsView()
	loadedProjectWorkflows(w,
		[]apiclient.WorkflowEntry{globalEntry("zeta"), brokenEntry("alpha"), globalEntry("own"), globalEntry("mid")},
		[]apiclient.WorkflowEntry{projectEntry("own"), projectEntry("beta")},
	)
	var order []string
	for _, line := range w.lines() {
		order = append(order, line.entry.Scope+":"+line.entry.Name)
	}
	want := []string{"global:alpha", "project:beta", "global:mid", "project:own", "global:own", "global:zeta"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// An overridden global row resolves, opens and forks as the global file it
// is; the project row as the project's.
func TestWorkflowsKeyARowByTheScopeThatOwnsItsFile(t *testing.T) {
	w := newWorkflowsView()
	loadedProjectWorkflows(w,
		[]apiclient.WorkflowEntry{globalEntry("review")},
		[]apiclient.WorkflowEntry{projectEntry("review")},
	)
	keys := map[string]int64{}
	for _, line := range w.lines() {
		keys[line.entry.Scope] = line.key().projectID
	}
	if keys["global"] != 0 || keys[scopeProject] != 1 {
		t.Errorf("keys = %v, want global by 0 and the project's own by 1", keys)
	}
}

// A switch drops the previous project's own rows: kept, they would be keyed
// and labeled as the new project's until its load landed, and for good when
// that load failed — and an editor opened on one would patch the global file
// as the new project's (review F5).
func TestWorkflowsSwitchDropsTheOldProjectsRows(t *testing.T) {
	w := newWorkflowsView()
	loadedProjectWorkflows(w,
		[]apiclient.WorkflowEntry{globalEntry("review")},
		[]apiclient.WorkflowEntry{projectEntry("review")},
	)
	stale := func(when string) {
		t.Helper()
		for _, line := range w.lines() {
			if line.entry.Scope == scopeProject || line.shadowedBy != "" {
				t.Errorf("%s: row %s/%s (key %+v, shadowed by %q) survived the switch",
					when, line.entry.Scope, line.entry.Name, line.key(), line.shadowedBy)
			}
		}
	}
	w.setProject(projectSel{id: 2, name: "b"})
	stale("before the reload")
	w.update(workflowsLoadedMsg{stamp: w.stamps.next(2), err: errors.New("connection refused")})
	stale("after a failed reload")
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
	loadedWorkflows(w, brokenEntry("busted"))
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
	loadedWorkflows(w, e)
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
	loadedWorkflows(w, entry)
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
	loadedWorkflows(w, globalEntry("review"))
	out := w.render(120, 24)
	if !strings.Contains(out, "review workflow") {
		t.Errorf("compact workflow registry lost its row detail:\n%s", out)
	}
	if strings.Contains(out, "Availability") {
		t.Errorf("compact workflow registry contains the guided overview:\n%s", out)
	}
}

// A failed project fetch degrades the view to the global rows plus an error
// line, never a blank view.
func TestWorkflowsIsolateAFailedProjectFetch(t *testing.T) {
	w := newWorkflowsView()
	w.project = projectSel{id: 1, name: "app"}
	w.update(workflowsLoadedMsg{
		global: []apiclient.WorkflowEntry{globalEntry("review")},
		ownErr: errors.New("project path missing"),
	})
	out := w.render(120, 30)
	if !strings.Contains(out, "app: registry unavailable: project path missing") {
		t.Errorf("render = %q, want the failed fetch to say so", out)
	}
	if !strings.Contains(out, "review") {
		t.Errorf("render lost the global rows when the project fetch failed:\n%s", out)
	}
	if line, ok := w.currentLine(); !ok || line.entry.Name != "review" {
		t.Errorf("cursor = %+v, want it past the error line on the first entry", line)
	}
}

// A failed global fetch keeps the last-good registry behind the warning.
func TestWorkflowsKeepLastGoodWhenTheGlobalFetchFails(t *testing.T) {
	w := newWorkflowsView()
	loadedWorkflows(w, globalEntry("review"))
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
	loadedWorkflows(w, builtin)
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
	loadedWorkflows(w, entry)
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

// The cursor walks the resolved list, and every row hints the selected
// project: a global row no longer hints nothing (task 132.12).
func TestWorkflowsCursorWalksTheResolvedList(t *testing.T) {
	w := newWorkflowsView()
	loadedProjectWorkflows(w,
		[]apiclient.WorkflowEntry{globalEntry("review")},
		[]apiclient.WorkflowEntry{projectEntry("release")},
	)
	line, ok := w.currentLine()
	if !ok || line.entry.Name != "release" {
		t.Fatalf("initial line = %+v, want the first entry", line)
	}
	if got := w.hintedProject(); got != 1 {
		t.Errorf("hintedProject() on a project row = %d, want the selection", got)
	}
	pressView(w, "j")
	line, ok = w.currentLine()
	if !ok || line.entry.Name != "review" {
		t.Fatalf("after j = %+v, want the next entry", line)
	}
	if got := w.hintedProject(); got != 1 {
		t.Errorf("hintedProject() on a global row = %d, want the selection", got)
	}
	pressView(w, "j")
	if line, _ = w.currentLine(); line.entry.Name != "review" {
		t.Error("the cursor walked off the last entry")
	}
}

// A global row says that editing it affects every project, expanded and in
// the editor's header; a builtin row keeps its no-file note.
func TestWorkflowsWarnThatAGlobalFileIsShared(t *testing.T) {
	w := newWorkflowsView()
	w.client = offlineClient()
	loadedProjectWorkflows(w, []apiclient.WorkflowEntry{globalEntry("review")}, nil)
	pressView(w, "enter")
	if out := w.render(120, 24); !strings.Contains(out, "affects every project") {
		t.Errorf("expanded global row has no shared-file warning:\n%s", out)
	}
	pressView(w, "i")
	if w.editor == nil {
		t.Fatal("the editor did not open")
	}
	if out := w.render(120, 24); !strings.Contains(out, "affects every project") {
		t.Errorf("editor on a global file has no shared-file warning:\n%s", out)
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
