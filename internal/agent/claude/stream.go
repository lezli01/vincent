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
	var texts []string
	for _, b := range line.Message.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
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
	switch {
	case ev.Text != "":
		ev.Type = agent.EventOutput
	case len(ev.Tools) > 0:
		ev.Type = agent.EventToolUse
	}
	return ev
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
