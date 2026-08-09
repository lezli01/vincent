package codex

import (
	"encoding/json"

	"github.com/lezli01/vincent/internal/agent"
)

// execLine is the superset of `codex exec --json` JSONL fields vincent reads
// (pinned against codex-cli 0.142.5; fixtures in testdata/ were captured
// from real runs). Parsing is tolerant: unknown event types become
// EventUnknown, transcripted verbatim but not normalized (phase 1 decision).
type execLine struct {
	Type    string     `json:"type"`
	Message string     `json:"message"` // type=error
	Item    *execItem  `json:"item"`
	Usage   *execUsage `json:"usage"` // turn.completed
	Err     *execError `json:"error"` // turn.failed
}

type execItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`    // agent_message
	Message string `json:"message"` // item type=error (advisory notices)
}

type execUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type execError struct {
	Message string `json:"message"`
}

// toolItems are the item types surfaced as tool_use when they start —
// item.started is the moment the agent invokes something, the analog of a
// claude tool_use content block.
var toolItems = map[string]bool{
	"command_execution": true,
	"mcp_tool_call":     true,
	"file_change":       true,
	"web_search":        true,
}

// stream normalizes codex JSONL lines into agent.Events. Unlike claude,
// codex has no single result event: the result text is the last
// agent_message and usage arrives with turn.completed, so normalization
// carries state across lines.
type stream struct {
	lastMessage string
}

// NewLineParser returns a parser for codex transcript lines (§13.2
// format=normalized). Each call gets its own stream: codex's result text is
// the last agent_message it saw, so two files must not share the state.
func (a *Adapter) NewLineParser() agent.LineParser {
	s := &stream{}
	return s.parse
}

// parse normalizes one verbatim JSONL line into an agent.Event. The raw
// line always rides along for lossless transcripts.
func (s *stream) parse(raw []byte) agent.Event {
	var line execLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	switch line.Type {
	case "item.started":
		if line.Item != nil && toolItems[line.Item.Type] {
			return agent.Event{
				Type:  agent.EventToolUse,
				Tools: []agent.ToolUse{{Name: line.Item.Type}},
				Raw:   raw,
			}
		}
	case "item.completed":
		if line.Item == nil {
			break
		}
		switch line.Item.Type {
		case "agent_message":
			if line.Item.Text != "" {
				s.lastMessage = line.Item.Text
				return agent.Event{Type: agent.EventOutput, Text: line.Item.Text, Raw: raw}
			}
		case "error":
			// Advisory error items (e.g. model-metadata notices) are not
			// terminal; turn.failed decides the outcome.
			return agent.Event{Type: agent.EventError, Message: line.Item.Message, Raw: raw}
		}
	case "error":
		return agent.Event{Type: agent.EventError, Message: line.Message, Raw: raw}
	case "turn.completed":
		res := agent.RunResult{ResultText: s.lastMessage}
		if line.Usage != nil {
			res.InputTokens = line.Usage.InputTokens
			res.OutputTokens = line.Usage.OutputTokens
		}
		return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
	case "turn.failed":
		res := agent.RunResult{IsError: true, ResultText: s.lastMessage}
		if line.Err != nil {
			res.ErrorMessage = line.Err.Message
		}
		if res.ErrorMessage == "" {
			res.ErrorMessage = "codex turn failed"
		}
		return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
	}
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
}
