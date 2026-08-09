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
	Type         string   `json:"type"`
	Text         string   `json:"text,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Message      string   `json:"message,omitempty"`
	Raw          string   `json:"raw,omitempty"`
	Line         string   `json:"line,omitempty"`
	ResultText   string   `json:"result_text,omitempty"`
	IsError      bool     `json:"is_error,omitempty"`
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
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
	ev := parse(raw)
	switch ev.Type {
	case agent.EventOutput:
		return normalizedLine{Type: "agent.output", Text: ev.Text}
	case agent.EventToolUse:
		names := make([]string, 0, len(ev.Tools))
		for _, t := range ev.Tools {
			names = append(names, t.Name)
		}
		return normalizedLine{Type: "agent.tool_use", Tools: names}
	case agent.EventUsage:
		return normalizedLine{Type: "agent.usage", Raw: string(ev.Raw)}
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
