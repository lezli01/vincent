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
			return d.attemptLine(run)
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
	return strip + d.followIndicator()
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
		renderState(t.State),
	}
	if k, n, ok := t.StepDisplay(); ok {
		parts = append(parts, fmt.Sprintf("%d/%d", k, n))
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

	lines := make([]string, 0, len(runs)*2)
	ids := make([]int64, 0, len(runs)*2)
	cursorLine := 0
	lastStep := -1
	for _, r := range runs {
		if r.StepIndex != lastStep {
			lastStep = r.StepIndex
			lines = append(lines, styleStepHeader.Render(
				fmt.Sprintf("  %d %s", r.StepIndex+1, stepLabel(r))))
			ids = append(ids, 0)
		}
		line := d.attemptLine(r)
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
func (d *detail) attemptLine(r apiclient.StepRun) string {
	mark := " "
	if r.PromptOverride || r.RunOverride {
		mark = editedBadge
	}
	fields := []string{fmt.Sprintf("    a%d %s %-9s", r.Attempt, mark, r.State)}
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
		return "this step wrote no transcript (a gate or a skipped step)", true
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

// outputLines renders the normalized records. Consecutive unparsed lines
// collapse into a count: a dialect vincent does not model must not be able to
// drown the output a human is reading.
func (d *detail) outputLines() []string {
	lines := make([]string, 0, len(d.records)+1)
	if d.truncated {
		lines = append(lines, styleDim.Render("  … earlier output truncated"))
	}
	rawRun := 0
	flushRaw := func() {
		if rawRun > 0 {
			lines = append(lines, styleDim.Render(
				fmt.Sprintf("  … %d unparsed line(s)", rawRun)))
			rawRun = 0
		}
	}
	for _, rec := range d.records {
		if rec.Type == "agent.raw" {
			rawRun++
			continue
		}
		flushRaw()
		if line, ok := renderRecord(rec); ok {
			lines = append(lines, line)
		}
	}
	flushRaw()
	return lines
}

// renderRecord maps one normalized record to a display line. A record with
// nothing a reader wants mid-tail reports ok=false: agent.usage is the whole
// point of that rule, since the timeline row already carries its numbers.
func renderRecord(rec apiclient.TranscriptRecord) (string, bool) {
	switch rec.Type {
	case "agent.output":
		return rec.Text, rec.Text != ""
	case "agent.tool_use":
		if len(rec.Tools) == 0 {
			return "", false
		}
		return styleTool.Render("▸ " + strings.Join(rec.Tools, ", ")), true
	case "agent.usage":
		return "", false
	case "agent.error":
		return styleBad.Render("✗ " + rec.Message), true
	case "agent.result":
		return renderResult(rec), true
	case "command.output", "vincent.output":
		if rec.Stream == "stderr" {
			return styleStderr.Render(rec.Text), true
		}
		return rec.Text, true
	case "vincent.command_started":
		return styleDim.Render("$ " + fieldOf(rec.Raw, "command")), true
	case "vincent.input_request":
		return styleAsk.Render("? " + firstNonEmpty(rec.Summary, rec.Kind, "input requested")), true
	case "vincent.input_response":
		return styleAsk.Render("✓ answered"), true
	case "vincent.input_timeout", "vincent.input_protocol_error", "vincent.error":
		return styleBad.Render("✗ " + firstNonEmpty(rec.Message, fieldOf(rec.Raw, "error"), rec.Type)), true
	default:
		if strings.HasPrefix(rec.Type, "vincent.") {
			return styleDim.Render("· " + strings.TrimPrefix(rec.Type, "vincent.")), true
		}
		return "", false
	}
}

func renderResult(rec apiclient.TranscriptRecord) string {
	if rec.IsError {
		return styleBad.Render("✗ " + firstNonEmpty(rec.Message, rec.ResultText, "run failed"))
	}
	return styleOK.Render("✓ " + firstNonEmpty(rec.ResultText, "run finished"))
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
