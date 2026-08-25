// Package scheduler decides which queued tasks may run (spec §11).
//
// It is a single goroutine, and it is the only place in vincent where
// `queued → running` happens. That is what makes the two concurrency caps
// safe: admission reads the current slot counts and writes the new state
// with nothing else racing it. Everything downstream — the actor, the human
// actions, crash recovery — moves tasks *out* of running, never into it.
//
// Ordering and both caps come from SQL (see store.ListAdmissible). The walk
// itself is here, because admitting a task changes the tallies and a single
// statement cannot see its own in-flight admissions.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// tickInterval is the safety net. Wake drives admission in practice — every
// committed state change fires it — so a tick normally finds nothing to do.
//
// It has two exceptions, and the second arrived with task 003: a task held by
// `admit_not_before` (§11) becomes admissible with no state change to wake
// anyone, so the tick is what picks it up — within 5 s of the hold expiring,
// which is why the hold needs no timer of its own.
const tickInterval = 5 * time.Second

// Admitter runs a task the scheduler has admitted. The task is already in
// state running when Admit is called.
type Admitter interface {
	// Admit takes ownership of an admitted task and begins executing it. It
	// must not block: the scheduler is walking the queue.
	//
	// An error means the task was not taken — a runner that is shutting
	// down, for instance — and the scheduler returns it to queued.
	Admit(ctx context.Context, task *store.Task) error
}

// Deps are the facilities the scheduler works with.
type Deps struct {
	Store    *store.Store
	Config   func() config.Config
	Admitter Admitter
	Logger   *slog.Logger
	// Now is the clock the §11 admission hold is measured against; nil means
	// time.Now. Injected by tests so "not before T, admitted after T" is
	// assertable without sleeping (task 003).
	Now func() time.Time
}

// Scheduler admits queued tasks within the §11 caps.
type Scheduler struct {
	deps Deps
	wake chan struct{}

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// persist is detached from shutdown: returning a task that could not be
	// admitted must still reach the database.
	persist context.Context //nolint:containedctx // deliberately outlives the run

	// reported remembers which unreconciled tasks have already been logged.
	// The refusal below is permanent for the life of the process — nothing
	// here reconciles a task — and the tick is 5 s, so without this the one
	// line an operator needs would arrive 720 times an hour. Touched only
	// from the admit walk, which is the single scheduler goroutine.
	reported map[int64]struct{}
}

// New returns a stopped scheduler.
func New(deps Deps) *Scheduler {
	return &Scheduler{deps: deps, wake: make(chan struct{}, 1), reported: map[int64]struct{}{}}
}

// Start begins admitting until ctx is canceled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	s.persist = context.WithoutCancel(ctx)
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop ends admission and waits for the loop to exit. In-flight tasks are
// not touched: stopping their actors is the runner's job.
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// Wake asks for a re-evaluation. Safe from any goroutine, never blocks: the
// store's event hook calls it on the writing goroutine.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		s.admit(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// admit walks the queue in §11 order, admitting while both caps have room.
// A project at its cap is skipped and the walk continues — a busy project
// must not starve the rest of the queue.
func (s *Scheduler) admit(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Fan-out parents whose subtrees have finished come back to the queue
	// first, so this same walk can admit them (§7.6, task 014 decision 25).
	s.resumeSettledParents(ctx)
	limit := s.deps.Config().MaxParallelTasks
	global, err := s.deps.Store.CountSlotHolders(ctx)
	if err != nil {
		s.deps.Logger.Error("admission: count slot holders", "error", err)
		return
	}
	candidates, err := s.deps.Store.ListAdmissible(ctx)
	if err != nil {
		s.deps.Logger.Error("admission: list admissible", "error", err)
		return
	}

	// admitted counts this walk's own admissions per project; the candidate
	// rows carry the counts as of the query, which predate them.
	admitted := map[int64]int{}
	now := s.now()
	for i := range candidates {
		if ctx.Err() != nil {
			return
		}
		c := candidates[i]
		log := s.deps.Logger.With("task", c.Task.ID)

		// A pause requested while the task was running outlives the task's
		// return to queued — a crash re-queues without clearing it (§6). Take
		// effect now rather than running a step the human already stopped.
		// This runs even with the caps full: the human asked for `paused`,
		// and a task showing `queued` until a slot frees would be a lie.
		if c.Task.PauseRequested {
			s.park(&c.Task, log)
			continue
		}
		// A queued task with a step run still marked `running` was never
		// reconciled: §12.4 finalizes the previous attempt *before* the task
		// returns to the queue, so admitting this one would start a second
		// attempt against a first the database still calls live (issue #142).
		// Recovery now fails startup rather than producing such a row; this
		// is the guard for one that predates the fix or arrived by some route
		// nobody has thought of. Refusing is safe — the task stays queued —
		// and the reason has to reach a human, because nothing else will.
		if c.OpenStepRuns > 0 {
			s.refuseUnreconciled(&c.Task, c.OpenStepRuns, log)
			continue
		}
		// An admission hold (§11, task 003): the task is queued and waiting on
		// something other than a slot — an agent's usage window, today.
		//
		// It is checked here, in the walk, rather than filtered out in
		// ListAdmissible's SQL, and the pause check above is why: hiding held
		// tasks from the query would mean a `pause` on one silently did not
		// take effect until the hold expired, which is exactly the "showing
		// queued while the human asked for paused" lie the comment above
		// calls out. Pause first, then the hold, then the caps.
		if c.Task.AdmitNotBefore != nil && now.Before(*c.Task.AdmitNotBefore) {
			continue
		}
		if global >= limit {
			continue // keep walking: later candidates may need parking
		}
		if c.ProjectCap != nil && c.ProjectSlots+admitted[c.Task.ProjectID] >= *c.ProjectCap {
			continue
		}
		if !s.start(ctx, &c.Task, log) {
			continue
		}
		global++
		admitted[c.Task.ProjectID]++
	}
}

// start moves one task into running and hands it to the runner, undoing the
// transition if the runner will not take it. It reports whether the task
// now holds a slot.
func (s *Scheduler) start(ctx context.Context, task *store.Task, log *slog.Logger) bool {
	running, _, err := s.deps.Store.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{})
	if err != nil {
		if _, conflict := store.AsStateConflict(err); conflict {
			return false // a human acted between the query and now
		}
		log.Error("admission: mark running", "error", err)
		return false
	}
	if err := s.deps.Admitter.Admit(ctx, running); err != nil {
		// Nobody is executing this task, so it must not stay running: an
		// orphaned running row would be invisible until the next startup
		// sweep. Interrupt is the same transition a crash produces, and it
		// consumes no retry (§7.2).
		log.Warn("admission: runner did not take the task; re-queuing", "error", err)
		if _, _, rerr := s.deps.Store.TransitionTask(s.persistCtx(), running.ID,
			store.TaskRunning, store.TaskQueued, store.TaskChange{}); rerr != nil {
			log.Error("admission: re-queue after failed handoff", "error", rerr)
		}
		return false
	}
	return true
}

// refuseUnreconciled declines to admit a task whose previous attempt is still
// open, and says so once per task per daemon process.
func (s *Scheduler) refuseUnreconciled(task *store.Task, open int, log *slog.Logger) {
	if _, seen := s.reported[task.ID]; seen {
		return
	}
	s.reported[task.ID] = struct{}{}
	log.Error("admission refused: task is queued but its previous attempt was never finalized; "+
		"crash recovery (§12.4) could not reconcile it. It will not run until it is. "+
		"Restart the daemon to retry recovery; `vincent doctor` reports the same finding",
		"open_step_runs", open, "step", task.CurrentStep)
}

// park applies a pause that was requested while the task was running and
// then outlived its return to the queue (§6).
func (s *Scheduler) park(task *store.Task, log *slog.Logger) {
	noPause := false
	if _, _, err := s.deps.Store.TransitionTask(s.persistCtx(), task.ID,
		store.TaskQueued, store.TaskPaused,
		store.TaskChange{PauseRequested: &noPause}); err != nil {
		if _, conflict := store.AsStateConflict(err); !conflict {
			log.Error("admission: park paused task", "error", err)
		}
		return
	}
	log.Info("task paused before admission; pause was requested while it ran")
}

// now returns the injected clock, defaulting to the real one.
func (s *Scheduler) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

func (s *Scheduler) persistCtx() context.Context {
	if s.persist == nil {
		return context.Background()
	}
	return s.persist
}

// resumeSettledParents returns every parent parked in `awaiting_children` to
// the queue once each of its descendants has settled (task 014).
//
// This is the scheduler's job rather than the last child's actor for the
// reason admission itself is: two children settling concurrently would both
// believe they were last, or neither would, and an actor writing another
// task's state breaks the sole-writer invariant outright. Here it is one
// goroutine making one decision, which is the property the whole package
// exists to provide.
func (s *Scheduler) resumeSettledParents(ctx context.Context) {
	parents, err := s.deps.Store.ListTasks(ctx, store.TaskFilter{
		State: store.TaskAwaitingChildren, Children: store.ChildrenInclude,
	})
	if err != nil {
		s.deps.Logger.Error("admission: list parked parents", "error", err)
		return
	}
	for _, parent := range parents {
		rollup, err := s.deps.Store.ChildrenOf(ctx, parent.ID)
		if err != nil {
			s.deps.Logger.Error("admission: roll up children", "task", parent.ID, "error", err)
			continue
		}
		if !rollup.Done() {
			continue
		}
		if _, _, err := s.deps.Store.TransitionTask(ctx, parent.ID,
			store.TaskAwaitingChildren, store.TaskQueued, store.TaskChange{}); err != nil {
			if _, conflict := store.AsStateConflict(err); conflict {
				// A human cancelled it between the list and the write.
				continue
			}
			s.deps.Logger.Error("admission: resume parent", "task", parent.ID, "error", err)
			continue
		}
		s.deps.Logger.Info("fan-out children settled; parent re-queued",
			"task", parent.ID, "children", rollup.Total)
	}
}

// WakeOn reports whether an event should trigger re-evaluation. Only two
// event types change what may be admitted; the rest (step progress, gate
// waits) would just spin the loop.
func WakeOn(e *store.Event) bool {
	return e != nil &&
		(e.Type == store.EventTaskStateChanged || e.Type == store.EventTaskPriorityChanged)
}
