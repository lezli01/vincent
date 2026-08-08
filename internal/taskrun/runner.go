// Package taskrun is the workflow execution engine: one goroutine per
// admitted task walks its workflow snapshot step by step and is the sole
// writer of that task's state and of its step_run rows (phase 2 decision).
// An actor lives for exactly one admission — reaching a gate, blocking, or
// pausing releases the task's concurrency slot and ends the goroutine; the
// scheduler admits it again when a human unblocks it.
//
// Admission itself belongs to internal/scheduler, which is the only place
// `queued → running` happens (§11). This package implements its Admitter,
// and hosts the human actions of §6, which reach a live run through the
// control map below.
package taskrun

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// DefaultAgent is used when neither the step, the task override, nor the
// workflow defaults name an agent. Aliases the §8.6 resolver's constant so
// the two can never drift.
const DefaultAgent = agent.DefaultAgent

// stopGrace bounds how long Stop waits for actors after cancellation.
const stopGrace = 20 * time.Second

// cancelGrace is §6's window between asking a process tree to exit and
// killing it.
const cancelGrace = 10 * time.Second

// ErrRunnerStopped is returned by Admit once the runner is shutting down;
// the scheduler returns the task to the queue rather than leaving it running
// with nobody executing it.
var ErrRunnerStopped = errors.New("task runner is stopping")

// Deps are the daemon facilities the engine works with.
type Deps struct {
	Store     *store.Store
	Config    func() config.Config
	Worktrees *worktree.Manager
	Agents    *agent.Registry
	Shells    *Shells
	DataDir   string
	Logger    *slog.Logger
	// Events receives live output chunks for the per-task SSE stream
	// (§13.3). Nil is tolerated (tests without streaming); the transcript
	// stays the durable copy either way.
	Events *events.Broker
}

// Runner executes admitted tasks and applies the §6 human actions.
type Runner struct {
	deps   Deps
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// stopping marks a graceful shutdown in progress: processes have been
	// asked to exit, and an abrupt exit is an interruption to re-queue, not
	// a failure to retry (§12.4).
	stopping atomic.Bool

	// base is the parent of every task's run context; canceling it stops
	// every actor. nil until Start.
	base context.Context //nolint:containedctx // the runner's own lifetime
	// persist is a context detached from shutdown: a task's final state must
	// still reach the database after the run context is canceled.
	persist context.Context //nolint:containedctx // deliberately outlives the run

	mu   sync.Mutex
	live map[int64]*liveRun
}

// stopper is the part of a live process the human actions need: ask it to
// exit, then insist. Both a procx.Proc and an agent run satisfy it.
type stopper interface {
	Terminate() error
	Kill() error
}

// liveRun is one task's in-flight execution — the channel through which a
// human action reaches a running process.
type liveRun struct {
	// cancel ends the task's run context, which unwinds the actor.
	cancel context.CancelFunc
	// done closes when the actor has finished.
	done chan struct{}

	mu sync.Mutex
	// pauseRequested mirrors the persisted flag for the live path; the actor
	// reads it at the next step boundary (§6).
	pauseRequested bool
	// canceling marks that the interruption about to happen is a human
	// cancel, so the attempt records `canceled` rather than `interrupted`.
	canceling bool
	// proc is the process currently executing this task's step, if any.
	proc stopper
}

// New returns a stopped runner.
func New(deps Deps) *Runner {
	if deps.Shells == nil {
		deps.Shells = NewShells(deps.Logger)
	}
	return &Runner{deps: deps, live: map[int64]*liveRun{}}
}

// Start prepares the runner to accept admissions until ctx is canceled or
// Stop is called.
func (r *Runner) Start(ctx context.Context) {
	r.persist = context.WithoutCancel(ctx)
	ctx, r.cancel = context.WithCancel(ctx)
	r.base = ctx
}

// Stop cancels in-flight runs (tree-killing their processes) and waits for
// the actors to persist their interrupted state.
func (r *Runner) Stop() {
	if r.cancel == nil {
		return
	}
	r.stopping.Store(true)
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopGrace):
		r.deps.Logger.Error("task executors did not stop in time; continuing shutdown")
	}
}

// StopGraceful is §12.4's shutdown path: ask every live process tree to
// exit, give the actors up to grace to persist their re-queue transitions
// (visible on SSE — the API is still up while this runs), then fall back to
// Stop's hard kill for whatever remains.
func (r *Runner) StopGraceful(grace time.Duration) {
	if r.cancel == nil {
		return
	}
	r.stopping.Store(true)

	r.mu.Lock()
	procs := make([]stopper, 0, len(r.live))
	for _, lr := range r.live {
		lr.mu.Lock()
		if lr.proc != nil {
			procs = append(procs, lr.proc)
		}
		lr.mu.Unlock()
	}
	r.mu.Unlock()
	for _, p := range procs {
		if err := p.Terminate(); err != nil {
			r.deps.Logger.Warn("shutdown: terminate failed; the grace-period kill will follow", "error", err)
		}
	}

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		r.deps.Logger.Warn("processes outlived the shutdown grace; killing", "grace", grace)
	}
	r.Stop()
}

// Admit implements scheduler.Admitter: it takes ownership of a task the
// scheduler has already moved to running and starts its actor. It returns
// immediately.
func (r *Runner) Admit(_ context.Context, task *store.Task) error {
	if r.base == nil || r.base.Err() != nil || r.stopping.Load() {
		return ErrRunnerStopped
	}
	taskCtx, cancel := context.WithCancel(r.base)
	lr := &liveRun{cancel: cancel, done: make(chan struct{})}

	r.mu.Lock()
	if _, exists := r.live[task.ID]; exists {
		// Two actors for one task would both claim to be its sole writer.
		r.mu.Unlock()
		cancel()
		return errors.New("task is already running")
	}
	r.live[task.ID] = lr
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		defer r.forgetRun(task.ID)
		r.execute(taskCtx, task)
	}()
	return nil
}

func (r *Runner) forgetRun(taskID int64) {
	r.mu.Lock()
	lr := r.live[taskID]
	delete(r.live, taskID)
	r.mu.Unlock()
	if lr != nil {
		close(lr.done)
	}
}

func (r *Runner) lookupRun(taskID int64) (*liveRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.live[taskID]
	return lr, ok
}

// Running reports whether the engine currently holds a live run for a task.
func (r *Runner) Running(taskID int64) bool {
	_, ok := r.lookupRun(taskID)
	return ok
}

// setProc registers the process executing the current step, so cancel can
// reach it. The returned function unregisters it.
func (r *Runner) setProc(taskID int64, p stopper) func() {
	lr, ok := r.lookupRun(taskID)
	if !ok {
		return func() {}
	}
	lr.mu.Lock()
	lr.proc = p
	lr.mu.Unlock()
	return func() {
		lr.mu.Lock()
		lr.proc = nil
		lr.mu.Unlock()
	}
}

func (r *Runner) pauseRequested(taskID int64) bool {
	lr, ok := r.lookupRun(taskID)
	if !ok {
		return false
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.pauseRequested
}

// canceling reports whether this task's interruption is a human cancel.
func (r *Runner) canceling(taskID int64) bool {
	lr, ok := r.lookupRun(taskID)
	if !ok {
		return false
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.canceling
}

// interrupting reports whether an abrupt process exit for this task should
// read as an interruption rather than a failure: a human cancel or a daemon
// shutdown has already asked the process to die (§6, §12.4).
func (r *Runner) interrupting(taskID int64) bool {
	return r.stopping.Load() || r.canceling(taskID)
}

// publishOutput streams one live-output chunk to the task's SSE subscribers
// (§13.3). No broker, no subscribers, or a slow subscriber all lose nothing
// durable: the transcript is the durable copy.
func (r *Runner) publishOutput(taskID int64, chunkType string, payload map[string]any) {
	if r.deps.Events == nil {
		return
	}
	r.deps.Events.PublishOutput(taskID, events.Chunk{Type: chunkType, Payload: payload})
}

// stop ends a live run for a cancel: §6's graceful term, then kill after the
// grace period. It blocks for as long as that takes, so it runs off the
// request path — the task is durably aborted before this is called, and
// reaping the process tree is bookkeeping the client need not wait for.
func (lr *liveRun) stop(grace time.Duration, log *slog.Logger) {
	lr.mu.Lock()
	lr.canceling = true
	proc := lr.proc
	lr.mu.Unlock()

	if proc != nil {
		if err := proc.Terminate(); err != nil {
			log.Warn("cancel: terminate failed; killing", "error", err)
		}
	}
	select {
	case <-lr.done:
	case <-time.After(grace):
		lr.mu.Lock()
		proc = lr.proc
		lr.mu.Unlock()
		if proc != nil {
			if err := proc.Kill(); err != nil {
				log.Warn("cancel: kill failed", "error", err)
			}
		}
	}
	// Unblock the actor even if no process was running — it may be between
	// steps, or waiting on git.
	lr.cancel()
}

// persistCtx returns the context for writes that must survive shutdown.
func (r *Runner) persistCtx() context.Context {
	if r.persist == nil {
		return context.Background()
	}
	return r.persist
}
