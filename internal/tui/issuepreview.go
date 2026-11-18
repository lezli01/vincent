package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issue preview (task 150.3): the selected card, in more detail, beside the
// board on a terminal wide enough to spare the room (task 150 decision 6).
//
// A list row carries everything but the description, so the row is what the
// pane draws and is always current; only the description is fetched. It is
// fetched once the selection has rested for issuePreviewDebounce, so holding an
// arrow key costs one read rather than one per card, and it is kept per issue
// until the issue's version moves.

const (
	// issuePreviewMinW is the narrowest terminal that gets a preview: below it
	// the four columns need the width more.
	issuePreviewMinW = 150
	// issuePreviewMaxW is the widest a preview grows; columns take the rest.
	issuePreviewMaxW = 56
	// issuePreviewDebounce is how long the selection must rest before the
	// description is read.
	issuePreviewDebounce = 150 * time.Millisecond
)

// issuePreview is a description as read at one version of its issue.
type issuePreview struct {
	version int64
	body    string
	err     string
}

// issuePreviewKey names what a read is for: an issue at a version.
type issuePreviewKey struct{ id, version int64 }

type (
	// issuePreviewDueMsg says the selection may have rested on key long enough.
	issuePreviewDueMsg struct{ key issuePreviewKey }
	// issuePreviewMsg is the description read for key.
	issuePreviewMsg struct {
		key  issuePreviewKey
		body string
		err  error
	}
)

// issuePreviewWidth is the pane's outer width on a terminal width wide.
func issuePreviewWidth(width int) int { return min(width*3/10, issuePreviewMaxW) }

// previewCmd arms the read of the selected issue's description, if the pane is
// up and does not already hold it or wait for it.
func (v *issuesView) previewCmd() tea.Cmd {
	if !v.previewOn || v.client == nil {
		return nil
	}
	row, ok := v.current()
	if !ok {
		return nil
	}
	key := issuePreviewKey{row.issue.ID, row.issue.Version}
	if p, held := v.previews[key.id]; (held && p.version == key.version) || v.previewArmed == key {
		return nil
	}
	v.previewArmed = key
	return tea.Tick(issuePreviewDebounce, func(time.Time) tea.Msg { return issuePreviewDueMsg{key} })
}

// previewDue reads the description if the selection is still where it was when
// the wait began. Having moved on, the wait for the new selection is running.
func (v *issuesView) previewDue(key issuePreviewKey) tea.Cmd {
	row, ok := v.current()
	if !ok || row.issue.ID != key.id || row.issue.Version != key.version || v.previewReading == key {
		return nil
	}
	client := v.client
	if client == nil {
		return nil
	}
	v.previewReading = key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, key.id, "")
		return issuePreviewMsg{key: key, body: iss.Body, err: err}
	}
}

// applyPreview keeps a read's answer. A failure is kept too, so a daemon that
// refuses is not asked again until the issue changes or `R` clears the slate.
func (v *issuesView) applyPreview(msg issuePreviewMsg) {
	if v.previewReading == msg.key {
		v.previewReading = issuePreviewKey{}
	}
	if v.previewArmed == msg.key {
		v.previewArmed = issuePreviewKey{}
	}
	if v.previews == nil {
		v.previews = map[int64]issuePreview{}
	}
	p := issuePreview{version: msg.key.version, body: sanitizeText(msg.body)}
	if msg.err != nil {
		p.err = errString(msg.err)
	}
	v.previews[msg.key.id] = p
}

// forgetPreviews drops every kept description, and any read under way.
func (v *issuesView) forgetPreviews() {
	v.previews = nil
	v.previewArmed, v.previewReading = issuePreviewKey{}, issuePreviewKey{}
}

// previewPane draws the pane in exactly w×h: the card's facts first, because
// they are short and always there, then as much of the description as fits.
func (v *issuesView) previewPane(w, h int) []string {
	row, ok := v.current()
	if !ok {
		body := []string{styleDim.Render("Select an issue to preview it.")}
		return drawBox(styleTitle.Render("Preview"), "", body, w, h, edgesRounded, styleDim)
	}
	iss := row.issue
	inner := max(w-4, 1)
	body := v.previewFacts(iss, row.section, inner)
	room := h - 2 - len(body)
	body = append(body, previewDescription(v.previewText(iss, inner), inner, room)...)
	return drawBox(styleKey.Render("#"+strconv.FormatInt(iss.ID, 10)), "", body, w, h, edgesRounded, styleDim)
}

// previewFacts is everything the pane knows from the list row.
func (v *issuesView) previewFacts(iss apiclient.Issue, section, inner int) []string {
	out := styled(wrapText(iss.Title, inner, wrapOpts{maxLines: 3}), styleTitle)

	look := issueLaneLooks[section]
	state := look.tone.Bold(true).Reverse(true).Render(" " + look.glyph + " " + issueLaneTitle(issueSections[section].label) + " ")
	if iss.Attention {
		state += "  " + styleWarn.Bold(true).Render(attentionBadge+" needs you")
	}
	out = append(out, "", state, "")
	if meta := previewMeta(iss, v.now()); meta != "" {
		out = append(out, styleDim.Render(meta))
	}
	if mark := issueKindMark(iss.Kind); mark != "" {
		out = append(out, mark)
	}
	if tags := issueCardTags(iss, inner, issuesShareRepo(v.issues)); tags != "" {
		out = append(out, tags)
	}

	status, style := issueCardStatus(iss)
	out = append(out, "", styleTitle.Render("▶ Work"))
	out = append(out, styled(wrapText(status, inner, wrapOpts{maxLines: 2}), style)...)
	if iss.MainWorktree != nil && iss.MainWorktree.Branch != "" {
		out = append(out, toneCyan.Render(iss.MainWorktree.Branch))
	}
	if d := iss.Delivery; d != nil && d.Summary != "" {
		out = append(out, styled(wrapText(oneLine(d.Summary), inner, wrapOpts{maxLines: 2}), styleDim)...)
	}
	return append(out, "")
}

// previewMeta says who filed the issue, when, and how urgent it is.
func previewMeta(iss apiclient.Issue, now time.Time) string {
	var parts []string
	if iss.Author != "" {
		parts = append(parts, "by "+oneLine(iss.Author))
	}
	if age := strings.TrimPrefix(issueAge(now, iss.CreatedAt), "◷ "); age == "now" {
		parts = append(parts, "opened just now")
	} else if age != "" {
		parts = append(parts, "opened "+age+" ago")
	}
	if iss.Priority > 0 {
		parts = append(parts, issuePriorityLabel(iss.Priority)+" priority")
	}
	return strings.Join(parts, " · ")
}

// previewText is the description's lines, or what stands in for them. An older
// version's text stays up while the new one is read, rather than flashing away.
func (v *issuesView) previewText(iss apiclient.Issue, inner int) []string {
	p, held := v.previews[iss.ID]
	switch {
	case !held:
		return []string{styleDim.Render("Loading…")}
	case p.err != "":
		return styled(wrapText("Could not read the description: "+p.err, inner, wrapOpts{}), styleWarn)
	case strings.TrimSpace(p.body) == "":
		return []string{styleDim.Render("No description.")}
	}
	return markdownLines(p.body, inner)
}

// previewDescription is the description under its heading, cut to room lines
// with a cue saying where the rest is.
func previewDescription(lines []string, inner, room int) []string {
	if room < 2 {
		return nil
	}
	out := []string{styleTitle.Render("≡ Description")}
	if len(lines) <= room-1 {
		return append(out, lines...)
	}
	cue := wrapText("… enter opens the full issue", inner, wrapOpts{maxLines: 1})
	if keep := room - 1 - len(cue); keep > 0 {
		out = append(out, lines[:keep]...)
	}
	return append(out, styled(cue, styleDim)...)
}

// joinColumns sets two blocks of equal height side by side.
func joinColumns(left, right []string) []string {
	out := make([]string, len(left))
	for i := range left {
		out[i] = left[i]
		if i < len(right) {
			out[i] += right[i]
		}
	}
	return out
}
