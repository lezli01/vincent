package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// PermissionMode selects how much autonomy the agent CLI gets (spec §9.4).
type PermissionMode string

// Permission modes (spec §9.4).
const (
	FullAuto   PermissionMode = "full-auto"
	Restricted PermissionMode = "restricted"
)

// InputPolicy is the step's reaction to a mid-run input request (spec §7.4).
// Ignored by adapters without input support.
type InputPolicy string

// Input policies (spec §7.4).
const (
	InputWait InputPolicy = "wait"
	InputDeny InputPolicy = "deny"
)

// Option provenance values (spec §9.6).
const (
	SourceCLI     = "cli"     // probed from the installed binary
	SourceCurated = "curated" // catalog shipped with vincent
)

// InputSupport is what an adapter can *ever* do about mid-run input, known
// without probing (spec §9.5, task 013). It is the static half of
// Availability.SupportsInput, which is the same question answered about the
// binary actually installed.
//
// It exists so §8.2 validation — which never spawns a process — can reject a
// workflow that requires interaction on an adapter where interaction is
// impossible, at the moment its author is looking at the file.
type InputSupport string

// Input support levels (task 013). There are two: no adapter supports input
// regardless of version, so an "always" would be a value nothing sets.
const (
	// InputNever is an adapter with no control channel at all — codex and
	// cursor. No version of the CLI can ask a question mid-run.
	InputNever InputSupport = "never"
	// InputDetected is an adapter whose support depends on the installed
	// binary — claude, gated to the fixture-verified version family (§9.3).
	// Only a probe can answer, so §8.2 does not judge it.
	InputDetected InputSupport = "detected"
)

// ErrRestrictedUnsupported is returned by Start when the adapter cannot
// honor PermissionMode Restricted on this platform (spec §9.4). It lives here
// rather than in an adapter package so the engine can recognize the condition
// without depending on any implementation.
//
// Adapters return it instead of silently downgrading to full-auto: running a
// step unrestricted because restricting was unavailable inverts the very
// choice the step made. Cursor on Windows is the one case today (§9.7).
var ErrRestrictedUnsupported = errors.New("restricted permission mode is unsupported on this platform")

// Adapter is the only surface the daemon consumes to run agents (spec §9.1);
// adding an agent CLI is one new implementation with zero core changes.
type Adapter interface {
	// Name returns the adapter name ("claude", "codex").
	Name() string
	// Detect probes the installed CLI: found, path, version, input support.
	// Absence is reported in the Availability, not as an error.
	Detect(ctx context.Context) (Availability, error)
	// Options probes the CLI ad hoc for selectable models/efforts, merged
	// with the curated catalog (spec §9.6). On probe failure it returns the
	// curated catalog alongside the error — degrade, never block.
	Options(ctx context.Context) (Options, error)
	// Path resolves the binary without spawning it (config knob, else PATH),
	// so the catalog cache can key on binary identity cheaply (§9.6; T2.11
	// interface addition).
	Path() (string, error)
	// Curated returns the compiled-in catalog without probing — what §8.2
	// validation reads while the cache is unprimed (T2.11 interface
	// addition).
	Curated() Options
	// NewLineParser returns a parser for this adapter's transcript lines, so
	// a recorded run can be normalized after the fact with exactly the code
	// that normalized it live (§13.2 format=normalized; T3.3 interface
	// addition). One parser handles one file, in order: some dialects carry
	// state across lines.
	NewLineParser() LineParser
	// Start launches one agent run. The returned handle's process is killed
	// (whole tree) when ctx is canceled.
	Start(ctx context.Context, spec RunSpec) (RunHandle, error)
}

// Availability is the result of Detect (spec §9.5).
type Availability struct {
	Found         bool
	Path          string // resolved binary path when found
	Version       string
	SupportsInput bool   // mid-run input requests (spec §7.4)
	LoggedIn      *bool  // nil = unknown (best effort; always nil in v1 for claude)
	Error         string // why not found / not probed
}

// RunSpec describes one agent run (spec §9.1).
type RunSpec struct {
	Prompt         string // written to stdin, never argv (Windows 8 KB argv limit)
	WorkDir        string // the task worktree
	Model          string // resolved per §8.6; "" = CLI default
	Effort         string // resolved per §8.6; adapter-native; "" = CLI default
	PermissionMode PermissionMode
	OnInput        InputPolicy // ignored when the adapter lacks input support
	// Env is the child's environment, resolved from §12.3 `environment:`
	// (T4.23). nil still means "inherit the daemon's", which is what tests
	// and any other caller get; the engine always populates it, so a running
	// step's environment is a decided value rather than an inherited one.
	Env []string
}

// RunHandle is a live agent run (spec §9.1).
type RunHandle interface {
	// Events streams normalized events; closed when the stream ends. Every
	// event carries the verbatim stream line in Raw for lossless transcripts.
	Events() <-chan Event
	// Respond answers the pending InputRequest (spec §7.4); an error is
	// returned if none is pending or the adapter lacks input support.
	Respond(resp InputResponse) error
	// Wait blocks until process exit and returns the assembled result.
	Wait() (RunResult, error)
	// Terminate asks the run's process tree to exit, the graceful half of
	// spec §6's cancel. On platforms with no such primitive it is the same
	// as Kill; the caller's grace period elapses either way.
	Terminate() error
	// Kill terminates the run's whole process tree.
	Kill() error
	// PID returns the OS process id of the run — recorded on the StepRun
	// (spec §14 pid column) for crash-recovery journaling (§12.4).
	PID() int
	// Argv is the command line the adapter actually spawned, for the §12.3
	// debug record. It exists because "which flags did this run get" was
	// unanswerable from outside the adapter, and that is precisely the
	// question a misbehaving run raises — a step that prompted for
	// permissions could not be shown to have resolved to `restricted`.
	Argv() []string
}

// EventType classifies normalized agent events (spec §9.1).
type EventType string

// Normalized event types. Unknown stream lines are transcripted but not
// normalized (tolerant parsing, phase 1 decision).
const (
	EventOutput  EventType = "output"
	EventToolUse EventType = "tool_use"
	// EventToolResult reports what a tool invocation did (T4.16). It carries
	// an outcome, not the tool's output — see ToolResult.
	EventToolResult EventType = "tool_result"
	// EventThinking carries the model's reasoning text (T4.16). It is
	// emitted only for whole reasoning blocks: a dialect that streams
	// token-level deltas coalesces them itself rather than emitting one
	// event per fragment, so a live tail is never a stream of half-words
	// (spec §9.7, amended).
	EventThinking EventType = "thinking"
	EventUsage    EventType = "usage"
	// EventInputRequest carries a mid-run input request (spec §7.4). A nil
	// Request means the adapter received a control message it could not
	// parse or that violates the serial-request contract — the engine fails
	// the attempt with input_protocol_error rather than wait on a request it
	// can't render (spec §18).
	EventInputRequest EventType = "input_request"
	// EventInputCanceled reports that the agent withdrew its pending input
	// request (T2.12 interface addition); the engine resumes the run via the
	// input_closed transition (spec §6).
	EventInputCanceled EventType = "input_canceled"
	EventResult        EventType = "result"
	EventError         EventType = "error"
	EventUnknown       EventType = "unknown"
)

// LineParser normalizes one verbatim stream line into an Event. Parsers are
// stateful for dialects that derive an event from earlier lines (codex's
// result text is the last agent_message it saw), so a parser instance
// belongs to exactly one stream or transcript file and must see its lines in
// order.
type LineParser func(raw []byte) Event

// Event is one normalized agent stream event.
type Event struct {
	Type    EventType
	Text    string        // EventOutput: assistant text; EventThinking: reasoning text
	Tools   []ToolUse     // tools invoked in this event
	Results []ToolResult  // EventToolResult: outcomes reported by this line
	Request *InputRequest // EventInputRequest
	Result  *RunResult    // EventResult
	Message string        // EventError: what went wrong
	// Raw is the verbatim stream line, which transcripts write. The one
	// exception is a coalesced EventThinking: its Text was accumulated
	// across earlier delta lines, so Raw is the line that closed the block
	// rather than the line the text came from (spec §9.7). Nothing downstream
	// pairs Text with Raw, and offsets stay correct because the closing line
	// is the one that had just been written.
	Raw []byte
}

// ToolUse is one tool invocation surfaced by the agent.
type ToolUse struct {
	Name string
	// Summary is the invocation's subject — the command run, the file
	// edited — extracted from the dialect's arguments by ToolSummary
	// (T4.14). Empty when the payload carried nothing recognizable, which
	// is not an error: a name alone is what the pane rendered before.
	Summary string
	// CallID correlates this invocation with the ToolResult that reports its
	// outcome (claude `tool_use_id`, cursor `toolCallId`, codex `item.id`).
	// Position cannot substitute: claude batches parallel tool calls (T4.8),
	// so the result following a call is routinely not that call's.
	CallID string
}

// ToolResult reports the outcome of one ToolUse (T4.16). It carries an
// outcome, never the tool's output body — a single grep result can be
// hundreds of lines, and the transcript already holds it verbatim.
type ToolResult struct {
	// CallID matches the ToolUse this reports on; empty when the dialect
	// gave no id, in which case a client can only render it standalone.
	CallID string
	// Name is the tool, when the dialect repeats it on the result. Empty is
	// normal — claude names the tool only on the call.
	Name string
	// Summary is the outcome in a few words: "exit 0", "created (+1 −0)",
	// the first line of the result text.
	Summary string
	// IsError reports a failed invocation. A dialect that says nothing about
	// success reports false — the flag means "known to have failed", never
	// "assumed fine".
	IsError bool
}

// InputRequest is a normalized mid-run input request (spec §7.4, §9.1); at
// most one is pending per run.
type InputRequest struct {
	Kind       string          // "question" | "permission"
	Questions  []Question      // kind=question
	Permission *PermissionReq  // kind=permission
	Raw        json.RawMessage // adapter-native payload, passed through untranslated
}

// Question is one structured question inside an InputRequest.
type Question struct {
	Text        string
	Header      string
	Options     []string // may be empty; free-text answers are always accepted
	MultiSelect bool
}

// PermissionReq describes a permission-kind input request.
type PermissionReq struct {
	Tool    string
	Summary string
}

// InputResponse answers an InputRequest (spec §9.1).
type InputResponse struct {
	Answers map[string][]string // question text → selected/typed answer(s)
	Allow   *bool               // kind=permission: approve or deny
	// Response is a free-text response in place of per-question answers —
	// the §7.4 deny-mode canned answer rides here; for a permission denial
	// it becomes the message the agent reads (T2.12 interface addition).
	Response string
}

// RunResult is the outcome of one agent run (spec §9.1). IsError reports a
// terminal error event — §7.1 success requires exit 0 AND !IsError.
type RunResult struct {
	ExitCode     int
	IsError      bool   // the stream's terminal result was an error, or none arrived
	ErrorMessage string // set when IsError
	ResultText   string // agent's final answer/summary
	InputTokens  int64  // 0 if unreported
	OutputTokens int64
	CostUSD      *float64 // nil if unreported (e.g. codex)
	// Failure is the adapter's verdict about *why* the run stopped, when it
	// recognized the reason in its own CLI's output (task 003, §9.1). nil —
	// "nothing recognized" — is every run that behaves as it did before this
	// field existed, which is what keeps an unrecognized failure on today's
	// nonzero_exit/agent_error path.
	//
	// It rides here rather than behind a new Adapter method because this is
	// where the material already is: the terminal result and the stderr tail
	// live inside the handle, and the engine never sees either.
	Failure *Failure
}

// FailureKind names a condition an adapter recognized in its CLI's output
// (task 003, §9.1).
//
// It is deliberately *not* a block_reason. That vocabulary belongs to
// internal/taskrun and internal/worktree (T1.5/T1.6 decision), and the engine
// does the kind → reason mapping, so a reason string has exactly one source of
// truth and internal/agent owns no entry in it.
type FailureKind string

// Recognized failure kinds (task 003).
const (
	// FailureUsageLimit is a run the CLI stopped because the account's usage
	// quota for the current window is spent. It is not a failure of the work:
	// the window reopens on its own, so the engine re-queues the task with an
	// admission hold instead of spending a retry (§7.2, §11).
	FailureUsageLimit FailureKind = "usage_limit"
	// FailureUnauthenticated is a run the CLI refused because it is not
	// logged in. Waiting cannot fix it — re-authenticating is a human action —
	// so it stays an ordinary failure under the §7.2 budget (§18).
	FailureUnauthenticated FailureKind = "unauthenticated"
)

// Failure is an adapter's verdict about a stopped run (task 003, §9.1).
type Failure struct {
	Kind FailureKind
	// RetryAfter is when the CLI said the limit resets — absolute, UTC. nil
	// means the CLI reported no usable reset time, and is what an unparseable
	// or implausible one becomes: a guessed timestamp would park a task for a
	// window nobody promised. The engine falls back to
	// `usage_limit_recheck_interval` (§12.3) when it is nil.
	RetryAfter *time.Time
}

// Options are the selectable models/efforts of an adapter (spec §9.1, §9.6).
type Options struct {
	Models        []Option // known model ids/aliases; never exhaustive — free text is always accepted
	Efforts       []Option // adapter-native effort levels
	DefaultModel  string   // "" = the CLI decides
	DefaultEffort string   // "" = the CLI decides
	// InputSupport is the adapter's static mid-run input capability (task
	// 013). It rides in the catalog rather than behind a new Adapter method
	// because §8.2 validation already reads Curated() and must not probe.
	// The zero value is deliberately not a level: an adapter that says
	// nothing is treated as InputDetected — unjudged — so a catalog built by
	// a test or a future adapter never gates a workflow by accident.
	InputSupport InputSupport
}

// InputEverPossible reports whether an adapter with these options could ever
// take mid-run input. Only InputNever answers false; an unset level is
// unjudged, which is what keeps validation from gating on silence.
func (o Options) InputEverPossible() bool { return o.InputSupport != InputNever }

// Option is one selectable value with provenance (spec §9.6).
type Option struct {
	Value  string `json:"value"`
	Source string `json:"source"` // SourceCLI | SourceCurated
}

// Registry holds the configured adapters by name, in registration order.
type Registry struct {
	names    []string
	adapters map[string]Adapter
}

// NewRegistry builds a registry from adapters; order is preserved.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: make(map[string]Adapter, len(adapters))}
	for _, a := range adapters {
		r.names = append(r.names, a.Name())
		r.adapters[a.Name()] = a
	}
	return r
}

// Get returns the adapter with the given name.
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}

// Names returns the registered adapter names in registration order.
func (r *Registry) Names() []string { return r.names }

// All returns the adapters in registration order.
func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.adapters[n])
	}
	return out
}
