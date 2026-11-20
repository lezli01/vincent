package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

// The Overview's cards (task 168.7). The right-hand Details pane is gone
// (task 168 decision 1); what it said — where the task's work lives and what
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
	return t.cardPair("Linked", t.linkedCard(), "Usage", t.usageCard(), width)
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

// linkedRow is one row of the Linked card: its text, and what enter on it
// opens — nil for a row that is a fact rather than a place.
type linkedRow struct {
	text string
	open func() tea.Cmd
}

// linkedRows are what the task is tied to: its issue (and the key that opens
// it), its pull request, its chat, and its branch and base.
func (t *taskView) linkedRows() []linkedRow {
	task := t.detail.task
	var out []linkedRow
	if iss := task.Issue; iss != nil {
		out = append(out, linkedRow{
			text: cardFact(ic("◇")+" issue", toneMagenta.Render(fmt.Sprintf("#%d", iss.ID))+" "+oneLine(iss.Title)) +
				"  " + styleDim.Render(iss.State) + "  " + styleKey.Render(openIssueKey),
			open: t.openIssueCmd,
		})
	} else {
		out = append(out, linkedRow{text: styleDim.Render(fmt.Sprintf("%-8s no linked issue", ic("◇")+" issue"))})
	}
	if t.pullTabAvailable() {
		out = append(out, linkedRow{
			text: cardFact(ic("⇡")+" PR", "linked · the Pull request tab"),
			open: func() tea.Cmd { return t.setTab(taskTabPull) },
		})
	} else {
		out = append(out, linkedRow{text: styleDim.Render(fmt.Sprintf("%-8s none", ic("⇡")+" PR"))})
	}
	switch {
	case task.OpenChatID != nil:
		id, pid := *task.OpenChatID, task.ProjectID
		out = append(out, linkedRow{
			text: cardFact(ic("◌")+" chat", "#"+strconv.FormatInt(id, 10)),
			open: func() tea.Cmd {
				return func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewTask} }
			},
		})
	case t.target().offersChat() && opKey(keymap.Chat) != "":
		out = append(out, linkedRow{text: styleDim.Render(fmt.Sprintf("%-8s none · ", ic("◌")+" chat")) +
			styleKey.Render(opKey(keymap.Chat)) + styleDim.Render(" starts one")})
	}
	if task.BranchName != "" {
		out = append(out, linkedRow{text: cardFact(ic("⎇")+" branch", toneCyan.Render(oneLine(task.BranchName)))})
	}
	if base := task.BaseDisplay(); base != "" {
		out = append(out, linkedRow{text: cardFact("  base", base)})
	}
	return out
}

// linkedCard draws the rows, the cursor's marked.
func (t *taskView) linkedCard() []string {
	rows := t.linkedRows()
	out := make([]string, len(rows))
	for i, r := range rows {
		if i+1 == t.linkedSel {
			out[i] = styleSelected.Render("› " + ansi.Strip(r.text))
			continue
		}
		out[i] = "  " + r.text
	}
	return out
}

// openableRows are the 1-based indexes of the Linked rows enter can open.
func (t *taskView) openableRows() []int {
	var out []int
	for i, r := range t.linkedRows() {
		if r.open != nil {
			out = append(out, i+1)
		}
	}
	return out
}

// moveLinked moves the Linked cursor across the rows it can open: down from
// none lands on the first, up past the first leaves the card.
func (t *taskView) moveLinked(delta int) {
	rows := t.openableRows()
	if len(rows) == 0 {
		t.linkedSel = 0
		return
	}
	at := slices.Index(rows, t.linkedSel)
	switch {
	case at < 0 && delta > 0:
		t.linkedSel = rows[0]
	case at < 0:
		t.linkedSel = 0
	case at+delta < 0:
		t.linkedSel = 0
	default:
		t.linkedSel = rows[min(at+delta, len(rows)-1)]
	}
}

// openLinked is enter on the Linked cursor's row, and false when the cursor
// is not on one.
func (t *taskView) openLinked() (tea.Cmd, bool) {
	rows := t.linkedRows()
	if t.linkedSel < 1 || t.linkedSel > len(rows) || rows[t.linkedSel-1].open == nil {
		return nil, false
	}
	return rows[t.linkedSel-1].open(), true
}

// nowAndTaskCards are the progress frame's two cards (task 168.7): what the
// task is doing now, and what it is for.
func (t *taskView) nowAndTaskCards(width int) []string {
	d := t.detail
	now := renderTaskDetailFactList(max(width/2, 20), t.progressFacts())
	for i := range now {
		now[i] = strings.TrimPrefix(now[i], "  ")
	}
	if run, ok := t.overviewAnchor(); ok && run.Live() {
		if line, ok := d.liveOutputLine(run.ID); ok {
			now = append(now, styleDim.Render("last  ")+ansi.Strip(line))
		}
	}
	about := wrapText(valueOr(strings.TrimSpace(d.task.Description), "no description"),
		max(width/2-8, 16), wrapOpts{maxLines: 4})
	about = append(about, styleDim.Render(fmt.Sprintf("priority %d", d.task.Priority)))
	return t.cardPair("Now", now, "The task", about, width)
}

// cardPair sets two cards side by side where they fit and stacks them where
// they do not, two cells in.
func (t *taskView) cardPair(leftTitle string, left []string, rightTitle string, right []string, width int) []string {
	inner := max(width-2, 10)
	if inner < 70 {
		out := drawBox(styleTitle.Render(leftTitle), "", left, inner, len(left)+2, edgesRounded, styleDim)
		out = append(out, drawBox(styleTitle.Render(rightTitle), "", right, inner, len(right)+2, edgesRounded, styleDim)...)
		return indentLines(out)
	}
	h := max(len(left), len(right)) + 2
	split := inner * 55 / 100
	leftBox := drawBox(styleTitle.Render(leftTitle), "", left, split-1, h, edgesRounded, styleDim)
	for i := range leftBox {
		leftBox[i] += " "
	}
	return indentLines(joinColumns(leftBox, drawBox(styleTitle.Render(rightTitle), "", right, inner-split, h, edgesRounded, styleDim)))
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
