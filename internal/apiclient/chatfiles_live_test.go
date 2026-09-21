package apiclient_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// Task 126.6's client half: ChatFiles against the real GET
// /v1/chats/{id}/files handler, a real worktree manager and a real chat
// runner, so every wire field — the empty array and the quoted mention
// included — is decoded by the type a client will actually use.

type chatFilesHarness struct {
	client    *apiclient.Client
	store     *store.Store
	projectID int64
}

func newChatFilesHarness(t *testing.T) *chatFilesHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "files.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// claude, because the `@"…"`-on-a-space rule is written there and the
	// point of this route is that no client rebuilds it. Nothing launches
	// it: the handler reads two static methods.
	reg := agent.NewRegistry(claude.New(func() string { return "claude" }), agenttest.StubNoMentions{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: reg, DataDir: dataDir, Logger: log,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: chats,
		Worktrees: worktree.NewManager(gitx.New(), dataDir),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &chatFilesHarness{client: apiclient.New(ts.URL, testToken), store: st, projectID: project.ID}
}

// chat writes a chat on agentName working in dir.
func (h *chatFilesHarness) chat(t *testing.T, agentName, dir string) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "files", State: chatstate.Idle, Agent: agentName,
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: dir,
	}
	if err := h.store.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// filesRepo is testrepo.Init with the developer's global core.excludesFile
// taken out of the answer, the way internal/worktree's file tests do it: a
// personal global ignore rule would otherwise remove a row on one machine.
func filesRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	excludes := filepath.Join(t.TempDir(), "empty-excludes")
	if err := os.WriteFile(excludes, nil, 0o644); err != nil {
		t.Fatalf("write empty excludes file: %v", err)
	}
	testrepo.Run(t, dir, "config", "core.excludesFile", excludes)
	return dir
}

// TestChatFilesLiveDecodesAListing covers the mention facts as flat siblings,
// truncated, and the one row a client must never build for itself: a path
// carrying a space, quoted after the sigil by the adapter.
func TestChatFilesLiveDecodesAListing(t *testing.T) {
	h := newChatFilesHarness(t)
	dir := filesRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "dir with space"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	testrepo.WriteFile(t, dir, filepath.Join("dir with space", "q.txt"), "x\n")
	c := h.chat(t, "claude", dir)

	got, err := h.client.ChatFiles(t.Context(), c.ID, 0)
	if err != nil {
		t.Fatalf("ChatFiles: %v", err)
	}
	if got.ChatID != c.ID || got.Agent != "claude" || got.WorkDir != dir {
		t.Errorf("identity = (%d, %q, %q), want (%d, %q, %q)",
			got.ChatID, got.Agent, got.WorkDir, c.ID, "claude", dir)
	}
	if got.MentionSigil != "@" || got.MentionPosition != "anywhere" || !got.MentionExpands {
		t.Errorf("mention = (%q, %q, %v), want (@, anywhere, true)",
			got.MentionSigil, got.MentionPosition, got.MentionExpands)
	}
	if got.Truncated {
		t.Error("truncated is true for a listing of two files")
	}
	want := apiclient.ChatFile{Path: "dir with space/q.txt", Mention: `@"dir with space/q.txt"`}
	if !slices.Contains(got.Files, want) {
		t.Fatalf("files = %+v, want it to contain %+v", got.Files, want)
	}
}

// TestChatFilesLiveLowersTheCap: a limit the client passes narrows the answer
// and sets truncated.
func TestChatFilesLiveLowersTheCap(t *testing.T) {
	h := newChatFilesHarness(t)
	dir := filesRepo(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		testrepo.WriteFile(t, dir, name, "x\n")
	}
	c := h.chat(t, "claude", dir)

	got, err := h.client.ChatFiles(t.Context(), c.ID, 1)
	if err != nil {
		t.Fatalf("ChatFiles: %v", err)
	}
	if len(got.Files) != 1 || !got.Truncated {
		t.Fatalf("limit 1 = %d rows, truncated %v; want 1 and true", len(got.Files), got.Truncated)
	}
}

// TestChatFilesLiveEmptyListingDecodesAsAnEmptySlice: the wire's `[]` is the
// one spelling of "none", and it arrives as a non-nil empty slice.
func TestChatFilesLiveEmptyListingDecodesAsAnEmptySlice(t *testing.T) {
	h := newChatFilesHarness(t)
	c := h.chat(t, "claude", testrepo.InitEmpty(t, "main"))

	got, err := h.client.ChatFiles(t.Context(), c.ID, 0)
	if err != nil {
		t.Fatalf("ChatFiles: %v", err)
	}
	if got.Files == nil {
		t.Fatal("files decoded as nil; the route promises an array")
	}
	if len(got.Files) != 0 {
		t.Errorf("files = %+v, want none", got.Files)
	}
}

// TestChatFilesLiveWithoutAMentionCapableAdapter: an empty sigil is the signal
// not to offer a picker, and the paths still arrive (task 126 decision 38).
func TestChatFilesLiveWithoutAMentionCapableAdapter(t *testing.T) {
	h := newChatFilesHarness(t)
	c := h.chat(t, agenttest.NoMentionsName, filesRepo(t))

	got, err := h.client.ChatFiles(t.Context(), c.ID, 0)
	if err != nil {
		t.Fatalf("ChatFiles: %v", err)
	}
	if got.MentionSigil != "" || got.MentionPosition != "" || got.MentionExpands {
		t.Errorf("mention = (%q, %q, %v), want empty",
			got.MentionSigil, got.MentionPosition, got.MentionExpands)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "README.md" || got.Files[0].Mention != "" {
		t.Errorf("files = %+v, want one unmentionable README.md", got.Files)
	}
}
