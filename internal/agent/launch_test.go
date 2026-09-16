package agent_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/procx"
)

// These tests hold HostLauncher to the process semantics the three adapters
// had when each spawned its own run (task 062.1): the child gets the Command's
// directory and environment, Kill reaps the whole tree, PID names a process
// §12.4 can journal, and Wait reports the exit code. The Windows no-window
// attribute is in launch_windows_test.go, where it can be read back.

// claudeArgs is a claude-shaped fakeagent invocation without input mode, so
// the child reads its prompt from stdin to EOF.
var claudeArgs = []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

// launch starts fakeagent through the host launcher with the given scenario
// variables layered over the test's environment.
func launch(t *testing.T, dir string, extraEnv ...string) agent.Process {
	t.Helper()
	path := agenttest.BuildFakeAgent(t)
	p, err := agent.HostLauncher{}.Launch(agent.Command{
		Path:   path,
		Args:   claudeArgs,
		Dir:    dir,
		Env:    append(os.Environ(), extraEnv...),
		Stdin:  strings.NewReader("host launcher prompt"),
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return p
}

// finish drains stdout, waits and releases, returning the exit code.
func finish(t *testing.T, p agent.Process, stdout io.Reader) int {
	t.Helper()
	_, _ = io.Copy(io.Discard, stdout)
	code, err := p.Wait()
	p.Release()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return code
}

func TestHostLauncherGivesTheChildItsEnvironment(t *testing.T) {
	p := launch(t, t.TempDir(),
		"FAKEAGENT_SCENARIO=report-env",
		"FAKEAGENT_REPORT_ENV=VINCENT_LAUNCH_MARKER",
		"VINCENT_LAUNCH_MARKER=only-in-command-env")
	out, err := io.ReadAll(p.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if code := finish(t, p, p.Stdout()); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(string(out), "VINCENT_LAUNCH_MARKER=[only-in-command-env]") {
		t.Errorf("child did not see Command.Env; stdout:\n%s", out)
	}
	if p.Stdin() != nil {
		t.Error("Stdin() is non-nil for a Command that asked for no pipe")
	}
	if argv := p.Argv(); len(argv) == 0 || !slices.Equal(argv[1:], claudeArgs) {
		t.Errorf("Argv() = %q, want the binary then %q", argv, claudeArgs)
	}
}

func TestHostLauncherRunsTheChildInItsDirectory(t *testing.T) {
	dir := t.TempDir()
	// Relative, so only a child whose working directory is dir finds it.
	const marker = "launch-marker.txt"
	if err := os.WriteFile(filepath.Join(dir, marker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := launch(t, dir, "FAKEAGENT_SCENARIO=success", "FAKEAGENT_EDIT_FILE="+marker)
	if code := finish(t, p, p.Stdout()); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	body, err := os.ReadFile(filepath.Join(dir, marker))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Error("marker untouched: the child did not run in Command.Dir")
	}
}

func TestHostLauncherWaitReportsTheExitCode(t *testing.T) {
	p := launch(t, t.TempDir(), "FAKEAGENT_SCENARIO=nonzero-exit")
	// A non-zero exit is the code, never Wait's error: finish fails on one.
	if code := finish(t, p, p.Stdout()); code != 3 {
		t.Errorf("exit code = %d, want fakeagent's 3", code)
	}
}

func TestHostLauncherStdinPipeIsRetained(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	p, err := agent.HostLauncher{}.Launch(agent.Command{
		Path:      path,
		Args:      claudeArgs,
		Dir:       t.TempDir(),
		Env:       append(os.Environ(), "FAKEAGENT_SCENARIO=success"),
		StdinPipe: true,
		Stderr:    io.Discard,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	stdin := p.Stdin()
	if stdin == nil {
		t.Fatal("Stdin() is nil for a Command that asked for a pipe")
	}
	if _, err := io.WriteString(stdin, "written after launch"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	// The child reads to EOF, so closing the pipe is what lets it finish.
	_ = stdin.Close()
	out, err := io.ReadAll(p.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if code := finish(t, p, p.Stdout()); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(string(out), "written after launch") {
		t.Errorf("child never read the piped prompt; stdout:\n%s", out)
	}
}

func TestHostLauncherPIDIsJournalable(t *testing.T) {
	p := launch(t, t.TempDir(), "FAKEAGENT_SCENARIO=hang")
	stdout := bufio.NewReader(p.Stdout())
	// One line proves the child is up and not merely forked.
	if _, err := stdout.ReadString('\n'); err != nil {
		t.Fatalf("read first line: %v", err)
	}
	id, err := procx.Identity(p.PID())
	if err != nil || id == "" {
		t.Errorf("procx.Identity(%d) = %q, %v; §12.4 could not journal this run", p.PID(), id, err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if code := finish(t, p, stdout); code == 0 {
		t.Error("exit code = 0 after Kill")
	}
}

func TestHostLauncherKillReapsTheTree(t *testing.T) {
	p := launch(t, t.TempDir(), "FAKEAGENT_SCENARIO=hang", "FAKEAGENT_SPAWN_CHILD=1")
	sc := bufio.NewScanner(p.Stdout())
	childPID := childPIDFrom(t, sc)
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// finish drains what the scanner has not buffered, which is all Wait
	// needs: the pipe emptied, not every line parsed.
	_ = finish(t, p, p.Stdout())
	awaitGone(t, childPID)
}

// TestContextCancelReapsTheTreeThroughTheLauncher is the adapter half: the
// cancel path reaches the tree through RunHandle.Kill, which now delegates to
// the launcher's Process, so an explicit host launcher must still reap a
// grandchild when the run's context ends.
func TestContextCancelReapsTheTreeThroughTheLauncher(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h, err := claude.New(func() string { return path }).Start(ctx, agent.RunSpec{
		Prompt:         "hang with a child",
		WorkDir:        t.TempDir(),
		PermissionMode: agent.FullAuto,
		Env:            append(os.Environ(), "FAKEAGENT_SCENARIO=hang", "FAKEAGENT_SPAWN_CHILD=1"),
		Launcher:       agent.HostLauncher{},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childPID := 0
	for ev := range h.Events() {
		if pid := childPIDOf(ev.Raw); pid != 0 {
			childPID = pid
			break
		}
	}
	if childPID == 0 {
		t.Fatal("fakeagent never reported a child pid")
	}
	cancel()
	for range h.Events() { //nolint:revive // draining is the contract
	}
	res, _ := h.Wait()
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0; context cancel must kill the run")
	}
	awaitGone(t, childPID)
}

// childPIDFrom scans stdout for fakeagent's child report.
func childPIDFrom(t *testing.T, sc *bufio.Scanner) int {
	t.Helper()
	for sc.Scan() {
		if pid := childPIDOf(sc.Bytes()); pid != 0 {
			if !processAlive(pid) {
				t.Fatalf("child %d is not alive before the kill", pid)
			}
			return pid
		}
	}
	t.Fatal("fakeagent never reported a child pid")
	return 0
}

func childPIDOf(line []byte) int {
	var v struct {
		Type string `json:"type"`
		PID  int    `json:"pid"`
	}
	if json.Unmarshal(line, &v) != nil || v.Type != "fakeagent.child" {
		return 0
	}
	return v.PID
}

func awaitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the tree kill", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processAlive reports whether pid names a live process. On Windows a
// successful handle open means alive; on POSIX signal 0 probes existence.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		_ = p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
