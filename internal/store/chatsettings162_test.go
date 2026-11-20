package store

import (
	"database/sql"
	"errors"
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

// TestUpdateChatSettingsJudgesStartedInsideTheWrite: the API reads a chat
// with no session and decides a model change is a first-turn choice, then a
// first turn starts before the write lands. The store, judging the row it
// writes, refuses the change for an adapter that cannot resume on another
// model (task 162 decision 8, review F2) — and still lets a title or the
// same model through.
func TestUpdateChatSettingsJudgesStartedInsideTheWrite(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	model, effort := "gpt-5", "high"

	// Before any turn the change is the first model: allowed.
	if _, err := s.UpdateChatSettings(t.Context(), c.ID, ChatSettingsPatch{Model: &model, KeepModelOnceStarted: true}); err != nil {
		t.Fatalf("a model change before the first turn: %v", err)
	}
	// The first turn starts between the API's read and its write.
	if _, err := s.CreateChatTurn(t.Context(), c.ID, "first"); err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	other := "gpt-5-mini"
	if _, err := s.UpdateChatSettings(t.Context(), c.ID, ChatSettingsPatch{Model: &other, KeepModelOnceStarted: true}); !errors.Is(err, ErrChatModelFixed) {
		t.Fatalf("a model change once started = %v, want ErrChatModelFixed", err)
	}
	if _, err := s.UpdateChatSettings(t.Context(), c.ID, ChatSettingsPatch{Effort: &effort, KeepModelOnceStarted: true}); !errors.Is(err, ErrChatModelFixed) {
		t.Fatalf("an effort change once started = %v, want ErrChatModelFixed", err)
	}
	got, err := s.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != model || got.Effort != "" {
		t.Fatalf("a refused change was written: %q/%q", got.Model, got.Effort)
	}
	title := "renamed"
	if _, err := s.UpdateChatSettings(t.Context(), c.ID, ChatSettingsPatch{Title: &title, Model: &model, KeepModelOnceStarted: true}); err != nil {
		t.Fatalf("the same model, once started: %v", err)
	}
	// An adapter with the capture may change it whenever.
	if _, err := s.UpdateChatSettings(t.Context(), c.ID, ChatSettingsPatch{Model: &other}); err != nil {
		t.Fatalf("a model change for a capable adapter: %v", err)
	}
}
