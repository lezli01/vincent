package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// Task 170 against the real handlers, a real store, the chat runner and a
// real worktree: a command typed into the shell prompt runs, its card shows
// the exit code and the output the daemon kept, `a` attaches it, and a
// pasted image becomes the daemon's chip in the draft. The stream is the real
// one too, so the cards move on chat.shell_changed and nothing else.

const chatPolishToken = "chat-polish-token"

// newChatPolishLive wires the daemon's chat half the way daemon.Run does and
// opens one codex chat in a chat view, subscribed to its stream.
func newChatPolishLive(t *testing.T) *chatDiffLive {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "polish.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	// codex, because it is an adapter that takes images (§9.x); claude
	// does not, and its refusal is chatshell_test.go's.
	reg := agent.NewRegistry(codex.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var tasks *taskrun.Runner
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg,
		DataDir: dataDir, Logger: log, Events: broker,
		Shell: func(ctx context.Context, taskID, runID int64, workDir, command string) (chatrun.ShellExec, error) {
			ex, err := tasks.ChatShell(ctx, taskID, runID, workDir, command)
			return chatrun.ShellExec{Argv: ex.Argv, Dir: ex.Dir, Env: ex.Env, Stop: ex.Stop}, err
		},
		InContainer: func(ctx context.Context, taskID int64) (bool, error) {
			return tasks.ChatRunsInContainer(ctx, taskID)
		},
	})
	tasks = taskrun.New(taskrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
		ChatTurns: chats,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	s := api.New(api.Deps{
		Token: chatPolishToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Broker: broker, Git: git, Worktrees: wt, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: chats, Runner: tasks,
		Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	client := apiclient.New(ts.URL, chatPolishToken)
	p, err := client.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: testrepo.Init(t, "main")})
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.CreateChat(t.Context(), apiclient.CreateChatRequest{ProjectID: p.ID, Title: "polish", Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}

	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = client
	l := &chatDiffLive{t: t, v: v, msgs: make(chan tea.Msg, 256), done: make(chan struct{})}
	t.Cleanup(func() { close(l.done) })
	v.update(tea.WindowSizeMsg{Width: 120, Height: 44})
	openChat(t, v, c.ID)
	l.run(v.streamCmd())
	t.Cleanup(func() {
		if v.streamStop != nil {
			v.streamStop()
		}
	})
	l.run(v.shellRunsCmd())
	return l
}

func TestChatShellAndImageLive(t *testing.T) {
	l := newChatPolishLive(t)
	v := l.v

	// The prompt: alt+x, the command, enter. The composer is not involved.
	v.composer.SetValue("a draft that stays")
	l.press("alt+x")
	if !v.shell.prompt {
		t.Fatalf("alt+x opened no prompt (note %q)", v.status().text)
	}
	v.paste("git --version")
	l.press("enter")
	l.until("the run's card with its output", func() bool {
		return len(v.shell.runs) == 1 && v.shell.runs[0].State != apiclient.ChatShellRunning &&
			strings.Contains(v.shell.out[v.shell.runs[0].ID], "git version")
	})
	run := v.shell.runs[0]
	if run.State != apiclient.ChatShellExited || run.ExitCode == nil || *run.ExitCode != 0 {
		t.Fatalf("the run ended %+v", run)
	}
	screen := l.screen()
	for _, want := range []string{"$ git --version", "exit 0", "│ git version"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the card lacks %q:\n%s", want, screen)
		}
	}
	if v.composer.Value() != "a draft that stays" {
		t.Fatalf("the shell touched the draft: %q", v.composer.Value())
	}

	// Attach: the conversation's cursor onto the card, then `a`.
	v.focusConversation()
	l.screen()
	v.moveCursor(shellCardLine(v, run.ID))
	l.press("a")
	l.until("the attach mark", func() bool { return len(v.shell.runs) == 1 && v.shell.runs[0].Attach })
	if screen := l.screen(); !strings.Contains(screen, "attached to next prompt") {
		t.Fatalf("the card does not say it is attached:\n%s", screen)
	}

	// An image pasted from the clipboard.
	l.press("esc") // the keyboard back to the composer
	withClipImage(t, fakePNG, nil)
	v.composer.SetValue("see ")
	v.composer.CursorEnd()
	l.press("ctrl+v")
	l.until("the image chip", func() bool { return strings.Contains(v.composer.Value(), "[image 1]") })
	if got := v.composer.Value(); got != "see [image 1]" {
		t.Fatalf("the draft after the paste: %q (note %q)", got, v.status().text)
	}
	images, err := v.client.ChatImages(t.Context(), v.chatID)
	if err != nil || len(images) != 1 || images[0].Chip != "[image 1]" {
		t.Fatalf("the daemon holds %+v (%v)", images, err)
	}

	// A refused image — no image signature — inserts nothing and says why.
	withClipImage(t, []byte("plain text"), nil)
	before := v.composer.Value()
	l.press("ctrl+v")
	l.until("the refusal", func() bool { return v.status().bad && strings.Contains(v.status().text, "image") })
	if v.composer.Value() != before {
		t.Fatalf("a refused image changed the draft to %q", v.composer.Value())
	}
}
