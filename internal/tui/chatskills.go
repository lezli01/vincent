package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's skill list (§15 view 9, task 124.13, issue #509): the
// skills the chat's agent CLI would load in the chat's directory, drawn
// between the note line and the composer, opened with `tab` (or `f2`) and
// closed with `esc`.
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

// chatSkillsDescLines is how many wrapped lines the highlighted row's
// description gets under the list. They are *reserved* whenever the list is
// open rather than added when a row is highlighted (decision 75), so arrowing
// through the rows does not change the body's budget and scroll the
// conversation under the reader.
const chatSkillsDescLines = 3

// chatSkillList is the list's whole state. The zero value is a closed list
// that has asked for nothing.
type chatSkillList struct {
	// open is whether the list is on screen and owns the keyboard.
	open bool
	// loading is a request in flight with nothing to draw yet.
	loading bool
	// data is the last answer for chatID; nil until one arrives. It is kept
	// across opens and dropped on a `chat.*` event, which is the daemon's
	// own signal that a turn boundary may have changed the listing
	// (decision 73: there is no refresh key here, and ctrl+r keeps its one
	// meaning).
	data *apiclient.ChatSkills
	// err is a failed fetch, shown on the note line.
	err string

	// filter is the list's own buffer, drawn on the title line and never in
	// the composer.
	filter string
	// cursor indexes rows; -1 is "nothing highlighted", which is what keeps
	// `enter` meaning "send the message as typed" until the human moves into
	// the list.
	cursor int
	rows   []chatSkillRow
}

// chatSkillRow is one drawn row: the flattened, sanitized text and whether it
// can be accepted.
type chatSkillRow struct {
	// invocation is the exact text inserted into the draft — the adapter's
	// own, byte for byte, never rebuilt here.
	invocation string
	// display is invocation flattened and sanitized for drawing. They differ
	// only for a hostile row, which is disabled anyway.
	display     string
	hint        string
	description string
	note        string
	// disabled marks a row that cannot be accepted, with why in reason.
	disabled bool
	reason   string
}

// chatSkillFlatten makes agent-supplied text safe to measure and to draw.
//
// sanitizeText alone is not enough: it deliberately keeps `\n` and `\t`
// (outputlines.go), and a multi-line string reaching a row would trip the
// #299 hazard chatrender.go documents — render's per-line ansi.Truncate
// measures a joined string as the sum of its rows. So every field is
// flattened to one line first, the way task 124 decision 67 flattens an
// invocation's arguments, and sanitized after.
func chatSkillFlatten(s string) string {
	return sanitizeText(agent.OneLine(s, 0))
}

// chatSkillHostile reports text a row must not be able to insert into the
// draft: a C0 or C1 control — a newline among them — or an ANSI escape,
// whose introducer is itself a control.
//
// Whitespace is allowed on purpose (decision 71). Banning it would disable
// codex's linked form for a duplicated name,
// `[$review](/path with spaces/.agents/skills/review/SKILL.md)`, which is
// exactly what task 124 decision 30 requires — and on macOS a chat
// worktree's path always has a space in it, so the ban would have fired on
// the common case rather than on a hostile one.
func chatSkillHostile(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	}) >= 0
}

// reset clears the filter and the highlight, which is the state `tab` opens
// the list in: every row, nothing selected.
func (l *chatSkillList) reset() {
	l.filter = ""
	l.cursor = -1
}

// forget drops the cached answer. The next open asks again.
func (l *chatSkillList) forget() {
	l.data = nil
	l.err = ""
	l.loading = false
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
// them.
func (l *chatSkillList) build() {
	l.rows = l.rows[:0]
	if !l.canList() {
		l.cursor = -1
		return
	}
	reason := ""
	if !l.canInvoke() {
		reason = chatSkillsNoInvokeReason(l.data)
	}
	for _, i := range rankChatSkills(l.data.Skills, l.filter) {
		s := l.data.Skills[i]
		row := chatSkillRow{
			invocation:  s.Invocation,
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

// move walks the highlight. From "nothing highlighted", `↓` lands on the
// first row and `↑` on the last.
func (l *chatSkillList) move(delta int) {
	if len(l.rows) == 0 {
		l.cursor = -1
		return
	}
	switch {
	case l.cursor < 0 && delta > 0:
		l.cursor = 0
	case l.cursor < 0:
		l.cursor = len(l.rows) - 1
	default:
		l.cursor = min(max(l.cursor+delta, 0), len(l.rows)-1)
	}
}

// pick is the row `enter` or `tab` would accept: the highlighted one, or the
// top match when nothing is highlighted.
func (l *chatSkillList) pick() (chatSkillRow, bool) {
	if len(l.rows) == 0 {
		return chatSkillRow{}, false
	}
	return l.rows[max(l.cursor, 0)], true
}

// typeText extends the filter with the text one key produced. The press's
// own Text is what is asked, not its spelling: that is what a terminal says
// the key typed, and it is empty for every ctrl, alt and named key, so the
// list's buffer can never take a control by accident.
func (l *chatSkillList) typeText(text string) bool {
	if text == "" || chatSkillHostile(text) {
		return false
	}
	l.filter += text
	l.cursor = -1
	l.build()
	return true
}

// backspace shortens the filter, and reports the press that found it already
// empty: that closes the list, which is the way out the human walked in by.
func (l *chatSkillList) backspace() (closed bool) {
	if l.filter == "" {
		return true
	}
	runes := []rune(l.filter)
	l.filter = string(runes[:len(runes)-1])
	l.cursor = -1
	l.build()
	return false
}

// height is how many lines the list occupies, which render must spend out of
// the body's budget before it draws anything (#299). It does not depend on
// the highlight: the description's lines are reserved, not added.
func (l *chatSkillList) height(paneHeight int) int {
	if !l.open {
		return 0
	}
	return 1 + l.window(paneHeight) + chatSkillsDescLines
}

// window is how many row lines are drawn: the picker's fixed window, what the
// pane has room for, and at most a third of the pane — below the §7.4
// popup's half, because this list is an aid to typing and that popup is the
// turn itself.
func (l *chatSkillList) window(paneHeight int) int {
	rows := max(len(l.rows), 1)
	return max(min(rows, pickerWindow, paneHeight/3-1-chatSkillsDescLines), 1)
}

// render draws the list: a title line carrying the filter and the probe's
// age, the windowed rows, and the highlighted row's description wrapped into
// the fixed reserve. One slice element per rendered line (#299).
func (l *chatSkillList) render(width, paneHeight int, now time.Time) []string {
	if !l.open {
		return nil
	}
	win := l.window(paneHeight)
	out := make([]string, 0, 1+win+chatSkillsDescLines)
	out = append(out, " "+l.titleLine(now))

	switch {
	case l.loading:
		out = append(out, styleDim.Render(fmt.Sprintf("   asking %s which skills it has…", l.agentName())))
	case len(l.rows) == 0 && l.filter != "":
		out = append(out, styleDim.Render("   nothing matches — backspace clears the filter, esc closes the list"))
	case len(l.rows) == 0:
		out = append(out, styleDim.Render(fmt.Sprintf("   %s reported no skills for this chat", l.agentName())))
	default:
		lines := make([]string, 0, len(l.rows))
		for i, r := range l.rows {
			lines = append(lines, chatSkillRowLine(r, i == l.cursor, width))
		}
		out = append(out, window(lines, max(l.cursor, 0), win)...)
	}
	// The reserve, whether or not a row is highlighted.
	body := make([]string, chatSkillsDescLines)
	if r, ok := l.pick(); ok && l.cursor >= 0 {
		text := r.description
		if r.disabled && r.reason != "" {
			text = strings.TrimSpace(r.reason + " · " + text)
		}
		wrapped := wrapPlain(text, max(width-5, 8))
		for i := range min(len(wrapped), chatSkillsDescLines) {
			body[i] = styleDim.Render("    " + wrapped[i])
		}
	}
	out = append(out, body...)
	// Pad to the promised height: a window narrower than the rows still owes
	// render exactly what height() said it would draw.
	for len(out) < 1+win+chatSkillsDescLines {
		out = append(out, "")
	}
	return out[:1+win+chatSkillsDescLines]
}

// titleLine names the list, the filter being typed and how old the answer is.
func (l *chatSkillList) titleLine(now time.Time) string {
	title := styleTitle.Render("skills")
	tail := make([]string, 0, 3)
	if l.filter != "" {
		tail = append(tail, l.filter)
	}
	if l.data != nil && l.data.ProbedAt != nil {
		tail = append(tail, "probed "+formatElapsed(max(now.Sub(*l.data.ProbedAt), 0).Truncate(time.Second))+" ago")
	}
	if len(l.rows) > 0 {
		tail = append(tail, "tab insert · ↑/↓ pick · esc close")
	}
	if len(tail) > 0 {
		title += styleDim.Render("  ·  " + strings.Join(tail, "  ·  "))
	}
	return title
}

// agentName is whose list this is, "the agent" before an answer arrived.
func (l *chatSkillList) agentName() string {
	if l.data != nil && l.data.Agent != "" {
		return l.data.Agent
	}
	return "the agent"
}

// chatSkillRowLine draws one row. The selection is a `› ` marker as well as a
// colour, so it survives NO_COLOR and 16 colours (§15 Colour).
//
// Below roughly 40 columns the description and the note are dropped before
// the invocation is truncated: what a row *is* is what a reader picks by.
func chatSkillRowLine(r chatSkillRow, selected bool, width int) string {
	inner := max(width-3, 8)
	mark, style := "  ", styleDim
	if selected {
		mark, style = styleFocus.Render("› "), styleTitle
	}
	if r.disabled {
		style = styleBad
	}
	name := ansi.Truncate(r.display, inner, "…")
	line := " " + mark + style.Render(name)
	used := cols(name)
	if r.hint != "" && used+1+cols(r.hint) <= inner {
		line += styleDim.Render(" " + r.hint)
		used += 1 + cols(r.hint)
	}
	// Below roughly 40 columns the description and the note go before the
	// invocation is truncated: what a row *is* is what a reader picks by.
	rest := strings.Join(nonEmptyStrings(r.description, r.note), "  ·  ")
	if room := inner - used - 2; rest != "" && room >= 8 && width >= 40 {
		line += "  " + styleDim.Render(ansi.Truncate(rest, room, "…"))
	}
	return line
}

// nonEmptyStrings drops the fields the CLI said nothing about, so two empties
// never render as a separator with nothing on either side.
func nonEmptyStrings(in ...string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
