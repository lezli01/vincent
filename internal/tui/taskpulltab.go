package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
)

// The task workspace's Pull Request tab (task 068, spec §15 view 2).
//
// It is the sixth tab and it is **conditional**: present only when this task
// has a live link and the integration is usable. Appending it after Workflow
// is what keeps 1–5 meaning what task 049 and task 051 taught them to mean,
// and being last is what makes its absence cost nothing — no other tab's
// number moves when it is not there.
//
// The check rows are fetched, never stored. A check result that is a minute
// old reads exactly like a current one while being wrong, which is the same
// reason a pull request is a pointer and not a snapshot: this refetches on
// tab open, on the reconciler's task.github_pull_changed, on the poll tick
// the workspace already subscribes to, and on `r`. Never per render.

// checksPollInterval is how often the tab refetches while it is open. It is
// the tab's own clock rather than the daemon's `github.poll_interval`,
// because that setting paces a background reconciler over every project and
// this is one screen a human is looking at right now.
const checksPollInterval = 15 * time.Second

// taskPullTab is the tab's own state: the rows, the cursor, and nothing else.
// Everything renderable about the pull request itself is read from taskView's
// existing pull row, so the tab and the Task Details section cannot disagree
// about what is linked.
type taskPullTab struct {
	checks   apiclient.GitHubTaskChecks
	loaded   bool
	err      string
	cursor   int
	note     string
	noteBad  bool
	fetching bool
}

// taskChecksMsg carries GET /v1/tasks/{id}/github/pull/checks.
type taskChecksMsg struct {
	taskID int64
	checks apiclient.GitHubTaskChecks
	err    error
}

// taskChecksTickMsg is the tab's own refetch clock. It carries the task id so
// a tick scheduled for a task the human has since left is dropped rather than
// refetching somebody else's checks.
type taskChecksTickMsg struct{ taskID int64 }

// pullTabAvailable reports whether the Pull Request tab exists for this task.
//
// Two conditions, and both are read off the pull row the workspace already
// fetched rather than probed separately: a live link, and an integration that
// is not switched off. `github.enabled: false` hides the tab as it hides the
// rest of the integration, and the daemon says so by answering the row with
// the `disabled` reason.
func (t *taskView) pullTabAvailable() bool {
	return t.pull.Linked && t.pull.Reason != github.ReasonDisabled
}

// tabs is the tab strip as it currently stands. It exists because the strip
// is no longer fixed: `cycleTab` used to be modulo taskTabCount, which lands
// on a tab that is not there the moment one of them is conditional.
func (t *taskView) tabs() []taskViewTab {
	tabs := []taskViewTab{taskTabSteps, taskTabDetails, taskTabOutput, taskTabDiff, taskTabWorkflow}
	if t.pullTabAvailable() {
		tabs = append(tabs, taskTabPull)
	}
	return tabs
}

// tabIndex is where the current tab sits in the strip, or 0 when it is not on
// it at all — which happens for exactly one moment, when a human unlinks the
// pull request while standing on its tab.
func (t *taskView) tabIndex(tab taskViewTab) int {
	for i, candidate := range t.tabs() {
		if candidate == tab {
			return i
		}
	}
	return 0
}

// checksCmd fetches the rollup.
func (t *taskView) checksCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	t.pullTab.fetching = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		checks, err := client.TaskGitHubChecks(ctx, id)
		return taskChecksMsg{taskID: id, checks: checks, err: err}
	}
}

// checksTickCmd schedules the next refetch. Scheduling it from the *reply*
// rather than from a repeating ticker is what keeps a slow GitHub from
// queueing fetches behind each other.
func (t *taskView) checksTickCmd() tea.Cmd {
	id := t.detail.taskID
	return tea.Tick(checksPollInterval, func(time.Time) tea.Msg {
		return taskChecksTickMsg{taskID: id}
	})
}

// leaveAbsentPullTab moves off the Pull Request tab when it has stopped
// existing. That happens for exactly one reason — the link went away, by `u`
// here or by a reconciler tick — and it is the only state a conditional tab
// can strand a human on.
func (t *taskView) leaveAbsentPullTab() {
	if t.tab == taskTabPull && !t.pullTabAvailable() {
		t.tab = taskTabDetails
	}
}

func (t *taskView) applyChecks(msg taskChecksMsg) {
	if msg.taskID != t.detail.taskID {
		return
	}
	t.pullTab.fetching = false
	t.pullTab.loaded = true
	if msg.err != nil {
		t.pullTab.err = errString(msg.err)
		return
	}
	t.pullTab.checks, t.pullTab.err = msg.checks, ""
	if t.pullTab.cursor >= len(msg.checks.Runs) {
		t.pullTab.cursor = max(len(msg.checks.Runs)-1, 0)
	}
}

// selectedCheck is the row the key hints are about, and nil when there are no
// rows.
func (t *taskView) selectedCheck() *apiclient.GitHubCheckRun {
	runs := t.pullTab.checks.Runs
	if t.pullTab.cursor < 0 || t.pullTab.cursor >= len(runs) {
		return nil
	}
	return &runs[t.pullTab.cursor]
}

func (t *taskView) updatePullTabKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		t.movePullCursor(-1)
		return nil
	case "down", "j":
		t.movePullCursor(1)
		return nil
	case "r":
		t.pullTab.note, t.pullTab.noteBad = "", false
		return tea.Batch(t.pullCmd(), t.checksCmd())
	case "o":
		return t.openPullCmd()
	case "c":
		return t.openCheckCmd()
	case "u":
		return t.unlinkPullCmd()
	}
	// Task actions stay reachable from here, as they do from Task Details:
	// the tab a human happens to be reading is not a statement about what
	// they may do to the task.
	return t.detail.update(msg)
}

func (t *taskView) movePullCursor(delta int) {
	n := len(t.pullTab.checks.Runs)
	if n == 0 {
		t.pullTab.cursor = 0
		return
	}
	t.pullTab.cursor = min(max(t.pullTab.cursor+delta, 0), n-1)
}

// openCheckCmd is `c`: the selected check's own page. A check that reported
// no URL says so rather than opening the pull request instead — a key that
// silently does something else is worse than one that explains itself.
func (t *taskView) openCheckCmd() tea.Cmd {
	run := t.selectedCheck()
	if run == nil {
		t.pullTab.note, t.pullTab.noteBad = "there is no check selected", true
		return nil
	}
	if run.URL == "" {
		t.pullTab.note, t.pullTab.noteBad = run.Name+" reported no page to open", true
		return nil
	}
	t.pullTab.note, t.pullTab.noteBad = "", false
	return openURLCmd(run.URL)
}

// unlinkPullCmd is `u`: the second home task 068 gives unlink (superseding
// task 052 decision 6, which put it only in the takeover). It writes
// vincent's own column and makes no GitHub call, and the suppression is
// sticky exactly as the takeover's is — the reconciler must be able to read
// the refusal, so the row is marked rather than cleared.
//
// The takeover keeps its copy: a pull request no task claims has no workspace
// to be reached from, and that is the case decision 6 exists for.
func (t *taskView) unlinkPullCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 || !t.pull.Linked {
		t.pullTab.note, t.pullTab.noteBad = "no pull request is linked to this task", true
		return nil
	}
	t.pullTab.note, t.pullTab.noteBad = "", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		if _, err := client.UnlinkGitHubPull(ctx, id); err != nil {
			return taskPullMsg{taskID: id, err: err}
		}
		pull, err := client.TaskGitHubPull(ctx, id)
		return taskPullMsg{taskID: id, pull: pull, err: err}
	}
}

func (t *taskView) renderPullTab(width, height int) string {
	var lines []string
	lines = append(lines, t.pullHeaderLines(width)...)
	lines = append(lines, "")
	lines = append(lines, t.checkLines(width)...)
	if lanes := t.laneRowLines(width); len(lanes) > 0 {
		lines = append(lines, "")
		lines = append(lines, lanes...)
	}
	if note := t.pullTab.note; note != "" {
		style := styleDim
		if t.pullTab.noteBad {
			style = styleWarn
		}
		lines = append(lines, "", style.Render("  "+note))
	}
	lines = append(lines, "", styleDim.Render("  "+t.pullHintLine()))
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:max(height, 1)], "\n")
}

// pullHeaderLines are the facts the Task Details section renders, on their own
// screen. They are read from the same pull row, so the two cannot disagree.
func (t *taskView) pullHeaderLines(width int) []string {
	title := fmt.Sprintf("  %s#%d", t.pull.Repo, t.pull.Number)
	rows := [][2]string{}
	if p := t.pull.Pull; p != nil {
		title += "  " + p.Title
		rows = append(rows,
			[2]string{"state", pullStateWord(*p)},
			[2]string{"head", p.HeadBranch},
			[2]string{"base", p.BaseBranch},
			[2]string{"author", p.Author},
			[2]string{"url", p.URL},
		)
	} else if t.pull.Reason != "" {
		rows = append(rows, [2]string{"github", github.Message(t.pull.Reason)})
	}
	rows = append(rows, [2]string{"linked by", t.pull.Source})
	out := []string{ansi.Truncate(styleTitle.Render(title), max(width, 1), "…")}
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		out = append(out, ansi.Truncate(
			fmt.Sprintf("    %-10s %s", styleDim.Render(row[0]), row[1]), max(width, 1), "…"))
	}
	return out
}

// pullStateWord is the one word for the pull request itself. It is computed
// here rather than read from a field because the daemon sends state, draft
// and merged separately, exactly as github.PullRequest.Status does.
func pullStateWord(p apiclient.GitHubPullRequest) string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == "closed":
		return "closed"
	case p.Draft:
		return "draft"
	default:
		return p.State
	}
}

func (t *taskView) checkLines(width int) []string {
	if !t.pullTab.loaded && t.pullTab.fetching {
		return []string{styleDim.Render("  checks — loading…")}
	}
	if t.pullTab.err != "" {
		return []string{styleWarn.Render("  checks — " + t.pullTab.err)}
	}
	checks := t.pullTab.checks
	if checks.Reason != "" {
		return []string{styleWarn.Render("  checks — " + github.Message(checks.Reason))}
	}
	if len(checks.Runs) == 0 {
		return []string{styleDim.Render("  checks — none reported on this pull request's head commit")}
	}
	head := fmt.Sprintf("  checks — %s on %s", checks.State, shortRef(checks.Ref))
	out := []string{styleTitle.Render(head)}
	for i, run := range checks.Runs {
		marker := "  "
		if i == t.pullTab.cursor {
			marker = "▸ "
		}
		line := fmt.Sprintf("  %s%-14s %s", marker, run.State, run.Name)
		if run.Actions() {
			line += styleDim.Render("  actions run " + strconv.FormatInt(run.RunID, 10))
		}
		out = append(out, ansi.Truncate(checkStyle(run).Render(line), max(width, 1), "…"))
	}
	return out
}

// checkStyle colours a row by what it is telling you: red for a conclusion a
// human has to act on, faint while it is still going, green when it passed.
// A neutral or skipped row is left unstyled — it is neither news nor a
// problem.
func checkStyle(run apiclient.GitHubCheckRun) lipgloss.Style {
	switch {
	case run.Failed():
		return styleBad
	case run.Running():
		return styleDim
	case run.State == "success":
		return styleOK
	default:
		return lipgloss.NewStyle()
	}
}

// shortRef abbreviates a commit the way git does. It is display only: the
// full ref is what was fetched against.
func shortRef(ref string) string {
	if len(ref) > 12 {
		return ref[:12]
	}
	if ref == "" {
		return "(unknown commit)"
	}
	return ref
}

// pullHintLine names what the selected row can do. Re-run is deliberately
// *absent* rather than present-and-refusing on a row no Actions run backs
// (task 068 decision 3) — and absent on every row until 068.4 lands the write
// leg, because a key hint for an operation that does not exist yet is the
// same lie in a different place.
func (t *taskView) pullHintLine() string {
	hints := []string{"o open PR", "r refresh", "u unlink"}
	if len(t.lanes) > 0 {
		hints = append(hints, "l open lane")
	}
	if run := t.selectedCheck(); run != nil && run.URL != "" {
		hints = append([]string{"c open check"}, hints...)
	}
	return strings.Join(hints, " · ")
}

// ---------------------------------------------------------------------------
// Lane rows (#316).
//
// A fan-out parent's own pull request is unchanged and stays on top. Beneath
// it, one row per lane: its branch, and the pull request linked to it. The
// rows carry no checks — checks stay one call for one task, and `l` opens the
// lane, whose own Pull Request tab has them.
// ---------------------------------------------------------------------------

// lanePullsCmd fetches the project's pull requests once, rather than one
// call per lane: every row carries the task it is linked to, so a single
// listing answers for all of them. `all` rather than the default `open`
// because a merged lane is precisely the one a reader is checking on.
func (t *taskView) lanePullsCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	project := t.detail.task.ProjectID
	if client == nil || id == 0 || project == 0 || len(t.lanes) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		pulls, err := client.ListGitHubPulls(ctx, project, apiclient.GitHubPullsOptions{State: "all"})
		return taskLanePullsMsg{taskID: id, pulls: pulls, err: err}
	}
}

// applyLanePulls keeps the last good mapping on a failed listing: GitHub
// being unreachable is not a lane losing its pull request, and blanking the
// rows would say it was.
func (t *taskView) applyLanePulls(msg taskLanePullsMsg) {
	if msg.taskID != t.detail.taskID || msg.err != nil {
		return
	}
	pulls := make(map[int64]apiclient.GitHubPullRequest, len(t.lanes))
	for _, pull := range msg.pulls {
		if pull.TaskID != nil {
			pulls[*pull.TaskID] = pull
		}
	}
	t.lanePulls = pulls
}

// laneRowLines is the lane block under the parent's own pull request.
func (t *taskView) laneRowLines(width int) []string {
	if len(t.lanes) == 0 {
		return nil
	}
	out := []string{styleTitle.Render(fmt.Sprintf("  lanes — %d", len(t.lanes)))}
	for i, lane := range t.lanes {
		marker := "  "
		if i == t.laneSel {
			marker = "▸ "
		}
		pull := "no pull request"
		if p, ok := t.lanePulls[lane.ID]; ok {
			pull = fmt.Sprintf("#%d %s", p.Number, p.Status())
		}
		line := fmt.Sprintf("  %s%-14s %-12s %s", marker, lane.State, pull,
			valueOr(lane.BranchName, "no branch"))
		line = laneRowStyle(lane).Render(line) +
			styleDim.Render("  "+laneName(lane)+" · task "+strconv.FormatInt(lane.ID, 10))
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	return out
}

// laneRowStyle colours a lane row by whether it is news: a lane that stopped
// short of `done` is what a reader on this tab is looking for.
func laneRowStyle(lane apiclient.Task) lipgloss.Style {
	switch {
	case lane.State == stateDone:
		return styleOK
	case lane.State == stateBlocked:
		return styleBad
	case laneSettled(lane.State):
		return styleWarn
	default:
		return styleDim
	}
}
