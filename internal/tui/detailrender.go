package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

var (
	styleStepHeader = lipgloss.NewStyle().Bold(true)
	styleSelected   = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	styleTool       = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	styleStderr     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Faint(true)
	styleAsk        = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	styleFocus      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// editedBadge marks an attempt whose prompt or command a human rewrote
// before retrying it (§6). A glyph, so it survives a monochrome terminal.
const editedBadge = "✎"

// timelinePanel renders the timeline pane's content for the shell: the task
// header, a stale-refresh note when there is one, and the attempt timeline.
// At one line — the collapsed band — it shows the selected attempt, which is
// the line the cursor would land on (§15: title bar plus the selected line).
func (d *detail) timelinePanel(height int) string {
	if d.taskID == 0 {
		return styleDim.Render("  no task selected")
	}
	if height <= 1 {
		if run := d.runByID(d.selectedRun); run.ID != 0 {
			// The collapsed band has no header above it to indent under.
			return d.attemptLine(run, false)
		}
		return d.headerLine()
	}
	var sb strings.Builder
	sb.WriteString(d.headerLine())
	used := 1
	if line := d.errorLine(); line != "" && height > 2 {
		sb.WriteString("\n")
		sb.WriteString(line)
		used++
	}
	sb.WriteString("\n")
	sb.WriteString(d.renderTimeline(height - used))
	d.timelineTop = used
	return sb.String()
}

// clickTimeline selects the attempt rendered at the given content line —
// the mouse half of "selecting an attempt is how scrollback is navigated".
// Step headers and chrome lines fall through to a plain focus click.
func (d *detail) clickTimeline(line int) tea.Cmd {
	idx := line - d.timelineTop
	if idx < 0 || idx >= len(d.visibleRuns) {
		return nil
	}
	id := d.visibleRuns[idx]
	if id == 0 || id == d.selectedRun {
		return nil
	}
	d.selectedRun = id
	return d.syncOutput()
}

// clickOutput is a click anywhere on the output panel: the title row switches
// the tab, and a row of the diff tab folds the file it names (task 012). x,y
// are box-relative, so the panel's border is row 0 and the content starts at
// row 1. The output tab's body has nothing to click — an attempt is selected
// in the timeline, not in its own scrollback.
func (d *detail) clickOutput(x, y int, focused bool) tea.Cmd {
	if y == 0 {
		return d.clickOutputTitle(x, y, focused)
	}
	if d.tab == tabDiff {
		d.diff.clickRow(y - 1)
	}
	return nil
}

// clickOutputTitle switches the output|diff tab when the click lands on its
// span in the panel title (§15: click a tab). x,y are box-relative; the
// focus glyph shifts the spans by two cells.
func (d *detail) clickOutputTitle(x, y int, focused bool) tea.Cmd {
	if y != 0 {
		return nil
	}
	start := 3 // after "┌─ "
	if focused {
		start += 2 // the "▸ " glyph
	}
	const outputW, sepW, diffW = 6, 3, 4
	switch {
	case x >= start && x < start+outputW:
		if d.tab == tabDiff {
			return d.toggleTab()
		}
	case x >= start+outputW+sepW && x < start+outputW+sepW+diffW:
		if d.tab == tabOutput {
			return d.toggleTab()
		}
	}
	return nil
}

// outputPanel renders the output|diff pane's content for the shell. The tab
// strip lives in the panel title (outputTitle), so the content is the pane
// alone. At one line it shows the tail's last line — the freshest thing a
// collapsed output pane can say.
func (d *detail) outputPanel(width, height int) string {
	if d.taskID == 0 {
		return styleDim.Render("  no task selected")
	}
	if width > 0 {
		d.width = width
	}
	if height <= 1 {
		return d.collapsedOutputLine()
	}
	if d.tab == tabDiff {
		return d.diff.render(d.width, height)
	}
	return d.renderOutputPane(height)
}

// collapsedOutputLine is the one line a collapsed output pane shows: the
// last rendered line of the tail, or why there is none.
func (d *detail) collapsedOutputLine() string {
	if body, ok := d.outputEmptyState(); ok {
		return styleDim.Render("  " + body)
	}
	lines := d.outputLines()
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// outputTitle is the output panel's border title: the §15 tab strip plus
// the follow state, so which tab is live and whether the tail follows stay
// visible even collapsed.
func (d *detail) outputTitle() string {
	strip := tabLabel("output", d.tab == tabOutput) +
		styleDim.Render(" │ ") + tabLabel("diff", d.tab == tabDiff)
	if d.tab == tabDiff {
		if d.diff.truncated {
			return strip + styleDim.Render(" · truncated")
		}
		return strip
	}
	// The level rides in the title rather than only in the footer: `v` is
	// the one key here whose effect can be invisible — pressing it on a run
	// with no reasoning and no unrecognized lines changes nothing on screen,
	// and a reader needs to see that the key did something.
	level := ""
	if d.level != levelNormal {
		level = styleDim.Render(" · " + d.level.String())
	}
	return strip + level + d.followIndicator()
}

// detailHints are the view's own keys, shown beside the task's actions so the
// bar is the one place that answers "what can I do here".
func (d *detail) detailHints() []string {
	hints := []string{styleKey.Render("d") + " diff"}
	if d.tab == tabDiff {
		hints = []string{styleKey.Render("d") + " output"}
	}
	if d.form != nil {
		hints = append(hints, styleAsk.Render("enter answer"))
	}
	if d.target().has(apiclient.ActionRetry) && d.stepEditable() {
		hints = append(hints, styleKey.Render("E")+" edit+retry")
	}
	// `repair` has no row in actionOrder — it is a form, not a key that acts
	// (task 025) — so its hint is the view's, the way `answer`'s is.
	if d.target().has(apiclient.ActionRepair) {
		hints = append(hints, styleKey.Render("R")+" repair")
	}
	return hints
}

// stepEditable reports whether the current step carries text E could edit —
// the same gate the action bar's hint and the palette share.
func (d *detail) stepEditable() bool {
	step, ok := d.task.Step(d.task.CurrentStep)
	if !ok {
		return false
	}
	_, _, editable := step.EditableText()
	return editable
}

func (d *detail) headerLine() string {
	if !d.loaded {
		return styleDim.Render(" loading task…")
	}
	t := d.task
	parts := []string{
		styleTitle.Render(fmt.Sprintf(" #%d %s", t.ID, t.Title)),
		renderDetailState(t.Task),
	}
	if k, n, ok := t.StepDisplay(); ok {
		parts = append(parts, fmt.Sprintf("%d/%d", k, n))
	}
	if loop := t.Loop.Display(); loop != "" {
		parts = append(parts, loop)
	}
	if t.ProjectName != "" {
		parts = append(parts, styleDim.Render(t.ProjectName))
	}
	if t.BranchName != "" {
		parts = append(parts, styleDim.Render(t.BranchName))
	}
	parts = append(parts, formatCost(t.CostUSD))
	return strings.Join(parts, styleDim.Render(" · "))
}

// errorLine surfaces a stale view without tearing it down: a failed refresh
// keeps the last good timeline on screen and says so.
func (d *detail) errorLine() string {
	switch {
	case d.loadErr != nil:
		return styleBad.Render(" ⚠ refresh failed: " + errString(d.loadErr))
	case d.fetchErr != nil:
		return styleBad.Render(" ⚠ transcript: " + errString(d.fetchErr))
	default:
		return ""
	}
}

// renderTimeline draws every attempt grouped under its step, windowed around
// the cursor so a long history still shows the selection.
func (d *detail) renderTimeline(height int) string {
	runs := d.attempts()
	if len(runs) == 0 {
		d.visibleRuns = nil
		if !d.loaded {
			return styleDim.Render("  loading…")
		}
		return styleDim.Render("  queued — no attempts yet")
	}

	// A `parallel` group's sub-steps all carry the group's step index (task
	// 014), and so do a `loop` body's steps across every iteration (task
	// 016), so the header for such an index names the structure step and its
	// members get a tier of their own beneath it. Outside both the shape is
	// unchanged: one header, then its attempts.
	//
	// A loop's iterations are folded shut with the latest open. Ten passes of
	// a four-step body is forty rows, and a reader arriving at a blocked task
	// wants the pass it stopped on, not the nine that worked — the same
	// judgement task 012 made about a twenty-file diff.
	groups := groupedIndexes(runs)
	loops := loopIndexes(runs)
	openIteration := latestIterations(runs)
	lines := make([]string, 0, len(runs)*2)
	ids := make([]int64, 0, len(runs)*2)
	cursorLine := 0
	lastStep := -1
	lastIteration := 0
	lastSub := ""
	for _, r := range runs {
		looped := loops[r.StepIndex]
		grouped := groups[r.StepIndex] && !looped
		if r.StepIndex != lastStep {
			lastStep = r.StepIndex
			lastSub, lastIteration = "", 0
			label := stepLabel(r)
			switch {
			case looped:
				label = d.structureLabel(r.StepIndex, "loop")
			case grouped:
				label = d.structureLabel(r.StepIndex, "parallel")
			}
			header := fmt.Sprintf("  %d %s", r.StepIndex+1, label)
			if from := d.includedFrom(r.StepIndex); from != "" {
				// A spliced step is an ordinary step in every other respect,
				// so the only thing worth saying is where it was written
				// (§7.9). The full chain is in the workflow graph's inspector.
				header += styleDim.Render("  from " + from)
			}
			lines = append(lines, styleStepHeader.Render(header))
			ids = append(ids, 0)
		}
		if looped && r.Iteration != lastIteration {
			lastIteration = r.Iteration
			lastSub = ""
			lines = append(lines, styleDim.Render("    "+iterationHeader(r, openIteration[r.StepIndex])))
			ids = append(ids, 0)
		}
		if looped && r.Iteration != openIteration[r.StepIndex] {
			// Folded: the header above stands for the whole iteration, and
			// its attempts are not rendered. Selecting a run inside a folded
			// iteration cannot happen, because it never entered ids.
			continue
		}
		// A repair is not an attempt of the blocked step and must never read
		// as one (task 025): it gets a tier of its own under the step header,
		// whatever shape that index otherwise has.
		repair := isRepairRun(r)
		if repair && lastSub != r.StepID {
			lastSub = r.StepID
			indent := "    · "
			if looped {
				indent = "      · "
			}
			lines = append(lines, styleDim.Render(indent+"repair (ad-hoc agent)"))
			ids = append(ids, 0)
		}
		if !repair && (grouped || looped) && r.StepID != lastSub {
			lastSub = r.StepID
			indent := "    · "
			if looped {
				indent = "      · "
			}
			lines = append(lines, styleDim.Render(indent+stepLabel(r)))
			ids = append(ids, 0)
		}
		line := d.attemptLine(r, grouped || looped || repair)
		if r.ID == d.selectedRun {
			cursorLine = len(lines)
			line = styleSelected.Render(line)
		}
		lines = append(lines, line)
		ids = append(ids, r.ID)
	}
	// The windowed ids are kept for hit-testing clicks on the same lines.
	start := windowStart(len(lines), cursorLine, height)
	end := len(lines)
	if height > 0 && len(lines) > height {
		end = start + height
	}
	d.visibleRuns = ids[start:end]
	return strings.Join(lines[start:end], "\n")
}

// isRepairRun reports whether a row is an ad-hoc repair rather than an
// attempt of the step at its index (§5.4, task 025).
func isRepairRun(r apiclient.StepRun) bool { return r.StepID == apiclient.RepairStepID }

// groupedIndexes reports which step indexes hold more than one distinct step
// id — which is exactly the `parallel` groups, since every other step type
// owns its index alone.
//
// Derived from the rows rather than read from the snapshot so the timeline
// renders correctly even before workflow_steps arrives, and for a task whose
// snapshot no longer parses.
func groupedIndexes(runs []apiclient.StepRun) map[int]bool {
	seen := map[int]map[string]bool{}
	for _, r := range runs {
		if isRepairRun(r) {
			// A repair sits at the blocked step's index under a reserved id
			// (task 025), so counting it would make every repaired step read
			// as a `parallel` group of itself.
			continue
		}
		if seen[r.StepIndex] == nil {
			seen[r.StepIndex] = map[string]bool{}
		}
		seen[r.StepIndex][r.StepID] = true
	}
	out := make(map[int]bool, len(seen))
	for index, ids := range seen {
		out[index] = len(ids) > 1
	}
	return out
}

// loopIndexes reports which step indexes hold rows from more than one
// iteration, or any row carrying an iteration at all — which is exactly the
// `loop` steps, since every other step type writes iteration 0.
//
// Derived from the rows rather than the snapshot for the reason groupedIndexes
// is: the timeline has to render before workflow_steps arrives, and for a task
// whose snapshot no longer parses.
func loopIndexes(runs []apiclient.StepRun) map[int]bool {
	out := map[int]bool{}
	for _, r := range runs {
		if r.Iteration > 0 {
			out[r.StepIndex] = true
		}
	}
	return out
}

// latestIterations is the highest iteration each loop index has a row for —
// the one iteration that renders open.
func latestIterations(runs []apiclient.StepRun) map[int]int {
	out := map[int]int{}
	for _, r := range runs {
		if r.Iteration > out[r.StepIndex] {
			out[r.StepIndex] = r.Iteration
		}
	}
	return out
}

// iterationHeader is one iteration's tier line, carrying the fold glyph and —
// for a `for_each` loop — the item that pass ran on, which is the whole reason
// a reader wants iteration headers rather than a flat list.
func iterationHeader(r apiclient.StepRun, open int) string {
	glyph := diffFoldClosed
	if r.Iteration == open {
		glyph = diffFoldOpen
	}
	out := fmt.Sprintf("%s iteration %d", glyph, r.Iteration)
	if r.LoopItem != nil && *r.LoopItem != "" {
		out += " · " + *r.LoopItem
	}
	return out
}

// structureLabel names a `parallel` group or a `loop` from the task's
// snapshot, since neither writes a step_runs row to take a name from (task
// 014 decision 17, task 016 decision 7). A snapshot that has not loaded yet
// falls back to the type name, which is still true.
func (d *detail) structureLabel(index int, kind string) string {
	for _, s := range d.task.WorkflowSteps {
		if s.Index == index {
			return fmt.Sprintf("%s (%s)", s.ID, kind)
		}
	}
	return "(" + kind + ")"
}

// includedFrom names the workflow step `index` was spliced in from (§7.9,
// task 019), or "" for a step the task's own workflow wrote.
//
// The *innermost* name, where the snapshot carries the whole chain: at the
// width of a step header, "from go-checks" is the answer to the question the
// reader is asking, and the chain that reached it is the graph inspector's
// job.
func (d *detail) includedFrom(index int) string {
	for _, s := range d.task.WorkflowSteps {
		if s.Index != index {
			continue
		}
		if n := len(s.ResolvedFrom); n > 0 {
			return s.ResolvedFrom[n-1]
		}
		return ""
	}
	return ""
}

// stepStateStopped is the §7.7 state a `condition` step records when its
// guard is false: the run ended here, on purpose and successfully.
const stepStateStopped = "stopped"

func stepLabel(r apiclient.StepRun) string {
	name := r.StepName
	if name == "" {
		name = r.StepID
	}
	if name == "" {
		name = r.StepType
	}
	return name
}

// attemptLine is one attempt: what it did, how long it actually worked, and
// what it cost.
// indented is true for an attempt inside a `parallel` group, which sits one
// tier deeper because its sub-step header is already at the attempt's usual
// indent.
func (d *detail) attemptLine(r apiclient.StepRun, indented bool) string {
	mark := " "
	if r.PromptOverride || r.RunOverride {
		mark = editedBadge
	}
	indent := "    "
	if indented {
		indent = "      "
	}
	fields := []string{fmt.Sprintf("%sa%d %s %-9s", indent, r.Attempt, mark, r.State)}
	if dur, ok := r.Duration(d.now()); ok {
		fields = append(fields, formatElapsed(dur))
	}
	if r.InputWaitMS > 0 {
		// The wait is excluded from the duration (§17), so it is shown rather
		// than silently subtracted.
		fields = append(fields,
			styleDim.Render("+"+formatElapsed(time.Duration(r.InputWaitMS)*time.Millisecond)+" waiting"))
	}
	if tokens := formatTokens(r); tokens != "" {
		fields = append(fields, tokens)
	}
	if r.CostUSD != nil {
		fields = append(fields, formatCost(r.CostUSD))
	}
	if r.FailureReason != nil && *r.FailureReason != "" {
		fields = append(fields, styleBad.Render(*r.FailureReason))
	}
	// A `skipped` row is either a human pressing skip (§6) or a false `if:`
	// guard (§7.7). The state cannot tell them apart, so the reason is shown
	// whenever there is one — a bare "skipped" means the human.
	if r.SkipReason != nil && *r.SkipReason != "" {
		fields = append(fields, styleDim.Render("by "+*r.SkipReason))
	}
	if r.State == stepStateStopped {
		fields = append(fields, styleDim.Render("workflow ended here"))
	}
	if r.Agent != nil && *r.Agent != "" {
		fields = append(fields, styleDim.Render(agentTriple(r)))
	}
	return strings.Join(fields, " ")
}

func formatTokens(r apiclient.StepRun) string {
	if r.InputTokens == nil && r.OutputTokens == nil {
		return ""
	}
	var in, out int64
	if r.InputTokens != nil {
		in = *r.InputTokens
	}
	if r.OutputTokens != nil {
		out = *r.OutputTokens
	}
	return fmt.Sprintf("%s↓/%s↑", compactCount(in), compactCount(out))
}

func compactCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// agentTriple renders the §8.6 resolution that actually ran, which is what a
// reader compares against when an attempt behaves differently from the last.
func agentTriple(r apiclient.StepRun) string {
	parts := []string{*r.Agent}
	if r.Model != nil && *r.Model != "" {
		parts = append(parts, *r.Model)
	}
	if r.Effort != nil && *r.Effort != "" {
		parts = append(parts, *r.Effort)
	}
	return strings.Join(parts, "/")
}

// window returns at most height lines around the focused one.
func window(lines []string, focus, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	start := windowStart(len(lines), focus, height)
	return lines[start : start+height]
}

// windowRange returns at most height lines, keeping the whole of the focused
// [from,to) range on screen — a row that wraps is one thing to read, and half
// of it is not readable. A range taller than the window is anchored at its
// first line: what a wrapped option starts with is what identifies it.
func windowRange(lines []string, from, to, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	start := windowStart(len(lines), from, height)
	if to > start+height {
		start = min(to-height, from)
	}
	start = max(min(start, len(lines)-height), 0)
	return lines[start : start+height]
}

// windowStart is where a height-line window over n lines begins so the
// focused line stays visible.
func windowStart(n, focus, height int) int {
	if height <= 0 || n <= height {
		return 0
	}
	return min(max(focus-height/2, 0), n-height)
}

func tabLabel(name string, active bool) string {
	if active {
		return styleTitle.Render(name)
	}
	return styleDim.Render(name)
}

// followIndicator says whether the tail is live, because dropping follow
// silently while output keeps arriving reads as a stalled run.
func (d *detail) followIndicator() string {
	switch {
	case d.noTranscript || d.displayRun == 0:
		return ""
	case d.following:
		return styleDim.Render(" · ") + styleOK.Render("▼ following")
	case d.newLines > 0:
		return styleDim.Render(" · ") +
			styleWarn.Render(fmt.Sprintf("⏸ paused · %d new", d.newLines))
	default:
		return styleDim.Render(" · ⏸ paused")
	}
}

func (d *detail) renderOutputPane(height int) string {
	if body, ok := d.outputEmptyState(); ok {
		return styleDim.Render("  " + body)
	}
	d.vp.SetWidth(max(d.width, 1))
	d.vp.SetHeight(height)
	if d.outputDirty || d.builtWidth != d.width {
		d.vp.SetContent(strings.Join(d.outputLines(), "\n"))
		d.outputDirty = false
		d.builtWidth = d.width
		if d.following {
			d.vp.GotoBottom()
		}
	}
	return d.vp.View()
}

// outputEmptyState distinguishes the reasons a pane can have no output: they
// are different problems and only one of them is worth waiting on.
func (d *detail) outputEmptyState() (string, bool) {
	switch {
	case d.selectedRun == 0:
		return "no attempt selected", true
	case d.noTranscript:
		return "this step wrote no transcript (a gate, a skipped step, or a condition)", true
	case d.fetching && len(d.records) == 0:
		return "loading transcript…", true
	case d.fetchErr != nil && len(d.records) == 0:
		return "transcript unavailable — it may have been pruned", true
	case len(d.records) == 0:
		return "no output yet", true
	default:
		return "", false
	}
}

// outputLines renders the normalized records into wrapped pane lines.
//
// Two rules shape the result beyond the per-record rendering. Consecutive
// unrecognized lines collapse into a count — a dialect vincent does not model
// must not be able to drown the output a human is reading — and `v` expands
// them rather than leaving the count a dead end. And an assistant message
// that follows anything else gets a blank line before it, which is what
// separates one turn from the next without spending a column on it.
func (d *detail) outputLines() []string {
	width := max(d.width, 1)
	lines := make([]string, 0, len(d.records)+1)
	if d.truncated {
		// Naming the key here is the whole point of T4.11: this line is the
		// one moment a reader is looking straight at the missing output, so
		// it is where the way to the rest of it belongs.
		lines = append(lines, styleDim.Render(gutterNone+"… earlier output truncated — press e for the whole transcript"))
	}
	// sawOutput drives the T4.16 result de-duplication: every dialect's
	// result text repeats assistant messages already on screen — cursor's is
	// the whole turn concatenated — so the final record shows its outcome
	// alone, unless nothing else ever rendered.
	var sawOutput, lastWasOutput bool
	rawRun := 0
	flushRaw := func() {
		if rawRun == 0 {
			return
		}
		lines = append(lines, styleDim.Render(
			fmt.Sprintf("%s… %d unrecognized line(s) (v)", gutterNone, rawRun)))
		rawRun = 0
	}
	for _, rec := range d.records {
		if rec.Type == "agent.raw" {
			if d.level == levelVerbose {
				flushRaw()
				lines = append(lines, wrapLine(paneLine{
					gutter:      gutterNone,
					gutterStyle: styleDim,
					segs:        []segment{{text: rec.Line, style: styleDim}},
				}, width)...)
				lastWasOutput = false
				continue
			}
			rawRun++
			continue
		}
		flushRaw()
		if rec.Type == "agent.thinking" {
			if block := thinkingBlock(rec.Text, d.level, width); len(block) > 0 {
				lines = append(lines, block...)
				lastWasOutput = false
			}
			continue
		}
		pl, ok := d.renderRecord(rec, sawOutput)
		if !ok {
			continue
		}
		if pl.isOutput {
			if !lastWasOutput && len(lines) > 0 {
				lines = append(lines, "")
			}
			sawOutput = true
		}
		lines = append(lines, wrapLine(pl, width)...)
		lastWasOutput = pl.isOutput
	}
	flushRaw()
	return lines
}

// renderRecord maps one normalized record to a pane line. A record with
// nothing a reader wants mid-tail reports ok=false: agent.usage is the whole
// point of that rule, since the timeline row already carries its numbers —
// though levelVerbose does show it, adapter-native payload and all, because
// that level means "show me the machine".
func (d *detail) renderRecord(rec apiclient.TranscriptRecord, sawOutput bool) (paneLine, bool) {
	switch rec.Type {
	case "agent.output":
		return plain(rec.Text, lipgloss.NewStyle(), true), rec.Text != ""
	case "agent.tool_use":
		if len(rec.Tools) == 0 {
			return paneLine{}, false
		}
		return toolUsePane(rec.Tools), true
	case "agent.tool_result":
		if len(rec.Results) == 0 {
			return paneLine{}, false
		}
		// One record can report several outcomes; the first owns the line
		// and the rest are rare enough to share it rather than earn rows.
		return toolResultLine(rec.Results[0]), true
	case "agent.usage":
		if d.level != levelVerbose {
			return paneLine{}, false
		}
		return plain(string(rec.Raw), styleDim, false), len(rec.Raw) > 0
	case "agent.error":
		return marked("✗ ", rec.Message, styleBad), true
	case "agent.result":
		return renderResult(rec, sawOutput), true
	case "command.output", "vincent.output":
		if rec.Stream == "stderr" {
			return plain(rec.Text, styleStderr, false), true
		}
		return plain(rec.Text, lipgloss.NewStyle(), false), true
	case "vincent.command_started":
		return marked("$ ", fieldOf(rec.Raw, "command"), styleDim), true
	case "vincent.input_request":
		return marked("? ", firstNonEmpty(rec.Summary, rec.Kind, "input requested"), styleAsk), true
	case "vincent.input_response":
		return marked("✓ ", "answered", styleAsk), true
	case "vincent.input_timeout", "vincent.input_protocol_error", "vincent.error":
		return marked("✗ ",
			firstNonEmpty(rec.Message, fieldOf(rec.Raw, "error"), rec.Type), styleBad), true
	default:
		if strings.HasPrefix(rec.Type, "vincent.") {
			return marked("· ", strings.TrimPrefix(rec.Type, "vincent."), styleDim), true
		}
		return paneLine{}, false
	}
}

// plain is a record with the blank gutter: assistant prose and command
// output, which sit flush against the pane's edge.
func plain(text string, style lipgloss.Style, isOutput bool) paneLine {
	return paneLine{
		gutter:      gutterNone,
		gutterStyle: style,
		segs:        []segment{{text: text, style: style}},
		isOutput:    isOutput,
	}
}

// marked is a record whose two-column gutter is a glyph in its own style.
func marked(glyph, text string, style lipgloss.Style) paneLine {
	return paneLine{
		gutter:      glyph,
		gutterStyle: style,
		segs:        []segment{{text: text, style: style}},
	}
}

// toolUseLine renders tool invocations as name plus subject. The name is
// what the agent chose to run and stays in the tool color; the subject is
// what it ran it on and is dimmed, so a column of tool calls scans by name
// while still saying what each one touched. A tool whose arguments yielded
// no subject renders exactly as it did before T4.14 — its bare name.
// toolUsePane lays out tool invocations as name plus subject. The name is
// what the agent chose to run and stays in the tool color; the subject is
// what it ran it on and is dimmed, so a column of tool calls scans by name
// while still saying what each one touched. A tool whose arguments yielded no
// subject renders exactly as it did before T4.14 — its bare name.
func toolUsePane(tools []apiclient.TranscriptTool) paneLine {
	segs := make([]segment, 0, len(tools)*3)
	for i, t := range tools {
		if i > 0 {
			segs = append(segs, segment{text: ", ", style: styleDim})
		}
		segs = append(segs, segment{text: t.Name, style: styleTool})
		if t.Summary != "" {
			segs = append(segs, segment{text: " " + t.Summary, style: styleDim})
		}
	}
	return paneLine{
		gutter:      gutterTool,
		gutterStyle: styleTool,
		segs:        segs,
	}
}

// renderResult renders the terminal record. On success it shows the outcome
// and nothing else: every dialect's result text repeats assistant messages
// already on screen — cursor's is the entire turn concatenated — so printing
// it again is the same words twice. The text is kept when nothing else
// rendered, which is what a codex turn with no agent_message looks like, and
// always on error, where it is the error and may be the only content there is.
func renderResult(rec apiclient.TranscriptRecord, sawOutput bool) paneLine {
	if rec.IsError {
		return marked("✗ ", firstNonEmpty(rec.Message, rec.ResultText, "run failed"), styleBad)
	}
	if !sawOutput {
		return marked("✓ ", firstNonEmpty(rec.ResultText, "run finished"), styleOK)
	}
	return marked("✓ ", resultOutcome(rec), styleOK)
}

// resultOutcome is the one-line summary that replaces a repeated result text.
// Tokens are deliberately absent — the attempt's own timeline row already
// carries them, and the point of this line is to stop saying things twice.
// Cost is here because it is reported nowhere else in this view.
func resultOutcome(rec apiclient.TranscriptRecord) string {
	if rec.CostUSD != nil {
		return fmt.Sprintf("done · %s", formatCost(rec.CostUSD))
	}
	return "done"
}

// fieldOf reads one string field out of a record's raw JSON — the annotation
// fields TranscriptRecord does not name individually.
func fieldOf(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// recordFromChunk converts a live-output chunk into the same record shape the
// normalized transcript delivers, so one renderer serves both.
func recordFromChunk(n apiclient.OutputNote) apiclient.TranscriptRecord {
	rec := apiclient.TranscriptRecord{Type: n.Type, Raw: n.Payload}
	if len(n.Payload) > 0 {
		// Field names match by construction: the chunk payloads and the
		// normalized records are the same vocabulary (§13.3).
		_ = json.Unmarshal(n.Payload, &rec)
		rec.Type = n.Type
	}
	return rec
}
