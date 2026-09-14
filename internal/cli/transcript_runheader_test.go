package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// Task 066.3 put claude's run header and its result metadata on the wire "so
// `vincent task transcript` renders them" (issue #371). The command renders the
// vocabulary the output pane renders, at the level it already mirrors — the
// plan is shown, an agent's command output is not — which for these two records
// is §15's `normal` (amended 2026-08-31, task 066): the working directory and
// the tools on a `# ` line, and the result line's elapsed time, turns and
// permission denials, never the ordinary end_turn/completed reasons.
//
// These run off the adapter's captured real-CLI runs rather than hand-written
// lines, so what is asserted is what a real claude run records.
func TestTranscriptRendersRunHeaderAndResultMetadata(t *testing.T) {
	tools := []string{"Task", "AskUserQuestion", "Bash", "Read", "Write"}
	for _, tc := range []struct {
		fixture string
		// result are fragments the result line must carry.
		result []string
		denied bool
	}{
		{"stream_permission_allow_2.1.226.jsonl", []string{"8.0s", "2 turns"}, false},
		{"stream_permission_deny_2.1.226.jsonl", []string{"7.3s", "2 turns", "1 denied"}, true},
		{"stream_question_2.1.226.jsonl", []string{"5.3s", "2 turns"}, false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			h := newLiveHarness(t)
			run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded,
				fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata", tc.fixture))...)
			lines := renderedTranscript(t, h, run)
			all := strings.Join(lines, "\n")

			// The header is the run's frame, so it is the first thing printed.
			if !strings.HasPrefix(lines[0], "# ") {
				t.Errorf("first line is not the run header:\n%s", all)
			} else {
				for _, want := range append([]string{`C:\work\repo`}, tools...) {
					if !strings.Contains(lines[0], want) {
						t.Errorf("run header %q is missing %q", lines[0], want)
					}
				}
			}

			result := lines[len(lines)-1]
			if !strings.HasPrefix(result, "= ") {
				t.Fatalf("last line is not the result line:\n%s", all)
			}
			for _, want := range tc.result {
				if !strings.Contains(result, want) {
					t.Errorf("result line %q is missing %q", result, want)
				}
			}
			if !tc.denied && strings.Contains(result, "denied") {
				t.Errorf("result line %q counts denials the run did not have", result)
			}
			// Every successful claude run ends end_turn/completed; naming the
			// ordinary reasons says nothing.
			for _, ordinary := range []string{"end_turn", "completed"} {
				if strings.Contains(result, ordinary) {
					t.Errorf("result line %q names the ordinary reason %q", result, ordinary)
				}
			}
		})
	}
}

// The adapters that report none of it render none of it. Cursor's stream even
// carries a cwd and a duration_ms, which its parser deliberately does not read
// (task 066, TestNoRunHeaderOrResultMetadata) — so neither may reach the text,
// and the result line stays exactly what it always was.
func TestTranscriptNoRunHeaderForAdaptersThatReportNone(t *testing.T) {
	for _, tc := range []struct{ agent, fixture string }{
		{"codex", filepath.Join("..", "agent", "codex", "testdata", "success.jsonl")},
		{"cursor", filepath.Join("..", "agent", "cursor", "testdata", "success_2026.08.04.jsonl")},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			h := newLiveHarness(t)
			run := h.addRunAs(t, tc.agent, h.taskID, "implement", store.StepSucceeded,
				fixtureLines(t, tc.fixture)...)
			lines := renderedTranscript(t, h, run)
			all := strings.Join(lines, "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "# ") {
					t.Errorf("%s rendered a run header %q:\n%s", tc.agent, line, all)
				}
			}
			if strings.Contains(all, "/tmp/wt") {
				t.Errorf("%s rendered a working directory its parser does not read:\n%s", tc.agent, all)
			}
			if got := lines[len(lines)-1]; got != "= done" {
				t.Errorf("%s result line = %q, want %q:\n%s", tc.agent, got, "= done", all)
			}
		})
	}
}

// fixtureLines reads a captured JSONL run, one transcript line per record.
func fixtureLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// renderedTranscript runs the command's default rendering and returns its
// stdout as lines.
func renderedTranscript(t *testing.T, h *liveHarness, run *store.StepRun) []string {
	t.Helper()
	out, errOut, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(run.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("rendered nothing")
	}
	return lines
}
