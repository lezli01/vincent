package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/reasons"
)

// The task workspace's wide layout (task 150.6): a title band, the steps on the
// left, the tabs in the middle, and the task's details on the right. The steps
// and the details are always on screen there, so they stop being tabs; a
// narrower terminal keeps the tabbed layout and reaches them by their digits.

const (
	// taskRailMinW is the narrowest terminal that shows the steps beside the
	// tabs.
	taskRailMinW = 130
	// taskDetailsMinW is the narrowest terminal that also shows the details.
	taskDetailsMinW = 150
	// taskBandH is the title band's outer height.
	taskBandH = 4
)

// wide reports whether the workspace is in its wide layout. It reads the
// terminal rather than the width of the last render: a framed render is narrower
// than the terminal it is drawn on.
func (t *taskView) wide() bool { return t.termW >= taskRailMinW }

// frameless reports that the wide layout draws its own panes.
func (t *taskView) frameless() bool { return t.wide() }

// taskPaneWidths is the rail's and the details' outer widths.
func taskPaneWidths(width int) (rail, details int) {
	rail = min(width*20/100, 38)
	if width >= taskDetailsMinW {
		details = min(width*18/100, 36)
	}
	return rail, details
}

// renderWide draws the wide layout in exactly width×height.
func (t *taskView) renderWide(width, height int) string {
	lines := t.taskBand(width)
	if !t.connected {
		lines = append(lines, styleWarn.Render("  ⚠ daemon unreachable — task data is stale"))
	}
	top := len(lines)
	h := max(height-top, 4)
	railW, detailsW := taskPaneWidths(width)
	midW := width - railW - detailsW

	panes := joinColumns(t.taskRail(railW, h), t.taskMiddle(railW, top, midW, h))
	if detailsW > 0 {
		panes = joinColumns(panes, t.taskDetailsPane(detailsW, h))
	}
	t.railTop, t.railW = top+1, railW
	return strings.Join(append(lines, panes...), "\n")
}

// taskBand is the task's name, state and where it has got to.
func (t *taskView) taskBand(width int) []string {
	d := t.detail
	inner := max(width-4, 1)
	if !d.loaded {
		body := []string{styleTitle.Render(fmt.Sprintf("#%d", d.taskID)), styleDim.Render("loading…")}
		return drawBox("", "", body, width, taskBandH, edgesRounded, styleDim)
	}
	task := d.task.Task
	pill := taskStatePill(task.State)
	room := max(inner-ansi.StringWidth(pill)-2, 1)
	id := "#" + strconv.FormatInt(task.ID, 10) + " "
	title := ansi.Truncate(valueOr(oneLine(task.Title), "untitled task"), max(room-len(id), 1), "…")
	first := padBetweenCells(styleKey.Render(id)+styleTitle.Render(title), pill, inner)
	return drawBox("", "", []string{first, ansi.Truncate(taskBandFacts(task), inner, "…")}, width, taskBandH, edgesRounded, styleDim)
}

// taskBandFacts is the band's second line.
func taskBandFacts(task apiclient.Task) string {
	sep := styleDim.Render("  ·  ")
	var parts []string
	if task.BlockReason != nil && *task.BlockReason != "" {
		parts = append(parts, styleBad.Bold(true).Render(reasons.Explain(*task.BlockReason).Title))
	}
	if task.StepTotal > 0 {
		step := min(task.CurrentStep+1, task.StepTotal)
		parts = append(parts, styleDim.Render(fmt.Sprintf("step %d of %d", step, task.StepTotal)))
	}
	if task.Workflow != "" {
		parts = append(parts, styleDim.Render(oneLine(task.Workflow)))
	}
	if task.BranchName != "" {
		parts = append(parts, styleDim.Render(oneLine(task.BranchName)))
	}
	return strings.Join(parts, sep)
}

// taskRail is the steps, drawn by the timeline the Steps tab draws, so a loop, a
// fan-out and a retry read the same in both places.
func (t *taskView) taskRail(w, h int) []string {
	d := t.detail
	savedW, savedFocus := d.width, d.focus
	d.width, d.focus = max(w-4, 1), focusTimeline
	d.timelineTop = 0
	body := strings.Split(d.renderTimeline(max(h-2, 1)), "\n")
	d.width, d.focus = savedW, savedFocus
	right := ""
	if d.loaded && d.task.StepTotal > 0 {
		right = toneBlue.Render(strconv.Itoa(d.task.StepTotal))
	}
	return drawBox(toneBlue.Bold(true).Render("▤ Steps"), right, body, w, h, edgesRounded, styleDim)
}

// taskMiddle is the tabs and the tab on screen, with a failure pinned under it.
// Its frame is the focus colour: this is the pane the keyboard is in.
func (t *taskView) taskMiddle(railW, top, w, h int) []string {
	inner := max(w-4, 1)
	saved := t.width
	t.width = inner
	strip := t.renderTabs()
	t.width = saved
	// The strip was measured for the root's frame; here it is drawn after the
	// rail, inside this pane's own border and padding.
	for i := range t.tabHits {
		t.tabHits[i].x0 += railW + 1
		t.tabHits[i].x1 += railW + 1
	}
	t.tabY = top + 1
	t.bodyY = t.tabY + 2

	foot := t.taskFailureCard(inner)
	room := max(h-2-2-len(foot), 1)
	t.width = inner
	tab := t.renderTabBody(inner, room)
	t.width = saved
	lines := strings.Split(tab, "\n")
	if len(lines) > room {
		lines = lines[:room]
	}
	body := make([]string, 0, h-2)
	body = append(body, strip, styleDim.Render(strings.Repeat("─", inner)))
	body = append(body, lines...)
	for len(body)+len(foot) < h-2 {
		body = append(body, "")
	}
	body = append(body, foot...)
	return drawBox("", "", body, w, h, edgesRounded, styleFocus)
}

// taskFailureCard is why the task stopped and how to get it going again. The
// Overview's own card says this at length, so it is not repeated there.
func (t *taskView) taskFailureCard(w int) []string {
	d := t.detail
	if !d.loaded || d.task.State != stateBlocked || t.tab == taskTabOverview {
		return nil
	}
	inner := max(w-4, 1)
	var body []string
	if detail := d.task.BlockDetail; detail != nil && strings.TrimSpace(*detail) != "" {
		body = styled(wrapText(oneLine(*detail), inner, wrapOpts{maxLines: 2}), styleTitle)
	}
	if keys := taskRecoveryKeys(d.task.Task); keys != "" {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, ansi.Truncate(keys, inner, "…"))
	}
	title := "✗ This task stopped"
	if r := d.task.BlockReason; r != nil && *r != "" {
		title = "✗ " + reasons.Explain(*r).Title
	}
	return drawBox(styleBad.Bold(true).Render(title), "", body, w, len(body)+2, edgesThick, styleBad)
}

// taskRecoveryKeys names the keys that act on a stopped task right now.
func taskRecoveryKeys(task apiclient.Task) string {
	target := taskActions{id: task.ID, state: task.State, actions: task.AvailableActions}
	var parts []string
	for _, o := range footerActionOps {
		key := opKey(o.op)
		if key == "" || !target.has(o.action) || o.action == apiclient.ActionCancel || o.action == apiclient.ActionArchive {
			continue
		}
		label := o.label
		if label == "" {
			label = actionLabel(target, o.action)
		}
		parts = append(parts, styleKey.Render(key)+" "+label)
	}
	return strings.Join(parts, "    ")
}

// taskDetailsPane is what the task is running with and what it has cost.
func (t *taskView) taskDetailsPane(w, h int) []string {
	d := t.detail
	title := styleTitle.Render("Details")
	if !d.loaded {
		return drawBox(title, "", nil, w, h, edgesRounded, styleDim)
	}
	task := d.task
	inner := max(w-4, 1)
	row := func(k, val string) string {
		room := max(inner-ansi.StringWidth(k)-1, 1)
		return padBetweenCells(styleDim.Render(k), ansi.Truncate(oneLine(val), room, "…"), inner)
	}
	body := []string{styleTitle.Render("⟡ Workflow"), row("name", valueOr(task.Workflow, "—"))}
	if task.WorkflowOrigin != nil {
		body = append(body, row("source", task.WorkflowOrigin.Source()))
	}
	if task.BranchName != "" {
		body = append(body, "", styleTitle.Render("⎇ Branch"), toneCyan.Render(ansi.Truncate(oneLine(task.BranchName), inner, "…")))
		if task.BaseBranch != "" {
			body = append(body, row("base", task.BaseBranch))
		}
	}
	body = append(body, "", styleTitle.Render("◷ Usage"))
	if elapsed, ok := task.Elapsed(d.now()); ok {
		body = append(body, row("elapsed", formatElapsed(elapsed)))
	}
	if tokens := task.InputTokens + task.OutputTokens; tokens > 0 {
		body = append(body, row("tokens", chatTokens(tokens)))
	}
	body = append(body, row("cost", formatCost(task.CostUSD)), "", styleTitle.Render("▦ Linked"))
	links := taskLinks(task.Task)
	if len(links) == 0 {
		links = []string{styleDim.Render("no issue or pull request")}
	}
	body = append(body, links...)
	if key := opKey(keymap.Chat); key != "" && t.target().offersChat() {
		body = append(body, "", styleDim.Render(key+" opens a chat on this task"))
	}
	return drawBox(title, "", body, w, h, edgesRounded, styleDim)
}

// taskLinks names what a task is tied to.
func taskLinks(task apiclient.Task) []string {
	var out []string
	if task.Issue != nil {
		out = append(out, "issue #"+strconv.FormatInt(task.Issue.ID, 10))
	}
	if task.GitHubPull != nil {
		out = append(out, "pull request linked")
	}
	if task.OpenChatID != nil {
		out = append(out, "chat #"+strconv.FormatInt(*task.OpenChatID, 10))
	}
	return out
}
