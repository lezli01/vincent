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
	// frameOutcome is "how did it end": done and archived. An aborted task
	// is in the attention frame and carries the outcome card too, under its
	// failure card (task 129.14).
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
		if t.showsOutcomeCard() {
			// The outcome card under an aborted task's failure card links
			// what was delivered; `3` stays the failure card's.
			links = append(links, t.deliveredLinks()...)
		}
		return links
	case frameOutcome:
		return t.deliveredLinks()
	default:
		// No link may open an empty transcript (#799 W12): a parent waiting
		// on its lanes, or a task that has not run anything yet, has no
		// output of its own to show, and its steps are where it stands.
		if run, ok := t.overviewAnchor(); ok && (run.Live() || run.TranscriptPath != nil) &&
			t.detail.task.State != stateAwaitingChildren {
			return []overviewLink{{taskTabOutput, "full output"}}
		}
		return []overviewLink{{taskTabSteps, "steps"}}
	}
}

// lanesNeedingYouLines name, on a parent waiting on its lanes, every lane
// that is waiting on a human, and offer `l` to the first (#799 W12): "20 of
// 30 lanes to settle" alone hides that one of them never will by itself.
func (t *taskView) lanesNeedingYouLines(width int) []string {
	lanes := t.lanesNeedingYou()
	if len(lanes) == 0 {
		return nil
	}
	out := []string{"", section("Lanes that need you"), ""}
	for _, lane := range lanes {
		glyph, style := "⏸", styleWarn
		what := stateWords(lane.State)
		if lane.State == stateBlocked {
			glyph, style = "✗", styleBad
			if lane.BlockReason != nil && *lane.BlockReason != "" {
				what += reasonSeparator + reasonTitle(*lane.BlockReason)
			}
		}
		line := "  " + style.Render(glyph+" "+laneName(lane)) + "  " + styleDim.Render(fmt.Sprintf("#%d · %s", lane.ID, what))
		if at := t.laneAtText(lane); at != "" {
			line += styleDim.Render(" · ") + at
		}
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	if key := opKey(keymap.Lane); key != "" {
		out = append(out, "", "  "+styleKey.Render(key)+" open "+laneName(lanes[0]))
	}
	return out
}

// deliveredLinks are the outcome card's links: the diff and, when one is
// linked, the pull request.
func (t *taskView) deliveredLinks() []overviewLink {
	links := []overviewLink{{taskTabDiff, "diff"}}
	if t.pullTabAvailable() {
		links = append(links, overviewLink{taskTabPull, "PR"})
	}
	return links
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
	if t.tab == taskTabOverview && tab == taskTabOutput && t.showsFailureCard() {
		// The failure card's evidence link is `N`'s landing (task 129.18):
		// the attempt it stopped on, at the line it failed on.
		if run, ok := t.overviewAnchor(); ok {
			return t.landOnFailure(run)
		}
	}
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
	if !d.actions.capturing() {
		switch msg.String() {
		case "pgup", "pgdown", "home", "end":
			t.overview.Update(msg)
			return nil
		case "down", "j":
			// ↑/↓ walk the Linked card's rows that open somewhere (task
			// 168.7); the pane pages with pgup/pgdn.
			t.moveLinked(1)
			return nil
		case "up", "k":
			t.moveLinked(-1)
			return nil
		}
	}
	if !d.actions.capturing() && msg.String() == opKey(keymap.Result) {
		return t.jumpToResult()
	}
	if !d.actions.capturing() {
		// The Task Details document's two pull-request keys, which came to
		// the Overview with the document (task 168 decision 3).
		switch msg.String() {
		case opKey(keymap.Browser):
			return t.openPullCmd()
		case "P":
			return t.openCreatePR()
		}
	}
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
		// `l` is on the Overview for the failure card's blamed lane, and on
		// a parent waiting on its lanes for the first that needs a human.
		if !t.showsFailureCard() {
			return b, t.detail.task.State == stateAwaitingChildren && len(t.lanesNeedingYou()) > 0
		}
		c := t.failureCard()
		return b, c.blamed && c.blame.taskID != 0
	}
	if b.op == keymap.Result {
		// `w` is on the Overview for the outcome card's result source only.
		_, _, ok := summaryRun(t.detail.task)
		return b, ok && t.showsOutcomeCard()
	}
	if b.key == "enter" {
		// enter is the frame's first link, worded as the body words it.
		links := t.overviewLinks()
		if len(links) == 0 {
			return b, false
		}
		b.hint = b.key + " " + links[0].label
	}
	return b, true
}

// renderOverview draws the frame into the Overview's scroll pane (#799 W1):
// a frame taller than the screen scrolls — pgup/pgdown, home/end and the
// wheel — with the cue on its last row, rather than losing its last rows.
func (t *taskView) renderOverview(width, height int) string {
	if t.overviewTask != t.detail.taskID {
		t.overviewTask = t.detail.taskID
		t.overview.GotoTop()
	}
	// One row short: the cards and the task's document follow the frame, so
	// the pane always scrolls, and its cue takes the last row. The frame's
	// row budget has to leave it there or the cue covers the frame's link.
	lines := t.overviewLines(width, max(height-1, 1))
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(width, 1), "…")
	}
	t.overview.SetSize(width, height)
	t.overview.SetContentLines(lines)
	return strings.Join(t.overview.View(), "\n")
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
	out := []string{"  " + renderDetailState(task.Task)}
	// The issue the task was started from, as a fact rather than a section
	// (task 130 decision 21.4): what it is for, at a glance. The wide
	// layout's band and Linked card already name it (task 168).
	if iss := task.Issue; iss != nil && !t.wide() {
		out = append(out, "  "+styleDim.Render("issue ")+
			fmt.Sprintf("#%d %s", iss.ID, oneLine(iss.Title))+"  "+styleDim.Render(iss.State))
	}
	out = append(out, "")
	switch {
	case t.showsFailureCard():
		out = t.failureFrame(out, width, height)
	case overviewFrameFor(task.State) == frameOutcome:
		out = append(out, wrappedOrNone(overviewSentence(task, len(t.lanes)), width)...)
		out = append(out, "")
		// What can be done next, on one line kept ahead of the outcome
		// card's result, which shrinks by those two rows rather than giving
		// them up (#1068): a finished task named none of follow-up, a pull
		// request or archive.
		next := t.outcomeNextLine(width)
		out = append(out, t.outcomeCardLines(width, height-len(out)-overviewLinkRows-len(next))...)
		out = append(out, next...)
	default:
		out = append(out, wrappedOrNone(overviewSentence(task, len(t.lanes)), width)...)
		if bar := t.progressBar(width); bar != "" {
			out = append(out, bar)
		}
		out = append(out, "")
		if overviewFrameFor(task.State) == frameAttention {
			out = append(out, renderTaskDetailFactList(width, t.attentionFacts())...)
		} else {
			// What it is doing now beside what it is for (task 168.7).
			out = append(out, t.nowAndTaskCards(width)...)
		}
		if t.overviewFrame() == frameAttention {
			out = append(out, t.overviewActionLines(width)...)
		}
		if task.State == stateAwaitingChildren {
			out = append(out, t.lanesNeedingYouLines(width)...)
			out = append(out, t.waitingOnLines(width)...)
		}
	}
	out = append(out, t.handoffItemLines(width)...)
	if t.detail.form != nil {
		// A pending question takes enter on every tab (#1062), so the line
		// says what enter does now rather than the link it cannot reach.
		out = append(out, "", "  "+styleKey.Render("enter")+" "+styleAsk.Render("answer the question"))
	} else if links := t.overviewLinks(); len(links) > 0 {
		// One link, on enter (task 168.7): the digits that used to key a
		// row of them are gone with the numbered strip.
		out = append(out, "", "  "+styleKey.Render("enter")+" "+links[0].label)
	}
	// Under the frame: where the task's work lives and what it has cost,
	// then the task's whole document — the Task Details tab's sections,
	// which the Overview took over with the Details pane (task 168
	// decisions 1 and 3).
	out = append(out, "")
	out = append(out, t.overviewCards(width)...)
	if doc := t.detailLines(width); len(doc) > 1 {
		out = append(out, "", styleDim.Render("  "+strings.Repeat("─", max(width-4, 1))))
		out = append(out, doc[1:]...)
	}
	return out
}

// overviewLinkRows are the rows the jump links take under every frame: a
// blank and the links' line.
const overviewLinkRows = 2

// failureFrame is the attention frame of a blocked or aborted task, under
// head. The rows are spent in priority order (#799 W1) — the card's headline
// and actions, the jump links, then the card's meaning, evidence and status,
// then the sentence — so a taller screen never shows fewer of the card's
// keys than a shorter one. When even the card's headline and actions will
// not fit, the card collapses to its first line and its keys (brief decision
// 8), and the issue line goes with it; an aborted task's outcome card
// collapses before the failure card does, so it is gone there too.
func (t *taskView) failureFrame(head []string, width, height int) []string {
	if len(head)+t.minCardRows(width)+overviewLinkRows > height {
		return append([]string{head[0], ""}, t.collapsedCard(width)...)
	}
	budget := height - len(head) - overviewLinkRows
	card := t.failureCardLines(width, budget)
	out := head
	if sentence := wrappedOrNone(overviewSentence(t.detail.task, len(t.lanes)), width); len(card)+len(sentence)+1 <= budget {
		out = append(append(out, sentence...), "")
	}
	out = append(out, card...)
	if t.showsOutcomeCard() {
		// Under the failure card, in what is left once the jump links
		// have their two rows (task 129.14 decision 2). Its blank separator
		// comes only with a card: a separator over nothing pushed the frame
		// a row over, and the scroll cue then took the links' row
		// (review F14).
		if outcome := t.outcomeCardLines(width, height-len(out)-1-overviewLinkRows); len(outcome) > 0 {
			out = append(append(out, ""), outcome...)
		}
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
			return "Queued, held: " + reasonTitle(reason) + "."
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

// outcomeNextLine is a finished task's next steps as one line of keys —
// follow up, a pull request when one can be opened, chat, archive — under a
// blank row; nil when none is offered.
func (t *taskView) outcomeNextLine(width int) []string {
	offered := t.detail.task.AvailableActions
	var parts []string
	add := func(key, label string) {
		if key != "" {
			parts = append(parts, styleKey.Render(key)+" "+label)
		}
	}
	if slices.Contains(offered, apiclient.ActionFollowUp) {
		add(opKey(keymap.FollowUp), "follow up")
	}
	if t.pullOffered() {
		add("P", "open a pull request")
	}
	if slices.Contains(offered, apiclient.ActionChat) {
		add(opKey(keymap.Chat), "chat")
	}
	if slices.Contains(offered, apiclient.ActionArchive) {
		add(opKey(keymap.Archive), "archive")
	}
	if len(parts) == 0 {
		return nil
	}
	line := "  " + styleDim.Render("next  ") + strings.Join(parts, styleDim.Render(" · "))
	return []string{"", ansi.Truncate(line, max(width, 1), "…")}
}

// pullOffered is whether `P` would open the pull-request form: nothing is
// linked and the daemon could build a compare URL.
func (t *taskView) pullOffered() bool {
	return t.pullTabAvailable() && !t.pull.Linked && t.pull.CompareURL != ""
}

// openIssueKey opens the task's linked issue from its workspace (task 168
// decision 2). A fixed key rather than a vocabulary term: `i` already means
// other things on other screens, and a term's key must mean one thing
// everywhere (§15). The Pull request tab keeps its own `i`, its comment.
const openIssueKey = "i"

// openIssueCmd opens the task's linked issue (task 168 decision 2), or nil
// when it has none. esc on the issue comes back to this task, on this tab.
func (t *taskView) openIssueCmd() tea.Cmd {
	iss := t.detail.task.Issue
	if !t.detail.loaded || iss == nil {
		return nil
	}
	msg := openIssueMsg{id: iss.ID, back: viewTask, projectID: t.detail.task.ProjectID}
	return func() tea.Msg { return msg }
}

// paletteExtras are the workspace's key-less palette rows. "Open this task's
// issue" is one (task 130 decision 21.4): it has no key because the
// workspace's single keys are spent, and a task started from an issue is the
// one that needs it. esc on the issue comes back here.
func (t *taskView) paletteExtras() []paletteEntry {
	iss := t.detail.task.Issue
	if !t.detail.loaded || iss == nil {
		return nil
	}
	msg := openIssueMsg{id: iss.ID, back: viewTask, projectID: t.detail.task.ProjectID}
	return []paletteEntry{{
		group:  "task",
		label:  fmt.Sprintf("open this task's issue — #%d %s", iss.ID, oneLine(iss.Title)),
		action: func() tea.Msg { return msg },
	}}
}
