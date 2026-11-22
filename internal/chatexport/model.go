package chatexport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

// Version is the export's shape version, carried as JSON's top-level
// `version` so the document can grow without a reader guessing.
const Version = 1

// Format is one of the three renderings.
type Format string

// The formats, spelled as `?format=` spells them.
const (
	FormatMarkdown Format = "md"
	FormatHTML     Format = "html"
	FormatJSON     Format = "json"
)

// ErrFormat is a format this build does not render.
var ErrFormat = errors.New("format must be md, html or json")

// ParseFormat reads a `?format=` / `--format` value; empty is Markdown.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "", FormatMarkdown:
		return FormatMarkdown, nil
	case FormatHTML:
		return FormatHTML, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("%w; got %q", ErrFormat, s)
	}
}

// ContentType is the response's Content-Type for f.
func (f Format) ContentType() string {
	switch f {
	case FormatHTML:
		return "text/html; charset=utf-8"
	case FormatJSON:
		return "application/json"
	default:
		return "text/markdown; charset=utf-8"
	}
}

// Ext is f's file extension, without the dot.
func (f Format) Ext() string {
	if f == "" {
		return string(FormatMarkdown)
	}
	return string(f)
}

// Filename is the suggested name for an export: `chat-<id>-<slug>.<ext>`,
// the slug drawn from the title (or omitted when the title has no letters
// or digits). The API sends it in Content-Disposition and the TUI prefills
// its path field with it; the CLI never invents one.
func Filename(id int64, title string, f Format) string {
	if slug := Slug(title); slug != "" {
		return fmt.Sprintf("chat-%d-%s.%s", id, slug, f.Ext())
	}
	return fmt.Sprintf("chat-%d.%s", id, f.Ext())
}

// slugMax bounds a filename's slug so a long first prompt does not become a
// path a filesystem refuses.
const slugMax = 40

// Slug is title lowercased, with every run of anything but an ASCII letter or
// digit collapsed to one hyphen, trimmed, and cut to slugMax at a hyphen
// where one is near. ASCII only on purpose: the name is a file on whatever
// filesystem the reader has.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	s := b.String()
	if len(s) > slugMax {
		s = s[:slugMax]
		if i := strings.LastIndexByte(s, '-'); i > slugMax/2 {
			s = s[:i]
		}
		s = strings.TrimRight(s, "-")
	}
	return s
}

// Export is the document model. JSON is this struct marshalled; Markdown and
// HTML are views of it.
type Export struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Chat       Chat      `json:"chat"`
	Turns      []Turn    `json:"turns"`
	// ShellRuns are the chat's shell passthrough runs (task 170), oldest
	// first. Each sits in the conversation after the turn its AfterTurn
	// names, which is where the Markdown and HTML renderings draw it.
	ShellRuns []ShellRun `json:"shell_runs"`
	// Diff is the base-to-tip diff of the directory the chat works in today
	// (task 159 decision 1), present only when it was asked for and could be
	// produced; an empty string is a diff with no changes. DiffUnavailable
	// says why an asked-for diff is absent. Neither is set when no diff was
	// asked for.
	Diff            *string `json:"diff"`
	DiffUnavailable string  `json:"diff_unavailable,omitempty"`
}

// Chat is the export's metadata block.
type Chat struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	ProjectID      int64     `json:"project_id"`
	Project        string    `json:"project,omitempty"`
	Agent          string    `json:"agent"`
	Model          string    `json:"model,omitempty"`
	Effort         string    `json:"effort,omitempty"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	State          string    `json:"state"`
	Branch         string    `json:"branch,omitempty"`
	BaseBranch     string    `json:"base_branch,omitempty"`
	BaseSHA        string    `json:"base_sha,omitempty"`
	LinkedTaskID   *int64    `json:"linked_task_id,omitempty"`
	HandoffTaskID  *int64    `json:"handoff_task_id,omitempty"`
	IssueID        *int64    `json:"issue_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// ForkedFrom is where a fork was cut from (task 167, task 171 decision
	// 5), absent for a chat that is not one.
	ForkedFrom *ForkedFrom `json:"forked_from,omitempty"`
	// PendingPreamble is turn 0 not yet run (task 171 decision 4): the
	// seeded context the chat's next turn will start a fresh session with,
	// still editable. Absent when no seeded turn is pending.
	PendingPreamble string `json:"pending_preamble,omitempty"`
}

// ForkedFrom is a fork's source: the chat, the turn it was cut before, and
// the source's agent ("" on a fork from before it was recorded).
type ForkedFrom struct {
	ChatID int64  `json:"chat_id"`
	Seq    int    `json:"seq"`
	Agent  string `json:"agent,omitempty"`
}

// Label is the provenance as every rendering words it: `#4 (claude),
// before turn 3`.
func (f *ForkedFrom) Label() string {
	s := fmt.Sprintf("#%d", f.ChatID)
	if f.Agent != "" {
		s += " (" + f.Agent + ")"
	}
	return fmt.Sprintf("%s, before turn %d", s, f.Seq)
}

// Turn is one exchange.
type Turn struct {
	Seq    int    `json:"seq"`
	State  string `json:"state"`
	Prompt string `json:"prompt"`
	// MentionContext is what the prompt's `#issue-N`, `#task-N`, `#pr-N` and
	// `@path#Lx-y` mentions expanded into when the turn started (task 166
	// decision 2): the snapshot the agent saw, absent when nothing expanded.
	MentionContext string `json:"mention_context,omitempty"`
	// Tools is one entry per top-level tool call, in call order. ToolsNote
	// replaces it when the transcript is not there to read, saying why.
	Tools     []ToolCall `json:"tools"`
	ToolsNote string     `json:"tools_note,omitempty"`
	// Answer is the turn's result_text.
	Answer       string `json:"answer,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// The cache counts are absent for an adapter whose input already counts
	// the cache, or that reports none: claude's and copilot's input leaves
	// out what the turn read from and wrote to the cache (task 157 decision
	// 3), so without them the line understates what the turn processed.
	CacheReadTokens     *int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *int64 `json:"cache_creation_tokens,omitempty"`
	// ContextTokens and ContextWindow are the context gauge's figures for
	// this turn (§5.5), absent when the adapter did not report them.
	ContextTokens *int64 `json:"context_tokens,omitempty"`
	ContextWindow *int64 `json:"context_window,omitempty"`
	// CostUSD is absent, never zero, for an adapter that reports none
	// (copilot, §9.9).
	CostUSD      *float64  `json:"cost_usd,omitempty"`
	DurationMS   *int64    `json:"duration_ms,omitempty"`
	FailReason   string    `json:"fail_reason,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	// Model, Effort and PermissionMode are what the turn ran with (task
	// 162 decision 7). All three are null on a turn from before they were
	// recorded — "not recorded", never the chat's current settings — and
	// Model or Effort alone is null when the turn ran on the adapter's
	// default.
	Model          *string `json:"model"`
	Effort         *string `json:"effort"`
	PermissionMode *string `json:"permission_mode"`
	// RewoundAt marks a turn a rewind took out of the conversation (task
	// 167 decision 9). An export leaves such turns out unless it was asked
	// for them, so one present here was asked for, and says so.
	RewoundAt *time.Time `json:"rewound_at,omitempty"`
	RewindID  string     `json:"rewind_id,omitempty"`
	// Seeded marks a turn that started a fresh session from a bounded
	// summary of the earlier turns rather than resuming the agent's own
	// (task 167 decision G): what it knew is not what the turns above say.
	Seeded bool `json:"seeded,omitempty"`
	// SeededContext is what a seeded turn's fresh session was given — its
	// turn 0 (task 171 decision 4) — read from the transcript's
	// `vincent.seeded` note. Absent on a turn that was not seeded, that was
	// seeded with nothing, or whose transcript is gone.
	SeededContext string `json:"seeded_context,omitempty"`
}

// TurnZeroHeading names a seeded context wherever an export shows one: it
// is a summary of earlier turns, never the earlier session itself.
const TurnZeroHeading = "Turn 0 · seeded context (summary)"

// NotePendingPreamble introduces a chat's pending turn 0.
const NotePendingPreamble = "The next turn starts a fresh agent session with this summary ahead of its message. It can still be edited."

// The notes a turn's marks render as, one wording for every format.
const (
	NoteRewound = "Rewound: a rewind took this turn out of the conversation; the agent no longer remembers it."
	NoteSeeded  = "Seeded, not resumed: this turn started a new agent session from a summary of the turns before it."
)

// Notes are the turn's marks as sentences, in a fixed order; none for an
// ordinary turn.
func (t *Turn) Notes() []string {
	var out []string
	if t.RewoundAt != nil {
		out = append(out, NoteRewound)
	}
	if t.Seeded {
		out = append(out, NoteSeeded)
	}
	return out
}

// ShellRun is one shell command a human ran in the chat (task 170): its
// command, how it ended, its stored output, and whether the agent was given
// it. Unlike a tool call's, its output is in the document: it is what the
// human saw, and what an attached run handed the agent.
type ShellRun struct {
	ID      int64  `json:"id"`
	Command string `json:"command"`
	// State is running, exited, timed_out, interrupted, canceled or failed;
	// ExitCode is set for an exited run only.
	State     string     `json:"state"`
	ExitCode  *int       `json:"exit_code"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// AfterTurn is the seq of the last turn that existed when the run
	// started, 0 for none.
	AfterTurn int `json:"after_turn"`
	// Output is the stored, capped output; Truncated says the daemon's cap
	// cut it. OutputNote replaces it when the file is not there to read.
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated,omitempty"`
	OutputNote string `json:"output_note,omitempty"`
	// Attach is a finished run marked for the next prompt and not yet sent;
	// AttachedTurn is the seq of the turn that was sent it.
	Attach       bool `json:"attach,omitempty"`
	AttachedTurn *int `json:"attached_turn"`
}

// NoteShellOutputMissing is a shell run whose output file is gone.
const NoteShellOutputMissing = "output unavailable: the stored output could not be read"

// Status is the run's ending: `exited 3`, or its state.
func (r *ShellRun) Status() string {
	if r.State == "exited" && r.ExitCode != nil {
		return fmt.Sprintf("exited %d", *r.ExitCode)
	}
	return r.State
}

// Sent says whether the agent was given the run.
func (r *ShellRun) Sent() string {
	switch {
	case r.AttachedTurn != nil:
		return fmt.Sprintf("sent to the agent with turn %d", *r.AttachedTurn)
	case r.Attach:
		return "attached to the next prompt"
	default:
		return "not sent to the agent"
	}
}

// ToolCall is one tool call: what was called, on what, and whether it
// failed. The call's output is never here (task 159 decision 3).
type ToolCall struct {
	Name   string `json:"name"`
	Arg    string `json:"arg,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// ToolSummary is a tool call's one-line form: its name, then its key
// argument (file path, command, pattern or URL) when the dialect reported
// one. It is the one definition of that line: `vincent task transcript`,
// `vincent chat transcript` and the export all print it.
func ToolSummary(name, arg string) string {
	return strings.TrimSpace(name + " " + arg)
}

// Summary is c's one-line form, without its failed marker.
func (c ToolCall) Summary() string { return ToolSummary(c.Name, c.Arg) }

// Notes for a turn whose tool list cannot be read.
const (
	// NotePruned is a transcript the retention sweep removed (§12.2).
	NotePruned = "tool calls unavailable: the transcript was pruned (transcript_retention_days)"
	// NoteNeverWritten is a turn that ended before writing one (task 103
	// decision 4: agent_unavailable, transcript_io_error).
	NoteNeverWritten = "tool calls unavailable: this turn never wrote a transcript"
)

// normalizedRecord is the part of a normalized transcript record (§13.2
// format=normalized) the export reads.
type normalizedRecord struct {
	Type         string `json:"type"`
	ParentCallID string `json:"parent_call_id"`
	// Context is a vincent.seeded note's seeded context.
	Context string `json:"context"`
	Tools   []struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		CallID  string `json:"call_id"`
	} `json:"tools"`
	Results []struct {
		CallID  string `json:"call_id"`
		IsError bool   `json:"is_error"`
	} `json:"results"`
}

// FromNormalized is what an export reads off one turn's normalized
// transcript: its top-level tool calls, and the context a seeded turn was
// given.
type FromNormalized struct {
	Tools         []ToolCall
	SeededContext string
}

// ReadNormalized is ToolsFromNormalized that also keeps the `context` of the
// turn's `vincent.seeded` note (task 171 decision 4).
func ReadNormalized(r io.Reader) (FromNormalized, error) {
	out := FromNormalized{Tools: []ToolCall{}}
	byID := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var rec normalizedRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.ParentCallID != "" {
			continue
		}
		switch rec.Type {
		case "vincent.seeded":
			if rec.Context != "" {
				out.SeededContext = rec.Context
			}
		case "agent.tool_use":
			for _, t := range rec.Tools {
				if t.CallID != "" {
					byID[t.CallID] = len(out.Tools)
				}
				out.Tools = append(out.Tools, ToolCall{Name: t.Name, Arg: t.Summary})
			}
		case "agent.tool_result":
			for _, res := range rec.Results {
				if i, ok := byID[res.CallID]; ok && res.IsError {
					out.Tools[i].Failed = true
				}
			}
		}
	}
	return out, sc.Err()
}

// ToolsFromNormalized reads a normalized transcript (JSONL, one §13.2 record
// a line) and returns its top-level tool calls in call order, each marked
// failed when its agent.tool_result said so. A call made inside a subagent is
// the subagent's work and is left out, as the chat's compact level leaves it
// out of the turn's own line. Lines that do not decode are skipped: the
// export is a summary, and one odd record must not cost the reader the rest.
func ToolsFromNormalized(r io.Reader) ([]ToolCall, error) {
	n, err := ReadNormalized(r)
	return n.Tools, err
}
