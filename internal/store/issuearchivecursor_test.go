package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestIssueArchiveCursorRoundTrips (task 143.3): a fresh database's cursor
// is 0, and a put is what the next get reads.
func TestIssueArchiveCursorRoundTrips(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	if c, err := s.GetIssueArchiveCursor(ctx); err != nil || c != 0 {
		t.Fatalf("fresh cursor = %d, %v; want 0", c, err)
	}
	for _, want := range []int64{42, 43} {
		if err := s.PutIssueArchiveCursor(ctx, want); err != nil {
			t.Fatalf("put %d: %v", want, err)
		}
		if c, err := s.GetIssueArchiveCursor(ctx); err != nil || c != want {
			t.Fatalf("cursor = %d, %v; want %d", c, err, want)
		}
	}
}

// TestMigration0053SeedsAtTheNewestEvent is task 143 decision 7: an upgrade
// seeds the cursor at MAX(events.id), so nothing already in the table —
// closed issues with settled tasks included — is ever acted on.
func TestMigration0053SeedsAtTheNewestEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 52)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		ts := formatTime(time.Now())
		for range 3 {
			if _, err := db.Exec(`INSERT INTO events (ts, type, payload_json) VALUES (?, 'issue.state_changed', '{"id":1,"from":"open","to":"closed"}')`, ts); err != nil {
				t.Fatalf("seed event: %v", err)
			}
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating to 0053): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	newest, err := s.MaxEventID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c, err := s.GetIssueArchiveCursor(t.Context()); err != nil || c != newest || c == 0 {
		t.Fatalf("seeded cursor = %d, %v; want MAX(events.id) %d", c, err, newest)
	}
}

// TestIssueHandoffClaimed (task 143 decision 10): an unresolved or waiting
// item claims its issue; released, returned and delivered ones do not.
func TestIssueHandoffClaimed(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss, _ := handedOffIssue(t, s, p.ID, "claimed", "s1")
	claimed := func() bool {
		t.Helper()
		ok, err := s.IssueHandoffClaimed(ctx, iss.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if claimed() {
		t.Fatal("an issue with no handoff reads claimed")
	}
	h, _ := mustHandoff(t, s, p.ID, "h1", iss.ID)
	if !claimed() {
		t.Fatal("an unresolved item does not claim its issue")
	}
	setOutcome(t, s, h, iss.ID, HandoffWaiting)
	if !claimed() {
		t.Fatal("a waiting item does not claim its issue")
	}
	setOutcome(t, s, h, iss.ID, HandoffReleased)
	if claimed() {
		t.Fatal("a released item still claims its issue")
	}
}

// TestIssueStateMove pins the one reading of an issue state change.
func TestIssueStateMove(t *testing.T) {
	cases := []struct {
		typ, from, to string
		want          string
		ok            bool
	}{
		{EventIssueStateChanged, "open", "closed", "closed", true},
		{EventIssueStateChanged, "closed", "open", "open", true},
		{EventIssueUpdated, "open", "closed", "closed", true},
		{EventIssueUpdated, "", "", "", false},
		{EventIssueUpdated, "open", "open", "", false},
		{EventIssueLabelsChanged, "open", "closed", "", false},
	}
	for _, c := range cases {
		got, ok := IssueStateMove(c.typ, c.from, c.to)
		if got != c.want || ok != c.ok {
			t.Errorf("IssueStateMove(%s, %q, %q) = %q, %v; want %q, %v", c.typ, c.from, c.to, got, ok, c.want, c.ok)
		}
	}
}
