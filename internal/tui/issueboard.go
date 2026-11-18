package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issues board (task 150.2): one column per lane, one card per issue.
// Which lane an issue is in stays the daemon's answer (task 134 decision 1);
// this file only draws it. A terminal too narrow for every column shows the
// ones around the cursor, and ← and → bring the others in.

const (
	// issueCardH is a card's outer height: five lines inside a border.
	issueCardH = 7
	// issueLaneMinW is the narrowest column a card stays readable in.
	issueLaneMinW = 28
)

// issueLaneLook is how one section is drawn; it is indexed like issueSections.
type issueLaneLook struct {
	glyph string
	tone  lipgloss.Style
}

var issueLaneLooks = []issueLaneLook{
	{"○", toneBlue},
	{"◐", styleWarn},
	{"⇄", toneMagenta},
	{"✓", styleOK},
}

// issueCardHit is where the last render drew a card, for a click.
type issueCardHit struct {
	x0, x1, y0, y1 int
	id             int64
}

// boardLines draws the columns in exactly height lines of width cells. top is
// the view line the board starts on, which a click is measured against.
func (v *issuesView) boardLines(width, height, top int) []string {
	v.cardHits = nil
	shown := v.shownSections()
	fit := min(max(width/issueLaneMinW, 1), shown)
	sec := max(v.cursorSection(), 0)
	first := max(min(sec-(fit-1)/2, shown-fit), 0)
	parts := partitionIssues(v.filtered())

	laneW := width / fit
	columns := make([][]string, 0, fit)
	x := 0
	for i := first; i < first+fit; i++ {
		w := laneW
		if i == first+fit-1 {
			w = width - x
		}
		more := issueLaneMore{left: i == first && first > 0, right: i == first+fit-1 && first+fit < shown}
		columns = append(columns, v.laneLines(i, parts[i], x, top, w, height, more))
		x += w
	}
	out := make([]string, height)
	for y := range out {
		var b strings.Builder
		for _, c := range columns {
			if y < len(c) {
				b.WriteString(c[y])
			}
		}
		out[y] = b.String()
	}
	return out
}

// issueLaneMore says a column has hidden neighbours on either side.
type issueLaneMore struct{ left, right bool }

// laneLines draws one column: its title and count in the top edge, the cards
// that fit around the selected one, and a cue for the cards that do not.
func (v *issuesView) laneLines(sec int, issues []apiclient.Issue, x, top, w, h int, more issueLaneMore) []string {
	look := issueLaneLooks[sec]
	title := look.tone.Bold(true).Render(ic(look.glyph) + " " + issueLaneTitle(issueSections[sec].label))
	if more.left {
		title = styleDim.Render("‹ ") + title
	}
	right := look.tone.Render(strconv.Itoa(len(issues)))
	if n := issueAttention(issues); n > 0 {
		right = styleWarn.Render(attentionBadge+" "+strconv.Itoa(n)) + "  " + right
	}
	if more.right {
		right += styleDim.Render(" ›")
	}

	room := h - 2
	per := max(room/issueCardH, 1)
	if len(issues) > per {
		per = max((room-1)/issueCardH, 1)
	}
	pos := 0
	for i, iss := range issues {
		if iss.ID == v.selected {
			pos = i
			v.page = per
		}
	}
	start := windowStart(len(issues), pos, per)
	end := min(start+per, len(issues))

	var body []string
	if len(issues) == 0 {
		none := "no issues"
		if strings.TrimSpace(v.filter.Value()) != "" {
			none = "none match"
		}
		body = append(body, styleDim.Render(none))
	}
	cardW := w - 4
	for _, iss := range issues[start:end] {
		y := top + 1 + len(body)
		v.cardHits = append(v.cardHits, issueCardHit{x0: x + 2, x1: x + 2 + cardW, y0: y, y1: y + issueCardH, id: iss.ID})
		body = append(body, v.issueCard(iss, cardW)...)
	}
	if cue := issueLaneCue(start, len(issues)-end); cue != "" {
		body = append(body, styleDim.Render(cue))
	}
	return drawBox(title, right, body, w, h, edgesRounded, styleDim)
}

// issueLaneTitle capitalizes a section's label for its column.
func issueLaneTitle(label string) string {
	r, n := utf8.DecodeRuneInString(label)
	return string(unicode.ToUpper(r)) + label[n:]
}

// issueAttention counts the issues waiting on a person, so a column's edge says
// so even when the card that needs someone is scrolled away.
func issueAttention(issues []apiclient.Issue) int {
	n := 0
	for _, iss := range issues {
		if iss.Attention {
			n++
		}
	}
	return n
}

// issueLaneCue counts the cards scrolled off a column, "" when none are.
func issueLaneCue(above, below int) string {
	switch {
	case above > 0 && below > 0:
		return fmt.Sprintf("▲ %d  ▼ %d more", above, below)
	case above > 0:
		return fmt.Sprintf("▲ %d more", above)
	case below > 0:
		return fmt.Sprintf("▼ %d more", below)
	}
	return ""
}

// issueCard draws one card (task 150 decision 4): id, kind and age; two lines
// of title; labels and source; one status line.
func (v *issuesView) issueCard(iss apiclient.Issue, w int) []string {
	inner := max(w-4, 1)
	selected := iss.ID == v.selected
	titleStyle := lipgloss.NewStyle()
	edges, border := edgesRounded, styleDim
	if selected {
		titleStyle, edges, border = styleTitle, edgesThick, styleFocus
	}

	head := styleKey.Render("#" + strconv.FormatInt(iss.ID, 10))
	if v.picked[iss.ID] {
		head = styleFocus.Render(ic("✔ ")) + head
	}
	if kind := issueKindMark(iss.Kind); kind != "" {
		head += "  " + kind
	}
	body := []string{padBetweenCells(head, styleDim.Render(issueAge(v.now(), iss.UpdatedAt)), inner)}

	title := wrapText(iss.Title, inner, wrapOpts{maxLines: 2})
	for i := range 2 {
		line := ""
		if i < len(title) {
			line = titleStyle.Render(title[i])
		}
		body = append(body, line)
	}
	body = append(body, issueCardTags(iss, inner, issuesShareRepo(v.issues)))
	status, style := issueCardStatus(iss)
	body = append(body, style.Render(status))
	return drawBox("", "", body, w, issueCardH, edges, border)
}

// padBetweenCells puts left and right at the two ends of width cells.
func padBetweenCells(left, right string, width int) string {
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// issueKindMark is a kind's glyph and word, "" for an issue with no kind.
func issueKindMark(kind string) string {
	switch kind {
	case "":
		return ""
	case "bug":
		return styleBad.Render(ic("◆ bug"))
	case "feature":
		return styleOK.Render(ic("✦ feature"))
	case "chore":
		return toneMagenta.Render(ic("● chore"))
	}
	return styleDim.Render("• " + kind)
}

// issueAge is how long ago an issue last changed, in the fewest cells.
func issueAge(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return ic("◷ now")
	case d < time.Hour:
		return ic("◷ ") + strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return ic("◷ ") + strconv.Itoa(int(d.Hours())) + "h"
	case d < 30*24*time.Hour:
		return ic("◷ ") + strconv.Itoa(int(d.Hours()/24)) + "d"
	case d < 365*24*time.Hour:
		return ic("◷ ") + strconv.Itoa(int(d.Hours()/(30*24))) + "mo"
	}
	return ic("◷ ") + strconv.Itoa(int(d.Hours()/(365*24))) + "y"
}

// issueCardTags is a card's labels on the left and its source on the right.
// The source never goes (#796): it is what a person matches the card against
// at its origin, so the labels give way to a `+N` count first.
func issueCardTags(iss apiclient.Issue, width int, sharedRepo bool) string {
	// With no labels to share the line with, the source may have all of it.
	limit := width
	if len(iss.Labels) > 0 {
		limit = width / 2
	}
	source := issueSourceBadge(iss)
	if ansi.StringWidth(source) > limit && sharedRepo && iss.Source.ShortRef != "" {
		source = iss.Source.ShortRef
	}
	if source != "" {
		source = fitLeft(source, limit, sourceKeep(iss))
	}
	room := width
	if source != "" {
		room -= ansi.StringWidth(source) + 1
	}
	var tags strings.Builder
	used := 0
	for i, label := range iss.Labels {
		pill := " " + label + " "
		need := ansi.StringWidth(pill) + 1
		rest := len(iss.Labels) - i - 1
		reserve := 0
		if rest > 0 {
			reserve = len(strconv.Itoa(rest)) + 1
		}
		if used+need+reserve > room {
			tags.WriteString(styleDim.Render("+" + strconv.Itoa(len(iss.Labels)-i)))
			break
		}
		tags.WriteString(tonePill.Render(pill) + " ")
		used += need
	}
	if source == "" {
		return tags.String()
	}
	return padBetweenCells(tags.String(), styleDim.Render(source), width)
}

// issueCardStatus is a card's last line, led by a glyph so it survives NO_COLOR:
// what is true of the issue, the thing that most needs a person first.
func issueCardStatus(iss apiclient.Issue) (string, lipgloss.Style) {
	tasks := ic(strings.TrimPrefix(issueTaskSummary(iss), "● "))
	hand := issueHandoffCell(iss)
	switch {
	case iss.Attention:
		return attentionBadge + " " + tasks + " · needs you", styleWarn.Bold(true)
	case iss.State == "closed":
		return ic("✓ ") + strings.TrimPrefix(issueStateBadge(iss), "closed · "), styleOK
	case hand != "" && iss.Lane == "hand_off":
		// The column already says the work is finished, and the marker is what
		// a narrow card must not lose.
		return ic("⇄ ") + hand, toneMagenta
	case hand != "":
		return ic("⇄ ") + hand + " · " + tasks, toneMagenta
	case iss.Active:
		return ic("▶ ") + tasks, toneCyan
	case iss.Lane == "hand_off":
		return tasks, styleOK
	}
	return ic("○ ") + tasks, styleDim
}

// filterBox is the rounded box above the board: the filter, and at its top
// right what the board is showing.
func (v *issuesView) filterBox(width int) []string {
	body := []string{toneBlue.Render(ic("⌕ ")) + styleDim.Render(v.filter.Placeholder())}
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-6, 10))
		body = fieldRows("", v.filter)
	}
	return drawBox("", v.boardSummary(), body, width, len(body)+2, edgesRounded, styleDim)
}

// boardSummary says what the board is showing, for the filter box's edge.
func (v *issuesView) boardSummary() string {
	var parts []string
	switch {
	case v.project.id == 0:
		parts = append(parts, "no project selected")
	case !v.loaded && v.loading:
		// The project's name, not a bare "listing…": after a switch this is
		// the one line saying which project the empty board is waiting on.
		parts = append(parts, "loading "+v.project.name+"…")
	case v.loaded:
		parts = append(parts, plural(len(v.issues), "issue", "issues"))
	}
	if !v.lastLoad.IsZero() {
		parts = append(parts, "updated "+v.lastLoad.Format("15:04:05"))
	}
	out := styleDim.Render(strings.Join(parts, " · "))
	if n := len(v.picked); n > 0 {
		out = styleFocus.Render(strconv.Itoa(n)+" selected") + styleDim.Render(" · ") + out
	}
	return out
}

// frameless reports that the board draws its own boxes, so the root must not
// wrap it in another. The form and the handoff form keep the root's frame.
func (v *issuesView) frameless() bool { return v.handoff == nil && v.w.form == nil }

// laneRows is the flat indices of the cards in the cursor's section.
func (v *issuesView) laneRows(lines []issueRow, sec int) []int {
	var out []int
	for i, row := range lines {
		if row.section == sec && row.selectable() {
			out = append(out, i)
		}
	}
	return out
}

// moveInLane moves the selection delta cards up or down its own column.
func (v *issuesView) moveInLane(delta int) {
	lines := v.lines()
	rows := v.laneRows(lines, v.cursorSection())
	for pos, i := range rows {
		if i == v.cursor {
			v.cursor = rows[min(max(pos+delta, 0), len(rows)-1)]
			v.remember(lines)
			return
		}
	}
}

// edgeOfLane selects the first or last card of the selection's column.
func (v *issuesView) edgeOfLane(last bool) {
	lines := v.lines()
	rows := v.laneRows(lines, v.cursorSection())
	if len(rows) == 0 {
		return
	}
	v.cursor = rows[0]
	if last {
		v.cursor = rows[len(rows)-1]
	}
	v.remember(lines)
}

// moveLane moves the selection to the nearest column in delta's direction that
// has a card, onto the card at the same height or the last one above it.
func (v *issuesView) moveLane(delta int) {
	lines := v.lines()
	sec := v.cursorSection()
	if sec < 0 {
		return
	}
	pos := 0
	for p, i := range v.laneRows(lines, sec) {
		if i == v.cursor {
			pos = p
		}
	}
	for s := sec + delta; s >= 0 && s < v.shownSections(); s += delta {
		if rows := v.laneRows(lines, s); len(rows) > 0 {
			v.cursor = rows[min(pos, len(rows)-1)]
			v.remember(lines)
			return
		}
	}
}
