package finishedarchive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/worktree"
)

// fakeStore answers FinishedArchiveDue with due and records each cutoff.
type fakeStore struct {
	mu      sync.Mutex
	due     []int64
	err     error
	cutoffs []time.Time
}

func (s *fakeStore) FinishedArchiveDue(_ context.Context, cutoff time.Time) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cutoffs = append(s.cutoffs, cutoff)
	return slices.Clone(s.due), s.err
}

func (s *fakeStore) setDue(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.due = ids
}

func (s *fakeStore) sweeps() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cutoffs)
}

// fakeArchiver records each call and answers from errs; a successful
// archive drops the id from the store's due set, as the real one would.
type fakeArchiver struct {
	mu    sync.Mutex
	st    *fakeStore
	calls []int64
	opts  []taskrun.ArchiveOptions
	errs  map[int64]error
}

func (a *fakeArchiver) Archive(_ context.Context, id int64, opts taskrun.ArchiveOptions) (*store.Task, worktree.BranchOutcome, error) {
	a.mu.Lock()
	a.calls = append(a.calls, id)
	a.opts = append(a.opts, opts)
	err := a.errs[id]
	a.mu.Unlock()
	if err == nil {
		a.st.mu.Lock()
		a.st.due = slices.DeleteFunc(a.st.due, func(d int64) bool { return d == id })
		a.st.mu.Unlock()
	}
	return &store.Task{ID: id}, worktree.BranchOutcome{}, err
}

func (a *fakeArchiver) ids() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func (a *fakeArchiver) setErr(id int64, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		delete(a.errs, id)
		return
	}
	a.errs[id] = err
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type harness struct {
	st    *fakeStore
	arch  *fakeArchiver
	w     *Worker
	after atomic.Int64
	log   *syncBuffer
}

func newHarness(after time.Duration) *harness {
	h := &harness{st: &fakeStore{}, log: &syncBuffer{}}
	h.arch = &fakeArchiver{st: h.st, errs: map[int64]error{}}
	h.after.Store(int64(after))
	h.w = New(Deps{
		Store:    h.st,
		Archiver: h.arch,
		After:    func() time.Duration { return time.Duration(h.after.Load()) },
		Logger:   slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return h
}

// TestSweepArchivesDueTasksUnattended: every due task is archived, in the
// query's order, unattended and marked archived_by, against the cutoff
// now − after.
func TestSweepArchivesDueTasksUnattended(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(12 * time.Hour)
		h.st.setDue(3, 1, 2)
		h.w.Sweep(t.Context())

		if got := h.arch.ids(); !slices.Equal(got, []int64{3, 1, 2}) {
			t.Fatalf("archived %v, want [3 1 2]", got)
		}
		for _, o := range h.arch.opts {
			if !o.Unattended || o.Force {
				t.Errorf("archive options %+v, want unattended and never forced", o)
			}
			if o.EventPayload["archived_by"] != ArchivedBy {
				t.Errorf("archived_by = %v, want %q", o.EventPayload["archived_by"], ArchivedBy)
			}
		}
		if want := time.Now().Add(-12 * time.Hour); !h.st.cutoffs[0].Equal(want) {
			t.Errorf("cutoff = %v, want %v", h.st.cutoffs[0], want)
		}
	})
}

// TestSweepOffDoesNothing: zero is off, and nothing is even read.
func TestSweepOffDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(0)
		h.st.setDue(1)
		h.w.Sweep(t.Context())
		if n := h.st.sweeps(); n != 0 {
			t.Errorf("read the due set %d times while off", n)
		}
		if got := h.arch.ids(); len(got) != 0 {
			t.Errorf("archived %v while off", got)
		}
	})
}

// TestRefusalBacksOff: a refused task is warned about once and left alone
// for RetryAfter, then asked again; a repeat refusal is logged at debug.
// Other due tasks are archived regardless.
func TestRefusalBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(time.Hour)
		h.st.setDue(1, 2)
		h.arch.setErr(1, errors.New("worktree has local changes"))

		h.w.Sweep(t.Context())
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("first sweep archived %v, want [1 2]", got)
		}
		if n := strings.Count(h.log.String(), "level=WARN msg=\"finished auto-archive: task not archived\""); n != 1 {
			t.Fatalf("warned %d times, want 1:\n%s", n, h.log)
		}

		time.Sleep(RetryAfter - time.Minute)
		h.w.Sweep(t.Context())
		if got := h.arch.ids(); len(got) != 2 {
			t.Fatalf("a refused task was asked again within RetryAfter: %v", got)
		}

		time.Sleep(time.Minute)
		h.w.Sweep(t.Context())
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2, 1}) {
			t.Fatalf("after RetryAfter archived %v, want [1 2 1]", got)
		}
		out := h.log.String()
		if n := strings.Count(out, "level=WARN"); n != 1 {
			t.Errorf("a repeat refusal warned again (%d warnings)", n)
		}
		if !strings.Contains(out, "level=DEBUG msg=\"finished auto-archive: task not archived\"") {
			t.Errorf("a repeat refusal is not logged at debug:\n%s", out)
		}

		h.arch.setErr(1, nil)
		time.Sleep(RetryAfter)
		h.w.Sweep(t.Context())
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2, 1, 1}) {
			t.Fatalf("a cleared refusal was not retried: %v", got)
		}
	})
}

// TestTaskLeavingDueSetIsForgotten: a refused task followed up (or archived
// by hand) leaves the due set; when it is due again it is asked at once,
// with a fresh warning.
func TestTaskLeavingDueSetIsForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(time.Hour)
		h.st.setDue(1)
		h.arch.setErr(1, errors.New("dirty"))
		h.w.Sweep(t.Context())

		h.st.setDue()
		h.w.Sweep(t.Context())
		h.st.setDue(1)
		h.w.Sweep(t.Context())
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 1}) {
			t.Fatalf("archived %v, want [1 1]", got)
		}
		if n := strings.Count(h.log.String(), "level=WARN"); n != 2 {
			t.Errorf("warned %d times, want 2", n)
		}
	})
}

// TestMovedTaskIsSkippedQuietly: a task archived by hand or followed up
// between the query and the archive is no refusal.
func TestMovedTaskIsSkippedQuietly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(time.Hour)
		h.st.setDue(1, 2)
		h.arch.setErr(1, &taskrun.InvalidActionError{TaskID: 1, Action: taskstate.Archive, State: store.TaskRunning})
		h.arch.setErr(2, store.ErrNotFound)
		h.w.Sweep(t.Context())
		if out := h.log.String(); strings.Contains(out, "not archived") {
			t.Errorf("a moved task was logged as refused:\n%s", out)
		}
		if len(h.w.refused) != 0 {
			t.Errorf("a moved task was remembered as refused: %v", h.w.refused)
		}
	})
}

// TestLoopSweepsOnStartAndEveryInterval: Start sweeps at once and then every
// DefaultInterval, reads the switch per sweep (a hot reload), and Stop
// returns only once the loop has exited.
func TestLoopSweepsOnStartAndEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(time.Hour)
		h.st.setDue(1)
		h.w.Start(t.Context())
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1}) {
			t.Fatalf("start-up sweep archived %v, want [1]", got)
		}

		h.after.Store(0)
		h.st.setDue(2)
		time.Sleep(DefaultInterval)
		synctest.Wait()
		if got := h.arch.ids(); len(got) != 1 {
			t.Fatalf("archived %v after being switched off", got)
		}

		h.after.Store(int64(time.Hour))
		time.Sleep(DefaultInterval)
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("archived %v after being switched back on, want [1 2]", got)
		}
		h.w.Stop()
		h.w.Stop()
	})
}
