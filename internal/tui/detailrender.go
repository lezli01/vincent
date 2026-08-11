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
		lines = append(lines, styleDim.Render(gutterNone+"… earlier output truncated"))
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
