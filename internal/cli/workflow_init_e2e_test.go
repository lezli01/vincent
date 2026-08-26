package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestWorkflowInitE2EWithoutDaemon is the cold start the command exists for:
// a user with the binary on their PATH, no daemon running, no checkout of
// this repository, and nothing in the config directory yet. It has to end
// with a file `vincent workflow validate` accepts.
//
// It runs through the real binary rather than in-process because "no daemon"
// is the claim under test, and only a separate process proves nothing was
// auto-started along the way.
func TestWorkflowInitE2EWithoutDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()

	out, code := runVincent(t, dataDir, cfgDir, "workflow", "init", "my-flow")
	if code != 0 {
		t.Fatalf("workflow init: code %d, out %q", code, out)
	}
	path := filepath.Join(cfgDir, workflow.GlobalDirName, "my-flow.yaml")
	if !strings.Contains(out, path) {
		t.Fatalf("workflow init printed %q, want the path %q", out, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing at the printed path: %v", err)
	}
	// No daemon was started along the way: nothing wrote a daemon.json.
	if _, err := os.Stat(filepath.Join(dataDir, "daemon.json")); !os.IsNotExist(err) {
		t.Errorf("workflow init started or recorded a daemon (%v)", err)
	}

	out, code = runVincent(t, dataDir, cfgDir, "workflow", "validate", path)
	if code != 0 {
		t.Fatalf("the file init wrote does not validate: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "my-flow") || strings.Contains(out, "invalid") {
		t.Errorf("validate says %q", out)
	}
}

// TestWorkflowInitE2ELiveReload: the daemon picks the new file up with no
// restart. The registry watcher already covers both directories, so what
// this proves is that `init` writes where the watcher is looking — not that
// the watcher works, which watch_test.go owns.
func TestWorkflowInitE2ELiveReload(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	// Point the adapters at nothing: no step ever runs here, and a machine
	// that happens to have claude installed must not change the outcome.
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(
		"agents:\n  claude:\n    path: \"/nonexistent/claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	repo := testrepo.Init(t, "main")
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}

	if out, code := runVincent(t, dataDir, cfgDir, "workflow", "init", "global-flow"); code != 0 {
		t.Fatalf("workflow init: code %d, out %q", code, out)
	}
	if out, code := runVincent(t, dataDir, cfgDir,
		"workflow", "init", "project-flow", "--project", "1", "--from", "fix-and-test"); code != 0 {
		t.Fatalf("workflow init --project: code %d, out %q", code, out)
	}

	// The watcher debounces a save by 100ms; poll rather than sleep once, so
	// a loaded CI runner is slow rather than flaky.
	deadline := time.Now().Add(15 * time.Second)
	var out string
	for {
		var code int
		out, code = runVincent(t, dataDir, cfgDir, "workflow", "ls", "--project", "1")
		if code != 0 {
			t.Fatalf("workflow ls: code %d, out %q", code, out)
		}
		if strings.Contains(out, "global-flow") && strings.Contains(out, "project-flow") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registry never picked the new files up without a restart:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Both are valid entries in the scope init chose, not invalid ones.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "global-flow") && !strings.Contains(line, "global") {
			t.Errorf("global-flow is not in the global scope: %q", line)
		}
		if strings.HasPrefix(line, "project-flow") && !strings.Contains(line, "project") {
			t.Errorf("project-flow is not in the project scope: %q", line)
		}
		if strings.HasPrefix(line, "global-flow") || strings.HasPrefix(line, "project-flow") {
			if strings.Contains(line, "invalid") {
				t.Errorf("init wrote a file the daemon rejects: %q", line)
			}
		}
	}
}
