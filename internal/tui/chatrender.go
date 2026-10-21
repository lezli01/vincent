package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/reasons"
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
	v.height = height
	head := []string{v.headerLine(width), ""}
	// The §7.4 popup takes the composer's place rather than stacking under
	// it (issue #801 F7): it owns the keyboard, so a composer drawn beside
	// it is a field nobody can type into, and the two together left the
	// conversation two lines at 80×24. The form is still part of the budget,
	// not something added after it (issue #299).
	var foot []string
	if v.form != nil {
		foot = v.formLines(width, height)
	} else {
		foot = v.footerLines(width, height)
	}
	room := max(height-len(head)-len(foot), 1)
	lines := make([]string, 0, len(head)+room+len(foot))
	lines = append(lines, head...)
	lines = append(lines, strings.Split(v.bodyView(width, room), "\n")...)
	lines = append(lines, foot...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// formLines is the §7.4 popup in the composer's place, capped at half the
// pane, with its key line pinned under the window (issue #801 F7): windowing
// the whole form around the cursor cut the line that says how to submit or
// leave it, which is the one line every question needs.
func (v *chatView) formLines(width, height int) []string {
	lines, from, to := v.form.lines(width)
	keys := lines[len(lines)-1]
	rows := windowRange(lines[:len(lines)-1], from, to, max(height/2-1, 1))
	out := make([]string, 0, len(rows)+2)
	out = append(out, "")
	out = append(out, rows...)
	return append(out, keys)
}

// bodyView lays the conversation into the viewport. The window is bottom-
// anchored while following — the newest turn is the one being read, and a
// chat that scrolled to the top on every render would be unusable while an
// agent is talking — and stays where a manual scroll put it otherwise
// (decision 5).
func (v *chatView) bodyView(width, height int) string {
	v.vp.SetWidth(max(width, 1))
	v.vp.SetHeight(max(height, 1))
	if v.bodyDirty || v.builtWidth != width || v.builtLinks != v.links.get() || v.builtLevel != v.level.get() {
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
		v.builtLinks = v.links.get()
		v.builtLevel = v.level.get()
		switch y, ok := anchorIndex(anchors, keep); {
		case v.following:
			v.vp.GotoBottom()
		case ok:
			v.vp.SetYOffset(y)
		}
	}
	return v.vp.View()
}

// chatHeaderSeg is one cell of the header and the rank it is dropped at
// when the line is short of room: the highest rank goes first.
type chatHeaderSeg struct {
	text string
	rank int
}

// chatHeaderTitleMin is how many cells the title keeps before a lower-ranked
// cell is dropped for it: enough to tell two chats apart, and no more —
// every ranked cell outranks the rest of the title.
const chatHeaderTitleMin = 8

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
	// The cells after the title, in display order, ranked by what a reader
	// most needs (issue #801 F4): the title came first and padBetween never
	// shrinks the left side, so a long one pushed the state, the branch and
	// the link off the line. Now the title truncates into what is left.
	mid := []chatHeaderSeg{
		{applyStateStyle(v.chat.State, chatStateLabel(v.chat.State)), 0},
		{styleDim.Render(v.chat.Agent), 7},
	}
	if v.chat.Branch != "" {
		mid = append(mid, chatHeaderSeg{styleDim.Render(v.chat.Branch), 6})
	}
	// The link is permanent and lives in the header rather than in the note
	// line, which is transient: a handed-off chat is history, and the one
	// thing a reader of it wants is where the work went (task 074).
	if v.chat.HandoffTaskID != nil {
		mid = append(mid, chatHeaderSeg{styleDim.Render(fmt.Sprintf("handed off to task %d", *v.chat.HandoffTaskID)), 5})
	}
	// The same permanence for a chat opened on a task (task 119): the task
	// is whose worktree this is, and whose lock this chat holds while open.
	if v.chat.LinkedTaskID != nil {
		mid = append(mid, chatHeaderSeg{styleDim.Render(fmt.Sprintf("on task #%d", *v.chat.LinkedTaskID)), 5})
	}
	pause := styleDim.Render("⏸")
	var right []chatHeaderSeg
	if !v.following {
		right = append(right, chatHeaderSeg{pause, 1})
	}
	if v.raw.get() {
		right = append(right, chatHeaderSeg{styleDim.Render("raw"), 2})
	}
	// The level rides in the header for the reason the output pane's title
	// carries it: ctrl+r on a conversation with no reasoning and no
	// unrecognized lines changes nothing on screen, and a reader needs to
	// see that the key did something.
	if l := v.level.get(); l != levelNormal {
		right = append(right, chatHeaderSeg{styleDim.Render(l.String()), 2})
	}
	// Where the window is, while it is not at the live end (issue #801 F8):
	// `⏸` says the body stopped following, and this says how far back it is.
	if pos := v.turnPosition(); pos != "" {
		right = append(right, chatHeaderSeg{styleDim.Render(pos), 4})
	}
	right = append(right, chatHeaderSeg{styleDim.Render(plural(len(v.turns), "turn", "turns")), 3})

	sep := styleDim.Render("  ·  ")
	join := func(segs []chatHeaderSeg, sepw string) string {
		var b strings.Builder
		for i, sg := range segs {
			switch {
			case i == 0:
			case segs[i-1].text == pause:
				// `⏸` leads what follows it, as it always has.
				b.WriteString(" ")
			default:
				b.WriteString(sepw)
			}
			b.WriteString(sg.text)
		}
		return b.String()
	}
	title := v.chat.Title
	for {
		midText, rightText := join(mid, sep), join(right, styleDim.Render("  ·  "))
		used := 1 + ansi.StringWidth(sep) + ansi.StringWidth(midText) + 2 + ansi.StringWidth(rightText) + 1
		room := width - used
		if width <= 0 || room >= min(ansi.StringWidth(title), chatHeaderTitleMin) || !dropHighest(&mid, &right) {
			t := title
			if width > 0 {
				t = ansi.Truncate(title, max(room, 1), "…")
			}
			return padBetween(" "+styleTitle.Render(t)+sep+midText, rightText+" ", width)
		}
	}
}

// dropHighest removes the highest-ranked cell of either side, and reports
// whether there was one to remove. Rank 0 — the state — is never dropped.
func dropHighest(mid, right *[]chatHeaderSeg) bool {
	best, side, at := 0, 0, -1
	for i, sg := range *mid {
		if sg.rank > best {
			best, side, at = sg.rank, 0, i
		}
	}
	for i, sg := range *right {
		if sg.rank > best {
			best, side, at = sg.rank, 1, i
		}
	}
	if at < 0 {
		return false
	}
	if side == 0 {
		*mid = append((*mid)[:at:at], (*mid)[at+1:]...)
	} else {
		*right = append((*right)[:at:at], (*right)[at+1:]...)
	}
	return true
}

// turnPosition is the `turn i/n` cue (issue #801 F8): the turn whose lines
// are at the top of the window, while the reader has scrolled away from the
// live end. Following, the newest turn is the one on screen and the cue
// would say nothing the turn count does not.
func (v *chatView) turnPosition() string {
	if v.following || len(v.turns) == 0 || len(v.turnAt) == 0 {
		return ""
	}
	top := v.vp.YOffset()
	at := 0
	for i := range v.turns {
		if start, ok := v.turnAt[v.turns[i].Seq]; ok && start <= top {
			at = i + 1
		}
	}
	return fmt.Sprintf("turn %d/%d", max(at, 1), len(v.turns))
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
	links := v.links.get()
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
				lineOpts{expandKey: chatExpandKey, raw: raw, hyperlinks: links, cache: &v.mdcache})
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
			body, _ := v.mdcache.lines(t.ResultText, width, level, raw, links)
			lines = append(lines, body...)
			pad(len(body))
		}
		if t.FailReason != "" {
			// Wrapped, never clipped (issue #801 F3): the message is the
			// one account of what went wrong, and its tail — "retrying
			// later is safe" — is usually the part worth reading. The
			// reason is humanised through the one catalogue of them.
			text := reasons.Explain(t.FailReason).Title
			if t.ErrorMessage != "" {
				text += ": " + t.ErrorMessage
			}
			for _, l := range wrapText(text, max(width-1, 1), wrapOpts{indent: 2}) {
				lines = append(lines, " "+styleBad.Render(l))
				pad(1)
			}
		}
	}
	if len(lines) == 0 {
		switch {
		case v.chat == nil && v.loadErr != "":
			// A failed load is not an empty chat (issue #801 F9): the body
			// says why there is nothing to show, not that nothing was said.
			for _, l := range wrapText("could not load this chat: "+v.loadErr, max(width-2, 1), wrapOpts{indent: 2}) {
				lines = append(lines, "  "+styleBad.Render(l))
				pad(1)
			}
		case v.chat == nil:
			lines = append(lines, styleDim.Render("  loading…"))
			pad(1)
		default:
			lines = append(lines, styleDim.Render("  Nothing said yet. Type below and press enter."))
			pad(1)
		}
	}
	return lines, anchors
}

// chatComposerWidth is how many columns the composer gets inside its border:
// the pane's, less the frame's two. It is one function because two callers
// size the composer — footerLines on every render and the WindowSizeMsg
// handler in chatview.go — and two copies of the arithmetic drift.
func chatComposerWidth(pane int) int { return max(pane-2, 10) }

// chatNoteRows is how many rows the note line may wrap to (issue #801 F2),
// taken out of the body's budget like everything else under it.
const chatNoteRows = 3

// noteLines is the one note under the conversation, wrapped: the close
// confirmation, the workspace's own note, the reminder that a question is
// put away, the inline skill hint or the mention note — the first that has
// something to say. A question keeps its answer keys whatever the width.
func (v *chatView) noteLines(width int) []string {
	text, style, keys := "", styleDim, ""
	switch {
	case v.closing && v.chat != nil:
		text, style, keys = v.closeQuestion(), styleWarn, chatCloseAnswers
	case v.note != "":
		text = v.note
		if v.noteBad {
			style = styleBad
		}
	case v.formAway != nil:
		// Persistent while the question is away (issue #801 F7): it is the
		// only thing on screen saying the agent is waiting, and how to answer.
		text, style = chatFormAwayNote, styleWarn
	case v.skills.inlineNote != "":
		// The inline skill list's own line (task 124.14): a sigil token the
		// agent did not report, or what the skill just accepted takes. Dim,
		// and below the workspace's own note rather than over it — this one
		// is a hint about a draft, never a refusal.
		text = v.skills.inlineNote
	case v.files.mentionNote() != "":
		// The file picker's own line (task 126.11): the mention verdict, and
		// only in the negative — this chat's CLI does not expand an `@`
		// mention, so it sees the path and may read the file itself
		// (task 126 decision 47, after task 124 decision 19's bad-news-only
		// rule). Dim, and below the workspace's own note for the reason the
		// skills hint is: it is a fact about a draft, never a refusal.
		text = v.files.mentionNote()
	default:
		return nil
	}
	return wrapNote(text, keys, width, style)
}

// wrapNote wraps text into at most chatNoteRows rows of width, each led by
// one space and styled on its own. keys, when there are any, end the last
// row whole: the question is what gives way to them.
func wrapNote(text, keys string, width int, style lipgloss.Style) []string {
	room := max(width-1, 1)
	full := text
	if keys != "" {
		full += " " + keys
	}
	lines := wrapText(full, room, wrapOpts{maxLines: chatNoteRows})
	if keys != "" && !strings.HasSuffix(lines[len(lines)-1], keys) {
		// Capped mid-question: wrap the question into what the keys leave.
		lines = wrapText(text, max(room-ansi.StringWidth(keys)-1, 1), wrapOpts{maxLines: chatNoteRows})
		lines[len(lines)-1] += " " + keys
	}
	for i, l := range lines {
		lines[i] = " " + style.Render(l)
	}
	return lines
}

// chatComposerMin and chatComposerMax bound the composer's rows (issue #801
// F6): three for an empty or short draft, growing with the draft to ten or a
// third of the pane, whichever is less.
const (
	chatComposerMin = 3
	chatComposerMax = 10
)

// sizeComposer grows the composer to the draft and reports how many of the
// draft's rows it still cannot show. The count is of wrapped rows at the
// composer's width — a long paragraph is as tall as a list of short lines.
func (v *chatView) sizeComposer(height int) (hidden int) {
	inner := max(v.composer.Width(), 1)
	rows := 0
	for _, line := range strings.Split(v.composer.Value(), "\n") {
		rows += max((ansi.StringWidth(line)+inner-1)/inner, 1)
	}
	limit := max(min(chatComposerMax, height/3), chatComposerMin)
	v.composer.SetHeight(min(max(rows, chatComposerMin), limit))
	return max(rows-limit, 0)
}

func (v *chatView) footerLines(width, height int) []string {
	out := []string{""}
	out = append(out, v.noteLines(width)...)
	// The in-progress indicator (task 089), here and not inline at the end of
	// the running turn's body: the body scrolls, and a reader who has scrolled
	// up — follow off, `⏸` in the header — is exactly the reader who needs to
	// be told that waiting is still the right thing to do. The footer is drawn
	// at every scroll position and it is next to where they type.
	//
	// It also survives the bodyDirty gate, which is what makes it useful at
	// levelQuiet: a turn that spends minutes running tools renders no new body
	// lines at all, and the frame here is the only thing that moves.
	//
	// This is not the "never a spinner" case §15 rules out. That rule is about
	// a send the daemon *refused* — a 409 chat_cap_reached, a turn that will
	// never exist and must not be drawn as pending. This draws only for a turn
	// the daemon has in `running`, so the two never apply to the same turn.
	if turn := v.runningTurn(); turn != nil {
		out = append(out, " "+styleDim.Render(ProgressLabel(v.frame, v.now().Sub(turn.StartedAt))))
	}
	// The skill list sits between the note line and the composer (task
	// 124.13), one slice element per rendered line and its height spent out
	// of the body's budget — a list that opened is a body that shrank, not a
	// frame that overflowed (#299).
	out = append(out, v.skills.render(width, height, v.now())...)
	// The `@` file picker sits in the same place and out of the same budget
	// (task 126.11). Appending both is safe because at most one is ever open:
	// each requires its own first rune of the one token under the cursor, and
	// syncInlineFiles stands this one down for an adapter that claimed `@`.
	out = append(out, v.files.render(width, height)...)
	// One element per rendered line, not one per widget (issue #299): bubbles'
	// viewport pads the composer's View to its height, and the border around
	// it adds two more, so a joined string would report a height of 1 and
	// render several. render's `room := height - len(head) - len(foot)` would
	// then overrun the frame. It would also feed a multi-line string to
	// render's per-line ansi.Truncate pass, which measures the whole thing as
	// the *sum* of its rows — ansi.StringWidth treats the `\n`s as zero-width
	// and never resets — so once that sum passes the pane width the tail is
	// cut and the box collapses to its top edge. That is why the frame is
	// split here the same way the composer's own View is, and why the border
	// is spent out of the budget rather than added after the join: §15's #299
	// amendment, "a composer that grew is a body that shrank, not a frame
	// that overflowed" — which since issue #801 F6 it does, with the draft.
	//
	// The width is the pane's, not the terminal's: render is what knows how
	// many columns the composer actually has, and a composer sized from the
	// whole terminal has its rows cut by the Truncate pass above. Two of them
	// go to the border, which is what shell.go's frame draws in.
	v.composer.SetWidth(chatComposerWidth(width))
	title := "message"
	if hidden := v.sizeComposer(height); hidden > 0 {
		// The draft is taller than the composer may grow: say how much of
		// it is out of sight rather than let half a draft read as all of it.
		title += " · +" + plural(hidden, "line", "lines")
	}
	rows := strings.Split(v.composer.View(), "\n")
	// focused=false on purpose: the composer holds the keyboard nearly always
	// here, so a permanently lit focus glyph would say nothing.
	box := frame(title, v.composer.View(), width, len(rows)+2, false)
	if box == "" {
		// Narrower or shorter than a box can be drawn in; the rows still are.
		out = append(out, rows...)
	} else {
		out = append(out, strings.Split(box, "\n")...)
	}
	// No key line of its own (issue #801 F5): the root draws the registry's
	// footer under the workspace, and a second, literal one disagreed with
	// it about which keys were live.
	return out
}
