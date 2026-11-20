package apiclient_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestChatDraftAgainstTheRealAPI is task 162's client half: PutChatDraft,
// ChatDraft, GetChat's embedded draft, ListChats' has_draft and ChatPrompts
// decode the real handlers' bodies.
func TestChatDraftAgainstTheRealAPI(t *testing.T) {
	st, err := storetest.Open(filepath.Join(t.TempDir(), "draft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	p := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	newChat := func(title, prompt string) *store.Chat {
		c := &store.Chat{ProjectID: p.ID, Title: title, Agent: "claude", Branch: "b-" + title, BaseBranch: "main", PermissionMode: "full_auto"}
		if err := st.CreateChat(ctx, c); err != nil {
			t.Fatal(err)
		}
		turn, err := st.CreateChatTurn(ctx, c.ID, prompt)
		if err != nil {
			t.Fatal(err)
		}
		turn.State = chatstate.TurnDone
		if _, err := st.FinishChatTurn(ctx, turn, ""); err != nil {
			t.Fatal(err)
		}
		return c
	}
	live := newChat("live", "rename the module")
	old := newChat("old", "explain the parser")
	if _, err := st.SetChatState(ctx, old.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log, Store: st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	draft, stash := "half typed", "set aside"
	if _, err := client.PutChatDraft(ctx, live.ID, apiclient.ChatDraftUpdate{Draft: &draft, Stash: &stash}); err != nil {
		t.Fatalf("PutChatDraft: %v", err)
	}
	d, err := client.ChatDraft(ctx, live.ID)
	if err != nil || d.Draft != draft || d.Stash != stash || d.UpdatedAt == nil {
		t.Fatalf("ChatDraft = %+v, %v", d, err)
	}
	c, _, err := client.GetChat(ctx, live.ID)
	if err != nil || c.Draft != draft || c.Stash != stash || !c.HasDraft {
		t.Fatalf("GetChat = %+v, %v", c, err)
	}
	list, err := client.ListChats(ctx, apiclient.ListChatsOptions{ProjectID: p.ID})
	if err != nil || len(list) != 1 || !list[0].HasDraft {
		t.Fatalf("ListChats = %+v, %v", list, err)
	}
	prompts, err := client.ChatPrompts(ctx, p.ID, "", 0)
	if err != nil || len(prompts) != 2 || prompts[0].Prompt != "explain the parser" || prompts[0].ChatState != "archived" {
		t.Fatalf("ChatPrompts = %+v, %v", prompts, err)
	}
	if prompts, err = client.ChatPrompts(ctx, p.ID, "module", 0); err != nil || len(prompts) != 1 || prompts[0].ChatID != live.ID {
		t.Fatalf("ChatPrompts(module) = %+v, %v", prompts, err)
	}
}
