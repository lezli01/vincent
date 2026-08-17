package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// ntLabels are the row captions, in row order.
var ntLabels = [ntRowCount]string{
	ntProject:     "project",
	ntWorkflow:    "workflow",
	ntTitle:       "title",
	ntDescription: "description",
	ntFields:      "fields",
	ntBranch:      "base branch",
	ntBranchName:  "branch",
	ntPriority:    "priority",
	ntAgent:       "agent",
	ntModel:       "model",
	ntEffort:      "effort",
	ntCreate:      "",
}

// render draws the form. The width is unused: every row is a short line and
// the description is wrapped by the textarea, which was sized on resize.
func (n *newTask) render(_, height int) string {
	if n.loadErr != nil {
		return fmt.Sprintf("\n  %s\n\n  press R to retry\n",
			styleBad.Render("could not load the form: "+errString(n.loadErr)))
	}
	if !n.loaded {
		return "\n  loading projects, workflows and adapters…\n"
	}
	if len(n.projects) == 0 {
		return "\n  no projects registered yet — add one in the Projects view (4)\n"
	}

	lines := make([]string, 0, int(ntRowCount)+12)
	cursorLine := 0
	for row := ntProject; row < ntRowCount; row++ {
		if row == n.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, n.renderRow(row))
		lines = append(lines, n.renderExpansion(row)...)
	}
	lines = append(lines, "")
	lines = append(lines, n.statusLines()...)
	return strings.Join(window(lines, cursorLine, height), "\n")
}

func (n *newTask) renderRow(row ntRow) string {
	cursor := "  "
	if row == n.cursor && n.mode != ntConfirming {
		cursor = styleFocus.Render("› ")
	}
	if row == ntCreate {
		label := "create task"
		if n.submitting {
			label = "creating…"
		}
		return cursor + styleKey.Render("[ "+label+" ]") + "  " +
			styleDim.Render("enter · ctrl+s from anywhere")
	}
	line := fmt.Sprintf("%s%-12s %s", cursor, ntLabels[row], n.rowValue(row))
	if msg, bad := n.rowErr[row]; bad {
		line += "  " + styleBad.Render("⚠ "+msg)
	}
	return line
}

// rowValue is the one-line summary a collapsed row shows.
func (n *newTask) rowValue(row ntRow) string {
	switch row {
	case ntProject:
		p, ok := n.project()
		if !ok {
			return styleDim.Render("(pick one)")
		}
		return p.Name + "  " + styleDim.Render(p.Path)
	case ntWorkflow:
		return n.workflowSummary()
	case ntTitle:
		if n.mode == ntEditing && n.cursor == ntTitle {
			return n.titleIn.View()
		}
		if n.titleText() == "" {
			return styleDim.Render("(required)")
		}
		return n.titleText()
	case ntDescription:
		return n.descriptionSummary()
	case ntFields:
		if len(n.fields) == 0 {
			return styleDim.Render("none · enter to add")
		}
		parts := make([]string, 0, len(n.fields))
		for _, f := range n.fields {
			parts = append(parts, f.key+"="+f.value)
		}
		return strings.Join(parts, "  ")
	case ntBranch:
		if n.mode == ntEditing && n.cursor == ntBranch {
			return n.branch.View()
		}
		return firstNonEmpty(strings.TrimSpace(n.branch.Value()), styleDim.Render("(project default)"))
	case ntBranchName:
		if n.mode == ntEditing && n.cursor == ntBranchName {
			return n.branchName.View()
		}
		return n.branchNameValue()
	case ntPriority:
		if n.mode == ntEditing && n.cursor == ntPriority {
			return n.priority.View()
		}
		return firstNonEmpty(strings.TrimSpace(n.priority.Value()), "0") + "  " +
			styleDim.Render("higher runs first · +/-")
	case ntAgent:
		return n.agentSummary()
	case ntModel:
		return n.overrideSummary(n.model, apiclient.ModelOf)
	case ntEffort:
		return n.overrideSummary(n.effort, apiclient.EffortOf)
	case ntCreate, ntRowCount:
	}
	return ""
}

func (n *newTask) workflowSummary() string {
	e := n.workflowEntry(n.workflow)
	if e == nil {
		if n.workflow == "" {
			return styleDim.Render("(pick one)")
		}
		return n.workflow
	}
	out := e.Name + "  " + styleDim.Render(e.Scope)
	if !e.Valid() {
		return out + "  " + styleBad.Render("✗ invalid")
	}
	if !e.RunsHere() {
		return out + "  " + styleBad.Render("✗ "+e.PlatformNote())
	}
	if bad := n.unavailableSteps(*e); len(bad) > 0 {
		out += "  " + styleWarn.Render(fmt.Sprintf("⚠ %d unavailable", len(bad)))
	}
	if e.RequiresInput {
		// Said on the workflow row because it is a fact about the workflow;
		// the agent row is where it becomes an error (§7.4, task 013).
		out += "  " + styleDim.Render("· needs an interactive agent")
	}
	return out
}

func (n *newTask) descriptionSummary() string {
	text := strings.TrimSpace(n.desc.Value())
	if n.mode == ntEditing && n.cursor == ntDescription {
		return ""
	}
	if text == "" {
		return styleDim.Render("(none) · enter to type, e for $EDITOR")
	}
	first, _, more := strings.Cut(text, "\n")
	if more {
		return first + styleDim.Render(fmt.Sprintf(" … (%d lines)", strings.Count(text, "\n")+1))
	}
	return first
}

// overrideSummary states plainly that an unset override means the workflow
// decides — the one thing §8.6 confusion turns into a wrong agent — and then
// names what it decides. The names come from POST /v1/resolve, so the form
// reports the daemon's own resolution instead of re-deriving §8.6 (T4.7).
//
// Until the resolution arrives (or when it failed) the suffix is simply
// absent: "(workflow default)" alone is incomplete, never wrong.
func (n *newTask) overrideSummary(v string, field func(apiclient.ResolvedStep) *apiclient.ResolvedField) string {
	if v != "" {
		return v
	}
	return styleDim.Render("(workflow default" + n.resolvedSuffix(field) + ")")
}

// agentSummary is overrideSummary for the agent row. It exists separately
// because an unresolved agent still names a value — §8.6 level 4 is the
// daemon's default adapter, not "nothing".
func (n *newTask) agentSummary() string {
	if n.agent != "" {
		return n.agent
	}
	return styleDim.Render("(workflow default" + n.resolvedAgents() + ")")
}

// resolvedAgents renders the distinct agents the draft's agent steps resolve
// to, in step order: " → claude", " → claude, codex" when they differ, and
// nothing at all before the resolution lands or when the workflow has no
// agent steps to run.
func (n *newTask) resolvedAgents() string {
	res, ok := n.resolved()
	if !ok {
		return ""
	}
	names := res.Agents()
	if len(names) == 0 {
		return "" // a workflow with no agent steps: the override is moot
	}
	return " → " + strings.Join(names, ", ")
}

// resolvedSuffix is resolvedAgents for model and effort, where §8.6 level 4
// may genuinely name nothing: an adapter that reports no default of its own
// leaves the choice to the CLI at run time, and that is what gets rendered
// rather than a guessed model name.
func (n *newTask) resolvedSuffix(field func(apiclient.ResolvedStep) *apiclient.ResolvedField) string {
	res, ok := n.resolved()
	if !ok {
		return ""
	}
	values, unnamed := res.Values(field)
	if unnamed {
		values = append(values, "CLI default")
	}
	if len(values) == 0 {
		return ""
	}
	return " → " + strings.Join(values, ", ")
}

// renderExpansion draws whatever the focused row opened underneath it.
func (n *newTask) renderExpansion(row ntRow) []string {
	if row != n.cursor {
		return nil
	}
	switch {
	case n.mode == ntPicking && n.pick != nil && ntRow(n.pick.row) == row:
		return n.renderPicker()
	case n.mode == ntFieldsOpen && row == ntFields:
		return n.renderFields()
	case n.mode == ntEditing && row == ntDescription:
		return append(strings.Split(n.desc.View(), "\n"),
			styleDim.Render("    esc leaves the field · e (from the row) opens $EDITOR"))
	}
	if row == ntWorkflow && n.mode == ntNavigating {
		return n.renderWorkflowDetail(n.workflow)
	}
	return nil
}

func (n *newTask) renderPicker() []string {
	p := n.pick
	out := p.renderBody()
	if ntRow(p.row) == ntWorkflow {
		if opt, ok := p.current(); ok {
			out = append(out, n.renderWorkflowDetail(opt.value)...)
		}
	}
	switch ntRow(p.row) {
	case ntAgent, ntModel, ntEffort:
		out = append(out, styleDim.Render(
			"    replaces the workflow's defaults; steps that pin their own keep them (§8.6)"))
	case ntProject, ntWorkflow, ntTitle, ntDescription, ntFields, ntBranch, ntPriority, ntCreate, ntRowCount:
	}
	hint := "    enter select · esc cancel"
	if len(p.options) > pickerWindow {
		hint = "    enter select · / filter · esc cancel"
	}
	out = append(out, styleDim.Render(hint))
	return out
}

// renderWorkflowDetail is §15's "description + step list, flagging steps
// whose agent is unavailable".
func (n *newTask) renderWorkflowDetail(name string) []string {
	e := n.workflowEntry(name)
	if e == nil {
		return nil
	}
	out := []string{}
	if e.Description != "" {
		out = append(out, styleDim.Render("    "+e.Description))
	}
	// The resolution describes the *committed* workflow; the picker previews
	// other entries, and those keep the registry's own text rather than a
	// resolution fetched for a different draft.
	res, resolved := n.resolved()
	resolved = resolved && name == n.workflow
	for i, s := range e.Steps {
		agent := s.Agent
		suffix := ""
		if resolved && i < len(res.Steps) && res.Steps[i].Agent != nil {
			agent = res.Steps[i].Agent.Value
		}
		switch {
		case s.Type != "agent":
			agent = ""
		case agent == "":
			// §8.6 level 4 with no resolution to hand: reported, never accused.
			agent = "adapter default"
		case n.agents.Unavailable(agent):
			suffix = "  " + styleWarn.Render("⚠ unavailable")
		}
		line := fmt.Sprintf("    %d. %-24s %s", i+1, firstNonEmpty(s.Name, s.ID), styleDim.Render(s.Type))
		if agent != "" {
			line += "  " + styleDim.Render(agent)
		}
		out = append(out, line+suffix)
	}
	for _, f := range e.Errors {
		out = append(out, styleBad.Render("    ✗ "+f.Message))
	}
	return out
}

func (n *newTask) renderFields() []string {
	f := n.fieldsEd
	out := []string{styleDim.Render("    custom fields — available to templates as .Task.Fields:")}
	for i, r := range f.rows {
		marker := "  "
		if i == f.cursor {
			marker = styleFocus.Render("▸ ")
		}
		key, value := r.key, r.value
		if i == f.cursor && f.editing == 1 {
			key = f.input.View()
		}
		if i == f.cursor && f.editing == 2 {
			value = f.input.View()
		}
		out = append(out, "    "+marker+firstNonEmpty(key, styleDim.Render("(key)"))+" = "+
			firstNonEmpty(value, styleDim.Render("(empty)")))
	}
	if len(f.rows) == 0 {
		out = append(out, styleDim.Render("      none yet"))
	}
	if f.err != "" {
		out = append(out, styleWarn.Render("    ⚠ "+f.err))
	}
	out = append(out, styleDim.Render("    a add · enter edit · d delete · esc done"))
	return out
}

func (n *newTask) statusLines() []string {
	switch {
	case n.mode == ntConfirming:
		return []string{styleWarn.Render("  discard this draft? y/n")}
	case n.err != "":
		return []string{styleBad.Render("  ⚠ " + n.err)}
	case n.submitting:
		return []string{styleDim.Render("  creating…")}
	}
	// The key hints live in the registry footer (T3.12); only form *state*
	// earns a line here.
	return nil
}

// priorityValue is the integer the form would send, for tests and for the
// nudge keys.
func (n *newTask) priorityValue() int {
	v, err := strconv.Atoi(strings.TrimSpace(n.priority.Value()))
	if err != nil {
		return 0
	}
	return v
}

// branchNameValue renders the branch row when it is not being edited: the name
// the daemon says this draft would get, and which level of the chain decided it.
//
// The preview comes from POST /v1/resolve rather than from rendering the template
// here. Branch naming is precedence resolution, and the PR L decision keeps
// resolution server-side — a form that rendered its own would be a second
// implementation to keep in step with the daemon's forever.
func (n *newTask) branchNameValue() string {
	typed := strings.TrimSpace(n.branchName.Value())
	res, ok := n.resolved()
	if !ok || res.Branch == nil {
		// No resolution yet. Show what was typed, or say nothing rather than
		// guess a name.
		return firstNonEmpty(typed, styleDim.Render("(from the project template)"))
	}
	b := res.Branch
	return b.Value + "  " + styleDim.Render("("+b.Explain()+")")
}
