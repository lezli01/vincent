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

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// The chat's diff pane (task 168) against the real handlers, a real store and
// a real git worktree: what the pane draws is the daemon's diff, a reject
// changes the file on disk, and the note it writes reaches the composer.

// chatDiffLive is the view under test and the pump that drives it. Commands
// run on their own goroutines — they shell out to git through the daemon —
// and their messages come back on msgs, folded in on the test's goroutine.
type chatDiffLive struct {
	t    *testing.T
	v    *chatView
	dir  string
	msgs chan tea.Msg
	done chan struct{}
}

// chatDiffLines is the tracked file the worktree changes: thirty lines, so
// two edits far enough apart are two hunks.
func chatDiffLines(edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		if v, ok := edit[i]; ok {
			b.WriteString(v + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func newChatDiffLive(t *testing.T) *chatDiffLive {
	t.Helper()
	const token = "chat-diff-token"
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "diff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repo := testrepo.Init(t, "main")
	// Whatever the host's git would do to line endings, these files hold LF.
	testrepo.Run(t, repo, "config", "core.autocrlf", "false")
	testrepo.WriteFile(t, repo, "long.txt", chatDiffLines(nil))
	testrepo.Run(t, repo, "add", "long.txt")
	testrepo.Run(t, repo, "commit", "-q", "-m", "long")
	dir := filepath.Join(t.TempDir(), "wt")
	testrepo.Run(t, repo, "worktree", "add", "-q", "-b", "chat-diff", dir, "main")
	testrepo.WriteFile(t, dir, "long.txt", chatDiffLines(map[int]string{2: "line 2 edited", 25: "line 25 edited"}))
	testrepo.WriteFile(t, dir, "new.txt", "brand new\n")

	p := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	c := &store.Chat{
		ProjectID: p.ID, Title: "review me", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", Branch: "chat-diff", WorktreePath: dir,
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	// One finished turn that carries no checkpoint, so the turn scope has
	// nothing to diff from and says so.
	turn, err := st.CreateChatTurn(ctx, c.ID, "change two lines")
	if err != nil {
		t.Fatal(err)
	}
	turn.State = chatstate.TurnDone
	if _, err := st.FinishChatTurn(ctx, turn, ""); err != nil {
		t.Fatal(err)
	}

	reg := agent.NewRegistry()
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st,
		Agents: reg, Git: gitx.New(), Catalog: agent.NewCatalogCache(reg), Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = apiclient.New(ts.URL, token)
	l := &chatDiffLive{t: t, v: v, dir: dir, msgs: make(chan tea.Msg, 256), done: make(chan struct{})}
	t.Cleanup(func() { close(l.done) })

	// The workspace around the conversation, so the Details pane is drawn.
	side := newChatsView()
	side.client = v.client
	side.project = projectSel{id: p.ID, name: p.Name}
	side.chats = []apiclient.Chat{{ID: c.ID, ProjectID: p.ID, Title: c.Title, State: "idle", Agent: "claude"}}
	side.loaded = true
	side.restoreSelection()
	v.side = side
	v.update(tea.WindowSizeMsg{Width: 170, Height: 44})

	// The open, without the stream: no broker is wired, and a blocking read
	// is not what this test is about. The `changes` read goes with it, as
	// openAt sends it.
	openChat(t, v, c.ID)
	v.resetDiff()
	l.run(v.diffStatCmd())
	return l
}

// run starts cmd, flattening batches, and queues what it answers.
func (l *chatDiffLive) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				l.run(c)
			}
			return
		}
		if msg == nil {
			return
		}
		select {
		case l.msgs <- msg:
		case <-l.done:
		}
	}()
}

// press sends one key to the view.
func (l *chatDiffLive) press(key string) {
	l.t.Helper()
	_, cmd := l.v.update(registryKey(l.t, key))
	l.run(cmd)
}

// until folds in whatever the commands have answered until cond holds.
func (l *chatDiffLive) until(what string, cond func() bool) {
	l.t.Helper()
	wait.Until(l.t, what, func() bool {
		for {
			select {
			case msg := <-l.msgs:
				_, cmd := l.v.update(msg)
				l.run(cmd)
				continue
			default:
			}
			return cond()
		}
	})
}

func (l *chatDiffLive) screen() string { return ansi.Strip(l.v.render(170, 44)) }

func (l *chatDiffLive) file(name string) string {
	l.t.Helper()
	data, err := os.ReadFile(filepath.Join(l.dir, name))
	if err != nil {
		l.t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// selected is the hunk under the pane's cursor.
func (l *chatDiffLive) selected() diffHunkRef {
	ref, _ := l.v.diff.pane.selectedHunk()
	return ref
}

func TestChatDiffPaneLive(t *testing.T) {
	l := newChatDiffLive(t)
	v := l.v

	// The Details pane's `changes` row fills from the stat read, and names
	// the key that opens the diff.
	l.until("the changes row", func() bool { return v.diff.stat != nil })
	if got := l.screen(); !strings.Contains(got, "changes") || !strings.Contains(got, "2 files +3 -2") ||
		!strings.Contains(got, "alt+d opens the diff") {
		t.Fatalf("the Details pane has no changes row (stat %+v):\n%s", v.diff.stat, got)
	}

	// The key swaps the pane and reads the diff.
	v.composer.SetValue("")
	l.press("alt+d")
	l.until("the diff", func() bool { return v.diff.loaded && !v.diff.fetching })
	if got := l.screen(); !strings.Contains(got, "long.txt") || !strings.Contains(got, "new.txt") ||
		!strings.Contains(got, "since the base") || strings.Contains(got, "change two lines") {
		t.Fatalf("the diff did not take the conversation's place:\n%s", got)
	}

	// ] and [ walk the hunks: long.txt's two, then new.txt's one.
	l.press("]")
	first := l.selected()
	l.press("]")
	second := l.selected()
	if first.path != "long.txt" || second.path != "long.txt" || first.id == "" || first.id == second.id {
		t.Fatalf("] walked %+v then %+v", first, second)
	}
	if got := l.screen(); !strings.Contains(got, "+line 25 edited") {
		t.Fatalf("landing on a hunk did not open its file:\n%s", got)
	}
	l.press("]")
	if got := l.selected(); got.path != "new.txt" {
		t.Fatalf("a third ] landed on %+v, want new.txt", got)
	}
	l.press("[")
	l.press("[")
	if got := l.selected(); got.id != first.id {
		t.Fatalf("[ [ landed on %+v, want %+v", got, first)
	}

	// a marks the hunk, and a second a clears the mark — both the daemon's.
	l.press("a")
	l.until("the accept", func() bool { return !v.diff.busy && l.selected().accepted })
	if got := l.screen(); !strings.Contains(got, "1/2") {
		t.Fatalf("the file row does not count the reviewed hunk:\n%s", got)
	}
	l.press("a")
	l.until("the mark to clear", func() bool { return !v.diff.busy && !l.selected().accepted })
	if got := l.screen(); !strings.Contains(got, "0/2") {
		t.Fatalf("the file row still counts a reviewed hunk:\n%s", got)
	}

	// x rejects the hunk: the file on disk changes, the diff and the changes
	// row are read again, and the note is the chat's draft.
	if !strings.Contains(first.header, "@@") {
		t.Fatalf("the selected hunk has no header: %+v", first)
	}
	l.press("x")
	l.until("the reject", func() bool {
		return !v.diff.busy && !v.diff.fetching && v.diff.stat != nil && v.diff.stat.Removed == 1
	})
	if got, want := l.file("long.txt"), chatDiffLines(map[int]string{25: "line 25 edited"}); got != want {
		t.Fatalf("long.txt after the reject:\n%s", got)
	}
	if got := l.screen(); !strings.Contains(got, "2 files +2 -1") {
		t.Fatalf("the changes row was not read again after the reject:\n%s", got)
	}
	if v.noteBad || !strings.Contains(v.note, "hunk rejected") {
		t.Fatalf("after the reject the note is %q (bad %v)", v.note, v.noteBad)
	}

	// s switches the scope; this chat's turn has no checkpoint, which is an
	// empty state carrying the daemon's reason, not a fall back to the base.
	l.press("s")
	l.until("the turn scope", func() bool { return v.diff.loaded && !v.diff.fetching })
	if got := l.screen(); !strings.Contains(got, "since the last turn") ||
		!strings.Contains(got, "turn 1 has no checkpoint to diff from") || strings.Contains(got, "new.txt") {
		t.Fatalf("the turn scope's empty state:\n%s", got)
	}
	l.press("s")
	l.until("the base scope again", func() bool { return v.diff.loaded && !v.diff.fetching })
	if got := l.screen(); !strings.Contains(got, "long.txt") {
		t.Fatalf("switching back did not read the base scope:\n%s", got)
	}

	// esc: the conversation is back, and the composer shows the note.
	l.press("esc")
	if v.diff.open || !v.composer.Focused() {
		t.Fatal("esc did not give the keyboard back to the composer")
	}
	draft := v.composer.Value()
	if !strings.Contains(draft, "long.txt "+first.header) {
		t.Fatalf("the composer does not show the reject's note: %q", draft)
	}
	stored, err := v.client.ChatDraft(context.Background(), v.chatID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Draft != draft {
		t.Fatalf("the composer holds %q, the daemon %q", draft, stored.Draft)
	}
	if got := l.screen(); !strings.Contains(got, "change two lines") {
		t.Fatalf("esc did not bring the conversation back:\n%s", got)
	}
}
