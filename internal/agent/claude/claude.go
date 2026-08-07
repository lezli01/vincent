// Package claude implements the agent adapter for the Claude Code CLI:
// headless `claude -p` runs with stream-json event parsing, model/effort
// passthrough, an ad-hoc --help options probe, and tree-kill support
// (spec §9.2; T1.7).
package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
)

// binaryName is what PATH resolution looks for when no path is configured.
const binaryName = "claude"

// restrictedTools is the --allowedTools value for restricted mode: file
// tools plus git — deliberately no test runners, which execute arbitrary
// project code anyway (T1.7 decision). Denied actions degrade per §9.4
// until T2.12 turns them into permission requests.
const restrictedTools = "Read,Glob,Grep,Edit,Write,MultiEdit,Bash(git:*)"

// Probe timeouts bound the --version and --help subprocesses.
const (
	versionTimeout = 10 * time.Second
	helpTimeout    = 15 * time.Second
)

// Adapter runs agents through the Claude Code CLI.
type Adapter struct {
	// pathFn returns the configured binary path; "" resolves from PATH.
	// A func rather than a value so config hot-reload reaches future runs.
	pathFn func() string
}

// New returns the Claude adapter. pathFn returns the configured binary path
// ("" = resolve "claude" from PATH); nil means always resolve from PATH.
func New(pathFn func() string) *Adapter {
	if pathFn == nil {
		pathFn = func() string { return "" }
	}
	return &Adapter{pathFn: pathFn}
}

// Name implements agent.Adapter.
func (a *Adapter) Name() string { return "claude" }

// resolvePath returns the binary to execute: the configured path when set,
// otherwise "claude" from PATH.
func (a *Adapter) resolvePath() (string, error) {
	if p := a.pathFn(); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("configured claude path %s: %w", p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath(binaryName)
	if err != nil {
		return "", fmt.Errorf("claude not found on PATH: %w", err)
	}
	return p, nil
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Detect implements agent.Adapter: path resolution plus a --version probe.
// logged_in stays unknown in v1 — there is no cheap documented probe, and
// state-file parsing would be an unpinned surface (T1.7 decision).
// SupportsInput is false until T2.12 lands the input engine.
func (a *Adapter) Detect(ctx context.Context) (agent.Availability, error) {
	path, err := a.resolvePath()
	if err != nil {
		return agent.Availability{Error: err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return agent.Availability{
			Path:  path,
			Error: fmt.Sprintf("claude --version failed: %v", err),
		}, nil
	}
	raw := strings.TrimSpace(string(out))
	version := versionRe.FindString(raw)
	if version == "" {
		version = raw
	}
	return agent.Availability{Found: true, Path: path, Version: version}, nil
}

// buildArgs assembles the pinned CLI invocation (spec §9.2, verified against
// 2.1.x): message-level stream-json (no --include-partial-messages, T1.7
// decision), permission-mode flags, model/effort passthrough. The prompt is
// never an argv element.
func buildArgs(spec agent.RunSpec) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.PermissionMode == agent.Restricted {
		args = append(args, "--allowedTools", restrictedTools)
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
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
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
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

// run is a live Claude Code process.
type run struct {
	cmd    *exec.Cmd
	proc   *procx.Proc
	stderr *tailWriter

	events     chan agent.Event
	readerDone chan struct{}
	procDone   chan struct{}

	mu       sync.Mutex
	terminal *agent.RunResult // parsed result event, if any

	waitOnce sync.Once
	waitRes  agent.RunResult
	waitErr  error
}

// maxLineBytes bounds one stream-json line; agent messages embed file
// contents, so lines can be large.
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
		ev := parseLine(line)
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

// Respond implements agent.RunHandle. Claude reports supports_input=false
// until T2.12 pins the bidirectional stream-json protocol, so there is never
// a pending request to answer.
func (r *run) Respond(agent.InputResponse) error {
	return errors.New("claude adapter does not support mid-run input yet (T2.12)")
}

// Kill implements agent.RunHandle: terminates the whole process tree.
func (r *run) Kill() error { return r.proc.Kill() }

// PID implements agent.RunHandle.
func (r *run) PID() int { return r.cmd.Process.Pid }

// Wait implements agent.RunHandle. It blocks until the stream is fully
// consumed and the process has exited, then assembles the RunResult per
// §7.1: the terminal result event plus the exit code.
func (r *run) Wait() (agent.RunResult, error) {
	r.waitOnce.Do(func() {
		<-r.readerDone
		err := r.cmd.Wait()
		close(r.procDone)
		r.proc.Release()

		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			r.waitErr = fmt.Errorf("wait for claude: %w", err)
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
			res.CostUSD = terminal.CostUSD
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
