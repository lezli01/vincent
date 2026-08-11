package codex

import (
	"encoding/json"
	"fmt"

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
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`    // agent_message
	Message string `json:"message"` // item type=error (advisory notices)
	// raw is the item object as it arrived. The field that says *what* a
	// tool item is doing differs per item type (`command` for
	// command_execution, `query` for web_search, …), and enumerating them
	// would mean inventing names for the types no capture exists for; the
	// raw object lets agent.ToolSummary read whichever is present and
	// return nothing when none is (T4.14).
	raw json.RawMessage
}

// UnmarshalJSON keeps the item's own bytes alongside the decoded fields.
// The local type drops the method set, so this does not recurse.
func (it *execItem) UnmarshalJSON(b []byte) error {
	type item execItem
	var decoded item
	if err := json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	*it = execItem(decoded)
	it.raw = append(json.RawMessage(nil), b...)
	return nil
}

type execUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type execError struct {
	Message string `json:"message"`
}

// result describes a completed tool item. `exit_code` and `status` are the
// two fields the captured shapes carry; an item type that reports neither
// yields a bare success, because "completed with nothing to say" is what a
// completion without a failure signal means. Nothing here guesses at fields
// no capture contains — the same rule that keeps codex reasoning filed as
// T4.17 rather than implemented blind.
func (it *execItem) result() agent.ToolResult {
	var fields struct {
		ExitCode *int   `json:"exit_code"`
		Status   string `json:"status"`
	}
	res := agent.ToolResult{CallID: it.ID, Name: it.Type}
	if err := json.Unmarshal(it.raw, &fields); err != nil {
		return res
	}
	if fields.ExitCode != nil {
		res.Summary = fmt.Sprintf("exit %d", *fields.ExitCode)
		res.IsError = *fields.ExitCode != 0
		return res
	}
	if fields.Status != "" && fields.Status != "completed" {
		res.Summary = fields.Status
		res.IsError = fields.Status == "failed"
	}
	return res
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
				Type: agent.EventToolUse,
				Tools: []agent.ToolUse{{
					Name:    line.Item.Type,
					Summary: agent.ToolSummary(line.Item.raw),
					CallID:  line.Item.ID,
				}},
				Raw: raw,
			}
		}
	case "item.completed":
		if line.Item == nil {
			break
		}
		if toolItems[line.Item.Type] {
			// The completion of a tool item is its outcome, correlated to
			// the item.started that opened it by the shared item id (T4.16).
			return agent.Event{
				Type:    agent.EventToolResult,
				Results: []agent.ToolResult{line.Item.result()},
				Raw:     raw,
			}
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
