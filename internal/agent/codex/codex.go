// Package codex implements the agent adapter for the Codex CLI: headless
// `codex exec --json` runs with JSONL event parsing, model/effort
// passthrough, and tree-kill support (spec §9.3; T2.9). Invocation and
// stream shapes are pinned against codex-cli 0.142.5.
package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
)

// binaryName is what PATH resolution looks for when no path is configured.
const binaryName = "codex"

// versionTimeout bounds the --version probe subprocess. 20 s rather than 10 s
// because the probe that this bound actually killed was this one, at the logon
// after a reboot (T4.22).
const versionTimeout = 20 * time.Second

// Adapter runs agents through the Codex CLI.
type Adapter struct {
	// pathFn returns the configured binary path; "" resolves from PATH.
	// A func rather than a value so config hot-reload reaches future runs.
	pathFn func() string
}

// New returns the Codex adapter. pathFn returns the configured binary path
// ("" = resolve "codex" from PATH); nil means always resolve from PATH.
func New(pathFn func() string) *Adapter {
	if pathFn == nil {
		pathFn = func() string { return "" }
	}
	return &Adapter{pathFn: pathFn}
}

// Name implements agent.Adapter.
func (a *Adapter) Name() string { return "codex" }

// Path implements agent.Adapter: the resolved binary, no subprocess.
func (a *Adapter) Path() (string, error) { return a.resolvePath() }

// resolvePath returns the binary to execute: the configured path when set,
// otherwise "codex" from PATH.
func (a *Adapter) resolvePath() (string, error) {
	if p := a.pathFn(); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("configured codex path %s: %w", p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath(binaryName)
	if err != nil {
		return "", fmt.Errorf("codex not found on PATH: %w", err)
	}
	return p, nil
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Detect implements agent.Adapter: path resolution, a --version probe
// (`codex-cli 0.142.5`), and a `login status` probe for logged_in (task 005).
// SupportsInput is permanently false: `codex exec` is strictly
// non-interactive once started (spec §9.3).
func (a *Adapter) Detect(ctx context.Context) (agent.Availability, error) {
	path, err := a.resolvePath()
	if err != nil {
		return agent.Availability{Error: err.Error()}, nil
	}
	out, _, err := agent.Probe(ctx, versionTimeout, path, "--version")
	if err != nil {
		return agent.Availability{
			Path:  path,
			Error: fmt.Sprintf("codex --version failed: %v", err),
		}, nil
	}
	raw := strings.TrimSpace(string(out))
	version := versionRe.FindString(raw)
	if version == "" {
		version = raw
	}
	return agent.Availability{
		Found:          true,
		Path:           path,
		Version:        version,
		LoggedIn:       a.loggedIn(ctx, path),
		VersionVerdict: versionVerdict(version),
		TestedVersions: agent.TestedVersionList(testedVersions),
	}, nil
}

// loggedIn probes `codex login status`. nil means "could not tell" — the §9.5
// contract — and is what a failure to *run* the probe reports, so a transient
// error never masquerades as a definite "not authenticated".
//
// Built deliberately as a copy of cursor's probe rather than a shared helper:
// the two CLIs answer with different words, and the only thing they have in
// common is the layering, which is a rule rather than code. Non-zero exit is
// false, an explicit negative is false, an explicit positive is true, and
// anything else is unknown rather than guessed. The logged-out wording is not
// fixture-verified — probing it means signing the developer out — so the
// unknown leg is load-bearing, not defensive.
//
// The timeout leg is not optional (T4.22): a probe killed on Windows exits 1,
// because the deadline is a TerminateProcess(pid, 1), and the exit-status
// branch below would then read a cold machine as a definite "not
// authenticated" — a false accusation against a logged-in account, on the one
// morning the user is least able to argue with it.
func (a *Adapter) loggedIn(ctx context.Context, path string) *bool {
	out, errOut, err := agent.Probe(ctx, versionTimeout, path, "login", "status")
	no, yes := false, true
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &no // ran and refused: a definite answer
		}
		return nil // could not run it at all
	}
	// Both streams, for the same reason cursor reads both: which one carries
	// the sentence is not something to assume about an unverified wording.
	text := strings.ToLower(string(out) + string(errOut))
	switch {
	case strings.Contains(text, "not logged in"), strings.Contains(text, "not authenticated"):
		return &no
	case strings.Contains(text, "logged in"), strings.Contains(text, "authenticated"):
		return &yes
	}
	return nil
}

// buildArgs assembles the pinned CLI invocation (spec §9.3, verified against
// codex-cli 0.142.5). Full-auto is the documented automation switch;
// restricted confines writes to the worktree — note that a `git commit` from
// a restricted step may be denied in a linked worktree, whose real git dir
// lives under the main repo (vincent itself never needs commits). The prompt
// is never an argv element: with stdin piped and no prompt argument, codex
// reads the instructions from stdin.
func buildArgs(spec agent.RunSpec) []string {
	args := []string{"exec", "--json"}
	if spec.PermissionMode == agent.Restricted {
		args = append(args, "--sandbox", "workspace-write")
	} else {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+spec.Effort)
	}
	return args
}

// Start implements agent.Adapter. The prompt is written via stdin (Windows
// argv limit); the process tree is killed when ctx is canceled. The caller
// must consume Events() until closed; Wait blocks on stream end.
func (a *Adapter) Start(ctx context.Context, spec agent.RunSpec) (agent.RunHandle, error) {
	path, err := a.resolvePath()
	if err != nil {
		return nil, err
	}
	//nolint:gosec // path comes from config or PATH resolution by design.
	cmd := exec.Command(path, buildArgs(spec)...)
	cmd.Dir = spec.WorkDir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	cmd.Stdin = strings.NewReader(spec.Prompt)
	stderr := &tailWriter{max: 64 * 1024}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}
	r := &run{
		cmd:        cmd,
		proc:       proc,
		stderr:     stderr,
		events:     make(chan agent.Event, 64),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
	}
	go r.readLoop(stdout)
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Kill()
		case <-r.procDone:
		}
	}()
	return r, nil
}

// run is a live Codex process.
type run struct {
	cmd    *exec.Cmd
	proc   *procx.Proc
	stderr *tailWriter

	events     chan agent.Event
	readerDone chan struct{}
	procDone   chan struct{}

	mu       sync.Mutex
	terminal *agent.RunResult // assembled from turn.completed / turn.failed
	// streamErr is readLoop's own reader failing, latched for Wait. It is
	// vincent losing the stream, not the CLI failing, and Wait says so with
	// agent.FailureStreamError rather than letting the exit code speak for a
	// transcript that is missing lines (#139).
	streamErr error

	waitOnce sync.Once
	waitRes  agent.RunResult
	waitErr  error
}

// maxLineBytes bounds one JSONL line; command items embed aggregated output,
// so lines can be large.
const maxLineBytes = 16 * 1024 * 1024

func (r *run) readLoop(rd io.Reader) {
	defer close(r.readerDone)
	defer close(r.events)
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	st := &stream{}
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		ev := st.parse(line)
		if ev.Type == agent.EventResult && ev.Result != nil {
			r.mu.Lock()
			res := *ev.Result
			r.terminal = &res
			r.mu.Unlock()
		}
		r.events <- ev
	}
	// A reader that stopped before the stream did leaves the CLI writing into
	// a pipe nobody empties, which would hang it until the step timeout.
	// Drain it, then let Wait name the failure — normalization ended early,
	// so the transcript is missing lines the CLI wrote (#139).
	if err := sc.Err(); err != nil {
		r.mu.Lock()
		r.streamErr = err
		r.mu.Unlock()
		_, _ = io.Copy(io.Discard, rd)
	}
}

// Events implements agent.RunHandle.
func (r *run) Events() <-chan agent.Event { return r.events }

// Respond implements agent.RunHandle. `codex exec` is strictly
// non-interactive — no mid-run input channel exists (spec §9.3), so there is
// never a pending request to answer.
func (r *run) Respond(agent.InputResponse) error {
	return errors.New("codex exec is non-interactive; mid-run input is not supported")
}

// Kill implements agent.RunHandle: terminates the whole process tree.
func (r *run) Kill() error { return r.proc.Kill() }

// Terminate implements agent.RunHandle: asks the tree to exit (spec §6).
func (r *run) Terminate() error { return r.proc.Terminate() }

// PID implements agent.RunHandle.
func (r *run) PID() int { return r.cmd.Process.Pid }

// Wait implements agent.RunHandle. It blocks until the stream is fully
// consumed and the process has exited, then assembles the RunResult per
// §7.1: the terminal turn event plus the exit code.
//
// RunResult.Failure is deliberately left nil (task 003): codex's usage-limit
// and unauthenticated wordings are not fixture-verified, and this adapter
// ships no guess in their place. A quota stop here therefore reads exactly as
// it did before task 003 — nonzero_exit or agent_error — rather than as an
// invented match. Adding it later is a `classify` beside this call and a
// `usage-limit` case in cmd/fakeagent's codex dialect, which is already there
// and already asserted to produce today's behaviour.
func (r *run) Wait() (agent.RunResult, error) {
	r.waitOnce.Do(func() {
		<-r.readerDone
		err := r.cmd.Wait()
		close(r.procDone)
		r.proc.Release()

		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			r.waitErr = fmt.Errorf("wait for codex: %w", err)
		}
		res := agent.RunResult{ExitCode: r.cmd.ProcessState.ExitCode()}
		r.mu.Lock()
		terminal, streamErr := r.terminal, r.streamErr
		r.mu.Unlock()
		if terminal != nil {
			res.IsError = terminal.IsError
			res.ErrorMessage = terminal.ErrorMessage
			res.ResultText = terminal.ResultText
			res.InputTokens = terminal.InputTokens
			res.OutputTokens = terminal.OutputTokens
			// CostUSD stays nil: codex does not report cost (spec §9.3).
		} else {
			res.IsError = true
			res.ErrorMessage = "stream ended without a result event"
			if tail := r.stderr.String(); tail != "" {
				res.ErrorMessage += ": " + tail
			}
		}
		// A stream vincent could not read to the end outranks whatever the
		// exit code says: the run may well have finished cleanly, but the
		// record of it is missing lines, and §7.1 success is a claim about
		// both (#139).
		if streamErr != nil {
			res.IsError = true
			res.ErrorMessage = "agent stream capture failed: " + streamErr.Error()
			res.Failure = &agent.Failure{Kind: agent.FailureStreamError}
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

// Argv implements agent.RunHandle: the command line actually spawned.
func (r *run) Argv() []string { return r.cmd.Args }
