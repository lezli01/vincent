package apiclient_test

import (
	"io"
	"log/slog"
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
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestIssueChatOverTheWire is task 140.10's client half against the real
// handlers, a real worktree manager and a real chat runner: CreateChatRequest.
// IssueID, Chat.IssueID, Chat.EndSHA and Chat.Warnings, the
// IssueHasLiveChat decoder, and Issue.MainWorktree.OccupantChatID on both the
// detail and the list — every field the TUI's `T` on an issue reads.
func TestIssueChatOverTheWire(t *testing.T) {
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "issuechat.db"))
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
	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{Store: st, Config: config.Default, Agents: reg, DataDir: dataDir, Logger: log})
	chats.Start(ctx)
	t.Cleanup(chats.Stop)
	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: chats,
		Git: gitx.New(), Worktrees: worktree.NewManager(gitx.New(), dataDir), WakeRunner: func() {},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	iss, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: project.ID, Title: "Talk it through"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	chat, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "talk", IssueID: &iss.ID})
	if err != nil {
		t.Fatalf("CreateChat on the issue: %v", err)
	}
	if chat.IssueID == nil || *chat.IssueID != iss.ID || chat.Branch == "" || chat.WorktreePath == "" || len(chat.Warnings) != 0 {
		t.Fatalf("chat = issue %v branch %q worktree %q warnings %v; want issue %d on a cut branch, no warnings",
			chat.IssueID, chat.Branch, chat.WorktreePath, chat.Warnings, iss.ID)
	}

	got, err := client.GetIssue(ctx, iss.ID, "")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if mw := got.MainWorktree; mw == nil || mw.Branch != chat.Branch || mw.OccupantChatID == nil ||
		*mw.OccupantChatID != chat.ID || got.Lane != "in_progress" {
		t.Errorf("issue = lane %s main worktree %+v; want in_progress, %q held by chat %d",
			got.Lane, got.MainWorktree, chat.Branch, chat.ID)
	}
	rows, err := client.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: project.ID})
	if err != nil || len(rows) != 1 || rows[0].MainWorktree == nil || rows[0].MainWorktree.OccupantChatID == nil ||
		*rows[0].MainWorktree.OccupantChatID != chat.ID {
		t.Errorf("ListIssues = %+v, %v; want the row held by chat %d", rows, err, chat.ID)
	}

	_, err = client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "again", IssueID: &iss.ID})
	if id, ok := apiclient.IssueHasLiveChat(err); !ok || id != chat.ID {
		t.Errorf("second chat err = %v; IssueHasLiveChat = %d, %v; want chat %d", err, id, ok, chat.ID)
	}

	listed, err := client.ListChats(ctx, apiclient.ListChatsOptions{ProjectID: project.ID})
	if err != nil || len(listed) != 1 || listed[0].IssueID == nil || *listed[0].IssueID != iss.ID {
		t.Errorf("ListChats = %+v, %v; want the chat naming issue %d", listed, err, iss.ID)
	}

	closed, err := client.CloseChat(ctx, chat.ID)
	if err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	if closed.State != "closed" {
		t.Errorf("closed chat state = %s", closed.State)
	}
	after, _, err := client.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if after.EndSHA == "" || after.IssueID == nil || *after.IssueID != iss.ID {
		t.Errorf("closed chat end_sha %q issue %v; want the branch tip stamped on issue %d", after.EndSHA, after.IssueID, iss.ID)
	}
	if got, err := client.GetIssue(ctx, iss.ID, ""); err != nil || got.MainWorktree == nil || got.MainWorktree.OccupantChatID != nil {
		t.Errorf("issue after close = %+v, %v; want the main worktree free", got.MainWorktree, err)
	}

	shut, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: project.ID, Title: "Already done"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := client.CloseIssue(ctx, shut.ID, apiclient.CloseIssueRequest{Reason: "completed"}); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	warned, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "revisit", IssueID: &shut.ID})
	if err != nil {
		t.Fatalf("CreateChat on a closed issue: %v", err)
	}
	if len(warned.Warnings) == 0 {
		t.Error("a chat on a closed issue came back with no warnings")
	}
}
