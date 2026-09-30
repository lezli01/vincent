package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 129.11: quiet and compact show the last commandTailLines lines of each
// command run's output, below a count of the rest; normal and verbose show all
// of it, exactly as before.

func commandRun(cmd string, n int, phase string) []apiclient.TranscriptRecord {
	recs := []apiclient.TranscriptRecord{
		{Type: "vincent.command_started", Raw: json.RawMessage(fmt.Sprintf(`{"command":%q}`, cmd))},
	}
	for i := 1; i <= n; i++ {
		recs = append(recs, apiclient.TranscriptRecord{
			Type: "command.output", Text: fmt.Sprintf("%s line %d", cmd, i), Phase: phase,
		})
	}
	return recs
}

func renderTail(recs []apiclient.TranscriptRecord, level outputLevel) []string {
	return plainLines(outputLines(recs, level, 80, lineOpts{expandKey: "v"}))
}

func TestCommandOutputIsTailedAtQuietAndCompact(t *testing.T) {
	recs := commandRun("make", 100, "")
	want := []string{"$ make", "  … 80 earlier line(s) (v)"}
	for i := 81; i <= 100; i++ {
		want = append(want, fmt.Sprintf("  make line %d", i))
	}
	for _, level := range []outputLevel{levelQuiet, levelCompact} {
		if got := renderTail(recs, level); !slices.Equal(got, want) {
			t.Errorf("%s:\n got: %q\nwant: %q", level, got, want)
		}
	}
}

func TestShortCommandOutputRendersWholeWithNoCount(t *testing.T) {
	recs := commandRun("make", commandTailLines, "")
	for _, level := range []outputLevel{levelQuiet, levelCompact} {
		got := renderTail(recs, level)
		if len(got) != commandTailLines+1 {
			t.Errorf("%s: %d lines, want %d: %q", level, len(got), commandTailLines+1, got)
		}
		if strings.Contains(strings.Join(got, "\n"), "earlier line(s)") {
			t.Errorf("%s stated a cut that did not happen: %q", level, got)
		}
	}
}

func TestLoudLevelsShowTheWholeCommandOutput(t *testing.T) {
	recs := commandRun("make", 100, "")
	for _, level := range []outputLevel{levelNormal, levelVerbose} {
		got := renderTail(recs, level)
		if len(got) != 101 || got[1] != "  make line 1" {
			t.Errorf("%s cut a command's output: %d lines, first %q", level, len(got), got[1])
		}
	}
}

func TestAFailingCommandsLastLineIsVisibleAtQuiet(t *testing.T) {
	recs := commandRun("go test ./...", 60, "")
	recs = append(recs, apiclient.TranscriptRecord{
		Type: "command.output", Text: "FAIL: TestSomething", Stream: "stderr",
	})
	got := renderTail(recs, levelQuiet)
	if got[len(got)-1] != "  FAIL: TestSomething" {
		t.Errorf("the failing line is not the last line: %q", got[len(got)-1])
	}
	if !slices.Contains(got, "  … 41 earlier line(s) (v)") {
		t.Errorf("stdout and stderr were not counted together: %q", got)
	}
}

func TestEachCommandRunHasItsOwnTail(t *testing.T) {
	recs := append(commandRun("build", 30, ""), commandRun("lint", 25, "check")...)
	got := renderTail(recs, levelCompact)
	var counts []string
	for _, l := range got {
		if strings.Contains(l, "earlier line(s)") {
			counts = append(counts, l)
		}
	}
	want := []string{"  … 10 earlier line(s) (v)", "  … 5 earlier line(s) (v)"}
	if !slices.Equal(counts, want) {
		t.Errorf("counts %q, want %q", counts, want)
	}
	for _, l := range []string{"$ build", "  build line 11", "  build line 30", "$ lint", "check ▏lint line 6", "check ▏lint line 25"} {
		if !slices.Contains(got, l) {
			t.Errorf("missing %q in %q", l, got)
		}
	}
	for _, l := range []string{"  build line 10", "check ▏lint line 5"} {
		if slices.Contains(got, l) {
			t.Errorf("%q should have been cut: %q", l, got)
		}
	}
}

func TestAgentCommandOutputStaysVerboseOnly(t *testing.T) {
	recs := []apiclient.TranscriptRecord{{Type: "agent.command_output", Output: "hidden"}}
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal} {
		if got := renderTail(recs, level); len(got) != 0 {
			t.Errorf("%s drew agent.command_output: %q", level, got)
		}
	}
}

func TestParseOutputLevelRoundTrips(t *testing.T) {
	for l := levelQuiet; l <= levelVerbose; l++ {
		if got, ok := parseOutputLevel(l.String()); !ok || got != l {
			t.Errorf("parseOutputLevel(%q) = %s, %v", l.String(), got, ok)
		}
	}
	if _, ok := parseOutputLevel("loud"); ok {
		t.Error("parseOutputLevel accepted loud")
	}
}

// TestAdoptAppliesOnStartAndOnChange is task 129.11 decision 2: the first
// configured value applies, a refetch with the same value leaves a `v` press
// standing, and a changed value applies.
func TestAdoptAppliesOnStartAndOnChange(t *testing.T) {
	h := newLevelHolder()
	h.adopt("")
	if h.get() != levelNormal {
		t.Fatalf("an absent key moved the level to %s", h.get())
	}
	h.adopt("quiet")
	if h.get() != levelQuiet {
		t.Fatalf("first fetch: %s, want quiet", h.get())
	}
	h.cycle()
	h.adopt("quiet")
	if h.get() != levelCompact {
		t.Fatalf("a refetch undid the v press: %s", h.get())
	}
	h.adopt("verbose")
	if h.get() != levelVerbose {
		t.Fatalf("a changed value did not apply: %s", h.get())
	}
	h.adopt("loud")
	if h.get() != levelVerbose {
		t.Fatalf("an unknown value moved the level: %s", h.get())
	}
}

// TestConfiguredLevelReachesBothWorkspaces is task 129.11 through the root:
// the configured level opens both panes, a reconnect's refetch of the same
// value leaves a `v` press standing, a changed value applies, and a built
// pane rebuilds without being marked dirty.
func TestConfiguredLevelReachesBothWorkspaces(t *testing.T) {
	links, level := newHyperlinkHolder(), newLevelHolder()
	m := &root{views: newViews(t.Context(), links, level), links: links, level: level}
	chat := m.views[viewChat].(*chatView)
	task := m.views[viewTask].(*taskView)

	m.Update(boardConfigMsg{outputLevel: "quiet"})
	if chat.level.get() != levelQuiet || task.detail.level.get() != levelQuiet {
		t.Fatalf("configured quiet opened the panes at %s (chat) and %s (task)",
			chat.level.get(), task.detail.level.get())
	}
	task.detail.cycleLevel()
	m.Update(boardConfigMsg{outputLevel: "quiet"})
	if level.get() != levelCompact {
		t.Fatalf("a refetch with the same value undid the v press: %s", level.get())
	}
	m.Update(daemonConfigMsg{err: errTest})
	m.Update(configSavedMsg{cfg: apiclient.Config{TUI: apiclient.ConfigTUI{
		Output: apiclient.ConfigOutput{Level: "verbose"},
	}}})
	if chat.level.get() != levelVerbose {
		t.Fatalf("a changed value did not apply: %s", chat.level.get())
	}

	d := newDetail(testCtx(t), level, newRawHolder(), links)
	d.width, d.height = 80, 40
	d.selectedRun = 1
	d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: commandRun("make", 30, "")})
	if strings.Contains(d.renderOutputPane(40), "earlier line(s)") {
		t.Fatal("verbose cut a command's output")
	}
	m.Update(daemonConfigMsg{config: apiclient.Config{TUI: apiclient.ConfigTUI{
		Output: apiclient.ConfigOutput{Level: "compact"},
	}}})
	if !strings.Contains(d.renderOutputPane(40), "10 earlier line(s)") {
		t.Fatal("a built pane did not rebuild at the newly configured level")
	}
}
