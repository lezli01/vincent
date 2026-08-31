package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// streamLine is the superset of stream-json fields vincent reads (pinned
// against claude 2.1.x). Parsing is tolerant: unknown event types become
// EventUnknown, transcripted verbatim but not normalized (phase 1 decision).
type streamLine struct {
	Type string `json:"type"`
	// SessionID is claude's own identifier for the conversation. Every line
	// carries it, including the `system`/`init` line that precedes any
	// assistant output, which is why sessionIDOf reads it off the raw line
	// rather than off a normalized event (task 063).
	SessionID    string         `json:"session_id"`
	Subtype      string         `json:"subtype"`
	Message      *streamMessage `json:"message"`
	IsError      bool           `json:"is_error"`
	Result       string         `json:"result"`
	TotalCostUSD *float64       `json:"total_cost_usd"`
	Usage        *streamUsage   `json:"usage"`
	// ParentToolUseID is the `Task` call a subagent's lines belong to, and
	// is `null` on every line of the main loop — which is every line of
	// every fixture captured so far (task 066). It is read and carried, and
	// nothing renders it yet: §15's pane is flat.
	ParentToolUseID string `json:"parent_tool_use_id"`
	// CWD and Tools are the `system`/`init` line's payload — the run header
	// (task 066). They appear on no other line type.
	CWD   string   `json:"cwd"`
	Tools []string `json:"tools"`
	// The rest are the `result` line's account of the run. claude sends ~20
	// fields there; these are the ones that answer a question the pane could
	// not previously answer at any level.
	DurationMS        int64                       `json:"duration_ms"`
	DurationAPIMS     int64                       `json:"duration_api_ms"`
	NumTurns          int                         `json:"num_turns"`
	StopReason        string                      `json:"stop_reason"`
	TerminalReason    string                      `json:"terminal_reason"`
	ModelUsage        map[string]streamModelUsage `json:"modelUsage"`
	PermissionDenials []streamPermissionDenial    `json:"permission_denials"`
	// ToolUseResult and ToolResultMeta ride on a `user` line beside the
	// message, not inside it (task 066). ToolUseResult is raw because claude
	// sends either the structured object or a bare error string — the deny
	// fixture's is `"Error: no user is available; permission denied"` — the
	// same two-shapes problem resultSummary already has.
	ToolUseResult  json.RawMessage        `json:"tool_use_result"`
	ToolResultMeta []streamToolResultMeta `json:"tool_result_meta"`
}

// streamModelUsage is one entry of the result line's `modelUsage` map, whose
// key is the model id. The model-describing members claude also sends there
// (`contextWindow`, `maxOutputTokens`, `canonicalModel`, `provider`) are
// deliberately not read: they are facts about the model, and §9.6's option
// catalog is where those belong.
type streamModelUsage struct {
	InputTokens              int64    `json:"inputTokens"`
	OutputTokens             int64    `json:"outputTokens"`
	CacheReadInputTokens     int64    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64    `json:"cacheCreationInputTokens"`
	CostUSD                  *float64 `json:"costUSD"`
}

// streamPermissionDenial is one entry of the result line's
// `permission_denials`. The refused `tool_input` is not read — it is the
// tool's arguments, and an outcome record carries no bodies (T4.16).
type streamPermissionDenial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

// streamToolResultMeta annotates one tool result on a `user` line.
// `non_execution_kind` is how a call that never ran announces itself; the one
// value captured so far is `permission-rule`.
type streamToolResultMeta struct {
	ID               string `json:"id"`
	NonExecutionKind string `json:"non_execution_kind"`
}

// streamToolUseResult is the object shape of a `user` line's
// `tool_use_result`. Only `type` is read: it is the verb, and everything else
// in the payload is the tool's body, which never enters the normalized stream
// (T4.16).
type streamToolUseResult struct {
	Type string `json:"type"`
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
	// The cache counts are not included in InputTokens: a run whose prompt
	// was almost entirely a cache hit reports a handful of input tokens and
	// tens of thousands of cache reads (task 066).
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// sessionIDOf reads claude's `session_id` off one verbatim stream line, or ""
// when the line has none or does not parse. It is deliberately separate from
// parseLine: a session id is not an event — it is a property of the run, and
// the run records it (§9.1, §9.2).
func sessionIDOf(raw []byte) string {
	var line struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return ""
	}
	return line.SessionID
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
	ev := parseTyped(&line, raw)
	// Every line carries it, so it is attached once here rather than in each
	// of the four arms (task 066).
	ev.ParentCallID = line.ParentToolUseID
	return ev
}

func parseTyped(line *streamLine, raw []byte) agent.Event {
	switch line.Type {
	case "assistant":
		return parseAssistant(line, raw)
	case "user":
		// A `user` line is claude replaying tool results back to the model.
		// It is the only place an invocation's outcome is reported, so it is
		// normalized rather than left to fall through as raw (T4.16).
		return parseToolResults(line, raw)
	case "result":
		return parseResult(line, raw)
	case "system":
		// `init` is the run header; claude sends other subtypes here
		// (compact boundaries among them) and an unmodelled one stays raw,
		// which is the phase 1 tolerant-parsing rule (task 066).
		if line.Subtype == "init" {
			return parseInit(line, raw)
		}
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	default:
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
}

// parseInit normalizes the `system`/`init` line claude opens every stream
// with: the directory it is working in and the tools it was given (task 066).
func parseInit(line *streamLine, raw []byte) agent.Event {
	return agent.Event{
		Type: agent.EventRunHeader,
		Header: &agent.RunHeader{
			WorkDir: line.CWD,
			Tools:   line.Tools,
		},
		Raw: raw,
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
	verb := resultVerb(line.ToolUseResult)
	for _, b := range line.Message.Content {
		if b.Type != "tool_result" {
			continue
		}
		ev.Results = append(ev.Results, agent.ToolResult{
			CallID:  b.ToolUseID,
			Summary: resultSummary(b.Content),
			Verb:    verb,
			Blocked: blockedByRule(line.ToolResultMeta, b.ToolUseID),
			IsError: b.IsError,
		})
	}
	if len(ev.Results) > 0 {
		ev.Type = agent.EventToolResult
	}
	return ev
}

// resultVerbs maps claude's structured `tool_use_result.type` onto the verb a
// reader sees. It holds exactly the types a captured run has shown, and an
// unmapped one yields no verb rather than a guessed past tense: a wrong guess
// fails silently — the verb is simply wrong, and nothing distinguishes that
// from a tool that reported no type at all (the T4.17 rule).
var resultVerbs = map[string]string{
	"create": "created",
}

// resultVerb reads the verb off a `user` line's `tool_use_result`. The
// payload is either the structured object or a bare string, so the object
// decode is *probed* — a string there is not an error, it is claude's other
// shape, and it simply carries no verb.
func resultVerb(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var res streamToolUseResult
	if err := json.Unmarshal(payload, &res); err != nil {
		return ""
	}
	return resultVerbs[res.Type]
}

// blockedByRule reports whether a permission rule refused this call, from the
// line's `tool_result_meta`. A meta entry with any other
// `non_execution_kind` is not treated as blocked: only `permission-rule` has
// been captured, and the others are unknown conditions rather than known ones.
func blockedByRule(meta []streamToolResultMeta, callID string) bool {
	for _, m := range meta {
		if m.ID == callID && m.NonExecutionKind == "permission-rule" {
			return true
		}
	}
	return false
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
		res.CacheReadTokens = line.Usage.CacheReadInputTokens
		res.CacheCreationTokens = line.Usage.CacheCreationInputTokens
	}
	res.Duration = time.Duration(line.DurationMS) * time.Millisecond
	res.APIDuration = time.Duration(line.DurationAPIMS) * time.Millisecond
	res.NumTurns = line.NumTurns
	res.StopReason = line.StopReason
	res.TerminalReason = line.TerminalReason
	res.ModelUsage = modelUsage(line.ModelUsage)
	for _, d := range line.PermissionDenials {
		res.PermissionDenials = append(res.PermissionDenials,
			agent.PermissionDenial{ToolName: d.ToolName, CallID: d.ToolUseID})
	}
	return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
}

// modelUsage flattens claude's `modelUsage` map into the normalized slice.
// The keys are sorted rather than ranged over: Go randomizes map iteration,
// and a pane whose per-model lines reorder between two reads of the same
// transcript is a bug a reader would blame on the run.
func modelUsage(m map[string]streamModelUsage) []agent.ModelUsage {
	if len(m) == 0 {
		return nil
	}
	models := make([]string, 0, len(m))
	for name := range m {
		models = append(models, name)
	}
	slices.Sort(models)
	out := make([]agent.ModelUsage, 0, len(models))
	for _, name := range models {
		u := m[name]
		out = append(out, agent.ModelUsage{
			Model:               name,
			InputTokens:         u.InputTokens,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CacheReadInputTokens,
			CacheCreationTokens: u.CacheCreationInputTokens,
			CostUSD:             u.CostUSD,
		})
	}
	return out
}
