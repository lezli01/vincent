package taskrun

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// pruneHarness is a store plus a data dir with transcript files on disk —
// pruning is a filesystem operation driven by a DB query, so both halves have
// to be real.
type pruneHarness struct {
	store     *store.Store
	dataDir   string
	projectID int64
	pruner    *TranscriptPruner
	retention int
}

func newPruneHarness(t *testing.T, retentionDays int) *pruneHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h := &pruneHarness{store: st, dataDir: dataDir, projectID: project.ID, retention: retentionDays}
	cfg := config.Default()
	cfg.TranscriptRetentionDays = retentionDays
	h.pruner = NewTranscriptPruner(Deps{
		Store:   st,
		Config:  func() config.Config { return cfg },
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

// task creates a task and, when archivedAgo is positive, marks it archived
// that long ago. It always writes a transcript file so pruning has something
// to delete.
func (h *pruneHarness) task(t *testing.T, title string, archivedAgo time.Duration) int64 {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "adhoc",
		State: store.TaskDone, WorkflowSnapshot: "name: adhoc\nsteps: []\n",
		BranchName: "vincent/" + title,
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if archivedAgo > 0 {
		at := time.Now().Add(-archivedAgo)
		task.State, task.ArchivedAt = store.TaskArchived, &at
		if err := h.store.UpdateTask(t.Context(), task); err != nil {
			t.Fatalf("UpdateTask: %v", err)
		}
	}
	dir := filepath.Join(h.dataDir, "transcripts", strconv.FormatInt(task.ID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0-1.jsonl"), []byte("{\"type\":\"x\"}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return task.ID
}

func (h *pruneHarness) exists(taskID int64) bool {
	_, err := os.Stat(filepath.Join(h.dataDir, "transcripts", strconv.FormatInt(taskID, 10)))
	return err == nil
}

// TestPruneRemovesOnlyExpiredArchivedTranscripts is the T4.3 done-when at a
// shrunk threshold: 7-day retention, tasks aged around it.
func TestPruneRemovesOnlyExpiredArchivedTranscripts(t *testing.T) {
	h := newPruneHarness(t, 7)

	old := h.task(t, "old-archived", 30*24*time.Hour)
	justOver := h.task(t, "just-over", 8*24*time.Hour)
	justUnder := h.task(t, "just-under", 6*24*time.Hour)
	live := h.task(t, "never-archived", 0)

	removed, freed, err := h.pruner.Prune(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (the two past the window)", removed)
	}
	if freed <= 0 {
		t.Errorf("freed = %d, want the bytes of the deleted files", freed)
	}
	if h.exists(old) || h.exists(justOver) {
		t.Error("an expired transcript survived pruning")
	}
	// Retention is measured from archival, so a task archived inside the
	// window keeps its transcript however long it ran before that.
	if !h.exists(justUnder) {
		t.Error("a transcript inside the retention window was pruned")
	}
	// A task that was never archived is never pruned, however old it is:
	// retention applies to archived work only (§17).
	if !h.exists(live) {
		t.Error("a non-archived task's transcript was pruned")
	}
}

// TestPruneIsIdempotent: the ticker runs this every 24 h forever, so a second
// pass over already-pruned tasks must be a no-op rather than an error.
func TestPruneIsIdempotent(t *testing.T) {
	h := newPruneHarness(t, 1)
	h.task(t, "gone", 10*24*time.Hour)

	if removed, _, err := h.pruner.Prune(t.Context(), time.Now()); err != nil || removed != 1 {
		t.Fatalf("first pass: removed %d, err %v; want 1, nil", removed, err)
	}
	removed, freed, err := h.pruner.Prune(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if removed != 0 || freed != 0 {
		t.Errorf("second pass removed %d (%d bytes), want a no-op", removed, freed)
	}
}

// TestPruneDisabledByZeroRetention: an operator who wants everything kept
// sets 0, and nothing is deleted however old it is.
func TestPruneDisabledByZeroRetention(t *testing.T) {
	h := newPruneHarness(t, 0)
	id := h.task(t, "ancient", 365*24*time.Hour)

	removed, _, err := h.pruner.Prune(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d with retention disabled, want 0", removed)
	}
	if !h.exists(id) {
		t.Error("a transcript was pruned with retention disabled")
	}
}

// TestPruneRunHonoursContextCancel: Run is a daemon goroutine, so it must
// return when the daemon's context ends rather than outlive it.
func TestPruneRunHonoursContextCancel(t *testing.T) {
	h := newPruneHarness(t, 1)
	h.task(t, "gone", 10*24*time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		h.pruner.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was canceled")
	}
}

// keyTask creates a task carrying an idempotency key recorded ago in the past,
// and returns the key's identifier.
func (h *pruneHarness) keyTask(t *testing.T, title string, ago time.Duration) string {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "adhoc",
		State: store.TaskQueued, WorkflowSnapshot: "name: adhoc\nsteps: []\n",
		BranchName: "vincent/" + title,
	}
	key := &store.IdempotencyKey{
		Method: "POST", Path: "/v1/tasks", Key: title, RequestSHA: "sha",
		CreatedAt: time.Now().Add(-ago),
	}
	if err := h.store.CreateTaskWithKey(t.Context(), task, nil, key); err != nil {
		t.Fatalf("CreateTaskWithKey: %v", err)
	}
	return title
}

// TestPruneKeysDropsExpiredOnly: the 24-hour pass also expires idempotency
// keys (task 040). The window is fixed rather than read from
// TranscriptRetentionDays, so a harness configured to keep transcripts forever
// still expires keys.
func TestPruneKeysDropsExpiredOnly(t *testing.T) {
	h := newPruneHarness(t, 0) // transcripts kept forever; keys are not
	expired := h.keyTask(t, "expired", 25*time.Hour)
	live := h.keyTask(t, "live", time.Hour)

	n, err := h.pruner.PruneKeys(t.Context(), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("first pass: removed %d, err %v; want 1, nil", n, err)
	}
	if _, err := h.store.GetIdempotencyKey(t.Context(), "POST", "/v1/tasks", expired); err == nil {
		t.Error("a key past the window survived the pass")
	}
	if _, err := h.store.GetIdempotencyKey(t.Context(), "POST", "/v1/tasks", live); err != nil {
		t.Errorf("a key inside the window was pruned: %v", err)
	}
	// The ticker runs this every 24 h forever, so a second pass over
	// already-pruned rows must be a no-op rather than an error.
	if n, err := h.pruner.PruneKeys(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("second pass: removed %d, err %v; want 0, nil", n, err)
	}
}
