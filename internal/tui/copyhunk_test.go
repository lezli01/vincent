package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

func init() {
	panelKeyProbes[ctxDiff]["ctrl+y"] = func(t *testing.T) {
		v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{ID: 4, State: stateDone, StepTotal: 1, BranchName: "feat/4-x"}})
		v.setTab(taskTabDiff)
		v.detail.diff = *loadedDiff(t, twoFileDiff)
		v.detail.diff.moveHunk(1)
		msg, ok := drain(v.updateKey(registryKey(t, "ctrl+y"))).(openCopyPickerMsg)
		if !ok || len(msg.items) == 0 || msg.items[0].label != "this hunk" {
			t.Fatalf("ctrl+y on the Diff produced %#v", msg)
		}
	}
}

// #1076: the Diff copies the selected hunk and the file's whole diff, as
// plain text.
func TestDiffCopiesTheHunkAndTheFile(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	if !p.moveHunk(1) {
		t.Fatal("no hunk to stand on")
	}
	items := p.diffCopyItems()
	var hunk, file string
	for _, it := range items {
		switch it.label {
		case "this hunk":
			hunk = it.text
		case "this file's diff":
			file = it.text
		}
	}
	if !strings.HasPrefix(hunk, "@@") {
		t.Fatalf("the hunk row copies %q, want it to start at its @@ header", hunk)
	}
	if strings.Contains(hunk, "\x1b") || strings.Contains(file, "\x1b") {
		t.Fatal("a copied diff carries colour codes")
	}
	if file == "" || !strings.Contains(file, hunk) {
		t.Fatalf("the file row does not hold the hunk:\n%s", file)
	}
}

// #1076: the Output tab copies the displayed attempt's whole output.
func TestOutputCopiesTheAttempt(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "build", "succeeded", false)})
	d.applyTranscript(detailTranscriptMsg{
		runID: d.displayRun,
		records: []apiclient.TranscriptRecord{
			{Type: "agent.output", Text: "compiled 12 packages"},
			{Type: "agent.output", Text: "ok all tests"},
		},
	})
	d.width = 120
	it, ok := d.outputCopyItem()
	if !ok {
		t.Fatal("no output row")
	}
	if !strings.Contains(it.text, "compiled 12 packages") || !strings.Contains(it.text, "ok all tests") || strings.Contains(it.text, "\x1b") {
		t.Fatalf("the output row copies %q", it.text)
	}
}
