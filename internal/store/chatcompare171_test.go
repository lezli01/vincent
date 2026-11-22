package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestChatComparePairing (task 171 decisions 6-8): a compare's two sides
// are found from either chat, the kept side is recorded only for one of
// them, and deleting one side's chat leaves the pair with that side unset.
func TestChatComparePairing(t *testing.T) {
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
	var ids [3]int64
	for i, agent := range []string{"claude", "claude", "codex"} {
		c := &store.Chat{ProjectID: p.ID, Title: "c", Agent: agent, PermissionMode: "full-auto"}
		if err := st.CreateChat(ctx, c); err != nil {
			t.Fatal(err)
		}
		ids[i] = c.ID
	}
	cmp := &store.ChatCompare{
		ProjectID: p.ID, SourceChatID: ids[0], Seq: 2, Prompt: "which?",
		Left:  store.ChatCompareSide{ChatID: &ids[1], Agent: "claude", Mode: "restricted"},
		Right: store.ChatCompareSide{ChatID: &ids[2], Agent: "codex", Mode: "full-auto", ModeNote: "codex cannot"},
	}
	if err := st.CreateChatCompare(ctx, cmp); err != nil {
		t.Fatal(err)
	}
	for side, id := range map[string]int64{"left": ids[1], "right": ids[2]} {
		got, err := st.GetChatCompareForChat(ctx, id)
		if err != nil || got.ID != cmp.ID || got.Side(id) != side || got.Right.ModeNote != "codex cannot" || got.Prompt != "which?" {
			t.Fatalf("from chat %d: %+v, %v", id, got, err)
		}
	}
	if _, err := st.GetChatCompareForChat(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("source chat is a side: %v", err)
	}
	if err := st.SetChatCompareKept(ctx, cmp.ID, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("kept a chat that is no side: %v", err)
	}
	if err := st.SetChatCompareKept(ctx, cmp.ID, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ForceDeleteChat(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetChatCompare(ctx, cmp.ID)
	if err != nil || got.Right.ChatID != nil || got.Left.ChatID == nil || got.KeptChatID == nil || *got.KeptChatID != ids[1] {
		t.Fatalf("after deleting the right side: %+v, %v", got, err)
	}
}
