package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The merge confirmation (task 068.4, issue #387).
//
// It opens with **no method chosen** (task 068 decision 4, settled again for
// the popup): the wire has no default and the CLI's `--method` is required,
// so a popup that preselected one would be the one place a method was picked
// for the human. `y` does nothing until ←/→ has picked merge, squash or
// rebase, and `enter` never confirms — it means "open the row" everywhere
// else, and a habitual `enter` must not merge a pull request.
//
// What it confirms is pinned: the head it shows, and sends as `head_sha`, is
// the commit whose checks the human was reading. When the rollup and the pull
// row disagree about that commit, `y` stays inert until a refetch makes them
// agree; a push after that is the daemon's `head_changed`.

// pullMergeMethods is the wire vocabulary, in the order ←/→ walks it.
var pullMergeMethods = []string{"merge", "squash", "rebase"}

// pullMergeFacts is what the popup shows. The task view refreshes it whenever
// the pull row or the rollup is refetched, so the popup confirms what is on
// screen now rather than what was there when it opened.
type pullMergeFacts struct {
	subject    string
	title      string
	headBranch string
	baseBranch string
	head       string
	moved      bool
	checks     string
	mergeable  bool
}

type pullMergeForm struct {
	taskID int64
	facts  pullMergeFacts
	// method indexes pullMergeMethods; -1 is "none chosen", which is where
	// the popup opens.
	method  int
	sending bool
	// submit posts the merge. Injected by the task view, which owns the API
	// client, so the popup itself has no way to reach the daemon.
	submit func(method, head string) tea.Cmd
}

func newPullMergeForm(taskID int64) *pullMergeForm {
	return &pullMergeForm{taskID: taskID, method: -1}
}

// ready reports that `y` would send.
func (f *pullMergeForm) ready() bool {
	return f.method >= 0 && !f.sending && f.facts.mergeable && f.facts.head != "" && !f.facts.moved
}

// update handles one key. exit asks the task view to close the popup.
func (f *pullMergeForm) update(msg tea.KeyPressMsg) (cmd tea.Cmd, exit bool) {
	switch msg.String() {
	case "left", "h":
		f.method = max(f.method-1, 0)
	case "right", "l":
		f.method = min(f.method+1, len(pullMergeMethods)-1)
	case "y":
		if !f.ready() || f.submit == nil {
			return nil, false
		}
		f.sending = true
		return f.submit(pullMergeMethods[f.method], f.facts.head), false
	case "n", "esc":
		return nil, true
	}
	return nil, false
}

func (f *pullMergeForm) lines(width int) []string {
	fit := func(s string) string { return ansi.Truncate(s, max(width, 1), "…") }
	facts := f.facts
	out := []string{
		fit(styleTitle.Render("  " + facts.subject + "  " + facts.title)),
		"",
		fit("  " + styleDim.Render("branches ") + valueOr(facts.headBranch, "?") + " → " + valueOr(facts.baseBranch, "?")),
		fit("  " + styleDim.Render("head     ") + shortRef(facts.head)),
		fit("  " + styleDim.Render("checks   ") + valueOr(facts.checks, "none reported")),
		"",
	}
	methods := make([]string, 0, len(pullMergeMethods))
	for i, m := range pullMergeMethods {
		if i == f.method {
			methods = append(methods, styleSelected.Render(" "+m+" "))
			continue
		}
		methods = append(methods, styleDim.Render(" "+m+" "))
	}
	out = append(out, fit("  "+styleDim.Render("method   ")+strings.Join(methods, " ")))
	out = append(out, "")
	switch {
	case f.sending:
		out = append(out, styleDim.Render("  merging…"))
	case facts.moved:
		out = append(out, fit(styleWarn.Render(
			"  ⚠ the head moved since these checks were fetched — y waits for the refetch")))
	case !facts.mergeable:
		out = append(out, fit(styleWarn.Render("  ⚠ this pull request can no longer be merged from here")))
	case f.method < 0:
		out = append(out, fit(styleDim.Render("  ←/→ choose a method — y does nothing until you do · n/esc cancel")))
	default:
		out = append(out, fit(styleDim.Render(
			"  y "+pullMergeMethods[f.method]+" at "+shortRef(facts.head)+" · ←/→ method · n/esc cancel")))
	}
	return out
}

func (f *pullMergeForm) height(width int) int { return len(f.lines(width)) }

func (f *pullMergeForm) render(width, height int) string {
	lines := f.lines(width)
	return strings.Join(windowRange(lines, 0, len(lines), height), "\n")
}
