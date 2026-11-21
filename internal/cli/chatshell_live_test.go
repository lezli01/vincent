package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// `vincent chat shell` and `vincent chat image` (task 170) against the real
// handlers, a started chat runner and the host's real shell. Every command
// the daemon runs is spelled in the sh-and-pwsh intersection (§8.3).

func newChatShellLive(t *testing.T) *liveHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	bin := func() string { return fake }
	return newLiveHarness(t, withChatShell(agent.NewRegistry(claude.New(bin), codex.New(bin))))
}

// chatShellJSON runs args with --json and decodes the run it prints.
func chatShellJSON(t *testing.T, args ...string) (*apiclient.ChatShellRun, int) {
	t.Helper()
	out, errOut, code := runCLI(t, append(args, "--json")...)
	var run apiclient.ChatShellRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("%v --json = %q exit %d (%s): %v", args, out, code, errOut, err)
	}
	return &run, code
}

func TestChatShellCommand(t *testing.T) {
	h := newChatShellLive(t)
	chat := h.addFilesChat(t, "claude", testrepo.Init(t, "main"), chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	// Success: the output on stdout, exit 0.
	out, errOut, code := runCLI(t, "chat", "shell", id, "git --version")
	if code != 0 || !strings.Contains(out, "git version") {
		t.Fatalf("shell git --version = %q exit %d (%s)", out, code, errOut)
	}

	// The command's own exit code is the process's.
	if _, errOut, code := runCLI(t, "chat", "shell", id, "exit 3"); code != 3 {
		t.Errorf("shell exit 3: exit %d (%s), want 3", code, errOut)
	}

	// --attach marks the finished run; --json prints the final row.
	run, code := chatShellJSON(t, "chat", "shell", id, "exit 4", "--attach")
	if code != 4 || run.State != apiclient.ChatShellExited || run.ExitCode == nil || *run.ExitCode != 4 || !run.Attach {
		t.Fatalf("shell --attach --json = %+v exit %d", run, code)
	}
	runID := strconv.FormatInt(run.ID, 10)

	// The listing: oldest first, the marked run's SENT column says so.
	out, errOut, code = runCLI(t, "chat", "shell", "ls", id)
	if code != 0 {
		t.Fatalf("shell ls exit %d (%s)", code, errOut)
	}
	rows := chatFileLines(out)
	if len(rows) != 4 || !strings.HasPrefix(rows[0], "ID") ||
		!strings.Contains(rows[2], "exited 3") || !strings.Contains(rows[3], "next prompt") ||
		!strings.HasSuffix(rows[3], "exit 4") {
		t.Errorf("shell ls:\n%s", out)
	}
	out, _, _ = runCLI(t, "chat", "shell", "ls", id, "--json")
	var runs []apiclient.ChatShellRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs) != 3 || runs[2].ID != run.ID {
		t.Errorf("shell ls --json = %s (%v)", out, err)
	}

	// After-the-fact attach and --undo.
	if out, errOut, code := runCLI(t, "chat", "shell", "attach", id, runID, "--undo"); code != 0 ||
		out != "run "+runID+" is no longer attached\n" {
		t.Errorf("attach --undo = %q exit %d (%s)", out, code, errOut)
	}
	if got, code := chatShellJSON(t, "chat", "shell", "attach", id, runID); code != 0 || !got.Attach {
		t.Errorf("attach = %+v exit %d", got, code)
	}

	// Refusals carry the daemon's message: a second command while one runs,
	// an unknown run, and any command on an archived chat.
	long, err := h.st.ListChatShellRuns(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		_, _, code := runCLI(t, "chat", "shell", id, "sleep 30")
		done <- code
	}()
	wait.Until(t, "the long command to start", func() bool {
		rs, err := h.st.ListChatShellRuns(t.Context(), chat.ID)
		return err == nil && len(rs) == len(long)+1
	})
	if _, errOut, code := runCLI(t, "chat", "shell", id, "git --version"); code != 1 || !strings.Contains(errOut, "already has a shell command running") {
		t.Errorf("a second command: exit %d (%s), want 1 with the refusal", code, errOut)
	}
	if _, errOut, code := runCLI(t, "chat", "cancel", id); code != 0 {
		t.Fatalf("chat cancel exit %d (%s)", code, errOut)
	}
	// The canceled command's own process says why it has no exit code.
	if code := <-done; code != 1 {
		t.Errorf("a canceled command exits %d, want 1", code)
	}
	if _, errOut, code := runCLI(t, "chat", "shell", "attach", id, "9999"); code != 1 || !strings.Contains(errOut, "Error:") {
		t.Errorf("attach of an unknown run: exit %d (%s)", code, errOut)
	}
	archived := h.addFilesChat(t, "claude", testrepo.Init(t, "main"), chatstate.Archived)
	if _, errOut, code := runCLI(t, "chat", "shell", strconv.FormatInt(archived.ID, 10), "git --version"); code != 1 ||
		!strings.Contains(errOut, "archived chat cannot run a shell command") {
		t.Errorf("a command on an archived chat: exit %d (%s)", code, errOut)
	}
	if _, _, code := runCLI(t, "chat", "shell", id, " "); code == 0 {
		t.Error("an empty command succeeded")
	}
}

func TestChatShellEnding(t *testing.T) {
	for state, want := range map[string]string{
		apiclient.ChatShellTimedOut:    "timed out",
		apiclient.ChatShellInterrupted: "interrupted",
		apiclient.ChatShellCanceled:    "canceled",
		apiclient.ChatShellFailed:      "failed to start",
	} {
		if got := chatShellEnding(state); !strings.Contains(got, want) {
			t.Errorf("%s reads %q", state, got)
		}
	}
}

var cliPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestChatImageCommand(t *testing.T) {
	h := newChatShellLive(t)
	dir := testrepo.Init(t, "main")
	chat := h.addFilesChat(t, "codex", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)
	file := filepath.Join(t.TempDir(), "screen shot.png")
	if err := os.WriteFile(file, cliPNG, 0o600); err != nil {
		t.Fatal(err)
	}

	if out, errOut, code := runCLI(t, "chat", "image", "add", id, file); code != 0 || out != "[image 1]\n" {
		t.Fatalf("image add = %q exit %d (%s)", out, code, errOut)
	}
	out, errOut, code := runCLI(t, "chat", "image", "add", id, file, "--json")
	var img apiclient.ChatImage
	if err := json.Unmarshal([]byte(out), &img); err != nil || code != 0 || img.Number != 2 ||
		img.Chip != "[image 2]" || img.MediaType != "image/png" || img.Name != "screen shot.png" {
		t.Fatalf("image add --json = %q exit %d (%s, %v)", out, code, errOut, err)
	}
	out, _, code = runCLI(t, "chat", "image", "ls", id)
	rows := chatFileLines(out)
	if code != 0 || len(rows) != 3 || !strings.HasPrefix(rows[2], "[image 2]") || !strings.Contains(rows[1], "image/png") {
		t.Errorf("image ls:\n%s", out)
	}

	// Refusals: not an image, an agent that takes none, a missing file.
	notImage := filepath.Join(t.TempDir(), "notes.png")
	if err := os.WriteFile(notImage, []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := runCLI(t, "chat", "image", "add", id, notImage); code != 1 || !strings.Contains(errOut, "Error:") {
		t.Errorf("a non-image: exit %d (%s)", code, errOut)
	}
	plain := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	if _, errOut, code := runCLI(t, "chat", "image", "add", strconv.FormatInt(plain.ID, 10), file); code != 1 ||
		!strings.Contains(errOut, "claude") {
		t.Errorf("an upload to claude: exit %d (%s), want the agent named", code, errOut)
	}
	if _, _, code := runCLI(t, "chat", "image", "add", id, filepath.Join(dir, "missing.png")); code == 0 {
		t.Error("a missing file succeeded")
	}
}
