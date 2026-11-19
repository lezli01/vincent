package api

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/finishedarchive"
	"github.com/lezli01/vincent/internal/store"
)

// TestFinishedTasksArchiveAfterTheirAge is task 155 over a real runner and
// real worktrees: a sweep archives a finished task with no issue and an open
// issue's finished task, unattended, removing the worktree and saying so on
// the event; a dirty worktree is refused, logged and left intact; and a task
// still running is not touched. The sweep is called directly with a
// one-nanosecond age, so the test never waits on the timer.
func TestFinishedTasksArchiveAfterTheirAge(t *testing.T) {
	h := newTaskHarness(t, 0, true)
	ctx := t.Context()
	log := &lockedBuffer{}
	w := finishedarchive.New(finishedarchive.Deps{
		Store:    h.store,
		Archiver: h.runner,
		After:    func() time.Duration { return time.Nanosecond },
		Logger:   slog.New(slog.NewTextHandler(log, nil)),
	})

	plain := h.createTask(t, map[string]any{"title": "plain"})
	h.waitForState(t, plain.ID, "done")
	iss := h.issue(t)
	onIssue := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "on an open issue"})
	h.waitForState(t, onIssue.ID, "done")
	dirty := h.createTask(t, map[string]any{"title": "dirty"})
	h.waitForState(t, dirty.ID, "done")
	paused := h.createTask(t, map[string]any{"title": "paused", "paused": true})

	before, err := h.store.GetTask(ctx, dirty.ID)
	if err != nil || before.WorktreePath == "" {
		t.Fatalf("dirty task = %+v, %v; want a worktree", before, err)
	}
	if err := os.WriteFile(filepath.Join(before.WorktreePath, "untracked.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plainBefore, err := h.store.GetTask(ctx, plain.ID)
	if err != nil || plainBefore.WorktreePath == "" {
		t.Fatalf("plain task = %+v, %v; want a worktree", plainBefore, err)
	}

	// finished_at has nanosecond resolution; make sure the clock has moved.
	time.Sleep(time.Millisecond)
	w.Sweep(ctx)

	for _, id := range []int64{plain.ID, onIssue.ID} {
		got, err := h.store.GetTask(ctx, id)
		if err != nil || got.State != store.TaskArchived {
			t.Fatalf("task %d after the sweep = %+v, %v; want archived", id, got, err)
		}
		if p := h.archiveEvent(t, id); p["archived_by"] != finishedarchive.ArchivedBy {
			t.Errorf("task %d archive payload = %v, want archived_by %q", id, p, finishedarchive.ArchivedBy)
		}
	}
	if _, err := os.Stat(plainBefore.WorktreePath); !os.IsNotExist(err) {
		t.Errorf("archived task's worktree still there: %v", err)
	}

	got, err := h.store.GetTask(ctx, dirty.ID)
	if err != nil || got.State != store.TaskDone || got.WorktreePath == "" {
		t.Fatalf("dirty task after the sweep = %+v, %v; want done with its worktree", got, err)
	}
	if _, err := os.Stat(got.WorktreePath); err != nil {
		t.Errorf("dirty task's worktree: %v", err)
	}
	if want := fmt.Sprintf("task=%d reason=worktree_dirty", dirty.ID); !strings.Contains(log.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, log)
	}

	if got, _ := h.store.GetTask(ctx, paused.ID); got.State == store.TaskArchived {
		t.Error("an unfinished task was archived")
	}
}
