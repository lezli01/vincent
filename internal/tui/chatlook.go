package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// How a chat turn reads in the workspace (task 150.5): the human's card, a
// heading that names who answered and when, and what the agent did as short
// check lines. The preview and the open conversation share all three, so
// opening a chat never changes how the conversation looks.

// chatTurnHeading heads the agent's half of a turn: who answered, which turn,
// when, and — once it ended — what it spent (task 157, chatTurnUsage).
func chatTurnHeading(agent string, t apiclient.ChatTurn, age string) string {
	line := toneMagenta.Bold(true).Render(ic("◈ ")+oneLine(agent)) + styleDim.Render(fmt.Sprintf("  ·  turn %d", t.Seq))
	if age != "" {
		line += styleDim.Render("  ·  " + age)
	}
	for _, part := range chatTurnUsage(t) {
		line += styleDim.Render("  ·  " + part)
	}
	return line
}

// chatTurnAges is how long ago a turn's prompt was sent and its answer ended;
// a turn still running is dated by its start.
func chatTurnAges(t apiclient.ChatTurn, now time.Time) (asked, answered string) {
	asked = issueAge(now, t.StartedAt)
	if t.EndedAt != nil {
		return asked, issueAge(now, *t.EndedAt)
	}
	return asked, asked
}

// toolCheckIndex pairs a transcript's tool calls with their outcomes, so a call
// and its result are one line. Only calls that can be paired are drawn this
// way: one with no call id, or a skill load, keeps the output pane's own lines.
type toolCheckIndex struct {
	results map[string]apiclient.TranscriptToolResult
	calls   map[string]bool
	skills  map[string]apiclient.TranscriptRecord
}

func newToolCheckIndex(records []apiclient.TranscriptRecord) *toolCheckIndex {
	x := &toolCheckIndex{
		results: map[string]apiclient.TranscriptToolResult{},
		calls:   map[string]bool{},
		skills:  skillLoadsAtCalls(records),
	}
	for _, rec := range records {
		if rec.ParentCallID != "" {
			continue
		}
		switch rec.Type {
		case "agent.tool_use":
			if x.covers(rec.Tools) {
				for _, t := range rec.Tools {
					x.calls[t.CallID] = true
				}
			}
		case "agent.tool_result":
			for _, r := range rec.Results {
				if r.CallID != "" {
					x.results[r.CallID] = r
				}
			}
		}
	}
	return x
}

// covers reports whether every call in a tool_use record is drawn as a check.
func (x *toolCheckIndex) covers(tools []apiclient.TranscriptTool) bool {
	if len(tools) == 0 {
		return false
	}
	for _, t := range tools {
		if _, skill := skillAtCall(t, x.skills); skill || t.CallID == "" {
			return false
		}
	}
	return true
}

// absorbs reports whether a tool_result record's outcomes are all on their
// calls' check lines already.
func (x *toolCheckIndex) absorbs(results []apiclient.TranscriptToolResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !x.calls[r.CallID] {
			return false
		}
	}
	return true
}

// line is one call as a check line. The mark carries the outcome without
// colour: done, failed, refused, or still going.
func (x *toolCheckIndex) line(t apiclient.TranscriptTool) paneLine {
	mark, style, outcome := "◌ ", styleDim, ""
	if r, ok := x.results[t.CallID]; ok {
		switch {
		case r.Blocked:
			mark, style, outcome = "⊘ ", styleBad, firstNonEmpty(r.Summary, "blocked")
		case r.IsError:
			mark, style, outcome = "✗ ", styleBad, firstNonEmpty(r.Summary, "failed")
		default:
			mark, style, outcome = "✓ ", styleOK, r.Verb
		}
	}
	segs := []segment{{text: strings.ToLower(t.Name), style: styleDim}}
	if t.Summary != "" {
		segs = append(segs, segment{text: " " + t.Summary, style: toneCyan})
	}
	if outcome != "" {
		segs = append(segs, segment{text: " · " + outcome, style: styleDim})
	}
	return paneLine{gutter: mark, gutterStyle: style, segs: segs}
}

// chatPreviewCheckCap is how many check lines one previewed turn draws.
const chatPreviewCheckCap = 8

// chatPreviewChecks is a previewed turn's tool activity, newest last.
func chatPreviewChecks(records []apiclient.TranscriptRecord, width int) []string {
	x := newToolCheckIndex(records)
	var checks []paneLine
	for _, rec := range records {
		if rec.Type != "agent.tool_use" || rec.ParentCallID != "" || !x.covers(rec.Tools) {
			continue
		}
		for _, t := range rec.Tools {
			checks = append(checks, x.line(t))
		}
	}
	var out []string
	if hidden := len(checks) - chatPreviewCheckCap; hidden > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("… %d earlier", hidden)))
		checks = checks[hidden:]
	}
	for _, c := range checks {
		out = append(out, wrapLine(c, width)...)
	}
	return out
}

// sanitizeToolRecord keeps what a preview draws of a tool record, with any
// escape an agent's tool arguments carried removed.
func sanitizeToolRecord(rec apiclient.TranscriptRecord) apiclient.TranscriptRecord {
	out := apiclient.TranscriptRecord{Type: rec.Type, ParentCallID: rec.ParentCallID}
	for _, t := range rec.Tools {
		t.Name, t.Summary = sanitizeText(t.Name), sanitizeText(t.Summary)
		out.Tools = append(out.Tools, t)
	}
	for _, r := range rec.Results {
		r.Summary, r.Verb = sanitizeText(r.Summary), sanitizeText(r.Verb)
		out.Results = append(out.Results, r)
	}
	return out
}
