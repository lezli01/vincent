package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's skill list (§15 view 9, task 124.13, issue #509): the
// skills the chat's agent CLI would load in the chat's directory, drawn
// between the note line and the composer, opened with `tab` (or `f2`) and
// closed with `esc`.
//
// It is one of the lists the chatInlineList core draws (chatinlinelist.go,
// issue #554): the rows, the filter buffer, the window arithmetic and the
// renderer live there, and everything that makes this list a *skills* list
// lives here.
//
// The whole adapter syntax comes off the wire (task 124 decision 9): the
// sigil, where it goes and each row's exact invocation are the daemon's
// adapter's words, and nothing here builds one. `/` is never a case label —
// it is the `filter` operation's default key (internal/keymap/keymap.go).
//
// The list owns its own filter buffer and never touches the draft until a
// row is accepted (task 124 decision 70). The alternative — opening by
// inserting the sigil into the message — has a hole with
// `invoke_position: leading`: on a draft of `fix the bug` it makes the first
// token `/fix`, so browsing is filtered rather than whole, accepting eats
// the word, and an `enter` with no highlight sends a message beginning with
// a `/` nobody typed.

// chatSkillMode is which of the list's two openers put it on screen
// (task 124 decision 94). The rows, the ranking, the hostile-row guard, the
// window arithmetic and the renderer are shared; what differs is who owns
// the keyboard and where the filter comes from.
type chatSkillMode uint8

const (
	// skillModeBrowse is `tab`'s list: it owns the keyboard and types into
	// its own filter buffer (decision 70).
	skillModeBrowse chatSkillMode = iota
	// skillModeInline is the draft's list: the composer keeps the keyboard
	// and the filter is the draft's own sigil token, minus the sigil.
	skillModeInline
)

// chatSkillList is the skills list's whole state: the inline picker core plus
// what only a skills list has. The zero value is a closed list that has asked
// for nothing.
type chatSkillList struct {
	chatInlineList

	// mode is which opener it is on screen for.
	mode chatSkillMode
	// data is the last answer for chatID; nil until one arrives. It is kept
	// across opens and dropped on a `chat.*` event, which is the daemon's
	// own signal that a turn boundary may have changed the listing
	// (decision 73: there is no refresh key here, and ctrl+r keeps its one
	// meaning).
	data *apiclient.ChatSkills
	// probeFailed latches the inline opener off for this chat (decision 91):
	// the human never asked for that probe, so a fetch that failed — or one
	// that answered a `list_verdict` other than `supported` — writes nothing
	// and is not re-fired on the next keystroke. forget() clears it when a
	// `chat.*` event drops the cached answer, and `tab` clears it because
	// then the human *did* ask.
	probeFailed bool
	// inlineNote is the inline opener's dim line under the composer: the
	// unmatched-name hint (decision 92) or what an accepted skill takes
	// (issue #510 item 3). Never a refusal — vincent sends the message as
	// typed (task 025 decision 5).
	inlineNote string
	// noteFor is the invocation inlineNote is about when it is an
	// after-accept note, "" when it is the transient hint. The note lives
	// until that invocation leaves the draft.
	noteFor string
}

// forget drops the cached answer. The next open asks again, and the inline
// opener's latch goes with it (decision 91): the answer this chat was told
// not to ask about again is the one that has just been thrown away.
func (l *chatSkillList) forget() {
	l.data = nil
	l.err = ""
	l.loading = false
	l.probeFailed = false
}

// canList reports an answer that has rows to draw — "supported" is the only
// verdict that makes an empty list mean "none" (task 124.9).
func (l *chatSkillList) canList() bool {
	return l.data != nil && l.data.ListVerdict == "supported"
}

// canInvoke reports that a picked row may be written into the draft.
// `unknown` behaves as `unsupported` here (decision 72): decision 58 gives an
// unregistered adapter `unknown` on both verdicts, and a client must not
// insert an invocation nobody said would work.
func (l *chatSkillList) canInvoke() bool {
	return l.data != nil && l.data.InvokeVerdict == "supported"
}

// build re-ranks the rows against the filter and keeps the highlight inside
// them. It is the skills half of what the core's typeText and backspace ask
// for: they move the filter and the owner re-ranks (issue #554 decision 1).
func (l *chatSkillList) build() {
	l.rows = l.rows[:0]
	l.matched = 0
	if !l.canList() {
		l.cursor = -1
		return
	}
	reason := ""
	if !l.canInvoke() {
		reason = chatSkillsNoInvokeReason(l.data)
	}
	ranked := rankChatSkills(l.data.Skills, l.filter)
	l.matched = len(ranked)
	// The cap is applied *after* the ranking, so what survives is the best
	// matches rather than an arbitrary prefix of the catalog (issue #553).
	if l.cap > 0 && len(ranked) > l.cap {
		ranked = ranked[:l.cap]
	}
	for _, i := range ranked {
		s := l.data.Skills[i]
		row := chatInlineRow{
			insert:      s.Invocation,
			display:     chatSkillFlatten(s.Invocation),
			hint:        chatSkillFlatten(s.ArgumentHint),
			description: chatSkillFlatten(s.Description),
			note:        chatSkillNote(s),
			disabled:    reason != "",
			reason:      reason,
		}
		switch {
		case s.Invocation == "":
			row.disabled, row.reason = true, "this skill has no invocation"
		case chatSkillHostile(s.Invocation):
			row.disabled, row.reason = true,
				"this skill's invocation carries a control character — vincent will not type it for you"
		}
		if row.display == "" {
			row.display = chatSkillFlatten(s.Name)
		}
		l.rows = append(l.rows, row)
	}
	if l.cursor >= len(l.rows) {
		l.cursor = len(l.rows) - 1
	}
}

// chatSkillNote is the dim tail: the scope and the plugin, each only where
// the CLI said one. A blank cell, never a dash (task 124 decisions 8 and 60).
func chatSkillNote(s apiclient.ChatSkill) string {
	parts := make([]string, 0, 2)
	if s.Scope != "" {
		parts = append(parts, chatSkillFlatten(s.Scope))
	}
	if s.Plugin != "" {
		parts = append(parts, chatSkillFlatten(s.Plugin))
	}
	return strings.Join(parts, " · ")
}

// chatSkillsNoInvokeReason says why a listed skill cannot be picked into the
// draft.
func chatSkillsNoInvokeReason(d *apiclient.ChatSkills) string {
	if d.UnavailableReason != "" {
		return d.UnavailableReason
	}
	return d.Agent + " does not say how a message invokes a skill — type it yourself"
}

// rankChatSkills orders the indices of skills that match q, Claude Code's
// own order (issue #509's prior art): a prefix of the name first, then a
// prefix of the part after a plugin namespace's `:` or of an alias, then a
// substring of the description. Within a tier the CLI's order is kept, which
// is what task 124 decision 17 requires of a list whose names may repeat.
//
// An empty q keeps every row in the CLI's order.
func rankChatSkills(skills []apiclient.ChatSkill, q string) []int {
	if len(skills) == 0 {
		return nil
	}
	if q = strings.ToLower(strings.TrimSpace(q)); q == "" {
		out := make([]int, len(skills))
		for i := range skills {
			out[i] = i
		}
		return out
	}
	tiers := make([][]int, 3)
	for i, s := range skills {
		switch tier := chatSkillTier(s, q); tier {
		case 0, 1, 2:
			tiers[tier] = append(tiers[tier], i)
		}
	}
	out := make([]int, 0, len(skills))
	for _, tier := range tiers {
		out = append(out, tier...)
	}
	return out
}

// chatSkillTier is one skill's match tier for q, or -1 for no match.
func chatSkillTier(s apiclient.ChatSkill, q string) int {
	name := strings.ToLower(s.Name)
	if strings.HasPrefix(name, q) {
		return 0
	}
	// A plugin skill matches on its bare name too, so `deploy` finds
	// `myplugin:deploy-app` — and so does any alias the CLI reported.
	if _, bare, ok := strings.Cut(name, ":"); ok && strings.HasPrefix(bare, q) {
		return 1
	}
	for _, a := range s.Aliases {
		if strings.HasPrefix(strings.ToLower(a), q) {
			return 1
		}
	}
	if strings.Contains(strings.ToLower(s.Description), q) {
		return 2
	}
	return -1
}

// ---- what a skills list tells the core before it draws (issue #554) ----

// core fills the data-neutral fields that do not depend on the clock and
// hands back the list to delegate to.
//
// It runs on every delegation rather than once at construction because
// nothing constructs a chatSkillList: chatview.go assigns a bare literal and
// hideSkills re-zeroes the filter, so a field wired by a constructor would be
// silently dropped at run time with nothing failing at compile time
// (issue #554 decisions 1 and 5).
func (l *chatSkillList) core() *chatInlineList {
	// Three wrapped lines of description, which is what §15 view 9 has
	// drawn since task 124.13; a list of paths would want one.
	l.reserve = 3
	l.loadingText = fmt.Sprintf("asking %s which skills it has…", l.agentName())
	l.emptyText = fmt.Sprintf("%s reported no skills for this chat", l.agentName())
	l.noMatchText = "nothing matches — backspace clears the filter, esc closes the list"
	return &l.chatInlineList
}

// titledCore is core plus the title line's words, which need the clock: the
// core renders the line's shape and this fills it (issue #554 decision 2).
func (l *chatSkillList) titledCore(now time.Time) *chatInlineList {
	c := l.core()
	c.title = "skills"
	tail := make([]string, 0, 4)
	// Only browse draws its filter: the inline list's filter is the token
	// the human can see in the draft, and printing it twice would read as
	// two buffers rather than one.
	if l.mode == skillModeBrowse && l.filter != "" {
		tail = append(tail, l.filter)
	}
	// The cap counter sits between the filter and the probe's age, where it
	// has been drawn since issue #553; appending it in the core would
	// reorder the line.
	if note := c.capNote(); note != "" {
		tail = append(tail, note)
	}
	if l.data != nil && l.data.ProbedAt != nil {
		tail = append(tail, "probed "+formatElapsed(max(now.Sub(*l.data.ProbedAt), 0).Truncate(time.Second))+" ago")
	}
	if len(l.rows) > 0 {
		if l.mode == skillModeInline {
			tail = append(tail, "tab complete · ↑/↓ pick · esc back to the draft")
		} else {
			tail = append(tail, "tab insert · ↑/↓ pick · esc close")
		}
	}
	c.tail = tail
	return c
}

// height is how many lines the list occupies, which render must spend out of
// the body's budget before it draws anything (#299).
//
// It shadows the promoted method rather than being dispatched into from one:
// an embedded struct's method never reaches its embedder's override
// (issue #554 decision 6), so every entry point sets the fields the core
// reads before handing over.
//
// A drawing chat never calls it: footerLines spends the lines by appending
// what render returned, one slice element each. What is left is the pair of
// #299 assertions — "height(paneHeight) is what render spends" — and they
// share one pane height, which is what unparam is seeing.
//
//nolint:unparam // the remaining callers are the #299 tests; see above
func (l *chatSkillList) height(paneHeight int) int { return l.core().height(paneHeight) }

// window is how many row lines are drawn. Test-facing for the same reason
// height is: the core's own window is what render consults.
//
//nolint:unparam // the remaining callers are the #299 tests; see height
func (l *chatSkillList) window(paneHeight int) int { return l.core().window(paneHeight) }

// titleLine names the list, the filter being typed and how old the answer is.
func (l *chatSkillList) titleLine(now time.Time) string { return l.titledCore(now).titleLine() }

// render draws the list: a title line carrying the filter and the probe's
// age, the windowed rows, and the highlighted row's description wrapped into
// the fixed reserve. One slice element per rendered line (#299).
func (l *chatSkillList) render(width, paneHeight int, now time.Time) []string {
	return l.titledCore(now).render(width, paneHeight)
}

// agentName is whose list this is, "the agent" before an answer arrived.
func (l *chatSkillList) agentName() string {
	if l.data != nil && l.data.Agent != "" {
		return l.data.Agent
	}
	return "the agent"
}

// refreshNote expires the after-accept note. It lives until the invocation
// it describes leaves the draft, or until the inline list opens again on a
// token of its own — either way what it is about is gone. The transient
// hint carries no noteFor and is recomputed from scratch on every sync.
func (l *chatSkillList) refreshNote(draft string, opened bool) {
	if l.noteFor == "" {
		return
	}
	if opened || !strings.Contains(draft, l.noteFor) {
		l.inlineNote, l.noteFor = "", ""
	}
}

// inlineFilter reports the filter an inline list would rank against for tok,
// and whether the draft asks for one at all. The sigil and its position are
// the wire's; nothing here knows which adapter it is talking about.
func (l *chatSkillList) inlineFilter(tok chatDraftToken) (string, bool) {
	// InvokeSigil is "" whenever the invoke verdict is not `supported`
	// (internal/api/chatskills.go), so this gates inline off for an adapter
	// that cannot invoke — tested for itself rather than left to that
	// coupling.
	if l.data == nil || !l.canList() || l.data.InvokeSigil == "" {
		return "", false
	}
	rest, ok := strings.CutPrefix(tok.text, l.data.InvokeSigil)
	if !ok {
		return "", false
	}
	switch l.data.InvokePosition {
	case "leading":
		// claude expands `/name` only at the very start of the message, and
		// the composer trims leading whitespace on send, so the position is
		// "the first token on row 0" rather than "column 0". A bare sigil
		// opens every row here, which is Claude Code's own behaviour and
		// almost always what it means (decision 93).
		if tok.line != 0 || !tok.first {
			return "", false
		}
	case "anywhere":
		// A bare `$` mid-prose is a shell variable far more often than the
		// start of an invocation, and an empty filter matches every row, so
		// the hide-on-no-match rule could not quiet it (decision 93). One
		// character after the sigil opens the list.
		if rest == "" {
			return "", false
		}
	default:
		return "", false
	}
	return rest, true
}

// chatFileMentionSigil is the rune a human writes a file with, not a skill.
// It is the one sigil chatSkillSigilShaped refuses; see there for why.
const chatFileMentionSigil = '@'

// chatSkillSigilShaped reports a token that could be an invocation under
// *some* adapter's sigil: one non-alphanumeric rune that is not
// chatFileMentionSigil, and then a name.
//
// It is the only thing the *first* inline fetch can be spent on
// (decision 91, narrowed by issue #552). The real sigil comes off the wire
// and is hard-coded nowhere, so before an answer is in hand there is nothing
// to test a token against but its shape; every keystroke after that is
// filtered by the answer's own sigil. A token this accepts that no adapter
// honours costs one silent probe, which chatSkillList.probeFailed latches off
// so a keystroke cannot re-fire it — and a lone sigil is deliberately not
// enough, because one punctuation rune says nothing about whose sigil it is.
//
// `@` is excluded because it is the one shape that can never pay off: no
// shipped adapter reports it (§9.1 — claude and cursor `/`, codex `$`), while
// `@name` is exactly what a human types to mean a file. Spending the chat's
// one probe there buys an agent CLI spawn and, when it fails, inline skills
// off for the rest of the chat. Only the bare short form reaches here anyway:
// `@README.md` and `@src/main.go` already fail the name loop below.
//
// The exclusion is unconditional because at probe time there is no wire sigil
// to consult. An adapter that reported `invoke_sigil: "@"` would simply not
// self-start its inline list from a typed `@` — it needs `tab`, or an answer
// already cached. Once an answer *is* in hand the wire's sigil wins unchanged:
// filtering runs through chatSkillList.inlineFilter, which never comes here.
func chatSkillSigilShaped(tok string) bool {
	runes := []rune(tok)
	if len(runes) < 2 || unicode.IsLetter(runes[0]) || unicode.IsDigit(runes[0]) || runes[0] == '_' {
		return false
	}
	if runes[0] == chatFileMentionSigil {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

// chatSkillNameShaped reports the text after a sigil that is a skill name
// rather than a path, which is the whole of decision 92: `/tmp/notes.md` and
// `/Users/x` are silent, `/tdds` is hinted.
//
// Both separators are tested on every platform, not `\` on Windows alone: a
// draft is prose a human may write about any machine, and a rule that
// differed per host would make the same message say two things — and a test
// of it pass on one CI leg and fail on another.
func chatSkillNameShaped(rest string) bool {
	return rest != "" && !strings.ContainsAny(rest, `/\`)
}

// chatSkillAcceptNote is what an accepted skill takes, for the note line:
// the invocation, its argument hint and its description (issue #510 item 3).
func chatSkillAcceptNote(r chatInlineRow) string {
	head := strings.TrimSpace(r.display + " " + r.hint)
	if r.description == "" {
		return head
	}
	return head + " — " + r.description
}
