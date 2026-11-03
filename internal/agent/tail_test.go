package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestTailWriterKeepsWritesUnderTheCap(t *testing.T) {
	w := NewTailWriter(16)
	for _, p := range []string{"abc", "def"} {
		n, err := w.Write([]byte(p))
		if n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", p, n, err, len(p))
		}
	}
	if got := w.String(); got != "abcdef" {
		t.Fatalf("String() = %q, want %q", got, "abcdef")
	}
}

func TestTailWriterKeepsTheLastMaxBytesAcrossWrites(t *testing.T) {
	w := NewTailWriter(5)
	for _, p := range []string{"abc", "def", "ghi"} {
		if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if got := w.String(); got != "efghi" {
		t.Fatalf("String() = %q, want %q", got, "efghi")
	}
}

func TestTailWriterKeepsTheTailOfOneOversizedWrite(t *testing.T) {
	w := NewTailWriter(4)
	p := []byte("0123456789")
	if n, err := w.Write(p); n != len(p) || err != nil {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(p))
	}
	if got := w.String(); got != "6789" {
		t.Fatalf("String() = %q, want %q", got, "6789")
	}
}

func TestTailWriterStringTrimsWhitespace(t *testing.T) {
	w := NewTailWriter(64)
	_, _ = w.Write([]byte("\n  error: boom \r\n\t"))
	if got := w.String(); got != "error: boom" {
		t.Fatalf("String() = %q, want %q", got, "error: boom")
	}
}

func TestTailWriterConcurrentWriters(t *testing.T) {
	const writers, writes = 8, 200
	w := NewTailWriter(100)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range writes {
				_, _ = w.Write([]byte("x"))
				_ = w.String()
			}
		}()
	}
	wg.Wait()
	if got := w.String(); got != strings.Repeat("x", 100) {
		t.Fatalf("String() = %d bytes, want 100 x's", len(got))
	}
}
