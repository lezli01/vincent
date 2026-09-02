package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The daemon view's status-line flow (task 082): the screen that offers to
// make vincent Claude Code's status line, and the screen that offers to take
// it back out again.
//
// It is a takeover for the same reason the config editor is — while it is open
// it owns the keyboard — and for one more that the editor does not have. This
// is the one place in vincent that writes to a file another tool owns, and
// §16's terms for that (statusline.go) include showing the exact JSON first.
// A line at the bottom of a busy view is not showing it; a screen with nothing
// else on it is.

// statusLineFlow is the open flow. Every field is what happened, not what is
// about to: the write is synchronous, the way the first-run acknowledgment's
// is, because it is one small local file and a spinner for it would be a
// fiction.
type statusLineFlow struct {
	plan statusLinePlan
	// planErr is a settings file that could not be read or parsed. The flow
	// still opens on it — `i` is documented and must do something — and says
	// what is wrong, because that is the fact somebody pressing `i` needs.
	planErr error
	// outcome is set once a write has happened, and turns the screen into a
	// report of it. err is a write that did not.
	outcome string
	err     error
}

func newStatusLineFlow(plan statusLinePlan, planErr error) *statusLineFlow {
	return &statusLineFlow{plan: plan, planErr: planErr}
}

// reporting is the second screen: something was done, or refused, and the
// flow is now saying so. Any key leaves it.
func (f *statusLineFlow) reporting() bool { return f.outcome != "" || f.err != nil }

// update answers one key and reports whether the flow is finished with the
// keyboard. The caller re-reads the plan when it closes: the file may be a
// different file now, and every screen here is drawn from a reading of it.
func (f *statusLineFlow) update(msg tea.KeyPressMsg, dataDir string) (done bool) {
	if f.reporting() {
		return true
	}
	switch msg.String() {
	case "esc":
		return true
	case "n":
		if f.plan.installed || f.planErr != nil {
			// There is no offer on those screens, so there is nothing to
			// decline; esc's answer is the right one.
			return true
		}
		if err := writeStatusLineDecline(dataDir); err != nil {
			// Worth saying out loud rather than swallowing: the answer was
			// "no", and an unrecorded no is a question that comes back.
			f.err = fmt.Errorf("remember that: %w", err)
			return false
		}
		return true
	case "enter", "y":
		if f.planErr != nil {
			return true
		}
		if f.plan.installed {
			if err := f.plan.uninstall(); err != nil {
				f.err = err
				return false
			}
			f.outcome = "restored the status line that was there before vincent"
			if len(f.plan.restore) == 0 {
				f.outcome = "removed the statusLine key — Claude Code's own default is back"
			}
			return false
		}
		if err := f.plan.install(); err != nil {
			f.err = err
			return false
		}
		f.outcome = "vincent is now Claude Code's status line"
		return false
	}
	return false
}

func (f *statusLineFlow) render(width int) []string {
	_ = width // The screen is hand-wrapped; nothing here reflows.
	out := []string{" " + styleTitle.Render("claude status line"), ""}
	switch {
	case f.reporting():
		return append(out, f.reportLines()...)
	case f.planErr != nil:
		return append(out,
			"   "+styleBad.Render("⚠ "+errString(f.planErr)),
			"",
			"   "+styleDim.Render("vincent will not rewrite a settings file it could not read:"),
			"   "+styleDim.Render("every key it did not understand would be dropped by the write."),
			"",
			"   "+styleKey.Render("esc")+styleDim.Render(" close"))
	case f.plan.installed:
		return append(out, f.uninstallLines()...)
	default:
		return append(out, f.installLines()...)
	}
}

// installLines is the offer. The order is the argument: what it does, then the
// exact bytes, then what it costs, then the keys.
func (f *statusLineFlow) installLines() []string {
	out := []string{
		"   " + styleDim.Render("Claude Code runs a command to draw its status line. Pointing it at"),
		"   " + styleDim.Render("vincent puts your running tasks on it, in every session, without the TUI."),
		"",
		"   " + styleDim.Render("This writes to ") + f.plan.path + styleDim.Render(":"),
		"",
	}
	out = append(out, previewLines(f.plan.preview())...)
	out = append(out, "", "   "+styleDim.Render("Every other key in that file is left exactly as it is."))
	if len(f.plan.current) > 0 {
		out = append(out,
			"   "+styleDim.Render("Your current status line is carried in that ")+
				statusLineWrapFlag+styleDim.Render(" payload: vincent runs"),
			"   "+styleDim.Render("it and prints its output too, and removing vincent puts it back verbatim."))
	} else {
		// The one thing a preview cannot show, and the one thing somebody
		// would be annoyed to discover afterwards.
		out = append(out,
			"   "+styleWarn.Render("You have no status line now, so Claude Code draws its own default."),
			"   "+styleWarn.Render("That default is not a command and cannot be carried along: when"),
			"   "+styleWarn.Render("vincent has nothing to say, the line will be empty instead."))
	}
	return append(out, "",
		"   "+styleKey.Render("enter")+styleDim.Render(" write it")+
			styleDim.Render("   ")+styleKey.Render("n")+styleDim.Render(" not now (remembered — press i to come back)")+
			styleDim.Render("   ")+styleKey.Render("esc")+styleDim.Render(" close"))
}

// uninstallLines is the reverse. It shows what comes back rather than what
// goes in, because that is the thing being decided.
func (f *statusLineFlow) uninstallLines() []string {
	out := []string{
		"   " + styleDim.Render("vincent is Claude Code's status line, from ") + f.plan.path + styleDim.Render("."),
		"",
	}
	if f.plan.restoreErr != nil {
		return append(out,
			"   "+styleBad.Render("⚠ cannot restore what it wrapped: "+errString(f.plan.restoreErr)),
			"",
			"   "+styleDim.Render("Removing it would delete a status line vincent promised to give"),
			"   "+styleDim.Render("back, so it refuses. Edit the file by hand to settle it."),
			"",
			"   "+styleKey.Render("esc")+styleDim.Render(" close"))
	}
	out = append(out, "   "+styleDim.Render("Removing it restores what was there before, verbatim:"), "")
	out = append(out, previewLines(f.plan.restorePreview())...)
	return append(out, "",
		"   "+styleKey.Render("enter")+styleDim.Render(" remove it")+
			styleDim.Render("   ")+styleKey.Render("esc")+styleDim.Render(" keep it"))
}

func (f *statusLineFlow) reportLines() []string {
	if f.err != nil {
		return []string{
			"   " + styleBad.Render("⚠ "+errString(f.err)),
			"",
			"   " + styleDim.Render("nothing was changed"),
			"",
			"   " + styleKey.Render("any key") + styleDim.Render(" close"),
		}
	}
	return []string{
		"   " + styleOK.Render("✓ ") + f.outcome,
		"",
		"   " + styleDim.Render(f.plan.path),
		"   " + styleDim.Render("Claude Code picks it up on its next render; already-open sessions"),
		"   " + styleDim.Render("may need a new prompt."),
		"",
		"   " + styleKey.Render("any key") + styleDim.Render(" close"),
	}
}

// previewLines indents the JSON fragment as a block. It is printed as it will
// be written, newlines and all: an elided or re-flowed preview is a preview of
// something else.
func previewLines(preview string) []string {
	lines := strings.Split(preview, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, "     "+line)
	}
	return out
}
