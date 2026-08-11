package claude

import (
	"encoding/json"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// streamLine is the superset of stream-json fields vincent reads (pinned
// against claude 2.1.x). Parsing is tolerant: unknown event types become
// EventUnknown, transcripted verbatim but not normalized (phase 1 decision).
type streamLine struct {
	Type         string         `json:"type"`
	Subtype      string         `json:"subtype"`
	Message      *streamMessage `json:"message"`
	IsError      bool           `json:"is_error"`
	Result       string         `json:"result"`
	TotalCostUSD *float64       `json:"total_cost_usd"`
	Usage        *streamUsage   `json:"usage"`
}

type streamMessage struct {
	Content []streamBlock `json:"content"`
}

type streamBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
	// Thinking is the reasoning text of a thinking block. The block also
	// carries a `signature` — an opaque attestation blob — which is
	// deliberately not read: it is not text and must never reach a pane.
	Thinking string `json:"thinking"`
	// ToolUseID, Content and IsError describe a tool_result block, which
	// arrives on a `user` line rather than an assistant one (T4.16).
	// Content is raw because claude sends either a string or an array of
	// blocks depending on the tool.
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	// ID and Input describe a tool_use block: the id a later tool_result
	// refers back to, and the tool's arguments — free-form per tool, so they
	// are kept raw and read by agent.ToolSummary (T4.14).
	ID    string          `json:"id"`
	Input json.RawMessage `json:"input"`
}

type streamUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// NewLineParser returns a parser for claude transcript lines (§13.2
// format=normalized). Claude's dialect is stateless — every line stands on
// its own — so each call hands back the same pure function.
func (a *Adapter) NewLineParser() agent.LineParser { return parseLine }

// parseLine normalizes one verbatim stream-json line into an agent.Event.
// The raw line always rides along for lossless transcripts.
func parseLine(raw []byte) agent.Event {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	switch line.Type {
	case "assistant":
		return parseAssistant(&line, raw)
	case "user":
		// A `user` line is claude replaying tool results back to the model.
		// It is the only place an invocation's outcome is reported, so it is
		// normalized rather than left to fall through as raw (T4.16).
		return parseToolResults(&line, raw)
	case "result":
		return parseResult(&line, raw)
	default:
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
}

func parseAssistant(line *streamLine, raw []byte) agent.Event {
	ev := agent.Event{Type: agent.EventUnknown, Raw: raw}
	if line.Message == nil {
		return ev
	}
	var texts, thoughts []string
	for _, b := range line.Message.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "thinking":
			if b.Thinking != "" {
				thoughts = append(thoughts, b.Thinking)
			}
		case "tool_use":
			ev.Tools = append(ev.Tools, agent.ToolUse{
				Name:    b.Name,
				Summary: agent.ToolSummary(b.Input),
				CallID:  b.ID,
			})
		}
	}
	ev.Text = strings.Join(texts, "\n")
	// Order is precedence, not preference: one line can hold reasoning, text
	// and tool calls at once, and it normalizes to a single event. Assistant
	// text wins because it is what the run is for, tool calls next, and
	// thinking last — a line that is *only* thinking is the case worth
	// surfacing, and claude delivers those blocks whole, so nothing has to be
	// coalesced the way cursor's deltas do (§9.7).
	switch {
	case ev.Text != "":
		ev.Type = agent.EventOutput
	case len(ev.Tools) > 0:
		ev.Type = agent.EventToolUse
	case len(thoughts) > 0:
		ev.Type = agent.EventThinking
		ev.Text = strings.Join(thoughts, "\n")
	}
	return ev
}

// parseToolResults normalizes the tool_result blocks of a `user` line. A line
// with none — claude also replays the original prompt this way — stays
// unknown rather than becoming an empty result event.
func parseToolResults(line *streamLine, raw []byte) agent.Event {
	ev := agent.Event{Type: agent.EventUnknown, Raw: raw}
	if line.Message == nil {
		return ev
	}
	for _, b := range line.Message.Content {
		if b.Type != "tool_result" {
			continue
		}
		ev.Results = append(ev.Results, agent.ToolResult{
			CallID:  b.ToolUseID,
			Summary: resultSummary(b.Content),
			IsError: b.IsError,
		})
	}
	if len(ev.Results) > 0 {
		ev.Type = agent.EventToolResult
	}
	return ev
}

// resultSummaryMax caps a tool result at roughly one pane line. The outcome
// is the point — "did that work" — and the tool's full output is in the
// transcript, where a reader who wants 500 lines of grep hits can find it.
const resultSummaryMax = 120

// resultSummary reduces a tool_result's content to one line. Claude sends
// either a bare string or an array of content blocks depending on the tool,
// so both are decoded and anything else yields nothing rather than a guess.
func resultSummary(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return agent.OneLine(text, resultSummaryMax)
	}
	var blocks []streamBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return agent.OneLine(strings.Join(texts, " "), resultSummaryMax)
}

func parseResult(line *streamLine, raw []byte) agent.Event {
	res := agent.RunResult{
		IsError:    line.IsError || (line.Subtype != "" && line.Subtype != "success"),
		ResultText: line.Result,
		CostUSD:    line.TotalCostUSD,
	}
	if res.IsError {
		res.ErrorMessage = line.Result
		if res.ErrorMessage == "" {
			res.ErrorMessage = line.Subtype
		}
	}
	if line.Usage != nil {
		res.InputTokens = line.Usage.InputTokens
		res.OutputTokens = line.Usage.OutputTokens
	}
	return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
}
