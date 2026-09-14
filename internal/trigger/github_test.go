package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// ghT0 is the seed tick in every GitHub test.
var ghT0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func githubDoc(id, sourceType string, project int, extra string) string {
	return fmt.Sprintf(`id: %s
enabled: true
source:
  type: %s
  project: %d
action:
  type: create_task
  title: 'gh {{ .Event.action }} {{ .Event.number }}'
`, id, sourceType, project) + extra
}

func ghIssue(n int, state string, updated time.Time, labels, assignees []string) GitHubIssue {
	return GitHubIssue{
		Number: n, Title: fmt.Sprintf("issue %d", n), Author: "alice", State: state,
		Labels: labels, Assignees: assignees, CreatedAt: ghT0.Add(-2 * time.Hour), UpdatedAt: updated,
	}
}

func ghPull(n int, state string, updated time.Time, draft, merged bool, reviewers []string) GitHubPull {
	return GitHubPull{
		Number: n, Title: fmt.Sprintf("pull %d", n), Author: "alice", State: state, Draft: draft, Merged: merged,
		RequestedReviewers: reviewers, CreatedAt: ghT0.Add(-2 * time.Hour), UpdatedAt: updated,
	}
}

// diffAfterSeed does what a trigger's first two ticks do: seed a snapshot
// from one listing at ghT0, persist it through the cursor's JSON, and diff the
// next listing against what decoded.
func diffAfterSeed(t *testing.T, kind string, seed, next GitHubListing) []Event {
	t.Helper()
	s := &ghSnapshot{Kind: kind, SeededAt: ghT0, Items: map[string]ghItem{}}
	_ = s.diff(kind, seed)
	decoded, ok := decodeSnapshot(&store.TriggerCursor{Cursor: s.encode()})
	if !ok {
		t.Fatalf("snapshot did not decode: %s", *s.encode())
	}
	return decoded.diff(kind, next)
}

// summarize is each event's action and number, with the list a labeled,
// unlabeled, assigned or review_requested event carries.
func summarize(evs []Event) []string {
	var out []string
	for _, ev := range evs {
		s := fmt.Sprintf("%v#%v", ev["action"], ev["number"])
		for _, k := range []string{"labels", "assignees", "reviewer"} {
			if v, ok := ev[k]; ok {
				s += fmt.Sprintf(" %s=%v", k, v)
			}
		}
		out = append(out, s)
	}
	return out
}

func TestGitHubIssueDiff(t *testing.T) {
	seen := ghT0.Add(-time.Hour)
	t1, t2 := ghT0.Add(time.Minute), ghT0.Add(2*time.Minute)
	opened := ghIssue(1, "open", t1, nil, nil)
	opened.CreatedAt = t1

	for _, tc := range []struct {
		name       string
		seed, next []GitHubIssue
		want       []string
	}{
		{"opened after the seed", nil, []GitHubIssue{opened}, []string{"opened#1"}},
		{"an unseen issue older than the seed is a baseline", nil, []GitHubIssue{ghIssue(1, "open", t1, nil, nil)}, nil},
		{
			"closed",
			[]GitHubIssue{ghIssue(1, "open", seen, nil, nil)},
			[]GitHubIssue{ghIssue(1, "closed", t1, nil, nil)},
			[]string{"closed#1"},
		},
		{
			"reopened, in gh's capitals",
			[]GitHubIssue{ghIssue(1, "CLOSED", seen, nil, nil)},
			[]GitHubIssue{ghIssue(1, "OPEN", t1, nil, nil)},
			[]string{"reopened#1"},
		},
		{
			"a change of case alone is no transition",
			[]GitHubIssue{ghIssue(1, "open", seen, nil, nil)},
			[]GitHubIssue{ghIssue(1, "OPEN", t1, nil, nil)},
			nil,
		},
		{
			"labeled carries only the added labels",
			[]GitHubIssue{ghIssue(1, "open", seen, []string{"a"}, nil)},
			[]GitHubIssue{ghIssue(1, "open", t1, []string{"x", "a"}, nil)},
			[]string{"labeled#1 labels=[x]"},
		},
		{
			"unlabeled carries only the removed labels",
			[]GitHubIssue{ghIssue(1, "open", seen, []string{"a", "b"}, nil)},
			[]GitHubIssue{ghIssue(1, "open", t1, []string{"b"}, nil)},
			[]string{"unlabeled#1 labels=[a]"},
		},
		{
			"a relabel is both",
			[]GitHubIssue{ghIssue(1, "open", seen, []string{"a"}, nil)},
			[]GitHubIssue{ghIssue(1, "open", t1, []string{"b"}, nil)},
			[]string{"labeled#1 labels=[b]", "unlabeled#1 labels=[a]"},
		},
		{
			"assigned",
			[]GitHubIssue{ghIssue(1, "open", seen, nil, nil)},
			[]GitHubIssue{ghIssue(1, "open", t1, nil, []string{"bob"})},
			[]string{"assigned#1 assignees=[bob]"},
		},
		{
			"unassigning synthesizes nothing",
			[]GitHubIssue{ghIssue(1, "open", seen, nil, []string{"bob"})},
			[]GitHubIssue{ghIssue(1, "open", t1, nil, nil)},
			nil,
		},
		{
			"unchanged",
			[]GitHubIssue{ghIssue(1, "open", seen, []string{"a"}, []string{"bob"})},
			[]GitHubIssue{ghIssue(1, "open", seen, []string{"a"}, []string{"bob"})},
			nil,
		},
		{
			"oldest change first",
			[]GitHubIssue{ghIssue(1, "open", seen, nil, nil), ghIssue(2, "open", seen, nil, nil)},
			[]GitHubIssue{ghIssue(2, "closed", t2, nil, nil), ghIssue(1, "open", t1, []string{"x"}, nil)},
			[]string{"labeled#1 labels=[x]", "closed#2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs := diffAfterSeed(t, SourceGitHubIssues, GitHubListing{Issues: tc.seed}, GitHubListing{Issues: tc.next})
			if got := summarize(evs); !slices.Equal(got, tc.want) {
				t.Errorf("events = %q, want %q", got, tc.want)
			}
		})
	}

	evs := diffAfterSeed(t, SourceGitHubIssues, GitHubListing{}, GitHubListing{Issues: []GitHubIssue{opened}})
	ev := evs[0]
	issue, _ := ev["Issue"].(map[string]any)
	if ev.ID() != fmt.Sprintf("github:issue:1:opened:%d", t1.Unix()) || ev["author"] != "alice" ||
		ev["state"] != "open" || issue["Title"] != "issue 1" {
		t.Errorf("opened event = %+v", ev)
	}
}

func TestGitHubPullDiff(t *testing.T) {
	seen, t1 := ghT0.Add(-time.Hour), ghT0.Add(time.Minute)
	opened := ghPull(1, "open", t1, false, false, nil)
	opened.CreatedAt = t1
	openedAsking := ghPull(1, "open", t1, false, false, []string{"carol"})
	openedAsking.CreatedAt = t1
	closedNew := ghPull(1, "closed", t1, false, false, nil)
	closedNew.CreatedAt = t1

	for _, tc := range []struct {
		name       string
		seed, next []GitHubPull
		want       []string
	}{
		{"opened after the seed", nil, []GitHubPull{opened}, []string{"opened#1"}},
		{
			"opened asking for review requests it too", nil,
			[]GitHubPull{openedAsking},
			[]string{"opened#1", "review_requested#1 reviewer=[carol]"},
		},
		{"a new pull already closed is a baseline", nil, []GitHubPull{closedNew}, nil},
		{"an unseen pull older than the seed is a baseline", nil, []GitHubPull{ghPull(1, "open", t1, false, false, nil)}, nil},
		{
			"ready_for_review",
			[]GitHubPull{ghPull(1, "open", seen, true, false, nil)},
			[]GitHubPull{ghPull(1, "open", t1, false, false, nil)},
			[]string{"ready_for_review#1"},
		},
		{
			"a draft that closes was never ready",
			[]GitHubPull{ghPull(1, "open", seen, true, false, nil)},
			[]GitHubPull{ghPull(1, "closed", t1, false, false, nil)},
			[]string{"closed#1"},
		},
		{
			"review_requested carries only new reviewers",
			[]GitHubPull{ghPull(1, "open", seen, false, false, []string{"carol"})},
			[]GitHubPull{ghPull(1, "open", t1, false, false, []string{"dave", "carol"})},
			[]string{"review_requested#1 reviewer=[dave]"},
		},
		{
			"closed",
			[]GitHubPull{ghPull(1, "open", seen, false, false, nil)},
			[]GitHubPull{ghPull(1, "closed", t1, false, false, nil)},
			[]string{"closed#1"},
		},
		{
			"merged, as REST's closed and merged",
			[]GitHubPull{ghPull(1, "open", seen, false, false, nil)},
			[]GitHubPull{ghPull(1, "closed", t1, false, true, nil)},
			[]string{"merged#1"},
		},
		{
			"merged, as gh's MERGED",
			[]GitHubPull{ghPull(1, "OPEN", seen, false, false, nil)},
			[]GitHubPull{ghPull(1, "MERGED", t1, false, false, nil)},
			[]string{"merged#1"},
		},
		{
			"gh's MERGED and REST's closed and merged are one state",
			[]GitHubPull{ghPull(1, "MERGED", seen, false, false, nil)},
			[]GitHubPull{ghPull(1, "closed", t1, false, true, nil)},
			nil,
		},
		{
			"unchanged",
			[]GitHubPull{ghPull(1, "open", seen, false, false, []string{"carol"})},
			[]GitHubPull{ghPull(1, "open", seen, false, false, []string{"carol"})},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs := diffAfterSeed(t, SourceGitHubPRs, GitHubListing{Pulls: tc.seed}, GitHubListing{Pulls: tc.next})
			if got := summarize(evs); !slices.Equal(got, tc.want) {
				t.Errorf("events = %q, want %q", got, tc.want)
			}
		})
	}

	// gh's spelling reaches templates as REST's vocabulary.
	evs := diffAfterSeed(t, SourceGitHubPRs,
		GitHubListing{Pulls: []GitHubPull{ghPull(1, "OPEN", seen, false, false, nil)}},
		GitHubListing{Pulls: []GitHubPull{ghPull(1, "MERGED", t1, false, false, nil)}})
	pull, _ := evs[0]["Pull"].(map[string]any)
	if evs[0]["state"] != "closed" || pull["State"] != "closed" || pull["Merged"] != true {
		t.Errorf("merged event = %+v", evs[0])
	}
}

// TestGitHubSeedTickAndRestart: the first tick stores a snapshot and fires
// nothing; a new manager over the same database decodes it and fires the next
// tick's transition; a failed listing is a failing poll that keeps the
// snapshot and publishes one poll_changed.
func TestGitHubSeedTickAndRestart(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("labels", githubDoc("labels", SourceGitHubIssues, 1, "match:\n  action: labeled\n"))
	seen := ghT0.Add(-time.Hour)
	seed := GitHubListing{Issues: []GitHubIssue{ghIssue(1, "open", seen, []string{"a"}, nil)}}

	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, map[int64]GitHubWant{1: {Issues: true}}) {
		t.Fatalf("wants before the seed = %+v", w)
	}
	h.m.JudgeGitHub(ctx, 1, seed)
	if rows := h.ledger("labels"); len(rows) != 0 {
		t.Fatalf("the seed tick wrote ledger rows: %+v", rows)
	}
	c := h.cursor("labels")
	if c == nil || c.Cursor == nil || !c.LastPollOK {
		t.Fatalf("cursor after the seed = %+v", c)
	}
	var snap ghSnapshot
	if err := json.Unmarshal([]byte(*c.Cursor), &snap); err != nil || snap.Kind != SourceGitHubIssues ||
		!slices.Equal(snap.Items["1"].Labels, []string{"a"}) || !snap.Watermark.Equal(seen) {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if pc := h.pollChanges("labels"); len(pc) != 1 || !pc[0].OK {
		t.Errorf("poll_changed after the seed = %+v", pc)
	}
	wantSince := map[int64]GitHubWant{1: {Issues: true, Since: seen.Add(-githubOverlap)}}
	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, wantSince) {
		t.Errorf("wants after the seed = %+v, want %+v", w, wantSince)
	}

	m := h.restart()
	h.clock.Set(ghT0.Add(10 * time.Minute))
	if w := m.GitHubWants(ctx); !reflect.DeepEqual(w, wantSince) {
		t.Errorf("wants after a restart = %+v, want %+v", w, wantSince)
	}
	t5 := ghT0.Add(5 * time.Minute)
	next := GitHubListing{Issues: []GitHubIssue{ghIssue(1, "open", t5, []string{"a", "x"}, nil)}}

	m.SetGitHubLister(func(_ context.Context, project int64, want GitHubWant) GitHubListing {
		if project != 1 || !want.Issues || !want.Since.Equal(seen.Add(-githubOverlap)) {
			t.Errorf("dry run listed project %d with %+v", project, want)
		}
		return next
	})
	dry, err := m.PollDry(ctx, "labels")
	if err != nil || dry.Seed || len(dry.Events) != 1 || dry.Events[0].Outcome != store.DeliveryFired {
		t.Errorf("PollDry = %+v, %v", dry, err)
	}
	if after := h.cursor("labels"); !reflect.DeepEqual(after, c) {
		t.Errorf("the dry run moved the cursor: %+v", after)
	}

	m.JudgeGitHub(ctx, 1, next)
	rows := h.ledger("labels")
	if len(rows) != 1 || rows[0].Outcome != store.DeliveryFired ||
		rows[0].EventID != fmt.Sprintf("github:issue:1:labeled:%d", t5.Unix()) {
		t.Fatalf("ledger after the restart tick = %+v", rows)
	}
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].body.Title != "gh labeled 1" {
		t.Errorf("replays = %+v", reqs)
	}
	if w := m.GitHubWants(ctx); !w[1].Since.Equal(t5.Add(-githubOverlap)) {
		t.Errorf("wants after the tick = %+v", w)
	}

	m.JudgeGitHub(ctx, 1, next)
	if n := len(h.ledger("labels")); n != 1 {
		t.Errorf("an unchanged listing wrote %d rows", n-1)
	}

	kept := *h.cursor("labels").Cursor
	failed := GitHubListing{Err: errors.New("github.enabled is off in config.yaml")}
	m.JudgeGitHub(ctx, 1, failed)
	m.JudgeGitHub(ctx, 1, failed)
	c = h.cursor("labels")
	if c.LastPollOK || !strings.Contains(c.LastPollError, "github.enabled") || c.Cursor == nil || *c.Cursor != kept {
		t.Errorf("cursor after failed listings = %+v", c)
	}
	if pc := h.pollChanges("labels"); len(pc) != 2 || pc[1].OK {
		t.Errorf("poll_changed = %+v, want the seed's and one failing", pc)
	}
}

// TestGitHubWants: one want per project, both kinds when both are armed, a
// watermark only when every trigger on the project has one, and nothing for a
// disarmed trigger.
func TestGitHubWants(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	seen := ghT0.Add(-time.Hour)
	h.write("issues", githubDoc("issues", SourceGitHubIssues, 1, "match:\n  action: labeled\n"))
	h.write("off", strings.Replace(githubDoc("off", SourceGitHubPRs, 2, "match:\n  action: merged\n"),
		"enabled: true", "enabled: false", 1))
	h.write("cmd", commandDoc(t, "cmd", true, filepath.Join(t.TempDir(), "unused.out")))

	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, map[int64]GitHubWant{1: {Issues: true}}) {
		t.Fatalf("unseeded wants = %+v", w)
	}
	issues := []GitHubIssue{ghIssue(1, "open", seen, nil, nil)}
	h.m.JudgeGitHub(ctx, 1, GitHubListing{Issues: issues})
	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, map[int64]GitHubWant{1: {Issues: true, Since: seen.Add(-githubOverlap)}}) {
		t.Errorf("seeded wants = %+v", w)
	}

	h.write("prs", githubDoc("prs", SourceGitHubPRs, 1, "match:\n  action: merged\n"))
	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, map[int64]GitHubWant{1: {Issues: true, Pulls: true}}) {
		t.Errorf("wants with an unseeded trigger on the project = %+v, want no since", w)
	}

	pulls := []GitHubPull{ghPull(1, "open", ghT0, false, false, nil)}
	h.m.JudgeGitHub(ctx, 1, GitHubListing{Issues: issues, Pulls: pulls})
	want := map[int64]GitHubWant{1: {Issues: true, Pulls: true, Since: seen.Add(-githubOverlap)}}
	if w := h.m.GitHubWants(ctx); !reflect.DeepEqual(w, want) {
		t.Errorf("wants with two seeded triggers = %+v, want the earlier watermark %+v", w, want)
	}

	h.enabled.Store(false)
	if w := h.m.GitHubWants(ctx); len(w) != 0 {
		t.Errorf("wants with triggers.enabled off = %+v", w)
	}
}

// TestGitHubLabelsMatchAddedLabels: `labels: [x]` on a labeled event matches
// the labels that event added, not every label the issue carries.
func TestGitHubLabelsMatchAddedLabels(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("x", githubDoc("x", SourceGitHubIssues, 1, "match:\n  action: labeled\n  labels: [x]\n"))
	seen := ghT0.Add(-time.Hour)
	h.m.JudgeGitHub(ctx, 1, GitHubListing{Issues: []GitHubIssue{
		ghIssue(1, "open", seen, []string{"x"}, nil), ghIssue(2, "open", seen, nil, nil),
	}})
	h.m.JudgeGitHub(ctx, 1, GitHubListing{Issues: []GitHubIssue{
		ghIssue(1, "open", ghT0.Add(time.Minute), []string{"x", "b"}, nil),
		ghIssue(2, "open", ghT0.Add(2*time.Minute), []string{"x"}, nil),
	}})
	outcomes := map[string]string{}
	for _, r := range h.ledger("x") {
		outcomes[strings.Join(strings.Split(r.EventID, ":")[:3], ":")] = r.Outcome
	}
	want := map[string]string{"github:issue:1": store.DeliveryFiltered, "github:issue:2": store.DeliveryFired}
	if !reflect.DeepEqual(outcomes, want) {
		t.Errorf("outcomes = %v, want %v", outcomes, want)
	}
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].body.Title != "gh labeled 2" {
		t.Errorf("replays = %+v", reqs)
	}
}

// TestGitHubAllowedActorsMatchAuthor: on a GitHub source allowed_actors is
// the author, compared without case (decision 31F).
func TestGitHubAllowedActorsMatchAuthor(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("prs", githubDoc("prs", SourceGitHubPRs, 1, "match:\n  action: opened\nallowed_actors: [alice]\n"))
	h.m.JudgeGitHub(ctx, 1, GitHubListing{})

	bob := ghPull(1, "open", ghT0.Add(time.Minute), false, false, nil)
	bob.Author, bob.CreatedAt = "bob", bob.UpdatedAt
	alice := ghPull(2, "open", ghT0.Add(2*time.Minute), false, false, nil)
	alice.Author, alice.CreatedAt = "Alice", alice.UpdatedAt
	h.m.JudgeGitHub(ctx, 1, GitHubListing{Pulls: []GitHubPull{bob, alice}})

	rows := h.ledger("prs")
	if len(rows) != 2 || rows[0].Outcome != store.DeliveryFiltered || rows[1].Outcome != store.DeliveryFired ||
		!strings.HasPrefix(rows[1].EventID, "github:pull:2:opened:") {
		t.Errorf("ledger = %+v, want bob filtered and Alice fired", rows)
	}
	j, err := h.m.Test(ctx, "prs", Event{"id": "x", "action": "opened", "author": "bob"})
	if err != nil || j.Matched || j.MatchMiss != "allowed_actors" || j.Outcome != store.DeliveryFiltered {
		t.Errorf("Test(bob) = %+v, %v", j, err)
	}
}
