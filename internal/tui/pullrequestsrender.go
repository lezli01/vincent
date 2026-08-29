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
		lines = append(lines, " "+v.filter.View())
	}
	lines = append(lines, "")

	body, cursorRow := v.bodyLines(width)
	footer := v.footerLines()
	room := max(height-len(lines)-len(footer), 1)
	lines = append(lines, window(body, cursorRow, room)...)
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// headerLine says what the screen is showing and how fresh it is.
func (v *pullRequestsView) headerLine(width int) string {
	left := " " + styleTitle.Render("open pull requests")
	switch {
	case len(v.available) == 0:
		left += styleDim.Render("  ·  no project with a usable GitHub integration")
	case !v.loaded && v.loading:
		left += styleDim.Render("  ·  listing…")
	default:
		left += styleDim.Render(fmt.Sprintf("  ·  %d across %s",
			v.countPulls(), plural(len(v.available), "project", "projects")))
	}
	right := ""
	if !v.lastLoad.IsZero() {
		right = styleDim.Render("updated "+v.lastLoad.Format("15:04:05")) + " "
	}
	return padBetween(left, right, width)
}

func (v *pullRequestsView) countPulls() int {
	n := 0
	for _, g := range v.groups {
		n += len(g.pulls)
	}
	return n
}

// bodyLines is the grouped list, and the index of the line the cursor is on
// so the window can follow it.
func (v *pullRequestsView) bodyLines(width int) (lines []string, cursorRow int) {
	if len(v.available) == 0 {
		return []string{
			styleDim.Render("  No registered project has a usable GitHub integration."),
			"",
			styleDim.Render("  A project qualifies when its origin remote is a github.com"),
			styleDim.Render("  repository and vincent can authenticate to it."),
		}, 0
	}
	if !v.loaded {
		return []string{styleDim.Render("  listing pull requests…")}, 0
	}

	q := strings.TrimSpace(v.filter.Value())
	rows := v.rows()
	seen := 0
	for _, g := range v.groups {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, v.groupHeader(g, width))
		if g.err != "" {
			// One group's failure, on that group. The others still render:
			// that separation is the whole point of listing per project.
			lines = append(lines, styleBad.Render("    ⚠ "+g.err))
			continue
		}
		shown := 0
		for _, p := range g.pulls {
			if q != "" && !pullMatches(g.project, p, strings.ToLower(q)) {
				continue
			}
			selected := seen < len(rows) && seen == v.cursor
			if selected {
				cursorRow = len(lines)
			}
			lines = append(lines, v.pullLine(p, width, selected))
			shown++
			seen++
		}
		if shown == 0 {
			lines = append(lines, styleDim.Render("    "+emptyGroupNote(len(g.pulls), q)))
		}
	}
	return lines, cursorRow
}

func emptyGroupNote(total int, query string) string {
	if total == 0 {
		return "no open pull requests"
	}
	return fmt.Sprintf("none of %s match %q",
		plural(total, "pull request", "pull requests"), query)
}

func (v *pullRequestsView) groupHeader(g pullGroup, width int) string {
	repo := ""
	for _, gp := range v.available {
		if gp.project.ID == g.project.ID {
			repo = gp.status.Repo
			break
		}
	}
	left := " " + styleTitle.Render(g.project.Name)
	if repo != "" {
		left += styleDim.Render("  " + repo)
	}
	count := ""
	if g.err == "" {
		count = styleDim.Render(fmt.Sprintf("%d open ", len(g.pulls)))
	}
	return padBetween(left, count, width)
}

// pullLine is one row: the number, the folded status word, the title, the
// head branch and the task that claims it, with how the claim was made —
// `auto` is the reconciler's head-branch match and `human` is a link
// somebody made by hand, and a screen that offers to unlink should say which
// it is about to override.
func (v *pullRequestsView) pullLine(p apiclient.GitHubPullRequest, width int, selected bool) string {
	marker := "  "
	if selected {
		marker = styleFocus.Render("› ")
	}
	number := padRight("#"+strconv.Itoa(p.Number), 7)
	status := padRight(p.Status(), 7)
	claim := v.claimText(p)
	branch := p.HeadBranch
	if branch != "" {
		branch = "⎇ " + branch
	}

	// The title takes what the fixed columns leave; nothing else is
	// truncatable without becoming a lie.
	fixed := 2 + len(number) + len(status) + ansi.StringWidth(branch) + ansi.StringWidth(ansi.Strip(claim)) + 6
	titleW := max(width-fixed, 12)
	title := ansi.Truncate(p.Title, titleW, "…")

	line := marker + styleKey.Render(number) + pullStatusStyle(p).Render(status) +
		padDisplayWidth(title, titleW) + "  " + styleDim.Render(branch)
	if claim != "" {
		line += "  " + claim
	}
	return line
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
// those are the rows a human is here to link.
func (v *pullRequestsView) claimText(p apiclient.GitHubPullRequest) string {
	if p.TaskID == nil {
		return styleDim.Render("unclaimed")
	}
	label := "task #" + strconv.FormatInt(*p.TaskID, 10)
	if t, ok := v.taskByID(*p.TaskID); ok && t.Title != "" {
		label += " " + ansi.Truncate(t.Title, 24, "…")
	}
	if p.LinkSource != "" {
		label += " (" + p.LinkSource + ")"
	}
	return styleDim.Render(label)
}

// footerLines are the popup-ish rows that own the keyboard while they are up,
// plus whatever the last action had to say.
func (v *pullRequestsView) footerLines() []string {
	switch {
	case v.picker != nil:
		out := append([]string{""}, v.picker.renderBody()...)
		return append(out, styleDim.Render(
			"  ↑/↓ choose · / filter · enter link · esc close the list"))
	case v.confirm != nil:
		return []string{
			"",
			styleWarn.Render("  " + v.confirm.text),
			styleDim.Render("  y unlink · any other key cancels"),
		}
	case v.note != "":
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		return []string{"", style.Render("  " + v.note)}
	}
	return nil
}
