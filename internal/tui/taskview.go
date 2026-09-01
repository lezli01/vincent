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

// taskViewTab names the full-screen task surfaces. Steps is deliberately
// first: entering a task lands on the execution history people most often came
// to inspect. Workflow was deliberately last (task 051): appending it leaves
// 1-4 bound to the tabs task 049 built the muscle memory on, and Pull Request
// is appended after it for the same reason (task 068).
//
// Pull Request is the first **conditional** tab: it exists only for a task
// with a live pull-request link and a usable integration. Being last is what
// makes that free — no other tab's number moves when it is absent — but it is
// not free for the *cycle*, which used to be modulo taskTabCount and would
// otherwise land on a tab that is not on the strip. taskView.tabs is the
// strip as it currently stands, and tab/⇧tab walk that instead.
type taskViewTab int

const (
	taskTabSteps taskViewTab = iota
	taskTabDetails
	taskTabOutput
	taskTabDiff
	taskTabWorkflow
	taskTabPull
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
	// workflow is the §15 workflow-graph tab (task 051). It is a sub-model
	// rather than more fields here because it owns a viewport and a
	// selection, and because the graph component is shared with the
	// workflows screen.
	workflow *workflowTab

	// pull is this task's pull-request row (task 052.6), refetched rather
	// than snapshotted: draft, state and merged status are live by nature.
	// pullFormPending is a create-a-pull-request intent that arrived before
	// the section it needs (task 069). The Pull Requests takeover picks a
	// task and navigates here, and the form needs the daemon's prefill, so
	// the intent waits for the fetch rather than opening an empty form.
	pullFormPending bool
	// createPR is the pull-request form, open only while a human has it up.
	pull        apiclient.GitHubTaskPull
	pullLoaded  bool
	pullErr     string
	pullNote    string
	pullNoteBad bool
	createPR    *createPRForm
	// pullTab is the §15 Pull Request tab (task 068). Its checks are fetched
	// and never stored, so it holds a rollup and a cursor and nothing else —
	// everything about the pull request itself is read off `pull` above, so
	// the tab and the Task Details section cannot disagree about what is
	// linked.
	pullTab taskPullTab

	connected bool
	width     int
	height    int

	// details is the Task Details tab's inspector. popupDetails is the
	// second instance of the same pane, owned by whichever answer, repair or
	// follow-up popup is open (task 059): a popup's reading position is its
	// own, and switching to Task details inside the popup must not move the
	// workspace tab behind it.
	details      detailsPane
	popupDetails detailsPane
	popupTab     popupTab

	tabHits []taskTabHit
	bodyY   int
}

// popupTab names the two tabs the three §6/§7.4 form popups carry (task 059).
// The form is first: the popup opened to ask something, and Task details is
// the reference you reach for while answering it.
type popupTab int

const (
	popupTabForm popupTab = iota
	popupTabDetails
	popupTabCount
)

func newTaskView(detail *detail) *taskView {
	return &taskView{detail: detail, connected: true, workflow: newWorkflowTab()}
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
	if f := t.createPR; f != nil {
		return f.paste(text)
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
	if t.createPR != nil {
		return ctxCreatePR
	}
	// While a form popup owns the keyboard it owns the footer and the ? sheet
	// too. Without these three arms both described the tab underneath, which
	// is what kept the rows tasks 025 and 027 registered unreachable from the
	// footer on the shipped surface (task 059).
	if t.popup {
		switch {
		case t.detail.followUp != nil:
			return ctxFollowUpForm
		case t.detail.repair != nil:
			return ctxRepairForm
		case t.detail.form != nil:
			return ctxForm
		}
	}
	switch t.tab {
	case taskTabDetails:
		return ctxTaskDetails
	case taskTabOutput:
		return ctxOutput
	case taskTabDiff:
		return ctxDiff
	case taskTabWorkflow:
		return ctxTaskWorkflow
	case taskTabPull:
		return ctxTaskPull
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
		t.workflow = newWorkflowTab()
		t.details.reset()
		t.popupDetails.reset()
		t.popup = false
		t.createPR = nil
		t.pull, t.pullLoaded, t.pullErr = apiclient.GitHubTaskPull{}, false, ""
		t.pullNote, t.pullNoteBad = "", false
		t.pullTab = taskPullTab{}
		t.pullFormPending = msg.openPR
		t.detail.active = true
		return t, tea.Batch(t.detail.open(msg.id, msg.state), t.pullCmd())
	case viewActivatedMsg:
		if msg.id != viewTask {
			return t, nil
		}
		t.detail.active = true
		return t, tea.Batch(t.detail.loadCmd(), t.detail.syncStream(), t.pullCmd())
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
	case taskWorkflowMsg:
		t.applyWorkflow(msg)
		return t, nil
	case taskLanesMsg:
		t.applyLanes(msg)
		return t, nil
	case taskPullMsg:
		t.applyPull(msg)
		return t, nil
	case taskChecksMsg:
		t.applyChecks(msg)
		return t, nil
	case taskChecksTickMsg:
		// The tick is dropped unless the tab is still open on the same task:
		// a human who has moved on is not asking GitHub anything.
		if msg.taskID != t.detail.taskID || t.tab != taskTabPull || !t.detail.active {
			return t, nil
		}
		return t, tea.Batch(t.checksCmd(), t.checksTickCmd())
	case taskPullCreatedMsg:
		return t, t.applyCreatedPull(msg)
	case createPREditMsg:
		if t.createPR != nil {
			t.createPR.applyEdit(msg)
		}
		return t, nil
	case openedURLMsg:
		// Every view sees this; only the one a human is looking at should
		// speak for it.
		if !t.detail.active {
			return t, nil
		}
		if msg.err != nil {
			t.pullNote, t.pullNoteBad = openFailure(msg), true
		} else {
			t.pullNote, t.pullNoteBad = "", false
		}
		return t, nil
	case noteMsg:
		// A reconciler tick that linked or unlinked this task's pull request
		// re-reads the section; the detail sub-model still sees the note.
		cmd := t.detail.update(msg)
		if ev, ok := msg.note.(apiclient.EventNote); ok &&
			ev.Event.Type == eventTaskGitHubPullChanged {
			cmds := []tea.Cmd{cmd, t.pullCmd()}
			if t.tab == taskTabPull {
				// The reconciler moved the link; the checks under it are
				// about a different pull request now.
				cmds = append(cmds, t.checksCmd())
			}
			return t, tea.Batch(cmds...)
		}
		return t, cmd
	}
	cmd := t.detail.update(msg)
	if t.popup && t.detail.form == nil && t.detail.repair == nil &&
		t.detail.followUp == nil && t.createPR == nil {
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
	case "5":
		return t.setTab(taskTabWorkflow)
	case "6":
		// Absent means absent: with no pull request linked, 6 does nothing
		// rather than landing on an empty screen (task 068).
		if !t.pullTabAvailable() {
			return nil
		}
		return t.setTab(taskTabPull)
	case "d":
		if t.tab == taskTabDiff {
			return t.setTab(taskTabOutput)
		}
		return t.setTab(taskTabDiff)
	case "esc":
		return func() tea.Msg { return selectViewMsg{id: viewHome} }
	case "enter":
		if t.detail.form != nil {
			t.openPopup()
			return nil
		}
		if t.tab == taskTabSteps && t.detail.selectedRun != 0 {
			return t.setTab(taskTabOutput)
		}
	case "R":
		cmd := t.detail.update(msg)
		if t.detail.repair != nil {
			t.openPopup()
		}
		return cmd
	case "F":
		cmd := t.detail.update(msg)
		if t.detail.followUp != nil {
			t.openPopup()
		}
		return cmd
	}

	if t.tab == taskTabDetails {
		return t.updateDetailsKey(msg)
	}
	if t.tab == taskTabWorkflow {
		return t.updateWorkflowKey(msg)
	}
	if t.tab == taskTabPull {
		return t.updatePullTabKey(msg)
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

// openPopup raises one of the three form popups on its own Question/Repair/
// Follow-up tab, with a fresh reading position for its Task details tab.
func (t *taskView) openPopup() {
	t.popup = true
	t.popupTab = popupTabForm
	t.popupDetails.reset()
}

// hasFormPopup reports whether the open popup is one of the three that carry
// the task-details tab. The compare-URL editor (task 052.6) does not.
func (t *taskView) hasFormPopup() bool {
	return t.createPR == nil &&
		(t.detail.followUp != nil || t.detail.repair != nil || t.detail.form != nil)
}

func (t *taskView) updatePopupKey(msg tea.KeyPressMsg) tea.Cmd {
	if f := t.createPR; f != nil {
		cmd, exit := f.update(msg)
		if exit {
			t.createPR, t.popup = nil, false
		}
		return cmd
	}
	if t.hasFormPopup() && t.popupTabKey(msg) {
		// Nothing the strip or the read-only pane does posts a command; that
		// is the point of decision 6, not an oversight.
		return nil
	}
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

// popupTabKey is the seam that makes the popup's tab strip survive a focused
// editor (task 059 decision 4): ctrl+t is taken here, before the form sees the
// press, so it works while the answer form's free-text textarea, the repair or
// follow-up prompt, or an agent/model/effort picker is open. The forms
// themselves stay unaware that they have tabs.
//
// While the Task details tab shows, the pane is a strictly read-only
// reference: unhandled keys stop here rather than reaching the task actions,
// and neither `o` nor `P` is offered — a popup that can raise a second popup
// is not a reference surface (decision 6).
func (t *taskView) popupTabKey(msg tea.KeyPressMsg) bool {
	if msg.String() == "ctrl+t" {
		t.popupTab = (t.popupTab + 1) % popupTabCount
		return true
	}
	if t.popupTab != popupTabDetails {
		return false
	}
	// esc closes one layer, which here is the tab and not the popup: the
	// draft underneath is exactly what the tab exists to protect (§15, 017
	// decision 13).
	if msg.String() == "esc" {
		t.popupTab = popupTabForm
		return true
	}
	t.popupDetails.updateKey(msg)
	return true
}

// switchTab walks the strip as it currently stands rather than the whole enum
// (task 068). The modulo is over len(tabs), which is what makes the cycle skip
// a Pull Request tab that is not there — the arithmetic was the first thing a
// conditional tab was going to get wrong.
func (t *taskView) switchTab(delta int) tea.Cmd {
	tabs := t.tabs()
	if len(tabs) == 0 {
		return nil
	}
	next := tabs[(t.tabIndex(t.tab)+delta+len(tabs))%len(tabs)]
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
	if tab == taskTabWorkflow {
		return t.openWorkflowTab()
	}
	if tab == taskTabPull {
		// Fetched on open, never per render: a stored check result reads
		// exactly like a current one while being wrong.
		return tea.Batch(t.checksCmd(), t.checksTickCmd())
	}
	return nil
}

func (t *taskView) updateDetailsKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "o":
		// The pull-request section's two keys (task 052.6 decision 2). Both
		// only reach a browser, which is what keeps the tab a read-only
		// inspector. Neither is offered inside a popup (task 059 decision 6).
		return t.openPullCmd()
	case "P":
		return t.openCreatePR()
	}
	if t.details.updateKey(msg) {
		return nil
	}
	// Task actions remain available from the read-only details tab.
	return t.detail.update(msg)
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
	case taskTabDetails:
		t.details.clickSidebar(msg.X, msg.Y-t.bodyY)
	case taskTabDiff:
		t.detail.diff.clickRow(msg.Y - t.bodyY)
	case taskTabWorkflow:
		if t.workflow != nil {
			t.workflow.graph.ClickAt(msg.X-1, msg.Y-t.bodyY)
		}
	}
	return nil
}

func (t *taskView) updateWheel(msg tea.MouseWheelMsg) tea.Cmd {
	// A popup owns the surface: PR S gave the palette and the answer popup
	// clicks, and updateClick honours it. The wheel is the same rule — a tick
	// behind an open popup must not scroll the pane under it.
	if t.popup {
		return nil
	}
	delta := 1
	if msg.Button == tea.MouseWheelUp {
		delta = -1
	}
	switch t.tab {
	case taskTabSteps:
		return t.detail.moveSelection(delta)
	case taskTabDetails:
		t.details.scrollAt(msg.X, delta)
	case taskTabOutput:
		if delta > 0 {
			t.detail.vp.ScrollDown(1)
		} else {
			t.detail.vp.ScrollUp(1)
		}
		t.detail.syncFollowToViewport()
	case taskTabDiff:
		t.detail.diff.scroll(delta)
	case taskTabWorkflow:
		if t.workflow != nil {
			t.workflow.graph.Scroll(delta)
		}
	case taskTabPull:
		t.movePullCursor(delta)
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
	if t.popup && (t.detail.form != nil || t.detail.repair != nil ||
		t.detail.followUp != nil || t.createPR != nil) {
		// overlay clips to the background it is given. The root adds visual
		// padding only after this render returns, so give the popup the full
		// content height here or a short timeline would clip it away.
		padded := strings.Split(out, "\n")
		for len(padded) < t.height {
			padded = append(padded, "")
		}
		out = strings.Join(padded, "\n")
		if t.createPR != nil {
			out = t.overlayCreatePR(out)
		} else {
			p := popupOverlayFor(t.detail)
			p.tab, p.details = t.popupTab, t.renderPopupDetails
			out = overlayPopup(out, t.width, t.height, p)
		}
	}
	return out
}

// taskTabNames are the strip's labels, indexed by taskViewTab.
var taskTabNames = [taskTabCount]string{
	taskTabSteps:    "Steps & Attempts",
	taskTabDetails:  "Task Details",
	taskTabOutput:   "Output",
	taskTabDiff:     "Diff",
	taskTabWorkflow: "Workflow",
	taskTabPull:     "Pull Request",
}

func (t *taskView) renderTabs() string {
	tabs := t.tabs()
	t.tabHits = t.tabHits[:0]
	var b strings.Builder
	b.WriteString("  ")
	x := 3 // root frame border plus the two-cell indent above
	for i, tab := range tabs {
		if i > 0 {
			b.WriteString(styleDim.Render(" │ "))
			x += 3
		}
		name := taskTabNames[tab]
		t.tabHits = append(t.tabHits, taskTabHit{tab: tab, x0: x, x1: x + len(name)})
		b.WriteString(tabLabel(name, t.tab == tab))
		x += len(name)
	}
	b.WriteString(styleDim.Render(fmt.Sprintf("   tab/⇧tab or 1–%d", len(tabs))))
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
	case taskTabWorkflow:
		return t.renderWorkflow(width, height)
	case taskTabPull:
		return t.renderPullTab(width, height)
	default:
		t.detail.focus = focusTimeline
		t.detail.width = width
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

// renderDetails is the workspace's Task Details tab. Both it and the popup's
// own tab (task 059) draw the same pane against the same document; only the
// instance holding the scroll and the selection differs.
func (t *taskView) renderDetails(width, height int) string {
	return t.details.render(width, height, t.detailsReady(), t.detailLines)
}

// renderPopupDetails is the Task details tab inside an open form popup.
func (t *taskView) renderPopupDetails(width, height int) string {
	return t.popupDetails.render(width, height, t.detailsReady(), t.detailLines)
}

// detailsReady says the document is the task rather than a placeholder, which
// is what decides whether the pane draws a sidebar at all.
func (t *taskView) detailsReady() bool {
	return t.detail.loaded && t.detail.taskID != 0
}

type taskDetailSection struct {
	title string
	lines []string
}

type taskDetailDocument struct {
	header   []string
	sections []taskDetailSection
}

var taskDetailSectionOrder = []string{
	"Description",
	"Overview",
	"Execution",
	"Relationships",
	"Fields",
	"Lifecycle",
	"Warnings",
	"Pending input",
	"GitHub issue",
	"GitHub pull request",
	"Workflow snapshot",
}

func splitTaskDetailDocument(lines []string) taskDetailDocument {
	var document taskDetailDocument
	current := -1
	for _, line := range lines {
		if title := taskDetailSectionTitle(line); title != "" {
			document.sections = append(document.sections, taskDetailSection{title: title})
			current = len(document.sections) - 1
		}
		if current < 0 {
			document.header = append(document.header, line)
		} else {
			document.sections[current].lines = append(document.sections[current].lines, line)
		}
	}
	return document
}

func taskDetailSectionTitle(line string) string {
	plain := strings.TrimSpace(ansi.Strip(line))
	for _, title := range taskDetailSectionOrder {
		if plain == title || strings.HasPrefix(plain, title+"  ") {
			return title
		}
	}
	return ""
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
		styleTitle.Render(fmt.Sprintf("  #%d  %s", task.ID, task.Title)),
	}
	meta := []string{renderDetailState(task.Task)}
	if task.ProjectName != "" {
		meta = append(meta, styleDim.Render(task.ProjectName))
	}
	if task.Workflow != "" {
		meta = append(meta, styleDim.Render(task.Workflow))
	}
	out = append(out, "  "+strings.Join(meta, styleDim.Render("  ·  ")))

	description := appendWrapped(nil, task.Description, width)
	out = appendTaskDetailSection(out, "Description", description)

	overview := []taskDetailFact{
		{"state", task.State},
		{"project", fmt.Sprintf("%s (#%d)", valueOr(task.ProjectName, "unknown"), task.ProjectID)},
		{"workflow", task.Workflow},
		{"priority", strconv.Itoa(task.Priority)},
	}
	paths := []taskDetailFact{
		{"workflow origin", task.WorkflowOrigin.Display()},
		{"branch", valueOr(task.BranchName, "not created")},
	}
	if task.WorktreePath != nil {
		paths = append(paths, taskDetailFact{"worktree", *task.WorktreePath})
	}
	overviewLines := renderTaskDetailFacts(width, overview)
	overviewLines = append(overviewLines, "")
	overviewLines = append(overviewLines, renderTaskDetailFactList(width, paths)...)
	out = appendTaskDetailSection(out, "Overview", overviewLines)

	execution := []taskDetailFact{
		{"current step", taskStep(task.Task)},
		{"cost", formatCost(task.CostUSD)},
		{"tokens", fmt.Sprintf("%d input · %d output", task.InputTokens, task.OutputTokens)},
		{"pause requested", strconv.FormatBool(task.PauseRequested)},
	}
	if task.BlockReason != nil {
		execution = append(execution, taskDetailFact{"block reason", *task.BlockReason})
	}
	if task.StatusMessage != nil {
		execution = append(execution, taskDetailFact{"status message", *task.StatusMessage})
	}
	if reason, until, ok := task.Hold(); ok {
		value := reason
		if until != nil {
			value += " until " + until.Format(time.RFC3339)
		}
		execution = append(execution, taskDetailFact{"queue hold", value})
	}
	actions := append([]string(nil), task.AvailableActions...)
	sort.Strings(actions)
	execution = append(execution, taskDetailFact{"available actions", valueOr(strings.Join(actions, ", "), "none")})
	out = appendTaskDetailSection(out, "Execution", renderTaskDetailFacts(width, execution))

	relationships := make([]taskDetailFact, 0, 8)
	if task.ParentTaskID != nil {
		relationships = append(relationships, taskDetailFact{"parent task", strconv.FormatInt(*task.ParentTaskID, 10)})
	}
	if task.LaneID != nil {
		relationships = append(relationships, taskDetailFact{"fan-out lane", *task.LaneID})
	}
	if task.LaneOrder != nil {
		relationships = append(relationships, taskDetailFact{"lane order", strconv.Itoa(*task.LaneOrder)})
	}
	if task.Loop != nil {
		relationships = append(relationships, taskDetailFact{"loop", task.Loop.Display()})
	}
	if task.Children != nil {
		relationships = append(relationships,
			taskDetailFact{"child progress", fmt.Sprintf("%d/%d settled", task.Children.Settled, task.Children.Total)},
			taskDetailFact{"children blocked", joinInt64(task.Children.Blocked)},
			taskDetailFact{"children at gate", joinInt64(task.Children.AwaitingGate)},
		)
		states := sortedStringKeys(task.Children.ByState)
		for _, state := range states {
			relationships = append(relationships, taskDetailFact{"children " + state, strconv.Itoa(task.Children.ByState[state])})
		}
	}
	if len(relationships) > 0 {
		out = appendTaskDetailSection(out, "Relationships", renderTaskDetailFacts(width, relationships))
	}

	fieldFacts := make([]taskDetailFact, 0, len(task.Fields))
	for _, key := range sortedStringKeys(task.Fields) {
		fieldFacts = append(fieldFacts, taskDetailFact{key, task.Fields[key]})
	}
	fieldLines := renderTaskDetailFacts(width, fieldFacts)
	if len(fieldLines) == 0 {
		fieldLines = []string{styleDim.Render("  none")}
	}
	out = appendTaskDetailSection(out, "Fields", fieldLines)

	lifecycle := []taskDetailFact{
		{"created", formatTaskTime(task.CreatedAt)},
		{"updated", formatTaskTime(task.UpdatedAt)},
		{"started", formatOptionalTaskTime(task.StartedAt)},
		{"finished", formatOptionalTaskTime(task.FinishedAt)},
		{"archived", formatOptionalTaskTime(task.ArchivedAt)},
	}
	out = appendTaskDetailSection(out, "Lifecycle", renderTaskDetailFacts(width, lifecycle))

	if len(task.Warnings) > 0 {
		warnings := make([]string, 0, len(task.Warnings))
		for _, warning := range task.Warnings {
			warnings = append(warnings, styleWarn.Render("  ⚠  "+warning))
		}
		out = appendTaskDetailSection(out, "Warnings", warnings)
	}
	if len(task.PendingInput) > 0 && string(task.PendingInput) != "null" {
		out = appendTaskDetailSection(out, "Pending input", appendWrapped(nil, prettyJSON(task.PendingInput), width))
	}
	if issue := task.GitHubIssue; issue != nil {
		issueLines := renderTaskDetailFacts(width, []taskDetailFact{
			{"issue", fmt.Sprintf("%s#%d · %s", issue.Repo, issue.Number, issue.Title)},
			{"state", issue.State},
			{"url", issue.URL},
			{"labels", valueOr(issue.LabelList(), "none")},
			{"author", valueOr(issue.Author, "unknown")},
			{"assignee", valueOr(issue.Assignee, "none")},
			{"milestone", valueOr(issue.Milestone, "none")},
			{"captured", formatTaskTime(issue.FetchedAt)},
		})
		if issue.Body != "" {
			issueLines = append(issueLines, "", styleDim.Render("  Body"), "")
			issueLines = appendWrapped(issueLines, issue.Body, width)
		}
		out = appendTaskDetailSection(out, "GitHub issue", issueLines)
	}
	out = appendTaskDetailSection(out, "GitHub pull request", t.pullSectionLines(width))

	workflow := make([]string, 0, len(task.WorkflowSteps)*4)
	if len(task.WorkflowSteps) == 0 {
		workflow = append(workflow, styleDim.Render("  no steps recorded"))
	} else {
		for i, step := range task.WorkflowSteps {
			if i > 0 {
				workflow = append(workflow, "")
			}
			workflow = append(workflow,
				styleTitle.Render(fmt.Sprintf("  %02d  %s", step.Index+1, step.ID))+
					styleDim.Render("  ·  "+step.Type),
			)
			if len(step.ResolvedFrom) > 0 {
				workflow = append(workflow,
					renderTaskDetailFacts(width, []taskDetailFact{{"resolved from", strings.Join(step.ResolvedFrom, " → ")}})...,
				)
			}
			for _, body := range []struct{ label, value string }{
				{"prompt", step.Prompt}, {"run", step.Run}, {"instructions", step.Instructions},
			} {
				if body.value == "" {
					continue
				}
				workflow = append(workflow, "", styleDim.Render("      "+body.label))
				workflow = appendWrappedIndented(workflow, body.value, width, "        ")
			}
		}
	}
	out = appendTaskDetailSection(out, "Workflow snapshot", workflow)
	return out
}

type taskDetailFact struct {
	label string
	value string
}

func appendTaskDetailSection(lines []string, title string, body []string) []string {
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines, section(title), "")
	return append(lines, body...)
}

func renderTaskDetailFacts(width int, facts []taskDetailFact) []string {
	if len(facts) == 0 {
		return nil
	}
	if width < 88 {
		return renderTaskDetailFactList(width, facts)
	}

	const gutter = 4
	columnWidth := max((width-4-gutter)/2, 12)
	out := make([]string, 0, len(facts))
	for i := 0; i < len(facts); i += 2 {
		left := taskDetailFactLines(facts[i], columnWidth)
		var right []string
		if i+1 < len(facts) {
			right = taskDetailFactLines(facts[i+1], columnWidth)
		}
		rows := max(len(left), len(right))
		for row := range rows {
			leftLine := ""
			if row < len(left) {
				leftLine = left[row]
			}
			line := "  " + leftLine
			if len(right) > 0 {
				rightLine := ""
				if row < len(right) {
					rightLine = right[row]
				}
				line = "  " + padDisplayWidth(leftLine, columnWidth) + strings.Repeat(" ", gutter) + rightLine
			}
			out = append(out, line)
		}
	}
	return out
}

func renderTaskDetailFactList(width int, facts []taskDetailFact) []string {
	out := make([]string, 0, len(facts))
	for _, fact := range facts {
		for _, line := range taskDetailFactLines(fact, max(width-4, 12)) {
			out = append(out, "  "+line)
		}
	}
	return out
}

func taskDetailFactLines(fact taskDetailFact, width int) []string {
	labelWidth := min(18, max(width-9, 8))
	valueWidth := max(width-labelWidth-1, 8)
	value := valueOr(fact.value, "none")
	if ansi.StringWidth(fact.label) > labelWidth {
		out := make([]string, 0, 3)
		for _, labelLine := range wrapTaskDetailText(fact.label, width) {
			out = append(out, styleDim.Render(labelLine))
		}
		for _, paragraph := range strings.Split(value, "\n") {
			if paragraph == "" {
				out = append(out, "")
				continue
			}
			for _, valueLine := range wrapTaskDetailText(paragraph, max(width-2, 8)) {
				out = append(out, "  "+valueLine)
			}
		}
		return out
	}
	var values []string
	for _, paragraph := range strings.Split(value, "\n") {
		if paragraph == "" {
			values = append(values, "")
			continue
		}
		values = append(values, wrapTaskDetailText(paragraph, valueWidth)...)
	}
	if len(values) == 0 {
		values = []string{"none"}
	}

	label := ansi.Truncate(fact.label, labelWidth, "…")
	label += strings.Repeat(" ", max(labelWidth-ansi.StringWidth(label), 0))
	indent := strings.Repeat(" ", labelWidth+1)
	out := make([]string, len(values))
	for i, valueLine := range values {
		if i == 0 {
			out[i] = styleDim.Render(label) + " " + valueLine
		} else {
			out[i] = indent + valueLine
		}
	}
	return out
}

func padDisplayWidth(line string, width int) string {
	return line + strings.Repeat(" ", max(width-ansi.StringWidth(line), 0))
}

func wrapTaskDetailText(text string, width int) []string {
	width = max(width, 1)
	soft := wrapPlain(text, width)
	out := make([]string, 0, len(soft))
	for _, line := range soft {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		var chunk strings.Builder
		chunkWidth := 0
		for _, r := range line {
			runeWidth := ansi.StringWidth(string(r))
			if chunkWidth > 0 && chunkWidth+runeWidth > width {
				out = append(out, chunk.String())
				chunk.Reset()
				chunkWidth = 0
			}
			chunk.WriteRune(r)
			chunkWidth += runeWidth
		}
		if chunk.Len() > 0 {
			out = append(out, chunk.String())
		}
	}
	return out
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

// overlayCreatePR draws the compare-URL editor over the workspace, on the
// same geometry the shell's popups use — the popup is the same kind of thing
// and should not sit somewhere else on the screen.
func (t *taskView) overlayCreatePR(bg string) string {
	pw := min(t.width-6, 120)
	if pw < 20 {
		pw = t.width
	}
	inner := pw - 2
	ph := min(t.createPR.height(inner)+2, max(t.height-4, 6))
	popup := frame("Open a pull request — #"+strconv.FormatInt(t.detail.taskID, 10),
		t.createPR.render(inner, ph-2), pw, ph, true)
	return overlay(bg, popup, max((t.width-pw)/2, 0), max((t.height-ph)/3, 1))
}
