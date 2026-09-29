package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The indicators here — a fan-out parent's lane breakdown, a loop's
// iteration strip and a task's step pips (task 129.10) — share the one glyph
// vocabulary attemptStateGlyph already spoke on the Steps tab, so a `×` means
// the same thing on the board, in the workspace header and on an attempt
// line (task 097 decisions 2 and 3): `●` running, `✓` done, `×` blocked or
// failed, `!` waiting on a human, `○` not started, `■` stopped, `–` skipped.
// Each glyph carries its meaning on its own and colour only reinforces it, so
// every indicator still reads under NO_COLOR (§15 Colour).
//
// They live in one file so the board and the workspace cannot drift into two
// dialects of it.

// taskStateGlyph is a task state spoken in the step-state glyph vocabulary:
// the glyph a lane's clause leads with and the one a board row's current step
// pip takes. A state the vocabulary has no glyph for — paused — returns "",
// and its caller spells the state out instead: `⏸` would have been a new
// meaning, and `○` would call a paused lane one that never started.
func taskStateGlyph(state string) string {
	switch state {
	case stateBlocked:
		return "×"
	case stateAwaitingGate, stateAwaitingInput:
		return attentionBadge
	case stateRunning, stateAwaitingChildren:
		return "●"
	case stateDone:
		return "✓"
	case stateQueued:
		return "○"
	case stateAborted:
		return "■"
	default:
		return ""
	}
}

// breakdownClause is one term of a fan-out parent's lane breakdown: the
// glyph and count as they read, and the task state whose colour they take.
type breakdownClause struct {
	text  string
	state string
}

// breakdownOrder is the clause order, by what a reader has to do about it:
// blocked lanes first, then lanes at a gate, then the working and the
// finished. The board cuts a cell that outgrows its three lines from the
// tail (wrapCellLines), so this order is also the order clauses survive in —
// blocked is the last to go, at any width.
var breakdownOrder = []string{
	stateBlocked, stateAwaitingGate, stateAwaitingInput,
	stateRunning, stateAwaitingChildren, stateDone,
}

// childrenBreakdown renders a rollup's by_state as clauses — `×1 !1 ●1 ✓2` —
// dropping every state with no lanes in it. States the order does not name
// (queued, paused, aborted, or one a newer daemon invented) follow in name
// order, so a lane is never missing from the count. Lanes waiting on a human
// share one `!` clause whichever kind of wait it is, as the board's own
// attention badge does, and a lane that is itself a fan-out parent counts as
// running. Nil for a nil or empty rollup, which leaves the caller its bare
// state.
func childrenBreakdown(r *apiclient.ChildrenRollup) []breakdownClause {
	if r == nil || r.Total == 0 {
		return nil
	}
	counts := make(map[string]int, len(r.ByState))
	for state, n := range r.ByState {
		counts[state] = n
	}
	// by_state and the id lists come from the same walk (store.ChildrenOf);
	// the larger of the two is kept so a daemon serving only one still
	// reports what a human has to act on.
	counts[stateBlocked] = max(counts[stateBlocked], len(r.Blocked))
	counts[stateAwaitingGate] = max(counts[stateAwaitingGate], len(r.AwaitingGate))

	merged := map[string]int{}
	order := make([]string, 0, len(counts))
	add := func(state string, n int) {
		if n <= 0 {
			return
		}
		key := taskStateGlyph(state)
		if key == "" {
			key = state
		}
		if _, seen := merged[key]; !seen {
			order = append(order, state)
		}
		merged[key] += n
	}
	for _, state := range breakdownOrder {
		add(state, counts[state])
		delete(counts, state)
	}
	rest := make([]string, 0, len(counts))
	for state := range counts {
		rest = append(rest, state)
	}
	sort.Strings(rest)
	for _, state := range rest {
		add(state, counts[state])
	}

	out := make([]breakdownClause, 0, len(order))
	for _, state := range order {
		glyph := taskStateGlyph(state)
		if glyph == "" {
			out = append(out, breakdownClause{text: fmt.Sprintf("%d %s", merged[state], state), state: state})
			continue
		}
		out = append(out, breakdownClause{text: glyph + strconv.Itoa(merged[glyph]), state: state})
	}
	return out
}

// breakdownText is the clauses as plain text, for a cell that wraps before it
// styles (the board, task 050).
func breakdownText(clauses []breakdownClause) string {
	parts := make([]string, len(clauses))
	for i, c := range clauses {
		parts[i] = c.text
	}
	return strings.Join(parts, " ")
}

// renderBreakdown is the clauses each in its lane state's colour, for a
// surface that styles whole fields (the Steps tab's fan-out row).
func renderBreakdown(clauses []breakdownClause) string {
	parts := make([]string, len(clauses))
	for i, c := range clauses {
		parts[i] = applyStateStyle(c.state, c.text)
	}
	return strings.Join(parts, " ")
}

// breakdownLineStyler styles one already-wrapped line of a board STATE cell
// that carries a breakdown: each clause in its lane state's colour and the
// rest — the parent's own state, the brackets — in the parent's. It works on
// a produced line rather than on the whole text because the board wraps plain
// text first and styles each line after (task 050 decision 8); a clause is
// one word, so it is never split across the lines it styles.
func breakdownLineStyler(parent lipgloss.Style, clauses []breakdownClause) func(string) string {
	byText := make(map[string]string, len(clauses))
	for _, c := range clauses {
		byText[c.text] = c.state
	}
	return func(line string) string {
		words := strings.Split(line, " ")
		for i, w := range words {
			core := strings.TrimRight(strings.TrimLeft(w, "("), ")…")
			state, ok := byText[core]
			if !ok || core == "" {
				words[i] = parent.Render(w)
				continue
			}
			at := strings.Index(w, core)
			words[i] = parent.Render(w[:at]) + applyStateStyle(state, core) + parent.Render(w[at+len(core):])
		}
		return strings.Join(words, parent.Render(" "))
	}
}

// Outcome states an index or an iteration reduces to. They are step-run
// states (§5.4), so attemptStateGlyph and stepStateStyle speak them.
const (
	outcomeFailed      = "failed"
	outcomeInterrupted = "interrupted"
	outcomeRunning     = "running"
	outcomeStopped     = stepStateStopped
	outcomeSkipped     = "skipped"
	outcomeSucceeded   = "succeeded"
)

// stepsOutcome reduces a set of step rows — one loop iteration's body, or
// everything written at one top-level step index — to the one state that
// best describes it: the newest attempt of each step id counts, and of those
// the worst wins. A step that failed and was then retried green is green; a
// body where one step failed is failed however many others passed. A `break`
// or body `condition` that ended the pass reads as stopped. Rows that were
// all skipped read as skipped, and anything else finished reads as
// succeeded — approved included, as stepStateStyle already pairs the two.
//
// Repair rows (§5.4, task 025) are not attempts of the step they sit under
// and are left out. "" for no rows at all.
func stepsOutcome(rows []apiclient.StepRun) string {
	newest := map[string]apiclient.StepRun{}
	for _, r := range rows {
		if isRepairRun(r) {
			continue
		}
		cur, ok := newest[r.StepID]
		if !ok || r.Attempt > cur.Attempt || (r.Attempt == cur.Attempt && r.ID > cur.ID) {
			newest[r.StepID] = r
		}
	}
	if len(newest) == 0 {
		return ""
	}
	has := map[string]bool{}
	allSkipped := true
	for _, r := range newest {
		switch r.State {
		case "failed", "rejected":
			has[outcomeFailed] = true
		case outcomeInterrupted, outcomeRunning, outcomeStopped:
			has[r.State] = true
		}
		if r.State != outcomeSkipped {
			allSkipped = false
		}
	}
	for _, state := range []string{outcomeFailed, outcomeInterrupted, outcomeRunning, outcomeStopped} {
		if has[state] {
			return state
		}
	}
	if allSkipped {
		return outcomeSkipped
	}
	return outcomeSucceeded
}

// loopStripMax is how many iterations a loop strip shows: enough to see a
// pattern in — green, green, red, green — and short enough to sit beside the
// loop counter in the workspace header at 80 columns.
const loopStripMax = 10

// loopIterationOutcomes is the outcome of every iteration of the loop at
// index, oldest first. A skipped body reads as a pass here: the iteration ran
// and nothing in it failed, which is the only question a strip answers.
func loopIterationOutcomes(runs []apiclient.StepRun, index int) []string {
	byIteration := map[int][]apiclient.StepRun{}
	for _, r := range runs {
		if r.StepIndex == index && r.Iteration > 0 {
			byIteration[r.Iteration] = append(byIteration[r.Iteration], r)
		}
	}
	iterations := make([]int, 0, len(byIteration))
	for i := range byIteration {
		iterations = append(iterations, i)
	}
	sort.Ints(iterations)
	out := make([]string, 0, len(iterations))
	for _, i := range iterations {
		outcome := stepsOutcome(byIteration[i])
		if outcome == "" {
			continue
		}
		if outcome == outcomeSkipped {
			outcome = outcomeSucceeded
		}
		out = append(out, outcome)
	}
	return out
}

// renderLoopStrip is a loop's newest iterations as glyphs, oldest first and
// each in its step-state colour, led by `…+k` when k older ones were cut —
// `…+2 ✓✓×✓✓✓✓✓✓●`. Empty when no iteration has a row yet.
func renderLoopStrip(runs []apiclient.StepRun, index int) string {
	outcomes := loopIterationOutcomes(runs, index)
	if len(outcomes) == 0 {
		return ""
	}
	var b strings.Builder
	if cut := len(outcomes) - loopStripMax; cut > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("…+%d ", cut)))
		outcomes = outcomes[cut:]
	}
	for _, o := range outcomes {
		b.WriteString(renderStepGlyph(o))
	}
	return b.String()
}

// renderStepGlyph is a step-run state's glyph in that state's colour.
func renderStepGlyph(state string) string {
	glyph := attemptStateGlyph(state)
	if style, ok := stepStateStyle(state); ok {
		return style.Render(glyph)
	}
	return glyph
}

// renderStepPips is one pip per top-level step of the workflow, from the
// rows written at each index: a `parallel` group, a `loop` or a `fan_out` is
// one step of the workflow and so one pip, reduced with stepsOutcome (a
// loop's by its newest iteration). An index with no row yet is `○`, and the
// current step of a task waiting on a human is `!` whatever its row says —
// a gate's row reads `running` for as long as it waits. Follow-up rounds sit
// past step_total (task 027) and are not steps of the workflow, so they get
// no pip. Empty for a task with no step count.
func renderStepPips(t apiclient.Task, runs []apiclient.StepRun) string {
	if t.StepTotal <= 0 {
		return ""
	}
	byIndex := map[int][]apiclient.StepRun{}
	latest := latestIterations(runs)
	for _, r := range runs {
		if r.StepIndex < 0 || r.StepIndex >= t.StepTotal || r.Iteration != latest[r.StepIndex] {
			continue
		}
		byIndex[r.StepIndex] = append(byIndex[r.StepIndex], r)
	}
	var b strings.Builder
	for i := range t.StepTotal {
		outcome := stepsOutcome(byIndex[i])
		if i == t.CurrentStep && needsAttention(t.State) && outcome != outcomeFailed {
			b.WriteString(styleWarn.Render(attentionBadge))
			continue
		}
		if outcome == "" {
			b.WriteString(attemptStateGlyph("pending"))
			continue
		}
		b.WriteString(renderStepGlyph(outcome))
	}
	return b.String()
}

// boardStepPips is the board's plain pips, derived from the list row alone —
// the board never fetches step rows — so they say less than the workspace's:
// `✓` for every step before the current one, the task's own state glyph at
// it and `○` after. A state with no glyph of its own (paused) reads `○` at
// the cursor, which is what its step is: not running. Empty with no step
// count.
func boardStepPips(t apiclient.Task) string {
	k, n, ok := t.StepDisplay()
	if !ok {
		return ""
	}
	current := taskStateGlyph(t.State)
	if current == "" {
		current = "○"
	}
	return strings.Repeat("✓", k-1) + current + strings.Repeat("○", n-k)
}
