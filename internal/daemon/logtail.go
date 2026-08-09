package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// tailWindow bounds one tail read. The log is size-rotated at 10 MB, so
// reading it whole to show the last few dozen lines wastes most of what it
// reads — and the TUI's daemon view re-reads on a timer.
const tailWindow = 64 << 10

// LogPath is the daemon log inside a data dir. Clients derive it themselves:
// the log is worth reading exactly when the daemon is not there to serve it.
func LogPath(dataDir string) string {
	return filepath.Join(dataDir, "logs", "daemon.log")
}

// TailFile returns up to n trailing lines of path, reading at most the last
// tailWindow bytes.
//
// It opens, reads and closes on every call, deliberately: lumberjack rotates
// by renaming the live file, and on Windows renaming a file another process
// holds open fails. A follower that kept a handle would break the daemon's
// own log rotation for as long as it was watching.
//
// A returned error is the file being absent or unreadable, which is a
// different fact from a log with nothing in it — an empty file returns no
// lines and no error.
func TailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if size > tailWindow {
		start = size - tailWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return lastLines(string(b), n, start > 0), nil
}

// lastLines splits a tail window into its final n lines. clipped reports that
// the window started mid-file, in which case the first line is a fragment of
// one the window cut in half and is dropped — unless it is the only line
// there is, where half a line still beats nothing.
func lastLines(s string, n int, clipped bool) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if clipped && len(lines) > 1 {
		lines = lines[1:]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
