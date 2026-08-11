package cursor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
)

// binaryName is what PATH resolution looks for when no path is configured.
//
// It is `cursor-agent`, never `cursor`: `cursor` on PATH is the editor
// launcher, and resolving it would spawn a GUI instead of an agent and hang
// the step until its timeout (spec §9.7).
const binaryName = "cursor-agent"

// defaultModel is what an unresolved §8.6 model becomes. Cursor persists the
// selected model in its own config, so omitting --model would mean "whatever
// the last invocation chose" — possibly a previous vincent step. Passing it
// always is what makes a run reproducible (spec §9.7).
const defaultModel = "auto"

// versionTimeout bounds the --version and status probe subprocesses.
const versionTimeout = 10 * time.Second

// Adapter runs agents through the Cursor CLI.
type Adapter struct {
	// pathFn returns the configured binary path; "" resolves from PATH.
	// A func rather than a value so config hot-reload reaches future runs.
	pathFn func() string
}

// New returns the Cursor adapter. pathFn returns the configured binary path
// ("" = resolve "cursor-agent" from PATH); nil means always resolve from PATH.
func New(pathFn func() string) *Adapter {
	if pathFn == nil {
		pathFn = func() string { return "" }
	}
	return &Adapter{pathFn: pathFn}
}

// Name implements agent.Adapter. The adapter — and therefore the workflow
// `agent:` value and the config key — is "cursor"; only the binary it spawns
// is "cursor-agent".
func (a *Adapter) Name() string { return "cursor" }

// Path implements agent.Adapter: the resolved binary, no subprocess.
func (a *Adapter) Path() (string, error) { return a.resolvePath() }

// resolvePath returns the binary to execute: the configured path when set,
// otherwise "cursor-agent" from PATH.
func (a *Adapter) resolvePath() (string, error) {
	if p := a.pathFn(); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("configured cursor path %s: %w", p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath(binaryName)
	if err != nil {
		return "", fmt.Errorf("cursor-agent not found on PATH: %w", err)
	}
	return p, nil
}

// Detect implements agent.Adapter: path resolution, a --version probe, and a
// `status` probe for logged_in — cursor is the only adapter that can answer
// that cheaply, and "installed but unauthenticated" otherwise looks identical
// to healthy right up until every run fails (spec §9.5, §9.7).
//
// SupportsInput is permanently false: cursor-agent has no input-format flag
// and no control channel, so a started run is non-interactive (spec §9.7).
func (a *Adapter) Detect(ctx context.Context) (agent.Availability, error) {
	path, err := a.resolvePath()
	if err != nil {
		return agent.Availability{Error: err.Error()}, nil
	}
	vctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(vctx, path, "--version").Output()
	if err != nil {
		return agent.Availability{
			Path:  path,
			Error: fmt.Sprintf("cursor-agent --version failed: %v", err),
		}, nil
	}
	// Recorded verbatim: the version is calver plus a commit sha
	// (2026.08.04-aaa8809), not semver. No gate parses it, and the sha is
	// part of the binary's identity (spec §9.7).
	return agent.Availability{
		Found:    true,
		Path:     path,
		Version:  strings.TrimSpace(string(out)),
		LoggedIn: a.loggedIn(ctx, path),
	}, nil
}

// loggedIn probes `cursor-agent status`. nil means "could not tell" — the
// §9.5 contract — and is what a failure to run the probe reports, so a
// transient error never masquerades as a definite "not authenticated".
//
// The logged-out wording is not fixture-verified (probing it would require
// signing the developer out), so the parse is deliberately layered: a
// non-zero exit is false, an explicit negative is false, an explicit positive
// is true, and anything else is unknown rather than guessed. T5.7 pins the
// logged-out leg by hand.
func (a *Adapter) loggedIn(ctx context.Context, path string) *bool {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "status").CombinedOutput()
	no, yes := false, true
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &no // ran and refused: a definite answer
		}
		return nil // could not run it at all
	}
	text := strings.ToLower(string(out))
	switch {
	case strings.Contains(text, "not logged in"), strings.Contains(text, "not authenticated"):
		return &no
	case strings.Contains(text, "logged in"):
		return &yes
	}
	return nil
}

// sandboxAvailable reports whether cursor's sandbox can run on this OS.
// `--sandbox enabled` requires macOS or Linux; on Windows the CLI exits 1
// with "Sandbox mode is enabled but not available on this system" before
// doing any work at all (verified against 2026.08.04-aaa8809).
//
// A var rather than a constant so tests exercise both legs on any host.
var sandboxAvailable = runtime.GOOS != "windows"

// SetSandboxAvailable overrides the platform capability and returns a restore
// func. It exists so tests in *other* packages — the engine's classification
// of the refusal, the M5 gate — can exercise both legs on every OS CI runs,
// instead of the Windows leg being the only one ever executed. Production
// code never calls it.
func SetSandboxAvailable(v bool) (restore func()) {
	prev := sandboxAvailable
	sandboxAvailable = v
	return func() { sandboxAvailable = prev }
}

// errRestricted is returned instead of starting a restricted step on a
// platform where cursor cannot restrict anything. It wraps the shared
// agent.ErrRestrictedUnsupported so the engine can classify it (as
// restricted_unsupported rather than agent_unavailable — the CLI is installed
// and fine, it just cannot restrict here) without knowing this package
// exists.
//
// The alternative — quietly falling back to --force — would run a step
// full-auto that explicitly asked not to be, turning a §9.4 safety choice
// into its opposite on one OS. Failing is the only honest option: the step
// fails under the retry policy with a stated reason, rather than succeeding
// with permissions nobody granted (spec §9.7).
var errRestricted = fmt.Errorf(
	"cursor restricted mode needs the CLI sandbox, which requires macOS or Linux; "+
		"refusing to silently run the step full-auto instead (spec §9.7): %w",
	agent.ErrRestrictedUnsupported)

// buildArgs assembles the pinned CLI invocation (spec §9.7, verified against
// cursor-agent 2026.08.04-aaa8809).
//
// --trust rides in both permission modes: every task runs in a git worktree
// the CLI has never seen, and a workspace-trust prompt in a headless run is a
// hang rather than a question. Cursor's own --worktree flag is never passed —
// worktrees belong to vincent (§10). The prompt is never an argv element:
// with stdin piped and no prompt argument, cursor-agent reads it from stdin.
func buildArgs(spec agent.RunSpec) ([]string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--trust"}
	if spec.PermissionMode == agent.Restricted {
		if !sandboxAvailable {
			return nil, errRestricted
		}
		args = append(args, "--sandbox", "enabled")
	} else {
		args = append(args, "--force")
	}
	model := spec.Model
	if model == "" {
		model = defaultModel
	}
	args = append(args, "--model", model)
	// spec.Effort is deliberately dropped: cursor has no effort flag, and
	// reasoning depth is selected through the model id (spec §9.7).
	return args, nil
}

// Start implements agent.Adapter. The prompt is written via stdin (Windows
// argv limit); the process tree is killed when ctx is canceled. The caller
// must consume Events() until closed; Wait blocks on stream end.
func (a *Adapter) Start(ctx context.Context, spec agent.RunSpec) (agent.RunHandle, error) {
	path, err := a.resolvePath()
	if err != nil {
		return nil, err
	}
	args, err := buildArgs(spec)
	if err != nil {
		return nil, err
	}
	//nolint:gosec // path comes from config or PATH resolution by design.
	cmd := exec.Command(path, args...)
	cmd.Dir = spec.WorkDir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	cmd.Stdin = strings.NewReader(spec.Prompt)
	stderr := &tailWriter{max: 64 * 1024}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cursor stdout pipe: %w", err)
	}
	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start cursor-agent: %w", err)
	}
	r := &run{
		cmd:        cmd,
		proc:       proc,
		stderr:     stderr,
		events:     make(chan agent.Event, 64),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
	}
	go r.readLoop(bufio.NewScanner(stdout))
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Kill()
		case <-r.procDone:
		}
	}()
	return r, nil
}

// run is a live cursor-agent process.
type run struct {
	cmd    *exec.Cmd
	proc   *procx.Proc
	stderr *tailWriter

	events     chan agent.Event
	readerDone chan struct{}
	procDone   chan struct{}

	mu       sync.Mutex
	terminal *agent.RunResult // assembled from the result event

	waitOnce sync.Once
	waitRes  agent.RunResult
	waitErr  error
}

// maxLineBytes bounds one stream line; tool_call payloads embed file contents
// and command output, so lines can be large.
const maxLineBytes = 16 * 1024 * 1024

func (r *run) readLoop(sc *bufio.Scanner) {
	defer close(r.readerDone)
	defer close(r.events)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		ev := parse(line)
		if ev.Type == agent.EventResult && ev.Result != nil {
			r.mu.Lock()
			res := *ev.Result
			r.terminal = &res
			r.mu.Unlock()
		}
		r.events <- ev
	}
	// A scanner error (over-long line, pipe error) ends normalization; the
	// process itself is still waited on and its exit code judged.
}

// Events implements agent.RunHandle.
func (r *run) Events() <-chan agent.Event { return r.events }

// Respond implements agent.RunHandle. cursor-agent -p is non-interactive —
// no mid-run input channel exists (spec §9.7), so there is never a pending
// request to answer.
func (r *run) Respond(agent.InputResponse) error {
	return errors.New("cursor-agent is non-interactive; mid-run input is not supported")
}

// Kill implements agent.RunHandle: terminates the whole process tree.
func (r *run) Kill() error { return r.proc.Kill() }

// Terminate implements agent.RunHandle: asks the tree to exit (spec §6).
func (r *run) Terminate() error { return r.proc.Terminate() }

// PID implements agent.RunHandle.
func (r *run) PID() int { return r.cmd.Process.Pid }

// Wait implements agent.RunHandle. It blocks until the stream is fully
// consumed and the process has exited, then assembles the RunResult per §7.1.
//
// The no-result path carries the stderr tail because on cursor it is the
// everyday failure, not a defensive corner: an invalid model id exits 1 with
// `ActionRequiredError: … Model name is not valid` on stderr and emits no
// result event at all (spec §9.7).
func (r *run) Wait() (agent.RunResult, error) {
	r.waitOnce.Do(func() {
		<-r.readerDone
		err := r.cmd.Wait()
		close(r.procDone)
		r.proc.Release()

		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			r.waitErr = fmt.Errorf("wait for cursor-agent: %w", err)
		}
		res := agent.RunResult{ExitCode: r.cmd.ProcessState.ExitCode()}
		r.mu.Lock()
		terminal := r.terminal
		r.mu.Unlock()
		if terminal != nil {
			res.IsError = terminal.IsError
			res.ErrorMessage = terminal.ErrorMessage
			res.ResultText = terminal.ResultText
			res.InputTokens = terminal.InputTokens
			res.OutputTokens = terminal.OutputTokens
			// CostUSD stays nil: cursor does not report cost (spec §9.7).
		} else {
			res.IsError = true
			res.ErrorMessage = "stream ended without a result event"
			if tail := r.stderr.String(); tail != "" {
				res.ErrorMessage += ": " + tail
			}
		}
		r.waitRes = res
	})
	return r.waitRes, r.waitErr
}

// tailWriter keeps the last max bytes written — enough stderr for error
// reporting without unbounded growth.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.buf))
}
