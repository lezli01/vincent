package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat composer's `@` file picker (§5.5, §15 view 9, task 126.11, issue
// #555): the files of the directory the chat's next turn would start in,
// drawn between the note line and the composer, opened by a typed `@` and a
// character after it, and accepted by replacing that token with the row's
// server-supplied mention text.
//
// It is the second list the chatInlineList core draws (chatinlinelist.go,
// issue #554) — the rows, the filter buffer, the window arithmetic and the
// renderer live there, and everything that makes this list a *files* list
// lives here — and it is what that core was extracted for.
//
// **Nothing here builds the inserted text.** `ChatFile.Mention` is the
// daemon's adapter's own bytes (task 126 decision 1), so claude's
// `@"path with spaces"` quoting rule has one definition and it is not this
// one. The picker is a typing aid: the message still goes to the CLI
// verbatim (§5.5, task 124 decision 9, after task 025 decision 5).
//
// The picker and the skills list can never both be drawn. Both openers read
// the *single* whitespace-delimited token under the cursor and both require
// it to **begin** with their sigil, and a token has one first rune; the one
// case where the two sigils could coincide — an adapter reporting
// `invoke_sigil: "@"` — stands this picker down for that chat, because §5.5
// makes the adapter's word authoritative and a skill invocation changes what
// the CLI *does* while a mention is an aid to typing. chatView.syncInlineFiles
// carries both halves.

// chatFileMentionMax is the most rows a build keeps, out of however many
// matched (task 126 decision 45). It is applied *after* the ranking, so what
// survives is the best matches rather than a prefix of the listing, and
// capNote() then draws `50 of 1,615` on the title line.
//
// The number is the one task 124 decision 101 predicted this list would set:
// a skills catalog is dozens of rows and a cap there would be theatre
// (issue #553 decision 1), while a repository is thousands and an uncapped
// build over a 50,000-row listing is the renderer hazard issue #553 was
// opened for.
const chatFileMentionMax = 50

// chatFileList is the file picker's whole state: the inline picker core plus
// what only a file list has. The zero value is a closed list that has asked
// for nothing, which is what chatView.open assigns.
//
// It shadows the core's render rather than being dispatched into from it
// (issue #554 decision 6): every entry point fills the fields the core reads
// before handing over. height and window are deliberately *not* shadowed —
// nothing outside render needs them, and titledCore() is the way to reach
// them with the fields filled.
type chatFileList struct {
	chatInlineList

	// data is the last answer for chatID; nil until one arrives. It is kept
	// across opens and dropped on a `chat.*` event, which is the daemon's
	// own signal that a turn may have changed the workspace — and the only
	// signal available, since a linked chat's event stream carries nothing
	// about the task steps writing into the same worktree
	// (internal/api/chatstream.go). There is no server cache and so no
	// `?refresh=`: this is the only cache (task 126 decision 5).
	data *apiclient.ChatFiles
	// failedFor is the draft token a failed fetch was fired for, and the
	// whole of task 126 decision 46's second half: syncInlineFiles runs on
	// *every* composer update, so without it typing `@src/m` would fire one
	// request per keystroke. A token that still begins with it is the same
	// attempt continuing; any other token, and forget(), clear it.
	//
	// It is deliberately not chatSkillList.probeFailed's per-chat latch:
	// that latch exists because a skills probe spawns an agent CLI, where
	// this is a 10–30 ms `git ls-files`, and one 409 must not disable the
	// picker for the session.
	failedFor string
	// accepted is every mention this list has written into the draft, the
	// way chatSkillList.noteFor records an invocation (task 126 decision
	// 42). While the token under the cursor is a whitespace-delimited piece
	// of one of these that the draft still holds, nothing opens.
	//
	// It exists for the mention that contains whitespace. chatDraftTokenAt
	// is whitespace-delimited and textarea.Word() returns `"file.md"` for a
	// cursor inside `@docs/my file.md`, so walking back into it and
	// accepting would yield `@docs/my file.md file.md` — a mention that
	// will not resolve. Silence is the answer rather than a note, because
	// the note line is the mention verdict's and a second sentence
	// competing for one line costs more than it says.
	accepted []string
}

// forget drops the cached answer, and the failed fetch with it: the answer
// this chat was told not to ask for again is the one that has just been
// thrown away. The next open asks again — which is the second half of task
// 126's invalidation rule, and the only one a linked chat has.
//
// The accepted mentions survive: a `chat.*` event says nothing about the
// draft, which is exactly what they are about.
func (l *chatFileList) forget() {
	l.data = nil
	l.err = ""
	l.loading = false
	l.failedFor = ""
}

// canMention reports an answer that can be picked from. An empty
// MentionSigil is the route's own spelling for "this adapter cannot mention
// files" (task 126 decision 38), and it is the signal not to offer a picker
// at all — the paths are still served, because they are true regardless of
// who reads them.
func (l *chatFileList) canMention() bool {
	return l.data != nil && l.data.MentionSigil != ""
}

// agentName is whose listing this is, "the agent" before an answer arrived.
func (l *chatFileList) agentName() string {
	if l.data != nil && l.data.Agent != "" {
		return l.data.Agent
	}
	return "the agent"
}

// mentionNote is the dim line under the composer while the picker is up, and
// it carries one fact in one direction (task 126 decision 43): an adapter
// whose CLI does **not** expand an `@` mention says so, and one that does
// says nothing. That is task 124 decision 19's bad-news-only rule, and it is
// what `vincent agents` already prints as `no @ file expansion` (§9.1).
//
// There is no "paths are relative to …" orientation line beside it: every row
// already draws a workspace-relative path, and one note line cannot carry two
// things.
func (l *chatFileList) mentionNote() string {
	if !l.open || l.data == nil || l.data.MentionExpands {
		return ""
	}
	return l.agentName() + " does not expand an @ mention — it sees the path and may read the file itself"
}

// remember records an accepted mention and drops the ones the draft no longer
// holds, so the suppression set stays the set of mentions actually in front of
// the human (task 126 decision 42).
func (l *chatFileList) remember(mention, draft string) {
	kept := make([]string, 0, len(l.accepted)+1)
	for _, m := range l.accepted {
		if m != mention && strings.Contains(draft, m) {
			kept = append(kept, m)
		}
	}
	kept = append(kept, mention)
	l.accepted = kept
}

// insideAccepted reports the cursor sitting on a whitespace-delimited piece of
// a mention this list already wrote and the draft still holds — the state
// task 126 decision 42 keeps the list shut in.
func (l *chatFileList) insideAccepted(tok, draft string) bool {
	for _, m := range l.accepted {
		if !strings.Contains(draft, m) {
			continue // edited away; it suppresses nothing
		}
		for _, piece := range strings.Fields(m) {
			if piece == tok {
				return true
			}
		}
	}
	return false
}

// mentionFilter reports the query an open list would rank against for tok,
// and whether the draft asks for one at all.
//
// The trigger rune is chatFileMentionSigil and not the wire's
// `mention_sigil`: `@` is what a human types to mean a file, every shipped
// adapter reports it (§9.1), and the wire's field is consulted as the
// capability flag through canMention. What the daemon's bytes decide is the
// text that goes *in*, which is the half a client must never rebuild.
//
// **A bare `@` opens nothing** (task 126 decision 41). `@` is positionally
// `anywhere`, so task 124 decision 93's `anywhere` branch applies unchanged:
// one character after the sigil is required. That is what keeps `@lezli01`
// and `cc @someone` — the false positive that matters in a chat about a pull
// request — silent, and what stops a repository-sized list appearing on one
// keystroke. `user@host` never reaches here: the token does not begin with
// the sigil.
func (l *chatFileList) mentionFilter(tok chatDraftToken) (string, bool) {
	rest, ok := strings.CutPrefix(tok.text, string(chatFileMentionSigil))
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// build re-ranks the rows against the filter and keeps the highlight inside
// them. It is the files half of what the core's typeText and backspace ask
// for: they move the filter and the owner re-ranks (issue #554 decision 1).
func (l *chatFileList) build() {
	// The cap is a parameter of the build rather than of the draw, so it is
	// set here and not in core(): syncInlineFiles builds on every keystroke
	// and renders only afterwards.
	l.cap = chatFileMentionMax
	l.rows = l.rows[:0]
	l.matched = 0
	if !l.canMention() {
		l.cursor = -1
		return
	}
	ranked := rankChatFiles(l.data.Files, l.filter)
	l.matched = len(ranked)
	// After the ranking, so what survives a cap is the best matches
	// (task 126 decision 45).
	if l.cap > 0 && len(ranked) > l.cap {
		ranked = ranked[:l.cap]
	}
	for _, i := range ranked {
		f := l.data.Files[i]
		row := chatInlineRow{
			insert:  f.Mention,
			display: chatSkillFlatten(f.Path),
			// The reserve draws the description, so the one reserved line
			// carries the path in full where the row line had to truncate it.
			description: chatSkillFlatten(f.Path),
		}
		switch {
		case f.Mention == "":
			row.disabled, row.reason = true, "the "+l.agentName()+" adapter cannot mention a file"
		case chatSkillHostile(f.Mention):
			// Task 124 decision 71's guard, reused verbatim: a control or an
			// ANSI escape is refused, whitespace is not — a path with a space
			// in it is the ordinary case on macOS, and its mention is quoted
			// by the adapter.
			row.disabled, row.reason = true,
				"this path carries a control character — vincent will not type it for you"
		}
		l.rows = append(l.rows, row)
	}
	if l.cursor >= len(l.rows) {
		l.cursor = len(l.rows) - 1
	}
}

// rankChatFiles orders the indices of the files that match q:
//
//  0. the **basename** begins with q;
//  1. any **path segment** begins with q, or the whole path does;
//  2. the whole path **contains** q.
//
// Case-insensitively, keeping the server's order — git's own — within a tier.
// An empty q keeps every row in it.
//
// Tier 0 is skipped once q carries a separator, and matching is against the
// whole path, so `@internal/tui/chat` behaves: with a separator typed, the
// human is spelling a path and a basename hit would push an unrelated file
// above it.
//
// Not fuzzy or subsequence matching. Nothing in the repository does it, it is
// a real algorithm to get right and to test, and it belongs behind this same
// one-function seam — chatFileTier — so it can replace tier 2 later.
func rankChatFiles(files []apiclient.ChatFile, q string) []int {
	if len(files) == 0 {
		return nil
	}
	query, sep := chatFileQuery(q)
	if query == "" {
		out := make([]int, len(files))
		for i := range files {
			out[i] = i
		}
		return out
	}
	tiers := make([][]int, 3)
	for i, f := range files {
		switch tier := chatFileTier(f.Path, query, sep); tier {
		case 0, 1, 2:
			tiers[tier] = append(tiers[tier], i)
		}
	}
	out := make([]int, 0, len(files))
	for _, tier := range tiers {
		out = append(out, tier...)
	}
	return out
}

// chatFileQuery normalizes what the human typed, and reports whether it holds
// a separator.
//
// **Both `/` and `\` are separators, on every platform**, which is task 124
// decision 92's rule and never `filepath.Separator`: a draft is prose a human
// may write about any machine, and a rule that differed per host would make
// the same message say two things — and a test of it pass on one CI leg and
// fail on another. A typed `\` can only have been meant as a separator, so it
// is folded to `/` here; a `\` in a *path* is left alone, because git's own
// bytes separate with `/` on every platform (task 126 decision 21), which
// makes a backslash there a character in a file's name.
func chatFileQuery(q string) (query string, sep bool) {
	query = strings.ToLower(strings.TrimSpace(q))
	query = strings.ReplaceAll(query, `\`, "/")
	return query, strings.Contains(query, "/")
}

// chatFileTier is one path's match tier for a normalized q, or -1 for no
// match. It is chatSkillTier's seam one type over.
func chatFileTier(path, q string, sep bool) int {
	p := strings.ToLower(path)
	// With a separator typed the query is a path, so the basename is not
	// what it is about.
	if !sep && strings.HasPrefix(p[strings.LastIndex(p, "/")+1:], q) {
		return 0
	}
	if strings.HasPrefix(p, q) {
		return 1
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, q) {
			return 1
		}
	}
	if strings.Contains(p, q) {
		return 2
	}
	return -1
}

// ---- what a file list tells the core before it draws (issue #554) ----

// core fills the data-neutral fields and hands back the list to delegate to.
//
// It runs on every delegation rather than once at construction because
// nothing constructs a chatFileList: chatView.open assigns a bare literal and
// hideFiles re-zeroes the filter, so a field wired by a constructor would be
// silently dropped at run time with nothing failing at compile time
// (issue #554 decisions 1 and 5).
func (l *chatFileList) core() *chatInlineList {
	// One line, not the skills list's three, which is what the core's own
	// reserve comment anticipated: a path is one line, and two more reserved
	// lines would come out of a body a #299 budget is already spending
	// (task 126 decision 45).
	l.reserve = 1
	// Only when nothing has been put there: the seam is how issue #553's
	// tests count the rows a frame styles, and overwriting it on every draw
	// would take that away.
	if l.rowLine == nil {
		l.rowLine = chatFilePathRowLine
	}
	l.loadingText = "listing this chat's files…"
	l.emptyText = l.agentName() + " reported no files for this chat"
	// Never drawn: the list hides rather than saying nothing matches
	// (task 124 decision 92, which bites hardest on `@lezli01`). It is
	// filled because the core reads it, and a blank line would be the one
	// thing worse than the sentence.
	l.noMatchText = "nothing matches"
	return &l.chatInlineList
}

// titledCore is core plus the title line's words. Unlike the skills list's it
// needs no clock: a listing carries no probe age, because `git ls-files` is
// not a probe.
func (l *chatFileList) titledCore() *chatInlineList {
	c := l.core()
	c.title = "files"
	tail := make([]string, 0, 3)
	// The truncation cell first: "your file is not here" and "your file does
	// not match" must not look the same, and only the daemon can say the
	// listing itself was cut.
	if l.data != nil && l.data.Truncated {
		tail = append(tail, "showing the first "+strconv.Itoa(len(l.data.Files)))
	}
	if note := c.capNote(); note != "" {
		tail = append(tail, note)
	}
	if len(l.rows) > 0 {
		tail = append(tail, "tab complete · ↑/↓ pick · esc back to the draft")
	}
	c.tail = tail
	return c
}

// render draws the list: a title line, the windowed rows, and the highlighted
// row's whole path wrapped into the one reserved line. One slice element per
// rendered line (#299).
func (l *chatFileList) render(width, paneHeight int) []string {
	return l.titledCore().render(width, paneHeight)
}

// chatFilePathRowLine draws one file row: the marker and the path, and
// nothing else.
//
// It is not chatSkillRowLine, which appends the description after the name —
// here the two are the same path, and a row reading `internal/tui/chat.go ·
// internal/tui/chat.go` says nothing twice. The full path still has somewhere
// to go: the reserved line under the list.
func chatFilePathRowLine(r chatInlineRow, selected bool, width int) string {
	inner := max(width-3, 8)
	mark, style := "  ", styleDim
	if selected {
		mark, style = styleFocus.Render("› "), styleTitle
	}
	if r.disabled {
		style = styleBad
	}
	return " " + mark + style.Render(ansi.Truncate(r.display, inner, "…"))
}
