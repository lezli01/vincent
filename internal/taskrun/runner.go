// Package taskrun is the workflow execution engine: one goroutine per
// admitted task walks its workflow snapshot step by step and is the sole
// writer of that task's state (phase 2 decision). An actor lives for exactly
// one admission — reaching a gate, blocking, or pausing releases the task's
// concurrency slot and ends the goroutine; the scheduler admits it again
// when a human unblocks it.
//
// Admission here is still the M1 interim loop bounded by the global
// max_parallel_tasks; T2.5 replaces it with the real scheduler (per-project
// caps, priority ordering, re-evaluation on every state change).
package taskrun

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// DefaultAgent is used when neither the step, the task override, nor the
// workflow defaults name an agent.
const DefaultAgent = "claude"

// tickInterval is the admission safety net: Wake normally drives the loop,
// but a tick also picks up config hot-reloads of max_parallel_tasks.
const tickInterval = 5 * time.Second

// stopGrace bounds how long Stop waits for actors after cancellation.
const stopGrace = 20 * time.Second

// Deps are the daemon facilities the engine works with.
type Deps struct {
	Store     *store.Store
	Config    func() config.Config
	Worktrees *worktree.Manager
	Agents    *agent.Registry
	Shells    *Shells
	DataDir   string
	Logger    *slog.Logger
}

// Runner admits queued tasks and runs them.
type Runner struct {
	deps   Deps
	wake   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// persist is a context detached from shutdown: a task's final state must
	// still reach the database after the run context is canceled.
	persist context.Context //nolint:containedctx // deliberately outlives the run

	// live tracks running tasks so human actions and shutdown can reach the
	// process (phase 2 decision); T2.6 delivers cancel/pause/answer through it.
	mu   sync.Mutex
	live map[int64]*liveRun
}

// liveRun is one task's in-flight execution. T2.6 adds the agent handle to
// it, so cancel and answer can reach the live process.
type liveRun struct {
	// pauseRequested is set by a pause action and read at the next step
	// boundary (§6: a running task finishes its current step first).
	pauseRequested bool
}

// New returns a stopped runner.
func New(deps Deps) *Runner {
	if deps.Shells == nil {
		deps.Shells = NewShells(deps.Logger)
	}
	return &Runner{
		deps: deps,
		wake: make(chan struct{}, 1),
		live: map[int64]*liveRun{},
	}
}

// Start sweeps leftovers from a previous run and begins admitting queued
// tasks until ctx is canceled or Stop is called.
func (r *Runner) Start(ctx context.Context) {
	r.persist = context.WithoutCancel(ctx)
	ctx, r.cancel = context.WithCancel(ctx)
	if n, err := r.deps.Store.SweepInterrupted(ctx); err != nil {
		r.deps.Logger.Error("startup sweep failed", "error", err)
	} else if n > 0 {
		r.deps.Logger.Warn("swept interrupted tasks from a previous run", "tasks", n)
	}
	r.wg.Add(1)
	go r.loop(ctx)
}

// Stop cancels in-flight runs (tree-killing their processes) and waits for
// the actors to persist their interrupted state.
func (r *Runner) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopGrace):
		r.deps.Logger.Error("task executors did not stop in time; continuing shutdown")
	}
}

// Wake nudges the admission loop; safe from any goroutine, never blocks.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// persistCtx returns the context for writes that must survive shutdown.
func (r *Runner) persistCtx() context.Context {
	if r.persist == nil {
		return context.Background()
	}
	return r.persist
}

func (r *Runner) loop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		r.admit(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
	}
}

// admit starts queued tasks while the global cap has room, in scheduler
// order (priority DESC, created_at ASC). Per-project caps and re-evaluation
// semantics arrive with T2.5.
func (r *Runner) admit(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	limit := r.deps.Config().MaxParallelTasks
	running, err := r.deps.Store.CountRunning(ctx)
	if err != nil {
		r.deps.Logger.Error("admission: count running", "error", err)
		return
	}
	if running >= limit {
		return
	}
	queued, err := r.deps.Store.ListQueuedInOrder(ctx)
	if err != nil {
		r.deps.Logger.Error("admission: list queued", "error", err)
		return
	}
	for i := range queued {
		if running >= limit || ctx.Err() != nil {
			return
		}
		task := queued[i]
		log := r.deps.Logger.With("task", task.ID)
		admitted, _, err := r.deps.Store.TransitionTask(ctx, task.ID,
			store.TaskQueued, store.TaskRunning, store.TaskChange{})
		if err != nil {
			if _, conflict := store.AsStateConflict(err); conflict {
				continue // someone acted on it between the query and now
			}
			log.Error("admission: mark running", "error", err)
			return
		}
		running++
		r.trackRun(admitted.ID)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.forgetRun(admitted.ID)
			defer r.Wake() // slot freed → re-admit
			r.execute(ctx, admitted)
		}()
	}
}

// trackRun registers a task as live so actions can reach it.
func (r *Runner) trackRun(taskID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[taskID] = &liveRun{}
}

func (r *Runner) forgetRun(taskID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, taskID)
}

func (r *Runner) lookupRun(taskID int64) (*liveRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.live[taskID]
	return lr, ok
}

// RequestPause marks a running task to stop at its next step boundary (§6).
// It reports whether the task is live; a task that is not running needs no
// deferral and transitions immediately.
func (r *Runner) RequestPause(taskID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.live[taskID]
	if !ok {
		return false
	}
	lr.pauseRequested = true
	return true
}

func (r *Runner) pauseRequested(taskID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.live[taskID]
	return ok && lr.pauseRequested
}

// Running reports whether the engine currently holds a live run for a task.
func (r *Runner) Running(taskID int64) bool {
	_, ok := r.lookupRun(taskID)
	return ok
}
