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
func (v *chatView) copyDocs() []copyDoc {
	docs := make([]copyDoc, 0, len(v.turns))
	for i := len(v.turns) - 1; i >= 0; i-- {
		t := &v.turns[i]
		if recs := v.turnRecords[t.Seq]; len(recs) > 0 {
			docs = append(docs, copyDocsFromRecords(recs, v.recordSeqs(t.Seq))...)
			continue
		}
		if t.ResultText != "" {
			// A retained-away answer has no record and so no identity: it
			// is offered as the captured text, which is what the pane is
			// drawing for it.
			docs = append(docs, copyDoc{text: t.ResultText})
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
	// The form is part of the budget, not something added after it (issue
	// #299): appended past the join it pushed the same number of lines off the
	// bottom of the terminal that the unsplit composer did.
	var form []string
	if v.form != nil {
		form = strings.Split(v.form.render(width, min(v.form.height(width), height/2)), "\n")
	}
	room := max(height-len(head)-len(foot)-len(form), 1)
	lines := make([]string, 0, len(head)+room+len(foot)+len(form))
	lines = append(lines, head...)
	lines = append(lines, strings.Split(v.bodyView(width, room), "\n")...)
	lines = append(lines, foot...)
	lines = append(lines, form...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
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
		// The paused anchor, as in the task workspace (#291): a resize, the
		// maxRecords cap and the level and raw toggles all rebuild the body,
		// and a reader who scrolled away from the tail keeps their place
		// across every one of them.
		keep := anchorAt(v.anchors, v.vp.YOffset())
		lines, anchors := v.bodyLinesAt(width)
		v.vp.SetContent(strings.Join(lines, "\n"))
		v.anchors = anchors
		v.bodyDirty = false
		v.builtWidth = width
		switch y, ok := anchorIndex(anchors, keep); {
		case v.following:
			v.vp.GotoBottom()
		case ok:
			v.vp.SetYOffset(y)
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
	lines, _ := v.bodyLinesAt(width)
	return lines
}

// bodyLinesAt is bodyLines with the per-line provenance the paused anchor
// needs, and is where the conversation's render pass opens and closes — one
// pass over every turn, so the memo sweeps what left the screen and keeps
// what did not.
func (v *chatView) bodyLinesAt(width int) ([]string, []lineAnchor) {
	lines := make([]string, 0, len(v.turns)*4)
	anchors := make([]lineAnchor, 0, len(v.turns)*4)
	pad := func(n int) {
		for range n {
			anchors = append(anchors, lineAnchor{})
		}
	}
	v.turnAt = make(map[int]int, len(v.turns))
	if v.truncated {
		lines = append(lines, styleDim.Render(gutterNone+
			"… earlier output truncated — the transcripts on disk are whole"))
		pad(1)
	}
	level := v.level.get()
	raw := v.raw.get()
	v.mdcache.begin()
	defer v.mdcache.sweep()
	for i := range v.turns {
		t := &v.turns[i]
		v.turnAt[t.Seq] = len(lines)
		lines = append(lines, styleDim.Render(fmt.Sprintf(" ── turn %d ──", t.Seq)))
		// The human's half of the turn is a right-aligned bubble; the agent's
		// half below is untouched, flush left at the full width. The pad is
		// by the bubble's *actual* height, so the prompt keeps carrying the
		// zero anchor and v.turnAt[t.Seq] still points at the separator.
		prompt := promptBubbleLines(t.Prompt, width)
		lines = append(lines, prompt...)
		pad(1 + len(prompt))
		switch recs := v.turnRecords[t.Seq]; {
		case len(recs) > 0:
			body, at := outputLinesAt(recs, v.recordSeqs(t.Seq), level, width,
				lineOpts{expandKey: chatExpandKey, raw: raw, cache: &v.mdcache})
			lines = append(lines, body...)
			anchors = append(anchors, at...)
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
			body, _ := v.mdcache.lines(t.ResultText, width, level, raw)
			lines = append(lines, body...)
			pad(len(body))
		}
		if t.FailReason != "" {
			lines = append(lines, " "+styleBad.Render(
				strings.TrimSpace(t.FailReason+" "+t.ErrorMessage)))
			pad(1)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, styleDim.Render("  Nothing said yet. Type below and press enter."))
		pad(1)
	}
	return lines, anchors
}

// chatComposerWidth is how many columns the composer gets inside its border:
// the pane's, less the frame's two. It is one function because two callers
// size the composer — footerLines on every render and the WindowSizeMsg
// handler in chatview.go — and two copies of the arithmetic drift.
func chatComposerWidth(pane int) int { return max(pane-2, 10) }

func (v *chatView) footerLines(width int) []string {
	out := []string{""}
	if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		out = append(out, " "+style.Render(v.note))
	}
	// One element per rendered line, not one per widget (issue #299): the
	// composer is SetHeight(3) and bubbles' viewport pads its View to that
	// height, and the border around it adds two more, so a joined string would
	// report a height of 1 and render 5. render's
	// `room := height - len(head) - len(foot)` would then overrun the frame by
	// four lines — the frame keeps the first h-4 — and the hint line below
	// would never be drawn. It would also feed a multi-line string to render's
	// per-line ansi.Truncate pass, which measures the whole thing as the *sum*
	// of its rows — ansi.StringWidth treats the `\n`s as zero-width and never
	// resets — so once that sum passes the pane width the tail is cut and the
	// box collapses to its top edge. That is why the frame is split here the
	// same way the composer's own View is, and why the border is spent out of
	// the budget rather than added after the join: §15's #299 amendment, "a
	// composer that grew is a body that shrank, not a frame that overflowed".
	//
	// The width is the pane's, not the terminal's: render is what knows how
	// many columns the composer actually has, and a composer sized from the
	// whole terminal has its rows cut by the Truncate pass above. Two of them
	// go to the border, which is what shell.go's frame draws in.
	v.composer.SetWidth(chatComposerWidth(width))
	rows := strings.Split(v.composer.View(), "\n")
	// focused=false on purpose: the composer holds the keyboard nearly always
	// here, so a permanently lit focus glyph would say nothing.
	box := frame("message", v.composer.View(), width, len(rows)+2, false)
	if box == "" {
		// Narrower or shorter than a box can be drawn in; the rows still are.
		out = append(out, rows...)
	} else {
		out = append(out, strings.Split(box, "\n")...)
	}
	hint := " enter send · ctrl+x stop the turn · ctrl+r detail · " +
		rawToggleKey + " raw · " + copyPickKey + " copy · " +
		"pgup/pgdown scroll · ctrl+g live · esc back to the chats board"
	out = append(out, styleDim.Render(ansi.Truncate(hint, width, "…")))
	return out
}
