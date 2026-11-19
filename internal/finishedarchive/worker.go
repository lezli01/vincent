package finishedarchive

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/issuearchive"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// ArchivedBy is the archived_by value an age-based archive writes on its
// task.state_changed → archived event (§13.3, task 155).
const ArchivedBy = "finished_after"

// DefaultInterval is how often the loop sweeps. A minute late on a
// twelve-hour clock is nothing, and the query is one indexed statement.
const DefaultInterval = time.Minute

// RetryAfter is how long a refused task waits before it is asked again.
const RetryAfter = time.Hour

// Store is the slice of *store.Store the worker reads.
type Store interface {
	FinishedArchiveDue(ctx context.Context, cutoff time.Time) ([]int64, error)
}

// Archiver is taskrun.Runner's archive.
type Archiver interface {
	Archive(ctx context.Context, id int64, opts taskrun.ArchiveOptions) (*store.Task, worktree.BranchOutcome, error)
}

// Deps wires a Worker.
type Deps struct {
	Store    Store
	Archiver Archiver
	// After reads archive_finished_tasks_after from the live config. It is
	// asked once per sweep; zero or less is off.
	After func() time.Duration
	// Interval is the time between sweeps; zero is DefaultInterval.
	Interval time.Duration
	Logger   *slog.Logger
}

// Worker is the age-based auto-archive loop.
type Worker struct {
	deps Deps

	// refused maps a task the archive refused to when it may be asked again.
	// Only the loop goroutine touches it.
	refused map[int64]time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Worker. It does nothing until Start.
func New(deps Deps) *Worker {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.After == nil {
		deps.After = func() time.Duration { return 0 }
	}
	if deps.Interval <= 0 {
		deps.Interval = DefaultInterval
	}
	return &Worker{deps: deps, refused: map[int64]time.Time{}}
}

// Start runs the loop on its own goroutine: one sweep at once, then one
// every Interval, until ctx ends or Stop is called.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		w.run(ctx)
	}()
}

// Stop cancels the loop and returns only once it has exited, so no archive
// is in flight when the daemon closes the store (§12.4). It is
// issuearchive.Worker's Stop shape. Stop before Start, or twice, is a no-op.
func (w *Worker) Stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if done == nil {
		return
	}
	cancel()
	<-done
}

func (w *Worker) run(ctx context.Context) {
	t := time.NewTicker(w.deps.Interval)
	defer t.Stop()
	w.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Sweep(ctx)
		}
	}
}

// Sweep archives every task due now, one at a time, in the order the query
// returns them. A refusal is logged and the next task is tried; nothing is
// ever forced.
func (w *Worker) Sweep(ctx context.Context) {
	after := w.deps.After()
	if after <= 0 {
		// Off: forget refusals too, so turning it back on asks everything.
		clear(w.refused)
		return
	}
	log := w.deps.Logger
	now := time.Now()
	ids, err := w.deps.Store.FinishedArchiveDue(ctx, now.Add(-after))
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("finished auto-archive: read due tasks", "error", err)
		}
		return
	}
	w.forgetAllBut(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if retry, ok := w.refused[id]; ok && now.Before(retry) {
			continue
		}
		_, _, err := w.deps.Archiver.Archive(ctx, id, taskrun.ArchiveOptions{
			Unattended:   true,
			EventPayload: map[string]any{"archived_by": ArchivedBy},
		})
		if err == nil {
			delete(w.refused, id)
			log.Info("finished auto-archive: archived task", "task", id, "after", after.String())
			continue
		}
		var invalid *taskrun.InvalidActionError
		if errors.As(err, &invalid) || errors.Is(err, store.ErrNotFound) {
			// Moved since the query — archived by hand, followed up — or
			// deleted. The next sweep's query has the truth.
			delete(w.refused, id)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		level := slog.LevelWarn
		if _, again := w.refused[id]; again {
			level = slog.LevelDebug
		}
		w.refused[id] = now.Add(RetryAfter)
		log.Log(ctx, level, "finished auto-archive: task not archived",
			"task", id, "reason", issuearchive.RefusalReason(err), "retry_after", RetryAfter.String(), "error", err)
	}
}

// forgetAllBut drops every remembered refusal for a task no longer due, so
// the map holds at most the due set.
func (w *Worker) forgetAllBut(ids []int64) {
	if len(w.refused) == 0 {
		return
	}
	due := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		due[id] = struct{}{}
	}
	for id := range w.refused {
		if _, ok := due[id]; !ok {
			delete(w.refused, id)
		}
	}
}
