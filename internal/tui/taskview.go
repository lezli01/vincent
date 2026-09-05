package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
// was appended after it for the same reason (task 068).
//
// Step Details (issue #323) is inserted **before** Pull Request rather than
// after it, which supersedes 068.3's placement decision. What 068.3 was
// protecting survives whole: digits bind to tabs and not to positions, and
// Step Details is unconditional, so no tab's number moves when the
// pull-request tab is absent — `6` is Step Details either way, and `7` does
// nothing when nothing is linked, exactly as `6` did before it. What is
// genuinely paid is that `6` changes meaning once, for a reader who has a
// linked pull request and had learned the old number; that cost was accepted
// deliberately.
//
// Pull Request is still the only **conditional** tab: it exists only for a
// task with a live pull-request link and a usable integration, and it stays
// last on the strip, so tabs() and the cycle keep the shape they have. Its
// absence is not free for the *cycle*, which used to be modulo taskTabCount
// and would otherwise land on a tab that is not on the strip. taskView.tabs is
// the strip as it currently stands, and tab/⇧tab walk that instead.
type taskViewTab int

const (
	taskTabSteps taskViewTab = iota
	taskTabDetails
	taskTabOutput
	taskTabDiff
	taskTabWorkflow
	taskTabStepDetails
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

	// stepDetails is the Step Details tab's pane (issue #323). It holds a
	// scroll offset and the strip's geometry and no selection of its own:
	// which attempt is being read is detail.selectedRun, shared with Output,
	// Diff and the timeline (task 049 decision 4).
	stepDetails stepDetailsPane

	tabHits []taskTabHit
	bodyY   int

	// stack is the chain of tasks this workspace was opened *through* — the
	// parents and lanes a reader drilled down from (#316). `esc` pops one
	// before it falls through to the board, so three lanes deep is three
	// presses. A task opened from the board arrives with an empty stack,
	// because there is nothing behind it but the board.
	stack []int64
	// stackPush is the task the next selectTaskMsg should push, and stackKeep
	// says that message is a pop and must leave the stack alone. Both are
	// fields rather than fields on the message because selectTaskMsg is the
	// board's, shared with every other way of opening a task.
	stackPush int64
	stackKeep bool
	// alive reports whether a task on the stack can still be opened, so an
	// archived or vanished one is dropped from the stack rather than popped
	// to. Injected so the walk is testable without a daemon; nil asks one.
	alive func(id int64) (state string, ok bool)

	// lanes are this task's fan-out lanes in merge order (§7.6), from the
	// existing GET /v1/tasks?parent_id= listing. It is the workspace's own
	// copy: the Workflow tab keeps a map keyed by lane id for its captions,
	// and this is a list because everything here — the Output pane's
	// selector, the Pull Request tab's rows, the `l` jump — walks merge order.
	lanes       []apiclient.Task
	lanesTaskID int64
	// laneSel indexes lanes for the Output pane's lane selector. -1 is the
	// task's own output, which is where the pane starts and returns to.
	laneSel int
	// laneDetail is the selected lane's own sub-model, and therefore the one
	// extra live subscription the Output pane is allowed to hold. Exactly one
	// at a time, torn down when the selection moves and when the workspace
	// leaves the fan-out: interleaving every lane would open 64 streams at
	// task 051 decision 1's expense and render lossy besides, because the
	// daemon drops live chunks for a slow subscriber (§13.3). The transcript
	// file stays the durable copy; this is a view, not a second store.
	laneDetail *detail
	// lanePulls is each lane task's pull request, keyed by lane task id. It
	// is one project listing rather than one call per lane — the same rule
	// the check rollup follows, one call for one screen.
	lanePulls map[int64]apiclient.GitHubPullRequest
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
	return &taskView{detail: detail, connected: true, workflow: newWorkflowTab(), laneSel: -1}
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
	case taskTabStepDetails:
		return ctxTaskStepDetails
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
		// Which of the three ways this task was opened decides what happens
		// to the back stack: a jump pushes what it left, a pop keeps what it
		// already truncated, and every other route — the board, the palette,
		// the pull-request takeover — starts fresh.
		switch {
		case t.stackKeep:
			t.stackKeep = false
		case t.stackPush != 0:
			t.stack = append(t.stack, t.stackPush)
			t.stackPush = 0
		default:
			t.stack = nil
		}
		t.tab = taskTabSteps
		t.workflow = newWorkflowTab()
		t.details.reset()
		t.popupDetails.reset()
		t.stepDetails.reset()
		t.popup = false
		t.createPR = nil
		t.pull, t.pullLoaded, t.pullErr = apiclient.GitHubTaskPull{}, false, ""
		t.pullNote, t.pullNoteBad = "", false
		t.pullTab = taskPullTab{}
		t.pullFormPending = msg.openPR
		t.detail.active = true
		laneCmd := t.resetLanes()
		return t, tea.Batch(t.detail.open(msg.id, msg.state), t.pullCmd(), laneCmd, t.lanesCmd())
	case viewActivatedMsg:
		if msg.id != viewTask {
			return t, nil
		}
		t.detail.active = true
		return t, tea.Batch(t.detail.loadCmd(), t.detail.syncStream(), t.pullCmd(),
			t.lanesCmd(), t.syncLaneDetail())
	case viewDeactivatedMsg:
		if msg.id != viewTask {
			return t, nil
		}
		t.detail.active = false
		t.popup = false
		// The lane subscription belongs to a screen nobody is looking at.
		return t, tea.Batch(t.detail.syncStream(), t.syncLaneDetail())
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
	case taskLaneListMsg:
		return t, t.applyLaneList(msg)
	case taskLanePullsMsg:
		t.applyLanePulls(msg)
		return t, nil
	case navPopMsg:
		return t, t.applyPop(msg)
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
		cmd := tea.Batch(t.detail.update(msg), t.laneUpdate(msg))
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
	// Everything that is not a key press, a click or one of the arms above
	// reaches both sub-models. Each one guards on its own task, attempt and
	// stream id, so a message for the parent is ignored by the lane and the
	// other way round — which is what lets the lane pane be an ordinary
	// detail rather than a second, thinner copy of one.
	cmd := tea.Batch(t.detail.update(msg), t.laneUpdate(msg))
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
		return t.setTab(taskTabStepDetails)
	case "7":
		// Absent means absent: with no pull request linked, 7 does nothing
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
		// One task at a time. Drilling into a lane and pressing esc used to
		// land on the board however deep the reader had gone (#316).
		return t.popCmd()
	case "l":
		if cmd := t.openLaneCmd(); cmd != nil {
			return cmd
		}
		// No lane under the selection: `l` keeps whatever the tab under it
		// already made of the key — vim-right in the Output pane.
	case "U":
		if cmd := t.openParentCmd(); cmd != nil {
			return cmd
		}
	case "<":
		if t.tab == taskTabOutput {
			return t.selectLane(-1)
		}
	case ">":
		if t.tab == taskTabOutput {
			return t.selectLane(1)
		}
	case "enter":
		if t.detail.form != nil {
			t.openPopup()
			return nil
		}
		if t.tab == taskTabSteps && t.detail.timelineFolded() {
			// `enter` means both things, chosen by the row under the cursor:
			// on a folded tier it opens the fold, and there is nothing else
			// it could usefully mean there — the attempt it would carry the
			// reader to is one they cannot see yet (issue #317).
			return t.detail.setTimelineFold(true)
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
	if t.tab == taskTabStepDetails {
		return t.updateStepDetailsKey(msg)
	}
	if t.tab == taskTabPull {
		return t.updatePullTabKey(msg)
	}
	if t.tab == taskTabSteps {
		// Gated on the tab, not handled in the switch above: ←/→ already walk
		// the Output tab's attempt selection just below, and O/C are the Diff
		// tab's fold-all. Each pair means the fold only while Steps &
		// Attempts has the screen.
		switch msg.String() {
		case " ", "space":
			return t.detail.toggleTimelineFold()
		case "right":
			return t.detail.setTimelineFold(true)
		case "left":
			return t.detail.setTimelineFold(false)
		case "O":
			return t.detail.setAllTimelineFolds(true)
		case "C":
			return t.detail.setAllTimelineFolds(false)
		}
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
		return tea.Batch(t.checksCmd(), t.checksTickCmd(), t.lanePullsCmd())
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
	case taskTabStepDetails:
		return t.clickStepDetailsSidebar(msg.X, msg.Y-t.bodyY)
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
		return t.detail.moveTimelineSelection(delta)
	case taskTabDetails:
		t.details.scrollAt(msg.X, delta)
	case taskTabStepDetails:
		return t.scrollStepDetailsAt(msg.X, delta)
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
	taskTabSteps:       "Steps & Attempts",
	taskTabDetails:     "Task Details",
	taskTabOutput:      "Output",
	taskTabDiff:        "Diff",
	taskTabWorkflow:    "Workflow",
	taskTabStepDetails: "Step Details",
	taskTabPull:        "Pull Request",
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
	case taskTabStepDetails:
		return t.renderStepDetails(width, height)
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
	head := make([]string, 0, 2)
	if len(t.lanes) > 0 {
		head = append(head, t.renderLaneSelector(width))
	}
	pane := t.detail
	if lane := t.laneDetail; lane != nil {
		lane.width = width
		lane.tab = tabOutput
		pane = lane
	}
	head = append(head, renderAttemptSelector(pane, width))
	if height <= len(head) {
		return strings.Join(head[:max(height, 1)], "\n")
	}
	return strings.Join(append(head, pane.renderOutputPane(height-len(head))), "\n")
}

// renderLaneSelector is the Output pane's lane strip (#316). It names the
// lane whose output is on screen, which is the task's own until `>` moves
// off it, and it is drawn only for a task that has lanes at all.
func (t *taskView) renderLaneSelector(width int) string {
	label := "this task's own output"
	if lane, ok := t.selectedLane(); ok {
		label = fmt.Sprintf("%d/%d · %s (task %d) · %s",
			t.laneSel+1, len(t.lanes), laneName(lane), lane.ID, lane.State)
		if lane.BlockReason != nil && *lane.BlockReason != "" {
			label += " · " + *lane.BlockReason
		}
	}
	line := "  Lane  " + styleSelected.Render(label) +
		styleDim.Render("   </> select lane · l open it")
	return ansi.Truncate(line, max(width, 1), "…")
}

// renderAttemptSelector is the Output pane's attempt strip. It takes the
// sub-model rather than reading t.detail because the pane below it is the
// selected lane's when one is selected, and a strip describing a different
// task's attempts than the pane under it is worse than no strip at all.
func renderAttemptSelector(d *detail, width int) string {
	runs := d.attempts()
	if len(runs) == 0 {
		return ansi.Truncate("  Attempt  —  no attempts", max(width, 1), "…")
	}
	i := d.runIndex(d.selectedRun)
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
		// An action, not a fact: a lane's parent is the one place a reader
		// standing in a lane wants to go, and a bare number was every route
		// they had (#316).
		relationships = append(relationships, taskDetailFact{
			"parent task",
			strconv.FormatInt(*task.ParentTaskID, 10) + "   " + styleDim.Render("U open it"),
		})
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

// ---------------------------------------------------------------------------
// Fan-out reach (#316): the back stack, the two jumps, and the Output pane's
// lane selector.
//
// A fan_out's lanes are child tasks (§7.6) and were reachable only by
// knowing their ids: the workspace rendered `parent task 41` as a number and
// `esc` always meant "the board". Everything below is one idea — a lane is a
// task, so opening one is opening a task, and coming back is popping the
// chain you came through rather than throwing it away.
// ---------------------------------------------------------------------------

// openTaskMsg is a jump made from inside the workspace: open `id`, and
// remember `from` so `esc` comes back to it. The root turns it into the
// board's own selectTaskMsg after pushing, so a lane opens by exactly the
// path every other task opens by.
type openTaskMsg struct {
	id    int64
	state string
	from  int64
}

// navPopMsg is the answer to `esc`: the stack with the popped entries already
// removed, and the task to land on. ok=false means nothing on the stack could
// be opened, and the board is the honest destination.
type navPopMsg struct {
	rest  []int64
	id    int64
	state string
	ok    bool
}

// taskLaneListMsg carries the workspace's own copy of GET /v1/tasks?parent_id=.
type taskLaneListMsg struct {
	taskID   int64
	children []apiclient.Task
	err      error
}

// taskLanePullsMsg carries one project pull-request listing, which is where
// the Pull Request tab's lane rows get their numbers and states.
type taskLanePullsMsg struct {
	taskID int64
	pulls  []apiclient.GitHubPullRequest
	err    error
}

// pushTask records the task a jump is leaving, to be pushed when the
// selectTaskMsg the jump turns into arrives. The root calls it.
func (t *taskView) pushTask(id int64) {
	if id != 0 {
		t.stackPush = id
	}
}

// popCmd is `esc`. It walks the stack from the top, dropping tasks that
// cannot be opened any more — archived, or gone — rather than popping to
// one, and falls through to the board when nothing is left.
//
// The walk is a command rather than a field read because "can this still be
// opened" is a question only the daemon can answer, and answering it wrongly
// strands a reader on a blank workspace.
func (t *taskView) popCmd() tea.Cmd {
	if len(t.stack) == 0 {
		return func() tea.Msg { return selectViewMsg{id: viewHome} }
	}
	stack := append([]int64(nil), t.stack...)
	alive := t.aliveFunc()
	return func() tea.Msg {
		for i := len(stack) - 1; i >= 0; i-- {
			if state, ok := alive(stack[i]); ok {
				return navPopMsg{rest: stack[:i], id: stack[i], state: state, ok: true}
			}
		}
		return navPopMsg{}
	}
}

func (t *taskView) applyPop(msg navPopMsg) tea.Cmd {
	if !msg.ok {
		t.stack = nil
		return func() tea.Msg { return selectViewMsg{id: viewHome} }
	}
	t.stack = msg.rest
	// The selectTaskMsg below is a pop, not a fresh open: the stack it lands
	// on is already the truncated one.
	t.stackKeep = true
	id, state := msg.id, msg.state
	return func() tea.Msg { return selectTaskMsg{id: id, state: state} }
}

// aliveFunc answers "can this task still be opened". Disconnected, the
// stack is all this view knows and is taken at its word — refusing to go
// back because the daemon is down would be the wrong kind of correct.
func (t *taskView) aliveFunc() func(int64) (string, bool) {
	if t.alive != nil {
		return t.alive
	}
	client := t.detail.client
	if client == nil {
		return func(int64) (string, bool) { return "", true }
	}
	return func(id int64) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		task, err := client.GetTask(ctx, id)
		if err != nil || task.ArchivedAt != nil {
			return "", false
		}
		return task.State, true
	}
}

// openLaneCmd is `l`: open the lane the current tab's selection resolves to,
// remembering this task so `esc` comes back to it.
func (t *taskView) openLaneCmd() tea.Cmd {
	id, ok := t.laneJump()
	if !ok {
		return nil
	}
	state := ""
	if lane, ok := t.laneByID(id); ok {
		state = lane.State
	}
	from := t.detail.taskID
	return func() tea.Msg { return openTaskMsg{id: id, state: state, from: from} }
}

// openParentCmd is `U`: the reciprocal of `l`. It works from any state the
// parent is in — a `blocked` or `done` parent's lanes are exactly the ones
// worth walking, which is the gap #316 opens on.
func (t *taskView) openParentCmd() tea.Cmd {
	id, ok := t.parentJump()
	if !ok {
		return nil
	}
	from := t.detail.taskID
	return func() tea.Msg { return openTaskMsg{id: id, from: from} }
}

// laneJump is the lane `l` opens from where the reader is standing. A tab that
// carries an explicit lane selection — the Workflow tab's graph cursor, the
// Output pane's selector, the Diff tab's lane sections — is taken at its word;
// every other tab means "the lane this task's failure is about", and the first
// lane in merge order when nothing is blamed.
func (t *taskView) laneJump() (int64, bool) {
	switch t.tab {
	case taskTabWorkflow:
		return t.graphLane()
	case taskTabOutput:
		if id, ok := t.outputLane(); ok {
			return id, true
		}
	case taskTabDiff:
		// The diff is grouped lane > file once the parent has merged
		// anything, so the row under the cursor names a lane on its own.
		// Falling through to the blamed lane here would open a different
		// lane than the one whose hunks the reader is reading.
		if id, ok := t.detail.diff.selectedLane(); ok {
			return id, true
		}
	case taskTabSteps:
		// The timeline's own answer is the row under the cursor, and only a
		// fan_out row has lanes to open.
		if !t.selectedFanOutRun() {
			return 0, false
		}
	}
	return t.blamedLane()
}

// parentJump is the task `U` opens: this lane's parent, or nothing at all for
// a task that is not a lane.
func (t *taskView) parentJump() (int64, bool) {
	if !t.detail.loaded || t.detail.task.ParentTaskID == nil {
		return 0, false
	}
	return *t.detail.task.ParentTaskID, true
}

// outputLane is the lane whose output the Output pane is showing, and false
// while it is showing the task's own.
func (t *taskView) outputLane() (childTaskID int64, ok bool) {
	lane, ok := t.selectedLane()
	if !ok {
		return 0, false
	}
	return lane.ID, true
}

func (t *taskView) selectedLane() (apiclient.Task, bool) {
	if t.laneSel < 0 || t.laneSel >= len(t.lanes) {
		return apiclient.Task{}, false
	}
	return t.lanes[t.laneSel], true
}

// selectLane is `<` and `>`. The cycle includes the task's own output, so a
// reader who walked into the lanes can walk back out of them the same way.
func (t *taskView) selectLane(delta int) tea.Cmd {
	if len(t.lanes) == 0 || delta == 0 {
		return nil
	}
	n := len(t.lanes) + 1
	t.laneSel = ((t.laneSel+1+delta)%n+n)%n - 1
	return t.syncLaneDetail()
}

// graphLane resolves the Workflow tab's selection to a lane.
//
// The join is against the lane columns the component already publishes —
// Column.Nodes is exactly the inline steps drawn inside one lane, and
// Column.Key is the lane's own key (workflowgraph.LaneKey). Node.Group is
// deliberately *not* it: a lane's inline node carries the enclosing fan_out
// group there, not the lane, so grouping on it would answer "some lane of
// this fan_out" rather than the one under the cursor.
func (t *taskView) graphLane() (int64, bool) {
	w := t.workflow
	if w == nil {
		return 0, false
	}
	node, ok := w.graph.SelectedNode()
	if !ok {
		return 0, false
	}
	for _, col := range w.graph.Lanes() {
		if col.Key != node.ID && !slices.Contains(col.Nodes, node.ID) {
			continue
		}
		if child, ok := t.laneByLaneID(col.ID); ok {
			return child.ID, true
		}
		return 0, false
	}
	return 0, false
}

// selectedFanOutRun reports whether the timeline's cursor is on a fan_out
// attempt — the row whose lanes `l` opens.
func (t *taskView) selectedFanOutRun() bool {
	return t.detail.runByID(t.detail.selectedRun).StepType == stepTypeFanOut
}

// blamedLane is the lane a tab with no selection of its own means: the one
// the join blamed, then whatever the Output pane is pointed at, then the
// first in merge order.
func (t *taskView) blamedLane() (int64, bool) {
	if len(t.lanes) == 0 {
		return 0, false
	}
	if blame, ok := t.detail.laneBlame(); ok && blame.taskID != 0 {
		return blame.taskID, true
	}
	if lane, ok := t.selectedLane(); ok {
		return lane.ID, true
	}
	return t.lanes[0].ID, true
}

func (t *taskView) laneByID(id int64) (apiclient.Task, bool) {
	for _, lane := range t.lanes {
		if lane.ID == id {
			return lane, true
		}
	}
	return apiclient.Task{}, false
}

func (t *taskView) laneByLaneID(id string) (apiclient.Task, bool) {
	for _, lane := range t.lanes {
		if lane.LaneID != nil && *lane.LaneID == id {
			return lane, true
		}
	}
	return apiclient.Task{}, false
}

// laneName is what a lane is called on screen: its lane id, and its title
// when the daemon recorded no lane id for it.
func laneName(lane apiclient.Task) string {
	if lane.LaneID != nil && *lane.LaneID != "" {
		return *lane.LaneID
	}
	return valueOr(lane.Title, "lane")
}

// lanesCmd fetches this task's lanes. It is the existing subtree listing —
// the rows already carry lane_id, lane_order, state, block_reason and
// branch_name, which is the whole of what the selector, the failure
// attribution and the Pull Request tab's rows need. No endpoint is added.
func (t *taskView) lanesCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		kids, err := client.ListTasks(ctx, apiclient.ListTasksOptions{ParentID: id})
		return taskLaneListMsg{taskID: id, children: kids, err: err}
	}
}

func (t *taskView) applyLaneList(msg taskLaneListMsg) tea.Cmd {
	if msg.taskID != t.detail.taskID || msg.err != nil {
		return nil
	}
	t.lanes, t.lanesTaskID = msg.children, msg.taskID
	// The timeline and the header render the lane's own block reason, which
	// the engine's message does not carry, so the sub-model needs the rows.
	t.detail.laneRows = msg.children
	if t.laneSel >= len(t.lanes) {
		t.laneSel = len(t.lanes) - 1
	}
	return t.syncLaneDetail()
}

// resetLanes drops everything about the task being left, tearing the lane
// subscription down with it.
func (t *taskView) resetLanes() tea.Cmd {
	t.lanes, t.lanesTaskID, t.laneSel = nil, 0, -1
	t.lanePulls = nil
	t.detail.laneRows = nil
	return t.syncLaneDetail()
}

// syncLaneDetail opens or closes the lane's sub-model so exactly one exists,
// for exactly the selected lane, while the workspace is on screen. It is the
// same shape detail.syncStream has and for the same reason: the subscription
// is derived from what is being looked at, never opened by hand.
func (t *taskView) syncLaneDetail() tea.Cmd {
	want := int64(0)
	if id, ok := t.outputLane(); ok && t.detail.active {
		want = id
	}
	if t.laneDetail != nil && t.laneDetail.taskID == want {
		return nil
	}
	if t.laneDetail != nil {
		t.laneDetail.active = false
		t.laneDetail.syncStream()
		t.laneDetail = nil
	}
	if want == 0 {
		return nil
	}
	lane := newDetail(t.detail.ctx, t.detail.level, t.detail.raw)
	lane.client = t.detail.client
	// The parent's opener, not the client's, so a test counting streams
	// counts this one too.
	lane.openStream = t.detail.openStream
	lane.active = true
	lane.width = t.width
	state := ""
	if row, ok := t.laneByID(want); ok {
		state = row.State
	}
	t.laneDetail = lane
	return lane.open(want, state)
}

// laneUpdate feeds the lane sub-model the same background messages the
// parent gets. Key presses and clicks never reach here — they are answered
// above, by the tab that has the keyboard.
func (t *taskView) laneUpdate(msg tea.Msg) tea.Cmd {
	if t.laneDetail == nil {
		return nil
	}
	return t.laneDetail.update(msg)
}
