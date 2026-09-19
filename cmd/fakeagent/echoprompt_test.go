package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestEchoPromptKeepsBlocksApart pins what echo-prompt records of an
// input-mode user line (issue #499): one text block is the JSON string every
// reader of the file already decodes, and several are an array of them, so a
// context block ahead of the message never reads as the two fused into one.
func TestEchoPromptKeepsBlocksApart(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	file := filepath.Join(t.TempDir(), "prompts.jsonl")
	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json"}

	for _, blocks := range [][]string{
		{"Title: Probe task\n", "/manual-only zebra"},
		{"/manual-only again"},
	} {
		content := make([]map[string]string, 0, len(blocks))
		for _, b := range blocks {
			content = append(content, map[string]string{"type": "text", "text": b})
		}
		line, err := json.Marshal(map[string]any{
			"type": "user", "message": map[string]any{"role": "user", "content": content},
		})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, args...)
		cmd.Stdin = strings.NewReader(string(line) + "\n")
		cmd.Env = append(cmd.Environ(), "FAKEAGENT_SCENARIO=echo-prompt", "FAKEAGENT_PROMPT_FILE="+file)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run fakeagent: %v\n%s", err, out)
		}
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n")), "\n")
	want := []string{
		`["Title: Probe task\n","/manual-only zebra"]`,
		`"/manual-only again"`,
	}
	if len(lines) != len(want) {
		t.Fatalf("prompt file = %q, want %d lines", raw, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %s, want %s", i+1, lines[i], want[i])
		}
	}
}
