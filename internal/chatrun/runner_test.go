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

// chat creates a chat with a real worktree, the way the API does, on claude —
// the adapter whose dialect every scenario in this file was written against.
func (h *harness) chat(t *testing.T) *store.Chat { return h.chatOn(t, "claude") }

// chatOn is chat, on a named adapter. All three shipped adapters can resume
// since task 070, so the tests that are *about* resuming run on each of them.
func (h *harness) chatOn(t *testing.T, agentName string) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.project.ID, Title: "a talk", State: chatstate.Idle,
		Agent: agentName, PermissionMode: string(agent.FullAuto), BaseBranch: "main",
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
//
// It runs on all three adapters since task 070. Each subtest exercises a
// different argv and a different stream: claude's `--resume <id>` with the id
// on every line, codex's `exec resume <id> -` with a `thread_id` on one, and
// cursor's `--resume <id>` with a `session_id` on every line. A vincent that
// dropped the id for any one of them fails by omission — a fresh session emits
// no recall line at all — rather than by a difference a reader has to squint
// at.
func TestTurnTwoSeesTurnOne(t *testing.T) {
	for _, agentName := range []string{"claude", "codex", "cursor"} {
		t.Run(agentName, func(t *testing.T) {
			h := newHarness(t)
			c := h.chatOn(t, agentName)

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
		})
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

// TestSessionLostFailsTheTurnAndLeavesTheChatUsable is task 063 decision 4. A
// silently fresh session would answer as if it had context it does not have,
// and a reader could not tell that apart from a working conversation.
//
// claude and codex both refuse an id they never minted, in their own wordings,
// and both fixtures back their adapter's markers. cursor is absent on purpose
// and TestCursorAdoptsAnUnknownSession is why.
func TestSessionLostFailsTheTurnAndLeavesTheChatUsable(t *testing.T) {
	for _, agentName := range []string{"claude", "codex"} {
		t.Run(agentName, func(t *testing.T) {
			h := newHarness(t)
			c := h.chatOn(t, agentName)
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
		})
	}
}

// TestCursorAdoptsAnUnknownSession states cursor's gap positively, which is
// the project's rule for a capability an adapter lacks (§9.7, task 070
// decision 2).
//
// cursor-agent 2026.08.11 does not refuse `--resume` of an id it has never
// seen: it starts a fresh chat *under that id*, exits 0 and answers normally
// (internal/agent/cursor/testdata/resume_unknown_2026.08.11.jsonl). There is
// no wording to classify because there is no refusal, so a cursor chat whose
// id has aged out gets an answer with no memory rather than `session_lost`.
// The turn succeeds, and the test says so rather than pretending otherwise.
func TestCursorAdoptsAnUnknownSession(t *testing.T) {
	h := newHarness(t)
	c := h.chatOn(t, "cursor")
	if _, err := h.store.SetChatSession(t.Context(), c.ID, "fake-session-gone"); err != nil {
		t.Fatalf("SetChatSession: %v", err)
	}

	turn := h.sendAndWait(t, c.ID, "still there?")
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s (%s: %s), want done: cursor has no session-lost refusal to classify",
			turn.State, turn.FailReason, turn.ErrorMessage)
	}
	if body := h.transcript(t, c.ID, turn.Seq); strings.Contains(body, "recalled:") {
		t.Fatalf("a chat with no prior turns recalled something; transcript:\n%s", body)
	}
	after := h.waitIdle(t, c.ID)
	if after.SessionID != "fake-session-gone" {
		t.Fatalf("session id = %q; cursor keeps the id it was handed", after.SessionID)
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

// TestAdaptersThatCanResume states the capability positively, in both
// directions (task 063 decision 3, task 070). No adapter ever replays a log as
// prompt context; the ones that cannot resume are refused instead.
//
// The false leg is agenttest.StubNonResuming rather than a shipped adapter.
// Until task 070 it was codex and cursor, and the day they learned to resume
// this test asserted the opposite of the truth — which is what any refusal
// pinned to whichever CLI happens to lack a capability today will keep doing.
func TestAdaptersThatCanResume(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	bin := func() string { return fake }
	for _, tc := range []struct {
		name string
		a    agent.Adapter
		want bool
	}{
		{"claude", claude.New(bin), true},
		{"codex", codex.New(bin), true},
		{"cursor", cursor.New(bin), true},
		{agenttest.NonResumingName, agenttest.StubNonResuming{}, false},
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

// The two clocks (task 067 decision 1). A chat turn gets §7.2's and §7.4's
// numbers verbatim: `agent_timeout` bounds a running turn, `input_timeout`
// bounds `awaiting_input`. Both expiries kill the process, fail the turn with
// the shared snake_case reason and return the chat to idle — which is what
// releases the `max_parallel_chats` slot §11 previously let a walked-away
// human hold forever.

// TestTurnPastAgentTimeoutFails covers §7.2's clock on a turn that will not
// stop on its own.
func TestTurnPastAgentTimeoutFails(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.AgentTimeout = config.Duration(150 * time.Millisecond)
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	c := h.chat(t)

	turn := h.sendAndWait(t, c.ID, "run forever")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonTimeout {
		t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonTimeout)
	}
	if got := h.waitIdle(t, c.ID); got.State != chatstate.Idle {
		t.Fatalf("chat = %s after a timeout, want idle", got.State)
	}
	assertSlotFree(t, h)
}

// TestChatPastInputTimeoutFails is the hole §11 named in its own words: a
// chat parked on a §7.4 request that nobody answers.
func TestChatPastInputTimeoutFails(t *testing.T) {
	h := newHarness(t)
	// The work clock stays generous: what this test bounds is the *wait*,
	// and a short agent_timeout would decide the outcome first.
	h.cfg.Defaults.AgentTimeout = config.Duration(30 * time.Second)
	h.cfg.Defaults.InputTimeout = config.Duration(150 * time.Millisecond)
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	c := h.chat(t)

	turn := h.sendAndWait(t, c.ID, "ask me something")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonInputTimeout {
		t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonInputTimeout)
	}
	if got := h.waitIdle(t, c.ID); got.State != chatstate.Idle {
		t.Fatalf("chat = %s after an input timeout, want idle", got.State)
	}
	assertSlotFree(t, h)
}

// TestAnsweringStopsTheInputClock proves the pair is a pair and not a sum: a
// human who answers inside the window resumes the work clock rather than
// carrying the input clock's remainder into the rest of the turn.
func TestAnsweringStopsTheInputClock(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.AgentTimeout = config.Duration(30 * time.Second)
	h.cfg.Defaults.InputTimeout = config.Duration(5 * time.Second)
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	c := h.chat(t)

	turn, err := h.runner.Send(t.Context(), c.ID, "ask me something")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.waitState(t, c.ID, chatstate.AwaitingInput)
	if err := h.runner.Answer(t.Context(), c.ID, agent.InputResponse{
		Answers: map[string][]string{"Which colour?": {"blue"}},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	got := h.waitTurn(t, turn.ID)
	if got.FailReason == ReasonInputTimeout {
		t.Fatal("an answered request still expired on the input clock")
	}
}

// TestTurnTranscriptStopsAtTheCap covers the other thing only the task engine
// used to do: `transcript_max_bytes` applies to a chat turn's transcript too.
func TestTurnTranscriptStopsAtTheCap(t *testing.T) {
	h := newHarness(t)
	h.cfg.TranscriptMaxBytes = 256
	t.Setenv("FAKEAGENT_SCENARIO", "flood")
	c := h.chat(t)

	turn := h.sendAndWait(t, c.ID, "say a lot")
	if turn.FailReason != ReasonTranscriptLimit {
		t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonTranscriptLimit)
	}
	path := filepath.Join(h.dataDir, "transcripts",
		worktree.ChatOwner(c.ID).Dir(), fmt.Sprintf("%d.jsonl", turn.Seq))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	// The cap bounds what is written, not to the byte: the writer stops at
	// the first line that would cross it and records that it did.
	if info.Size() > 64<<10 {
		t.Fatalf("transcript is %d bytes with a 256-byte cap", info.Size())
	}
}

// waitState waits for the chat to reach one state, for the tests that act
// while a turn is live.
func (h *harness) waitState(t *testing.T, chatID int64, want chatstate.State) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.store.GetChat(t.Context(), chatID)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if got.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat %d never reached %s", chatID, want)
}

// assertSlotFree proves the §11 slot came back: the store no longer counts a
// chat holding a process, which is what a send that was 409 before now
// depends on.
func assertSlotFree(t *testing.T, h *harness) {
	t.Helper()
	n, err := h.store.CountChatsHoldingProcess(t.Context())
	if err != nil {
		t.Fatalf("CountChatsHoldingProcess: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d chats still hold a process after the clock fired", n)
	}
}
