package taskrun

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	txn "github.com/lezli01/vincent/internal/transcript"
)

// transcript is the shared writer (internal/transcript), aliased so this
// package's existing callers read as they did before the extraction (task
// 063). The naming convention below is the part that is genuinely taskrun's.
type transcript = txn.Writer

// openTranscript creates the transcript file for one attempt (spec §12.2:
// {data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl, with a
// sub-step of a `parallel` group taking {step_index}-{step_id}-{attempt}).
//
// subStepID is empty for an ordinary step, whose index owns its name. A
// member of a `parallel` group shares its group's index with its siblings, so
// its id joins the name — `{step_index}-{step_id}-{attempt}.jsonl` — or three
// concurrent sub-steps would open, and truncate, one file (task 014 decision
// 16). A `loop` body step shares its loop's index *and* repeats, so it takes
// an iteration segment as well:
// `{step_index}-i{iteration}-{step_id}-{attempt}.jsonl` (task 016
// decision 13). Ids are slugs, so nothing here can escape the directory.
//
// Only loop bodies gain the segment. Adding it everywhere for uniformity
// would rename every transcript vincent has ever written to make a directory
// listing consistent with itself; nothing parses these names, because
// `transcript_path` is stored on the row.
// outputFields renders a `vincent.output` note's fields. It stays an alias
// here so the step executors read unchanged after the extraction.
func outputFields(phase, stream, text string, partial bool) map[string]any {
	return txn.OutputFields(phase, stream, text, partial)
}

func openTranscript(dataDir string, taskID int64, stepIndex, iteration, attempt int, subStepID string) (*transcript, error) {
	dir := filepath.Join(dataDir, "transcripts", strconv.FormatInt(taskID, 10))
	name := fmt.Sprintf("%d-%d.jsonl", stepIndex, attempt)
	switch {
	case iteration > 0:
		name = fmt.Sprintf("%d-i%d-%s-%d.jsonl", stepIndex, iteration, subStepID, attempt)
	case subStepID != "":
		name = fmt.Sprintf("%d-%s-%d.jsonl", stepIndex, subStepID, attempt)
	}
	return txn.Open(dir, name)
}

// outputTailBytes bounds the tail by size as well as by line count.
//
// A line count alone stopped being a bound once capture kept over-long lines
// instead of dropping them (#139): 200 chunks of `outputChunkBytes`, or 200
// agent events each carrying a megabyte of text, is not a tail. What this
// feeds is a template field and a prompt block (§8.4), so the size that
// matters is bytes.
const outputTailBytes = 256 * 1024

// outputTail keeps the last n lines of a process's output, bounded by a byte
// ceiling, for the step's result summary (§8.4 `.Steps`) and for the retry
// failure block (§8.4).
type outputTail struct {
	limit    int
	maxBytes int
	lines    []string
	bytes    int
}

// newOutputTail bounds by outputTailBytes. A caller that already reads from a
// bound of its own — the repair prompt's transcript excerpt (task 025) — uses
// newOutputTailBytes so this one does not silently narrow it.
func newOutputTail(limit int) *outputTail {
	return newOutputTailBytes(limit, outputTailBytes)
}

func newOutputTailBytes(limit, maxBytes int) *outputTail {
	return &outputTail{limit: limit, maxBytes: maxBytes}
}

func (o *outputTail) add(line string) {
	o.lines = append(o.lines, line)
	o.bytes += len(line) + 1
	for len(o.lines) > 1 && (len(o.lines) > o.limit || o.bytes > o.maxBytes) {
		o.bytes -= len(o.lines[0]) + 1
		o.lines = o.lines[1:]
	}
	// One line over the bound on its own is cut to its own tail rather than
	// dropped: dropping it would leave the tail empty, which says less than
	// its last kilobytes do.
	if len(o.lines) == 1 && len(o.lines[0]) > o.maxBytes {
		o.lines[0] = tailBytes(o.lines[0], o.maxBytes)
		o.bytes = len(o.lines[0]) + 1
	}
}

// tailBytes returns the last n bytes of s, moved forward to a rune boundary
// so the result is still valid UTF-8 — it lands in JSON and in a SQLite TEXT
// column, and half a rune is not text.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}

func (o *outputTail) String() string { return strings.Join(o.lines, "\n") }
