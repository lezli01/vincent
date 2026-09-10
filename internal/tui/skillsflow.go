package tui

import (
	"bytes"
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/skill"
)

// The daemon view's published-skill offer (§9.8, task 095), built on task
// 082's status-line pattern rather than on the first-run notice's: a line on
// the view that opens a takeover, a decline that persists in tui.json, and
// nothing that re-asks while it is set. A skill that is not installed costs a
// user help in their own agent session; it is not worth a blocking overlay.
//
// The offer is actionable at all only because vincent runs `skills add` with
// an agent selection: the published command opens an interactive picker, and
// a picker cannot be driven from here.

// skillsInstallTimeout bounds the shell-out. A first `npx skills add`
// downloads the package before it does anything, so this is generous — but it
// is bounded, because a TUI with a child it will wait on forever is a TUI
// that cannot be quit cleanly.
const skillsInstallTimeout = 5 * time.Minute

// skillsInstalledMsg lands one install attempt. output is the CLI's own
// stdout and stderr, kept so a failure can show what it said rather than only
// that it failed.
type skillsInstalledMsg struct {
	output string
	err    error
}

// skillsFlow is the open takeover. Unlike the status-line flow the write here
// is not a local file rewrite but a subprocess that downloads a package, so
// this one has a running state and does its work in a tea.Cmd: blocking the
// event loop on a network install would freeze the whole TUI.
type skillsFlow struct {
	skills []apiclient.DoctorSkill
	// slugs is the `--agent` selection, derived from the adapters this daemon
	// reports. It is fixed when the flow opens so that the command shown is
	// the command run.
	slugs   []string
	names   []string
	running bool
	// outcome and err report a finished attempt; log is what the CLI printed.
	outcome string
	err     error
	log     string
}

func newSkillsFlow(skills []apiclient.DoctorSkill, adapters []string) *skillsFlow {
	return &skillsFlow{
		skills: skills,
		slugs:  skill.Slugs(adapters),
		names:  skill.Missing(skills),
	}
}

// reporting is the second screen: an install has finished, one way or the
// other, and the flow is saying so.
func (f *skillsFlow) reporting() bool { return f.outcome != "" || f.err != nil }

// update answers one key. It returns a command to run and whether the flow is
// finished with the keyboard — two returns rather than the status-line flow's
// one, because this flow's work is asynchronous.
func (f *skillsFlow) update(msg tea.KeyPressMsg, dataDir string) (tea.Cmd, bool) {
	if f.running {
		// Nothing but the running install may act while it is running; a
		// second `npx` over the same store is a race with no upside.
		return nil, false
	}
	if f.reporting() {
		return nil, true
	}
	switch msg.String() {
	case "esc":
		return nil, true
	case "n":
		if len(f.names) == 0 {
			return nil, true
		}
		if err := writeSkillsDecline(dataDir); err != nil {
			f.err = err
			return nil, false
		}
		return nil, true
	case "enter", "y":
		if len(f.names) == 0 {
			return nil, true
		}
		f.running = true
		return f.installCmd(), false
	}
	return nil, false
}

// installCmd runs every missing skill's install, one `npx` per skill, off the
// event loop.
func (f *skillsFlow) installCmd() tea.Cmd {
	names, slugs := append([]string(nil), f.names...), f.slugs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), skillsInstallTimeout)
		defer cancel()
		var buf bytes.Buffer
		for _, name := range names {
			if err := skill.Install(ctx, name, slugs, &buf); err != nil {
				return skillsInstalledMsg{output: buf.String(), err: err}
			}
		}
		return skillsInstalledMsg{output: buf.String()}
	}
}

// applyInstalled lands the attempt.
func (f *skillsFlow) applyInstalled(msg skillsInstalledMsg) {
	f.running = false
	f.log = msg.output
	if msg.err != nil {
		f.err = msg.err
		return
	}
	f.outcome = "installed " + strings.Join(f.names, ", ")
}

func (f *skillsFlow) render(width int) []string {
	_ = width // Hand-wrapped, like every other takeover on this view.
	out := []string{" " + styleTitle.Render("agent skills"), ""}
	switch {
	case f.running:
		return append(out,
			"   "+styleDim.Render("running ")+skill.Command(f.names[0], f.slugs),
			"",
			"   "+styleDim.Render("The first run downloads the package, so this can take a minute."),
			"   "+styleDim.Render("The TUI keeps working; this screen updates when it finishes."))
	case f.reporting():
		return append(out, f.reportLines()...)
	default:
		return append(out, f.offerLines()...)
	}
}

// offerLines is the offer: what is on this machine, then what pressing enter
// runs, spelled in full. The command is shown for the reason the status-line
// flow shows its JSON — this leaves vincent's own directories, and the exact
// thing it does belongs on screen before it happens.
func (f *skillsFlow) offerLines() []string {
	out := []string{
		"   " + styleDim.Render("vincent publishes skills so an agent you talk to directly knows how"),
		"   " + styleDim.Render("to author a vincent workflow. Runs inside vincent do not need them —"),
		"   " + styleDim.Render("the built-in workflows carry the same text in their own prompts."),
		"",
	}
	for _, s := range f.skills {
		out = append(out, "   "+skillRowLine(s))
	}
	if len(f.names) == 0 {
		return append(out, "",
			"   "+styleDim.Render("Nothing to install."),
			"",
			"   "+styleKey.Render("esc")+styleDim.Render(" close"))
	}
	out = append(out, "", "   "+styleDim.Render("Pressing enter runs, once per skill:"), "")
	for _, name := range f.names {
		out = append(out, "     "+skill.Command(name, f.slugs))
	}
	return append(out, "",
		"   "+styleDim.Render("That needs node on PATH, and network on its first run. It installs into"),
		"   "+styleDim.Render("the global skills store and links it into each agent's directory."),
		"",
		"   "+styleKey.Render("enter")+styleDim.Render(" run it")+
			styleDim.Render("   ")+styleKey.Render("n")+styleDim.Render(" not now (remembered — press S to come back)")+
			styleDim.Render("   ")+styleKey.Render("esc")+styleDim.Render(" close"))
}

func (f *skillsFlow) reportLines() []string {
	out := []string{}
	if f.err != nil {
		out = append(out,
			"   "+styleBad.Render("⚠ "+errString(f.err)),
			"")
	} else {
		out = append(out, "   "+styleOK.Render("✓ ")+f.outcome, "")
	}
	if f.log != "" {
		for _, line := range lastLines(strings.Split(strings.TrimRight(f.log, "\n"), "\n"), 8) {
			out = append(out, "     "+styleDim.Render(line))
		}
		out = append(out, "")
	}
	return append(out, "   "+styleKey.Render("any key")+styleDim.Render(" close"))
}

// lastLines is the tail of a slice, so a long npm log does not push the
// outcome off the screen.
func lastLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// skillRowLine renders one skill's state, naming both versions wherever the
// state is about the difference between them.
func skillRowLine(s apiclient.DoctorSkill) string {
	name := s.Name
	var state string
	switch s.State {
	case apiclient.SkillCurrent:
		state = styleOK.Render("current " + s.Installed)
	case apiclient.SkillAbsent:
		state = styleWarn.Render("not installed") + styleDim.Render("  ships "+s.Shipped)
	case apiclient.SkillOlder:
		state = styleWarn.Render("out of date") +
			styleDim.Render("  installed "+s.Installed+", ships "+s.Shipped)
	case apiclient.SkillNewer:
		state = styleWarn.Render("newer than this build") +
			styleDim.Render("  installed "+s.Installed+", ships "+s.Shipped)
	case apiclient.SkillDiffers:
		state = styleWarn.Render("differs") +
			styleDim.Render("  installed "+s.Installed+", ships "+s.Shipped)
	default:
		state = styleBad.Render(s.State) + styleDim.Render("  "+s.Message)
	}
	row := name + "  " + state
	if agents := s.Agents(); len(agents) > 0 {
		row += styleDim.Render("  ·  " + strings.Join(agents, ", "))
	}
	return row
}
