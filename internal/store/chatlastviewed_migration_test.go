package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration0064StartsHistoryRead (review F2): a chat that existed before
// last_viewed_at — finished turns, a turn recovery left without an end, or
// archived — reads unread = false after the upgrade, and a turn that ends
// after it still marks it unread.
func TestMigration0064StartsHistoryRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 63)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		chat := func(id, state string) string {
			return `INSERT INTO chats (id, project_id, title, state, agent, model, effort, permission_mode, branch, base_branch, created_at, updated_at)
				VALUES (` + id + `, 1, 'c` + id + `', '` + state + `', 'claude', 'opus', 'high', 'full-auto', 'b` + id + `', 'main', '` + backfillAt(1) + `', '` + backfillAt(1) + `')`
		}
		for _, q := range []string{
			`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
				VALUES (1, 'p1', '/p1', 'main', '` + backfillAt(0) + `', '` + backfillAt(0) + `')`,
			chat("1", "idle"),
			chat("2", "archived"),
			chat("3", "idle"),
			chat("4", "idle"),
			// Chat 1: two finished turns, the later one failed.
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at, ended_at)
				VALUES (1, 1, 'a', 'done', '` + backfillAt(2) + `', '` + backfillAt(3) + `')`,
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at, ended_at)
				VALUES (1, 2, 'b', 'failed', '` + backfillAt(4) + `', '` + backfillAt(5) + `')`,
			// Chat 2: archived with a finished turn.
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at, ended_at)
				VALUES (2, 1, 'a', 'done', '` + backfillAt(2) + `', '` + backfillAt(3) + `')`,
			// Chat 3: interrupted with no ended_at.
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at)
				VALUES (3, 1, 'a', 'interrupted', '` + backfillAt(2) + `')`,
			// Chat 4: never had a turn.
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 63 -> 64): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, id := range []int64{1, 2, 3, 4} {
		c, err := s.GetChat(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Unread {
			t.Errorf("chat %d is unread after the upgrade, want its history read", id)
		}
	}

	// A turn that ends after the upgrade is news.
	db := s.db
	if _, err := db.Exec(`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at, ended_at)
		VALUES (1, 3, 'c', 'done', '` + backfillAt(6) + `', '` + backfillAt(7) + `')`); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetChat(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unread {
		t.Error("a turn ending after the upgrade left the chat read")
	}
}
