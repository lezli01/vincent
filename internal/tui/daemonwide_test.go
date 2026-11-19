package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The daemon dashboard (task 150.7). Pure model tests, one property each (task
// 129 decision 7).

func wideDaemon(t *testing.T, height int) *daemonView {
	t.Helper()
	d := newTestDaemonView([]string{"daemon started"}, nil)
	d.update(tea.WindowSizeMsg{Width: 180, Height: height})
	d.info = testInfo()
	d.info.MaxParallelTasks = 4
	d.info.Slots = apiclient.InfoSlots{Used: 3, Lanes: 1, AwaitingInput: 1}
	out := false
	d.info.Agents = []apiclient.AgentStatus{
		{Name: "claude", Available: true, Version: "2.1.0", Path: "/usr/local/bin/claude", SupportsInput: true},
		{Name: "codex", Available: true, Version: "0.150.1", LoggedIn: &out},
		{Name: "cursor", Error: "cursor-agent is not on PATH"},
	}
	d.infoOK = true
	return d
}

func TestDaemonWideSummarizesTheWork(t *testing.T) {
	d := wideDaemon(t, 44)
	out := d.render(180, 44)
	assertFills(t, out, 180, 44)
	plain := ansi.Strip(out)
	for _, want := range []string{"▶ Work", "3 of 4 slots in use", "2 working", "1 waiting on you", "1 lanes"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the dashboard is missing %q:\n%s", want, plain)
		}
	}
}

// What stops an adapter running is the first thing its card says.
func TestDaemonWideDrawsAdapterCards(t *testing.T) {
	plain := ansi.Strip(wideDaemon(t, 44).render(180, 44))
	for _, want := range []string{"◈ claude", "✓ ready", "2.1.0", "◈ codex", "⚠ not logged in", "no interactive input", "✗ cursor-agent is not on PATH"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the cards are missing %q:\n%s", want, plain)
		}
	}
}

// A short screen keeps the adapters to a line each, so the log has room.
func TestDaemonWideShortScreenKeepsAdapterLines(t *testing.T) {
	d := wideDaemon(t, adapterCardsMinH-1)
	out := d.render(180, adapterCardsMinH-1)
	assertFills(t, out, 180, adapterCardsMinH-1)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "◈ claude") || !strings.Contains(plain, "◈ Adapters") {
		t.Errorf("a short screen drew the cards:\n%s", plain)
	}
}

// The pause state moves into the Work box rather than being said twice.
func TestDaemonWideSaysThePauseOnce(t *testing.T) {
	d := wideDaemon(t, 44)
	d.pauseOK = true
	d.pause = apiclient.GlobalPause{State: apiclient.PausePaused}
	if got := strings.Count(ansi.Strip(d.render(180, 44)), "paused"); got != 1 {
		t.Errorf("the pause is said %d times", got)
	}
}
