package chatrun

import (
	"bytes"
	"io"
	"runtime"
	"strings"
	"testing"
)

// repeatReader yields n copies of b, then the tail — a file far bigger than
// anything the test holds in memory.
func repeatReader(b byte, n int64, tail string) io.Reader {
	return io.MultiReader(io.LimitReader(byteSource(b), n), strings.NewReader(tail))
}

type byteSource byte

func (s byteSource) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(s)
	}
	return len(p), nil
}

// allocatedBy is the bytes f allocates.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestReadRangeBoundsMemory (review F5): one line far longer than the cap,
// in range or before it, is read through a buffer, never held whole.
func TestReadRangeBoundsMemory(t *testing.T) {
	const huge = 64 << 20
	const bound = 8 * MentionLinesLimit // a few copies of the cap; the line is 4096 of them

	t.Run("a line longer than the cap", func(t *testing.T) {
		var lines []string
		var truncated, ok bool
		n := allocatedBy(func() {
			lines, _, truncated, ok = readRange(repeatReader('x', huge, "\n"), 1, 1, MentionLinesLimit)
		})
		if !ok || !truncated || len(lines) != 1 || len(lines[0]) != MentionLinesLimit {
			t.Fatalf("lines %d (first %d bytes), truncated %v, ok %v; want one capped line", len(lines), len(lines[0]), truncated, ok)
		}
		if n > bound {
			t.Errorf("allocated %d bytes for a %d-byte line, want under %d", n, huge, bound)
		}
	})

	t.Run("a long prefix before the range", func(t *testing.T) {
		var lines []string
		var ok bool
		n := allocatedBy(func() {
			lines, _, _, ok = readRange(repeatReader('x', huge, "\nwanted\n"), 2, 2, MentionLinesLimit)
		})
		if !ok || len(lines) != 1 || lines[0] != "wanted" {
			t.Fatalf("lines = %q, ok %v; want [wanted]", lines, ok)
		}
		if n > bound {
			t.Errorf("allocated %d bytes skipping a %d-byte line, want under %d", n, huge, bound)
		}
	})
}

// TestReadRangeLines pins what the bounded reader still answers exactly.
func TestReadRangeLines(t *testing.T) {
	long := strings.Repeat("y", 10000)
	cases := []struct {
		name      string
		body      string
		from, to  int
		limit     int
		want      []string
		last      int
		truncated bool
		ok        bool
	}{
		{"LF", "a\nb\nc\n", 2, 3, 100, []string{"b", "c"}, 3, false, true},
		{"CRLF", "a\r\nb\r\nc", 1, 3, 100, []string{"a", "b", "c"}, 3, false, true},
		{"past the end", "a\n", 3, 4, 100, nil, 0, false, false},
		{"blank lines", "\n\nx\n", 1, 3, 100, []string{"", "", "x"}, 3, false, true},
		{"CRLF line at the cap", "abcd\r\nz\n", 1, 2, 5, []string{"abcd"}, 1, true, true},
		{"cut mid line", "abcdefgh\n", 1, 1, 4, []string{"abcd"}, 1, true, true},
		{"long line across buffers", long + "\r\nz\n", 1, 2, 20000, []string{long, "z"}, 2, false, true},
		{"NUL in range", "a\nb\x00c\n", 1, 2, 100, nil, 0, false, false},
		{"NUL past the cap in a long line", strings.Repeat("q", 9000) + "\x00\n", 1, 1, 100, nil, 0, false, false},
		{"NUL before the range", "\x00\nok\n", 2, 2, 100, []string{"ok"}, 2, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, last, truncated, ok := readRange(bytes.NewReader([]byte(c.body)), c.from, c.to, c.limit)
			if strings.Join(lines, "|") != strings.Join(c.want, "|") || len(lines) != len(c.want) ||
				last != c.last || truncated != c.truncated || ok != c.ok {
				t.Errorf("= %q, %d, %v, %v; want %q, %d, %v, %v", lines, last, truncated, ok, c.want, c.last, c.truncated, c.ok)
			}
		})
	}
}
