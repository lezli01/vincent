package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

// The Overview's cards (task 162.7). The right-hand Details pane is gone
// (task 162 decision 1); what it said — where the task's work lives and what
// it has cost — is two cards side by side under the frame.

// progressBar is one segment per workflow step, coloured by how far the task
// has got: done, running, stopped, or not yet. "" when the task has no steps
// to draw.
func (t *taskView) progressBar(width int) string {
	d := t.detail
	steps := d.railSteps()
	n := len(steps)
	if n == 0 || width < 8 {
		return ""
	}
	room := width - 2 - (n - 1)
	seg := max(room/n, 1)
	parts := make([]string, 0, n)
	for _, s := range steps {
		bar := strings.Repeat("━", seg)
		state := d.railState(s)
		switch {
		case d.railCurrent(s) && d.task.State == stateBlocked:
			parts = append(parts, styleBad.Render(bar))
		case d.railCurrent(s) && (d.task.State == stateAwaitingGate || d.task.State == stateAwaitingInput):
			parts = append(parts, styleWarn.Render(bar))
		case state == "running":
			parts = append(parts, styleFocus.Render(bar))
		case state == "":
			parts = append(parts, styleDim.Render(strings.Repeat("─", seg)))
		default:
			style, ok := stepStateStyle(state)
			if !ok {
				style = styleDim
			}
			parts = append(parts, style.Render(bar))
		}
	}
	return "  " + strings.Join(parts, " ")
}

// overviewCards are the Linked and Usage cards, side by side where they fit
// and stacked where they do not.
func (t *taskView) overviewCards(width int) []string {
	d := t.detail
	if !d.loaded {
		return nil
	}
	linked, usage := t.linkedCard(), t.usageCard()
	inner := max(width-2, 10)
	if inner < 70 {
		out := drawBox(styleTitle.Render("Linked"), "", linked, inner, len(linked)+2, edgesRounded, styleDim)
		out = append(out, drawBox(styleTitle.Render("Usage"), "", usage, inner, len(usage)+2, edgesRounded, styleDim)...)
		return indentLines(out)
	}
	h := max(len(linked), len(usage)) + 2
	left := inner * 55 / 100
	leftBox := drawBox(styleTitle.Render("Linked"), "", linked, left-1, h, edgesRounded, styleDim)
	for i := range leftBox {
		leftBox[i] += " "
	}
	out := joinColumns(leftBox, drawBox(styleTitle.Render("Usage"), "", usage, inner-left, h, edgesRounded, styleDim))
	return indentLines(out)
}

func indentLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "  " + l
	}
	return out
}

// cardFact is one label/value row of a card.
func cardFact(label, value string) string {
	return styleDim.Render(fmt.Sprintf("%-8s ", label)) + value
}

// linkedCard is what the task is tied to: its issue (and the key that opens
// it), its pull request, its chat, and its branch and base.
func (t *taskView) linkedCard() []string {
	task := t.detail.task
	var out []string
	if iss := task.Issue; iss != nil {
		line := cardFact(ic("◇")+" issue", toneMagenta.Render(fmt.Sprintf("#%d", iss.ID))+" "+oneLine(iss.Title)) +
			"  " + styleDim.Render(iss.State)
		line += "  " + styleKey.Render(openIssueKey)
		out = append(out, line)
	} else {
		out = append(out, styleDim.Render(fmt.Sprintf("%-8s no linked issue", ic("◇")+" issue")))
	}
	if task.GitHubPull != nil {
		out = append(out, cardFact(ic("⇡")+" PR", "linked · the Pull request tab"))
	} else {
		out = append(out, styleDim.Render(fmt.Sprintf("%-8s none", ic("⇡")+" PR")))
	}
	switch {
	case task.OpenChatID != nil:
		out = append(out, cardFact(ic("◌")+" chat", "#"+strconv.FormatInt(*task.OpenChatID, 10)))
	case t.target().offersChat() && opKey(keymap.Chat) != "":
		out = append(out, styleDim.Render(fmt.Sprintf("%-8s none · ", ic("◌")+" chat"))+styleKey.Render(opKey(keymap.Chat))+styleDim.Render(" starts one"))
	}
	if task.BranchName != "" {
		out = append(out, cardFact(ic("⎇")+" branch", toneCyan.Render(oneLine(task.BranchName))))
	}
	if base := task.BaseDisplay(); base != "" {
		out = append(out, cardFact("  base", base))
	}
	return out
}

// usageCard is how long the task has run, what it has spent, and what it runs.
func (t *taskView) usageCard() []string {
	d := t.detail
	task := d.task
	var out []string
	if elapsed, ok := task.Elapsed(d.now()); ok {
		out = append(out, cardFact("elapsed", formatElapsed(elapsed)))
	}
	if task.InputTokens+task.OutputTokens > 0 {
		out = append(out, cardFact("tokens", fmt.Sprintf("%s in · %s out", chatTokens(task.InputTokens), chatTokens(task.OutputTokens))))
	}
	out = append(out, cardFact("cost", formatCost(task.CostUSD)))
	if task.Workflow != "" {
		wf := oneLine(task.Workflow)
		if task.WorkflowOrigin != nil {
			wf += styleDim.Render(" · " + task.WorkflowOrigin.Source())
		}
		out = append(out, cardFact("workflow", wf))
	}
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		out = append(out, cardFact("worktree", ansi.Truncate(*task.WorktreePath, 60, "…")))
	}
	return out
}
