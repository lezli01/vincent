package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The wide new-task screen (task 150.11): everything the draft decides on one
// screen, instead of the guided form's six stages. The workflows are on the
// left, the form is in the middle, and what the draft would run is on the right.
//
// This is a drawing of the same form. The rows, their order, the cursor and every
// key are newTask's own, so tab walks the same fields in the same order at any
// width, and a narrower terminal keeps the guided form.

// frameless reports that the form draws its own panes.
func (n *newTask) frameless() bool {
	return n.loaded && n.loadErr == nil && len(n.projects) > 0 &&
		n.width >= mgmtWideMinW && n.height >= formWideMinH
}

// ntWidePaneWidths is the workflow list's and the run preview's outer widths.
func ntWidePaneWidths(width int) (list, run int) {
	side := min(max(width*22/100, 30), 42)
	return side, side
}

func (n *newTask) renderWide(width, height int) string {
	listW, runW := ntWidePaneWidths(width)
	out := n.workflowPane(listW, height)
	out = joinColumns(out, n.widePane(width-listW-runW, height))
	out = joinColumns(out, n.runPane(runW, height))
	// The screen as text, and where its panes start, for a click (#1065).
	n.wideText = n.wideText[:0]
	for _, l := range out {
		n.wideText = append(n.wideText, ansi.Strip(l))
	}
	n.wideListW, n.wideFormEnd = listW, width-runW
	return strings.Join(out, "\n")
}

// updateClick is a click on the wide form (#1065): a workflow card chooses
// that workflow, and the create button submits, as enter on it does.
func (n *newTask) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft || !n.frameless() || n.mode != ntNavigating || n.submitting {
		return nil
	}
	if msg.X < n.wideListW {
		for _, h := range n.wfHits {
			if h.y == msg.Y {
				n.cursor = ntWorkflow
				return n.applyPick(ntWorkflow, h.value, false)
			}
		}
		return nil
	}
	if msg.X >= n.wideFormEnd || msg.Y < 0 || msg.Y >= len(n.wideText) {
		return nil
	}
	line := []rune(n.wideText[msg.Y])
	for _, label := range []string{"[ create task ]", "[ hand off to task ]"} {
		at := strings.Index(string(line), label)
		if at < 0 {
			continue
		}
		x0 := len([]rune(string(line)[:at]))
		if msg.X >= x0 && msg.X < x0+len([]rune(label)) {
			return n.submit()
		}
	}
	return nil
}

// updateWideKey is the wide form's spatial keys: ← goes to the workflow list on
// the left, where ↑/↓ choose a workflow, and → or enter comes back to the field
// it left. tab keeps walking every field in order, the same as on any width.
func (n *newTask) updateWideKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool) {
	if !n.frameless() {
		return nil, false
	}
	key := msg.String()
	if n.cursor != ntWorkflow {
		switch key {
		case "left":
			n.formRow, n.cursor = n.cursor, ntWorkflow
			return nil, true
		case "up", "k":
			// The top of the form is the top: the list is beside it, not above.
			return nil, n.rowAbove() == ntWorkflow
		}
		return nil, false
	}
	switch key {
	case "up", "k":
		return n.stepWorkflow(-1), true
	case "down", "j":
		return n.stepWorkflow(1), true
	case "right", "enter":
		n.enterForm()
		return nil, true
	case opKey(keymap.Filter):
		n.openPicker(ntWorkflow)
		n.pick.filtering = true
		n.pick.filter.Focus()
		return nil, true
	}
	return nil, false
}

// rowAbove is the focusable row before the cursor, or the cursor's own.
func (n *newTask) rowAbove() ntRow {
	for row := n.cursor - 1; row >= ntProject; row-- {
		if n.focusable(row) {
			return row
		}
	}
	return n.cursor
}

// enterForm moves from the workflow list into the form: to the field ← left, or
// the form's first.
func (n *newTask) enterForm() {
	if n.formRow > ntWorkflow && n.focusable(n.formRow) {
		n.cursor = n.formRow
		return
	}
	for row := ntWorkflow + 1; row < ntRowCount; row++ {
		if n.focusable(row) {
			n.cursor = row
			return
		}
	}
}

// stepWorkflow chooses the next workflow down the list, past any that cannot be
// chosen, and stops at either end.
func (n *newTask) stepWorkflow(delta int) tea.Cmd {
	options := n.workflowOptions()
	at := slices.IndexFunc(options, func(o pickerOption) bool { return o.value == n.workflow })
	for i := at + delta; i >= 0 && i < len(options); i += delta {
		if !options[i].disabled {
			return n.applyPick(ntWorkflow, options[i].value, false)
		}
	}
	return nil
}

// liveBindings keeps the wide form's keys off every other width.
func (n *newTask) liveBindings(rows []binding) []binding {
	return slices.DeleteFunc(slices.Clone(rows), func(b binding) bool {
		return b.context == ctxNewTask && b.wideForm && !n.frameless()
	})
}

// workflowScopeName heads one group of the workflow list.
func workflowScopeName(scope string) string {
	switch scope {
	case scopeProject:
		return "This project"
	case "global":
		return "Global"
	}
	return "Built-in"
}

// workflowMeta says how long a workflow is and how often it stops for you.
func workflowMeta(e apiclient.WorkflowEntry) string {
	meta := plural(len(e.Steps), "step", "steps")
	gates := 0
	for _, s := range e.Steps {
		if s.Type == "manual" {
			gates++
		}
	}
	if gates > 0 {
		meta += " · " + plural(gates, "gate", "gates")
	}
	return meta
}

// workflowPicking reports that the workflow list has the keyboard.
func (n *newTask) workflowPicking() bool {
	return n.mode == ntPicking && n.pick != nil && ntRow(n.pick.row) == ntWorkflow
}

// workflowPane lists the workflows as cards grouped by where they come from.
// While the list is open its cursor is the highlighted card.
func (n *newTask) workflowPane(w, h int) []string {
	inner := max(w-4, 1)
	options := n.workflowOptions()
	current := n.workflow
	var body []string
	if n.workflowPicking() {
		p := n.pick
		if opt, ok := p.current(); ok {
			current = opt.value
		}
		narrowed := make([]pickerOption, 0, len(p.matches))
		for _, i := range p.matches {
			narrowed = append(narrowed, p.options[i])
		}
		options = narrowed
		if p.filtering || p.filter.Value() != "" {
			p.filter.SetWidth(max(inner-2, 10))
			body = append(body, fieldRows(toneBlue.Render(ic("⌕ ")), p.filter)...)
		}
	}
	from, to := 0, 0
	var tags []string
	for _, scope := range []string{scopeProject, "global", "builtin"} {
		heading := false
		for _, opt := range options {
			e := n.workflowEntry(opt.value)
			if e == nil || (workflowScopeName(e.Scope) != workflowScopeName(scope)) {
				continue
			}
			if !heading {
				if len(body) > 0 {
					body = append(body, "")
				}
				body = append(body, styleDim.Render(workflowScopeName(scope)))
				heading = true
			}
			selected := opt.value == current
			if selected {
				from, to = len(body), len(body)+4
			}
			card := workflowCard(*e, opt, selected, inner)
			for len(tags) < len(body) {
				tags = append(tags, "")
			}
			for range card {
				if opt.disabled {
					tags = append(tags, "")
				} else {
					tags = append(tags, opt.value)
				}
			}
			body = append(body, card...)
		}
	}
	right, edges, border := toneBlue.Render(strconv.Itoa(len(options))), edgesRounded, styleDim
	if n.cursor == ntWorkflow && n.mode != ntConfirming {
		right = styleDim.Render("↑↓ choose  ") + right
		edges, border = edgesThick, styleFocus
	}
	start := windowRangeStart(len(body), from, to, max(h-2, 1))
	n.wfHits = n.wfHits[:0]
	for k := 0; k < max(h-2, 1) && start+k < len(tags); k++ {
		if v := tags[start+k]; v != "" {
			n.wfHits = append(n.wfHits, ntWorkflowHit{y: 1 + k, value: v})
		}
	}
	return drawBox(toneBlue.Bold(true).Render(ic("⟡ Workflow")), right, windowRange(body, from, to, max(h-2, 1)), w, h, edges, border)
}

// ntWorkflowHit is one line of a workflow card in the wide list.
type ntWorkflowHit struct {
	y     int
	value string
}

// workflowCard is one workflow. One that cannot be chosen says why.
func workflowCard(e apiclient.WorkflowEntry, opt pickerOption, selected bool, w int) []string {
	second, style := e.Description, styleDim
	if opt.disabled {
		second, style = opt.note, styleBad
	}
	return workflowCardLines(e, stylePlain, second, style, selected, w)
}

// workflowCardLines draws one workflow card, the shape the new-task list and
// the Workflows screen share: the name and its length on top, one line about it
// below, and a thick border on the selected card so selection does not rely on
// colour.
func workflowCardLines(e apiclient.WorkflowEntry, name lipgloss.Style, second string, secondStyle lipgloss.Style, selected bool, w int) []string {
	inner := max(w-4, 1)
	edges, border := edgesRounded, styleDim
	if selected {
		name, edges, border = styleTitle, edgesThick, styleFocus
	}
	head := padBetweenCells(name.Render(ansi.Truncate(ic("⟡ ")+oneLine(e.Name), max(inner-12, 8), "…")), styleDim.Render(workflowMeta(e)), inner)
	tail := secondStyle.Render(ansi.Truncate(oneLine(second), inner, "…"))
	return drawBox("", "", []string{head, tail}, w, wfCardH, edges, border)
}

// widePane is the form itself.
func (n *newTask) widePane(w, h int) []string {
	inner := max(w-4, 1)
	text := max(inner-4, 10)
	n.detailWidth = text
	n.desc.SetWidth(text)
	n.titleIn.SetWidth(text)
	var s formStack

	if n.rowVisible(ntSource) {
		s.add(formSection(ic("▦ From"), "", strings.Split(n.rowValue(ntSource), "\n"), inner, n.cursor == ntSource), n.cursor == ntSource)
	}
	s.add(formSection("Title", styleDim.Render("what should the agent do?"), n.titleRows(), inner, n.cursor == ntTitle), n.cursor == ntTitle)
	s.add(formSection(ic("≡ Description"), toneBlue.Render(opKey(keymap.Editor))+styleDim.Render(" $EDITOR"), n.descriptionRows(text), inner, n.cursor == ntDescription), n.cursor == ntDescription)
	s.add(formSection(ic("▤ Fields"), n.fieldsNote(), n.fieldRows(text), inner, n.cursor == ntFields), n.cursor == ntFields)

	s.add(n.gitAndStart(inner))

	if n.mode == ntPicking && n.pick != nil && !n.workflowPicking() {
		n.pick.setWidth(text)
		s.add(formSection(n.pick.heading, "", n.renderPicker(), inner, true), true)
	}
	if note := n.mainCheckoutConsequence(); note != "" {
		s.add(styled(wrapText(note, inner, wrapOpts{}), styleWarn), false)
	}
	s.add([]string{n.renderRow(ntCreate)}, n.cursor == ntCreate)
	s.add(n.statusLines(), false)

	missing := n.missing()
	pill := formPill(true, ic("✓ ready"))
	if len(missing) > 0 {
		pill = formPill(false, attentionBadge+" "+strconv.Itoa(len(missing))+" to fill")
	}
	return drawBox(styleTitle.Render(tabGlyph(viewHome)+" "+n.title()), pill, s.view(max(h-2, 1)), w, h, edgesRounded, styleDim)
}

// titleRows is the title section's body.
func (n *newTask) titleRows() []string {
	if n.mode == ntEditing && n.cursor == ntTitle {
		return n.titleIn.rows()
	}
	rows := []string{firstNonEmpty(n.titleText(), styleDim.Render("(required) enter to type"))}
	if msg, bad := n.rowErr[ntTitle]; bad {
		rows = append(rows, styleBad.Render("⚠ "+msg))
	}
	return rows
}

// ntDescriptionRows is how much of a description the closed section shows.
const ntDescriptionRows = 4

// descriptionRows is the description section's body.
func (n *newTask) descriptionRows(width int) []string {
	if n.mode == ntEditing && n.cursor == ntDescription {
		return strings.Split(n.desc.View(), "\n")
	}
	text := strings.TrimSpace(n.desc.Value())
	if text == "" {
		return []string{styleDim.Render("(none) enter to type")}
	}
	var rows []string
	for _, para := range strings.Split(text, "\n") {
		rows = append(rows, wrapText(para, width, wrapOpts{})...)
	}
	if hidden := len(rows) - ntDescriptionRows; hidden > 0 {
		rows = append(rows[:ntDescriptionRows-1:ntDescriptionRows-1], styleDim.Render("… "+plural(hidden+1, "more line", "more lines")))
	}
	return rows
}

// fieldsNote marks what the star in the fields section means, when one shows.
func (n *newTask) fieldsNote() string {
	for _, f := range n.fields {
		if f.declared && f.definition.Required {
			return styleWarn.Render("* required")
		}
	}
	return ""
}

// fieldKind names a field's type in words.
func fieldKind(f kv) string {
	if !f.declared {
		return "custom"
	}
	switch f.definition.Type {
	case apiclient.WorkflowFieldEnum:
		if f.definition.Multiple {
			return "many"
		}
		return "choice"
	case apiclient.WorkflowFieldInteger, apiclient.WorkflowFieldNumber:
		return "number"
	case apiclient.WorkflowFieldBoolean:
		return "yes / no"
	}
	return "text"
}

// fieldLabelW is the label column of the fields section.
const fieldLabelW = 22

// fieldRows is the fields section's body: the editor while it is open.
func (n *newTask) fieldRows(width int) []string {
	if n.mode == ntFieldsOpen || n.mode == ntFieldPicking {
		return n.renderFields()
	}
	if len(n.fields) == 0 {
		return []string{styleDim.Render("none · enter to add")}
	}
	rows := make([]string, 0, len(n.fields))
	for _, f := range n.fields {
		name := f.key
		if f.declared {
			name = f.definition.DisplayLabel()
		}
		label := padRight(ansi.Truncate(oneLine(name), fieldLabelW-3, "…"), fieldLabelW-2)
		mark := "  "
		if f.declared && f.definition.Required {
			mark = styleWarn.Render("* ")
		}
		value := f.value
		switch {
		case value != "":
		case f.declared && f.definition.Required:
			value = styleWarn.Render("(required)")
		default:
			value = styleDim.Render("(optional)")
		}
		left := label + mark + ansi.Truncate(oneLine(value), max(width-fieldLabelW-12, 8), "…")
		rows = append(rows, padBetweenCells(left, toneCyan.Render(fieldKind(f)), width))
	}
	return rows
}

// gitAndStart is the two boxes side by side: where the work happens, and when
// and with what it starts.
func (n *newTask) gitAndStart(inner int) (block []string, focused bool) {
	half := (inner - 1) / 2
	gitRows := []ntRow{ntWorktree, ntMergeBack, ntBranch, ntBranchName}
	startRows := []ntRow{ntPriority, ntPaused, ntAgent, ntModel, ntEffort}
	git, gitFocus := n.wideRows(gitRows, half-4)
	start, startFocus := n.wideRows(startRows, inner-1-half-4)
	git, start = formEqualRows(git, start)
	title := ic("⎇ Branch")
	if n.rowVisible(ntWorktree) {
		title = ic("⎇ Worktree")
	}
	return formBeside(
		formSection(title, "", git, half, gitFocus),
		formSection(ic("◷ Start"), "", start, inner-1-half, startFocus),
	), gitFocus || startFocus
}

// ntWideLabelW is the label column of the two boxes under the fields.
const ntWideLabelW = 13

// wideRows draws the visible rows of one box, and says whether the cursor is on
// one of them.
func (n *newTask) wideRows(rows []ntRow, width int) (out []string, focused bool) {
	for _, row := range rows {
		if !n.rowVisible(row) {
			continue
		}
		here := row == n.cursor && n.mode != ntConfirming
		focused = focused || here
		value, _, _ := strings.Cut(n.wideValue(row), "\n")
		line := formRowMark(here) + styleDim.Render(padRight(ntLabels[row], ntWideLabelW)) + value
		out = append(out, ansi.Truncate(line, width, "…"))
		if msg, bad := n.rowErr[row]; bad {
			out = append(out, styled(wrapText("⚠ "+msg, max(width-2, 1), wrapOpts{prefix: "  ", indent: 2}), styleBad)...)
		}
	}
	return out, focused
}

// wideValue is what a row says in the two boxes. Most say what they say
// everywhere; the two with a fixed set of answers show them as chips.
func (n *newTask) wideValue(row ntRow) string {
	switch row {
	case ntPaused:
		return formChip("now", !n.paused) + " " + formChip("paused", n.paused)
	case ntPriority:
		if n.mode == ntEditing && n.cursor == ntPriority {
			return n.priority.View()
		}
		return styleDim.Render("− ") + firstNonEmpty(strings.TrimSpace(n.priority.Value()), "0") + styleDim.Render(" +")
	}
	return n.rowValue(row)
}

// missing is what still stands between this draft and a task, in words.
func (n *newTask) missing() []string {
	var out []string
	if n.workflow == "" {
		out = append(out, "a workflow")
	}
	if n.titleText() == "" {
		out = append(out, "title is required")
	}
	for _, f := range n.fields {
		if f.declared && f.definition.Required && strings.TrimSpace(f.value) == "" {
			out = append(out, f.definition.DisplayLabel()+" is required")
		}
	}
	return out
}

// footerSummary says how the draft stands, for the status bar.
func (n *newTask) footerSummary(width int) string {
	if !n.frameless() || width < mgmtWideMinW {
		return ""
	}
	out := " " + styleDim.Render(n.workflow+"  ·  ")
	if missing := n.missing(); len(missing) > 0 {
		return out + styleWarn.Render(attentionBadge+" "+missing[0]) + "  "
	}
	return out + styleOK.Render(ic("✓ ")+"ready to create") + "  "
}

// runPane says what the draft would do, as the daemon resolves it.
func (n *newTask) runPane(w, h int) []string {
	inner := max(w-4, 1)
	row := func(k, v string) string { return padBetweenCells(styleDim.Render(k), v, inner) }

	foot := []string{"", styleTitle.Render(ic("⎇ Where it runs"))}
	foot = append(foot,
		row("worktree", n.runWorktree()),
		row("branch", toneCyan.Render(ansi.Truncate(n.runBranch(), max(inner-10, 8), "…"))))
	if base := strings.TrimSpace(n.branch.Value()); base != "" && !n.sideTask() {
		foot = append(foot, row("from", ansi.Truncate(base, max(inner-8, 8), "…")))
	}
	when := "when a slot is free"
	if n.paused {
		when = "paused until you resume"
	}
	foot = append(foot, "", styleTitle.Render(ic("◷ When")), row("start", when),
		"", styleTitle.Render(ic("✓ Before you create")))
	foot = append(foot, n.runChecks(inner)...)

	body := n.runAbout(inner)
	body = append(body, n.runSteps(max(h-2-len(foot)-len(body), 2))...)
	return drawBox(styleTitle.Render("What will run"), "", append(body, foot...), w, h, edgesRounded, styleDim)
}

// previewedWorkflow is the workflow the right pane describes: the one under the
// list's cursor while the list is open, so choosing is informed, else the draft's.
func (n *newTask) previewedWorkflow() string {
	if n.workflowPicking() {
		if opt, ok := n.pick.current(); ok {
			return opt.value
		}
	}
	return n.workflow
}

// runAbout is the previewed workflow's description and whatever is wrong with
// it, whole: the card on the left has room for one line of either (#791).
func (n *newTask) runAbout(width int) []string {
	e := n.workflowEntry(n.previewedWorkflow())
	if e == nil {
		return nil
	}
	var out []string
	if e.Description != "" {
		out = append(out, styled(wrapText(e.Description, width, wrapOpts{}), styleDim)...)
	}
	for _, f := range e.Errors {
		out = append(out, styled(wrapText("✗ "+f.Message, width, wrapOpts{indent: 2}), styleBad)...)
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

// runWorktree says where the draft's work would happen.
func (n *newTask) runWorktree() string {
	if b, ok := n.adoptedBranch(); ok && b.MainCheckout {
		return styleWarn.Render("your checkout")
	}
	switch {
	case n.sideTask():
		return "its own"
	case n.worktreeChoice():
		return "the issue's main"
	}
	return "a new one"
}

// runBranch names the branch the draft would run on.
func (n *newTask) runBranch() string {
	if n.sideTask() {
		return "off the issue's main"
	}
	if typed := strings.TrimSpace(n.branchName.Value()); typed != "" {
		return typed
	}
	if res, ok := n.resolved(); ok && res.Branch != nil {
		return res.Branch.Value
	}
	return "from the project template"
}

// runChecks lists what the draft has and what it still lacks.
func (n *newTask) runChecks(width int) []string {
	var out []string
	if n.titleText() != "" {
		out = append(out, styleOK.Render(ic("✓ "))+"title")
	}
	if e := n.workflowEntry(n.workflow); e != nil {
		if bad := n.unavailableSteps(*e); len(bad) > 0 {
			out = append(out, styleWarn.Render(attentionBadge+" ")+plural(len(bad), "step needs", "steps need")+" an unavailable agent")
		} else {
			out = append(out, styleOK.Render(ic("✓ "))+"every step's agent is available")
		}
	}
	for _, m := range n.missing() {
		out = append(out, styleWarn.Render(attentionBadge+" ")+ansi.Truncate(m, max(width-2, 8), "…"))
	}
	return out
}

// runSteps is the workflow's steps as the daemon resolved them, cut to room.
func (n *newTask) runSteps(room int) []string {
	name := n.previewedWorkflow()
	e := n.workflowEntry(name)
	out := []string{styleTitle.Render(ic("▶ Steps"))}
	if e == nil {
		return append(out, styleDim.Render("no workflow chosen"))
	}
	// The resolution describes the committed workflow, never one being previewed.
	res, resolved := n.resolved()
	resolved = resolved && name == n.workflow
	for i, s := range e.Steps {
		if len(out)+2 > room {
			return append(out, styleDim.Render("… "+plural(len(e.Steps)-i, "more step", "more steps")))
		}
		var step *apiclient.ResolvedStep
		if resolved && i < len(res.Steps) {
			step = &res.Steps[i]
		}
		glyph, how := n.stepLook(s, step)
		out = append(out,
			styleDim.Render(strconv.Itoa(i+1)+"  ")+toneMagenta.Render(glyph)+" "+oneLine(firstNonEmpty(s.Name, s.ID)),
			"      "+how)
	}
	return out
}

// stepLook is a step's mark and what it would run with.
func (n *newTask) stepLook(s apiclient.WorkflowEntryStep, r *apiclient.ResolvedStep) (glyph, how string) {
	switch s.Type {
	case "agent":
		agent := s.Agent
		var parts []string
		if r != nil && r.Agent != nil {
			agent = r.Agent.Value
		}
		parts = append(parts, firstNonEmpty(agent, "adapter default"))
		if r != nil && r.Model != nil && r.Model.Value != "" {
			parts = append(parts, r.Model.Value)
		}
		if r != nil && r.Effort != nil && r.Effort.Value != "" {
			parts = append(parts, r.Effort.Value)
		}
		if agent != "" && n.agents.Unavailable(agent) {
			return ic("◈"), styleWarn.Render("⚠ " + agent + " is unavailable")
		}
		return ic("◈"), styleDim.Render(strings.Join(parts, " · "))
	case "manual":
		return "⏸", styleDim.Render("waits for you")
	case "command":
		return "$", styleDim.Render("runs a command")
	}
	return "•", styleDim.Render(s.Type)
}
