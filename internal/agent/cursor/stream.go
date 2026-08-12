package cursor

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// streamLine is the superset of `cursor-agent --output-format stream-json`
// fields vincent reads (pinned against cursor-agent 2026.08.04-aaa8809;
// fixtures in testdata/ were captured from real runs). Parsing is tolerant:
// unknown event types become EventUnknown, transcripted verbatim but not
// normalized (phase 1 decision).
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"` // type=assistant
	// ToolCall names the invoked tool by its *key* — `{"editToolCall":{…}}`,
	// `{"shellToolCall":{…}}` — rather than by a name field, so it is decoded
	// as a map and the key is the name.
	ToolCall map[string]json.RawMessage `json:"tool_call"`
	// CallID is the line-level id shared by a tool_call's started and
	// completed events, which is what correlates a result with its call
	// (T4.14). The payload repeats it as `toolCallId`; the line-level field
	// is preferred because it is present on both subtypes at the same depth.
	CallID string `json:"call_id"`
	// Text carries a thinking delta's fragment (type=thinking,
	// subtype=delta); the closing `completed` line has none.
	Text    string `json:"text"`
	IsError bool   `json:"is_error"` // type=result
	Result  string `json:"result"`   // type=result
	Usage   *struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		// cacheReadTokens/cacheWriteTokens are reported and deliberately not
		// recorded: §17 tracks tokens in/out, and folding cache traffic into
		// either would make cursor's numbers incomparable with the other
		// adapters'.
	} `json:"usage"`
}

// stream normalizes cursor stream-json lines. The result text needs no state
// — cursor's terminal `result` carries it whole, unlike codex — but thinking
// does: reasoning arrives as token-level `delta` lines and is accumulated
// here, then emitted as one event when `completed` closes the block (§9.7).
type stream struct {
	thinking strings.Builder
}

// NewLineParser returns a parser for cursor transcript lines (§13.2
// format=normalized). Each call gets its own stream: a half-accumulated
// thinking block must not leak between two files.
func (a *Adapter) NewLineParser() agent.LineParser {
	s := &stream{}
	return s.parse
}

// parse normalizes one verbatim stream-json line into an agent.Event. The raw
// line always rides along for lossless transcripts.
func (s *stream) parse(raw []byte) agent.Event {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	switch line.Type {
	case "assistant":
		// Assistant messages arrive whole (content blocks), not as deltas.
		if text := assistantText(line); text != "" {
			return agent.Event{Type: agent.EventOutput, Text: text, Raw: raw}
		}
	case "thinking":
		return s.parseThinking(line, raw)
	case "tool_call":
		// `started` is the moment the agent invokes something — the analog of
		// a claude tool_use block and of codex's item.started.
		switch line.Subtype {
		case "started":
			if name, payload := toolCall(line.ToolCall); name != "" {
				return agent.Event{
					Type: agent.EventToolUse,
					Tools: []agent.ToolUse{{
						Name:    name,
						Summary: callSummary(payload),
						CallID:  line.CallID,
					}},
					Raw: raw,
				}
			}
		case "completed":
			// `completed` reports the outcome. It is a tool_result, never a
			// second tool_use: normalizing it as an invocation would
			// double-count every call.
			if name, payload := toolCall(line.ToolCall); name != "" {
				return agent.Event{
					Type:    agent.EventToolResult,
					Results: []agent.ToolResult{completedResult(name, line.CallID, payload)},
					Raw:     raw,
				}
			}
		}
	case "result":
		res := agent.RunResult{
			// Cursor's result text is every assistant message of the turn
			// concatenated, not the last one; it is used verbatim.
			ResultText: line.Result,
			// A subtype other than "success" is an error even when the
			// is_error flag is absent: the flag is the CLI's own summary and
			// the subtype is the shape of the terminal event.
			IsError: line.IsError || (line.Subtype != "" && line.Subtype != "success"),
		}
		if res.IsError {
			res.ErrorMessage = line.Result
			if res.ErrorMessage == "" {
				res.ErrorMessage = "cursor run failed"
			}
		}
		if line.Usage != nil {
			res.InputTokens = line.Usage.InputTokens
			res.OutputTokens = line.Usage.OutputTokens
		}
		// CostUSD stays nil: cursor reports no cost (spec §9.7).
		return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
	}
	// system and user fall through here on purpose, as do the thinking
	// `delta` lines parseThinking swallowed — they are genuinely unmodeled
	// lines and a client that asks to see raw lines should see them.
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
}

// parseThinking accumulates reasoning deltas and emits the block whole when
// `completed` closes it (§9.7, amended by T4.16).
//
// Coalescing here rather than in a client is what preserves the part of the
// original decision that was right: a live tail of token-level fragments
// buries the assistant text the pane exists to show. It costs one documented
// exception to Event.Raw — the emitted event's Raw is the `completed` line,
// while its Text came from the deltas before it — and a run killed mid-block
// loses the buffer, which is the correct trade for reasoning text.
func (s *stream) parseThinking(line streamLine, raw []byte) agent.Event {
	switch line.Subtype {
	case "delta":
		s.thinking.WriteString(line.Text)
	case "completed":
		// The `completed` line carries no text of its own; everything is in
		// the deltas that preceded it. The text keeps its own line breaks —
		// a client decides how much of a reasoning block to show, and it
		// cannot do that once the structure has been flattened away.
		text := strings.TrimSpace(s.thinking.String())
		s.thinking.Reset()
		if text != "" {
			return agent.Event{Type: agent.EventThinking, Text: text, Raw: raw}
		}
	}
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
}

// completedResult reads a tool call's outcome out of its completed payload.
//
// It keys on the **presence** of `result.success` rather than on any failure
// shape, and that choice survives now that the fixtures are real: the
// success payloads are verbatim (T5.7 re-capture), and the one failure shape
// observed in the wild — a hook rejection, `result.rejected` with a reason —
// carries no `success`, so presence classifies it correctly without this code
// having to know the name `rejected` or any sibling that follows it. Keying on
// a failure shape would mean enumerating shapes the CLI has never documented.
func completedResult(name, callID string, payload json.RawMessage) agent.ToolResult {
	res := agent.ToolResult{CallID: callID, Name: name}
	var wrapper struct {
		Result struct {
			Success json.RawMessage `json:"success"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil || len(wrapper.Result.Success) == 0 {
		res.IsError = true
		return res
	}
	res.Summary = successSummary(wrapper.Result.Success)
	return res
}

// successSummary describes what a successful call did. An edit reports the
// lines it moved, which is the outcome; falling back to ToolSummary would
// repeat the path the tool_use line already shows, which says nothing new.
func successSummary(success json.RawMessage) string {
	var edit struct {
		LinesAdded   *int `json:"linesAdded"`
		LinesRemoved *int `json:"linesRemoved"`
	}
	if err := json.Unmarshal(success, &edit); err == nil &&
		(edit.LinesAdded != nil || edit.LinesRemoved != nil) {
		return fmt.Sprintf("+%d −%d", deref(edit.LinesAdded), deref(edit.LinesRemoved))
	}
	return agent.ToolSummary(success)
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// assistantText joins the text blocks of an assistant message.
func assistantText(line streamLine) string {
	if line.Message == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range line.Message.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// toolCallSuffix marks the one key of a tool_call object that names the tool.
const toolCallSuffix = "ToolCall"

// toolCall derives the normalized tool name from the tool_call object's
// tool-shaped key — `editToolCall` → `edit`, `shellToolCall` → `shell` — and
// returns that key's value, which holds the call's arguments and result.
//
// Only keys ending in "ToolCall" qualify. The object carries siblings —
// `toolCallId`, `startedAtMs`, `hookAdditionalContexts` — so "the single key"
// would be wrong and "the first key" worse: sorted, `hookAdditionalContexts`
// precedes `shellToolCall` and every shell invocation would be misnamed.
// Remaining keys are sorted only to keep a hypothetical multi-tool payload
// deterministic rather than map-order noise.
func toolCall(call map[string]json.RawMessage) (string, json.RawMessage) {
	var keys []string
	for k := range call {
		if strings.HasSuffix(k, toolCallSuffix) && k != toolCallSuffix {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "", nil
	}
	sort.Strings(keys)
	return strings.TrimSuffix(keys[0], toolCallSuffix), call[keys[0]]
}

// callSummary reads the subject out of a tool call's payload. The arguments
// live one level down under `args`; the payload itself is tried second
// because a shell call repeats its `description` as a sibling of `args`, and
// a description is better than nothing when the arguments carry no key
// agent.ToolSummary knows.
func callSummary(payload json.RawMessage) string {
	var wrapper struct {
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(payload, &wrapper); err == nil {
		if s := agent.ToolSummary(wrapper.Args); s != "" {
			return s
		}
	}
	return agent.ToolSummary(payload)
}
