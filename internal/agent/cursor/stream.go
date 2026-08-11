package cursor

import (
	"encoding/json"
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
	CallID  string `json:"call_id"`
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

// NewLineParser returns a parser for cursor transcript lines (§13.2
// format=normalized). Unlike codex, cursor's terminal `result` event carries
// the complete result text, so no state crosses lines and every parser
// instance is interchangeable — the constructor exists to satisfy the
// interface, not to hold state.
func (a *Adapter) NewLineParser() agent.LineParser { return parse }

// parse normalizes one verbatim stream-json line into an agent.Event. The raw
// line always rides along for lossless transcripts.
func parse(raw []byte) agent.Event {
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
	case "tool_call":
		// `started` is the moment the agent invokes something — the analog of
		// a claude tool_use block and of codex's item.started. `completed`
		// carries the result and would double-count the invocation.
		if line.Subtype == "started" {
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
	// system, user, and thinking fall through here on purpose. thinking
	// events are token-level reasoning deltas: transcripted verbatim, never
	// surfaced live, because a tail of reasoning fragments buries the
	// assistant text it exists to show (§9.7).
	return agent.Event{Type: agent.EventUnknown, Raw: raw}
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
