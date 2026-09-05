package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestWriteFileWaitsOutAReaderOfTheTarget: a Windows rename cannot replace a
// file another handle has open unless that handle passed FILE_SHARE_DELETE,
// which `os.Open` does not. The daemon opens its own workflow files that way
// on every registry reload, so a save that raced one used to answer 500.
func TestWriteFileWaitsOutAReaderOfTheTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocks.yaml")
	if err := os.WriteFile(path, []byte("name: blocks\n"), FilePerm); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the target: %v", err)
	}
	// The handle closes while the write is retrying, which is the shape of
	// the real contention: a reload that is already on its way out.
	closed := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = f.Close()
		close(closed)
	}()

	want := []byte("name: blocks\ndescription: replaced\n")
	if err := WriteFile(path, want); err != nil {
		t.Fatalf("WriteFile with a reader on the target: %v", err)
	}
	<-closed
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("file = %q, want %q", got, want)
	}
	if _, err := os.Stat(path + ".vincent-tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file survived the write: %v", err)
	}
}

// TestReplaceFileDoesNotRetryAnErrorNobodyWillClear: the wait is for a handle
// that is about to close, so an error that says something else is returned on
// the first attempt rather than after the window.
func TestReplaceFileDoesNotRetryAnErrorNobodyWillClear(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	err := replaceFile(filepath.Join(dir, "absent.vincent-tmp"), filepath.Join(dir, "absent.yaml"), FilePerm)
	if err == nil {
		t.Fatal("replaceFile succeeded with no source file")
	}
	if waited := time.Since(start); waited >= replaceWaitFor {
		t.Errorf("waited %s on an error that is not contention", waited)
	}
}

func TestContended(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"sharing violation", windows.ERROR_SHARING_VIOLATION, true},
		{"lock violation", windows.ERROR_LOCK_VIOLATION, true},
		{"access denied", windows.ERROR_ACCESS_DENIED, true},
		{"wrapped in a LinkError", &os.LinkError{Err: windows.ERROR_SHARING_VIOLATION}, true},
		{"file not found", windows.ERROR_FILE_NOT_FOUND, false},
		{"disk full", windows.ERROR_DISK_FULL, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contended(tc.err); got != tc.want {
				t.Errorf("contended(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
