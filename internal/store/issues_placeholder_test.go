package store

import (
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Backfill placeholders (task 130 decision 22.3): a remote keyed
// "legacy:{repo}#{number}" with synced_at NULL is the one kind of row the
// importer may match by number.

// placeholder writes a backfill-shaped remote: an imported issue whose
// remote is keyed legacy and was never synced, as migration 0040 leaves it.
func placeholder(t *testing.T, s *Store, projectID int64, repo string, number int, state issuestate.State) *Issue {
	t.Helper()
	in := remoteIn(projectID, "", number)
	in.RemoteKey = LegacyRemoteKeyPrefix + repo + "#" + strconv.Itoa(number)
	in.Repo, in.Title, in.State, in.RemoteJSON = repo, "snapshot title", state, ""
	if state == issuestate.Closed {
		in.CloseReason = issuestate.Completed
	}
	iss := mustUpsertRemote(t, s, in)
	if _, err := s.db.ExecContext(t.Context(), `
		UPDATE issue_remotes SET synced_at = NULL, remote_updated_at = NULL WHERE issue_id = ?`, iss.ID); err != nil {
		t.Fatal(err)
	}
	return iss
}

func TestAdoptPlaceholderRemoteRekeysAndAdoptsState(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	wasOpen := placeholder(t, s, p.ID, "o/r", 1, issuestate.Open)
	wasClosed := placeholder(t, s, p.ID, "o/r", 2, issuestate.Closed)

	closed := remoteIn(p.ID, "I_1", 1)
	closed.Repo, closed.Title, closed.State, closed.CloseReason = "O/R", "GitHub title", issuestate.Closed, issuestate.NotPlanned
	closed.Labels = []string{"bug"}
	opened := remoteIn(p.ID, "I_2", 2)
	for _, in := range []RemoteIssue{closed, opened} {
		adopted, err := s.AdoptPlaceholderRemote(ctx, in, issuestate.Sync)
		if err != nil || !adopted {
			t.Fatalf("adopt %s = %v, %v; want adopted", in.RemoteKey, adopted, err)
		}
	}

	got, err := s.GetIssue(ctx, wasOpen.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote.RemoteKey != "I_1" || got.Remote.SyncedAt == nil || got.Title != "GitHub title" ||
		got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned || len(got.Labels) != 1 {
		t.Errorf("open placeholder after adopting a closed issue: %+v / %+v", got, got.Remote)
	}
	got, err = s.GetIssue(ctx, wasClosed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote.RemoteKey != "I_2" || got.Remote.SyncedAt == nil || got.State != issuestate.Open || got.ClosedAt != nil {
		t.Errorf("closed placeholder after adopting an open issue: %+v / %+v", got, got.Remote)
	}
	for _, id := range []int64{wasOpen.ID, wasClosed.ID} {
		if ws, err := s.IssueWrites(ctx, id); err != nil || len(ws) != 0 {
			t.Errorf("issue %d: adoption queued write-back %+v, %v", id, ws, err)
		}
	}

	// Adopted once: synced now, so the same number never matches again.
	again := remoteIn(p.ID, "I_other", 1)
	if adopted, err := s.AdoptPlaceholderRemote(ctx, again, issuestate.Sync); err != nil || adopted {
		t.Errorf("a synced row was matched by number: %v, %v", adopted, err)
	}
}

func TestAdoptPlaceholderRemoteMatchesOnlyPlaceholders(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	other := testProject(t, s, "other")

	// A real node id row, even one never synced, is never matched by number
	// (decision 15.5).
	realRow := mustUpsertRemote(t, s, remoteIn(p.ID, "I_real", 1))
	if _, err := s.db.ExecContext(ctx, `UPDATE issue_remotes SET synced_at = NULL WHERE issue_id = ?`, realRow.ID); err != nil {
		t.Fatal(err)
	}
	// A placeholder of another repository, and one in another project.
	placeholder(t, s, p.ID, "o/elsewhere", 2, issuestate.Open)
	placeholder(t, s, other.ID, "o/r", 3, issuestate.Open)
	// A placeholder whose node id is already known keeps waiting.
	known := placeholder(t, s, p.ID, "o/r", 4, issuestate.Open)
	mustUpsertRemote(t, s, remoteIn(p.ID, "I_4", 4))

	for _, in := range []RemoteIssue{
		remoteIn(p.ID, "I_new", 1), remoteIn(p.ID, "I_2", 2), remoteIn(p.ID, "I_3", 3), remoteIn(p.ID, "I_4", 4),
	} {
		if adopted, err := s.AdoptPlaceholderRemote(ctx, in, issuestate.Sync); err != nil || adopted {
			t.Errorf("%s #%d adopted = %v, %v; want nothing matched", in.RemoteKey, in.Number, adopted, err)
		}
	}
	got, err := s.GetIssue(ctx, known.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote.SyncedAt != nil || got.Remote.RemoteKey != LegacyRemoteKeyPrefix+"o/r#4" {
		t.Errorf("a placeholder was touched when its key was known: %+v", got.Remote)
	}
	got, err = s.GetIssue(ctx, realRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote.RemoteKey != "I_real" {
		t.Errorf("a real node id row was re-keyed: %+v", got.Remote)
	}
}
