package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestTaskShowStepFromRealDaemon is task 100's proof that `task show --step`
// reads what the engine actually recorded, not only what a stub serves: a
// command step runs to completion against a real daemon, and the text view
// carries its rendered `run` body, the shell it ran under and its working
// directory. The body stays inside the sh ∩ pwsh intersection (`git ...`).
func TestTaskShowStepFromRealDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("agents:\n  claude:\n    path: \"/nonexistent/claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repo := testrepo.Init(t, "main")
	writeActionWorkflow(t, repo, "probe", "steps:\n  - id: probe\n    type: command\n    run: git --version\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "workflows")

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	id := addActionTask(t, dataDir, cfgDir, "probe task", "--workflow", "probe")
	waitForState(t, dataDir, cfgDir, id, "done")

	out, code := runVincent(t, dataDir, cfgDir, "task", "show", id, "--json")
	if code != 0 {
		t.Fatalf("task show --json: code %d, out %q", code, out)
	}
	var detail struct {
		Steps []struct {
			ID int64 `json:"id"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &detail); err != nil || len(detail.Steps) != 1 {
		t.Fatalf("task show --json: %v, want one step run (%q)", err, out)
	}
	run := strconv.FormatInt(detail.Steps[0].ID, 10)

	out, code = runVincent(t, dataDir, cfgDir, "task", "show", id, "--step", run)
	if code != 0 {
		t.Fatalf("task show --step: code %d, out %q", code, out)
	}
	out = strings.ReplaceAll(out, "\r\n", "\n")
	if !strings.Contains(out, "  rendered run:\n    git --version\n") {
		t.Errorf("the rendered run body is missing:\n%s", out)
	}
	for _, label := range []string{"shell", "working dir"} {
		line := regexp.MustCompile(`(?m)^  ` + label + ` +(.+)$`).FindStringSubmatch(out)
		if line == nil || strings.HasPrefix(line[1], "not recorded") {
			t.Errorf("%s was not recorded by the engine:\n%s", label, out)
		}
	}
}
