package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestChatDiffAgainstTheRealAPI is task 169's client half: ChatDiff,
// ChatDiffStat, RejectChatHunk, AcceptChatHunk and UnacceptChatHunk against
// the real handlers, so the client's wire types and the server's cannot
// drift, and each 409 arrives as the code a client branches on.
func TestChatDiffAgainstTheRealAPI(t *testing.T) {
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "diff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()

	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "config", "core.autocrlf", "false")
	dir := filepath.Join(t.TempDir(), "wt")
	testrepo.Run(t, repo, "worktree", "add", "-q", "-b", "chat", dir, "main")
	testrepo.WriteFile(t, dir, "README.md", "test repo\nchanged here\n")
	testrepo.WriteFile(t, dir, "sub/new.txt", "untracked\n")

	p := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	newChat := func(state chatstate.State) *store.Chat {
		c := &store.Chat{
			ProjectID: p.ID, Title: "diff", Agent: "claude", State: state,
			BaseBranch: "main", PermissionMode: "full_auto", WorktreePath: dir,
		}
		if err := st.CreateChat(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	chat, live := newChat(chatstate.Idle), newChat(chatstate.Running)

	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st,
		Git: gitx.New(), Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	wantCode := func(what string, err error, code string) {
		t.Helper()
		var apiErr *apiclient.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != code || apiErr.Message == "" {
			t.Errorf("%s: err = %v, want 409 %s", what, err, code)
		}
	}

	d, err := client.ChatDiff(ctx, chat.ID, "")
	if err != nil {
		t.Fatalf("ChatDiff: %v", err)
	}
	if d.Scope != apiclient.ChatDiffScopeBase || len(d.From) != 40 || d.Added != 2 || d.Removed != 0 || len(d.Files) != 2 {
		t.Fatalf("ChatDiff = %+v", d)
	}
	readme, added := d.Files[0], d.Files[1]
	if readme.Path != "README.md" || readme.Status != "modified" || !readme.Rejectable || readme.Binary ||
		readme.OldPath != "" || readme.Added != 1 || len(readme.Hunks) != 1 {
		t.Fatalf("README.md = %+v", readme)
	}
	hk := readme.Hunks[0]
	if len(hk.Hash) != 64 || !strings.HasPrefix(hk.Header, "@@ -") || hk.Body != " test repo\n+changed here" ||
		hk.Added != 1 || hk.Removed != 0 || hk.Accepted {
		t.Errorf("hunk = %+v", hk)
	}
	// The untracked file, its path /-separated on every OS.
	if added.Path != "sub/new.txt" || added.Status != "added" || len(added.Hunks) != 1 {
		t.Errorf("untracked file = %+v", added)
	}

	stat, err := client.ChatDiffStat(ctx, chat.ID)
	if err != nil || *stat != (apiclient.ChatDiffStat{Files: 2, Added: 2, Removed: 0}) {
		t.Errorf("ChatDiffStat = %+v, %v", stat, err)
	}

	_, err = client.ChatDiff(ctx, chat.ID, apiclient.ChatDiffScopeTurn)
	wantCode("turn scope with no turn", err, apiclient.CodeChatDiffUnavailable)
	var apiErr *apiclient.Error
	if _, err = client.ChatDiff(ctx, chat.ID, "all"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("unknown scope: %v, want 400", err)
	}

	// Accept, read back, unaccept twice.
	if err := client.AcceptChatHunk(ctx, chat.ID, apiclient.ChatDiffScopeBase, "README.md", hk.Hash); err != nil {
		t.Fatalf("AcceptChatHunk: %v", err)
	}
	if d, err = client.ChatDiff(ctx, chat.ID, apiclient.ChatDiffScopeBase); err != nil || !d.Files[0].Hunks[0].Accepted {
		t.Fatalf("accepted hunk reads back %+v, %v", d, err)
	}
	for range 2 {
		if err := client.UnacceptChatHunk(ctx, chat.ID, "README.md", hk.Hash); err != nil {
			t.Fatalf("UnacceptChatHunk: %v", err)
		}
	}
	if d, err = client.ChatDiff(ctx, chat.ID, apiclient.ChatDiffScopeBase); err != nil || d.Files[0].Hunks[0].Accepted {
		t.Fatalf("unaccepted hunk reads back %+v, %v", d, err)
	}
	wantCode("accept of an unknown hash",
		client.AcceptChatHunk(ctx, chat.ID, "base", "README.md", strings.Repeat("a", 64)), apiclient.CodeChatHunkStale)

	// A live chat refuses both writes.
	wantCode("accept on a live chat",
		client.AcceptChatHunk(ctx, live.ID, "base", "README.md", hk.Hash), apiclient.CodeChatTurnLive)
	_, err = client.RejectChatHunk(ctx, live.ID, "base", "README.md", hk.Hash)
	wantCode("reject on a live chat", err, apiclient.CodeChatTurnLive)

	// Reject: the line is gone from disk and the draft holds the note.
	res, err := client.RejectChatHunk(ctx, chat.ID, apiclient.ChatDiffScopeBase, "README.md", hk.Hash)
	if err != nil {
		t.Fatalf("RejectChatHunk: %v", err)
	}
	if !res.NoteAdded || res.Warning != "" || !strings.Contains(res.Draft, "- README.md "+hk.Header) {
		t.Errorf("RejectChatHunk = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(got) != "test repo\n" {
		t.Errorf("README.md after the reject = %q", got)
	}
	if draft, err := client.ChatDraft(ctx, chat.ID); err != nil || draft.Draft != res.Draft {
		t.Errorf("ChatDraft = %+v, %v; want the reject's draft", draft, err)
	}
	_, err = client.RejectChatHunk(ctx, chat.ID, "base", "README.md", hk.Hash)
	wantCode("a second reject of the same hash", err, apiclient.CodeChatHunkStale)

	// A binary file is not rejectable, and the client says which refusal.
	testrepo.WriteFile(t, dir, "bin.dat", "a\x00b\x00")
	_, err = client.RejectChatHunk(ctx, chat.ID, "base", "bin.dat", hk.Hash)
	wantCode("reject of a binary file", err, apiclient.CodeChatHunkNotRejectable)
	if d, err = client.ChatDiff(ctx, chat.ID, ""); err != nil || len(d.Files) != 2 || !d.Files[0].Binary ||
		d.Files[0].Rejectable || d.Files[0].Hunks == nil {
		t.Errorf("binary file = %+v, %v", d, err)
	}
}
