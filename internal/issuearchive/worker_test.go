package issuearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// fakeStore serves an in-memory event table and per-issue answers.
type fakeStore struct {
	mu         sync.Mutex
	events     []store.Event
	cursor     int64
	cursorPuts []int64
	archivable map[int64][]int64 // issue → ArchivableIssueTasks
	claimed    map[int64]bool
	taskIssue  map[int64]int64
	main       map[int64]bool // task → holds the issue's main role
	gone       map[int64]bool // task → deleted
	handoffs   map[int64][]int64 // handoff → issue ids
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		archivable: map[int64][]int64{}, claimed: map[int64]bool{},
		taskIssue: map[int64]int64{}, handoffs: map[int64][]int64{},
		main: map[int64]bool{}, gone: map[int64]bool{},
	}
}

func (f *fakeStore) add(typ string, body map[string]any) *store.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(body)
	e := store.Event{ID: int64(len(f.events) + 1), Type: typ, Payload: b}
	f.events = append(f.events, e)
	return &e
}

func (f *fakeStore) GetIssueArchiveCursor(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursor, nil
}

func (f *fakeStore) PutIssueArchiveCursor(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor = id
	f.cursorPuts = append(f.cursorPuts, id)
	return nil
}

func (f *fakeStore) ListEvents(_ context.Context, flt store.EventFilter) ([]store.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Event
	for _, e := range f.events {
		if e.ID <= flt.AfterID || (len(flt.Types) > 0 && !slices.Contains(flt.Types, e.Type)) {
			continue
		}
		out = append(out, e)
		if flt.Limit > 0 && len(out) == flt.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) ArchivableIssueTasks(_ context.Context, issueID int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.archivable[issueID], nil
}

func (f *fakeStore) IssueHandoffClaimed(_ context.Context, issueID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimed[issueID], nil
}

func (f *fakeStore) GetTask(_ context.Context, id int64) (*store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone[id] {
		return nil, store.ErrNotFound
	}
	t := &store.Task{ID: id}
	if iss, ok := f.taskIssue[id]; ok {
		t.IssueID = &iss
	}
	if f.main[id] {
		t.IssueWorktree = store.IssueWorktreeMain
	}
	return t, nil
}

func (f *fakeStore) ListHandoffItems(_ context.Context, id int64) ([]*store.HandoffItem, store.HandoffState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*store.HandoffItem
	for _, iss := range f.handoffs[id] {
		out = append(out, &store.HandoffItem{HandoffID: id, IssueID: iss})
	}
	return out, store.HandoffSettled, nil
}

// fakeArchiver records each call and answers from errs.
type fakeArchiver struct {
	mu    sync.Mutex
	calls []archiveCall
	errs  map[int64]error
	// gate, when set, holds every Archive until it is closed, ignoring ctx
	// the way a worktree removal already under way does.
	gate chan struct{}
	// after, when set, runs after each call is recorded.
	after func(id int64)
}

type archiveCall struct {
	id   int64
	opts taskrun.ArchiveOptions
}

func (a *fakeArchiver) Archive(_ context.Context, id int64, opts taskrun.ArchiveOptions) (*store.Task, worktree.BranchOutcome, error) {
	a.mu.Lock()
	a.calls = append(a.calls, archiveCall{id, opts})
	gate, after, err := a.gate, a.after, a.errs[id]
	a.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if after != nil {
		after(id)
	}
	return &store.Task{ID: id}, worktree.BranchOutcome{}, err
}

func (a *fakeArchiver) ids() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]int64, 0, len(a.calls))
	for _, c := range a.calls {
		out = append(out, c.id)
	}
	return out
}

type harness struct {
	st      *fakeStore
	arch    *fakeArchiver
	w       *Worker
	enabled atomic.Bool
	log     *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newHarness() *harness {
	h := &harness{st: newFakeStore(), arch: &fakeArchiver{errs: map[int64]error{}}, log: &syncBuffer{}}
	h.enabled.Store(true)
	h.w = New(Deps{
		Store: h.st, Archiver: h.arch, Enabled: h.enabled.Load,
		Logger: slog.New(slog.NewTextHandler(h.log, nil)),
	})
	return h
}

// emit appends an event and publishes it, as the store's post-commit hook does.
func (h *harness) emit(typ string, body map[string]any) {
	h.w.OnEvent(h.st.add(typ, body))
}

func closeIssue(id int64) map[string]any {
	return map[string]any{"id": id, "from": "open", "to": "closed"}
}

// TestCloseArchivesInQueryOrder: a close archives what ArchivableIssueTasks
// returns, in its order, unattended, never forced, with archived_by and
// issue_id on the payload (task 143 decisions 9 and 1).
func TestCloseArchivesInQueryOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{3, 1, 2}
		h.w.Start(t.Context())
		synctest.Wait()

		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{3, 1, 2}) {
			t.Fatalf("archived %v, want the query's order [3 1 2]", got)
		}
		for _, c := range h.arch.calls {
			if !c.opts.Unattended || c.opts.Force {
				t.Errorf("task %d archived with %+v, want unattended and unforced", c.id, c.opts)
			}
			if c.opts.EventPayload["archived_by"] != ArchivedBy || c.opts.EventPayload["issue_id"] != int64(7) {
				t.Errorf("task %d payload = %v", c.id, c.opts.EventPayload)
			}
		}
		if h.st.cursor != 1 {
			t.Errorf("cursor = %d, want 1", h.st.cursor)
		}
	})
}

// TestEventsThatTouchAnIssue: each event that can make an issue archivable
// asks about it, and those that cannot ask nothing.
func TestEventsThatTouchAnIssue(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		body map[string]any
		want bool
	}{
		{"api close", store.EventIssueStateChanged, closeIssue(7), true},
		{"reopen", store.EventIssueStateChanged, map[string]any{"id": 7, "from": "closed", "to": "open"}, false},
		{"sync close", store.EventIssueUpdated, closeIssue(7), true},
		{"sync edit", store.EventIssueUpdated, map[string]any{"id": 7}, false},
		{"task done", store.EventTaskStateChanged, map[string]any{"to": "done", "issue_id": 7}, true},
		{"task aborted", store.EventTaskStateChanged, map[string]any{"to": "aborted", "issue_id": 7}, true},
		{"task archived", store.EventTaskStateChanged, map[string]any{"to": "archived", "issue_id": 7}, false},
		{"task running", store.EventTaskStateChanged, map[string]any{"to": "running", "issue_id": 7}, false},
		{"task without issue", store.EventTaskStateChanged, map[string]any{"to": "done"}, false},
		{"issue chat closed", store.EventChatClosed, map[string]any{"state": "closed", "issue_id": 7}, true},
		{"issue chat archived", store.EventChatArchived, map[string]any{"state": "archived", "issue_id": 7}, true},
		{"issue chat idle", store.EventChatState, map[string]any{"state": "idle", "issue_id": 7}, false},
		{"linked chat closed", store.EventChatClosed, map[string]any{"state": "closed", "linked_task_id": 40}, true},
		{"handoff item", store.EventHandoffItemChanged, map[string]any{"issue_id": 7, "to": "released"}, true},
		{"handoff settled", store.EventHandoffSettled, map[string]any{"id": 5, "task_id": 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness()
				h.st.archivable[7] = []int64{1}
				h.st.taskIssue[40] = 7
				h.st.handoffs[5] = []int64{7}
				h.w.Start(t.Context())
				h.emit(tc.typ, tc.body)
				synctest.Wait()
				if got := len(h.arch.ids()) == 1; got != tc.want {
					t.Errorf("archived = %v, want %v", h.arch.ids(), tc.want)
				}
			})
		})
	}
}

// TestRefusalsAreLoggedAndNeverBlock: a dirty worktree, a chat lock and an
// unfinished lane are each logged with their reason, the next task is still
// tried, an already-archived task is silent, and the cursor moves on; the
// later event that clears the cause retries.
func TestRefusalsAreLoggedAndNeverBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1, 2, 3, 4, 5}
		h.arch.errs[1] = &worktree.Error{Reason: worktree.ReasonWorktreeDirty, Message: "dirty"}
		h.arch.errs[2] = &store.TaskLockedError{TaskID: 2, ChatID: 9}
		h.arch.errs[3] = &taskrun.UnfinishedDescendantsError{TaskID: 3, IDs: []int64{30}}
		h.arch.errs[4] = &taskrun.InvalidActionError{TaskID: 4}
		h.w.Start(t.Context())
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2, 3, 4, 5}) {
			t.Fatalf("archived %v, want every task tried", got)
		}
		log := h.log.String()
		for _, want := range []string{"reason=worktree_dirty", "reason=task_locked_by_chat", "reason=unfinished_descendants"} {
			if !strings.Contains(log, want) {
				t.Errorf("log lacks %q:\n%s", want, log)
			}
		}
		if strings.Contains(log, "task=4 issue=7 reason") {
			t.Errorf("an already-archived task was logged:\n%s", log)
		}
		if h.st.cursor != 1 {
			t.Fatalf("cursor = %d, want past the close", h.st.cursor)
		}

		// The chat closes: the issue is asked again.
		delete(h.arch.errs, 2)
		h.st.archivable[7] = []int64{1, 2, 3}
		h.emit(store.EventChatClosed, map[string]any{"state": "closed", "issue_id": 7})
		synctest.Wait()
		if got := h.arch.ids()[5:]; !slices.Equal(got, []int64{1, 2, 3}) {
			t.Errorf("retry archived %v, want [1 2 3]", got)
		}
	})
}

// TestHandoffClaimHoldsTheArchive is task 143 decision 10: a claimed issue
// archives nothing, and the handoff event that ends the claim archives it.
func TestHandoffClaimHoldsTheArchive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1}
		h.st.claimed[7] = true
		h.w.Start(t.Context())
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); len(got) != 0 {
			t.Fatalf("a claimed issue archived %v", got)
		}
		h.st.mu.Lock()
		h.st.claimed[7] = false
		h.st.mu.Unlock()
		h.emit(store.EventHandoffItemChanged, map[string]any{"issue_id": 7, "from": "", "to": "released"})
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1}) {
			t.Errorf("after the release archived %v, want [1]", got)
		}
	})
}

// TestDisabledPassesEventsOver: with archive_tasks_on_issue_close off the
// worker archives nothing and still moves its cursor; turned back on, it
// acts on later events only — no restart, no backfill.
func TestDisabledPassesEventsOver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1}
		h.st.archivable[8] = []int64{2}
		h.enabled.Store(false)
		h.w.Start(t.Context())
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); len(got) != 0 {
			t.Fatalf("disabled worker archived %v", got)
		}
		if h.st.cursor != 1 {
			t.Fatalf("cursor = %d, want 1", h.st.cursor)
		}
		h.enabled.Store(true)
		h.emit(store.EventIssueStateChanged, closeIssue(8))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{2}) {
			t.Errorf("re-enabled worker archived %v, want only the later close's [2]", got)
		}
	})
}

// TestStartReplaysPastTheCursor is crash replay (§12.4): a close committed
// after the persisted cursor but never acted on is archived when the worker
// starts, with no event published to it.
func TestStartReplaysPastTheCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1}
		h.st.add(store.EventIssueStateChanged, closeIssue(6))
		h.st.cursor = 1
		h.st.add(store.EventIssueStateChanged, closeIssue(7))
		h.w.Start(t.Context())
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1}) {
			t.Errorf("start archived %v, want [1]", got)
		}
		if h.st.cursor != 2 {
			t.Errorf("cursor = %d, want 2", h.st.cursor)
		}
	})
}

// TestPagesPersistTheCursor: the cursor is written after every page, and
// an issue touched twice in a page is asked once.
func TestPagesPersistTheCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1}
		for range pageSize + 5 {
			h.st.add(store.EventTaskStateChanged, map[string]any{"to": "done", "issue_id": 7})
		}
		h.w.Start(t.Context())
		synctest.Wait()
		if !slices.Equal(h.st.cursorPuts, []int64{pageSize, pageSize + 5}) {
			t.Errorf("cursor writes = %v, want one per page", h.st.cursorPuts)
		}
		if got := h.arch.ids(); len(got) != 2 {
			t.Errorf("archived %v, want once per page", got)
		}
	})
}

// TestOnEventNeverBlocks: a burst of wakes while a drain is busy coalesces
// into the one-slot channel.
func TestOnEventNeverBlocks(t *testing.T) {
	h := newHarness()
	e := &store.Event{Type: store.EventIssueStateChanged}
	for range 10 {
		h.w.OnEvent(e)
	}
	h.w.OnEvent(&store.Event{Type: store.EventIssueCommentAdded})
	if len(h.w.wake) != 1 {
		t.Errorf("wake channel holds %d, want 1", len(h.w.wake))
	}
}

// TestStopJoinsAnInFlightArchive: Stop returns only once an archive already
// under way has returned, so the daemon never closes the store or the log
// under one (review F1).
func TestStopJoinsAnInFlightArchive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.arch.gate = make(chan struct{})
		h.st.archivable[7] = []int64{1}
		h.w.Start(t.Context())
		synctest.Wait()
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1}) {
			t.Fatalf("archived %v, want task 1 in flight", got)
		}

		var stopped atomic.Bool
		go func() {
			h.w.Stop()
			stopped.Store(true)
		}()
		synctest.Wait()
		if stopped.Load() {
			t.Fatal("Stop returned while an archive was in flight")
		}
		close(h.arch.gate)
		synctest.Wait()
		if !stopped.Load() {
			t.Fatal("Stop did not return after the archive did")
		}
		h.w.Stop() // a second Stop is a no-op
	})
}

// TestStopBeforeStart: a daemon that fails before starting the worker still
// calls Stop on its way out.
func TestStopBeforeStart(t *testing.T) {
	newHarness().w.Stop()
}

// TestRefusedSideWorkHoldsTheMainTasks: when a side task or merge-back is
// refused, the issue's main tasks are not archived that round, so a main
// branch is never released under side work still to land on it; once the
// refusal clears, the next event archives all of them (review F3).
func TestRefusedSideWorkHoldsTheMainTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{2, 3, 1}
		h.st.main[1] = true
		h.arch.errs[2] = &store.TaskLockedError{TaskID: 2, ChatID: 9}
		h.w.Start(t.Context())
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{2, 3}) {
			t.Fatalf("archived %v, want side tasks 2 and 3 tried and main task 1 held", got)
		}

		delete(h.arch.errs, 2)
		h.st.archivable[7] = []int64{2, 1}
		h.emit(store.EventChatClosed, map[string]any{"state": "closed", "issue_id": 7})
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{2, 3, 2, 1}) {
			t.Fatalf("archived %v, want 2 then main task 1 once the lock cleared", got)
		}
	})
}

// TestARefusedMainTaskDoesNotHoldOthers: a main task's own refusal holds
// nothing; only side work orders the main tasks.
func TestARefusedMainTaskDoesNotHoldOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.st.archivable[7] = []int64{1, 2}
		h.st.main[1], h.st.main[2] = true, true
		h.arch.errs[1] = &worktree.Error{Reason: worktree.ReasonWorktreeDirty, Message: "dirty"}
		h.w.Start(t.Context())
		h.emit(store.EventIssueStateChanged, closeIssue(7))
		synctest.Wait()
		if got := h.arch.ids(); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("archived %v, want both main tasks tried", got)
		}
	})
}

// TestReopenMidLoopStopsTheRest: the issue is asked again before every
// archive after the first, so a reopen, a new task or a handoff claim while
// the loop runs archives nothing more (review F4).
func TestReopenMidLoopStopsTheRest(t *testing.T) {
	for _, tc := range []struct {
		name string
		flip func(f *fakeStore)
	}{
		{"reopened", func(f *fakeStore) { f.archivable[7] = nil }},
		{"claimed by a handoff", func(f *fakeStore) { f.claimed[7] = true }},
		{"task no longer returned", func(f *fakeStore) { f.archivable[7] = []int64{3} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness()
				h.st.archivable[7] = []int64{1, 2, 3}
				h.arch.after = func(id int64) {
					if id == 1 {
						h.st.mu.Lock()
						tc.flip(h.st)
						h.st.mu.Unlock()
					}
				}
				h.w.Start(t.Context())
				h.emit(store.EventIssueStateChanged, closeIssue(7))
				synctest.Wait()
				want := []int64{1}
				if tc.name == "task no longer returned" {
					want = []int64{1, 3}
				}
				if got := h.arch.ids(); !slices.Equal(got, want) {
					t.Fatalf("archived %v, want %v", got, want)
				}
			})
		})
	}
}
