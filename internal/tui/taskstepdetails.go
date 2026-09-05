package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task workspace's Step Details tab (issue #323, spec §15 view 2).
//
// Task Details' workflow snapshot answers "what does this workflow say"; this
// answers "what did *this attempt* actually get". The two are the template and
// the substitution, and seeing both is the point — neither replaces the other,
// which is why the snapshot section is left exactly as it was.
//
// The layout mirrors the Task Details inspector: a sidebar against an
// independently scrolled content pane. Only what the sidebar lists differs —
// attempts here, document sections there — and that difference is why this is
// a sibling of detailsPane rather than a second instance of it. detailsPane
// owns its selection; this one must not, because the attempt cursor is the
// workspace's, shared with the timeline, Output and Diff (task 049 decision
// 4). Arriving from Output lands on the attempt already being read, ←/→ keep
// working, and a move made here is still made when you go back.

// stepInputNotRecorded is what a nil recorded field says. nil means the record
// does not exist for this attempt — a row written before migration 0027 — and
// drawing it as an empty body would claim the step was handed nothing, which
// is the one thing this tab must never say. A non-nil empty string is a render
// that genuinely produced nothing and is said differently.
const stepInputNotRecorded = "not recorded (this attempt predates the record)"

// stepInputRenderedEmpty is a recorded render that came out empty.
const stepInputRenderedEmpty = "(rendered empty)"

// stepDetailsPane is the tab's scroll offset and the strip's drawn geometry,
// and nothing else. drawn is the attempt the content was last built for, so a
// move of the shared cursor starts the new attempt at the top rather than
// halfway down somebody else's facts.
type stepDetailsPane struct {
	top   int
	count int
	h     int

	sidebarW   int
	sidebarY   int
	sidebarTop int
	drawn      int64
}

func (p *stepDetailsPane) reset() {
	p.top, p.drawn = 0, 0
}

func (p *stepDetailsPane) clamp() {
	p.top = min(max(p.top, 0), max(p.count-p.h, 0))
}

// render draws the pane at width×height. runs is the attempt list in timeline
// order and selected is the shared cursor; lines produces the selected
// attempt's facts at the content width the pane settles on.
func (p *stepDetailsPane) render(width, height int, runs []apiclient.StepRun, selected int64, lines func(width int) []string) string {
	p.sidebarW = min(30, max(width/3, 18))
	contentWidth := max(width-p.sidebarW-3, 12)
	if selected != p.drawn {
		p.drawn, p.top = selected, 0
	}

	content := lines(contentWidth)
	bodyH := max(height, 1)
	p.sidebarY = 0
	p.count, p.h = len(content), bodyH
	p.clamp()
	visible := windowRange(content, p.top, p.top+bodyH, bodyH)
	sidebar := p.renderSidebar(runs, selected, bodyH)

	out := make([]string, 0, bodyH)
	separator := " " + styleDim.Render("│") + " "
	for row := range bodyH {
		left := ""
		if row < len(sidebar) {
			left = sidebar[row]
		}
		right := ""
		if row < len(visible) {
			right = ansi.Truncate(visible[row], contentWidth, "…")
		}
		out = append(out, padDisplayWidth(left, p.sidebarW)+separator+right)
	}
	return strings.Join(out, "\n")
}

func (p *stepDetailsPane) renderSidebar(runs []apiclient.StepRun, selected int64, height int) []string {
	lines := make([]string, 0, height)
	cursor := 0
	for i, r := range runs {
		if r.ID == selected {
			cursor = i
			break
		}
	}
	p.sidebarTop = windowStart(len(runs), cursor, height)
	end := min(p.sidebarTop+height, len(runs))
	for _, r := range runs[p.sidebarTop:end] {
		label := "  " + ansi.Truncate(stepDetailsAttemptLabel(r), max(p.sidebarW-4, 1), "…")
		label = padDisplayWidth(label, p.sidebarW)
		if r.ID == selected {
			label = styleSelected.Render("› " + strings.TrimPrefix(label, "  "))
		} else {
			label = styleDim.Render(label)
		}
		lines = append(lines, label)
	}
	if p.sidebarTop == 0 && end == len(runs) && len(lines)+3 <= height {
		lines = append(lines, "", styleDim.Render("  ↑/↓ attempts"), styleDim.Render("  pgup/pgdn scroll"))
	}
	return lines
}

// stepDetailsAttemptLabel is one sidebar row: the step, the iteration when
// there is one, and the attempt number. Short by design — the sidebar is
// narrow, and the content pane repeats the identity in full.
func stepDetailsAttemptLabel(r apiclient.StepRun) string {
	label := strconv.Itoa(r.StepIndex+1) + " " + stepLabel(r)
	if r.Iteration > 0 {
		label += fmt.Sprintf(" ▸%d", r.Iteration)
	}
	return label + fmt.Sprintf(" #%d", r.Attempt)
}

// renderStepDetails is the workspace's Step Details tab.
func (t *taskView) renderStepDetails(width, height int) string {
	d := t.detail
	if note := stepDetailsPlaceholder(d); note != nil {
		return strings.Join(windowRange(note, 0, len(note), height), "\n")
	}
	return t.stepDetails.render(width, height, d.attempts(), d.selectedRun, t.stepDetailLines)
}

// stepDetailsPlaceholder is the document before there is one to show. A
// placeholder has no attempts to put in a sidebar, so it is drawn at the full
// width instead — the same rule detailsPane's `ready` encodes.
func stepDetailsPlaceholder(d *detail) []string {
	switch {
	case d.taskID == 0:
		return []string{styleDim.Render("  no task selected")}
	case !d.loaded && d.loadErr != nil:
		return []string{styleBad.Render("  task unavailable: " + errString(d.loadErr))}
	case !d.loaded:
		return []string{styleDim.Render("  loading task…")}
	case len(d.attempts()) == 0:
		return []string{styleDim.Render("  no attempts yet — nothing has been handed to a step")}
	}
	return nil
}

// updateStepDetailsKey is the tab's navigation. ↑/↓ and ←/→ both move the
// *shared* attempt cursor rather than a local one, so the move is still made
// when the reader goes back to Output; pgup/pgdn scroll this pane's own body.
// Everything else falls through to the task actions, as the Task Details tab
// does — the tab is a read-only inspector, not a mode.
func (t *taskView) updateStepDetailsKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k", "left":
		return t.detail.moveSelection(-1)
	case "down", "j", "right":
		return t.detail.moveSelection(1)
	case "pgup":
		t.stepDetails.top -= max(t.stepDetails.h-1, 1)
		t.stepDetails.clamp()
		return nil
	case "pgdown":
		t.stepDetails.top += max(t.stepDetails.h-1, 1)
		t.stepDetails.clamp()
		return nil
	case "home", "g":
		return t.selectAttemptAt(0)
	case "end", "G":
		return t.selectAttemptAt(len(t.detail.attempts()) - 1)
	}
	return t.detail.update(msg)
}

func (t *taskView) selectAttemptAt(i int) tea.Cmd {
	runs := t.detail.attempts()
	if len(runs) == 0 {
		return nil
	}
	return t.detail.selectRun(runs, i)
}

// clickStepDetailsSidebar selects the attempt under a click, where x and y are
// relative to the pane's own top-left corner.
func (t *taskView) clickStepDetailsSidebar(x, y int) tea.Cmd {
	runs := t.detail.attempts()
	row := t.stepDetails.sidebarTop + y - t.stepDetails.sidebarY
	if x > t.stepDetails.sidebarW+1 || y < t.stepDetails.sidebarY || row < 0 || row >= len(runs) {
		return nil
	}
	return t.detail.selectRun(runs, row)
}

// scrollStepDetailsAt is the wheel: over the sidebar it walks attempts, over
// the content it scrolls the body — the two scroll independently, as they do
// on Task Details.
func (t *taskView) scrollStepDetailsAt(x, delta int) tea.Cmd {
	if x <= t.stepDetails.sidebarW+1 {
		return t.detail.moveSelection(delta)
	}
	t.stepDetails.top += delta
	t.stepDetails.clamp()
	return nil
}

// stepDetailLines is the selected attempt's document: what it was given, what
// resolved it, what steered it and what it produced, in that order — the order
// a reader asking "why did this attempt do that" wants them in.
func (t *taskView) stepDetailLines(width int) []string {
	d := t.detail
	run := d.runByID(d.selectedRun)
	if run.ID == 0 {
		runs := d.attempts()
		if len(runs) == 0 {
			return []string{styleDim.Render("  no attempts yet")}
		}
		run = runs[len(runs)-1]
	}

	identity := fmt.Sprintf("  step %d  %s", run.StepIndex+1, stepLabel(run))
	trailer := fmt.Sprintf("  ·  %s  ·  attempt %d  ·  %s", run.StepType, run.Attempt, run.State)
	if run.Iteration > 0 {
		trailer = fmt.Sprintf("  ·  %s  ·  iteration %d  ·  attempt %d  ·  %s",
			run.StepType, run.Iteration, run.Attempt, run.State)
	}
	out := []string{styleTitle.Render(identity) + styleDim.Render(trailer)}

	out = appendTaskDetailSection(out, "Input", stepInputLines(run, width))
	out = appendTaskDetailSection(out, "Resolution", stepResolutionLines(run, t.stepDefFor(run), width))
	out = appendTaskDetailSection(out, "Control flow", t.stepControlFlowLines(run, width))
	out = appendTaskDetailSection(out, "Outcome", stepOutcomeLines(run, d.now(), width))
	return out
}

// stepDefFor is this attempt's step in the task's workflow snapshot, which is
// where the §7.9 include chain lives. The id is tried first: a `parallel`
// group's sub-steps share their group's index (task 014), so the index alone
// would answer with the group.
func (t *taskView) stepDefFor(run apiclient.StepRun) apiclient.WorkflowStep {
	steps := t.detail.task.WorkflowSteps
	if run.StepID != "" {
		for _, step := range steps {
			if step.ID == run.StepID {
				return step
			}
		}
	}
	for _, step := range steps {
		if step.Index == run.StepIndex {
			return step
		}
	}
	return apiclient.WorkflowStep{}
}

// stepInputLines is what the adapter or the shell was handed.
func stepInputLines(run apiclient.StepRun, width int) []string {
	var out []string
	if run.InputTruncated {
		// Said out loud rather than elided: the record has a size ceiling, and
		// silently showing a prefix as though it were the whole thing is what
		// this design refused.
		out = append(out,
			styleWarn.Render("  ⚠  the record was cut at its 64 KiB ceiling — what follows is a prefix of what the step got"),
			"")
	}
	if run.RenderedPrompt != nil || run.StepType == "agent" {
		out = append(out, styleDim.Render("  rendered prompt"))
		out = append(out, stepRenderedBlock(run.RenderedPrompt, width, true)...)
		out = append(out, "")
	}
	if run.RenderedRun != nil || run.StepType == "command" {
		out = append(out, styleDim.Render("  rendered run"))
		out = append(out, stepRenderedBlock(run.RenderedRun, width, false)...)
		out = append(out, "")
	}
	// `check:` is a field an agent or command step *may* carry (§8.2), so an
	// absent one is not a missing record and is not reported as one — unlike
	// the prompt of an agent step or the run of a command step, which the type
	// guarantees.
	if run.RenderedCheck != nil {
		out = append(out, styleDim.Render("  rendered check"))
		out = append(out, stepRenderedBlock(run.RenderedCheck, width, false)...)
		out = append(out, "")
	}
	out = append(out, styleDim.Render("  result summary"))
	if strings.TrimSpace(run.ResultSummary) == "" {
		out = append(out, styleDim.Render("    none"))
	} else {
		out = appendWrappedIndented(out, run.ResultSummary, width, "    ")
	}
	return out
}

// stepRenderedBlock draws one recorded input. markTrailer is set for an agent
// step's prompt, where the daemon appends the previous attempt's failure on a
// retry: the join is marked so a reader can tell what the workflow wrote from
// what vincent added.
func stepRenderedBlock(value *string, width int, markTrailer bool) []string {
	if value == nil {
		return []string{styleDim.Render("    " + stepInputNotRecorded)}
	}
	if *value == "" {
		return []string{styleDim.Render("    " + stepInputRenderedEmpty)}
	}
	if !markTrailer {
		return appendWrappedIndented(nil, *value, width, "    ")
	}
	body, trailer := splitFailureTrailer(*value)
	out := appendWrappedIndented(nil, body, width, "    ")
	if trailer == "" {
		return out
	}
	out = append(out, "", styleDim.Render("    ── appended by vincent: the previous attempt's failure ──"), "")
	return appendWrappedIndented(out, trailer, width, "    ")
}

// failureTrailerTag opens the block a retried agent step's prompt carries
// (§8.4). It is the daemon's, not the workflow's.
const failureTrailerTag = "<previous-attempt-failure"

// splitFailureTrailer separates the workflow's own render from the daemon's
// appended block. An attempt that was not a retry has no trailer.
func splitFailureTrailer(prompt string) (body, trailer string) {
	i := strings.Index(prompt, failureTrailerTag)
	if i < 0 {
		return prompt, ""
	}
	return strings.TrimRight(prompt[:i], "\n"), prompt[i:]
}

// stepResolutionLines is the §8.6 resolution the row journaled. These are the
// attempt's own values and not a re-resolution: config hot-reloads and task
// patches can make what a client would compute now disagree with what ran, and
// the row is the one that is right.
func stepResolutionLines(run apiclient.StepRun, def apiclient.WorkflowStep, width int) []string {
	return renderTaskDetailFacts(width, []taskDetailFact{
		{"agent", stepSourced(run.Agent, run.AgentSource)},
		{"model", stepSourced(run.Model, run.ModelSource)},
		{"effort", stepSourced(run.Effort, run.EffortSource)},
		{"permission mode", stepRecorded(run.PermissionMode)},
		{"timeout", stepRecordedMS(run.TimeoutMS)},
		{"check timeout", stepRecordedMS(run.CheckTimeoutMS)},
		{"shell", stepRecorded(run.Shell)},
		{"working dir", stepRecorded(run.WorkDir)},
		{"resolved from", stepResolvedFrom(def)},
	})
}

// stepSourced is one of the three resolved values with the level that supplied
// it: step, task, workflow or adapter (§8.6). The value without its level is
// still worth showing — an older row has the one and not the other.
//
// One space, not two: the field column word-wraps its value, and wrapping
// rejoins on single spaces, so a wider separator is written here and never
// reaches the screen. Spelling it as it renders keeps the two in agreement.
func stepSourced(value, source *string) string {
	if value == nil || *value == "" {
		return stepInputNotRecorded
	}
	if source == nil || *source == "" {
		return *value + " (level not recorded)"
	}
	return *value + " (from the " + *source + ")"
}

func stepRecorded(value *string) string {
	if value == nil || *value == "" {
		return stepInputNotRecorded
	}
	return *value
}

func stepRecordedMS(ms int64) string {
	if ms <= 0 {
		return stepInputNotRecorded
	}
	return formatElapsed(time.Duration(ms) * time.Millisecond)
}

// stepResolvedFrom is the §7.9 include chain, read straight off the task's
// snapshot — it already reaches the client, and Task Details and the graph's
// step modal already render it. An empty chain is a step the task's own
// workflow wrote.
func stepResolvedFrom(def apiclient.WorkflowStep) string {
	if len(def.ResolvedFrom) == 0 {
		return "this task's own workflow"
	}
	return strings.Join(def.ResolvedFrom, " → ")
}

// stepControlFlowLines is what decided that this attempt ran, and on what.
func (t *taskView) stepControlFlowLines(run apiclient.StepRun, width int) []string {
	facts := make([]taskDetailFact, 0, 5)
	// A guard is re-evaluated every time it is reached and is never sticky
	// (§7.7), so this is display only: what it rendered to on this attempt,
	// not a decision anything reads back. A step with no `if:` has no record
	// missing, so it is simply not asked about.
	if run.RenderedIf != nil {
		facts = append(facts, taskDetailFact{"if: rendered to", stepGuardValue(*run.RenderedIf)})
	}
	facts = append(facts, taskDetailFact{"iteration", stepIterationValue(run)})
	if run.LoopItem != nil {
		facts = append(facts, taskDetailFact{"for_each item", valueOr(*run.LoopItem, stepInputRenderedEmpty)})
	}
	if run.RenderedForEach != nil {
		facts = append(facts, taskDetailFact{"for_each list", stepForEachValue(*run.RenderedForEach)})
	}
	// The lane is the *open task's* own, which is the whole of what this tab
	// is about: a lane's inputs are read by opening the lane, which `l` and
	// `U` already make a short trip.
	if lane := t.detail.task.LaneID; lane != nil && *lane != "" {
		facts = append(facts, taskDetailFact{"fan-out lane", *lane})
	}
	return renderTaskDetailFacts(width, facts)
}

func stepGuardValue(rendered string) string {
	if rendered == "" {
		return stepInputRenderedEmpty
	}
	return rendered
}

func stepIterationValue(run apiclient.StepRun) string {
	if run.Iteration == 0 {
		return "not inside a loop"
	}
	if run.LoopTotal > 0 {
		return fmt.Sprintf("%d of %d", run.Iteration, run.LoopTotal)
	}
	return fmt.Sprintf("%d (total not recorded)", run.Iteration)
}

// stepForEachValue unpacks the resolved item list, which is carried as a JSON
// array. Unparseable bytes are shown raw rather than swallowed: the record is
// evidence, and a client that hides what it cannot read is worse than one that
// shows it.
func stepForEachValue(raw string) string {
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return raw
	}
	if len(items) == 0 {
		return "resolved to no items"
	}
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%d. %s", i+1, item)
	}
	return strings.Join(lines, "\n")
}

// stepOutcomeLines is what the attempt cost and what became of it.
func stepOutcomeLines(run apiclient.StepRun, now time.Time, width int) []string {
	active := "not started"
	if dur, ok := run.Duration(now); ok {
		active = formatElapsed(dur)
	}
	wait := "none"
	if run.InputWaitMS > 0 {
		wait = formatElapsed(time.Duration(run.InputWaitMS) * time.Millisecond)
	}
	return renderTaskDetailFacts(width, []taskDetailFact{
		{"tokens", valueOr(formatTokens(run), "not reported")},
		{"cost", formatCost(run.CostUSD)},
		{"active duration", active},
		{"waiting on a human", wait},
		{"exit code", stepExitCode(run.ExitCode)},
		{"check exit code", stepExitCode(run.CheckExitCode)},
		{"failure reason", stepOptional(run.FailureReason, "none")},
		{"skip reason", stepOptional(run.SkipReason, "none")},
		{"edited before retry", stepOverrideValue(run)},
		{"transcript", stepOptional(run.TranscriptPath, "none — this step produced no transcript")},
	})
}

func stepExitCode(code *int) string {
	if code == nil {
		return "none"
	}
	return strconv.Itoa(*code)
}

func stepOptional(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

// stepOverrideValue is the §6 edit+retry badge: a human replaced the step's
// text before this attempt ran, which is why the rendered input above may not
// match the workflow snapshot at all.
func stepOverrideValue(run apiclient.StepRun) string {
	switch {
	case run.PromptOverride && run.RunOverride:
		return "yes — the prompt and the command"
	case run.PromptOverride:
		return "yes — the prompt"
	case run.RunOverride:
		return "yes — the command"
	}
	return "no"
}
