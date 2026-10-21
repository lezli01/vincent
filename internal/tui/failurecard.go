package tui

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/reasons"
)

// The failure card (task 129.13) is the Overview's attention frame for a
// `blocked` or `aborted` task: what failed, why, the evidence, and what can
// be done about it, on one screen. `awaiting_input` and `awaiting_gate` keep
// the plain attention frame — a question or a gate is not a failure.
//
// Everything but the evidence is derived from the snapshot the detail
// sub-model already holds. The evidence is one transcript fetch per failing
// attempt, never refetched on an event (T3.2, task 129 decision 7).

const (
	// evidenceLines is how many lines of a transcript's evidence the card
	// shows at most, fewer when the height budget will not hold them.
	evidenceLines = 5
	// actionEditRetry is the card's name for `E`: not a §6 action of its own
	// but retry with an edited step, so it sits directly after retry.
	actionEditRetry = "edit_retry"
)

// failureCard is the pure derivation the card renders from.
type failureCard struct {
	aborted bool
	// run is the attempt the card names, and the one the `3`/`6` links land
	// on (overviewAnchor asks cardRun for it, so the two cannot drift).
	run    apiclient.StepRun
	hasRun bool
	// reason is the §18 code; stepless says no attempt row carries it — a
	// worktree, container, snapshot, condition or cost-cap block — so the
	// evidence is the daemon's block_detail instead of a transcript.
	reason   string
	stepless bool
	detail   string
	// stepName and stepNum name the step a step-less block sits at.
	stepName string
	stepNum  int
	// status is what the attempt last said about itself: neutral, quoted,
	// and never the reason (§5.4, task 036 decision 6).
	status string
	blame  laneBlame
	blamed bool
}

// cardRun is the attempt a stopped task is about: the newest attempt at the
// current step that did not succeed — the rule laneBlame's newest fan_out
// follows — and the newest attempt of all when there is none.
func cardRun(task apiclient.TaskDetail) (apiclient.StepRun, bool) {
	var failing, newest apiclient.StepRun
	for _, r := range task.Steps {
		if r.ID > newest.ID {
			newest = r
		}
		if r.StepIndex == task.CurrentStep && r.State != stepSucceeded && r.ID > failing.ID {
			failing = r
		}
	}
	if failing.ID != 0 {
		return failing, true
	}
	return newest, newest.ID != 0
}

const stepSucceeded = "succeeded"

// deriveFailureCard reads the card off a task and the lane blame the detail
// sub-model computed for it.
func deriveFailureCard(task apiclient.TaskDetail, blame laneBlame, blamed bool) failureCard {
	c := failureCard{aborted: task.State == stateAborted, blame: blame, blamed: blamed}
	c.run, c.hasRun = cardRun(task)
	if task.BlockReason != nil {
		c.reason = *task.BlockReason
	}
	runReason := ""
	if c.hasRun && c.run.FailureReason != nil {
		runReason = *c.run.FailureReason
	}
	if c.reason == "" {
		c.reason = runReason
	}
	c.stepless = c.reason != "" && runReason != c.reason
	if task.BlockDetail != nil {
		c.detail = strings.TrimSpace(*task.BlockDetail)
	}
	if k, _, ok := task.StepDisplay(); ok {
		c.stepNum, c.stepName = k, task.StepName
	}
	if c.hasRun {
		c.status = statusOf(c.run)
	}
	if c.status == "" && task.StatusMessage != nil {
		c.status = strings.TrimSpace(*task.StatusMessage)
	}
	return c
}

// glyph carries the card's kind without colour: ✗ a failure, ⚠ a fan-out a
// lane stopped, ■ a task that was stopped.
func (c failureCard) glyph() string {
	switch {
	case c.aborted:
		return "■"
	case c.blamed:
		return "⚠"
	default:
		return "✗"
	}
}

// headline is the card's first line without its styling: the step, the
// loop iteration or lane, the attempt and the reason's title, with the raw
// code returned apart so it can be dimmed beside it.
func (c failureCard) headline() (line, code string) {
	head, name, tail, code := c.headlineParts()
	return head + name + tail, code
}

// headlineParts splits the headline for truncateKeep: the step name is the
// long part, and the attempt and the reason after it are what the card is
// about, so they survive a narrow card and the name gives way (#800 W4).
func (c failureCard) headlineParts() (head, name, tail, code string) {
	var parts []string
	head = c.glyph() + " "
	switch {
	case c.hasRun && !c.stepless:
		head += fmt.Sprintf("Step %d ", c.run.StepIndex+1)
		name = stepLabel(c.run)
		if c.run.Iteration > 0 {
			it := fmt.Sprintf("iteration %d", c.run.Iteration)
			if c.run.LoopTotal > 0 {
				it += fmt.Sprintf(" of %d", c.run.LoopTotal)
			}
			parts = append(parts, it)
			if c.run.LoopItem != nil {
				parts = append(parts, "item "+strconv.Quote(*c.run.LoopItem))
			}
		}
		if c.blamed && c.blame.laneID != "" {
			parts = append(parts, "lane "+strconv.Quote(c.blame.laneID))
		}
		// The snapshot carries no retry budget, so "of n" is never claimed.
		parts = append(parts, fmt.Sprintf("attempt %d", c.run.Attempt))
	case c.stepNum > 0 && c.stepName != "":
		head += fmt.Sprintf("Step %d ", c.stepNum)
		name = c.stepName
	case c.stepNum > 0:
		parts = append(parts, fmt.Sprintf("Step %d", c.stepNum))
	}
	title := "stopped"
	if c.reason != "" {
		title = reasons.Explain(c.reason).Title
	}
	parts = append(parts, title)
	if c.reason != "" && c.reason != title {
		code = c.reason
	}
	tail = strings.Join(parts, " · ")
	if name != "" {
		tail = " · " + tail
	}
	return head, name, tail, code
}

// wantsEvidence reports whether the card fetches a transcript: a blocked
// task whose reason an attempt carries and that attempt kept a transcript.
func (c failureCard) wantsEvidence() bool {
	return !c.aborted && c.hasRun && !c.stepless && c.run.TranscriptPath != nil
}

// selectEvidence picks the last n meaningful lines of a failing attempt's
// transcript. A `check_failed` is the check's own output — for an agent
// step, never the agent's final message; any other failure is the
// attempt's output and errors.
func selectEvidence(reason string, records []apiclient.TranscriptRecord, n int) []string {
	var out []string
	for _, rec := range records {
		text := ""
		switch {
		case reason == reasonCheckFailed:
			if rec.Phase == "check" {
				text = rec.Text
			}
		case rec.Type == "command.output" || rec.Type == "agent.output" || rec.Type == "vincent.output":
			text = rec.Text
		case strings.HasSuffix(rec.Type, ".error"):
			text = rec.Message
		}
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimRight(line, " \t\r"); strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

const reasonCheckFailed = "check_failed"

// cardAction is one action on the card: the wire action, its key and what
// it does.
type cardAction struct {
	action, key, does string
}

// cardActions orders the offered actions: the reason's catalogue actions in
// the catalogue's order (task 025 decision 8), `E` directly after retry,
// then every other offered action in the action bar's order. Nothing the
// daemon offers is dropped; an action unbound by tui.keys is, because it has
// nothing to press.
func cardActions(reason string, offered []string) []cardAction {
	var order []string
	add := func(a string) {
		if slices.Contains(offered, a) && !slices.Contains(order, a) {
			order = append(order, a)
		}
	}
	for _, a := range reasons.Explain(reason).Actions {
		add(a)
	}
	for _, a := range overviewActions {
		add(a.action)
	}
	if i := slices.Index(order, apiclient.ActionRetry); i >= 0 {
		order = slices.Insert(order, i+1, actionEditRetry)
	}
	out := make([]cardAction, 0, len(order))
	for _, name := range order {
		a, ok := overviewAction(name)
		if !ok {
			continue
		}
		key := "enter"
		if a.op != "" {
			key = opKey(a.op)
		}
		if key == "" {
			continue // unbound by tui.keys: nothing to press
		}
		out = append(out, cardAction{action: name, key: key, does: a.does})
	}
	return out
}

func overviewAction(name string) (overviewActionDef, bool) {
	if name == actionEditRetry {
		return overviewActionDef{actionEditRetry, keymap.EditRetry, "edit the step's prompt or command, then run it again"}, true
	}
	for _, a := range overviewActions {
		if a.action == name {
			return a, true
		}
	}
	return overviewActionDef{}, false
}

// failureEvidence is the card's one transcript fetch, keyed by the attempt
// it is about.
type failureEvidence struct {
	taskID, runID int64
	fetching      bool
	records       []apiclient.TranscriptRecord
	err           error
}

// failureEvidenceMsg carries the fetch's result.
type failureEvidenceMsg struct {
	taskID, runID int64
	records       []apiclient.TranscriptRecord
	err           error
}

func (t *taskView) failureCard() failureCard {
	blame, blamed := t.detail.laneBlame()
	return deriveFailureCard(t.detail.task, blame, blamed)
}

// showsFailureCard is whether the Overview draws the card rather than the
// plain attention frame.
func (t *taskView) showsFailureCard() bool {
	s := t.detail.task.State
	return t.detail.loaded && (s == stateBlocked || s == stateAborted)
}

// syncFailureEvidence issues the evidence fetch the first time the Overview
// shows a card for a failing attempt, and again only when that attempt
// changes — a retry that blocks again. It runs after every message, and an
// event that leaves the failing attempt where it was fetches nothing.
func (t *taskView) syncFailureEvidence() tea.Cmd {
	if t.tab != taskTabOverview || !t.showsFailureCard() {
		return nil
	}
	c := t.failureCard()
	if !c.wantsEvidence() {
		return nil
	}
	taskID, runID := t.detail.taskID, c.run.ID
	if t.evidence.taskID == taskID && t.evidence.runID == runID {
		return nil
	}
	client := t.detail.client
	t.evidence = failureEvidence{taskID: taskID, runID: runID, fetching: client != nil}
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		recs, _, err := client.Transcript(ctx, taskID, runID,
			apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes})
		return failureEvidenceMsg{taskID: taskID, runID: runID, records: recs, err: err}
	}
}

func (t *taskView) applyFailureEvidence(msg failureEvidenceMsg) {
	if msg.taskID != t.evidence.taskID || msg.runID != t.evidence.runID {
		return // a fetch for an attempt the card has moved off
	}
	t.evidence.fetching = false
	t.evidence.records, t.evidence.err = msg.records, msg.err
}

// daemonLogLine is the card's honest "no evidence here": the daemon's log is
// where a failure without a transcript or a detail left its trace.
func daemonLogLine() string {
	if key := opKey(keymap.Palette); key != "" {
		return "details are in the daemon log (" + key + " daemon)"
	}
	return "details are in the daemon log"
}

// cardParts are the full card's sections, each a run of lines that is kept
// or dropped whole, never cut in half.
type cardParts struct {
	headline string
	meaning  []string
	evidence []string
	status   []string
	blame    []string
	actions  []string
}

// essential is the card that must fit whole before anything else is added:
// the headline, the lane it blames and what can be done.
func (p cardParts) essential() int { return 1 + len(p.blame) + len(p.actions) }

// lines assembles the card in reading order from the sections the budget
// kept.
func (p cardParts) lines(meaning, evidence, status bool) []string {
	out := []string{p.headline}
	if meaning {
		out = append(out, p.meaning...)
	}
	if evidence {
		out = append(out, p.evidence...)
	}
	if status {
		out = append(out, p.status...)
	}
	out = append(append(out, p.blame...), p.actions...)
	return out
}

// collapsedCard is the card at its smallest: its first line and its
// actions' keys on one line. The headline is fitted to width like the full
// card's, so the step name gives way before the attempt and the reason
// (review F8) — a short screen is where the card collapses.
func (t *taskView) collapsedCard(width int) []string {
	c := t.failureCard()
	actions := cardActions(c.reason, t.detail.task.AvailableActions)
	return []string{c.renderHeadline(width), c.renderActionKeys(actions)}
}

// failureCardLines renders the full card within the given height budget,
// spending rows in priority order (#799 W1): the headline and the actions
// are the card's floor — the caller collapses it when even they will not
// fit — then the reason's meaning, then the evidence, shrunk to what is left
// before it is dropped, then the status. budget < 0 means unbounded.
func (t *taskView) failureCardLines(width, budget int) []string {
	c := t.failureCard()
	actions := cardActions(c.reason, t.detail.task.AvailableActions)
	if budget < 0 {
		return t.cardParts(c, actions, width, math.MaxInt).lines(true, true, true)
	}
	p := t.cardParts(c, actions, width, 1)
	left := budget - p.essential()
	meaning := len(p.meaning) <= left
	if meaning {
		left -= len(p.meaning)
	}
	// The evidence section's own three rows (blank, heading, blank) and at
	// least one line of it, or none of it.
	evidence := false
	if len(p.evidence) > 0 {
		if n := left - 3; n >= 1 {
			p = t.cardParts(c, actions, width, n)
			evidence = len(p.evidence) <= left
		}
	}
	if evidence {
		left -= len(p.evidence)
	}
	status := len(p.status) <= left
	return p.lines(meaning, evidence, status)
}

// minCardRows is how many rows the full card needs at its floor.
func (t *taskView) minCardRows(width int) int {
	c := t.failureCard()
	return t.cardParts(c, cardActions(c.reason, t.detail.task.AvailableActions), width, 1).essential()
}

// renderHeadline draws the headline in width cells, the step name giving
// way first; a width of zero or less draws it whole.
func (c failureCard) renderHeadline(width int) string {
	head, name, tail, code := c.headlineParts()
	style := styleBad
	switch {
	case c.aborted:
		style = styleDim
	case c.blamed:
		style = styleWarn
	}
	dim := ""
	if code != "" {
		dim = "  " + styleDim.Render(code)
	}
	if whole := "  " + style.Render(head+name+tail) + dim; width <= 0 || cols(whole) <= width {
		return whole
	}
	return truncateKeep("  "+style.Render(head), style.Render(name), style.Render(tail)+dim, width)
}

func (c failureCard) renderActionKeys(actions []cardAction) string {
	if len(actions) == 0 {
		return styleDim.Render("  no action available")
	}
	parts := make([]string, len(actions))
	for i, a := range actions {
		parts[i] = styleKey.Render(a.key) + " " + strings.ReplaceAll(a.action, "_", " ")
	}
	return "  " + strings.Join(parts, styleDim.Render(" · "))
}

// cardParts renders the card's sections with at most n lines of evidence.
func (t *taskView) cardParts(c failureCard, actions []cardAction, width, n int) cardParts {
	p := cardParts{headline: c.renderHeadline(width)}
	if c.reason != "" {
		if m := reasons.Explain(c.reason).Meaning; m != "" {
			p.meaning = wrapText(m, width, hanging("    "))
		}
	}
	// A fan-out's evidence is the lane it blames, drawn below.
	if !c.aborted && !c.blamed {
		p.evidence = append([]string{"", section("Evidence"), ""}, t.evidenceBody(c, width, n)...)
	}
	if c.status != "" {
		p.status = []string{"", styleDim.Render(ansi.Truncate("  status  “"+c.status+"”", max(width, 1), "…"))}
	}
	if c.blamed {
		p.blame = append([]string{""}, c.laneBlameLines(width)...)
	}
	p.actions = []string{"", section("What you can do"), ""}
	if len(actions) == 0 {
		p.actions = append(p.actions, styleDim.Render("  no action available"))
	}
	for _, a := range actions {
		line := "  " + styleKey.Render(fmt.Sprintf("%-6s", a.key)) + " " +
			strings.ReplaceAll(a.action, "_", " ") + styleDim.Render(" — "+a.does)
		p.actions = append(p.actions, ansi.Truncate(line, max(width, 1), "…"))
	}
	return p
}

// evidenceBody is the evidence section's lines, at most n of them. A
// transcript gives its last evidenceLines at most; the daemon's block_detail
// is the whole of what a step-less block left, so it takes every row of the
// budget and says how much it left out when even that is short (task 070
// decision 2).
func (t *taskView) evidenceBody(c failureCard, width, n int) []string {
	n = max(n, 1)
	quote := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = ansi.Truncate("    "+l, max(width, 1), "…")
		}
		return out
	}
	fallback := func() []string {
		if c.detail != "" {
			lines := wrapText(c.detail, width, hanging("    "))
			if len(lines) <= n {
				return lines
			}
			kept := lines[:max(n-1, 0)]
			return append(kept, styleDim.Render("    … "+
				plural(len(lines)-len(kept), "more line", "more lines")))
		}
		return []string{styleDim.Render("    " + daemonLogLine())}
	}
	if !c.wantsEvidence() {
		return fallback()
	}
	ev := t.evidence
	if ev.taskID != t.detail.taskID || ev.runID != c.run.ID || ev.fetching {
		return []string{styleDim.Render("    loading evidence…")}
	}
	if ev.err != nil {
		return fallback()
	}
	lines := selectEvidence(c.reason, ev.records, min(n, evidenceLines))
	if len(lines) == 0 {
		return fallback()
	}
	return quote(lines)
}

// laneBlameLines are the card's copy of the fan-out attribution: which lane,
// which task, the engine's sentence, the lane's own state, and `l`.
func (c failureCard) laneBlameLines(width int) []string {
	b := c.blame
	out := []string{ansi.Truncate(b.headLine(styleWarn), max(width, 1), "…")}
	for _, line := range b.messageLines() {
		out = append(out, ansi.Truncate(styleDim.Render("    "+line), max(width, 1), "…"))
	}
	tail := b.laneFactLine(styleWarn)
	if b.taskID != 0 {
		if key := opKey(keymap.Lane); key != "" {
			hint := key + " open the lane"
			if tail != "" {
				hint = "   " + hint
			}
			tail += styleWarn.Render(hint)
		}
	}
	if tail != "" {
		out = append(out, ansi.Truncate(styleWarn.Render("    ")+tail, max(width, 1), "…"))
	}
	return out
}
