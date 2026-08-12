package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// transcriptDetail parks a detail view on one finished attempt whose
// transcript is at path, with the viewer replaced by a recorder. path is
// taken as given — including a path that does not exist, which is the pruned
// case.
func transcriptDetail(t *testing.T, path string, opened *[]string) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 1
	run := attempt(1, 0, 1, "implement", "failed", false)
	run.TranscriptPath = &path
	loadDetail(d, []apiclient.StepRun{run})
	d.displayRun = 1
	d.focus = focusOutput
	d.exec = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		*opened = append(*opened, cmd.Args[len(cmd.Args)-1])
		return func() tea.Msg { return fn(nil) }
	}
	return d
}

// writeWholeTranscript puts a transcript on disk shaped like the case this
// feature exists for: longer than the pane's window, so "the whole file" and
// "what is on screen" are different things.
func writeWholeTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	var b strings.Builder
	for range 500 {
		b.WriteString(`{"type":"agent.output","text":"line `)
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString(`"}`)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// TestOpenTranscriptHandsTheWholeFileToTheEditor is T4.11's done-when: the
// complete record is reachable from the detail view, not just the tail the
// pane holds. What opens is the file itself — no copy, no window, no range.
func TestOpenTranscriptHandsTheWholeFileToTheEditor(t *testing.T) {
	var opened []string
	path := writeWholeTranscript(t)
	d := transcriptDetail(t, path, &opened)

	cmd := d.updateKey(registryKey(t, "e"))
	if cmd == nil {
		t.Fatal("e produced no command")
	}
	msg := runCmd(t, cmd, 10*time.Second)
	if _, ok := msg.(transcriptOpenedMsg); !ok {
		t.Fatalf("e produced %T, want transcriptOpenedMsg", msg)
	}
	if len(opened) != 1 || opened[0] != path {
		t.Fatalf("editor opened %v, want exactly [%s]", opened, path)
	}
	if d.actions.status != "" {
		t.Errorf("a successful open left a status: %q", d.actions.status)
	}
}

// The keys act on the pane, so needing to focus it first would be a step
// nobody would guess at — the same reasoning `v` and the tab keys carry.
func TestOpenTranscriptWorksFromEitherFocus(t *testing.T) {
	for _, focus := range []detailFocus{focusTimeline, focusOutput} {
		var opened []string
		d := transcriptDetail(t, writeWholeTranscript(t), &opened)
		d.focus = focus
		if cmd := d.updateKey(registryKey(t, "e")); cmd != nil {
			runCmd(t, cmd, 10*time.Second)
		}
		if len(opened) != 1 {
			t.Errorf("focus %v: editor opened %d files, want 1", focus, len(opened))
		}
	}
}

// TestOpenTranscriptNamesAPrunedFile: openEditorPath must not create the file
// it is given, so a pruned transcript would open as an empty buffer — which
// reads as "the step produced nothing" rather than "this was deleted". §12.3
// prunes archived tasks, so it is a state a reader will meet.
func TestOpenTranscriptNamesAPrunedFile(t *testing.T) {
	var opened []string
	gone := filepath.Join(t.TempDir(), "pruned.jsonl")
	d := transcriptDetail(t, gone, &opened)

	if cmd := d.updateKey(registryKey(t, "e")); cmd != nil {
		t.Fatal("e tried to open a transcript that is not there")
	}
	if len(opened) != 0 {
		t.Fatalf("editor was launched anyway: %v", opened)
	}
	if !strings.Contains(d.actions.status, "pruned") || !d.actions.statusBad {
		t.Errorf("status = %q (bad=%v), want it to say the file was pruned",
			d.actions.status, d.actions.statusBad)
	}
}

// A gate never opened a transcript, which is a different nothing from a
// pruned one and says so.
func TestOpenTranscriptOnAStepThatWroteNone(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 1
	gate := attempt(1, 0, 1, "review", "succeeded", false)
	gate.TranscriptPath = nil
	loadDetail(d, []apiclient.StepRun{gate})
	d.displayRun = 1
	d.exec = func(*exec.Cmd, tea.ExecCallback) tea.Cmd {
		t.Fatal("editor launched for a step with no transcript")
		return nil
	}

	if cmd := d.updateKey(registryKey(t, "e")); cmd != nil {
		t.Fatal("e produced a command for a step with no transcript")
	}
	if !strings.Contains(d.actions.status, "no transcript") {
		t.Errorf("status = %q, want it to say the step wrote none", d.actions.status)
	}
}

// A viewer that fails leaves no trace in the terminal it took over, so the
// bar is the only place that can report it.
func TestOpenTranscriptReportsAFailedViewer(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 1
	path := writeWholeTranscript(t)
	run := attempt(1, 0, 1, "implement", "failed", false)
	run.TranscriptPath = &path
	loadDetail(d, []apiclient.StepRun{run})
	d.displayRun = 1
	d.exec = func(_ *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return fn(errors.New("exec: \"vi\": executable file not found")) }
	}

	cmd := d.updateKey(registryKey(t, "e"))
	if cmd == nil {
		t.Fatal("e produced no command")
	}
	d.update(runCmd(t, cmd, 10*time.Second))
	if !strings.Contains(d.actions.status, "transcript viewer") || !d.actions.statusBad {
		t.Errorf("status = %q (bad=%v), want the failed viewer named",
			d.actions.status, d.actions.statusBad)
	}
}

// TestTruncatedOutputNamesTheKey: the truncation line is the one moment a
// reader is looking straight at the output they cannot see, which makes it
// the place the way to the rest of it belongs. A discoverable key is the
// difference between T4.11 being fixed and being merely implemented.
func TestTruncatedOutputNamesTheKey(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 1
	path := writeWholeTranscript(t)
	run := attempt(1, 0, 1, "implement", "failed", false)
	run.TranscriptPath = &path
	loadDetail(d, []apiclient.StepRun{run})
	d.displayRun = 1
	d.truncated = true
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: "the tail that survived"}}

	got := strings.Join(d.outputLines(), "\n")
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncation is not reported at all:\n%s", got)
	}
	if !strings.Contains(got, "press e") {
		t.Errorf("truncation line does not say how to read the rest:\n%s", got)
	}
}
