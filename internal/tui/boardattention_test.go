package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func shownIDs(b *board) []int64 {
	var out []int64
	for _, t := range b.visible() {
		out = append(out, t.ID)
	}
	return out
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// attentionFixture is a board with one task in each interesting shape: the
// three needs-you states, a parent whose served rollup counts a blocked lane,
// one whose lanes are all running, and one an older daemon served no rollup
// for.
func attentionFixture(t *testing.T) *shell {
	t.Helper()
	s, _ := newShellFixture(t,
		task(1, stateRunning, inProject("api")),
		task(2, stateBlocked, inProject("api")),
		task(3, stateAwaitingInput, inProject("web")),
		task(4, stateAwaitingGate, inProject("web")),
		task(5, stateAwaitingChildren, inProject("api"), withChildren(apiclient.ChildrenRollup{
			Total: 2, ByState: map[string]int{stateBlocked: 1, stateRunning: 1}, Blocked: []int64{51},
		})),
		task(6, stateAwaitingChildren, inProject("api"), withChildren(apiclient.ChildrenRollup{
			Total: 1, ByState: map[string]int{stateRunning: 1},
		})),
		task(7, stateAwaitingChildren, inProject("web")),
		task(8, stateDone, inProject("web")),
	)
	s.focus = panelTasks
	return s
}

// TestAttentionFilterKeepsWhatNeedsYou is decision 5's predicate: the three
// needs-you states, plus a parent whose served rollup has a lane that does —
// and not a parent with no rollup, which is what an older daemon sends.
func TestAttentionFilterKeepsWhatNeedsYou(t *testing.T) {
	s := attentionFixture(t)
	s.update(registryKey(t, "H"))
	got := shownIDs(s.board)
	want := map[int64]bool{2: true, 3: true, 4: true, 5: true}
	if len(got) != len(want) {
		t.Fatalf("H shows %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("H shows task %d, which needs nobody", id)
		}
	}
}

// TestAttentionFilterComposes: `/` and `H` both have to keep a row, grouping
// still groups what is left, `V` marks only that and `!` cycles within it.
func TestAttentionFilterComposes(t *testing.T) {
	s := attentionFixture(t)
	s.board.group, s.board.configGroup = grouping{groupProject}, grouping{groupProject}
	s.update(registryKey(t, "H"))
	s.board.filter.SetValue("web")
	if got := shownIDs(s.board); !sameIDs(got, []int64{3, 4}) && !sameIDs(got, []int64{4, 3}) {
		t.Errorf("H with /web shows %v, want the web tasks that need you, 3 and 4", got)
	}
	for _, r := range s.board.allRows() {
		if r.header && !strings.Contains(r.label, "web") {
			t.Errorf("a group left empty drew a header: %+v", r.label)
		}
	}

	s.board.filter.SetValue("")
	s.board.update(key("V"))
	if len(s.board.marks) != 4 || s.board.marks.has(1) || s.board.marks.has(8) {
		t.Errorf("V marked %v, want only the four tasks H is showing", s.board.marks)
	}

	seen := map[int64]bool{}
	for range 6 {
		s.update(jumpAttentionMsg{})
		id, _ := s.board.selected()
		seen[id] = true
	}
	for id := range seen {
		if id == 1 || id == 6 || id == 7 || id == 8 {
			t.Errorf("! reached task %d, which H hides", id)
		}
	}
}

// TestAttentionFilterTogglesBackKeepingTheCursor: H again restores the whole
// board, with the cursor still on the task it was on.
func TestAttentionFilterTogglesBackKeepingTheCursor(t *testing.T) {
	s := attentionFixture(t)
	s.update(registryKey(t, "H"))
	s.board.selectedID = 3
	s.board.restoreSelection(s.board.rows())
	s.update(registryKey(t, "H"))
	if s.board.attentionOnly {
		t.Fatal("H again left the filter on")
	}
	if got := len(shownIDs(s.board)); got != 8 {
		t.Errorf("the full board shows %d tasks, want 8", got)
	}
	s.board.restoreSelection(s.board.rows())
	if id, _ := s.board.selected(); id != 3 {
		t.Errorf("cursor on %d after H off, want 3", id)
	}
}

// TestAttentionFilterTitleAndEmptyState: the title names the filter beside
// `/`, and a board with nothing needing you says how to turn it off, by the
// effective key, legibly at 80 columns with no colour.
func TestAttentionFilterTitleAndEmptyState(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateDone))
	s.focus = panelTasks
	s.update(registryKey(t, "H"))
	s.board.filter.SetValue("x")
	if got := s.panelTitle(panelTasks); got != "Tasks — needs you — /x" {
		t.Errorf("title = %q, want %q", got, "Tasks — needs you — /x")
	}
	s.board.filter.SetValue("")
	if got := s.panelTitle(panelTasks); got != "Tasks — needs you" {
		t.Errorf("title = %q, want %q", got, "Tasks — needs you")
	}

	body := ansi.Strip(s.board.render(80, 12))
	if !strings.Contains(body, "nothing needs you — "+registryKeyName(t, "H")+" shows every task") {
		t.Errorf("empty state %q does not name the way out", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("line %q is %d columns wide", line, w)
		}
	}
}

// registryKeyName is the key a registry row is pressed with under the
// current keymap — the name a rendered hint must use.
func registryKeyName(t *testing.T, k string) string {
	t.Helper()
	return registryKey(t, k).String()
}
