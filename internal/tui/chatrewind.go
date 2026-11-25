package tui

// Rewinding and forking a chat from a turn (task 167, §15): the `rewind`
// operation and the palette's "rewind…" open a turn picker that rewinds the
// chat's code, its conversation or both to the state before a turn ran;
// "fork from turn…" opens the same picker to cut a new chat from one. Which
// turn may be chosen, and why not, is the daemon's answer on each turn
// (ChatTurn.Rewind) and on each saved state (Chat.RewoundStates): the picker
// greys a row out with the reason the endpoint's 409 would carry, and keeps
// no copy of the rules. Rewound turns stay in the conversation, hidden until
// the "show rewound turns" toggle draws them dimmed (decision 9).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// The rewind modes of POST /v1/chats/{id}/rewind, in the order tab cycles
// them. both is the daemon's default and the picker's (decision F).
const (
	rewindBoth         = "both"
	rewindCode         = "code"
	rewindConversation = "conversation"
)

var rewindModes = []string{rewindBoth, rewindCode, rewindConversation}

// The refusal reasons a turn's rewind and fork availability carries, the same
// codes the endpoints' 409s do.
const (
	reasonCheckpointUnavailable = "chat_checkpoint_unavailable"
	reasonForeignCommits        = "chat_rewind_foreign_commits"
	reasonTurnLive              = "chat_turn_live"
	reasonTurnRewound           = "chat_turn_rewound"
	reasonChatTerminal          = "chat_terminal"
)

// rewindReasonText is a refusal reason in plain words. An unknown code is
// shown as itself: a newer daemon's reason is still a reason.
func rewindReasonText(code string) string {
	switch code {
	case reasonCheckpointUnavailable:
		return "no checkpoint"
	case reasonForeignCommits:
		return "commits not made by this chat"
	case reasonTurnLive:
		return "a turn is live"
	case reasonTurnRewound:
		return "already rewound"
	case reasonChatTerminal:
		return "chat closed"
	}
	return code
}

// rewindPickerVisible is how many rows the picker draws at once, so a long
// conversation does not push the composer off the screen.
const rewindPickerVisible = 8

// chatRewindRow is one choosable target: a turn (seq), or a state an
// earlier code rewind saved (forward, its k).
type chatRewindRow struct {
	seq     int
	forward int
	prompt  string
	turn    *apiclient.ChatTurn
	blocked string // a forward row's refusal, "" when allowed
}

// chatRewindPicker is the turn picker. It owns the keyboard while open.
type chatRewindPicker struct {
	// fork is the "fork from turn…" picker; otherwise it rewinds in mode.
	fork bool
	// compare is the "compare two agents from turn…" picker (task 171): a
	// fork picker whose agent choice is the second side — the first is the
	// chat's own agent — and whose enter starts both.
	compare bool
	mode    string
	// agents is the fork's agent choice (task 171 decision 2): "" — the
	// source's own agent — then every other available adapter that can
	// hold a chat, by name; agent indexes it. Tab cycles it as it cycles
	// the rewind mode, which a fork does not have.
	agents []string
	agent  int
	rows   []chatRewindRow
	cursor int
	busy   bool
	err    string
	// drawn is the rows render last drew, for a click (#1144).
	drawn []clickRow
}

func (p *chatRewindPicker) clickRows() []clickRow { return p.drawn }

// selectsOnClick: a click on a row only points at it. Rewinding drops the
// later turns, and a fork or compare starts chats; enter does those, and
// one stray click must not (#1144).
func (p *chatRewindPicker) selectsOnClick() {}

// pointAt is a click on a row (#1144).
func (p *chatRewindPicker) pointAt(i int) bool {
	if p.busy || i < 0 || i >= len(p.rows) {
		return false
	}
	p.cursor = i
	return true
}

// newChatRewindPicker lists every turn not already rewound, oldest first,
// then (rewinding only) the saved states a forward rewind restores. The
// cursor starts on the newest turn: undoing the last turn is the usual ask.
func newChatRewindPicker(c *apiclient.Chat, turns []apiclient.ChatTurn, fork bool, agents map[string]apiclient.Agent) *chatRewindPicker {
	p := &chatRewindPicker{fork: fork, mode: rewindBoth}
	if fork {
		p.agents = forkAgents(c, agents)
	}
	for i := range turns {
		t := &turns[i]
		if t.RewoundAt != nil {
			continue
		}
		p.rows = append(p.rows, chatRewindRow{seq: t.Seq, prompt: t.Prompt, turn: t})
	}
	p.cursor = max(len(p.rows)-1, 0)
	if !fork && c != nil {
		for _, s := range c.RewoundStates {
			p.rows = append(p.rows, chatRewindRow{forward: s.K, blocked: s.Blocked})
		}
	}
	return p
}

// forkAgents is the fork picker's agent choice: the source's agent first,
// as "", then every other adapter GET /v1/agents says is available and can
// resume a session — the daemon's 400 for any other — sorted by name. An
// adapter that has not said whether it resumes is left out, as the new-chat
// form leaves it out.
func forkAgents(c *apiclient.Chat, agents map[string]apiclient.Agent) []string {
	out := []string{""}
	var names []string
	for name, a := range agents {
		if c != nil && name == c.Agent {
			continue
		}
		if a.Available && a.SupportsResume != nil && *a.SupportsResume {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return append(out, names...)
}

// compareMode turns a fork picker into the compare picker. Its agent choice
// is every other agent forkAgents offers, without the "same agent" entry:
// a compare's sides are two different agents.
func (p *chatRewindPicker) compareMode() {
	p.compare = true
	if len(p.agents) > 0 && p.agents[0] == "" {
		p.agents = p.agents[1:]
	}
}

// forkAgent is the agent the fork goes to, "" for the source's own.
func (p *chatRewindPicker) forkAgent() string {
	if p.agent < 0 || p.agent >= len(p.agents) {
		return ""
	}
	return p.agents[p.agent]
}

// why is the row's refusal under the picker's action, "" when it may be
// chosen. A turn the daemon said nothing about is not offered: the daemon
// would be the one to refuse it, and the picker does not guess.
func (p *chatRewindPicker) why(r chatRewindRow) string {
	if r.forward > 0 {
		return r.blocked
	}
	rw := r.turn.Rewind
	if rw == nil {
		return reasonCheckpointUnavailable
	}
	switch {
	case p.fork:
		return rw.Fork
	case p.mode == rewindCode:
		return rw.Code
	case p.mode == rewindConversation:
		return rw.Conversation
	case rw.Code != "":
		return rw.Code
	}
	return rw.Conversation
}

// label is the row's text: the turn and its prompt on one line, or the saved
// state a forward rewind restores, then what the action would do there.
func (p *chatRewindPicker) label(r chatRewindRow) string {
	var s string
	if r.forward > 0 {
		s = fmt.Sprintf("forward to the code before rewind %d (code only)", r.forward)
	} else {
		prompt := strings.TrimSpace(oneLine(r.prompt))
		if prompt == "" {
			prompt = "(no prompt)"
		}
		s = fmt.Sprintf("turn %d  %s", r.seq, prompt)
	}
	if why := p.why(r); why != "" {
		return s + " — " + rewindReasonText(why)
	}
	if r.forward == 0 && !p.fork && p.mode != rewindCode && r.turn.Rewind.ConversationSeeded {
		return s + " — seeded, not resumed"
	}
	return s
}

// title is the picker's first line: what enter does, and the keys.
func (p *chatRewindPicker) title() string {
	if p.compare {
		names := make([]string, len(p.agents))
		for i, a := range p.agents {
			if i == p.agent {
				a = "[" + a + "]"
			}
			names[i] = a
		}
		return "compare this agent with " + strings.Join(names, " ") +
			" from before a turn — tab agent, enter starts both, esc close"
	}
	if p.fork {
		if len(p.agents) < 2 {
			return "fork a new chat from the state before a turn — enter fork, esc close"
		}
		names := make([]string, len(p.agents))
		for i, a := range p.agents {
			if a == "" {
				a = "same agent"
			}
			if i == p.agent {
				a = "[" + a + "]"
			}
			names[i] = a
		}
		return "fork onto " + strings.Join(names, " ") + " from before a turn — tab agent, enter fork, esc close"
	}
	modes := make([]string, len(rewindModes))
	for i, m := range rewindModes {
		modes[i] = m
		if m == p.mode {
			modes[i] = "[" + m + "]"
		}
	}
	return "rewind " + strings.Join(modes, " ") + " to before a turn — tab mode, enter rewind, esc close"
}

func (p *chatRewindPicker) render(width int) []string {
	out := []string{" " + styleDim.Render(p.title())}
	p.drawn = p.drawn[:0]
	// The other modes, by their keys (#1080).
	var other []string
	switch {
	case p.compare:
		other = []string{rewindForkKey + " fork", rewindCompareKey + " back to rewind"}
	case p.fork:
		other = []string{rewindForkKey + " back to rewind", rewindCompareKey + " compare two agents"}
	default:
		other = []string{rewindForkKey + " fork a new chat instead", rewindCompareKey + " compare two agents"}
	}
	out = append(out, ansi.Truncate(" "+styleDim.Render(strings.Join(other, " · ")), width, "…"))
	if len(p.rows) == 0 {
		return append(out, "   "+styleDim.Render("no turn to go back to"))
	}
	from := 0
	if len(p.rows) > rewindPickerVisible {
		from = min(max(p.cursor-rewindPickerVisible/2, 0), len(p.rows)-rewindPickerVisible)
	}
	to := min(from+rewindPickerVisible, len(p.rows))
	for i := from; i < to; i++ {
		r := p.rows[i]
		text := p.label(r)
		var line string
		switch {
		case i == p.cursor:
			line = " " + styleSelected.Render("▸ "+text)
		case p.why(r) != "":
			line = "   " + styleDim.Render(text)
		default:
			line = "   " + text
		}
		line = ansi.Truncate(line, width, "…")
		p.drawn = append(p.drawn, drawnRow(line, i))
		out = append(out, line)
	}
	switch {
	case p.busy && p.compare:
		out = append(out, "   "+styleDim.Render("forking twice and starting both…"))
	case p.busy && p.fork:
		out = append(out, "   "+styleDim.Render("forking…"))
	case p.busy:
		out = append(out, "   "+styleDim.Render("rewinding…"))
	case p.err != "":
		out = append(out, ansi.Truncate("   "+styleBad.Render(p.err), width, "…"))
	}
	return out
}

// chatRewoundMsg is a rewind's or a fork's answer.
type chatRewoundMsg struct {
	chatID int64
	rewind *apiclient.RewindResult
	fork   *apiclient.ForkResult
	err    error
}

// openRewindPickerMsg is the palette's "rewind…", "fork from turn…" and
// "compare two agents from turn…": the rows are key-less, so they reach the
// view as a message.
type openRewindPickerMsg struct {
	chatID  int64
	fork    bool
	compare bool
}

// chatComparedMsg is a compare's answer (task 171).
type chatComparedMsg struct {
	chatID int64
	res    *apiclient.CompareResult
	err    error
}

// toggleRewoundMsg is the palette's "show rewound turns" row.
type toggleRewoundMsg struct{ chatID int64 }

// openRewindPicker opens the picker, or says why it cannot.
func (v *chatView) openRewindPicker(fork bool) {
	if v.chat == nil {
		return
	}
	v.rewind = newChatRewindPicker(v.chat, v.everyTurn(), fork, v.agents)
	v.hideInline("")
	v.hideFiles()
}

// updateRewindKey is the picker's keyboard.
func (v *chatView) updateRewindKey(msg tea.KeyPressMsg) tea.Cmd {
	p := v.rewind
	if p.busy {
		// The request is in flight: only esc, which closes the picker and
		// leaves the answer to land as a note.
		if msg.String() == "esc" {
			v.rewind = nil
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		v.rewind = nil
	case rewindForkKey, rewindCompareKey:
		// #1080: the picker's other two modes, named on its first line —
		// forking and comparing were palette-only rows nobody would find.
		v.switchRewindPicker(msg.String())
		return nil
	case "up":
		p.cursor = max(p.cursor-1, 0)
	case "down":
		p.cursor = min(p.cursor+1, max(len(p.rows)-1, 0))
	case "tab", "right":
		if p.fork {
			p.agent = (p.agent + 1) % max(len(p.agents), 1)
		} else {
			p.mode = rewindModes[(slices.Index(rewindModes, p.mode)+1)%len(rewindModes)]
		}
	case "shift+tab", "left":
		if p.fork {
			p.agent = (p.agent + max(len(p.agents), 1) - 1) % max(len(p.agents), 1)
		} else {
			p.mode = rewindModes[(slices.Index(rewindModes, p.mode)+len(rewindModes)-1)%len(rewindModes)]
		}
	case "enter":
		if len(p.rows) == 0 {
			v.rewind = nil
			return nil
		}
		r := p.rows[p.cursor]
		if why := p.why(r); why != "" {
			p.err = "cannot " + p.verb() + " here: " + rewindReasonText(why)
			return nil
		}
		return v.rewindCmd(p, r)
	}
	p.err = ""
	return nil
}

func (p *chatRewindPicker) verb() string {
	if p.compare {
		return "compare"
	}
	if p.fork {
		return "fork"
	}
	return "rewind"
}

// rewindCmd sends the chosen rewind or fork.
func (v *chatView) rewindCmd(p *chatRewindPicker, r chatRewindRow) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil {
		p.err = "not connected"
		return nil
	}
	p.busy, p.err = true, ""
	if p.compare {
		// The prompt is the composer's draft when there is one, else the
		// turn's own (the daemon's default): "ask both this", or "how would
		// each have answered turn N".
		req := apiclient.CompareRequest{
			Turn: r.seq, Prompt: strings.TrimSpace(v.composer.Value()),
			Agents: []string{v.chat.Agent, p.forkAgent()},
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			res, err := client.CompareChat(ctx, id, req)
			return chatComparedMsg{chatID: id, res: res, err: err}
		}
	}
	if p.fork {
		// Another agent's fork takes that agent's own model and effort
		// defaults (decision 2): the source's would mean nothing to it.
		req := apiclient.ForkRequest{Turn: r.seq, Agent: p.forkAgent()}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			res, err := client.ForkChat(ctx, id, req)
			return chatRewoundMsg{chatID: id, fork: res, err: err}
		}
	}
	req := apiclient.RewindRequest{Turn: r.seq, Mode: p.mode}
	if r.forward > 0 {
		req = apiclient.RewindRequest{Rewound: r.forward}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := client.RewindChat(ctx, id, req)
		return chatRewoundMsg{chatID: id, rewind: res, err: err}
	}
}

// applyRewound settles the picker. A refusal stays on it, in plain words; a
// rewind reloads the chat — the turns it marked, and the draft the daemon put
// turn N's prompt back into (task 164) — and a fork opens the new chat.
func (v *chatView) applyRewound(msg chatRewoundMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		text := errString(msg.err)
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) {
			text = apiErr.Message
			if plain := rewindReasonText(apiErr.Code); plain != apiErr.Code {
				text = plain
			}
		}
		if v.rewind != nil {
			v.rewind.busy, v.rewind.err = false, text
		} else {
			v.status().result(text, true)
		}
		return nil
	}
	v.rewind = nil
	if msg.fork != nil {
		c := msg.fork.Chat
		note := "forked from chat #" + strconv.FormatInt(v.chatID, 10)
		if v.chat != nil && c.Agent != v.chat.Agent {
			note += " onto " + c.Agent
		}
		if msg.fork.Seeded {
			note += " — its first turn is seeded, not resumed"
		}
		note += skippedNote(msg.fork.Skipped)
		back := v.back
		return func() tea.Msg {
			return openChatMsg{id: c.ID, projectID: c.ProjectID, back: back, note: note}
		}
	}
	res := msg.rewind
	note := "rewound"
	if n := len(res.RewoundTurns); n > 0 {
		note += " — " + plural(n, "turn", "turns") + " hidden"
	}
	if res.Seeded {
		note += "; the next turn is seeded, not resumed"
	}
	v.status().result(note+skippedNote(res.Skipped), len(res.Skipped) > 0)
	// The draft is re-read rather than set here: the daemon holds it, and a
	// composer with unsaved typing keeps it (applyRemoteDraft's rule).
	return v.loadCmd()
}

// skippedNote names the files a rewind or fork left in place because the
// snapshot could not read them (decision D), "" when there were none.
func skippedNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return "; left in place, unreadable when saved: " + strings.Join(paths, ", ")
}

// visibleTurns is the turns the conversation draws: every turn not rewound,
// and the rewound ones too while the toggle shows them (decision 9).
func visibleTurns(all []apiclient.ChatTurn, showRewound bool) []apiclient.ChatTurn {
	if showRewound {
		return all
	}
	out := make([]apiclient.ChatTurn, 0, len(all))
	for i := range all {
		if all[i].RewoundAt == nil {
			out = append(out, all[i])
		}
	}
	return out
}

// rewoundCount is how many turns a rewind left behind.
func rewoundCount(all []apiclient.ChatTurn) int {
	n := 0
	for i := range all {
		if all[i].RewoundAt != nil {
			n++
		}
	}
	return n
}

// toggleRewound shows or hides the rewound turns.
func (v *chatView) toggleRewound() {
	n := rewoundCount(v.everyTurn())
	if n == 0 && !v.showRewound {
		v.status().note("no turn of this chat has been rewound", false)
		return
	}
	v.showRewound = !v.showRewound
	v.turns = visibleTurns(v.everyTurn(), v.showRewound)
	v.bodyDirty = true
	if v.showRewound {
		v.status().result("showing "+plural(n, "rewound turn", "rewound turns")+", dimmed", false)
	} else {
		v.status().result("rewound turns hidden", false)
	}
}

// rewoundHeading is the marker line above a rewound turn the toggle shows.
func rewoundHeading(t apiclient.ChatTurn) string {
	s := "↶ rewound"
	if t.RewoundAt != nil {
		s += " " + t.RewoundAt.Local().Format("2006-01-02 15:04")
	}
	return s
}

// dimLines redraws a rewound turn's lines dimmed: its own colours are
// dropped so the whole turn reads as set aside.
func dimLines(lines []string) {
	for i, l := range lines {
		if l != "" {
			lines[i] = styleDim.Render(ansi.Strip(l))
		}
	}
}

// forkedFromLabel is a fork's provenance as the card and header read it
// (task 171 decision 5): "forked from #N (claude)", the agent left out on a
// fork from before it was recorded. "" for a chat that is not a fork.
func forkedFromLabel(c *apiclient.Chat) string {
	if c == nil || c.ForkedFrom == nil {
		return ""
	}
	label := fmt.Sprintf("forked from #%d", c.ForkedFrom.ChatID)
	if c.ForkedFrom.Agent != "" {
		label += " (" + c.ForkedFrom.Agent + ")"
	}
	return label
}

// turnZeroMaxLines bounds the seeded context the conversation shows before
// the first turn; the rest is counted, and `vincent chat show` prints it all.
const turnZeroMaxLines = 40

// turnZeroLines is a fork's head before its first turn (task 171 decision
// 4): its provenance, then either the seeded context the first turn will
// receive, under the "turn 0" heading, or the native resume it will make
// instead. Nil for a chat that is neither a fork nor seeded.
func turnZeroLines(c *apiclient.Chat, width int) []string {
	if c == nil {
		return nil
	}
	var out []string
	if label := forkedFromLabel(c); label != "" {
		out = append(out, " "+styleDim.Render(fmt.Sprintf("⑂ %s, before turn %d", label, c.ForkedFrom.Seq)))
	}
	switch {
	case c.ResumePending == "seeded" && c.PendingPreamble != "":
		out = append(out, seededContextLines(c, width)...)
	case c.ResumePending == "resumed" && c.ForkedFrom != nil:
		out = append(out, " "+styleDim.Render(fmt.Sprintf("↶ resumed from turn %d: the agent's own session continues", c.ForkedFrom.Seq-1)))
	}
	return out
}

// seededContextLines is the "turn 0" block: the seeded context the chat's
// next turn will receive, bounded, and how to edit it. It is the same block
// whether the chat is a fork before its first turn or a seeded rewind with
// turns kept (review F2) — task 171 decision 4 says never hidden, and the
// PATCH accepts edits on both. Nil when no seeded context is pending.
func seededContextLines(c *apiclient.Chat, width int) []string {
	if c == nil || c.ResumePending != "seeded" || c.PendingPreamble == "" {
		return nil
	}
	out := []string{"", " " + styleStepHeader.Render("turn 0 · seeded context (summary)")}
	body := strings.Split(strings.TrimRight(c.PendingPreamble, "\n"), "\n")
	shown := body
	if len(shown) > turnZeroMaxLines {
		shown = shown[:turnZeroMaxLines]
	}
	for _, l := range shown {
		for _, w := range wrapText(l, max(width-3, 1), wrapOpts{indent: 0}) {
			out = append(out, "   "+styleDim.Render(w))
		}
	}
	if more := len(body) - len(shown); more > 0 {
		out = append(out, "   "+styleDim.Render(fmt.Sprintf("… %d more lines (vincent chat show %d)", more, c.ID)))
	}
	return append(out, "   "+styleDim.Render(fmt.Sprintf("edit before the next turn: \"edit turn 0…\" in the palette, or vincent chat preamble %d", c.ID)))
}

// resumePendingLine says how the chat's next turn picks up after a
// conversation rewind, "" when none is pending.
func resumePendingLine(c *apiclient.Chat) string {
	if c == nil {
		return ""
	}
	switch c.ResumePending {
	case "":
		return ""
	case "seeded":
		return "↶ the next turn starts a fresh session given the earlier turns — seeded, not resumed"
	case "resumed":
		return "↶ the next turn resumes the agent's session from the rewind point"
	}
	return "↶ the next turn picks up after a rewind (" + c.ResumePending + ")"
}

// rewindPaletteExtras are the conversation's key-less rewind rows: forking
// and the rewound-turn toggle. "rewind…" is the rewind operation's own
// registry row. Forking is left out on an ended chat, as rewind is: the
// daemon refuses both there with chat_terminal (task 167 decision 8), so the
// picker would open with every row disabled (review F8). The toggle stays —
// reading an ended chat's rewound turns changes nothing.
func (v *chatView) rewindPaletteExtras() []paletteEntry {
	if v.chat == nil {
		return nil
	}
	id := v.chat.ID
	var out []paletteEntry
	if !chatstate.Terminal(chatstate.State(v.chat.State)) {
		out = append(out, paletteEntry{
			group:  "chat",
			label:  "fork from turn… — a new chat from the state before a turn",
			action: func() tea.Msg { return openRewindPickerMsg{chatID: id, fork: true} },
		})
	}
	if !chatstate.Terminal(chatstate.State(v.chat.State)) && len(forkAgents(v.chat, v.agents)) > 1 {
		out = append(out, paletteEntry{
			group:  "chat",
			label:  "compare two agents from turn… — this agent and another answer the same prompt, side by side",
			action: func() tea.Msg { return openRewindPickerMsg{chatID: id, compare: true} },
		})
	}
	if v.compare != nil {
		back := v.back
		out = append(out, paletteEntry{
			group:  "chat",
			label:  "compare side by side — this chat and the other side of its compare",
			action: func() tea.Msg { return openCompareMsg{chatID: id, back: back} },
		})
	}
	if preambleEditable(v.chat) {
		out = append(out, paletteEntry{
			group:  "chat",
			label:  "edit turn 0… — the seeded context the next turn starts with, in $EDITOR",
			action: func() tea.Msg { return editPreambleMsg{chatID: id} },
		})
	}
	if rewoundCount(v.everyTurn()) > 0 {
		label := "show rewound turns, dimmed"
		if v.showRewound {
			label = "hide rewound turns"
		}
		out = append(out, paletteEntry{
			group:  "chat",
			label:  label,
			action: func() tea.Msg { return toggleRewoundMsg{chatID: id} },
		})
	}
	return out
}

// everyTurn is allTurns, or turns where only that was set: before the first
// load they are the same nothing, and a view built around a turn list (the
// tests', a search landing) has no rewound turns to tell apart.
func (v *chatView) everyTurn() []apiclient.ChatTurn {
	if v.allTurns == nil {
		return v.turns
	}
	return v.allTurns
}

// editPreambleMsg is the palette's "edit turn 0…" row.
type editPreambleMsg struct{ chatID int64 }

// chatPreambleEditedMsg carries a turn-0 editing session's outcome: the
// context it started from and what the editor left.
type chatPreambleEditedMsg struct {
	chatID   int64
	original string
	text     string
	err      error
}

// preambleEditable is whether the chat's turn 0 may be edited now: the
// store's own rule (task 171 decision 4) — a seeded context pending and no
// process holding the chat — read off the DTO so the palette does not offer
// an edit the daemon would refuse. The daemon still decides.
func preambleEditable(c *apiclient.Chat) bool {
	return c != nil && c.ResumePending == "seeded" && c.PendingPreamble != "" &&
		!chatstate.HoldsProcess(chatstate.State(c.State)) && !chatstate.Terminal(chatstate.State(c.State))
}

// editPreamble hands turn 0 to $EDITOR through the same round trip the
// composer's ctrl+g uses (task 163 decision 3). What comes back is sent as
// `PATCH /v1/chats/{id}` with pending_preamble.
func (v *chatView) editPreamble() tea.Cmd {
	if !preambleEditable(v.chat) {
		v.status().note("no seeded context to edit — this chat's next turn is not seeded", false)
		return nil
	}
	chatID, original := v.chatID, v.chat.PendingPreamble
	cmd, err := editTextCmd(v.exec, fmt.Sprintf("chat%d-turn0", chatID), ".md", original,
		func(text string, err error) tea.Msg {
			return chatPreambleEditedMsg{chatID: chatID, original: original, text: text, err: err}
		})
	if err != nil {
		v.status().result("editor: "+errString(err), true)
		return nil
	}
	return cmd
}

// applyPreambleEdit sends an edited turn 0. Unlike the composer's draft, an
// emptied file means something here: it clears the seeded context, so the
// next turn starts with no earlier context at all (decision 4) — the same
// explicit choice `vincent chat preamble --clear` makes. An untouched file
// sends nothing.
func (v *chatView) applyPreambleEdit(msg chatPreambleEditedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		v.status().result("editor: "+errString(msg.err)+" — turn 0 is unchanged", true)
		return nil
	}
	text := msg.text
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	if text == msg.original {
		v.status().result("turn 0 is unchanged", false)
		return nil
	}
	client, id := v.client, v.chatID
	if client == nil {
		v.status().result("not connected", true)
		return nil
	}
	v.status().result("saving turn 0…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.PatchChat(ctx, id, apiclient.PatchChatRequest{PendingPreamble: &text})
		return chatPreambleSavedMsg{chatID: id, chat: chat, cleared: text == "", err: err}
	}
}

// chatPreambleSavedMsg is the PATCH's answer.
type chatPreambleSavedMsg struct {
	chatID  int64
	chat    *apiclient.Chat
	cleared bool
	err     error
}

func (v *chatView) applyPreambleSaved(msg chatPreambleSavedMsg) {
	if msg.chatID != v.chatID {
		return
	}
	if msg.err != nil {
		text := errString(msg.err)
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) && apiErr.Code == "chat_preamble_not_pending" {
			text = "turn 0 can no longer be edited — a turn has started, or the chat no longer resumes seeded"
		}
		v.status().result(text, true)
		return
	}
	v.chat = msg.chat
	v.bodyDirty = true
	if msg.cleared {
		v.status().result("turn 0 cleared — the next turn starts a fresh session with no earlier context", false)
		return
	}
	v.status().result("turn 0 saved — the next turn starts with the edited context", false)
}

// applyCompared settles the compare picker: a refusal stays on it, in plain
// words; a compare opens the split view on its two sides.
func (v *chatView) applyCompared(msg chatComparedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		text := errString(msg.err)
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) {
			text = apiErr.Message
			if plain := rewindReasonText(apiErr.Code); plain != apiErr.Code {
				text = plain
			}
		}
		if v.rewind != nil {
			v.rewind.busy, v.rewind.err = false, text
		} else {
			v.status().result(text, true)
		}
		return nil
	}
	v.rewind = nil
	left := msg.res.Left.Chat.ID
	return func() tea.Msg { return openCompareMsg{chatID: left, back: viewChat} }
}

// chatCompareOfMsg is the compare this chat is a side of, nil for none.
type chatCompareOfMsg struct {
	chatID int64
	cmp    *apiclient.ChatCompare
}

// compareOfCmd asks, once per chat, whether a fork is a side of a compare:
// only a fork can be, so no other chat pays for the read.
func (v *chatView) compareOfCmd() tea.Cmd {
	if v.chat == nil || v.chat.ForkedFrom == nil || v.compareAsked == v.chatID || v.client == nil {
		return nil
	}
	v.compareAsked = v.chatID
	client, id := v.client, v.chatID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cmp, err := client.GetChatCompare(ctx, id)
		if err != nil {
			return chatCompareOfMsg{chatID: id}
		}
		return chatCompareOfMsg{chatID: id, cmp: cmp}
	}
}

// openComparePicker opens the turn picker as the compare picker (task 171).
// The rewind picker's mode keys (#1080).
const (
	rewindForkKey    = "f"
	rewindCompareKey = "c"
)

// canFork and canCompare say whether the picker may switch to that mode:
// never on an ended chat, and compare only with a second agent to ask.
func (v *chatView) canFork() bool {
	return v.chat != nil && !chatstate.Terminal(chatstate.State(v.chat.State))
}

func (v *chatView) canCompare() bool {
	return v.canFork() && len(forkAgents(v.chat, v.agents)) > 1
}

// switchRewindPicker moves the open picker to fork or compare, or back to
// rewind when it is already there, with the cursor kept on its turn.
func (v *chatView) switchRewindPicker(key string) {
	old := v.rewind
	var seq int
	if old.cursor < len(old.rows) {
		seq = old.rows[old.cursor].seq
	}
	switch {
	case key == rewindForkKey && old.fork && !old.compare,
		key == rewindCompareKey && old.compare:
		v.openRewindPicker(false)
	case key == rewindForkKey && v.canFork():
		v.openRewindPicker(true)
	case key == rewindCompareKey && v.canCompare():
		v.openComparePicker()
	case !v.canFork():
		old.err = "this chat has ended — nothing forks or compares from it"
		return
	default:
		old.err = "no second agent that can hold a chat is available to compare with"
		return
	}
	for i, r := range v.rewind.rows {
		if seq != 0 && r.seq == seq {
			v.rewind.cursor = i
		}
	}
}

func (v *chatView) openComparePicker() {
	v.openRewindPicker(true)
	if v.rewind != nil {
		v.rewind.compareMode()
	}
}
