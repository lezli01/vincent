package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// ntLabels are the row captions, in row order.
var ntLabels = [ntRowCount]string{
	ntProject:     "project",
	ntWorkflow:    "workflow",
	ntTitle:       "title",
	ntDescription: "description",
	ntFields:      "fields",
	ntBranch:      "base branch",
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
	case ntPriority:
		if n.mode == ntEditing && n.cursor == ntPriority {
			return n.priority.View()
		}
		return firstNonEmpty(strings.TrimSpace(n.priority.Value()), "0") + "  " +
			styleDim.Render("higher runs first · +/-")
	case ntAgent:
		return n.overrideSummary(n.agent)
	case ntModel:
		return n.overrideSummary(n.model)
	case ntEffort:
		return n.overrideSummary(n.effort)
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
	if bad := n.unavailableSteps(*e); len(bad) > 0 {
		out += "  " + styleWarn.Render(fmt.Sprintf("⚠ %d unavailable", len(bad)))
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
// decides — the one thing §8.6 confusion turns into a wrong agent.
func (n *newTask) overrideSummary(v string) string {
	if v == "" {
		return styleDim.Render("(workflow default)")
	}
	return v
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
	out = append(out, styleDim.Render("    enter select · esc cancel"))
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
	for i, s := range e.Steps {
		agent := s.Agent
		suffix := ""
		switch {
		case s.Type != "agent":
			agent = ""
		case agent == "":
			// §8.6 level 4: the adapter's own default. The registry does not
			// resolve it, so it is reported, never accused.
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
	hint := "  ↑↓ move · enter open · ctrl+s create · R re-probe adapters · esc back"
	if n.cursor == ntDescription {
		hint = "  ↑↓ move · enter type · e $EDITOR · ctrl+s create · esc back"
	}
	return []string{styleDim.Render(hint)}
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
