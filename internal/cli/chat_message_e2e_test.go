package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestChatMessageFileReachesTheStoredPrompt is task 124.5's acceptance
// (issue #501), through the real binary, a real daemon and a fakeagent turn:
// a message given with --message-file is the prompt the daemon stored, byte
// for byte. The unit tests prove the request body; this proves the turn.
//
// stdin is fed through exec.Cmd.Stdin rather than a shell pipe, so the test
// runs identically on Windows. What a shell does to argv — Git Bash's `/name`
// rewrite — is exactly what this flag keeps out of the path, and proving that
// on Git Bash itself is m14's (124.15).
func TestChatMessageFileReachesTheStoredPrompt(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	// A session store, so the second turn resumes the first the way a real
	// chat does. The daemon inherits it, so it is set before the daemon starts.
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("agents:\n  claude:\n    path: \""+filepath.ToSlash(fake)+"\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	repo := testrepo.Init(t, "main")
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	out, code := runVincent(t, dataDir, cfgDir,
		"chat", "start", "message file", "--project", "1", "--agent", "claude", "--json")
	if code != 0 {
		t.Fatalf("chat start: code %d, out %q", code, out)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == 0 {
		t.Fatalf("chat start --json: %v (%q)", err, out)
	}
	id := strconv.FormatInt(created.ID, 10)

	// Turn 1: `printf '/x hi' | vincent chat send N --message-file -`, the
	// issue's own form — no trailing newline, because printf adds none.
	if out, code := runVincentStdin(t, dataDir, cfgDir, "/x hi",
		"chat", "send", id, "--message-file", "-"); code != 0 {
		t.Fatalf("chat send --message-file -: code %d, out %q", code, out)
	}

	// Turn 2: a file, holding what no argv carries intact through every shell
	// and ending in "\n", which is kept: nothing is trimmed.
	path := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(path, []byte(messageFileProbe), 0o600); err != nil {
		t.Fatalf("write message file: %v", err)
	}
	if out, code := runVincent(t, dataDir, cfgDir,
		"chat", "send", id, "--message-file", path); code != 0 {
		t.Fatalf("chat send --message-file PATH: code %d, out %q", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "chat", "show", id, "--json")
	if code != 0 {
		t.Fatalf("chat show --json: code %d, out %q", code, out)
	}
	var shown struct {
		Turns []struct {
			Seq    int    `json:"seq"`
			State  string `json:"state"`
			Prompt string `json:"prompt"`
		} `json:"turns"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("chat show --json is not JSON: %v (%q)", err, out)
	}
	if len(shown.Turns) != 2 {
		t.Fatalf("turns = %+v, want two", shown.Turns)
	}
	for i, want := range []string{"/x hi", messageFileProbe} {
		turn := shown.Turns[i]
		if turn.State != "done" {
			t.Errorf("turn %d state = %q, want done", turn.Seq, turn.State)
		}
		if turn.Prompt != want {
			t.Errorf("turn %d prompt = %q, want exactly %q", turn.Seq, turn.Prompt, want)
		}
	}
}
