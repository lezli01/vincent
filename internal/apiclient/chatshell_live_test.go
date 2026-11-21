package apiclient_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/copilot"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/worktree"
)

// chatPolishHarness is the daemon's chat wiring for task 170 over the real
// handlers: fakeagent behind all four adapters, a task runner resolving the
// shell the way daemon.Run wires it, and the API over both.
type chatPolishHarness struct {
	client    *apiclient.Client
	store     *store.Store
	dataDir   string
	projectID int64
}

func newChatPolishHarness(t *testing.T) *chatPolishHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "chats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	bin := func() string { return fake }
	reg := agent.NewRegistry(claude.New(bin), codex.New(bin), cursor.New(bin), copilot.New(bin))
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
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Broker: broker, Git: git, Worktrees: wt, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: chats, Runner: tasks,
		Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	h := &chatPolishHarness{client: apiclient.New(ts.URL, testToken), store: st, dataDir: dataDir}
	p, err := h.client.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: testrepo.Init(t, "main")})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.projectID = p.ID
	return h
}

func (h *chatPolishHarness) chat(t *testing.T, agentName string) *apiclient.Chat {
	t.Helper()
	c, err := h.client.CreateChat(t.Context(), apiclient.CreateChatRequest{
		ProjectID: h.projectID, Title: "polish " + agentName, Agent: agentName,
	})
	if err != nil {
		t.Fatalf("CreateChat(%s): %v", agentName, err)
	}
	return c
}

// shell runs command to its ending through the client.
func (h *chatPolishHarness) shell(t *testing.T, chatID int64, command string) *apiclient.ChatShellRun {
	t.Helper()
	run, err := h.client.RunChatShell(t.Context(), chatID, command)
	if err != nil {
		t.Fatalf("RunChatShell(%q): %v", command, err)
	}
	if run.State != apiclient.ChatShellRunning || run.ChatID != chatID || run.Command != command {
		t.Fatalf("RunChatShell answered %+v", run)
	}
	return h.awaitShell(t, chatID, run.ID)
}

func (h *chatPolishHarness) awaitShell(t *testing.T, chatID, runID int64) *apiclient.ChatShellRun {
	t.Helper()
	var out *apiclient.ChatShellRun
	wait.Until(t, "the shell run to end", func() bool {
		run, err := h.client.ChatShellRun(t.Context(), chatID, runID)
		if err != nil {
			t.Fatalf("ChatShellRun: %v", err)
		}
		out = run
		return run.State != apiclient.ChatShellRunning
	})
	return out
}

// wantAPIError asserts err is the envelope with this status and code.
func wantAPIError(t *testing.T, what string, err error, status int, code string) *apiclient.Error {
	t.Helper()
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("%s = %v, want %d %s", what, err, status, code)
	}
	return apiErr
}

func TestLiveChatShell(t *testing.T) {
	h := newChatPolishHarness(t)
	ctx := t.Context()
	c := h.chat(t, "claude")

	run := h.shell(t, c.ID, "git --version")
	if run.State != apiclient.ChatShellExited || run.ExitCode == nil || *run.ExitCode != 0 ||
		run.EndedAt == nil || run.Truncated || run.Attach || run.AttachedTurn != nil || run.AfterTurn != 0 {
		t.Fatalf("finished run = %+v", run)
	}
	out, err := h.client.ChatShellOutput(ctx, c.ID, run.ID)
	if err != nil || !strings.Contains(out, "git version") || int64(len(out)) != run.OutputBytes {
		t.Errorf("ChatShellOutput = %q (%v), row says %d bytes", out, err, run.OutputBytes)
	}
	failed := h.shell(t, c.ID, "exit 5")
	if failed.ExitCode == nil || *failed.ExitCode != 5 {
		t.Errorf("exit 5 recorded %v", failed.ExitCode)
	}
	runs, err := h.client.ChatShellRuns(ctx, c.ID)
	if err != nil || len(runs) != 2 || runs[0].ID != run.ID || runs[1].ID != failed.ID {
		t.Errorf("ChatShellRuns = %+v (%v), want both, oldest first", runs, err)
	}

	// Attach, unattach, attach: the mark is the client's to flip until a
	// turn takes it.
	for _, attach := range []bool{true, false, true} {
		got, err := h.client.AttachChatShell(ctx, c.ID, failed.ID, attach)
		if err != nil || got.Attach != attach {
			t.Fatalf("AttachChatShell(%v) = %+v, %v", attach, got, err)
		}
	}

	// Refusals.
	_, err = h.client.RunChatShell(ctx, c.ID, "  ")
	_ = wantAPIError(t, "an empty command", err, http.StatusUnprocessableEntity, "validation_failed")
	_, err = h.client.ChatShellRun(ctx, c.ID, 9999)
	_ = wantAPIError(t, "an unknown run", err, http.StatusNotFound, "not_found")
	long, err := h.client.RunChatShell(ctx, c.ID, "sleep 60")
	if err != nil {
		t.Fatalf("RunChatShell: %v", err)
	}
	_, err = h.client.RunChatShell(ctx, c.ID, "git --version")
	_ = wantAPIError(t, "a second command while one runs", err, http.StatusConflict, "invalid_state")
	_, err = h.client.AttachChatShell(ctx, c.ID, long.ID, true)
	_ = wantAPIError(t, "attach on a running run", err, http.StatusConflict, "invalid_state")
	// The chat's cancel stops it, though the chat is idle.
	if err := h.client.CancelChat(ctx, c.ID); err != nil {
		t.Fatalf("CancelChat: %v", err)
	}
	if got := h.awaitShell(t, c.ID, long.ID); got.State != apiclient.ChatShellCanceled {
		t.Errorf("canceled run is %q", got.State)
	}

	// Every change was published as chat.shell_changed, in the chat's
	// replayable family.
	evs, err := h.store.ListEvents(ctx, store.EventFilter{Types: []string{apiclient.EventChatShell}})
	if err != nil || len(evs) < 6 {
		t.Errorf("%d %s events (%v), want one per change", len(evs), apiclient.EventChatShell, err)
	}
	if !slices.Contains(store.ChatEventTypes(), apiclient.EventChatShell) {
		t.Errorf("%s is not in the chat event family", apiclient.EventChatShell)
	}

	// The marked run reaches the next turn and no later one.
	promptFile := filepath.Join(t.TempDir(), "prompts")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	if _, err := h.client.SendChat(ctx, c.ID, "why did that fail?"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	wait.Until(t, "the turn to take the run", func() bool {
		got, err := h.client.ChatShellRun(ctx, c.ID, failed.ID)
		return err == nil && got.AttachedTurn != nil && *got.AttachedTurn == 1 && !got.Attach
	})
	wait.Until(t, "the turn to end", func() bool {
		got, _, err := h.client.GetChat(ctx, c.ID)
		return err == nil && got.State == "idle"
	})
	seen, _ := os.ReadFile(promptFile)
	if !strings.Contains(string(seen), `shell-run command=\"exit 5\"`) || strings.Contains(string(seen), "git --version") {
		t.Errorf("the agent was handed:\n%s", seen)
	}
	_, turns, err := h.client.GetChat(ctx, c.ID)
	if err != nil || len(turns) != 1 || turns[0].Prompt != "why did that fail?" {
		t.Errorf("the turn row = %+v (%v), want the message as typed", turns, err)
	}
	_, err = h.client.AttachChatShell(ctx, c.ID, failed.ID, true)
	_ = wantAPIError(t, "attach on a taken run", err, http.StatusConflict, "invalid_state")

	// A terminal chat runs nothing, and its runs stay readable.
	if _, err := h.client.ArchiveChat(ctx, c.ID, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	_, err = h.client.RunChatShell(ctx, c.ID, "git --version")
	_ = wantAPIError(t, "a command on an archived chat", err, http.StatusConflict, "invalid_state")
	if runs, err := h.client.ChatShellRuns(ctx, c.ID); err != nil || len(runs) != 3 {
		t.Errorf("an archived chat lists %d runs (%v), want 3", len(runs), err)
	}
}

var livePNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestLiveChatImages(t *testing.T) {
	h := newChatPolishHarness(t)
	ctx := t.Context()

	agents, err := h.client.ListAgents(ctx, false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	want := map[string]bool{"claude": false, "codex": true, "cursor": false, "copilot": true}
	for _, a := range agents {
		if a.SupportsImages == nil || *a.SupportsImages != want[a.Name] {
			t.Errorf("%s supports_images = %v, want %v", a.Name, a.SupportsImages, want[a.Name])
		}
	}

	record := filepath.Join(t.TempDir(), "images")
	t.Setenv("FAKEAGENT_IMAGES_FILE", record)
	c := h.chat(t, "codex")
	first, err := h.client.AddChatImage(ctx, c.ID, "screen shot.png", livePNG)
	if err != nil {
		t.Fatalf("AddChatImage: %v", err)
	}
	if first.Number != 1 || first.Chip != "[image 1]" || first.MediaType != "image/png" ||
		first.Bytes != int64(len(livePNG)) || first.Name != "screen shot.png" {
		t.Errorf("first image = %+v", first)
	}
	second, err := h.client.AddChatImage(ctx, c.ID, "", []byte("GIF89a\x01\x00\x01\x00"))
	if err != nil || second.Number != 2 || second.Chip != "[image 2]" || second.MediaType != "image/gif" {
		t.Fatalf("second image = %+v, %v", second, err)
	}
	list, err := h.client.ChatImages(ctx, c.ID)
	if err != nil || len(list) != 2 || list[0] != *first || list[1] != *second {
		t.Errorf("ChatImages = %+v (%v)", list, err)
	}

	_, err = h.client.AddChatImage(ctx, c.ID, "notes.png", []byte("plain text"))
	_ = wantAPIError(t, "a non-image", err, http.StatusUnprocessableEntity, "validation_failed")
	_, err = h.client.AddChatImage(ctx, c.ID, "big.png", append(slices.Clone(livePNG), make([]byte, 10<<20)...))
	_ = wantAPIError(t, "an image over 10 MiB", err, http.StatusUnprocessableEntity, "validation_failed")
	_, err = h.client.SendChat(ctx, c.ID, "look at [image 7]")
	if e := wantAPIError(t, "a chip with no image", err, http.StatusUnprocessableEntity, "validation_failed"); !strings.Contains(e.Message, "codex") {
		t.Errorf("the refusal %q does not name the agent", e.Message)
	}

	// The chip resolves to the stored file for the adapter; the turn keeps
	// the message as typed.
	if _, err := h.client.SendChat(ctx, c.ID, "describe [image 2]"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	wait.Until(t, "the turn to end", func() bool {
		got, turns, err := h.client.GetChat(ctx, c.ID)
		return err == nil && got.State == "idle" && len(turns) == 1 && turns[0].State == "done"
	})
	passed, err := os.ReadFile(record)
	stored := filepath.Join(h.dataDir, "transcripts", worktree.ChatOwner(c.ID).Dir(), "images", "2.gif")
	if err != nil || strings.TrimSpace(string(passed)) != stored {
		t.Errorf("codex was passed %q (%v), want %q", passed, err, stored)
	}

	// An adapter without the capability takes neither an upload nor a chip.
	plain := h.chat(t, "claude")
	_, err = h.client.AddChatImage(ctx, plain.ID, "a.png", livePNG)
	if e := wantAPIError(t, "an upload to claude", err, http.StatusUnprocessableEntity, "validation_failed"); !strings.Contains(e.Message, "claude") {
		t.Errorf("the refusal %q does not name the agent", e.Message)
	}
	_, err = h.client.SendChat(ctx, plain.ID, "see [image 1]")
	_ = wantAPIError(t, "a chip sent to claude", err, http.StatusUnprocessableEntity, "validation_failed")

	// Shell output and images go with the chat on a permanent delete.
	h.shell(t, c.ID, "git --version")
	dir := filepath.Join(h.dataDir, "transcripts", worktree.ChatOwner(c.ID).Dir())
	for _, sub := range []string{"images", "shell"} {
		if entries, err := os.ReadDir(filepath.Join(dir, sub)); err != nil || len(entries) == 0 {
			t.Fatalf("%s holds nothing before the delete (%v)", sub, err)
		}
	}
	if _, err := h.client.ForceDeleteChat(ctx, c.ID); err != nil {
		t.Fatalf("ForceDeleteChat: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the chat's directory survived its delete (%v)", err)
	}
	if runs, _ := h.store.ListChatShellRuns(ctx, c.ID); len(runs) != 0 {
		t.Errorf("%d shell rows survived the delete", len(runs))
	}
	if imgs, _ := h.store.ListChatImages(ctx, c.ID); len(imgs) != 0 {
		t.Errorf("%d image rows survived the delete", len(imgs))
	}

	// A terminal chat takes no image.
	if _, err := h.client.ArchiveChat(ctx, plain.ID, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	_, err = h.client.AddChatImage(ctx, plain.ID, "a.png", livePNG)
	_ = wantAPIError(t, "an upload to an archived chat", err, http.StatusConflict, "invalid_state")
}
