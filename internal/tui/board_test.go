package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

var testNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// testBoard is the flat table: sorting, filtering, columns and selection are
// the same questions grouped or not, and answering them without group headers
// in the way keeps the row index equal to the task index. Grouping has its
// own fixture and its own file (boardgroup_test.go).
func testBoard() *board {
	b := newBoard()
	b.now = func() time.Time { return testNow }
	b.bell = func() {}
	b.loaded = true
	b.group, b.configGroup = nil, nil
	return b
}

func task(id int64, state string, opts ...func(*apiclient.Task)) apiclient.Task {
	t := apiclient.Task{
		ID: id, State: state, Title: "task " + state,
		ProjectName: "proj", StepTotal: 3, StepName: "build",
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func withUpdated(at time.Time) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.UpdatedAt = at }
}

func withStarted(at time.Time) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.StartedAt = &at }
}

func withPriority(p int) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.Priority = p }
}

func withCreated(at time.Time) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.CreatedAt = at }
}

func ids(tasks []apiclient.Task) []int64 {
	out := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func equalIDs(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSortBandsPinAttentionAboveEverything is §15's pinning rule: work
// waiting on a human is never below work that is not.
func TestSortBandsPinAttentionAboveEverything(t *testing.T) {
	tasks := []apiclient.Task{
		task(1, stateDone),
		task(2, stateRunning, withStarted(testNow.Add(-time.Minute))),
		task(3, stateQueued),
		task(4, stateBlocked),
		task(5, statePaused),
		task(6, stateAwaitingInput),
	}
	sortTasks(tasks)
	got := ids(tasks)
	// Both attention tasks first (equal UpdatedAt keeps input order), then
	// running, queued, paused, terminal.
	if !equalIDs(got, 4, 6, 2, 3, 5, 1) {
		t.Errorf("order = %v, want [4 6 2 3 5 1]", got)
	}
}

// TestSortQueuedFollowsSchedulerOrder means the board answers "what runs
// next" without the reader knowing §11's rule.
func TestSortQueuedFollowsSchedulerOrder(t *testing.T) {
	older := testNow.Add(-time.Hour)
	tasks := []apiclient.Task{
		task(10, stateQueued, withPriority(0), withCreated(older)),
		task(11, stateQueued, withPriority(5), withCreated(testNow)),
		task(12, stateQueued, withPriority(5), withCreated(older)),
	}
	sortTasks(tasks)
	// priority DESC, then created_at ASC, then id ASC.
	if got := ids(tasks); !equalIDs(got, 12, 11, 10) {
		t.Errorf("order = %v, want [12 11 10]", got)
	}
}

// TestSortAttentionOldestWaitFirst surfaces the task most likely forgotten.
func TestSortAttentionOldestWaitFirst(t *testing.T) {
	tasks := []apiclient.Task{
		task(1, stateAwaitingGate, withUpdated(testNow.Add(-time.Minute))),
		task(2, stateBlocked, withUpdated(testNow.Add(-time.Hour))),
		task(3, stateAwaitingInput, withUpdated(testNow.Add(-time.Second))),
	}
	sortTasks(tasks)
	if got := ids(tasks); !equalIDs(got, 2, 1, 3) {
		t.Errorf("order = %v, want [2 1 3] (oldest wait first)", got)
	}
}

// TestSortRunningLongestFirst surfaces a wedged task.
func TestSortRunningLongestFirst(t *testing.T) {
	tasks := []apiclient.Task{
		task(1, stateRunning, withStarted(testNow.Add(-time.Minute))),
		task(2, stateRunning, withStarted(testNow.Add(-time.Hour))),
		task(3, stateRunning),
	}
	sortTasks(tasks)
	if got := ids(tasks); !equalIDs(got, 2, 1, 3) {
		t.Errorf("order = %v, want [2 1 3]", got)
	}
}

func TestFilterTasks(t *testing.T) {
	tasks := []apiclient.Task{
		{ID: 1, Title: "add board view", ProjectName: "vincent", State: stateRunning},
		{ID: 2, Title: "fix procx", ProjectName: "other", State: stateDone},
		{ID: 42, Title: "unrelated", ProjectName: "vincent", State: stateQueued},
	}
	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{1, 2, 42}},
		{"board", []int64{1}},
		{"VINCENT", []int64{1, 42}}, // case-insensitive
		{"done", []int64{2}},        // matches state
		{"42", []int64{42}},         // matches id
		{"  board  ", []int64{1}},   // trimmed
		{"nothingatall", []int64{}}, // empty result is legitimate
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := ids(filterTasks(tasks, tc.query)); !equalIDs(got, tc.want...) {
				t.Errorf("filter(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestColumnsDropByPriority pins the degradation order: cost, then the step
// name, then the workflow, then the project.
//
// The workflow outranks the step name deliberately. "survey" tells a reader
// nothing on its own — it needs the workflow it belongs to — while the
// workflow alone still says what a task is doing.
func TestColumnsDropByPriority(t *testing.T) {
	for _, tc := range []struct {
		width                             int
		project, workflow, stepName, cost bool
	}{
		{220, true, true, true, true},
		{160, true, true, true, true},
		{115, true, true, true, false},   // cost goes first
		{100, true, true, false, false},  // then the step name
		{85, true, false, false, false},  // then the workflow
		{70, false, false, false, false}, // then the project
	} {
		got := columnsFor(tc.width, nil, false)
		if got.project != tc.project || got.workflow != tc.workflow ||
			got.stepName != tc.stepName || got.cost != tc.cost {
			t.Errorf("width %d = %+v, want project=%v workflow=%v stepName=%v cost=%v",
				tc.width, got, tc.project, tc.workflow, tc.stepName, tc.cost)
		}
	}
}

// TestBoardColumnsFitWidth keeps the table from overflowing the terminal,
// which is what makes the whole board wrap into unreadable mush. Dropping a
// column has to actually buy back the space — an earlier version clamped the
// title to its minimum instead and overflowed by a character at width 80.
func TestBoardColumnsFitWidth(t *testing.T) {
	// 65 is the narrowest board that still fits: below it every optional column
	// is already shed and the title is clamped at its minimum, which the
	// columns deliberately overflow rather than hide the id or the state. A
	// bulk selection raises that floor by exactly the marker column's three
	// cells (task 011) — nothing is left for them to come out of.
	for width := 65; width <= 220; width++ {
		for _, marking := range []bool{false, true} {
			if marking && width < 68 {
				continue
			}
			cols, _ := boardColumns(width, nil, marking)
			total := 0
			for _, c := range cols {
				total += c.Width + colPadding
			}
			if total > width {
				t.Fatalf("width %d (marking=%v): columns total %d, which overflows",
					width, marking, total)
			}
		}
	}
}

// TestBoardColumnsAlwaysKeepNavigationColumns: however narrow the terminal,
// the columns you steer by survive.
func TestBoardColumnsAlwaysKeepNavigationColumns(t *testing.T) {
	for _, width := range []int{20, 40, 65, 120} {
		cols, _ := boardColumns(width, nil, false)
		titles := make([]string, 0, len(cols))
		for _, c := range cols {
			titles = append(titles, c.Title)
		}
		joined := strings.Join(titles, ",")
		for _, must := range []string{"ID", "TITLE", "STATE", "ELAPSED"} {
			if !strings.Contains(joined, must) {
				t.Errorf("width %d dropped %s (columns: %s)", width, must, joined)
			}
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := formatElapsed(45 * time.Second); got != "45s" {
		t.Errorf("elapsed 45s = %q", got)
	}
	if got := formatElapsed(4*time.Minute + 12*time.Second); got != "4m12s" {
		t.Errorf("elapsed 4m12s = %q", got)
	}
	if got := formatElapsed(90 * time.Minute); got != "1h30m" {
		t.Errorf("elapsed 90m = %q", got)
	}
	// A task no adapter costed shows a dash: $0.00 is a claim.
	if got := formatCost(nil); got != "—" {
		t.Errorf("nil cost = %q, want a dash", got)
	}
	c := 0.41
	if got := formatCost(&c); got != "$0.41" {
		t.Errorf("cost = %q", got)
	}
	// An unparsable snapshot has no step count; "1/0" would be a lie.
	if got := formatStep(apiclient.Task{StepTotal: 0}, true); got != "—" {
		t.Errorf("stepless task = %q, want a dash", got)
	}
	withName := formatStep(apiclient.Task{CurrentStep: 1, StepTotal: 6, StepName: "build"}, true)
	if withName != "2/6 build" {
		t.Errorf("step = %q, want %q", withName, "2/6 build")
	}
	if noName := formatStep(apiclient.Task{CurrentStep: 1, StepTotal: 6, StepName: "build"}, false); noName != "2/6" {
		t.Errorf("narrow step = %q, want %q", noName, "2/6")
	}
}

// TestAttentionBadgeSurvivesMonochrome: the pin is a glyph, not colour
// alone, so it still reads without ANSI support.
func TestAttentionBadgeSurvivesMonochrome(t *testing.T) {
	rendered := renderState(stateAwaitingInput)
	if !strings.Contains(lipgloss.NewStyle().Render(rendered), attentionBadge) {
		t.Errorf("rendered state %q lacks the %q badge", rendered, attentionBadge)
	}
	if strings.Contains(renderState(stateRunning), attentionBadge) {
		t.Error("running is not a needs-attention state and must not be badged")
	}
}

func stateEvent(id int64, to string) apiclient.EventNote {
	payload, _ := json.Marshal(map[string]string{"to": to})
	return apiclient.EventNote{Event: apiclient.Event{
		ID: id, Type: "task.state_changed", Payload: payload,
	}}
}

// TestBellRingsOnAwaitingInput covers the three rules that make the bell
// bearable: only awaiting_input rings, a replayed event id is silent, and a
// burst collapses into one interruption.
func TestBellRingsOnAwaitingInput(t *testing.T) {
	b := testBoard()
	rings := 0
	b.bell = func() { rings++ }
	now := testNow
	b.now = func() time.Time { return now }

	run := func(n apiclient.Note) {
		if cmd := b.updateNote(n); cmd != nil {
			if msg := cmd(); msg != nil {
				// Batch: run the contained commands, which is where the bell
				// call lives.
				if batch, ok := msg.(tea.BatchMsg); ok {
					for _, c := range batch {
						if c != nil {
							c()
						}
					}
				}
			}
		}
	}

	run(stateEvent(1, stateRunning))
	if rings != 0 {
		t.Fatalf("rings = %d after a non-awaiting transition, want 0", rings)
	}

	run(stateEvent(2, stateAwaitingInput))
	if rings != 1 {
		t.Fatalf("rings = %d after awaiting_input, want 1", rings)
	}

	// A Last-Event-ID replay re-delivers the same event: ids are monotonic,
	// so it must be silent.
	run(stateEvent(2, stateAwaitingInput))
	if rings != 1 {
		t.Errorf("rings = %d after a replay, want 1 — a reconnect must not ring again", rings)
	}

	// A second, genuinely new event inside the rate-limit window is one
	// interruption, not two.
	run(stateEvent(3, stateAwaitingInput))
	if rings != 1 {
		t.Errorf("rings = %d inside the rate-limit window, want 1", rings)
	}

	// Past the window, a new event rings again.
	now = testNow.Add(2 * bellInterval)
	run(stateEvent(4, stateAwaitingInput))
	if rings != 2 {
		t.Errorf("rings = %d past the rate-limit window, want 2", rings)
	}

	// And the event swallowed by the rate limit still cannot ring on replay.
	now = testNow.Add(4 * bellInterval)
	run(stateEvent(3, stateAwaitingInput))
	if rings != 2 {
		t.Errorf("rings = %d replaying a rate-limited event, want 2", rings)
	}
}

// TestRefreshFailureKeepsRows: a failed refresh is not a lost connection.
func TestRefreshFailureKeepsRows(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateRunning)}})
	rendered := b.render(120, 20)
	if !strings.Contains(rendered, "task running") {
		t.Fatalf("row missing before the failure: %q", rendered)
	}

	b.updateLoaded(boardLoadedMsg{err: errors.New("http 500")})
	rendered = b.render(120, 20)
	if !strings.Contains(rendered, "task running") {
		t.Errorf("rows were dropped on a failed refresh: %q", rendered)
	}
	if !strings.Contains(rendered, "refresh failed") {
		t.Errorf("stale board does not say so: %q", rendered)
	}
	if !strings.Contains(rendered, "showing") {
		t.Errorf("stale board does not say how stale it is: %q", rendered)
	}

	// Recovery clears the warning.
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateRunning)}})
	if strings.Contains(b.render(120, 20), "refresh failed") {
		t.Error("the stale warning survived a successful refresh")
	}
}

// TestEmptyStatesAreDistinct: two different problems, two different exits.
func TestEmptyStatesAreDistinct(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: nil})
	if got := b.render(120, 20); !strings.Contains(got, "no tasks yet") {
		t.Errorf("first-run empty state = %q", got)
	}

	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{task(1, stateRunning)}})
	b.filter.SetValue("nothingmatches")
	got := b.render(120, 20)
	if !strings.Contains(got, "no tasks match") {
		t.Errorf("filtered-empty state = %q", got)
	}
	if !strings.Contains(got, "esc to clear") {
		t.Errorf("filtered-empty state gives no way out: %q", got)
	}
}

// TestHeaderCountsIgnoreFilter: a filter must not hide that something needs
// you.
func TestHeaderCountsIgnoreFilter(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateRunning),
		task(2, stateAwaitingInput),
	}})
	b.filter.SetValue("task running")

	header := b.headerLine()
	if !strings.Contains(header, "1 need attention") {
		t.Errorf("header = %q, want the attention count despite the filter", header)
	}
	if !strings.Contains(header, "1/") {
		t.Errorf("header = %q, want the running count", header)
	}
}

// TestSelectionTracksTaskIDAcrossResort is the bug the widget invites: the
// table clamps its cursor index but never remaps it.
func TestSelectionTracksTaskIDAcrossResort(t *testing.T) {
	b := testBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateRunning, withStarted(testNow.Add(-time.Hour))),
		task(2, stateRunning, withStarted(testNow.Add(-time.Minute))),
	}})
	b.render(120, 20)

	// Select the second row (task 2), then have task 1 leave the running
	// band so the order changes under the cursor.
	b.tbl.SetCursor(1)
	b.rememberSelection()
	if b.selectedID != 2 {
		t.Fatalf("selectedID = %d, want 2", b.selectedID)
	}

	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateAwaitingInput), // jumps to the top band
		task(2, stateRunning, withStarted(testNow.Add(-time.Minute))),
	}})
	b.render(120, 20)

	if got, _ := b.selected(); got != 2 {
		t.Errorf("selected task = %d after a re-sort, want 2 — the cursor followed the index, not the task", got)
	}
}

// Enter moved to the shell in T3.10: opening the row under the cursor is
// the fused screen's job (shell_test.go), not a message the board emits.

// TestBoardIgnoresStaleLoads: commands run on their own goroutines, so an
// older ListTasks response can land after a newer one. It must not drag the
// board backwards — found as a Windows CI flake where a row reverted from
// running 2/3 to queued 1/3.
func TestBoardIgnoresStaleLoads(t *testing.T) {
	b := testBoard()
	b.client = apiclient.New("http://127.0.0.1:1", "token")
	first := b.loadCmd()  // seq 1
	second := b.loadCmd() // seq 2
	if first == nil || second == nil {
		t.Fatal("loadCmd returned nil with a client set")
	}

	b.updateLoaded(boardLoadedMsg{seq: 2, tasks: []apiclient.Task{task(1, stateRunning)}})
	b.updateLoaded(boardLoadedMsg{seq: 1, tasks: []apiclient.Task{task(1, stateQueued)}})
	if b.tasks[0].State != stateRunning {
		t.Fatalf("state = %s; a stale fetch clobbered a newer one", b.tasks[0].State)
	}
	// The next real fetch still applies.
	b.updateLoaded(boardLoadedMsg{seq: 3, tasks: []apiclient.Task{task(1, stateDone)}})
	if b.tasks[0].State != stateDone {
		t.Fatalf("state = %s, want the newer fetch applied", b.tasks[0].State)
	}
}

func TestFilterCapturesKeys(t *testing.T) {
	b := testBoard()
	if b.capturesInput() {
		t.Fatal("board captures input before the filter is open")
	}
	b.updateKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !b.capturesInput() {
		t.Fatal("opening the filter did not capture input")
	}
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if b.capturesInput() {
		t.Error("escape did not release the filter")
	}
	if b.filter.Value() != "" {
		t.Errorf("escape left the filter value %q", b.filter.Value())
	}
}

// TestBoardRefreshDebounced: a burst of events must not become a burst of
// requests.
func TestBoardRefreshDebounced(t *testing.T) {
	b := testBoard()
	first := b.scheduleRefresh()
	if first == nil {
		t.Fatal("the first event did not schedule a refresh")
	}
	if second := b.scheduleRefresh(); second != nil {
		t.Error("a second event inside the window scheduled another refresh")
	}
	// The window closing re-opens it for the next burst.
	b.update(boardRefreshMsg{})
	if again := b.scheduleRefresh(); again == nil {
		t.Error("the debounce window never reopened")
	}
}

// TestNonTaskEventsDoNotRefetch keeps unrelated traffic off the fetch path.
func TestNonTaskEventsDoNotRefetch(t *testing.T) {
	b := testBoard()
	note := apiclient.EventNote{Event: apiclient.Event{
		ID: 1, Type: "daemon.shutting_down", Payload: json.RawMessage("{}"),
	}}
	b.updateNote(note)
	if b.refreshPending {
		t.Error("a daemon event scheduled a task refetch")
	}
}
