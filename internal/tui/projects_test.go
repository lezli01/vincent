package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
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
// read as though it were capped at the daemon-wide figure. Both numerators
// are the served `slots_used` (§11), so the projects carry the figure rather
// than a task list to be walked — see capCell and issue #324.
func TestProjectCapCellSeparatesTheProjectCapFromTheGlobalOne(t *testing.T) {
	p := newProjectsView()
	capped := testProject(1, "capped")
	two := 2
	capped.MaxParallelTasks = &two
	capped.SlotsUsed = 1
	uncapped := testProject(2, "uncapped")
	uncapped.SlotsUsed = 1
	loadedProjects(p, []apiclient.Project{capped, uncapped}, []apiclient.Task{
		{ID: 12, ProjectID: 2, State: stateQueued},
	})

	if got, want := p.capCell(capped), "1 / 2"; got != want {
		t.Errorf("capped cell = %q, want %q", got, want)
	}
	got := p.capCell(uncapped)
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

	pressView(p, "D")
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

// The overview fetches on activation and on the events that move a figure —
// task, issue, chat and project, plus the registry for the form's picker —
// and on nothing at all while it is hidden (task 132.15).
func TestProjectsRefetchOnActivationAndOnEvents(t *testing.T) {
	p := newProjectsView()
	if cmd := p.setClient(&apiclient.Client{}); cmd != nil {
		t.Error("connecting fetched while the overview is hidden")
	}
	event := func(typ string) tea.Cmd {
		p.refreshPending = false
		_, cmd := p.update(noteMsg{note: apiclient.EventNote{
			Event: apiclient.Event{Type: typ, Payload: json.RawMessage("{}")},
		}})
		return cmd
	}
	if cmd := event("task.state_changed"); cmd != nil {
		t.Error("an event scheduled a refresh while the overview is hidden")
	}
	if _, cmd := p.update(viewActivatedMsg{id: viewProjects}); cmd == nil {
		t.Error("activation did not refetch")
	}
	if _, cmd := p.update(viewActivatedMsg{id: viewHome}); cmd != nil {
		t.Error("another view's activation triggered a fetch")
	}
	for _, evType := range []string{
		"project.created", "task.state_changed", "issue.created", "chat.state_changed",
		eventWorkflowRegistryChanged,
	} {
		if event(evType) == nil {
			t.Errorf("%s did not schedule a refresh", evType)
		}
	}
	p.update(viewDeactivatedMsg{id: viewProjects})
	if cmd := event("task.state_changed"); cmd != nil {
		t.Error("an event scheduled a refresh after the overview was left")
	}
	p.refreshPending = true
	if _, cmd := p.update(projectsRefreshMsg{}); cmd != nil {
		t.Error("a debounce that fired after the overview was left still fetched")
	}
}

// overviewProject is a project row carrying every figure the table renders.
func overviewProject(id int64, name string, attention, queued, blocked, done, issues, imported, chats, waiting int) apiclient.Project {
	pr := testProject(id, name)
	pr.Stats = &apiclient.ProjectStats{}
	pr.Stats.Tasks.Attention = attention
	pr.Stats.Tasks.ByState = map[string]int{stateQueued: queued, stateBlocked: blocked, stateDone: done}
	pr.Stats.Issues.Open, pr.Stats.Issues.OpenImported = issues, imported
	pr.Stats.Chats.Live, pr.Stats.Chats.AwaitingInput = chats, waiting
	return pr
}

// overviewLine finds the rendered line that starts with label, stripped.
func overviewLine(t *testing.T, out, label string) []string {
	t.Helper()
	for line := range strings.SplitSeq(ansi.Strip(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == label {
			return f
		}
	}
	t.Fatalf("no %q line in:\n%s", label, ansi.Strip(out))
	return nil
}

func TestProjectOverviewRendersEveryProjectAndTheirTotals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		projects []apiclient.Project
		want     []string // the totals row's fields after "total"
	}{
		{
			"one",
			[]apiclient.Project{overviewProject(1, "api", 1, 2, 3, 4, 5, 1, 2, 1)},
			[]string{"1", "4/3", "2", "3", "4", "5", "(1)", "2", "(1)"},
		},
		{"several", []apiclient.Project{
			overviewProject(1, "api", 1, 2, 3, 4, 5, 1, 2, 1),
			overviewProject(2, "web", 2, 0, 1, 10, 0, 0, 1, 0),
			overviewProject(3, "docs", 0, 1, 0, 0, 3, 3, 0, 0),
		}, []string{"3", "4/3", "3", "4", "14", "8", "(4)", "3", "(1)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProjectsView()
			p.update(projectsLoadedMsg{
				projects: tc.projects,
				// The installation-wide slots are /v1/info's, deliberately
				// unlike any sum of the rows' slots_used.
				info:   apiclient.Info{MaxParallelTasks: 3, Slots: apiclient.InfoSlots{Used: 4}},
				infoOK: true,
			})
			out := p.render(130, 30)
			for _, pr := range tc.projects {
				overviewLine(t, out, pr.Name)
			}
			got := overviewLine(t, out, "total")[1:]
			if len(got) < len(tc.want) || strings.Join(got[:len(tc.want)], " ") != strings.Join(tc.want, " ") {
				t.Errorf("totals = %v, want %v first", got, tc.want)
			}
		})
	}
}

func TestProjectOverviewZeroProjectsSaysHowToAddOne(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, nil, nil)
	out := ansi.Strip(p.render(130, 30))
	for _, want := range []string{"press a", "vincent project add"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty overview is missing %q:\n%s", want, out)
		}
	}
}

// A row the daemon could not count reads as unknown, never as zeros, and
// adds nothing to the totals.
func TestProjectOverviewNullStatsRenderDashes(t *testing.T) {
	p := newProjectsView()
	counted := overviewProject(1, "api", 2, 0, 0, 0, 0, 0, 0, 0)
	unknown := testProject(2, "web")
	unknown.SlotsUsed = 1
	loadedProjects(p, []apiclient.Project{counted, unknown}, nil)
	out := p.render(130, 30)
	f := overviewLine(t, out, "web")
	if f[1] != "—" || f[2] != "1" || f[3] != "—" {
		t.Errorf("null-stats row = %v, want a dash for attention, the served slots, then dashes", f)
	}
	if got := overviewLine(t, out, "total")[1]; got != "2" {
		t.Errorf("total attention = %s, want only the counted row's 2", got)
	}
}

// Narrowing sheds the detail pane first, then columns in ovShedOrder, and the
// name, attention and running stay to the end.
func TestProjectOverviewShedsDetailThenColumns(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{overviewProject(1, "api", 0, 0, 0, 0, 0, 0, 0, 0)}, nil)
	wide := overviewFullWidth() + overviewDetailWidth
	if out := p.render(wide, 30); !strings.Contains(out, "Execution defaults") {
		t.Errorf("width %d has no detail pane:\n%s", wide, out)
	}
	full := overviewFullWidth()
	out := p.render(full, 30)
	if strings.Contains(out, "Execution defaults") {
		t.Errorf("width %d kept the detail pane beside a full table", full)
	}
	if cols, _ := overviewColumns(full); len(cols) != int(ocCount) {
		t.Errorf("width %d shed a column before it had to: %v", full, cols)
	}
	prev := int(ocCount)
	shed := 0
	for width := full; width >= 28; width-- {
		cols, name := overviewColumns(width)
		total := 0
		for _, c := range cols {
			w := ovColSpec[c].width
			if c == ocName {
				w = name
			}
			total += w + colPadding
		}
		if total > width && len(cols) > 3 {
			t.Errorf("width %d: columns need %d cells", width, total)
		}
		if len(cols) < prev {
			kept := map[ovCol]bool{}
			for _, c := range cols {
				kept[c] = true
			}
			for _, c := range ovShedOrder[:shed+prev-len(cols)] {
				if kept[c] {
					t.Errorf("width %d kept %q but shed a later column", width, ovColSpec[c].title)
				}
			}
			shed += prev - len(cols)
			prev = len(cols)
		}
	}
	cols, _ := overviewColumns(28)
	if len(cols) != 3 || cols[0] != ocName || cols[1] != ocAttention || cols[2] != ocRunning {
		t.Errorf("narrowest columns = %v, want name, attention, running", cols)
	}
}

// The header and the rows must agree about how many cells a row has, or the
// table indexes out of range on the next resize.
func TestProjectRowsMatchTheirColumns(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	for _, width := range []int{50, 100, 200} {
		cols, _ := overviewColumns(width)
		rows := p.rowsFor(p.visible(), cols)
		if len(rows) == 0 {
			t.Fatal("no rows built")
		}
		if len(rows[0]) != len(cols) {
			t.Errorf("width %d: row has %d cells, header has %d", width, len(rows[0]), len(cols))
		}
	}
}

// A resize across a breakpoint must not panic or lose the selection.
func TestProjectsSurviveAColumnBreakpoint(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "a"), testProject(2, "b")}, nil)
	p.render(200, 24)
	p.tbl.SetCursor(1)
	p.rememberSelection()
	p.render(60, 24)
	p.render(200, 24)
	if got, ok := p.current(); !ok || got.ID != 2 {
		t.Errorf("selection after two resizes = %+v, want project 2", got)
	}
}

// The "needs you" list is the board's `!` filter over every project, in the
// board's order — oldest wait first — each row naming its project.
func TestProjectOverviewAttentionListAcrossProjects(t *testing.T) {
	p := newProjectsView()
	at := func(id int64, project, state string, minutes int) apiclient.Task {
		return apiclient.Task{
			ID: id, ProjectID: id / 10, ProjectName: project, State: state, Title: "t" + strconv.FormatInt(id, 10),
			UpdatedAt: time.Date(2026, 10, 5, 12, minutes, 0, 0, time.UTC),
		}
	}
	loadedProjects(p,
		[]apiclient.Project{testProject(1, "api"), testProject(2, "web")},
		[]apiclient.Task{
			at(11, "api", stateRunning, 0),
			at(12, "api", stateBlocked, 30),
			at(21, "web", stateAwaitingInput, 10),
			at(22, "web", stateQueued, 0),
		})
	attn := p.attention()
	if len(attn) != 2 || attn[0].ID != 21 || attn[1].ID != 12 {
		t.Fatalf("attention = %v, want 21 then 12", attn)
	}
	out := ansi.Strip(p.render(130, 30))
	i21, i12 := strings.Index(out, "web  #21"), strings.Index(out, "api  #12")
	if i21 < 0 || i12 < 0 || i21 > i12 {
		t.Errorf("needs-you rows missing, unprefixed or out of order:\n%s", out)
	}
	if strings.Contains(out, "#11") || strings.Contains(out, "#22") {
		t.Errorf("a task that does not need anyone is listed:\n%s", out)
	}
}

// Enter selects (task 132 decision 42); `e` alone edits.
func TestProjectOverviewEnterPicksAndEEdits(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p,
		[]apiclient.Project{testProject(1, "api"), testProject(2, "web")},
		[]apiclient.Task{{ID: 21, ProjectID: 2, ProjectName: "web", State: stateBlocked}})
	p.render(130, 30)
	pressView(p, "down")
	msg, ok := pressView(p, "enter")().(overviewPickMsg)
	if !ok || msg.project.ID != 2 || msg.task != nil {
		t.Fatalf("enter on a project row = %+v, want project 2 picked", msg)
	}
	if p.form != nil {
		t.Error("enter opened the edit form")
	}

	pressView(p, "tab")
	msg, ok = pressView(p, "enter")().(overviewPickMsg)
	if !ok || msg.task == nil || msg.task.ID != 21 || msg.project.ID != 2 || msg.project.Name != "web" {
		t.Fatalf("enter on a needs-you row = %+v, want task 21 in web", msg)
	}
	pressView(p, "tab")
	if p.inAttention {
		t.Error("tab did not return to the table")
	}

	pressView(p, "e")
	if p.form == nil || p.form.adding() {
		t.Error("e did not open the edit form")
	}
}

// With nothing that needs anyone, tab has nowhere to go.
func TestProjectOverviewTabSkipsAnEmptyList(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
	pressView(p, "tab")
	if p.inAttention {
		t.Error("tab moved into an empty needs-you list")
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

// A project with no GitHub remote has no integration to fail: both its
// GitHub and sync cells read as a dash, never as a reason or ✗.
func TestProjectOverviewNotGitHubIsADash(t *testing.T) {
	p := newProjectsView()
	pr := overviewProject(1, "api", 0, 0, 0, 0, 0, 0, 0, 0)
	pr.Stats.IssueSync.Enabled, pr.Stats.IssueSync.Reason = true, "not_github"
	failing := overviewProject(2, "web", 0, 0, 0, 0, 0, 0, 0, 0)
	failing.Stats.IssueSync.Enabled = true
	loadedProjects(p, []apiclient.Project{pr, failing}, nil)
	p.update(githubProbeMsg{projects: []githubProject{
		{project: pr, status: apiclient.GitHubStatus{Enabled: true, Reason: "not_github"}},
		{project: failing, status: apiclient.GitHubStatus{Enabled: true, Available: true, Repo: "acme/web"}},
	}})
	if got := p.overviewCell(pr, ocGitHub); got != "—" {
		t.Errorf("github cell = %q, want a dash", got)
	}
	if got := p.overviewCell(pr, ocSync); got != "—" {
		t.Errorf("sync cell = %q, want a dash", got)
	}
	if got := p.overviewCell(failing, ocSync); got != "✗" {
		t.Errorf("failing sync cell = %q, want ✗", got)
	}
	if got := p.overviewCell(failing, ocGitHub); got != "✓ acme/web" {
		t.Errorf("github cell = %q, want the repository", got)
	}
}
