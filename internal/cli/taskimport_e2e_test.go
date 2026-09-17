package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
)

// importShown is the slice of `task show --json` this file asserts on.
type importShown struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Steps []struct {
		ID        int64 `json:"id"`
		StepIndex int   `json:"step_index"`
	} `json:"steps"`
}

func showImported(t *testing.T, dataDir, cfgDir string, id int64) importShown {
	t.Helper()
	out, code := runVincent(t, dataDir, cfgDir, "task", "show", strconv.FormatInt(id, 10), "--json")
	if code != 0 {
		t.Fatalf("task show %d: code %d, out %q", id, code, out)
	}
	var shown importShown
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
	}
	return shown
}

func writeImportConfig(t *testing.T, cfgDir, fake string) {
	t.Helper()
	cfg := fmt.Sprintf("listen: \"127.0.0.1:0\"\nagents:\n  claude:\n    path: %q\n", fake)
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestTaskImportRoundTripE2E is task 117's acceptance through the real binary:
// back up an archived task, delete it, import it back with its id, its step
// runs and its transcript bytes; then import the same archive into another
// installation whose step run ids collide.
func TestTaskImportRoundTripE2E(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	writeImportConfig(t, cfgDir, fake)
	repo := testrepo.Init(t, "main")

	d1 := startDaemonProcess(t, dataDir, cfgDir, "success")
	c1 := waitDaemonAPI(t, dataDir, d1)
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	// The second task is the one imported, so its step run ids start past 1.
	waitTaskState(t, dataDir, cfgDir, addTask(t, dataDir, cfgDir, "first"), "done")
	id := addTask(t, dataDir, cfgDir, "brought back")
	waitTaskState(t, dataDir, cfgDir, id, "done")
	idText := strconv.FormatInt(id, 10)
	if out, code := runVincent(t, dataDir, cfgDir, "task", "archive", idText); code != 0 {
		t.Fatalf("task archive: code %d, out %q", code, out)
	}
	before := showImported(t, dataDir, cfgDir, id)
	if len(before.Steps) == 0 {
		t.Fatalf("task %d has no step runs to import", id)
	}
	transcript, code := runVincent(t, dataDir, cfgDir, "task", "transcript", idText, "--raw")
	if code != 0 || transcript == "" {
		t.Fatalf("task transcript before delete: code %d, out %q", code, transcript)
	}

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "backup", archive); code != 0 {
		t.Fatalf("daemon backup: code %d, out %q", code, out)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "task", "delete", idText); code != 0 {
		t.Fatalf("task delete: code %d, out %q", code, out)
	}

	out, code := runVincent(t, dataDir, cfgDir, "task", "import", archive, idText)
	if code != 0 {
		t.Fatalf("task import: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "imported task "+idText) || !strings.Contains(out, "ids kept") {
		t.Errorf("task import printed %q", out)
	}
	after := showImported(t, dataDir, cfgDir, id)
	if after.ID != id || after.State != "archived" || !slices.Equal(after.Steps, before.Steps) {
		t.Errorf("imported task = %+v, want %+v archived", after, before)
	}
	if got, code := runVincent(t, dataDir, cfgDir, "task", "transcript", idText, "--raw"); code != 0 || got != transcript {
		t.Errorf("task transcript after import: code %d, out %q; want the original %q", code, got, transcript)
	}

	out, code = runVincent(t, dataDir, cfgDir, "task", "import", archive, idText)
	if code != 1 || !strings.Contains(out, "already exists") {
		t.Errorf("second import: code %d, out %q; want 1 and task_exists", code, out)
	}

	c1.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, d1)

	// No daemon: the import cannot open the database, and says so.
	out, code = runVincent(t, dataDir, cfgDir, "task", "import", archive, idText)
	if code != 2 || !strings.Contains(out, "needs a running daemon") {
		t.Errorf("import with no daemon: code %d, out %q; want 2 and the policy line", code, out)
	}

	// Another installation, same repository (so the same project name and
	// id), whose one task has at least as many step runs as the imported
	// task's highest id: those ids are taken, and the task id is free.
	var highest int64
	for _, s := range before.Steps {
		highest = max(highest, s.ID)
	}
	data2, cfg2 := t.TempDir(), t.TempDir()
	writeImportConfig(t, cfg2, fake)
	var wf strings.Builder
	wf.WriteString("name: many\ndescription: enough attempts to take the ids\nsteps:\n")
	for i := range highest {
		fmt.Fprintf(&wf, "  - id: s%d\n    type: command\n    run: \"exit 0\"\n", i)
	}
	if err := os.MkdirAll(filepath.Join(cfg2, "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg2, "workflows", "many.yaml"), []byte(wf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d2 := startDaemonProcess(t, data2, cfg2, "success")
	c2 := waitDaemonAPI(t, data2, d2)
	if out, code := runVincent(t, data2, cfg2, "project", "add", repo); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	out, code = runVincent(t, data2, cfg2,
		"task", "add", "--project", "1", "--title", "occupant", "--workflow", "many", "--json")
	if code != 0 {
		t.Fatalf("task add: code %d, out %q", code, out)
	}
	var occupant struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &occupant); err != nil || occupant.ID == id {
		t.Fatalf("occupant task = %q (%v); its id must not be %d", out, err, id)
	}
	waitTaskState(t, data2, cfg2, occupant.ID, "done")

	out, code = runVincent(t, data2, cfg2, "task", "import", archive, idText, "--json")
	if code != 0 {
		t.Fatalf("cross-installation import: code %d, out %q", code, out)
	}
	var res struct {
		TaskID             int64 `json:"task_id"`
		StepRunsRenumbered bool  `json:"step_runs_renumbered"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("task import --json is not JSON: %v (%q)", err, out)
	}
	if res.TaskID != id || !res.StepRunsRenumbered {
		t.Errorf("cross-installation import = %+v, want task %d with step runs renumbered", res, id)
	}
	imported := showImported(t, data2, cfg2, id)
	if len(imported.Steps) != len(before.Steps) || imported.Steps[0].ID == before.Steps[0].ID {
		t.Errorf("imported step runs = %+v, want %d renumbered", imported.Steps, len(before.Steps))
	}
	// The transcript path points into this data dir: the file is here, and
	// the route serves the original bytes from it.
	if _, err := os.Stat(filepath.Join(data2, backup.TranscriptsPrefix, idText)); err != nil {
		t.Errorf("transcripts were not placed: %v", err)
	}
	if got, code := runVincent(t, data2, cfg2, "task", "transcript", idText, "--raw"); code != 0 || got != transcript {
		t.Errorf("cross-installation transcript: code %d, out %q; want %q", code, got, transcript)
	}

	c2.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, d2)
}
