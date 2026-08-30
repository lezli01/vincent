package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// doctorHarness is the API plus the two things doctor reports on that only
// the daemon owns: the store and the worktree manager. The data dir is shared
// between them, exactly as internal/daemon wires it, because the whole point
// of the orphan scan is that {data_dir}/worktrees and the task table describe
// the same thing.
type doctorHarness struct {
	*projectHarness
	dirs config.Dirs
	git  *gitx.Git
}

func newDoctorHarness(t *testing.T) *doctorHarness {
	t.Helper()
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(dirs.Data, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, dirs.Data)
	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The same reclaimer the daemon wires: doctor reports gc's scan and `--fix`
	// runs gc's removal, so a harness that stubbed either would be testing a
	// second classifier that does not exist.
	reclaimer := taskrun.NewReclaimer(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: wt,
		DataDir:   dirs.Data,
		Logger:    logger,
	})
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:12345",
		Dirs:        dirs,
		LogPath:     filepath.Join(dirs.Data, "logs", "daemon.log"),
		TailLog:     func(string, int) ([]string, error) { return []string{"a log line"}, nil },
		RequestStop: func() {},
		Logger:      logger,
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Reclaimer:   reclaimer,
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &doctorHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		dirs:           dirs,
		git:            git,
	}
}

func (h *doctorHarness) report(t *testing.T) doctor.Report {
	t.Helper()
	resp, body := doRequest(t, h.ts, http.MethodGet, "/v1/doctor", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/doctor: %d %s", resp.StatusCode, body)
	}
	var rep doctor.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("doctor body: %v (%s)", err, body)
	}
	return rep
}

func (h *doctorHarness) fix(t *testing.T, force bool) doctor.FixResult {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, "/v1/doctor/fix", map[string]bool{"force": force})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/doctor/fix: %d %s", resp.StatusCode, body)
	}
	var res doctor.FixResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("fix body: %v (%s)", err, body)
	}
	return res
}

// seedProject registers a repo and returns its id and path.
func (h *doctorHarness) seedProject(t *testing.T) (int64, string) {
	t.Helper()
	repo := testrepo.Init(t, "main")
	resp, body := h.doJSON(t, http.MethodPost, "/v1/projects", map[string]any{"path": repo})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register project: %d %s", resp.StatusCode, body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("project body: %v", err)
	}
	return p.ID, repo
}

// seedTask inserts a task directly, so a state can be staged without driving
// a whole run through the engine.
func (h *doctorHarness) seedTask(t *testing.T, projectID int64, state store.TaskState, title string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID:    projectID,
		Title:        title,
		WorkflowName: "ad-hoc",
		BaseBranch:   "main",
		BranchName:   "vincent/" + title,
		State:        state,
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// TestDoctorReportsEveryGroup is the §13.2 contract: one request, the whole
// picture. A group that silently went missing would send the user back to the
// five surfaces this command replaced.
func TestDoctorReportsEveryGroup(t *testing.T) {
	h := newDoctorHarness(t)
	rep := h.report(t)

	if rep.Paths.ConfigDir != h.dirs.Config || rep.Paths.DataDir != h.dirs.Data {
		t.Errorf("paths = %+v, want the harness dirs", rep.Paths)
	}
	if rep.Daemon.Status != doctor.StatusRunning || rep.Daemon.PID == 0 {
		t.Errorf("daemon = %+v; the handler runs inside the daemon", rep.Daemon)
	}
	if rep.Daemon.Port != 12345 {
		t.Errorf("daemon port = %d, want the listen addr's port", rep.Daemon.Port)
	}
	if rep.Daemon.UptimeSeconds < 1 {
		t.Errorf("uptime = %d, want the ~60s the harness staged", rep.Daemon.UptimeSeconds)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Name != "claude" || !rep.Agents[0].Available {
		t.Errorf("agents = %+v", rep.Agents)
	}
	if rep.Storage.DiskTotalBytes == 0 {
		t.Error("disk row is empty")
	}
	if !rep.Storage.OrphansKnown {
		t.Error("OrphansKnown is false with a store wired in")
	}
	if !rep.Tasks.Known || !rep.Database.Known {
		t.Errorf("database/tasks unknown with a store wired in: %+v %+v", rep.Database, rep.Tasks)
	}
	if rep.Log.Path == "" {
		t.Error("log path is empty")
	}
	if !rep.Healthy() {
		t.Errorf("a fresh installation is unhealthy: %v", rep.Problems)
	}
}

// TestDoctorDatabaseRows pins the three database facts, including the one
// comparison that needs the binary: an applied version ahead of what this
// build embeds means the file was written by a newer vincent.
func TestDoctorDatabaseRows(t *testing.T) {
	h := newDoctorHarness(t)
	rep := h.report(t)
	if rep.Database.Path != filepath.Join(h.dirs.Data, "vincent.db") {
		t.Errorf("Database.Path = %q", rep.Database.Path)
	}
	if rep.Database.SizeBytes == 0 {
		t.Error("Database.SizeBytes = 0 for an open database")
	}
	if rep.Database.IntegrityCheck != "ok" {
		t.Errorf("integrity_check = %q, want ok on a fresh store", rep.Database.IntegrityCheck)
	}
	newest := store.NewestMigration()
	if newest == 0 {
		t.Fatal("store embeds no migrations")
	}
	if rep.Database.NewestMigration != newest || rep.Database.SchemaVersion != newest {
		t.Errorf("schema version %d / newest %d, want both %d",
			rep.Database.SchemaVersion, rep.Database.NewestMigration, newest)
	}
}

// TestDoctorTaskCountsMatchSeededRows: "12 blocked" has to be visible without
// opening the board, which is only useful if the number is right.
func TestDoctorTaskCountsMatchSeededRows(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, _ := h.seedProject(t)
	h.seedTask(t, projectID, store.TaskBlocked, "one")
	h.seedTask(t, projectID, store.TaskBlocked, "two")
	h.seedTask(t, projectID, store.TaskQueued, "three")

	rep := h.report(t)
	if rep.Tasks.Counts["blocked"] != 2 || rep.Tasks.Counts["queued"] != 1 || rep.Tasks.Total != 3 {
		t.Fatalf("counts = %v (total %d)", rep.Tasks.Counts, rep.Tasks.Total)
	}
	if _, ok := rep.Tasks.Counts["done"]; !ok {
		t.Error("the tally omits states with no rows; the whole §6 vocabulary is expected")
	}
	// Blocked tasks are information, not a defect (decision 7).
	if !rep.Healthy() {
		t.Errorf("blocked tasks set the exit code: %v", rep.Problems)
	}
}

// A queued task holding a step run the database still calls `running` is the
// §12.4 contradiction: it cannot be admitted and it will never move on its
// own, so doctor names it rather than letting it sit invisible on the board
// (issue #142).
func TestDoctorReportsUnreconciledTasks(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, _ := h.seedProject(t)
	stuck := h.seedTask(t, projectID, store.TaskQueued, "stuck")
	h.seedTask(t, projectID, store.TaskRunning, "healthy")
	run := &store.StepRun{
		TaskID: stuck.ID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := h.store.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	rep := h.report(t)
	if len(rep.Tasks.Unreconciled) != 1 {
		t.Fatalf("unreconciled = %+v, want the one queued task", rep.Tasks.Unreconciled)
	}
	got := rep.Tasks.Unreconciled[0]
	if got.TaskID != stuck.ID || got.State != string(store.TaskQueued) || got.OpenStepRuns != 1 {
		t.Errorf("unreconciled = %+v, want task %d queued with 1 open run", got, stuck.ID)
	}
	if rep.Healthy() {
		t.Error("an unreconciled task did not set the exit code")
	}
	var found bool
	for _, p := range rep.Problems {
		if p.Group == doctor.GroupTasks {
			found = true
		}
	}
	if !found {
		t.Errorf("no tasks-group problem: %+v", rep.Problems)
	}

	// A `running` task with an open run is normal operation, not a defect.
	if _, err := h.store.TerminalizeOpenStepRuns(
		t.Context(), stuck.ID, store.StepInterrupted, "interrupted"); err != nil {
		t.Fatalf("TerminalizeOpenStepRuns: %v", err)
	}
	if rep := h.report(t); !rep.Healthy() {
		t.Errorf("still unhealthy once reconciled: %v", rep.Problems)
	}
}

func TestDoctorRequiresAuthAndTheRightMethod(t *testing.T) {
	h := newDoctorHarness(t)

	resp, body := doRequest(t, h.ts, http.MethodGet, "/v1/doctor", "")
	wantError(t, resp, body, http.StatusUnauthorized, CodeUnauthorized)

	// The read-only report and the repair are separate methods on purpose
	// (decision 5): a GET that deletes directories would be wrong in a table
	// clients read as a contract.
	resp, body = doRequest(t, h.ts, http.MethodPost, "/v1/doctor", testToken)
	wantError(t, resp, body, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
	if allow := resp.Header.Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want GET", allow)
	}

	resp, body = doRequest(t, h.ts, http.MethodGet, "/v1/doctor/fix", testToken)
	wantError(t, resp, body, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
}

// makeOrphan creates a worktree that no task row claims — the shape §10
// defines, and the residue of a `DELETE /v1/projects/{id}` whose removal
// failed partway. No task is created at all, so nothing can claim it.
func (h *doctorHarness) makeOrphan(t *testing.T, projectID int64, repo string, id int64) string {
	t.Helper()
	branch := "vincent/orphan-" + strconv.FormatInt(id, 10)
	path, err := h.wt.Create(t.Context(), repo, worktree.TaskOwner(id), branch, "main", false)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	_ = projectID
	return path
}

// claimWorktree records a task's worktree_path, which is what makes the
// directory that task's rather than an orphan. The engine writes this on
// admission; a test that skips it is staging the crash window, not a live
// worktree.
func (h *doctorHarness) claimWorktree(t *testing.T, task *store.Task, path string) {
	t.Helper()
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &path, nil); err != nil {
		t.Fatalf("claim worktree: %v", err)
	}
}

// TestDoctorFixRemovesACleanOrphan is the whole reason --fix exists: nothing
// in vincent reclaims an orphaned worktree today.
func TestDoctorFixRemovesACleanOrphan(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, repo := h.seedProject(t)
	// Id 4242 has no task row at all, which is what a
	// `DELETE /v1/projects/{id}?force` leaves behind.
	path := h.makeOrphan(t, projectID, repo, 4242)

	rep := h.report(t)
	if len(rep.Storage.Orphans) != 1 || rep.Storage.Orphans[0].TaskID != 4242 {
		t.Fatalf("orphans = %+v", rep.Storage.Orphans)
	}
	if rep.Storage.Orphans[0].Skip != "" {
		t.Fatalf("a freshly created worktree is not eligible: %+v", rep.Storage.Orphans[0])
	}
	if rep.Healthy() {
		t.Error("orphans present but the report is healthy")
	}

	res := h.fix(t, false)
	if !hasFixAction(res.Actions, doctor.ActionRemoveWorktree, doctor.FixDone) {
		t.Fatalf("actions = %+v", res.Actions)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("orphan directory survives --fix: %v", err)
	}
	if len(res.Report.Storage.Orphans) != 0 {
		t.Errorf("the fresh report still lists orphans: %+v", res.Report.Storage.Orphans)
	}
	if !res.Report.Healthy() {
		t.Errorf("still unhealthy after the fix: %v", res.Report.Problems)
	}
}

// TestDoctorFixRefusesADirtyOrphanWithoutForce mirrors the archive path: work
// nobody has looked at is not deleted on a whim.
func TestDoctorFixRefusesADirtyOrphanWithoutForce(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, repo := h.seedProject(t)
	path := h.makeOrphan(t, projectID, repo, 99)
	if err := os.WriteFile(filepath.Join(path, "uncommitted.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}

	rep := h.report(t)
	if len(rep.Storage.Orphans) != 1 || rep.Storage.Orphans[0].Skip != worktree.ReasonWorktreeDirty {
		t.Fatalf("orphans = %+v, want one skipped as dirty", rep.Storage.Orphans)
	}

	res := h.fix(t, false)
	if !hasFixAction(res.Actions, doctor.ActionRemoveWorktree, doctor.FixSkipped) {
		t.Fatalf("actions = %+v, want the removal skipped", res.Actions)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a dirty orphan was removed without force: %v", err)
	}

	res = h.fix(t, true)
	if !hasFixAction(res.Actions, doctor.ActionRemoveWorktree, doctor.FixDone) {
		t.Fatalf("actions with force = %+v", res.Actions)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("--force did not remove the dirty orphan: %v", err)
	}
}

// TestDoctorFixSkipsVacuumWhileWorkIsInFlight is decision 4. A full VACUUM
// rewrites the file under an exclusive lock, and imposing that on a step
// mid-write is worse than declining to compact.
func TestDoctorFixSkipsVacuumWhileWorkIsInFlight(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, _ := h.seedProject(t)

	res := h.fix(t, false)
	if !hasFixAction(res.Actions, doctor.ActionCompactDatabase, doctor.FixDone) {
		t.Fatalf("idle compaction = %+v, want done", res.Actions)
	}

	h.seedTask(t, projectID, store.TaskRunning, "in-flight")
	res = h.fix(t, false)
	if !hasFixAction(res.Actions, doctor.ActionCompactDatabase, doctor.FixSkipped) {
		t.Fatalf("compaction with a running task = %+v, want skipped", res.Actions)
	}
	for _, a := range res.Actions {
		if a.Action == doctor.ActionCompactDatabase && a.Detail == "" {
			t.Error("a skipped compaction gave no reason; the user has to be told")
		}
	}

	// awaiting_input holds a slot too — its agent process is alive, idle on
	// its stdin (§6, §11) — so it must block a rewrite for the same reason.
	h.seedTask(t, projectID, store.TaskAwaitingInput, "waiting")
	res = h.fix(t, false)
	if !hasFixAction(res.Actions, doctor.ActionCompactDatabase, doctor.FixSkipped) {
		t.Fatalf("compaction with awaiting_input = %+v, want skipped", res.Actions)
	}
}

// TestDoctorFixNeverRemovesAStrayFile pins --fix's blast radius, which task
// 005 draws around what vincent *creates* rather than around what it names:
// only directories, only inside a data root. A stray file there is somebody
// else's — an editor swap file, a note — and no `--force` clears that.
//
// A directory with an unfamiliar name is deliberately not protected the same
// way. It is inside vincent's own root and nothing claims it, so it is an
// orphan like any other; the name-based reading was rejected because the
// crash-window orphan is named after a live task.
func TestDoctorFixNeverRemovesAStrayFile(t *testing.T) {
	h := newDoctorHarness(t)
	root := filepath.Join(h.dirs.Data, doctor.WorktreesDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stray := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(stray, []byte("mine\n"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	res := h.fix(t, true) // even with force
	if !hasFixAction(res.Actions, doctor.ActionRemoveWorktree, doctor.FixSkipped) {
		t.Fatalf("actions = %+v, want the stray file skipped", res.Actions)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a file vincent did not create was removed: %v", err)
	}
}

// TestDoctorLiveWorktreeIsNotAnOrphan is the leg that would delete work in
// progress if it regressed. What makes the directory live is the row's
// worktree_path claim, not its name (§10, task 005).
func TestDoctorLiveWorktreeIsNotAnOrphan(t *testing.T) {
	h := newDoctorHarness(t)
	projectID, repo := h.seedProject(t)
	task := h.seedTask(t, projectID, store.TaskQueued, "live")
	path, err := h.wt.Create(t.Context(), repo, worktree.TaskOwner(task.ID), "vincent/live-one", "main", false)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	h.claimWorktree(t, task, path)

	rep := h.report(t)
	if rep.Storage.WorktreeCount != 1 {
		t.Errorf("WorktreeCount = %d, want 1", rep.Storage.WorktreeCount)
	}
	if len(rep.Storage.Orphans) != 0 {
		t.Fatalf("a live task's worktree was called an orphan: %+v", rep.Storage.Orphans)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree missing before the fix: %v", err)
	}
	h.fix(t, true)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("--fix --force removed a live task's worktree: %v", err)
	}
}

// TestWorktreesDirNamesAgree keeps the two spellings of {data_dir}/worktrees
// pinned to each other: the scan finds what --fix deletes, and they come from
// different packages.
func TestWorktreesDirNamesAgree(t *testing.T) {
	dataDir := t.TempDir()
	m := worktree.NewManager(gitx.New(), dataDir)
	if want := filepath.Join(dataDir, doctor.WorktreesDirName); m.Root() != want {
		t.Fatalf("worktree.Manager root = %q, doctor scans %q", m.Root(), want)
	}
}

func hasFixAction(actions []doctor.FixAction, name, status string) bool {
	for _, a := range actions {
		if a.Action == name && a.Status == status {
			return true
		}
	}
	return false
}
