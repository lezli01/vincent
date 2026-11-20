package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The wide workspace's steps rail (task 164.1).
//
// The Steps tab's timeline spends a heading per step and two or three rows
// per attempt, with wrapped output excerpts under a failure, so four steps
// fill a pane. The rail is the same data read for orientation instead: one
// row per step — state glyph, name, a right-aligned duration — on a
// connecting line, a dim second row only when the step has something to say,
// and attempts, members, iterations or lanes only where they are the point.
// The full timeline is still the narrow layout's Steps tab, which has a
// screen of its own to spend.
//
// Rows carry the ids windowTimeline and clickTimeline already understand: a
// run id for a row that stands for an attempt, -lane.ID for a lane, 0 for a
// row that selects nothing. A step's own rows carry its latest run, so a click
// on a step selects it.

// railStep is one workflow step as the rail draws it.
type railStep struct {
	index    int
	label    string
	kind     string
	followUp int
	runs     []apiclient.StepRun
}

// Step kinds the rail draws a shape for; anything else is a plain step.
const (
	railKindParallel  = "parallel"
	railKindLoop      = "loop"
	railKindManual    = "manual"
	railKindCondition = "condition"
)

// railSteps groups the attempts by step index and fills in the workflow's
// steps that have not run yet, from the snapshot, so the rail shows the whole
// way to done.
func (d *detail) railSteps() []railStep {
	runs := d.attempts()
	byIndex := map[int]int{}
	var out []railStep
	for _, r := range runs {
		i, ok := byIndex[r.StepIndex]
		if !ok {
			i = len(out)
			byIndex[r.StepIndex] = i
			out = append(out, railStep{index: r.StepIndex, followUp: d.followUpRound(r.StepIndex)})
		}
		out[i].runs = append(out[i].runs, r)
	}
	for _, s := range d.task.WorkflowSteps {
		if _, ok := byIndex[s.Index]; ok {
			continue
		}
		byIndex[s.Index] = len(out)
		out = append(out, railStep{index: s.Index})
	}
	groups, loops := groupedIndexes(runs), loopIndexes(runs)
	for i := range out {
		s := &out[i]
		s.kind = d.snapshotType(s.index)
		switch s.kind {
		case railKindParallel, railKindLoop, railKindManual, railKindCondition, stepTypeFanOut:
		default:
			s.kind = ""
		}
		switch {
		case s.kind != "":
		case loops[s.index]:
			s.kind = railKindLoop
		case groups[s.index]:
			s.kind = railKindParallel
		}
		s.label = d.railLabel(*s)
	}
	sortRailSteps(out)
	return out
}

func sortRailSteps(steps []railStep) {
	for i := 1; i < len(steps); i++ {
		for j := i; j > 0 && steps[j].index < steps[j-1].index; j-- {
			steps[j], steps[j-1] = steps[j-1], steps[j]
		}
	}
}

// snapshotType is the step type the task's snapshot gives the index, or "".
func (d *detail) snapshotType(index int) string {
	for _, s := range d.task.WorkflowSteps {
		if s.Index == index {
			return s.Type
		}
	}
	return ""
}

// railLabel names a step. A structural step writes no row of its own name, so
// its id comes from the snapshot, as structureLabel's does.
func (d *detail) railLabel(s railStep) string {
	if s.followUp > 0 {
		return fmt.Sprintf("follow-up %d", s.followUp)
	}
	for _, w := range d.task.WorkflowSteps {
		if w.Index == s.index && (w.Type == railKindParallel || w.Type == railKindLoop || w.Type == stepTypeFanOut) {
			return w.ID
		}
	}
	if len(s.runs) > 0 {
		return stepLabel(s.runs[0])
	}
	for _, w := range d.task.WorkflowSteps {
		if w.Index == s.index {
			return w.ID
		}
	}
	return "step " + strconv.Itoa(s.index+1)
}

// railState is what the step's glyph and colour say: its latest attempt's
// state, except that a step with any attempt running is running, and the step
// the task is stopped at speaks for the task.
func (d *detail) railState(s railStep) string {
	if len(s.runs) == 0 {
		return ""
	}
	for _, r := range s.runs {
		if r.Live() {
			return "running"
		}
	}
	return s.runs[len(s.runs)-1].State
}

// railCurrent reports whether the task is stopped at this step.
func (d *detail) railCurrent(s railStep) bool {
	return d.task.StepTotal > 0 && s.index == d.task.CurrentStep && s.followUp == 0
}

// railGlyph is the step's glyph and the style the step row takes.
func (d *detail) railGlyph(s railStep) (string, lipgloss.Style) {
	state := d.railState(s)
	if d.railCurrent(s) {
		switch d.task.State {
		case stateAwaitingGate, stateAwaitingInput:
			return attentionBadge, styleWarn.Bold(true)
		case stateBlocked:
			return ic("✗"), styleBad.Bold(true)
		}
	}
	if state == "" {
		return ic("○"), styleDim
	}
	style, ok := stepStateStyle(state)
	if !ok {
		style = lipgloss.Style{}
	}
	if state == "running" {
		style = style.Bold(true)
	}
	glyph := attemptStateGlyph(state)
	if state == "failed" || state == "rejected" {
		glyph = ic("✗")
	}
	switch s.kind {
	case stepTypeFanOut:
		if state == "running" {
			glyph = ic("◈")
		}
	case railKindLoop:
		if state == "running" {
			glyph = "⟳"
		}
	case railKindCondition:
		if state == "succeeded" {
			glyph = "◇"
		}
	}
	return glyph, style
}

// railRight is the step row's right-hand column: how long it took, or what it
// is waiting for.
func (d *detail) railRight(s railStep) string {
	if d.railCurrent(s) {
		switch d.task.State {
		case stateAwaitingGate:
			return styleWarn.Render("needs you")
		case stateAwaitingInput:
			return styleWarn.Render("asks you")
		case stateBlocked:
			return styleBad.Render("blocked")
		}
	}
	if len(s.runs) == 0 {
		// A gate not reached yet says so in its tag; the column stays empty.
		return ""
	}
	var total time.Duration
	seen := false
	for _, r := range s.runs {
		if dur, ok := r.Duration(d.now()); ok {
			total += dur
			seen = true
		}
	}
	if !seen {
		return ""
	}
	text := formatElapsed(total)
	if d.railState(s) == "running" {
		return styleFocus.Render(text)
	}
	return styleDim.Render(text)
}

// railMeta is the step's dim second row, or "" when it has nothing to add.
func (d *detail) railMeta(s railStep) string {
	if len(s.runs) == 0 {
		return ""
	}
	last := s.runs[len(s.runs)-1]
	if last.FailureReason != nil && *last.FailureReason != "" && last.State != "succeeded" {
		text := reasonTitle(*last.FailureReason)
		if n := len(s.runs); n > 1 && s.kind == "" {
			text += fmt.Sprintf(" · %d attempts", n)
		}
		return styleBad.Render(text)
	}
	if s.kind == stepTypeFanOut {
		if clauses := childrenBreakdown(d.task.Children); len(clauses) > 0 {
			return renderBreakdown(clauses)
		}
	}
	// A guard says what it tested (§7.7): a condition step, and any step a
	// false `if:` skipped. The text is the guard as it rendered.
	if guard := railGuard(last); guard != "" {
		return guard
	}
	var parts []string
	if s.kind == "" && len(s.runs) > 1 {
		parts = append(parts, styleWarn.Render(fmt.Sprintf("↻ %d attempts", len(s.runs))))
	}
	if last.Agent != nil && *last.Agent != "" {
		parts = append(parts, *last.Agent)
	}
	if cost := railCost(s.runs); cost != nil {
		parts = append(parts, formatCost(cost))
	} else if tokens := formatTokens(last); tokens != "" && last.Agent != nil {
		parts = append(parts, ansi.Strip(tokens))
	}
	if len(parts) == 0 {
		return ""
	}
	return styleDim.Render(strings.Join(parts, " · "))
}

// railCost sums what the step's attempts reported, nil when none did.
func railCost(runs []apiclient.StepRun) *float64 {
	var sum float64
	seen := false
	for _, r := range runs {
		if r.CostUSD != nil {
			sum += *r.CostUSD
			seen = true
		}
	}
	if !seen {
		return nil
	}
	return &sum
}

// railRows is the rail: its lines, their ids, and the selection's row span.
func (d *detail) railRows(width int) (lines []string, ids []int64, selFrom, selTo int) {
	steps := d.railSteps()
	sel := d.runByID(d.selectedRun)
	cursorLane := d.cursorLane()
	selFrom, selTo = 0, 1
	add := func(line string, id int64, selected bool) {
		line = ansi.Truncate(line, width, "…")
		if selected {
			line = styleSelected.Render(padDisplayWidth(ansi.Strip(line), width))
			selFrom, selTo = len(lines), len(lines)+1
		}
		lines = append(lines, line)
		ids = append(ids, id)
	}
	pipe := styleDim.Render("│")
	for n, s := range steps {
		lastStep := n == len(steps)-1
		var stepID int64
		if len(s.runs) > 0 {
			stepID = s.runs[len(s.runs)-1].ID
		}
		onStep := sel.ID != 0 && sel.StepIndex == s.index && cursorLane == 0
		glyph, style := d.railGlyph(s)
		left := style.Render(glyph + "  " + s.label)
		if tag := railKindTag(s.kind); tag != "" {
			left += styleDim.Render("  " + tag)
		}
		add(padBetweenCells(left, d.railRight(s), width), stepID, onStep && (len(s.runs) <= 1 || s.kind != ""))

		var body []railLine
		if meta := d.railMeta(s); meta != "" {
			body = append(body, railLine{text: meta})
		}
		body = append(body, d.railExpansion(s, sel, cursorLane, width-3)...)
		for _, b := range body {
			add(pipe+"  "+b.text, b.id, b.selected)
		}
		if len(body) == 0 && !lastStep {
			add(pipe, 0, false)
		}
	}
	return lines, ids, selFrom, selTo
}

// railLine is one row under a step.
type railLine struct {
	text     string
	id       int64
	selected bool
}

func railKindTag(kind string) string {
	switch kind {
	case stepTypeFanOut:
		return "fan-out"
	case railKindLoop:
		return "loop"
	case railKindParallel:
		return "parallel"
	case railKindManual:
		return "gate"
	case railKindCondition:
		return "condition"
	default:
		return ""
	}
}

// railExpansion is what opens under a step: its attempts when it was retried
// and is selected, a parallel group's members, a loop's passes and a
// fan-out's lanes.
func (d *detail) railExpansion(s railStep, sel apiclient.StepRun, cursorLane int64, width int) []railLine {
	switch s.kind {
	case stepTypeFanOut:
		return d.railLanes(s, cursorLane, width)
	case railKindLoop:
		return d.railLoop(s, sel, cursorLane, width)
	case railKindParallel:
		return d.railMembers(s.runs, sel, cursorLane, width)
	}
	if len(s.runs) < 2 || sel.ID == 0 || sel.StepIndex != s.index {
		return nil
	}
	out := make([]railLine, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, d.railAttempt(r, sel.ID == r.ID && cursorLane == 0, width))
	}
	return out
}

// railAttempt is one attempt row: `✗ 2  failed       0.4s`.
func (d *detail) railAttempt(r apiclient.StepRun, selected bool, width int) railLine {
	style, ok := stepStateStyle(r.State)
	if !ok {
		style = lipgloss.Style{}
	}
	glyph := attemptStateGlyph(r.State)
	if r.State == "failed" || r.State == "rejected" {
		glyph = ic("✗")
	}
	mark := "  "
	if selected {
		mark = "▸ "
	}
	left := mark + style.Render(fmt.Sprintf("%s %d", glyph, r.Attempt)) + "  " + styleDim.Render(r.State)
	right := ""
	if dur, ok := r.Duration(d.now()); ok {
		right = styleDim.Render(formatElapsed(dur))
	}
	return railLine{text: padBetweenCells(left, right, width), id: r.ID, selected: selected}
}

// railMembers is a parallel group's (or one loop pass's) members, as a tree:
// each member's latest attempt.
func (d *detail) railMembers(runs []apiclient.StepRun, sel apiclient.StepRun, cursorLane int64, width int) []railLine {
	var order []string
	latest := map[string]apiclient.StepRun{}
	for _, r := range runs {
		if _, ok := latest[r.StepID]; !ok {
			order = append(order, r.StepID)
		}
		latest[r.StepID] = r
	}
	out := make([]railLine, 0, len(order))
	for i, id := range order {
		r := latest[id]
		branch := "├─ "
		if i == len(order)-1 {
			branch = "└─ "
		}
		style, ok := stepStateStyle(r.State)
		if !ok {
			style = lipgloss.Style{}
		}
		left := styleDim.Render(branch) + style.Render(attemptStateGlyph(r.State)+" "+subStepLabel(r))
		right := ""
		if dur, ok := r.Duration(d.now()); ok {
			right = styleDim.Render(formatElapsed(dur))
		}
		selected := sel.ID != 0 && sel.StepIndex == r.StepIndex && sel.StepID == r.StepID && sel.Iteration == r.Iteration && cursorLane == 0
		out = append(out, railLine{text: padBetweenCells(left, right, width), id: r.ID, selected: selected})
	}
	return out
}

// railLoop is a loop's passes as pips, `3 / 5`, and — while the loop is
// selected — the selected pass's members.
func (d *detail) railLoop(s railStep, sel apiclient.StepRun, cursorLane int64, width int) []railLine {
	byIter := map[int][]apiclient.StepRun{}
	var iters []int
	total := 0
	for _, r := range s.runs {
		if _, ok := byIter[r.Iteration]; !ok {
			iters = append(iters, r.Iteration)
		}
		byIter[r.Iteration] = append(byIter[r.Iteration], r)
		total = max(total, r.LoopTotal)
	}
	var pips []string
	for _, it := range iters {
		rs := byIter[it]
		state := rs[len(rs)-1].State
		for _, r := range rs {
			if r.Live() {
				state = "running"
			}
		}
		style, ok := stepStateStyle(state)
		if !ok {
			style = lipgloss.Style{}
		}
		pips = append(pips, style.Render(attemptStateGlyph(state)))
	}
	for range max(total-len(iters), 0) {
		pips = append(pips, styleDim.Render("·"))
	}
	count := strconv.Itoa(len(iters))
	if total > 0 {
		count += " / " + strconv.Itoa(total)
	}
	out := []railLine{{text: padBetweenCells(strings.Join(pips, " "), styleDim.Render(count), width)}}
	if sel.ID == 0 || sel.StepIndex != s.index || len(iters) == 0 {
		return out
	}
	pass := sel.Iteration
	if _, ok := byIter[pass]; !ok {
		pass = iters[len(iters)-1]
	}
	out = append(out, railLine{text: styleDim.Render(fmt.Sprintf("%s %d", d.tierNoun(s.index), pass))})
	return append(out, d.railMembers(byIter[pass], sel, cursorLane, width)...)
}

// railLanes is a fan-out's lanes as a tree, its latest round's open and the
// earlier rounds one line each.
func (d *detail) railLanes(s railStep, cursorLane int64, width int) []railLine {
	latest := -1
	for _, r := range s.runs {
		latest = max(latest, r.Iteration)
	}
	var current []apiclient.Task
	earlier := map[int]int{}
	var rounds []int
	first := d.firstFanOutIndex()
	for _, lane := range d.laneRows {
		index := first
		if lane.ParentStepIndex != nil {
			index = *lane.ParentStepIndex
		}
		if index != s.index {
			continue
		}
		if lane.LaneRound != nil && *lane.LaneRound < latest {
			if _, ok := earlier[*lane.LaneRound]; !ok {
				rounds = append(rounds, *lane.LaneRound)
			}
			earlier[*lane.LaneRound]++
			continue
		}
		current = append(current, lane)
	}
	var out []railLine
	for _, round := range rounds {
		out = append(out, railLine{text: styleDim.Render(fmt.Sprintf("round %d · %s", round, countNoun(earlier[round], "lane")))})
	}
	tree := d.laneTree(current)
	for i, e := range tree {
		lane := e.lane
		branch := "├─ "
		if i == len(tree)-1 {
			branch = "└─ "
		}
		glyph := taskStateGlyph(lane.State)
		if glyph == "" {
			glyph = " "
		}
		pad := strings.Repeat("  ", e.depth)
		left := styleDim.Render(pad+branch) + laneRowStyle(lane).Render(glyph+" "+laneName(lane))
		right := styleDim.Render(fmt.Sprintf("#%d %s", lane.ID, railLaneState(lane.State)))
		out = append(out, railLine{text: padBetweenCells(left, right, width), id: -lane.ID, selected: lane.ID == cursorLane})
	}
	return out
}

// railLaneState is a lane's state in the rail's few cells.
func railLaneState(state string) string {
	switch state {
	case stateAwaitingGate, stateAwaitingInput:
		return "gate"
	case stateAwaitingChildren:
		return "lanes"
	default:
		return stateWords(state)
	}
}

// firstFanOutIndex is the first fan-out step's index, where a lane from a
// daemon older than parent_step_index goes, as laneSlots places it.
func (d *detail) firstFanOutIndex() int {
	for _, r := range d.attempts() {
		if r.StepType == stepTypeFanOut {
			return r.StepIndex
		}
	}
	return -1
}

// renderRail draws the rail in width×height and records the click geometry.
func (d *detail) renderRail(width, height int) string {
	if len(d.task.Steps) == 0 && len(d.task.WorkflowSteps) == 0 {
		d.visibleRuns = nil
		if !d.loaded {
			return styleDim.Render("loading…")
		}
		return styleDim.Render("queued — nothing has run yet")
	}
	lines, ids, from, to := d.railRows(width)
	return d.windowTimeline(lines, ids, nil, from, to, height)
}

// railDone is how many of the workflow's steps are behind the task, for the
// rail's `2/4`.
func railDone(task apiclient.Task) int {
	if task.State == stateDone {
		return task.StepTotal
	}
	return min(max(task.CurrentStep, 0), task.StepTotal)
}

// railGuard is the `if:` an attempt rendered, as the rail says it: what a
// condition step tested and how it came out, or why a guard skipped a step.
// "" for an attempt with no guard recorded.
func railGuard(r apiclient.StepRun) string {
	if r.RenderedIf == nil || strings.TrimSpace(*r.RenderedIf) == "" {
		return ""
	}
	guard := "if: " + oneLine(*r.RenderedIf)
	switch {
	case r.SkipReason != nil && *r.SkipReason == "condition":
		return styleDim.Render("skipped · " + guard + " was false")
	case r.State == stepStateStopped:
		return styleDim.Render(guard + " · the workflow ended here")
	default:
		return styleDim.Render(guard)
	}
}
