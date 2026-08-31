package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// transcriptChunkSize bounds one backwards read while locating a line
// boundary. Transcript lines are far smaller than this, so the scan almost
// always ends in its first chunk.
const transcriptChunkSize = 64 * 1024

// lineBoundary reports the offset just past the last newline at or before
// pos: the nearest record boundary that does not overshoot.
//
// It answers both range questions. As an *end* it keeps a read off the middle
// of a record — a transcript is appended to while it is read, so its size is
// regularly mid-line, and handing that position back as a resume cursor would
// make the next fetch start inside a record (spec §13.2). As a *tail start* it
// snaps backwards into the record containing the requested position, so a
// window narrower than the last record still returns that record instead of
// nothing.
func lineBoundary(r io.ReaderAt, pos int64) (int64, error) {
	size := pos
	buf := make([]byte, transcriptChunkSize)
	for end := size; end > 0; {
		start := max(end-transcriptChunkSize, 0)
		n, err := r.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return start + int64(i) + 1, nil
			}
		}
		end = start
	}
	return 0, nil
}

// normalizeTranscript rewrites a raw JSONL range into the §13.3 live-output
// shapes so a client renders scrollback and the live tail with one code path
// (spec §13.2 format=normalized). parse is the owning adapter's line parser,
// or nil for a step that never ran an agent.
//
// The mapping is lossless by construction: vincent's own annotations pass
// through under their own names, and any line the parser does not recognize
// is emitted as agent.raw rather than dropped — a transcript that silently
// omits lines is not the durable record §12.2 promises.
func normalizeTranscript(w io.Writer, src io.Reader, parse agent.LineParser) error {
	br := bufio.NewReader(src)
	enc := json.NewEncoder(w)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if trimmed := trimLine(line); len(trimmed) > 0 {
				if encErr := enc.Encode(normalizeLine(trimmed, parse)); encErr != nil {
					return encErr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func trimLine(line []byte) []byte {
	return []byte(strings.TrimRight(string(line), "\r\n"))
}

// normalizedLine is one line of a normalized transcript. Every field is
// omitempty so each type carries only what it means; `type` is always set.
type normalizedLine struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Tools was `[]string` through M4 and became objects with T4.14: a bare
	// name renders as a keyword, and the subject a reader wants — the
	// command run, the file edited — has nowhere else to ride. Nothing
	// durable broke in the change: normalized transcripts are computed from
	// the raw file on every read and never stored (§13.2).
	Tools []toolLine `json:"tools,omitempty"`
	// Results carries an agent.tool_result record's outcomes (T4.16).
	Results      []resultLine `json:"results,omitempty"`
	Message      string       `json:"message,omitempty"`
	Raw          string       `json:"raw,omitempty"`
	Line         string       `json:"line,omitempty"`
	ResultText   string       `json:"result_text,omitempty"`
	IsError      bool         `json:"is_error,omitempty"`
	InputTokens  int64        `json:"input_tokens,omitempty"`
	OutputTokens int64        `json:"output_tokens,omitempty"`
	CostUSD      *float64     `json:"cost_usd,omitempty"`
	// ParentCallID rides on any record produced inside a subagent call
	// (task 066). It is carried on the wire ahead of any renderer: the pane
	// is flat today, and a transcript recorded now renders under whatever
	// nesting lands later, because §13.2 re-normalizes on every read.
	ParentCallID string `json:"parent_call_id,omitempty"`
	// WorkDir and AvailableTools are the agent.run_header record (task 066):
	// where the CLI said it was running and what it said it could reach.
	// The tool list cannot ride on `tools` — that is agent.tool_use's
	// objects, and a bare name list is a different shape.
	WorkDir        string   `json:"work_dir,omitempty"`
	AvailableTools []string `json:"available_tools,omitempty"`
	// The rest enrich agent.result with the run's own account of itself
	// (task 066). Durations are milliseconds, matching the dialect they came
	// from and needing no unit in the name a reader has to remember.
	DurationMS        int64                `json:"duration_ms,omitempty"`
	APIDurationMS     int64                `json:"api_duration_ms,omitempty"`
	NumTurns          int                  `json:"num_turns,omitempty"`
	StopReason        string               `json:"stop_reason,omitempty"`
	TerminalReason    string               `json:"terminal_reason,omitempty"`
	CacheReadTokens   int64                `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  int64                `json:"cache_write_tokens,omitempty"`
	ModelUsage        []modelUsageLine     `json:"model_usage,omitempty"`
	PermissionDenials []permissionDenyLine `json:"permission_denials,omitempty"`
}

// modelUsageLine is one model's share of a run. It carries what the run
// spent, never what the model is — §9.6's catalog answers that (§13.2).
type modelUsageLine struct {
	Model            string   `json:"model"`
	InputTokens      int64    `json:"input_tokens,omitempty"`
	OutputTokens     int64    `json:"output_tokens,omitempty"`
	CacheReadTokens  int64    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64    `json:"cache_write_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
}

// permissionDenyLine is one tool call a permission rule refused over the
// whole run — the run-level counterpart of a result's `blocked` (§13.2).
type permissionDenyLine struct {
	ToolName string `json:"tool_name,omitempty"`
	CallID   string `json:"call_id,omitempty"`
}

// resultLine reports one tool invocation's outcome. It never carries the
// tool's output body — that is in the transcript, and a single grep result
// would otherwise drown the pane (§13.2, T4.16).
type resultLine struct {
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Verb is the dialect's structured outcome ("created"), absent when it
	// reported none; Blocked marks a call a permission rule refused, which
	// is a different thing from one that ran and failed (task 066).
	Verb    string `json:"verb,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// toolLine names one tool invocation inside an agent.tool_use record.
// Summary is the call's subject and is absent when the dialect's arguments
// carried nothing recognizable; call_id correlates the invocation with the
// agent.tool_result that reports its outcome (§13.2, T4.14).
type toolLine struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	CallID  string `json:"call_id,omitempty"`
}

// toolLines maps normalized tool uses onto their wire shape.
func toolLines(tools []agent.ToolUse) []toolLine {
	out := make([]toolLine, 0, len(tools))
	for _, t := range tools {
		out = append(out, toolLine{Name: t.Name, Summary: t.Summary, CallID: t.CallID})
	}
	return out
}

// resultLines maps normalized tool results onto their wire shape.
func resultLines(results []agent.ToolResult) []resultLine {
	out := make([]resultLine, 0, len(results))
	for _, r := range results {
		out = append(out, resultLine{
			CallID: r.CallID, Name: r.Name, Summary: r.Summary,
			Verb: r.Verb, Blocked: r.Blocked, IsError: r.IsError,
		})
	}
	return out
}

// modelUsageLines maps a run's per-model breakdown onto its wire shape.
func modelUsageLines(usage []agent.ModelUsage) []modelUsageLine {
	if len(usage) == 0 {
		return nil
	}
	out := make([]modelUsageLine, 0, len(usage))
	for _, u := range usage {
		out = append(out, modelUsageLine{
			Model:            u.Model,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheCreationTokens,
			CostUSD:          u.CostUSD,
		})
	}
	return out
}

// permissionDenyLines maps the run-level denial list onto its wire shape.
func permissionDenyLines(denials []agent.PermissionDenial) []permissionDenyLine {
	if len(denials) == 0 {
		return nil
	}
	out := make([]permissionDenyLine, 0, len(denials))
	for _, d := range denials {
		out = append(out, permissionDenyLine{ToolName: d.ToolName, CallID: d.CallID})
	}
	return out
}

// vincentLine sniffs vincent's own annotations, which are already normalized
// — they are what the engine chose to record about its own run.
func vincentLine(raw []byte) (json.RawMessage, bool) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, false
	}
	if !strings.HasPrefix(probe.Type, "vincent.") {
		return nil, false
	}
	return json.RawMessage(raw), true
}

// normalizeLine maps one transcript line to its normalized form.
func normalizeLine(raw []byte, parse agent.LineParser) any {
	if passthrough, ok := vincentLine(raw); ok {
		return passthrough
	}
	if parse == nil {
		// A step with no agent: nothing but vincent's own lines is expected,
		// so anything else is surfaced verbatim rather than guessed at.
		return normalizedLine{Type: "agent.raw", Line: string(raw)}
	}
	return normalizedEvent(parse(raw), raw)
}

// normalizedEvent maps one parsed event onto its wire record. It is split
// from normalizeLine so that ParentCallID — which rides on any record, of any
// type — is attached in one place rather than in each arm (task 066).
func normalizedEvent(ev agent.Event, raw []byte) normalizedLine {
	out := normalizedRecord(ev, raw)
	out.ParentCallID = ev.ParentCallID
	return out
}

func normalizedRecord(ev agent.Event, raw []byte) normalizedLine {
	switch ev.Type {
	case agent.EventOutput:
		return normalizedLine{Type: "agent.output", Text: ev.Text}
	case agent.EventToolUse:
		return normalizedLine{Type: "agent.tool_use", Tools: toolLines(ev.Tools)}
	case agent.EventToolResult:
		return normalizedLine{Type: "agent.tool_result", Results: resultLines(ev.Results)}
	case agent.EventThinking:
		return normalizedLine{Type: "agent.thinking", Text: ev.Text}
	case agent.EventUsage:
		return normalizedLine{Type: "agent.usage", Raw: string(ev.Raw)}
	case agent.EventRunHeader:
		out := normalizedLine{Type: "agent.run_header"}
		if ev.Header != nil {
			out.WorkDir = ev.Header.WorkDir
			out.AvailableTools = ev.Header.Tools
		}
		return out
	case agent.EventError:
		return normalizedLine{Type: "agent.error", Message: ev.Message}
	case agent.EventResult:
		out := normalizedLine{Type: "agent.result"}
		if ev.Result != nil {
			out.ResultText = ev.Result.ResultText
			out.IsError = ev.Result.IsError
			out.Message = ev.Result.ErrorMessage
			out.InputTokens = ev.Result.InputTokens
			out.OutputTokens = ev.Result.OutputTokens
			out.CostUSD = ev.Result.CostUSD
			out.DurationMS = ev.Result.Duration.Milliseconds()
			out.APIDurationMS = ev.Result.APIDuration.Milliseconds()
			out.NumTurns = ev.Result.NumTurns
			out.StopReason = ev.Result.StopReason
			out.TerminalReason = ev.Result.TerminalReason
			out.CacheReadTokens = ev.Result.CacheReadTokens
			out.CacheWriteTokens = ev.Result.CacheCreationTokens
			out.ModelUsage = modelUsageLines(ev.Result.ModelUsage)
			out.PermissionDenials = permissionDenyLines(ev.Result.PermissionDenials)
		}
		return out
	case agent.EventInputRequest, agent.EventInputCanceled, agent.EventUnknown:
		// Input control messages are recorded readably by the engine as
		// vincent.input_* annotations; their wire lines fall through here with
		// everything else the dialect does not normalize.
		return normalizedLine{Type: "agent.raw", Line: string(raw)}
	default:
		return normalizedLine{Type: "agent.raw", Line: string(raw)}
	}
}

// transcriptParser returns the line parser for a step run's agent, or nil for
// a step that ran no agent (command, check, manual gate).
func (s *Server) transcriptParser(agentName string) agent.LineParser {
	if agentName == "" || s.deps.Agents == nil {
		return nil
	}
	a, ok := s.deps.Agents.Get(agentName)
	if !ok {
		return nil
	}
	return a.NewLineParser()
}
