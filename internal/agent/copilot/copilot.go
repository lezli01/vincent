package copilot

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// binaryName is what PATH resolution looks for when no path is configured. It
// is `copilot` on every OS: the npm package installs a `copilot.cmd` shim on
// Windows, which exec.LookPath finds through PATHEXT.
const binaryName = "copilot"

// versionTimeout bounds the --version probe subprocess. 20 s, the bound every
// adapter carries for the same cold-logon reason (T4.22).
const versionTimeout = 20 * time.Second

// noAutoUpdate rides on every spawn, the probe included: without it the
// launcher may run a newer cached build than the one Detect reported, and the
// version verdict would describe a binary that never runs (task 138
// decision 5).
const noAutoUpdate = "--no-auto-update"

// Adapter runs agents through the GitHub Copilot CLI.
type Adapter struct {
	// pathFn returns the configured binary path; "" resolves from PATH.
	// A func rather than a value so config hot-reload reaches future runs.
	pathFn func() string
}

// New returns the copilot adapter. pathFn returns the configured binary path
// ("" = resolve "copilot" from PATH); nil means always resolve from PATH.
func New(pathFn func() string) *Adapter {
	if pathFn == nil {
		pathFn = func() string { return "" }
	}
	return &Adapter{pathFn: pathFn}
}

// Name implements agent.Adapter.
func (a *Adapter) Name() string { return "copilot" }

// Path implements agent.Adapter: the resolved binary, no subprocess.
func (a *Adapter) Path() (string, error) { return a.resolvePathWith(nil) }

// resolvePathWith resolves the binary where a run through l executes: on the
// host for nil, inside the image for a containerized step (task 062.2
// decision 2).
func (a *Adapter) resolvePathWith(l agent.Launcher) (string, error) {
	return agent.ResolveWith(l, "copilot", a.pathFn(), binaryName)
}

// Detect implements agent.Adapter: path resolution and a
// `copilot --no-auto-update --version` probe.
//
// LoggedIn is nil by design (task 138 decision 10, open question 3 closed by
// 138.6): copilot has no `auth status` or `whoami` command; reading
// `~/.copilot` is ruled out by T1.7; probing with an invalid model and reading
// the refusal was rejected as a text-matching hack; and an env token is not
// proof — COPILOT_GITHUB_TOKEN, GH_TOKEN and GITHUB_TOKEN may hold a classic
// `ghp_` PAT, which Copilot rejects, so their presence says nothing. A run
// that does stop logged out is classified unauthenticated (failure.go).
// `copilot version` is never called — it checks for updates over the network.
// SupportsInput is permanently false (§9.9).
func (a *Adapter) Detect(ctx context.Context) (agent.Availability, error) {
	return a.detectWith(ctx, nil)
}

// detectWith is Detect through a launcher, so a test can see the probe's argv
// the way it sees a run's.
func (a *Adapter) detectWith(ctx context.Context, l agent.Launcher) (agent.Availability, error) {
	path, err := a.resolvePathWith(l)
	if err != nil {
		return agent.Availability{Error: err.Error()}, nil
	}
	out, _, err := agent.ProbeWith(ctx, l, versionTimeout, path, noAutoUpdate, "--version")
	if err != nil {
		return agent.Availability{
			Path:  path,
			Error: fmt.Sprintf("copilot --version failed: %v", err),
		}, nil
	}
	version := parseVersion(string(out))
	return agent.Availability{
		Found:          true,
		Path:           path,
		Version:        version,
		VersionVerdict: versionVerdict(version),
		TestedVersions: agent.TestedVersionList(testedVersions),
	}, nil
}

// versionLineRe matches copilot's version sentence, `GitHub Copilot CLI
// 1.0.105.` (version_1.0.105.txt), trailing period and all.
var versionLineRe = regexp.MustCompile(`^GitHub Copilot CLI (\S+?)\.?$`)

// parseVersion reads the version from `--version` output: the semver out of
// the first line's sentence, or that line verbatim when it is not the
// sentence — a build that rewords it is still recorded, and judges untested
// rather than unknown. The update hint on the second line is ignored.
func parseVersion(out string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	first = strings.TrimSpace(first)
	if m := versionLineRe.FindStringSubmatch(first); m != nil {
		return m[1]
	}
	return first
}

// newSessionID mints the random (version 4) UUID passed as --session-id.
// Copilot adopts any UUID it has not seen (session_id_unknown_1.0.105.jsonl),
// so the id is known before the process writes a line — which is the point:
// a killed run may never reach the result line that would report one (task
// 138 decision 3).
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildArgs assembles the pinned CLI invocation (spec §9.9, verified against
// 1.0.105).
//
// Full-auto is the three `--allow-all-*` flags, never `COPILOT_ALLOW_ALL`: the
// env spelling also trusts the worktree and loads its hooks and MCP servers
// (task 138 decision 4), so Start strips an inherited one (childEnv). There is no `-p` and no prompt argument: the prompt
// is stdin only (decision 2), so a prompt that reads like a flag is only ever
// a prompt. No `-C` either — the process cwd is the worktree. The built-in
// GitHub MCP server is left on; --disable-builtin-mcps is not passed (§9.9).
//
// The vincent MCP server (§13.4) rides inline in --additional-mcp-config,
// with --allow-tool naming it (task 138 decision 6, see mcpConfig).
//
// A resumed run (a chat turn, §5.5) passes `--resume=<id>` instead of
// --session-id and is otherwise the same argv (task 138.11,
// resume_1.0.105.jsonl). It is always the `=` spelling: --resume takes an
// optional value, so a separate token could be read as something else. And
// it is never --session-id with the old id: copilot adopts any UUID it does
// not know as a fresh session (session_id_unknown_1.0.105.jsonl), so a
// forgotten session would silently restart — a chat that quietly lost its
// context — where --resume= refuses it (resume_unknown_1.0.105.txt) and the
// turn is classified as session_lost. --trust-folder is never passed on either path; it cannot combine
// with --resume.
//
// Restricted (task 138.10) replaces the three `--allow-all-*` flags with
// restrictedArgs: a CLI-enforced tool set with no shell in it, on every OS.
// A resumed restricted run re-passes the same flags.
func buildArgs(spec agent.RunSpec, sessionID string) ([]string, error) {
	args := []string{"--output-format", "json", noAutoUpdate}
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume="+spec.ResumeSessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if spec.PermissionMode == agent.Restricted {
		args = append(args, restrictedArgs(spec.MCP)...)
	} else {
		args = append(args, "--allow-all-tools", "--allow-all-paths", "--allow-all-urls")
	}
	// --model only when §8.6 resolved one: with none, the CLI picks `auto`
	// itself (decision 9), and that is what the record says too.
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	// The effort is passed through as given. Effort support is per model and
	// `auto` refuses any (unsupported_effort_1.0.105.txt); the CLI's fast
	// exit 1 says so through the no-result path, so no pair is predicted here.
	if spec.Effort != "" {
		args = append(args, "--reasoning-effort", spec.Effort)
	}
	if spec.MCP != nil {
		cfg, err := mcpConfig(spec.MCP)
		if err != nil {
			return nil, err
		}
		// --allow-tool names the server although --allow-all-tools already
		// covers it in full-auto: restricted mode drops --allow-all-tools,
		// and the vincent tools stay allowed there (task 057 decision 9,
		// restricted_mcp_1.0.105.jsonl).
		args = append(args, "--additional-mcp-config", cfg, "--allow-tool="+spec.MCP.Name)
	}
	return args, nil
}

// restrictedTools is the --available-tools set of a restricted run (spec
// §9.9, task 138.10): read, search and edit, and nothing else. A tool left out
// does not exist for the model at all. `apply_patch` is the edit tool of the
// models `auto` routes to that do not use `edit`/`create`
// (restricted_write_1.0.105.jsonl edited through it); without it those models
// have no write tool. Its paths come from the patch text, not a `path`
// argument, and the path check still covers them: the same capture's
// `*** Add File:` and `*** Delete File:` outside the worktree were denied.
//
// The shell is deliberately absent — `bash` on macOS and Linux, `powershell`
// on Windows — so the set is the same on every GOOS, and restricted copilot
// has no git. A visible shell runs any command copilot judges read-only
// without asking (138.3, every OS), and no allow or deny rule bounds it to
// git: deny beats allow, so `--deny-tool=shell` kills git too (task 138.10
// decision 1). There is no fetch or web tool either, so no URL rule is needed.
var restrictedTools = []string{"view", "grep", "glob", "edit", "create", "apply_patch"}

// restrictedArgs is the permission half of a restricted argv.
// `--allow-tool=read` and `--allow-tool=write` approve reads and edits;
// `--allow-all-paths` is not passed, so copilot's path verification still
// refuses a path outside the worktree, and in a headless run that refusal is a
// `denied` tool result (restricted_write_1.0.105.jsonl: two apply_patch writes
// outside it). The vincent MCP
// server joins the set by its name (restricted_mcp_1.0.105.jsonl: `vincent`
// makes `vincent-health` visible); its --allow-tool is appended with
// --additional-mcp-config.
//
// Never passed: `--allow-all-*`, `--allow-all`, `--yolo` and
// `COPILOT_ALLOW_ALL` (each widens past the set), `--sandbox` and
// `--assisted-approval` (out of scope, §9.9).
func restrictedArgs(srv *agent.MCPServer) []string {
	tools := restrictedTools
	if srv != nil {
		tools = append(slices.Clone(tools), srv.Name)
	}
	return []string{"--available-tools=" + strings.Join(tools, ","), "--allow-tool=read", "--allow-tool=write"}
}

// allowAllEnv is the env spelling of full-auto (help_environment_1.0.105.txt).
const allowAllEnv = "COPILOT_ALLOW_ALL"

// childEnv is env with COPILOT_ALLOW_ALL removed, in every permission mode
// (spec §9.9, task 138.10 review F2). Not setting it is not enough: env, or
// the daemon's own environment when env is nil, may already carry it — a
// natural setting for someone who runs copilot full-auto in their shell — and
// then a restricted run would have every tool approved past
// --available-tools, and every run would trust the worktree and load its
// hooks, which a restricted agent can write. The name is matched
// case-insensitively, as Windows reads it; on POSIX a differently cased name
// is not the variable, and dropping it costs nothing. An env without it is
// returned as is, so nil keeps meaning "inherit".
func childEnv(env []string) []string {
	src := env
	if src == nil {
		src = os.Environ()
	}
	isAllowAll := func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(k, allowAllEnv)
	}
	if !slices.ContainsFunc(src, isAllowAll) {
		return env
	}
	return slices.DeleteFunc(slices.Clone(src), isAllowAll)
}

// mcpConfigFile is the --additional-mcp-config document: copilot's
// ~/.copilot/mcp-config.json shape, one HTTP server. Typed rather than a map so
// the field set is fixed and every value is escaped by encoding/json, never by
// string formatting.
type mcpConfigFile struct {
	Servers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	// Tools is copilot's per-server filter; "*" admits every tool the
	// server lists. Without it the CLI loads none of them.
	Tools []string `json:"tools"`
}

// mcpConfig renders the per-step server as inline JSON for
// --additional-mcp-config (spec §9.9).
//
// The token is on the command line, as with claude (§9.2): visible to `ps`
// for the life of the step and redacted from the §12.3 debug record by
// redactMCPToken. On Windows the npm `.cmd` shim runs the line as
// `cmd /c`. With more than two `"` on that line, `cmd /c` drops the first
// and the last one, so a quoted shim path plus this quoted JSON split the
// path at its space (review F1). An `@file` carrier on a spaced temp path
// would fail the same way. The host spawn therefore wraps a batch file's
// line in one outer pair of quotes (procx.BatchCmdLine), and
// TestStartMCPCallbackThroughCmdShim runs the callback through a shim in a
// spaced directory. Past that, the JSON carries nothing else cmd.exe
// reinterprets: the URL is a loopback address and the token is base64url.
// So there is no file carrier (decision 6).
//
// There is no strict switch: the user's own ~/.copilot/mcp-config.json
// servers augment the run rather than being replaced (§9.9).
func mcpConfig(srv *agent.MCPServer) (string, error) {
	b, err := json.Marshal(mcpConfigFile{Servers: map[string]mcpServerEntry{
		srv.Name: {
			Type:    "http",
			URL:     srv.URL,
			Headers: map[string]string{"Authorization": "Bearer " + srv.Token},
			Tools:   []string{"*"},
		},
	}})
	if err != nil {
		return "", fmt.Errorf("render mcp config: %w", err)
	}
	return string(b), nil
}

// Start implements agent.Adapter. The prompt is written via stdin (Windows
// argv limit); the process tree is killed when ctx is canceled. The caller
// must consume Events() until closed; Wait blocks on stream end.
func (a *Adapter) Start(ctx context.Context, spec agent.RunSpec) (agent.RunHandle, error) {
	// A resumed run's session is the one it resumes; a fresh run's is minted
	// here so it is known before the process writes a line.
	sessionID := spec.ResumeSessionID
	if sessionID == "" {
		sessionID = newSessionID()
	}
	args, err := buildArgs(spec, sessionID)
	if err != nil {
		return nil, err
	}
	path, err := a.resolvePathWith(spec.Launcher)
	if err != nil {
		return nil, err
	}
	stderr := &tailWriter{max: 64 * 1024}
	proc, err := agent.Launch(spec.Launcher, agent.Command{
		Path:   path,
		Args:   args,
		Dir:    spec.WorkDir,
		Env:    childEnv(spec.Env),
		Stdin:  strings.NewReader(spec.JoinedPrompt()),
		Stderr: stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("start copilot: %w", err)
	}
	r := &run{
		proc:       proc,
		stderr:     stderr,
		sessionID:  sessionID,
		resuming:   spec.ResumeSessionID != "",
		events:     make(chan agent.Event, 64),
		readerDone: make(chan struct{}),
		procDone:   make(chan struct{}),
	}
	go r.readLoop(proc.Stdout())
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Kill()
		case <-r.procDone:
		}
	}()
	return r, nil
}

// run is a live copilot process.
type run struct {
	proc   agent.Process
	stderr *tailWriter
	// sessionID is the --session-id or --resume= id this run was started
	// with. It is the run's session whether or not a result line ever
	// confirms it.
	sessionID string
	// resuming records that the run was started with --resume=, so Wait can
	// tell a refused resume from any other failure (§9.2's rule).
	resuming bool

	events     chan agent.Event
	readerDone chan struct{}
	procDone   chan struct{}

	mu       sync.Mutex
	terminal *agent.RunResult // assembled from the result event
	// streamErr is readLoop's own reader failing, latched for Wait (#139).
	streamErr error

	waitOnce sync.Once
	waitRes  agent.RunResult
	waitErr  error
}

// maxLineBytes bounds one stream line; tool payloads embed file contents and
// command output, so lines can be large.
const maxLineBytes = 16 * 1024 * 1024

func (r *run) readLoop(rd io.Reader) {
	defer close(r.readerDone)
	defer close(r.events)
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	// One parser per run: message deltas and tool names are carried across
	// lines, so the state belongs to this stream and nothing else (§9.9).
	parse := (&stream{}).parse
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
	// A reader that stopped before the stream did leaves the CLI writing into
	// a pipe nobody empties, which would hang it until the step timeout.
	// Drain it, then let Wait name the failure (#139).
	if err := sc.Err(); err != nil {
		r.mu.Lock()
		r.streamErr = err
		r.mu.Unlock()
		_, _ = io.Copy(io.Discard, rd)
	}
}

// Events implements agent.RunHandle.
func (r *run) Events() <-chan agent.Event { return r.events }

// Respond implements agent.RunHandle. A copilot step has no mid-run input
// channel (§9.9), so there is never a pending request to answer.
func (r *run) Respond(agent.InputResponse) error {
	return errors.New("copilot is non-interactive; mid-run input is not supported")
}

// Kill implements agent.RunHandle: terminates the whole process tree.
func (r *run) Kill() error { return r.proc.Kill() }

// Terminate implements agent.RunHandle: asks the tree to exit (spec §6).
// Copilot answers a SIGTERM by ending its stream with `abort` and a result
// that exits 0 (sigterm_1.0.105.jsonl); the engine records the termination it
// asked for regardless, and the parser reads the abort as an error (§9.9).
func (r *run) Terminate() error { return r.proc.Terminate() }

// PID implements agent.RunHandle.
func (r *run) PID() int { return r.proc.PID() }

// Wait implements agent.RunHandle. It blocks until the stream is fully
// consumed and the process has exited, then assembles the RunResult per §7.1.
//
// SessionID is always the --session-id or --resume= id the run was started
// with, even when
// no result line arrived — a killed run included. A result line that names a
// different session contradicts the one thing this adapter chose, and is
// reported as an error rather than recorded.
//
// The no-result path carries the stderr tail: an unavailable model or an
// unsupported effort exits 1 with the reason on stderr and no result line
// (unavailable_model_1.0.105.txt).
func (r *run) Wait() (agent.RunResult, error) {
	r.waitOnce.Do(func() {
		<-r.readerDone
		exitCode, err := r.proc.Wait()
		close(r.procDone)
		r.proc.Release()

		if err != nil {
			r.waitErr = fmt.Errorf("wait for copilot: %w", err)
		}
		r.mu.Lock()
		terminal, streamErr := r.terminal, r.streamErr
		r.mu.Unlock()
		res := assemble(exitCode, r.sessionID, r.resuming, terminal, streamErr, r.stderr.String())
		r.waitRes = res
	})
	return r.waitRes, r.waitErr
}

// assemble builds the RunResult Wait returns from what the run left behind:
// the process's exit code, the session id it was started with, the result
// line's verdict (nil when none arrived), the reader's own failure, and the
// stderr tail. resuming is whether the run was a --resume= run.
func assemble(exitCode int, sessionID string, resuming bool, terminal *agent.RunResult, streamErr error, stderrTail string) agent.RunResult {
	res := agent.RunResult{ExitCode: exitCode, SessionID: sessionID}
	switch {
	case terminal == nil:
		res.IsError = true
		res.ErrorMessage = "stream ended without a result event"
		if stderrTail != "" {
			res.ErrorMessage += ": " + stderrTail
		}
	case terminal.SessionID != "" && terminal.SessionID != sessionID:
		res.IsError = true
		res.ResultText = terminal.ResultText
		res.ErrorMessage = fmt.Sprintf("copilot reported session %s, not the session %s it was given",
			terminal.SessionID, sessionID)
	default:
		res.IsError = terminal.IsError
		res.ErrorMessage = terminal.ErrorMessage
		res.ResultText = terminal.ResultText
		// The run's account of itself, as the stream assembled it (task
		// 138.7). CostUSD stays nil: a premium request is not a price.
		res.InputTokens = terminal.InputTokens
		res.OutputTokens = terminal.OutputTokens
		res.CacheReadTokens = terminal.CacheReadTokens
		res.CacheCreationTokens = terminal.CacheCreationTokens
		res.ReasoningOutputTokens = terminal.ReasoningOutputTokens
		res.ModelUsage = terminal.ModelUsage
		res.Duration = terminal.Duration
		res.APIDuration = terminal.APIDuration
	}
	res.Failure = classify(res, stderrTail)
	if f := classifyResume(res, stderrTail, resuming); f != nil {
		res.Failure = f
	}
	if streamErr != nil {
		streamFailure(&res, streamErr)
	}
	return res
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
func (r *run) Argv() []string { return r.proc.Argv() }

// SupportsResume implements agent.Resumer (task 138.11): `--resume=<id>`
// reloads the session (resume_1.0.105.jsonl answered a question about the
// previous run), the session id is the one passed in (Start), and an id the
// CLI no longer knows is classified session_lost (resume_unknown_1.0.105.txt,
// failure.go). Nothing is emulated: vincent never replays a conversation as
// prompt context.
func (a *Adapter) SupportsResume() bool { return true }

// SupportsRestrictedResume implements agent.RestrictedResumer. A --resume run
// re-passes the same restricted flags, and they hold there:
// restricted_resume_1.0.105.jsonl resumed a restricted session, both its
// apply_patch writes outside the worktree (a new file and an existing one)
// were denied and it had no shell (task 138.10, review F1).
func (a *Adapter) SupportsRestrictedResume() bool { return true }

// UsageIsSessionTotal implements agent.CumulativeUsage: copilot's usage
// checkpoint is the session's running total, so a resumed run's figures
// include every earlier run's (resume_1.0.105.jsonl; spec §9.9, task 138.11
// decision 1).
func (a *Adapter) UsageIsSessionTotal() bool { return true }
