package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/issuearchive"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/scheduler"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// lockedBuffer is a log sink the worker writes from its own goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// withIssueArchiver wires the auto-archive worker (task 143.3) into a task
// harness the way daemon.Run does: a broker subscriber beside the
// scheduler's wake, over the harness's real runner.
func (h *taskHarness) withIssueArchiver(t *testing.T) *lockedBuffer {
	t.Helper()
	log := &lockedBuffer{}
	w := issuearchive.New(issuearchive.Deps{
		Store:    h.store,
		Archiver: h.runner,
		Logger:   slog.New(slog.NewTextHandler(log, nil)),
	})
	h.store.SetEventHook(func(e *store.Event) {
		if scheduler.WakeOn(e) {
			h.sched.Wake()
		}
		w.OnEvent(e)
	})
	w.Start(t.Context())
	return log
}

func (h *taskHarness) closeIssue(t *testing.T, id int64) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", id),
		map[string]any{"reason": "completed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close issue %d: %d %s", id, resp.StatusCode, body)
	}
}

func (h *taskHarness) waitArchived(t *testing.T, id int64) {
	t.Helper()
	wait.Until(t, fmt.Sprintf("task %d archived", id), func() bool {
		task, err := h.store.GetTask(t.Context(), id)
		return err == nil && task.State == store.TaskArchived
	})
}

// archiveEvent is task id's task.state_changed → archived payload.
func (h *taskHarness) archiveEvent(t *testing.T, id int64) map[string]any {
	t.Helper()
	evs, err := h.store.ListEvents(t.Context(), store.EventFilter{
		Types: []string{store.EventTaskStateChanged}, TaskID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["to"] == "archived" {
			return p
		}
	}
	t.Fatalf("task %d has no archive event", id)
	return nil
}

// TestClosingAnIssueArchivesItsSettledTasks is task 143.3 end to end over
// a real runner and real worktrees: an API close archives the issue's done
// task unattended and says so on the event; a dirty worktree is refused,
// logged and left intact; a root that settles after the close is archived
// when it does; and a sync close (issue.updated) counts as a close.
func TestClosingAnIssueArchivesItsSettledTasks(t *testing.T) {
	h := newTaskHarness(t, 0, true)
	log := h.withIssueArchiver(t)
	ctx := t.Context()

	// An API close.
	iss := h.issue(t)
	done := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "work"})
	h.waitForState(t, done.ID, "done")
	h.closeIssue(t, iss.ID)
	h.waitArchived(t, done.ID)
	p := h.archiveEvent(t, done.ID)
	if p["archived_by"] != issuearchive.ArchivedBy || p["issue_id"] != float64(iss.ID) {
		t.Errorf("archive payload = %v, want archived_by %q and issue_id %d", p, issuearchive.ArchivedBy, iss.ID)
	}

	// A dirty worktree is never forced.
	dirtyIss := h.issue(t)
	dirty := h.createTask(t, map[string]any{"issue_id": dirtyIss.ID, "title": "dirty"})
	h.waitForState(t, dirty.ID, "done")
	before, err := h.store.GetTask(ctx, dirty.ID)
	if err != nil || before.WorktreePath == "" {
		t.Fatalf("dirty task = %+v, %v; want a worktree", before, err)
	}
	if err := os.WriteFile(filepath.Join(before.WorktreePath, "untracked.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.closeIssue(t, dirtyIss.ID)
	wait.Until(t, "the dirty refusal logged", func() bool {
		return strings.Contains(log.String(), fmt.Sprintf("task=%d issue=%d reason=worktree_dirty", dirty.ID, dirtyIss.ID))
	})
	got, err := h.store.GetTask(ctx, dirty.ID)
	if err != nil || got.State != store.TaskDone || got.WorktreePath == "" {
		t.Fatalf("dirty task after the close = %+v, %v; want done with its worktree", got, err)
	}
	if _, err := os.Stat(got.WorktreePath); err != nil {
		t.Errorf("dirty task's worktree: %v", err)
	}

	// A root still working at the close is archived once it settles.
	lateIss := h.issue(t)
	late := h.createTask(t, map[string]any{"issue_id": lateIss.ID, "title": "late", "paused": true})
	h.closeIssue(t, lateIss.ID)
	if got, _ := h.store.GetTask(ctx, late.ID); got.State == store.TaskArchived {
		t.Fatal("an unsettled task was archived")
	}
	if resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/resume", late.ID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %s", resp.StatusCode, body)
	}
	h.waitArchived(t, late.ID)

	// A sync close: issue.updated with to=closed.
	remote, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "o/r#9", Repo: "o/r", Number: 9,
		Title: "imported", Author: "someone", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	synced := h.createTask(t, map[string]any{"issue_id": remote.ID, "title": "synced"})
	h.waitForState(t, synced.ID, "done")
	if _, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "o/r#9", Repo: "o/r", Number: 9,
		Title: "imported", Author: "someone", State: issuestate.Closed, CloseReason: issuestate.Completed,
	}, issuestate.Sync); err != nil {
		t.Fatalf("sync close: %v", err)
	}
	h.waitArchived(t, synced.ID)
}
