package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The board's content-conditional columns (task 129.16): STATUS kept at 120
// columns while a row has something to say, an all-empty COST shed, and a
// pull request marker on rows.

// fullContent is a board whose rows have every kind of optional content, so
// every content-conditional column is a candidate and only the width decides.
var fullContent = boardContent{status: true, cost: true, pr: true}

func withCost(usd float64) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.CostUSD = &usd }
}

func withPull(number int) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.GitHubPull = &apiclient.GitHubPullLink{Repo: "octo/repo", Number: number, Source: "reconciler"}
	}
}

// oldColumnsFor is the ladder as it stood before task 129.16, with its
// status gate at maxTitle and COST unconditional, so "unchanged apart from
// COST and PR" is checked against the rule rather than a copied table.
func oldColumnsFor(width int, g grouping, marking bool) columnSet {
	set := columnSet{
		mark: marking, project: !g.has(groupProject), workflow: !g.has(groupWorkflow),
		stepName: true, cost: true, status: true,
	}
	if set.titleWidth(width) < maxTitle {
		set.status = false
	}
	for set.titleWidth(width) < minTitle {
		switch {
		case set.status:
			set.status = false
		case set.cost:
			set.cost = false
		case set.stepName:
			set.stepName = false
		case set.workflow:
			set.workflow = false
		case set.project:
			set.project = false
		default:
			return set
		}
	}
	return set
}

var ladderWidths = []int{80, 100, 120, 160, 200}

func ladderGroupings() []grouping { return []grouping{nil, defaultGrouping()} }

// TestStatusKeptAt120Grouped is the headline: the default grouped board at
// 120 columns keeps STATUS, cuts STEP to its counter and drops COST, and the
// title still identifies something.
func TestStatusKeptAt120Grouped(t *testing.T) {
	for _, marking := range []bool{false, true} {
		for _, content := range []boardContent{
			{status: true},
			{status: true, cost: true},
			{status: true, cost: true, pr: true},
		} {
			set := columnsFor(120, defaultGrouping(), marking, content)
			if !set.status || set.cost || set.stepName || set.pr {
				t.Errorf("marking=%v %+v: set %+v, want STATUS on, COST/PR off, short STEP",
					marking, content, set)
			}
			if w := set.titleWidth(120); w < minTitleWithStatus {
				t.Errorf("marking=%v %+v: title %d under %d", marking, content, w, minTitleWithStatus)
			}
			if !set.statusLine {
				t.Errorf("marking=%v: STATUS at 120 is below the old gate and must be one line", marking)
			}
		}
	}
	if w := columnsFor(120, defaultGrouping(), false, boardContent{status: true}).titleWidth(120); w != 40 {
		t.Errorf("title at 120 grouped = %d, want 40", w)
	}
	if w := columnsFor(120, defaultGrouping(), true, boardContent{status: true}).titleWidth(120); w != 37 {
		t.Errorf("title at 120 grouped, marked = %d, want 37", w)
	}
}

// TestNoStatusContentLeavesTheLadder: with no status message on any row, STATUS
// is not a candidate, and the set is the one the old ladder chose apart from
// COST (content-conditional) and PR (a new column).
func TestNoStatusContentLeavesTheLadder(t *testing.T) {
	for _, g := range ladderGroupings() {
		for _, marking := range []bool{false, true} {
			for _, width := range ladderWidths {
				old := oldColumnsFor(width, g, marking)
				got := columnsFor(width, g, marking, boardContent{cost: true})
				if got.status {
					t.Errorf("%s %d: STATUS with no status content", g.label(), width)
				}
				old.status, old.statusLine = false, false
				if got != old {
					t.Errorf("%s %d marking=%v: %+v, want the old set %+v", g.label(), width, marking, got, old)
				}
			}
		}
	}
}

// TestEmptyCostIsShed: every cost nil means no COST at any width.
func TestEmptyCostIsShed(t *testing.T) {
	for _, g := range ladderGroupings() {
		for width := 40; width <= 400; width++ {
			for _, content := range []boardContent{{}, {status: true}, {status: true, pr: true}} {
				if cols, set := boardColumns(width, g, false, content); set.cost || columnWidth(cols, "COST") != 0 {
					t.Fatalf("%s %d %+v: COST with no cost on any row", g.label(), width, content)
				}
			}
		}
	}
	if !columnsFor(200, nil, false, boardContent{cost: true}).cost {
		t.Error("one row with a cost must bring COST back")
	}
}

// TestPRIsShedFirst: the pull request marker goes before anything else, and
// is never on without a live link.
func TestPRIsShedFirst(t *testing.T) {
	for _, g := range ladderGroupings() {
		for _, marking := range []bool{false, true} {
			for width := 40; width <= 400; width++ {
				set := columnsFor(width, g, marking, fullContent)
				if set.pr && (!set.cost || !set.stepName || !set.status) {
					t.Fatalf("%s %d: PR kept while a lower column was shed: %+v", g.label(), width, set)
				}
				without := columnsFor(width, g, marking, boardContent{status: true, cost: true})
				if without.pr {
					t.Fatalf("%s %d: PR with no link on any row", g.label(), width)
				}
				// Shedding PR is all it may take: the rest is the set the board
				// chooses without PR content.
				if !set.pr {
					set.pr = false
					if set != without {
						t.Fatalf("%s %d: %+v, want %+v once PR is shed", g.label(), width, set, without)
					}
				}
			}
		}
	}
	if !columnsFor(200, defaultGrouping(), false, fullContent).pr {
		t.Error("200 columns grouped has room for PR")
	}
}

// TestContentColumnsFitWidth keeps every content combination inside the
// terminal down to the narrowest board that fits at all.
func TestContentColumnsFitWidth(t *testing.T) {
	var combos []boardContent
	for i := range 8 {
		combos = append(combos, boardContent{status: i&1 != 0, cost: i&2 != 0, pr: i&4 != 0})
	}
	for _, g := range ladderGroupings() {
		for width := 69; width <= 400; width++ {
			for _, c := range combos {
				cols, set := boardColumns(width, g, true, c)
				total := 0
				for _, col := range cols {
					total += col.Width + colPadding
				}
				if total > width {
					t.Fatalf("%s %d %+v: columns total %d", g.label(), width, c, total)
				}
				if set.status && set.titleWidth(width) < minTitleWithStatus {
					t.Fatalf("%s %d: STATUS admitted with a %d-cell title", g.label(), width, set.titleWidth(width))
				}
			}
		}
	}
}

// TestStatusIsOneLineBelowTheOldGate: at 120 columns a long status is cut to
// one line with an ellipsis and the rows stay one line tall; at 200 flat the
// same message still wraps (task 050 decision 6, kept above the gate).
func TestStatusIsOneLineBelowTheOldGate(t *testing.T) {
	long := strings.Repeat("clause ", 30)
	b := groupedBoard(task(1, stateRunning, withStatus(long)))

	b.render(120, 30)
	cols, set := b.columns()
	if !set.status || !set.statusLine {
		t.Fatalf("120: set %+v, want a one-line STATUS", set)
	}
	if h := b.rowHeight(b.rows(), cols, set); h != 1 {
		t.Errorf("120: row height %d, want 1", h)
	}
	cells := b.cellsFor(b.rows()[firstTaskRow(b.rows())], testNow, set, cols)
	status := cells[len(cells)-1].text
	if !strings.HasSuffix(status, "…") || ansi.StringWidth(status) > columnWidth(cols, "STATUS") {
		t.Errorf("120: STATUS cell %q, want cut to %d cells with …", status, columnWidth(cols, "STATUS"))
	}

	b.group = nil
	b.render(200, 30)
	cols, set = b.columns()
	if !set.status || set.statusLine {
		t.Fatalf("200 flat: set %+v, want a wrapping STATUS", set)
	}
	if h := b.rowHeight(b.rows(), cols, set); h < 2 {
		t.Errorf("200 flat: row height %d, want the status to wrap", h)
	}
}

// TestBoardColumnsFollowHeldRows: the flags come from every row the board
// holds — a collapsed group's rows included — not from the viewport.
func TestBoardColumnsFollowHeldRows(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), inWorkflow("build")),
		task(2, stateDone, inProject("web"), inWorkflow("build"),
			withStatus("green"), withCost(1.5), withPull(42)),
	)
	b.render(200, 30)
	_, set := b.columns()
	if !set.status || !set.cost || !set.pr {
		t.Fatalf("set %+v, want STATUS, COST and PR from task 2", set)
	}
	if !strings.Contains(ansi.Strip(b.render(200, 30)), "⇡#42") {
		t.Error("the linked row carries no ⇡#42 marker")
	}

	// Fold task 2's group away: its row is held, so the columns stay.
	b.folds = b.folds.with(foldPath{"web"})
	if strings.Contains(ansi.Strip(b.render(200, 30)), "⇡#42") {
		t.Fatal("the folded group's row is still on screen")
	}
	if _, after := b.columns(); after != set {
		t.Errorf("folding a group changed the columns: %+v, want %+v", after, set)
	}

	// Filter task 2 away: it is no longer held, so its columns go.
	b.filter.SetValue("api")
	if _, filtered := b.columns(); filtered.status || filtered.cost || filtered.pr {
		t.Errorf("filtered set %+v, want no content columns", filtered)
	}
}

func TestFormatPR(t *testing.T) {
	for _, tc := range []struct {
		link *apiclient.GitHubPullLink
		want string
	}{
		{nil, ""},
		{&apiclient.GitHubPullLink{Repo: "o/r", Number: 123}, "⇡#123"},
		{&apiclient.GitHubPullLink{Repo: "o/r", Number: 123, Suppressed: true}, ""},
		{&apiclient.GitHubPullLink{Suppressed: true}, ""},
	} {
		if got := formatPR(tc.link); got != tc.want {
			t.Errorf("formatPR(%+v) = %q, want %q", tc.link, got, tc.want)
		}
	}
}

// TestPRMarkerWithoutColour: the marker is a glyph and a number, never a
// colour, so NO_COLOR keeps it whole; and a board at 80 columns sheds it
// rather than overflowing.
func TestPRMarkerWithoutColour(t *testing.T) {
	b := groupedBoard(task(1, stateDone, withPull(7), withStatus("shipped")))
	frame := b.render(200, 30)
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	if !strings.Contains(buf.String(), prGlyph+"#7") {
		t.Error("the PR marker did not survive the NO_COLOR downgrade")
	}

	narrow := ansi.Strip(b.render(80, 30))
	if strings.Contains(narrow, "#7") {
		t.Error("80 columns kept the PR marker, which is shed first")
	}
	for line := range strings.SplitSeq(narrow, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Errorf("line over 80 cells: %q", line)
		}
	}
}

// TestBoardMarksPullsWithoutAskingGitHub: the marker renders from the list
// row alone — a board over linked tasks never requests anything under
// /github/.
func TestBoardMarksPullsWithoutAskingGitHub(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "delivered task")
	if _, err := h.st.SetTaskGitHubPull(context.Background(), task.ID,
		store.LinkPull("octo/repo", 77, github.SourceHuman, time.Now())); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}
	h.p.until(20*time.Second, "the marker to render on the board", func() bool {
		return strings.Contains(content(h.m), "⇡#77")
	})
	for _, p := range h.paths.snapshot() {
		if strings.Contains(p, "/github/") {
			t.Errorf("the board requested %s", p)
		}
	}
}
