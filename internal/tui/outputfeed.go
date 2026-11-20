package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The Output tab's feed (task 168.4, decision 4).
//
// The pane used to be one attempt's transcript and nothing else: a reader
// arriving at a running task saw a stream with no step around it, and every
// other attempt was a `←/→` away and out of sight. The feed is the whole
// task, in execution order: every attempt the pane is not showing folds to
// one line said from its step-run row — no transcript is fetched for it —
// and the attempt it is showing opens between a divider that names it and a
// line that says how it ended. Selecting another attempt (the rail, ←/→)
// moves which one is open, so the cost stays the one transcript the pane
// always fetched.

// feedWrap places the displayed attempt's lines inside the task's feed.
// body and anchors are the displayed attempt's own; the feed's lines carry
// zero anchors, which is "belongs to no record" and is never anchored to.
func (d *detail) feedWrap(body []string, anchors []lineAnchor, width int) ([]string, []lineAnchor) {
	runs := d.attempts()
	if len(runs) == 0 {
		return body, anchors
	}
	out := make([]string, 0, len(body)+len(runs)+4)
	outAnchors := make([]lineAnchor, 0, cap(out))
	add := func(lines ...string) {
		for _, l := range lines {
			out = append(out, l)
			outAnchors = append(outAnchors, lineAnchor{})
		}
	}
	shown := false
	for _, r := range runs {
		if r.ID != d.displayRun {
			add(d.feedFold(r, width))
			continue
		}
		shown = true
		add(d.feedDivider(r, width))
		out = append(out, body...)
		outAnchors = append(outAnchors, anchors...)
		if end := d.feedEnd(r); end != "" {
			add(end)
		}
	}
	if !shown {
		out = append(out, body...)
		outAnchors = append(outAnchors, anchors...)
	}
	return out, outAnchors
}

// feedDivider opens the displayed attempt: its step, what ran it, which
// attempt, and when it started.
func (d *detail) feedDivider(r apiclient.StepRun, width int) string {
	meta := []string{}
	switch {
	case r.Agent != nil && *r.Agent != "":
		meta = append(meta, agentTriple(r))
	case r.StepType != "":
		meta = append(meta, r.StepType)
	}
	attempt := fmt.Sprintf("attempt %d", r.Attempt)
	if r.Iteration > 0 {
		attempt = fmt.Sprintf("%s %d · %s", d.tierNoun(r.StepIndex), r.Iteration, attempt)
	}
	meta = append(meta, attempt)
	left := styleDim.Render("── ") + styleTitle.Render(feedLabel(r)) + styleDim.Render("  ·  "+strings.Join(meta, "  ·  ")+" ")
	right := ""
	if !r.StartedAt.IsZero() {
		right = styleDim.Render(" " + r.StartedAt.Local().Format("15:04:05"))
	}
	fill := max(width-ansi.StringWidth(left)-ansi.StringWidth(right), 1)
	return ansi.Truncate(left+styleDim.Render(strings.Repeat("─", fill))+right, max(width, 1), "…")
}

// feedLabel names an attempt's step: a loop's or a group's member by its own
// id, every other step by its name.
func feedLabel(r apiclient.StepRun) string {
	if r.Iteration > 0 || isRepairRun(r) {
		return subStepLabel(r)
	}
	return stepLabel(r)
}

// feedFold is an attempt the pane is not showing, as one line: its state,
// step, what ran it, how long and what it cost, and the start of what it
// said. ←/→ on Output, or a click on its rail row, opens it.
func (d *detail) feedFold(r apiclient.StepRun, width int) string {
	style, ok := stepStateStyle(r.State)
	if !ok {
		style = styleDim
	}
	glyph := attemptStateGlyph(r.State)
	if r.State == "failed" || r.State == "rejected" {
		glyph = ic("✗")
	}
	parts := []string{}
	if r.Attempt > 1 {
		parts = append(parts, fmt.Sprintf("attempt %d", r.Attempt))
	}
	if r.Agent != nil && *r.Agent != "" {
		parts = append(parts, *r.Agent)
	}
	if dur, ok := r.Duration(d.now()); ok {
		parts = append(parts, formatElapsed(dur))
	}
	if r.CostUSD != nil {
		parts = append(parts, formatCost(r.CostUSD))
	}
	if r.FailureReason != nil && *r.FailureReason != "" {
		parts = append(parts, reasonTitle(*r.FailureReason))
	}
	// A fold is a closed divider: the same rule as the open attempt's, drawn
	// dashed, so it reads as a step and never as one of the agent's `▸` tool
	// calls.
	line := styleDim.Render("┄┄ ") + style.Render(glyph+" "+feedLabel(r)) + styleDim.Render("  ·  "+strings.Join(parts, "  ·  "))
	if summary := firstLine(r.ResultSummary); summary != "" {
		line += styleDim.Render("  ·  “" + summary + "”")
	}
	line = ansi.Truncate(line, max(width-2, 1), "…")
	if fill := width - ansi.StringWidth(line) - 1; fill > 0 {
		line += " " + styleDim.Render(strings.Repeat("┄", fill))
	}
	return line
}

// feedEnd says how the displayed attempt ended, or "" while it runs: the
// exit code a command step left, or the state an agent step finished in.
func (d *detail) feedEnd(r apiclient.StepRun) string {
	if r.FinishedAt == nil || r.Live() {
		return ""
	}
	after := ""
	if dur, ok := r.Duration(d.now()); ok {
		after = " after " + formatElapsed(dur)
	}
	failed := r.State == "failed" || r.State == "rejected"
	if r.ExitCode != nil {
		text := fmt.Sprintf("exited %d%s", *r.ExitCode, after)
		if failed || *r.ExitCode != 0 {
			return styleBad.Bold(true).Render(ic("✗") + " " + text)
		}
		return styleOKDim.Render(ic("✓") + " " + text)
	}
	text := stepStateLabel(r.State) + after
	if r.FailureReason != nil && *r.FailureReason != "" {
		text += " · " + reasonTitle(*r.FailureReason)
	}
	if failed {
		return styleBad.Bold(true).Render(ic("✗") + " " + text)
	}
	style, ok := stepStateStyle(r.State)
	if !ok {
		style = styleDim
	}
	return style.Render(attemptStateGlyph(r.State) + " " + text)
}

// stepStateLabel is a step-run state as the feed's last line says it.
func stepStateLabel(state string) string {
	switch state {
	case stepStateStopped:
		return "the workflow ended here"
	default:
		return state
	}
}

// firstLine is a text's first non-empty line, trimmed.
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// feedStatus is the line pinned under the Output pane: on a live attempt what
// it is doing now and how long it has been at it, with the follow state; on a
// finished one, which attempt is open and how to move.
func (d *detail) feedStatus(width int) string {
	runs := d.attempts()
	i := d.runIndex(d.displayRun)
	if i < 0 {
		return ""
	}
	run := runs[i]
	right := styleDim.Render(fmt.Sprintf("attempt %d/%d  ← →", i+1, len(runs)))
	var left string
	if run.Live() {
		left = toneCyan.Render(ProgressLabel(d.frame, d.now().Sub(run.StartedAt)))
		// What it last said is the now-line's, under the app header, while
		// the task runs (task 129.9); this line says how long and how much.
		if tokens := formatTokens(run); tokens != "" {
			left += styleDim.Render("  ·  ") + tokens
		}
		if run.CostUSD != nil {
			left += styleDim.Render("  ·  " + formatCost(run.CostUSD))
		}
		right = d.followState() + "  " + right
	} else {
		left = styleDim.Render(feedLabel(run) + " · " + run.State)
	}
	if l := d.level.get(); l != levelNormal {
		left += styleDim.Render("  ·  " + l.String())
	}
	if d.raw.get() {
		left += styleDim.Render("  ·  raw")
	}
	room := max(width-ansi.StringWidth(right)-2, 1)
	return padBetweenCells(ansi.Truncate(left, room, "…"), right, max(width, 1))
}

// followState is the pinned line's follow clause.
func (d *detail) followState() string {
	switch {
	case d.following:
		return styleOK.Render("▼ following")
	case d.newLines > 0:
		return styleWarn.Render(fmt.Sprintf("▲ %d new below · f", d.newLines))
	default:
		return styleDim.Render("⏸ paused · f")
	}
}
