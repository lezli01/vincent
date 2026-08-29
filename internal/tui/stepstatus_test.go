package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The attempt line carries both readings of the status (task 036): the live
// one on a running attempt, and the terminal one on a finished attempt beside
// — never instead of — the daemon's failure_reason. The two are rendered in
// different styles, because a stale self-report presented as the daemon's
// verdict is the failure mode this feature has to avoid.
func TestAttemptLineRendersStatusDistinctlyFromTheFailureReason(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 7

	failed := attempt(1, 0, 1, "implement", "failed", false)
	failed.FailureReason = ptr("check_failed")
	failed.StatusMessage = ptr("3 tests red in internal/store")
	live := attempt(2, 0, 2, "implement", "running", true)
	live.StatusMessage = ptr("scaffolding the migration")
	loadDetail(d, []apiclient.StepRun{failed, live})

	got := d.timelinePanel(30)
	for _, want := range []string{
		statusGlyph + " 3 tests red in internal/store",
		statusGlyph + " scaffolding the migration",
		"check_failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline is missing %q:\n%s", want, got)
		}
	}

	// The styles must actually differ, or "visually distinct" is a comment
	// rather than a property. Compared on the rendered escape sequences,
	// since that is what a terminal sees.
	reason := styleBad.Render("check_failed")
	status := styleStatus.Render(statusGlyph + " 3 tests red in internal/store")
	if !strings.Contains(got, reason) {
		t.Errorf("the failure reason lost its styleBad rendering:\n%q", got)
	}
	if !strings.Contains(got, status) {
		t.Errorf("the status lost its own rendering:\n%q", got)
	}
	if styleBad.Render("x") == styleStatus.Render("x") {
		t.Error("the status and the failure reason render identically")
	}
}

// result_summary has been stored, served and read by `.Steps.<id>.Result`
// since the first release and shown on no screen at all. It renders under an
// attempt that did not succeed — where a reader is asking "what went wrong"
// and `failure_reason` answers only which category — and nowhere else.
func TestTimelineRendersResultSummaryOnFailure(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 7

	failed := attempt(1, 0, 1, "implement", "failed", false)
	failed.FailureReason = ptr("nonzero_exit")
	failed.ResultSummary = "FAIL\tgithub.com/lezli01/vincent/internal/store\t0.4s"
	ok := attempt(2, 0, 2, "implement", "succeeded", false)
	ok.ResultSummary = "all green, nothing to report"
	loadDetail(d, []apiclient.StepRun{failed, ok})

	got := d.timelinePanel(30)
	if !strings.Contains(got, "FAIL\tgithub.com/lezli01/vincent/internal/store\t0.4s") &&
		!strings.Contains(got, "FAIL github.com/lezli01/vincent/internal/store 0.4s") {
		t.Errorf("the failed attempt's result summary is missing:\n%s", got)
	}
	if strings.Contains(got, "all green, nothing to report") {
		t.Errorf("a succeeded attempt's summary was rendered; it belongs to the output pane:\n%s", got)
	}
}

// Long summaries wrap inside the pane, but remain a preview: Task 78 contains
// several multi-thousand-character command and agent results, and rendering
// every byte would replace one overflow bug with a timeline-sized wall of text.
func TestSummaryLinesWrapAndCapPreview(t *testing.T) {
	r := attempt(1, 0, 1, "implement", "failed", false)
	r.ResultSummary = strings.Repeat("panic: boom goroutine 1 [running] main.main() ", 20) +
		strings.Repeat("a", 80)
	got := summaryLines(r, false, 42)
	if len(got) != timelineSummaryMaxLines {
		t.Fatalf("summaryLines returned %d lines, want %d: %q", len(got), timelineSummaryMaxLines, got)
	}
	for i, line := range got {
		if width := ansi.StringWidth(line); width > 42 {
			t.Errorf("summary line %d is %d cells wide, want <= 42: %q", i, width, line)
		}
	}
	if !strings.HasSuffix(ansi.Strip(got[len(got)-1]), "…") {
		t.Errorf("truncated summary has no ellipsis: %q", got[len(got)-1])
	}
	if got := summaryLines(attempt(1, 0, 1, "implement", "succeeded", false), false, 42); got != nil {
		t.Error("a succeeded attempt with no summary rendered a line")
	}
}

func TestTimelineWrapsStatusAndMapsContinuationRowsToAttempt(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 78
	d.width = 48

	failed := attempt(885, 0, 1, "classify", "failed", false)
	failed.StatusMessage = ptr("enhancement path: judged by the new behaviour because this exit is normal")
	failed.ResultSummary = strings.Repeat("a required check is still pending on the pull request ", 8)
	loadDetail(d, []apiclient.StepRun{failed})

	got := d.renderTimeline(30)
	for i, line := range strings.Split(got, "\n") {
		if width := ansi.StringWidth(line); width > d.width {
			t.Errorf("timeline line %d is %d cells wide, want <= %d: %q", i, width, d.width, line)
		}
	}
	if !strings.Contains(got, statusGlyph+" enhancement path") {
		t.Errorf("wrapped status is missing:\n%s", got)
	}
	if !strings.Contains(got, "↳ a required check") {
		t.Errorf("wrapped summary is missing:\n%s", got)
	}
	runRows := 0
	for _, id := range d.visibleRuns {
		if id == failed.ID {
			runRows++
		}
	}
	if runRows < 5 {
		t.Errorf("only %d continuation rows map to run %d; visible ids = %v", runRows, failed.ID, d.visibleRuns)
	}
}

// The board cell: present when there is something to say, empty when there is
// not — a column of dashes would read as something missing rather than as the
// ordinary case — and truncated with an ellipsis to the column's width.
func TestFormatStatus(t *testing.T) {
	if got := formatStatus(nil); got != "" {
		t.Errorf("formatStatus(nil) = %q, want empty", got)
	}
	if got := formatStatus(ptr("")); got != "" {
		t.Errorf("formatStatus(\"\") = %q, want empty", got)
	}
	if got := formatStatus(ptr("compiling")); got != "compiling" {
		t.Errorf("formatStatus = %q", got)
	}
	long := strings.Repeat("x", widthStatus+20)
	got := formatStatus(&long)
	if len([]rune(got)) > widthStatus {
		t.Errorf("formatStatus width = %d, want at most %d", len([]rune(got)), widthStatus)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated status = %q, want an ellipsis", got)
	}
}

// The shedding ladder (task 036): the status is the last column admitted and
// the first shed, so a board narrow enough to drop it renders exactly as it
// did before the column existed. Asserted over every width rather than at a
// few, because what matters is that there is no width at which the status
// displaces a column a reader navigates by.
func TestStatusColumnIsTheLastAdmitted(t *testing.T) {
	groupings := []grouping{nil, {groupProject}, {groupProject, groupWorkflow}}
	sawStatus := false
	for width := 20; width <= 400; width++ {
		for _, g := range groupings {
			set := columnsFor(width, g, false)
			if !set.status {
				continue
			}
			sawStatus = true
			if !set.cost || !set.stepName {
				t.Fatalf("width %d %s: status displaced cost/step name: %+v",
					width, g.label(), set)
			}
			if !g.has(groupWorkflow) && !set.workflow {
				t.Fatalf("width %d %s: status displaced the workflow: %+v", width, g.label(), set)
			}
			if !g.has(groupProject) && !set.project {
				t.Fatalf("width %d %s: status displaced the project: %+v", width, g.label(), set)
			}
		}
	}
	if !sawStatus {
		t.Fatal("no width in 20..400 carries the status column at all")
	}
}

// The one recorded decision this column could have overturned: a grouped
// board drops PROJECT and WORKFLOW because the headers name them, and the
// width that frees goes to the title (see columnsFor). Admitting the status
// on exactly that freed width would reverse it — which is what
// minTitleWithStatus is set high enough to prevent.
//
// Checked at the widths a grouped board actually carries the status at.
// Below them the ladder has always spent freed width on buying back the step
// name and cost first, which predates this column and is not its business.
func TestStatusColumnDoesNotEatTheWidthGroupingFrees(t *testing.T) {
	for _, width := range []int{160, 240, 400} {
		flat := columnsFor(width, nil, false)
		grouped := columnsFor(width, grouping{groupProject, groupWorkflow}, false)
		if grouped.titleWidth(width) <= flat.titleWidth(width) {
			t.Errorf("width %d: grouped title %d, flat %d — grouping must gain title space",
				width, grouped.titleWidth(width), flat.titleWidth(width))
		}
	}
}

func withStatus(message string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.StatusMessage = &message }
}

// The cell reaches the rendered board, and only on a board wide enough to
// carry the column.
func TestBoardRendersTheStatusCell(t *testing.T) {
	b := groupedBoard(task(1, stateRunning, withStatus("compiling internal/store")))
	if out := b.render(240, 20); !strings.Contains(out, "STATUS") ||
		!strings.Contains(out, "compiling internal/store") {
		t.Errorf("a 240-column board rendered no status:\n%s", out)
	}
	if out := b.render(120, 20); strings.Contains(out, "compiling internal/store") {
		t.Errorf("a 120-column board rendered the status it should have shed:\n%s", out)
	}
}
