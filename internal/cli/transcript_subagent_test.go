package cli

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// Task 109 put a claude subagent's records on an ASCII rail. The command has
// no levels and prints the pane's `normal`, where a subagent — one level
// quieter — shows its prose, its tool calls and their outcomes, and nothing of
// its reasoning or its unrecognized lines. These run through the real
// handlers off captured runs.
func TestTranscriptRendersSubagentRail(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded,
		fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata", "stream_subagent_async_2.1.268.jsonl"))...)
	lines := renderedTranscript(t, h, run)
	all := strings.Join(lines, "\n")

	for _, want := range []string{
		"| -> Verify 022 gate walkthrough claims",
		"| -> Verify 088 gate walkthrough claims",
		"| > Bash git status --short",
		"| < Sanitized tool output.",
		"| Sanitized text for this excerpt.",
		"| = completed - Verify 022 gate walkthrough claims - 30 tool uses - 5m00s",
		"< started in background",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript is missing %q:\n%s", want, all)
		}
	}
	for _, gone := range []string{"Async agent launched", "Working out which check", "unrecognized", "Running Read"} {
		if strings.Contains(all, gone) {
			t.Errorf("transcript shows %q:\n%s", gone, all)
		}
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "| -> ") &&
			(i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "| ") || strings.HasPrefix(lines[i+1], "| -> ")) {
			t.Errorf("label %q dangles:\n%s", line, all)
		}
	}
	// The main loop's own lines between a subagent's are not railed.
	if !strings.Contains(all, "\n> Agent ") || !strings.Contains(all, "\n> Bash git status --short") {
		t.Errorf("main-loop tool calls lost their place:\n%s", all)
	}
	// The result de-duplication counts the main loop's prose only.
	if got := lines[len(lines)-1]; !strings.HasPrefix(got, "= done") {
		t.Errorf("last line = %q, want the result outcome", got)
	}
}

// TestTranscriptSubagentFailure: a failed agent's end is marked apart from a
// completed one's.
func TestTranscriptSubagentFailure(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded,
		fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata", "stream_subagent_failed_2.1.268.jsonl"))...)
	lines := renderedTranscript(t, h, run)
	if got := lines[len(lines)-1]; got != "| ! failed - Write vincent-triggers skill" {
		t.Errorf("last line = %q, want the failed completion line:\n%s", got, strings.Join(lines, "\n"))
	}
}

// TestTranscriptSubagentJSONAndRawUnchanged: --json and --raw are the records
// and the dialect, with no rail anywhere.
func TestTranscriptSubagentJSONAndRawUnchanged(t *testing.T) {
	h := newLiveHarness(t)
	fixture := fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata", "stream_subagent_sync_2.1.263.jsonl"))
	run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded, fixture...)
	args := []string{"task", "transcript", strconv.FormatInt(h.taskID, 10), "--step", strconv.FormatInt(run.ID, 10)}

	raw, errOut, code := runCLI(t, append(args, "--raw")...)
	if code != 0 {
		t.Fatalf("--raw exit = %d (%s)", code, errOut)
	}
	if got := strings.TrimRight(strings.ReplaceAll(raw, "\r\n", "\n"), "\n"); got != strings.Join(fixture, "\n") {
		t.Errorf("--raw is not the transcript byte for byte")
	}

	js, errOut, code := runCLI(t, append(args, "--json")...)
	if code != 0 {
		t.Fatalf("--json exit = %d (%s)", code, errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(js), "\n") {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("--json printed a non-record line %q", line)
		}
	}
	for _, want := range []string{`"type":"agent.subagent_started"`, `"type":"agent.subagent_finished"`, `"parent_call_id":"toolu_01SccLMcQ3heAyKbreuZ4axP"`} {
		if !strings.Contains(js, want) {
			t.Errorf("--json is missing %s", want)
		}
	}
}
