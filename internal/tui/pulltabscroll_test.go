package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// manyChecks replaces the fixture's rollup with n successful checks named
// check-00 onward.
func manyChecks(v *taskView, n int) {
	checks := checkRollupFixture()
	checks.Runs = nil
	for i := range n {
		checks.Runs = append(checks.Runs, apiclient.GitHubCheckRun{
			Name: fmt.Sprintf("check-%02d", i), State: "success",
			URL: fmt.Sprintf("https://github.com/octo/api/actions/runs/77/job/%d", i), RunID: 77,
		})
	}
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: checks})
}

// The window says what it leaves out (#799 W2, task 070 decision 2): a cue
// below while the tail is out of sight, one above once the head is, and none
// when every row fits.
func TestPullTabCheckWindowStatesWhatItHides(t *testing.T) {
	v := pullTabFixture(t)
	frame := ansi.Strip(v.render(120, 40))
	if strings.Contains(frame, "more") {
		t.Fatalf("three checks fit, yet the tab carries a cue:\n%s", frame)
	}

	manyChecks(v, 30)
	frame = ansi.Strip(v.render(120, 40))
	if strings.Contains(frame, "↑") || !strings.Contains(frame, "↓") {
		t.Fatalf("at the top of 30 checks, want only a ↓ cue:\n%s", frame)
	}
	for range 29 {
		v.movePullCursor(1)
	}
	frame = ansi.Strip(v.render(120, 40))
	if !strings.Contains(frame, "↑") || strings.Contains(frame, "↓ ") {
		t.Fatalf("at the bottom of 30 checks, want only a ↑ cue:\n%s", frame)
	}
	if !strings.Contains(frame, "check-29") {
		t.Fatalf("the last check is not on screen with the cursor on it:\n%s", frame)
	}
}

// The window moves only when the cursor leaves it: a press that stays inside
// the visible rows does not scroll them.
func TestPullTabCheckWindowIsStable(t *testing.T) {
	v := pullTabFixture(t)
	manyChecks(v, 30)
	firstRow := func() string {
		frame := ansi.Strip(v.render(120, 40))
		if i := strings.Index(frame, "check-"); i >= 0 {
			return frame[i : i+len("check-00")]
		}
		return ""
	}
	// Walk to the bottom, which scrolls, then one step back up, which must not.
	for range 29 {
		v.movePullCursor(1)
	}
	before := firstRow()
	v.movePullCursor(-1)
	if after := firstRow(); before == "check-00" || after != before {
		t.Fatalf("a press inside the window scrolled it: first row %q, then %q", before, after)
	}
}

// The header's labels share one value column whatever the pull request's
// state (#799 W16): padding a styled label counted its escape codes as width.
func TestPullTabHeaderLabelsAlign(t *testing.T) {
	merged := linkedPull()
	merged.Pull.Merged, merged.Pull.State = true, "closed"
	draft := linkedPull()
	draft.Pull.Draft = true
	gone := linkedPull()
	gone.Pull, gone.Reason = nil, "not_found"
	for name, pull := range map[string]apiclient.GitHubTaskPull{
		"open": linkedPull(), "merged": merged, "draft": draft, "unreachable": gone,
	} {
		t.Run(name, func(t *testing.T) {
			v := pullTabFixture(t)
			v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
			lines := v.pullHeaderLines(120)
			if len(lines) < 2 {
				t.Fatalf("no labelled rows: %q", lines)
			}
			for _, line := range lines[1:] {
				plain := ansi.Strip(line)
				// The value starts after the four-cell indent and the
				// ten-cell label column and its one space.
				const at = 4 + 10 + 1
				if len(plain) <= at || plain[at-1] != ' ' || plain[at] == ' ' {
					t.Fatalf("row %q has no value at column %d", plain, at)
				}
			}
		})
	}
}

// `▸` on a lane row marks the lane `l` opens from this tab (#799 W17) — not
// the Output tab's lane selection, which this tab cannot move.
func TestPullTabLaneMarkerIsTheLaneLOpens(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.pull = apiclient.GitHubTaskPull{Linked: true, Repo: "lezli01/vincent", Number: 300}
	// The Output tab points at the other lane, 43.
	v.selectLane(1)
	v.selectLane(1)
	v.tab = taskTabPull

	jump, ok := v.laneJump()
	if !ok {
		t.Fatal("the fixture has lanes, yet l opens none from the Pull Request tab")
	}
	frame := ansi.Strip(v.render(140, 30))
	marked := ""
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "▸") && strings.Contains(line, "· task ") {
			if marked != "" {
				t.Fatalf("two lane rows carry ▸:\n%s", frame)
			}
			marked = line
		}
	}
	if want := fmt.Sprintf("· task %d", jump); !strings.Contains(marked, want) {
		t.Fatalf("▸ sits on %q, but l opens task %d:\n%s", marked, jump, frame)
	}
	msg, isOpen := v.updateKey(synthKey("l"))().(openTaskMsg)
	if !isOpen || msg.id != jump {
		t.Fatalf("l opened %+v, want task %d", msg, jump)
	}
}
