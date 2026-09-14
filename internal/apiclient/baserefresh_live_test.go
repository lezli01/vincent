package apiclient_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// baseRefreshHarness wires a client to the real task and chat handlers over a
// real store, so the base refresh record's client wire types are proven
// against what the server actually writes (§13.2, task 099).
type baseRefreshHarness struct {
	client  *apiclient.Client
	st      *store.Store
	project *store.Project
}

func newBaseRefreshHarness(t *testing.T) *baseRefreshHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Worktrees:   worktree.NewManager(gitx.New(), dataDir),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &baseRefreshHarness{client: apiclient.New(ts.URL, testToken), st: st, project: p}
}

// fullBaseRefresh sets every field of the record. No single worktree creation
// produces this combination; it exists so that a field the client fails to
// decode cannot hide behind being empty on both sides.
func fullBaseRefresh() (*store.BaseRefresh, apiclient.BaseRefresh) {
	stored := &store.BaseRefresh{
		Fetch: store.BaseFetch{
			Remote: "origin", Ref: "refs/heads/main", Result: "error", Error: "could not read from remote",
		},
		FastForward: store.BaseFastForward{
			Result: "skipped", Reason: "error", Worktree: "/somewhere/checkout", Error: "cannot lock ref",
		},
	}
	want := apiclient.BaseRefresh{
		Fetch: apiclient.BaseFetch{
			Result: "error", Remote: "origin", Ref: "refs/heads/main", Error: "could not read from remote",
		},
		FastForward: apiclient.BaseFastForward{
			Result: "skipped", Reason: "error", Worktree: "/somewhere/checkout", Error: "cannot lock ref",
		},
	}
	return stored, want
}

func TestTaskBaseRefreshOverTheWire(t *testing.T) {
	h := newBaseRefreshHarness(t)
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.project.ID, Title: "t", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "vincent/1-t", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Nothing recorded yet: null on the wire, nil in the client.
	got, err := h.client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.BaseSHA != "" || got.BaseRefresh != nil {
		t.Errorf("unclaimed task: base_sha %q, base_refresh %+v; want neither", got.BaseSHA, got.BaseRefresh)
	}

	sha := strings.Repeat("0f", 20)
	stored, want := fullBaseRefresh()
	if err := h.st.ClaimTaskWorktree(ctx, task.ID, t.TempDir(), sha, stored); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	if got, err = h.client.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.BaseSHA != sha {
		t.Errorf("BaseSHA = %q, want %q", got.BaseSHA, sha)
	}
	if got.BaseRefresh == nil || *got.BaseRefresh != want {
		t.Errorf("BaseRefresh = %+v, want %+v", got.BaseRefresh, want)
	}
}

func TestChatBaseRefreshOverTheWire(t *testing.T) {
	h := newBaseRefreshHarness(t)
	ctx := t.Context()
	chat := &store.Chat{
		ProjectID: h.project.ID, Title: "c", State: chatstate.Idle, Agent: "claude",
		PermissionMode: "full_auto", BaseBranch: "main",
	}
	if err := h.st.CreateChat(ctx, chat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	got, _, err := h.client.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.BaseSHA != "" || got.BaseRefresh != nil {
		t.Errorf("unclaimed chat: base_sha %q, base_refresh %+v; want neither", got.BaseSHA, got.BaseRefresh)
	}

	sha := strings.Repeat("c0", 20)
	stored, want := fullBaseRefresh()
	if _, err := h.st.ClaimChatWorktree(ctx, chat.ID, t.TempDir(), sha, stored); err != nil {
		t.Fatalf("ClaimChatWorktree: %v", err)
	}
	if got, _, err = h.client.GetChat(ctx, chat.ID); err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.BaseSHA != sha {
		t.Errorf("BaseSHA = %q, want %q", got.BaseSHA, sha)
	}
	if got.BaseRefresh == nil || *got.BaseRefresh != want {
		t.Errorf("BaseRefresh = %+v, want %+v", got.BaseRefresh, want)
	}
}
