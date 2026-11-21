package chatrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/transcript"
	"github.com/lezli01/vincent/internal/worktree"
)

// A chat's shell passthrough (task 170, spec §5.5).
//
// A human runs a command in the chat's workspace through the daemon — a
// client never runs it, because the daemon owns every process (the ownership
// invariant). It is not a turn: it holds no `max_parallel_chats` slot, it is
// legal beside a live turn, and its output reaches the agent only when the
// human marks the finished run for the next prompt. A chat's `restricted`
// permission mode restricts the agent, not this: the command runs as the
// daemon's user, exactly as one typed in the human's own terminal would.

// The two bounds of a shell run. They are fixed rather than configured: this
// is a quick look at the workspace, and anything longer or louder belongs in
// the human's own terminal.
const (
	// ShellTimeout is how long a command may run before its tree is killed
	// and the run ends `timed_out`.
	ShellTimeout = 2 * time.Minute
	// ShellOutputCap is how much combined stdout and stderr is stored. Past
	// it the output is dropped, the run is marked truncated and
	// ShellTruncationMarker ends the stored file.
	ShellOutputCap = 256 << 10
)

// ShellTruncationMarker is the last line of a stored output that hit
// ShellOutputCap, so the file says so wherever it is read — the output
// route, an export, the agent's context.
const ShellTruncationMarker = "\n[output truncated at 256 KiB]\n"

// shellWaitDelay bounds how long a finished or killed command's Wait may
// block on an output pipe a stray descendant still holds open.
const shellWaitDelay = 5 * time.Second

// errShellTimeout is the cancel cause of a run that hit ShellTimeout.
var errShellTimeout = errors.New("shell timeout")

// ErrNoShell is a shell command on a runner the daemon wired no shell into.
var ErrNoShell = errors.New("no shell is wired")

// ShellExec is a shell command resolved to a process to spawn: the argv, and
// the directory and environment of the host process. Stop, when set, signals
// the command inside a container before the host process is killed; it is
// nil on the host. internal/taskrun resolves it and the daemon wires it, so
// this package never imports taskrun.
type ShellExec struct {
	Argv []string
	Dir  string
	Env  []string
	Stop func()
}

// shellCtl cancels a chat's running shell command and reports when its
// goroutine has written the run's ending. cancel is set before the control
// is registered and never changes.
type shellCtl struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// RunShell starts command in the chat's workspace and returns its row,
// `running`. It refuses a terminal chat with store.ErrInvalidChatAction and
// a chat that already has a command running with store.ErrChatShellBusy.
//
// The row is written before anything is spawned (crash-first, §12.4), and
// the command runs on a goroutine of its own, never the chat's actor: a
// shell run and a turn are independent.
func (r *Runner) RunShell(ctx context.Context, chatID int64, command string) (*store.ChatShellRun, error) {
	// The cancel exists before the run is registered, so a stop that finds
	// the run can always cancel it (review F7): one landing before the
	// goroutine starts leaves it a canceled context, and it ends at once.
	runCtx, cancel := context.WithCancelCause(r.base)
	ctl := &shellCtl{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if r.shellHolds[chatID] > 0 {
		r.mu.Unlock()
		cancel(errCanceled)
		return nil, fmt.Errorf("chat %d is being archived, handed off or deleted: %w", chatID, store.ErrInvalidChatAction)
	}
	if r.shells[chatID] != nil {
		r.mu.Unlock()
		cancel(errCanceled)
		return nil, fmt.Errorf("chat %d: %w", chatID, store.ErrChatShellBusy)
	}
	r.shells[chatID] = ctl
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		if r.shells[chatID] == ctl {
			delete(r.shells, chatID)
		}
		r.mu.Unlock()
		cancel(errCanceled)
		close(ctl.done)
	}
	// The state is read only once the run is registered: a caller that ends
	// the chat holds it (HoldShell) and then changes its state, so a run
	// registered after that change sees the terminal state here, and one
	// registered before it is found and stopped by the holder.
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		release()
		return nil, err
	}
	if chatstate.Terminal(chat.State) {
		release()
		return nil, fmt.Errorf("chat %d: %w", chatID, store.ErrInvalidChatAction)
	}
	run, err := r.deps.Store.CreateChatShellRun(ctx, chatID, command, r.deps.Now())
	if err != nil {
		release()
		return nil, err
	}
	// The goroutine owns the row from here; the caller gets a copy.
	started := *run
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer release()
		r.runShell(runCtx, chat, run)
	}()
	return &started, nil
}

// HoldShell stops the chat's running shell command, if any, and refuses new
// ones until the returned release is called. An archive, handoff or delete
// holds it across the state change that ends the chat (review F7), so no
// command starts in a directory that is about to go or change hands. The
// release is safe to call more than once.
func (r *Runner) HoldShell(ctx context.Context, chatID int64) func() {
	r.mu.Lock()
	r.shellHolds[chatID]++
	r.mu.Unlock()
	r.StopShell(ctx, chatID)
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if r.shellHolds[chatID]--; r.shellHolds[chatID] <= 0 {
				delete(r.shellHolds, chatID)
			}
			r.mu.Unlock()
		})
	}
}

// ShellRunning reports whether this runner has a shell command running for
// the chat.
func (r *Runner) ShellRunning(chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shells[chatID] != nil
}

// StopShell cancels the chat's running shell command, if it has one, and
// waits until its ending is written. The run ends `canceled`.
func (r *Runner) StopShell(ctx context.Context, chatID int64) {
	ctl := r.cancelShell(chatID)
	if ctl == nil {
		return
	}
	select {
	case <-ctl.done:
	case <-ctx.Done():
	}
}

// cancelShell cancels the chat's running shell command without waiting, and
// returns its control, nil when there was none.
func (r *Runner) cancelShell(chatID int64) *shellCtl {
	r.mu.Lock()
	ctl := r.shells[chatID]
	r.mu.Unlock()
	if ctl != nil {
		ctl.cancel(errCanceled)
	}
	return ctl
}

// shellDir is where a chat's shell output lives: beside its transcripts, so
// a permanent delete of the chat removes it with them.
func (r *Runner) shellDir(chatID int64) string {
	return filepath.Join(r.deps.DataDir, "transcripts", worktree.ChatOwner(chatID).Dir(), "shell")
}

// runShell runs one command to its ending. It is the sole writer of the
// run's row apart from the attach mark.
func (r *Runner) runShell(ctx context.Context, chat *store.Chat, run *store.ChatShellRun) {
	fail := func(out *cappedFile, err error) {
		if out != nil {
			_, _ = fmt.Fprintf(out, "vincent: %v\n", err)
		}
		r.finishShell(run, out, store.ChatShellFailed, nil)
	}
	if ctx.Err() != nil {
		// Stopped before it started (review F7): nothing is spawned.
		state := store.ChatShellCanceled
		if r.base.Err() != nil {
			state = store.ChatShellInterrupted
		}
		r.finishShell(run, nil, state, nil)
		return
	}
	if err := os.MkdirAll(r.shellDir(chat.ID), 0o700); err != nil {
		fail(nil, err)
		return
	}
	run.OutputPath = filepath.Join(r.shellDir(chat.ID), fmt.Sprintf("%d.log", run.ID))
	out, err := newCappedFile(run.OutputPath, ShellOutputCap)
	if err != nil {
		run.OutputPath = ""
		fail(nil, err)
		return
	}
	if r.deps.Shell == nil {
		fail(out, ErrNoShell)
		return
	}
	// Where the chat's turns run (Workspace, and the task's container for a
	// linked chat on a containerized task), resolved per run because a
	// linked chat's directory is its task's claim.
	workDir, err := r.Workspace(ctx, chat)
	if err != nil {
		fail(out, err)
		return
	}
	var taskID int64
	if chat.Linked() {
		taskID = *chat.LinkedTaskID
	}
	ex, err := r.deps.Shell(ctx, taskID, run.ID, workDir, run.Command)
	if err != nil {
		fail(out, err)
		return
	}
	timeout := ShellTimeout
	if r.shellTimeout > 0 {
		timeout = r.shellTimeout
	}
	runCtx, cancel := context.WithTimeoutCause(ctx, timeout, errShellTimeout)
	defer cancel()

	cmd := exec.Command(ex.Argv[0], ex.Argv[1:]...) //nolint:gosec // the human's own command, by design (task 170)
	cmd.Dir, cmd.Env = ex.Dir, ex.Env
	// One writer for both streams: os/exec then serializes the writes, and
	// the stored file is the output in the order it was produced.
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = shellWaitDelay
	proc, err := procx.Start(cmd)
	if err != nil {
		fail(out, err)
		return
	}
	defer proc.Release()
	// Journal the pid and its identity before waiting on anything, so §12.4
	// recovery can prove and kill an orphaned tree.
	pid := cmd.Process.Pid
	run.PID = &pid
	if id, err := procx.Identity(pid); err == nil && id != "" {
		run.ProcIdentity = &id
	}
	if err := r.deps.Store.UpdateChatShellRun(r.persist, run); err != nil {
		r.deps.Logger.Warn("journal chat shell run", "run", run.ID, "error", err)
	}

	// Tree-kill on the timeout, a stop, or daemon shutdown. Inside the
	// container first, then the host process: killing only the runtime
	// client leaves the command running in there.
	waited := make(chan struct{})
	var killer sync.WaitGroup
	killer.Add(1)
	go func() {
		defer killer.Done()
		select {
		case <-runCtx.Done():
			if ex.Stop != nil {
				ex.Stop()
			}
			_ = proc.Kill()
		case <-waited:
		}
	}()
	waitErr := cmd.Wait()
	// Read before the deferred cancel can make every run look canceled.
	ended, cause := runCtx.Err() != nil, context.Cause(runCtx)
	close(waited)
	killer.Wait()

	var exitErr *exec.ExitError
	switch {
	case ended && r.base.Err() != nil:
		// The daemon went away under the command: interrupted, as a turn is.
		r.finishShell(run, out, store.ChatShellInterrupted, nil)
	case ended && errors.Is(cause, errShellTimeout):
		_, _ = fmt.Fprintf(out, "\nvincent: killed after %s\n", timeout)
		r.finishShell(run, out, store.ChatShellTimedOut, nil)
	case ended:
		r.finishShell(run, out, store.ChatShellCanceled, nil)
	case waitErr == nil, errors.As(waitErr, &exitErr), errors.Is(waitErr, exec.ErrWaitDelay):
		code := cmd.ProcessState.ExitCode()
		r.finishShell(run, out, store.ChatShellExited, &code)
	default:
		fail(out, waitErr)
	}
}

// finishShell closes the output and writes the run's ending. It persists on
// the context that outlives shutdown, as a turn's ending does.
func (r *Runner) finishShell(run *store.ChatShellRun, out *cappedFile, state string, exit *int) {
	if out != nil {
		run.OutputBytes, run.Truncated = out.finish()
	}
	now := r.deps.Now()
	run.State, run.ExitCode, run.EndedAt, run.PID = state, exit, &now, nil
	ctx := r.persist
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.deps.Store.UpdateChatShellRun(ctx, run); err != nil {
		r.deps.Logger.Error("finish chat shell run", "run", run.ID, "error", err)
	}
}

// cappedFile stores at most limit bytes of what is written to it and drops
// the rest, reporting every write as complete so the command never sees a
// short write or a closed pipe — a flood is bounded by ShellTimeout, not by
// breaking the command's stdout.
type cappedFile struct {
	mu        sync.Mutex
	f         *os.File
	n, limit  int64
	truncated bool
}

func newCappedFile(path string, limit int64) (*cappedFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // the path is this daemon's own, under the chat's data dir
	if err != nil {
		return nil, err
	}
	return &cappedFile{f: f, limit: limit}, nil
}

func (c *cappedFile) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - c.n
	keep := p
	if int64(len(keep)) > room {
		keep, c.truncated = keep[:room], true
	}
	if len(keep) > 0 && c.f != nil {
		n, _ := c.f.Write(keep)
		c.n += int64(n)
	}
	return len(p), nil
}

// finish marks a truncated output, closes the file and reports the stored
// size and whether the cap was reached.
func (c *cappedFile) finish() (size int64, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f == nil {
		return c.n, c.truncated
	}
	if c.truncated {
		n, _ := c.f.WriteString(ShellTruncationMarker)
		c.n += int64(n)
	}
	_ = c.f.Close()
	c.f = nil
	return c.n, c.truncated
}

// takeShellRuns hands the chat's marked shell runs to turn and returns the
// context block that rides in its preamble (task 170): the first turn to
// start takes them — a queued message's turn like any other — and no later
// turn sees them again. An unmarked run never reaches the agent. The block
// is part of RunSpec.Preamble, never the human's message, and the turn row
// keeps the message as typed.
func (r *Runner) takeShellRuns(ctx context.Context, chat *store.Chat, turn *store.ChatTurn, tr *transcript.Writer) string {
	runs, err := r.deps.Store.TakeChatShellRuns(ctx, chat.ID, turn.Seq)
	if err != nil {
		r.deps.Logger.Warn("chat shell runs not attached", "chat", chat.ID, "turn", turn.ID, "error", err)
		return ""
	}
	if len(runs) == 0 {
		return ""
	}
	block := ShellContext(runs)
	ids := make([]int64, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	tr.Note("shell_attached", map[string]any{"runs": ids, "bytes": len(block), "context": block})
	return block
}

// ShellContext renders shell runs as the block an agent is given: each
// command, how it ended, and its stored output.
func ShellContext(runs []*store.ChatShellRun) string {
	var sb strings.Builder
	sb.WriteString("The human ran the following in this conversation's workspace and attached the output to this message.\n")
	for _, run := range runs {
		fmt.Fprintf(&sb, "\n<shell-run command=%q state=%q", run.Command, run.State)
		if run.ExitCode != nil {
			fmt.Fprintf(&sb, " exit-code=\"%d\"", *run.ExitCode)
		}
		sb.WriteString(">\n")
		if run.OutputPath != "" {
			if data, err := os.ReadFile(run.OutputPath); err == nil && len(data) > 0 {
				sb.Write(data)
				if data[len(data)-1] != '\n' {
					sb.WriteByte('\n')
				}
			}
		}
		sb.WriteString("</shell-run>\n")
	}
	return sb.String()
}

// recoverShells finalizes the shell runs a previous daemon died under
// (§12.4): each is `interrupted`, and its process is killed only once the
// pid is proved to still hold it — the guard a turn's orphan gets.
func (r *Runner) recoverShells(ctx context.Context) error {
	runs, err := r.deps.Store.ListRunningChatShellRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		chat, err := r.deps.Store.GetChat(ctx, run.ChatID)
		if err != nil {
			return err
		}
		// Inside a task's container the host pid is only the runtime
		// client's; the command is reached through its pid file.
		if chat.Linked() && r.deps.StopShellOrphan != nil {
			r.deps.StopShellOrphan(ctx, *chat.LinkedTaskID, run.ID)
		}
		killOrphanPID(run.PID, run.ProcIdentity, r.deps.Logger)
		if run.OutputPath != "" {
			if fi, err := os.Stat(run.OutputPath); err == nil {
				run.OutputBytes = fi.Size()
			}
		}
		now := r.deps.Now()
		run.State, run.EndedAt, run.PID = store.ChatShellInterrupted, &now, nil
		if err := r.deps.Store.UpdateChatShellRun(ctx, run); err != nil {
			return fmt.Errorf("recover chat shell run %d: %w", run.ID, err)
		}
	}
	return nil
}
