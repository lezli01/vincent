package agent

import (
	"encoding/json"
	"strings"
)

// Tool arguments are free-form per dialect and per tool: claude sends a
// tool's whole `input` object, cursor an `args` object under the tool's key,
// codex the fields of the item itself. Rather than three tables of
// tool-name → field, one ordered preference over *argument names* answers
// "what is this call about" for all of them — the names converge because the
// underlying tools do (T4.14).
//
// A key that is absent costs nothing: the summary comes back empty and the
// caller renders the bare tool name, which is what every tool rendered as
// before this existed.

// toolSummaryKeys is that preference, subject first. Order is the whole
// design: `command` beats `description` because "git status" is what a
// reader wants and "Show git working tree status" is what the model wrote
// about it, and cursor's shell calls carry both.
var toolSummaryKeys = []string{
	"command",       // claude Bash, codex command_execution, cursor shellToolCall
	"file_path",     // claude Read/Write/Edit
	"notebook_path", // claude NotebookEdit
	// `pattern` precedes `path` because a search carries both and the
	// pattern is the subject — "Grep TODO" says more than "Grep internal".
	// Cursor's edits carry only `path`, so nothing is lost by the order.
	"pattern",     // claude Glob/Grep
	"path",        // cursor editToolCall
	"url",         // claude WebFetch
	"query",       // claude WebSearch, codex web_search
	"prompt",      // claude Task, and MCP tools that take one
	"description", // last resort: a sentence about the call, not the call
}

// toolSummaryMax caps a summary in runes. Generous enough for a real command
// line, small enough that a pathological argument cannot push a transcript
// record into the megabytes — the cap is here rather than in the TUI because
// every client would otherwise need the same guard.
const toolSummaryMax = 200

// CommandOutputMax caps a CommandOutput in runes. Two orders of magnitude
// above toolSummaryMax because this *is* the output body and a truncated one
// is worth little, and still bounded because a step that runs `go test ./...`
// must not be able to push a single record into the megabytes. The cap is
// here rather than in each client for toolSummaryMax's reason: every one of
// them would otherwise need the same guard, and they would not agree.
const CommandOutputMax = 8000

// TruncateOutput applies CommandOutputMax to a command's output, reporting
// whether it cut. Unlike OneLine it keeps the newlines — an output body read
// as one long line is not the thing that was printed — and it trims to a
// rune boundary so the result is still valid UTF-8.
func TruncateOutput(s string) (string, bool) {
	runes := []rune(s)
	if len(runes) <= CommandOutputMax {
		return s, false
	}
	return string(runes[:CommandOutputMax]), true
}

// ToolSummary extracts a one-line subject from a tool call's arguments.
// raw is the dialect's own arguments object; a payload that is absent,
// unparseable, or carries none of the known keys yields "".
func ToolSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	for _, k := range toolSummaryKeys {
		s, ok := fields[k].(string)
		if !ok {
			continue
		}
		if line := OneLine(s, toolSummaryMax); line != "" {
			return line
		}
	}
	return ""
}

// OneLine flattens text to a single line of at most maxRunes, for a field a
// client renders inline. Whitespace runs — including the newlines of a
// heredoc or a multi-line command — collapse to single spaces, because a
// record's rendering must not depend on what a tool argument happened to
// contain.
func OneLine(s string, maxRunes int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return flat
	}
	runes := []rune(flat)
	if len(runes) <= maxRunes {
		return flat
	}
	return strings.TrimRight(string(runes[:maxRunes]), " ") + "…"
}
