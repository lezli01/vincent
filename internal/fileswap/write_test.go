package fileswap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.md")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("content = %q, %v; want new", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want only the file: %v", len(entries), entries)
	}
}

func TestWriteFileIntoAMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "out.md")
	if err := WriteFile(path, []byte("x"), 0o644); err == nil {
		t.Fatal("WriteFile into a missing directory succeeded")
	}
}
