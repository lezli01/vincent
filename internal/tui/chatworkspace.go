package tui

import (
	"context"
	"fmt"
	"slices"
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
	// chatPreviewToolTurns is how many of those show their tool activity, and
	// chatPreviewToolTail how much of each transcript is read for it.
	chatPreviewToolTurns = 3
	chatPreviewToolTail  = 64 << 10
	// chatFooterSummaryMinW is the narrowest terminal whose status bar carries
	// the board's summary. Narrower ones keep it on the filter box's edge.
	chatFooterSummaryMinW = 150
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
	// tools is the latest turns' tool activity by seq. A transcript that cannot
	// be read, or has gone to retention, leaves its turn without check lines.
	tools map[int][]apiclient.TranscriptRecord
	// stat is the Details pane's `changes` row (task 169 decision 10), read
	// with the preview and so once per change of the chat, never per event.
	stat *apiclient.ChatDiffStat
	err  string
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
		tools   map[int][]apiclient.TranscriptRecord
		// stat is the chat's changes since its base (task 169 decision 10),
		// nil when the daemon has none to give.
		stat *apiclient.ChatDiffStat
	}
)

// frameless reports that the live board draws its own panes, so the root must
// not wrap it in another frame. The new-chat form and the archive keep theirs.
func (v *chatsView) frameless() bool {
	return v.createWide() || (v.workspaceShown() && v.create == nil)
}

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
		msg := chatPreviewMsg{key: key, updated: updated, chat: chat, turns: turns, err: err}
		if err == nil {
			msg.tools = readPreviewTools(ctx, client, key.id, turns)
			// A chat with no directory to diff answers 409; that is a chat
			// with no `changes` row, not a preview that failed.
			if stat, serr := client.ChatDiffStat(ctx, key.id); serr == nil {
				msg.stat = stat
			}
		}
		return msg
	}
}

// readPreviewTools reads the tool activity of the latest turns, the ones the
// pane has room for. Each read asks for the transcript's tail only.
func readPreviewTools(ctx context.Context, client *apiclient.Client, id int64, turns []apiclient.ChatTurn) map[int][]apiclient.TranscriptRecord {
	tools := map[int][]apiclient.TranscriptRecord{}
	for _, t := range turns[max(len(turns)-chatPreviewToolTurns, 0):] {
		records, _, err := client.ChatTurnTranscript(ctx, id, t.Seq, apiclient.TranscriptOptions{Tail: chatPreviewToolTail})
		if err != nil {
			continue
		}
		for _, rec := range records {
			if rec.Type == "agent.tool_use" || rec.Type == "agent.tool_result" {
				tools[t.Seq] = append(tools[t.Seq], sanitizeToolRecord(rec))
			}
		}
	}
	return tools
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
	p := chatPreview{updated: msg.updated, tools: msg.tools, stat: msg.stat}
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
		return drawBox(toneBlue.Bold(true).Render(ic("◌ Chats")), "", body, width, height, edgesRounded, styleDim)
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
	out = joinColumns(out, chatPreviewPane(c, ok, p, v.now(), width-listW-detailsW, height))
	if detailsW > 0 {
		var turns []apiclient.ChatTurn
		// No key on the board: the diff opens from the conversation.
		var changes chatChanges
		if p != nil {
			turns, changes.stat = p.turns, p.stat
		}
		out = joinColumns(out, chatDetailsPane(c, ok, turns, changes, v.now(), detailsW, height))
	}
	return out
}

// chatsFilterBox is the rounded box above the panes.
func (v *chatsView) chatsFilterBox(width int) []string {
	body := []string{toneBlue.Render(ic("⌕ ")) + styleDim.Render(v.filter.Placeholder())}
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-6, 10))
		body = fieldRows("", v.filter)
	}
	title, summary := "", ""
	if v.archived {
		title = archiveSwitches(false, v.window)
	}
	if v.archived || width < chatFooterSummaryMinW {
		summary = v.chatsSummary()
	}
	return drawBox(title, summary, body, width, len(body)+2, edgesRounded, styleDim)
}

// footerSummary is the live board's state for the status bar, "" where the
// filter box's edge carries it instead.
func (v *chatsView) footerSummary(width int) string {
	if v.createWide() {
		return v.create.footerSummary()
	}
	if v.archived || v.create != nil || width < chatFooterSummaryMinW {
		return ""
	}
	parts, waiting := v.chatsSummaryParts()
	out := styleDim.Render(strings.Join(parts, " · "))
	if waiting != "" {
		out += styleDim.Render(" · ") + styleWarn.Render(attentionBadge+" "+waiting)
	}
	return " " + out + "  "
}

// footerSummary is the board's, because the board is beside the conversation.
func (v *chatView) footerSummary(width int) string {
	if !v.frameless() {
		return ""
	}
	return v.side.footerSummary(width)
}

// chatsSummary says what the board is showing, for the filter box's edge.
func (v *chatsView) chatsSummary() string {
	if v.archived {
		return styleDim.Render(v.pageCount(plural(len(v.chats), "chat", "chats")))
	}
	parts, waiting := v.chatsSummaryParts()
	out := styleDim.Render(strings.Join(parts, " · "))
	if waiting != "" {
		out = styleWarn.Render(waiting) + styleDim.Render(" · ") + out
	}
	return out
}

// chatsSummaryParts is the live board's state, and how many chats wait on you.
func (v *chatsView) chatsSummaryParts() (parts []string, waiting string) {
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
	if n := countChatsAwaiting(v.chats); n > 0 && v.loadErr == "" {
		waiting = strconv.Itoa(n) + " waiting on you"
	}
	return parts, waiting
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
	title := toneBlue.Bold(true).Render(ic("◌ Chats"))
	if v.archived {
		title = toneBlue.Bold(true).Render(ic("▣ Archived chats"))
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
	if v.create != nil {
		body = append(chatDraftCard(inner), "")
	}
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
			body = append(body, look.tone.Bold(true).Render(ic(look.glyph)+" "+look.name)+" "+styleDim.Render(strconv.Itoa(counts[b])))
		}
		if i == v.cursor {
			from, to = len(body), len(body)+chatCardH
		}
		spans = append(spans, span{len(body), r.chat.ID})
		body = append(body, v.chatCard(*r.chat, i == v.cursor && v.create == nil, inner)...)
	}
	room := max(h-2, 1)
	start := 0
	// While a chat is being written the list has no selection to follow, and the
	// card standing in for the draft stays at the top.
	if len(body) > room && v.create == nil {
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
	head := styleKey.Render("#" + strconv.FormatInt(c.ID, 10))
	// The unread mark (task 165) beside the id, where a reader scanning the
	// rail's left edge finds it; the status line keeps the chat's state.
	if mark := unreadMark(c); mark != "" {
		head += " " + mark
	}
	head += "  " + toneMagenta.Render(ic("◈ ")+oneLine(c.Agent))
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
	if c.TurnCount > 0 {
		status += " · " + plural(c.TurnCount, "turn", "turns")
	}
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
		return ic("▶ working ") + spinnerFrame(frame), toneCyan
	case "idle":
		return ic("○ idle"), styleDim
	}
	return ic("✓ ") + chatStateLabel(c.State), styleDim
}

// chatLinkMark names what a chat was opened on, "" for a free chat.
func chatLinkMark(c apiclient.Chat) string {
	switch {
	case c.IssueID != nil:
		return ic("▦ #") + strconv.FormatInt(*c.IssueID, 10)
	case c.LinkedTaskID != nil:
		return ic("▶ #") + strconv.FormatInt(*c.LinkedTaskID, 10)
	}
	return ""
}

// chatStatePill is a chat's state, set into the conversation's top edge.
func chatStatePill(state string) string {
	style, label := styleDim, chatStateLabel(state)
	switch state {
	case "awaiting_input":
		style, label = styleWarn, attentionBadge+" "+label
	case "running":
		style = toneCyan
	}
	return style.Bold(true).Reverse(true).Render(" " + label + " ")
}

// chatPreviewPane is the selected chat's latest turns, newest at the bottom,
// with an agent's question pinned under them.
func chatPreviewPane(c apiclient.Chat, ok bool, p *chatPreview, now time.Time, w, h int) []string {
	if !ok {
		body := []string{styleDim.Render("Select a chat to read it here.")}
		return drawBox(styleTitle.Render("Conversation"), "", body, w, h, edgesRounded, styleDim)
	}
	inner := max(w-4, 1)
	room := max(h-2, 1)
	foot := chatPreviewFoot(c, inner)
	lines := chatPreviewLines(c, p, now, inner)
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
func chatPreviewLines(c apiclient.Chat, p *chatPreview, now time.Time, inner int) []string {
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
		asked, answered := chatTurnAges(t, now)
		out = append(out, promptCardLines(t.Prompt, asked, inner)...)
		out = append(out, "", chatTurnHeading(c.Agent, t, answered), "")
		if checks := chatPreviewChecks(p.tools[t.Seq], inner); len(checks) > 0 {
			out = append(append(out, checks...), "")
		}
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
		hint = ic("▶ working · enter follows it live")
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
	for i, q := range req.Questions {
		if i > 0 {
			body = append(body, "")
		}
		body = append(body, styled(wrapText(sanitizeText(q.Text), inner, wrapOpts{maxLines: 3}), styleTitle)...)
		body = append(body, "")
		for _, o := range q.Options {
			body = append(body, ansi.Truncate("  "+ic("○ ")+oneLine(o), inner, "…"))
		}
		body = append(body, styleDim.Render("  ✎ "+answerCardFreeText))
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
//
// The `changes` row is back (task 169 decision 10): task 150 decision 17
// dropped it because nothing could say what a chat had changed, and the chat
// diff routes now can.
func chatDetailsPane(c apiclient.Chat, ok bool, turns []apiclient.ChatTurn, changes chatChanges, now time.Time, w, h int) []string {
	title := styleTitle.Render("Details")
	if !ok {
		return drawBox(title, "", nil, w, h, edgesRounded, styleDim)
	}
	inner := max(w-4, 1)
	row := func(k, val string) string { return padBetweenCells(styleDim.Render(k), oneLine(val), inner) }

	body := []string{styleTitle.Render(ic("◈ Agent")), row("agent", c.Agent)}
	if c.Model != "" {
		body = append(body, row("model", c.Model))
	}
	if c.Effort != "" {
		body = append(body, row("effort", c.Effort))
	}
	if c.PermissionMode != "" {
		body = append(body, row("permission", strings.ReplaceAll(c.PermissionMode, "_", "-")))
	}
	if c.Branch != "" {
		body = append(body, "", styleTitle.Render(ic("⎇ Branch")), toneCyan.Render(ansi.Truncate(oneLine(c.Branch), inner, "…")))
		if c.BaseBranch != "" {
			body = append(body, row("base", c.BaseBranch))
		}
	}
	body = append(body, changes.rows(row)...)
	body = append(body, "", styleTitle.Render(ic("◷ Activity")), row("turns", strconv.Itoa(len(turns))))
	tokens, cost, priced := chatUsage(turns)
	if tokens > 0 {
		body = append(body, row("tokens", chatTokens(tokens)))
	}
	if priced {
		body = append(body, row("cost", fmt.Sprintf("$%.2f", cost)))
	}
	if age := strings.TrimPrefix(issueAge(now, c.CreatedAt), ic("◷ ")); age != "" {
		if age != "now" {
			age += " ago"
		}
		body = append(body, row("started", age))
	}
	body = append(body, "", styleTitle.Render(ic("▦ Linked")))
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

// chatTokens is a token count in the fewest cells: `950`, `18.2k`, `1.1M`.
// It is the TUI's one compact number formatter (task 157); compactCount is
// the same thing under the name the step details use.
func chatTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_950:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
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

// chatOpenFilterMinH is the shortest terminal that keeps the filter box above
// an open conversation; a shorter one spends the rows on the conversation.
const chatOpenFilterMinH = 16

// paneEdge is the conversation pane's top edge: the chat on the left, and the
// reading modes in force and its state on the right.
func (v *chatView) paneEdge(w int) (title, right string) {
	if v.chat == nil {
		return styleTitle.Render("chat"), ""
	}
	right = chatStatePill(v.chat.State)
	for _, mark := range slices.Backward(v.headerMarks()) {
		right = mark.text + " " + right
	}
	id := "#" + strconv.FormatInt(v.chatID, 10)
	// The context gauge rides on this edge (task 157): with its bar where the
	// title keeps its minimum beside it, with the percentage alone where it
	// does not, and not at all where even that would push the state off — a
	// box drops a right side that does not fit whole.
	fits := func(g string) bool {
		return ansi.StringWidth(g)+1+ansi.StringWidth(right)+ansi.StringWidth(id)+chatPaneEdgeChrome+chatHeaderTitleMin <= w
	}
	for _, wide := range []bool{true, false} {
		if g, _ := chatGauge(v.turns, wide, v.compactCommand(), v.gaugeCompactKey()); g != "" && fits(g) {
			right = g + " " + right
			break
		}
	}
	room := max(w-ansi.StringWidth(right)-ansi.StringWidth(id)-chatPaneEdgeChrome, chatHeaderTitleMin)
	return styleKey.Render(id) + " " + styleTitle.Render(ansi.Truncate(oneLine(v.chat.Title), room, "…")), right
}

// chatPaneEdgeChrome is the columns a box's corners, rules and gaps take from
// its top edge, so the state beside a long title is never dropped.
const chatPaneEdgeChrome = 10

// renderWorkspace is the conversation with the list on its left and the
// details on its right, under the board's filter. Its frame is the focus
// colour: this is the pane the keyboard is in.
func (v *chatView) renderWorkspace(width, height int) string {
	listW, detailsW := chatPaneWidths(width)
	v.side.selectedID = v.chatID
	v.side.restoreSelection()
	var filter []string
	if height >= chatOpenFilterMinH {
		filter = v.side.chatsFilterBox(width)
	}
	height -= len(filter)
	out := v.side.chatListPane(listW, height, len(filter))

	midW := width - listW - detailsW
	var head []string
	if totals := v.totalsLine(); totals != "" {
		head = []string{totals}
	}
	body := strings.Split(v.conversationUnder(head, max(midW-4, 4), max(height-2, 4)), "\n")
	title, right := v.paneEdge(midW)
	box := drawBox(title, right, body, midW, height, edgesRounded, styleFocus)
	// The search count and `↓ N new` sit on the pane's bottom edge (task
	// 160 decisions 3 and 5).
	if label := v.edgeLabel(); label != "" && len(box) > 0 {
		box[len(box)-1] = boxBottom(label, midW-2, edgesRounded, styleFocus)
	}
	// The body starts inside the box's top edge, under head (review F2);
	// the label is on the box's bottom edge, all of whose width follows.
	v.convHit = v.convHit.shift(listW+2, len(filter)+1)
	v.convHit.edgeY, v.convHit.edgeX0, v.convHit.edgeX1 = len(filter)+height-1, listW, listW+midW
	out = joinColumns(out, box)
	if detailsW > 0 {
		var c apiclient.Chat
		ok := v.chat != nil
		if ok {
			c = *v.chat
		}
		out = joinColumns(out, chatDetailsPane(c, ok, v.turns, v.changes(), v.now(), detailsW, height))
	}
	return strings.Join(append(filter, out...), "\n")
}

// updateClick opens the chat card under the pointer from inside a conversation.
// A click in the conversation itself works in both layouts (task 160
// decisions 1 and 5, review F2); the cards are the workspace's alone.
func (v *chatView) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if v.form != nil {
		return nil
	}
	// The conversation's click targets are not on screen under the diff.
	if !v.diff.open && v.updateConvClick(msg) {
		return v.fetchVisibleTranscripts()
	}
	if !v.sideShown(v.termW) {
		return nil
	}
	id, ok := v.side.cardAt(msg)
	if !ok || id == v.chatID {
		return nil
	}
	pid := v.side.project.id
	return func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewChats} }
}
