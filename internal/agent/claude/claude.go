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
//
// `mcp__vincent__*` is in the list in full (task 057 decision 9). Without it a
// restricted step wired to §13.4's server would see every vincent tool and be
// denied every call — the worst of both, a tool list that is a lie. So
// `restricted` bounds what a step does to the filesystem and the shell, not
// what it does to vincent: a restricted step can create and cancel tasks. That
// is stated in §9.4 and §16 because it is only defensible written down.
const restrictedTools = "Read,Glob,Grep,Edit,Write,MultiEdit,Bash(git:*),mcp__vincent__*"

// Probe timeouts bound the --version and --help subprocesses. Raised from
// 10 s/15 s after a cold logon timed out three probes that all answered in
// well under a second on a warm machine (T4.22): the first minute after logon
// is when every agent CLI is paged in from disk and scanned, and it is also
// exactly when the daemon primes its catalog.
const (
	versionTimeout = 20 * time.Second
	helpTimeout    = 25 * time.Second
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
		Found:          true,
		Path:           path,
		Version:        version,
		SupportsInput:  supportsInput(version),
		VersionVerdict: versionVerdict(version),
		TestedVersions: agent.TestedVersionList(testedVersions),
	}, nil
}

// probeVersion runs `claude --version` and extracts the semver, falling back
// to the raw output when no semver is found (an unparseable version never
// enables input mode — supportsInput rejects it).
func probeVersion(ctx context.Context, path string) (string, error) {
	out, _, err := agent.Probe(ctx, versionTimeout, path, "--version")
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
func buildArgs(spec agent.RunSpec, inputMode bool) ([]string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if spec.PermissionMode == agent.Restricted {
		args = append(args, "--allowedTools", restrictedTools)
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if inputMode {
		args = append(args, "--input-format", "stream-json", "--permission-prompt-tool", "stdio")
	}
	// --resume takes the session id claude itself reported on a previous run
	// (§9.2, task 063). It is the whole of chat continuity: the CLI reloads
	// its own conversation, so turn N sees turns 1..N-1 without vincent
	// replaying anything as prompt context.
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume", spec.ResumeSessionID)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	if spec.MCP != nil {
		cfg, err := mcpConfig(spec.MCP)
		if err != nil {
			return nil, err
		}
		// --strict-mcp-config rides with it, always: without it the user's own
		// `.mcp.json` and their global servers are loaded into a vincent step
		// too, which is a step running with tools nobody in this repository
		// chose and a config file vincent does not control (§9.2).
		args = append(args, "--mcp-config", cfg, "--strict-mcp-config")
	}
	return args, nil
}

// mcpConfig renders §13.4's endpoint as claude's inline `--mcp-config` value.
//
// The token is on the command line, and that is a real cost rather than an
// oversight: it is visible to `ps` for the life of the step, and it reaches the
// §12.3 debug record, which is why that record redacts it. What bounds the cost
// is what the token is — a secret minted for one step run, dead when the step
// ends, and useful only against a loopback listener on this machine (§16).
// claude offers no env-var indirection for an inline config, and the
// alternative — writing a config file into the worktree — is the thing cursor
// has to do and that §9.7 documents as a cost, not a preference.
func mcpConfig(srv *agent.MCPServer) (string, error) {
	b, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			srv.Name: map[string]any{
				"type":    "http",
				"url":     srv.URL,
				"headers": map[string]string{"Authorization": "Bearer " + srv.Token},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("render mcp config: %w", err)
	}
	return string(b), nil
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
	args, err := buildArgs(spec, inputMode)
	if err != nil {
		return nil, err
	}
	//nolint:gosec // path comes from config or PATH resolution by design.
	cmd := exec.Command(path, args...)
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
		resuming:   spec.ResumeSessionID != "",
		proc:       proc,
		stderr:     stderr,
		stdin:      stdin,
		inputMode:  inputMode,
		events:     make(chan agent.Event, 64),
		raw:        make(chan agent.Event, 64),
		promote:    make(chan agent.Event, 8),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
	}
	if inputMode {
		if _, err := stdin.Write(promptLine); err != nil {
			_ = r.Kill()
			return nil, fmt.Errorf("write prompt to claude: %w", err)
		}
	}
	go r.mux()
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

	// raw carries readLoop's events to the mux; promote carries requests
	// surfaced by Respond. One goroutine owns events and closes it, so
	// Respond can deliver a queued request without racing readLoop's close.
	raw     chan agent.Event
	promote chan agent.Event

	// resuming records that this run was started with a session id, so Wait
	// can tell a session claude refused to load from any other failure
	// (task 063 decision 4).
	resuming bool

	mu       sync.Mutex
	terminal *agent.RunResult // parsed result event, if any
	// sessionID is the last `session_id` claude stamped on a stream line.
	// Every line carries it, so the last one seen is the session the run
	// actually ran in — which is what a resumed run must store, since
	// claude may hand a resumed conversation a new id.
	sessionID string
	// streamErr is readLoop's own reader failing, latched for Wait. It is
	// vincent losing the stream, not the CLI failing, and Wait says so with
	// agent.FailureStreamError rather than letting the exit code speak for a
	// transcript that is missing lines (#139).
	streamErr error
	pending   *pendingRequest // the one the engine is answering; guarded by mu
	// queue holds requests that arrived while another was pending. The real
	// CLI does issue concurrent control requests — it batches parallel tool
	// calls, so a restricted run can ask about several at once — and failing
	// the attempt on the second one killed real runs (found in Windows
	// testing, 2026-08-11). The engine still sees exactly one at a time,
	// because a task has one awaiting_input state (§6); the adapter
	// serializes rather than the CLI promising to.
	queue []queuedRequest

	waitOnce sync.Once
	waitRes  agent.RunResult
	waitErr  error
}

// queuedRequest is a control request waiting for its turn: the event to
// surface and the state needed to answer it.
type queuedRequest struct {
	ev   agent.Event
	pend *pendingRequest
}

// maxLineBytes bounds one stream-json line; agent messages embed file
// contents, so lines can be large.
const maxLineBytes = 16 * 1024 * 1024

// mux owns the events channel. readLoop and Respond both produce events, and
// only one goroutine may close a channel — routing both through here is what
// lets Respond surface a queued request at all. It forwards rather than
// blocks, so a consumer calling Respond from its own event loop cannot
// deadlock against it.
func (r *run) mux() {
	defer close(r.events)
	for {
		select {
		case ev, ok := <-r.raw:
			if !ok {
				// The stream ended; nothing queued can still be answered.
				return
			}
			r.events <- ev
		case ev := <-r.promote:
			r.events <- ev
		}
	}
}

func (r *run) readLoop(rd io.Reader) {
	defer close(r.readerDone)
	defer close(r.raw)
	defer r.closeStdin()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if sid := sessionIDOf(line); sid != "" {
			r.mu.Lock()
			r.sessionID = sid
			r.mu.Unlock()
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
		r.raw <- ev
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
			// Queue it rather than failing the attempt. The engine answers
			// one at a time; Respond promotes the next when this one is done.
			// The line is still returned so the transcript keeps it verbatim.
			r.queue = append(r.queue, queuedRequest{ev: ev, pend: pend})
			return agent.Event{Type: agent.EventUnknown, Raw: line}
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
	var next *queuedRequest
	if len(r.queue) > 0 {
		q := r.queue[0]
		r.queue = r.queue[1:]
		r.pending = q.pend
		next = &q
	}
	r.mu.Unlock()

	if next != nil {
		// The CLI is blocked waiting on this one too, so it will emit no
		// further lines to carry it: surfacing it here is the only way the
		// engine ever sees it. readerDone guards against a run that died
		// between the answer and the promotion.
		select {
		case r.promote <- next.ev:
		case <-r.readerDone:
		}
	}
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
		terminal, streamErr, sessionID := r.terminal, r.streamErr, r.sessionID
		r.mu.Unlock()
		res.SessionID = sessionID
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
		// The adapter's verdict on *why* the run stopped (task 003, §9.1).
		// It happens here because this is the one place that holds both the
		// terminal result and the stderr tail; the engine sees neither.
		res.Failure = classify(res, r.stderr.String())
		// A resume claude refused outranks the generic verdict: the id is
		// gone, and no amount of retrying this run will bring it back
		// (task 063 decision 4).
		if f := classifyResume(res, r.stderr.String(), r.resuming); f != nil {
			res.Failure = f
		}
		// A stream vincent could not read to the end outranks whatever the
		// exit code says: the run may well have finished cleanly, but the
		// record of it is missing lines, and §7.1 success is a claim about
		// both (#139). It is set last so it wins over `classify`'s verdict —
		// a usage limit whose transcript is broken is still a broken
		// transcript, and the retry that follows rewrites the file.
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

// SupportsResume implements agent.Resumer: claude can reload one of its own
// conversations with `--resume <session_id>` (§9.2), which is what makes a
// chat's second turn see the first (task 063 decision 3).
func (a *Adapter) SupportsResume() bool { return true }
