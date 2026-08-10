package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTestDaemonView is a view with a deterministic clock and no filesystem
// underneath it; each test supplies the tail it wants.
func newTestDaemonView(lines []string, tailErr error) *daemonView {
	d := newDaemonView()
	d.now = func() time.Time { return testNow }
	d.dataDir = "/data"
	d.connected = true
	d.tail = func(string, int) ([]string, error) { return lines, tailErr }
	return d
}

func testInfo() apiclient.Info {
	return apiclient.Info{
		Version:          "0.4.0",
		Commit:           "abcdef0123456789",
		Built:            "2026-08-01",
		PID:              4242,
		StartedAt:        testNow.Add(-90 * time.Minute),
		Listen:           "127.0.0.1:51234",
		MaxParallelTasks: 3,
		Agents: []apiclient.AgentStatus{
			{
				Name: "claude", Available: true, Path: "/usr/bin/claude",
				Version: "2.1.0", SupportsInput: true,
			},
			{Name: "codex", Available: false, Error: "not found in PATH"},
		},
	}
}

func testConfig() apiclient.Config {
	return apiclient.Config{
		Listen:           "127.0.0.1:0",
		MaxParallelTasks: 3,
		Defaults: apiclient.ConfigDefaults{
			AgentTimeout: "1h0m0s", CommandTimeout: "15m0s", InputTimeout: "24h0m0s",
		},
		TranscriptRetentionDays: 90,
		LogLevel:                "info",
		Agents: map[string]apiclient.AgentPath{
			"claude": {Path: "/usr/bin/claude"},
			"codex":  {Path: ""},
		},
	}
}

func loadedDaemon(d *daemonView) {
	d.update(daemonInfoMsg{info: testInfo()})
	d.update(daemonConfigMsg{config: testConfig()})
	d.update(daemonLogMsg{lines: []string{"level=INFO msg=started", "level=INFO msg=ready"}})
}

func renderDaemon(d *daemonView) string { return d.render(120, 40) }

// State 1: all three sources fresh. Every field the view promises has to be
// on screen, including the paths someone reads this view to find.
func TestDaemonViewRendersIdentityConfigAdaptersAndLog(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	loadedDaemon(d)
	out := renderDaemon(d)

	for _, want := range []string{
		"0.4.0", "abcdef012345", "2026-08-01", "4242", "127.0.0.1:51234",
		"/data", "/data",
		"1h0m0s", "15m0s", "24h0m0s", "90 days", "info",
		"claude", "/usr/bin/claude", "codex", "not found in PATH",
		"level=INFO msg=ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the view does not show %q", want)
		}
	}
	// Uptime is derived from started_at, not from a fetched second count.
	if !strings.Contains(out, "1h 30m") {
		t.Errorf("uptime is not rendered from started_at:\n%s", out)
	}
}

// States 2 and 3: each daemon-backed block degrades alone. A config fetch
// that failed must not take the identity down with it.
func TestDaemonViewFailuresAreIsolatedPerSource(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	loadedDaemon(d)
	d.update(daemonConfigMsg{err: errors.New("config boom")})
	out := renderDaemon(d)

	if !strings.Contains(out, "config boom") {
		t.Error("the config failure is not reported")
	}
	if !strings.Contains(out, "0.4.0") {
		t.Error("the config failure blanked the identity block")
	}
	if !strings.Contains(out, "info") || !strings.Contains(out, "90 days") {
		t.Error("the config block did not keep its last-good values")
	}
	if !strings.Contains(out, "showing") {
		t.Error("stale config is not marked as stale")
	}
	if !strings.Contains(out, "level=INFO msg=ready") {
		t.Error("the config failure reached the log pane")
	}
}

// State 4: activated, nothing back yet. A blank frame reads as a broken
// daemon; "loading" reads as a slow one.
func TestDaemonViewSaysLoadingBeforeTheFirstFetch(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	out := renderDaemon(d)
	if strings.Count(out, "loading…") < 2 {
		t.Errorf("the un-fetched blocks do not say loading:\n%s", out)
	}
	if !strings.Contains(out, "reading the log…") {
		t.Error("the un-read log pane does not say so")
	}
}

// States 5 and 6: an unreadable log and an empty one are different facts,
// and only one of them is a problem.
func TestDaemonViewSeparatesAnUnreadableLogFromAnEmptyOne(t *testing.T) {
	unreadable := newTestDaemonView(nil, errors.New("permission denied"))
	unreadable.update(daemonLogMsg{err: errors.New("permission denied")})
	if out := renderDaemon(unreadable); !strings.Contains(out, "no daemon log: permission denied") {
		t.Errorf("an unreadable log is not reported:\n%s", out)
	}

	empty := newTestDaemonView(nil, nil)
	empty.update(daemonLogMsg{})
	out := renderDaemon(empty)
	if !strings.Contains(out, "the daemon log is empty") {
		t.Errorf("an empty log is not distinguished:\n%s", out)
	}
	if strings.Contains(out, "no daemon log") {
		t.Error("an empty log rendered as an unreadable one")
	}
}

// State 7: a catalog with nothing in it.
func TestDaemonViewNamesAnEmptyAdapterList(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	info := testInfo()
	info.Agents = nil
	d.update(daemonInfoMsg{info: info})
	if out := renderDaemon(d); !strings.Contains(out, "no adapters configured") {
		t.Errorf("an empty catalog is not named:\n%s", out)
	}
}

// State 8: the daemon is gone. The log still renders — that is the whole
// reason this view is reachable then — and the daemon-supplied blocks say
// they are stale rather than presenting dead figures as current.
func TestDaemonViewMarksTheBlocksStaleWhileDisconnected(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	loadedDaemon(d)
	d.setConnected(false)
	out := renderDaemon(d)

	if !strings.Contains(out, "stale") || !strings.Contains(out, "not reachable") {
		t.Errorf("the blocks do not say the daemon is gone:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO msg=ready") {
		t.Error("the log pane went away with the daemon")
	}
	if !strings.Contains(out, "/data") {
		t.Error("the paths went away with the daemon")
	}
}

// Nothing fetched and no daemon: the blocks must not say "loading" forever
// about a daemon that is not coming.
func TestDaemonViewSaysUnavailableWhenItNeverReachedTheDaemon(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	d.setConnected(false)
	out := renderDaemon(d)
	if !strings.Contains(out, "unavailable") {
		t.Errorf("a never-connected view does not say unavailable:\n%s", out)
	}
	if strings.Contains(out, "loading…") {
		t.Error("a never-connected view claims it is loading")
	}
}

// flatten executes a command and any batched children, collecting what they
// produced. Tick commands are not run: they would sleep.
func flatten(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, flatten(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// R re-reads the log even with no daemon to ask, which is what makes it
// useful on the disconnected screen.
func TestDaemonViewRefreshKeyRereadsTheLog(t *testing.T) {
	reads := 0
	d := newTestDaemonView([]string{"fresh"}, nil)
	d.tail = func(string, int) ([]string, error) {
		reads++
		return []string{"fresh"}, nil
	}

	_, cmd := d.updateKey(tea.KeyPressMsg{Code: 'R', Text: "R"})
	msgs := flatten(cmd)
	if reads != 1 {
		t.Fatalf("tail read %d times, want 1", reads)
	}
	if len(msgs) != 1 {
		t.Fatalf("R produced %d messages, want the log read alone", len(msgs))
	}
	if _, ok := msgs[0].(daemonLogMsg); !ok {
		t.Fatalf("R produced %T, want a daemonLogMsg", msgs[0])
	}
}

// The log read needs no client. That is the point of reading it off disk.
func TestDaemonViewLogReadNeedsNoClient(t *testing.T) {
	d := newTestDaemonView([]string{"still here"}, nil)
	d.setConnected(false)

	msgs := flatten(d.logCmd())
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want the log read", len(msgs))
	}
	got, ok := msgs[0].(daemonLogMsg)
	if !ok || got.err != nil || len(got.lines) != 1 {
		t.Fatalf("log read = %+v, want the tail with no daemon involved", msgs[0])
	}
}

// The ticker exists only while the view is on screen: off-screen it stops by
// not re-arming, because a tea.Tick cannot be cancelled.
func TestDaemonViewTickerStopsWhenTheViewGoesOffScreen(t *testing.T) {
	d := newTestDaemonView([]string{"a"}, nil)
	d.update(viewActivatedMsg{id: viewDaemon})
	if !d.visible {
		t.Fatal("activation did not mark the view visible")
	}
	if _, cmd := d.update(daemonTickMsg{}); cmd == nil {
		t.Fatal("a visible view did not re-arm the ticker")
	}
	d.update(viewDeactivatedMsg{id: viewDaemon})
	if _, cmd := d.update(daemonTickMsg{}); cmd != nil {
		t.Error("an off-screen view kept ticking")
	}
}

// Another view's activation is not this view's.
func TestDaemonViewIgnoresAnotherViewsActivation(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	if _, cmd := d.update(viewActivatedMsg{id: viewHome}); cmd != nil {
		t.Error("the home screen's activation started the daemon view")
	}
	if d.visible {
		t.Error("the home screen's activation marked the daemon view visible")
	}
}

// Follow is a property of the pane, exactly as in the detail view: scrolling
// away drops it and G re-arms it.
func TestDaemonViewFollowDropsOnScrollAndRearmsOnG(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line"
	}
	d.update(daemonLogMsg{lines: lines})
	renderDaemon(d)

	d.updateKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if d.following {
		t.Error("scrolling up did not drop follow")
	}
	d.updateKey(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !d.following {
		t.Error("G did not re-arm follow")
	}
	d.updateKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	d.updateKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !d.following {
		t.Error("f did not re-arm follow")
	}
}

// This view has no text entry, so the global single-key bindings work
// throughout it.
func TestDaemonViewNeverCapturesInput(t *testing.T) {
	if newDaemonView().capturesInput() {
		t.Error("the daemon view captures input")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{90 * time.Second, "1m"},
		{95 * time.Minute, "1h 35m"},
		{50 * time.Hour, "2d 2h 0m"},
	}
	for _, c := range cases {
		if got := formatUptime(c.in); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A configured adapter with no pinned path is resolved from PATH, which is
// a different statement from an empty setting.
func TestDaemonViewNamesAnUnpinnedAdapterPath(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	d.update(daemonConfigMsg{config: testConfig()})
	if out := renderDaemon(d); !strings.Contains(out, "resolved from PATH") {
		t.Errorf("an unpinned adapter path is not explained:\n%s", out)
	}
}
