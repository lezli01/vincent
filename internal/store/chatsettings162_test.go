package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration0062LeavesOldTurnsUnrecorded: a turn written before the
// run-settings columns existed reads back with none of them — "not
// recorded" — and is never back-filled from its chat's settings (task 162
// decision 7).
func TestMigration0062LeavesOldTurnsUnrecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 61)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for _, q := range []string{
			`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
				VALUES (1, 'p1', '/p1', 'main', '` + backfillAt(0) + `', '` + backfillAt(0) + `')`,
			`INSERT INTO chats (id, project_id, title, state, agent, model, effort, permission_mode, branch, base_branch, created_at, updated_at)
				VALUES (1, 1, 'old', 'idle', 'claude', 'opus', 'high', 'full-auto', 'b1', 'main', '` + backfillAt(1) + `', '` + backfillAt(1) + `')`,
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, started_at)
				VALUES (1, 1, 'hello', 'done', '` + backfillAt(2) + `')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 61 -> 62): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	turns, err := s.ListChatTurns(t.Context(), 1)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns = %v, %v", turns, err)
	}
	if got := turns[0]; got.Model != "" || got.Effort != "" || got.PermissionMode != "" {
		t.Errorf("old turn = %q/%q/%q, want all unrecorded", got.Model, got.Effort, got.PermissionMode)
	}
	// A turn written now records what it resolved to.
	turn, err := s.CreateChatTurnWith(t.Context(), 1, "again", TurnSettings{Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChatTurn(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "opus" || got.Effort != "low" || got.PermissionMode != "full-auto" {
		t.Errorf("new turn = %q/%q/%q, want opus/low/full-auto", got.Model, got.Effort, got.PermissionMode)
	}
}
