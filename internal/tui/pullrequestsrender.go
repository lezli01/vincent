package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The pull-requests takeover's rendering. It is split from the view for the
// reason the board and the detail panes are: what a screen *is* and what it
// *looks like* change for different reasons and at different rates.

var (
	// stylePullMerged and friends give the one status word its own colour.
	// Merged beats closed and draft beats open, the order a human reads them
	// in — the same fold GitHubPullRequest.Status applies.
	stylePullMerged = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	stylePullDraft  = lipgloss.NewStyle().Faint(true)
)

func (v *pullRequestsView) render(width, height int) string {
	if width < 4 || height < 2 {
		return ""
	}
	lines := make([]string, 0, height)
	lines = append(lines, v.headerLine(width))
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-1, 10))
		lines = append(lines, fieldRows(" ", v.filter)...)
	}
	lines = append(lines, "")

	// The footer is measured first: a wrapped question or note takes lines
	// the rows give back, and the window then keeps the cursor's row and its
	// detail on screen in what is left (#804 PL-5, PL-6).
	body, from, to := v.bodyLines(width)
	footer := v.footerLines(width)
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, windowRange(body, from, to, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// headerLine says what the screen is showing and how fresh it is.
func (v *pullRequestsView) headerLine(width int) string {
	left := " " + styleTitle.Render(pullsNoun(v.state))
	st, probed := v.github()
	switch {
	case v.project.id == 0:
		left += styleDim.Render("  ·  no project selected")
	case !probed:
		left += styleDim.Render("  ·  asking GitHub about " + v.project.name + "…")
	case !st.Available:
		left += styleDim.Render("  ·  " + v.project.name + " has no usable GitHub integration")
	case !v.loaded && v.loading:
		// The project's name, not a bare "listing…": after a switch this is
		// the one line saying which project the empty list is waiting on.
		left += styleDim.Render("  ·  loading " + v.project.name + "…")
	case v.loadErr != "":
		// No count: a failed listing knows nothing about how many there
		// are, and "0 in" would say the repository has none (review F2).
		left += styleDim.Render("  ·  could not list " + v.githubWhere(st))
	default:
		left += styleDim.Render(fmt.Sprintf("  ·  %d in %s", len(v.pulls), v.githubWhere(st)))
	}
	right := ""
	if !v.lastLoad.IsZero() {
		right = styleDim.Render("updated "+v.lastLoad.Format("15:04:05")) + " "
	}
	return padBetween(left, right, width)
}

// githubWhere names what the header is about: the repository when the probe
// said which, the project otherwise.
func (v *pullRequestsView) githubWhere(st apiclient.GitHubStatus) string {
	if st.Repo != "" {
		return st.Repo
	}
	return v.project.name
}

// pullsNoun names what the listing holds in the state `s` has it on (#804
// PL-4): a closed listing that still says "open" describes the wrong list.
func pullsNoun(state string) string {
	if state == "" || state == "all" {
		return "pull requests"
	}
	return state + " pull requests"
}

// bodyLines is the list, and the [from,to) range of lines the cursor's row
// and its detail occupy so the window can keep the whole of it in view.
func (v *pullRequestsView) bodyLines(width int) (lines []string, from, to int) {
	if v.project.id == 0 {
		// Never an unfiltered list in its place (task 132.11). No selection
		// means no project is registered only once a listing has said so;
		// until then it is still resolving.
		if v.noProjects {
			return []string{styleDim.Render("  " + noProjectsEmpty())}, 0, 1
		}
		return []string{styleDim.Render("  Resolving the project…")}, 0, 1
	}
	st, probed := v.github()
	if !probed {
		return []string{styleDim.Render("  asking whether " + v.project.name + " has a usable GitHub integration…")}, 0, 1
	}
	if !st.Available {
		// The view stays on a switch to such a project (task 132.11): its
		// probe's reason stands in for the rows, and no listing is issued.
		// Wrapped, not cut: the reason carries its remedy (#804 PL-5).
		lines = pullStyleLines(styleWarn, wrapText("This project has no usable GitHub integration: "+st.Unavailable(),
			width, wrapOpts{prefix: "  ", indent: 2}))
		lines = append(lines, "",
			styleDim.Render("  A project qualifies when its origin remote is a github.com"),
			styleDim.Render("  repository and vincent can authenticate to it."))
		return lines, 0, 1
	}
	if v.loadErr != "" {
		return pullStyleLines(styleBad, wrapText(v.loadErr, width, wrapOpts{prefix: "  ⚠ ", indent: 4})), 0, 1
	}
	if !v.loaded {
		return []string{styleDim.Render("  listing " + v.project.name + "'s pull requests…")}, 0, 1
	}

	q := strings.TrimSpace(v.filter.Value())
	rows := v.rows()
	if len(rows) == 0 {
		return []string{styleDim.Render("  " + emptyPullsNote(v.state, len(v.pulls), q))}, 0, 1
	}
	pulls := make([]apiclient.GitHubPullRequest, len(rows))
	for i, r := range rows {
		pulls[i] = r.pull
	}
	layout := v.pullColumns(pulls, width)
	lines = make([]string, 0, len(rows)+2)
	for i, p := range pulls {
		if i != v.cursor {
			lines = append(lines, v.pullLine(p, layout, false))
			continue
		}
		from = len(lines)
		lines = append(lines, v.pullLine(p, layout, true))
		lines = append(lines, pullDetailLines(p, layout, width)...)
		to = len(lines)
	}
	return lines, from, to
}

func emptyPullsNote(state string, total int, query string) string {
	if total == 0 {
		return "no " + pullsNoun(state)
	}
	return fmt.Sprintf("none of %s match %q",
		plural(total, "pull request", "pull requests"), query)
}

// pullCols is the row layout every row of one render shares, so the
// columns line up down the list (#804 PL-3). A zero branch width drops the
// branch from the rows; the selected row's detail line still carries it.
type pullCols struct {
	title, claim, branch int
}

// pullFixedW is the marker, the number and the status word: the columns
// that never give way.
const pullFixedW = 2 + 7 + 7

// pullColumns lays the rows out in the width: the title first, then the
// claim, and the head branch in whatever is left (#804 PL-3). The branch is
// the column that gives way — a dependabot branch is long, says little the
// title does not, and is on the detail line in full — never the title or
// the claim, which are what a human scans this list for.
func (v *pullRequestsView) pullColumns(pulls []apiclient.GitHubPullRequest, width int) pullCols {
	var maxTitle, maxClaim, minClaim, maxBranch int
	for _, p := range pulls {
		maxTitle = max(maxTitle, cols(p.Title))
		maxClaim = max(maxClaim, cols(v.claimText(p, 0)))
		// The narrowest a claim can be drawn: claimText drops the task's
		// title before it gives up "task #n (how)".
		minClaim = max(minClaim, cols(v.claimText(p, 1)))
		if p.HeadBranch != "" {
			maxBranch = max(maxBranch, cols("⎇ "+p.HeadBranch))
		}
	}
	avail := max(width-pullFixedW, 0)
	// The claim keeps "task #n (how)" whole and lends its task title to the
	// row's title when the row is short of room.
	// Its floor is the widest "task #n (how)" in the list, never a fixed
	// count a five-digit task id outgrows (review F10); the title falls to
	// its own floor first.
	c := pullCols{claim: max(min(maxClaim, max(avail*2/5, 16)), minClaim)}
	c.title = max(min(maxTitle, avail-c.claim-2), 12)
	// A short title hands its spare cells back to the claim.
	c.claim = min(maxClaim, max(c.claim, avail-c.title-2))
	if rest := avail - c.title - 2 - c.claim - 2; rest >= 10 && maxBranch > 0 {
		c.branch = min(rest, maxBranch)
	}
	return c
}

// pullLine is one row: the number, the folded status word, the title, the
// task that claims it, with how the claim was made — `auto` is the
// reconciler's head-branch match and `human` is a link somebody made by
// hand, and a screen that offers to unlink should say which it is about to
// override — and the head branch when the width has room for it.
func (v *pullRequestsView) pullLine(p apiclient.GitHubPullRequest, c pullCols, selected bool) string {
	marker := "  "
	if selected {
		marker = styleFocus.Render("› ")
	}
	number := padRight("#"+strconv.Itoa(p.Number), 7)
	status := padRight(p.Status(), 7)
	title := padDisplayWidth(ansi.Truncate(p.Title, c.title, "…"), c.title)
	claim := styleDim.Render(v.claimText(p, c.claim))

	line := marker + styleKey.Render(number) + pullStatusStyle(p).Render(status) + title + "  "
	if c.branch == 0 || p.HeadBranch == "" {
		return line + claim
	}
	claim = padDisplayWidth(claim, c.claim)
	return line + claim + "  " + styleDim.Render(pullMiddleTruncate("⎇ "+p.HeadBranch, c.branch))
}

// pullDetailLines sit under the selected row (#804 PL-10): its whole title,
// wrapped, when the row had to cut it, and `head → base` in full, which the
// row gives way on first.
func pullDetailLines(p apiclient.GitHubPullRequest, c pullCols, width int) []string {
	indent := strings.Repeat(" ", pullFixedW)
	var out []string
	if cols(p.Title) > c.title {
		out = wrapText(p.Title, width, wrapOpts{prefix: indent, indent: pullFixedW})
	}
	branches := p.HeadBranch
	if branches != "" && p.BaseBranch != "" {
		branches += " → " + p.BaseBranch
	}
	if branches != "" {
		out = append(out, wrapText("⎇ "+branches, width, wrapOpts{prefix: indent, indent: pullFixedW + 2})...)
	}
	return pullStyleLines(styleDim, out)
}

// pullMiddleTruncate cuts s to width cells from the middle, keeping both ends:
// a branch's prefix says who made it and its tail which change it is.
func pullMiddleTruncate(s string, width int) string {
	n := cols(s)
	if n <= width {
		return s
	}
	if width <= 1 {
		return ansi.Truncate(s, width, "")
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return ansi.Truncate(s, head, "") + "…" + ansi.TruncateLeft(s, n-tail, "")
}

// pullStyleLines styles each wrapped line on its own — wrap plain text, then
// style (task 050 decision 8).
func pullStyleLines(style lipgloss.Style, lines []string) []string {
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	return lines
}

func pullStatusStyle(p apiclient.GitHubPullRequest) lipgloss.Style {
	switch p.Status() {
	case "merged":
		return stylePullMerged
	case "closed":
		return styleBad
	case "draft":
		return stylePullDraft
	default:
		return styleOK
	}
}

// claimText names the task that claims a row, or says nothing claims it —
// those are the rows a human is here to link. It is plain text in at most
// width cells, zero meaning its natural width: the task's title is what
// gives way, so "task #n" and how the claim was made always survive
// (#804 PL-3).
func (v *pullRequestsView) claimText(p apiclient.GitHubPullRequest, width int) string {
	if p.TaskID == nil {
		return "unclaimed"
	}
	core := "task #" + strconv.FormatInt(*p.TaskID, 10)
	source := ""
	if p.LinkSource != "" {
		source = " (" + p.LinkSource + ")"
	}
	title := ""
	if t, ok := v.taskByID(*p.TaskID); ok && t.Title != "" {
		title = ansi.Truncate(t.Title, 24, "…")
	}
	if width > 0 {
		if room := width - cols(core) - cols(source) - 1; room < 4 {
			title = ""
		} else {
			title = ansi.Truncate(title, room, "…")
		}
	}
	if title != "" {
		core += " " + title
	}
	return core + source
}

// footerLines are the popup-ish rows that own the keyboard while they are up,
// plus whatever the last action had to say.
func (v *pullRequestsView) footerLines(width int) []string {
	switch {
	case v.picker != nil:
		out := append([]string{""}, v.picker.renderBody()...)
		return append(out, styleDim.Render(
			"  ↑/↓ choose · / filter · enter link · esc close the list"))
	case v.confirm != nil:
		// Wrapped, so a long consequence never pushes its keys off the
		// line (#804 PL-6).
		out := append([]string{""}, pullStyleLines(styleWarn, wrapText(v.confirm.text, width, wrapOpts{prefix: "  ", indent: 2}))...)
		return append(out, styleDim.Render("  y unlink · any other key cancels"))
	case v.note != "":
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		return append([]string{""}, pullStyleLines(style, wrapText(v.note, width, wrapOpts{prefix: "  ", indent: 2}))...)
	}
	return nil
}
