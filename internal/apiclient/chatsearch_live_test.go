package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
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

// TestSearchChatsAgainstTheRealAPI is task 158's client half: SearchChats
// decodes the real GET /v1/chats/search handler's body, and a too-short
// query surfaces as the typed error code.
func TestSearchChatsAgainstTheRealAPI(t *testing.T) {
	st, err := storetest.Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	p := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	c := &store.Chat{ProjectID: p.ID, Title: "ratelimit talk", Agent: "claude", Branch: "b", BaseBranch: "main", PermissionMode: "full_auto"}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateChatTurn(ctx, c.ID, "why the 429?")
	if err != nil {
		t.Fatal(err)
	}
	turn.State, turn.ResultText = chatstate.TurnDone, "it returns status429Body"
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetChatState(ctx, c.ID, chatstate.Archived); err != nil {
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

	got, err := client.SearchChats(ctx, "429", apiclient.SearchChatsOptions{})
	if err != nil {
		t.Fatalf("SearchChats: %v", err)
	}
	if len(got) != 1 || got[0].Chat.ID != c.ID || got[0].Chat.State != "archived" || got[0].MatchCount != 2 {
		t.Fatalf("results = %+v, want the archived chat matched twice", got)
	}
	m := got[0].Matches
	if len(m) != 2 || m[0].Field != apiclient.ChatSearchFieldPrompt || m[1].Field != apiclient.ChatSearchFieldResult || m[1].Seq != 1 {
		t.Fatalf("matches = %+v, want turn 1's prompt then its answer", m)
	}
	var marked string
	for _, s := range m[1].Segments {
		if s.Match {
			marked += s.Text
		}
	}
	if marked != "429" || m[1].Snippet != "it returns status429Body" {
		t.Errorf("answer match = %+v, want 429 marked", m[1])
	}

	if got, err := client.SearchChats(ctx, "429", apiclient.SearchChatsOptions{Archived: "false"}); err != nil || len(got) != 0 {
		t.Errorf("archived=false = %v, %v; want no results", got, err)
	}

	_, err = client.SearchChats(ctx, "42", apiclient.SearchChatsOptions{})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != apiclient.CodeQueryTooShort {
		t.Errorf("short query err = %v, want 400 %s", err, apiclient.CodeQueryTooShort)
	}
}
