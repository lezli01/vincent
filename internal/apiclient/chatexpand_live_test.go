package apiclient_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 166 decision 6: ExpandChat against the real POST
// /v1/chats/{id}/expand handler and a real chat runner — the function a turn
// runs — so the note and the preview a client draws are decoded by the type
// it will actually use.
func TestExpandChatAgainstTheRealHandler(t *testing.T) {
	h := newChatFilesHarness(t)
	ctx := t.Context()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := h.chat(t, "claude", dir)
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "Crash on start"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	draft := "why #issue-" + strconv.FormatInt(iss.ID, 10) + " at @main.go#L3 and #task-77 @main.go"
	got, err := h.client.ExpandChat(ctx, c.ID, draft)
	if err != nil {
		t.Fatalf("ExpandChat: %v", err)
	}
	if len(got.Resolved) != 2 || got.Resolved[0].Kind != "issue" || got.Resolved[1].Kind != "lines" ||
		got.Resolved[1].Path != "main.go" || got.Resolved[1].From != 3 {
		t.Errorf("resolved = %+v", got.Resolved)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Token != "#task-77" {
		t.Errorf("unresolved = %+v, want only #task-77 (a bare @main.go is not vincent's)", got.Unresolved)
	}
	if !strings.Contains(got.Context, "title: Crash on start") || !strings.Contains(got.Context, "func main() {}") {
		t.Errorf("context = %q", got.Context)
	}
	if got.Bytes != len(got.Context) || got.TokensEstimate != got.Bytes/4 {
		t.Errorf("bytes %d, tokens %d for a %d-byte context", got.Bytes, got.TokensEstimate, len(got.Context))
	}
	if want := (agent.RunSpec{Prompt: draft, Preamble: got.Context}).JoinedPrompt(); got.Prompt != want {
		t.Errorf("prompt = %q, want %q", got.Prompt, want)
	}

	none, err := h.client.ExpandChat(ctx, c.ID, "nothing here")
	if err != nil {
		t.Fatal(err)
	}
	if none.Context != "" || none.Prompt != "nothing here" || none.Resolved == nil || none.Unresolved == nil {
		t.Errorf("a draft with no mention answered %+v", none)
	}

	done := &store.Chat{
		ProjectID: h.projectID, Title: "done", State: chatstate.Archived, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: dir,
	}
	if err := h.store.CreateChat(ctx, done); err != nil {
		t.Fatal(err)
	}
	_, err = h.client.ExpandChat(ctx, done.ID, draft)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("ExpandChat on an archived chat = %v, want a 409", err)
	}
}
