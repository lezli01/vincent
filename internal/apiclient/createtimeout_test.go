package apiclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestCreateChatOutlivesTheLoopbackDeadline: POST /v1/chats answers only
// once the chat's worktree exists — a base fetch and a `git worktree add`,
// which on Windows routinely take longer than requestTimeout. A client held
// to the loopback deadline reported a timeout for a chat the daemon went on
// to create, so the call rides worktreeTimeout instead.
func TestCreateChatOutlivesTheLoopbackDeadline(t *testing.T) {
	const slack = 2 * time.Second
	delay := ScaleTimeout(requestTimeout) + slack
	if delay >= worktreeTimeout {
		t.Fatalf("%s=%q stretches requestTimeout to %s, past worktreeTimeout %s: the deadlines cannot be told apart",
			EnvTimeoutScale, os.Getenv(EnvTimeoutScale), delay-slack, worktreeTimeout)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":7,"title":"a talk"}`)
	}))
	defer ts.Close()

	chat, err := New(ts.URL, "test-token").CreateChat(context.Background(), CreateChatRequest{ProjectID: 1, Title: "a talk"})
	if err != nil {
		t.Fatalf("CreateChat after %s: %v, want it to wait for the worktree", delay, err)
	}
	if chat.ID != 7 {
		t.Fatalf("CreateChat = chat %d, want 7", chat.ID)
	}
}
