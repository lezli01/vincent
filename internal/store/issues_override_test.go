package store

import (
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// A resolution a post-function overrode after a done write (task 142.22,
// #953): the importer adopts the provider's reason and names the override
// on the write.

// settledClose closes an imported open issue as not_planned by a human and
// settles the write done, as the drain does after a 204. It returns the
// refresh input the provider would then answer, still open.
func settledClose(t *testing.T, s *Store) (*Issue, RemoteIssue) {
	t.Helper()
	ctx := t.Context()
	p := testProject(t, s, "p")
	in := remoteIn(p.ID, "I_1", 1)
	iss := mustUpsertRemote(t, s, in)
	mustTransition(t, s, iss.ID, issuestate.Close, issuestate.NotPlanned, issuestate.Human)
	rows, err := s.PendingIssueWrites(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending writes = %v, %v; want one", rows, err)
	}
	if ok, err := s.SettleIssueWrite(ctx, rows[0].ID, OutboxDone, "", &rows[0].Desired); !ok || err != nil {
		t.Fatalf("settle = %v, %v", ok, err)
	}
	return iss, in
}

func newestWriteOf(t *testing.T, s *Store, issueID int64) *IssueOutbox {
	t.Helper()
	writes, err := s.IssueWrites(t.Context(), issueID)
	if err != nil || len(writes) == 0 {
		t.Fatalf("writes of %d = %v, %v", issueID, writes, err)
	}
	return writes[len(writes)-1]
}

func TestRefreshMarksAnOverriddenResolution(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	iss, in := settledClose(t, s)
	after, _ := s.MaxEventID(ctx)

	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	got := mustUpsertRemote(t, s, in)
	if got.State != issuestate.Closed || got.CloseReason != issuestate.Completed {
		t.Errorf("issue = %s/%s, want the provider's closed/completed", got.State, got.CloseReason)
	}
	w := newestWriteOf(t, s, iss.ID)
	if w.Status != OutboxConflict || w.LastReason != OutboxReasonResolutionOverridden {
		t.Errorf("write = %s/%s, want conflict/%s", w.Status, w.LastReason, OutboxReasonResolutionOverridden)
	}
	if got.Sync == nil || got.Sync.ID != w.ID || got.Sync.Status != OutboxConflict {
		t.Errorf("issue.Sync = %+v, want the re-marked row", got.Sync)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after, Types: []string{EventIssueUpdated}})
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	body := payloadOf(t, evs[0])
	if body["by"] != string(issuestate.Sync) || body["reason_from"] != string(issuestate.NotPlanned) ||
		body["reason_to"] != string(issuestate.Completed) {
		t.Errorf("payload = %s, want by sync, reason_from not_planned, reason_to completed", evs[0].Payload)
	}
	if _, ok := body["from"]; ok {
		t.Errorf("payload = %s: a closed→closed refresh carries no state from/to", evs[0].Payload)
	}
	var changed []string
	for _, c := range body["changed"].([]any) {
		changed = append(changed, c.(string))
	}
	if !slices.Equal(changed, []string{"close_reason", "sync"}) {
		t.Errorf("changed = %v, want [close_reason sync]", changed)
	}
	c, _ := s.CountIssueWrites(ctx, got.ProjectID)
	if c != (IssueOutboxCounts{Conflict: 1}) {
		t.Errorf("counts = %+v, want the one conflict", c)
	}

	// The next refresh, reading the same, changes nothing and writes nothing.
	after, _ = s.MaxEventID(ctx)
	mustUpsertRemote(t, s, in)
	if evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after}); len(evs) != 0 {
		t.Errorf("a settled override announced again: %d events", len(evs))
	}
	if n := countOutbox(t, s, iss.ID); n != 1 {
		t.Errorf("outbox rows = %d, want the one: sync enqueues none", n)
	}
}

func TestRefreshMatchingADoneWriteLeavesItDone(t *testing.T) {
	s := openTest(t)
	iss, in := settledClose(t, s)
	in.State, in.CloseReason = issuestate.Closed, issuestate.NotPlanned
	got := mustUpsertRemote(t, s, in)
	if got.CloseReason != issuestate.NotPlanned {
		t.Errorf("close reason = %s, want not_planned", got.CloseReason)
	}
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone || w.LastReason != "" {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
}

// A reopen after a done close is someone changing the issue, not an
// override: a plain refresh.
func TestRefreshReopenedAfterADoneCloseIsNoOverride(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	iss, in := settledClose(t, s)
	after, _ := s.MaxEventID(ctx)
	got := mustUpsertRemote(t, s, in) // still open on the provider's side
	if got.State != issuestate.Open {
		t.Errorf("state = %s, want the provider's open", got.State)
	}
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after, Types: []string{EventIssueUpdated}})
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	body := payloadOf(t, evs[0])
	if body["from"] != "closed" || body["to"] != "open" {
		t.Errorf("payload = %s, want from closed to open", evs[0].Payload)
	}
	if _, ok := body["reason_from"]; ok {
		t.Errorf("payload = %s: a state change carries no reason delta", evs[0].Payload)
	}
}

// A listing that may trail the write, not confirmed by id, is held rather
// than adopted (task 142.13 decision 1) — and so never marked.
func TestUnconfirmedReadOfADoneCloseIsHeldNotMarked(t *testing.T) {
	s := openTest(t)
	iss, in := settledClose(t, s)
	in.State, in.CloseReason, in.Unconfirmed = issuestate.Closed, issuestate.Completed, true
	got := mustUpsertRemote(t, s, in)
	if got.CloseReason != issuestate.NotPlanned {
		t.Errorf("close reason = %s, want the local not_planned held", got.CloseReason)
	}
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
}

func TestPendingAndFailedWritesKeepTheirHold(t *testing.T) {
	for _, status := range []string{OutboxPending, OutboxFailed} {
		t.Run(status, func(t *testing.T) {
			s := openTest(t)
			ctx := t.Context()
			p := testProject(t, s, "p")
			in := remoteIn(p.ID, "I_1", 1)
			iss := mustUpsertRemote(t, s, in)
			mustTransition(t, s, iss.ID, issuestate.Close, issuestate.NotPlanned, issuestate.Human)
			rows, _ := s.PendingIssueWrites(ctx, time.Time{})
			if status == OutboxFailed {
				if ok, err := s.SettleIssueWrite(ctx, rows[0].ID, OutboxFailed, "no_write_scope", nil); !ok || err != nil {
					t.Fatalf("settle = %v, %v", ok, err)
				}
			}
			// Pending holds whatever the provider reads; failed holds while
			// it shows the write's base (open). Neither is marked.
			read := in
			if status == OutboxPending {
				read.State, read.CloseReason = issuestate.Closed, issuestate.Completed
			}
			got := mustUpsertRemote(t, s, read)
			if got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned {
				t.Errorf("issue = %s/%s, want the local closed/not_planned held", got.State, got.CloseReason)
			}
			if w := newestWriteOf(t, s, iss.ID); w.Status != status || w.LastReason == OutboxReasonResolutionOverridden {
				t.Errorf("write = %s/%s, want %s untouched", w.Status, w.LastReason, status)
			}
		})
	}
}

// Every closed→closed reason move carries the delta, whatever its cause —
// here a re-resolve upstream with no write of vincent's at all.
func TestClosedReasonMoveCarriesTheDelta(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	in := remoteIn(p.ID, "I_1", 1)
	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	mustUpsertRemote(t, s, in)
	after, _ := s.MaxEventID(ctx)
	in.CloseReason = issuestate.Duplicate
	mustUpsertRemote(t, s, in)
	evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after, Types: []string{EventIssueUpdated}})
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	body := payloadOf(t, evs[0])
	if body["reason_from"] != string(issuestate.Completed) || body["reason_to"] != string(issuestate.Duplicate) {
		t.Errorf("payload = %s, want reason_from completed, reason_to duplicate", evs[0].Payload)
	}
}

// Only the first adopted read after the write can show an override (review
// F1 on #956). A close the tracker honored, then re-resolved there by a
// person, is a plain remote change: the write stays done.
func TestRefreshReResolvedAfterAnHonoredCloseIsNoOverride(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	iss, in := settledClose(t, s)
	in.State, in.CloseReason = issuestate.Closed, issuestate.NotPlanned
	mustUpsertRemote(t, s, in) // honored

	after, _ := s.MaxEventID(ctx)
	in.CloseReason = issuestate.Completed
	got := mustUpsertRemote(t, s, in)
	if got.CloseReason != issuestate.Completed {
		t.Errorf("close reason = %s, want the tracker's completed", got.CloseReason)
	}
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone || w.LastReason != "" {
		t.Errorf("write = %s/%s, want done: the close was honored before the re-resolve", w.Status, w.LastReason)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after, Types: []string{EventIssueUpdated}})
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	if slices.Contains(changedOf(t, evs[0]), "sync") {
		t.Errorf("payload = %s: a re-resolve names no sync change", evs[0].Payload)
	}
	if c, _ := s.CountIssueWrites(ctx, got.ProjectID); c.Conflict != 0 {
		t.Errorf("counts = %+v, want no conflict", c)
	}
}

// A reopen in the tracker, then a close there with another reason, is two
// plain remote changes: neither marks the write.
func TestRefreshReopenThenCloseAfterADoneCloseIsNoOverride(t *testing.T) {
	s := openTest(t)
	iss, in := settledClose(t, s)
	in.State, in.CloseReason = issuestate.Closed, issuestate.NotPlanned
	mustUpsertRemote(t, s, in) // honored
	in.State, in.CloseReason = issuestate.Open, ""
	mustUpsertRemote(t, s, in)
	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	got := mustUpsertRemote(t, s, in)
	if got.State != issuestate.Closed || got.CloseReason != issuestate.Completed {
		t.Errorf("issue = %s/%s, want the tracker's closed/completed", got.State, got.CloseReason)
	}
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone || w.LastReason != "" {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
}

// The same when the very first read after the write is the reopen: the
// write was read back then, so the later close is not its override.
func TestRefreshCloseAfterAReopenedFirstReadIsNoOverride(t *testing.T) {
	s := openTest(t)
	iss, in := settledClose(t, s)
	mustUpsertRemote(t, s, in) // still open: reopened there
	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	mustUpsertRemote(t, s, in)
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone || w.LastReason != "" {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
}

// A done row settled before read_back_at existed, whose issue has already
// adopted another reason, is not flagged on the first refresh after an
// upgrade: nothing changed, and nothing is announced.
func TestRefreshOfAnAlreadyAdoptedReasonIsNoOverride(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	iss, in := settledClose(t, s)
	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	// As a pre-upgrade daemon left it: the issue adopted completed, and the
	// done row was never read back.
	if _, err := s.db.ExecContext(ctx, `UPDATE issues SET close_reason = ? WHERE id = ?`,
		string(issuestate.Completed), iss.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.MaxEventID(ctx)
	mustUpsertRemote(t, s, in)
	if w := newestWriteOf(t, s, iss.ID); w.Status != OutboxDone || w.LastReason != "" {
		t.Errorf("write = %s/%s, want done", w.Status, w.LastReason)
	}
	if evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after}); len(evs) != 0 {
		t.Errorf("an unchanged refresh announced %d events", len(evs))
	}
}

func changedOf(t *testing.T, ev Event) []string {
	t.Helper()
	var changed []string
	for _, c := range payloadOf(t, ev)["changed"].([]any) {
		changed = append(changed, c.(string))
	}
	return changed
}
