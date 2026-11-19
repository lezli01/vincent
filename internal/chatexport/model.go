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
	// Diff is the base-to-tip diff of the directory the chat works in today
	// (task 156 decision 1), present only when it was asked for and could be
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
}

// Turn is one exchange.
type Turn struct {
	Seq    int    `json:"seq"`
	State  string `json:"state"`
	Prompt string `json:"prompt"`
	// Tools is one entry per top-level tool call, in call order. ToolsNote
	// replaces it when the transcript is not there to read, saying why.
	Tools     []ToolCall `json:"tools"`
	ToolsNote string     `json:"tools_note,omitempty"`
	// Answer is the turn's result_text.
	Answer       string `json:"answer,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// CostUSD is absent, never zero, for an adapter that reports none
	// (copilot, §9.9).
	CostUSD      *float64  `json:"cost_usd,omitempty"`
	DurationMS   *int64    `json:"duration_ms,omitempty"`
	FailReason   string    `json:"fail_reason,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

// ToolCall is one tool call: what was called, on what, and whether it
// failed. The call's output is never here (task 156 decision 3).
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
	Tools        []struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		CallID  string `json:"call_id"`
	} `json:"tools"`
	Results []struct {
		CallID  string `json:"call_id"`
		IsError bool   `json:"is_error"`
	} `json:"results"`
}

// ToolsFromNormalized reads a normalized transcript (JSONL, one §13.2 record
// a line) and returns its top-level tool calls in call order, each marked
// failed when its agent.tool_result said so. A call made inside a subagent is
// the subagent's work and is left out, as the chat's compact level leaves it
// out of the turn's own line. Lines that do not decode are skipped: the
// export is a summary, and one odd record must not cost the reader the rest.
func ToolsFromNormalized(r io.Reader) ([]ToolCall, error) {
	calls := []ToolCall{}
	byID := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var rec normalizedRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.ParentCallID != "" {
			continue
		}
		switch rec.Type {
		case "agent.tool_use":
			for _, t := range rec.Tools {
				if t.CallID != "" {
					byID[t.CallID] = len(calls)
				}
				calls = append(calls, ToolCall{Name: t.Name, Arg: t.Summary})
			}
		case "agent.tool_result":
			for _, res := range rec.Results {
				if i, ok := byID[res.CallID]; ok && res.IsError {
					calls[i].Failed = true
				}
			}
		}
	}
	return calls, sc.Err()
}
