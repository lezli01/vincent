package chatrun

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// wt writes rel under dir, creating parents.
func wt(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rd(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.New().Run(t.Context(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// agentCommits simulates turn seq's agent committing: the commit is made and
// the turn row's head_after moved to it, as recordHeadAfter would have
// recorded had the agent made it while running.
func (h *harness) agentCommits(t *testing.T, chat *store.Chat, seq int, msg string) {
	t.Helper()
	gitIn(t, chat.WorktreePath, "add", "-A")
	gitIn(t, chat.WorktreePath, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", msg)
	turns, err := h.store.ListChatTurns(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := findTurn(turns, seq)
	turn.HeadAfter = gitIn(t, chat.WorktreePath, "rev-parse", "HEAD")
	if err := h.store.UpdateChatTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) turnDone(t *testing.T, chatID int64, prompt string) *store.ChatTurn {
	t.Helper()
	turn := h.sendAndWait(t, chatID, prompt)
	h.waitIdle(t, chatID)
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn %d = %s (%s: %s)", turn.Seq, turn.State, turn.FailReason, turn.ErrorMessage)
	}
	return turn
}

// refs is the harness's installation-scoped ref names.
func (h *harness) refs(t *testing.T) ChatRefs {
	t.Helper()
	refs, err := h.runner.Refs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func wantRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	var rr *RewindRefusal
	if !errors.As(err, &rr) || rr.Reason != reason {
		t.Fatalf("err = %v, want refusal %s", err, reason)
	}
}

// TestCodeRewindRestoresExactly: a code rewind to turn 2 puts back the
// modified, deleted and untracked files turn 2 started with, removes what
// came later, leaves ignored files alone, and saves the replaced state —
// tip and dirty files — as rewound-1, which a forward rewind restores.
func TestCodeRewindRestoresExactly(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	dir := c.WorktreePath
	wt(t, dir, ".gitignore", "*.log\n")
	wt(t, dir, "gone.txt", "doomed\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "-m", "base")
	h.turnDone(t, c.ID, "one")
	h.agentCommits(t, c, 1, "turn one")

	// Turn 2 starts on a dirty tree: modified, deleted, untracked.
	wt(t, dir, "README.md", "modified\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	wt(t, dir, "new/nested.txt", "untracked\n")
	two := h.turnDone(t, c.ID, "two")
	h.agentCommits(t, c, 2, "turn two")
	h.turnDone(t, c.ID, "three")
	wt(t, dir, "README.md", "later\n")
	wt(t, dir, "stray.txt", "stray\n")
	wt(t, dir, "keep.log", "ignored\n")
	tip := gitIn(t, dir, "rev-parse", "HEAD")

	res, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindCode})
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if res.Ref != h.refs(t).Rewound(c.ID, 1) {
		t.Errorf("ref = %q", res.Ref)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != two.HeadBefore {
		t.Errorf("HEAD = %s, want turn 2's head_before %s", head, two.HeadBefore)
	}
	for rel, want := range map[string]string{
		"README.md": "modified\n", "new/nested.txt": "untracked\n", "keep.log": "ignored\n",
	} {
		if got, ok := rd(t, dir, rel); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", rel, got, ok, want)
		}
	}
	for _, rel := range []string{"gone.txt", "stray.txt"} {
		if _, ok := rd(t, dir, rel); ok {
			t.Errorf("%s survived the rewind", rel)
		}
	}
	// rewound-1 holds the replaced tip and its dirty state.
	if parent := gitIn(t, h.repo, "rev-parse", res.Ref+"^1"); parent != tip {
		t.Errorf("rewound-1's parent = %s, want the old tip %s", parent, tip)
	}
	if body := gitIn(t, h.repo, "show", res.Ref+":stray.txt"); body != "stray" {
		t.Errorf("rewound-1 stray.txt = %q", body)
	}
	// Conversation untouched by a code-only rewind.
	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	for _, tr := range turns {
		if tr.RewoundAt != nil {
			t.Errorf("turn %d marked rewound by a code-only rewind", tr.Seq)
		}
	}

	// Forward: back to what the rewind replaced.
	states, err := h.runner.RewoundStates(t.Context(), c)
	if err != nil || len(states) != 1 || states[0].Blocked != "" {
		t.Fatalf("RewoundStates = %+v, %v", states, err)
	}
	fwd, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Rewound: 1})
	if err != nil {
		t.Fatalf("forward Rewind: %v", err)
	}
	if fwd.Ref != h.refs(t).Rewound(c.ID, 2) {
		t.Errorf("forward ref = %q", fwd.Ref)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != tip {
		t.Errorf("HEAD after forward = %s, want %s", head, tip)
	}
	if got, _ := rd(t, dir, "README.md"); got != "later\n" {
		t.Errorf("README after forward = %q", got)
	}
	if _, ok := rd(t, dir, "stray.txt"); !ok {
		t.Error("stray.txt not restored going forward")
	}

	// The chain is broken only by vincent's own rewinds: still allowed.
	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1, Mode: RewindCode}); err != nil {
		t.Fatalf("rewind after own rewinds: %v", err)
	}
}

// TestCodeRewindRefusesForeignCommits is decision 3: a commit no turn made
// refuses a code rewind, while conversation rewind and fork stay available.
func TestCodeRewindRefusesForeignCommits(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	h.turnDone(t, c.ID, "one")
	h.turnDone(t, c.ID, "two")
	// A commit between the turns' records: nobody's turn.
	gitIn(t, c.WorktreePath, "-c", "user.name=x", "-c", "user.email=x@y", "commit", "-q", "--allow-empty", "-m", "foreign")

	_, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1, Mode: RewindCode})
	wantRefusal(t, err, ReasonRewindForeignCommits)
	_, err = h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1})
	wantRefusal(t, err, ReasonRewindForeignCommits)
	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	opts := h.runner.RewindOptions(t.Context(), c, turns)
	if o := opts[1]; o.Code != ReasonRewindForeignCommits || o.Conversation != "" || o.Fork != "" {
		t.Errorf("options[1] = %+v", o)
	}
	if _, err := h.runner.PlanFork(t.Context(), c.ID, 1); err != nil {
		t.Errorf("PlanFork: %v", err)
	}
}

// TestCodeRewindAfterConversationRewind is review F1: a conversation-only
// rewind leaves the commits of the turns it took out on the branch, and only
// those turns explain them. A later code rewind to a turn before them must
// still find the chain whole, not read the chat's own commits as foreign.
func TestCodeRewindAfterConversationRewind(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	h.turnDone(t, c.ID, "one")
	two := h.turnDone(t, c.ID, "two")
	h.agentCommits(t, c, 2, "turn two")
	h.turnDone(t, c.ID, "three")
	h.agentCommits(t, c, 3, "turn three")
	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 3, Mode: RewindConversation}); err != nil {
		t.Fatalf("conversation Rewind: %v", err)
	}
	h.turnDone(t, c.ID, "four")

	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	opts := h.runner.RewindOptions(t.Context(), c, turns)
	for seq := 1; seq <= 2; seq++ {
		if o := opts[seq]; o.Code != "" {
			t.Errorf("turn %d code option blocked: %q", seq, o.Code)
		}
	}
	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindBoth}); err != nil {
		t.Fatalf("code rewind to turn 2 after a conversation rewind: %v", err)
	}
	if head := gitIn(t, c.WorktreePath, "rev-parse", "HEAD"); head != two.HeadBefore {
		t.Errorf("HEAD = %s, want turn 2's head_before %s", head, two.HeadBefore)
	}
}

// TestCodeRewindRefusesUnsavedTrackedFile is review F2: an edit to a tracked
// file vincent cannot read is not in the pre-rewind save (which holds HEAD's
// blob for it), and the restore's reset would destroy it. The rewind is
// refused before anything moves or any rewound ref is written.
func TestCodeRewindRefusesUnsavedTrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod cannot make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	h := newHarness(t)
	c := h.chat(t)
	dir := c.WorktreePath
	h.turnDone(t, c.ID, "one")
	h.turnDone(t, c.ID, "two")
	wt(t, dir, "README.md", "SECRET-EDIT\n")
	p := filepath.Join(dir, "README.md")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	_, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1, Mode: RewindCode})
	wantRefusal(t, err, ReasonRewindUnsavedFiles)
	if !strings.Contains(err.Error(), "README.md") {
		t.Errorf("refusal %q does not name the file", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := rd(t, dir, "README.md"); got != "SECRET-EDIT\n" {
		t.Errorf("README.md = %q, want the unsaved edit kept", got)
	}
	if out := gitIn(t, h.repo, "for-each-ref", "--format=%(refname)", h.refs(t).Rewound(c.ID, 1)); out != "" {
		t.Errorf("a refused rewind wrote %s", out)
	}
	// Readable again, the save takes it and the rewind goes ahead.
	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1, Mode: RewindCode}); err != nil {
		t.Fatalf("Rewind once readable: %v", err)
	}
}

// TestFailedStartTurnKeepsTheChainWhole is review F3: a turn that failed
// after its checkpoint but before any process ran (here, the adapter's Start
// failing) still records head_after, so it does not read as a foreign-commit
// break and code rewinds at and before it stay allowed.
func TestFailedStartTurnKeepsTheChainWhole(t *testing.T) {
	h := newHarness(t)
	c := h.chatOn(t, perRunAgentName)
	h.perRun.setResults(agent.RunResult{SessionID: "s1", ResultText: "ok"})
	h.turnDone(t, c.ID, "one")
	// No scripted run left: Start fails, after turn 2's checkpoint.
	two := h.sendAndWait(t, c.ID, "two")
	h.waitIdle(t, c.ID)
	if two.State != chatstate.TurnFailed || two.CheckpointSHA == "" {
		t.Fatalf("turn 2 = %s, checkpoint %q; want a failed turn with a checkpoint", two.State, two.CheckpointSHA)
	}
	if two.HeadAfter != two.HeadBefore {
		t.Errorf("turn 2 head_after = %q, want its head_before %q", two.HeadAfter, two.HeadBefore)
	}
	h.perRun.setResults(agent.RunResult{SessionID: "s1", ResultText: "ok"})
	h.turnDone(t, c.ID, "three")

	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	opts := h.runner.RewindOptions(t.Context(), c, turns)
	for seq := 1; seq <= 3; seq++ {
		if o := opts[seq]; o.Code != "" {
			t.Errorf("turn %d code option blocked: %q", seq, o.Code)
		}
	}
	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1, Mode: RewindCode}); err != nil {
		t.Fatalf("code rewind across a failed-start turn: %v", err)
	}
}

// TestRecoverRecordsHeadAfter is review F3's recovery half: a turn a dead
// daemon left running is finalized interrupted with head_after read from the
// worktree, where the turn's process left it.
func TestRecoverRecordsHeadAfter(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	one := h.turnDone(t, c.ID, "one")
	turn, err := h.store.CreateChatTurn(t.Context(), c.ID, "cut off")
	if err != nil {
		t.Fatal(err)
	}
	turn.CheckpointSHA, turn.HeadBefore = one.CheckpointSHA, one.HeadAfter
	if err := h.store.UpdateChatTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	// What the cut-off turn's agent committed before the daemon died.
	gitIn(t, c.WorktreePath, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "--allow-empty", "-m", "turn two")
	if err := h.runner.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, err := h.store.GetChatTurn(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := gitIn(t, c.WorktreePath, "rev-parse", "HEAD"); got.State != chatstate.TurnInterruptedState || got.HeadAfter != want {
		t.Errorf("recovered turn = %s, head_after %q; want interrupted at %s", got.State, got.HeadAfter, want)
	}
}

// TestRewindGuards: a live chat, an ended chat, a turn without a checkpoint
// and an already-rewound turn are each refused.
func TestRewindGuards(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	one := h.turnDone(t, c.ID, "one")
	h.turnDone(t, c.ID, "two")

	for _, st := range []chatstate.State{chatstate.Running, chatstate.AwaitingInput} {
		if _, err := h.store.SetChatState(t.Context(), c.ID, st); err != nil {
			t.Fatal(err)
		}
		if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1}); !errors.Is(err, ErrTurnLive) {
			t.Errorf("%s: err = %v, want ErrTurnLive", st, err)
		}
		if _, err := h.runner.PlanFork(t.Context(), c.ID, 1); !errors.Is(err, ErrTurnLive) {
			t.Errorf("%s fork: err = %v, want ErrTurnLive", st, err)
		}
	}
	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Idle); err != nil {
		t.Fatal(err)
	}
	// A draining queue: messages waiting and not paused.
	if err := liveGuard(&store.Chat{State: chatstate.Idle, QueuedCount: 1}); !errors.Is(err, ErrTurnLive) {
		t.Errorf("draining queue: %v", err)
	}
	wantRefusal(t, liveGuard(&store.Chat{State: chatstate.Archived}), ReasonChatTerminal)

	// No checkpoint: a pre-0066 row.
	one.CheckpointSHA, one.HeadBefore = "", ""
	if err := h.store.UpdateChatTurn(t.Context(), one); err != nil {
		t.Fatal(err)
	}
	_, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1})
	wantRefusal(t, err, ReasonCheckpointUnavailable)
	_, err = h.runner.PlanFork(t.Context(), c.ID, 1)
	wantRefusal(t, err, ReasonCheckpointUnavailable)

	if _, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindConversation}); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	_, err = h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindConversation})
	wantRefusal(t, err, ReasonTurnRewound)
}

// TestConversationRewindResumesAtTheAnchor: claude with the capability
// resumes turn 1's session at turn 1's anchor, forking it — the next turn
// remembers turn 1 and not turn 2, runs under a new session id, and the
// rewound prompt is back in the draft.
func TestConversationRewindResumesAtTheAnchor(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	one := h.turnDone(t, c.ID, "alpha")
	if one.ResumeAnchor == "" {
		t.Fatal("turn 1 recorded no resume anchor")
	}
	h.turnDone(t, c.ID, "beta")
	if _, err := h.store.SetChatDraft(t.Context(), c.ID, "typed"); err != nil {
		t.Fatal(err)
	}
	res, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindConversation})
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if res.Seeded || res.RewindID == "" || !slices.Equal(res.RewoundSeqs, []int{2}) {
		t.Errorf("result = %+v", res)
	}
	if res.Chat.Draft != "beta\n\ntyped" || res.Chat.ResumeAt != one.ResumeAnchor || res.Chat.SessionID != one.SessionID {
		t.Errorf("chat after rewind: draft %q resume_at %q session %q", res.Chat.Draft, res.Chat.ResumeAt, res.Chat.SessionID)
	}
	turns, _ := h.store.ListChatTurns(t.Context(), c.ID)
	if turns[1].RewoundAt == nil || turns[1].RewindID != res.RewindID || turns[0].RewoundAt != nil {
		t.Errorf("rewound marks wrong: %+v / %+v", turns[0].RewoundAt, turns[1].RewoundAt)
	}

	three := h.turnDone(t, c.ID, "gamma")
	if three.Seq != 3 || three.Seeded {
		t.Errorf("turn 3 seq %d seeded %v", three.Seq, three.Seeded)
	}
	body := h.transcript(t, c.ID, 3)
	if !strings.Contains(body, "alpha") || strings.Contains(body, "beta") {
		t.Errorf("turn 3 should recall alpha only:\n%s", body)
	}
	after := h.waitIdle(t, c.ID)
	if three.SessionID == "" || three.SessionID == one.SessionID || after.SessionID != three.SessionID {
		t.Errorf("sessions: turn1 %q turn3 %q chat %q", one.SessionID, three.SessionID, after.SessionID)
	}
	if after.ResumeAt != "" || after.PendingPreamble != "" {
		t.Errorf("plan not cleared: %q %q", after.ResumeAt, after.PendingPreamble)
	}
}

// TestConversationRewindSeeds: codex, and a claude without the capability,
// start a fresh session seeded with the kept turns, and the turn says so.
func TestConversationRewindSeeds(t *testing.T) {
	for _, tc := range []struct{ name, agent, noResumeAt string }{
		{"codex", "codex", ""}, {"claude-without-resume-at", "claude", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_CLAUDE_NO_RESUME_AT", tc.noResumeAt)
			h := newHarness(t)
			c := h.chatOn(t, tc.agent)
			h.turnDone(t, c.ID, "alpha")
			h.turnDone(t, c.ID, "beta")
			res, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 2, Mode: RewindConversation})
			if err != nil {
				t.Fatalf("Rewind: %v", err)
			}
			if !res.Seeded || res.Chat.SessionID != "" || !strings.Contains(res.Chat.PendingPreamble, "alpha") ||
				strings.Contains(res.Chat.PendingPreamble, "beta") {
				t.Fatalf("plan: seeded %v session %q preamble %q", res.Seeded, res.Chat.SessionID, res.Chat.PendingPreamble)
			}
			three := h.turnDone(t, c.ID, "gamma")
			if !three.Seeded {
				t.Error("turn 3 not marked seeded")
			}
			if body := h.transcript(t, c.ID, 3); !strings.Contains(body, "vincent.seeded") {
				t.Errorf("no seeded note in transcript:\n%s", body)
			}
			if after := h.waitIdle(t, c.ID); after.PendingPreamble != "" || after.SessionID != three.SessionID {
				t.Errorf("plan not consumed: %q %q", after.PendingPreamble, after.SessionID)
			}
		})
	}
}

// TestRewindToTurnOneStartsFresh: nothing is kept, so nothing is seeded.
func TestRewindToTurnOneStartsFresh(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	h.turnDone(t, c.ID, "alpha")
	res, err := h.runner.Rewind(t.Context(), c.ID, RewindRequest{Turn: 1})
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if res.Seeded || res.Chat.SessionID != "" || res.Chat.ResumeAt != "" || res.Chat.PendingPreamble != "" {
		t.Errorf("plan = %+v", res.Chat)
	}
	if res.Ref == "" || res.Chat.Draft != "alpha" {
		t.Errorf("ref %q draft %q", res.Ref, res.Chat.Draft)
	}
}

// TestForkPlanAndCheckpoint: a fork plan copies the source's settings and
// resumes at the previous turn's anchor; ApplyForkCheckpoint lays the
// checkpoint's dirty state onto a fresh worktree, unstaged.
func TestForkPlanAndCheckpoint(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	one := h.turnDone(t, c.ID, "alpha")
	wt(t, c.WorktreePath, "README.md", "dirty\n")
	wt(t, c.WorktreePath, "extra.txt", "new\n")
	two := h.turnDone(t, c.ID, "beta")

	plan, err := h.runner.PlanFork(t.Context(), c.ID, 2)
	if err != nil {
		t.Fatalf("PlanFork: %v", err)
	}
	if plan.Agent != c.Agent || plan.PermissionMode != c.PermissionMode || plan.HeadBefore != two.HeadBefore ||
		plan.CheckpointSHA != two.CheckpointSHA || plan.SessionID != one.SessionID || plan.ResumeAt != one.ResumeAnchor ||
		plan.Seeded {
		t.Errorf("plan = %+v", plan)
	}
	if p1, err := h.runner.PlanFork(t.Context(), c.ID, 1); err != nil || p1.SessionID != "" || p1.Seeded {
		t.Errorf("seq 1 plan = %+v, %v", p1, err)
	}

	fresh := filepath.Join(t.TempDir(), "fork")
	gitIn(t, h.repo, "worktree", "add", "-q", "-b", "fork", fresh, plan.HeadBefore)
	if _, err := h.runner.ApplyForkCheckpoint(t.Context(), fresh, plan); err != nil {
		t.Fatalf("ApplyForkCheckpoint: %v", err)
	}
	if got, _ := rd(t, fresh, "README.md"); got != "dirty\n" {
		t.Errorf("README = %q", got)
	}
	if got, _ := rd(t, fresh, "extra.txt"); got != "new\n" {
		t.Errorf("extra.txt = %q", got)
	}
	if staged := gitIn(t, fresh, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged: %q", staged)
	}
	if head := gitIn(t, fresh, "rev-parse", "HEAD"); head != plan.HeadBefore {
		t.Errorf("HEAD moved: %s", head)
	}
}

// TestPruneAndSweepCheckpointRefs: pruning removes a chat's refs; the sweep
// removes an orphan's and keeps an archived chat's.
func TestPruneAndSweepCheckpointRefs(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	h.turnDone(t, c.ID, "one")
	sha := gitIn(t, h.repo, "rev-parse", h.refs(t).Turn(c.ID, 1))
	orphan := int64(987654)
	gitIn(t, h.repo, "update-ref", h.refs(t).Turn(orphan, 1), sha)
	gitIn(t, h.repo, "update-ref", h.refs(t).Rewound(orphan, 1), sha)
	// Another installation sharing the repository (review F4): its chat ids
	// mean nothing in this database — neither the orphan's nor c's own.
	other := NewChatRefs("0123456789abcdef")
	gitIn(t, h.repo, "update-ref", other.Turn(orphan, 1), sha)
	gitIn(t, h.repo, "update-ref", other.Turn(c.ID, 1), sha)
	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}

	n, err := h.runner.SweepCheckpointRefs(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1", n, err)
	}
	if out := gitIn(t, h.repo, "for-each-ref", "--format=%(refname)", h.refs(t).Prefix(orphan)); out != "" {
		t.Errorf("orphan refs left: %q", out)
	}
	if out := gitIn(t, h.repo, "for-each-ref", "--format=%(refname)", h.refs(t).Prefix(c.ID)); out == "" {
		t.Error("the archived chat's refs were swept")
	}
	if out := gitIn(t, h.repo, "for-each-ref", "--format=%(refname)", other.Root()); strings.Count(out, "\n") != 1 {
		t.Errorf("another installation's refs = %q, want both kept", out)
	}
	if err := h.runner.PruneCheckpointRefs(t.Context(), h.repo, c.ID); err != nil {
		t.Fatal(err)
	}
	if out := gitIn(t, h.repo, "rev-parse", "--verify", other.Turn(c.ID, 1)); out != sha {
		t.Errorf("pruning chat %d took another installation's chat %d ref", c.ID, c.ID)
	}
	if out := gitIn(t, h.repo, "for-each-ref", "--format=%(refname)", h.refs(t).Prefix(c.ID)); out != "" {
		t.Errorf("refs left after prune: %q", out)
	}
}

func TestTailLinesOf(t *testing.T) {
	s := strings.Repeat("line\n", 300)
	got, cut := tailLinesOf(s, 200, 1<<20)
	if !cut || strings.Count(got, "\n") != 200 {
		t.Errorf("cut %v lines %d", cut, strings.Count(got, "\n"))
	}
	if got, cut := tailLinesOf("a\nb\n", 200, 1<<20); cut || got != "a\nb\n" {
		t.Errorf("%q %v", got, cut)
	}
}
