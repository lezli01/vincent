package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestTranscriptPrintsEditDeltaNotPatch is task 110 at the command: an edit's
// outcome prints as its `+N −M` delta, with no CLI change, and the
// agent.patch body is skipped the way agent.command_output is — this command
// has no verbosity control. `--json` still carries the record.
func TestTranscriptPrintsEditDeltaNotPatch(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "agent", "claude", "testdata", "stream_edit_2.1.268.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")

	h := newLiveHarness(t)
	run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded, lines...)
	task, step := strconv.FormatInt(h.taskID, 10), strconv.FormatInt(run.ID, 10)

	out, _, code := runCLI(t, "task", "transcript", task, "--step", step)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, out)
	}
	for _, want := range []string{"\n< +2 −1\n", "\n< +12 −6\n", "\n< +1 −1\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"@@ -590,7 +590,8 @@", "fsnotify watcher"} {
		if strings.Contains(out, absent) {
			t.Errorf("output printed the patch body (%q):\n%s", absent, out)
		}
	}

	out, _, code = runCLI(t, "task", "transcript", task, "--step", step, "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0 (%s)", code, out)
	}
	if n := strings.Count(out, `"type":"agent.patch"`); n != 4 {
		t.Errorf("--json carries %d agent.patch records, want 4:\n%s", n, out)
	}
}
