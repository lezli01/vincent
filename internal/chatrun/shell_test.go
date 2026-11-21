package chatrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
)

// withShell wires the host's §8.3 shell into the harness's runner, the way
// the daemon wires taskrun's. placed records where each command was asked to
// run.
func (h *harness) withShell(t *testing.T) *[]int64 {
	t.Helper()
	shells := taskrun.NewShells(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var placed []int64
	h.runner.deps.Shell = func(_ context.Context, taskID, _ int64, workDir, command string) (ShellExec, error) {
		placed = append(placed, taskID)
		argv, err := shells.HostCommand(command)
		return ShellExec{Argv: argv, Dir: workDir, Env: os.Environ()}, err
	}
	return &placed
}

// shellDone runs command to its ending and returns the finished row.
func (h *harness) shellDone(t *testing.T, chatID int64, command string) *store.ChatShellRun {
	t.Helper()
	run, err := h.runner.RunShell(t.Context(), chatID, command)
	if err != nil {
		t.Fatalf("RunShell(%q): %v", command, err)
	}
	if run.State != store.ChatShellRunning {
		t.Fatalf("RunShell answered state %q, want running", run.State)
	}
	return h.awaitShell(t, chatID, run.ID)
}

func (h *harness) awaitShell(t *testing.T, chatID, runID int64) *store.ChatShellRun {
	t.Helper()
	var out *store.ChatShellRun
	waitFor(t, func() bool {
		run, err := h.store.GetChatShellRun(t.Context(), chatID, runID)
		if err != nil {
			t.Fatalf("GetChatShellRun: %v", err)
		}
		out = run
		return run.State != store.ChatShellRunning && !h.runner.ShellRunning(chatID)
	})
	return out
}

func TestShellRunRecordsExitAndOutput(t *testing.T) {
	h := newHarness(t)
	h.withShell(t)
	c := h.chat(t)

	ok := h.shellDone(t, c.ID, "git --version")
	if ok.State != store.ChatShellExited || ok.ExitCode == nil || *ok.ExitCode != 0 || ok.EndedAt == nil || ok.PID != nil {
		t.Fatalf("git --version ended %+v", ok)
	}
	data, err := os.ReadFile(ok.OutputPath)
	if err != nil || !strings.Contains(string(data), "git version") || ok.OutputBytes != int64(len(data)) {
		t.Errorf("output = %q (%v), row says %d bytes", data, err, ok.OutputBytes)
	}
	if want := filepath.Join(h.dataDir, "transcripts"); !strings.HasPrefix(ok.OutputPath, want) {
		t.Errorf("output at %q, want it beside the chat's transcripts under %q", ok.OutputPath, want)
	}
	if ok.Truncated || ok.Attach || ok.AttachedTurn != nil || ok.AfterTurn != 0 {
		t.Errorf("fresh run = %+v", ok)
	}

	bad := h.shellDone(t, c.ID, "exit 3")
	if bad.State != store.ChatShellExited || bad.ExitCode == nil || *bad.ExitCode != 3 {
		t.Errorf("exit 3 ended %q with %v", bad.State, bad.ExitCode)
	}
	// A shell run holds no max_parallel_chats slot and never moves the chat.
	assertSlotFree(t, h)
}

func TestShellRunsInTheChatsWorkspace(t *testing.T) {
	h := newHarness(t)
	placed := h.withShell(t)
	c := h.chat(t)
	// `git rev-parse --show-toplevel` answers the directory it ran in.
	run := h.shellDone(t, c.ID, "git rev-parse --show-toplevel")
	data, _ := os.ReadFile(run.OutputPath)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	want, _ := filepath.EvalSymlinks(c.WorktreePath)
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Errorf("ran in %q, want the chat's worktree %q", got, want)
	}

	// A linked chat runs in its task's worktree and names the task, which is
	// what taskrun places in the task's container.
	task, linked := h.linkedTask(t, h.repo)
	h.shellDone(t, linked.ID, "git --version")
	if n := len(*placed); n != 2 || (*placed)[0] != 0 || (*placed)[1] != task.ID {
		t.Errorf("placements = %v, want [0 %d]", *placed, task.ID)
	}
}

func TestShellRefusals(t *testing.T) {
	h := newHarness(t)
	h.withShell(t)
	c := h.chat(t)

	first, err := h.runner.RunShell(t.Context(), c.ID, "sleep 30")
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if _, err := h.runner.RunShell(t.Context(), c.ID, "git --version"); !errors.Is(err, store.ErrChatShellBusy) {
		t.Errorf("second command while one runs = %v, want ErrChatShellBusy", err)
	}
	if _, err := h.store.SetChatShellAttach(t.Context(), c.ID, first.ID, true); !errors.Is(err, store.ErrChatShellNotAttachable) {
		t.Errorf("attach on a running run = %v, want ErrChatShellNotAttachable", err)
	}
	// The chat's cancel reaches the command, with no turn to stop.
	if err := h.runner.Cancel(t.Context(), c.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := h.awaitShell(t, c.ID, first.ID); got.State != store.ChatShellCanceled || got.ExitCode != nil {
		t.Errorf("canceled run ended %q with %v", got.State, got.ExitCode)
	}

	for _, st := range []chatstate.State{chatstate.Archived, chatstate.Closed, chatstate.HandedOff} {
		dead := h.chat(t)
		if _, err := h.store.SetChatState(t.Context(), dead.ID, st); err != nil {
			t.Fatalf("SetChatState(%s): %v", st, err)
		}
		if _, err := h.runner.RunShell(t.Context(), dead.ID, "git --version"); !errors.Is(err, store.ErrInvalidChatAction) {
			t.Errorf("RunShell on a %s chat = %v, want ErrInvalidChatAction", st, err)
		}
	}
}

func TestShellTimeoutKillsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.withShell(t)
	h.runner.shellTimeout = 300 * time.Millisecond
	c := h.chat(t)
	began := time.Now()
	run := h.shellDone(t, c.ID, "sleep 60")
	if run.State != store.ChatShellTimedOut || run.ExitCode != nil {
		t.Errorf("run ended %q with %v, want timed_out", run.State, run.ExitCode)
	}
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the kill took %s; the command was left to finish", took)
	}
	data, _ := os.ReadFile(run.OutputPath)
	if !strings.Contains(string(data), "killed after") {
		t.Errorf("output %q does not say the command was killed", data)
	}
}

func TestShellStopOnShutdownIsInterrupted(t *testing.T) {
	h := newHarness(t)
	h.withShell(t)
	c := h.chat(t)
	run, err := h.runner.RunShell(t.Context(), c.ID, "sleep 60")
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	waitFor(t, func() bool {
		got, _ := h.store.GetChatShellRun(t.Context(), c.ID, run.ID)
		return got != nil && got.PID != nil
	})
	h.runner.Stop()
	got, err := h.store.GetChatShellRun(t.Context(), c.ID, run.ID)
	if err != nil || got.State != store.ChatShellInterrupted {
		t.Errorf("after shutdown the run is %+v (%v), want interrupted", got, err)
	}
}

// TestCappedFileMarksTheCap pins the output bound directly: a shell body a
// test may use (`git`, `exit`, `sleep`) cannot produce 256 KiB on every OS.
func TestCappedFileMarksTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	out, err := newCappedFile(path, ShellOutputCap)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 100<<10)
	for range 3 {
		if n, err := out.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v; a capped write must still report success", n, err)
		}
	}
	size, truncated := out.finish()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || size != int64(len(data)) || len(data) != ShellOutputCap+len(ShellTruncationMarker) {
		t.Errorf("stored %d bytes (row %d, truncated %v), want the cap plus the marker", len(data), size, truncated)
	}
	if !strings.HasSuffix(string(data), ShellTruncationMarker) {
		t.Errorf("the stored output does not end in the truncation marker")
	}

	small, _ := newCappedFile(filepath.Join(t.TempDir(), "s.log"), ShellOutputCap)
	_, _ = small.Write([]byte("ok\n"))
	if size, truncated := small.finish(); size != 3 || truncated {
		t.Errorf("small output = %d bytes, truncated %v", size, truncated)
	}
}

func TestRecoverFinalizesARunningShellRun(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	run, err := h.store.CreateChatShellRun(t.Context(), c.ID, "sleep 600", time.Now())
	if err != nil {
		t.Fatalf("CreateChatShellRun: %v", err)
	}
	// A journal that names this very test process under an identity that is
	// not its own: the PID-reuse guard must refuse to kill it.
	pid, ident := os.Getpid(), "not-this-process"
	run.PID, run.ProcIdentity = &pid, &ident
	if err := h.store.UpdateChatShellRun(t.Context(), run); err != nil {
		t.Fatalf("UpdateChatShellRun: %v", err)
	}
	if err := h.runner.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, err := h.store.GetChatShellRun(t.Context(), c.ID, run.ID)
	if err != nil || got.State != store.ChatShellInterrupted || got.EndedAt == nil || got.PID != nil {
		t.Errorf("recovered run = %+v (%v), want interrupted", got, err)
	}
	// And the chat can run another: the old row no longer counts as running.
	h.withShell(t)
	h.shellDone(t, c.ID, "git --version")
}

// readPrompts is everything the fake CLI was handed so far: echo-prompt
// writes one JSON line per invocation, the preamble's block first.
func readPrompts(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prompts: %v", err)
	}
	var sb strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		// A prompt with a preamble is an array of blocks; one without is
		// the message alone.
		var blocks []string
		var single string
		if err := json.Unmarshal([]byte(line), &blocks); err != nil {
			if err := json.Unmarshal([]byte(line), &single); err != nil {
				t.Fatalf("prompt line %q: %v", line, err)
			}
			blocks = []string{single}
		}
		sb.WriteString(strings.Join(blocks, "\n") + "\n")
	}
	return sb.String()
}

func TestAttachedShellOutputReachesExactlyOneTurn(t *testing.T) {
	h := newHarness(t)
	h.withShell(t)
	promptFile := filepath.Join(t.TempDir(), "prompts")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	c := h.chat(t)

	unmarked := h.shellDone(t, c.ID, "git --version")
	marked := h.shellDone(t, c.ID, "exit 4")
	if _, err := h.store.SetChatShellAttach(t.Context(), c.ID, marked.ID, true); err != nil {
		t.Fatalf("attach: %v", err)
	}

	first := h.sendAndWait(t, c.ID, "what went wrong?")
	if first.Prompt != "what went wrong?" {
		t.Errorf("the turn row holds %q, want the message as typed", first.Prompt)
	}
	seen := readPrompts(t, promptFile)
	if n := strings.Count(seen, `<shell-run command="exit 4" state="exited" exit-code="4">`); n != 1 {
		t.Fatalf("the first turn saw the marked run %d time(s):\n%s", n, seen)
	}
	if strings.Contains(seen, "git --version") {
		t.Errorf("an unmarked run reached the agent:\n%s", seen)
	}
	got, _ := h.store.GetChatShellRun(t.Context(), c.ID, marked.ID)
	if got.Attach || got.AttachedTurn == nil || *got.AttachedTurn != first.Seq {
		t.Errorf("taken run = attach %v, attached_turn %v, want cleared and turn %d", got.Attach, got.AttachedTurn, first.Seq)
	}
	if other, _ := h.store.GetChatShellRun(t.Context(), c.ID, unmarked.ID); other.AttachedTurn != nil {
		t.Errorf("the unmarked run was taken by turn %d", *other.AttachedTurn)
	}
	if _, err := h.store.SetChatShellAttach(t.Context(), c.ID, marked.ID, true); !errors.Is(err, store.ErrChatShellNotAttachable) {
		t.Errorf("re-attaching a taken run = %v, want ErrChatShellNotAttachable", err)
	}

	h.sendAndWait(t, c.ID, "and now?")
	seen = readPrompts(t, promptFile)
	if n := strings.Count(seen, "<shell-run "); n != 1 {
		t.Errorf("after a second turn the agent has seen %d shell blocks, want 1", n)
	}
	// A run started now is placed after the two turns that exist.
	if late := h.shellDone(t, c.ID, "git --version"); late.AfterTurn != 2 {
		t.Errorf("after_turn = %d, want 2", late.AfterTurn)
	}
}
