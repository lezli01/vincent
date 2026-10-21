package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Issue #800 W4: an identity line keeps the part that tells rows apart when
// the name in it is long.

const stripStepName = "verify-every-package-under-race-and-report-the-flakes-it-finds"

// At 80 columns the attempt strip used to end on the step name, losing the
// state and the follow marker the #597 amendment puts after the identity.
func TestWorkspaceIdentityAttemptStripKeepsNumberAndPausedState(t *testing.T) {
	d := outputStripDetail(t, attempt(1, 26, 1, stripStepName, "running", true))
	var runs []apiclient.StepRun
	for i := range 39 {
		runs = append(runs, attempt(int64(i+1), 26, i+1, stripStepName, "failed", false))
	}
	runs[38] = attempt(39, 26, 39, stripStepName, "running", true)
	d.task.Steps = runs
	d.selectedRun, d.displayRun = 39, 39
	d.following, d.newLines = false, 5

	got := ansi.Strip(renderAttemptSelector(d, 78))
	if w := ansi.StringWidth(got); w > 78 {
		t.Fatalf("strip is %d cells at 78: %q", w, got)
	}
	for _, want := range []string{"39/39 · step 27 ", "attempt 39 · running", "⏸ paused · 5 new"} {
		if !strings.Contains(got, want) {
			t.Errorf("attempt strip misses %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "verify-") || !strings.Contains(got, "…") {
		t.Errorf("the step name should be shortened, not dropped: %q", got)
	}
	if strings.Contains(got, "←/→") {
		t.Errorf("the hint should go before the name is cut: %q", got)
	}
}

// Three attempts of one long-named step all read `21 step-20-run-the-en…`
// in the Step Details sidebar; each must keep its attempt number.
func TestWorkspaceIdentitySidebarAttemptsAreDistinguishable(t *testing.T) {
	v := stepDetailsFixture(t)
	name := "step-20-run-the-entire-integration-suite"
	v.detail.task.Steps = []apiclient.StepRun{
		attempt(601, 20, 1, name, "failed", false),
		attempt(602, 20, 2, name, "failed", false),
		attempt(603, 20, 3, name, "running", true),
	}
	v.detail.selectedRun = 601
	got := ansi.Strip(v.renderStepDetails(80, 24))
	rows := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		side, _, ok := strings.Cut(line, "│")
		if !ok || !strings.Contains(side, "21 ") {
			continue
		}
		rows[strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(side), "›"))] = true
	}
	if len(rows) != 3 {
		t.Fatalf("sidebar rows are not distinguishable, got %d distinct of 3:\n%s", len(rows), got)
	}
	for n := 1; n <= 3; n++ {
		if !strings.Contains(got, fmt.Sprintf("#%d", n)) {
			t.Errorf("sidebar lost attempt #%d:\n%s", n, got)
		}
	}
}

// A diff file row keeps its `+N -M` whole and elides the middle of a long
// path, so both the directory and the file name stay readable.
func TestWorkspaceIdentityDiffRowsKeepCounts(t *testing.T) {
	path := "internal/some/very/deeply/nested/package/directory/with/a/long/file_name_test.go"
	diff := "diff --git a/" + path + " b/" + path + "\n" +
		"index 1111111..2222222 100644\n--- a/" + path + "\n+++ b/" + path + "\n" +
		"@@ -1,2 +1,2 @@\n-old\n+new\n context\n"
	p := loadedDiff(t, diff)
	for _, width := range []int{80, 60, 40} {
		got := ansi.Strip(p.render(width, 10))
		var row string
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, "name_test.go") {
				row = line
			}
		}
		if row == "" {
			t.Fatalf("width %d: no file row keeps the file name:\n%s", width, got)
		}
		if !strings.HasSuffix(strings.TrimRight(row, " "), "+1 -1") {
			t.Errorf("width %d: file row lost its counts: %q", width, row)
		}
		if !strings.Contains(row, "internal/") || !strings.Contains(row, "…") {
			t.Errorf("width %d: file row should elide the path's middle: %q", width, row)
		}
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("width %d: file row is %d cells", width, w)
		}
	}
}

// Issue #800 W18: the eighteen-cell `waiting on a human` and nineteen-cell
// `edited before retry` labels line their values up with every other fact.
func TestWorkspaceIdentityFactLabelsAlign(t *testing.T) {
	run := attempt(1, 0, 1, "build", "succeeded", false)
	lines := stepOutcomeLines(run, fixedNow, 76)
	col := map[string]int{}
	for _, line := range lines {
		plain := ansi.Strip(line)
		for _, label := range []string{"tokens", "waiting on a human", "edited before retry", "exit code"} {
			if strings.HasPrefix(strings.TrimSpace(plain), label+" ") {
				rest := plain[strings.Index(plain, label)+len(label):]
				col[label] = ansi.StringWidth(plain) - ansi.StringWidth(strings.TrimLeft(rest, " "))
			}
		}
	}
	if len(col) != 4 {
		t.Fatalf("labels not found on a line with their value, got %v:\n%s", col, ansi.Strip(strings.Join(lines, "\n")))
	}
	for label, c := range col {
		if c != col["tokens"] {
			t.Errorf("%q's value starts at column %d, tokens' at %d:\n%s", label, c, col["tokens"], ansi.Strip(strings.Join(lines, "\n")))
		}
	}
	for _, line := range lines {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "waiting on a human ") && !strings.Contains(plain, "waiting on a human  ") {
			t.Errorf("an eighteen-cell label butts against its value: %q", plain)
		}
	}
}

// Issue #800 W14: at 80 columns the strip keeps its tab hint, and no
// abbreviation reads as a different tab than the breadcrumb names.
func TestWorkspaceIdentityTabStripKeepsHintAt80Columns(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	for _, tab := range v.tabs() {
		v.tab = tab
		frame := ansi.Strip(v.render(78, 24))
		strip := strings.Split(frame, "\n")[1]
		if !strings.Contains(strip, "tab/⇧tab or digits") {
			t.Errorf("tab %v: strip lost the tab hint at 80 columns: %q", tab, strip)
		}
		if ansi.StringWidth(strip) > 78 {
			t.Errorf("tab %v: strip is %d cells", tab, ansi.StringWidth(strip))
		}
		if strings.Contains(strip, "Inputs") {
			t.Errorf("tab %v: Step Details is abbreviated to a different name: %q", tab, strip)
		}
	}
	if short := taskTabShort[taskTabStepDetails]; !strings.HasPrefix(taskTabStepDetails.String(), strings.TrimSuffix(short, ".")) {
		t.Errorf("Step Details abbreviates to %q, which is not a shortening of its name", short)
	}
}

// Issue #800 W15: a task that failed to load says so the same way on every
// tab, rather than as each tab's own empty state.
func TestWorkspaceIdentityLoadFailureIsOneMessageOnEveryTab(t *testing.T) {
	d := taskDetailFixture(t)
	d.loaded = false
	d.loadErr = errors.New("connection refused")
	v := newTaskView(d)
	want := "task unavailable: connection refused"
	for _, tab := range v.tabs() {
		v.tab = tab
		got := strings.TrimSpace(ansi.Strip(v.renderTabBody(80, 20)))
		if got != want {
			t.Errorf("tab %v on a failed load reads %q, want %q", tab, got, want)
		}
	}
}

// Issue #800 W15: the disconnected banner sits at the indent every other
// banner does, with a blank line under it.
func TestWorkspaceIdentityDisconnectedBannerSpacing(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	v.connected = false
	lines := strings.Split(ansi.Strip(v.render(100, 24)), "\n")
	for i, line := range lines {
		if !strings.Contains(line, "daemon unreachable") {
			continue
		}
		if !strings.HasPrefix(line, "  ⚠") {
			t.Errorf("banner is not indented 2: %q", line)
		}
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != "" {
			t.Errorf("banner has no blank line after it:\n%s", strings.Join(lines, "\n"))
		}
		return
	}
	t.Fatal("no disconnected banner")
}

// Issue #800 W19: a reason's raw code and the agent are separated.
func TestWorkspaceIdentityReasonCodeAndAgentAreSeparated(t *testing.T) {
	d := newTestDetail(t)
	run := attempt(1, 0, 1, "build", "failed", false)
	run.FailureReason = ptr("check_failed")
	got := ansi.Strip(strings.Join(d.attemptFields(run, false), " "))
	if !strings.Contains(got, "check_failed · claude") {
		t.Errorf("agent runs into the reason code: %q", got)
	}
}
