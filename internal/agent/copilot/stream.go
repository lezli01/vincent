package copilot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

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
	// SessionID, ExitCode and Usage are the result line's own (type=result).
	// Of its usage only the two durations are read: premiumRequests never
	// becomes CostUSD (task 138 non-goals), and the token counts it lacks
	// come from the usage checkpoint before it (task 138.7).
	SessionID string `json:"sessionId"`
	ExitCode  *int   `json:"exitCode"`
	Usage     *struct {
		SessionDurationMs  int64 `json:"sessionDurationMs"`
		TotalAPIDurationMs int64 `json:"totalApiDurationMs"`
	} `json:"usage"`
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
		// DetailedContent is what the CLI shows a human: a `diff --git`
		// for the file tools, the same body as Content for the shell.
		DetailedContent string `json:"detailedContent"`
	} `json:"result"` // complete, on success
	// ShellExecution is on the shell tool's completion
	// (shell_1.0.105.jsonl): the command's own exit code.
	ShellExecution *struct {
		ExitCode *int `json:"exitCode"`
	} `json:"shellExecution"` // complete
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"` // complete, on failure
}

// subagentData is subagent.started's and subagent.completed's payload
// (subagent_1.0.105.jsonl). ToolCallID is the root agent's `task` call that
// spawned the sub-run.
type subagentData struct {
	ToolCallID       string `json:"toolCallId"`
	AgentType        string `json:"agentType"`        // started
	AgentDescription string `json:"agentDescription"` // started
	TotalToolCalls   int    `json:"totalToolCalls"`   // completed
	TotalTokens      int64  `json:"totalTokens"`      // completed
	DurationMs       int64  `json:"durationMs"`       // completed
}

// tokenCount is one `{tokenCount}` cell of a checkpoint's tokenDetails.
type tokenCount struct {
	TokenCount int64 `json:"tokenCount"`
}

// tokenDetails splits a token spend the way claude's usage does (§9.2):
// `input` is the *uncached* input — input + cache_read + cache_write is the
// `usage.inputTokens` beside it (subagent_1.0.105.jsonl: 15 + 41889 + 8959
// = 50863).
type tokenDetails struct {
	Input      tokenCount `json:"input"`
	CacheRead  tokenCount `json:"cache_read"`
	CacheWrite tokenCount `json:"cache_write"`
	Output     tokenCount `json:"output"`
}

// checkpointData is session.usage_checkpoint's payload, as far as vincent
// reads it. Its figures are the *session's* running totals, subagents
// included (task 138.7 decision 3).
type checkpointData struct {
	AccountingSnapshot *struct {
		TokenDetails tokenDetails `json:"tokenDetails"`
		ModelMetrics map[string]struct {
			Usage struct {
				ReasoningTokens int64 `json:"reasoningTokens"`
			} `json:"usage"`
			TokenDetails tokenDetails `json:"tokenDetails"`
		} `json:"modelMetrics"`
	} `json:"accountingSnapshot"`
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
	// agents maps a subagent's agentId to the toolCallId that spawned it,
	// learned from subagent.started: every later line carrying the agentId
	// is that sub-run's, and is stamped with the call as its ParentCallID
	// (task 109).
	agents map[string]string
	// usage is the root agent's last usage checkpoint, folded into the
	// result. The last one wins: each is a running total.
	usage *agent.RunResult
}

// NewLineParser returns a parser for copilot transcript lines (§13.2
// format=normalized). Each call gets its own stream: a half-accumulated
// message must not leak between two files.
func (a *Adapter) NewLineParser() agent.LineParser {
	s := &stream{}
	return s.parse
}

// parse normalizes one verbatim line into an agent.Event. The raw line always
// rides along for lossless transcripts. A line from a known subagent is
// stamped with the call that spawned it — save subagent.started and
// .completed, which are the root agent's account of the sub-run.
func (s *stream) parse(raw []byte) agent.Event {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	ev := s.event(line, raw)
	if parent, ok := s.agents[line.AgentID]; ok && ev.Type != agent.EventSubagentStarted && ev.Type != agent.EventSubagentFinished {
		ev.ParentCallID = parent
	}
	return ev
}

// event is parse's dispatch on the line's type.
func (s *stream) event(line streamLine, raw []byte) agent.Event {
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
				// A subagent's report is its own output, never the run's
				// answer.
				if line.AgentID == "" {
					s.last = text
				}
				return agent.Event{Type: agent.EventOutput, Text: text, Raw: raw}
			}
		}
	case "assistant.reasoning":
		// The closing line carries the whole block, so the
		// assistant.reasoning_delta lines before it stay unknown rather than
		// being coalesced (task 066's whole blocks only).
		var d struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(line.Data, &d) == nil && strings.TrimSpace(d.Content) != "" {
			return agent.Event{Type: agent.EventThinking, Text: d.Content, Raw: raw}
		}
	case "user.message":
		// The root agent's echo of the prompt vincent wrote, carrying
		// nothing, as cursor's does (task 124.2). A subagent's is the
		// prompt its parent wrote it: not an echo of vincent's, so it stays
		// unknown, stamped with its parent call.
		if line.AgentID == "" {
			return agent.Event{Type: agent.EventInputEcho, Raw: raw}
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
			res := s.toolResult(d)
			return agent.Event{
				Type:    agent.EventToolResult,
				Results: []agent.ToolResult{res},
				Output:  commandOutput(res, d),
				Patch:   patch(res, d),
				Raw:     raw,
			}
		}
	case "subagent.started":
		var d subagentData
		if json.Unmarshal(line.Data, &d) == nil && d.ToolCallID != "" {
			if line.AgentID != "" {
				if s.agents == nil {
					s.agents = map[string]string{}
				}
				s.agents[line.AgentID] = d.ToolCallID
			}
			return agent.Event{Type: agent.EventSubagentStarted, Subagent: &agent.Subagent{
				CallID:      d.ToolCallID,
				AgentType:   d.AgentType,
				Description: d.AgentDescription,
			}, Raw: raw}
		}
	case "subagent.completed":
		var d subagentData
		if json.Unmarshal(line.Data, &d) == nil && d.ToolCallID != "" {
			return agent.Event{Type: agent.EventSubagentFinished, Subagent: &agent.Subagent{
				CallID:      d.ToolCallID,
				ToolUses:    d.TotalToolCalls,
				TotalTokens: d.TotalTokens,
				Duration:    time.Duration(d.DurationMs) * time.Millisecond,
			}, Raw: raw}
		}
	case "session.usage_checkpoint":
		// Only the root agent's checkpoint is the run's usage; none with an
		// agentId has been captured, and one would be a sub-run's share.
		if line.AgentID == "" {
			if u := checkpointUsage(line.Data); u != nil {
				s.usage = u
				return agent.Event{Type: agent.EventUsage, Raw: raw}
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
	// Everything else — the MCP and tool bookkeeping, the reasoning, tool
	// call and live shell deltas, model call telemetry, turn boundaries,
	// subagent.selected and .configured, the deltas swallowed above — is a
	// genuinely unmodeled line, and a client that asks to see raw lines
	// should see it.
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
}

// checkpointUsage reads a usage checkpoint's token counts, nil when it
// carries no accounting snapshot. ModelUsage is sorted by model id: the
// snapshot is a JSON object, whose order says nothing.
func checkpointUsage(data json.RawMessage) *agent.RunResult {
	var d checkpointData
	if json.Unmarshal(data, &d) != nil || d.AccountingSnapshot == nil {
		return nil
	}
	snap := d.AccountingSnapshot
	u := &agent.RunResult{
		InputTokens:         snap.TokenDetails.Input.TokenCount,
		OutputTokens:        snap.TokenDetails.Output.TokenCount,
		CacheReadTokens:     snap.TokenDetails.CacheRead.TokenCount,
		CacheCreationTokens: snap.TokenDetails.CacheWrite.TokenCount,
	}
	models := make([]string, 0, len(snap.ModelMetrics))
	for m := range snap.ModelMetrics {
		models = append(models, m)
	}
	slices.Sort(models)
	for _, m := range models {
		mm := snap.ModelMetrics[m]
		u.ReasoningOutputTokens += mm.Usage.ReasoningTokens
		// CostUSD stays nil: a premium request is not a price (task 138
		// non-goals).
		u.ModelUsage = append(u.ModelUsage, agent.ModelUsage{
			Model:               m,
			InputTokens:         mm.TokenDetails.Input.TokenCount,
			OutputTokens:        mm.TokenDetails.Output.TokenCount,
			CacheReadTokens:     mm.TokenDetails.CacheRead.TokenCount,
			CacheCreationTokens: mm.TokenDetails.CacheWrite.TokenCount,
		})
	}
	return u
}

// toolResult reads one call's outcome. A failed call is a tool result and
// never a run failure — a `denied` one included (denied_1.0.105.jsonl): the
// model sees the refusal and answers around it. A denial is reported as
// Blocked, because the call never ran.
func (s *stream) toolResult(d toolData) agent.ToolResult {
	res := agent.ToolResult{CallID: d.ToolCallID, Name: s.tools[d.ToolCallID]}
	switch {
	case d.Success != nil && *d.Success:
		switch {
		case d.ShellExecution != nil && d.ShellExecution.ExitCode != nil:
			// The shell's outcome is its exit code; the body rides on the
			// event's Output.
			res.Summary = fmt.Sprintf("exit %d", *d.ShellExecution.ExitCode)
		case d.Result != nil:
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
	// Tokens are the last root usage checkpoint's: session totals, which on
	// a fresh session are the run's (§9.9; a resumed run's are task
	// 138.11's). CostUSD stays nil and NumTurns 0: the CLI reports neither,
	// and counting turn_start lines would be emulation.
	if u := s.usage; u != nil {
		res.InputTokens = u.InputTokens
		res.OutputTokens = u.OutputTokens
		res.CacheReadTokens = u.CacheReadTokens
		res.CacheCreationTokens = u.CacheCreationTokens
		res.ReasoningOutputTokens = u.ReasoningOutputTokens
		res.ModelUsage = u.ModelUsage
	}
	if line.Usage != nil {
		res.Duration = time.Duration(line.Usage.SessionDurationMs) * time.Millisecond
		res.APIDuration = time.Duration(line.Usage.TotalAPIDurationMs) * time.Millisecond
	}
	return &res
}

// shellTools are the tools whose completion carries a command's output. Only
// `bash` is captured (shell_1.0.105.jsonl, macOS); the Windows and Linux
// names are task 138.3's to confirm, so nothing else is guessed at here.
var shellTools = map[string]bool{"bash": true}

// shellTrailer is the line copilot appends to a shell result for the model:
// `<shellId: 0 completed with exit code 0>`. The exit code is the result's
// Summary; the trailer is not something the command printed.
var shellTrailer = regexp.MustCompile(`\n?<shellId: [^>]*>\s*$`)

// commandOutput is a shell call's output body (task 070), nil for any other
// tool and for a call that did not run.
func commandOutput(res agent.ToolResult, d toolData) *agent.CommandOutput {
	if !shellTools[res.Name] || res.IsError || d.Result == nil {
		return nil
	}
	body := shellTrailer.ReplaceAllString(d.Result.Content, "")
	if strings.TrimSpace(body) == "" {
		return nil
	}
	text, truncated := agent.TruncateOutput(body)
	return &agent.CommandOutput{CallID: res.CallID, Name: res.Name, Text: text, Truncated: truncated}
}

// editTools are the tools whose detailedContent diff is a change they made.
// `view` writes one too — the file or directory it read, as context lines or
// as a "created" listing (restricted_1.0.105.jsonl) — so the diff is never
// read off a tool that is not an edit.
var editTools = map[string]bool{"edit": true, "apply_patch": true}

// patch is an edit's hunks (task 110), nil for any other tool, a failed call,
// or a diff with no hunk. The `diff --git`, `index` and `---`/`+++` header
// before the first hunk is dropped: Patch.Text is hunks under their `@@`
// headers, and the tool_use already names the file.
func patch(res agent.ToolResult, d toolData) *agent.Patch {
	if !editTools[res.Name] || res.IsError || d.Result == nil {
		return nil
	}
	diff := d.Result.DetailedContent
	i := strings.Index(diff, "\n@@")
	switch {
	case strings.HasPrefix(diff, "@@"):
		i = 0
	case i < 0:
		return nil
	default:
		i++
	}
	text, truncated := agent.TruncatePatch(strings.TrimRight(diff[i:], "\n") + "\n")
	return &agent.Patch{CallID: res.CallID, Name: res.Name, Text: text, Truncated: truncated}
}

// firstLine returns s up to its first newline.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
