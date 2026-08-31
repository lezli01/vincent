package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's rendering: history, live tail, composer.

func (v *chatView) render(width, height int) string {
	if width < 4 || height < 4 {
		return ""
	}
	head := []string{v.headerLine(width), ""}
	foot := v.footerLines(width)
	body := v.bodyLines(width)
	room := max(height-len(head)-len(foot), 1)
	// The conversation is anchored at the bottom: the newest turn is the one
	// being read, and a chat that scrolled to the top on every render would
	// be unusable while an agent is talking.
	lines := make([]string, 0, len(head)+room+len(foot))
	lines = append(lines, head...)
	lines = append(lines, window(body, max(len(body)-1, 0), room)...)
	lines = append(lines, foot...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	out := strings.Join(lines, "\n")
	if v.form != nil {
		return out + "\n" + v.form.render(width, min(v.form.height(width), height/2))
	}
	return out
}

func (v *chatView) headerLine(width int) string {
	if v.chat == nil {
		left := " " + styleTitle.Render("chat")
		if v.loadErr != "" {
			left += styleDim.Render("  ·  " + v.loadErr)
		} else {
			left += styleDim.Render("  ·  loading…")
		}
		return padBetween(left, "", width)
	}
	left := " " + styleTitle.Render(v.chat.Title) +
		styleDim.Render("  ·  ") + applyStateStyle(v.chat.State, chatStateLabel(v.chat.State)) +
		styleDim.Render(fmt.Sprintf("  ·  %s  ·  %s", v.chat.Agent, v.chat.Branch))
	right := styleDim.Render(fmt.Sprintf("%s ", plural(len(v.turns), "turn", "turns")))
	return padBetween(left, right, width)
}

// bodyLines is the whole conversation: every finished turn, then the running
// turn's live tail.
func (v *chatView) bodyLines(width int) []string {
	lines := make([]string, 0, len(v.turns)*4+len(v.scrollback))
	for i := range v.turns {
		t := &v.turns[i]
		lines = append(lines, styleDim.Render(fmt.Sprintf(" ── turn %d ──", t.Seq)))
		lines = append(lines, wrapCellLines("› "+t.Prompt, width-2, 6)...)
		switch {
		case t.State == "running":
			// The running turn renders from the tail below, not from here:
			// its ResultText does not exist yet.
		case t.FailReason != "":
			lines = append(lines, " "+styleBad.Render(
				strings.TrimSpace(t.FailReason+" "+t.ErrorMessage)))
		case t.ResultText != "":
			lines = append(lines, wrapCellLines(t.ResultText, width-2, 40)...)
		}
	}
	if len(v.scrollback) > 0 {
		lines = append(lines, styleDim.Render(" ── live ──"))
		lines = append(lines, v.scrollback...)
	}
	if len(lines) == 0 {
		lines = append(lines, styleDim.Render("  Nothing said yet. Type below and press enter."))
	}
	return lines
}

func (v *chatView) footerLines(width int) []string {
	out := []string{""}
	if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		out = append(out, " "+style.Render(v.note))
	}
	out = append(out, v.composer.View())
	hint := " enter send · ctrl+x stop the turn · esc back to the chats board"
	out = append(out, styleDim.Render(ansi.Truncate(hint, width, "…")))
	return out
}

// transcriptLines renders fetched transcript records as tail lines. It is
// deliberately plain: the live tail's job is to show that something is
// happening and what, and the durable record is one route away.
func transcriptLines(records []apiclient.TranscriptRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		if line := transcriptLine(r); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func transcriptLine(r apiclient.TranscriptRecord) string {
	switch r.Type {
	case "agent.output", "vincent.output", "command.output":
		return "  " + strings.TrimRight(r.Text, "\n")
	case "agent.thinking":
		return "  " + styleDim.Render(strings.TrimRight(r.Text, "\n"))
	case "agent.tool_use":
		names := make([]string, 0, len(r.Tools))
		for _, t := range r.Tools {
			names = append(names, t.Name)
		}
		if len(names) == 0 {
			return ""
		}
		return "  " + styleDim.Render("· "+strings.Join(names, ", "))
	case "agent.error":
		return "  " + styleBad.Render(r.Message)
	default:
		return ""
	}
}

// outputNoteLine renders one live chunk. The chunk carries the agent's own
// raw line, so it is decoded the same way the transcript route's normalized
// records are — one shape for scrollback and tail, which is what §13.3's
// normalization is for.
func outputNoteLine(note apiclient.OutputNote) string {
	var body struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(note.Payload, &body); err != nil || body.Raw == "" {
		return ""
	}
	var rec apiclient.TranscriptRecord
	if err := json.Unmarshal([]byte(body.Raw), &rec); err == nil && rec.Type != "" {
		if line := transcriptLine(rec); line != "" {
			return line
		}
	}
	// A dialect line the client cannot name is still evidence the agent is
	// working; it is shown dimmed rather than dropped.
	return "  " + styleDim.Render(ansi.Truncate(strings.TrimSpace(body.Raw), 200, "…"))
}
