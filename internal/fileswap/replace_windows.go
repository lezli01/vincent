package workflow

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceFile puts tmp in place of path with mode perm, completing the atomic
// write WriteFile started — retrying while another handle stands in the way.
//
// Windows is the platform where a rename is not purely an operation on a name.
// Replacing an open file needs every other handle on it to have been opened
// with FILE_SHARE_DELETE, and Go's own `os.Open` does not pass that flag
// (`syscall.Open` shares read and write only), so an ordinary reader of the
// target blocks the replacement with ERROR_SHARING_VIOLATION or
// ERROR_ACCESS_DENIED. `os.Chmod` is narrower still — it opens for
// FILE_WRITE_ATTRIBUTES sharing only writes — so a plain reader fails it too.
// The daemon supplies those readers itself: writing a workflow makes the
// scope directory's watcher fire, the registry reloads, and the reload reads
// every `*.yaml` in the directory including the one being replaced. A virus
// scanner opening the freshly closed temporary file does the same to the
// source side of the rename.
//
// None of those handles is held for long, so the fix is to wait for them
// rather than to fail the write: the caller is an API request whose only other
// answer is a 500 for a file that is about to become writable. A failure that
// outlasts the window is still returned, so a genuinely locked file is still
// an error rather than a hang.
func replaceFile(tmp, path string, perm os.FileMode) error {
	deadline := time.Now().Add(replaceWaitFor)
	delay := replaceFirstDelay
	for {
		err := replaceOnce(tmp, path, perm)
		if err == nil || !contended(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay *= 2; delay > replaceMaxDelay {
			delay = replaceMaxDelay
		}
	}
}

// The wait is bounded generously because the cost of exhausting it is a 500 on
// a save the user has to redo, while the cost of waiting is a request that
// takes a moment longer. The first delay is short so the common case — a
// reader that has already returned — costs one sleep.
const (
	replaceWaitFor    = 2 * time.Second
	replaceFirstDelay = 5 * time.Millisecond
	replaceMaxDelay   = 100 * time.Millisecond
)

func replaceOnce(tmp, path string, perm os.FileMode) error {
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// contended reports whether err is Windows saying another handle is in the
// way, rather than saying the caller may not do this at all. The three codes
// are indistinguishable from a real permission problem at this level, which is
// why the retry is bounded rather than open-ended.
func contended(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
