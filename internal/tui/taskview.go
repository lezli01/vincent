package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// taskViewTab names the four full-screen task surfaces. Steps is deliberately
// first: entering a task lands on the execution history people most often came
// to inspect.
type taskViewTab int

const (
	taskTabSteps taskViewTab = iota
	taskTabDetails
	taskTabOutput
	taskTabDiff
	taskTabCount
)

type taskTabHit struct {
	tab    taskViewTab
	x0, x1 int
}

// taskView is the routed workspace for one task. It keeps detail as one
// sub-model because the attempt cursor owns the transcript and diff state, but
// gives each way of reading that state the entire viewport.
type taskView struct {
	detail *detail
	tab    taskViewTab
	popup  bool

	connected bool
	width     int
	height    int

	detailsTop   int
	detailsCount int
	detailsH     int
	tabHits      []taskTabHit
	bodyY        int
}

func newTaskView(detail *detail) *taskView {
	return &taskView{detail: detail, connected: true}
}

func (t *taskView) title() string {
	if t.detail.taskID == 0 {
		return "Task"
	}
	return fmt.Sprintf("Task #%d", t.detail.taskID)
}

func (t *taskView) setClient(c *apiclient.Client) tea.Cmd {
	return t.detail.setClient(c)
}

func (t *taskView) setConnected(ok bool) { t.connected = ok }

func (t *taskView) hintedProject() int64 {
	if !t.detail.loaded {
		return 0
	}
	return t.detail.task.ProjectID
}

func (t *taskView) capturesInput() bool {
	return t.popup || t.detail.capturesInput()
}

func (t *taskView) paste(text string) tea.Cmd {
	if !t.popup {
		return nil
	}
	if f := t.detail.followUp; f != nil {
		return f.paste(text)
	}
	if f := t.detail.repair; f != nil {
		return f.paste(text)
	}
	if f := t.detail.form; f != nil {
		return f.paste(text)
	}
	return nil
}

func (t *taskView) bindingContext() bindingContext {
	switch t.tab {
	case taskTabDetails:
		return ctxTaskDetails
	case taskTabOutput:
		return ctxOutput
	case taskTabDiff:
		return ctxDiff
	default:
		return ctxTimeline
	}
}

func (t *taskView) target() taskActions { return t.detail.target() }

func (t *taskView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width, t.height = msg.Width, msg.Height
		return t, t.detail.update(msg)
	case selectTaskMsg:
		t.tab = taskTabSteps
		t.detailsTop = 0
		t.popup = false
		t.detail.active = true
		return t, t.detail.open(msg.id, msg.state)
	case viewActivatedMsg:
		if msg.id != viewTask {
			return t, nil
		}
		t.detail.active = true
		return t, tea.Batch(t.detail.loadCmd(), t.detail.syncStream())
	case viewDeactivatedMsg:
		if msg.id != viewTask {
			return t, nil
		}
		t.detail.active = false
		t.popup = false
		return t, t.detail.syncStream()
	case tea.KeyPressMsg:
		return t, t.updateKey(msg)
	case tea.MouseClickMsg:
		return t, t.updateClick(msg)
	case tea.MouseWheelMsg:
		return t, t.updateWheel(msg)
	}
	cmd := t.detail.update(msg)
	if t.popup && t.detail.form == nil && t.detail.repair == nil && t.detail.followUp == nil {
		t.popup = false
	}
	return t, cmd
}

func (t *taskView) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if t.popup {
		return t.updatePopupKey(msg)
	}

	switch msg.String() {
	case "tab", "]":
		return t.switchTab(1)
	case "shift+tab", "[":
		return t.switchTab(-1)
	case "1":
		return t.setTab(taskTabSteps)
	case "2":
		return t.setTab(taskTabDetails)
	case "3":
		return t.setTab(taskTabOutput)
	case "4":
		return t.setTab(taskTabDiff)
	case "d":
		if t.tab == taskTabDiff {
			return t.setTab(taskTabOutput)
		}
		return t.setTab(taskTabDiff)
	case "esc":
		return func() tea.Msg { return selectViewMsg{id: viewHome} }
	case "enter":
		if t.detail.form != nil {
			t.popup = true
			return nil
		}
		if t.tab == taskTabSteps && t.detail.selectedRun != 0 {
			return t.setTab(taskTabOutput)
		}
	case "R":
		cmd := t.detail.update(msg)
		if t.detail.repair != nil {
			t.popup = true
		}
		return cmd
	case "F":
		cmd := t.detail.update(msg)
		if t.detail.followUp != nil {
			t.popup = true
		}
		return cmd
	}

	if t.tab == taskTabDetails {
		return t.updateDetailsKey(msg)
	}
	if t.tab == taskTabOutput {
		switch msg.String() {
		case "left", "h":
			return t.detail.moveSelection(-1)
		case "right", "l":
			return t.detail.moveSelection(1)
		}
	}
	if t.tab == taskTabSteps {
		t.detail.focus = focusTimeline
		t.detail.tab = tabOutput
	} else {
		t.detail.focus = focusOutput
		if t.tab == taskTabOutput {
			t.detail.tab = tabOutput
		} else {
			t.detail.tab = tabDiff
		}
	}
	return t.detail.update(msg)
}

func (t *taskView) updatePopupKey(msg tea.KeyPressMsg) tea.Cmd {
	if f := t.detail.followUp; f != nil {
		cmd, exit := f.update(msg, t.detail.client)
		if exit {
			t.detail.followUp, t.popup = nil, false
		}
		return cmd
	}
	if f := t.detail.repair; f != nil {
		cmd, exit := f.update(msg, t.detail.client)
		if exit {
			t.detail.repair, t.popup = nil, false
		}
		return cmd
	}
	if f := t.detail.form; f != nil {
		cmd, exit := f.update(msg, t.detail.client, t.detail.taskID)
		if exit {
			t.popup = false
		}
		return cmd
	}
	t.popup = false
	return nil
}

func (t *taskView) switchTab(delta int) tea.Cmd {
	next := taskViewTab((int(t.tab) + delta + int(taskTabCount)) % int(taskTabCount))
	return t.setTab(next)
}

func (t *taskView) setTab(tab taskViewTab) tea.Cmd {
	if tab < 0 || tab >= taskTabCount || tab == t.tab {
		return nil
	}
	t.tab = tab
	if tab == taskTabDiff {
		t.detail.tab = tabDiff
		t.detail.diff.openTask(t.detail.taskID)
		return t.detail.diff.fetch(t.detail.client, true)
	}
	if tab == taskTabOutput || tab == taskTabSteps {
		t.detail.tab = tabOutput
	}
	return nil
}

func (t *taskView) updateDetailsKey(msg tea.KeyPressMsg) tea.Cmd {
	page := max(t.detailsH-1, 1)
	switch msg.String() {
	case "up", "k":
		t.detailsTop--
	case "down", "j":
		t.detailsTop++
	case "pgup":
		t.detailsTop -= page
	case "pgdown":
		t.detailsTop += page
	case "home", "g":
		t.detailsTop = 0
	case "end", "G":
		t.detailsTop = max(t.detailsCount-t.detailsH, 0)
	default:
		// Task actions remain available from the read-only details tab.
		return t.detail.update(msg)
	}
	t.detailsTop = min(max(t.detailsTop, 0), max(t.detailsCount-t.detailsH, 0))
	return nil
}

func (t *taskView) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if t.popup {
		return nil
	}
	// The root's outer frame puts the tab strip on body row 1.
	if msg.Y == 1 {
		for _, hit := range t.tabHits {
			if msg.X >= hit.x0 && msg.X < hit.x1 {
				return t.setTab(hit.tab)
			}
		}
		return nil
	}
	switch t.tab {
	case taskTabSteps:
		return t.detail.clickTimeline(msg.Y - t.bodyY)
	case taskTabDiff:
		t.detail.diff.clickRow(msg.Y - t.bodyY)
	}
	return nil
}

func (t *taskView) updateWheel(msg tea.MouseWheelMsg) tea.Cmd {
	delta := 1
	if msg.Button == tea.MouseWheelUp {
		delta = -1
	}
	switch t.tab {
	case taskTabSteps:
		return t.detail.moveSelection(delta)
	case taskTabDetails:
		t.detailsTop = min(max(t.detailsTop+delta, 0), max(t.detailsCount-t.detailsH, 0))
	case taskTabOutput:
		if delta > 0 {
			t.detail.vp.ScrollDown(1)
		} else {
			t.detail.vp.ScrollUp(1)
		}
		t.detail.syncFollowToViewport()
	case taskTabDiff:
		t.detail.diff.scroll(delta)
	}
	return nil
}

func (t *taskView) render(width, height int) string {
	if width > 0 {
		t.width = width
	}
	if height > 0 {
		t.height = height
	}
	lines := []string{t.renderTabs(), ""}
	t.bodyY = 3 // root border + tab strip + blank separator
	bodyH := max(t.height-len(lines), 1)
	if !t.connected {
		lines = append(lines, styleWarn.Render(" ⚠ daemon unreachable — task data is stale"))
		t.bodyY++
		bodyH--
	}
	body := t.renderTabBody(t.width, max(bodyH, 1))
	lines = append(lines, body)
	out := strings.Join(lines, "\n")
	if t.popup && (t.detail.form != nil || t.detail.repair != nil || t.detail.followUp != nil) {
		// overlay clips to the background it is given. The root adds visual
		// padding only after this render returns, so give the popup the full
		// content height here or a short timeline would clip it away.
		padded := strings.Split(out, "\n")
		for len(padded) < t.height {
			padded = append(padded, "")
		}
		out = strings.Join(padded, "\n")
		host := shell{detail: t.detail, bodyW: t.width, bodyH: t.height}
		out = host.overlayPopup(out)
	}
	return out
}

func (t *taskView) renderTabs() string {
	names := []string{"Steps & Attempts", "Task Details", "Output", "Diff"}
	t.tabHits = t.tabHits[:0]
	var b strings.Builder
	b.WriteString("  ")
	x := 3 // root frame border plus the two-cell indent above
	for i, name := range names {
		if i > 0 {
			b.WriteString(styleDim.Render(" │ "))
			x += 3
		}
		tab := taskViewTab(i)
		t.tabHits = append(t.tabHits, taskTabHit{tab: tab, x0: x, x1: x + len(name)})
		b.WriteString(tabLabel(name, t.tab == tab))
		x += len(name)
	}
	b.WriteString(styleDim.Render("   tab/⇧tab or 1–4"))
	return b.String()
}

func (t *taskView) renderTabBody(width, height int) string {
	switch t.tab {
	case taskTabDetails:
		return t.renderDetails(width, height)
	case taskTabOutput:
		t.detail.focus = focusOutput
		t.detail.tab = tabOutput
		return t.renderOutput(width, height)
	case taskTabDiff:
		t.detail.focus = focusOutput
		t.detail.tab = tabDiff
		return t.detail.diff.render(width, height)
	default:
		t.detail.focus = focusTimeline
		return t.detail.timelinePanel(height)
	}
}

func (t *taskView) renderOutput(width, height int) string {
	t.detail.width = width
	selector := t.renderOutputAttemptSelector(width)
	if height <= 1 {
		return selector
	}
	return selector + "\n" + t.detail.renderOutputPane(height-1)
}

func (t *taskView) renderOutputAttemptSelector(width int) string {
	runs := t.detail.attempts()
	if len(runs) == 0 {
		return ansi.Truncate("  Attempt  —  no attempts", max(width, 1), "…")
	}
	i := t.detail.runIndex(t.detail.selectedRun)
	if i < 0 {
		return ansi.Truncate("  Attempt  —  no attempt selected", max(width, 1), "…")
	}
	run := runs[i]
	identity := fmt.Sprintf(
		"%d/%d · step %d %s · attempt %d · %s",
		i+1, len(runs), run.StepIndex+1, stepLabel(run), run.Attempt, run.State,
	)
	if run.Iteration > 0 {
		identity = fmt.Sprintf(
			"%d/%d · step %d %s · iteration %d · attempt %d · %s",
			i+1, len(runs), run.StepIndex+1, stepLabel(run), run.Iteration, run.Attempt, run.State,
		)
	}
	line := "  Attempt  " + styleSelected.Render(identity) + styleDim.Render("   ←/→ select")
	return ansi.Truncate(line, max(width, 1), "…")
}

func (t *taskView) renderDetails(width, height int) string {
	lines := t.detailLines(width)
	t.detailsCount, t.detailsH = len(lines), height
	t.detailsTop = min(max(t.detailsTop, 0), max(len(lines)-height, 0))
	return strings.Join(windowRange(lines, t.detailsTop, t.detailsTop+height, height), "\n")
}

func (t *taskView) detailLines(width int) []string {
	d := t.detail
	if d.taskID == 0 {
		return []string{styleDim.Render("  no task selected")}
	}
	if !d.loaded {
		if d.loadErr != nil {
			return []string{styleBad.Render("  task unavailable: " + errString(d.loadErr))}
		}
		return []string{styleDim.Render("  loading task…")}
	}

	task := d.task
	out := []string{
		styleTitle.Render(fmt.Sprintf("  #%d %s", task.ID, task.Title)),
		"",
		section("Description"),
	}
	out = appendWrapped(out, task.Description, width)
	out = append(out, "", section("Task"),
		taskFact("state", task.State),
		taskFact("project", fmt.Sprintf("%s (#%d)", valueOr(task.ProjectName, "unknown"), task.ProjectID)),
		taskFact("workflow", task.Workflow),
		taskFact("workflow origin", task.WorkflowOrigin.Display()),
		taskFact("branch", valueOr(task.BranchName, "not created")),
		taskFact("priority", strconv.Itoa(task.Priority)),
		taskFact("current step", taskStep(task.Task)),
		taskFact("cost", formatCost(task.CostUSD)),
		taskFact("tokens", fmt.Sprintf("%d input · %d output", task.InputTokens, task.OutputTokens)),
	)
	if task.WorktreePath != nil {
		out = append(out, taskFact("worktree", *task.WorktreePath))
	}
	if task.BlockReason != nil {
		out = append(out, taskFact("block reason", *task.BlockReason))
	}
	if task.StatusMessage != nil {
		out = append(out, taskFact("status message", *task.StatusMessage))
	}
	if reason, until, ok := task.Hold(); ok {
		value := reason
		if until != nil {
			value += " until " + until.Format(time.RFC3339)
		}
		out = append(out, taskFact("queue hold", value))
	}
	if task.ParentTaskID != nil {
		out = append(out, taskFact("parent task", strconv.FormatInt(*task.ParentTaskID, 10)))
	}
	if task.LaneID != nil {
		out = append(out, taskFact("fan-out lane", *task.LaneID))
	}
	if task.LaneOrder != nil {
		out = append(out, taskFact("lane order", strconv.Itoa(*task.LaneOrder)))
	}
	if task.Loop != nil {
		out = append(out, taskFact("loop", task.Loop.Display()))
	}

	out = append(out, "", section("Fields"))
	if len(task.Fields) == 0 {
		out = append(out, styleDim.Render("  none"))
	} else {
		keys := sortedStringKeys(task.Fields)
		for _, key := range keys {
			out = append(out, taskFact(key, task.Fields[key]))
		}
	}

	out = append(out, "", section("Lifecycle"),
		taskFact("created", formatTaskTime(task.CreatedAt)),
		taskFact("updated", formatTaskTime(task.UpdatedAt)),
		taskFact("started", formatOptionalTaskTime(task.StartedAt)),
		taskFact("finished", formatOptionalTaskTime(task.FinishedAt)),
		taskFact("archived", formatOptionalTaskTime(task.ArchivedAt)),
		taskFact("pause requested", strconv.FormatBool(task.PauseRequested)),
	)
	actions := append([]string(nil), task.AvailableActions...)
	sort.Strings(actions)
	out = append(out, taskFact("available actions", valueOr(strings.Join(actions, ", "), "none")))

	if len(task.Warnings) > 0 {
		out = append(out, "", section("Warnings"))
		for _, warning := range task.Warnings {
			out = append(out, styleWarn.Render("  ⚠ "+warning))
		}
	}
	if len(task.PendingInput) > 0 && string(task.PendingInput) != "null" {
		out = append(out, "", section("Pending input"))
		out = appendWrapped(out, prettyJSON(task.PendingInput), width)
	}
	if task.Children != nil {
		out = append(out, "", section("Fan-out children"),
			taskFact("progress", fmt.Sprintf("%d/%d settled", task.Children.Settled, task.Children.Total)),
			taskFact("blocked", joinInt64(task.Children.Blocked)),
			taskFact("awaiting gate", joinInt64(task.Children.AwaitingGate)),
		)
		states := sortedStringKeys(task.Children.ByState)
		for _, state := range states {
			out = append(out, taskFact(state, strconv.Itoa(task.Children.ByState[state])))
		}
	}
	if issue := task.GitHubIssue; issue != nil {
		out = append(out, "", section("GitHub issue"),
			taskFact("issue", fmt.Sprintf("%s#%d · %s", issue.Repo, issue.Number, issue.Title)),
			taskFact("url", issue.URL),
			taskFact("state", issue.State),
			taskFact("labels", valueOr(issue.LabelList(), "none")),
			taskFact("author", valueOr(issue.Author, "unknown")),
			taskFact("assignee", valueOr(issue.Assignee, "none")),
			taskFact("milestone", valueOr(issue.Milestone, "none")),
			taskFact("captured", formatTaskTime(issue.FetchedAt)),
		)
		if issue.Body != "" {
			out = append(out, styleDim.Render("  body"))
			out = appendWrapped(out, issue.Body, width)
		}
	}

	out = append(out, "", section("Workflow snapshot"))
	if len(task.WorkflowSteps) == 0 {
		out = append(out, styleDim.Render("  no steps recorded"))
	} else {
		for _, step := range task.WorkflowSteps {
			out = append(out, fmt.Sprintf("  %d. %s · %s", step.Index+1, step.ID, step.Type))
			if len(step.ResolvedFrom) > 0 {
				out = append(out, taskFact("resolved from", strings.Join(step.ResolvedFrom, " → ")))
			}
			for _, body := range []struct{ label, value string }{
				{"prompt", step.Prompt}, {"run", step.Run}, {"instructions", step.Instructions},
			} {
				if body.value == "" {
					continue
				}
				out = append(out, styleDim.Render("    "+body.label))
				out = appendWrappedIndented(out, body.value, width, "      ")
			}
		}
	}
	return out
}

func taskFact(label, value string) string {
	return "  " + styleDim.Render(fmt.Sprintf("%-18s", label)) + " " + valueOr(value, "none")
}

func taskStep(task apiclient.Task) string {
	if k, n, ok := task.StepDisplay(); ok {
		name := task.StepName
		if name != "" {
			name = " · " + name
		}
		return fmt.Sprintf("%d/%d%s", k, n, name)
	}
	return "none"
}

func appendWrapped(lines []string, text string, width int) []string {
	if strings.TrimSpace(text) == "" {
		return append(lines, styleDim.Render("  none"))
	}
	return appendWrappedIndented(lines, text, width, "  ")
}

func appendWrappedIndented(lines []string, text string, width int, indent string) []string {
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		for _, line := range wrapPlain(paragraph, max(width-len(indent), 8)) {
			lines = append(lines, indent+line)
		}
	}
	return lines
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatTaskTime(value time.Time) string {
	if value.IsZero() {
		return "not recorded"
	}
	return value.Format(time.RFC3339)
}

func formatOptionalTaskTime(value *time.Time) string {
	if value == nil {
		return "not yet"
	}
	return formatTaskTime(*value)
}

func prettyJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func joinInt64(values []int64) string {
	if len(values) == 0 {
		return "none"
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.FormatInt(value, 10)
	}
	return strings.Join(out, ", ")
}
