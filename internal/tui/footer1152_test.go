package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Issue #1152: the selected task's state decides which keys lead the
// footer, and at 80 columns the keys that state most needs are on the line.

// stateActions is what the daemon reports in available_actions for a state:
// taskstate's human actions, the one definition of what may happen next.
func stateActions(state string) []string {
	var out []string
	for _, a := range taskstate.HumanActionsFrom(taskstate.State(state)) {
		out = append(out, string(a))
	}
	return out
}

// footerOrder is the column each want starts at on the line, -1 if absent.
func footerOrder(line string, wants ...string) []int {
	out := make([]int, len(wants))
	for i, w := range wants {
		out[i] = strings.Index(line, w)
	}
	return out
}

// TestFooterActionsLeadByState: among the task's own segments, the ones that
// move a task in this state on come first — the answer before cancel on a
// task waiting on a question, the recovery keys on a blocked one, the
// verdict at a gate, the next step on a finished one.
func TestFooterActionsLeadByState(t *testing.T) {
	cases := []struct {
		state string
		lead  []string // in this order, and ahead of every other action
		after []string
	}{
		{stateAwaitingInput, []string{"enter answer"}, []string{"c cancel"}},
		{stateAwaitingGate, []string{"a approve", "x reject"}, []string{"s skip", "c cancel"}},
		{stateBlocked, []string{"r retry", "E edit+retry", "R repair"}, []string{"s skip", "c cancel"}},
		{stateDone, []string{"F follow up", "A archive"}, []string{"T chat"}},
		{statePaused, []string{"p resume"}, []string{"c cancel"}},
	}
	for _, c := range cases {
		target := taskActions{id: 9, state: c.state, actions: stateActions(c.state), editRetry: true}
		line := ansi.Strip(renderFooter(400, nil, &actionBar{}, target, 0, false))
		all := append(append([]string(nil), c.lead...), c.after...)
		at := footerOrder(line, all...)
		for i, x := range at {
			if x < 0 {
				t.Fatalf("%s: %q is not on the line %q", c.state, all[i], line)
			}
		}
		for i := 1; i < len(at); i++ {
			if i < len(c.lead) && at[i] < at[i-1] {
				t.Errorf("%s: %q comes before %q: %q", c.state, c.lead[i], c.lead[i-1], line)
			}
		}
		last := at[len(c.lead)-1]
		for i, a := range c.after {
			if at[len(c.lead)+i] < last {
				t.Errorf("%s: %q comes ahead of the lead %v: %q", c.state, a, c.lead, line)
			}
		}
	}
}

// boardRootAt is a root on the board at width, its one visible task the
// selected one, and a blocked task in another project so that `!` competes
// for the line the way it does on a busy daemon.
func boardRootAt(t *testing.T, width int, selected apiclient.Task) *root {
	t.Helper()
	other := task(99, stateBlocked, inProjectID(otherProjectID, "other"))
	other.AvailableActions = stateActions(stateBlocked)
	selected.AvailableActions = stateActions(selected.State)
	m := attentionRoot(t)
	s := m.views[viewHome].(*shell)
	s.board.now = func() time.Time { return testNow }
	// The flat table, as newShellFixture has it: the cursor is on the one
	// row, whatever column a card would put it in.
	s.board.cards = false
	s.board.group, s.board.configGroup = nil, nil
	s.update(boardLoadedMsg{tasks: []apiclient.Task{selected, other}})
	m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	m.active = viewHome
	if got := s.board.target(); got.id != selected.ID {
		t.Fatalf("%s: the board selected task %d, want %d", selected.State, got.id, selected.ID)
	}
	return m
}

// TestBoardFooterAt80KeepsWhatTheStateNeeds is #1152's third acceptance
// criterion: at 80 columns the board's footer carries the keys the selected
// task most needs, by state, on one line that keeps the pinned segment.
func TestBoardFooterAt80KeepsWhatTheStateNeeds(t *testing.T) {
	cases := []struct {
		state string
		want  []string
	}{
		{stateBlocked, []string{"r retry", "R repair"}},
		{stateAwaitingGate, []string{"a approve", "x reject"}},
		// The answer is opened from the workspace, so on the board the key
		// that answers is the one that opens it.
		{stateAwaitingInput, []string{"enter open to answer"}},
		{stateDone, []string{"F follow up", "A archive"}},
		{statePaused, []string{"p resume"}},
		{stateRunning, []string{"p pause", "c cancel"}},
	}
	for _, c := range cases {
		m := boardRootAt(t, 80, task(7, c.state))
		raw := m.footerLine()
		line := ansi.Strip(raw)
		if strings.Contains(raw, "\n") || ansi.StringWidth(raw) > 80 {
			t.Fatalf("%s: the footer is not one line of 80 cells: %q", c.state, line)
		}
		for _, p := range []string{": commands", "? help", "q quit"} {
			if !strings.Contains(line, p) {
				t.Fatalf("%s: the pinned segment lost %q: %q", c.state, p, line)
			}
		}
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("%s at 80 columns: the footer does not show %q: %q", c.state, w, line)
			}
		}
	}
}

// TestBoardAnswerHintStandsForEnter: on the board, the answer segment is the
// board's own enter, so the line names enter once and `+N` counts it once.
func TestBoardAnswerHintStandsForEnter(t *testing.T) {
	m := boardRootAt(t, 200, task(7, stateAwaitingInput))
	line := ansi.Strip(m.footerLine())
	if n := strings.Count(line, "enter "); n != 1 {
		t.Fatalf("the board names enter %d times, want once as the way to the answer: %q", n, line)
	}
	if !strings.Contains(line, "enter open to answer") {
		t.Fatalf("the board's footer = %q, want enter open to answer", line)
	}
	if strings.Contains(line, "enter answer") {
		t.Fatalf("the board advertises enter as an immediate answer: %q", line)
	}
}

// TestTriggersAndProjectsKeysHaveHints is #1152's second part: the triggers
// screen's `e`, `D`, `B` and `/` and the projects screen's `e` carried no
// hint, so the footer could never name them however wide the line.
func TestTriggersAndProjectsKeysHaveHints(t *testing.T) {
	for ctx, keys := range map[bindingContext][]string{
		ctxTriggers: {"e", "D", "B", "/"},
		ctxProjects: {"e"},
	} {
		hinted := map[string]string{}
		for _, b := range bindingsFor(ctx) {
			hinted[b.key] = b.hint
		}
		for _, k := range keys {
			h, ok := hinted[k]
			if !ok {
				t.Errorf("%s: no registry row for %q", ctx, k)
				continue
			}
			if !strings.HasPrefix(h, k+" ") {
				t.Errorf("%s: %q has hint %q, want one naming the key", ctx, k, h)
			}
		}
	}
}
