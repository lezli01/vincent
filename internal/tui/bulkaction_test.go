package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 011: one §6 action sent to a whole selection.

// bulkServer is a stand-in daemon that records the action calls it received
// and answers each one from a per-task script. It is not a fake §6 — the live
// harness covers the real handlers — it exists to prove the *client* side: one
// call per marked task, and what the bar does with a mixed set of answers.
type bulkServer struct {
	ts *httptest.Server

	mu sync.Mutex
	// calls are the paths in the order they arrived, so "sequential, in board
	// order" is an assertion rather than a hope.
	calls  []string
	forced []int64
	// dirty are the ids that refuse an unforced archive, the way a task with
	// uncommitted changes does (§6).
	dirty map[int64]bool
	// fail are ids that refuse outright, with a 409.
	fail map[int64]bool
}

func newBulkServer(t *testing.T) *bulkServer {
	t.Helper()
	s := &bulkServer{dirty: map[int64]bool{}, fail: map[int64]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		if _, err := fmt.Sscanf(r.PathValue("id"), "%d", &id); err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		var body struct {
			Force bool `json:"force"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		s.mu.Lock()
		s.calls = append(s.calls, r.PathValue("action")+" "+r.PathValue("id"))
		if body.Force {
			s.forced = append(s.forced, id)
		}
		dirty, fail := s.dirty[id] && !body.Force, s.fail[id]
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case fail:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_state","message":"the task is running",` +
				`"details":{"state":"running"}}}`))
		case dirty:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"worktree_dirty","message":"uncommitted changes",` +
				`"details":{"reason":"worktree_dirty"}}}`))
		default:
			_, _ = fmt.Fprintf(w, `{"id":%d,"state":"archived","branch":{"name":"vincent/%d","result":"deleted"}}`, id, id)
		}
	})
	s.ts = httptest.NewServer(mux)
	t.Cleanup(s.ts.Close)
	return s
}

func (s *bulkServer) client() *apiclient.Client { return apiclient.New(s.ts.URL, "token") }

func (s *bulkServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// runBulk executes the command a key produced and hands back the message it
// resolved to, the way the runtime would.
func runBulk(t *testing.T, cmd tea.Cmd) bulkResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("the key produced no command — nothing was dispatched")
	}
	msg := runCmd(t, cmd, 30*time.Second)
	got, ok := msg.(bulkResultMsg)
	if !ok {
		t.Fatalf("command returned %T, want a bulkResultMsg", msg)
	}
	return got
}

// bulkTarget is a selection of tasks that all accept `archive`.
func bulkTarget(ids ...int64) taskActions {
	marked := make([]markedTask, 0, len(ids))
	for _, id := range ids {
		marked = append(marked, markedTask{id: id, actions: []string{apiclient.ActionArchive}})
	}
	return taskActions{id: ids[0], state: stateDone, actions: []string{apiclient.ActionArchive}, marked: marked}
}

// TestBulkArchiveAsksOnceAndSendsOneCallPerTask: the point of the feature is
// one confirmation for the batch, and the point of the invariant is that the
// daemon still sees an ordinary §6 action per task.
func TestBulkArchiveAsksOnceAndSendsOneCallPerTask(t *testing.T) {
	srv := newBulkServer(t)
	bar := &actionBar{}
	target := bulkTarget(3, 5, 8)

	cmd, handled := bar.handleKey("A", srv.client(), target)
	if !handled || cmd != nil {
		t.Fatalf("A dispatched without asking (handled=%v, cmd=%v)", handled, cmd != nil)
	}
	if got := bar.confirmPrompt(target); !strings.Contains(got, "3 selected tasks") {
		t.Fatalf("prompt = %q, want it to name the whole selection", got)
	}

	cmd, handled = bar.handleKey("y", srv.client(), target)
	if !handled {
		t.Fatal("y was not taken as the answer to the confirmation")
	}
	msg := runBulk(t, cmd)

	want := []string{"archive 3", "archive 5", "archive 8"}
	if got := srv.seen(); !equalStrings(got, want) {
		t.Fatalf("calls = %v, want one per marked task in board order: %v", got, want)
	}
	if len(msg.done) != 3 || msg.total != 3 {
		t.Fatalf("result = %+v, want all three accepted", msg)
	}
	bar.applyBulkResult(msg)
	if bar.status == "" || bar.statusBad {
		t.Fatalf("status = %q (bad=%v), want a plain report", bar.status, bar.statusBad)
	}
	if !strings.Contains(bar.status, "3 of 3") {
		t.Errorf("status = %q, want it to say how many of how many moved", bar.status)
	}
	if !strings.Contains(bar.status, "branches deleted") {
		t.Errorf("status = %q, want the branch cleanup named (§10)", bar.status)
	}
}

// TestBulkArchiveReAsksForTheDirtyOnesOnly: `force` *is* the dirty
// confirmation (§6), and by the time it is asked the clean tasks are already
// archived — so the second question is about the refusals, and says so.
func TestBulkArchiveReAsksForTheDirtyOnesOnly(t *testing.T) {
	srv := newBulkServer(t)
	srv.dirty[5] = true
	bar := &actionBar{}
	target := bulkTarget(3, 5, 8)

	bar.handleKey("A", srv.client(), target)
	cmd, _ := bar.handleKey("y", srv.client(), target)
	first := runBulk(t, cmd)
	if len(first.done) != 2 || len(first.dirty) != 1 || first.dirty[0] != 5 {
		t.Fatalf("first pass = %+v, want two archived and #5 held back", first)
	}
	bar.applyBulkResult(first)

	if !bar.capturing() {
		t.Fatal("a dirty worktree did not re-ask — the batch just failed")
	}
	prompt := bar.confirmPrompt(target)
	if !strings.Contains(prompt, "1 of 3 selected tasks") {
		t.Fatalf("re-ask = %q, want it scoped to the refusals", prompt)
	}
	if !strings.Contains(bar.status, "2 of 3") {
		t.Errorf("status = %q, want what the first pass did to survive the re-ask", bar.status)
	}

	cmd, _ = bar.handleKey("y", srv.client(), target)
	second := runBulk(t, cmd)
	if len(second.done) != 1 || second.total != 1 {
		t.Fatalf("forced pass = %+v, want just the dirty task", second)
	}
	srv.mu.Lock()
	forced := append([]int64(nil), srv.forced...)
	srv.mu.Unlock()
	if len(forced) != 1 || forced[0] != 5 {
		t.Fatalf("forced %v, want force on #5 alone — the clean tasks were already archived", forced)
	}
}

// TestBulkReportNamesTheFirstRefusal: a batch that partly failed says so, with
// one failure named. Forty error lines are not a status line — the board's own
// rows are the durable account.
func TestBulkReportNamesTheFirstRefusal(t *testing.T) {
	srv := newBulkServer(t)
	srv.fail[5], srv.fail[8] = true, true
	bar := &actionBar{}
	target := bulkTarget(3, 5, 8)

	bar.handleKey("A", srv.client(), target)
	cmd, _ := bar.handleKey("y", srv.client(), target)
	msg := runBulk(t, cmd)
	bar.applyBulkResult(msg)

	if !bar.statusBad {
		t.Errorf("status %q is not marked bad, though two tasks refused", bar.status)
	}
	for _, want := range []string{"1 of 3", "#5: the task is running", "and 1 more failed"} {
		if !strings.Contains(bar.status, want) {
			t.Errorf("status = %q, want %q in it", bar.status, want)
		}
	}
}

// TestBulkActionKeysCountTheTasksTheyMove: an action offered because *some*
// marked task accepts it has to say how many, or the key is a promise about
// nine rows that moves seven.
func TestBulkActionKeysCountTheTasksTheyMove(t *testing.T) {
	target := taskActions{
		id: 1, state: stateDone, actions: []string{apiclient.ActionArchive},
		marked: []markedTask{
			{id: 1, actions: []string{apiclient.ActionArchive}},
			{id: 2, actions: []string{apiclient.ActionArchive}},
			{id: 3, actions: []string{apiclient.ActionCancel}},
		},
	}
	bar := &actionBar{}
	line := bar.render(target)
	if !strings.Contains(line, "archive (2)") {
		t.Errorf("action line = %q, want archive counted at 2 of the 3 marked", line)
	}
	if !strings.Contains(line, "cancel (1)") {
		t.Errorf("action line = %q, want cancel offered for the one task that accepts it", line)
	}
}

// TestBulkKeysWorkFromAnyPanel: the footer counts the selection wherever the
// eye is, so `A` over the output pane must archive the selection rather than
// the one task the detail panels happen to be showing.
func TestBulkKeysWorkFromAnyPanel(t *testing.T) {
	srv := newBulkServer(t)
	s, _ := newShellFixture(t,
		task(1, stateDone, withActions(apiclient.ActionArchive)),
		task(2, stateDone, withActions(apiclient.ActionArchive)))
	s.board.client = srv.client()
	s.board.marks = markSet{1, 2}
	s.focus = panelOutput
	s.detail.taskID = 1

	s.update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if !s.bar.capturing() {
		t.Fatal("A over the output pane did not raise the bulk confirmation")
	}
	if got := s.bar.confirmPrompt(s.board.target()); !strings.Contains(got, "2 selected tasks") {
		t.Fatalf("prompt = %q, want the selection named", got)
	}

	_, cmd := s.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	msg := runBulk(t, cmd)
	if len(msg.done) != 2 {
		t.Fatalf("result = %+v, want both marked tasks archived", msg)
	}
	// What the daemon accepted leaves the selection; the board refetches.
	s.update(msg)
	if s.board.hasMarks() {
		t.Errorf("the archived tasks stayed marked: %v", s.board.marks)
	}
}

// TestBulkForceReAskSurvivesTheCursorMoving is the seam between the two halves
// of a bulk archive: the clean tasks are gone from the list by the time the
// re-ask is on screen, so the cursor lands on another row and the detail panels
// reset the shared bar. The question is about the selection, not about the row
// under the cursor, and it has to still be there.
func TestBulkForceReAskSurvivesTheCursorMoving(t *testing.T) {
	srv := newBulkServer(t)
	srv.dirty[1] = true
	s, _ := newShellFixture(t,
		task(1, stateDone, withActions(apiclient.ActionArchive)),
		task(2, stateDone, withActions(apiclient.ActionArchive)))
	s.board.client = srv.client()
	s.board.marks = markSet{1, 2}
	// Finished tasks read newest first, so the cursor starts on #2 — the one
	// that is about to be archived and disappear.
	if s.cursor != 2 {
		t.Fatalf("fixture cursor = #%d, want the row that this archive removes", s.cursor)
	}

	s.update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	_, cmd := s.update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	msg := runBulk(t, cmd)

	// The refetch that follows drops #2, and the cursor lands on #1.
	s.update(msg)
	s.update(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateDone, withActions(apiclient.ActionArchive)),
	}})
	s.render(120, 37)
	if s.cursor != 1 {
		t.Fatalf("cursor = #%d after the archived row went away, want it moved to #1", s.cursor)
	}

	if !s.bar.capturing() {
		t.Fatal("the force re-ask was cleared when the cursor moved off the archived row")
	}
	if got := s.bar.confirmPrompt(s.board.target()); !strings.Contains(got, "uncommitted") {
		t.Fatalf("prompt = %q, want the dirty-worktree question", got)
	}
}

// TestBulkSelectionLeavesFailuresMarked: a retry must not need the selection
// to be built again.
func TestBulkSelectionLeavesFailuresMarked(t *testing.T) {
	b := markedBoard(
		task(1, stateDone, withActions(apiclient.ActionArchive)),
		task(2, stateRunning, withActions(apiclient.ActionCancel)),
	)
	b.marks = markSet{1, 2}

	b.update(bulkResultMsg{
		action: apiclient.ActionArchive, total: 2,
		done:   []int64{1},
		failed: []bulkFailure{{id: 2, err: fmt.Errorf("boom")}},
	})
	if b.marks.has(1) {
		t.Errorf("an archived task stayed selected: %v", b.marks)
	}
	if !b.marks.has(2) {
		t.Errorf("the refusal lost its mark: %v — a retry would need re-selecting", b.marks)
	}
}

func equalStrings(got, want []string) bool {
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
