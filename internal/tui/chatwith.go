package tui

// Choosing the agent for a chat on a task or an issue (task 161 decision 7,
// §15). `T` stays instant with the daemon's default; the `chat_with`
// operation and the palette's "chat with…" open this small form first —
// agent, model, effort and an optional first message — then make the same
// call `T` makes with those fields. A target that already has an open chat
// skips the form: the linked chat's agent is fixed, so it is a navigation.

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatWithTarget is what the form opens a chat on: a task or an issue.
type chatWithTarget struct {
	taskID int64
	issue  *apiclient.Issue
	// back is the issue screen the form was opened from, where the chat's
	// esc returns (#932). Unused for a task, whose chat returns to the board.
	back viewID
}

func (t chatWithTarget) label() string {
	if t.issue != nil {
		return "issue #" + strconv.FormatInt(t.issue.ID, 10)
	}
	return "task #" + strconv.FormatInt(t.taskID, 10)
}

// openChatWithMsg asks the root to raise the form.
type openChatWithMsg struct{ target chatWithTarget }

// chatWithAgentsMsg carries the adapters the form offers.
type chatWithAgentsMsg struct {
	agents []apiclient.Agent
	err    error
}

// taskChatWithCmd is `chat_with` on a task target: the form, or — with a chat
// already open — the navigation `T` makes. nil when the key does nothing.
func taskChatWithCmd(client *apiclient.Client, t taskActions, bar *actionBar) tea.Cmd {
	if !t.offersChat() {
		return nil
	}
	if t.openChatID != 0 {
		return taskChatCmd(client, t, bar)
	}
	target := chatWithTarget{taskID: t.id}
	return func() tea.Msg { return openChatWithMsg{target: target} }
}

// issueChatWithCmd is `chat_with` on an issue: the form, or the navigation to
// the chat already holding the issue's main worktree.
func issueChatWithCmd(client *apiclient.Client, iss apiclient.Issue, note func(string, bool), back viewID) tea.Cmd {
	if iss.ID == 0 {
		return nil
	}
	if issueOccupantChat(iss) != 0 {
		return issueChatCmd(client, iss, note, back)
	}
	target := chatWithTarget{issue: &iss, back: back}
	return func() tea.Msg { return openChatWithMsg{target: target} }
}

// cwRow is one row of the form, in tab order.
type cwRow int

const (
	cwAgent cwRow = iota
	cwModel
	cwEffort
	cwMessage
	cwRowCount
)

// chatWithForm is the popup.
type chatWithForm struct {
	target   chatWithTarget
	agents   apiclient.Agents
	agentIdx int
	model    string
	effort   string
	message  textarea.Model
	focus    cwRow
	pick     *picker
	err      string
	busy     bool
	loaded   bool
}

func newChatWithForm(target chatWithTarget) *chatWithForm {
	return &chatWithForm{target: target, message: newFirstMessageEditor()}
}

// init fetches the adapters, narrowed on arrival to those that can resume.
func (f *chatWithForm) init(client *apiclient.Client) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		agents, err := client.ListAgents(ctx, false)
		return chatWithAgentsMsg{agents: agents, err: err}
	}
}

func (f *chatWithForm) applyAgents(msg chatWithAgentsMsg) {
	f.loaded = true
	if msg.err != nil {
		f.err = "could not list agents: " + errString(msg.err)
		return
	}
	f.agents = resumableAgents(msg.agents)
}

func (f *chatWithForm) agent() (apiclient.Agent, bool) {
	if f.agentIdx < 0 || f.agentIdx >= len(f.agents) {
		return apiclient.Agent{}, false
	}
	return f.agents[f.agentIdx], true
}

func (f *chatWithForm) agentName() string {
	a, _ := f.agent()
	return a.Name
}

func (f *chatWithForm) setAgent(name string) {
	for i, a := range f.agents {
		if a.Name == name && i != f.agentIdx {
			// A catalog belongs to its adapter: values chosen under another
			// one would submit a pair that never existed.
			f.agentIdx, f.model, f.effort = i, "", ""
		}
	}
}

// hasEfforts reports whether the selected adapter has effort levels; cursor
// has none (§9.7), and the row is skipped for it.
func (f *chatWithForm) hasEfforts() bool {
	a, ok := f.agent()
	return !ok || len(a.Efforts) > 0
}

func (f *chatWithForm) moveFocus(delta int) {
	next := (f.focus + cwRow(delta) + cwRowCount) % cwRowCount
	if next == cwEffort && !f.hasEfforts() {
		next = (next + cwRow(delta) + cwRowCount) % cwRowCount
	}
	f.focus = next
	if f.focus == cwMessage {
		f.message.Focus()
	} else {
		f.message.Blur()
	}
}

// update runs the form's keyboard. done reports that the popup should close.
func (f *chatWithForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (done bool, cmd tea.Cmd) {
	if f.pick != nil {
		res := f.pick.update(msg)
		if res.chosen {
			switch cwRow(f.pick.row) {
			case cwAgent:
				f.setAgent(res.value)
			case cwModel:
				f.model = res.value
			case cwEffort:
				f.effort = res.value
			case cwMessage, cwRowCount:
			}
		}
		if res.closed {
			f.pick = nil
		}
		return false, res.cmd
	}
	key := msg.String()
	if f.focus == cwMessage {
		multi := strings.Contains(f.message.Value(), "\n")
		switch {
		case key == "enter" || key == "ctrl+s":
			return f.submit(client)
		case key == "esc", key == "tab", key == "shift+tab",
			!multi && (key == "up" || key == "down"):
		default:
			f.message, cmd = f.message.Update(msg)
			return false, cmd
		}
	}
	switch key {
	case "esc":
		return true, nil
	case "ctrl+s":
		return f.submit(client)
	case "tab", "down":
		f.moveFocus(1)
	case "shift+tab", "up":
		f.moveFocus(-1)
	case "left", "right":
		if f.focus == cwAgent && len(f.agents) > 0 {
			delta := 1
			if key == "left" {
				delta = -1
			}
			f.setAgent(f.agents[(f.agentIdx+delta+len(f.agents))%len(f.agents)].Name)
		}
	case "enter":
		f.openRow()
	}
	return false, nil
}

func (f *chatWithForm) openRow() {
	a, _ := f.agent()
	switch f.focus {
	case cwAgent:
		opts := make([]pickerOption, 0, len(f.agents))
		for _, ag := range f.agents {
			opts = append(opts, pickerOption{value: ag.Name, label: ag.Name, note: ag.Version})
		}
		f.pick = newPicker(int(cwAgent), "agent", opts, false, f.agentName())
	case cwModel:
		f.pick = newPicker(int(cwModel), "model", catalogPickerOptions(a.Models, a.DefaultModel), true, f.model)
	case cwEffort:
		f.pick = newPicker(int(cwEffort), "effort", catalogPickerOptions(a.Efforts, a.DefaultEffort), true, f.effort)
	case cwMessage, cwRowCount:
	}
}

// catalogPickerOptions is a catalog led by the agent's own default, with each
// value's provenance — newChatForm.catalogOptions without the form.
func catalogPickerOptions(opts []apiclient.AgentOption, def string) []pickerOption {
	out := make([]pickerOption, 0, len(opts)+1)
	out = append(out, pickerOption{value: "", label: "(agent default)", note: def})
	for _, o := range opts {
		out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
	}
	return out
}

// submit makes the call `T` makes, with the chosen fields. The popup closes:
// the outcome lands where `T`'s does — the workspace on success, the screen's
// own note on a refusal.
func (f *chatWithForm) submit(client *apiclient.Client) (bool, tea.Cmd) {
	if client == nil {
		f.err = "not connected"
		return false, nil
	}
	if f.busy {
		return false, nil
	}
	f.busy = true
	message := f.message.Value()
	if strings.TrimSpace(message) == "" {
		message = ""
	}
	agentName, model, effort := f.agentName(), f.model, f.effort
	if iss := f.target.issue; iss != nil {
		id, pid, title, back := iss.ID, iss.ProjectID, issueChatTitle(*iss), f.target.back
		return true, func() tea.Msg {
			// worktreeTimeout, not actionTimeout: the daemon places the
			// issue's main worktree before it answers.
			chat, err := client.CreateChat(context.Background(), apiclient.CreateChatRequest{
				ProjectID: pid, Title: title, IssueID: &id,
				Agent: agentName, Model: model, Effort: effort, Message: message,
			})
			if err != nil {
				if chatID, live := apiclient.IssueHasLiveChat(err); live {
					return issueChatOpenedMsg{issueID: id, projectID: pid, chatID: chatID, back: back}
				}
				return issueChatOpenedMsg{issueID: id, projectID: pid, err: err, back: back}
			}
			out := issueChatOpenedMsg{issueID: id, projectID: pid, chatID: chat.ID, warnings: chat.Warnings, back: back}
			out.draft, out.note = unsentFirstMessage(chat, message)
			return out
		}
	}
	id := f.target.taskID
	return true, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.OpenTaskChat(ctx, id, apiclient.OpenTaskChatRequest{
			Agent: agentName, Model: model, Effort: effort, Message: message,
		})
		if err != nil {
			if chatID, locked := apiclient.TaskLockedByChat(err); locked {
				return taskChatOpenedMsg{taskID: id, chatID: chatID}
			}
			return taskChatOpenedMsg{taskID: id, err: err}
		}
		out := taskChatOpenedMsg{taskID: id, chatID: chat.ID}
		out.draft, out.note = unsentFirstMessage(chat, message)
		return out
	}
}

func (f *chatWithForm) paste(text string) tea.Cmd {
	if f.pick != nil {
		return f.pick.paste(text)
	}
	if f.focus != cwMessage {
		return nil
	}
	var cmd tea.Cmd
	f.message, cmd = f.message.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (f *chatWithForm) render(w, _ int) string {
	inner := max(w-2, 20)
	f.message.SetWidth(max(inner-12, 10))
	a, _ := f.agent()
	value := func(v, def string) string {
		if v != "" {
			return v
		}
		return styleDim.Render(strings.TrimSpace("(agent default) " + def))
	}
	agentName := f.agentName()
	switch {
	case agentName == "" && !f.loaded:
		agentName = styleDim.Render("loading…")
	case agentName == "":
		agentName = styleDim.Render("the daemon's default")
	}
	rows := []struct {
		row   cwRow
		label string
		value []string
	}{
		{cwAgent, "agent", []string{agentName + styleDim.Render("   ← → step · enter list")}},
		{cwModel, "model", []string{value(f.model, a.DefaultModel)}},
		{cwEffort, "effort", []string{value(f.effort, a.DefaultEffort)}},
		{cwMessage, "message", strings.Split(f.message.View(), "\n")},
	}
	lines := []string{" " + styleTitle.Render("chat with… on "+f.target.label()), ""}
	for _, r := range rows {
		if r.row == cwEffort && !f.hasEfforts() {
			continue
		}
		marker := "  "
		if r.row == f.focus && f.pick == nil {
			marker = "▸ "
		}
		lines = append(lines, indentRows(marker+padRight(r.label, 9), r.value)...)
		if r.row == f.focus && f.pick != nil {
			f.pick.setWidth(inner)
			lines = append(lines, f.pick.renderBody()...)
		}
	}
	lines = append(lines, "")
	switch {
	case f.err != "":
		lines = append(lines, styleBad.Render("  "+f.err))
	case f.pick != nil:
		lines = append(lines, styleDim.Render("  ↑/↓ choose · enter pick · esc close the list"))
	default:
		lines = append(lines, styleDim.Render("  enter on the message or ctrl+s opens the chat · ctrl+j newline · esc cancel"))
	}
	return strings.Join(lines, "\n")
}
