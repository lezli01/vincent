package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestForkedFromAgentAndPreambleEdit (task 171): a fork's source agent
// round-trips, and the seeded context is editable only while it is pending
// and the chat holds no process.
func TestForkedFromAgentAndPreambleEdit(t *testing.T) {
	st, err := storetest.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	src, seq := int64(1), 2
	c := &store.Chat{
		ProjectID: p.ID, Title: "fork", Agent: "codex", PermissionMode: "full-auto",
		PendingPreamble: "summary", ForkedFromChatID: &src, ForkedFromSeq: &seq, ForkedFromAgent: "claude",
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetChat(ctx, c.ID)
	if err != nil || got.ForkedFromAgent != "claude" {
		t.Fatalf("forked_from_agent = %q, %v", got.ForkedFromAgent, err)
	}
	if out, err := st.EditChatPendingPreamble(ctx, c.ID, "edited"); err != nil || out.PendingPreamble != "edited" {
		t.Fatalf("edit = %+v, %v", out, err)
	}
	if _, err := st.SetChatState(ctx, c.ID, chatstate.Running); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EditChatPendingPreamble(ctx, c.ID, "x"); !errors.Is(err, store.ErrPreambleNotPending) {
		t.Errorf("edit while running = %v", err)
	}
	if _, err := st.SetChatState(ctx, c.ID, chatstate.Idle); err != nil {
		t.Fatal(err)
	}
	if out, err := st.EditChatPendingPreamble(ctx, c.ID, ""); err != nil || out.PendingPreamble != "" {
		t.Fatalf("clear = %+v, %v", out, err)
	}
	if _, err := st.EditChatPendingPreamble(ctx, c.ID, "again"); !errors.Is(err, store.ErrPreambleNotPending) {
		t.Errorf("edit after clearing = %v", err)
	}
	// An ended chat has no next turn to seed, though its row still holds
	// the plan (review F4). Terminal is final, so one chat per state.
	for _, ended := range []chatstate.State{chatstate.Archived, chatstate.Closed, chatstate.HandedOff} {
		e := &store.Chat{ProjectID: p.ID, Title: "ended", Agent: "codex", PermissionMode: "full-auto", PendingPreamble: "summary"}
		if err := st.CreateChat(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetChatState(ctx, e.ID, ended); err != nil {
			t.Fatal(err)
		}
		if _, err := st.EditChatPendingPreamble(ctx, e.ID, "x"); !errors.Is(err, store.ErrPreambleNotPending) {
			t.Errorf("edit while %s = %v", ended, err)
		}
		if got, err := st.GetChat(ctx, e.ID); err != nil || got.PendingPreamble != "summary" {
			t.Errorf("edit while %s wrote %q, %v", ended, got.PendingPreamble, err)
		}
	}
}
