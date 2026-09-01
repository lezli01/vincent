package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The chat workspace's rendering: history, live tail, composer.
//
// The conversation body is the task workspace's output pane, verbatim
// (task 071 decision 2): each turn's records go through outputlines.go's
// renderer at the session's shared level, so a line delivered over
// GET /v1/chats/{id}/events and the same line refetched from that turn's
// transcript are the same line. The only rendering this file still owns is
// what is *around* the records — the turn separators, the prompts, and the
// fallback for a turn whose transcript is gone (§17).

// chatExpandKey names the key that raises the level here. It is not `v`: a
// letter would land in the composer, which owns every printable key
// (decision 4).
const chatExpandKey = "ctrl+r"

// copyDocs is the chat's source for the copy picker: every assistant
// document in the loaded conversation, newest first — the newest turn's
// newest record is "message 1".
//
// A turn whose transcript has gone to retention contributes its ResultText
// instead (§17), which is what the pane is drawing for it: the picker offers
// what is on screen, not what is on disk.
func (v *chatView) copyDocs() []string {
	docs := make([]string, 0, len(v.turns))
	for i := len(v.turns) - 1; i >= 0; i-- {
		t := &v.turns[i]
		if recs := v.turnRecords[t.Seq]; len(recs) > 0 {
			docs = append(docs, copyDocsFromRecords(recs)...)
			continue
		}
		if t.ResultText != "" {
			docs = append(docs, t.ResultText)
		}
	}
	return docs
}

func (v *chatView) render(width, height int) string {
	if width < 4 || height < 4 {
		return ""
	}
	if width > 0 {
		v.width = width
	}
	head := []string{v.headerLine(width), ""}
	foot := v.footerLines(width)
	room := max(height-len(head)-len(foot), 1)
	lines := make([]string, 0, len(head)+room+len(foot))
	lines = append(lines, head...)
	lines = append(lines, strings.Split(v.bodyView(width, room), "\n")...)
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

// bodyView lays the conversation into the viewport. The window is bottom-
// anchored while following — the newest turn is the one being read, and a
// chat that scrolled to the top on every render would be unusable while an
// agent is talking — and stays where a manual scroll put it otherwise
// (decision 5).
func (v *chatView) bodyView(width, height int) string {
	v.vp.SetWidth(max(width, 1))
	v.vp.SetHeight(max(height, 1))
	if v.bodyDirty || v.builtWidth != width {
		v.vp.SetContent(strings.Join(v.bodyLines(width), "\n"))
		v.bodyDirty = false
		v.builtWidth = width
		if v.following {
			v.vp.GotoBottom()
		}
	}
	return v.vp.View()
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
	// The link is permanent and lives in the header rather than in the note
	// line, which is transient: a handed-off chat is history, and the one
	// thing a reader of it wants is where the work went (task 074).
	if v.chat.HandoffTaskID != nil {
		left += styleDim.Render(fmt.Sprintf("  ·  handed off to task %d", *v.chat.HandoffTaskID))
	}
	right := plural(len(v.turns), "turn", "turns")
	// The level rides in the header for the reason the output pane's title
	// carries it: ctrl+r on a conversation with no reasoning and no
	// unrecognized lines changes nothing on screen, and a reader needs to
	// see that the key did something.
	if l := v.level.get(); l != levelNormal {
		right = l.String() + "  ·  " + right
	}
	if v.raw.get() {
		right = "raw  ·  " + right
	}
	if !v.following {
		right = "⏸ " + right
	}
	return padBetween(left, styleDim.Render(right+" "), width)
}

// bodyLines is the whole conversation: every turn's prompt followed by its
// records, at the session's level.
//
// It also records where each turn starts, which is what lets a scroll say
// which turns are on screen and fetch the transcripts they need (decision 6).
func (v *chatView) bodyLines(width int) []string {
	lines := make([]string, 0, len(v.turns)*4)
	v.turnAt = make(map[int]int, len(v.turns))
	if v.truncated {
		lines = append(lines, styleDim.Render(gutterNone+
			"… earlier output truncated — the transcripts on disk are whole"))
	}
	level := v.level.get()
	raw := v.raw.get()
	for i := range v.turns {
		t := &v.turns[i]
		v.turnAt[t.Seq] = len(lines)
		lines = append(lines, styleDim.Render(fmt.Sprintf(" ── turn %d ──", t.Seq)))
		lines = append(lines, wrapCellLines("› "+t.Prompt, width-2, 6)...)
		switch recs := v.turnRecords[t.Seq]; {
		case len(recs) > 0:
			lines = append(lines, outputLines(recs, level, width,
				lineOpts{expandKey: chatExpandKey, raw: raw})...)
		case t.State == "running":
			// Nothing has arrived yet; the tail fills in as it does.
		case t.ResultText != "":
			// §17: a transcript that has gone to retention still leaves the
			// turn's answer, and it is shown with no banner — a reader who
			// did not ask for the record is not told it is missing.
			//
			// It goes through the same renderer the records do (task 073
			// decision 5): the answer is assistant prose whichever door it
			// came in, and the first acceptance criterion is that the same
			// Markdown renders identically in both workspaces at the same
			// width. That drops the 40-line cap and the two-column
			// narrowing this used to apply — a strict improvement, since a
			// retained-away answer is all the turn has left and the cap hid
			// its tail.
			// It follows the rendered/raw toggle too (task 076 decision 2):
			// a retained-away answer is still assistant prose, and the
			// mode is the session's, not the record's.
			lines = append(lines, assistantLines(t.ResultText, width, raw)...)
		}
		if t.FailReason != "" {
			lines = append(lines, " "+styleBad.Render(
				strings.TrimSpace(t.FailReason+" "+t.ErrorMessage)))
		}
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
	hint := " enter send · ctrl+x stop the turn · ctrl+r detail · " +
		rawToggleKey + " raw · " + copyPickKey + " copy · " +
		"pgup/pgdown scroll · ctrl+g live · esc back to the chats board"
	out = append(out, styleDim.Render(ansi.Truncate(hint, width, "…")))
	return out
}
