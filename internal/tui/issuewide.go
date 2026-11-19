package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issue detail's wide layout (task 150.10): a title band, the conversation
// in a pane the keyboard scrolls, and the issue's facts beside it, the shape the
// task workspace has (taskworkspace.go). The facts stay on screen while the
// description and the thread scroll, so they leave the page. A terminal too
// small for two panes keeps the single framed page.

const (
	// issueWideMinW is the narrowest terminal that shows the details beside
	// the conversation.
	issueWideMinW = 120
	// issueWideMinH is the shortest: the band and two pane borders would leave
	// a shorter terminal too little of the page.
	issueWideMinH = 18
	// issueDetailsMaxW is the widest the details pane grows.
	issueDetailsMaxW = 42
	// issueBandTitleMax caps the band's title lines, so a very long title
	// cannot push the conversation off the screen.
	issueBandTitleMax = 4
)

// wide reports whether the detail is in its wide layout. It reads the terminal
// rather than the width of the last render, which a frame makes narrower. The
// issue form takes the whole pane and keeps the root's frame.
func (v *issueView) wide() bool {
	return v.termW >= issueWideMinW && v.termH >= issueWideMinH && v.w.form == nil
}

// frameless reports that the wide layout draws its own panes.
func (v *issueView) frameless() bool { return v.wide() }

// renderWide draws the wide layout in exactly width×height.
func (v *issueView) renderWide(width, height int) string {
	lines := v.issueBand(width)
	top := len(lines)
	h := max(height-top, 4)
	detailsW := min(width*30/100, issueDetailsMaxW)
	mainW := width - detailsW
	v.paneW = mainW

	// The pane's border is the line above the strip and the column before its
	// padding, which is what a click is measured against.
	body := v.paneLines(max(mainW-4, 1), max(h-2, 2), top+1)
	main := drawBox("", "", body, mainW, h, edgesRounded, styleFocus)
	return strings.Join(append(lines, joinColumns(main, v.issueDetailsPane(detailsW, h))...), "\n")
}

// issueBand is the issue's name, its lane and what is true of it right now.
func (v *issueView) issueBand(width int) []string {
	inner := max(width-4, 1)
	if !v.loaded {
		body := []string{styleTitle.Render(v.title())}
		if v.loadErr == nil {
			body = append(body, styleDim.Render("reading…"))
		}
		return drawBox("", "", body, width, len(body)+2, edgesRounded, styleDim)
	}
	iss := v.issue
	pill := issueLanePill(issueSectionOf(iss))
	id := "#" + strconv.FormatInt(iss.ID, 10) + "  "
	room := max(inner-cols(id)-cols(pill)-2, 1)
	indent := strings.Repeat(" ", cols(id))

	var body []string
	for i, l := range wrapText(oneLine(iss.Title), room, wrapOpts{maxLines: issueBandTitleMax}) {
		if i == 0 {
			body = append(body, padBetweenCells(styleKey.Render(id)+styleTitle.Render(l), pill, inner))
			continue
		}
		body = append(body, indent+styleTitle.Render(l))
	}
	body = append(body, ansi.Truncate(issueBandFacts(iss, time.Now()), inner, "…"))
	return drawBox("", "", body, width, len(body)+2, edgesRounded, styleDim)
}

// issueBandFacts is the band's last line: the card's status first, because it is
// what most needs a person, then the kind, who filed the issue and when.
func issueBandFacts(iss apiclient.Issue, now time.Time) string {
	status, style := issueCardStatus(iss)
	parts := []string{style.Render(status)}
	if mark := issueKindMark(iss.Kind); mark != "" {
		parts = append(parts, mark)
	}
	if meta := previewMeta(iss, now); meta != "" {
		parts = append(parts, styleDim.Render(meta))
	}
	return strings.Join(parts, styleDim.Render("  ·  "))
}

// issueDetailsPane is the facts the narrow page lists above the description, and
// the Source section it ends on.
func (v *issueView) issueDetailsPane(w, h int) []string {
	title := styleTitle.Render("Details")
	if !v.loaded {
		return drawBox(title, "", nil, w, h, edgesRounded, styleDim)
	}
	iss := v.issue
	inner := max(w-4, 1)
	body := factLines("state", issueStateBadge(iss), inner)
	for _, f := range issueFacts(iss) {
		body = append(body, factLines(f[0], f[1], inner)...)
	}
	if iss.Source != nil {
		body = append(body, "", styleTitle.Render("Source"))
		for _, f := range issueSourceFacts(iss) {
			body = append(body, factLines(f[0], f[1], inner)...)
		}
	}
	if iss.Sync != nil {
		body = append(body, factLines("sync", issueSyncFact(iss.Sync), inner)...)
	}
	// The pane does not scroll: a cut says how much is missing rather than
	// passing for the whole list.
	if room := max(h-2, 1); len(body) > room {
		hidden := len(body) - (room - 1)
		body = append(body[:room-1:room-1], styleDim.Render("… "+plural(hidden, "more line", "more lines")))
	}
	return drawBox(title, "", body, w, h, edgesRounded, styleDim)
}

// issueFacts is what is true of every issue, in the order both layouts list it.
func issueFacts(iss apiclient.Issue) [][2]string {
	labels := "none"
	if len(iss.Labels) > 0 {
		labels = strings.Join(iss.Labels, ", ")
	}
	kind := iss.Kind
	if kind == "" {
		kind = "none"
	}
	return [][2]string{
		{"lane", issueLaneLabel(iss)},
		{"labels", labels},
		{"kind", kind},
		{"priority", issuePriorityLabel(iss.Priority)},
		{"author", iss.Author},
	}
}

// issueSourceFacts is where an imported issue came from, leaving out what the
// provider did not report.
func issueSourceFacts(iss apiclient.Issue) [][2]string {
	src := iss.Source
	var out [][2]string
	for _, f := range [][2]string{
		{"provider", src.Provider},
		{"issue", issueSourceBadge(iss)},
		{"url", src.URL},
		{"remote", src.RemoteState},
	} {
		if f[1] != "" {
			out = append(out, f)
		}
	}
	return out
}

// sectionHead is a page section's heading: with its icon in the wide layout, and
// indented to the frame's margin on the narrow page.
func (v *issueView) sectionHead(icon, name string) string {
	if v.wide() {
		return styleTitle.Render(ic(icon + " " + name))
	}
	return " " + styleTitle.Render(name)
}

// commentCard is one comment in the wide layout: its author and time in the top
// edge, its text inside. Called inside pageLines' render pass, so the body shares
// the description's cache.
func (v *issueView) commentCard(c *apiclient.IssueComment, remoteTag string, width int) []string {
	meta := c.CreatedAt.Local().Format("2006-01-02 15:04")
	if c.Remote {
		meta += " · " + remoteTag
	}
	if c.UpdatedAt.After(c.CreatedAt) {
		meta += " · edited"
	}
	md, _ := v.md.lines(c.Body, max(width-4, 10), levelNormal, v.raw.get(), v.links.get())
	return drawBox(styleKey.Render(c.Author), styleDim.Render(meta), md, width, len(md)+2, edgesRounded, styleDim)
}
