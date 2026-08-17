package taskrun

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func openTestTranscript(t *testing.T, limit int64) *transcript {
	t.Helper()
	tr, err := openTranscript(t.TempDir(), 1, 0, 1, "")
	if err != nil {
		t.Fatalf("openTranscript: %v", err)
	}
	t.Cleanup(tr.Close)
	tr.SetMax(limit)
	return tr
}

// TestTranscriptCapLatches: once over the limit the attempt is doomed, so a
// later smaller write must not un-latch it — otherwise a run could creep past
// the cap indefinitely, one short line at a time.
func TestTranscriptCapLatches(t *testing.T) {
	tr := openTestTranscript(t, 64)
	if tr.Exceeded() {
		t.Fatal("a fresh transcript reports Exceeded")
	}
	tr.Raw([]byte(strings.Repeat("x", 100)))
	if !tr.Exceeded() {
		t.Fatal("Exceeded is false after passing the cap")
	}
	before := tr.size
	tr.Raw([]byte("y"))
	if !tr.Exceeded() {
		t.Error("Exceeded un-latched on a later write")
	}
	if tr.size != before {
		t.Errorf("size advanced from %d to %d after the cap; writes must be dropped", before, tr.size)
	}
}

// TestTranscriptCapWritesWholeLines: the line that trips the cap is written
// in full. A transcript is replayed by a JSONL parser (§13.2), and half a
// line would turn a size failure into a parse failure for every later reader.
func TestTranscriptCapWritesWholeLines(t *testing.T) {
	tr := openTestTranscript(t, 10)
	tr.Raw([]byte(`{"type":"agent.output","text":"a much longer line than the cap"}`))
	tr.Close()

	f, err := os.Open(tr.Path())
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var v map[string]any
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Errorf("line %d is not parseable JSON: %v (%q)", lines+1, err, sc.Text())
		}
		lines++
	}
	if lines != 1 {
		t.Errorf("wrote %d lines, want the one whole line that tripped the cap", lines)
	}
}

// TestTranscriptCapDisabledByZero: no cap means no cap, however much is
// written.
func TestTranscriptCapDisabledByZero(t *testing.T) {
	tr := openTestTranscript(t, 0)
	for range 100 {
		tr.Raw([]byte(strings.Repeat("x", 1000)))
	}
	if tr.Exceeded() {
		t.Error("Exceeded with the cap disabled")
	}
	if tr.size < 100_000 {
		t.Errorf("size = %d; writes were dropped with the cap disabled", tr.size)
	}
}

// TestNoteOverLimitSurvivesTheCap: the annotation explaining *why* the
// transcript stops is the one line a reader needs most, so the cap must not
// suppress it.
func TestNoteOverLimitSurvivesTheCap(t *testing.T) {
	tr := openTestTranscript(t, 16)
	tr.Raw([]byte(strings.Repeat("x", 64)))
	if !tr.Exceeded() {
		t.Fatal("setup: cap not tripped")
	}

	tr.Note("ignored", map[string]any{"k": "v"})
	sizeAfterNormalNote := tr.size

	tr.NoteOverLimit("transcript_limit", map[string]any{"max_bytes": 16})
	tr.Close()

	if tr.size <= sizeAfterNormalNote {
		t.Error("NoteOverLimit was dropped by the cap")
	}
	body, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(body), "vincent.transcript_limit") {
		t.Errorf("the limit annotation is missing from the file:\n%s", body)
	}
	if strings.Contains(string(body), "vincent.ignored") {
		t.Error("an ordinary note was written after the cap")
	}
}
