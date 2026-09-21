package chatrun

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Task 124.9's placement half, as task 124.17 leaves it: Workspace answers
// "which directory would this chat's next turn start in?" without starting
// anything, SkillPlace answers where a listing of that directory would be
// obtained, and every turn ending past placement invalidates the skill cache
// for the directory it ran in — before the ending is recorded, because
// recording it is what tells a client to refetch (§9.6, §13.2).

// linkedTask creates a blocked task working in dir and opens a chat linked to
// it. Launchers stays nil, so the chat's turns run on the host like a free
// chat's, in the task's worktree.
func (h *harness) linkedTask(t *testing.T, dir string) (*store.Task, *store.Chat) {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.project.ID, Title: "linked", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/1-linked", State: store.TaskBlocked,
		WorktreePath: dir,
	}
	if err := h.store.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &store.Chat{Title: "why", Agent: "claude", PermissionMode: string(agent.FullAuto)}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return task, c
}

// TestWorkspaceOfAFreeChatIsItsWorktree needs no store: the chat row the caller
// already holds names the directory, and a free chat runs on the host (§16)
// whatever SkillPlace would say. The chat here was never stored, so a read
// would fail.
func TestWorkspaceOfAFreeChatIsItsWorktree(t *testing.T) {
	h := newHarness(t)
	h.runner.deps.SkillPlace = func(context.Context, int64, int64) (agent.Launcher, string, []string, error) {
		t.Error("SkillPlace asked about a free chat")
		return nil, "nowhere", nil, nil
	}
	chat := &store.Chat{ID: 999, WorktreePath: t.TempDir()}

	dir, err := h.runner.Workspace(t.Context(), chat)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != chat.WorktreePath {
		t.Errorf("Workspace = %q, want %q", dir, chat.WorktreePath)
	}
	dir, l, place, env, err := h.runner.SkillPlace(t.Context(), chat)
	if err != nil || dir != chat.WorktreePath || l != nil || place != "" || env != nil {
		t.Errorf("SkillPlace of a free chat = %q, %v, %q, %v (%v); want the host",
			dir, l, place, env, err)
	}
}

// TestWorkspaceReadsTheLinkedTaskFresh is task 119 decision 1 at the new
// entry point: the task owns the worktree claim, so the answer is the task's
// row as it is now, not whatever the chat row held when it was read.
func TestWorkspaceReadsTheLinkedTaskFresh(t *testing.T) {
	h := newHarness(t)
	task, c := h.linkedTask(t, h.repo)

	dir, err := h.runner.Workspace(t.Context(), c)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != h.repo {
		t.Errorf("Workspace = %q, want %q", dir, h.repo)
	}

	moved := t.TempDir()
	task.WorktreePath = moved
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if dir, err := h.runner.Workspace(t.Context(), c); err != nil || dir != moved {
		t.Errorf("Workspace after the task moved = %q (%v), want %q", dir, err, moved)
	}

	task.WorktreePath = ""
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if _, err := h.runner.Workspace(t.Context(), c); !errors.Is(err, ErrLinkedTaskNoWorktree) {
		t.Errorf("Workspace on a task with no worktree = %v, want ErrLinkedTaskNoWorktree", err)
	}
}

// TestWorkspaceMissingLinkedTaskIsAnError keeps a store error an error: a task
// that cannot be read has no directory to offer, host or otherwise.
func TestWorkspaceMissingLinkedTaskIsAnError(t *testing.T) {
	h := newHarness(t)
	gone := int64(999)
	chat := &store.Chat{ID: 1, LinkedTaskID: &gone}

	if _, err := h.runner.Workspace(t.Context(), chat); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Workspace on a missing task = %v, want ErrNotFound", err)
	}
}

// TestSkillPlaceAsksWhereTheProbeWouldRun is task 124.17 decision 1, which
// amends task 124 decision 56: the skills route no longer reads a
// settings-only bit, it resolves a placement. Nil means the host — every free
// chat, and any daemon that wires nothing — and a wired SkillPlace is asked
// about the linked task and this chat, and its answer, or its error, is
// returned whole.
func TestSkillPlaceAsksWhereTheProbeWouldRun(t *testing.T) {
	h := newHarness(t)
	task, c := h.linkedTask(t, h.repo)

	if dir, l, place, env, err := h.runner.SkillPlace(t.Context(), c); err != nil ||
		dir != h.repo || l != nil || place != "" || env != nil {
		t.Errorf("SkillPlace with no hook = %q, %v, %q, %v (%v); want the host at %q",
			dir, l, place, env, err, h.repo)
	}

	type ask struct{ taskID, chatID int64 }
	var asked []ask
	wantEnv := []string{"HOME=/vincent-home"}
	h.runner.deps.SkillPlace = func(_ context.Context, taskID, chatID int64) (
		agent.Launcher, string, []string, error,
	) {
		asked = append(asked, ask{taskID, chatID})
		return failingLauncher{}, "cid", wantEnv, nil
	}
	dir, l, place, env, err := h.runner.SkillPlace(t.Context(), c)
	if err != nil || dir != h.repo || place != "cid" || !slices.Equal(env, wantEnv) {
		t.Errorf("SkillPlace = %q, %v, %q, %v (%v); want %q, cid, %v",
			dir, l, place, env, err, h.repo, wantEnv)
	}
	if _, ok := l.(failingLauncher); !ok {
		t.Errorf("launcher = %T, want the hook's own", l)
	}
	// Keyed by chat, never by turn: a probe has no turn (decision 4).
	if len(asked) != 1 || asked[0] != (ask{task.ID, c.ID}) {
		t.Errorf("SkillPlace asked %+v, want [{%d %d}]", asked, task.ID, c.ID)
	}

	errGone := errors.New("the task's container is not running")
	h.runner.deps.SkillPlace = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return nil, "", nil, errGone
	}
	if _, _, _, _, err := h.runner.SkillPlace(t.Context(), c); !errors.Is(err, errGone) {
		t.Errorf("SkillPlace with a gone container = %v, want %v", err, errGone)
	}
}

// recordingLauncher is a host launcher that keeps every Command it started.
// It is how a test reads the environment a turn actually ran with, which is
// otherwise invisible: RunSpec.Env reaches the CLI through the adapter.
type recordingLauncher struct {
	agent.HostLauncher
	mu   sync.Mutex
	envs [][]string
}

func (l *recordingLauncher) Launch(cmd agent.Command) (agent.Process, error) {
	l.mu.Lock()
	l.envs = append(l.envs, slices.Clone(cmd.Env))
	l.mu.Unlock()
	return l.HostLauncher.Launch(cmd)
}

func (l *recordingLauncher) started() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]string(nil), l.envs...)
}

// TestLinkedTurnCarriesItsPlacementEnvironment is task 124.17 decision 3, and
// the regression it fixes: a linked chat's turn built its RunSpec with no Env
// at all, so a turn inside a task's container ran under the *image's* HOME
// and never read the `~/.claude`, `~/.codex` and `~/.cursor` that
// `container.mount_agent_config` had mounted beneath container.HomeDir for
// it. §16 said the session store persisted under that mount; only agent steps
// were making it true.
//
// The launcher here is a host one, because what is under test is that the
// environment Launchers answers reaches the process — not docker.
func TestLinkedTurnCarriesItsPlacementEnvironment(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newHarness(t)
	_, c := h.linkedTask(t, h.repo)
	rec := &recordingLauncher{}
	want := "VINCENT_TEST_HOME=/vincent-home"
	h.runner.deps.Launchers = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return rec, "cid", append(os.Environ(), want), nil
	}

	if turn := h.sendAndWait(t, c.ID, "why did it block?"); turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)

	started := rec.started()
	if len(started) == 0 {
		t.Fatal("the turn never reached the launcher")
	}
	for i, env := range started {
		if !slices.Contains(env, want) {
			t.Errorf("command %d ran without %q", i, want)
		}
	}
}

// TestHostTurnCarriesNoEnvironment is decision 3's other side: nothing
// changes for a chat that runs on this machine. Launchers answers nil, which
// is the daemon's own environment, and that is what every chat carried before
// task 124.17.
func TestHostTurnCarriesNoEnvironment(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newHarness(t)
	_, c := h.linkedTask(t, h.repo)
	rec := &recordingLauncher{}
	h.runner.deps.Launchers = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return rec, "", nil, nil
	}

	if turn := h.sendAndWait(t, c.ID, "why did it block?"); turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)

	started := rec.started()
	if len(started) == 0 {
		t.Fatal("the turn never reached the launcher")
	}
	for i, env := range started {
		if env != nil {
			t.Errorf("command %d ran with an environment of its own: %v", i, env)
		}
	}
}

// invalidation is one InvalidateSkills call: the directory it named, and the
// state the turn's row held in the store at that moment.
type invalidation struct {
	dir   string
	state chatstate.TurnState
	err   error
}

type invalidations struct {
	mu    sync.Mutex
	calls []invalidation
}

func (i *invalidations) get() []invalidation {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]invalidation(nil), i.calls...)
}

// recordInvalidations wires InvalidateSkills to note each call along with the
// chat's latest turn as the store has it. A turn whose stored state is still
// running when the hook fires had its ending recorded after — and so after the
// chat.turn_changed a client refetches on.
func (h *harness) recordInvalidations(chatID int64) *invalidations {
	inv := &invalidations{}
	h.runner.deps.InvalidateSkills = func(dir string) {
		call := invalidation{dir: dir}
		turns, err := h.store.ListChatTurns(context.Background(), chatID)
		switch {
		case err != nil:
			call.err = err
		case len(turns) == 0:
			call.err = errors.New("the chat has no turn")
		default:
			call.state = turns[len(turns)-1].State
		}
		inv.mu.Lock()
		inv.calls = append(inv.calls, call)
		inv.mu.Unlock()
	}
	return inv
}

// assertInvalidatedOnce is the shared verdict: one call, for dir, made while
// the turn was still running in the store.
func assertInvalidatedOnce(t *testing.T, inv *invalidations, dir string) {
	t.Helper()
	calls := inv.get()
	if len(calls) != 1 {
		t.Fatalf("InvalidateSkills called %d time(s), want once: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.err != nil {
		t.Fatalf("reading the turn inside the hook: %v", got.err)
	}
	if got.dir != dir {
		t.Errorf("invalidated %q, want %q", got.dir, dir)
	}
	if got.state != chatstate.TurnRunning {
		t.Errorf("turn was %q when the cache was invalidated, want %q: the ending "+
			"was recorded first, so a refetch could re-cache the pre-turn list", got.state, chatstate.TurnRunning)
	}
}

// TestTurnEndingInvalidatesItsDirectory covers the endings a free chat's turn
// reaches through the fakeagent: done, a failed run, and a clock. Each
// invalidates its own worktree exactly once, before finish.
func TestTurnEndingInvalidatesItsDirectory(t *testing.T) {
	cases := []struct {
		name     string
		scenario string
		timeout  time.Duration // agent_timeout; 0 keeps the default
		state    chatstate.TurnState
		reason   string
	}{
		{"done", "success", 0, chatstate.TurnDone, ""},
		{"agent error", "error-event", 0, chatstate.TurnFailed, ReasonAgentError},
		{"timeout", "hang", 150 * time.Millisecond, chatstate.TurnFailed, ReasonTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", tc.scenario)
			h := newHarness(t)
			if tc.timeout > 0 {
				h.cfg.Defaults.AgentTimeout = config.Duration(tc.timeout)
			}
			c := h.chat(t)
			inv := h.recordInvalidations(c.ID)

			turn := h.sendAndWait(t, c.ID, "hello")
			if turn.State != tc.state || turn.FailReason != tc.reason {
				t.Fatalf("turn = %s/%s (%s), want %s/%s",
					turn.State, turn.FailReason, turn.ErrorMessage, tc.state, tc.reason)
			}
			h.waitIdle(t, c.ID)
			assertInvalidatedOnce(t, inv, c.WorktreePath)
		})
	}
}

// TestLinkedTurnInvalidatesTheTasksWorktree is the linked half: the directory
// is the one placement resolved, the task's, never a worktree of the chat's
// own (it has none).
func TestLinkedTurnInvalidatesTheTasksWorktree(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newHarness(t)
	_, c := h.linkedTask(t, h.repo)
	inv := h.recordInvalidations(c.ID)

	turn := h.sendAndWait(t, c.ID, "why did it block?")
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)
	assertInvalidatedOnce(t, inv, h.repo)
}

// failingLauncher refuses to start anything, which is how a turn reaches
// runTurn's adapter.Start failure branch with its directory already placed.
type failingLauncher struct{ agent.HostLauncher }

var errNoLaunch = errors.New("launch refused")

func (failingLauncher) Launch(agent.Command) (agent.Process, error) { return nil, errNoLaunch }

// TestStartFailureInvalidatesToo is the one ending that skips Wait: Start
// failed after placement, and the directory is invalidated all the same.
func TestStartFailureInvalidatesToo(t *testing.T) {
	h := newHarness(t)
	_, c := h.linkedTask(t, h.repo)
	h.runner.deps.Launchers = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return failingLauncher{}, "", nil, nil
	}
	inv := h.recordInvalidations(c.ID)

	// No waitIdle: this branch's finish is handed no chat, so the turn row is
	// the ending to wait for — and the invalidation precedes it.
	turn := h.sendAndWait(t, c.ID, "hello")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonAgentError {
		t.Fatalf("turn = %s/%s (%s), want failed/%s",
			turn.State, turn.FailReason, turn.ErrorMessage, ReasonAgentError)
	}
	assertInvalidatedOnce(t, inv, h.repo)
}

// TestPlacementFailureInvalidatesNothing is the other side of the line: a turn
// that failed before it had a directory has nothing to invalidate. A container
// that has gone away is the case that matters — it fails here, through
// Launchers, and never falls back to the host (task 119 decision 3).
func TestPlacementFailureInvalidatesNothing(t *testing.T) {
	h := newHarness(t)
	_, c := h.linkedTask(t, h.repo)
	errGone := errors.New("the task's container is not running")
	h.runner.deps.Launchers = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return nil, "", nil, errGone
	}
	inv := h.recordInvalidations(c.ID)

	turn := h.sendAndWait(t, c.ID, "hello")
	if turn.State != chatstate.TurnFailed || turn.ErrorMessage != errGone.Error() {
		t.Fatalf("turn = %s/%s (%s), want failed with %q",
			turn.State, turn.FailReason, turn.ErrorMessage, errGone)
	}
	h.waitIdle(t, c.ID)
	if calls := inv.get(); len(calls) != 0 {
		t.Errorf("a turn with no directory invalidated %+v", calls)
	}
}

// TestNilInvalidateSkillsIsTolerated is a runner built without a cache — every
// test before task 124.9, and any daemon that wires none — ending turns both
// ways without tripping over the missing hook.
func TestNilInvalidateSkillsIsTolerated(t *testing.T) {
	for _, scenario := range []string{"success", "error-event"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", scenario)
			h := newHarness(t)
			c := h.chat(t)
			if turn := h.sendAndWait(t, c.ID, "hello"); !chatstate.TurnTerminal(turn.State) {
				t.Fatalf("turn = %s, want an ending", turn.State)
			}
			h.waitIdle(t, c.ID)
		})
	}
}
