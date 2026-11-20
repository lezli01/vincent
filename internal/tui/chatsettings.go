package tui

import (
	"context"
	"fmt"
	"runtime"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// A chat's run settings between turns (task 160 decision 9): the chip on the
// composer border, the alt+p model-and-effort picker, and the shift+tab
// permission-mode toggle that asks before raising to full-auto (§16, decision
// 4). Every change is a PATCH /v1/chats/{id}; the daemon holds the settings
// and refuses what the adapter cannot do, so nothing here is a second copy of
// those rules beyond what keeps a key from offering a refusal.

const (
	modeFullAuto   = "full-auto"
	modeRestricted = "restricted"
)

// chatSettingsChip is the composer border's model · effort · mode.
func chatSettingsChip(c *apiclient.Chat) string {
	if c == nil {
		return ""
	}
	model, effort := c.Model, c.Effort
	if model == "" {
		model = "default model"
	}
	if effort == "" {
		effort = "default effort"
	}
	mode := c.PermissionMode
	if mode == "" {
		mode = modeFullAuto
	}
	return model + " · " + effort + " · " + mode
}

// chatPickerRow is one choosable value in the picker: a model or an effort,
// "" being the adapter's default.
type chatPickerRow struct {
	field string // "model" | "effort"
	value string
}

func (r chatPickerRow) label() string {
	v := r.value
	if v == "" {
		v = "default"
	}
	return r.field + ": " + v
}

// chatSettingsPicker is the alt+p list. It owns the keyboard while open.
type chatSettingsPicker struct {
	rows   []chatPickerRow
	cursor int
}

// newChatSettingsPicker lists a, the chat agent's catalog, the way the
// new-chat form sources its options (§9.6).
func newChatSettingsPicker(a apiclient.Agent, c *apiclient.Chat) *chatSettingsPicker {
	p := &chatSettingsPicker{}
	add := func(field string, opts []apiclient.AgentOption, current string) {
		p.rows = append(p.rows, chatPickerRow{field: field})
		for _, o := range opts {
			p.rows = append(p.rows, chatPickerRow{field: field, value: o.Value})
			if o.Value == current && current != "" {
				p.cursor = len(p.rows) - 1
			}
		}
	}
	add("model", a.Models, c.Model)
	add("effort", a.Efforts, "")
	return p
}

func (p *chatSettingsPicker) render(width int) []string {
	out := []string{" " + styleDim.Render("model and effort for the next turns — enter choose, esc close")}
	for i, r := range p.rows {
		line := "   " + r.label()
		if i == p.cursor {
			line = " " + styleSelected.Render("▸ "+r.label())
		}
		out = append(out, ansi.Truncate(line, width, "…"))
	}
	return out
}

// openSettingsPicker opens the picker, or says why it cannot.
func (v *chatView) openSettingsPicker() {
	c := v.chat
	switch {
	case c == nil:
		return
	case chatstate.Terminal(chatstate.State(c.State)):
		v.note, v.noteBad = "this chat has ended; its settings no longer change", true
		return
	}
	a, ok := v.agents[c.Agent]
	switch {
	case !ok:
		v.note, v.noteBad = "the agent catalog has not loaded yet", true
	case c.SessionID != "" && a.CannotChangeOnResume():
		v.note, v.noteBad = c.Agent+" cannot change model or effort on a resumed session; open a new chat to change them", true
	default:
		v.picker = newChatSettingsPicker(a, c)
		v.note = ""
		v.hideInline("")
		v.hideFiles()
	}
}

// updatePickerKey is the picker's keyboard.
func (v *chatView) updatePickerKey(msg tea.KeyPressMsg) tea.Cmd {
	p := v.picker
	switch msg.String() {
	case "esc":
		v.picker = nil
	case "up":
		p.cursor = max(p.cursor-1, 0)
	case "down":
		p.cursor = min(p.cursor+1, len(p.rows)-1)
	case "enter":
		v.picker = nil
		if len(p.rows) == 0 {
			return nil
		}
		r := p.rows[p.cursor]
		value := r.value
		req := apiclient.PatchChatRequest{Model: &value}
		if r.field == "effort" {
			req = apiclient.PatchChatRequest{Effort: &value}
		}
		return v.patchSettingsCmd(req)
	}
	return nil
}

// toggleMode is shift+tab: lower to restricted at once, ask before raising to
// full-auto, and say why when only one mode is legal (decision 2).
func (v *chatView) toggleMode() tea.Cmd {
	c := v.chat
	if c == nil {
		return nil
	}
	if chatstate.Terminal(chatstate.State(c.State)) {
		v.note, v.noteBad = "this chat has ended; its settings no longer change", true
		return nil
	}
	if c.PermissionMode == modeRestricted {
		v.raising = true
		v.note = ""
		v.hideInline("")
		v.hideFiles()
		return nil
	}
	if why := v.cannotRestrict(c.Agent); why != "" {
		v.note, v.noteBad = why, true
		return nil
	}
	mode := modeRestricted
	return v.patchSettingsCmd(apiclient.PatchChatRequest{PermissionMode: &mode})
}

// cannotRestrict says why a chat on name can only run full-auto, or "".
func (v *chatView) cannotRestrict(name string) string {
	if name == "codex" {
		// §9.3: `codex exec resume` has no --sandbox, so a resumed turn
		// cannot be restricted; the daemon refuses it the same way.
		return "codex cannot keep a resumed turn restricted, so this chat stays full-auto"
	}
	if a, ok := v.agents[name]; ok && a.CannotRestrict() {
		return fmt.Sprintf("%s cannot run restricted on %s, so this chat stays full-auto", name, runtime.GOOS)
	}
	return ""
}

// raiseQuestion is the full-auto confirmation's text.
func (v *chatView) raiseQuestion() string {
	return "raise this chat to full-auto? its agent will run any command as you, without asking"
}

// chatPatchedMsg is a PATCH's answer.
type chatPatchedMsg struct {
	chatID int64
	chat   *apiclient.Chat
	err    error
}

func (v *chatView) patchSettingsCmd(req apiclient.PatchChatRequest) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil {
		v.note, v.noteBad = "not connected", true
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.PatchChat(ctx, id, req)
		return chatPatchedMsg{chatID: id, chat: chat, err: err}
	}
}

func (v *chatView) applyPatched(msg chatPatchedMsg) {
	if msg.chatID != v.chatID {
		return
	}
	if msg.err != nil {
		v.note, v.noteBad = errString(msg.err), true
		return
	}
	v.chat = msg.chat
	v.note, v.noteBad = "next turns run on "+chatSettingsChip(msg.chat), false
}
