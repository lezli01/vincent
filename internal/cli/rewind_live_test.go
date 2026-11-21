package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// `vincent chat rewind` and `vincent chat fork` (task 167) against the real
// handlers and a started chat runner over the fake agent: the command sends
// the body the route decodes and prints what the route answered, and a
// refusal reaches the user as the route's reason with a non-zero exit.
func TestChatRewindAndForkCommands(t *testing.T) {
	h := newChatShellLive(t)
	// A project of its own whose repository is the chat's directory: the
	// harness's default project has no repository, and a checkpoint ref is
	// read from the project's.
	dir := testrepo.Init(t, "main")
	p := &store.Project{Name: "rewind", Path: dir, DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	h.projectID = p.ID
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)
	turn := func(prompt string, seq int) {
		t.Helper()
		if _, errOut, code := runCLI(t, "chat", "send", id, prompt); code != 0 {
			t.Fatalf("chat send exit %d (%s)", code, errOut)
		}
		wait.Until(t, "the turn is done and its checkpoint offered", func() bool {
			turns, err := h.st.ListChatTurns(t.Context(), chat.ID)
			c, cerr := h.st.GetChat(t.Context(), chat.ID)
			return err == nil && cerr == nil && len(turns) == seq &&
				turns[seq-1].State == chatstate.TurnDone && c.State == chatstate.Idle && turns[seq-1].CheckpointSHA != ""
		})
	}
	testrepo.WriteFile(t, dir, "a.txt", "one\n")
	turn("first", 1)
	testrepo.WriteFile(t, dir, "a.txt", "two\n")
	turn("second", 2)

	var fork apiclient.ForkResult
	out, errOut, code := runCLI(t, "chat", "fork", id, "--turn", "1", "--title", "forked", "--json")
	if err := json.Unmarshal([]byte(out), &fork); err != nil || code != 0 || fork.Chat.Title != "forked" ||
		fork.Chat.ForkedFrom == nil || fork.Chat.ForkedFrom.Seq != 1 {
		t.Fatalf("fork --json = %q exit %d (%s): %v", out, code, errOut, err)
	}
	var res apiclient.RewindResult
	wait.Until(t, "the chat's queue drains so a rewind is accepted", func() bool {
		out, errOut, code = runCLI(t, "chat", "rewind", id, "--turn", "2", "--mode", "conversation")
		return code == 0
	})
	if !strings.Contains(out, "rewound to before turn 2") {
		t.Fatalf("rewind --mode conversation = %q (%s)", out, errOut)
	}
	out, errOut, _ = runCLI(t, "chat", "rewind", id, "--turn", "1", "--mode", "code", "--json")
	if err := json.Unmarshal([]byte(out), &res); err != nil || !strings.HasSuffix(res.Ref, "/rewound-1") {
		t.Fatalf("rewind --json = %q (%s): %v", out, errOut, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); strings.ReplaceAll(string(b), "\r", "") != "one\n" {
		t.Errorf("a.txt = %q after the CLI rewind", b)
	}

	// The same target again is the route's refusal, named.
	_, errOut, code = runCLI(t, "chat", "rewind", id, "--turn", "2")
	if code == 0 || !strings.Contains(errOut+out, "rewound") {
		t.Errorf("rewind of a rewound turn exit %d (%s)", code, errOut)
	}
	_, errOut, code = runCLI(t, "chat", "fork", id, "--turn", "2")
	if code == 0 || errOut == "" {
		t.Errorf("fork of a rewound turn exit %d (%s), want a refusal", code, errOut)
	}
}
