package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Issue #597: the routed workspace's Output tab draws the lane and attempt
// strips and the pane, and none of the live state outputTitle carries —
// that title is drawn only by the home shell, which production never shows.

// outputStripDetail is a detail sub-model showing one attempt of step 0.
func outputStripDetail(t *testing.T, run apiclient.StepRun) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 5
	d.loaded = true
	d.task.Steps = []apiclient.StepRun{run}
	d.selectedRun = run.ID
	d.syncOutput() // no client: resets the pane and sets follow, fetches nothing
	return d
}

func TestWorkspaceOutputShowsLiveState(t *testing.T) {
	d := outputStripDetail(t, attempt(1, 0, 1, "implement", "running", true))
	v := newTaskView(d)

	got := ansi.Strip(v.renderOutput(120, 20))
	for _, want := range []string{"▼ following", "working…"} {
		if !strings.Contains(got, want) {
			t.Errorf("live attempt's Output tab is missing %q:\n%s", want, got)
		}
	}

	d.following = false // as if the reader had scrolled up
	d.newLines = 3
	got = ansi.Strip(v.renderOutput(120, 20))
	if !strings.Contains(got, "⏸ paused · 3 new") {
		t.Errorf("scrolling up is not visible on the Output tab:\n%s", got)
	}

	d.level.set(levelVerbose)
	d.raw.toggle()
	got = ansi.Strip(v.renderOutput(120, 20))
	for _, want := range []string{levelVerbose.String(), "raw"} {
		if !strings.Contains(got, want) {
			t.Errorf("Output tab does not name %q:\n%s", want, got)
		}
	}
}

func TestWorkspaceOutputFinishedAttemptNeverFollows(t *testing.T) {
	run := attempt(1, 0, 1, "implement", "succeeded", false)
	d := outputStripDetail(t, run)
	got := ansi.Strip(newTaskView(d).renderOutput(120, 20))
	for _, lie := range []string{"following", "paused"} {
		if strings.Contains(got, lie) {
			t.Errorf("finished attempt claims %q (T3.3):\n%s", lie, got)
		}
	}
}

// loadRecords installs a fetched transcript window for the displayed run.
func loadRecords(d *detail, recs []apiclient.TranscriptRecord) {
	d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: recs, next: int64(len(recs))})
}

func bodyLines(n int) []apiclient.TranscriptRecord {
	recs := make([]apiclient.TranscriptRecord, 0, n)
	for i := range n {
		recs = append(recs, apiclient.TranscriptRecord{
			Type: "command.output", Text: fmt.Sprintf("body line %03d", i), Phase: "run", Stream: "stdout",
		})
	}
	return recs
}

func TestFailedAttemptOpensAtItsEnd(t *testing.T) {
	run := attempt(1, 0, 1, "implement", "failed", false)
	run.FailureReason = ptr("exit_nonzero")
	d := outputStripDetail(t, run)
	loadRecords(d, bodyLines(200))

	got := ansi.Strip(d.renderOutputPane(10))
	if !strings.Contains(got, "body line 199") {
		t.Errorf("failed attempt did not open at its end:\n%s", got)
	}
}

func TestCheckFailedAttemptOpensAtFirstCheckLine(t *testing.T) {
	run := attempt(1, 0, 1, "implement", "failed", false)
	run.FailureReason = ptr("check_failed")
	d := outputStripDetail(t, run)
	recs := bodyLines(100)
	for i := range 50 {
		recs = append(recs, apiclient.TranscriptRecord{
			Type: "command.output", Text: fmt.Sprintf("check line %03d", i), Phase: "check", Stream: "stdout",
		})
	}
	loadRecords(d, recs)

	got := ansi.Strip(d.renderOutputPane(10))
	if !strings.Contains(got, "check line 000") {
		t.Errorf("check_failed attempt did not open at its first check line:\n%s", got)
	}
}

func TestSucceededAttemptStillOpensAtTop(t *testing.T) {
	d := outputStripDetail(t, attempt(1, 0, 1, "implement", "succeeded", false))
	loadRecords(d, bodyLines(200))

	got := ansi.Strip(d.renderOutputPane(10))
	if !strings.Contains(got, "body line 000") {
		t.Errorf("succeeded attempt no longer opens at the top:\n%s", got)
	}
}

func TestCheckPhaseLinesCarryAGutter(t *testing.T) {
	d := newTestDetail(t)
	d.records = []apiclient.TranscriptRecord{
		{Type: "command.output", Text: "body says hi", Phase: "run", Stream: "stdout"},
		{Type: "command.output", Text: "FAIL TestThing", Phase: "check", Stream: "stdout"},
	}
	var body, check string
	for _, l := range d.outputLines() {
		l = ansi.Strip(l)
		switch {
		case strings.Contains(l, "body says hi"):
			body = l
		case strings.Contains(l, "FAIL TestThing"):
			check = l
		}
	}
	if check == "" || body == "" {
		t.Fatalf("lines missing: body=%q check=%q", body, check)
	}
	if !strings.Contains(check, "check") {
		t.Errorf("check-phase line has no gutter label: %q", check)
	}
	if strings.Contains(body, "check") {
		t.Errorf("body line carries the check gutter: %q", body)
	}
}
