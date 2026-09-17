package claude

import (
	"encoding/json"
	"fmt"
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
	// ParentToolUseID is the spawning call a subagent's lines belong to, and
	// is `null` on every line of the main loop (task 066). The call is named
	// `Agent` in every captured run, and nothing here reads the name: the id
	// is the attribution (task 109).
	ParentToolUseID string `json:"parent_tool_use_id"`
	// The rest of the block is the `system` task lines claude writes about
	// the subagents and background shells it runs (task 109). Only
	// `task_started` names its `task_type`; `task_progress` and
	// `task_notification` do not, which is why streamParser remembers the
	// calls it has seen.
	TaskType     string `json:"task_type"`
	ToolUseID    string `json:"tool_use_id"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	Backgrounded bool   `json:"is_backgrounded"`
	Status       string `json:"status"`
	Summary      string `json:"summary"`
	LastToolName string `json:"last_tool_name"`
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
// `tool_use_result`. Only `type`, `status` and `structuredPatch` are read:
// the first two are the verb, and the patch is an edit's delta and its
// hunks. Everything else in the payload is the tool's body — an edit's
// `originalFile`, `oldString` and `newString`, a write's `content` — and
// never enters the normalized stream (T4.16).
type streamToolUseResult struct {
	Type string `json:"type"`
	// Status is how a subagent call returned (task 109): `async_launched`
	// for one that went to the background, `completed` for one the main
	// loop waited on.
	Status string `json:"status"`
	// StructuredPatch is what an `Edit`, or a `Write` of type `update`,
	// changed (task 110). An `Edit`'s payload carries no `type` at all, and a
	// `Write` of type `create` always sends this empty.
	StructuredPatch []streamHunk `json:"structuredPatch"`
}

// streamHunk is one hunk of a `structuredPatch`. Every entry of Lines starts
// with `+`, `-` or a space, as a unified diff's body lines do.
type streamHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
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
	// The task lines reuse the key for a different tally (task 109): a
	// subagent's running or final tokens, tool uses and wall clock. No line
	// carries both shapes.
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMS  int64 `json:"duration_ms"`
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
// format=normalized). Each call hands back a fresh one, because the dialect
// is no longer quite stateless: see streamParser.
func (a *Adapter) NewLineParser() agent.LineParser { return new(streamParser).parse }

// streamParser normalizes claude's stream-json. Every line stands on its own
// except one: a subagent's `task_notification` does not say it is a
// subagent's (task 109). Only `task_started` names its `task_type`, and a
// failed subagent's notification carries no `usage` either, so it is
// indistinguishable on its face from a background shell's. The parser
// therefore remembers every call it has seen acting as a subagent — named by
// a `local_agent` start, a progress line, or a child line's
// `parent_tool_use_id` — and recognizes a notification by its call.
//
// The memory costs one thing, and it is stated rather than hidden: a
// transcript range that opens after every earlier line of a subagent can
// leave that subagent's failed notification as agent.raw. A completed one is
// still recognized by its usage.
//
// The zero value is ready to use. A parser belongs to one stream.
type streamParser struct {
	subagents map[string]bool
}

// parse normalizes one verbatim stream-json line into an agent.Event. The raw
// line always rides along for lossless transcripts.
func (p *streamParser) parse(raw []byte) agent.Event {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
	p.remember(line.ParentToolUseID)
	var ev agent.Event
	if line.Type == "system" && strings.HasPrefix(line.Subtype, "task_") {
		ev = p.parseTask(&line, raw)
	} else {
		ev = parseTyped(&line, raw)
	}
	// Every line carries it, so it is attached once here rather than in each
	// of the four arms (task 066).
	ev.ParentCallID = line.ParentToolUseID
	return ev
}

func (p *streamParser) remember(callID string) {
	if callID == "" {
		return
	}
	if p.subagents == nil {
		p.subagents = map[string]bool{}
	}
	p.subagents[callID] = true
}

// parseTask normalizes the task lines of a subagent (task 109). A background
// shell's (`local_bash`) and every subtype not named here stay unknown, which
// is the phase 1 tolerant-parsing rule: nobody asked for background shells,
// and `task_updated` carries a patch rather than a state.
func (p *streamParser) parseTask(line *streamLine, raw []byte) agent.Event {
	unknown := agent.Event{Type: agent.EventUnknown, Raw: raw}
	if line.ToolUseID == "" {
		return unknown
	}
	sub := &agent.Subagent{CallID: line.ToolUseID}
	if u := line.Usage; u != nil {
		sub.ToolUses = u.ToolUses
		sub.TotalTokens = u.TotalTokens
		sub.Duration = time.Duration(u.DurationMS) * time.Millisecond
	}
	switch line.Subtype {
	case "task_started":
		if line.TaskType != "local_agent" {
			return unknown
		}
		p.remember(line.ToolUseID)
		sub.Description = line.Description
		sub.AgentType = line.SubagentType
		sub.Background = line.Backgrounded
		return agent.Event{Type: agent.EventSubagentStarted, Subagent: sub, Raw: raw}
	case "task_progress":
		// Every captured progress line is a subagent's and names its
		// subagent_type; a shell reports none, so one without it is left
		// alone rather than guessed at.
		if line.SubagentType == "" && !p.subagents[line.ToolUseID] {
			return unknown
		}
		p.remember(line.ToolUseID)
		sub.Description = line.Description
		sub.AgentType = line.SubagentType
		sub.LastTool = line.LastToolName
		return agent.Event{Type: agent.EventSubagentProgress, Subagent: sub, Raw: raw}
	case "task_notification":
		if !p.subagents[line.ToolUseID] && line.Usage == nil {
			return unknown
		}
		sub.Status = line.Status
		sub.Summary = agent.OneLine(line.Summary, resultSummaryMax)
		return agent.Event{Type: agent.EventSubagentFinished, Subagent: sub, Raw: raw}
	}
	return unknown
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
	res := decodeToolUseResult(line.ToolUseResult)
	verb, launched := resultVerb(res)
	for _, b := range line.Message.Content {
		if b.Type != "tool_result" {
			continue
		}
		summary := resultSummary(b.Content)
		if launched {
			// The text beside a background launch is claude's instruction
			// to its own model — internal metadata it asks never be quoted
			// — and the verb already says what happened (task 109).
			summary = ""
		}
		ev.Results = append(ev.Results, agent.ToolResult{
			CallID:  b.ToolUseID,
			Summary: summary,
			Verb:    verb,
			Blocked: blockedByRule(line.ToolResultMeta, b.ToolUseID),
			IsError: b.IsError,
		})
	}
	// An edit's patch replaces the prose outcome with its delta and rides
	// along as the body (task 110). `tool_use_result` belongs to the line,
	// not to a block, so it is attributed only when the line reports exactly
	// one result — every recorded line with a patch does — rather than
	// guessed onto one of several.
	if len(ev.Results) == 1 && !ev.Results[0].IsError && res != nil && len(res.StructuredPatch) > 0 {
		r := &ev.Results[0]
		text, added, removed := unifiedPatch(res.StructuredPatch)
		r.Summary = fmt.Sprintf("+%d −%d", added, removed)
		text, truncated := agent.TruncatePatch(text)
		ev.Patch = &agent.Patch{CallID: r.CallID, Name: r.Name, Text: text, Truncated: truncated}
	}
	if len(ev.Results) > 0 {
		ev.Type = agent.EventToolResult
	}
	return ev
}

// unifiedPatch renders a `structuredPatch` as unified hunks and counts the
// lines it adds and removes. The counts are the `+` and `-` body lines across
// every hunk — a hunk header's oldLines and newLines include context — and
// they are taken from the whole patch, before any cap, so a truncated patch
// still reports its true delta.
func unifiedPatch(hunks []streamHunk) (text string, added, removed int) {
	var b strings.Builder
	for i, h := range hunks {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
			b.WriteByte('\n')
			b.WriteString(l)
		}
	}
	return b.String(), added, removed
}

// resultVerbs maps claude's structured `tool_use_result.type` onto the verb a
// reader sees. It holds exactly the types a captured run has shown, and an
// unmapped one yields no verb rather than a guessed past tense: a wrong guess
// fails silently — the verb is simply wrong, and nothing distinguishes that
// from a tool that reported no type at all (the T4.17 rule).
//
// `update` is a `Write` that overwrote an existing file (task 110). An `Edit`
// sends no `type` at all, and is left without a verb rather than given one
// inferred from its payload's keys.
var resultVerbs = map[string]string{
	"create": "created",
	"update": "updated",
}

// resultStatusVerbs maps a subagent call's `tool_use_result.status` onto its
// verb (task 109), under resultVerbs' rule. `completed` is observed too and
// deliberately absent: a call the main loop waited on returns the report, and
// its first line says more than a verb would.
var resultStatusVerbs = map[string]string{
	"async_launched": "started in background",
}

// decodeToolUseResult probes a `user` line's `tool_use_result` for the
// structured object. The payload is either that object or a bare string, so
// a failed decode is not an error — a string there is claude's other shape,
// and it simply carries no verb and no patch. nil is either that or no
// payload at all.
func decodeToolUseResult(payload json.RawMessage) *streamToolUseResult {
	if len(payload) == 0 {
		return nil
	}
	var res streamToolUseResult
	if err := json.Unmarshal(payload, &res); err != nil {
		return nil
	}
	return &res
}

// resultVerb reads the verb off a decoded `tool_use_result`, and whether it
// reports a background launch.
func resultVerb(res *streamToolUseResult) (verb string, launched bool) {
	if res == nil {
		return "", false
	}
	if v, ok := resultStatusVerbs[res.Status]; ok {
		return v, true
	}
	return resultVerbs[res.Type], false
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
