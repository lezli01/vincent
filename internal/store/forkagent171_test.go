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
}
