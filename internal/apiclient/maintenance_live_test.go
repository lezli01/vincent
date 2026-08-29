package apiclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// gcHarness wires a client to the real maintenance handlers over a real data
// dir — the endpoints' whole subject is what is on disk, so a fake would
// prove nothing about the wire types.
type gcHarness struct {
	client  *apiclient.Client
	dataDir string
	st      *store.Store
}

func newGCHarness(t *testing.T) *gcHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worktrees := worktree.NewManager(gitx.New(), dataDir)
	reclaimer := taskrun.NewReclaimer(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: worktrees,
		DataDir:   dataDir,
		Logger:    logger,
	})
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      logger,
		Store:       st,
		Worktrees:   worktrees,
		Reclaimer:   reclaimer,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &gcHarness{client: apiclient.New(ts.URL, testToken), dataDir: dataDir, st: st}
}

// plant creates a directory under a data root with a known payload size.
func (h *gcHarness) plant(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(h.dataDir, root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return dir
}

func TestOrphansAndGCOverTheWire(t *testing.T) {
	h := newGCHarness(t)
	wt := h.plant(t, "worktrees", "999999")
	ts := h.plant(t, "transcripts", "999998")

	rep, err := h.client.Orphans(t.Context())
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(rep.Orphans) != 2 {
		t.Fatalf("orphans = %d, want 2 (%+v)", len(rep.Orphans), rep.Orphans)
	}
	if rep.Bytes <= 0 {
		t.Errorf("bytes = %d, want the planted payloads' size", rep.Bytes)
	}
	kinds := map[string]string{}
	for _, o := range rep.Orphans {
		kinds[o.Path] = o.Kind
		if o.TaskID == nil {
			t.Errorf("orphan %s has no task_id, though its name is an id", o.Path)
		}
		if o.Removed {
			t.Errorf("GET /orphans reports %s as removed", o.Path)
		}
	}
	if kinds[wt] != apiclient.OrphanWorktree || kinds[ts] != apiclient.OrphanTranscript {
		t.Errorf("kinds = %+v, want worktree and transcript", kinds)
	}

	// A dry run leaves everything where it is and says so.
	dry, err := h.client.GC(t.Context(), false, true)
	if err != nil {
		t.Fatalf("GC dry run: %v", err)
	}
	if !dry.DryRun || dry.Reclaimed != 0 {
		t.Errorf("dry run report = %+v, want dry_run true and nothing reclaimed", dry)
	}
	for _, path := range []string{wt, ts} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run removed %s", path)
		}
	}

	// The real thing. --force, because the planted worktree has no repo
	// behind it and git therefore cannot judge it — dirty_unknown, the
	// common case for a real orphan.
	got, err := h.client.GC(t.Context(), true, false)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if got.Reclaimed != 2 || got.ReclaimedBytes <= 0 {
		t.Fatalf("reclaimed = %d / %d bytes, want 2 and a positive size",
			got.Reclaimed, got.ReclaimedBytes)
	}
	for _, path := range []string{wt, ts} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s survived gc", path)
		}
	}
}

// TestGCWithoutForceReportsTheSkipReason: git cannot judge a directory whose
// repo never existed, so the default run declines and names why.
func TestGCWithoutForceReportsTheSkipReason(t *testing.T) {
	h := newGCHarness(t)
	wt := h.plant(t, "worktrees", "424242")

	rep, err := h.client.GC(t.Context(), false, false)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if rep.Reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 without force", rep.Reclaimed)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].SkipReason != worktree.ReasonDirtyUnknown {
		t.Fatalf("orphans = %+v, want one skipped %q", rep.Orphans, worktree.ReasonDirtyUnknown)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("a skipped orphan was removed: %v", err)
	}
}

// TestInfoCarriesTheOrphanCount: the count rides /v1/info (not /v1/health,
// which is unauthenticated and deliberately {status, version}) and is
// computed per request, so it drops the moment gc runs.
func TestInfoCarriesTheOrphanCount(t *testing.T) {
	h := newGCHarness(t)
	info, err := h.client.Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Orphans != 0 {
		t.Fatalf("orphans = %d on a clean daemon, want 0", info.Orphans)
	}
	h.plant(t, "worktrees", "777")
	if info, err = h.client.Info(t.Context()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Orphans != 1 {
		t.Fatalf("orphans = %d after planting one, want 1", info.Orphans)
	}
	if _, err := h.client.GC(t.Context(), true, false); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if info, err = h.client.Info(t.Context()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Orphans != 0 {
		t.Errorf("orphans = %d after gc, want 0 — the count must not be cached", info.Orphans)
	}
}

// TestClaimedDirectoryIsNeverAnOrphanOverTheWire: the claim, not the name, is
// what protects a live task's worktree.
func TestClaimedDirectoryIsNeverAnOrphanOverTheWire(t *testing.T) {
	h := newGCHarness(t)
	ctx := context.Background()
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := h.st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "live", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskRunning,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	dir := h.plant(t, "worktrees", strconv.FormatInt(task.ID, 10))
	if err := h.st.SetTaskProgress(ctx, task.ID, nil, &dir, nil); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}

	rep, err := h.client.GC(t.Context(), true, false)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Fatalf("orphans = %+v, want none — the directory is claimed", rep.Orphans)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("a claimed worktree was deleted by gc: %v", err)
	}
}
