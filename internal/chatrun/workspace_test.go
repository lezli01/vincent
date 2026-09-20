package chatrun

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Task 124.9's placement half: Workspace answers "where would this chat's next
// turn run, and is that inside a container?" without starting anything, and
// every turn ending past placement invalidates the skill cache for the
// directory it ran in — before the ending is recorded, because recording it is
// what tells a client to refetch (§9.6, §13.2).

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
// whatever InContainer would say. The chat here was never stored, so a read
// would fail.
func TestWorkspaceOfAFreeChatIsItsWorktree(t *testing.T) {
	h := newHarness(t)
	h.runner.deps.InContainer = func(context.Context, int64) (bool, error) {
		t.Error("InContainer asked about a free chat")
		return true, nil
	}
	chat := &store.Chat{ID: 999, WorktreePath: t.TempDir()}

	dir, in, err := h.runner.Workspace(t.Context(), chat)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != chat.WorktreePath || in {
		t.Errorf("Workspace = %q, %v; want %q, false", dir, in, chat.WorktreePath)
	}
}

// TestWorkspaceReadsTheLinkedTaskFresh is task 119 decision 1 at the new
// entry point: the task owns the worktree claim, so the answer is the task's
// row as it is now, not whatever the chat row held when it was read.
func TestWorkspaceReadsTheLinkedTaskFresh(t *testing.T) {
	h := newHarness(t)
	task, c := h.linkedTask(t, h.repo)

	dir, in, err := h.runner.Workspace(t.Context(), c)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if dir != h.repo || in {
		t.Errorf("Workspace = %q, %v; want %q, false", dir, in, h.repo)
	}

	moved := t.TempDir()
	task.WorktreePath = moved
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if dir, _, err := h.runner.Workspace(t.Context(), c); err != nil || dir != moved {
		t.Errorf("Workspace after the task moved = %q (%v), want %q", dir, err, moved)
	}

	task.WorktreePath = ""
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if _, _, err := h.runner.Workspace(t.Context(), c); !errors.Is(err, ErrLinkedTaskNoWorktree) {
		t.Errorf("Workspace on a task with no worktree = %v, want ErrLinkedTaskNoWorktree", err)
	}
}

// TestWorkspaceMissingLinkedTaskIsAnError keeps a store error an error: a task
// that cannot be read has no directory to offer, host or otherwise.
func TestWorkspaceMissingLinkedTaskIsAnError(t *testing.T) {
	h := newHarness(t)
	gone := int64(999)
	chat := &store.Chat{ID: 1, LinkedTaskID: &gone}

	if _, _, err := h.runner.Workspace(t.Context(), chat); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Workspace on a missing task = %v, want ErrNotFound", err)
	}
}

// TestWorkspaceReportsTheContainerBit is the bit the skills route answers
// `unknown` on (task 124 decision 56). Nil means the host; a wired InContainer
// is asked about the linked task and its answer, or its error, is returned.
func TestWorkspaceReportsTheContainerBit(t *testing.T) {
	h := newHarness(t)
	task, c := h.linkedTask(t, h.repo)

	if _, in, err := h.runner.Workspace(t.Context(), c); err != nil || in {
		t.Errorf("Workspace with no InContainer = %v (%v), want false", in, err)
	}

	var asked []int64
	h.runner.deps.InContainer = func(_ context.Context, taskID int64) (bool, error) {
		asked = append(asked, taskID)
		return true, nil
	}
	dir, in, err := h.runner.Workspace(t.Context(), c)
	if err != nil || dir != h.repo || !in {
		t.Errorf("Workspace = %q, %v (%v); want %q, true", dir, in, err, h.repo)
	}
	if len(asked) != 1 || asked[0] != task.ID {
		t.Errorf("InContainer asked about %v, want [%d]", asked, task.ID)
	}

	errSettings := errors.New("unparseable snapshot")
	h.runner.deps.InContainer = func(context.Context, int64) (bool, error) { return false, errSettings }
	if _, _, err := h.runner.Workspace(t.Context(), c); !errors.Is(err, errSettings) {
		t.Errorf("Workspace with a failing InContainer = %v, want %v", err, errSettings)
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
	h.runner.deps.Launchers = func(context.Context, int64, int64) (agent.Launcher, error) {
		return failingLauncher{}, nil
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
	h.runner.deps.Launchers = func(context.Context, int64, int64) (agent.Launcher, error) {
		return nil, errGone
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
