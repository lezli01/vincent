package store

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// The outbox for a provider other than GitHub (task 142.5): any provider
// internal/issueprovider declares as writing back enqueues, and the
// duplicate-of lookup stays within the closing issue's provider and repo.

func TestCloseOfAJiraIssueEnqueuesItsWrite(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "10001", "PROJ", 1))

	mustTransition(t, s, iss.ID, issuestate.Close, issuestate.Completed, issuestate.Human)
	rows, err := s.PendingIssueWrites(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending writes = %v, %v; want one", rows, err)
	}
	if o := rows[0]; o.Op != OutboxOpSetState || o.Desired != RemoteStateOf(issuestate.Closed, issuestate.Completed) ||
		o.Base != RemoteStateOf(issuestate.Open, "") {
		t.Errorf("row = %+v", o)
	}

	// Sync's own transition is never written back.
	other := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "10002", "PROJ", 2))
	mustTransition(t, s, other.ID, issuestate.Close, issuestate.Completed, issuestate.Sync)
	if n := countOutbox(t, s, other.ID); n != 0 {
		t.Errorf("sync's close enqueued %d rows, want 0", n)
	}
}

func TestUnknownProviderStaysLocal(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	in := remoteIn(p.ID, "G_1", 1)
	in.Provider = "gitlab"
	iss := mustUpsertRemote(t, s, in)
	mustTransition(t, s, iss.ID, issuestate.Close, issuestate.Completed, issuestate.Human)
	if n := countOutbox(t, s, iss.ID); n != 0 {
		t.Errorf("an unknown provider enqueued %d rows, want 0", n)
	}
}

func TestDuplicateOfStaysWithinProviderAndRepo(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	sameTarget := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "20007", "PROJ", 7))
	otherRepo := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "20008", "OPS", 8))
	ghTarget := mustUpsertRemote(t, s, remoteIn(p.ID, "I_9", 9))

	for _, tc := range []struct {
		name   string
		target int64
		want   int
	}{
		{"same provider and repo", sameTarget.ID, 7},
		{"a different repo", otherRepo.ID, 0},
		{"a GitHub target", ghTarget.ID, 0},
	} {
		iss := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "3"+tc.name, "proj", 100))
		if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Duplicate, &tc.target, issuestate.Human); err != nil {
			t.Fatalf("%s: close: %v", tc.name, err)
		}
		writes, err := s.IssueWrites(ctx, iss.ID)
		if err != nil || len(writes) != 1 {
			t.Fatalf("%s: writes = %+v, %v", tc.name, writes, err)
		}
		if got := writes[0].Desired.DuplicateOf; got != tc.want {
			t.Errorf("%s: DuplicateOf = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRequestIssueSyncWritesItsProvider(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	if err := s.RequestIssueSync(ctx, p.ID, "jira"); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetIssueSyncState(ctx, p.ID)
	if err != nil || st.Provider != "jira" {
		t.Errorf("state = %+v, %v; want provider jira", st, err)
	}
}
