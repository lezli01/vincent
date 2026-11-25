package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// A chat's run settings between turns (task 162 decision 9): the chip on the
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
	// current marks the value the chat runs at now, for each field (review
	// F4): the cursor is on one row, but both fields have a value.
	current bool
}

func (r chatPickerRow) label() string {
	v := r.value
	if v == "" {
		v = "default"
	}
	mark := "  "
	if r.current {
		mark = "• "
	}
	return mark + r.field + ": " + v
}

// chatSettingsPicker is the alt+p list. It owns the keyboard while open.
type chatSettingsPicker struct {
	rows   []chatPickerRow
	cursor int
	// drawn is the rows render last drew, for a click (#1144).
	drawn []clickRow
}

func (p *chatSettingsPicker) clickRows() []clickRow { return p.drawn }

// pointAt is a click on a row (#1144).
func (p *chatSettingsPicker) pointAt(i int) bool {
	if i < 0 || i >= len(p.rows) {
		return false
	}
	p.cursor = i
	return true
}

// newChatSettingsPicker lists a, the chat agent's catalog, the way the
// new-chat form sources its options (§9.6).
func newChatSettingsPicker(a apiclient.Agent, c *apiclient.Chat) *chatSettingsPicker {
	p := &chatSettingsPicker{}
	// add lists one field, "" (the adapter's default) first, marks the
	// chat's current value and returns its row, or -1 when the catalog no
	// longer lists it.
	add := func(field string, opts []apiclient.AgentOption, current string) int {
		at := -1
		p.rows = append(p.rows, chatPickerRow{field: field, current: current == ""})
		if current == "" {
			at = len(p.rows) - 1
		}
		for _, o := range opts {
			if o.Value == "" {
				continue // the default row above is this one
			}
			cur := o.Value == current
			p.rows = append(p.rows, chatPickerRow{field: field, value: o.Value, current: cur})
			if cur {
				at = len(p.rows) - 1
			}
		}
		return at
	}
	model := add("model", a.Models, c.Model)
	effort := add("effort", a.Efforts, c.Effort)
	// The cursor starts on the current model, else the current effort.
	switch {
	case model >= 0:
		p.cursor = model
	case effort >= 0:
		p.cursor = effort
	}
	return p
}

func (p *chatSettingsPicker) render(width int) []string {
	out := []string{" " + styleDim.Render("model and effort for the next turns — enter choose, esc close")}
	p.drawn = p.drawn[:0]
	for i, r := range p.rows {
		line := "   " + r.label()
		if i == p.cursor {
			line = " " + styleSelected.Render("▸ "+r.label())
		}
		line = ansi.Truncate(line, width, "…")
		p.drawn = append(p.drawn, drawnRow(line, i))
		out = append(out, line)
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
		v.status().note("this chat has ended; its settings no longer change", true)
		return
	}
	a, ok := v.agents[c.Agent]
	switch {
	case !ok:
		v.status().note("the agent catalog has not loaded yet", true)
	case c.SessionID != "" && a.CannotChangeOnResume():
		v.status().note(c.Agent+" cannot change model or effort on a resumed session; open a new chat to change them", true)
	default:
		v.picker = newChatSettingsPicker(a, c)
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
		v.status().note("this chat has ended; its settings no longer change", true)
		return nil
	}
	if c.PermissionMode == modeRestricted {
		if v.clampTask != 0 {
			// Decision 3's clamp, as the daemon reported it on an earlier
			// raise: only restricted is legal, so the key says why and asks
			// nothing (decision 2, review F3).
			v.status().note(clampNote(v.clampTask), true)
			return nil
		}
		v.raising = true
		v.hideInline("")
		v.hideFiles()
		return nil
	}
	if why := v.cannotRestrict(c.Agent); why != "" {
		v.status().note(why, true)
		return nil
	}
	mode := modeRestricted
	return v.patchSettingsCmd(apiclient.PatchChatRequest{PermissionMode: &mode})
}

// cannotRestrict says why a chat on name can only run full-auto, or "".
func (v *chatView) cannotRestrict(name string) string {
	a, ok := v.agents[name]
	if !ok {
		return ""
	}
	// The daemon's own answer, from GET /v1/agents (review F3): codex today,
	// whose `exec resume` has no --sandbox (§9.3). The daemon refuses the
	// same way, so the TUI keeps no copy of which adapters these are.
	if a.CannotResumeRestricted() {
		return fmt.Sprintf("%s cannot keep a resumed turn restricted, so this chat stays full-auto", name)
	}
	if a.CannotRestrict() {
		return fmt.Sprintf("%s cannot run restricted on %s, so this chat stays full-auto", name, runtime.GOOS)
	}
	return ""
}

// clampNote is why a chat linked to a restricted task stays restricted.
func clampNote(taskID int64) string {
	return fmt.Sprintf("task #%d runs restricted, so a chat on it can never run full-auto", taskID)
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
		v.status().result("not connected", true)
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
		v.status().result(errString(msg.err), true)
		// A 409 naming a linked task is decision 3's clamp: the chat can
		// never run full-auto. Say that as the reason, and keep it, so the
		// next shift+tab says it without asking first (review F3).
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) && apiErr.Status == http.StatusConflict {
			if id, err := strconv.ParseInt(apiErr.Details["linked_task_id"], 10, 64); err == nil && id > 0 {
				v.clampTask = id
				v.status().result(clampNote(id), true)
			}
		}
		return
	}
	v.chat = msg.chat
	v.status().result("next turns run on "+chatSettingsChip(msg.chat), false)
}
