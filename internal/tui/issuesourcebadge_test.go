package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func jiraSource(key string) *apiclient.IssueSource {
	return &apiclient.IssueSource{
		Provider: "jira", ProviderName: "Jira", Repo: strings.SplitN(key, "-", 2)[0],
		Key: key, Ref: key, ShortRef: key,
	}
}

// badgeBoard is a board whose every card is crowded: a long title, labels
// and a kind, and a running task's cell.
func badgeBoard(sources ...*apiclient.IssueSource) *issuesView {
	v := issuesFixture()
	v.issues = nil
	handoffID := int64(12345)
	for i, src := range sources {
		v.issues = append(v.issues, apiclient.Issue{
			ID: int64(100 + i), ProjectID: 1, State: "open", Lane: "in_progress",
			Title:  strings.Repeat("A very long issue title that will not fit ", 5),
			Labels: []string{"backend", "regression", "p1"}, Kind: "bug",
			Source: src, TaskCount: 3, Active: true, Attention: true,
			Handoff: &apiclient.IssueHandoff{State: markerWaiting, HandoffID: &handoffID, Paused: true},
		})
	}
	v.clampCursor()
	return v
}

// The id an issue was synced from stays on its card at every width the TUI
// supports (2026-11-16): the title gives way to it, and a badge that still
// does not fit keeps its number.
func TestIssueSourceBadgeAlwaysVisible(t *testing.T) {
	for _, c := range []struct {
		name    string
		sources []*apiclient.IssueSource
		want    []string
	}{
		{
			"one GitHub repository",
			[]*apiclient.IssueSource{
				ghSource(&apiclient.IssueSource{Repo: "octo/web", Number: 41}),
				ghSource(&apiclient.IssueSource{Repo: "octo/web", Number: 12345}),
			},
			[]string{"#41", "#12345"},
		},
		{
			"several repositories keep the number",
			[]*apiclient.IssueSource{
				ghSource(&apiclient.IssueSource{Repo: "some-organisation/a-very-long-repository-name", Number: 41}),
				ghSource(&apiclient.IssueSource{Repo: "octo/web", Number: 7}),
			},
			[]string{"#41", "#7"},
		},
		{
			"Jira keys whole",
			[]*apiclient.IssueSource{jiraSource("PLATFORM-1234"), jiraSource("PLATFORM-7")},
			[]string{"PLATFORM-1234", "PLATFORM-7"},
		},
	} {
		for _, width := range []int{80, 100, 120, 200} {
			v := badgeBoard(c.sources...)
			out := v.render(width, 30)
			for i, l := range strings.Split(out, "\n") {
				if lw := ansi.StringWidth(l); lw > width {
					t.Errorf("%s at %d: line %d is %d cells", c.name, width, i, lw)
				}
			}
			lines := stripLines(out)
			for i, want := range c.want {
				id := "#" + strconv.Itoa(100+i)
				var row string
				for _, l := range lines {
					if strings.Contains(l, id+" ") {
						row = l
					}
				}
				if row == "" {
					t.Fatalf("%s at %d: no row for %s:\n%s", c.name, width, id, ansi.Strip(out))
				}
				if !strings.Contains(row, want) {
					t.Errorf("%s at %d: the card lost its source id %q: %q", c.name, width, want, row)
				}
			}
		}
	}
}

func TestFitLeftKeepsTheNumber(t *testing.T) {
	for _, c := range []struct {
		s       string
		w, keep int
		want    string
	}{
		{"octo/web#41", 20, 3, "octo/web#41"},
		{"octo/web#41", 8, 3, "…/web#41"},
		{"octo/web#41", 2, 3, "…#41"},
		{"PROJ-12", 3, 7, "PROJ-12"},
	} {
		if got := fitLeft(c.s, c.w, c.keep); got != c.want {
			t.Errorf("fitLeft(%q, %d, %d) = %q, want %q", c.s, c.w, c.keep, got, c.want)
		}
	}
}
