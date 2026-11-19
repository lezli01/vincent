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

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestMarkChatViewedLive is task 156's client half against the real handler:
// Unread and LastViewedAt decode on the reads, MarkChatViewed clears the mark,
// and an unknown chat is a 404 *apiclient.Error.
func TestMarkChatViewedLive(t *testing.T) {
	st, err := storetest.Open(filepath.Join(t.TempDir(), "viewed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c := &store.Chat{
		ProjectID: project.ID, Title: "seen", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	turn, err := st.CreateChatTurn(ctx, c.ID, "hi")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	end := time.Now().Add(-time.Minute)
	turn.State, turn.EndedAt = chatstate.TurnDone, &end
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log, Store: st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	got, _, err := client.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !got.Unread || got.LastViewedAt != nil {
		t.Fatalf("before view: unread=%v last_viewed_at=%v, want true and nil", got.Unread, got.LastViewedAt)
	}

	before := time.Now().Add(-time.Second)
	viewed, err := client.MarkChatViewed(ctx, c.ID)
	if err != nil {
		t.Fatalf("MarkChatViewed: %v", err)
	}
	if viewed.Unread || viewed.LastViewedAt == nil || viewed.LastViewedAt.Before(before) {
		t.Fatalf("after view: unread=%v last_viewed_at=%v", viewed.Unread, viewed.LastViewedAt)
	}
	list, err := client.ListChats(ctx, apiclient.ListChatsOptions{})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(list) != 1 || list[0].Unread || list[0].LastViewedAt == nil {
		t.Fatalf("listed after view = %+v, want one read chat", list)
	}

	_, err = client.MarkChatViewed(ctx, 99999)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("MarkChatViewed(unknown) = %v, want a 404 *apiclient.Error", err)
	}
}
