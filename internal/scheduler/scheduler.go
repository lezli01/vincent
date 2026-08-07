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
}

// New returns a stopped scheduler.
func New(deps Deps) *Scheduler {
	return &Scheduler{deps: deps, wake: make(chan struct{}, 1)}
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
	limit := s.deps.Config().MaxParallelTasks
	global, err := s.deps.Store.CountSlotHolders(ctx)
	if err != nil {
		s.deps.Logger.Error("admission: count slot holders", "error", err)
		return
	}
	if global >= limit {
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
	for i := range candidates {
		if global >= limit || ctx.Err() != nil {
			return
		}
		c := candidates[i]
		log := s.deps.Logger.With("task", c.Task.ID)

		// A pause requested while the task was running outlives the task's
		// return to queued — a crash re-queues without clearing it (§6). Take
		// effect now rather than running a step the human already stopped.
		if c.Task.PauseRequested {
			s.park(&c.Task, log)
			continue
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

func (s *Scheduler) persistCtx() context.Context {
	if s.persist == nil {
		return context.Background()
	}
	return s.persist
}

// WakeOn reports whether an event should trigger re-evaluation. Only two
// event types change what may be admitted; the rest (step progress, gate
// waits) would just spin the loop.
func WakeOn(e *store.Event) bool {
	return e != nil &&
		(e.Type == store.EventTaskStateChanged || e.Type == store.EventTaskPriorityChanged)
}
