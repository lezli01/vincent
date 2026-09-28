package taskrun

// `result_summary` is what a human reads on a failure — the board, the detail
// view, the repair prompt (§8.4). A command's error is at the *end* of its
// output, so when the output tail exceeds the 4096-byte cap the summary must
// keep the end, not the start (issue #594), and must never split a rune.

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/store"
)

const summaryTailError = "FINAL-ERROR-LINE"

// runFailingCommand runs cmd as the only step of a task and returns its one
// step run once the task has blocked.
func runFailingCommand(t *testing.T, cmd string) store.StepRun {
	t.Helper()
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, "name: tail\nsteps:\n"+commandStep("emit", cmd, "max_retries: 0"))
	if got := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked", got.State)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	return runs[0]
}

func TestResultSummaryKeepsTheTailOfLongCommandOutput(t *testing.T) {
	// 100 lines of 80 bytes is ~8 KiB of filler, well under the 200-line
	// tail, then the error line, then a nonzero exit.
	run := runFailingCommand(t, script(
		"i=0; while [ $i -lt 100 ]; do printf 'filler-%03d-%s\\n' $i "+
			"'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; i=$((i+1)); done\n"+
			"printf '%s\\n' '"+summaryTailError+"'\nexit 1",
		"for ($i = 0; $i -lt 100; $i++) { Write-Output ('filler-{0:D3}-' -f $i + ('x' * 70)) }\n"+
			"Write-Output '"+summaryTailError+"'\nexit 1",
	))
	if len(run.ResultSummary) > resultSummaryLimit+128 {
		t.Errorf("result_summary is %d bytes, want it bounded near %d", len(run.ResultSummary), resultSummaryLimit)
	}
	if !strings.Contains(run.ResultSummary, summaryTailError) {
		t.Errorf("result_summary lost the last line %q, where the error is; it ends %q",
			summaryTailError, lastBytes(run.ResultSummary, 120))
	}
	if strings.Contains(run.ResultSummary, "filler-000-") {
		t.Errorf("result_summary kept the head of the output (filler-000) instead of its tail")
	}
}

func TestResultSummaryNeverSplitsARune(t *testing.T) {
	if runtime.GOOS == "windows" {
		// pwsh's console encoding makes the byte stream of a non-ASCII rune a
		// property of the host, not of the engine; the cut itself is
		// platform-independent and proven here on POSIX.
		t.Skip("non-ASCII command output depends on the pwsh console encoding")
	}
	// One 4500-byte line of 3-byte runes: no line boundary to cut at, so the
	// cut falls inside the line and must land on a rune boundary.
	run := runFailingCommand(t,
		"i=0; while [ $i -lt 1500 ]; do printf '€'; i=$((i+1)); done\n"+
			"printf '\\n%s\\n' '"+summaryTailError+"'\nexit 1")
	if !utf8.ValidString(run.ResultSummary) {
		t.Errorf("result_summary is not valid UTF-8: the cut split a rune; it ends %q",
			lastBytes(run.ResultSummary, 32))
	}
	if !strings.Contains(run.ResultSummary, summaryTailError) {
		t.Errorf("result_summary lost the last line %q", summaryTailError)
	}
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
