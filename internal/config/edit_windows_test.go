package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteFileWaitsOutAReaderOfTheTarget (issue #969): a Windows rename
// cannot replace a file another handle has open unless that handle passed
// FILE_SHARE_DELETE, which `os.Open`/`os.ReadFile` do not. The daemon opens
// config.yaml and every trigger file that way on its own reload, so a config
// PATCH or a trigger write that raced one answered 500. workflow.WriteFile
// waits such a handle out (internal/workflow/replace_windows.go); this writer
// must too.
func TestWriteFileWaitsOutAReaderOfTheTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("max_concurrent: 1\n"), FilePerm); err != nil {
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

	want := []byte("max_concurrent: 2\n")
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
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file survived the write: %v", err)
	}
}
