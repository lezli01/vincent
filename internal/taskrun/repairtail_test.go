package taskrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepairTailKeepsItsOwnByteWindow pins the bound the repair prompt's
// transcript excerpt reads to (task 025), which is its own and not §8.4's.
//
// outputTail gained a byte ceiling when capture stopped dropping over-long
// lines (#139). tailLines has already narrowed the file to
// repairTranscriptTailBytes before the tail sees a line, so applying §8.4's
// smaller outputTailBytes on top of it would halve the excerpt without
// anything saying so — a silent narrowing is exactly what #139 is about.
func TestRepairTailKeepsItsOwnByteWindow(t *testing.T) {
	t.Parallel()

	if outputTailBytes >= repairTranscriptTailBytes {
		t.Skip("the two bounds no longer differ; there is nothing to narrow")
	}

	// Lines that are individually small, so only the byte bounds — not the
	// line count and not the single-line cut — can decide what survives. The
	// file is written past repairTranscriptTailBytes so the read window, not
	// the file, is what bounds the answer.
	const lineBytes = 4 << 10
	const lines = 2 * repairTranscriptTailBytes / lineBytes
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var sb strings.Builder
	for range lines {
		sb.WriteString(strings.Repeat("x", lineBytes-1))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// The seek lands on a line boundary here, and that whole line is dropped
	// as the fragment it cannot be told apart from.
	want := repairTranscriptTailBytes - lineBytes

	got := tailLines(path, lines, repairTranscriptTailBytes)
	if len(got)+1 != want {
		t.Fatalf("tail = %d bytes, want %d: the repair excerpt was narrowed past its own bound", len(got)+1, want)
	}
	if len(got) <= outputTailBytes {
		t.Fatalf("tail = %d bytes, which fits inside outputTailBytes (%d); §8.4's ceiling is being applied to the repair excerpt",
			len(got), outputTailBytes)
	}
}
