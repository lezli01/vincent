package tui

import (
	"context"
	"fmt"
	"math"
	"regexp"
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
	// shows at most, fewer when the height budget will not hold them (#1086:
	// five was the tail by position, and cut off the line that named a
	// failing test).
	evidenceLines = 12
	// pinnedEvidenceLines is the evidence the card pinned under the other
	// tabs carries: the same lines, shortened.
	pinnedEvidenceLines = 3
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

// statusLabel names the status as what it is: the last thing the step said
// before it stopped, which may be a stage it had already passed (#1086).
func (c failureCard) statusLabel() string {
	if c.aborted {
		return "last status before it stopped"
	}
	return "last status before the failure"
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

// evidenceLine is one line of a failing attempt's output as the card quotes
// it. gap marks the dim `⋯` drawn where selected lines are not adjacent in
// the transcript, so two lines are never read as consecutive when they were
// not.
type evidenceLine struct {
	text   string
	stderr bool
	gap    bool
}

// failurePattern is a line that says what failed in the words of the tool
// that failed: a Go test's `--- FAIL`, a panic, git's `fatal:`, a compiler's
// or a CLI's `error:`. These are what the chats opened on stopped tasks
// quoted first (#1086).
var failurePattern = regexp.MustCompile(`(?i)(^--- fail\b|^panic:|\bfatal:|\berror(\[[^\]]*\])?:)`)

// weakFailurePattern is a bare `FAIL` summary row: worth more than an
// arbitrary line, less than the line that names the failing test.
var weakFailurePattern = regexp.MustCompile(`^(FAIL|FAILED|ERROR)\b`)

// maxContinuation bounds how many more-indented lines after a failure line
// count as part of it — a test's message under its `--- FAIL`, the first
// frames of a trace — so one long trace cannot crowd out a second failure.
const maxContinuation = 4

// failingPhase is the phase of the attempt that failed: "check" when the
// attempt's last command was its check, "run" for the step's own body. The
// transcript says which ran last; without a command record the reason does.
func failingPhase(reason string, records []apiclient.TranscriptRecord) string {
	for i := len(records) - 1; i >= 0; i-- {
		if r := records[i]; r.Type == "vincent.command_started" {
			if ph := firstNonEmpty(r.Phase, fieldOf(r.Raw, "phase")); ph != "" {
				return ph
			}
		}
	}
	if reason == reasonCheckFailed {
		return "check"
	}
	return "run"
}

// failingCommand is the script the failing phase ran, as the shell received
// it — empty for an agent's body, which runs no command.
func failingCommand(phase string, records []apiclient.TranscriptRecord) string {
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Type != "vincent.command_started" {
			continue
		}
		if firstNonEmpty(r.Phase, fieldOf(r.Raw, "phase")) == phase {
			return strings.TrimSpace(fieldOf(r.Raw, "command"))
		}
	}
	return ""
}

// rankedLine is a candidate line with its position and its rank: 2 a
// failure line or what is indented under one, 1 stderr or a bare FAIL row,
// 0 anything else.
type rankedLine struct {
	evidenceLine
	rank int
}

// phaseLines are the lines a failing phase printed, in transcript order. A
// check's are the check's own output — for an agent step, never the agent's
// final message; a body's are everything else the attempt printed, its
// errors included.
func phaseLines(phase string, records []apiclient.TranscriptRecord) []rankedLine {
	var out []rankedLine
	for _, rec := range records {
		text, errRec := "", false
		switch {
		case phase == "check":
			if rec.Phase == "check" && (rec.Type == "command.output" || rec.Type == "vincent.output") {
				text = rec.Text
			}
		case rec.Phase == "check":
		case rec.Type == "command.output" || rec.Type == "agent.output" || rec.Type == "vincent.output":
			text = rec.Text
		case strings.HasSuffix(rec.Type, ".error"):
			text, errRec = rec.Message, true
		}
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimRight(line, " \t\r"); strings.TrimSpace(line) == "" {
				continue
			}
			l := rankedLine{evidenceLine: evidenceLine{text: line, stderr: rec.Stream == "stderr"}}
			switch {
			case errRec || failurePattern.MatchString(strings.TrimSpace(line)):
				l.rank = 2
			case l.stderr || weakFailurePattern.MatchString(strings.TrimSpace(line)):
				l.rank = 1
			}
			out = append(out, l)
		}
	}
	// What is indented under a failure line belongs to it.
	for i := range out {
		if out[i].rank != 2 {
			continue
		}
		indent := leadingSpace(out[i].text)
		for j := i + 1; j < len(out) && j <= i+maxContinuation && leadingSpace(out[j].text) > indent; j++ {
			out[j].rank = 2
		}
	}
	return out
}

func leadingSpace(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// selectEvidence picks at most n rows of a failing attempt's output (#1086):
// failure lines first, earliest first, since the first failure is usually
// the cause and the rest its consequences; then stderr and bare FAIL rows,
// newest first; then the tail by position. The rows come back in transcript
// order with a gap row wherever the picks are not adjacent, and the gaps
// count toward n.
func selectEvidence(phase string, records []apiclient.TranscriptRecord, n int) []evidenceLine {
	lines := phaseLines(phase, records)
	if len(lines) == 0 || n <= 0 {
		return nil
	}
	order := make([]int, 0, len(lines))
	for i, l := range lines {
		if l.rank == 2 {
			order = append(order, i)
		}
	}
	for rank := 1; rank >= 0; rank-- {
		for i := len(lines) - 1; i >= 0; i-- {
			if lines[i].rank == rank {
				order = append(order, i)
			}
		}
	}
	picked := make([]bool, len(lines))
	rows := func() int {
		count, prev := 0, -1
		for i, p := range picked {
			if !p {
				continue
			}
			if prev >= 0 && i != prev+1 {
				count++
			}
			count++
			prev = i
		}
		return count
	}
	kept := 0
	for _, i := range order {
		if kept >= n {
			break
		}
		picked[i] = true
		if rows() > n {
			picked[i] = false
			continue
		}
		kept++
	}
	var out []evidenceLine
	prev := -1
	for i, p := range picked {
		if !p {
			continue
		}
		if prev >= 0 && i != prev+1 {
			out = append(out, evidenceLine{gap: true})
		}
		out = append(out, lines[i].evidenceLine)
		prev = i
	}
	return out
}

const reasonCheckFailed = "check_failed"

// exitSentence says which part of the attempt failed and how it exited:
// "the check exited 1", or "the command printed nothing and exited 1" when
// it left no output to quote (#1086). Empty when no exit code says anything
// — a timeout or a kill leaves none, and the reason already says it.
func exitSentence(run apiclient.StepRun, phase string, printed bool) string {
	code, subject := run.ExitCode, "the step"
	switch {
	case phase == "check":
		code, subject = run.CheckExitCode, "the check"
	case run.StepType == "command":
		subject = "the command"
	case run.StepType == "agent":
		subject = "the agent"
	}
	if code == nil || *code < 0 {
		return ""
	}
	if !printed {
		return fmt.Sprintf("%s printed nothing and exited %d", subject, *code)
	}
	return fmt.Sprintf("%s exited %d", subject, *code)
}

// commandLine is a script's first line, with how many lines follow it.
func commandLine(script string) string {
	first, rest, multi := strings.Cut(script, "\n")
	line := "$ " + strings.TrimSpace(first)
	if multi {
		line += "  (+" + plural(strings.Count(rest, "\n")+1, "line", "lines") + ")"
	}
	return line
}

// cardEvidence is what a failing attempt's transcript says, for both the
// Overview's card and the one pinned under the other tabs.
type cardEvidence struct {
	// loading is a fetch still in flight; ok is evidence to show at all —
	// false sends the card to the daemon's block_detail or the daemon log.
	loading, ok bool
	// command is the failing script's first line as the card draws it, and
	// script the whole of it, which `Q` hands the agent (#1087).
	command, script string
	exit            string
	lines           []evidenceLine
}

// readEvidence reads a failing attempt's records for card c, at most n
// lines of them.
func readEvidence(c failureCard, records []apiclient.TranscriptRecord, n int) cardEvidence {
	phase := failingPhase(c.reason, records)
	out := cardEvidence{lines: selectEvidence(phase, records, n)}
	if script := failingCommand(phase, records); script != "" {
		out.command, out.script = commandLine(script), script
	}
	out.exit = exitSentence(c.run, phase, len(out.lines) > 0)
	out.ok = len(out.lines) > 0 || out.exit != ""
	return out
}

// cardEvidence reads the fetched transcript for card c, at most n lines.
func (t *taskView) cardEvidence(c failureCard, n int) cardEvidence {
	if !c.wantsEvidence() {
		return cardEvidence{}
	}
	ev := t.evidence
	if ev.taskID != t.detail.taskID || ev.runID != c.run.ID || ev.fetching {
		return cardEvidence{loading: true}
	}
	if ev.err != nil {
		return cardEvidence{}
	}
	return readEvidence(c, ev.records, n)
}

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
	// `Q` sits directly before chat (#1087): the same chat, with the
	// question already asked.
	if i := slices.Index(order, apiclient.ActionChat); i >= 0 {
		order = slices.Insert(order, i, actionDiagnose)
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
	if name == actionDiagnose {
		const does = "ask an agent why it stopped, with this card as its first message"
		return overviewActionDef{actionDiagnose, keymap.Diagnose, does}, true
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

// syncFailureEvidence issues the evidence fetch the first time the
// workspace shows a card for a failing attempt — the Overview's, or the one
// pinned under every other tab (#1086) — and again only when that attempt
// changes, a retry that blocks again. It runs after every message, and an
// event that leaves the failing attempt where it was fetches nothing.
func (t *taskView) syncFailureEvidence() tea.Cmd {
	if !t.showsFailureCard() {
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
	// diagnosis is what a chat concluded about this stop, or an earlier one
	// at the same step for the same reason (#1087).
	diagnosis []string
	status    []string
	blame     []string
	actions   []string
}

// essential is the card that must fit whole before anything else is added:
// the headline, the lane it blames and what can be done.
func (p cardParts) essential() int { return 1 + len(p.blame) + len(p.actions) }

// lines assembles the card in reading order from the sections the budget
// kept.
func (p cardParts) lines(meaning, evidence, diagnosis, status bool) []string {
	out := []string{p.headline}
	if meaning {
		out = append(out, p.meaning...)
	}
	if evidence {
		out = append(out, p.evidence...)
	}
	if diagnosis {
		out = append(out, p.diagnosis...)
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
		return t.cardParts(c, actions, width, math.MaxInt).lines(true, true, true, true)
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
		n := left - 3
		// The diagnosis and the status keep their rows when the evidence
		// can spare them and still show a few lines.
		if s := len(p.diagnosis) + len(p.status); n-s >= 3 {
			n -= s
		}
		if n >= 1 {
			p = t.cardParts(c, actions, width, n)
			evidence = len(p.evidence) <= left
		}
	}
	if evidence {
		left -= len(p.evidence)
	}
	diagnosis := len(p.diagnosis) > 0 && len(p.diagnosis) <= left
	if diagnosis {
		left -= len(p.diagnosis)
	}
	status := len(p.status) <= left
	return p.lines(meaning, evidence, diagnosis, status)
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
	if !c.blamed {
		p.diagnosis = t.diagnosisLines(c, width)
	}
	if c.status != "" {
		p.status = []string{"", styleDim.Render(ansi.Truncate("  "+c.statusLabel()+"  “"+c.status+"”", max(width, 1), "…"))}
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
// transcript gives the command that failed, how it exited and up to
// evidenceLines of what it printed (#1086); the daemon's block_detail is the
// whole of what a step-less block left, so it takes every row of the budget
// and says how much it left out when even that is short (task 070 decision
// 2).
func (t *taskView) evidenceBody(c failureCard, width, n int) []string {
	n = max(n, 1)
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
	// The command and the exit sentence come first, each when there is a
	// row for it beside at least one quoted line.
	head := func(ev cardEvidence) []string {
		var out []string
		if ev.command != "" {
			out = append(out, styleDim.Render(ansi.Truncate("    "+ev.command, max(width, 1), "…")))
		}
		if ev.exit != "" {
			out = append(out, ansi.Truncate("    "+styleBad.Render(ev.exit), max(width, 1), "…"))
		}
		return out
	}
	ev := t.cardEvidence(c, evidenceLines)
	switch {
	case ev.loading:
		return []string{styleDim.Render("    loading evidence…")}
	case !ev.ok:
		return fallback()
	}
	top := head(ev)
	for len(top) > 0 && len(top)+min(len(ev.lines), 1) > n {
		top = top[1:] // the command gives way before the exit sentence
	}
	ev = t.cardEvidence(c, min(n-len(top), evidenceLines))
	return append(top, quoteEvidence(ev.lines, "    ", width)...)
}

// quoteEvidence draws evidence lines under indent, stderr behind a red bar
// so it reads apart from stdout without colour's help being needed to read
// the words.
func quoteEvidence(lines []evidenceLine, indent string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		switch {
		case l.gap:
			out = append(out, styleDim.Render(indent+"⋯"))
		case l.stderr:
			out = append(out, ansi.Truncate(indent+styleBad.Render("▍")+l.text, max(width, 1), "…"))
		default:
			out = append(out, ansi.Truncate(indent+styleDim.Render("▏")+l.text, max(width, 1), "…"))
		}
	}
	return out
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
