package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/nerdfont"
)

// The Nerd Font flow (task 151). Pure model tests, one property each (task 129
// decision 7): nothing here downloads, and no real font directory is read.

func fontFixture(t *testing.T, env map[string]string) *fontFlow {
	t.Helper()
	rel := nerdfont.Release{URL: "https://example.invalid/font.zip", Size: 1000, Faces: []nerdfont.Face{{Name: "A.ttf", Size: 1, SHA256: "00"}}}
	return newFontFlow(rel, filepath.Join(t.TempDir(), "fonts"), nil, func(k string) string { return env[k] })
}

// openFontFlow is the daemon view with the flow open on its offer.
func openFontFlow(t *testing.T) *daemonView {
	t.Helper()
	d := newTestDaemonView(nil, nil)
	d.updateKey(registryKey(t, "U"))
	if d.fonts == nil {
		t.Fatal("U did not open the font flow")
	}
	return d
}

func fontScreen(f *fontFlow) string { return ansi.Strip(strings.Join(f.render(100), "\n")) }

// What is downloaded, and where it goes, is on screen before anything happens.
func TestFontFlowOffersBeforeInstalling(t *testing.T) {
	f := fontFixture(t, nil)
	if f.stage != fontOffer {
		t.Fatalf("the flow opened on stage %d", f.stage)
	}
	for _, want := range []string{"example.invalid/font.zip", "fonts", "checksum"} {
		if !strings.Contains(fontScreen(f), want) {
			t.Errorf("the offer does not say %q:\n%s", want, fontScreen(f))
		}
	}
	if cmd, done := f.update(keyPress("esc"), nil); cmd != nil || !done {
		t.Fatal("esc did not close the offer without acting")
	}
}

func TestFontFlowInstallsOnlyOnEnter(t *testing.T) {
	f := fontFixture(t, nil)
	if cmd, _ := f.update(keyPress("x"), nil); cmd != nil {
		t.Fatal("an unrelated key started the install")
	}
	if cmd, _ := f.update(keyPress("enter"), nil); cmd == nil || f.stage != fontWorking {
		t.Fatal("enter did not start the install")
	}
}

// Installing does not turn the icons on: someone has to see them first.
func TestFontFlowAsksBeforeTurningTheIconsOn(t *testing.T) {
	f := fontFixture(t, nil)
	f.applyWork(fontWorkDoneMsg{}, offlineClient())
	if f.stage != fontPreview {
		t.Fatalf("after installing the flow is on stage %d", f.stage)
	}
	if !strings.Contains(fontScreen(f), "Do those look like icons?") {
		t.Errorf("the preview does not ask:\n%s", fontScreen(f))
	}
	if cmd, done := f.update(keyPress("n"), offlineClient()); cmd != nil || !done {
		t.Fatal("n changed something")
	}
}

func TestFontFlowTurnsTheIconsOnWithYes(t *testing.T) {
	f := fontFixture(t, nil)
	f.applyWork(fontWorkDoneMsg{}, offlineClient())
	if cmd, _ := f.update(keyPress("y"), offlineClient()); cmd == nil {
		t.Fatal("y wrote nothing")
	}
	f.applySaved(configSavedMsg{path: "tui.icons", cfg: apiclient.Config{}})
	if !strings.Contains(fontScreen(f), "Nerd Font icons are on.") {
		t.Errorf("the report does not say so:\n%s", fontScreen(f))
	}
}

func TestFontFlowReportsAFailedInstall(t *testing.T) {
	f := fontFixture(t, nil)
	f.applyWork(fontWorkDoneMsg{err: errors.New("checksum mismatch")}, nil)
	if !strings.Contains(fontScreen(f), "checksum mismatch") {
		t.Errorf("the failure is not reported:\n%s", fontScreen(f))
	}
}

// Over SSH a font installed here never reaches the screen.
func TestFontFlowDoesNotOfferAnInstallOverSSH(t *testing.T) {
	f := fontFixture(t, map[string]string{"SSH_CONNECTION": "1 2 3 4"})
	if f.stage != fontPreview || !strings.Contains(fontScreen(f), "SSH") {
		t.Fatalf("stage %d:\n%s", f.stage, fontScreen(f))
	}
}

func TestFontFlowSkipsTheInstallOnATerminalWithTheIcons(t *testing.T) {
	f := fontFixture(t, map[string]string{"TERM_PROGRAM": "ghostty"})
	if f.stage != fontPreview || !strings.Contains(fontScreen(f), "Ghostty already includes") {
		t.Fatalf("stage %d:\n%s", f.stage, fontScreen(f))
	}
}

// The samples are real icons even while tui.icons is unicode.
func TestFontSamplesAreNerdGlyphs(t *testing.T) {
	setIconSet("unicode")
	if !strings.Contains(fontSampleRow(), "\uf41b") {
		t.Errorf("the samples are not Nerd Font glyphs: %q", fontSampleRow())
	}
}

func TestDaemonOffersTheFontUntilTheIconsAreOn(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	d.configOK = true
	if _, ok := d.fontLine(); !ok {
		t.Fatal("no offer with Unicode icons")
	}
	d.config.TUI.Icons = iconSetNerd
	if _, ok := d.fontLine(); ok {
		t.Fatal("the offer stayed after the icons were turned on")
	}
}
