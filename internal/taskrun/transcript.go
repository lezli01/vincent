package taskrun

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// transcript is one attempt's transcript file (spec §12.2:
// {data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl, with a
// sub-step of a `parallel` group taking {step_index}-{step_id}-{attempt}).
// Agent
// stream lines are written verbatim so the file stays replayable; vincent's
// own annotations are namespaced `vincent.*` (phase 1 decision).
type transcript struct {
	path string
	mu   sync.Mutex
	f    *os.File
	// size is the byte length written so far. Every append returns the
	// position after it, which live-output chunks carry so a client can tell
	// exactly which lines its transcript fetch already covered (§13.3).
	size int64
	// max is the §12.3 per-attempt cap; 0 disables it. Past the cap writes
	// are dropped and exceeded latches, which the step executors poll to fail
	// the attempt (§18 transcript_limit).
	max      int64
	exceeded bool
	// err is the first write, encode or close failure. A transcript that
	// could not record what happened is not the lossless record §12.2
	// promises, and the step executors turn it into transcript_io_error
	// rather than let an attempt claim success over evidence that is not
	// there (§7.1, §18).
	err    error
	closed bool
}

// openTranscript creates the transcript file for one attempt.
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
func openTranscript(dataDir string, taskID int64, stepIndex, iteration, attempt int, subStepID string) (*transcript, error) {
	dir := filepath.Join(dataDir, "transcripts", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}
	name := fmt.Sprintf("%d-%d.jsonl", stepIndex, attempt)
	switch {
	case iteration > 0:
		name = fmt.Sprintf("%d-i%d-%s-%d.jsonl", stepIndex, iteration, subStepID, attempt)
	case subStepID != "":
		name = fmt.Sprintf("%d-%s-%d.jsonl", stepIndex, subStepID, attempt)
	}
	path := filepath.Join(dir, name)
	//nolint:gosec // G304: path is built here from the daemon's transcript dir and the step's own indices
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create transcript: %w", err)
	}
	return &transcript{path: path, f: f}, nil
}

// Path is the transcript's location, recorded on the StepRun.
func (t *transcript) Path() string { return t.path }

// SetMax installs the per-attempt cap (§12.3). Zero or negative disables it.
func (t *transcript) SetMax(limit int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.max = limit
}

// Exceeded reports that the cap was passed. It latches: once a transcript is
// over the limit the attempt is doomed, and un-latching on a later smaller
// write would let a run creep past the cap indefinitely.
func (t *transcript) Exceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exceeded
}

// Err reports the first write, encode or close failure, or nil.
//
// Like Exceeded it latches, and for a stronger reason: a line that failed to
// land is gone, and a later successful write does not put it back. Disk-full,
// permission and short-write faults all arrive here, and ENOSPC on a buffered
// filesystem usually arrives at Close and nowhere else — which is why Close
// feeds this latch too.
func (t *transcript) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// fail latches the first error. The caller holds t.mu.
func (t *transcript) fail(err error) {
	if t.err == nil {
		t.err = err
	}
}

// Raw appends a verbatim stream line and reports the file offset just past
// it. A short or failed write still advances by what landed, so the offset
// never claims more of the file than exists.
//
// The line that trips the cap is written whole rather than truncated: a
// transcript is replayed by a JSONL parser (§13.2), and half a line would
// turn a size failure into a parse failure for every reader afterwards. The
// overshoot is bounded by one line.
func (t *transcript) Raw(line []byte) int64 { return t.write(line, false) }

// write appends a line. force bypasses the cap, which exactly one caller
// needs: the annotation recording *why* the transcript stopped. Suppressing
// that line would leave a reader with a file that simply ends.
func (t *transcript) write(line []byte, force bool) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exceeded && !force {
		return t.size
	}
	want := len(line) + 1
	n, err := t.f.Write(append(line, '\n'))
	t.size += int64(n)
	switch {
	case err != nil:
		t.fail(err)
	case n < want:
		// io.Writer's contract says a short write reports an error, and
		// os.File honours it; checking anyway costs a comparison and is the
		// difference between trusting the contract and having checked.
		t.fail(io.ErrShortWrite)
	}
	if t.max > 0 && t.size > t.max {
		t.exceeded = true
	}
	return t.size
}

// Note appends a namespaced vincent annotation and reports the offset past
// it.
func (t *transcript) Note(kind string, fields map[string]any) int64 {
	return t.note(kind, fields, false)
}

// NoteOverLimit is Note for the one annotation the cap must not suppress:
// the record of why the transcript stops.
func (t *transcript) NoteOverLimit(kind string, fields map[string]any) int64 {
	return t.note(kind, fields, true)
}

func (t *transcript) note(kind string, fields map[string]any, force bool) int64 {
	entry := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		entry[k] = v
	}
	entry["type"] = "vincent." + kind
	entry["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(entry)
	if err != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.fail(err)
		return t.size
	}
	return t.write(b, force)
}

// outputFields is one record of process output, tagged with its stream and
// the phase that produced it (the step's own command, or its check).
//
// partial marks a piece of a line too long to record whole: the next output
// record on the same phase and stream continues it (#139, §12.2). It is
// absent rather than false on an ordinary line, so the common record keeps
// the shape every reader already knows.
//
// One map serves the transcript record and the §13.3 live chunk, which is
// what keeps the durable and live shapes from drifting.
func outputFields(phase, stream, text string, partial bool) map[string]any {
	f := map[string]any{"phase": phase, "stream": stream, "text": text}
	if partial {
		f["partial"] = true
	}
	return f
}

// Close closes the file, latching a close failure into Err.
//
// It is idempotent, because runAttempt closes explicitly — before it judges
// the attempt, so a close-time ENOSPC still reaches the outcome — while the
// deferred close stays as the guard for every early return.
func (t *transcript) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if err := t.f.Close(); err != nil {
		t.fail(err)
	}
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
