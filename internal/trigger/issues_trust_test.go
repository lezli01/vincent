package trigger

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestIssuesLabelTrustIsProviderAware pins task 142 open question 11,
// settled by #908: on `type: issues` a label change by sync is trusted from
// GitHub and untrusted from Jira, so a Jira one goes through allowed_actors —
// and, on a trigger that names none, fails closed with an allowed_actors
// miss. GitHub's rows are what they were before the decision.
func TestIssuesLabelTrustIsProviderAware(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	// listed can match every action below by sync, for alice only.
	h.write("listed", issuesDoc("listed",
		"match:\n  action: [labeled, unlabeled, closed]\nallowed_actors: [alice]\n"))
	// unlisted loaded on labeled/unlabeled's exemption.
	h.write("unlisted", issuesDoc("unlisted", "match:\n  action: [labeled, unlabeled]\n"))
	// local matches closed without an allowlist only because it leaves sync
	// out of match.by — closed by sync could not load without one.
	h.write("local", issuesDoc("local", "match:\n  action: closed\n  by: human\n"))

	type row struct {
		trigger, provider, action, by, author string
		fires                                 bool
		miss                                  string // the expected MatchMiss when !fires
	}
	var rows []row
	for _, provider := range []string{issueprovider.GitHub, issueprovider.Jira} {
		for _, action := range []string{"labeled", "unlabeled", "closed"} {
			label := action != "closed"
			for _, by := range []string{"sync", "human"} {
				sync := by == "sync"
				// With allowed_actors: a sync change needs a listed author
				// whatever the provider; a human's is never checked.
				rows = append(rows,
					row{"listed", provider, action, by, "Alice", true, ""},
					row{"listed", provider, action, by, "bob", !sync, "allowed_actors"})
				if label {
					// Without: GitHub's sync label change is trusted as
					// before; Jira's fails closed on any author.
					fires := !sync || provider == issueprovider.GitHub
					rows = append(rows,
						row{"unlisted", provider, action, by, "alice", fires, "allowed_actors"},
						row{"unlisted", provider, action, by, "bob", fires, "allowed_actors"})
				} else {
					// closed is untrusted from both trackers, so the only
					// loadable unlisted trigger leaves sync out of match.by.
					rows = append(rows,
						row{"local", provider, action, by, "bob", !sync, "by"},
						row{"unlisted", provider, action, by, "bob", false, "action"})
				}
			}
		}
	}
	for _, r := range rows {
		name := strings.Join([]string{r.trigger, r.provider, r.action, r.by, r.author}, "/")
		t.Run(name, func(t *testing.T) {
			ev := Event{
				"id": "e:" + name, "action": r.action, "by": r.by, "author": r.author,
				"labels": []any{"x"},
				"Issue":  map[string]any{"Source": map[string]any{"Provider": r.provider}},
			}
			j, err := h.m.Test(ctx, r.trigger, ev)
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			if j.Matched != r.fires {
				t.Fatalf("matched = %v (miss %q), want %v", j.Matched, j.MatchMiss, r.fires)
			}
			if !r.fires && !strings.HasPrefix(j.MatchMiss, r.miss) {
				t.Errorf("miss = %q, want %q", j.MatchMiss, r.miss)
			}
			if !r.fires && j.Outcome != store.DeliveryFiltered {
				t.Errorf("outcome = %q, want filtered", j.Outcome)
			}
		})
	}
}

// TestJiraSyncLabelFailsClosed drives the decision through the real poll: an
// imported Jira issue's label change by sync is filtered on a trigger with
// no allowed_actors, and recorded in the ledger as filtered, while the same
// change on a GitHub issue fires as it always has.
func TestJiraSyncLabelFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	upsert := func(provider, key string, labels []string) *store.Issue {
		t.Helper()
		iss, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
			ProjectID: 1, Provider: provider, RemoteKey: key, Repo: "o/r", Number: 7,
			Title: "imported " + key, Author: "outsider", State: issuestate.Open, Labels: labels,
		}, issuestate.Sync)
		if err != nil {
			t.Fatalf("UpsertRemoteIssue: %v", err)
		}
		return iss
	}
	gh := upsert(issueprovider.GitHub, "NODE_1", []string{"a"})
	jira := upsert(issueprovider.Jira, "10007", []string{"a"})
	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n"))
	h.m.pollIssues(ctx)
	upsert(issueprovider.GitHub, "NODE_1", []string{"a", "ready"})
	upsert(issueprovider.Jira, "10007", []string{"a", "ready"})
	h.m.pollIssues(ctx)

	outcomes := map[int64]string{}
	for _, r := range h.ledger("lbl") {
		id, ok := parseIssueEventID(r.EventID)
		if !ok {
			t.Fatalf("ledger event id %q is not an issue event's", r.EventID)
		}
		outcomes[id] = r.Outcome
	}
	if len(outcomes) != 2 || outcomes[gh.ID] != store.DeliveryFired || outcomes[jira.ID] != store.DeliveryFiltered {
		t.Errorf("outcomes = %v, want GitHub %d fired and Jira %d filtered", outcomes, gh.ID, jira.ID)
	}
	if n := len(h.api.requests()); n != 1 {
		t.Errorf("replays = %d, want the GitHub one only", n)
	}
}

// TestJiraLabelNote: an issues trigger that loads on labeled/unlabeled's
// GitHub exemption still loads — no existing trigger is refused by #908 —
// and earns the non-fatal note that a Jira label change will not fire it.
// Listing allowed_actors, or leaving sync out of match.by, clears the note.
func TestJiraLabelNote(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		note        bool
	}{
		{"labeled", "match:\n  action: labeled\n", true},
		{"unlabeled list", "match:\n  action: [unlabeled]\n", true},
		{"by sync", "match:\n  action: labeled\n  by: [sync, human]\n", true},
		{"allowed_actors", "match:\n  action: labeled\nallowed_actors: [alice]\n", false},
		{"no sync", "match:\n  action: labeled\n  by: human\n", false},
		{"closed listed", "match:\n  action: closed\nallowed_actors: [alice]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, errs := Parse([]byte(issuesDoc("t1", tc.extra)), "t1")
			if len(errs) > 0 {
				t.Fatalf("refused: %v", errs)
			}
			var notes []string
			for _, w := range Deprecations(d) {
				if strings.Contains(w, "Jira") {
					notes = append(notes, w)
				}
			}
			if (len(notes) == 1) != tc.note {
				t.Errorf("Jira notes = %v, want note %v", notes, tc.note)
			}
		})
	}
}
