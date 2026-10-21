package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Issue #804: the pull-requests takeover (§15 view 7), its create-PR popup,
// and the archived chats board (view 10). Each test below is one of the
// issue's acceptance criteria.

// plainLines is a render with its styling stripped, one entry per line.
func issue804Lines(out string) []string {
	return strings.Split(ansi.Strip(out), "\n")
}

// At 80 columns a row on a long dependabot branch still shows the number,
// the whole title and the claim (PL-3): the branch is what gives way, not the
// title.
func TestPullRowKeepsTitleAndClaimAt80Columns(t *testing.T) {
	const title = "Refactor the session cache"
	v := pullRequestsFixture(testPull(42, title, claimedBy(7, "auto"), func(p *apiclient.GitHubPullRequest) {
		p.HeadBranch = "dependabot/go_modules/github.com/charmbracelet/x/ansi-0.11.4"
	}))
	var row string
	for _, line := range issue804Lines(v.render(80, 24)) {
		if strings.Contains(line, "#42") {
			row = line
		}
	}
	if !strings.Contains(row, title) {
		t.Errorf("the 80-column row lost its title to the branch:\n%q", row)
	}
	if !strings.Contains(row, "task #7") {
		t.Errorf("the 80-column row lost its claim:\n%q", row)
	}
}

// Review F10: the claim's column is never narrower than its widest
// "task #n (how)", so at the 60-column floor a five-digit task id keeps the
// way it was claimed; the title is what gives way.
func TestPullRowKeepsALongClaimAt60Columns(t *testing.T) {
	v := pullRequestsFixture(
		testPull(42, "Refactor the session cache for the slow path", claimedBy(12345, "human")),
		testPull(43, "Bump a dependency", claimedBy(7, "auto"), func(p *apiclient.GitHubPullRequest) {
			p.HeadBranch = "dependabot/go_modules/github.com/charmbracelet/x/ansi-0.11.4"
		}),
	)
	rows := map[string]string{}
	for _, line := range issue804Lines(v.render(60, 24)) {
		for _, n := range []string{"#42", "#43"} {
			if strings.Contains(line, n+" ") && rows[n] == "" {
				rows[n] = line
			}
		}
	}
	for n, want := range map[string]string{"#42": "task #12345 (human)", "#43": "task #7"} {
		if row := rows[n]; !strings.Contains(row, want) || ansi.StringWidth(row) > 60 {
			t.Errorf("the 60-column row %s does not keep %q within 60 cells:\n%q", n, want, row)
		}
	}
}

// Review F18: the detail line's `⎇` glyph never stands alone on a line —
// the long-branch case the detail line exists for.
func TestPullDetailBranchStaysBesideItsGlyph(t *testing.T) {
	for _, w := range []int{60, 40, 30} {
		v := pullRequestsFixture(testPull(42, "Bump ansi", func(p *apiclient.GitHubPullRequest) {
			p.HeadBranch = "dependabot/go_modules/github.com/charmbracelet/x/ansi-0.11.4"
			p.BaseBranch = "master"
		}))
		for _, line := range issue804Lines(v.render(w, 24)) {
			if strings.TrimSpace(line) == "⎇" {
				t.Errorf("at %d columns the ⎇ glyph is alone on a line:\n%s", w, strings.Join(issue804Lines(v.render(w, 24)), "\n"))
			}
			if ansi.StringWidth(line) > w {
				t.Errorf("at %d columns a line is %d cells: %q", w, ansi.StringWidth(line), line)
			}
		}
	}
}

// After `s` moves the listing to closed, the header and the empty state name
// that state instead of still saying "open" (PL-4).
func TestPullHeaderAndEmptyStateNameTheState(t *testing.T) {
	v := pullRequestsFixture()
	v.cycleState()
	if v.state != "closed" {
		t.Fatalf("state = %q after one s, want closed", v.state)
	}
	v.note = ""
	out := ansi.Strip(v.render(120, 24))
	if strings.Contains(out, "open") {
		t.Errorf("the closed listing still says \"open\":\n%s", out)
	}
	if !strings.Contains(out, "closed") {
		t.Errorf("the closed listing never names its state:\n%s", out)
	}
}

// The no-GitHub reason carries its remedy, so it must be readable in full at
// 80×24 rather than cut to one line (PL-5).
func TestPullUnavailableReasonIsReadableAt80Columns(t *testing.T) {
	v := pullRequestsFixture()
	const reason = "gh is not authenticated for github.com — run `gh auth login` and choose this account, then press r to retry"
	v.probes[0].status = apiclient.GitHubStatus{Reason: "unauthenticated", Message: reason}
	text := strings.Join(strings.Fields(ansi.Strip(v.render(80, 24))), " ")
	if !strings.Contains(text, reason) {
		t.Errorf("the reason is not readable in full at 80 columns:\n%s", v.render(80, 24))
	}
}

// The unlink question shows its consequence and its keys at 80 columns
// (PL-6): a long consequence must wrap, not push the keys off the line.
func TestPullUnlinkQuestionWrapsAt80Columns(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "ship it", claimedBy(7, "human")))
	v.askUnlink()
	if v.confirm == nil {
		t.Fatal("u on a claimed row asked nothing")
	}
	v.confirm.text = strings.Repeat("unlinking hands this pull request back to the reconciler ", 3) + "(end)"
	text := strings.Join(strings.Fields(ansi.Strip(v.render(80, 24))), " ")
	if !strings.Contains(text, "(end)") {
		t.Errorf("the unlink consequence was clipped at 80 columns:\n%s", v.render(80, 24))
	}
}

// The PR list reaches its first and last rows in one key (PL-9).
func TestPullListHomeAndEndReachTheEnds(t *testing.T) {
	pulls := make([]apiclient.GitHubPullRequest, 0, 45)
	for i := 1; i <= 45; i++ {
		pulls = append(pulls, testPull(i, fmt.Sprintf("pull %d", i)))
	}
	v := pullRequestsFixture(pulls...)
	v.update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := len(v.rows()) - 1; v.cursor != want {
		t.Errorf("end left the cursor on %d, want the last row %d", v.cursor, want)
	}
	v.update(tea.KeyPressMsg{Code: tea.KeyHome})
	if v.cursor != 0 {
		t.Errorf("home left the cursor on %d, want 0", v.cursor)
	}
}

// With a 30-line body in a short popup the key footer stays visible (PL-1):
// it is pinned, not the last line of a list windowed from the top.
func TestCreatePRFormKeepsKeysVisibleWithALongBody(t *testing.T) {
	f := createPRFixture(t)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("- change %d", i+1)
	}
	f.setRow(cprBody, strings.Join(lines, "\n"))
	f.err = "push rejected"
	out := ansi.Strip(f.render(76, 20))
	if !strings.Contains(out, "push rejected") {
		t.Errorf("the error is off-screen in a 20-row popup:\n%s", out)
	}
}

// The URL is shown without the encoded body (PL-1).
func TestCreatePRFormURLOmitsTheBody(t *testing.T) {
	f := createPRFixture(t)
	if out := ansi.Strip(f.render(76, 40)); strings.Contains(out, "body=") {
		t.Errorf("the shown URL still carries the encoded body:\n%s", out)
	}
}

// archivedChats804 is the archived chats board with 65 rows (AR-7).
func archivedChats804() *chatsView {
	v := newArchivedChatsView()
	v.now = chatsFixture().now
	v.project = projectSel{id: 7, name: "repo"}
	chats := make([]apiclient.Chat, 0, 65)
	for i := 1; i <= 65; i++ {
		chats = append(chats, testChat(int64(i), "archived", fmt.Sprintf("chat %d", i)))
	}
	v.applyLoaded(chatsLoadedMsg{archived: true, chats: chats})
	return v
}

// Archived chats reach their first and last rows in one key (AR-7).
func TestArchivedChatsHomeAndEndReachTheEnds(t *testing.T) {
	v := archivedChats804()
	v.update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if want := len(v.rows()) - 1; v.cursor != want {
		t.Errorf("end left the cursor on %d, want the last row %d", v.cursor, want)
	}
	v.update(tea.KeyPressMsg{Code: tea.KeyHome})
	if v.cursor != 0 {
		t.Errorf("home left the cursor on %d, want 0", v.cursor)
	}
}

// Archived chats reload when activated, as the live board does (AR-12).
func TestArchivedChatsReloadOnActivation(t *testing.T) {
	v := archivedChats804()
	v.client = offlineClient()
	if _, cmd := v.update(viewActivatedMsg{id: viewArchivedChats}); cmd == nil {
		t.Error("activating the archived chats board issued no reload")
	}
}

// The archived chats header names the date window (AR-5).
func TestArchivedChatsHeaderNamesTheWindow(t *testing.T) {
	v := archivedChats804()
	if header := issue804Lines(v.render(120, 30))[0]; !strings.Contains(header, "archived") || !strings.Contains(header, "days") {
		t.Errorf("the archived chats header does not name the window:\n%q", header)
	}
}
