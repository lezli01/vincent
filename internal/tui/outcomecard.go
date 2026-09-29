package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The outcome card (task 129.14, issue #602) is the Overview's answer to
// "what did this task deliver": the result an agent reported, the changes on
// the branch, its commits, its pull request, and what it cost. It renders on
// `done` and `archived`, and on `aborted` under the failure card, headed as
// incomplete (brief decisions 1 and 2).
//
// Everything but the changes and the commits is read off the snapshot and
// the pull-request row the workspace already holds. Those two are one fetch
// each per open of the task — never refetched on an event (T3.4), cached by
// task id, and dropped when the task changes or the workspace closes. The
// diff stats are counted client-side from the unified diff (task 100
// decision 2); the daemon computes no outcome (#589 decision 4).

const (
	// resultLines is how many rendered lines of the result summary the card
	// shows at most.
	resultLines = 6
	// outcomeCommits is how many commit subjects the card lists.
	outcomeCommits = 3
)

// summaryRun picks the attempt whose result_summary the card shows: the
// newest succeeded agent attempt, follow-up rounds included (task 027); then
// the newest attempt that said anything at all; then the task's own status
// message, returned as fallback with ok false. The task's own attempts only
// (task 088 decision 9) — a lane's summary is the lane's.
func summaryRun(task apiclient.TaskDetail) (run apiclient.StepRun, fallback string, ok bool) {
	var agent, said apiclient.StepRun
	for _, r := range task.Steps {
		if r.StepType == "agent" && r.State == stepSucceeded && r.ID > agent.ID {
			agent = r
		}
		if strings.TrimSpace(r.ResultSummary) != "" && r.ID > said.ID {
			said = r
		}
	}
	switch {
	case agent.ID != 0:
		return agent, "", true
	case said.ID != 0:
		return said, "", true
	}
	if task.StatusMessage != nil {
		fallback = strings.TrimSpace(*task.StatusMessage)
	}
	return apiclient.StepRun{}, fallback, false
}

// resultSource names where a result came from, the way the failure card's
// headline names an attempt: the step, the loop iteration and item when
// there is one, and the attempt.
func resultSource(run apiclient.StepRun) string {
	parts := []string{fmt.Sprintf("from step %d %s", run.StepIndex+1, stepLabel(run))}
	if run.Iteration > 0 {
		parts = append(parts, fmt.Sprintf("iteration %d", run.Iteration))
		if run.LoopItem != nil {
			parts = append(parts, "item "+strconv.Quote(*run.LoopItem))
		}
	}
	parts = append(parts, fmt.Sprintf("attempt %d", run.Attempt))
	return strings.Join(parts, " · ")
}

// outcomeFetch is the card's two fetches, keyed by the task they are for.
type outcomeFetch struct {
	taskID int64
	// diffAsked and commitsAsked say the fetch was issued for taskID; they
	// are what keeps an event from issuing a second one.
	diffAsked, commitsAsked bool
	// checksAsked says the card has asked for the Pull Request tab's check
	// rollup once on this open, the fetch the tab itself makes on entry.
	checksAsked bool

	diffDone bool
	diffErr  error
	files    []diffFile

	commitsDone bool
	commitsErr  error
	commits     []apiclient.Commit
}

// outcomeDiffMsg and outcomeCommitsMsg carry the two fetches' results.
type outcomeDiffMsg struct {
	taskID   int64
	sections []apiclient.DiffSection
	err      error
}

type outcomeCommitsMsg struct {
	taskID  int64
	commits []apiclient.Commit
	err     error
}

// showsOutcomeCard is whether the Overview draws the card.
func (t *taskView) showsOutcomeCard() bool {
	if !t.detail.loaded {
		return false
	}
	switch t.detail.task.State {
	case stateDone, stateAborted, stateArchived:
		return true
	}
	return false
}

// syncOutcome issues the card's fetches the first time the Overview shows it
// for a task, and never again for that task: it runs after every message,
// and every message after the first finds them already asked. An archived
// task has no worktree, so it asks no diff (the route would 409).
func (t *taskView) syncOutcome() tea.Cmd {
	if t.tab != taskTabOverview || !t.detail.active || !t.showsOutcomeCard() {
		return nil
	}
	client, taskID := t.detail.client, t.detail.taskID
	if t.outcome.taskID != taskID {
		t.outcome = outcomeFetch{taskID: taskID}
	}
	var cmds []tea.Cmd
	if !t.outcome.diffAsked && t.detail.task.State != stateArchived && client != nil {
		t.outcome.diffAsked = true
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			// The Diff tab's own request, so the card and the tab count
			// the same diff; the sections are summed, never told apart.
			sections, err := client.DiffByLane(ctx, taskID)
			return outcomeDiffMsg{taskID: taskID, sections: sections, err: err}
		})
	}
	if !t.outcome.commitsAsked && client != nil {
		t.outcome.commitsAsked = true
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			commits, err := client.TaskCommits(ctx, taskID)
			return outcomeCommitsMsg{taskID: taskID, commits: commits, err: err}
		})
	}
	// The checks are the Pull Request tab's, fetched on its entry. The card
	// asks for that same fetch once, and only once the pull row has said
	// there is a tab to have checks for.
	if !t.outcome.checksAsked && t.pullTabAvailable() && !t.pullTab.loaded && !t.pullTab.fetching {
		t.outcome.checksAsked = true
		cmds = append(cmds, t.checksCmd())
	}
	return tea.Batch(cmds...)
}

func (t *taskView) applyOutcomeDiff(msg outcomeDiffMsg) {
	if msg.taskID != t.outcome.taskID {
		return
	}
	t.outcome.diffDone, t.outcome.diffErr = true, msg.err
	for _, sec := range msg.sections {
		_, files := parseDiffFiles(splitDiff(sec.Diff))
		t.outcome.files = append(t.outcome.files, files...)
	}
}

func (t *taskView) applyOutcomeCommits(msg outcomeCommitsMsg) {
	if msg.taskID != t.outcome.taskID {
		return
	}
	t.outcome.commitsDone = true
	t.outcome.commits, t.outcome.commitsErr = msg.commits, msg.err
}

// branchGone is the commits route's 409: the branch no longer exists, which
// for an archived task means archive deleted it (task 092 deletes a branch
// with nothing past its base).
func (o outcomeFetch) branchGone() bool {
	var apiErr *apiclient.Error
	return o.commitsDone && errors.As(o.commitsErr, &apiErr) && apiErr.Status == http.StatusConflict
}

// jumpToResult is the result source's jump: the shared attempt cursor moves
// to the attempt the result came from and Output opens at its end, which is
// where an agent's final message is.
func (t *taskView) jumpToResult() tea.Cmd {
	run, _, ok := summaryRun(t.detail.task)
	if !ok || !t.showsOutcomeCard() {
		return nil
	}
	d := t.detail
	var cmds []tea.Cmd
	if run.ID != d.selectedRun {
		d.selectedRun = run.ID
		cmds = append(cmds, d.syncOutput())
	}
	// After syncOutput, which follows only a live attempt.
	d.setFollowing(true)
	return tea.Batch(append(cmds, t.setTab(taskTabOutput))...)
}

// outcomeCardLines renders the card within the given height budget. A card
// that does not fit gives up its result text first, then collapses to its
// heading and its changes line; on an aborted task it is the card that
// collapses, never the failure card above it (brief decision 2).
func (t *taskView) outcomeCardLines(width, height int) []string {
	if height <= 0 {
		return nil
	}
	for _, n := range []int{resultLines, 2} {
		if out := t.outcomeBody(width, n); len(out) <= height {
			return out
		}
	}
	out := []string{t.outcomeHeading(), t.changesLine()}
	return out[:min(len(out), height)]
}

func (t *taskView) outcomeHeading() string {
	if t.detail.task.State == stateAborted {
		return section("incomplete — what was delivered")
	}
	return section("Outcome")
}

// outcomeBody is the whole card with the result cut to n rendered lines.
func (t *taskView) outcomeBody(width, n int) []string {
	out := []string{t.outcomeHeading(), ""}
	out = append(out, t.resultLines(width, n)...)
	out = append(out, "", t.changesLine())
	out = append(out, t.commitLines(width)...)
	if line, ok := t.outcomePullLine(); ok {
		out = append(out, line)
	}
	out = append(out, t.costLine())
	if line, ok := t.rollupLine(); ok {
		out = append(out, line)
	}
	for i, line := range out {
		out[i] = ansi.Truncate(line, max(width, 1), "…")
	}
	return out
}

// resultLines is the result section: the source label, then the summary
// rendered as Markdown and cut to n lines.
func (t *taskView) resultLines(width, n int) []string {
	run, fallback, ok := summaryRun(t.detail.task)
	var label, text string
	switch {
	case ok:
		label = styleTitle.Render("Result") + "  " + resultSource(run)
		if key := opKey(keymap.Result); key != "" {
			label += "   " + styleKey.Render(key) + " output"
		}
		text = run.ResultSummary
	case fallback != "":
		label = styleTitle.Render("Result") + "  " + styleDim.Render("from the task's status message")
		text = fallback
	default:
		return []string{"  " + styleTitle.Render("Result") + "  " + styleDim.Render("no step reported a result")}
	}
	out := []string{"  " + label}
	// A summary is kept by its tail (#594), so it can start inside a fence or
	// halfway through a rune; the renderer takes whatever is left whole.
	text = strings.TrimSpace(strings.ToValidUTF8(text, "�"))
	if text == "" {
		return append(out, styleDim.Render("    the attempt left no summary"))
	}
	rendered := markdownLines(text, max(width-4, 12))
	for len(rendered) > 0 && strings.TrimSpace(ansi.Strip(rendered[len(rendered)-1])) == "" {
		rendered = rendered[:len(rendered)-1]
	}
	cut := len(rendered) > n
	if cut {
		rendered = rendered[:n]
	}
	for _, line := range rendered {
		out = append(out, "    "+line)
	}
	if cut {
		out = append(out, styleDim.Render("    …"))
	}
	return out
}

// changesLine is the files, the line counts and the branch. An archived task
// has no worktree to diff, so it says what archive kept instead.
func (t *taskView) changesLine() string {
	task := t.detail.task
	branch := valueOr(task.BranchName, "not created")
	o := t.outcome
	if task.State == stateArchived {
		if o.branchGone() {
			return "  ± worktree removed — branch " + branch + " gone"
		}
		return "  ± worktree removed — branch " + branch + " kept"
	}
	switch {
	case o.taskID != t.detail.taskID || !o.diffDone:
		return styleDim.Render("  ± reading the diff…")
	case o.diffErr != nil:
		return styleDim.Render("  ± diff unavailable: " + errString(o.diffErr))
	case len(o.files) == 0:
		return "  ± no changes · branch " + branch
	}
	added, removed := 0, 0
	for _, f := range o.files {
		added += f.added
		removed += f.removed
	}
	return fmt.Sprintf("  ± %s · +%d −%d · branch %s", plural(len(o.files), "file", "files"), added, removed, branch)
}

// commitLines is the commit count and the newest subjects. An older daemon
// that serves no commits route, and a branch that is gone, leave no line.
func (t *taskView) commitLines(width int) []string {
	o := t.outcome
	if o.taskID != t.detail.taskID || !o.commitsDone || o.branchGone() ||
		errors.Is(o.commitsErr, apiclient.ErrCommitsUnsupported) {
		return nil
	}
	if o.commitsErr != nil {
		return []string{styleDim.Render("  ● commits unavailable: " + errString(o.commitsErr))}
	}
	if len(o.commits) == 0 {
		return []string{"  ● no commits past the base"}
	}
	out := []string{"  ● " + plural(len(o.commits), "commit", "commits")}
	for i := len(o.commits) - 1; i >= 0 && i >= len(o.commits)-outcomeCommits; i-- {
		c := o.commits[i]
		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		out = append(out, ansi.Truncate("    "+styleDim.Render(sha)+" "+c.Subject, max(width, 1), "…"))
	}
	return out
}

// outcomePullLine is the linked pull request and its check rollup, read off
// the pull row and the rollup the Pull Request tab already holds.
func (t *taskView) outcomePullLine() (string, bool) {
	if t.detail.task.GitHubPull == nil {
		return "", false
	}
	if !t.pull.Linked {
		return styleDim.Render("  ⇡ pull request: reading…"), true
	}
	line := "  ⇡ #" + strconv.Itoa(t.pull.Number)
	if p := t.pull.Pull; p != nil {
		line += " " + p.Status()
	}
	if checks := t.checksSummary(); checks != "" {
		line += " · " + checks
	}
	return line, true
}

// checksSummary is the rollup in a glyph and a word: never colour alone.
func (t *taskView) checksSummary() string {
	c := t.pullTab.checks
	if !t.pullTab.loaded || t.pullTab.err != "" || c.Reason != "" {
		return ""
	}
	if len(c.Runs) == 0 {
		return "no checks"
	}
	passed := 0
	for _, r := range c.Runs {
		if r.State == "success" {
			passed++
		}
	}
	frac := fmt.Sprintf("checks %d/%d", passed, len(c.Runs))
	switch c.State {
	case "success":
		return frac + " ✓ passing"
	case "failure":
		return frac + " ✗ failing"
	case "in_progress":
		return frac + " ◌ running"
	default:
		return frac
	}
}

// costLine is the spend — the whole tree's when the task has lanes (task
// 116) — and the §17 active time, the sum of the attempts' durations.
func (t *taskView) costLine() string {
	task := t.detail.task
	label, cost := "cost", taskOwnCost(task)
	if task.Children != nil && task.Children.Total > 0 {
		label, cost = "tree cost", treeCost(cost, task.Children.CostUSD)
	}
	var active time.Duration
	now := time.Now()
	for _, run := range task.Steps {
		if d, ok := run.Duration(now); ok {
			active += d
		}
	}
	return "  $ " + label + " " + formatCost(cost) + " · active " + formatElapsed(active)
}

// rollupLine is the lanes merged and the loop's iterations, each left out
// when it is zero.
func (t *taskView) rollupLine() (string, bool) {
	task := t.detail.task
	var parts []string
	if c := task.Children; c != nil && c.ByState[stateDone] > 0 {
		parts = append(parts, plural(c.ByState[stateDone], "lane", "lanes")+" merged")
	}
	iterations := 0
	for _, run := range task.Steps {
		iterations = max(iterations, run.Iteration)
	}
	if iterations > 0 {
		parts = append(parts, "loop ran "+plural(iterations, "iteration", "iterations"))
	}
	if len(parts) == 0 {
		return "", false
	}
	return "  ↻ " + strings.Join(parts, " · "), true
}
