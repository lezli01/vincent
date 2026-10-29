package copilot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// streamLine is the superset of `copilot --output-format json` fields vincent
// reads (pinned against 1.0.105; fixtures in testdata/ were captured from real
// runs). Every line but the last is an envelope — `{type, data, id,
// timestamp, parentId, ephemeral?}` — with the payload under `data`. The
// terminal `result` line is the exception: no `data`, no `id`, its fields at
// the top level. Parsing is tolerant: unknown event types become
// EventUnknown, transcripted verbatim but not normalized (phase 1 decision).
type streamLine struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	// SessionID and ExitCode are the result line's own (type=result). Its
	// `usage` — premiumRequests, durations, code changes — stays unread
	// here: usage normalization is task 138.7's, and premium requests never
	// become CostUSD (task 138 non-goals).
	SessionID string `json:"sessionId"`
	ExitCode  *int   `json:"exitCode"`
	// AgentID is set on a subagent's lines and absent on the root agent's
	// (subagent_1.0.105.jsonl). A subagent's error or abort is the subagent's
	// own, and never decides how the run ended.
	AgentID string `json:"agentId"`
}

// messageData is assistant.message_delta's and assistant.message's payload.
type messageData struct {
	MessageID    string `json:"messageId"`
	DeltaContent string `json:"deltaContent"` // assistant.message_delta
	Content      string `json:"content"`      // assistant.message
}

// toolData is tool.execution_start's and tool.execution_complete's payload.
// Arguments is kept raw: it is an object for most tools and a bare string for
// `apply_patch` (edit_1.0.105.jsonl).
type toolData struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`  // start
	Arguments  json.RawMessage `json:"arguments"` // start
	// MCPServerName and MCPToolName are on an MCP tool's start line:
	// toolName is the two joined with a dash (mcp_1.0.105.jsonl), which a
	// server or tool name containing a dash makes ambiguous to split back.
	MCPServerName string `json:"mcpServerName"` // start
	MCPToolName   string `json:"mcpToolName"`   // start
	Success       *bool  `json:"success"`       // complete
	Result        *struct {
		Content string `json:"content"`
	} `json:"result"` // complete, on success
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"` // complete, on failure
}

// stream normalizes one copilot run's lines. It carries state because the
// dialect spreads what vincent records across lines: assistant text arrives
// as deltas, a tool result names its call only by id, and the terminal
// `result` line carries no answer text and no error message — those are
// whatever the stream said before it.
type stream struct {
	// deltaID and deltas accumulate the message being streamed, so a
	// message whose closing line came without content still has its text.
	deltaID string
	deltas  strings.Builder
	// last is the last assistant message's text: the run's answer, which the
	// result line does not repeat.
	last string
	// tools maps a toolCallId to the tool name its start line gave, because
	// the complete line does not repeat it.
	tools map[string]string
	// sessionError is the root agent's last session.error message. It only
	// explains a nonzero exit: a session.error is "for timeline display" in
	// the 1.0.105 schema, and a rate-limited turn can switch models and still
	// finish cleanly, so the result line's exitCode decides (§9.9).
	sessionError string
	// aborted is the reason an `abort` line gave. A run stopped under SIGTERM
	// still ends `result{exitCode:0}` (sigterm_1.0.105.jsonl), so this is
	// what keeps it from reading as a success (§9.9).
	aborted string
}

// NewLineParser returns a parser for copilot transcript lines (§13.2
// format=normalized). Each call gets its own stream: a half-accumulated
// message must not leak between two files.
func (a *Adapter) NewLineParser() agent.LineParser {
	s := &stream{}
	return s.parse
}

// parse normalizes one verbatim line into an agent.Event. The raw line always
// rides along for lossless transcripts.
func (s *stream) parse(raw []byte) agent.Event {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	switch line.Type {
	case "assistant.message_delta":
		// Coalesced into the closing assistant.message, the way cursor
		// coalesces thinking: a live tail of token fragments buries the text
		// it exists to show.
		var d messageData
		if json.Unmarshal(line.Data, &d) == nil {
			if d.MessageID != s.deltaID {
				s.deltaID = d.MessageID
				s.deltas.Reset()
			}
			s.deltas.WriteString(d.DeltaContent)
		}
	case "assistant.message":
		var d messageData
		if json.Unmarshal(line.Data, &d) == nil {
			text := d.Content
			if text == "" && d.MessageID == s.deltaID {
				text = s.deltas.String()
			}
			s.deltaID = ""
			s.deltas.Reset()
			// A message that only requests tools has no content; the
			// requests are reported by tool.execution_start, so the line
			// itself stays unmodeled rather than becoming empty output.
			if text != "" {
				s.last = text
				return agent.Event{Type: agent.EventOutput, Text: text, Raw: raw}
			}
		}
	case "tool.execution_start":
		var d toolData
		if json.Unmarshal(line.Data, &d) == nil && d.ToolName != "" {
			if s.tools == nil {
				s.tools = map[string]string{}
			}
			s.tools[d.ToolCallID] = d.ToolName
			return agent.Event{
				Type: agent.EventToolUse,
				Tools: []agent.ToolUse{{
					Name:      d.ToolName,
					Summary:   agent.ToolSummary(d.Arguments),
					CallID:    d.ToolCallID,
					MCPServer: d.MCPServerName,
					MCPTool:   d.MCPToolName,
				}},
				Raw: raw,
			}
		}
	case "tool.execution_complete":
		var d toolData
		if json.Unmarshal(line.Data, &d) == nil && d.ToolCallID != "" {
			return agent.Event{
				Type:    agent.EventToolResult,
				Results: []agent.ToolResult{s.toolResult(d)},
				Raw:     raw,
			}
		}
	case "session.error":
		var d struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(line.Data, &d)
		msg := d.Message
		if msg == "" {
			msg = "copilot reported a session error"
		}
		if line.AgentID == "" {
			s.sessionError = msg
		}
		return agent.Event{Type: agent.EventError, Text: msg, Raw: raw}
	case "abort":
		// Recorded, not normalized: the line stays unknown, and the result
		// that follows it is an error (below).
		var d struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(line.Data, &d)
		if line.AgentID != "" {
			break
		}
		s.aborted = d.Reason
		if s.aborted == "" {
			s.aborted = "aborted"
		}
	case "result":
		return agent.Event{Type: agent.EventResult, Result: s.result(line), Raw: raw}
	}
	// Everything else — the MCP and tool bookkeeping, reasoning, model call
	// telemetry, turn boundaries, the usage checkpoints task 138.7 will read,
	// the deltas swallowed above — is a genuinely unmodeled line, and a
	// client that asks to see raw lines should see it.
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
}

// toolResult reads one call's outcome. A failed call is a tool result and
// never a run failure — a `denied` one included (denied_1.0.105.jsonl): the
// model sees the refusal and answers around it. A denial is reported as
// Blocked, because the call never ran.
func (s *stream) toolResult(d toolData) agent.ToolResult {
	res := agent.ToolResult{CallID: d.ToolCallID, Name: s.tools[d.ToolCallID]}
	switch {
	case d.Success != nil && *d.Success:
		if d.Result != nil {
			res.Summary = agent.OneLine(firstLine(d.Result.Content), agent.ToolSummaryMax)
		}
	default:
		res.IsError = true
		if d.Error != nil {
			res.Blocked = d.Error.Code == "denied"
			res.Summary = agent.OneLine(firstLine(d.Error.Message), agent.ToolSummaryMax)
		}
	}
	return res
}

// result assembles the terminal line. It carries no answer text, so the last
// assistant message is the answer, as codex's is (§9.3).
//
// A nonzero exitCode is an error, explained by the root agent's last
// session.error when there was one; so is a zero one after the root agent's
// `abort`, because that is how a terminated run ends on 1.0.105. A
// session.error alone never fails a zero exit. The engine records a
// termination it caused whatever the adapter says, so this is the second
// line of defense, not the first (§9.9).
func (s *stream) result(line streamLine) *agent.RunResult {
	res := agent.RunResult{ResultText: s.last, SessionID: line.SessionID}
	if line.ExitCode != nil {
		res.ExitCode = *line.ExitCode
	}
	switch {
	case res.ExitCode != 0:
		res.IsError = true
		res.ErrorMessage = s.sessionError
		if res.ErrorMessage == "" {
			res.ErrorMessage = fmt.Sprintf("copilot run failed (exit %d)", res.ExitCode)
		}
	case s.aborted != "":
		res.IsError = true
		res.ErrorMessage = "copilot run aborted: " + s.aborted
	}
	// Token counts and cost stay unreported: on 1.0.105 tokens live only on
	// usage checkpoints and turn ends, which are task 138.7's (§9.9).
	return &res
}

// firstLine returns s up to its first newline.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
