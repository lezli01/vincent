package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// e2eTask is the slice of a task row this file asserts on.
type e2eTask struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	CostUSD      *float64 `json:"cost_usd"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
}

// TestBackupRestoreRoundTripE2E is task 030's acceptance, driven through the
// real binary: a backup taken **while a task is running**, restored into a
// clean pair of directories, and a daemon started on the result that reports
// the same tasks, step runs and costs.
//
// Two daemon generations before the backup, because the fake agent's
// behaviour comes from the environment its daemon was started with: the first
// runs a task to completion so there are token counts to compare, the second
// hangs so a task is genuinely mid-step when the archive is written.
func TestBackupRestoreRoundTripE2E(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	cfg := fmt.Sprintf("listen: \"127.0.0.1:0\"\nagents:\n  claude:\n    path: %q\n", fake)
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// A global workflow, so the config half of the archive carries both the
	// file and the tree beneath it.
	if err := os.MkdirAll(filepath.Join(cfgDir, "workflows"), 0o700); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "workflows", "keepme.yaml"),
		[]byte("name: keepme\ndescription: a global workflow\nsteps:\n  - id: noop\n    type: command\n    run: \"exit 0\"\n"),
		0o600); err != nil {
		t.Fatalf("write global workflow: %v", err)
	}
	repo := testrepo.Init(t, "main")

	// Generation one: a task that finishes, with usage the result event
	// reports, so the restored database has costs to compare.
	first := startDaemonProcess(t, dataDir, cfgDir, "big-usage")
	c1 := waitDaemonAPI(t, dataDir)
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	doneID := addTask(t, dataDir, cfgDir, "finished before the backup")
	waitTaskState(t, dataDir, cfgDir, doneID, "done")
	c1.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, first)

	// Generation two: a task that is still running when the archive is taken.
	second := startDaemonProcess(t, dataDir, cfgDir, "hang")
	c2 := waitDaemonAPI(t, dataDir)
	runningID := addTask(t, dataDir, cfgDir, "still running at backup time")
	waitJournaledPID(t, c2, runningID)

	archive := filepath.Join(t.TempDir(), "vincent-backup.tar.gz")
	out, code := runVincent(t, dataDir, cfgDir, "daemon", "backup", archive)
	if code != 0 {
		t.Fatalf("daemon backup: code %d, out %q", code, out)
	}
	if !strings.Contains(out, archive) || !strings.Contains(out, "database") {
		t.Errorf("daemon backup printed %q, want the path and the sizes", out)
	}
	// The command refuses to overwrite what it just wrote.
	out, code = runVincent(t, dataDir, cfgDir, "daemon", "backup", archive)
	if code != 1 || !strings.Contains(out, "already exists") {
		t.Errorf("second backup to the same path: code %d, out %q; want 1 and a refusal", code, out)
	}

	// Restore refuses while the daemon it would overwrite is up.
	out, code = runVincent(t, dataDir, cfgDir, "daemon", "restore", archive)
	if code != 1 || !strings.Contains(out, "daemon is running") {
		t.Fatalf("restore against a live daemon: code %d, out %q; want 1", code, out)
	}

	c2.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, second)

	// Into a clean installation, the way a user moving machines would.
	newData, newCfg := t.TempDir(), t.TempDir()
	out, code = runVincent(t, newData, newCfg, "daemon", "restore", archive)
	if code != 0 {
		t.Fatalf("daemon restore: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "restored") {
		t.Errorf("daemon restore printed %q", out)
	}
	for _, want := range []string{
		filepath.Join(newData, backup.DatabaseEntry),
		filepath.Join(newData, backup.TranscriptsPrefix, strconv.FormatInt(doneID, 10)),
		filepath.Join(newCfg, config.FileName),
		filepath.Join(newCfg, "workflows", "keepme.yaml"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("restore did not produce %s: %v", want, err)
		}
	}
	// The running-identity files are not in a backup and must not appear.
	for _, gone := range []string{"token", "daemon.json", backup.DatabaseEntry + "-wal"} {
		if _, err := os.Stat(filepath.Join(newData, gone)); err == nil {
			t.Errorf("restore produced %s, which a backup does not carry", gone)
		}
	}

	// Generation three, on the restored installation.
	third := startDaemonProcess(t, newData, newCfg, "success")
	c3 := waitDaemonAPI(t, newData)
	t.Cleanup(func() { _ = third.Process.Kill() })

	var restored []e2eTask
	c3.get(t, "/v1/tasks?archived=all", &restored)
	byTitle := map[string]e2eTask{}
	for _, task := range restored {
		byTitle[task.Title] = task
	}
	if len(byTitle) != 2 {
		t.Fatalf("restored tasks = %+v, want both", restored)
	}
	finished, ok := byTitle["finished before the backup"]
	if !ok {
		t.Fatalf("the finished task did not survive the round trip: %+v", restored)
	}
	if finished.State != "done" {
		t.Errorf("restored state = %q, want done", finished.State)
	}
	if finished.InputTokens == 0 || finished.OutputTokens == 0 {
		t.Errorf("restored task = %+v, want the token counts the original recorded", finished)
	}

	var steps []struct {
		StepIndex int    `json:"step_index"`
		State     string `json:"state"`
	}
	c3.get(t, fmt.Sprintf("/v1/tasks/%d/steps", finished.ID), &steps)
	var succeeded bool
	for _, s := range steps {
		if s.State == "succeeded" {
			succeeded = true
		}
	}
	if !succeeded {
		t.Errorf("restored step runs = %+v, want the succeeded attempt", steps)
	}

	c3.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, third)
}

// TestBackupRefusesWithoutADaemonE2E: `backup` is a write of daemon-owned
// state, so it follows `doctor --fix`'s policy — and says so, with the
// no-binary fallback, rather than leaving a user to guess.
func TestBackupRefusesWithoutADaemonE2E(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")

	out, code := runVincent(t, dataDir, cfgDir, "daemon", "backup", dst)
	if code != 2 {
		t.Fatalf("backup with no daemon: code %d, out %q; want 2", code, out)
	}
	if !strings.Contains(out, "needs a running daemon") {
		t.Errorf("backup with no daemon printed %q, want the policy line", out)
	}
	if !strings.Contains(out, "vincent.db-wal") {
		t.Errorf("backup with no daemon printed %q, want the stopped-daemon fallback", out)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("a refused backup still wrote the destination")
	}
}

// TestRestoreRefusalsE2E covers the refusals that need no daemon at all. The
// archives are built here rather than taken from a running instance: a
// manifest from the future cannot be produced by this binary.
func TestRestoreRefusalsE2E(t *testing.T) {
	dbCopy := filepath.Join(t.TempDir(), backup.DatabaseEntry)
	if err := os.WriteFile(dbCopy, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	src := backup.Dirs{Data: t.TempDir(), Config: t.TempDir()}
	if err := os.WriteFile(filepath.Join(src.Config, config.FileName), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	makeArchive := func(t *testing.T, schema int) string {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "backup.tar.gz")
		if _, err := backup.Create(dst, backup.Source{
			Database: dbCopy, DataDir: src.Data, ConfigDir: src.Config,
			Manifest: backup.Manifest{
				VincentVersion: "v99.0.0",
				SchemaVersion:  schema,
				CreatedAt:      backup.FormatTime(time.Now()),
			},
		}); err != nil {
			t.Fatalf("create archive: %v", err)
		}
		return dst
	}

	t.Run("schema from the future", func(t *testing.T) {
		archive := makeArchive(t, store.NewestMigration()+1)
		dataDir, cfgDir := t.TempDir(), t.TempDir()
		out, code := runVincent(t, dataDir, cfgDir, "daemon", "restore", archive)
		if code != 1 {
			t.Fatalf("restore of a newer schema: code %d, out %q; want 1", code, out)
		}
		if !strings.Contains(out, "schema version") || !strings.Contains(out, "v99.0.0") {
			t.Errorf("restore printed %q, want the schema versions and the writing binary", out)
		}
		if _, err := os.Stat(filepath.Join(dataDir, backup.DatabaseEntry)); err == nil {
			t.Error("a refused restore still wrote the database")
		}
	})

	t.Run("not a backup", func(t *testing.T) {
		notAnArchive := filepath.Join(t.TempDir(), "notes.tar.gz")
		if err := os.WriteFile(notAnArchive, []byte("just some text"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		out, code := runVincent(t, t.TempDir(), t.TempDir(), "daemon", "restore", notAnArchive)
		if code != 1 {
			t.Fatalf("restore of a non-archive: code %d, out %q; want 1", code, out)
		}
	})

	t.Run("occupied without force", func(t *testing.T) {
		archive := makeArchive(t, store.NewestMigration())
		dataDir, cfgDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, backup.DatabaseEntry),
			[]byte("the database that was here"), 0o600); err != nil {
			t.Fatalf("seed database: %v", err)
		}
		out, code := runVincent(t, dataDir, cfgDir, "daemon", "restore", archive)
		if code != 1 || !strings.Contains(out, "--force") {
			t.Fatalf("restore over existing state: code %d, out %q; want 1 and a --force pointer", code, out)
		}
		body, err := os.ReadFile(filepath.Join(dataDir, backup.DatabaseEntry))
		if err != nil || string(body) != "the database that was here" {
			t.Fatalf("a refused restore changed the database: %q (%v)", body, err)
		}
	})

	t.Run("force displaces and deletes nothing", func(t *testing.T) {
		archive := makeArchive(t, store.NewestMigration())
		dataDir, cfgDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, backup.DatabaseEntry),
			[]byte("the database that was here"), 0o600); err != nil {
			t.Fatalf("seed database: %v", err)
		}
		out, code := runVincent(t, dataDir, cfgDir, "daemon", "restore", archive, "--force", "--json")
		if code != 0 {
			t.Fatalf("restore --force: code %d, out %q", code, out)
		}
		var rep restoreReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("restore --json is not JSON: %v (%q)", err, out)
		}
		if len(rep.Displaced) != 1 {
			t.Fatalf("displaced = %+v, want the one occupied path", rep.Displaced)
		}
		// The assertion is on the bytes, not on a name: a rename that
		// clobbered the old file would pass the weaker check.
		body, err := os.ReadFile(rep.Displaced[0].To)
		if err != nil || string(body) != "the database that was here" {
			t.Fatalf("displaced state at %s = %q (%v), want the pre-restore bytes",
				rep.Displaced[0].To, body, err)
		}
		restored, err := os.ReadFile(filepath.Join(dataDir, backup.DatabaseEntry))
		if err != nil || !strings.HasPrefix(string(restored), "SQLite format 3") {
			t.Fatalf("restored database = %q (%v)", restored, err)
		}
	})
}

// addTask creates a task through the CLI and returns its id.
func addTask(t *testing.T, dataDir, cfgDir, title string) int64 {
	t.Helper()
	out, code := runVincent(t, dataDir, cfgDir,
		"task", "add", "--project", "1", "--title", title, "--json")
	if code != 0 {
		t.Fatalf("task add %q: code %d, out %q", title, code, out)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
	}
	return created.ID
}

func waitTaskState(t *testing.T, dataDir, cfgDir string, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		out, code := runVincent(t, dataDir, cfgDir, "task", "ls", "--json")
		if code == 0 {
			var tasks []e2eTask
			if err := json.Unmarshal([]byte(out), &tasks); err == nil {
				for _, task := range tasks {
					if task.ID != id {
						continue
					}
					if task.State == want {
						return
					}
					if task.State == "blocked" || task.State == "aborted" {
						t.Fatalf("task %d ended %s, want %s", id, task.State, want)
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("task %d never reached %s", id, want)
}
