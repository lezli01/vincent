package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
