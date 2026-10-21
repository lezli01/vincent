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

// lines is the facts above the pinned footer. The subject and the checks
// wrap to at most three lines each rather than being cut to one (#804 PL-1):
// the title is what says which pull request this is, and a failing check's
// name sat past the cut.
func (f *pullMergeForm) lines(width int) []string {
	fit := func(s string) string { return ansi.Truncate(s, max(width, 1), "…") }
	facts := f.facts
	out := make([]string, 0, 12)
	for _, line := range wrapText(facts.subject+"  "+facts.title, max(width-2, 1), wrapOpts{maxLines: 3}) {
		out = append(out, styleTitle.Render("  "+line))
	}
	out = append(out,
		"",
		fit("  "+styleDim.Render("branches ")+valueOr(facts.headBranch, "?")+" → "+valueOr(facts.baseBranch, "?")),
		fit("  "+styleDim.Render("head     ")+shortRef(facts.head)),
	)
	const label = "  checks   "
	checks := wrapText(valueOr(facts.checks, "none reported"), max(width-cols(label), 1), wrapOpts{maxLines: 3})
	for i, line := range checks {
		lead := strings.Repeat(" ", cols(label))
		if i == 0 {
			lead = "  " + styleDim.Render("checks   ")
		}
		out = append(out, lead+line)
	}
	return append(out, "")
}

// footer is the method row and the status line, pinned under the facts so a
// short popup keeps the keys that confirm or cancel (#804 PL-1).
func (f *pullMergeForm) footer(width int) []string {
	fit := func(s string) string { return ansi.Truncate(s, max(width, 1), "…") }
	facts := f.facts
	methods := make([]string, 0, len(pullMergeMethods))
	for i, m := range pullMergeMethods {
		if i == f.method {
			methods = append(methods, styleSelected.Render(" "+m+" "))
			continue
		}
		methods = append(methods, styleDim.Render(" "+m+" "))
	}
	out := []string{fit("  " + styleDim.Render("method   ") + strings.Join(methods, " ")), ""}
	var status string
	style := styleDim
	switch {
	case f.sending:
		status = "merging…"
	case facts.moved:
		status, style = "⚠ the head moved since these checks were fetched — y waits for the refetch", styleWarn
	case !facts.mergeable:
		status, style = "⚠ this pull request can no longer be merged from here", styleWarn
	case f.method < 0:
		status = "←/→ choose a method — y does nothing until you do · n/esc cancel"
	default:
		status = "y " + pullMergeMethods[f.method] + " at " + shortRef(facts.head) + " · ←/→ method · n/esc cancel"
	}
	for _, line := range wrapText(status, max(width-2, 1), wrapOpts{maxLines: 3}) {
		out = append(out, style.Render("  "+line))
	}
	return out
}

func (f *pullMergeForm) height(width int) int { return len(f.lines(width)) + len(f.footer(width)) }

func (f *pullMergeForm) render(width, height int) string {
	return pinFooter(f.lines(width), f.footer(width), 0, 0, width, height)
}
