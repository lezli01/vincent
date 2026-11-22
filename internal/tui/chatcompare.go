package tui

// The compare split view (task 171 decision 9, §15): the two sides of a
// `vincent chat compare` side by side — each fork's latest turns, its agent
// and the permission mode it actually ran under — refreshed while either
// side is working, with keep left / keep right (decision 8). The view holds
// nothing the daemon does not: the pair is GET /v1/chats/{id}/compare and
// each side is the chat read the chats board's preview pane makes.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/keymap"
)

// compareRefresh is how often the view re-reads both sides while either is
// working. A compare is two turns watched at once, not a conversation being
// typed into, so a second is live enough and costs two reads.
const compareRefresh = time.Second

// compareStackBelow is the width under which the sides stack rather than
// sit side by side: two panes narrower than forty columns hold no prose.
const compareStackBelow = 80

type (
	// compareLoadedMsg is one read of the pair and both sides.
	compareLoadedMsg struct {
		gen   int
		cmp   *apiclient.ChatCompare
		sides [2]*chatPreview
		err   error
	}
	// compareTickMsg asks for the next read while a side is working.
	compareTickMsg struct{ gen int }
	// compareKeptMsg is a keep's answer.
	compareKeptMsg struct {
		res *apiclient.KeepResult
		err error
	}
	// openCompareMsg opens the split view on the compare chatID is a side of.
	openCompareMsg struct {
		chatID int64
		back   viewID
	}
)

type compareView struct {
	client *apiclient.Client
	// chatID is the side the view was opened from; the pair is read from it.
	chatID int64
	back   viewID
	gen    int
	cmp    *apiclient.ChatCompare
	sides  [2]*chatPreview
	err    string
	// focus is the side enter opens: 0 left, 1 right.
	focus int
	// confirm is the side a keep is waiting on y for, -1 for none. It owns
	// the next key, as the chat's close confirmation does.
	confirm int
	busy    bool
	note    string
	noteBad bool
	ticking bool
	now     func() time.Time
}

func newCompareView() *compareView {
	return &compareView{confirm: -1, now: time.Now}
}

func (v *compareView) title() string { return "compare" }

// frameless: the screen draws its own header and its two panes are boxes of
// their own, as the chat workspace's are; a frame around them would title
// the screen twice.
func (v *compareView) frameless() bool { return true }

func (v *compareView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	if v.chatID == 0 {
		return nil
	}
	return v.loadCmd()
}

// setProject: a compare belongs to one project, and a switch away from it
// lands on the chats board (switchFallback), so the view only forgets.
func (v *compareView) setProject(projectSel) tea.Cmd { return nil }

func (v *compareView) leaveForSwitch() {
	v.chatID, v.cmp, v.sides = 0, nil, [2]*chatPreview{}
	v.gen++
}

// open points the view at the compare chatID is a side of.
func (v *compareView) open(chatID int64) tea.Cmd {
	v.chatID, v.cmp, v.sides, v.err = chatID, nil, [2]*chatPreview{}, ""
	v.focus, v.confirm, v.busy, v.note, v.noteBad = 0, -1, false, "", false
	v.gen++
	v.ticking = false
	return v.loadCmd()
}

// loadCmd reads the pair, then each side the way the board's preview does.
func (v *compareView) loadCmd() tea.Cmd {
	client, id, gen := v.client, v.chatID, v.gen
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cmp, err := client.GetChatCompare(ctx, id)
		if err != nil {
			return compareLoadedMsg{gen: gen, err: err}
		}
		msg := compareLoadedMsg{gen: gen, cmp: cmp}
		for i, side := range []apiclient.CompareSide{cmp.Left, cmp.Right} {
			if side.ChatID == nil {
				continue
			}
			chat, turns, err := client.GetChat(ctx, *side.ChatID)
			var tools map[int][]apiclient.TranscriptRecord
			if err == nil {
				tools = readPreviewTools(ctx, client, *side.ChatID, turns)
			}
			p := previewFrom(chat, turns, tools, err)
			msg.sides[i] = &p
		}
		return msg
	}
}

// working reports whether either side holds a process, which is what keeps
// the view re-reading.
func (v *compareView) working() bool {
	for _, p := range v.sides {
		if p != nil && p.err == "" && chatstate.HoldsProcess(chatstate.State(p.chat.State)) {
			return true
		}
	}
	return false
}

func (v *compareView) applyLoaded(msg compareLoadedMsg) tea.Cmd {
	if msg.gen != v.gen {
		return nil
	}
	if msg.err != nil {
		v.err = errString(msg.err)
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) && apiErr.Status == 404 {
			v.err = fmt.Sprintf("chat %d is not a side of a compare", v.chatID)
		}
		return nil
	}
	v.err, v.cmp, v.sides = "", msg.cmp, msg.sides
	return v.armTick()
}

// armTick schedules the next read while a side works, once.
func (v *compareView) armTick() tea.Cmd {
	if !v.working() || v.ticking {
		return nil
	}
	v.ticking = true
	gen := v.gen
	return tea.Tick(compareRefresh, func(time.Time) tea.Msg { return compareTickMsg{gen: gen} })
}

func (v *compareView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case viewActivatedMsg:
		// A tick armed before the screen was left was delivered to another
		// view and dropped: forget it, so this read can arm the next one.
		// The new generation makes the lost tick's twin harmless if it is
		// still in flight.
		v.gen++
		v.ticking = false
		return v, v.loadCmd()
	case compareLoadedMsg:
		return v, v.applyLoaded(msg)
	case compareTickMsg:
		if msg.gen != v.gen {
			return v, nil
		}
		v.ticking = false
		return v, v.loadCmd()
	case compareKeptMsg:
		return v, v.applyKept(msg)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

func (v *compareView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	key := msg.String()
	if v.confirm >= 0 {
		side := v.confirm
		v.confirm = -1
		if key == "y" {
			return v, v.keepCmd(side)
		}
		v.note, v.noteBad = "nothing kept", false
		return v, nil
	}
	switch key {
	case "esc":
		back := v.back
		if back == 0 {
			back = viewChats
		}
		return v, func() tea.Msg { return selectViewMsg{id: back} }
	case "tab", "left", "right", "shift+tab":
		v.focus = 1 - v.focus
	case "enter":
		return v, v.openSide(v.focus)
	case "[":
		v.askKeep(0)
	case "]":
		v.askKeep(1)
	}
	if key == opKey(keymap.Refresh) {
		v.note = ""
		return v, v.loadCmd()
	}
	return v, nil
}

// side is one side's record and its read, nil before the first read.
func (v *compareView) side(i int) (apiclient.CompareSide, *chatPreview) {
	if v.cmp == nil {
		return apiclient.CompareSide{}, nil
	}
	if i == 0 {
		return v.cmp.Left, v.sides[0]
	}
	return v.cmp.Right, v.sides[1]
}

func (v *compareView) openSide(i int) tea.Cmd {
	s, p := v.side(i)
	if s.ChatID == nil || p == nil {
		v.note, v.noteBad = "that side's chat is gone", true
		return nil
	}
	id, pid := *s.ChatID, p.chat.ProjectID
	return func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewCompare} }
}

// askKeep asks before keeping: the other side is archived and its branch
// deleted, which cannot be undone. A refusal the view can already see —
// a side still working, a side already kept — is said instead.
func (v *compareView) askKeep(i int) {
	switch {
	case v.cmp == nil || v.busy:
		return
	case v.cmp.KeptChatID != nil:
		v.note, v.noteBad = "a side was already kept", false
		return
	case v.working():
		v.note, v.noteBad = "both answers must finish before one is kept — wait, or cancel the running side", true
		return
	}
	keep, _ := v.side(i)
	drop, _ := v.side(1 - i)
	if keep.ChatID == nil {
		v.note, v.noteBad = "that side's chat is gone", true
		return
	}
	v.confirm = i
	v.note, v.noteBad = fmt.Sprintf("keep %s (%s)? the %s side (%s) is archived and its branch deleted — y keeps, any other key cancels",
		sideName(i), keep.Agent, sideName(1-i), drop.Agent), false
}

func sideName(i int) string {
	if i == 0 {
		return "left"
	}
	return "right"
}

func (v *compareView) keepCmd(i int) tea.Cmd {
	s, _ := v.side(i)
	if v.client == nil || s.ChatID == nil {
		return nil
	}
	client, id := v.client, *s.ChatID
	v.busy, v.note, v.noteBad = true, "keeping "+sideName(i)+"…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := client.KeepCompareSide(ctx, id)
		return compareKeptMsg{res: res, err: err}
	}
}

func (v *compareView) applyKept(msg compareKeptMsg) tea.Cmd {
	v.busy = false
	if msg.err != nil {
		text := errString(msg.err)
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) {
			text = apiErr.Message
		}
		v.note, v.noteBad = text, true
		return v.loadCmd()
	}
	note := fmt.Sprintf("kept chat %d", msg.res.Kept.ID)
	if a := msg.res.Archived; a != nil {
		note += fmt.Sprintf("; chat %d archived", a.ID)
		if msg.res.BranchDeleted {
			note += ", its branch deleted"
		}
	}
	v.note, v.noteBad = note, false
	return v.loadCmd()
}

func (v *compareView) render(width, height int) string {
	if width < 4 || height < 4 {
		return ""
	}
	lines := []string{v.headerLine(width)}
	if v.cmp != nil {
		lines = append(lines, ansi.Truncate(" "+styleDim.Render("prompt: ")+oneLine(sanitizeText(v.cmp.Prompt)), width, "…"))
	}
	if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		lines = append(lines, ansi.Truncate(" "+style.Render(v.note), width, "…"))
	}
	room := max(height-len(lines), 3)
	switch {
	case v.err != "":
		lines = append(lines, "", " "+styleWarn.Render(v.err))
	case v.cmp == nil:
		lines = append(lines, "", " "+styleDim.Render("Loading…"))
	case width < compareStackBelow:
		top := room / 2
		lines = append(lines, v.pane(0, width, top)...)
		lines = append(lines, v.pane(1, width, room-top)...)
	default:
		left := width / 2
		lp, rp := v.pane(0, left, room), v.pane(1, width-left, room)
		for i := range min(len(lp), len(rp)) {
			lines = append(lines, lp[i]+rp[i])
		}
	}
	return strings.Join(lines, "\n")
}

func (v *compareView) headerLine(width int) string {
	if v.cmp == nil {
		return padBetween(" "+styleTitle.Render("compare"), "", width)
	}
	left := fmt.Sprintf(" %s  %s", styleTitle.Render("compare #"+strconv.FormatInt(v.cmp.ID, 10)),
		styleDim.Render(fmt.Sprintf("chat #%d, from before turn %d", v.cmp.SourceChatID, v.cmp.Seq)))
	right := ""
	if v.cmp.KeptChatID != nil {
		right = styleOK.Render(fmt.Sprintf("✓ kept #%d", *v.cmp.KeptChatID)) + " "
	}
	return padBetween(left, right, width)
}

// pane is one side: the chat's latest turns as the board's preview draws
// them, and pinned under them the agent and the mode the side actually runs
// under — full-auto said in warning colour with its reason (decision 7) —
// and whether it was kept.
func (v *compareView) pane(i, w, h int) []string {
	s, p := v.side(i)
	inner := max(w-4, 1)
	var foot []string
	mode := styleOK.Render(s.PermissionMode)
	if s.PermissionMode != "restricted" {
		mode = styleWarn.Render(s.PermissionMode)
	}
	foot = append(foot, toneMagenta.Render(ic("◈ ")+s.Agent)+styleDim.Render(" · ")+mode)
	if s.ModeNote != "" {
		foot = append(foot, styled(wrapText(sanitizeText(s.ModeNote), inner, wrapOpts{maxLines: 2}), styleDim)...)
	}
	switch {
	case v.cmp.KeptChatID != nil && s.ChatID != nil && *v.cmp.KeptChatID == *s.ChatID:
		foot = append(foot, styleOK.Render("✓ kept · enter continues it"))
	case v.cmp.KeptChatID != nil:
		foot = append(foot, styleDim.Render("discarded — archived, branch deleted"))
	default:
		key := "["
		if i == 1 {
			key = "]"
		}
		foot = append(foot, styleDim.Render(key+" keep this side · enter opens it"))
	}
	edges, border := edgesRounded, styleDim
	if i == v.focus {
		edges, border = edgesThick, styleFocus
	}
	if s.ChatID == nil || p == nil {
		body := []string{styleDim.Render("This side's chat was deleted.")}
		return drawBox(styleTitle.Render(sideName(i)), "", append(body, foot...), w, h, edges, border)
	}
	room := max(h-2, 1)
	lines := chatPreviewLines(p.chat, p, v.now(), inner)
	if keep := room - len(foot); len(lines) > keep {
		lines = lines[max(len(lines)-max(keep, 0), 0):]
	}
	body := make([]string, 0, room)
	body = append(body, lines...)
	for len(body)+len(foot) < room {
		body = append(body, "")
	}
	body = append(body, foot...)
	title := styleKey.Render("#"+strconv.FormatInt(p.chat.ID, 10)) + " " + styleTitle.Render(sideName(i))
	return drawBox(title, chatStatePill(p.chat.State), body, w, h, edges, border)
}
