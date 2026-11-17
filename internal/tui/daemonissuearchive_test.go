package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// heldArchive is an auto-archive report with two held tasks — one with two
// reasons, one waiting for a retry — a leftover chat directory, and no error.
func heldArchive() apiclient.DoctorIssueArchive {
	return apiclient.DoctorIssueArchive{
		Known: true, Enabled: true,
		Held: []apiclient.DoctorHeldTask{
			{
				TaskID: 41, IssueID: 7, Title: "teach the parser about tabs", State: "done", Role: "main",
				Reasons: []string{"dirty", "locked_by_chat"},
			},
			{
				TaskID: 52, IssueID: 9, Title: "bump the docs", State: "aborted",
				Reasons: []string{apiclient.DoctorHoldAwaitingRetry},
			},
		},
		ChatDirs: []apiclient.DoctorLeftoverChatDir{
			{IssueID: 7, ChatID: 3, Path: filepath.FromSlash("/data/worktrees/issue-7")},
		},
	}
}

// The block's rows are the CLI's facts: issue, task, role, state, every
// reason in order, then the task title; and the leftover chat directory.
func TestIssueArchiveLinesListEveryHeldTask(t *testing.T) {
	lines := issueArchiveLines(heldArchive(), 120)
	out := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.HasPrefix(strings.TrimSpace(out), "auto-archive held") || !strings.Contains(lines[0], "2 tasks") {
		t.Errorf("the heading does not say what the block is and how many: %q", lines[0])
	}
	first := ansi.Strip(lines[1])
	for _, want := range []string{"issue #7", "task #41", "main", "done", "dirty, locked_by_chat", "teach the parser about tabs"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first held row does not show %q: %q", want, first)
		}
	}
	if i, j := strings.Index(first, "locked_by_chat"), strings.Index(first, "teach"); i > j {
		t.Errorf("the title does not trail the reasons: %q", first)
	}
	second := ansi.Strip(lines[2])
	for _, want := range []string{"issue #9", "task #52", "aborted", "awaiting_retry", "bump the docs"} {
		if !strings.Contains(second, want) {
			t.Errorf("the second held row does not show %q: %q", want, second)
		}
	}
	if strings.Contains(second, "  task  ") {
		t.Errorf("an empty role rendered as a word: %q", second)
	}
	chat := ansi.Strip(lines[3])
	for _, want := range []string{"chat dir", "issue #7", "chat #3", filepath.FromSlash("/data/worktrees/issue-7")} {
		if !strings.Contains(chat, want) {
			t.Errorf("the chat-dir row does not show %q: %q", want, chat)
		}
	}
	if len(lines) != 4 {
		t.Errorf("want heading, two held rows and one chat dir, got %d lines:\n%s", len(lines), out)
	}
}

// awaiting_retry alone is dim — nothing holds the task — and every other
// reason is a warning. Nothing is bad: decision 16 says this is information.
func TestIssueArchiveLinesStyleTheReasons(t *testing.T) {
	lines := issueArchiveLines(heldArchive(), 120)
	if !strings.Contains(lines[1], styleWarn.Render("dirty, locked_by_chat")) {
		t.Errorf("real holds do not render as a warning: %q", lines[1])
	}
	if !strings.Contains(lines[2], styleDim.Render("awaiting_retry")) {
		t.Errorf("awaiting_retry alone does not render dim: %q", lines[2])
	}
	if !strings.Contains(lines[3], styleDim.Render(ansi.Strip(lines[3]))) {
		t.Errorf("the chat-dir row is not dim: %q", lines[3])
	}
	for _, l := range lines {
		if strings.Contains(l, "✗") {
			t.Errorf("a row renders as a problem: %q", l)
		}
	}
}

func TestIssueArchiveLinesReportTheError(t *testing.T) {
	ia := apiclient.DoctorIssueArchive{Known: true, Enabled: true, Error: "list tasks: database is locked"}
	lines := issueArchiveLines(ia, 120)
	if len(lines) != 2 || !strings.Contains(lines[1], styleWarn.Render("   ⚠ held tasks unknown: list tasks: database is locked")) {
		t.Errorf("the error is not one warning line: %q", lines)
	}
	if strings.Contains(lines[0], "tasks") {
		t.Errorf("the heading counts tasks it does not know: %q", lines[0])
	}
}

// Quiet is no lines at all, heading included.
func TestIssueArchiveLinesAreQuietWhenNothingIsHeld(t *testing.T) {
	off := heldArchive()
	off.Enabled = false
	unknown := heldArchive()
	unknown.Known = false
	for name, ia := range map[string]apiclient.DoctorIssueArchive{
		"known false":   unknown,
		"enabled false": off,
		"nothing held":  {Known: true, Enabled: true, Held: []apiclient.DoctorHeldTask{}, ChatDirs: []apiclient.DoctorLeftoverChatDir{}},
	} {
		if lines := issueArchiveLines(ia, 120); len(lines) != 0 {
			t.Errorf("%s: want no lines, got %q", name, lines)
		}
	}
}

// A held task's title is drawn as one line with no terminal controls: an
// escape sequence or a line break in it, as a synced issue's title may carry,
// would drive the terminal and split the row past the view's height budget.
func TestIssueArchiveLinesCleanTheTitle(t *testing.T) {
	ia := heldArchive()
	ia.Held[0].Title = "evil\x1b]0;pwned\x07\nsecond line"
	for _, width := range []int{0, 200, 75} {
		lines := issueArchiveLines(ia, width)
		if len(lines) != 4 {
			t.Fatalf("width %d: want heading, two held rows and a chat dir, got %d lines: %q", width, len(lines), lines)
		}
		row := lines[1]
		if strings.Contains(row, "\n") || strings.Contains(row, "\x1b]") || strings.Contains(row, "\x07") {
			t.Errorf("width %d: the title reached the row raw: %q", width, row)
		}
		if width != 75 && !strings.Contains(ansi.Strip(row), "evil second line") {
			t.Errorf("width %d: the cleaned title is missing: %q", width, ansi.Strip(row))
		}
		if width > 0 {
			if w := ansi.StringWidth(row); w > width {
				t.Errorf("the row is %d cells wide in a %d-cell pane: %q", w, width, row)
			}
		}
	}
}

// A narrow terminal loses the title first: every reason survives.
func TestIssueArchiveLinesElideTheTitleNotTheReasons(t *testing.T) {
	ia := heldArchive()
	ia.Held[0].Title = strings.Repeat("a long task title ", 6) + "END"
	full := ansi.Strip(issueArchiveLines(ia, 200)[1])
	if !strings.Contains(full, "END") {
		t.Fatalf("a wide pane cut the title: %q", full)
	}
	width := strings.Index(full, "a long") + 12
	row := ansi.Strip(issueArchiveLines(ia, width)[1])
	if !strings.Contains(row, "dirty, locked_by_chat") || strings.Contains(row, "END") || !strings.Contains(row, "…") {
		t.Errorf("at width %d the title was not elided behind the reasons: %q", width, row)
	}
	if w := ansi.StringWidth(row); w > width {
		t.Errorf("the row is %d cells wide in a %d-cell pane: %q", w, width, row)
	}
	// Cut or whole, the title is drawn in the same style: the elided row is
	// the whole row's prefix plus an ellipsis, escape codes included.
	whole := issueArchiveLines(ia, 200)[1]
	cut := issueArchiveLines(ia, width)[1]
	if !strings.HasSuffix(cut, "…") || !strings.HasPrefix(whole, strings.TrimSuffix(cut, "…")) {
		t.Errorf("the elided title is styled unlike the whole one:\n whole %q\n cut   %q", whole, cut)
	}
	// Narrower than the reasons: the title goes entirely, the reasons stay
	// for the frame to elide.
	row = ansi.Strip(issueArchiveLines(ia, 20)[1])
	if strings.Contains(row, "a long") {
		t.Errorf("the title survived a pane too narrow for the reasons: %q", row)
	}
}

// With the block quiet the view renders exactly as it did before the block
// existed; with it present it sits between the database and the adapters.
func TestDaemonViewPlacesTheAutoArchiveBlock(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	loadedDatabase(d)
	before := renderDaemon(d)
	d.doctor.IssueArchive = apiclient.DoctorIssueArchive{Known: true, Enabled: true}
	if got := renderDaemon(d); got != before {
		t.Errorf("an empty auto-archive group changed the layout:\n%s\n--- want ---\n%s", got, before)
	}
	d.doctor.IssueArchive = heldArchive()
	out := ansi.Strip(renderDaemon(d))
	db, held, adapters := strings.Index(out, "\n database"), strings.Index(out, "auto-archive held"), strings.Index(out, "\n adapters")
	if db < 0 || held < 0 || adapters < 0 || db >= held || held >= adapters {
		t.Errorf("the block is not between the database and the adapters (%d, %d, %d):\n%s", db, held, adapters, out)
	}
	if !strings.Contains(out, "\n\n auto-archive held") {
		t.Errorf("the block has no separator above it:\n%s", out)
	}
}

// On a short terminal with the block present, the pinned pane still starts
// at a block heading (review F24) — the auto-archive one counts as one.
func TestDaemonPinnedBlocksCountTheAutoArchiveHeading(t *testing.T) {
	ia := heldArchive()
	for i := range 6 {
		ia.Held = append(ia.Held, apiclient.DoctorHeldTask{
			TaskID: int64(60 + i), IssueID: 11, Title: fmt.Sprintf("held %d", i), State: "done",
			Reasons: []string{"dirty"},
		})
	}
	for _, sz := range [][2]int{{80, 24}, {78, 20}, {80, 30}, {80, 40}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			_, d := daemonRoot(t, sz[0], sz[1], []string{"level=INFO msg=ready"})
			report := testDoctorReport()
			report.IssueArchive = ia
			d.update(daemonDoctorMsg{report: report})
			d.focusConfig = false
			lines := strings.Split(ansi.Strip(d.render(sz[0]-2, sz[1]-3)), "\n")
			logPath := filepath.FromSlash("/data/logs/daemon.log")
			at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, logPath) })
			if at < 0 || at+2 >= len(lines) {
				t.Fatalf("no identity block:\n%s", strings.Join(lines, "\n"))
			}
			next := strings.TrimSpace(lines[at+1])
			if strings.Contains(next, "↑") {
				next = strings.TrimSpace(lines[at+2])
			}
			heading := slices.ContainsFunc([]string{"config in effect", "database", "auto-archive held", "adapters"}, func(h string) bool {
				return strings.HasPrefix(next, h)
			})
			if next != "" && !heading {
				t.Errorf("the pane starts mid-block, under identity: %q\n%s", next, strings.Join(lines, "\n"))
			}
		})
	}
}
