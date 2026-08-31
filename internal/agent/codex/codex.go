// Package codex implements the agent adapter for the Codex CLI: headless
// `codex exec --json` runs with JSONL event parsing, model/effort
// passthrough, and tree-kill support (spec §9.3; T2.9). Invocation and
// stream shapes are pinned against codex-cli 0.142.5, and the resumed
// invocation (`exec resume`, §5.5) against 0.150.1.
package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
// codex-cli 0.142.5 for a fresh run and 0.150.1 for a resumed one). Full-auto
// is the documented automation switch; restricted confines writes to the
// worktree — note that a `git commit` from a restricted step may be denied in
// a linked worktree, whose real git dir lives under the main repo (vincent
// itself never needs commits).
//
// A resumed run is a different subcommand, not a flag: `codex exec resume
// [SESSION_ID] [PROMPT]`. Two consequences the fresh shape does not have:
//
//   - The prompt must be the literal `-`. `codex exec` with no prompt
//     argument reads stdin, which is why a fresh run passes none; `exec
//     resume` reads stdin only "if `-` is used" (0.150.1 --help). Omitting it
//     leaves the child waiting on a stdin nobody reads until the step
//     timeout.
//   - `exec resume` has no `-s/--sandbox`, so restricted has no argv spelling
//     here at all and a resumed run is always full-auto (task 072 decision 1).
//     Nothing in vincent can reach that combination: only a chat turn sets
//     ResumeSessionID, and POST /v1/chats hardcodes full_auto with no request
//     field to override it. The day that stops being true, this needs an
//     answer rather than a silently dropped restriction, which is what
//     TestChatsAreAlwaysFullAuto in internal/api exists to force.
func buildArgs(spec agent.RunSpec) []string {
	resuming := spec.ResumeSessionID != ""
	args := []string{"exec", "--json"}
	if resuming {
		// `codex exec resume [SESSION_ID] [PROMPT]`, verified against
		// codex-cli 0.150.1 (task 070). The prompt is left off argv on
		// purpose: `resume`'s help documents `-` for stdin, but a run with
		// no PROMPT argument reads stdin anyway — the capture's stderr says
		// "Reading prompt from stdin…" — which is what `exec` does and what
		// RunSpec.Prompt's stdin-only contract needs (Windows argv limit).
		//
		// `resume` takes the same options as `exec` bar one, so everything
		// below still applies; only the subcommand and the id are inserted
		// here.
		args = append(args, "resume", spec.ResumeSessionID)
	}
	switch {
	case resuming:
		// The one option `exec resume` does not take is `-s/--sandbox`; it
		// carries only --dangerously-bypass-approvals-and-sandbox. So
		// Restricted has no argv spelling on a resumed run, and such a run is
		// always full-auto (§9.3, task 072 decision 1). That is guarded
		// structurally rather than by a run-time check: POST /v1/chats
		// hardcodes full_auto with no request field to override it, nothing
		// else in the codebase sets RunSpec.ResumeSessionID, and
		// TestChatsAreAlwaysFullAuto fails the day either changes.
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	case spec.PermissionMode == agent.Restricted:
		args = append(args, "--sandbox", "workspace-write")
	default:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+spec.Effort)
	}
	if spec.MCP != nil {
		// codex has no --mcp-config, but `codex exec` takes `-c key=value`
		// dotted TOML overrides, and 0.150.1's `mcp add` confirms
		// streamable-HTTP servers with a bearer token read from an env var
		// (§9.3, task 057 decision 8). Per-run: nothing here mutates
		// ~/.codex/config.toml, which is the user's file and not vincent's.
		//
		// The token goes through the environment rather than argv, because
		// codex offers the indirection and claude does not. MCPTokenEnv is
		// where the adapter puts it.
		prefix := "mcp_servers." + spec.MCP.Name + "."
		args = append(args,
			"-c", prefix+"url="+strconv.Quote(spec.MCP.URL),
			"-c", prefix+"bearer_token_env_var="+strconv.Quote(MCPTokenEnv))
	}
	return args
}

// MCPTokenEnv is the environment variable codex is told to read §13.4's bearer
// token from. It is exported so a test can assert the child's environment
// carries it, which is the only place the token appears for this adapter.
//
//nolint:gosec // G101: the name of an env var, not a credential.
const MCPTokenEnv = "VINCENT_MCP_TOKEN"

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
	if spec.MCP != nil {
		// Appended after the resolved environment on purpose: §12.3's
		// `environment.unset` must not be able to strip the channel the step
		// was wired to, and a later assignment is what the child reads.
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, MCPTokenEnv+"="+spec.MCP.Token)
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
		resuming:   spec.ResumeSessionID != "",
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

	// resuming records that this run was started with a thread id, so Wait
	// can tell a thread codex refused to load from any other failure
	// (task 072 decision 2).
	resuming bool

	mu       sync.Mutex
	terminal *agent.RunResult // assembled from turn.completed / turn.failed
	// sessionID is the last `thread_id` codex reported. Only `thread.started`
	// carries one — unlike claude, codex does not stamp it on every line — and
	// a resumed run repeats the id it was given (verified against 0.150.1,
	// testdata/resume_0.150.1.jsonl). Last-wins anyway, matching claude (§9.2):
	// what a future build does with a forked thread is its business, and the
	// id the run actually finished in is the one a chat must store.
	sessionID string
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
		if tid := threadIDOf(line); tid != "" {
			r.mu.Lock()
			r.sessionID = tid
			r.mu.Unlock()
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
// A usage-limit or unauthenticated stop is still deliberately left
// unclassified (task 003): those wordings are not fixture-verified, and this
// adapter ships no guess in their place. A quota stop here therefore reads
// exactly as it did before task 003 — nonzero_exit or agent_error. The one
// condition it does name is a thread codex refused to resume, which is
// fixture-verified (task 070; testdata/resume_lost_0.150.1.txt).
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
		terminal, streamErr, sessionID := r.terminal, r.streamErr, r.sessionID
		r.mu.Unlock()
		res.SessionID = sessionID
		if terminal != nil {
			res.IsError = terminal.IsError
			res.ErrorMessage = terminal.ErrorMessage
			res.ResultText = terminal.ResultText
			res.InputTokens = terminal.InputTokens
			res.OutputTokens = terminal.OutputTokens
			res.CacheReadTokens = terminal.CacheReadTokens
			res.CacheCreationTokens = terminal.CacheCreationTokens
			res.ReasoningOutputTokens = terminal.ReasoningOutputTokens
			// The thread id the stream reported, which a chat stores and
			// hands back as ResumeSessionID next turn (task 070).
			res.SessionID = terminal.SessionID
			// CostUSD stays nil: codex does not report cost (spec §9.3).
		} else {
			res.IsError = true
			res.ErrorMessage = "stream ended without a result event"
			if tail := r.stderr.String(); tail != "" {
				res.ErrorMessage += ": " + tail
			}
		}
		// The adapter's verdict on *why* the run stopped (§9.1). It happens
		// here because this is the one place that holds both the terminal
		// result and the stderr tail; the engine sees neither. A refused
		// resume is the only thing codex classifies, and only for a run that
		// actually passed a thread id (task 072 decision 2).
		res.Failure = classifyResume(res, r.stderr.String(), r.resuming)
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

// SupportsResume implements agent.Resumer. It became true with task 070,
// which satisfied the precondition task 063 decision 3 set: the stream's
// `thread_id` is read into RunResult.SessionID, and the `exec resume <id>`
// argv is pinned by a capture against a named build (codex-cli 0.150.1,
// testdata/resume_0.150.1.jsonl — the resumed turn reports the *same*
// thread_id and answers a question about the previous turn). Nothing is
// emulated: codex resumes its own session, and vincent never replays a
// conversation as prompt context.
//
// Resuming is what lets a chat (§5.5) run on this adapter. Each of its three
// halves has a captured fixture under testdata/: the argv in buildArgs, the
// `thread_id` in stream.go, and the refusal of a dead id in failure.go.
func (a *Adapter) SupportsResume() bool { return true }
