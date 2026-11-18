package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/keymap"
)

// The chats workspace (task 150.5, decision 7): the live chats board and the
// open conversation as one screen. Chat cards sit on the left, grouped by what
// each chat needs from you; the conversation is in the middle; the chat's
// details are on the right.
//
// The board previews the selected chat from one read of it. `enter` moves into
// the live conversation, which chatView draws between the same two panes, so
// opening a chat changes where the keyboard is rather than what is on screen.
// The archived board keeps its table until task 150.7.

const (
	// chatWorkspaceMinW is the narrowest terminal that shows a conversation
	// beside the list. Below it the list has the screen, and enter opens the
	// conversation over it.
	chatWorkspaceMinW = 100
	// chatDetailsMinW is the narrowest terminal that also shows the details.
	chatDetailsMinW = 150
	// chatCardH is a chat card's outer height.
	chatCardH = 6
	// chatPreviewTurns is how many of the latest turns the preview draws.
	chatPreviewTurns = 6
	// chatPreviewDebounce is how long the selection must rest before the
	// chat is read, so holding an arrow key costs one read.
	chatPreviewDebounce = 150 * time.Millisecond
)

// chatPaneWidths is the list's and the details' outer widths on a terminal
// width wide; the conversation has the rest.
func chatPaneWidths(width int) (list, details int) {
	if width < chatWorkspaceMinW {
		return width, 0
	}
	list = min(max(width*22/100, 32), 46)
	if width >= chatDetailsMinW {
		details = min(width*18/100, 38)
	}
	return list, details
}

// chatCardHit is where the last render drew a chat card, for a click.
type chatCardHit struct {
	x0, x1, y0, y1 int
	id             int64
}

// chatPreview is one read of a chat, at the row's updated_at when it was read.
type chatPreview struct {
	updated time.Time
	chat    apiclient.Chat
	turns   []apiclient.ChatTurn
	err     string
}

// chatPreviewKey names what a read is for: a chat as of a change.
type chatPreviewKey struct {
	id      int64
	updated int64
}

type (
	// chatPreviewDueMsg says the selection may have rested on key long enough.
	chatPreviewDueMsg struct{ key chatPreviewKey }
	// chatPreviewMsg is the chat read for key.
	chatPreviewMsg struct {
		key     chatPreviewKey
		updated time.Time
		chat    *apiclient.Chat
		turns   []apiclient.ChatTurn
		err     error
	}
)

// frameless reports that the live board draws its own panes, so the root must
// not wrap it in another frame. The new-chat form and the archive keep theirs.
func (v *chatsView) frameless() bool { return v.workspaceShown() && v.create == nil }

// workspaceShown reports whether the board is drawn as the workspace: always
// for the live board, and on a wide terminal for the archive.
func (v *chatsView) workspaceShown() bool { return !v.archived || v.termW >= mgmtWideMinW }

// previewCmd arms the read of the selected chat, if the conversation is on
// screen and does not already hold that chat as of its latest change.
func (v *chatsView) previewCmd() tea.Cmd {
	if !v.workspaceShown() || !v.previewOn || v.client == nil {
		return nil
	}
	c, ok := v.current()
	if !ok {
		return nil
	}
	key := chatPreviewKey{c.ID, c.UpdatedAt.UnixNano()}
	if p, held := v.previews[c.ID]; (held && p.updated.Equal(c.UpdatedAt)) || v.previewArmed == key {
		return nil
	}
	v.previewArmed = key
	return tea.Tick(chatPreviewDebounce, func(time.Time) tea.Msg { return chatPreviewDueMsg{key} })
}

// previewDue reads the chat if the selection is still where it was.
func (v *chatsView) previewDue(key chatPreviewKey) tea.Cmd {
	c, ok := v.current()
	if !ok || c.ID != key.id || c.UpdatedAt.UnixNano() != key.updated || v.previewReading == key || v.client == nil {
		return nil
	}
	client, updated := v.client, c.UpdatedAt
	v.previewReading = key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		chat, turns, err := client.GetChat(ctx, key.id)
		return chatPreviewMsg{key: key, updated: updated, chat: chat, turns: turns, err: err}
	}
}

// applyPreview keeps a read's answer, sanitized: a prompt and an answer are
// text an agent or a paste wrote, and an escape in one must not reach the
// terminal. A failure is kept too, so it is not retried until the chat changes.
func (v *chatsView) applyPreview(msg chatPreviewMsg) {
	if v.previewReading == msg.key {
		v.previewReading = chatPreviewKey{}
	}
	if v.previewArmed == msg.key {
		v.previewArmed = chatPreviewKey{}
	}
	if v.previews == nil {
		v.previews = map[int64]chatPreview{}
	}
	p := chatPreview{updated: msg.updated}
	if msg.err != nil {
		p.err = errString(msg.err)
		v.previews[msg.key.id] = p
		return
	}
	if msg.chat != nil {
		p.chat = *msg.chat
	}
	p.turns = make([]apiclient.ChatTurn, len(msg.turns))
	for i, t := range msg.turns {
		t.Prompt, t.ResultText, t.ErrorMessage = sanitizeText(t.Prompt), sanitizeText(t.ResultText), sanitizeText(t.ErrorMessage)
		p.turns[i] = t
	}
	v.previews[msg.key.id] = p
}

// forgetPreviews drops every kept read, and any read under way.
func (v *chatsView) forgetPreviews() {
	v.previews = nil
	v.previewArmed, v.previewReading = chatPreviewKey{}, chatPreviewKey{}
}

// renderWorkspace is the live board: the filter, the panes, and any question
// or note under them.
func (v *chatsView) renderWorkspace(width, height int) string {
	v.previewOn = width >= chatWorkspaceMinW
	lines := make([]string, 0, height)
	lines = append(lines, v.chatsFilterBox(width)...)
	footer := v.footerLines(width)
	room := height - len(lines) - len(footer)
	v.cardHits = nil
	if room >= 3 {
		lines = append(lines, v.workspacePanes(width, room, len(lines))...)
	}
	lines = append(lines, footer...)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// workspacePanes lays the three panes side by side in exactly height lines.
func (v *chatsView) workspacePanes(width, height, top int) []string {
	if v.project.id == 0 {
		// Nothing to list and nothing to read: the reason has the whole width.
		text := "Resolving the project…"
		if v.noProjects {
			text = noProjectsEmpty()
		}
		body := styled(wrapText(text, max(width-4, 1), wrapOpts{}), styleDim)
		return drawBox(toneBlue.Bold(true).Render("◌ Chats"), "", body, width, height, edgesRounded, styleDim)
	}
	listW, detailsW := chatPaneWidths(width)
	out := v.chatListPane(listW, height, top)
	if width < chatWorkspaceMinW {
		return out
	}
	c, ok := v.current()
	var p *chatPreview
	if ok {
		if held, have := v.previews[c.ID]; have {
			p = &held
		}
	}
	out = joinColumns(out, chatPreviewPane(c, ok, p, width-listW-detailsW, height))
	if detailsW > 0 {
		var turns []apiclient.ChatTurn
		if p != nil {
			turns = p.turns
		}
		out = joinColumns(out, chatDetailsPane(c, ok, turns, v.now(), detailsW, height))
	}
	return out
}

// chatsFilterBox is the rounded box above the panes.
func (v *chatsView) chatsFilterBox(width int) []string {
	body := []string{toneBlue.Render("⌕ ") + styleDim.Render(v.filter.Placeholder())}
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-6, 10))
		body = fieldRows("", v.filter)
	}
	title := ""
	if v.archived {
		title = archiveSwitch(false)
	}
	return drawBox(title, v.chatsSummary(), body, width, len(body)+2, edgesRounded, styleDim)
}

// chatsSummary says what the board is showing, for the filter box's edge.
func (v *chatsView) chatsSummary() string {
	if v.archived {
		return styleDim.Render(v.label() + " · " + plural(len(v.chats), "chat", "chats"))
	}
	var parts []string
	switch {
	case v.loadErr != "":
		parts = append(parts, oneLine(v.loadErr))
	case v.project.id == 0:
		parts = append(parts, "no project selected")
	case !v.loaded && v.loading:
		// The project's name, not a bare "loading…": after a switch this is
		// the one line saying which project the empty board is waiting on.
		parts = append(parts, "loading "+v.project.name+"…")
	default:
		parts = append(parts, plural(len(v.chats), "chat", "chats"))
		// Which listing is on, named only when it is not the default: a board
		// hiding terminal chats must say so once `s` has been pressed, or an
		// empty board reads as no chats at all (issue #298).
		if v.scope != apiclient.ArchivedExclude {
			parts = append(parts, chatScopeLabel(v.scope))
		}
	}
	if !v.lastLoad.IsZero() {
		parts = append(parts, "updated "+v.lastLoad.Format("15:04:05"))
	}
	out := styleDim.Render(strings.Join(parts, " · "))
	if n := countChatsAwaiting(v.chats); n > 0 && v.loadErr == "" {
		out = styleWarn.Render(strconv.Itoa(n)+" waiting on you") + styleDim.Render(" · ") + out
	}
	return out
}

// chatGroupLook is one group's heading on the list.
type chatGroupLook struct {
	glyph, name string
	tone        lipgloss.Style
}

// chatGroupLooks is indexed by chatBand.
var chatGroupLooks = []chatGroupLook{
	{attentionBadge, "Waiting on you", styleWarn},
	{"▶", "Running", toneCyan},
	{"○", "Idle", styleDim},
	{"✓", "Ended", styleDim},
}

// chatListPane draws the cards in their groups, scrolled to keep the selected
// card on screen. top is the view line the pane starts on.
func (v *chatsView) chatListPane(w, h, top int) []string {
	title := toneBlue.Bold(true).Render("◌ Chats")
	if v.archived {
		title = toneBlue.Bold(true).Render("▣ Archived chats")
	}
	right := toneBlue.Render(strconv.Itoa(len(v.rows())))
	inner := max(w-4, 1)
	rows := v.rows()
	if len(rows) == 0 {
		text := "…"
		if v.loaded {
			text = "No chats. Press " + opKey(keymap.New) + " to start one."
		}
		return drawBox(title, right, styled(wrapText(text, inner, wrapOpts{}), styleDim), w, h, edgesRounded, styleDim)
	}

	counts := map[int]int{}
	for _, r := range rows {
		counts[chatBand(r.chat.State)]++
	}
	var body []string
	type span struct {
		from int
		id   int64
	}
	spans := make([]span, 0, len(rows))
	from, to, band := 0, 0, -1
	for i, r := range rows {
		if b := chatBand(r.chat.State); b != band {
			if band >= 0 {
				body = append(body, "")
			}
			band = b
			look := chatGroupLooks[b]
			body = append(body, look.tone.Bold(true).Render(look.glyph+" "+look.name)+" "+styleDim.Render(strconv.Itoa(counts[b])))
		}
		if i == v.cursor {
			from, to = len(body), len(body)+chatCardH
		}
		spans = append(spans, span{len(body), r.chat.ID})
		body = append(body, v.chatCard(*r.chat, i == v.cursor, inner)...)
	}
	room := max(h-2, 1)
	start := 0
	if len(body) > room {
		start = min(max(to-room, min(from, max(from-1, 0))), len(body)-room)
		if from < start {
			start = from
		}
	}
	v.cardHits = v.cardHits[:0]
	for _, s := range spans {
		y := top + 1 + s.from - start
		if s.from >= start && s.from+chatCardH <= start+room {
			v.cardHits = append(v.cardHits, chatCardHit{x0: 2, x1: 2 + inner, y0: y, y1: y + chatCardH, id: s.id})
		}
	}
	return drawBox(title, right, body[start:], w, h, edgesRounded, styleDim)
}

// chatCard draws one chat: id, agent and age; two lines of title; its state and
// what it is linked to.
func (v *chatsView) chatCard(c apiclient.Chat, selected bool, w int) []string {
	inner := max(w-4, 1)
	titleStyle := lipgloss.NewStyle()
	edges, border := edgesRounded, styleDim
	if selected {
		titleStyle, edges, border = styleTitle, edgesThick, styleFocus
	}
	head := styleKey.Render("#"+strconv.FormatInt(c.ID, 10)) + "  " + toneMagenta.Render("◈ "+oneLine(c.Agent))
	body := []string{padBetweenCells(head, styleDim.Render(chatCardAge(c, v.now())), inner)}
	title := wrapText(oneLine(c.Title), inner, wrapOpts{maxLines: 2})
	for i := range 2 {
		line := ""
		if i < len(title) {
			line = titleStyle.Render(title[i])
		}
		body = append(body, line)
	}
	status, style := chatCardStatus(c, v.frame)
	body = append(body, padBetweenCells(style.Render(status), toneCyan.Render(chatLinkMark(c)), inner))
	return drawBox("", "", body, w, chatCardH, edges, border)
}

// chatCardAge is how long ago a live chat last changed, or when an ended one
// ended: a duration that keeps growing reads as a running conversation (#298).
func chatCardAge(c apiclient.Chat, now time.Time) string {
	if chatstate.Terminal(chatstate.State(c.State)) {
		return chatActivity(c, now)
	}
	return issueAge(now, c.UpdatedAt)
}

// chatCardStatus is a card's last line, led by a glyph so it survives NO_COLOR.
func chatCardStatus(c apiclient.Chat, frame int) (string, lipgloss.Style) {
	switch c.State {
	case "awaiting_input":
		return attentionBadge + " has a question", styleWarn.Bold(true)
	case "running":
		return "▶ working " + spinnerFrame(frame), toneCyan
	case "idle":
		return "○ idle", styleDim
	}
	return "✓ " + chatStateLabel(c.State), styleDim
}

// chatLinkMark names what a chat was opened on, "" for a free chat.
func chatLinkMark(c apiclient.Chat) string {
	switch {
	case c.IssueID != nil:
		return "▦ #" + strconv.FormatInt(*c.IssueID, 10)
	case c.LinkedTaskID != nil:
		return "▶ #" + strconv.FormatInt(*c.LinkedTaskID, 10)
	}
	return ""
}

// chatStatePill is a chat's state, set into the conversation's top edge.
func chatStatePill(state string) string {
	style := styleDim
	switch state {
	case "awaiting_input":
		style = styleWarn
	case "running":
		style = toneCyan
	}
	return style.Bold(true).Reverse(true).Render(" " + chatStateLabel(state) + " ")
}

// chatPreviewPane is the selected chat's latest turns, newest at the bottom,
// with an agent's question pinned under them.
func chatPreviewPane(c apiclient.Chat, ok bool, p *chatPreview, w, h int) []string {
	if !ok {
		body := []string{styleDim.Render("Select a chat to read it here.")}
		return drawBox(styleTitle.Render("Conversation"), "", body, w, h, edgesRounded, styleDim)
	}
	inner := max(w-4, 1)
	room := max(h-2, 1)
	foot := chatPreviewFoot(c, inner)
	lines := chatPreviewLines(c, p, inner)
	if keep := room - len(foot); len(lines) > keep {
		lines = lines[max(len(lines)-keep, 0):]
	}
	body := make([]string, 0, room)
	body = append(body, lines...)
	for len(body)+len(foot) < room {
		body = append(body, "")
	}
	body = append(body, foot...)
	title := styleKey.Render("#"+strconv.FormatInt(c.ID, 10)) + " " + styleTitle.Render(oneLine(c.Title))
	return drawBox(title, chatStatePill(c.State), body, w, h, edgesRounded, styleDim)
}

// chatPreviewLines is the conversation as one read saw it.
func chatPreviewLines(c apiclient.Chat, p *chatPreview, inner int) []string {
	switch {
	case p == nil:
		return []string{styleDim.Render("Loading…")}
	case p.err != "":
		return styled(wrapText("Could not read this chat: "+p.err, inner, wrapOpts{}), styleWarn)
	case len(p.turns) == 0:
		return []string{styleDim.Render("Nothing said yet.")}
	}
	var out []string
	for _, t := range p.turns[max(len(p.turns)-chatPreviewTurns, 0):] {
		out = append(out, promptBubbleLines(t.Prompt, inner)...)
		out = append(out, "", toneMagenta.Bold(true).Render("◈ "+oneLine(c.Agent))+styleDim.Render(fmt.Sprintf("  ·  turn %d", t.Seq)))
		if t.ResultText != "" {
			out = append(out, markdownLines(t.ResultText, inner)...)
		}
		if t.FailReason != "" {
			text := t.FailReason
			if t.ErrorMessage != "" {
				text += ": " + t.ErrorMessage
			}
			out = append(out, styled(wrapText(text, inner, wrapOpts{}), styleBad)...)
		}
		out = append(out, "")
	}
	return out
}

// chatPreviewFoot is what is pinned under the conversation: the question being
// asked, or what enter does.
func chatPreviewFoot(c apiclient.Chat, inner int) []string {
	if c.State == "awaiting_input" {
		if card := chatQuestionCard(c, inner); card != nil {
			return card
		}
	}
	hint := "enter opens this chat"
	switch c.State {
	case "running":
		hint = "▶ working · enter follows it live"
	case "idle":
		hint = "enter continues this chat"
	}
	return []string{styleDim.Render(hint)}
}

// chatQuestionCard is an agent's pending question as a thick card, nil when the
// chat carries none that can be read.
func chatQuestionCard(c apiclient.Chat, w int) []string {
	req, ok, err := c.PendingRequest()
	if err != nil || !ok {
		return nil
	}
	inner := max(w-4, 1)
	var body []string
	for _, q := range req.Questions {
		body = append(body, styled(wrapText(sanitizeText(q.Text), inner, wrapOpts{maxLines: 3}), styleTitle)...)
		for _, o := range q.Options {
			body = append(body, ansi.Truncate("○ "+oneLine(o), inner, "…"))
		}
	}
	if req.Permission != nil {
		body = append(body, styleTitle.Render("Permission requested"))
	}
	body = append(body, "", styleDim.Render("enter to answer"))
	return drawBox(styleWarn.Bold(true).Render(chatAskingTitle(c.Agent)), "", body, w, len(body)+2, edgesThick, styleWarn)
}

// chatAskingTitle heads a question card.
func chatAskingTitle(agent string) string {
	return attentionBadge + " " + oneLine(agent) + " is asking"
}

// chatDetailsPane is what a chat is running with and what it has cost.
func chatDetailsPane(c apiclient.Chat, ok bool, turns []apiclient.ChatTurn, now time.Time, w, h int) []string {
	title := styleTitle.Render("Details")
	if !ok {
		return drawBox(title, "", nil, w, h, edgesRounded, styleDim)
	}
	inner := max(w-4, 1)
	row := func(k, val string) string { return padBetweenCells(styleDim.Render(k), oneLine(val), inner) }

	body := []string{styleTitle.Render("◈ Agent"), row("agent", c.Agent)}
	if c.Model != "" {
		body = append(body, row("model", c.Model))
	}
	if c.Effort != "" {
		body = append(body, row("effort", c.Effort))
	}
	if c.Branch != "" {
		body = append(body, "", styleTitle.Render("⎇ Branch"), toneCyan.Render(ansi.Truncate(oneLine(c.Branch), inner, "…")))
		if c.BaseBranch != "" {
			body = append(body, row("base", c.BaseBranch))
		}
	}
	body = append(body, "", styleTitle.Render("◷ Activity"), row("turns", strconv.Itoa(len(turns))))
	tokens, cost, priced := chatUsage(turns)
	if tokens > 0 {
		body = append(body, row("tokens", chatTokens(tokens)))
	}
	if priced {
		body = append(body, row("cost", fmt.Sprintf("$%.2f", cost)))
	}
	if age := strings.TrimPrefix(issueAge(now, c.CreatedAt), "◷ "); age != "" {
		if age != "now" {
			age += " ago"
		}
		body = append(body, row("started", age))
	}
	body = append(body, "", styleTitle.Render("▦ Linked"))
	links := chatLinks(c)
	if len(links) == 0 {
		links = []string{styleDim.Render("no issue or task")}
	}
	body = append(body, links...)
	return drawBox(title, "", body, w, h, edgesRounded, styleDim)
}

// chatUsage adds up a conversation's tokens and cost. priced is false when no
// turn reported a cost, which is not the same as costing nothing.
func chatUsage(turns []apiclient.ChatTurn) (tokens int64, cost float64, priced bool) {
	for _, t := range turns {
		tokens += t.InputTokens + t.OutputTokens
		if t.CostUSD != nil {
			cost += *t.CostUSD
			priced = true
		}
	}
	return tokens, cost, priced
}

// chatTokens is a token count in the fewest cells.
func chatTokens(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// chatLinks names everything a chat is tied to.
func chatLinks(c apiclient.Chat) []string {
	var out []string
	if c.IssueID != nil {
		out = append(out, "issue #"+strconv.FormatInt(*c.IssueID, 10))
	}
	if c.LinkedTaskID != nil {
		out = append(out, "task #"+strconv.FormatInt(*c.LinkedTaskID, 10))
	}
	if c.HandoffTaskID != nil {
		out = append(out, "handed off to task #"+strconv.FormatInt(*c.HandoffTaskID, 10))
	}
	return out
}

// updateClick selects the chat card under the pointer.
func (v *chatsView) updateClick(msg tea.MouseClickMsg) {
	if id, ok := v.cardAt(msg); ok && v.create == nil && v.confirm == nil && v.delPrompt == nil {
		v.selectedID = id
		v.restoreSelection()
	}
}

// cardAt is the chat card under a left click.
func (v *chatsView) cardAt(msg tea.MouseClickMsg) (int64, bool) {
	if msg.Button != tea.MouseLeft {
		return 0, false
	}
	for _, h := range v.cardHits {
		if msg.X >= h.x0 && msg.X < h.x1 && msg.Y >= h.y0 && msg.Y < h.y1 {
			return h.id, true
		}
	}
	return 0, false
}

// The open conversation, between the workspace's two side panes.

// sideShown reports whether the conversation sits in the workspace. A chat
// opened from somewhere else — an issue, the archive — has its own way back and
// keeps the whole screen.
func (v *chatView) sideShown(width int) bool {
	return v.side != nil && width >= chatWorkspaceMinW && (v.back == 0 || v.back == viewChats)
}

// frameless reports that the workspace draws its own panes.
func (v *chatView) frameless() bool { return v.sideShown(v.termW) }

// renderWorkspace is the conversation with the list on its left and the
// details on its right. Its frame is the focus colour: this is the pane the
// keyboard is in.
func (v *chatView) renderWorkspace(width, height int) string {
	listW, detailsW := chatPaneWidths(width)
	v.side.selectedID = v.chatID
	v.side.restoreSelection()
	out := v.side.chatListPane(listW, height, 0)

	midW := width - listW - detailsW
	body := strings.Split(v.renderConversation(max(midW-4, 4), max(height-2, 4)), "\n")
	out = joinColumns(out, drawBox("", "", body, midW, height, edgesRounded, styleFocus))
	if detailsW > 0 {
		var c apiclient.Chat
		ok := v.chat != nil
		if ok {
			c = *v.chat
		}
		out = joinColumns(out, chatDetailsPane(c, ok, v.turns, v.now(), detailsW, height))
	}
	return strings.Join(out, "\n")
}

// updateClick opens the chat card under the pointer from inside a conversation.
func (v *chatView) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if !v.sideShown(v.termW) || v.form != nil {
		return nil
	}
	id, ok := v.side.cardAt(msg)
	if !ok || id == v.chatID {
		return nil
	}
	pid := v.side.project.id
	return func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewChats} }
}
