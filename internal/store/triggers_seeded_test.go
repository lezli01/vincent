package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTriggerSeededDedupesButDoesNotCount: a `seeded` row makes its key
// delivered — the backlog a seed saw must never fire — and never counts
// against `limits.max_per_hour`, because recording is not firing (task 096
// decision 31B).
func TestTriggerSeededDedupesButDoesNotCount(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Now()
	for i, key := range []string{"s1", "s2", "s3"} {
		if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
			TriggerID: "t1", EventID: key, DedupeKey: key, Outcome: DeliverySeeded,
			CreatedAt: now.Add(-time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("RecordTriggerDelivery: %v", err)
		}
	}
	if got, err := s.TriggerKeyDelivered(ctx, "t1", "s2"); err != nil || !got {
		t.Errorf("seeded key delivered = %v, %v; want true", got, err)
	}
	if got, _ := s.TriggerKeyDelivered(ctx, "t2", "s2"); got {
		t.Error("a seeded key leaked to another trigger")
	}
	if n, err := s.CountTriggerFiredSince(ctx, "t1", now.Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("CountTriggerFiredSince = %d, %v; want 0 — seeded rows are not fires", n, err)
	}
}

// TestMigration0030PreservesDeliveries applies every migration up to 0029,
// writes ledger rows, then lets Open run 0030's rebuild: the rows survive with
// their ids, the three indexes are back, and the new CHECK admits `seeded`
// while still refusing an unknown outcome.
func TestMigration0030PreservesDeliveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		v, err := migrationVersion(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if v > 29 {
			continue
		}
		if err := applyMigration(db, e.Name(), v); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
	}
	ts := formatTime(time.Now())
	if _, err := db.Exec(`INSERT INTO trigger_deliveries (id, trigger_id, event_id, dedupe_key, outcome, detail, created_at)
		VALUES (7, 't1', 'e1', 'k1', 'fired', '', ?), (9, 't1', 'e2', 'k2', 'refused', '{"error":{}}', ?)`, ts, ts); err != nil {
		t.Fatalf("seed 0029 rows: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO trigger_deliveries (trigger_id, outcome, created_at) VALUES ('t1', 'seeded', ?)`, ts); err == nil {
		t.Fatal("0029's CHECK admitted `seeded`; the test is not exercising the rebuild")
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating to 0030): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	rows, err := s.ListTriggerDeliveries(ctx, "t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 9 || rows[0].Detail != `{"error":{}}` || rows[1].ID != 7 {
		t.Fatalf("rows after rebuild = %+v", rows)
	}
	idx := func() []string {
		var out []string
		r, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'trigger_deliveries' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var n string
			if err := r.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}()
	// 0033 rebuilt the table again and added its group index (task 122).
	if got := strings.Join(idx, ","); got != "idx_trigger_deliveries_age,idx_trigger_deliveries_created,idx_trigger_deliveries_group,idx_trigger_deliveries_key" {
		t.Errorf("indexes after rebuild = %s", got)
	}
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{TriggerID: "t1", Outcome: DeliverySeeded}); err != nil {
		t.Errorf("seeded refused after 0030: %v", err)
	}
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{TriggerID: "t1", Outcome: "bogus"}); err == nil {
		t.Error("an unknown outcome was admitted after the rebuild")
	}
	// A new row continues the id sequence rather than reusing a copied id.
	if row, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{TriggerID: "t1", Outcome: DeliveryFired}); err != nil || row.ID <= 9 {
		t.Errorf("new row id = %+v, %v; want > 9", row, err)
	}
}

// TestFindTaskForBranch: no match, an archived match ignored, and the newest
// of several unarchived rows.
func TestFindTaskForBranch(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	if _, err := s.FindTaskForBranch(ctx, p.ID, "vincent/none"); err == nil {
		t.Fatal("found a task on a branch nobody holds")
	}
	archived := newTask(p.ID, "old", TaskArchived)
	archived.BranchName = "vincent/x"
	at := time.Now()
	archived.ArchivedAt = &at
	if err := s.CreateTask(ctx, archived, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindTaskForBranch(ctx, p.ID, "vincent/x"); err == nil {
		t.Error("an archived task was chosen as a reaction target")
	}
	live := newTask(p.ID, "live", TaskBlocked)
	live.BranchName = "vincent/x"
	if err := s.CreateTask(ctx, live, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindTaskForBranch(ctx, p.ID, "vincent/x")
	if err != nil || got.ID != live.ID {
		t.Errorf("FindTaskForBranch = %+v, %v; want task %d", got, err, live.ID)
	}
}
