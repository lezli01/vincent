package agenttest

import (
	"bytes"
	"os"
	"testing"
)

// TestBuildFakeAgentLinux pins the cross-compile a containerized agent step's
// tests and the m12 gate rely on: whatever the host, the binary is a Linux
// ELF, and each architecture is built once.
func TestBuildFakeAgentLinux(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles cmd/fakeagent")
	}
	path := BuildFakeAgentLinux(t, "")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.Read(magic); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(magic, []byte("\x7fELF")) {
		t.Fatalf("magic = %q, want an ELF binary", magic)
	}
	if again := BuildFakeAgentLinux(t, "amd64"); again != path {
		t.Fatalf("amd64 built twice: %s then %s", path, again)
	}
}
