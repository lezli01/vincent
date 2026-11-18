package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/nerdfont"
)

// The Nerd Font flow (task 151): the daemon view's `U`. It says what it will
// download before it does, and turns the icons on only once someone has looked
// at them, because a program cannot find out which font its terminal draws with.

// fontInstallTimeout bounds the download, so the flow can never wait forever.
const fontInstallTimeout = 15 * time.Minute

// fontStage is the screen the flow is on.
type fontStage int

const (
	// fontOffer says what installing does.
	fontOffer fontStage = iota
	// fontWorking waits on an install, a removal or a config write.
	fontWorking
	// fontPreview shows the icons and asks whether they look right.
	fontPreview
	// fontReport says how it ended.
	fontReport
)

// fontWorkDoneMsg lands one install or removal.
type fontWorkDoneMsg struct {
	removed bool
	err     error
}

// fontFlow is the open flow.
type fontFlow struct {
	release nerdfont.Release
	dir     string
	state   nerdfont.State
	term    nerdfont.TerminalInfo
	stage   fontStage
	// blocked is why there is nothing to install here.
	blocked string
	// working says what the flow is waiting on.
	working string
	// removed is a removal whose config write is under way.
	removed bool
	outcome string
	err     string
}

// newFontFlow opens on the offer when installing would help, and on the icons
// when it would not.
func newFontFlow(rel nerdfont.Release, dir string, dirErr error, env func(string) string) *fontFlow {
	f := &fontFlow{release: rel, dir: dir, term: nerdfont.Terminal(env), stage: fontPreview}
	if dirErr != nil {
		f.blocked = errString(dirErr) + "."
		return f
	}
	state, err := nerdfont.Detect(rel, dir)
	f.state = state
	switch {
	case err != nil:
		f.blocked = errString(err)
	case f.term.Remote:
		f.blocked = "You are connected over SSH. A font installed here would not reach your screen, so install it on the machine in front of you."
	case f.term.Bundled:
		f.blocked = f.term.Name + " already includes these icons, so there is nothing to install."
	case state == nerdfont.StateConflict:
		f.blocked = dir + " already holds a different font file with the same name, so vincent will not touch it."
	case state != nerdfont.StateInstalled:
		f.stage = fontOffer
	}
	return f
}

// update answers one key, and reports when the flow has finished.
func (f *fontFlow) update(msg tea.KeyPressMsg, client *apiclient.Client) (tea.Cmd, bool) {
	key := msg.String()
	switch f.stage {
	case fontWorking:
		return nil, false
	case fontReport:
		return nil, true
	case fontOffer:
		switch key {
		case "enter", "y":
			f.stage, f.working = fontWorking, "Downloading and checking the font…"
			return f.installCmd(), false
		case "n", "esc":
			return nil, true
		}
	case fontPreview:
		switch key {
		case "enter", "y":
			return f.setIcons(client, iconSetNerd), false
		case "u":
			if f.state == nerdfont.StateInstalled {
				f.stage, f.working = fontWorking, "Removing the font…"
				return f.removeCmd(), false
			}
		case "n", "esc":
			return nil, true
		}
	}
	return nil, false
}

func (f *fontFlow) installCmd() tea.Cmd {
	rel, dir := f.release, f.dir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fontInstallTimeout)
		defer cancel()
		return fontWorkDoneMsg{err: nerdfont.Install(ctx, rel, dir)}
	}
}

func (f *fontFlow) removeCmd() tea.Cmd {
	rel, dir := f.release, f.dir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		_, err := nerdfont.Uninstall(ctx, rel, dir)
		return fontWorkDoneMsg{removed: true, err: err}
	}
}

// setIcons writes tui.icons through the daemon, which owns the file.
func (f *fontFlow) setIcons(client *apiclient.Client, set string) tea.Cmd {
	if client == nil {
		f.stage, f.err = fontReport, "the daemon is not reachable, so tui.icons was not changed"
		return nil
	}
	f.stage, f.working = fontWorking, "Saving tui.icons…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.PatchConfig(ctx, apiclient.ConfigPatch{TUI: &apiclient.ConfigTUIPatch{Icons: &set}})
		return configSavedMsg{path: "tui.icons", cfg: cfg, err: err}
	}
}

// applyWork lands an install or a removal.
func (f *fontFlow) applyWork(msg fontWorkDoneMsg, client *apiclient.Client) tea.Cmd {
	if msg.err != nil {
		f.stage, f.err = fontReport, errString(msg.err)
		return nil
	}
	if msg.removed {
		f.removed = true
		return f.setIcons(client, "unicode")
	}
	f.state, f.stage = nerdfont.StateInstalled, fontPreview
	return nil
}

// applySaved lands the config write.
func (f *fontFlow) applySaved(msg configSavedMsg) {
	f.stage = fontReport
	switch {
	case msg.err != nil:
		f.err = "tui.icons was not changed: " + errString(msg.err)
	case f.removed:
		f.outcome = "The font is removed and vincent is back to Unicode icons."
	default:
		f.outcome = "Nerd Font icons are on."
	}
}

// fontSamples are real icons, whatever tui.icons says: this screen is how
// someone finds out whether they can see them.
var fontSamples = []struct{ glyph, label string }{
	{"▦", "issue"},
	{"▶", "task"},
	{"◌", "chat"},
	{"◆", "project"},
	{"⟡", "workflow"},
	{"⎇", "branch"},
	{"⇅", "pull request"},
	{"✓", "done"},
}

func fontSampleRow() string {
	parts := make([]string, 0, len(fontSamples))
	for _, s := range fontSamples {
		parts = append(parts, toneCyan.Render(nerdGlyphs.Replace(s.glyph))+" "+s.label)
	}
	return strings.Join(parts, "   ")
}

func (f *fontFlow) render(width int) []string {
	para := func(style lipgloss.Style, text string) []string {
		return styled(wrapText(text, max(width-3, 20), wrapOpts{prefix: "   ", indent: 3}), style)
	}
	out := []string{" " + styleTitle.Render("nerd font icons"), ""}
	switch f.stage {
	case fontWorking:
		return append(out, para(styleDim, f.working)...)
	case fontReport:
		if f.err != "" {
			out = append(out, para(styleBad, "⚠ "+f.err)...)
		} else {
			out = append(out, para(styleOK, f.outcome)...)
		}
		return append(out, "", "   "+styleKey.Render("any key")+styleDim.Render(" close"))
	case fontOffer:
		return append(out, f.offerLines(para)...)
	}
	return append(out, f.previewLines(para)...)
}

func (f *fontFlow) offerLines(para func(lipgloss.Style, string) []string) []string {
	out := para(styleDim, "vincent can draw its icons with "+nerdfont.Family+" instead of plain Unicode.")
	out = append(out, "")
	out = append(out, para(styleDim, "Pressing enter downloads "+humanBytes(f.release.Size)+" from the Nerd Fonts project:")...)
	out = append(out, para(lipgloss.NewStyle(), f.release.URL)...)
	out = append(out, "")
	out = append(out, para(styleDim, "It checks the download against a checksum built into vincent, then installs four font files for your user only, into:")...)
	out = append(out, para(lipgloss.NewStyle(), f.dir)...)
	return append(out, "",
		"   "+styleKey.Render("enter")+styleDim.Render(" install")+
			styleDim.Render("   ")+styleKey.Render("esc")+styleDim.Render(" close"))
}

func (f *fontFlow) previewLines(para func(lipgloss.Style, string) []string) []string {
	var out []string
	if f.blocked != "" {
		out = append(para(styleWarn, f.blocked), "")
	}
	out = append(out, "   "+fontSampleRow(), "")
	out = append(out, para(styleTitle, "Do those look like icons?")...)
	if !f.term.Bundled {
		out = append(out, para(styleDim, "If you see boxes or question marks, choose "+nerdfont.Family+" in "+f.term.Steps+", then press U here again.")...)
	}
	keys := "   " + styleKey.Render("y") + styleDim.Render(" turn them on") +
		styleDim.Render("   ") + styleKey.Render("n") + styleDim.Render(" keep Unicode")
	if f.state == nerdfont.StateInstalled {
		keys += styleDim.Render("   ") + styleKey.Render("u") + styleDim.Render(" remove the font")
	}
	return append(out, "", keys)
}
