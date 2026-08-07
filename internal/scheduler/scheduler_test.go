package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// stubAdmitter stands in for the engine. Admission is what these tests are
// about, so nothing here executes: a task admitted by the scheduler simply
// stays running, exactly as it would while its first step ran.
type stubAdmitter struct {
	mu       sync.Mutex
	admitted []int64
	err      error
}

func (s *stubAdmitter) Admit(_ context.Context, task *store.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.admitted = append(s.admitted, task.ID)
	return nil
}

func (s *stubAdmitter) ids() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64{}, s.admitted...)
}

type harness struct {
	store    *store.Store
	admitter *stubAdmitter
	sched    *Scheduler
}

func newHarness(t *testing.T, maxParallel int) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	adm := &stubAdmitter{}
	h := &harness{store: st, admitter: adm}
	h.sched = New(Deps{
		Store: st,
		Config: func() config.Config {
			c := config.Default()
			c.MaxParallelTasks = maxParallel
			return c
		},
		Admitter: adm,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

func (h *harness) project(t *testing.T, name string, limit *int) int64 {
	t.Helper()
	p := &store.Project{Name: name, Path: t.TempDir(), DefaultBranch: "main", MaxParallelTasks: limit}
	if err := h.store.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p.ID
}

// task inserts one task. age backdates created_at, so FIFO order within a
// priority is assertable without sleeping.
func (h *harness) task(
	t *testing.T, projectID int64, title string, state store.TaskState, priority int, age time.Duration,
) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: projectID, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + title,
		Priority: priority, State: state,
		CreatedAt: time.Now().Add(-age),
	}
	if err := h.store.CreateTask(t.Context(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *harness) state(t *testing.T, id int64) store.TaskState {
	t.Helper()
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task.State
}

func intPtr(n int) *int { return &n }

// TestAdmissionOrder asserts §11's ordering: priority DESC, then FIFO.
func TestAdmissionOrder(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	newest := h.task(t, p, "newest", store.TaskQueued, 0, time.Minute)
	oldest := h.task(t, p, "oldest", store.TaskQueued, 0, 3*time.Minute)
	urgent := h.task(t, p, "urgent", store.TaskQueued, 5, 0)

	h.sched.admit(t.Context())

	want := []int64{urgent.ID, oldest.ID, newest.ID}
	got := h.admitter.ids()
	if len(got) != len(want) {
		t.Fatalf("admitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("admitted %v, want %v (priority desc, then created_at asc)", got, want)
		}
	}
}

// TestGlobalCap asserts the global cap bounds admission (§11).
func TestGlobalCap(t *testing.T) {
	h := newHarness(t, 2)
	p := h.project(t, "proj", nil)
	for i := range 5 {
		h.task(t, p, "t"+string(rune('a'+i)), store.TaskQueued, 0, time.Duration(5-i)*time.Minute)
	}

	h.sched.admit(t.Context())

	if got := len(h.admitter.ids()); got != 2 {
		t.Fatalf("admitted %d tasks, want 2 (global max_parallel_tasks)", got)
	}
}

// TestGlobalCapCountsExistingSlotHolders asserts the cap counts what is
// already running, not just this walk's admissions.
func TestGlobalCapCountsExistingSlotHolders(t *testing.T) {
	h := newHarness(t, 2)
	p := h.project(t, "proj", nil)
	h.task(t, p, "already", store.TaskRunning, 0, time.Minute)
	h.task(t, p, "queued", store.TaskQueued, 0, 0)

	h.sched.admit(t.Context())

	if got := len(h.admitter.ids()); got != 1 {
		t.Fatalf("admitted %d tasks, want 1 (one slot already held)", got)
	}
}

// TestAwaitingInputHoldsSlot is the §6/§11 rule that tasks.md and §11 both
// used to get wrong: an agent idle on its stdin still owns its process, and
// therefore its slot.
func TestAwaitingInputHoldsSlot(t *testing.T) {
	h := newHarness(t, 1)
	p := h.project(t, "proj", nil)
	h.task(t, p, "asking", store.TaskAwaitingInput, 0, time.Minute)
	h.task(t, p, "queued", store.TaskQueued, 0, 0)

	h.sched.admit(t.Context())

	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v; awaiting_input must hold a slot", got)
	}
}

// TestParkedStatesDoNotHoldSlots asserts the converse: a gate can wait for
// hours without starving the queue (§11).
func TestParkedStatesDoNotHoldSlots(t *testing.T) {
	h := newHarness(t, 1)
	p := h.project(t, "proj", nil)
	h.task(t, p, "gate", store.TaskAwaitingGate, 0, 3*time.Minute)
	h.task(t, p, "blocked", store.TaskBlocked, 0, 2*time.Minute)
	h.task(t, p, "paused", store.TaskPaused, 0, time.Minute)
	queued := h.task(t, p, "queued", store.TaskQueued, 0, 0)

	h.sched.admit(t.Context())

	if got := h.admitter.ids(); len(got) != 1 || got[0] != queued.ID {
		t.Fatalf("admitted %v, want [%d]", got, queued.ID)
	}
}

// TestPerProjectCapSkipsAndContinues asserts a project at its cap is skipped
// while the walk continues — one busy project must not starve the queue.
func TestPerProjectCapSkipsAndContinues(t *testing.T) {
	h := newHarness(t, 10)
	capped := h.project(t, "capped", intPtr(1))
	other := h.project(t, "other", nil)

	// The capped project's tasks sort first, so a walk that stopped at a
	// capped project instead of skipping would admit nothing from `other`.
	first := h.task(t, capped, "capped-1", store.TaskQueued, 0, 4*time.Minute)
	h.task(t, capped, "capped-2", store.TaskQueued, 0, 3*time.Minute)
	h.task(t, capped, "capped-3", store.TaskQueued, 0, 2*time.Minute)
	free := h.task(t, other, "other-1", store.TaskQueued, 0, time.Minute)

	h.sched.admit(t.Context())

	got := h.admitter.ids()
	want := []int64{first.ID, free.ID}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("admitted %v, want %v (capped project yields one, walk continues)", got, want)
	}
}

// TestPerProjectCapCountsExistingSlotHolders asserts the per-project cap
// counts the project's running tasks, not only this walk's.
func TestPerProjectCapCountsExistingSlotHolders(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "capped", intPtr(2))
	h.task(t, p, "running", store.TaskRunning, 0, 2*time.Minute)
	h.task(t, p, "q1", store.TaskQueued, 0, time.Minute)
	h.task(t, p, "q2", store.TaskQueued, 0, 0)

	h.sched.admit(t.Context())

	if got := len(h.admitter.ids()); got != 1 {
		t.Fatalf("admitted %d tasks, want 1 (cap 2, one already running)", got)
	}
}

// TestPausedRequestParksInsteadOfAdmitting covers the crash path of §6: a
// pause requested while the task ran survives the re-queue, so admission
// must honour it rather than start a step the human already stopped.
func TestPausedRequestParksInsteadOfAdmitting(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	task := h.task(t, p, "pausing", store.TaskRunning, 0, time.Minute)
	if _, err := h.store.RequestPause(t.Context(), task.ID); err != nil {
		t.Fatalf("RequestPause: %v", err)
	}
	// A crash re-queues the task without clearing the request (§12.4).
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
		store.TaskRunning, store.TaskQueued, store.TaskChange{}); err != nil {
		t.Fatalf("re-queue: %v", err)
	}

	h.sched.admit(t.Context())

	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v; a pending pause must park instead", got)
	}
	if got := h.state(t, task.ID); got != store.TaskPaused {
		t.Fatalf("state = %s, want paused", got)
	}
	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.PauseRequested {
		t.Error("pause_requested still set after parking; it would park again on resume")
	}
}

// TestAdmitFailureRequeues asserts a task never stays running with nobody
// executing it — the invisible-until-next-startup failure mode.
func TestAdmitFailureRequeues(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	task := h.task(t, p, "orphan", store.TaskQueued, 0, time.Minute)
	h.admitter.err = errors.New("runner is stopping")

	h.sched.admit(t.Context())

	if got := h.state(t, task.ID); got != store.TaskQueued {
		t.Fatalf("state = %s, want queued (the runner refused it)", got)
	}
}

// TestWakeOnFiltersEvents asserts the scheduler only re-evaluates on events
// that can change what may be admitted.
func TestWakeOnFiltersEvents(t *testing.T) {
	cases := []struct {
		evType string
		want   bool
	}{
		{store.EventTaskStateChanged, true},
		{store.EventTaskPriorityChanged, true},
		{"step.started", false},
		{"gate.waiting", false},
	}
	for _, c := range cases {
		if got := WakeOn(&store.Event{Type: c.evType}); got != c.want {
			t.Errorf("WakeOn(%s) = %v, want %v", c.evType, got, c.want)
		}
	}
	if WakeOn(nil) {
		t.Error("WakeOn(nil) = true, want false")
	}
}
