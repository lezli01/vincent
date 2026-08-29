package taskrun

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// reclaimHarness is a store, a real git repo and a real data dir. gc is a
// filesystem operation driven by a DB query and adjudicated by git, so all
// three halves have to be real — a fake worktree would not reproduce the
// dirty_unknown case, which is the common one.
type reclaimHarness struct {
	store     *store.Store
	dataDir   string
	repo      string
	projectID int64
	worktrees *worktree.Manager
	rc        *Reclaimer
}

func newReclaimHarness(t *testing.T) *reclaimHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	wt := worktree.NewManager(gitx.New(), dataDir)
	deps := Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: wt,
		DataDir:   dataDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &reclaimHarness{
		store: st, dataDir: dataDir, repo: repo, projectID: project.ID,
		worktrees: wt, rc: NewReclaimer(deps),
	}
}

func (h *reclaimHarness) worktreeRoot() string {
	return filepath.Join(h.dataDir, "worktrees")
}

func (h *reclaimHarness) transcriptRoot() string {
	return filepath.Join(h.dataDir, "transcripts")
}

// task inserts a row and returns it. It creates nothing on disk: the tests
// decide separately what exists where, which is the whole point of a
// claim-based definition.
func (h *reclaimHarness) task(t *testing.T, title string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "adhoc",
		State: store.TaskQueued, WorkflowSnapshot: "name: adhoc\nsteps: []\n",
		BranchName: worktree.BranchName(0, title), BaseBranch: "main",
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// archive marks the row archived. The row stays, which is exactly what makes
// its transcripts §17's business rather than gc's.
func (h *reclaimHarness) archive(t *testing.T, task *store.Task) {
	t.Helper()
	now := time.Now()
	task.State, task.ArchivedAt = store.TaskArchived, &now
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask(archived): %v", err)
	}
}

// realWorktree creates a task with an actual git worktree, claimed by the row
// exactly as ensureWorktree claims it.
func (h *reclaimHarness) realWorktree(t *testing.T, title string) *store.Task {
	t.Helper()
	task := h.task(t, title)
	branch := worktree.BranchName(task.ID, title)
	created, err := h.worktrees.CreateAndClaim(t.Context(), h.repo, task.ID, branch, "main", false,
		func(c worktree.Created) error {
			return h.store.SetTaskProgress(t.Context(), task.ID, nil, &c.Path, nil)
		})
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	task.WorktreePath = created.Path
	task.BranchName = branch
	return task
}

// orphan turns a real worktree into an orphan the way a project delete does:
// the rows go, the directory stays.
func (h *reclaimHarness) orphan(t *testing.T, title string) string {
	t.Helper()
	task := h.realWorktree(t, title)
	if err := h.store.DeleteProjectCascade(t.Context(), h.projectID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}
	// Re-register so later tasks in the same test still have a project.
	project := &store.Project{Name: "proj", Path: h.repo, DefaultBranch: "main"}
	if err := h.store.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("re-CreateProject: %v", err)
	}
	h.projectID = project.ID
	return task.WorktreePath
}

func (h *reclaimHarness) mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "payload"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// find returns the orphan entry for path, or nil.
func find(rep Report, path string) *Orphan {
	for i := range rep.Orphans {
		if rep.Orphans[i].Path == path {
			return &rep.Orphans[i]
		}
	}
	return nil
}

// TestScanClassifiesByClaimNotName is the definition itself: what makes a
// directory an orphan is that no row's worktree_path names it.
func TestScanClassifiesByClaimNotName(t *testing.T) {
	h := newReclaimHarness(t)

	live := h.realWorktree(t, "live")
	// The crash case: `git worktree add` succeeded, worktree_path was never
	// written. The directory is named after an existing task row, so a
	// name-based definition would call it claimed — and the row's next
	// admission would keep failing worktree_path_occupied forever.
	crashed := h.task(t, "crashed")
	crashedDir := h.mkdir(t, filepath.Join(h.worktreeRoot(), strconv.FormatInt(crashed.ID, 10)))
	// A directory whose name is not an id at all.
	stray := h.mkdir(t, filepath.Join(h.worktreeRoot(), "not-an-id"))
	// A file directly under the root: reported, never removed.
	strayFile := filepath.Join(h.worktreeRoot(), "notes.txt")
	if err := os.WriteFile(strayFile, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	rep, err := h.rc.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if find(rep, live.WorktreePath) != nil {
		t.Error("a claimed worktree was reported as an orphan")
	}
	for _, path := range []string{crashedDir, stray, strayFile} {
		o := find(rep, path)
		if o == nil {
			t.Fatalf("%s was not reported as an orphan", path)
		}
		if o.Kind != KindWorktree {
			t.Errorf("%s kind = %q, want %q", path, o.Kind, KindWorktree)
		}
	}
	if got := find(rep, crashedDir); got.TaskID != crashed.ID {
		t.Errorf("crashed orphan task_id = %d, want %d", got.TaskID, crashed.ID)
	}
	if got := find(rep, strayFile); got.Skip != SkipNotADirectory {
		t.Errorf("stray file skip = %q, want %q", got.Skip, SkipNotADirectory)
	}
	if got := find(rep, stray); got.Bytes <= 0 {
		t.Errorf("orphan bytes = %d, want the size of its payload", got.Bytes)
	}
}

// TestScanReportsReverseMismatchWithoutTouchingTheRow covers §18's
// worktree_missing shape: report only, no row modified.
func TestScanReportsReverseMismatchWithoutTouchingTheRow(t *testing.T) {
	h := newReclaimHarness(t)
	task := h.realWorktree(t, "gone")
	if err := os.RemoveAll(task.WorktreePath); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	rep, err := h.rc.Reclaim(t.Context(), true, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].TaskID != task.ID {
		t.Fatalf("mismatches = %+v, want the one task pointing at a missing dir", rep.Mismatches)
	}
	if rep.Mismatches[0].Path != task.WorktreePath {
		t.Errorf("mismatch path = %q, want %q", rep.Mismatches[0].Path, task.WorktreePath)
	}
	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.WorktreePath != task.WorktreePath {
		t.Errorf("worktree_path = %q, want it untouched (%q)", after.WorktreePath, task.WorktreePath)
	}
	if after.State != task.State {
		t.Errorf("state = %q, want it untouched (%q)", after.State, task.State)
	}
}

// TestReclaimRemovesCleanOrphanAndKeepsTheBranch is §10's standing promise:
// vincent never deletes a branch, gc included.
func TestReclaimRemovesCleanOrphanAndKeepsTheBranch(t *testing.T) {
	h := newReclaimHarness(t)
	task := h.realWorktree(t, "clean")
	branch := task.BranchName
	path := task.WorktreePath
	// Drop the claim only — the directory and the repo both stay, which is
	// the one shape where git can still judge dirtiness.
	empty := ""
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &empty, nil); err != nil {
		t.Fatalf("clear claim: %v", err)
	}

	rep, err := h.rc.Reclaim(t.Context(), false, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	o := find(rep, path)
	if o == nil || !o.Removed || o.Skip != "" {
		t.Fatalf("clean orphan not removed: %+v", o)
	}
	if rep.Reclaimed != 1 || rep.ReclaimedBytes <= 0 {
		t.Errorf("reclaimed = %d / %d bytes, want 1 and a positive size",
			rep.Reclaimed, rep.ReclaimedBytes)
	}
	if exists(path) {
		t.Error("the orphan is still on disk")
	}
	if testrepo.Run(t, h.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == "" {
		t.Errorf("branch %s no longer resolves — gc must never delete a branch", branch)
	}
}

// TestReclaimSkipsDirtyOrphanUntilForced: an untracked file is enough, the
// same rule `git worktree remove` itself uses.
func TestReclaimSkipsDirtyOrphanUntilForced(t *testing.T) {
	h := newReclaimHarness(t)
	task := h.realWorktree(t, "dirty")
	testrepo.WriteFile(t, task.WorktreePath, "scratch.txt", "unsaved work\n")
	empty := ""
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &empty, nil); err != nil {
		t.Fatalf("clear claim: %v", err)
	}

	rep, err := h.rc.Reclaim(t.Context(), false, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	o := find(rep, task.WorktreePath)
	if o == nil || o.Skip != worktree.ReasonWorktreeDirty {
		t.Fatalf("dirty orphan skip = %+v, want %q", o, worktree.ReasonWorktreeDirty)
	}
	if !exists(task.WorktreePath) {
		t.Fatal("a dirty orphan was removed without force")
	}

	forced, err := h.rc.Reclaim(t.Context(), true, false)
	if err != nil {
		t.Fatalf("forced Reclaim: %v", err)
	}
	if o := find(forced, task.WorktreePath); o == nil || !o.Removed {
		t.Fatalf("forced run did not remove the dirty orphan: %+v", o)
	}
	if exists(task.WorktreePath) {
		t.Error("the dirty orphan survived --force")
	}
}

// TestReclaimReportsDirtyUnknownWhenTheRepoIsGone is the *common* case: an
// orphan's .git file points into a repository that has been deleted, so
// `git status` fails and nobody can say what is in the directory.
func TestReclaimReportsDirtyUnknownWhenTheRepoIsGone(t *testing.T) {
	h := newReclaimHarness(t)
	path := h.orphan(t, "repo-gone")
	if err := os.RemoveAll(h.repo); err != nil {
		t.Skipf("cannot remove the test repo on this platform: %v", err)
	}

	rep, err := h.rc.Reclaim(t.Context(), false, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	o := find(rep, path)
	if o == nil || o.Skip != worktree.ReasonDirtyUnknown {
		t.Fatalf("skip = %+v, want %q", o, worktree.ReasonDirtyUnknown)
	}
	if !exists(path) {
		t.Fatal("an unjudgeable orphan was removed without force")
	}
	forced, err := h.rc.Reclaim(t.Context(), true, false)
	if err != nil {
		t.Fatalf("forced Reclaim: %v", err)
	}
	if o := find(forced, path); o == nil || !o.Removed {
		t.Fatalf("forced run did not remove it: %+v", o)
	}
	if exists(path) {
		t.Error("the orphan survived --force")
	}
}

// TestReclaimRefusesPathsOutsideTheWorktreeRoot asserts on the filesystem,
// not on a returned error: whatever the DB says, a directory outside
// {data_dir}/worktrees is not gc's to delete.
func TestReclaimRefusesPathsOutsideTheWorktreeRoot(t *testing.T) {
	h := newReclaimHarness(t)
	outside := h.mkdir(t, filepath.Join(t.TempDir(), "precious"))
	traversal := h.mkdir(t, filepath.Join(h.worktreeRoot(), "..", "escaped"))

	for _, path := range []string{outside, traversal} {
		if err := h.worktrees.Reclaim(path); err == nil {
			t.Errorf("Reclaim(%s) returned no error", path)
		}
		if !exists(path) {
			t.Fatalf("%s was deleted — containment failed", path)
		}
	}
	// And through the whole gc path, with a row claiming the outside
	// directory so the scan has every excuse to touch it.
	task := h.task(t, "outside-claim")
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &outside, nil); err != nil {
		t.Fatalf("claim outside path: %v", err)
	}
	if _, err := h.rc.Reclaim(t.Context(), true, false); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if !exists(outside) {
		t.Error("a claimed path outside the root was deleted")
	}
	if !exists(traversal) {
		t.Error("a ..-traversal path outside the root was deleted")
	}
}

// TestDryRunMatchesTheRealReportAndRemovesNothing.
func TestDryRunMatchesTheRealReportAndRemovesNothing(t *testing.T) {
	h := newReclaimHarness(t)
	a := h.mkdir(t, filepath.Join(h.worktreeRoot(), "111"))
	b := h.mkdir(t, filepath.Join(h.transcriptRoot(), "222"))

	dry, err := h.rc.Reclaim(t.Context(), true, true)
	if err != nil {
		t.Fatalf("dry Reclaim: %v", err)
	}
	if len(dry.Orphans) != 2 {
		t.Fatalf("dry run orphans = %d, want 2", len(dry.Orphans))
	}
	if dry.Reclaimed != 0 || dry.ReclaimedBytes != 0 {
		t.Errorf("dry run claims to have reclaimed %d / %d bytes",
			dry.Reclaimed, dry.ReclaimedBytes)
	}
	if !dry.DryRun {
		t.Error("dry run report does not say so")
	}
	for _, path := range []string{a, b} {
		if !exists(path) {
			t.Fatalf("dry run removed %s", path)
		}
	}

	scan, err := h.rc.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scan.Bytes != dry.Bytes || len(scan.Orphans) != len(dry.Orphans) {
		t.Errorf("scan and dry run disagree: %d/%d bytes, %d/%d orphans",
			scan.Bytes, dry.Bytes, len(scan.Orphans), len(dry.Orphans))
	}
	for i := range scan.Orphans {
		if scan.Orphans[i].Path != dry.Orphans[i].Path ||
			scan.Orphans[i].Bytes != dry.Orphans[i].Bytes {
			t.Errorf("entry %d differs: %+v vs %+v", i, scan.Orphans[i], dry.Orphans[i])
		}
	}
}

// TestReclaimTranscriptOfACascadeDeletedRow: DeleteProjectCascade drops the
// row, so §17's pruner — which walks archived *rows* — will never look at
// this directory again. It is gc's, and it needs no dirty check.
func TestReclaimTranscriptOfACascadeDeletedRow(t *testing.T) {
	h := newReclaimHarness(t)
	deleted := h.task(t, "cascade-deleted")
	dir := h.mkdir(t, filepath.Join(h.transcriptRoot(),
		strconv.FormatInt(deleted.ID, 10)))
	if err := h.store.DeleteProjectCascade(t.Context(), h.projectID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}

	// No force: a transcript directory is vincent's own output, so nothing
	// here is ever skipped for dirtiness.
	rep, err := h.rc.Reclaim(t.Context(), false, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	o := find(rep, dir)
	if o == nil || o.Kind != KindTranscript || !o.Removed || o.Skip != "" {
		t.Fatalf("cascade-deleted transcript not reclaimed: %+v", o)
	}
	if exists(dir) {
		t.Error("the cascade-deleted transcript directory is still on disk")
	}
}

// TestArchivedRowKeepsItsTranscript proves the other half of the split: while
// the row exists, its transcripts belong to §17 retention, not to gc.
func TestArchivedRowKeepsItsTranscript(t *testing.T) {
	h := newReclaimHarness(t)
	archived := h.task(t, "archived")
	dir := h.mkdir(t, filepath.Join(h.transcriptRoot(),
		strconv.FormatInt(archived.ID, 10)))
	h.archive(t, archived)

	rep, err := h.rc.Reclaim(t.Context(), true, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if o := find(rep, dir); o != nil {
		t.Fatalf("an archived task's transcript was treated as an orphan: %+v", o)
	}
	if !exists(dir) {
		t.Error("an archived task's transcript directory was removed by gc")
	}
}

// TestScanCannotDeleteAWorktreeBeingCreated is the race the claim-based
// definition creates and the claim lock closes. The claim callback is the
// seam: the scan is launched from inside it, at the exact instant the
// directory exists and no row names it.
func TestScanCannotDeleteAWorktreeBeingCreated(t *testing.T) {
	h := newReclaimHarness(t)
	task := h.task(t, "racing")

	var (
		wg       sync.WaitGroup
		scanRep  Report
		scanErr  error
		inClaim  = make(chan struct{})
		released = make(chan struct{})
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-inClaim
		// This blocks on the claim lock until CreateAndClaim returns; the
		// test's assertion is that it cannot observe the unclaimed window.
		scanRep, scanErr = h.rc.Reclaim(context.Background(), true, false)
		close(released)
	}()

	branch := worktree.BranchName(task.ID, "racing")
	created, err := h.worktrees.CreateAndClaim(t.Context(), h.repo, task.ID, branch, "main", false,
		func(c worktree.Created) error {
			close(inClaim)
			return h.store.SetTaskProgress(t.Context(), task.ID, nil, &c.Path, nil)
		})
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	<-released
	wg.Wait()
	if scanErr != nil {
		t.Fatalf("concurrent Reclaim: %v", scanErr)
	}
	path := created.Path
	if o := find(scanRep, path); o != nil {
		t.Fatalf("the scan saw a worktree being created as an orphan: %+v", o)
	}
	if !exists(path) {
		t.Fatal("a live task's worktree was deleted by a concurrent gc run")
	}
}

// TestReclaimKeepsGoingPastAnUndeletableOrphan is the Windows locked-file
// case — the very failure that produces these orphans in the first place. The
// failure is reported per path and the totals count only what actually went.
func TestReclaimKeepsGoingPastAnUndeletableOrphan(t *testing.T) {
	h := newReclaimHarness(t)
	locked := h.mkdir(t, filepath.Join(h.worktreeRoot(), "111"))
	other := h.mkdir(t, filepath.Join(h.worktreeRoot(), "222"))

	f, err := os.Open(filepath.Join(locked, "payload"))
	if err != nil {
		t.Fatalf("open payload: %v", err)
	}
	defer func() { _ = f.Close() }()

	rep, err := h.rc.Reclaim(t.Context(), true, false)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if o := find(rep, other); o == nil || !o.Removed {
		t.Fatalf("the removable orphan was not reclaimed: %+v", o)
	}
	if exists(other) {
		t.Error("a removable orphan survived a run that hit a locked one")
	}
	// POSIX happily unlinks an open file, so the locked leg only asserts
	// where the OS actually refuses. Where it does refuse — Windows — the
	// report must name the failure and the totals must not count it.
	o := find(rep, locked)
	if o == nil {
		t.Fatal("the locked orphan is not in the report")
	}
	if o.Removed {
		if rep.Reclaimed != 2 {
			t.Errorf("reclaimed = %d, want 2 where the OS allows the removal", rep.Reclaimed)
		}
		return
	}
	if o.Error == "" {
		t.Error("a failed removal is reported without an error message")
	}
	if rep.Reclaimed != 1 {
		t.Errorf("reclaimed = %d, want 1 — the failed removal must not be counted", rep.Reclaimed)
	}
	if rep.ReclaimedBytes != o2Bytes(rep, other) {
		t.Errorf("reclaimed_bytes = %d, want only the orphan that went", rep.ReclaimedBytes)
	}
}

func o2Bytes(rep Report, path string) int64 {
	if o := find(rep, path); o != nil {
		return o.Bytes
	}
	return 0
}

// TestCountMatchesTheScan keeps the cheap /v1/info figure honest against the
// full report it summarizes.
func TestCountMatchesTheScan(t *testing.T) {
	h := newReclaimHarness(t)
	if n, err := h.rc.Count(t.Context()); err != nil || n != 0 {
		t.Fatalf("Count on a clean daemon = %d, %v; want 0, nil", n, err)
	}
	h.mkdir(t, filepath.Join(h.worktreeRoot(), "111"))
	h.mkdir(t, filepath.Join(h.transcriptRoot(), "222"))
	h.realWorktree(t, "claimed")

	n, err := h.rc.Count(t.Context())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	rep, err := h.rc.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if n != len(rep.Orphans) || n != 2 {
		t.Errorf("Count = %d, scan orphans = %d, want 2", n, len(rep.Orphans))
	}
}
