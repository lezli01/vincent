// Package claude implements the agent adapter for the Claude Code CLI:
// headless `claude -p` runs with stream-json event parsing, model/effort
// passthrough, an ad-hoc --help options probe, and tree-kill support
// (spec §9.2; T1.7).
package claude

import (
	"bufio"
	"context"
	"encoding/json"
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
const binaryName = "claude"

// restrictedTools is the --allowedTools value for restricted mode: file
// tools plus git — deliberately no test runners, which execute arbitrary
// project code anyway (T1.7 decision). With input mode live, tools outside
// this list surface as §7.4 permission requests (subject to on_input);
// without it they degrade per §9.4 as before.
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

// Path implements agent.Adapter: the resolved binary, no subprocess.
func (a *Adapter) Path() (string, error) { return a.resolvePath() }

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
// SupportsInput is version-gated to the fixture-verified family (input.go).
func (a *Adapter) Detect(ctx context.Context) (agent.Availability, error) {
	path, err := a.resolvePath()
	if err != nil {
		return agent.Availability{Error: err.Error()}, nil
	}
	version, err := probeVersion(ctx, path)
	if err != nil {
		return agent.Availability{Path: path, Error: err.Error()}, nil
	}
	return agent.Availability{
		Found:         true,
		Path:          path,
		Version:       version,
		SupportsInput: supportsInput(version),
	}, nil
}

// probeVersion runs `claude --version` and extracts the semver, falling back
// to the raw output when no semver is found (an unparseable version never
// enables input mode — supportsInput rejects it).
func probeVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("claude --version failed: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if v := versionRe.FindString(raw); v != "" {
		return v, nil
	}
	return raw, nil
}

// buildArgs assembles the pinned CLI invocation (spec §9.2, verified against
// 2.1.x): message-level stream-json (no --include-partial-messages, T1.7
// decision), permission-mode flags, model/effort passthrough. The prompt is
// never an argv element. inputMode adds the §7.4 control-protocol flags
// (input.go) — enabled whenever the binary supports it, regardless of the
// on_input policy, since deny-mode auto-answers still need the stream.
func buildArgs(spec agent.RunSpec, inputMode bool) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.PermissionMode == agent.Restricted {
		args = append(args, "--allowedTools", restrictedTools)
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if inputMode {
		args = append(args, "--input-format", "stream-json", "--permission-prompt-tool", "stdio")
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
//
// When the installed CLI's version passes the §7.4 input gate, the run
// starts in input mode: control-protocol flags, prompt as a stream-json
// user message, stdin retained for Respond. A failed version probe degrades
// to the plain invocation — never a run failure.
func (a *Adapter) Start(ctx context.Context, spec agent.RunSpec) (agent.RunHandle, error) {
	path, err := a.resolvePath()
	if err != nil {
		return nil, err
	}
	inputMode := false
	if version, verr := probeVersion(ctx, path); verr == nil {
		inputMode = supportsInput(version)
	}
	//nolint:gosec // path comes from config or PATH resolution by design.
	cmd := exec.Command(path, buildArgs(spec, inputMode)...)
	cmd.Dir = spec.WorkDir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	var stdin io.WriteCloser
	var promptLine []byte
	if inputMode {
		promptLine, err = userMessageLine(spec.Prompt)
		if err != nil {
			return nil, err
		}
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("claude stdin pipe: %w", err)
		}
	} else {
		cmd.Stdin = strings.NewReader(spec.Prompt)
	}
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
		stdin:      stdin,
		inputMode:  inputMode,
		events:     make(chan agent.Event, 64),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
	}
	if inputMode {
		if _, err := stdin.Write(promptLine); err != nil {
			_ = r.Kill()
			return nil, fmt.Errorf("write prompt to claude: %w", err)
		}
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

	// Input mode (spec §7.4): stdin is retained for control_response writes
	// and closed when the terminal result arrives (the CLI otherwise waits
	// for the next user turn) or the stream ends.
	inputMode bool
	stdin     io.WriteCloser
	stdinOnce sync.Once
	stdinMu   sync.Mutex // serializes control writes

	events     chan agent.Event
	readerDone chan struct{}
	procDone   chan struct{}

	mu       sync.Mutex
	terminal *agent.RunResult // parsed result event, if any
	pending  *pendingRequest  // at most one (spec §7.4); guarded by mu

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
	defer r.closeStdin()
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		ev := r.parseStreamLine(line)
		if ev.Type == agent.EventResult && ev.Result != nil {
			r.mu.Lock()
			res := *ev.Result
			r.terminal = &res
			r.mu.Unlock()
			// Single-turn semantics: with stdin retained the CLI would wait
			// for another user message after the result — end the session.
			r.closeStdin()
		}
		r.events <- ev
	}
	// A scanner error (over-long line, pipe error) ends normalization; the
	// process itself is still waited on and its exit code judged.
}

// parseStreamLine handles the control-protocol lines that need run state
// (pending-request tracking) and defers everything else to parseLine.
func (r *run) parseStreamLine(line []byte) agent.Event {
	var head struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: line}
	}
	switch head.Type {
	case "control_request":
		ev, pend := parseControlRequest(line)
		if pend == nil {
			return ev // protocol error: Request is nil, engine fails the attempt
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.pending != nil {
			// Requests are serial (spec §7.4); a second concurrent request
			// breaks the contract — fail fast rather than mis-route answers.
			return agent.Event{
				Type:    agent.EventInputRequest,
				Message: "control_request while another is pending",
				Raw:     line,
			}
		}
		r.pending = pend
		return ev
	case "control_cancel_request":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.pending != nil && r.pending.id == head.RequestID {
			r.pending = nil
			return agent.Event{Type: agent.EventInputCanceled, Raw: line}
		}
		return agent.Event{Type: agent.EventUnknown, Raw: line}
	default:
		return parseLine(line)
	}
}

// closeStdin closes the retained input-mode stdin exactly once; a no-op for
// plain runs.
func (r *run) closeStdin() {
	if r.stdin == nil {
		return
	}
	r.stdinOnce.Do(func() { _ = r.stdin.Close() })
}

// Events implements agent.RunHandle.
func (r *run) Events() <-chan agent.Event { return r.events }

// Respond implements agent.RunHandle: translates the answer onto the wire
// (input.go) and writes it to the live process. The pending slot clears only
// after a successful write, so a failed write stays answerable.
func (r *run) Respond(resp agent.InputResponse) error {
	if !r.inputMode {
		return errors.New("claude run started without input support")
	}
	r.mu.Lock()
	pend := r.pending
	r.mu.Unlock()
	if pend == nil {
		return errors.New("no pending input request")
	}
	line, err := buildControlResponse(pend, resp)
	if err != nil {
		return err
	}
	r.stdinMu.Lock()
	_, werr := r.stdin.Write(line)
	r.stdinMu.Unlock()
	if werr != nil {
		return fmt.Errorf("write control response: %w", werr)
	}
	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
	return nil
}

// Kill implements agent.RunHandle: terminates the whole process tree.
func (r *run) Kill() error { return r.proc.Kill() }

// Terminate implements agent.RunHandle: asks the tree to exit (spec §6).
func (r *run) Terminate() error { return r.proc.Terminate() }

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
