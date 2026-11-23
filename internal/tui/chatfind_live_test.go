package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestChatSearchLive drives the conversation search against the real
// handlers over httptest (task 160 decision 2): the daemon counts a match in
// a turn this view never loaded, landing on it fetches that turn through the
// transcript route, and the cursor ends on the line that shows it. It is what
// keeps the search's wire types from drifting between client and server.
// chatFindLive is a chat served by the real handlers over httptest, its
// turns' transcripts written from transcripts (seq → JSONL, seq from 1),
// and a chat view on it whose turns are listed but none fetched.
func chatFindLive(t *testing.T, transcripts []string) *chatView {
	t.Helper()
	const token = "chat-search-token"
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "search.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: "search", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "transcripts", worktree.ChatOwner(chat.ID).Dir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, body := range transcripts {
		seq := i + 1
		turn, err := st.CreateChatTurn(ctx, chat.ID, fmt.Sprintf("question %d", seq))
		if err != nil {
			t.Fatal(err)
		}
		turn.State, turn.ResultText = chatstate.TurnDone, fmt.Sprintf("answer %d", seq)
		if _, err := st.FinishChatTurn(ctx, turn, ""); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.jsonl", seq)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	agents := agent.NewRegistry(claude.New(func() string { return "claude" }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: agents,
		Catalog: agent.NewCatalogCache(agents), Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = apiclient.New(ts.URL, token)
	v.chatID = chat.ID
	v.composer.Focus()
	loaded := runCmd(t, v.loadCmd(), 30*time.Second)
	msg, ok := loaded.(chatLoadedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("loading the chat produced %#v", loaded)
	}
	v.applyLoaded(msg)
	return v
}

func TestChatFindLive(t *testing.T) {
	// Eight turns, so the oldest is outside the five fetched up front; only
	// the first says the word.
	var transcripts []string
	for seq := 1; seq <= 8; seq++ {
		text := fmt.Sprintf("plain answer %d", seq)
		if seq == 1 {
			text = "the Needle is in turn one"
		}
		transcripts = append(transcripts,
			fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`+"\n", text))
	}
	v := chatFindLive(t, transcripts)
	// The eager fetches: the five newest turns, never turn 1.
	for _, seq := range []int{4, 5, 6, 7, 8} {
		v.fetched[seq] = true
		v.applyTranscript(runCmd(t, v.transcriptCmd(seq), 30*time.Second).(chatTranscriptMsg))
	}
	v.render(100, 30)

	v.openSearchLine()
	v.search.buf.Set("needle")
	_, cmd := v.updateKey(registryKey(t, "enter"))
	res := runCmd(t, cmd, 30*time.Second).(chatSearchMsg)
	if res.err != nil {
		t.Fatalf("search: %v", res.err)
	}
	if res.result.Total != 1 || len(res.result.Turns) != 1 || res.result.Turns[0].Seq != 1 {
		t.Fatalf("search = %+v, want turn 1 alone", res.result)
	}
	fetch := v.applySearch(res)
	if fetch == nil {
		t.Fatal("landing on turn 1 fetched nothing")
	}
	got := runCmd(t, fetch, 30*time.Second).(chatTranscriptMsg)
	if got.seq != 1 {
		t.Fatalf("fetched turn %d, want 1", got.seq)
	}
	v.applyTranscript(got)
	if a := anchorAt(v.anchors, v.cursor); a.turn != 1 {
		t.Fatalf("the cursor is in turn %d, want 1", a.turn)
	}
	if line := ansi.Strip(v.lines[v.cursor]); !strings.Contains(line, "Needle") {
		t.Fatalf("the cursor is on %q", line)
	}
	if label := ansi.Strip(v.edgeLabel()); !strings.Contains(label, "1/1") {
		t.Fatalf("edge label %q, want 1/1", label)
	}
}

// TestChatFindCountsWhatTheDaemonCounts is review F1: the daemon's count for
// a turn the view never loaded is the count the view arrives at once it
// loads it. Real claude transcripts carry a run header whose work_dir and
// available_tools name `Edit`, `Read`, `Bash` and the worktree path; a
// daemon that counted those made every unloaded turn "match" such a query,
// and n/N landed on turns with nothing to show.
func TestChatFindCountsWhatTheDaemonCounts(t *testing.T) {
	var transcripts []string
	for _, name := range []string{
		"stream_blocks_context_2.1.277.jsonl",
		"stream_edit_2.1.268.jsonl",
		"stream_subagent_sync_2.1.263.jsonl",
		"stream_skill_model_2.1.277.jsonl",
	} {
		b, err := os.ReadFile(filepath.Join("..", "agent", "claude", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		transcripts = append(transcripts, string(b))
	}
	v := chatFindLive(t, transcripts)
	for seq := range transcripts {
		v.fetched[seq+1] = true
		v.applyTranscript(runCmd(t, v.transcriptCmd(seq+1), 30*time.Second).(chatTranscriptMsg))
	}
	for _, q := range []string{"edit", "read", "bash", "task", "work", "repo", "the", "claude"} {
		res, err := v.client.SearchChat(context.Background(), v.chatID, q)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		daemon := map[int]int{}
		for _, turn := range res.Turns {
			daemon[turn.Seq] = turn.Matches
		}
		v.search.query = q
		v.search.server = nil
		v.recomputeMatches()
		client := map[int]int{}
		for _, m := range v.search.matches {
			if m.unloaded {
				t.Fatalf("%q: turn %d is loaded but counted as unloaded", q, m.turn)
			}
			client[m.turn]++
		}
		for seq := 1; seq <= len(transcripts); seq++ {
			if daemon[seq] != client[seq] {
				t.Errorf("%q in turn %d: the daemon counts %d, the loaded turn %d",
					q, seq, daemon[seq], client[seq])
			}
		}
	}
}
