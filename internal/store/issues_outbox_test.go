package store

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// The state write-back outbox (task 130.10).

func countOutbox(t *testing.T, s *Store, issueID int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM issue_sync_outbox WHERE issue_id = ?`, issueID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustTransition(t *testing.T, s *Store, id int64, a issuestate.Action, r issuestate.Reason, by issuestate.Actor) *Issue {
	t.Helper()
	iss, err := s.TransitionIssue(t.Context(), id, a, r, nil, by)
	if err != nil {
		t.Fatalf("TransitionIssue(%d, %s): %v", id, a, err)
	}
	return iss
}

func TestCloseOfAnImportedIssueEnqueuesItsWrite(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	var kicks atomic.Int32
	s.OnIssueOutboxEnqueued(func() { kicks.Add(1) })

	got := mustTransition(t, s, iss.ID, issuestate.Close, issuestate.NotPlanned, issuestate.Human)
	if kicks.Load() != 1 {
		t.Errorf("drain kicked %d times, want 1", kicks.Load())
	}
	rows, err := s.PendingIssueWrites(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending writes = %v, %v; want one", rows, err)
	}
	o := rows[0]
	if o.IssueID != iss.ID || o.ProjectID != p.ID || o.Op != OutboxOpSetState || o.Origin != issuestate.Human ||
		o.Desired != (IssueStateValue{State: "closed", Reason: "not_planned"}) ||
		o.Base != (IssueStateValue{State: "open"}) {
		t.Errorf("row = %+v", o)
	}
	if got.Sync == nil || got.Sync.ID != o.ID || got.Sync.Status != OutboxPending {
		t.Errorf("issue.Sync = %+v, want the pending row", got.Sync)
	}
}

func TestOutboxRowRollsBackWithTheStateChange(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	// A dangling duplicate_of fails the transaction after the guard: the
	// state, the event and the outbox row must all be absent.
	missing := int64(999)
	before, _ := s.MaxEventID(t.Context())
	_, err := s.TransitionIssue(t.Context(), iss.ID, issuestate.Close, issuestate.Duplicate, &missing, issuestate.Human)
	if !errors.Is(err, ErrInvalidDuplicateOf) {
		t.Fatalf("close = %v, want ErrInvalidDuplicateOf", err)
	}
	after, _ := s.MaxEventID(t.Context())
	if n := countOutbox(t, s, iss.ID); n != 0 || after != before {
		t.Errorf("after a failed close: %d outbox rows, events %d→%d", n, before, after)
	}
	// And inside one transaction: a commit that fails takes the row along.
	err = s.withTx(t.Context(), func(tx *sql.Tx) error {
		cur, err := getIssue(context.Background(), tx, iss.ID)
		if err != nil {
			return err
		}
		if _, err := enqueueStateWriteTx(t.Context(), tx, cur, issuestate.Closed, issuestate.Completed, nil,
			issuestate.Human, time.Now()); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil || countOutbox(t, s, iss.ID) != 0 {
		t.Errorf("a rolled-back transaction left %d rows (err %v)", countOutbox(t, s, iss.ID), err)
	}
}

func TestCloseReopenCloseCoalescesToOneWrite(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))

	mustTransition(t, s, iss.ID, issuestate.Close, "", issuestate.Human)
	mustTransition(t, s, iss.ID, issuestate.Reopen, "", issuestate.Human)
	// Close then reopen against base open sends nothing at all.
	if rows, _ := s.PendingIssueWrites(ctx, time.Time{}); len(rows) != 0 {
		t.Fatalf("close→reopen left %d pending writes, want 0", len(rows))
	}
	mustTransition(t, s, iss.ID, issuestate.Close, issuestate.NotPlanned, issuestate.Human)
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	if len(rows) != 1 || rows[0].Desired.Reason != "not_planned" || rows[0].Base.State != "open" {
		t.Fatalf("close→reopen→close = %+v, want one write from base open", rows)
	}
	all, _ := s.IssueWrites(ctx, iss.ID)
	if len(all) != 2 || all[0].Status != OutboxSuperseded {
		t.Errorf("history = %+v, want the first superseded", all)
	}
}

func TestLocalAndNonLiveIssuesNeverEnqueue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	local, err := s.CreateIssue(ctx, NewIssue{ProjectID: p.ID, Title: "local"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	mustTransition(t, s, local.ID, issuestate.Close, "", issuestate.Human)
	moved := mustUpsertRemote(t, s, remoteIn(p.ID, "I_2", 2))
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_2", RemoteStatusMoved, ""); err != nil {
		t.Fatal(err)
	}
	mustTransition(t, s, moved.ID, issuestate.Close, "", issuestate.Human)
	// Sync's own transitions are GitHub's value: never written back.
	synced := mustUpsertRemote(t, s, remoteIn(p.ID, "I_3", 3))
	mustTransition(t, s, synced.ID, issuestate.RemoteClosed, "", issuestate.Sync)
	for _, id := range []int64{local.ID, moved.ID, synced.ID} {
		if n := countOutbox(t, s, id); n != 0 {
			t.Errorf("issue %d has %d outbox rows, want 0", id, n)
		}
	}
}

func TestRefreshDefersToAPendingWrite(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	in := remoteIn(p.ID, "I_1", 1)
	iss := mustUpsertRemote(t, s, in)
	mustTransition(t, s, iss.ID, issuestate.Close, "", issuestate.Human)

	// The tick before the drain: GitHub still says open, new title.
	in.Title = "renamed upstream"
	got := mustUpsertRemote(t, s, in)
	if got.State != issuestate.Closed || got.Title != "renamed upstream" {
		t.Errorf("refresh with a pending write = %s %q, want closed and the new title", got.State, got.Title)
	}
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	if _, err := s.SettleIssueWrite(ctx, rows[0].ID, OutboxDone, "", &rows[0].Desired); err != nil {
		t.Fatal(err)
	}
	// With nothing pending GitHub is the authority again.
	if got := mustUpsertRemote(t, s, in); got.State != issuestate.Open {
		t.Errorf("refresh after the write settled = %s, want open", got.State)
	}
}

func TestSettleAndDeferAnnounceSync(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	mustTransition(t, s, iss.ID, issuestate.Close, "", issuestate.Human)
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	id := rows[0].ID

	after, _ := s.MaxEventID(ctx)
	next := time.Now().Add(time.Hour)
	for range 2 {
		if err := s.DeferIssueWrite(ctx, id, "disabled", next, false); err != nil {
			t.Fatal(err)
		}
	}
	evs, _ := s.ListEvents(ctx, EventFilter{AfterID: after, Types: []string{EventIssueUpdated}})
	if len(evs) != 1 {
		t.Errorf("deferring twice for one reason announced %d times, want 1", len(evs))
	}
	if due, _ := s.PendingIssueWrites(ctx, time.Now()); len(due) != 0 {
		t.Errorf("a deferred write is due early: %+v", due)
	}
	c, _ := s.CountIssueWrites(ctx, p.ID)
	if c != (IssueOutboxCounts{Pending: 1}) {
		t.Errorf("counts = %+v", c)
	}
	if ok, err := s.SettleIssueWrite(ctx, id, OutboxFailed, "no_write_scope", nil); !ok || err != nil {
		t.Fatalf("settle = %v, %v", ok, err)
	}
	if ok, _ := s.SettleIssueWrite(ctx, id, OutboxDone, "", nil); ok {
		t.Error("a settled write settled again")
	}
	c, _ = s.CountIssueWrites(ctx, p.ID)
	if c != (IssueOutboxCounts{Failed: 1}) {
		t.Errorf("counts after failing = %+v", c)
	}
	got, _ := s.GetIssue(ctx, iss.ID)
	if got.State != issuestate.Closed || got.Sync == nil || got.Sync.Status != OutboxFailed ||
		got.Sync.LastReason != "no_write_scope" {
		t.Errorf("a failed write = %s / %+v; want the local state kept and the failure on Sync", got.State, got.Sync)
	}
}

// TestRefreshKeepsTheStateOfAFailedWrite is the regression test for the
// 130 gate's scenario 8: a reopen that failed no_write_scope was undone by
// the next import, which still saw the issue closed on GitHub.
func TestRefreshKeepsTheStateOfAFailedWrite(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	in := remoteIn(p.ID, "I_1", 1)
	in.State, in.CloseReason = issuestate.Closed, issuestate.Completed
	iss := mustUpsertRemote(t, s, in)
	mustTransition(t, s, iss.ID, issuestate.Reopen, "", issuestate.Human)
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	if ok, err := s.SettleIssueWrite(ctx, rows[0].ID, OutboxFailed, "no_write_scope", nil); !ok || err != nil {
		t.Fatalf("settle = %v, %v", ok, err)
	}

	// GitHub still shows the base the write failed to replace.
	if got := mustUpsertRemote(t, s, in); got.State != issuestate.Open {
		t.Errorf("refresh after a failed write = %s, want the local open kept", got.State)
	}
	// Someone else moves it on GitHub: GitHub is the authority again.
	in.CloseReason = issuestate.NotPlanned
	if got := mustUpsertRemote(t, s, in); got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned {
		t.Errorf("refresh after GitHub moved = %s/%s, want closed/not_planned", got.State, got.CloseReason)
	}
}

func TestSettlingDoneRebasesAPendingSuccessor(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	mustTransition(t, s, iss.ID, issuestate.Close, "", issuestate.Human)
	first, _ := s.PendingIssueWrites(ctx, time.Time{})
	// The human reopens while the close is in flight.
	mustTransition(t, s, iss.ID, issuestate.Reopen, "", issuestate.Human)
	mustTransition(t, s, iss.ID, issuestate.Close, issuestate.NotPlanned, issuestate.Human)
	if _, err := s.SettleIssueWrite(ctx, first[0].ID, OutboxDone, "", &first[0].Desired); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	if len(rows) != 1 || rows[0].Base != first[0].Desired {
		t.Errorf("successor = %+v, want it based on the write that landed", rows)
	}
}

func TestAnUndoneWriteThatLandedIsWrittenBack(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	mustTransition(t, s, iss.ID, issuestate.Close, "", issuestate.Human)
	first, _ := s.PendingIssueWrites(ctx, time.Time{})
	// Reopened while the close was in flight: no write is pending...
	mustTransition(t, s, iss.ID, issuestate.Reopen, "", issuestate.Human)
	if rows, _ := s.PendingIssueWrites(ctx, time.Time{}); len(rows) != 0 {
		t.Fatalf("pending = %+v", rows)
	}
	// ...until the close turns out to have landed.
	if _, err := s.SettleIssueWrite(ctx, first[0].ID, OutboxDone, "", &first[0].Desired); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.PendingIssueWrites(ctx, time.Time{})
	if len(rows) != 1 || rows[0].Desired.State != "open" || rows[0].Base != first[0].Desired {
		t.Errorf("after the undone close landed: %+v, want a reopen based on it", rows)
	}
}
