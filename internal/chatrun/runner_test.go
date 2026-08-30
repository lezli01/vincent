package chatrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

type harness struct {
	store   *store.Store
	runner  *Runner
	repo    string
	project *store.Project
	dataDir string
	// sessionDir is the fake CLI's own conversation store. It lives outside
	// vincent's data directory on purpose: continuity has to come from the
	// agent remembering, not from anything vincent kept.
	sessionDir string
	cfg        config.Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "chat.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h := &harness{
		store: st, repo: repo, project: project, dataDir: dataDir,
		sessionDir: t.TempDir(), cfg: config.Default(),
	}
	t.Setenv("FAKEAGENT_SESSION_DIR", h.sessionDir)
	h.runner = New(Deps{
		Store:     st,
		Config:    func() config.Config { return h.cfg },
		Worktrees: worktree.NewManager(gitx.New(), dataDir),
		Agents: agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return fake }),
			cursor.New(func() string { return fake }),
		),
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.runner.Start(t.Context())
	t.Cleanup(h.runner.Stop)
	return h
}

// chat creates a chat with a real worktree, the way the API does. It is always
// on claude: the other two adapters cannot resume, so a chat on one never gets
// past creation — which is asserted at the adapter level in
// TestAdaptersThatCannotResume and over the wire in internal/api.
func (h *harness) chat(t *testing.T) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.project.ID, Title: "a talk", State: chatstate.Idle,
		Agent: "claude", PermissionMode: string(agent.FullAuto), BaseBranch: "main",
	}
	if err := h.store.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	c.Branch = worktree.BranchName(c.ID, c.Title)
	if err := h.store.SetChatBranch(t.Context(), c.ID, c.Branch); err != nil {
		t.Fatalf("SetChatBranch: %v", err)
	}
	created, err := worktree.NewManager(gitx.New(), h.dataDir).CreateAndClaim(
		t.Context(), h.repo, worktree.ChatOwner(c.ID), c.Branch, "main", false,
		func(w worktree.Created) error {
			_, err := h.store.SetChatWorktree(t.Context(), c.ID, w.Path, w.BaseSHA)
			return err
		})
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	c.WorktreePath = created.Path
	return c
}

// sendAndWait runs one turn to completion and returns the finished row.
func (h *harness) sendAndWait(t *testing.T, chatID int64, prompt string) *store.ChatTurn {
	t.Helper()
	turn, err := h.runner.Send(t.Context(), chatID, prompt)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return h.waitTurn(t, turn.ID)
}

// waitIdle waits for the chat itself to come to rest. finish writes the turn's
// ending *before* it returns the chat to idle — deliberately, so a reader never
// sees an idle chat with a turn still running — so a terminal turn row is not
// yet proof that the chat is done.
func (h *harness) waitIdle(t *testing.T, chatID int64) *store.Chat {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.store.GetChat(t.Context(), chatID)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if got.State == chatstate.Idle {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat %d never returned to idle", chatID)
	return nil
}

func (h *harness) waitTurn(t *testing.T, turnID int64) *store.ChatTurn {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.store.GetChatTurn(t.Context(), turnID)
		if err != nil {
			t.Fatalf("GetChatTurn: %v", err)
		}
		if chatstate.TurnTerminal(got.State) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("turn %d never finished", turnID)
	return nil
}

// TestTurnTwoSeesTurnOne is the acceptance criterion of task 063 and the
// reason the feature exists: the second turn answers with something only the
// first supplies. Nothing here replays a log — the fake CLI is asked to resume
// its own session, and it says what it remembers.
func TestTurnTwoSeesTurnOne(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)

	first := h.sendAndWait(t, c.ID, "my favourite colour is heliotrope")
	if first.State != chatstate.TurnDone {
		t.Fatalf("turn 1 = %s (%s: %s)", first.State, first.FailReason, first.ErrorMessage)
	}
	if first.SessionID == "" {
		t.Fatal("turn 1 recorded no session id, so there is nothing to resume")
	}
	// The chat, not the turn, is what the next turn resumes.
	after := h.waitIdle(t, c.ID)
	if after.SessionID != first.SessionID {
		t.Fatalf("chat session id = %q, want the turn's %q", after.SessionID, first.SessionID)
	}

	second := h.sendAndWait(t, c.ID, "what is it again?")
	if second.State != chatstate.TurnDone {
		t.Fatalf("turn 2 = %s (%s: %s)", second.State, second.FailReason, second.ErrorMessage)
	}
	// The recall line is only ever emitted by a run that was handed a
	// session the store already held, so its presence is the proof.
	body := h.transcript(t, c.ID, second.Seq)
	if !strings.Contains(body, "heliotrope") {
		t.Fatalf("turn 2 does not recall turn 1; transcript:\n%s", body)
	}
	if !strings.Contains(body, "recalled:") {
		t.Fatalf("turn 2 ran a fresh session, not a resume; transcript:\n%s", body)
	}
	if second.Seq != 2 {
		t.Fatalf("turn 2 seq = %d, want 2", second.Seq)
	}
}

// transcript reads one turn's durable record — the file, not the broker, since
// that is what a reader gets after the fact.
func (h *harness) transcript(t *testing.T, chatID int64, seq int) string {
	t.Helper()
	path := filepath.Join(h.runner.transcriptDir(chatID), fmt.Sprintf("%d.jsonl", seq))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return string(b)
}

// TestSessionLostFailsTheTurnAndLeavesTheChatUsable is decision 4. A silently
// fresh session would answer as if it had context it does not have, and a
// reader could not tell that apart from a working conversation.
func TestSessionLostFailsTheTurnAndLeavesTheChatUsable(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	if _, err := h.store.SetChatSession(t.Context(), c.ID, "fake-session-gone"); err != nil {
		t.Fatalf("SetChatSession: %v", err)
	}

	turn := h.sendAndWait(t, c.ID, "still there?")
	if turn.State != chatstate.TurnFailed {
		t.Fatalf("turn state = %s, want failed", turn.State)
	}
	if turn.FailReason != ReasonSessionLost {
		t.Fatalf("fail reason = %q, want %q", turn.FailReason, ReasonSessionLost)
	}
	after := h.waitIdle(t, c.ID)
	if after.SessionID != "fake-session-gone" {
		t.Fatalf("session id = %q; a lost session is not silently cleared", after.SessionID)
	}
	// And nothing was fabricated: no fresh conversation was written.
	entries, err := os.ReadDir(h.sessionDir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a fresh session was started behind the human's back: %v", entries)
	}
}

// TestCapRefusesRatherThanQueues is decision 1. The refusal is immediate and
// the turn never becomes a row waiting for a slot: a chat that queued would be
// exactly the wait chats exist to avoid.
func TestCapRefusesRatherThanQueues(t *testing.T) {
	h := newHarness(t)
	h.cfg.MaxParallelChats = 1
	t.Setenv("FAKEAGENT_SCENARIO", "hang")

	busy := h.chat(t)
	if _, err := h.runner.Send(t.Context(), busy.ID, "hold the line"); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	waitFor(t, func() bool { return h.runner.Running(busy.ID) })

	other := h.chat(t)
	_, err := h.runner.Send(t.Context(), other.ID, "me too")
	if !errors.Is(err, ErrChatCapReached) {
		t.Fatalf("second Send err = %v, want ErrChatCapReached", err)
	}
	turns, err := h.store.ListChatTurns(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("ListChatTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("a refused send wrote %d turn rows; it must queue nothing", len(turns))
	}
	if err := h.runner.Cancel(t.Context(), busy.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

// TestChatsNeverAppearAsTasks: the separate entity is the whole design, and a
// chat leaking onto the board would undo it.
func TestChatsNeverAppearAsTasks(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	h.sendAndWait(t, c.ID, "hello")

	tasks, err := h.store.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("ListTasks returned %d rows for a store holding only chats", len(tasks))
	}
	chats, err := h.store.ListChats(t.Context(), store.ChatFilter{})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("ListChats = %d, want 1", len(chats))
	}
}

// TestRecoverFinalizesInterruptedAndDoesNotResend is decision 5. §12.4's
// auto-resume rule stops applying here: re-running would re-send the human's
// message, and the session being resumed died with the process.
func TestRecoverFinalizesInterruptedAndDoesNotResend(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	// A turn journaled as running by a daemon that is now gone. The pid is
	// this process's, which is alive but carries no matching identity, so
	// the PID-reuse guard must refuse to kill it.
	turn, err := h.store.CreateChatTurn(t.Context(), c.ID, "the message that must not be re-sent")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	pid := os.Getpid()
	identity := "not-the-identity-this-pid-has"
	turn.PID = &pid
	turn.ProcIdentity = &identity
	if err := h.store.UpdateChatTurn(t.Context(), turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}

	if err := h.runner.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := h.store.GetChatTurn(t.Context(), turn.ID)
	if err != nil {
		t.Fatalf("GetChatTurn: %v", err)
	}
	if got.State != chatstate.TurnInterruptedState {
		t.Fatalf("turn state = %s, want interrupted", got.State)
	}
	after, err := h.store.GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if after.State != chatstate.Idle {
		t.Fatalf("chat state = %s, want idle", after.State)
	}
	// Nothing re-ran: the conversation store is still empty, so the human's
	// message was not sent to any agent.
	entries, err := os.ReadDir(h.sessionDir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("an interrupted turn was re-sent: %v", entries)
	}
	turns, err := h.store.ListChatTurns(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("ListChatTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("recovery created %d turns, want the original 1", len(turns))
	}
}

// TestAdaptersThatCannotResume states the capability positively, in both
// directions (decision 3). No adapter ever replays a log as prompt context.
func TestAdaptersThatCannotResume(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	bin := func() string { return fake }
	for _, tc := range []struct {
		name string
		a    agent.Adapter
		want bool
	}{
		{"claude", claude.New(bin), true},
		{"codex", codex.New(bin), false},
		{"cursor", cursor.New(bin), false},
	} {
		if got := agent.CanResume(tc.a); got != tc.want {
			t.Errorf("CanResume(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
