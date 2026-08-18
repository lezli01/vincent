package taskrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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
	n, _ := t.f.Write(append(line, '\n'))
	t.size += int64(n)
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
		return t.size
	}
	return t.write(b, force)
}

// Output appends one line of process output, tagged with its stream and the
// phase that produced it (the step's own command, or its check), and reports
// the offset past it.
func (t *transcript) Output(phase, stream, text string) int64 {
	return t.Note("output", map[string]any{"phase": phase, "stream": stream, "text": text})
}

// Close closes the file.
func (t *transcript) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.f.Close()
}

// outputTail keeps the last n lines of a process's output for the step's
// result summary (§8.4 `.Steps`) and for the retry failure block (§8.4).
type outputTail struct {
	limit int
	lines []string
}

func newOutputTail(limit int) *outputTail { return &outputTail{limit: limit} }

func (o *outputTail) add(line string) {
	o.lines = append(o.lines, line)
	if len(o.lines) > o.limit {
		o.lines = o.lines[len(o.lines)-o.limit:]
	}
}

func (o *outputTail) String() string { return strings.Join(o.lines, "\n") }
