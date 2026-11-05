package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// Task 142.14 decision 3: `S` is offered exactly where the project has a
// sync pass to wake — judged by the list's own loaded sync status, never by
// the GitHub probe — in the footer, the `?` sheet and the palette alike.

// syncOffered reports where `S` is offered on the issues list: the rows the
// footer draws from, the `?` sheet, and the palette.
func syncOffered(h *newTaskLiveHarness) (footer, help, palette bool) {
	list := h.m.views[viewIssues].(*issuesView)
	for _, b := range list.liveBindings(bindingsFor(ctxIssues)) {
		footer = footer || b.key == issueSyncKey
	}
	for _, l := range h.m.helpLines(200) {
		help = help || strings.Contains(l, "from its issue source now")
	}
	h.m.openPalette()
	for _, e := range h.m.palette.entries {
		palette = palette || e.key == issueSyncKey
	}
	h.m.palette = nil
	return footer, help, palette
}

func bindIssueSource(t *testing.T, h *newTaskLiveHarness, src store.IssueSource) {
	t.Helper()
	ctx := context.Background()
	p, err := h.st.GetProject(ctx, h.projectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	p.IssueSource = src
	if err := h.st.UpdateProject(ctx, p); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
}

func TestIssueSyncKeyFollowsTheSyncStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origin  string
		source  store.IssueSource
		offered bool
	}{
		{"github", "https://github.com/octo/repo.git", store.IssueSource{}, true},
		{"jira", "https://example.com/o/r.git", store.IssueSource{
			Provider: store.IssueProviderJira, JiraSite: "https://acme.atlassian.net", JiraProjectKey: "PROJ",
		}, true},
		{"none", "https://github.com/octo/repo.git", store.IssueSource{Provider: store.IssueProviderNone}, false},
		{"github pinned off github.com", "https://example.com/o/r.git", store.IssueSource{Provider: store.IssueProviderGitHub}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &requestRecorder{}
			h := newNewTaskLiveHarnessWith(t, liveOptions{wrap: rec.wrap})
			addLiveRemote(t, h.repo, tc.origin)
			if tc.source.Provider != "" {
				bindIssueSource(t, h, tc.source)
			}
			list := issuesListView(t, h)
			h.send(selectViewMsg{id: viewIssues})
			h.p.until(10*time.Second, "the issues list and its sync status", func() bool {
				return list.loaded && list.sync != nil
			})
			if h.m.githubAvailable() {
				t.Fatal("the harness has a GitHub integration; the gate must not need one")
			}
			footer, help, palette := syncOffered(h)
			if footer != tc.offered || help != tc.offered || palette != tc.offered {
				t.Fatalf("S offered in footer %v, ? %v, palette %v; want %v (status %+v)",
					footer, help, palette, tc.offered, *list.sync)
			}

			h.sendKey(keyPress("S"))
			want := "POST /v1/projects/" + strconv.FormatInt(h.projectID, 10) + "/issues/sync"
			if !tc.offered {
				// Hidden is not refused: the key does nothing at all.
				if got := rec.matching(want); len(got) != 0 || list.note != "" {
					t.Fatalf("a hidden S sent %v, note %q", got, list.note)
				}
				return
			}
			h.p.until(10*time.Second, "the sync answer", func() bool {
				return strings.HasPrefix(list.note, "sync requested")
			})
			if got := rec.matching(want); len(got) != 1 {
				t.Fatalf("S sent %v, want one %s", got, want)
			}
			if tc.source.Provider == store.IssueProviderJira && !strings.Contains(list.note, "acme.atlassian.net/PROJ") {
				t.Errorf("the note does not name the Jira source: %q", list.note)
			}
		})
	}
}

// A status that has not loaded offers no `S`, and one for another project
// is not this project's answer.
func TestIssueSyncKeyHiddenUntilLoaded(t *testing.T) {
	v := issuesFixture()
	if v.syncable() {
		t.Fatal("S offered before any sync status loaded")
	}
	st := issueSyncFixture("github", "pending")
	v.sync, v.syncFor = &st, v.project.id+1
	if v.syncable() {
		t.Fatal("S offered on another project's sync status")
	}
	v.syncFor = v.project.id
	if !v.syncable() {
		t.Fatal("S not offered on a pending GitHub project")
	}
	for _, b := range v.liveBindings(bindingsFor(ctxIssues)) {
		if b.key == issueSyncKey {
			return
		}
	}
	t.Fatal("liveBindings dropped an offered S")
}
