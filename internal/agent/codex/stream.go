package codex

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// execLine is the superset of `codex exec --json` JSONL fields vincent reads
// (pinned against codex-cli 0.142.5, extended against 0.150.1; fixtures in
// testdata/ were captured from real runs). Parsing is tolerant: unknown event types become
// EventUnknown, transcripted verbatim but not normalized (phase 1 decision).
type execLine struct {
	Type     string     `json:"type"`
	Message  string     `json:"message"` // type=error
	Item     *execItem  `json:"item"`
	ThreadID string     `json:"thread_id"` // thread.started
	Usage    *execUsage `json:"usage"`     // turn.completed
	Err      *execError `json:"error"`     // turn.failed
}

type execItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`    // agent_message, reasoning
	Message string `json:"message"` // item type=error (advisory notices)
	// Items is a todo_list's entries — the agent's running plan (task 070).
	Items []execTodo `json:"items"`
	// Changes is a file_change's edits. It is an array of objects, which
	// agent.ToolSummary cannot read at all (it takes string values), which
	// is why this item's subject is built here (task 070 decision 4).
	Changes []execChange `json:"changes"`
	// AggregatedOutput is a command_execution's merged stdout and stderr.
	AggregatedOutput string `json:"aggregated_output"`
	// Server and Tool name an mcp_tool_call's subject. They are codex-shaped
	// names, not names that converge across dialects, so they stay out of
	// agent.toolSummaryKeys and are read here (task 070 decision 4).
	Server string `json:"server"`
	Tool   string `json:"tool"`
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
	// The remaining three counters, all captured in
	// testdata/plan_0.150.1.jsonl. codex's names for the first two are the
	// other dialect's names for the same quantities, so they land in the
	// RunResult fields task 066 already defined rather than in new ones.
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

type execError struct {
	Message string `json:"message"`
}

// execTodo is one entry of a todo_list item.
type execTodo struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

// execChange is one edit reported by a file_change item.
type execChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// result describes a completed tool item. `exit_code` and `status` are the
// two fields the captured shapes carry; an item type that reports neither
// yields a bare success, because "completed with nothing to say" is what a
// completion without a failure signal means. Nothing here guesses at fields
// no capture contains — the rule that kept codex reasoning unimplemented
// until `testdata/reasoning_0.147.0.jsonl` showed what one looks like.
func (it *execItem) result() agent.ToolResult {
	var fields struct {
		ExitCode *int   `json:"exit_code"`
		Status   string `json:"status"`
	}
	res := agent.ToolResult{CallID: it.ID, Name: it.Type}
	if err := json.Unmarshal(it.raw, &fields); err != nil {
		return res
	}
	// `mcp_tool_call.error.message` is *not* read here. The captured shape
	// (testdata/mcp_0.150.1.jsonl) carries `error: null` even on a call the
	// same line reports as `status: "failed"` — the server's explanation
	// came back inside `result`, not in `error`. A populated `error` has no
	// capture, so the arm for it is deferred with the fixture requirement
	// attached (§9.3 deferred list, task 070 decision 6) rather than written
	// from the upstream schema and left untested.
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

// planItems are the item types that report the agent's plan rather than a
// tool invocation. A todo_list is the only one 0.150.1 emits.
var planItems = map[string]bool{"todo_list": true}

// toolSummaryLimit bounds the summaries this adapter builds itself, matching
// what agent.ToolSummary applies to the ones it extracts. It is spelled here
// because that cap is unexported and this is the same kind of field.
const toolSummaryLimit = 200

// summary is the item's subject. Most item types keep it under a name
// agent.ToolSummary already prefers (`command`, `query`), so the shared
// extractor answers for them. Two do not, and both are read here rather than
// by widening agent.toolSummaryKeys: `changes` is an array of objects the
// extractor cannot read at all, and `server`/`tool` are codex-shaped names in
// a list whose design is names that converge across dialects (task 070
// decision 4).
func (it *execItem) summary() string {
	switch it.Type {
	case "file_change":
		return changeSummary(it.Changes)
	case "mcp_tool_call":
		return mcpSummary(it)
	}
	return agent.ToolSummary(it.raw)
}

// changeSummary names the paths a file_change touched and what it did to
// each: "update notes.txt", and "add a.go, delete b.go" for several. The
// path is basename-only — a file_change reports absolute paths, and a column
// of worktree prefixes says nothing a reader of that task did not know.
func changeSummary(changes []execChange) string {
	if len(changes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		// Both separators, not filepath's: a fixture captured on one
		// platform is read on all three, so the host's idea of a separator
		// is the wrong one to use.
		name := path.Base(strings.ReplaceAll(c.Path, `\`, "/"))
		switch {
		case c.Kind != "" && name != "" && name != ".":
			parts = append(parts, c.Kind+" "+name)
		case name != "" && name != ".":
			parts = append(parts, name)
		case c.Kind != "":
			parts = append(parts, c.Kind)
		}
	}
	return agent.OneLine(strings.Join(parts, ", "), toolSummaryLimit)
}

// mcpSummary names an MCP call by server and tool — "vincent/health" — with
// the call's own arguments as the subject when it carried any, since `server`
// and `tool` say which tool ran and `arguments` says what it ran on.
func mcpSummary(it *execItem) string {
	name := it.Tool
	if it.Server != "" && it.Tool != "" {
		name = it.Server + "/" + it.Tool
	} else if it.Server != "" {
		name = it.Server
	}
	var args struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(it.raw, &args); err == nil {
		if subject := agent.ToolSummary(args.Arguments); subject != "" {
			if name == "" {
				return subject
			}
			return agent.OneLine(name+" "+subject, toolSummaryLimit)
		}
	}
	return agent.OneLine(name, toolSummaryLimit)
}

// planEvent normalizes a todo_list item into the agent's running plan. Every
// version of the list arrives whole, so the event carries the whole list and
// the item id ties successive versions together (task 070).
func planEvent(it *execItem, raw []byte) agent.Event {
	plan := &agent.Plan{CallID: it.ID, Items: make([]agent.PlanItem, 0, len(it.Items))}
	for _, t := range it.Items {
		plan.Items = append(plan.Items, agent.PlanItem{
			Text:      agent.OneLine(t.Text, toolSummaryLimit),
			Completed: t.Completed,
		})
	}
	return agent.Event{Type: agent.EventPlan, Plan: plan, Raw: raw}
}

// commandOutput is the body a command_execution printed, or nil when it
// printed nothing. codex merges stdout and stderr into one field and reports
// it on the completion, which is why this rides on the same event as the
// outcome rather than arriving on a line of its own.
func commandOutput(it *execItem) *agent.CommandOutput {
	if it.Type != "command_execution" || it.AggregatedOutput == "" {
		return nil
	}
	text, truncated := agent.TruncateOutput(it.AggregatedOutput)
	return &agent.CommandOutput{
		CallID: it.ID, Name: it.Type, Text: text, Truncated: truncated,
	}
}

// threadIDOf reads codex's `thread_id` off one verbatim JSONL line, or ""
// when the line has none or does not parse. Only `thread.started` carries
// one, and both a fresh and a resumed run open with it (verified against
// 0.150.1). It is deliberately separate from parse: a thread id is not an
// event — it is a property of the run, and the run records it (§9.1, §9.3).
func threadIDOf(raw []byte) string {
	var line struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return ""
	}
	return line.ThreadID
}

// stream normalizes codex JSONL lines into agent.Events. Unlike claude,
// codex has no single result event: the result text is the last
// agent_message and usage arrives with turn.completed, so normalization
// carries state across lines.
type stream struct {
	lastMessage string
	// threadID is thread.started's id, carried to the terminal result the
	// way lastMessage is: a chat stores RunResult.SessionID and hands it
	// back on the next turn (§9.1, task 070).
	threadID string
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
	case "thread.started":
		// Not an event in its own right: nothing renders a thread id, and
		// the run it identifies has not produced anything yet. It is held
		// for the terminal result, which is what a chat reads (task 070).
		// It falls through to EventUnknown, which still transcripts the line
		// verbatim. threadIDOf reads the same field off the raw line for the
		// run itself, which is what carries the id when a run produces no
		// terminal result at all — a refused resume (failure.go).
		s.threadID = line.ThreadID
	case "item.started":
		if line.Item == nil {
			break
		}
		if planItems[line.Item.Type] {
			return planEvent(line.Item, raw)
		}
		if toolItems[line.Item.Type] {
			return agent.Event{
				Type: agent.EventToolUse,
				Tools: []agent.ToolUse{{
					Name:    line.Item.Type,
					Summary: line.Item.summary(),
					CallID:  line.Item.ID,
				}},
				Raw: raw,
			}
		}
	case "item.updated":
		// The only event that reports progress *within* an item. codex
		// 0.150.1 emits it for a todo_list ticking over and for nothing else
		// vincent has captured — a command_execution goes started →
		// completed even when it runs for half a minute, so its streaming
		// half stays unimplemented rather than written from the schema
		// (§9.3 deferred list, task 070 decision 6).
		if line.Item != nil && planItems[line.Item.Type] {
			return planEvent(line.Item, raw)
		}
	case "item.completed":
		if line.Item == nil {
			break
		}
		if planItems[line.Item.Type] {
			return planEvent(line.Item, raw)
		}
		if toolItems[line.Item.Type] {
			// The completion of a tool item is its outcome, correlated to
			// the item.started that opened it by the shared item id (T4.16).
			// A command_execution's completion also carries what the command
			// printed; that rides along as Output and becomes its own record
			// downstream, because an outcome and an output body are shown at
			// different verbosity levels (task 070 decision 2).
			return agent.Event{
				Type:    agent.EventToolResult,
				Results: []agent.ToolResult{line.Item.result()},
				Output:  commandOutput(line.Item),
				Raw:     raw,
			}
		}
		switch line.Item.Type {
		case "agent_message":
			if line.Item.Text != "" {
				s.lastMessage = line.Item.Text
				return agent.Event{Type: agent.EventOutput, Text: line.Item.Text, Raw: raw}
			}
		case "reasoning":
			// Whole blocks, so this needs none of the cursor parser's
			// delta accumulation (T4.16): codex delivers each reasoning
			// item complete on its own `item.completed`, with no
			// `item.started` to correlate against and no partial text to
			// buffer. That is claude's shape, not cursor's, and it is what
			// EventThinking's whole-blocks-only contract asks for.
			if line.Item.Text != "" {
				return agent.Event{Type: agent.EventThinking, Text: line.Item.Text, Raw: raw}
			}
		case "error":
			// Advisory error items (e.g. model-metadata notices) are not
			// terminal; turn.failed decides the outcome.
			return agent.Event{Type: agent.EventError, Message: line.Item.Message, Raw: raw}
		}
	case "error":
		return agent.Event{Type: agent.EventError, Message: line.Message, Raw: raw}
	case "turn.completed":
		res := agent.RunResult{ResultText: s.lastMessage, SessionID: s.threadID}
		if line.Usage != nil {
			res.InputTokens = line.Usage.InputTokens
			res.OutputTokens = line.Usage.OutputTokens
			res.CacheReadTokens = line.Usage.CachedInputTokens
			res.CacheCreationTokens = line.Usage.CacheWriteInputTokens
			res.ReasoningOutputTokens = line.Usage.ReasoningOutputTokens
		}
		return agent.Event{Type: agent.EventResult, Result: &res, Raw: raw}
	case "turn.failed":
		res := agent.RunResult{IsError: true, ResultText: s.lastMessage, SessionID: s.threadID}
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
