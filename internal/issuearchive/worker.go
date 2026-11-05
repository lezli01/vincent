package issuearchive

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/worktree"
)

// ArchivedBy is the archived_by value an auto-archive writes on its
// task.state_changed → archived event (§13.3, task 143 decision 9).
const ArchivedBy = "issue_closed"

// pageSize bounds one read of the events table.
const pageSize = 200

// Refusal reasons the worker logs. ReasonWorktreeDirty is worktree's own;
// the rest name the error the archive returned.
const (
	reasonLocked     = "task_locked_by_chat"
	reasonUnfinished = "unfinished_descendants"
	reasonError      = "error"
)

// eventTypes are the events that can make an issue archivable. Nothing else
// is read.
var eventTypes = []string{
	store.EventIssueStateChanged, store.EventIssueUpdated,
	store.EventTaskStateChanged,
	store.EventChatState, store.EventChatClosed, store.EventChatArchived, store.EventChatHandedOff,
	store.EventHandoffItemChanged, store.EventHandoffSettled,
}

// Store is the slice of *store.Store the worker reads and writes.
type Store interface {
	GetIssueArchiveCursor(ctx context.Context) (int64, error)
	PutIssueArchiveCursor(ctx context.Context, id int64) error
	ListEvents(ctx context.Context, f store.EventFilter) ([]store.Event, error)
	ArchivableIssueTasks(ctx context.Context, issueID int64) ([]int64, error)
	IssueHandoffClaimed(ctx context.Context, issueID int64) (bool, error)
	GetTask(ctx context.Context, id int64) (*store.Task, error)
	ListHandoffItems(ctx context.Context, handoffID int64) ([]*store.HandoffItem, store.HandoffState, error)
}

// Archiver is taskrun.Runner's archive.
type Archiver interface {
	Archive(ctx context.Context, id int64, opts taskrun.ArchiveOptions) (*store.Task, worktree.BranchOutcome, error)
}

// Deps wires a Worker.
type Deps struct {
	Store    Store
	Archiver Archiver
	// Enabled reads archive_tasks_on_issue_close from the live config. It is
	// asked once per page.
	Enabled func() bool
	Logger  *slog.Logger
}

// Worker is the auto-archive loop. OnEvent is its broker subscription; Start
// runs it until ctx ends.
type Worker struct {
	deps Deps
	wake chan struct{}
}

// New returns a Worker. It does nothing until Start.
func New(deps Deps) *Worker {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Enabled == nil {
		deps.Enabled = func() bool { return true }
	}
	return &Worker{deps: deps, wake: make(chan struct{}, 1)}
}

// OnEvent is the broker subscription. It runs on the store's writing
// goroutine, so it only signals: the one-slot channel coalesces a burst into
// one more drain, and the drain reads the table, not e.
func (w *Worker) OnEvent(e *store.Event) {
	if e == nil || !slices.Contains(eventTypes, e.Type) {
		return
	}
	w.Wake()
}

// Wake asks the loop for one more drain without blocking.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start runs the loop on its own goroutine. It drains once at start, so a
// close the previous daemon committed but never acted on is replayed (§12.4).
func (w *Worker) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *Worker) run(ctx context.Context) {
	w.Drain(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.Drain(ctx)
		}
	}
}

// Drain processes every event after the cursor, a page at a time,
// persisting the cursor after each page. A read or write failure is logged
// and ends the drain; the next wake retries from the persisted cursor.
func (w *Worker) Drain(ctx context.Context) {
	log := w.deps.Logger
	cursor, err := w.deps.Store.GetIssueArchiveCursor(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("issue auto-archive: read cursor", "error", err)
		}
		return
	}
	for ctx.Err() == nil {
		page, err := w.deps.Store.ListEvents(ctx, store.EventFilter{
			AfterID: cursor, Types: eventTypes, Limit: pageSize,
		})
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("issue auto-archive: read events", "after", cursor, "error", err)
			}
			return
		}
		if len(page) == 0 {
			return
		}
		w.processPage(ctx, page)
		last := page[len(page)-1].ID
		if err := w.deps.Store.PutIssueArchiveCursor(ctx, last); err != nil {
			if ctx.Err() == nil {
				log.Warn("issue auto-archive: write cursor", "cursor", last, "error", err)
			}
			return
		}
		cursor = last
		if len(page) < pageSize {
			return
		}
	}
}

// processPage archives what it can for every issue the page touched, once
// per issue, in id order.
func (w *Worker) processPage(ctx context.Context, page []store.Event) {
	// Read per page, as notify reads per event: a hot reload governs the
	// next page with no restart.
	if !w.deps.Enabled() {
		return
	}
	touched := map[int64]bool{}
	for i := range page {
		for _, id := range w.issuesOf(ctx, &page[i]) {
			touched[id] = true
		}
	}
	ids := make([]int64, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		w.archiveIssue(ctx, id)
	}
}

// payload is the union of the keys the worker reads off its event types.
type payload struct {
	ID           int64  `json:"id"`
	From         string `json:"from"`
	To           string `json:"to"`
	State        string `json:"state"`
	IssueID      int64  `json:"issue_id"`
	LinkedTaskID int64  `json:"linked_task_id"`
}

// issuesOf returns the issues e may have made archivable.
func (w *Worker) issuesOf(ctx context.Context, e *store.Event) []int64 {
	var p payload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil
	}
	switch e.Type {
	case store.EventIssueStateChanged, store.EventIssueUpdated:
		if to, ok := store.IssueStateMove(e.Type, p.From, p.To); ok && to == string(issuestate.Closed) {
			return []int64{p.ID}
		}
	case store.EventTaskStateChanged:
		// archived is settled too, but archiving never makes anything
		// else archivable — and the worker's own archives would wake it.
		to := taskstate.State(p.To)
		if p.IssueID != 0 && taskstate.Settled(to) && to != taskstate.Archived {
			return []int64{p.IssueID}
		}
	case store.EventChatState, store.EventChatClosed, store.EventChatArchived, store.EventChatHandedOff:
		if !chatstate.Terminal(chatstate.State(p.State)) {
			return nil
		}
		if p.IssueID != 0 {
			return []int64{p.IssueID}
		}
		if p.LinkedTaskID != 0 {
			return w.issueOfTask(ctx, p.LinkedTaskID)
		}
	case store.EventHandoffItemChanged:
		if p.IssueID != 0 {
			return []int64{p.IssueID}
		}
	case store.EventHandoffSettled:
		items, _, err := w.deps.Store.ListHandoffItems(ctx, p.ID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				w.deps.Logger.Warn("issue auto-archive: read handoff items", "handoff", p.ID, "error", err)
			}
			return nil
		}
		out := make([]int64, 0, len(items))
		for _, it := range items {
			out = append(out, it.IssueID)
		}
		return out
	}
	return nil
}

// issueOfTask is the issue a linked chat's task belongs to, if any.
func (w *Worker) issueOfTask(ctx context.Context, taskID int64) []int64 {
	t, err := w.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			w.deps.Logger.Warn("issue auto-archive: read task", "task", taskID, "error", err)
		}
		return nil
	}
	if t.IssueID == nil {
		return nil
	}
	return []int64{*t.IssueID}
}

// archiveIssue archives the issue's archivable tasks one at a time, in the
// order the query returns them. A refusal is logged and the next task is
// tried; nothing is ever forced.
func (w *Worker) archiveIssue(ctx context.Context, issueID int64) {
	log := w.deps.Logger
	claimed, err := w.deps.Store.IssueHandoffClaimed(ctx, issueID)
	if err != nil {
		log.Warn("issue auto-archive: read handoff claim", "issue", issueID, "error", err)
		return
	}
	if claimed {
		// Decision 10: the release, return, delivery or settle that ends
		// the claim is a handoff event, which wakes this issue again.
		return
	}
	ids, err := w.deps.Store.ArchivableIssueTasks(ctx, issueID)
	if err != nil {
		log.Warn("issue auto-archive: read archivable tasks", "issue", issueID, "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		_, _, err := w.deps.Archiver.Archive(ctx, id, taskrun.ArchiveOptions{
			Unattended:   true,
			EventPayload: map[string]any{"archived_by": ArchivedBy, "issue_id": issueID},
		})
		if err == nil {
			log.Info("issue auto-archive: archived task", "task", id, "issue", issueID)
			continue
		}
		var invalid *taskrun.InvalidActionError
		if errors.As(err, &invalid) || errors.Is(err, store.ErrNotFound) {
			// Already archived — a replayed page, or a human got there
			// first — or deleted since the query.
			continue
		}
		if ctx.Err() != nil {
			return
		}
		log.Warn("issue auto-archive: task not archived",
			"task", id, "issue", issueID, "reason", refusalReason(err), "error", err)
	}
}

// refusalReason names why an archive refused, for daemon.log.
func refusalReason(err error) string {
	if r := worktree.ReasonOf(err); r != "" {
		return r
	}
	if _, ok := store.AsTaskLocked(err); ok {
		return reasonLocked
	}
	var unfinished *taskrun.UnfinishedDescendantsError
	if errors.As(err, &unfinished) {
		return reasonUnfinished
	}
	return reasonError
}
