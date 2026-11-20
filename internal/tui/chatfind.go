package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Task 160: the chat's conversation focus — search, prompt jumps, per-call
// expansion — and the `↓ N new` marker.
//
// The composer keeps the keyboard by default (task 071 decision 4, as
// amended by task 160 decision 1). ctrl+f hands it to the conversation and
// opens the search line; esc peels one layer at a time, search line →
// conversation focus → composer, and never leaves the chat from either.

// chatSearchKey opens the conversation's search line from the composer. A
// combination, because the composer owns every printable key.
const chatSearchKey = "ctrl+f"

// chatConvSearchQuery is the longest query the search line takes, matching
// the daemon's bound (GET /v1/chats/{id}/search).
const chatConvSearchQuery = 256

// chatCallKey names one expanded record within a chat (task 160 decision 4):
// the turn's seq and the record's tool call id, or, for a record with none,
// its position in the turn — the client's record identities are reassigned on
// every refetch, and the set must survive leaving the chat and coming back.
type chatCallKey struct {
	turn int
	call string
	idx  int
}

// chatMatch is one occurrence of the query.
type chatMatch struct {
	turn int
	// idx is the record's position in the turn, or one of the two below.
	idx int
	// nth is the occurrence's ordinal within its text, or within the turn
	// when the turn is unloaded.
	nth int
	// unloaded marks a match the daemon counted in a turn whose transcript
	// this view has not fetched: landing on it fetches exactly that turn.
	unloaded bool
}

const (
	matchPrompt     = -1 // in the turn's `You` card
	matchResultText = -2 // in the turn's result_text, drawn when it has no records
)

// chatSearchState is the search line and its results.
type chatSearchState struct {
	// typing is the search line open for input; buf is what it holds.
	typing bool
	buf    string
	// query is the committed search, "" for none.
	query string
	// server is the daemon's per-turn count for the current query.
	server  map[int]int
	matches []chatMatch
	current int
	// land is a match in an unloaded turn waiting for that turn's
	// transcript, by its turn and ordinal within the turn.
	land *chatMatch
	err  string
}

// chatSearchMsg is the daemon's answer to GET /v1/chats/{id}/search.
type chatSearchMsg struct {
	chatID int64
	query  string
	result *apiclient.ChatSearch
	err    error
}

// focusConversation hands the keyboard to the conversation. The draft is
// untouched; the composer only stops taking keys.
func (v *chatView) focusConversation() {
	if v.convFocus {
		return
	}
	v.convFocus = true
	v.hideInline("")
	v.hideFiles()
	v.composer.Blur()
	if v.cursor < 0 || v.cursor >= len(v.anchors) {
		v.cursor = max(v.vp.YOffset()+v.vp.Height()-1, 0)
		v.cursor = min(v.cursor, max(len(v.anchors)-1, 0))
	}
	v.bodyDirty = true
}

// focusComposer hands the keyboard back to the composer.
func (v *chatView) focusComposer() {
	v.convFocus = false
	v.search.typing = false
	v.composer.Focus()
	v.bodyDirty = true
}

// openSearchLine opens the search line, from the composer or from the
// conversation, prefilled with the current query.
func (v *chatView) openSearchLine() {
	v.focusConversation()
	v.search.typing = true
	v.search.buf = v.search.query
}

// updateSearchLineKey is the search line's keyboard. It owns every key: a
// printable one types, enter searches, esc closes the line and leaves the
// conversation focused.
func (v *chatView) updateSearchLineKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.search.typing = false
		return nil
	case "enter":
		v.search.typing = false
		return v.runSearch(strings.TrimSpace(v.search.buf))
	case "backspace":
		if r := []rune(v.search.buf); len(r) > 0 {
			v.search.buf = string(r[:len(r)-1])
		}
		return nil
	case "ctrl+c":
		return nil
	}
	if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 &&
		len(v.search.buf)+len(msg.Text) <= chatConvSearchQuery {
		v.search.buf += msg.Text
	}
	return nil
}

// runSearch commits a query: the loaded turns are searched here at once, and
// the daemon is asked about every turn, loaded or not.
func (v *chatView) runSearch(q string) tea.Cmd {
	v.search.query, v.search.server, v.search.land, v.search.err = q, nil, nil, ""
	v.search.matches, v.search.current = nil, 0
	v.bodyDirty = true
	if q == "" {
		return nil
	}
	v.recomputeMatches()
	if len(v.search.matches) > 0 {
		v.search.current = v.firstMatchFromCursor()
		v.landOn(v.search.current)
	}
	client, id := v.client, v.chatID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.SearchChat(ctx, id, q)
		return chatSearchMsg{chatID: id, query: q, result: res, err: err}
	}
}

// applySearch folds the daemon's per-turn counts in.
func (v *chatView) applySearch(msg chatSearchMsg) tea.Cmd {
	if msg.chatID != v.chatID || msg.query != v.search.query {
		return nil
	}
	if msg.err != nil {
		v.search.err = errString(msg.err)
		return nil
	}
	v.search.server = map[int]int{}
	for _, t := range msg.result.Turns {
		v.search.server[t.Seq] = t.Matches
	}
	had := len(v.search.matches) > 0
	var at chatMatch
	if had {
		at = v.search.matches[v.search.current]
	}
	v.recomputeMatches()
	v.bodyDirty = true
	if len(v.search.matches) == 0 {
		return nil
	}
	if had {
		v.search.current = v.indexOfMatch(at)
		return nil
	}
	v.search.current = v.firstMatchFromCursor()
	return v.landOn(v.search.current)
}

// recomputeMatches lists every occurrence in turn order: a loaded turn's from
// its prompt and records, an unloaded one's from the daemon's count, or from
// its prompt and result_text before that count arrives.
func (v *chatView) recomputeMatches() {
	q := strings.ToLower(v.search.query)
	v.search.matches = v.search.matches[:0]
	if q == "" {
		return
	}
	add := func(turn, idx, n int, unloaded bool) {
		for k := range n {
			v.search.matches = append(v.search.matches, chatMatch{turn: turn, idx: idx, nth: k, unloaded: unloaded})
		}
	}
	for _, t := range v.turns {
		recs, loaded := v.turnRecords[t.Seq]
		if loaded && len(recs) > 0 {
			add(t.Seq, matchPrompt, countFold(t.Prompt, q), false)
			for i, rec := range recs {
				add(t.Seq, i, countFold(recordSearchText(rec), q), false)
			}
			continue
		}
		if n, ok := v.search.server[t.Seq]; ok && t.State != "running" && !v.fetchedEmpty(t.Seq) {
			add(t.Seq, matchPrompt, n, true)
			continue
		}
		add(t.Seq, matchPrompt, countFold(t.Prompt, q), false)
		add(t.Seq, matchResultText, countFold(t.ResultText, q), false)
	}
	if v.search.current >= len(v.search.matches) {
		v.search.current = 0
	}
}

// fetchedEmpty is a turn whose fetch came back with nothing — a transcript
// gone to retention — which only its prompt and result_text can match here.
func (v *chatView) fetchedEmpty(seq int) bool {
	_, has := v.turnRecords[seq]
	return v.fetched[seq] && !has
}

func (v *chatView) indexOfMatch(m chatMatch) int {
	for i, x := range v.search.matches {
		if x.turn == m.turn && x.idx == m.idx && x.nth == m.nth {
			return i
		}
	}
	for i, x := range v.search.matches {
		if x.turn >= m.turn {
			return i
		}
	}
	return 0
}

// firstMatchFromCursor is the first match in or after the cursor's turn,
// wrapping to the first.
func (v *chatView) firstMatchFromCursor() int {
	turn := anchorAt(v.anchors, v.cursor).turn
	for i, m := range v.search.matches {
		if m.turn >= turn {
			return i
		}
	}
	return 0
}

// stepMatch moves to the next (+1) or previous (-1) match, wrapping.
func (v *chatView) stepMatch(d int) tea.Cmd {
	n := len(v.search.matches)
	if n == 0 {
		return nil
	}
	v.search.current = ((v.search.current+d)%n + n) % n
	return v.landOn(v.search.current)
}

// landOn moves the cursor to match i, fetching its turn first when the match
// was only counted by the daemon, and expanding the record it is in when the
// level does not draw it (task 160 decision 3).
func (v *chatView) landOn(i int) tea.Cmd {
	if i < 0 || i >= len(v.search.matches) {
		return nil
	}
	m := v.search.matches[i]
	if m.unloaded {
		land := m
		v.search.land = &land
		var turn apiclient.ChatTurn
		for _, t := range v.turns {
			if t.Seq == m.turn {
				turn = t
			}
		}
		// Forced: a turn scrolled past earlier and then dropped by the cap
		// is asked for again.
		delete(v.fetched, m.turn)
		return v.fetchTurn(turn)
	}
	v.search.land = nil
	v.rebuildBody()
	line, found := v.matchLine(m)
	if !found && m.idx >= 0 {
		v.expandRecord(m.turn, m.idx)
		v.rebuildBody()
		line, _ = v.matchLine(m)
	}
	v.moveCursor(line)
	return nil
}

// landPending finishes a landing that waited for a turn's transcript.
func (v *chatView) landPending(seq int) {
	l := v.search.land
	if l == nil || l.turn != seq {
		return
	}
	v.search.land = nil
	v.recomputeMatches()
	for i, m := range v.search.matches {
		if m.turn != seq {
			continue
		}
		// The l.nth-th match of the turn, or its last when the turn counts
		// fewer here than the daemon did.
		j := i + l.nth
		for j >= len(v.search.matches) || v.search.matches[j].turn != seq {
			j--
		}
		v.search.current = j
		v.landOn(j)
		return
	}
}

// matchLine finds the body line a loaded match is drawn on.
func (v *chatView) matchLine(m chatMatch) (int, bool) {
	q := strings.ToLower(v.search.query)
	start, ok := v.turnAt[m.turn]
	if !ok {
		return 0, false
	}
	var want int64 = -1
	if m.idx >= 0 {
		if seqs := v.turnSeqs[m.turn]; m.idx < len(seqs) {
			want = seqs[m.idx]
		}
	}
	// The line of the record (or the nearest record before it: an assistant
	// document is anchored to its first record) that shows the query.
	best := -1
	for y := start; y < len(v.anchors) && v.anchors[y].turn == m.turn; y++ {
		a := v.anchors[y]
		switch m.idx {
		case matchPrompt:
			if a.you && lineHas(v.lines, y, q) {
				return y, true
			}
		case matchResultText:
			if !a.you && lineHas(v.lines, y, q) {
				return y, true
			}
		default:
			if a.rec != 0 && a.rec <= want && lineHas(v.lines, y, q) {
				best = y
				if a.rec == want {
					return y, true
				}
			}
		}
	}
	if best >= 0 {
		return best, true
	}
	return start, false
}

func lineHas(lines []string, y int, q string) bool {
	return y >= 0 && y < len(lines) && strings.Contains(strings.ToLower(ansi.Strip(lines[y])), q)
}

// expandRecord adds one record to the expanded set.
func (v *chatView) expandRecord(turn, idx int) {
	recs := v.turnRecords[turn]
	if idx < 0 || idx >= len(recs) {
		return
	}
	v.expandedSet()[recordKey(turn, idx, recs[idx])] = true
}

func recordKey(turn, idx int, rec apiclient.TranscriptRecord) chatCallKey {
	if call := recordCall(rec); call != "" {
		return chatCallKey{turn: turn, call: call}
	}
	return chatCallKey{turn: turn, idx: idx}
}

// expandedSet is this chat's expanded set, kept for the session in memory.
func (v *chatView) expandedSet() map[chatCallKey]bool {
	if v.expanded == nil {
		v.expanded = map[int64]map[chatCallKey]bool{}
	}
	set := v.expanded[v.chatID]
	if set == nil {
		set = map[chatCallKey]bool{}
		v.expanded[v.chatID] = set
	}
	return set
}

// expandedFor is the lineOpts predicate for one turn.
func (v *chatView) expandedFor(turn int) func(call string, rec int64) bool {
	set := v.expanded[v.chatID]
	if len(set) == 0 {
		return func(string, int64) bool { return false }
	}
	seqs := v.turnSeqs[turn]
	return func(call string, rec int64) bool {
		if call != "" {
			return set[chatCallKey{turn: turn, call: call}]
		}
		for i, s := range seqs {
			if s == rec {
				return set[chatCallKey{turn: turn, idx: i}]
			}
		}
		return false
	}
}

// toggleCallAt expands or folds the tool call under body line y; a line
// that belongs to no call is left alone.
func (v *chatView) toggleCallAt(y int) {
	a := anchorAt(v.anchors, y)
	if a.turn == 0 {
		return
	}
	var key chatCallKey
	switch {
	case a.call != "":
		key = chatCallKey{turn: a.turn, call: a.call}
	case a.rec != 0:
		seqs := v.turnSeqs[a.turn]
		idx := -1
		for i, s := range seqs {
			if s == a.rec {
				idx = i
			}
		}
		if idx < 0 || !v.expandedSet()[chatCallKey{turn: a.turn, idx: idx}] {
			return // only an expanded record folds by its position
		}
		key = chatCallKey{turn: a.turn, idx: idx}
	default:
		return
	}
	set := v.expandedSet()
	if set[key] {
		delete(set, key)
	} else {
		set[key] = true
	}
	v.rebuildBody()
}

// jumpPrompt moves the cursor to the previous (-1) or next (+1) `You` card.
func (v *chatView) jumpPrompt(d int) {
	cur := anchorAt(v.anchors, v.cursor)
	for y := v.cursor + d; y >= 0 && y < len(v.anchors); y += d {
		a := v.anchors[y]
		if !a.you || (a.turn == cur.turn && cur.you) {
			continue
		}
		// The card's first line.
		for y > 0 && v.anchors[y-1].you && v.anchors[y-1].turn == a.turn {
			y--
		}
		v.moveCursor(y)
		return
	}
}

// moveCursor puts the cursor on body line y and scrolls it into view.
func (v *chatView) moveCursor(y int) {
	y = max(min(y, len(v.anchors)-1), 0)
	v.cursor = y
	h := max(v.vp.Height(), 1)
	switch top := v.vp.YOffset(); {
	case y < top:
		v.vp.SetYOffset(y)
	case y >= top+h:
		v.vp.SetYOffset(y - h + 1)
	}
	v.syncFollowToViewport()
	v.bodyDirty = true
}

// rebuildBody builds the body now, so a cursor can be placed on its lines.
func (v *chatView) rebuildBody() {
	v.bodyDirty = true
	if v.builtWidth > 0 {
		v.bodyView(v.builtWidth, max(v.vp.Height(), 1))
	}
}

// updateConversationKey is the conversation focus's keyboard. It answers
// every key: one it has no meaning for is spent here rather than typed into
// a draft the reader cannot see being typed into.
func (v *chatView) updateConversationKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "i":
		v.focusComposer()
	case "j", "down":
		v.moveCursor(v.cursor + 1)
	case "k", "up":
		v.moveCursor(v.cursor - 1)
	case "g":
		v.vp.GotoTop()
		v.moveCursor(0)
	case "G":
		v.moveCursor(len(v.anchors) - 1)
		v.follow()
	case "pgup":
		v.vp.PageUp()
		v.syncFollowToViewport()
		v.cursor = v.vp.YOffset()
	case "pgdown":
		v.vp.PageDown()
		v.syncFollowToViewport()
		v.cursor = min(v.vp.YOffset()+v.vp.Height()-1, max(len(v.anchors)-1, 0))
	case opKey(keymap.Filter):
		v.openSearchLine()
	case "n":
		return v.stepMatch(1)
	case "N":
		return v.stepMatch(-1)
	case "{":
		v.jumpPrompt(-1)
	case "}":
		v.jumpPrompt(1)
	case "enter":
		v.toggleCallAt(v.cursor)
	case "alt+G": // the composer's follow key, since task 157 decision 1
		v.moveCursor(len(v.anchors) - 1)
		v.follow()
	case "alt+up":
		v.vp.ScrollUp(1)
		v.syncFollowToViewport()
	case "alt+down":
		v.vp.ScrollDown(1)
		v.syncFollowToViewport()
	case "alt+g":
		v.vp.GotoTop()
		v.syncFollowToViewport()
	case chatExpandKey:
		v.level.cycle()
	default:
		return nil
	}
	v.bodyDirty = true
	return v.fetchVisibleTranscripts()
}

// follow re-arms follow and clears the `↓ N new` marker (decision 5).
func (v *chatView) follow() {
	v.setFollowing(true)
	v.newLines = 0
	v.bodyDirty = true
}

// edgeLabel is what the conversation pane's bottom edge carries: the search
// count and the `↓ N new` marker (task 160 decisions 3 and 5).
func (v *chatView) edgeLabel() string {
	var parts []string
	if v.search.query != "" {
		switch {
		case len(v.search.matches) == 0 && v.search.err != "":
			parts = append(parts, styleBad.Render("search failed"))
		case len(v.search.matches) == 0:
			parts = append(parts, styleDim.Render("0/0"))
		default:
			parts = append(parts, styleKey.Render(fmt.Sprintf("%d/%d", v.search.current+1, len(v.search.matches))))
		}
	}
	if v.newLines > 0 && !v.following {
		parts = append(parts, chatNewMarkStyle.Render(fmt.Sprintf("↓ %d new", v.newLines)))
	}
	return strings.Join(parts, "  ")
}

// searchLine is the open search line, drawn above the composer.
func (v *chatView) searchLine(width int) string {
	return ansi.Truncate(styleKey.Render("/")+v.search.buf+"█"+
		styleDim.Render("  enter search · esc close"), width, "…")
}

// decorate highlights the query on every line it shows on, the current
// match's line in its own style, and the cursor while the conversation has
// the keyboard. It runs over the built lines, so wrapping is already done;
// the line's own escapes, hyperlinks among them, survive outside a match.
func (v *chatView) decorate(lines []string) []string {
	q := strings.ToLower(v.search.query)
	cur := -1
	if q != "" && len(v.search.matches) > 0 && v.search.land == nil {
		cur = v.cursor
	}
	if q == "" && !v.convFocus {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	for y, l := range out {
		if q != "" {
			style := chatMatchStyle
			if y == cur {
				style = chatMatchCurrentStyle
			}
			out[y] = highlightFold(l, q, style)
		}
		if v.convFocus && y == v.cursor {
			out[y] = chatCursorMark + ansi.TruncateLeft(out[y], 1, "")
		}
	}
	return out
}

// highlightFold styles every case-insensitive occurrence of q (lower-cased)
// in a rendered line, measured in cells so the ANSI around it is kept.
func highlightFold(line, q string, style lipgloss.Style) string {
	plain := []rune(ansi.Strip(line))
	needle := []rune(q)
	if len(needle) == 0 || len(plain) < len(needle) {
		return line
	}
	var ranges []lipgloss.Range
	cell := 0
	cells := make([]int, len(plain)+1)
	for i, r := range plain {
		cells[i] = cell
		cell += ansi.StringWidth(string(r))
	}
	cells[len(plain)] = cell
	for i := 0; i+len(needle) <= len(plain); {
		match := true
		for j, r := range needle {
			if unicode.ToLower(plain[i+j]) != r {
				match = false
				break
			}
		}
		if !match {
			i++
			continue
		}
		ranges = append(ranges, lipgloss.NewRange(cells[i], cells[i+len(needle)], style))
		i += len(needle)
	}
	if len(ranges) == 0 {
		return line
	}
	return lipgloss.StyleRanges(line, ranges...)
}

// countFold counts non-overlapping case-insensitive occurrences of q
// (lower-cased) in s — the daemon's countFold, so a loaded turn counts what
// the daemon counted for it.
func countFold(s, q string) int {
	if q == "" || s == "" {
		return 0
	}
	return strings.Count(strings.ToLower(s), q)
}

// recordSearchText is every string a record says, drawn at the current
// level or not (task 160 decision 3).
func recordSearchText(rec apiclient.TranscriptRecord) string {
	parts := []string{
		rec.Text, rec.Message, rec.Summary, rec.Line, rec.ResultText, rec.Output, rec.Patch,
		rec.Description, rec.Error, rec.Args, rec.Name, rec.LastTool, rec.StopReason,
	}
	for _, t := range rec.Tools {
		parts = append(parts, t.Name, t.Summary)
	}
	for _, r := range rec.Results {
		parts = append(parts, r.Name, r.Summary, r.Verb)
	}
	for _, it := range rec.Items {
		parts = append(parts, it.Text)
	}
	return strings.Join(parts, "\x00")
}

// updateConvClick is a click in the conversation pane: on a tool call's line
// it focuses the conversation and toggles that call, and on the bottom edge
// while `↓ N new` shows it follows again.
func (v *chatView) updateConvClick(msg tea.MouseClickMsg) bool {
	h := v.convHit
	if msg.Button != tea.MouseLeft {
		return false
	}
	if msg.Y == h.edgeY && msg.X >= h.edgeX0 && msg.X < h.edgeX1 && v.newLines > 0 && !v.following {
		v.follow()
		return true
	}
	if msg.X < h.x0 || msg.X >= h.x1 {
		return false
	}
	if msg.Y < h.y0 || msg.Y >= h.y0+h.rows {
		return false
	}
	y := v.vp.YOffset() + msg.Y - h.y0
	if anchorAt(v.anchors, y).call == "" {
		return false
	}
	v.focusConversation()
	v.cursor = y
	v.toggleCallAt(y)
	return true
}

// chatConvHit is where the conversation is on screen, recorded at render:
// the body's columns [x0, x1) and its rows from y0, and the row and columns
// [edgeX0, edgeX1) that `↓ N new` is drawn on, edgeY -1 when it is not drawn.
type chatConvHit struct {
	x0, x1, y0, rows, edgeY, edgeX0, edgeX1 int
}

// shift moves a hit recorded against its content's origin to (dx, dy).
func (h chatConvHit) shift(dx, dy int) chatConvHit {
	h.x0 += dx
	h.x1 += dx
	h.y0 += dy
	h.edgeX0 += dx
	h.edgeX1 += dx
	if h.edgeY >= 0 {
		h.edgeY += dy
	}
	return h
}
