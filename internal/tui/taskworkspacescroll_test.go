package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// blockedCardKeys are the action keys and jump links a blocked check_failed
// task's failure card offers on the Overview.
var blockedCardKeys = []string{"retry", "edit retry", "skip", "repair", "chat", "cancel", "3", "6"}

// cardKeysIn reports which of blockedCardKeys a stripped Overview frame shows,
// in either the full card's rows or the collapsed card's one line.
func cardKeysIn(frame string) map[string]bool {
	seen := map[string]bool{}
	for _, line := range strings.Split(frame, "\n") {
		f := strings.Fields(line)
		for _, k := range blockedCardKeys {
			switch k {
			case "3":
				seen[k] = seen[k] || strings.Contains(line, "3 output of attempt")
			case "6":
				seen[k] = seen[k] || strings.Contains(line, "6 what it was given")
			default:
				seen[k] = seen[k] || (len(f) > 1 && strings.HasPrefix(strings.Join(f[1:], " "), k+" —")) ||
					strings.Contains(line, " "+k+" ·") || strings.HasSuffix(line, " "+k)
			}
		}
	}
	return seen
}

// TestOverviewBlockedActionsReachableAtEveryHeight (#799 W1): at 80 columns
// and every height from 18 to 40, each action key and jump link of a blocked
// task is on the Overview or reachable by scrolling it — pgdown or the wheel.
// renderOverview cuts its lines at the pane height and the Overview scrolls
// nowhere, so at 80×18–20 the card ends at `E edit retry`/`R repair` and
// `s skip`, `c cancel` and the links are gone.
func TestOverviewBlockedActionsReachableAtEveryHeight(t *testing.T) {
	for h := 18; h <= 40; h++ {
		t.Run(fmt.Sprint(h), func(t *testing.T) {
			v := cardView(t, cardTask("check_failed", failedRun(5, 2, "check_failed")))
			v.width, v.height = 80, h
			seen := cardKeysIn(ansi.Strip(v.render(80, h)))
			for range 10 {
				v.updateKey(key("pgdown"))
				for k, ok := range cardKeysIn(ansi.Strip(v.render(80, h))) {
					seen[k] = seen[k] || ok
				}
			}
			for range 40 {
				v.update(tea.MouseWheelMsg{X: 10, Y: 8, Button: tea.MouseWheelDown})
				for k, ok := range cardKeysIn(ansi.Strip(v.render(80, h))) {
					seen[k] = seen[k] || ok
				}
			}
			for _, k := range blockedCardKeys {
				if !seen[k] {
					t.Errorf("at 80×%d %q is neither shown nor reachable by scrolling:\n%s",
						h, k, ansi.Strip(v.render(80, h)))
				}
			}
		})
	}
}

// TestOverviewTallerNeverShowsLess (#799 W1): a taller screen never shows
// fewer of a blocked task's action keys than a shorter one, without
// scrolling. Today the card collapses to one line of keys below
// cardCollapseRows, so a short screen shows every key and the next row up
// shows fewer.
func TestOverviewTallerNeverShowsLess(t *testing.T) {
	prev, prevH := -1, 0
	for h := 10; h <= 40; h++ {
		v := cardView(t, cardTask("check_failed", failedRun(5, 2, "check_failed")))
		v.width, v.height = 80, h
		n := 0
		for _, ok := range cardKeysIn(ansi.Strip(v.render(80, h))) {
			if ok {
				n++
			}
		}
		if n < prev {
			t.Errorf("80×%d shows %d card keys, fewer than the %d at 80×%d", h, n, prev, prevH)
		}
		prev, prevH = n, h
	}
}

// TestAbortedOverviewKeepsItsLinks: an aborted task stacks the outcome card
// under the failure card, and the jump links outrank both cards' lower lines
// at every height, so a taller screen never loses them (review F14).
func TestAbortedOverviewKeepsItsLinks(t *testing.T) {
	for h := cardCollapseRows + 1; h <= 40; h++ {
		v := outcomeView(t, outcomeTask(stateAborted))
		v.width, v.height = 100, h
		if got := ansi.Strip(v.render(100, h)); !strings.Contains(got, "3 output of attempt") {
			t.Errorf("100×%d hides the jump links:\n%s", h, got)
		}
	}
}

// TestPullTabWindowsChecksAroundCursor (#799 W2): with 30 checks at 120×40,
// moving the cursor down through every check keeps the cursor's row and the
// hint line on screen. renderPullTab cuts its lines at the pane height
// (`lines[:max(height,1)]`), so the rows stop part-way, `▸` sits on an
// invisible row, and the hint is never drawn.
func TestPullTabWindowsChecksAroundCursor(t *testing.T) {
	v := pullTabFixture(t)
	checks := checkRollupFixture()
	checks.Runs = nil
	for i := range 30 {
		checks.Runs = append(checks.Runs, apiclient.GitHubCheckRun{
			Name: fmt.Sprintf("check-%02d", i), State: "success",
			URL: fmt.Sprintf("https://github.com/octo/api/actions/runs/77/job/%d", i), RunID: 77,
		})
	}
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: checks})
	hint := ansi.Strip(v.pullHintLine())
	for i := range 40 {
		frame := ansi.Strip(v.render(120, 40))
		want := fmt.Sprintf("check-%02d", min(i, 29))
		cursor := false
		for _, line := range strings.Split(frame, "\n") {
			cursor = cursor || (strings.Contains(line, "▸") && strings.Contains(line, want))
		}
		if !cursor {
			t.Fatalf("after %d presses the cursor row %s is not on screen:\n%s", i, want, frame)
		}
		if !strings.Contains(frame, strings.TrimSpace(hint)) {
			t.Fatalf("after %d presses the hint line is not on screen:\n%s", i, frame)
		}
		v.updateKey(registryKey(t, "down"))
	}
}

// TestDiffEmptyStateNamesNoDKey (#799 W9): the never-fetched Diff tab does not
// tell the human to press `d`, which on the Diff tab switches to Output.
func TestDiffEmptyStateNamesNoDKey(t *testing.T) {
	p := newDiffPane()
	text, ok := p.emptyState()
	if !ok {
		t.Fatal("a never-fetched diff pane has no empty state")
	}
	if strings.Contains(text, "press d") {
		t.Errorf("the empty state says %q, but d on the Diff tab switches to Output", text)
	}
}
