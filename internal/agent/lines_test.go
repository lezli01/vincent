package agent

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func collectLines(t *testing.T, in string) ([][]byte, error) {
	t.Helper()
	var got [][]byte
	err := ReadLines(strings.NewReader(in), func(line []byte) {
		got = append(got, line)
	})
	return got, err
}

func TestReadLinesSkipsBlankLines(t *testing.T) {
	got, err := collectLines(t, "one\n\n   \n\t\r\ntwo\n")
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(got) != 2 || string(got[0]) != "one" || string(got[1]) != "two" {
		t.Fatalf("lines = %q, want [one two]", got)
	}
}

func TestReadLinesDeliversAFinalUnterminatedLine(t *testing.T) {
	got, err := collectLines(t, "one\ntwo")
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(got) != 2 || string(got[1]) != "two" {
		t.Fatalf("lines = %q, want [one two]", got)
	}
}

// Every line handed to each must survive the next scan: the adapters send it
// on a channel and the transcript keeps it as the event's Raw bytes.
func TestReadLinesHandsOutPrivateCopies(t *testing.T) {
	// Same-length lines are what a reused scanner buffer would overwrite in
	// place.
	got, err := collectLines(t, "aaaa\nbbbb\ncccc\n")
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	want := []string{"aaaa", "bbbb", "cccc"}
	if len(got) != len(want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("line %d = %q, want %q (buffer reused?)", i, got[i], want[i])
		}
	}
}

// eofReader records whether it was read to the end.
type eofReader struct {
	r   io.Reader
	eof bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.eof = true
	}
	return n, err
}

// A line past the cap stops the scan, and the rest of the stream must still be
// read: a pipe nobody empties hangs the CLI until the step timeout (#139).
func TestReadLinesDrainsTheReaderOnAnOversizedLine(t *testing.T) {
	in := "first\n" + strings.Repeat("x", MaxLineBytes+1) + "\n" +
		strings.Repeat("after the long line\n", 1000)
	rd := &eofReader{r: strings.NewReader(in)}
	var got []string
	err := ReadLines(rd, func(line []byte) { got = append(got, string(line)) })
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("ReadLines err = %v, want bufio.ErrTooLong", err)
	}
	if len(got) != 1 || got[0] != "first" {
		t.Fatalf("lines = %q, want [first]", got)
	}
	if !rd.eof {
		t.Fatal("reader was not drained to EOF after the scanner failed")
	}
}
