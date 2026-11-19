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
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestChatQueueOverTheWire is task 156's routes against the real handlers
// and a real chat runner (review F3): send's two-shaped 202, the queue's
// list, edit and delete, the 409 an edit or delete takes once a message has
// started, the pause a cancel leaves, resume's 409 chat_cap_reached, and
// clear. It is what holds ChatSend, ChatQueue and ChatQueuedMessage to the
// server's DTOs.
func TestChatQueueOverTheWire(t *testing.T) {
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "chatqueue.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Every turn hangs until it is cancelled, so the test decides when each
	// one ends.
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	cfg := func() config.Config {
		c := config.Default()
		c.MaxParallelChats = 1
		return c
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{Store: st, Config: cfg, Agents: reg, DataDir: dataDir, Logger: log})
	chats.Start(ctx)
	t.Cleanup(chats.Stop)
	s := api.New(api.Deps{
		Token: testToken, Config: cfg, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: chats,
		Git: gitx.New(), Worktrees: worktree.NewManager(gitx.New(), dataDir), WakeRunner: func() {},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	chat, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "queue"})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	waitChat := func(id int64, what string, ok func(*apiclient.Chat) bool) *apiclient.Chat {
		t.Helper()
		var got *apiclient.Chat
		wait.Until(t, what, func() bool {
			c, _, err := client.GetChat(ctx, id)
			got = c
			return err == nil && ok(c)
		})
		return got
	}
	conflict := func(err error, code, what string) {
		t.Helper()
		var apiErr *apiclient.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != code {
			t.Errorf("%s = %v, want a 409 %s", what, err, code)
		}
	}

	// An idle chat starts a turn: the 202's turn shape.
	sent, err := client.SendChat(ctx, chat.ID, "first")
	if err != nil || sent.Turn == nil || sent.Queued != nil || sent.Turn.Prompt != "first" {
		t.Fatalf("SendChat on an idle chat = %+v, %v; want a started turn", sent, err)
	}
	// Wait for the process, not only the row: a cancel before the runner
	// tracks it has nothing to stop.
	wait.Until(t, "the first turn's process", func() bool { return chats.Running(chat.ID) })

	// A live chat queues: the 202's queued shape.
	var queued []*apiclient.ChatQueuedMessage
	for _, prompt := range []string{"second", "third"} {
		got, err := client.SendChat(ctx, chat.ID, prompt)
		if err != nil || got.Turn != nil || got.Queued == nil {
			t.Fatalf("SendChat(%q) on a running chat = %+v, %v; want it queued", prompt, got, err)
		}
		if got.Queued.ID == 0 || got.Queued.ChatID != chat.ID || got.Queued.Prompt != prompt || got.Queued.CreatedAt.IsZero() {
			t.Errorf("queued message = %+v, want id, chat %d, %q and a creation time", got.Queued, chat.ID, prompt)
		}
		queued = append(queued, got.Queued)
	}
	q, err := client.ChatQueue(ctx, chat.ID)
	if err != nil || len(q.Messages) != 2 || q.Paused ||
		q.Messages[0].ID != queued[0].ID || q.Messages[1].ID != queued[1].ID ||
		q.Messages[0].Position >= q.Messages[1].Position {
		t.Fatalf("ChatQueue = %+v, %v; want second then third, unpaused", q, err)
	}
	if c, _, err := client.GetChat(ctx, chat.ID); err != nil || c.QueuedCount != 2 {
		t.Errorf("GetChat queued_count = %+v, %v; want 2", c, err)
	}

	edited, err := client.EditChatQueued(ctx, chat.ID, queued[0].ID, "second, edited")
	if err != nil || edited.ID != queued[0].ID || edited.Prompt != "second, edited" {
		t.Errorf("EditChatQueued = %+v, %v; want the new text on the same message", edited, err)
	}
	if err := client.DeleteChatQueued(ctx, chat.ID, queued[1].ID); err != nil {
		t.Errorf("DeleteChatQueued: %v", err)
	}
	conflict(client.DeleteChatQueued(ctx, chat.ID, queued[1].ID), "invalid_state", "a second delete")

	// A cancel pauses the queue and leaves the chat idle.
	if err := client.CancelChat(ctx, chat.ID); err != nil {
		t.Fatalf("CancelChat: %v", err)
	}
	paused := waitChat(chat.ID, "the cancelled chat idle with its queue paused", func(c *apiclient.Chat) bool {
		return c.State == "idle" && c.QueuePaused
	})
	if paused.QueuePauseReason != chatrun.ReasonCanceled || paused.QueuedCount != 1 {
		t.Errorf("paused chat reason %q queued %d, want %q and 1", paused.QueuePauseReason, paused.QueuedCount, chatrun.ReasonCanceled)
	}
	if q, err := client.ChatQueue(ctx, chat.ID); err != nil || !q.Paused || q.PauseReason != chatrun.ReasonCanceled {
		t.Errorf("ChatQueue after cancel = %+v, %v; want it paused, canceled", q, err)
	}

	// Resuming starts a turn, so the cap applies: another chat holds the
	// only slot.
	other, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "hog"})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if _, err := client.SendChat(ctx, other.ID, "hold the slot"); err != nil {
		t.Fatalf("SendChat on the other chat: %v", err)
	}
	wait.Until(t, "the other chat's process", func() bool { return chats.Running(other.ID) })
	_, err = client.ResumeChatQueue(ctx, chat.ID)
	conflict(err, "chat_cap_reached", "ResumeChatQueue with the slot taken")
	if err := client.CancelChat(ctx, other.ID); err != nil {
		t.Fatalf("CancelChat other: %v", err)
	}
	waitChat(other.ID, "the other chat idle", func(c *apiclient.Chat) bool { return c.State == "idle" })

	resumed, err := client.ResumeChatQueue(ctx, chat.ID)
	if err != nil || resumed.Paused || len(resumed.Messages) != 0 {
		t.Fatalf("ResumeChatQueue = %+v, %v; want the pause lifted and the message started", resumed, err)
	}
	_, turns, err := client.GetChat(ctx, chat.ID)
	if err != nil || len(turns) != 2 || turns[1].Prompt != "second, edited" {
		t.Fatalf("turns after resume = %+v, %v; want the edited message as turn 2", turns, err)
	}
	// It is a turn now: the queue refuses to edit or delete it.
	_, err = client.EditChatQueued(ctx, chat.ID, queued[0].ID, "too late")
	conflict(err, "invalid_state", "an edit of a started message")
	conflict(client.DeleteChatQueued(ctx, chat.ID, queued[0].ID), "invalid_state", "a delete of a started message")

	wait.Until(t, "the resumed turn's process", func() bool { return chats.Running(chat.ID) })
	// Clear deletes what is queued and answers 204.
	if got, err := client.SendChat(ctx, chat.ID, "fourth"); err != nil || got.Queued == nil {
		t.Fatalf("SendChat(fourth) = %+v, %v; want it queued", got, err)
	}
	if err := client.ClearChatQueue(ctx, chat.ID); err != nil {
		t.Fatalf("ClearChatQueue: %v", err)
	}
	if q, err := client.ChatQueue(ctx, chat.ID); err != nil || len(q.Messages) != 0 || q.Paused {
		t.Errorf("ChatQueue after clear = %+v, %v; want it empty and unpaused", q, err)
	}
	if err := client.CancelChat(ctx, chat.ID); err != nil {
		t.Fatalf("CancelChat: %v", err)
	}
	waitChat(chat.ID, "the chat idle at the end", func(c *apiclient.Chat) bool { return c.State == "idle" })
}
