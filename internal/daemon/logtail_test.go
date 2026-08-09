package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestTailFileShorterThanWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, path, "one\ntwo\nthree\n")

	lines, err := TailFile(path, 2)
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}
	if want := []string{"two", "three"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

// A final line with no newline is the normal shape of a log that is being
// written to right now, so it must not be dropped.
func TestTailFileKeepsUnterminatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, path, "one\ntwo")

	lines, err := TailFile(path, 10)
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}
	if want := []string{"one", "two"}; !equalLines(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

// Past the window the read starts mid-file, so the first line it sees is
// half of one the window cut — dropped rather than rendered as a truncated
// log entry.
func TestTailFileLongerThanWindowDropsTheClippedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	var b strings.Builder
	const lineLen = 200
	for i := 0; b.Len() < tailWindow*2; i++ {
		fmt.Fprintf(&b, "line-%06d%s\n", i, strings.Repeat("x", lineLen))
	}
	writeFile(t, path, b.String())

	lines, err := TailFile(path, 0)
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("no lines returned")
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "line-") || len(l) != len("line-000000")+lineLen {
			t.Fatalf("line %d is not whole: %q", i, l)
		}
	}
	all := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if lines[len(lines)-1] != all[len(all)-1] {
		t.Fatalf("last line = %q, want %q", lines[len(lines)-1], all[len(all)-1])
	}
}

// An empty log and a missing one are different facts: the view says
// "nothing logged yet" for one and reports the error for the other.
func TestTailFileEmptyIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, path, "")

	lines, err := TailFile(path, 10)
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %q, want none", lines)
	}
}

func TestTailFileMissingReportsTheError(t *testing.T) {
	_, err := TailFile(filepath.Join(t.TempDir(), "absent.log"), 10)
	if err == nil {
		t.Fatal("want an error for a missing log")
	}
}

// Rotation is the reason the reader opens and closes on every call rather
// than following a handle: lumberjack rotates by renaming the live file, and
// on Windows that rename fails while another process holds it open. This runs
// on all three CI platforms, so a reader that kept a handle fails here.
func TestTailFileFollowsLumberjackRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	lj := &lumberjack.Logger{Filename: path, MaxSize: 1, MaxBackups: 3}
	defer lj.Close()

	line := strings.Repeat("y", 1023) + "\n"
	for i := 0; i < 1200; i++ {
		// Tailing while the writer rotates underneath is the real sequence;
		// doing it only afterwards would not exercise the overlap.
		if _, err := lj.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if i%100 == 0 {
			if _, err := TailFile(path, 20); err != nil {
				t.Fatalf("tail during write %d: %v", i, err)
			}
		}
	}
	entries, err := filepath.Glob(filepath.Join(dir, "daemon-*.log"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("log did not rotate (glob %v, err %v)", entries, err)
	}

	if _, err := lj.Write([]byte("after-rotation\n")); err != nil {
		t.Fatalf("write after rotation: %v", err)
	}
	lines, err := TailFile(path, 5)
	if err != nil {
		t.Fatalf("TailFile after rotation: %v", err)
	}
	if len(lines) == 0 || lines[len(lines)-1] != "after-rotation" {
		t.Fatalf("tail = %q, want it to end at the post-rotation line", lines)
	}
}

func TestLogTailReportsAMissingLogAsText(t *testing.T) {
	got := LogTail(t.TempDir(), 5)
	if !strings.HasPrefix(got, "(no daemon log:") {
		t.Fatalf("LogTail = %q", got)
	}
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
