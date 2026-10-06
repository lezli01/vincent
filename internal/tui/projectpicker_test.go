package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// statsProject is a project row as GET /v1/projects?stats=true serves it.
func statsProject(id int64, name string, attention, active, issues, slots int, maxPar *int) apiclient.Project {
	p := testProject(id, name)
	p.SlotsUsed = slots
	p.MaxParallelTasks = maxPar
	p.Stats = &apiclient.ProjectStats{}
	p.Stats.Tasks.Attention = attention
	p.Stats.Tasks.Active = active
	p.Stats.Issues.Open = issues
	return p
}

// pickerRoot is a connected root whose client answers the project list from
// projects, counting the calls that asked for stats.
func pickerRoot(t *testing.T, projects []apiclient.Project) (*root, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("stats") == "true" {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projects)
	}))
	t.Cleanup(ts.Close)
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.phase = phaseConnected
	m.client = apiclient.New(ts.URL, "t")
	m.width, m.height = 100, 30
	names := make([]apiclient.Project, 0, len(projects))
	for _, p := range projects {
		names = append(names, testProject(p.ID, p.Name))
	}
	m.projects = names
	m.sel = projectSel{id: projects[0].ID, name: projects[0].Name}
	return m, &calls
}

// drainPicker runs cmd and feeds what it returns back into the root, flattening
// batches, until nothing is left — skipping ticks, which tests fire by hand.
func drainPicker(m *root, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			drainPicker(m, c)
		}
	case projectPickerRefreshMsg:
		// A debounce tick: the test decides when it fires.
	default:
		_, next := m.Update(msg)
		drainPicker(m, next)
	}
}

func pressRoot(m *root, msg tea.KeyPressMsg) {
	_, cmd := m.Update(msg)
	drainPicker(m, cmd)
}

func atKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: '@', Text: "@"} }

func twoProjects() []apiclient.Project {
	two := 2
	return []apiclient.Project{
		statsProject(1, "api", 0, 1, 0, 0, nil),
		statsProject(2, "web", 3, 4, 5, 1, &two),
	}
}

func TestProjectPickerOpensFiltersAndSelects(t *testing.T) {
	m, calls := pickerRoot(t, twoProjects())
	var stub []string
	m.views[viewIssues] = &scopeStub{calls: &stub}

	pressRoot(m, atKey())
	if m.projPick == nil {
		t.Fatal("@ did not open the project picker")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("opening made %d stats calls, want exactly one", got)
	}
	if !m.projPick.loaded {
		t.Fatal("the stats answer did not land in the picker")
	}
	if got := m.projPick.matches()[m.projPick.cursor].ID; got != 1 {
		t.Fatalf("the picker opened on project %d, want the current one", got)
	}

	for _, r := range "we" {
		pressRoot(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.projPick.matches(); len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("filter %q matched %v", m.projPick.input.Value(), got)
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.projPick != nil {
		t.Fatal("enter left the picker open")
	}
	if m.sel != (projectSel{id: 2, name: "web"}) || m.selWhy == "" {
		t.Fatalf("sel = %+v (%q), want web", m.sel, m.selWhy)
	}
	if strings.Join(stub, ",") != "setProject:web" {
		t.Errorf("project-scoped views got %v, want the switch", stub)
	}
	if v := m.views[viewChats].(*chatsView); v.project != m.sel {
		t.Errorf("chats view holds %+v, want %+v", v.project, m.sel)
	}
	if got := ansi.Strip(m.headerLine()); !strings.Contains(got, "◆ web") {
		t.Errorf("header %q does not name the new project", got)
	}
}

func TestProjectPickerEscAndPaste(t *testing.T) {
	m, _ := pickerRoot(t, twoProjects())
	pressRoot(m, atKey())
	m.Update(tea.PasteMsg{Content: "web"})
	if got := m.projPick.input.Value(); got != "web" {
		t.Fatalf("paste left the filter %q", got)
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.projPick != nil {
		t.Fatal("esc left the picker open")
	}
	if m.sel.id != 1 {
		t.Fatalf("esc switched the project to %+v", m.sel)
	}
}

func TestProjectPickerIsExclusive(t *testing.T) {
	m, calls := pickerRoot(t, twoProjects())
	// Over the palette, @ types into the palette's search.
	pressRoot(m, tea.KeyPressMsg{Code: ':', Text: ":"})
	pressRoot(m, atKey())
	if m.projPick != nil || m.palette == nil {
		t.Fatal("@ opened the picker over the palette")
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// Over the help sheet, nothing.
	pressRoot(m, tea.KeyPressMsg{Code: '?', Text: "?"})
	pressRoot(m, atKey())
	if m.projPick != nil {
		t.Fatal("@ opened the picker over the help sheet")
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// With the picker up, : and ? type into its filter.
	pressRoot(m, atKey())
	if m.projPick == nil {
		t.Fatal("@ did not open the picker")
	}
	pressRoot(m, tea.KeyPressMsg{Code: ':', Text: ":"})
	pressRoot(m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.palette != nil || m.help {
		t.Fatal("a key opened another popup over the picker")
	}
	if got := m.projPick.input.Value(); got != ":?" {
		t.Errorf("filter = %q, want the keys typed", got)
	}
	// A header click while it is up opens nothing new.
	before := calls.Load()
	m.headerLine()
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.headerHit[0], Y: 0})
	if calls.Load() != before {
		t.Error("a header click refetched the open picker")
	}
	// The palette's row reaches the picker.
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.openPalette()
	var found bool
	for _, e := range m.palette.entries {
		if e.key == "@" && e.global {
			found = true
		}
	}
	if !found {
		t.Fatal("the palette lists no switch-project row")
	}
	for _, r := range "switch project" {
		pressRoot(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette != nil || m.projPick == nil {
		t.Fatal("the palette's switch-project row did not open the picker")
	}
}

func TestProjectPickerHeaderClick(t *testing.T) {
	m, calls := pickerRoot(t, twoProjects())
	m.version = "0.9.0"
	line := ansi.Strip(m.headerLine())
	at := strings.Index(line, "◆")
	if at < 0 {
		t.Fatalf("no segment in %q", line)
	}
	x := ansi.StringWidth(line[:at])
	if x != m.headerHit[0] {
		t.Fatalf("hit starts at %d, the glyph is drawn at %d", m.headerHit[0], x)
	}
	// Outside the segment: nothing.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 0})
	if m.projPick != nil {
		t.Fatal("a click on the app name opened the picker")
	}
	_, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.headerHit[1] - 1, Y: 0})
	drainPicker(m, cmd)
	if m.projPick == nil || calls.Load() != 1 {
		t.Fatalf("a click on the segment: picker %v, %d calls", m.projPick != nil, calls.Load())
	}
}

// TestProjectPickerFetchesOnlyWhileOpen holds the refresh budget: one call on
// open, one per debounced burst while open, none while closed, and a late
// answer for a closed picker dropped.
func TestProjectPickerFetchesOnlyWhileOpen(t *testing.T) {
	m, calls := pickerRoot(t, twoProjects())
	m.notes = make(chan apiclient.Note)
	event := func(typ string) tea.Cmd {
		_, cmd := m.Update(noteMsg{note: apiclient.EventNote{Event: apiclient.Event{Type: typ}}})
		return cmd
	}

	// Closed: events schedule nothing.
	for _, typ := range []string{"task.state_changed", "issue.created", "chat.updated"} {
		event(typ)
	}
	if m.projPickPending {
		t.Fatal("an event armed the picker's debounce while it was closed")
	}
	m.Update(projectPickerRefreshMsg{})
	if calls.Load() != 0 {
		t.Fatalf("%d stats calls while closed", calls.Load())
	}

	pressRoot(m, atKey())
	if calls.Load() != 1 {
		t.Fatalf("opening made %d calls", calls.Load())
	}
	for _, typ := range []string{"task.state_changed", "task.created", "issue.updated", "chat.updated", "project.updated", "step.started"} {
		event(typ)
	}
	if !m.projPickPending {
		t.Fatal("a burst of events armed no refetch")
	}
	_, cmd := m.Update(projectPickerRefreshMsg{})
	drainPicker(m, cmd)
	if calls.Load() != 2 {
		t.Fatalf("a burst made %d refetches, want one", calls.Load()-1)
	}

	// A late answer for a closed picker is dropped.
	seq := m.projPickSeq
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(projectPickerMsg{seq: seq, projects: []apiclient.Project{testProject(9, "late")}})
	if m.projPick != nil {
		t.Fatal("a late answer reopened the picker")
	}
	// And a stale answer for a reopened one too.
	pressRoot(m, atKey())
	m.Update(projectPickerMsg{seq: seq, projects: []apiclient.Project{testProject(9, "late")}})
	for _, p := range m.projPick.projects {
		if p.ID == 9 {
			t.Fatal("a stale answer replaced the reopened picker's rows")
		}
	}
}

func TestProjectPickerRows(t *testing.T) {
	cap4 := 4
	awaitingChat := statsProject(1, "api", 0, 2, 7, 1, nil)
	awaitingChat.Stats.Chats.AwaitingInput = 3
	degraded := testProject(3, "ops")
	degraded.SlotsUsed = 2
	pp := newProjectPicker([]apiclient.Project{
		awaitingChat,
		statsProject(2, "web", 5, 6, 1, 2, &cap4),
		degraded,
	}, 2, "")

	row := func(i, width int) string {
		return ansi.Strip(pp.row(pp.projects[i], false, width))
	}
	if got := row(0, 80); strings.Contains(got, "!") || !strings.Contains(got, "1 running · 2 active · 7 open issues") {
		t.Errorf("uncapped row with chat attention only: %q", got)
	}
	if got := row(1, 80); !strings.Contains(got, "◆   web") || !strings.Contains(got, "!5 · 2/4 running · 6 active · 1 open issues") {
		t.Errorf("capped current row: %q", got)
	}
	if got := strings.TrimSpace(row(2, 80)); got != "ops" {
		t.Errorf("null-stats row = %q, want the name alone", got)
	}

	// Shedding: issues, then active, then running, then attention, then the
	// name shortens.
	w := ansi.StringWidth
	full := w("web") + 2 + w("!5 · 2/4 running · 6 active · 1 open issues")
	for _, c := range []struct {
		drop int
		want string
	}{
		{0, "!5 · 2/4 running · 6 active · 1 open issues"},
		{1, "!5 · 2/4 running · 6 active"},
		{w(" · 1 open issues") + 1, "!5 · 2/4 running"},
		{w(" · 6 active · 1 open issues") + 1, "!5"},
		{w("  !5 · 2/4 running · 6 active · 1 open issues"), ""},
	} {
		name, figures := projectPickerFit(pp.projects[1], full-c.drop)
		if name != "web" || ansi.Strip(figures) != c.want {
			t.Errorf("width %d: %q %q, want web %q", full-c.drop, name, ansi.Strip(figures), c.want)
		}
	}
	if name, figures := projectPickerFit(pp.projects[1], 2); name != "w…" || figures != "" {
		t.Errorf("width 2: %q %q, want the name behind an ellipsis", name, figures)
	}

	out := ansi.Strip(pp.render(64, 12))
	for _, want := range []string{"api", "web", "ops"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}

// TestPaletteOpensPickerWithAtYielded: a tui.keys that gives `@` to another
// operation leaves `project` unbound (task 128's yield), and the palette's
// "switch project" row still opens the picker — it runs its own action, not
// a replay of the key it no longer has (review F3 on #718).
func TestPaletteOpensPickerWithAtYielded(t *testing.T) {
	if warnings := applyKeys(map[string]string{"filter": "@"}); len(warnings) != 1 {
		t.Fatalf("yielding @ warned %q, want one warning", warnings)
	}
	t.Cleanup(func() { setKeymap(keymap.Default()) })
	m, calls := pickerRoot(t, twoProjects())

	m.openPalette()
	m.palette.input.SetValue("switch project")
	got := m.palette.matches()
	if len(got) != 1 || !got[0].unbound {
		t.Fatalf("palette rows for %q: %+v, want the one unbound switch row", m.palette.input.Value(), got)
	}
	pressRoot(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette != nil {
		t.Fatal("enter left the palette open")
	}
	if m.projPick == nil {
		t.Fatal("the palette's switch project row did not open the picker with project unbound")
	}
	if calls.Load() != 1 {
		t.Errorf("opening from the palette made %d stats calls, want one", calls.Load())
	}
}

// The overview row is reached only by ↓ (review F8): a filter matching
// nothing or an empty list never highlights it, enter there just closes, and
// a refetch that adds a match puts the cursor on the match.
func TestProjectPickerOverviewRowOnlyByDown(t *testing.T) {
	pp := newProjectPicker(twoProjects(), 1, "")
	for _, r := range "zzz" {
		pp.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if pp.overview {
		t.Fatal("a filter matching nothing landed on the overview row")
	}
	if out := pp.render(60, 12); strings.Contains(out, "› "+projectPickerOverview) {
		t.Errorf("the overview row is highlighted without ↓:\n%s", out)
	}
	pp.land(projectPickerMsg{projects: append(twoProjects(), apiclient.Project{ID: 3, Name: "zzz"})})
	if m := pp.matches(); pp.overview || len(m) != 1 || m[pp.cursor].ID != 3 {
		t.Fatalf("after a refetch: overview %v, cursor %d; want the new match", pp.overview, pp.cursor)
	}
	pp.update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !pp.overview {
		t.Fatal("↓ past the last match did not reach the overview row")
	}
	pp.land(projectPickerMsg{projects: twoProjects()})
	if !pp.overview {
		t.Fatal("a refetch moved the cursor off the overview row")
	}
	pp.update(tea.KeyPressMsg{Code: tea.KeyUp})
	if pp.overview {
		t.Fatal("↑ stayed on the overview row")
	}
	pp.update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	pp.update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if pick, done, cmd := pp.update(tea.KeyPressMsg{Code: tea.KeyEnter}); pick != nil || !done || cmd != nil {
		t.Errorf("enter on no match: pick %v, done %v, cmd set %v; want a plain close", pick, done, cmd != nil)
	}

	empty := newProjectPicker(nil, 0, "")
	if empty.overview {
		t.Error("a picker over no projects opened on the overview row")
	}
	if _, done, cmd := empty.update(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || cmd != nil {
		t.Error("enter over no projects opened the overview without ↓")
	}
}

// TestProjectPickerDefaultMark is task 132.18 (decision 56): `★` marks the
// project `tui.default_project` names, in its own column after `◆`, by exact
// name; an unset or unknown default marks nothing; names stay aligned; and a
// narrow row sheds figures and then the name, never a mark.
func TestProjectPickerDefaultMark(t *testing.T) {
	projects := twoProjects()
	for _, c := range []struct {
		name     string
		def      string
		current  int64
		api, web string // each row's mark columns, cursor column stripped
	}{
		{"default only", "web", 1, "◆   ", "  ★ "},
		{"both on one row", "api", 1, "◆ ★ ", "    "},
		{"unset", "", 1, "◆   ", "    "},
		{"unknown name", "ops", 1, "◆   ", "    "},
		{"not a prefix or case match", "Web", 2, "    ", "◆   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			pp := newProjectPicker(projects, c.current, c.def)
			for i, want := range []string{c.api, c.web} {
				got := ansi.Strip(pp.row(projects[i], false, 80))
				// The mark columns run from after the cursor column to the name.
				rest := strings.TrimLeft(got[2:], "◆★ ")
				if marks := got[2 : len(got)-len(rest)]; marks != want {
					t.Errorf("row %q: marks %q, want %q", got, marks, want)
				}
				// The name starts in the same cell on every row.
				if at := strings.Index(got, projects[i].Name); ansi.StringWidth(got[:at]) != 6 {
					t.Errorf("row %q: name at cell %d, want 6", got, ansi.StringWidth(got[:at]))
				}
			}
		})
	}

	pp := newProjectPicker(projects, 2, "web")
	for _, width := range []int{40, 20, 8, 7} {
		got := ansi.Strip(pp.row(projects[1], true, width))
		if !strings.HasPrefix(got, "› ◆ ★ ") {
			t.Errorf("width %d: %q lost a mark", width, got)
		}
		if w := ansi.StringWidth(got); w > width {
			t.Errorf("width %d: %q is %d cells wide", width, got, w)
		}
	}
	if got := ansi.Strip(pp.row(projects[1], false, 20)); strings.Contains(got, "open issues") || !strings.Contains(got, "web") {
		t.Errorf("width 20: %q, want figures shed before the name", got)
	}
	if got := ansi.Strip(pp.row(projects[1], false, 7)); got != "  ◆ ★ …" {
		t.Errorf("width 7: %q, want the name cut to an ellipsis", got)
	}
}

// TestProjectPickerDefaultMarkFollowsConfig: a config save landing while the
// picker is open moves the `★` without reopening it (task 132 decision 56).
func TestProjectPickerDefaultMarkFollowsConfig(t *testing.T) {
	m, _ := pickerRoot(t, twoProjects())
	m.Update(configSavedMsg{cfg: apiclient.Config{TUI: apiclient.ConfigTUI{DefaultProject: "api"}}})
	pressRoot(m, atKey())
	if m.projPick == nil || m.projPick.defaultProject != "api" {
		t.Fatalf("picker opened with default %q, want api", m.projPick.defaultProject)
	}
	starred := func() []string {
		var out []string
		for _, line := range strings.Split(ansi.Strip(m.projPick.render(64, 12)), "\n") {
			if strings.Contains(line, projectPickerDefaultGlyph) {
				out = append(out, line)
			}
		}
		return out
	}
	if got := starred(); len(got) != 1 || !strings.Contains(got[0], "api") {
		t.Fatalf("starred rows %q, want api's alone", got)
	}

	m.Update(configSavedMsg{cfg: apiclient.Config{TUI: apiclient.ConfigTUI{DefaultProject: "web"}}})
	if m.projPick == nil {
		t.Fatal("the save closed the picker")
	}
	if got := starred(); len(got) != 1 || !strings.Contains(got[0], "web") {
		t.Fatalf("after the save, starred rows %q, want web's alone", got)
	}
	// A failed save leaves the mark where it was.
	m.Update(configSavedMsg{err: errors.New("boom")})
	if got := starred(); len(got) != 1 || !strings.Contains(got[0], "web") {
		t.Errorf("after a failed save, starred rows %q, want web's still", got)
	}
}
