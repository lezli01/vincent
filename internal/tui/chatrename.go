package tui

// Renaming a chat (task 161 decision 6, §15): the `rename` operation on the
// chats boards and the palette's "rename chat" from the conversation open one
// prompt, prefilled with the current title. It is a root popup, like the
// project picker, so every surface that names a chat reaches the same one.
// The title is display only: the branch keeps its slug.

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatTitleMaxRunes mirrors the daemon's bound on a rename (api.ChatTitleMaxRunes),
// so the prompt refuses what the daemon would rather than round-tripping.
const chatTitleMaxRunes = 72

// openChatRenameMsg asks the root to raise the rename prompt for one chat.
type openChatRenameMsg struct {
	id    int64
	title string
}

// chatRenamedMsg reports PATCH /v1/chats/{id}.
type chatRenamedMsg struct {
	chat *apiclient.Chat
	err  error
}

// chatRenamePrompt is the one-line prompt.
type chatRenamePrompt struct {
	id    int64
	input textField
	err   string
	busy  bool
}

func newChatRenamePrompt(id int64, title string) *chatRenamePrompt {
	in := newTextField()
	in.SetPlaceholder("the chat's new title")
	in.SetValue(title)
	in.CursorEnd()
	in.Focus()
	return &chatRenamePrompt{id: id, input: in}
}

// update runs the prompt's keyboard: enter renames, esc closes.
func (p *chatRenamePrompt) update(msg tea.KeyPressMsg, client *apiclient.Client) (done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return true, nil
	case "enter":
		if p.busy {
			return false, nil
		}
		title := strings.TrimSpace(p.input.Value())
		switch {
		case title == "":
			p.err = "a chat needs a title"
			return false, nil
		case utf8.RuneCountInString(title) > chatTitleMaxRunes:
			p.err = p.tooLong()
			return false, nil
		case client == nil:
			p.err = "not connected"
			return false, nil
		}
		p.busy, p.err = true, ""
		id := p.id
		return false, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			chat, err := client.RenameChat(ctx, id, title)
			return chatRenamedMsg{chat: chat, err: err}
		}
	}
	p.input, cmd = p.input.Update(msg)
	p.err = ""
	return false, cmd
}

// tooLong says how far over the bound the typed title is. A title typed at
// creation or taken from a task is not bounded, so the prefilled one can be
// over it; renaming is where the bound applies (task 161 decision 4).
func (p *chatRenamePrompt) tooLong() string {
	n := utf8.RuneCountInString(strings.TrimSpace(p.input.Value()))
	return "this title is " + strconv.Itoa(n) + " characters; a rename keeps at most " +
		strconv.Itoa(chatTitleMaxRunes) + " — shorten it"
}

func (p *chatRenamePrompt) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (p *chatRenamePrompt) render(w, _ int) string {
	inner := max(w-2, 10)
	p.input.SetWidth(max(inner-1, 1))
	lines := []string{" " + styleTitle.Render("rename chat #"+strconv.FormatInt(p.id, 10))}
	lines = append(lines, fieldRows(" ", p.input)...)
	switch {
	case p.busy:
		lines = append(lines, styleDim.Render("  renaming…"))
	case p.err != "":
		lines = append(lines, styleBad.Render("  "+p.err))
	case utf8.RuneCountInString(strings.TrimSpace(p.input.Value())) > chatTitleMaxRunes:
		// Said before enter, not after it (review F3): a chat created with
		// a typed title, or on a task, may already be longer than a rename
		// keeps, and the prompt opens holding it.
		lines = append(lines, styleBad.Render("  "+p.tooLong()))
	default:
		lines = append(lines, styleDim.Render("  enter rename · esc cancel · the branch keeps its name"))
	}
	return strings.Join(lines, "\n")
}

// applyRenamed settles the prompt: a refusal stays on it, a rename closes it.
// The chat.updated event repaints every list that shows the title.
func (m *root) applyRenamed(msg chatRenamedMsg) {
	if m.rename == nil {
		return
	}
	if msg.err != nil {
		m.rename.busy, m.rename.err = false, actionErrString(msg.err)
		return
	}
	m.rename = nil
	m.selNotice, m.selNoticeWarn = "chat renamed: "+oneLine(msg.chat.Title), false
}

// paletteExtraser is a surface with key-less palette rows of its own beyond
// the task workspace's: the chat workspace's "rename chat" (task 161).
type paletteExtraser interface {
	paletteExtras() []paletteEntry
}

// paletteExtras is the conversation's "rename chat" row. It has no key: the
// composer owns every printable one (decision 6), so ctrl+p is the way in.
func (v *chatView) paletteExtras() []paletteEntry {
	if v.chat == nil {
		return nil
	}
	msg := openChatRenameMsg{id: v.chat.ID, title: v.chat.Title}
	return append([]paletteEntry{{
		group:  "chat",
		label:  "rename chat — the branch keeps its name",
		action: func() tea.Msg { return msg },
	}}, v.rewindPaletteExtras()...)
}
