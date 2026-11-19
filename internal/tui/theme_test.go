package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// useNerdIcons turns the Nerd Font glyphs on for one test.
func useNerdIcons(t *testing.T) {
	t.Helper()
	setIconSet(iconSetNerd)
	t.Cleanup(func() { setIconSet("unicode") })
}

// Unicode is what a terminal gets unless someone asks for more, an older
// daemon's empty answer included.
func TestIconsDefaultToUnicode(t *testing.T) {
	for _, set := range []string{"", "unicode", "something-newer"} {
		setIconSet(set)
		if got := ic("▦ Issues"); got != "▦ Issues" {
			t.Errorf("set %q drew %q", set, got)
		}
	}
}

func TestNerdIconsReplaceTheGlyphs(t *testing.T) {
	useNerdIcons(t)
	for in, want := range map[string]string{
		"▦ Issues":  " Issues",
		"◆ bug":     " bug",
		"● chore":   " chore",
		"✦ feature": " feature",
		"✓ done":    " done",
	} {
		if got := ic(in); got != want {
			t.Errorf("ic(%q) = %q, want %q", in, got, want)
		}
	}
}

// A Nerd Font glyph takes one cell, so no layout moves when the set changes.
func TestNerdIconsKeepTheLayout(t *testing.T) {
	useNerdIcons(t)
	v := boardFixture()
	lines := strings.Split(v.render(180, 40), "\n")
	if len(lines) != 40 {
		t.Fatalf("drew %d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > 180 {
			t.Errorf("line %d is %d wide", i, got)
		}
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(out, "○ Open") || !strings.Contains(out, " Open") {
		t.Errorf("the board kept its Unicode icons:\n%s", out)
	}
}

// Someone's title is theirs: an icon set never rewrites what they typed.
func TestNerdIconsLeaveTitlesAlone(t *testing.T) {
	useNerdIcons(t)
	v := boardFixture()
	v.issues = []apiclient.Issue{{ID: 1, Title: "ship ✓ and ▶ later", State: "open", Lane: "open"}}
	v.clampCursor()
	if out := ansi.Strip(v.render(180, 40)); !strings.Contains(out, "ship ✓ and ▶ later") {
		t.Errorf("the title was rewritten:\n%s", out)
	}
}

// The root adopts the setting from a config answer, and a failed one says
// nothing about it.
func TestRootAppliesTheIconSet(t *testing.T) {
	t.Cleanup(func() { setIconSet("unicode") })
	m := connectedRoot(t)
	m.Update(boardConfigMsg{icons: iconSetNerd, tabs: []string{"issues", "tasks"}})
	if !nerdIcons.Load() {
		t.Fatal("the config answer did not turn the Nerd Font icons on")
	}
	m.Update(boardConfigMsg{err: errString2("boom")})
	if !nerdIcons.Load() {
		t.Fatal("a failed config answer changed the icons")
	}
}

type errString2 string

func (e errString2) Error() string { return string(e) }

// The step pips speak the same icon set as the rest of the card.
func TestNerdIconsReachTheStepPips(t *testing.T) {
	useNerdIcons(t)
	task := apiclient.Task{State: stateRunning, StepTotal: 3, CurrentStep: 1}
	if got, want := boardStepPips(task), ""; got != want {
		t.Errorf("pips = %q, want %q", got, want)
	}
}
