package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/reasons"
)

// The Overview tab (task 129.12, task 129 decision 1) is the workspace's
// landing tab: one screen that answers the question the task's state raises.
// It renders from the snapshot the detail sub-model already holds — no call
// and no field of its own (T3.2, snapshot-then-events) — so a state change
// that arrives while it is open swaps the frame on the next render and never
// touches the attempt cursor.

// overviewFrame is which of the three questions the Overview answers.
type overviewFrame int

const (
	// frameProgress is "what is it doing": running, queued, paused and
	// awaiting_children (decision 4 of the brief puts the parked fan-out
	// parent here — its lanes are the ones doing the work).
	frameProgress overviewFrame = iota
	// frameAttention is "what does it need from me": blocked, awaiting_input,
	// awaiting_gate and aborted.
	frameAttention
	// frameOutcome is "how did it end": done and archived.
	frameOutcome
)

// overviewFrameFor picks the frame from the §6 state alone. A state this
// client does not know — a daemon newer than it — reads as progress, the one
// frame that claims nothing about the task needing anyone.
func overviewFrameFor(state string) overviewFrame {
	switch state {
	case stateBlocked, stateAwaitingInput, stateAwaitingGate, stateAborted:
		return frameAttention
	case stateDone, stateArchived:
		return frameOutcome
	default:
		return frameProgress
	}
}

// overviewLink is one jump link: a tab digit and what pressing it shows.
type overviewLink struct {
	tab   taskViewTab
	label string
}

// overviewLinks are the frame's jump links. The keys are the tabs' own
// digits, so a link introduces no key; what it adds is the attempt the
// digit lands on (overviewAnchor).
func (t *taskView) overviewLinks() []overviewLink {
	if !t.detail.loaded {
		return nil
	}
	switch overviewFrameFor(t.detail.task.State) {
	case frameAttention:
		links := []overviewLink{{taskTabOutput, "output"}, {taskTabStepDetails, "what it was given"}}
		if run, ok := t.overviewAnchor(); ok {
			links[0].label = fmt.Sprintf("output of attempt %d", run.Attempt)
		}
		return links
	case frameOutcome:
		links := []overviewLink{{taskTabDiff, "diff"}}
		if t.pullTabAvailable() {
			links = append(links, overviewLink{taskTabPull, "PR"})
		}
		return links
	default:
		return []overviewLink{{taskTabOutput, "full output"}}
	}
}

// overviewLinkFor reports whether tab is one of the current frame's links.
func (t *taskView) overviewLinkFor(tab taskViewTab) (overviewLink, bool) {
	for _, l := range t.overviewLinks() {
		if l.tab == tab {
			return l, true
		}
	}
	return overviewLink{}, false
}

// overviewAnchor is the attempt the Overview is about: the live one while
// something runs, and otherwise the newest — which, for a task that stopped,
// is the attempt the block or the failure is on. Attempt ids are allocated in
// order, so newest is the largest id rather than the last row of the
// timeline's step-major sort. A blocked or aborted task is about the attempt
// its failure card names (cardRun), so the link and the card cannot differ.
func (t *taskView) overviewAnchor() (apiclient.StepRun, bool) {
	if t.showsFailureCard() {
		return cardRun(t.detail.task)
	}
	var newest, live apiclient.StepRun
	for _, run := range t.detail.task.Steps {
		if run.ID > newest.ID {
			newest = run
		}
		if run.Live() && run.ID > live.ID {
			live = run
		}
	}
	if live.ID != 0 {
		return live, true
	}
	return newest, newest.ID != 0
}

// jumpTab is every digit press, on every tab. On the Overview a digit that
// is one of the frame's links first moves the shared attempt cursor to the
// attempt the link is about, then switches; anywhere else it only switches.
// One path, so a link and a plain digit cannot drift apart. Diff and Pull
// Request are task-level and move nothing.
func (t *taskView) jumpTab(tab taskViewTab) tea.Cmd {
	var cmds []tea.Cmd
	if t.tab == taskTabOverview && (tab == taskTabOutput || tab == taskTabStepDetails) {
		if _, ok := t.overviewLinkFor(tab); ok {
			if run, ok := t.overviewAnchor(); ok && run.ID != t.detail.selectedRun {
				t.detail.selectedRun = run.ID
				cmds = append(cmds, t.detail.syncOutput())
			}
		}
	}
	return tea.Batch(append(cmds, t.setTab(tab))...)
}

// updateOverviewKey is what the Overview does with a key the workspace did
// not take: the §6 actions, and nothing that would move the attempt cursor
// behind a screen that does not draw it.
func (t *taskView) updateOverviewKey(msg tea.KeyPressMsg) tea.Cmd {
	d := t.detail
	if !d.actions.capturing() && msg.String() == opKey(keymap.EditRetry) {
		return d.update(msg)
	}
	cmd, _ := d.actions.handleKey(msg.String(), d.client, d.target())
	return cmd
}

// overviewLiveBindings drops the jump-link rows the current frame does not
// offer and gives the rest the frame's own wording, so the footer, `?` and
// the palette list exactly the links the body draws.
func (t *taskView) overviewLiveBindings(b binding) (binding, bool) {
	if b.op == keymap.Lane {
		// `l` is on the Overview for the failure card's blamed lane only.
		if !t.showsFailureCard() {
			return b, false
		}
		c := t.failureCard()
		return b, c.blamed && c.blame.taskID != 0
	}
	var tab taskViewTab
	switch b.key {
	case "3":
		tab = taskTabOutput
	case "4":
		tab = taskTabDiff
	case "6":
		tab = taskTabStepDetails
	case "7":
		tab = taskTabPull
	default:
		return b, true
	}
	l, ok := t.overviewLinkFor(tab)
	if !ok {
		return b, false
	}
	b.hint = b.key + " " + l.label
	return b, true
}

func (t *taskView) renderOverview(width, height int) string {
	lines := t.overviewLines(width, height)
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(width, 1), "…")
	}
	if len(lines) > height {
		lines = lines[:max(height, 1)]
	}
	return strings.Join(lines, "\n")
}

func (t *taskView) overviewLines(width, height int) []string {
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
	// The `#id title` line is the workspace's, drawn once above every tab
	// (task 129.9).
	out := []string{
		"  " + renderDetailState(task.Task),
		"",
	}
	switch {
	case t.showsFailureCard() && height < cardCollapseRows:
		// A short terminal keeps the card's first line and its keys; the
		// sentence goes with the rest (brief decision 8).
		out = append(out[:2], t.failureCardLines(width, height, true)...)
	case t.showsFailureCard():
		out = appendWrapped(out, overviewSentence(task, len(t.lanes)), width)
		out = append(out, "")
		out = append(out, t.failureCardLines(width, height-len(out), false)...)
	default:
		out = appendWrapped(out, overviewSentence(task, len(t.lanes)), width)
		out = append(out, "")
		var body []taskDetailFact
		switch overviewFrameFor(task.State) {
		case frameAttention:
			body = t.attentionFacts()
		case frameOutcome:
			body = t.outcomeFacts()
		default:
			body = t.progressFacts()
		}
		out = append(out, renderTaskDetailFactList(width, body)...)
		if t.overviewFrame() == frameAttention {
			out = append(out, t.overviewActionLines(width)...)
		}
	}
	if links := t.overviewLinks(); len(links) > 0 {
		parts := make([]string, len(links))
		for i, l := range links {
			parts[i] = styleKey.Render(taskTabDigits[l.tab]) + " " + l.label
		}
		out = append(out, "", "  "+strings.Join(parts, styleDim.Render("   ·   ")))
	}
	return out
}

func (t *taskView) overviewFrame() overviewFrame { return overviewFrameFor(t.detail.task.State) }

// overviewSentence is the one line that says what state the task is in, in
// words rather than the state's wire name.
func overviewSentence(task apiclient.TaskDetail, lanes int) string {
	step := ""
	if k, n, ok := task.StepDisplay(); ok {
		step = fmt.Sprintf(" step %d of %d", k, n)
		if task.StepName != "" {
			step += " (" + task.StepName + ")"
		}
	}
	switch task.State {
	case stateRunning:
		return "Running" + step + "."
	case stateQueued:
		if reason, _, ok := task.Hold(); ok {
			return "Queued, held: " + reason + "."
		}
		return "Queued, waiting for a free slot."
	case statePaused:
		return "Paused at" + valueOr(step, " its current step") + "; nothing runs until it is resumed."
	case stateAwaitingChildren:
		total, settled := lanes, 0
		if c := task.Children; c != nil {
			total, settled = c.Total, c.Settled
		}
		return fmt.Sprintf("Waiting on %d of %d lanes to settle.", total-settled, total)
	case stateBlocked:
		return "Blocked at" + valueOr(step, " its current step") + "; a human decides what happens next."
	case stateAwaitingInput:
		return "Waiting for an answer at" + valueOr(step, " its current step") + "."
	case stateAwaitingGate:
		return "Waiting at a gate for approval at" + valueOr(step, " its current step") + "."
	case stateAborted:
		return "Aborted; it runs no further."
	case stateDone:
		return "Done; every step finished."
	case stateArchived:
		return "Archived."
	default:
		return "State: " + task.State + "."
	}
}

// attemptName names an attempt the way the Output strip does.
func attemptName(run apiclient.StepRun) string {
	s := fmt.Sprintf("step %d %s · attempt %d · %s", run.StepIndex+1, stepLabel(run), run.Attempt, run.State)
	if run.Iteration > 0 {
		s = fmt.Sprintf("step %d %s · iteration %d · attempt %d · %s",
			run.StepIndex+1, stepLabel(run), run.Iteration, run.Attempt, run.State)
	}
	return s
}

// statusOf is an attempt's own status message, which stays neutral (task 036
// decision 6): it is what the step last said, never the failure.
func statusOf(run apiclient.StepRun) string {
	if run.StatusMessage != nil {
		return strings.TrimSpace(*run.StatusMessage)
	}
	return ""
}

func (t *taskView) progressFacts() []taskDetailFact {
	task := t.detail.task
	facts := []taskDetailFact{{"current step", taskStep(task.Task)}}
	run, ok := t.overviewAnchor()
	if ok {
		facts = append(facts, taskDetailFact{"attempt", attemptName(run)})
	}
	// While the task runs, the now-line under the app header is the one
	// place its latest status is said (task 129.9); the frames that hide the
	// now-line keep the fact.
	if task.State == stateRunning {
		return facts
	}
	status := ""
	if ok {
		status = statusOf(run)
	}
	if status == "" && task.StatusMessage != nil {
		status = *task.StatusMessage
	}
	return append(facts, taskDetailFact{"latest status", valueOr(status, "nothing said yet")})
}

func (t *taskView) attentionFacts() []taskDetailFact {
	task := t.detail.task
	var facts []taskDetailFact
	run, ok := t.overviewAnchor()
	reason := ""
	if task.BlockReason != nil {
		reason = *task.BlockReason
	}
	if reason == "" && ok && run.FailureReason != nil {
		reason = *run.FailureReason
	}
	if reason != "" {
		e := reasons.Explain(reason)
		value := e.Title
		if e.Title != reason {
			value += " (" + reason + ")"
		}
		if e.Meaning != "" {
			value += "\n" + e.Meaning
		}
		facts = append(facts, taskDetailFact{"reason", value})
	}
	if ok {
		facts = append(facts, taskDetailFact{"attempt", attemptName(run)})
	}
	if req, pending, err := task.PendingRequest(); err == nil && pending {
		facts = append(facts, taskDetailFact{"question", pendingText(req)})
	}
	if task.State == stateAwaitingGate && ok {
		for _, step := range task.WorkflowSteps {
			if step.Index == run.StepIndex && step.Instructions != "" {
				facts = append(facts, taskDetailFact{"gate", strings.TrimSpace(step.Instructions)})
				break
			}
		}
	}
	return facts
}

// pendingText is the question an awaiting_input task is asking.
func pendingText(req apiclient.InputRequest) string {
	if p := req.Permission; p != nil {
		return strings.TrimSpace("permission for " + p.Tool + ": " + p.Summary)
	}
	texts := make([]string, 0, len(req.Questions))
	for _, q := range req.Questions {
		texts = append(texts, q.Text)
	}
	return valueOr(strings.Join(texts, "\n"), req.Kind)
}

func (t *taskView) outcomeFacts() []taskDetailFact {
	task := t.detail.task
	final := ""
	if task.StatusMessage != nil {
		final = *task.StatusMessage
	}
	if run, ok := t.overviewAnchor(); ok && final == "" {
		final = valueOr(statusOf(run), strings.TrimSpace(run.ResultSummary))
	}
	return []taskDetailFact{
		{"final status", valueOr(final, "nothing said")},
		{"cost", formatCost(taskOwnCost(task))},
		{"branch", valueOr(task.BranchName, "not created")},
	}
}

type overviewActionDef struct {
	action string
	op     keymap.Op
	does   string
}

// overviewActions explains each §6 action the daemon offers, in the order
// the action bar draws them. The keys come from the effective keymap.
var overviewActions = []overviewActionDef{
	{apiclient.ActionAnswer, "", "answer the question it is waiting on"},
	{apiclient.ActionApprove, keymap.Approve, "let the gate pass and carry on"},
	{apiclient.ActionReject, keymap.Reject, "refuse the gate; the task blocks"},
	{apiclient.ActionRetry, keymap.Retry, "run the step again"},
	{apiclient.ActionRepair, keymap.Repair, "have an agent fix what stopped it, then carry on"},
	{apiclient.ActionSkip, keymap.Skip, "move past this step without running it"},
	{apiclient.ActionResume, keymap.Pause, "carry on from where it paused"},
	{apiclient.ActionPause, keymap.Pause, "stop at the next step boundary"},
	{apiclient.ActionCancel, keymap.Cancel, "kill what is running and abort the task"},
	{apiclient.ActionFollowUp, keymap.FollowUp, "run more work on the finished branch"},
	{apiclient.ActionChat, keymap.Chat, "open a chat on this task's worktree"},
	{apiclient.ActionArchive, keymap.Archive, "remove the worktree and file the task away"},
}

func (t *taskView) overviewActionLines(width int) []string {
	offered := t.detail.task.AvailableActions
	out := []string{"", section("What you can do"), ""}
	n := 0
	for _, a := range overviewActions {
		if !slices.Contains(offered, a.action) {
			continue
		}
		key := "enter"
		if a.op != "" {
			key = opKey(a.op)
		}
		if key == "" {
			continue // unbound by tui.keys: nothing to press
		}
		line := "  " + styleKey.Render(fmt.Sprintf("%-6s", key)) + " " + a.action + styleDim.Render(" — "+a.does)
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
		n++
	}
	if n == 0 {
		out = append(out, styleDim.Render("  no action available"))
	}
	return out
}
