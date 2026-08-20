package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// pressView sends one named key to a view and returns whatever it asked for.
func pressView(v panel, name string) tea.Cmd {
	_, cmd := v.update(namedKey(name))
	return cmd
}

func namedKey(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
	}
}

func loadedProjects(p *projectsView, projects []apiclient.Project, tasks []apiclient.Task) {
	p.update(projectsLoadedMsg{
		projects: projects,
		tasks:    tasks,
		info:     apiclient.Info{MaxParallelTasks: 3},
		infoOK:   true,
	})
}

func testProject(id int64, name string) apiclient.Project {
	return apiclient.Project{ID: id, Name: name, Path: "/repos/" + name, DefaultBranch: "main"}
}

// The two caps are independent, so a project with none of its own must not
// read as though it were capped at the daemon-wide figure.
func TestProjectCapCellSeparatesTheProjectCapFromTheGlobalOne(t *testing.T) {
	p := newProjectsView()
	capped := testProject(1, "capped")
	two := 2
	capped.MaxParallelTasks = &two
	loadedProjects(p, []apiclient.Project{capped, testProject(2, "uncapped")}, []apiclient.Task{
		{ID: 10, ProjectID: 1, State: stateRunning},
		{ID: 11, ProjectID: 2, State: stateRunning},
		{ID: 12, ProjectID: 2, State: stateQueued},
	})

	if got, want := p.capCell(capped), "1 / 2"; got != want {
		t.Errorf("capped cell = %q, want %q", got, want)
	}
	got := p.capCell(testProject(2, "uncapped"))
	if !strings.Contains(got, "global 3") {
		t.Errorf("uncapped cell = %q, want it to name the daemon-wide limit", got)
	}
	if strings.Contains(got, "1 / 3") {
		t.Errorf("uncapped cell = %q, must not read as a cap of 3", got)
	}
}

func TestProjectsEmptyStatesAreDistinct(t *testing.T) {
	p := newProjectsView()
	if body, ok := p.emptyBody(nil); !ok || !strings.Contains(body, "loading") {
		t.Errorf("before the first load: %q", body)
	}
	loadedProjects(p, nil, nil)
	body, ok := p.emptyBody(nil)
	if !ok || !strings.Contains(body, "press a") {
		t.Errorf("no projects: %q, want a pointer at the add key", body)
	}
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	p.filter.SetValue("nothing")
	body, ok = p.emptyBody(p.visible())
	if !ok || !strings.Contains(body, "filter") {
		t.Errorf("filtered to nothing: %q, want the filter named", body)
	}
}

// A failed refresh keeps the rows and says how stale they are.
func TestProjectsKeepRowsWhenARefreshFails(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	p.update(projectsLoadedMsg{err: errors.New("daemon went away")})
	if len(p.projects) != 1 {
		t.Fatalf("projects = %d, want the last-good row kept", len(p.projects))
	}
	lines := strings.Join(p.statusLines(), "\n")
	if !strings.Contains(lines, "refresh failed") {
		t.Errorf("status = %q, want the failure surfaced", lines)
	}
}

// The forceable 409 becomes a second question; the running-task 409 does not.
func TestProjectsDeleteRePromptsOnlyWhenForceIsTheRemedy(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)

	pressView(p, "d")
	if p.confirm == nil || p.confirm.force {
		t.Fatalf("confirm = %+v, want an unforced first ask", p.confirm)
	}

	p.update(projectDeletedMsg{id: 1, forced: false, err: &apiclient.Error{
		Status:  http.StatusConflict,
		Code:    "invalid_state",
		Message: "project has 2 non-archived task(s); pass ?force to archive them and delete anyway",
	}})
	if p.confirm == nil {
		t.Fatal("no second confirmation after a forceable 409")
	}
	if !p.confirm.force {
		t.Error("second confirmation does not carry force")
	}
	if p.err != "" {
		t.Errorf("err = %q, want the 409 asked about rather than reported", p.err)
	}

	p.confirm = nil
	p.update(projectDeletedMsg{id: 1, forced: false, err: &apiclient.Error{
		Status:  http.StatusConflict,
		Code:    "invalid_state",
		Message: "task 7 is running; cancel it before deleting the project",
	}})
	if p.confirm != nil {
		t.Errorf("confirm = %+v, want no prompt for a 409 force cannot fix", p.confirm)
	}
	if !strings.Contains(p.err, "is running") {
		t.Errorf("err = %q, want the daemon's refusal reported", p.err)
	}
}

// A 409 on the forced request is a refusal too — offering force again would
// be offering the same failure.
func TestProjectsDeleteDoesNotReAskAfterAForcedConflict(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	p.update(projectDeletedMsg{id: 1, forced: true, err: &apiclient.Error{
		Status:  http.StatusConflict,
		Message: "project has 2 non-archived task(s); pass ?force to archive them and delete anyway",
	}})
	if p.confirm != nil {
		t.Errorf("confirm = %+v, want none after a forced request already failed", p.confirm)
	}
	if p.err == "" {
		t.Error("err is empty; the failure has to be reported somewhere")
	}
}

func TestProjectsAddFormSendsOnlyTheTouchedFields(t *testing.T) {
	f := newProjectForm(nil, nil)
	f.path.SetValue("/repos/vincent")
	req := f.createRequest()
	if req.Path != "/repos/vincent" {
		t.Errorf("Path = %q", req.Path)
	}
	if req.Name != nil || req.DefaultBranch != nil ||
		req.DefaultWorkflow != nil || req.MaxParallelTasks != nil {
		t.Errorf("request = %+v, want every untouched field omitted", req)
	}
}

func TestProjectFormBlocksLocallyOnlyOnAnEmptyPath(t *testing.T) {
	f := newProjectForm(nil, nil)
	f.path.SetValue("")
	if cmd := f.submit(); cmd != nil {
		t.Error("submit issued a request with no path")
	}
	if _, ok := f.rowErr[pfPath]; !ok {
		t.Error("no error on the path row")
	}
	// A path that is not a repository is the daemon's call, not the form's:
	// with no client there is nothing to send, but the local gate must pass.
	f = newProjectForm(nil, nil)
	f.path.SetValue("/not/a/repo")
	f.submit()
	if len(f.rowErr) != 0 {
		t.Errorf("rowErr = %v, want no client-side verdict on the path", f.rowErr)
	}
	if f.err != "not connected" {
		t.Errorf("err = %q, want the submit to have got as far as needing a client", f.err)
	}
}

// The three PATCH states have to survive the form, not just the wire type.
func TestProjectFormPatchesOnlyWhatChanged(t *testing.T) {
	workflow, cap4 := "review", 4
	original := testProject(1, "vincent")
	original.DefaultWorkflow = &workflow
	original.MaxParallelTasks = &cap4

	f := newProjectForm(nil, &original)
	if req := f.patchRequest(); !isEmptyPatch(t, req) {
		t.Errorf("untouched form patches %s, want an empty body", encode(t, req))
	}

	f.name.SetValue("vincent-core")
	req := f.patchRequest()
	if v, ok := req.Name.Value(); !ok || v != "vincent-core" {
		t.Errorf("Name = %v, want the new name set", req.Name)
	}
	if !req.MaxParallelTasks.IsZero() || !req.DefaultWorkflow.IsZero() {
		t.Errorf("patch = %s, want the untouched fields absent", encode(t, req))
	}

	// Clearing a nullable field is a null, which is not the same as absent.
	f.workflow, f.workflowSet = "", true
	f.cap.SetValue("")
	req = f.patchRequest()
	if !req.DefaultWorkflow.IsNull() {
		t.Errorf("DefaultWorkflow = %s, want an explicit null", encode(t, req))
	}
	if !req.MaxParallelTasks.IsNull() {
		t.Errorf("MaxParallelTasks = %s, want an explicit null", encode(t, req))
	}
}

// Zero is the daemon's rule to enforce; the form must send it rather than
// invent a second copy of the check.
func TestProjectFormSendsAZeroCapForTheDaemonToReject(t *testing.T) {
	original := testProject(1, "vincent")
	f := newProjectForm(nil, &original)
	f.cap.SetValue("0")
	req := f.patchRequest()
	v, ok := req.MaxParallelTasks.Value()
	if !ok || v != 0 {
		t.Errorf("MaxParallelTasks = %s, want 0 sent through", encode(t, req))
	}
}

func TestProjectFormParksTheDaemonsMessageOnTheRowItNames(t *testing.T) {
	original := testProject(1, "vincent")
	f := newProjectForm(nil, &original)
	f.applyFailure(&apiclient.Error{
		Status:  http.StatusBadRequest,
		Code:    "validation_failed",
		Message: `default_branch "nope" does not resolve to a local branch`,
	})
	if _, ok := f.rowErr[pfBranch]; !ok {
		t.Fatalf("rowErr = %v, want the branch row named", f.rowErr)
	}
	if f.cursor != pfBranch {
		t.Errorf("cursor = %v, want it moved to the row carrying the error", f.cursor)
	}
	if f.err != "" {
		t.Errorf("err = %q, want a routed message not to also be a form error", f.err)
	}

	// An error that names no field stays a form error rather than landing
	// on an unrelated row.
	f = newProjectForm(nil, &original)
	f.applyFailure(&apiclient.Error{Status: 500, Code: "internal", Message: "boom"})
	if len(f.rowErr) != 0 {
		t.Errorf("rowErr = %v, want nothing routed", f.rowErr)
	}
	if f.err == "" {
		t.Error("err is empty; an unroutable failure has to show somewhere")
	}
}

// An invalid workflow is listed so the human can see what broke, and refused
// so they cannot select it.
func TestProjectFormListsInvalidWorkflowsAndRefusesThem(t *testing.T) {
	f := newProjectForm(nil, nil)
	f.workflows = []apiclient.WorkflowEntry{
		{Name: "good", Scope: "global"},
		{Name: "busted", Scope: "project", Errors: []apiclient.WorkflowFinding{{Message: "steps is required"}}},
	}
	opts := f.workflowOptions()
	var busted pickerOption
	for _, o := range opts {
		if o.value == "busted" {
			busted = o
		}
	}
	if busted.value == "" {
		t.Fatalf("options = %+v, want the broken entry listed", opts)
	}
	if !busted.disabled {
		t.Error("the broken entry is selectable")
	}
	if !strings.Contains(busted.note, "steps is required") {
		t.Errorf("note = %q, want the entry's own finding", busted.note)
	}
}

// The filter owns the keyboard; a bare confirmation does not, so the global
// single-key bindings keep working while one is up.
func TestProjectsCaptureFollowsTheTextFields(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	if p.capturesInput() {
		t.Error("captures input while merely listing")
	}
	pressView(p, "/")
	if !p.capturesInput() {
		t.Error("the filter does not capture")
	}
	pressView(p, "esc")

	pressView(p, "d")
	if p.capturesInput() {
		t.Error("a single-key confirmation should not capture")
	}
	pressView(p, "n")

	pressView(p, "a")
	if p.capturesInput() {
		t.Error("the form captures before a row is being typed into")
	}
	pressView(p, "enter")
	if !p.capturesInput() {
		t.Error("an open text row does not capture")
	}
}

func TestProjectsRefetchOnActivationAndOnEvents(t *testing.T) {
	p := newProjectsView()
	p.client = &apiclient.Client{}
	if _, cmd := p.update(viewActivatedMsg{id: viewProjects}); cmd == nil {
		t.Error("activation did not refetch")
	}
	if _, cmd := p.update(viewActivatedMsg{id: viewHome}); cmd != nil {
		t.Error("another view's activation triggered a fetch")
	}
	for _, evType := range []string{"project.created", "task.state_changed", eventWorkflowRegistryChanged} {
		p.refreshPending = false
		_, cmd := p.update(noteMsg{note: apiclient.EventNote{
			Event: apiclient.Event{Type: evType, Payload: json.RawMessage("{}")},
		}})
		if cmd == nil {
			t.Errorf("%s did not schedule a refresh", evType)
		}
	}
}

// TestProjectColumnsFitTheirWidth is a T3.8 finding: the widths ignored the
// table's per-cell padding and overflowed by a whole column's worth, so the
// last column ("running / cap") arrived cut. The path column is also the
// one that must not eat a wide terminal.
func TestProjectColumnsFitTheirWidth(t *testing.T) {
	for _, width := range []int{80, 100, 120, 160, 240} {
		cols, set := projectColumns(width)
		total := 0
		for _, c := range cols {
			total += c.Width + colPadding
		}
		if total > width {
			t.Errorf("width %d: columns need %d cells — the last one gets cut", width, total)
		}
		// Whatever survives has to be readable, the last column included.
		last := cols[len(cols)-1]
		if last.Title != "running / cap" || last.Width != pcolCap {
			t.Errorf("width %d: last column = %q/%d, want the full cap column", width, last.Title, last.Width)
		}
		for _, c := range cols {
			if c.Width < 4 {
				t.Errorf("width %d: column %q shrank to %d", width, c.Title, c.Width)
			}
			if c.Title == "path" && c.Width > pcolMaxPath {
				t.Errorf("width %d: path took %d cells, capped at %d", width, c.Width, pcolMaxPath)
			}
		}
		if width >= 160 && !set.path {
			t.Errorf("width %d dropped the path column", width)
		}
	}
}

// The header and the rows must agree about how many cells a row has, or the
// table indexes out of range on the next resize.
func TestProjectRowsMatchTheirColumns(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	for _, width := range []int{70, 100, 200} {
		cols, set := projectColumns(width)
		rows := p.rowsFor(p.visible(), set)
		if len(rows) == 0 {
			t.Fatal("no rows built")
		}
		if len(rows[0]) != len(cols) {
			t.Errorf("width %d: row has %d cells, header has %d", width, len(rows[0]), len(cols))
		}
	}
}

// A resize across the path breakpoint must not panic or lose the selection.
func TestProjectsSurviveAColumnBreakpoint(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "a"), testProject(2, "b")}, nil)
	p.render(200, 24)
	p.tbl.SetCursor(1)
	p.rememberSelection()
	p.render(80, 24) // path column disappears
	p.render(200, 24)
	if got, ok := p.current(); !ok || got.ID != 2 {
		t.Errorf("selection after two resizes = %+v, want project 2", got)
	}
}

func TestProjectsGuidedLayoutPairsTheRailWithAWorkload(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p,
		[]apiclient.Project{testProject(1, "vincent"), testProject(2, "docs")},
		[]apiclient.Task{{ID: 20, ProjectID: 1, State: stateRunning, Title: "Improve takeover UX"}},
	)
	out := p.render(160, 32)
	for _, want := range []string{
		"Projects · 2", "Overview · vincent", "Repository", "Execution defaults",
		"Current workload", "Improve takeover UX",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guided projects render is missing %q:\n%s", want, out)
		}
	}
}

func TestProjectsCompactFallbackKeepsTheTable(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	out := p.render(120, 24)
	if !strings.Contains(out, "running / cap") {
		t.Errorf("compact projects render lost its table:\n%s", out)
	}
	if strings.Contains(out, "Current workload") {
		t.Errorf("compact projects render contains the guided overview:\n%s", out)
	}
}

func TestProjectFormUsesTheGuidedWorkSurface(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	p.render(160, 32)
	pressView(p, "a")
	out := p.render(160, 32)
	for _, want := range []string{"Projects · 1", "vincent", "Add project", "repository", "ctrl+s save"} {
		if !strings.Contains(out, want) {
			t.Errorf("guided project form is missing %q:\n%s", want, out)
		}
	}
}

func TestProjectFormAndFilterSurviveTheGuidedBreakpoint(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	p.render(160, 32)
	pressView(p, "a")
	form := p.form
	form.move(3)
	form.activate()
	pick := form.pick
	p.render(160, 32)
	p.render(120, 24)
	if p.form != form || form.pick != pick || pick == nil {
		t.Error("resize closed or replaced the project form's workflow picker")
	}

	p.form = nil
	pressView(p, "/")
	p.update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	p.render(160, 32)
	p.render(120, 24)
	if !p.filtering || p.filter.Value() != "v" {
		t.Errorf("resize changed the filter: filtering=%v value=%q", p.filtering, p.filter.Value())
	}
}

func TestProjectsHintTheProjectUnderTheCursor(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(7, "vincent")}, nil)
	p.render(100, 24)
	if got := p.hintedProject(); got != 7 {
		t.Errorf("hintedProject() = %d, want 7", got)
	}
}

// helpers

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func isEmptyPatch(t *testing.T, req apiclient.PatchProjectRequest) bool {
	t.Helper()
	return encode(t, req) == "{}"
}
