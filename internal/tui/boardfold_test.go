package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Collapsible board groups (task 054). The fixture is boardgroup_test.go's
// groupedBoard — folding is a view over the grouping, so it is tested against
// the same board the grouping is.

var (
	keyLeft  = tea.KeyPressMsg{Code: tea.KeyLeft}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
	keyFoldC = tea.KeyPressMsg{Code: 'C', Text: "C"}
	keyFoldO = tea.KeyPressMsg{Code: 'O', Text: "O"}
	keyGroup = tea.KeyPressMsg{Code: 'g', Text: "g"}
)

// twoProjectBoard is three tasks over two projects and three workflows: deep
// enough that a nested fold has a sub-header to hide and a sibling to leave
// alone. Queued, so the band sort is id order and the expected rows read off
// the fixture.
func twoProjectBoard() *board {
	return groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("docs")),
		task(3, stateQueued, inProject("web"), inWorkflow("build")),
	)
}

// press drives a key and runs whatever command it returned, which is how the
// runtime would — the fold writes ride one.
func foldPress(b *board, msg tea.KeyPressMsg) {
	_, cmd := b.updateKey(msg)
	if cmd != nil {
		cmd()
	}
	b.render(160, 20)
}

func wantRows(t *testing.T, b *board, want ...string) {
	t.Helper()
	got := rowLabels(b.rows())
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows =\n  %v\nwant\n  %v", got, want)
	}
}

// TestFoldHidesTheSubtreeAndExpandsOneLevel is the shape of the feature: a
// collapsed group takes its tasks *and* its sub-headers off the board, ← walks
// outwards, and → puts back exactly one level — the workflow left folded
// inside the project stays folded.
func TestFoldHidesTheSubtreeAndExpandsOneLevel(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)

	foldPress(b, keyLeft)
	wantRows(t, b,
		"▾ api",
		" ▸ build",
		" ▾ docs", "#2",
		"▾ web",
		" ▾ build", "#3",
	)

	// Again, on the header it just closed: the parent folds, taking the open
	// `docs` sub-header with it.
	foldPress(b, keyLeft)
	wantRows(t, b,
		"▸ api",
		"▾ web",
		" ▾ build", "#3",
	)

	foldPress(b, keyRight)
	wantRows(t, b,
		"▾ api",
		" ▸ build",
		" ▾ docs", "#2",
		"▾ web",
		" ▾ build", "#3",
	)

	foldPress(b, keyRight)
	wantRows(t, b,
		"▾ api",
		" ▾ build", "#1",
		" ▾ docs", "#2",
		"▾ web",
		" ▾ build", "#3",
	)
	if len(b.folds) != 0 {
		t.Errorf("folds left over after unfolding everything: %v", b.folds)
	}
}

// TestFoldingIsAViewOverTheBandSort is the property 009 decision 2 was not
// allowed to lose: the folded board is the unfolded one with rows deleted.
// Group order and within-group order are never recomputed, so a group holding
// a task that needs a human still rises to the top.
func TestFoldingIsAViewOverTheBandSort(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateBlocked, inProject("web"), inWorkflow("deploy")),
		task(3, stateQueued, inProject("api"), inWorkflow("docs")),
	)
	b.render(160, 20)
	unfolded := rowLabels(b.allRows())

	b.folds = b.folds.with(foldPath{"web"})
	folded := rowLabels(b.rows())

	// Everything still on screen is in the order it was, and the only rows
	// missing are the ones under the collapsed header.
	var kept []string
	for _, l := range unfolded {
		if l == "▾ web" {
			kept = append(kept, "▸ web")
			continue
		}
		if strings.HasPrefix(l, " ") || l == "#2" {
			// The `web` subtree: its workflow sub-header and its task.
			if len(kept) > 0 && kept[len(kept)-1] == "▸ web" {
				continue
			}
		}
		kept = append(kept, l)
	}
	if strings.Join(folded, "|") != strings.Join(kept, "|") {
		t.Errorf("folded rows =\n  %v\nwant the unfolded list minus the subtree\n  %v", folded, kept)
	}
	// And the blocked task's group is still first: the fold did not re-sort.
	if b.rows()[0].label != "web" {
		t.Errorf("first group = %q, want the one holding the blocked task", b.rows()[0].label)
	}
}

// cursorRowIsLandable asserts the cursor is on a row the board says it may
// rest on — never off the end, never on an open header, never on a row a fold
// took away.
func cursorRowIsLandable(t *testing.T, b *board, after string) {
	t.Helper()
	rows := b.rows()
	i := b.tbl.Cursor()
	if i < 0 || i >= len(rows) {
		t.Fatalf("%s: cursor at %d, outside the %d rows on screen", after, i, len(rows))
	}
	if rows[i].header && !rows[i].collapsed {
		t.Fatalf("%s: cursor parked on the open header %q", after, rows[i].label)
	}
}

// TestCursorRestsOnAFoldAndStepsOverAnOpenHeader is task 054 decision 2. A
// collapsed header stands in for its tasks, so ↑/↓ stop on it; an expanded one
// names rows that are present, so they step over it as they always did.
func TestCursorRestsOnAFoldAndStepsOverAnOpenHeader(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)

	foldPress(b, keyLeft) // fold api › build, cursor lands on the header it closed
	if r := b.rowAt(b.tbl.Cursor()); !r.header || !r.collapsed || r.label != "build" {
		t.Fatalf("← left the cursor on %+v, want the collapsed `build` header", r)
	}

	// Down from a fold reaches the next task, stepping over `docs`.
	foldPress(b, tea.KeyPressMsg{Code: tea.KeyDown})
	if got, ok := b.selected(); !ok || got != 2 {
		t.Fatalf("down selected %d (ok=%v), want 2", got, ok)
	}
	// And up comes back to the fold rather than sliding past it.
	foldPress(b, tea.KeyPressMsg{Code: tea.KeyUp})
	if r := b.rowAt(b.tbl.Cursor()); !r.header || !r.collapsed {
		t.Fatalf("up did not stop on the collapsed header: %+v", r)
	}
}

// TestCursorNeverLandsOnAHiddenRow walks every way the rows can change under
// the cursor.
func TestCursorNeverLandsOnAHiddenRow(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)

	for _, step := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"←", keyLeft},
		{"← again", keyLeft},
		{"→", keyRight},
		{"C", keyFoldC},
		{"O", keyFoldO},
		{"C again", keyFoldC},
		{"g", keyGroup},
		{"g again", keyGroup},
	} {
		foldPress(b, step.key)
		cursorRowIsLandable(t, b, step.name)
	}

	// A refetch that reorders the board, with folds in place.
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(3, stateBlocked, inProject("web"), inWorkflow("build")),
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("docs")),
	}})
	b.render(160, 20)
	cursorRowIsLandable(t, b, "a refetch")
}

// TestFoldedGroupSwallowingTheCursorKeepsItNearby: a `g` press or a refetch
// that puts the selected task inside a closed group leaves the cursor on the
// header standing in for it, not at the top of the board.
func TestFoldedGroupSwallowingTheCursorKeepsItNearby(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, tea.KeyPressMsg{Code: tea.KeyDown})
	foldPress(b, tea.KeyPressMsg{Code: tea.KeyDown}) // #3, in web › build
	if got, _ := b.selected(); got != 3 {
		t.Fatalf("fixture selected %d, want 3", got)
	}
	b.folds = b.folds.with(foldPath{"web"})
	b.render(160, 20)
	if r := b.rowAt(b.tbl.Cursor()); !r.header || r.label != "web" {
		t.Fatalf("cursor went to %+v, want the `web` header now standing in for #3", r)
	}
}

// TestCollapsedHeaderIsNotATask is the other half of decision 2: a fold has no
// state and no available_actions, so nothing that acts on a task acts on it.
func TestCollapsedHeaderIsNotATask(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, keyLeft)

	if id, ok := b.selected(); ok {
		t.Fatalf("a collapsed header resolved to task %d, want no task", id)
	}
	if got := b.target(); got.id != 0 {
		t.Errorf("action target on a fold = %d, want none", got.id)
	}
	if line := b.actionLine(); line != "" {
		t.Errorf("a fold offered actions: %q", line)
	}
	// space is a statement about a task; there is none here.
	foldPress(b, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if b.hasMarks() {
		t.Errorf("space on a fold marked something: %v", b.marks)
	}
	// The panels keep the task they were showing rather than blanking.
	if b.selectedID != 1 {
		t.Errorf("selectedID = %d, want the last task (1) still remembered", b.selectedID)
	}
}

// TestCollapsedHeaderCountsWhatItHides is what makes the fold honest, and what
// answers the failure 009 decision 4 named: the count, the attention badge and
// the bulk-selection count all survive the fold.
func TestCollapsedHeaderCountsWhatItHides(t *testing.T) {
	b := groupedBoard(
		task(1, stateBlocked, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("api"), inWorkflow("build")),
	)
	b.render(160, 20)
	b.marks = b.marks.add(1, 2)
	b.folds = b.folds.with(foldPath{"api"})

	r := b.rows()[0]
	if !r.collapsed || r.count != 2 || r.attention != 1 || r.marked != 2 {
		t.Fatalf("collapsed header = %+v, want 2 tasks, 1 needing attention, 2 marked", r)
	}
	cell := r.headerCell()
	for _, want := range []string{groupGlyphFolded, "api", "2", attentionBadge, markGlyph} {
		if !strings.Contains(cell, want) {
			t.Errorf("collapsed header cell %q does not carry %q", cell, want)
		}
	}
}

// TestBulkSelectionIgnoresFolds: the selection is a set of tasks, not a view
// (task 011), so `V` reaches into a closed group — and a filter, which is also
// only a view, changes no fold.
func TestBulkSelectionIgnoresFolds(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, keyFoldC)

	foldPress(b, tea.KeyPressMsg{Code: 'V', Text: "V"})
	for _, id := range []int64{1, 2, 3} {
		if !b.marks.has(id) {
			t.Errorf("V did not mark #%d inside a collapsed group (marks %v)", id, b.marks)
		}
	}

	before := slices.Clone(b.folds)
	b.filter.SetValue("web")
	b.render(160, 20)
	if !slices.EqualFunc(b.folds, before, foldPath.equal) {
		t.Errorf("a filter changed the fold set: %v, was %v", b.folds, before)
	}
}

// TestFoldsSurviveRegroupingAndFilters, and drop only when the names in them
// leave the board (task 054 decision 4). The two acceptance criteria conflict
// unless pruning is against task values rather than the rendered grouping.
func TestFoldsSurviveRegroupingAndFilters(t *testing.T) {
	b := groupedBoard(
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("web"), inWorkflow("deploy")),
	)
	b.render(160, 20)
	foldPress(b, keyLeft)
	want := foldPath{"api", "build"}
	if !b.folds.has(want) {
		t.Fatalf("← did not fold %v (folds %v)", want, b.folds)
	}

	// `g` all the way round, including the project-only and flat views where
	// a [project, workflow] path renders nothing at all.
	for range groupingCycle() {
		foldPress(b, keyGroup)
		if !b.folds.has(want) {
			t.Fatalf("grouping %s dropped %v (folds %v)", b.group.label(), want, b.folds)
		}
	}
	b.filter.SetValue("web")
	b.render(160, 20)
	if !b.folds.has(want) {
		t.Fatalf("a filter dropped %v (folds %v)", want, b.folds)
	}
	b.filter.SetValue("")

	// A load that no longer holds the project, which is what archiving one
	// away looks like from here.
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(2, stateQueued, inProject("web"), inWorkflow("deploy")),
	}})
	if b.folds.has(want) {
		t.Errorf("a vanished project was resurrected: %v", b.folds)
	}

	// A disconnected board holds no news about which projects exist.
	b.folds = b.folds.with(want)
	b.updateLoaded(boardLoadedMsg{tasks: nil})
	if !b.folds.has(want) {
		t.Errorf("an empty task list pruned the fold set: %v", b.folds)
	}
}

// TestFlatGroupingHasNoFolds is decision 5: the four keys are inert, and the
// saved set is kept rather than cleared, so cycling back restores it.
func TestFlatGroupingHasNoFolds(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, keyLeft)
	want := slices.Clone(b.folds)

	b.group = nil
	b.render(160, 20)
	for _, k := range []tea.KeyPressMsg{keyLeft, keyRight, keyFoldC, keyFoldO} {
		foldPress(b, k)
		if !slices.EqualFunc(b.folds, want, foldPath.equal) {
			t.Fatalf("%s changed the fold set on a flat board: %v, want %v", k.String(), b.folds, want)
		}
	}
	for _, r := range b.rows() {
		if r.header {
			t.Fatalf("a flat board produced the header %q", r.label)
		}
	}
	// And the hints are not offered for keys that do nothing.
	s := &shell{board: b}
	for _, row := range s.liveBindings(bindingsFor(ctxTasks)) {
		if row.fold {
			t.Errorf("a flat board still advertises %q", row.key)
		}
	}
	b.group = defaultGrouping()
	if !slices.EqualFunc(b.folds, want, foldPath.equal) {
		t.Errorf("folds = %v after cycling back, want %v", b.folds, want)
	}
}

// TestAwaitingInputOpensItsGroup is decision 3's third safeguard: a fold can
// never *become* the board 009 decision 4 was protecting against, because the
// transition that would create one opens it.
func TestAwaitingInputOpensItsGroup(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, keyFoldC)
	if len(b.folds) == 0 {
		t.Fatal("C folded nothing")
	}

	payload, err := json.Marshal(map[string]string{"from": stateRunning, "to": stateAwaitingInput})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	id := int64(2)
	b.updateNote(apiclient.EventNote{Event: apiclient.Event{
		ID: 9, Type: "task.state_changed", TaskID: &id, Payload: payload,
	}})
	b.render(160, 20)

	for _, p := range []foldPath{{"api"}, {"api", "docs"}} {
		if b.folds.has(p) {
			t.Errorf("%v is still folded over a task that started waiting (folds %v)", p, b.folds)
		}
	}
	// The sibling project is none of the transition's business.
	if !b.folds.has(foldPath{"web"}) {
		t.Errorf("an unrelated group was opened too: %v", b.folds)
	}
	if got := rowLabels(b.rows()); !slices.Contains(got, "#2") {
		t.Errorf("the waiting task is still hidden: %v", got)
	}
}

// A transition to anything else leaves the folds alone: opening a group for
// every state change would make the board unfoldable on a busy installation.
func TestOtherTransitionsLeaveFoldsAlone(t *testing.T) {
	b := twoProjectBoard()
	b.render(160, 20)
	foldPress(b, keyFoldC)
	before := slices.Clone(b.folds)

	payload, err := json.Marshal(map[string]string{"to": stateRunning})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	id := int64(2)
	b.updateNote(apiclient.EventNote{Event: apiclient.Event{
		ID: 9, Type: "task.state_changed", TaskID: &id, Payload: payload,
	}})
	if !slices.EqualFunc(b.folds, before, foldPath.equal) {
		t.Errorf("a run transition opened a group: %v, was %v", b.folds, before)
	}
}

// TestJumpAttentionOpensTheGroupItLandsIn: `!` is the key that exists for
// finding work waiting on a human, so a fold is never allowed to be what
// stops it (decision 3).
func TestJumpAttentionOpensTheGroupItLandsIn(t *testing.T) {
	s, _ := newShellFixture(t,
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateAwaitingInput, inProject("web"), inWorkflow("deploy")),
	)
	s.board.group, s.board.configGroup = defaultGrouping(), defaultGrouping()
	s.board.foldsLoaded = true
	s.render(120, 37)
	s.board.updateKey(keyFoldC)
	s.render(120, 37)
	if !s.board.folds.has(foldPath{"web"}) {
		t.Fatalf("C did not fold the waiting task's project: %v", s.board.folds)
	}

	s.jumpAttention()
	if s.board.folds.has(foldPath{"web"}) || s.board.folds.has(foldPath{"web", "deploy"}) {
		t.Errorf("! left the group it jumped into folded: %v", s.board.folds)
	}
	if !s.board.folds.has(foldPath{"api"}) {
		t.Errorf("! opened a group it did not land in: %v", s.board.folds)
	}
}

// TestFoldsRoundTripThroughTUIState: one board's folds are the next board's,
// the §16 acknowledgment survives a fold write, and a field this build does
// not know about survives both (task 054 decision 1).
func TestFoldsRoundTripThroughTUIState(t *testing.T) {
	dir := t.TempDir()
	const seeded = `{"full_auto_notice_ack": true, "written_by_a_later_build": "keep me"}`
	if err := os.WriteFile(filepath.Join(dir, "tui.json"), []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed tui.json: %v", err)
	}

	first := twoProjectBoard()
	first.setDataDir(dir)
	first.render(160, 20)
	foldPress(first, keyLeft)
	if len(first.folds) == 0 {
		t.Fatal("← folded nothing")
	}

	second := twoProjectBoard()
	second.setDataDir(dir)
	if !slices.EqualFunc(second.folds, first.folds, foldPath.equal) {
		t.Errorf("a second board read %v, want %v", second.folds, first.folds)
	}
	if !noticeAcknowledged(dir) {
		t.Error("writing the folds lost the full-auto acknowledgment")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tui.json"))
	if err != nil {
		t.Fatalf("read tui.json: %v", err)
	}
	if !strings.Contains(string(raw), "keep me") {
		t.Errorf("a field this build does not know about was rewritten away:\n%s", raw)
	}

	// And a fold write does not bury the notice for a dir that never saw it.
	fresh := t.TempDir()
	if err := writeFolds(fresh, foldSet{{"api"}}); err != nil {
		t.Fatalf("writeFolds: %v", err)
	}
	if noticeAcknowledged(fresh) {
		t.Error("a fold write acknowledged the full-auto notice")
	}
}

// TestUnreadableFoldsMeanEverythingExpanded is the fail-open direction
// tui.json's existing contract already uses: whatever is wrong with the file,
// the answer is the board every version before this one rendered.
func TestUnreadableFoldsMeanEverythingExpanded(t *testing.T) {
	for name, seed := range map[string]string{
		"no file":     "",
		"not json":    "{{{",
		"wrong shape": `{"board_folds": 7}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if seed != "" {
				if err := os.WriteFile(filepath.Join(dir, "tui.json"), []byte(seed), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			if got := loadFolds(dir); len(got) != 0 {
				t.Errorf("loadFolds = %v, want nothing folded", got)
			}
		})
	}
	if got := loadFolds(""); len(got) != 0 {
		t.Errorf("loadFolds with no data dir = %v, want nothing folded", got)
	}
}

// TestReconnectDoesNotRereadTheFolds: setDataDir runs again on every
// reconnect, and the folds on screen are newer than the ones on disk whenever
// a key was pressed since.
func TestReconnectDoesNotRereadTheFolds(t *testing.T) {
	dir := t.TempDir()
	b := twoProjectBoard()
	b.setDataDir(dir)
	b.folds = b.folds.with(foldPath{"api"})
	b.setDataDir(dir)
	if !b.folds.has(foldPath{"api"}) {
		t.Errorf("a reconnect re-read the file over the folds on screen: %v", b.folds)
	}
}
