package tui

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The issue detail's `Issue | Work` strip (task 144, #933, §15 view 13).
// The Issue tab is the page as it was, with the linked tasks collapsed to
// one "N tasks, M chats" line; the Work tab lists every root task the issue
// started and every chat that concerns it — opened on the issue, linked to
// one of its tasks, or handed off into one — from the existing task listing
// and #931's GET /v1/chats?issue_id= (decision 3: client-only, unpaged).
//
// The strip is drawn and hit-tested the way the task workspace's is
// (taskview.go renderTabs/tabStrip), but is not shared with it: that strip
// is grouped, abbreviates and is digit-addressed, and this one has two tabs
// and no digit keys (decision 2), so a common helper would be all options.

// issueTab is the strip's selection.
type issueTab int

const (
	issueTabIssue issueTab = iota
	issueTabWork
	issueTabCount
)

func (t issueTab) String() string {
	if t == issueTabWork {
		return "Work"
	}
	return "Issue"
}

// issueTabHint is the strip's one statement of how to move.
const issueTabHint = "tab/⇧tab or ]/["

// issueTabHit is where a tab landed on the strip, in screen columns.
type issueTabHit struct {
	tab    issueTab
	x0, x1 int
}

// renderStrip draws the strip, with the hint when it fits, and records the
// tabs' columns for the mouse.
func (v *issueView) renderStrip(width int) string {
	var b strings.Builder
	b.WriteString("  ")
	x := 3 // root frame border plus the two-cell indent, as the workspace's strip
	v.tabHits = v.tabHits[:0]
	for t := range issueTabCount {
		if t > 0 {
			b.WriteString(styleDim.Render(tabSepPrimary))
			x += ansi.StringWidth(tabSepPrimary)
		}
		name := t.String()
		if t == v.tab {
			b.WriteString(styleTitle.Render(name))
		} else {
			b.WriteString(name)
		}
		w := ansi.StringWidth(name)
		v.tabHits = append(v.tabHits, issueTabHit{tab: t, x0: x, x1: x + w})
		x += w
	}
	line := b.String()
	if hint := styleDim.Render("  " + issueTabHint); ansi.StringWidth(line+hint) <= width {
		line += hint
	}
	return line
}

// setTab moves the strip, keeping each tab's own place: the two pages have
// different rows, so a cursor carried across would land on an arbitrary one.
func (v *issueView) setTab(t issueTab) {
	if t == v.tab || t < 0 || t >= issueTabCount {
		return
	}
	v.tabPos[v.tab] = [2]int{v.cursor, v.scroll}
	v.tab = t
	v.cursor, v.scroll = v.tabPos[t][0], v.tabPos[t][1]
	v.cursor = min(v.cursor, max(len(v.selectRows())-1, 0))
}

// cycleTab is tab/] (+1) and shift+tab/[ (−1), wrapping.
func (v *issueView) cycleTab(delta int) {
	n := int(issueTabCount)
	v.setTab(issueTab(((int(v.tab)+delta)%n + n) % n))
}

// issueTabKey is the strip's keys. The form and the prompt own the keyboard
// before update ever reaches here.
func issueTabKey(key string) (int, bool) {
	switch key {
	case "tab", "]":
		return 1, true
	case "shift+tab", "[":
		return -1, true
	}
	return 0, false
}

// clickStrip selects the tab under the pointer on the strip's line.
func (v *issueView) clickStrip(x int) {
	for _, hit := range v.tabHits {
		if x >= hit.x0 && x < hit.x1 {
			v.setTab(hit.tab)
			return
		}
	}
}

// workChats is the Work tab's chats, newest first.
func (v *issueView) workChats() []apiclient.Chat {
	out := slices.Clone(v.chats)
	slices.SortStableFunc(out, func(a, b apiclient.Chat) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out
}

// listsChat reports whether the Work tab lists chat id.
func (v *issueView) listsChat(id int64) bool {
	return slices.ContainsFunc(v.chats, func(c apiclient.Chat) bool { return c.ID == id })
}

// chatEventChatID is the chat a chat.* event is about: its payload's id.
func chatEventChatID(ev apiclient.Event) int64 {
	if !strings.HasPrefix(ev.Type, "chat.") || len(ev.Payload) == 0 {
		return 0
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(ev.Payload, &body) != nil {
		return 0
	}
	return body.ID
}

// chatEventConcerns reports whether a chat event moves the Work tab: a chat
// it lists, or one linked to a task it lists — a chat newly opened on one of
// the issue's tasks is not listed yet, but its task is.
func (v *issueView) chatEventConcerns(ev apiclient.Event) bool {
	if id := chatEventChatID(ev); id != 0 && v.listsChat(id) {
		return true
	}
	if tid, ok := linkedTaskOf(ev); ok && v.linksTask(tid) {
		return true
	}
	return false
}

// chatStateGlyph is a chat state in the step-state glyph vocabulary
// (glyphs.go): `●` running, `!` waiting on you, `·` idle, `✓` handed off —
// its work went on in a task — and `■` closed or archived.
func chatStateGlyph(state string) string {
	switch state {
	case "running":
		return "●"
	case "awaiting_input":
		return attentionBadge
	case "handed_off":
		return "✓"
	case "closed", "archived":
		return "■"
	}
	return "·"
}

// workChatTail is a chat row's dim tail: where it was opened, then its
// state, a hand-off naming the task it went to.
func (v *issueView) workChatTail(c apiclient.Chat) string {
	var parts []string
	switch {
	case c.LinkedTaskID != nil:
		parts = append(parts, "on task #"+strconv.FormatInt(*c.LinkedTaskID, 10))
	case c.IssueID != nil:
		parts = append(parts, "on the issue")
	}
	state := chatStateLabel(c.State)
	if c.State == "handed_off" && c.HandoffTaskID != nil {
		state += " → #" + strconv.FormatInt(*c.HandoffTaskID, 10)
	}
	return strings.Join(append(parts, state), " · ")
}

// workSummary is the Issue tab's one line for the Work tab: what it holds.
func (v *issueView) workSummary() string {
	out := plural(v.issue.Tasks.Count, "task", "tasks")
	if a := len(v.issue.Tasks.ActiveIDs); a > 0 {
		out += " (" + strconv.Itoa(a) + " active)"
	}
	if v.chatsErr != nil {
		out += ", chats unavailable"
	} else {
		out += ", " + plural(len(v.chats), "chat", "chats")
	}
	return out
}

// issueTabLines is the Issue tab's Work section: the one selectable line
// enter on which switches to the Work tab (decision 1).
func (v *issueView) issueTabWorkLines(rows []issueDetailRow, width, base int) []string {
	lines := []string{"", " " + styleTitle.Render("Work")}
	for i, row := range rows {
		if !row.workLine {
			continue
		}
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, base+len(lines))
		lines = append(lines, ansi.Truncate(marker+v.workSummary()+styleDim.Render(" — tab for the work"), width, "…"))
	}
	if len(v.tasks) == 0 && len(v.chats) == 0 {
		lines = append(lines, styleDim.Render("  no task yet · "+opKey(keymap.Add)+" starts one, "+
			opKey(keymap.Chat)+" talks it through in a chat"))
	}
	return lines
}

// workTabLines is the Work tab: the Tasks group, then the Chats group, each
// newest first and each saying so when it is empty.
func (v *issueView) workTabLines(rows []issueDetailRow, width, base int) []string {
	iss := v.issue
	lines := []string{"", " " + styleTitle.Render("Tasks") +
		styleDim.Render("  "+plural(iss.Tasks.Count, "task", "tasks")+", "+
			strconv.Itoa(len(iss.Tasks.ActiveIDs))+" active")}
	if len(v.tasks) == 0 {
		lines = append(lines, styleDim.Render("  no task yet · "+opKey(keymap.Add)+" starts one"))
	}
	for i, row := range rows {
		if row.chat != nil || row.task.ID == 0 {
			continue
		}
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, base+len(lines))
		lines = append(lines, issueTaskLine(row, marker, width))
	}

	head := " " + styleTitle.Render("Chats")
	if v.chatsErr == nil {
		head += styleDim.Render("  " + plural(len(v.chats), "chat", "chats"))
	}
	lines = append(lines, "", head)
	switch {
	case v.chatsErr != nil:
		lines = append(lines, styleDim.Render("  chats unavailable"))
	case len(v.chats) == 0:
		lines = append(lines, styleDim.Render("  no chat yet · "+opKey(keymap.Chat)+" opens one on the issue"))
	}
	for i, row := range rows {
		c := row.chat
		if c == nil {
			continue
		}
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, base+len(lines))
		tail := v.workChatTail(*c)
		lead := marker + applyStateStyle(c.State, chatStateGlyph(c.State)) + " " +
			styleKey.Render("chat #"+strconv.FormatInt(c.ID, 10)) + "  "
		title := fitTitle(c.Title, width-cols(lead)-2-cols(tail))
		lines = append(lines, lead+title+"  "+styleDim.Render(tail))
	}
	return lines
}

// issueTaskLine is one linked task's row: its glyph, id, title and a dim
// tail of role, state, `archived` and a fan-out parent's `lanes n/m`.
func issueTaskLine(row issueDetailRow, marker string, width int) string {
	t := row.task
	if row.child {
		marker += "  └ "
	}
	glyph := taskStateGlyph(t.State)
	if glyph == "" {
		glyph = "·"
	}
	state := t.State
	// An archived task's state already says so; the suffix is for a row
	// whose state is something else (review F3: `archived · archived`).
	if t.ArchivedAt != nil && t.State != stateArchived {
		state += " · archived"
	}
	if role := issueTaskRole(t); role != "" {
		state = role + " · " + state
	}
	if r := t.Children; r != nil && r.Total > 0 {
		state += " · lanes " + strconv.Itoa(r.Settled) + "/" + strconv.Itoa(r.Total)
	}
	lead := marker + stateStyles[t.State].Render(glyph) + " " +
		styleKey.Render("#"+strconv.FormatInt(t.ID, 10)) + "  "
	title := fitTitle(t.Title, width-cols(lead)-2-cols(state))
	return lead + title + "  " + styleDim.Render(state)
}
