package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The wide new-chat screen (task 150.11): the form sits where the conversation
// will be, between the chat list and what the chat would get. The rows, their
// order and their keys are newChatForm's own, so a narrower terminal keeps the
// plain form with nothing to relearn.

// createWide reports that the new-chat form is drawn as the workspace.
func (v *chatsView) createWide() bool {
	return v.create != nil && v.termW >= mgmtWideMinW && v.height >= formWideMinH
}

// renderCreateWide is the chat list, the form and the summary side by side.
func (v *chatsView) renderCreateWide(width, height int) string {
	listW, detailsW := chatPaneWidths(width)
	out := joinColumns(v.chatListPane(listW, height, 0), v.create.widePane(width-listW-detailsW, height))
	if detailsW > 0 {
		out = joinColumns(out, v.create.summaryPane(v.runningChats(), detailsW, height))
	}
	return strings.Join(out, "\n")
}

// runningChats counts the chats working right now.
func (v *chatsView) runningChats() int {
	n := 0
	for _, c := range v.chats {
		if c.State == "running" {
			n++
		}
	}
	return n
}

// chatDraftCard stands in the list for the chat being written.
func chatDraftCard(w int) []string {
	body := []string{styleTitle.Render("A new conversation"), styleDim.Render("appears here once created")}
	return append([]string{toneBlue.Bold(true).Render(ic("✦ New chat"))},
		drawBox("", "", body, w, 4, edgesThick, styleFocus)...)
}

// chatChipMax is the longest catalog drawn as chips. A longer one, such as
// cursor's, is a list behind enter.
const chatChipMax = 6

// widePane is the form as a stack of sections.
func (f *newChatForm) widePane(w, h int) []string {
	inner := max(w-4, 1)
	text := max(inner-4, 10)
	f.title.SetWidth(text)
	var s formStack

	s.add(formSection("Title", styleDim.Render("what is this conversation about"), f.title.rows(), inner, f.focus == ncTitle), f.focus == ncTitle)
	s.add(f.agentSection(inner))
	s.add(f.modelSections(inner))
	s.add(f.branchSection(inner))

	switch {
	case f.submitting:
		s.add([]string{styleDim.Render("creating…")}, false)
	case f.err != "":
		s.add(styled(wrapText("⚠ "+f.err, inner, wrapOpts{}), styleBad), false)
	}
	pill := formPill(true, ic("✓ ready"))
	if strings.TrimSpace(f.title.Value()) == "" {
		pill = formPill(false, attentionBadge+" needs a title")
	}
	return drawBox(styleTitle.Render(ic("✦ New chat")), pill, s.view(max(h-2, 1)), w, h, edgesRounded, styleDim)
}

// chatAgentState says whether an adapter can take a chat right now. An adapter
// that cannot tell whether it is signed in is never reported as signed in.
func chatAgentState(a apiclient.Agent) string {
	switch {
	case !a.Available:
		return styleWarn.Render("⚠ unavailable")
	case a.NotAuthenticated():
		return styleWarn.Render("⚠ not signed in")
	case a.LoggedIn != nil:
		return styleOK.Render(ic("✓ ") + "signed in")
	}
	return styleDim.Render("sign-in unknown")
}

// agentSection is one card per adapter that can hold a conversation.
func (f *newChatForm) agentSection(inner int) (block []string, focused bool) {
	focused = f.focus == ncAgent
	right := styleDim.Render("← → choose")
	body := []string{styleDim.Render("no installed agent can hold a chat")}
	if n := len(f.agents); n > 0 {
		room := inner - 4 - (n - 1)
		cardW := max(room/n, 12)
		body = nil
		for i, a := range f.agents {
			w := cardW
			if i == n-1 {
				w = max(room-cardW*(n-1), 12)
			}
			card := chatAgentCard(a, i == f.agentIdx, w)
			if body == nil {
				body = card
				continue
			}
			body = formBeside(body, card)
		}
	}
	if focused && f.pick != nil {
		f.pick.setWidth(inner - 4)
		body = append(body, f.pick.renderBody()...)
	}
	return formSection(ic("◈ Agent"), right, body, inner, focused), focused
}

// chatAgentCard is one adapter. The filled dot marks the choice without colour.
func chatAgentCard(a apiclient.Agent, selected bool, w int) []string {
	mark, name, edges, border := ic("○"), oneLine(a.Name), edgesRounded, styleDim
	if selected {
		mark, name, edges, border = styleFocus.Render(ic("◉")), styleTitle.Render(oneLine(a.Name)), edgesThick, styleFocus
	}
	return drawBox("", "", []string{mark + " " + name, styleDim.Render(oneLine(a.Version)), chatAgentState(a)}, w, 5, edges, border)
}

// optionChips draws a catalog as chips, led by the agent's own default.
func optionChips(options []apiclient.AgentOption, value string) string {
	chips := []string{formChip("default", value == "")}
	for _, o := range options {
		chips = append(chips, formChip(oneLine(o.Value), o.Value == value))
	}
	return strings.Join(chips, " ")
}

// modelValue is the model row in the section: chips for a short catalog, the
// choice and a count for a long one, and for an adapter that lists no models
// the free-text list, because it still accepts any id.
func (f *newChatForm) modelValue() string {
	models := f.models()
	switch {
	case len(models) == 0:
		return firstNonEmpty(f.model, styleDim.Render("default")) + styleDim.Render("   enter to type a model")
	case len(models) <= chatChipMax:
		return optionChips(models, f.model)
	}
	return firstNonEmpty(f.model, styleDim.Render("default")) + styleDim.Render("   enter: "+strconv.Itoa(len(models))+" models")
}

// modelSections is the model box and, for an adapter that has the concept, the
// effort box beside it.
func (f *newChatForm) modelSections(inner int) (block []string, focused bool) {
	focused = f.focus == ncModel || f.focus == ncEffort
	listing := func(row ncRow, body []string) []string {
		if f.focus == row && f.pick != nil {
			f.pick.setWidth(inner - 4)
			return append(body, f.pick.renderBody()...)
		}
		return body
	}
	if !f.hasEfforts() {
		return formSection("Model", "", listing(ncModel, []string{f.modelValue()}), inner, f.focus == ncModel), focused
	}
	half := (inner - 1) / 2
	model, effort := formEqualRows(
		listing(ncModel, []string{f.modelValue()}),
		listing(ncEffort, []string{optionChips(f.efforts(), f.effort)}))
	return formBeside(
		formSection("Model", "", model, half, f.focus == ncModel),
		formSection("Effort", "", effort, inner-1-half, f.focus == ncEffort),
	), focused
}

// hasEfforts reports that the selected adapter has effort levels to choose from.
// Cursor has none: reasoning depth is part of its model id (§9.7).
func (f *newChatForm) hasEfforts() bool { return len(f.efforts()) > 0 }

// branchSection chooses what the chat runs on. The base row and the existing
// branch row are the form's own two rows; the chips only say which one decides.
func (f *newChatForm) branchSection(inner int) (block []string, focused bool) {
	focused = f.focus == ncBase || f.focus == ncBranch
	text := max(inner-4, 10)
	adopted := strings.TrimSpace(f.branch.Value()) != "" || (f.focus == ncBranch && f.pick != nil)
	f.base.SetWidth(max(text-10, 10))
	body := []string{formChip("A new branch", !adopted) + " " + formChip("A branch that exists", adopted), ""}
	row := func(r ncRow, label, value string) string {
		return formRowMark(f.focus == r && f.pick == nil) + styleDim.Render(padRight(label, 8)) + value
	}
	body = append(body, indentRows(row(ncBase, "from", ""), f.base.rows())...)
	body = append(body, ansi.Truncate(row(ncBranch, "branch", f.branchValue()), text, "…"))
	if f.focus == ncBranch && f.pick != nil {
		f.pick.setWidth(text)
		body = append(body, f.pick.renderBody()...)
	}
	if f.branchErr != "" {
		body = append(body, styled(wrapText("⚠ "+f.branchErr, text, wrapOpts{}), styleBad)...)
	}
	return formSection(ic("⎇ Branch"), "", body, inner, focused), focused
}

// summaryPane says what the chat would get.
func (f *newChatForm) summaryPane(running, w, h int) []string {
	inner := max(w-4, 1)
	row := func(k, v string) string { return padBetweenCells(styleDim.Render(k), oneLine(v), inner) }
	body := []string{styleTitle.Render(ic("◈ Agent")), row("agent", firstNonEmpty(f.agentName(), "daemon default"))}
	body = append(body, row("model", firstNonEmpty(f.model, f.defaultModel(), "default")))
	if f.hasEfforts() {
		body = append(body, row("effort", firstNonEmpty(f.effort, f.defaultEffort(), "default")))
	}
	body = append(body, row("permission", "full-auto"), "", styleTitle.Render(ic("⎇ Where it works")))

	name := strings.TrimSpace(f.branch.Value())
	if name == "" {
		base := firstNonEmpty(strings.TrimSpace(f.base.Value()), f.projectDefaultBranch(), "the default branch")
		body = append(body, toneCyan.Render(ansi.Truncate("vincent/{id}-…", inner, "…")), row("from", base), row("worktree", "a new one"))
	} else {
		where := "a new one"
		for _, b := range f.branches {
			if b.Name == name && b.MainCheckout {
				where = "your checkout"
			}
		}
		body = append(body, toneCyan.Render(ansi.Truncate(oneLine(name), inner, "…")), row("worktree", where), row("when archived", "branch is kept"))
	}
	body = append(body, "", styleTitle.Render(ic("◷ Room")), row("chats running", strconv.Itoa(running)), "")
	body = append(body, styled(wrapText("The agent can run commands as you. The worktree keeps its changes apart from other work.", inner, wrapOpts{}), styleDim)...)
	return drawBox(styleTitle.Render("This chat"), "", body, w, h, edgesRounded, styleDim)
}

// projectDefaultBranch is the selected project's default branch, if known.
func (f *newChatForm) projectDefaultBranch() string {
	for _, p := range f.projects {
		if p.ID == f.projectID {
			return p.DefaultBranch
		}
	}
	return ""
}

// footerSummary says how the draft stands, for the status bar.
func (f *newChatForm) footerSummary() string {
	out := " " + styleDim.Render(firstNonEmpty(f.agentName(), "agent")+"  ·  ")
	if strings.TrimSpace(f.title.Value()) == "" {
		return out + styleWarn.Render(attentionBadge+" a chat needs a title") + "  "
	}
	return out + styleOK.Render(ic("✓ ")+"ready to create") + "  "
}

// stepOption moves a short catalog's value along its chips, wrapping through the
// agent's own default.
func stepOption(options []apiclient.AgentOption, value string, delta int) string {
	values := make([]string, 0, len(options)+1)
	values = append(values, "")
	at := 0
	for i, o := range options {
		values = append(values, o.Value)
		if o.Value == value {
			at = i + 1
		}
	}
	return values[(at+delta+len(values))%len(values)]
}
