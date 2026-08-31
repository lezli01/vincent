package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The new-chat form (§15, task 067), a layer over the chats board rather than
// a seventh view: it is opened from there, it returns there, and it means
// nothing anywhere else.
//
// It is deliberately smaller than the new-task form. A chat has no workflow,
// no fields, no github issue and no scheduling, so the whole form is project,
// title, agent, model, effort and base branch — and only the first two are
// required.

// chatCreatedMsg reports the outcome of POST /v1/chats.
type chatCreatedMsg struct {
	chat *apiclient.Chat
	err  error
}

// newChatFieldsMsg carries what the form needs to offer choices: the
// registered projects and the adapters that can hold a conversation.
type newChatFieldsMsg struct {
	projects []apiclient.Project
	agents   []apiclient.Agent
	err      error
}

// newChatForm is the create form.
type newChatForm struct {
	client *apiclient.Client

	projects []apiclient.Project
	agents   []apiclient.Agent

	projectID int64
	agentIdx  int

	title  textinput.Model
	model  textinput.Model
	effort textinput.Model
	base   textinput.Model

	// focus indexes the fields in tab order: project, title, agent, model,
	// effort, base.
	focus int

	err        string
	submitting bool
}

// newChatFields is the number of focusable fields.
const newChatFields = 6

func newNewChatForm(client *apiclient.Client, hintProject int64) *newChatForm {
	f := &newChatForm{client: client, projectID: hintProject}
	f.title = textinput.New()
	f.title.Placeholder = "what is this conversation about"
	f.model = textinput.New()
	f.model.Placeholder = "model override (optional)"
	f.effort = textinput.New()
	f.effort.Placeholder = "effort override (optional)"
	f.base = textinput.New()
	f.base.Placeholder = "base branch (optional — the project's default)"
	f.focus = 1
	f.title.Focus()
	return f
}

// init fetches the projects and adapters the pickers offer.
func (f *newChatForm) init() tea.Cmd {
	client := f.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return newChatFieldsMsg{err: err}
		}
		agents, _ := client.ListAgents(ctx, false)
		return newChatFieldsMsg{projects: projects, agents: agents}
	}
}

// capturesInput is true for every row of an open draft, not only the four
// text ones — a recorded exception to PR L's rule that a form captures input
// "only while a text row is in edit mode" (task 067 decision 6, §15 amended
// 2026-08-31).
//
// The rule is not changed anywhere else: `newTask.capturesInput` still
// answers false while navigating, and widening it globally would make `?`,
// `:`, `!` and `M` unreachable from every form in the TUI. The exception is
// scoped to this form because four of its six rows are text fields and a live
// draft sits behind the two that are not, so `q` on the project row quit the
// TUI with the draft (issue #279). `esc` stays the way out.
func (f *newChatForm) capturesInput() bool { return true }

func (f *newChatForm) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	switch f.focus {
	case 1:
		f.title, cmd = f.title.Update(tea.PasteMsg{Content: text})
	case 3:
		f.model, cmd = f.model.Update(tea.PasteMsg{Content: text})
	case 4:
		f.effort, cmd = f.effort.Update(tea.PasteMsg{Content: text})
	case 5:
		f.base, cmd = f.base.Update(tea.PasteMsg{Content: text})
	}
	return cmd
}

// applyFields records the pickers' contents, defaulting the project to the
// hint and the agent to the first adapter that can resume — which is the
// daemon's own default, so the form and the API agree before anyone types.
func (f *newChatForm) applyFields(msg newChatFieldsMsg) {
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.projects, f.agents = msg.projects, resumableAgents(msg.agents)
	if f.projectID == 0 && len(f.projects) > 0 {
		f.projectID = f.projects[0].ID
	}
}

// resumableAgents is the agent picker's contents: only adapters that can hold
// a conversation (decision row 29). The daemon's `agent_cannot_resume` refusal
// stays the authority — applyFailure still renders it — this only keeps the
// picker from offering a choice that is certain to be refused.
//
// It follows the `input_verdict` precedent: a daemon too old to send
// `supports_resume` says nothing about any adapter, and nothing is dropped on
// the strength of a field that was never sent.
func resumableAgents(agents []apiclient.Agent) []apiclient.Agent {
	var out []apiclient.Agent
	for _, a := range agents {
		if !a.CannotResume() {
			out = append(out, a)
		}
	}
	return out
}

// applyFailure puts the daemon's refusal on the form rather than closing it.
//
// `agent_cannot_resume` is rendered as the typed refusal it is: an adapter
// that cannot resume its own session cannot hold a conversation, and telling
// the human to pick another adapter is a different sentence from "creation
// failed" (task 063 decision 3).
func (f *newChatForm) applyFailure(err error) {
	f.submitting = false
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Code == "agent_cannot_resume" {
		f.err = "that agent cannot resume its own session, so it cannot hold a " +
			"conversation — pick one that can"
		return
	}
	f.err = errString(err)
}

// update runs the form's keyboard. done reports that the layer should close.
func (f *newChatForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (cmd tea.Cmd, done bool) {
	f.client = client
	switch msg.String() {
	case "esc":
		return nil, true
	case "tab", "down":
		f.moveFocus(1)
		return nil, false
	case "shift+tab", "up":
		f.moveFocus(-1)
		return nil, false
	case "left":
		f.cycle(-1)
		return nil, false
	case "right":
		f.cycle(1)
		return nil, false
	case "ctrl+s", "enter":
		if f.focus == 1 || msg.String() == "ctrl+s" {
			return f.submit(), false
		}
		f.moveFocus(1)
		return nil, false
	}
	switch f.focus {
	case 1:
		f.title, cmd = f.title.Update(msg)
	case 3:
		f.model, cmd = f.model.Update(msg)
	case 4:
		f.effort, cmd = f.effort.Update(msg)
	case 5:
		f.base, cmd = f.base.Update(msg)
	}
	return cmd, false
}

func (f *newChatForm) moveFocus(delta int) {
	f.focus = (f.focus + delta + newChatFields) % newChatFields
	f.title.Blur()
	f.model.Blur()
	f.effort.Blur()
	f.base.Blur()
	switch f.focus {
	case 1:
		f.title.Focus()
	case 3:
		f.model.Focus()
	case 4:
		f.effort.Focus()
	case 5:
		f.base.Focus()
	}
}

// cycle steps the two picker fields. The text fields ignore it: left and
// right are cursor movement there.
func (f *newChatForm) cycle(delta int) {
	switch f.focus {
	case 0:
		if len(f.projects) == 0 {
			return
		}
		i := 0
		for j, p := range f.projects {
			if p.ID == f.projectID {
				i = j
				break
			}
		}
		f.projectID = f.projects[(i+delta+len(f.projects))%len(f.projects)].ID
	case 2:
		if len(f.agents) == 0 {
			return
		}
		f.agentIdx = (f.agentIdx + delta + len(f.agents)) % len(f.agents)
	}
}

func (f *newChatForm) agentName() string {
	if f.agentIdx < 0 || f.agentIdx >= len(f.agents) {
		return ""
	}
	return f.agents[f.agentIdx].Name
}

func (f *newChatForm) submit() tea.Cmd {
	title := strings.TrimSpace(f.title.Value())
	if title == "" {
		f.err = "a chat needs a title"
		return nil
	}
	if f.projectID == 0 {
		f.err = "pick a project"
		return nil
	}
	client := f.client
	if client == nil {
		f.err = "not connected"
		return nil
	}
	f.err = ""
	f.submitting = true
	req := apiclient.CreateChatRequest{
		ProjectID: f.projectID, Title: title, Agent: f.agentName(),
		Model:      strings.TrimSpace(f.model.Value()),
		Effort:     strings.TrimSpace(f.effort.Value()),
		BaseBranch: strings.TrimSpace(f.base.Value()),
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.CreateChat(ctx, req)
		return chatCreatedMsg{chat: chat, err: err}
	}
}

func (f *newChatForm) projectName() string {
	for _, p := range f.projects {
		if p.ID == f.projectID {
			return p.Name
		}
	}
	if f.projectID == 0 {
		return "—"
	}
	return strconv.FormatInt(f.projectID, 10)
}

func (f *newChatForm) render(width, height int) string {
	rows := []struct{ label, value string }{
		{"project", f.projectName() + styleDim.Render("   ← → to change")},
		{"title", f.title.View()},
		{"agent", f.agentLabel()},
		{"model", f.model.View()},
		{"effort", f.effort.View()},
		{"base", f.base.View()},
	}
	lines := []string{" " + styleTitle.Render("new chat"), ""}
	for i, r := range rows {
		marker := "  "
		if i == f.focus {
			marker = "▸ "
		}
		lines = append(lines, marker+padRight(r.label, 9)+r.value)
	}
	lines = append(lines, "")
	switch {
	case f.submitting:
		lines = append(lines, " "+styleDim.Render("creating…"))
	case f.err != "":
		lines = append(lines, " "+styleBad.Render(f.err))
	default:
		lines = append(lines, " "+styleDim.Render("ctrl+s create · esc cancel"))
	}
	_ = width
	_ = height
	return strings.Join(lines, "\n")
}

func (f *newChatForm) agentLabel() string {
	name := f.agentName()
	if name == "" {
		name = styleDim.Render("the daemon's default")
	}
	return name + styleDim.Render("   ← → to change")
}
