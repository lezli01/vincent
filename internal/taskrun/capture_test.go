package taskrun

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Command bodies that emit a single line longer than the 1 MiB
// `bufio.Scanner` token the command capture installs
// (`steps.go`'s `scanner.Buffer(…, 1024*1024)`). Minified JSON, a base64
// blob or a `git diff` of a generated file all reach that on one line, so
// this is an ordinary command rather than a hostile one. The sentinel rides
// at the end of the line so its presence in the transcript means the *whole*
// line was captured, not a prefix.
const (
	overlongSentinel = "VINCENT-CAPTURE-SENTINEL"

	// `%1100000s` right-aligns the sentinel in a 1.1 MB field: one line,
	// well past the scanner's cap, and the shell writes it in one go.
	overlongLineCmdPosix   = `printf '%1100000s\n' ` + overlongSentinel
	overlongLineCmdWindows = `Write-Output (("x" * 1100000) + "` + overlongSentinel + `")`
)

func overlongLineSnapshot(cmd string) string {
	return "name: overlong\nsteps:\n" + commandStep("emit", cmd, "max_retries: 0")
}

// TestOverlongLineSnapshotParses keeps the Windows branch of the snapshot
// honest on POSIX, the way TestSnapshotsParseOnEveryPlatform does for the
// engine tests' own bodies.
func TestOverlongLineSnapshotParses(t *testing.T) {
	for name, src := range map[string]string{
		"posix":   overlongLineSnapshot(overlongLineCmdPosix),
		"windows": overlongLineSnapshot(overlongLineCmdWindows),
	} {
		if _, _, err := workflow.Parse([]byte(src), workflow.Options{}); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, src)
		}
	}
}

// TestCommandOverlongLineNeverSucceedsSilently: a command step whose output
// vincent could not capture must not be reported as a success.
//
// `runShellCommand` scans each stream with a `bufio.Scanner` capped at 1 MiB
// per token. A longer line stops the scanner with `bufio.Scanner: token too
// long`; the recovery path writes a `vincent.error` note, drains the rest of
// the pipe to `io.Discard`, and then judges the attempt from the exit status
// alone. A command that emits one long line and exits 0 therefore lands as
// `succeeded` with its output silently thrown away — the transcript is
// neither the lossless record §12.2 promises nor the "everything is
// transcripted" of the security model, and §7.1's exit-0 rule is the only
// thing consulted.
//
// Either fix satisfies this test: capture the line in bounded chunks so the
// evidence survives, or terminalize the attempt so the loss is visible.
// Reporting success while the bytes are gone is what must not happen.
func TestCommandOverlongLineNeverSucceedsSilently(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, overlongLineSnapshot(
		script(overlongLineCmdPosix, overlongLineCmdWindows)))
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1 (max_retries 0)", len(runs))
	}
	body, err := os.ReadFile(runs[0].TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if runs[0].ExitCode == nil || *runs[0].ExitCode != 0 {
		t.Fatalf("setup: command exit code = %v, want 0 — the shell did not emit the line "+
			"(transcript: %s)", runs[0].ExitCode, tailOf(body))
	}

	// The whole line reaching the transcript means capture held and success
	// is honest; nothing more to check. It is looked for inside an output
	// record rather than anywhere in the file, because `vincent.command_started`
	// quotes the command — which contains the sentinel too.
	if capturedOutputContains(t, body, overlongSentinel) {
		return
	}

	// It did not, so vincent lost the output. Success is then a lie about
	// the evidence.
	if runs[0].State == store.StepSucceeded {
		t.Errorf("step succeeded with its output discarded: the %d-byte line never reached "+
			"the transcript (%d bytes on disk) and the attempt was judged from exit 0 alone",
			1100000, len(body))
	}
	if final.State == store.TaskDone {
		t.Errorf("task = done with a step whose output was discarded; want the loss surfaced "+
			"(block_reason %q)", final.BlockReason)
	}
}

// capturedOutputContains reports whether any `vincent.output` record in the
// transcript carries want, whatever form the capture path chose to record it
// in — one line, or several bounded chunks.
func capturedOutputContains(t *testing.T, body []byte, want string) bool {
	t.Helper()
	var joined strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var rec struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Errorf("transcript line is not parseable JSON: %v", err)
			continue
		}
		if rec.Type == "vincent.output" {
			joined.WriteString(rec.Text)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	return strings.Contains(joined.String(), want)
}

// tailOf keeps a failure message readable when the transcript is large.
func tailOf(body []byte) string {
	const keep = 512
	if len(body) <= keep {
		return string(body)
	}
	return "…" + string(body[len(body)-keep:])
}
