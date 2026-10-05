package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task board scoped to the selected project (task 132.8).

const otherProjectID = 2

func inProjectID(id int64, name string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.ProjectID, t.ProjectName = id, name }
}

// twoProjectTasks is a live list spanning the fixture project and another,
// each with a `build` workflow and a task needing a human.
func twoProjectTasks() []apiclient.Task {
	return []apiclient.Task{
		task(1, stateQueued, inWorkflow("build"), withTitle("home build")),
		task(2, stateAwaitingInput, inWorkflow("docs"), withTitle("home docs")),
		task(3, stateQueued, inProjectID(otherProjectID, "other"), inWorkflow("build"), withTitle("other build")),
		task(4, stateBlocked, inProjectID(otherProjectID, "other"), inWorkflow("docs"), withTitle("other docs")),
	}
}

func scopedBoard() *board {
	b := groupedBoard(twoProjectTasks()...)
	b.group, b.configGroup = grouping{groupWorkflow}, grouping{groupWorkflow}
	b.render(160, 20)
	return b
}

func TestBoardListOptionsScopeOnlyTheArchive(t *testing.T) {
	live := selectTestProject(newBoard())
	if got := live.listOptions(); got.ProjectID != 0 {
		t.Errorf("the live listing names project %d, want the global list (decision 17)", got.ProjectID)
	}
	arch := selectTestProject(newArchivedBoard())
	arch.now = func() time.Time { return testNow }
	if got := arch.listOptions(); got.ProjectID != testProjectID {
		t.Errorf("the archived listing names project %d, want %d", got.ProjectID, testProjectID)
	}
}

func TestBoardShowsTheSelectedProjectOnly(t *testing.T) {
	b := scopedBoard()
	if got := ids(b.visible()); !sameIDs(got, []int64{2, 1}) {
		t.Fatalf("visible = %v, want the selected project's 2 and 1", got)
	}
	b.setProject(projectSel{id: otherProjectID, name: "other"})
	if got := ids(b.visible()); !sameIDs(got, []int64{4, 3}) {
		t.Fatalf("after the switch visible = %v, want the other project's 4 and 3", got)
	}
	b.setProject(projectSel{})
	if got := b.visible(); len(got) != 0 {
		t.Errorf("with no selection the board shows %v, want nothing", ids(got))
	}
}

// TestLiveSwitchFetchesNothing: the live listing is global, so a switch is a
// re-derive. The archived board refetches, starts from the first page and
// says it is loading the new project until the answer lands.
func TestLiveSwitchFetchesNothing(t *testing.T) {
	live := scopedBoard()
	live.client = deadClient()
	if cmd := live.setProject(projectSel{id: otherProjectID, name: "other"}); cmd != nil {
		t.Error("the live board's switch issued a load")
	}

	arch := testArchivedBoard()
	arch.client = deadClient()
	arch.page = 2
	if cmd := arch.setProject(projectSel{id: otherProjectID, name: "other"}); cmd == nil {
		t.Fatal("the archived board's switch issued no load")
	}
	if arch.page != 0 {
		t.Errorf("page = %d after a switch, want 0", arch.page)
	}
	if got := arch.listOptions().ProjectID; got != otherProjectID {
		t.Errorf("the archived reload asks for project %d, want %d", got, otherProjectID)
	}
	out := ansi.Strip(arch.render(160, 20))
	if !strings.Contains(out, "loading other…") || strings.Contains(out, "task archived") {
		t.Errorf("the archived board does not say it is loading the new project:\n%s", out)
	}
}

func TestNoProjectColumnAtAnyWidth(t *testing.T) {
	for width := 20; width <= 400; width += 4 {
		for _, g := range []grouping{nil, {groupWorkflow}, {groupProject, groupWorkflow}} {
			cols, _ := boardColumns(width, g, false, fullContent)
			for _, c := range cols {
				if c.Title == "PROJECT" {
					t.Fatalf("width %d %s carries a PROJECT column", width, g.label())
				}
			}
		}
	}
	for _, b := range []*board{newBoard(), newArchivedBoard()} {
		if got := b.filter.Placeholder(); got != "filter by id, title or state" {
			t.Errorf("placeholder = %q", got)
		}
	}
}

// TestFoldsAreKeptPerProject is decision 35: a `[workflow]` fold in one
// project does not collapse the same workflow in another, and switching
// away and back restores it.
func TestFoldsAreKeptPerProject(t *testing.T) {
	b := scopedBoard()
	b.setDataDir(t.TempDir())
	b.setFolds(b.folds().with(foldPath{"build"}))
	wantRows(t, b, "▾ docs", "#2", "▸ build")

	b.setProject(projectSel{id: otherProjectID, name: "other"})
	b.render(160, 20)
	if len(b.folds()) != 0 {
		t.Errorf("the other project inherited folds %v", b.folds())
	}
	wantRows(t, b, "▾ docs", "#4", "▾ build", "#3")

	b.setProject(projectSel{id: testProjectID, name: "proj"})
	wantRows(t, b, "▾ docs", "#2", "▸ build")

	// A live load prunes each project against its own tasks: B has no
	// `deploy`, but that says nothing about A's.
	b.foldsBy = b.foldsBy.with(otherProjectID, foldSet{{"deploy"}})
	b.updateLoaded(boardLoadedMsg{tasks: twoProjectTasks()})
	if len(b.foldsBy[otherProjectID]) != 0 || !b.folds().has(foldPath{"build"}) {
		t.Errorf("prune = %v, want B's dead fold gone and A's kept", b.foldsBy)
	}
}

// TestFoldsFollowAProjectRename: under the default grouping a fold path
// starts with the project's name, and a rename — seen first from a task load
// or from the project list — rewrites it rather than letting the next prune
// drop it (review F10).
func TestFoldsFollowAProjectRename(t *testing.T) {
	renamed := func(name string) []apiclient.Task {
		tasks := twoProjectTasks()
		for i := range tasks {
			if tasks[i].ProjectID == testProjectID {
				tasks[i].ProjectName = name
			}
		}
		return tasks
	}
	for _, c := range []struct {
		name   string
		rename func(b *board, to string)
	}{
		{"by a task load", func(b *board, to string) {
			b.updateLoaded(boardLoadedMsg{tasks: renamed(to)})
		}},
		{"by the project list", func(b *board, to string) {
			b.setProjects([]apiclient.Project{{ID: testProjectID, Name: to}, {ID: otherProjectID, Name: "other"}})
			b.updateLoaded(boardLoadedMsg{tasks: renamed(to)})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := scopedBoard()
			b.group, b.configGroup = defaultGrouping(), defaultGrouping()
			dir := t.TempDir()
			b.setDataDir(dir)
			b.setProjects([]apiclient.Project{{ID: testProjectID, Name: "proj"}, {ID: otherProjectID, Name: "other"}})
			b.updateLoaded(boardLoadedMsg{tasks: twoProjectTasks()})
			b.setFolds(b.folds().with(foldPath{"proj", "build"}))
			c.rename(b, "api")
			if !b.folds().has(foldPath{"api", "build"}) || len(b.folds()) != 1 {
				t.Fatalf("folds after the rename = %v, want [api build]", b.folds())
			}
			if got := readTUIState(dir).BoardFoldsByProject[testProjectID]; len(got) != 1 || !foldPath(got[0]).equal(foldPath{"api", "build"}) {
				t.Errorf("persisted = %v, want the rewritten path", got)
			}
		})
	}
}

func TestArchivedLoadPrunesNoFolds(t *testing.T) {
	b := testArchivedBoard()
	b.setFolds(foldSet{{"gone"}})
	b.updateLoaded(boardLoadedMsg{archived: true, tasks: []apiclient.Task{task(9, stateArchived)}})
	if !b.folds().has(foldPath{"gone"}) {
		t.Errorf("an archived load pruned the folds: %v", b.foldsBy)
	}
}

// TestLegacyFoldsMigrateToProjectIDs: a project-prefixed path moves under
// that project's id, a bare project header and a project-less path are
// dropped, and the legacy field leaves the file — but only once the project
// list has arrived.
func TestLegacyFoldsMigrateToProjectIDs(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"full_auto_notice_ack": true, "board_folds": [["proj", "build"], ["proj"], ["build"], ["gone", "docs"]]}`
	if err := os.WriteFile(filepath.Join(dir, "tui.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	b := selectTestProject(newBoard())
	b.setDataDir(dir)
	if len(b.foldsBy) != 0 || len(b.legacyFolds) != 4 {
		t.Fatalf("before the project list: folds %v, legacy %v", b.foldsBy, b.legacyFolds)
	}
	if !strings.Contains(readFile(t, dir), `"board_folds"`) {
		t.Fatal("the legacy field was dropped before it was migrated")
	}

	b.setProjects([]apiclient.Project{{ID: testProjectID, Name: "proj"}, {ID: otherProjectID, Name: "other"}})
	if got := b.foldsBy[testProjectID]; len(got) != 1 || !got.has(foldPath{"proj", "build"}) {
		t.Errorf("migrated %v, want only [proj build] under the project", b.foldsBy)
	}
	if len(b.foldsBy) != 1 {
		t.Errorf("migrated sets %v, want one project", b.foldsBy)
	}
	raw := readFile(t, dir)
	if strings.Contains(raw, `"board_folds"`) || !strings.Contains(raw, "full_auto_notice_ack") {
		t.Errorf("tui.json after migration:\n%s", raw)
	}
	var st tuiState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if got := st.BoardFoldsByProject[testProjectID]; len(got) != 1 {
		t.Errorf("persisted %v", st.BoardFoldsByProject)
	}

	// A second board reads the migrated sets, and a removed project's set
	// is dropped when it leaves the list.
	second := selectTestProject(newBoard())
	second.setDataDir(dir)
	if !second.folds().has(foldPath{"proj", "build"}) {
		t.Errorf("a second board read %v", second.foldsBy)
	}
	second.setProjects([]apiclient.Project{{ID: otherProjectID, Name: "other"}})
	if len(second.foldsBy) != 0 {
		t.Errorf("a removed project's folds survived: %v", second.foldsBy)
	}
}

func readFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "tui.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAttentionCountIsGlobalAndLabelled is decision 34: the header counts
// every project's attention and says `(all projects)` while some is
// elsewhere — over a committed filter's `(all tasks)` — and `!` walks the
// selected project only.
func TestAttentionCountIsGlobalAndLabelled(t *testing.T) {
	b := scopedBoard()
	if got := headerText(b); !strings.Contains(got, "2 need attention "+allProjectsLabel) {
		t.Errorf("header = %q, want both projects counted and labelled", got)
	}
	b.filter.SetValue("docs")
	if got := headerText(b); !strings.Contains(got, allProjectsLabel) || strings.Contains(got, "(all tasks)") {
		t.Errorf("header with a filter = %q, want the one wider label", got)
	}
	if got := b.attentionTally(); !got.allProjects || got.n != 2 {
		t.Errorf("tally = %+v", got)
	}
	footer := ansi.Strip(renderFooterTally(b))
	if !strings.Contains(footer, "next attention (2, all projects)") {
		t.Errorf("footer = %q", footer)
	}

	b.tasks = b.tasks[:2] // the other project's question answered
	if got := headerText(b); strings.Contains(got, allProjectsLabel) {
		t.Errorf("header = %q, labelled with nothing elsewhere", got)
	}

	s, _ := newShellFixture(t, twoProjectTasks()...)
	_ = s.jumpAttention()
	if id, _ := s.board.selected(); id != 2 {
		t.Errorf("! landed on %d, want the selected project's 2", id)
	}
	_ = s.jumpAttention()
	if id, _ := s.board.selected(); id != 2 {
		t.Errorf("! reached %d in another project", id)
	}
}

func renderFooterTally(b *board) string {
	line, _ := buildFooter(200, nil, &actionBar{}, taskActions{}, b.attentionTally(), false, false)
	return line
}

// TestMarksInAnotherProjectAreNotDispatched: a mark survives a switch away
// but is neither counted nor dispatched while its project is not selected.
func TestMarksInAnotherProjectAreNotDispatched(t *testing.T) {
	b := scopedBoard()
	b.marks = markSet{1, 3}
	if got := b.projectMarks(); len(got) != 1 || !got.has(1) {
		t.Errorf("projectMarks = %v, want only 1", got)
	}
	for _, m := range b.markedTargets() {
		if m.id == 3 {
			t.Error("a mark in another project was dispatched")
		}
	}
	b.setProject(projectSel{id: otherProjectID, name: "other"})
	if got := b.projectMarks(); len(got) != 1 || !got.has(3) {
		t.Errorf("after the switch projectMarks = %v, want only 3", got)
	}

	// esc clears the selected project's marks and leaves the other's
	// (review F11).
	b.clearMarks()
	if b.hasMarks() {
		t.Error("esc left a mark in the selected project")
	}
	b.setProject(projectSel{id: testProjectID, name: "proj"})
	if got := b.projectMarks(); len(got) != 1 || !got.has(1) {
		t.Errorf("after esc elsewhere projectMarks = %v, want 1 kept", got)
	}
}
