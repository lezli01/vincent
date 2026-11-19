package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// TestChatSendQueuesOnALiveChat is task 156 through the real binary and a
// real daemon (review F3): `vincent chat send` on a chat whose turn is still
// running queues the message and says so instead of waiting, `vincent chat
// queue list` and `edit` round-trip it, and the queue drains in order once the
// live turn ends done.
func TestChatSendQueuesOnALiveChat(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	env := append(hermeticEnv(), config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = env
		_, _ = cmd.CombinedOutput()
	})
	// Every turn takes long enough for the sends below to land behind it.
	// The daemon inherits this, so it is set before the daemon starts.
	t.Setenv("FAKEAGENT_DELAY_MS", "1500")
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
	out, code := runVincent(t, dataDir, cfgDir, "chat", "start", "queue", "--project", "1", "--agent", "claude", "--json")
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

	type turn struct {
		State  string `json:"state"`
		Prompt string `json:"prompt"`
	}
	show := func() (state string, turns []turn) {
		t.Helper()
		out, code := runVincent(t, dataDir, cfgDir, "chat", "show", id, "--json")
		if code != 0 {
			t.Fatalf("chat show --json: code %d, out %q", code, out)
		}
		var shown struct {
			Chat struct {
				State string `json:"state"`
			} `json:"chat"`
			Turns []turn `json:"turns"`
		}
		if err := json.Unmarshal([]byte(out), &shown); err != nil {
			t.Fatalf("chat show --json is not JSON: %v (%q)", err, out)
		}
		return shown.Chat.State, shown.Turns
	}

	// The first send waits for its answer, so it runs beside the test.
	first := exec.Command(vincentBin, "chat", "send", id, "first")
	first.Env = env
	if err := first.Start(); err != nil {
		t.Fatalf("start the first send: %v", err)
	}
	t.Cleanup(func() { _ = first.Process.Kill(); _ = first.Wait() })
	wait.Until(t, "the first turn running", func() bool {
		state, turns := show()
		return state == "running" && len(turns) == 1
	})

	out, code = runVincent(t, dataDir, cfgDir, "chat", "send", id, "second")
	if code != 0 || !strings.Contains(out, "queued as message") {
		t.Fatalf("chat send on a running chat: code %d, out %q; want it queued", code, out)
	}
	out, code = runVincent(t, dataDir, cfgDir, "chat", "send", id, "third", "--json")
	var third struct {
		ID     int64  `json:"id"`
		Prompt string `json:"prompt"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &third) != nil || third.ID == 0 || third.Prompt != "third" {
		t.Fatalf("chat send --json on a running chat: code %d, out %q; want the queued message", code, out)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "chat", "queue", "edit", id,
		strconv.FormatInt(third.ID, 10), "third, edited"); code != 0 {
		t.Fatalf("chat queue edit: code %d, out %q", code, out)
	}
	out, code = runVincent(t, dataDir, cfgDir, "chat", "queue", "list", id, "--json")
	var q struct {
		Messages []struct {
			ID     int64  `json:"id"`
			Prompt string `json:"prompt"`
		} `json:"messages"`
		Paused bool `json:"paused"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &q) != nil {
		t.Fatalf("chat queue list --json: code %d, out %q", code, out)
	}
	if len(q.Messages) != 2 || q.Messages[0].Prompt != "second" || q.Messages[1].Prompt != "third, edited" || q.Paused {
		t.Fatalf("chat queue list = %+v, want second then the edited third, unpaused", q)
	}

	if err := first.Wait(); err != nil {
		t.Fatalf("the first send: %v", err)
	}
	wait.Until(t, "the queue drained", func() bool {
		state, turns := show()
		return state == "idle" && len(turns) == 3
	})
	_, turns := show()
	for i, want := range []string{"first", "second", "third, edited"} {
		if turns[i].Prompt != want || turns[i].State != "done" {
			t.Errorf("turn %d = %q/%s, want %q/done", i+1, turns[i].Prompt, turns[i].State, want)
		}
	}
	if out, code := runVincent(t, dataDir, cfgDir, "chat", "queue", "list", id); code != 0 || !strings.Contains(out, "no queued messages") {
		t.Errorf("chat queue list after the drain: code %d, out %q", code, out)
	}
}
