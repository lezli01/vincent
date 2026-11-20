package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestEditLinesRewritesNamedLines covers FAKEAGENT_EDIT_LINES, the shape of
// FAKEAGENT_EDIT_FILE scripts/168-gate.sh needs (task 168): two edits far
// enough apart for git to cut two hunks, each line keeping its own ending so
// a CRLF checkout on Windows gains no line-ending change, and the
// FAKEAGENT_WRITE_FILE beside it still adding its own file.
func TestEditLinesRewritesNamedLines(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	for _, tc := range []struct {
		name, spec, before, want string
	}{
		{"two lines", "2,4", "a\nb\nc\nd\ne\n", "a\nfakeagent was here\nc\nfakeagent was here\ne\n"},
		{"crlf kept", "2", "a\r\nb\r\nc\r\n", "a\r\nfakeagent was here\r\nc\r\n"},
		{"no final newline", "3", "a\nb\nc", "a\nb\nfakeagent was here"},
		{"past the end and junk are skipped", "9, x,0,1", "a\nb\n", "fakeagent was here\nb\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "-p", "--output-format", "stream-json")
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader("do a thing")
			cmd.Env = append(cmd.Environ(),
				"FAKEAGENT_EDIT_FILE=notes.txt", "FAKEAGENT_EDIT_LINES="+tc.spec,
				"FAKEAGENT_WRITE_FILE=added.txt", "FAKEAGENT_WRITE_CONTENT=new\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run fakeagent: %v\n%s", err, out)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("notes.txt = %q, want %q", got, tc.want)
			}
			added, err := os.ReadFile(filepath.Join(dir, "added.txt"))
			if err != nil || string(added) != "new\n" {
				t.Errorf("added.txt = %q, %v; want %q", added, err, "new\n")
			}
		})
	}
}
