package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestStatusFromInsideAStep is task 033's whole claim, driven end to end
// through the real binary: an agent step runs `vincent status`, and the
// message it wrote reaches the row, `vincent task show` and `--json`.
//
// It goes through the fake agent rather than calling the command directly
// because the interesting part is the addressing: §8.5's VINCENT_TASK_ID and
// VINCENT_STEP_ID have to reach an *agent* step's environment for the command
// to know which row it is. Calling it from the test's own shell would prove
// the endpoint and skip the reason the endpoint is reachable at all.
func TestStatusFromInsideAStep(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	// The daemon reads the scenario from its own environment and hands it to
	// the step through the §12.3 policy, so this is set before it starts.
	t.Setenv("FAKEAGENT_SCENARIO", "set-status")
	t.Setenv("FAKEAGENT_VINCENT_BIN", vincentBin)
	t.Setenv("FAKEAGENT_STATUS", "scaffolding the migration")
	// Past the daemon's one-second coalescing floor (§13.3), so the second
	// message is a write in its own right rather than one folded into the
	// first — which is how a real step that narrates its work behaves.
	t.Setenv("FAKEAGENT_STATUS_HOLD_MS", "1500")
	t.Setenv("FAKEAGENT_STATUS_FINAL", "3 tests red in internal/store")

	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("agents:\n  claude:\n    path: \""+filepath.ToSlash(fake)+"\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "workflows"), 0o700); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "workflows", "narrate.yaml"), []byte(
		"name: narrate\ndescription: one agent step that reports on itself.\n"+
			"defaults:\n  agent: claude\n  max_retries: 0\nsteps:\n"+
			"  - id: implement\n    type: agent\n    prompt: report your status\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	repo := testrepo.Init(t, "main")
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	out, code := runVincent(t, dataDir, cfgDir, "task", "add",
		"--project", "1", "--workflow", "narrate", "--title", "narrating task", "--json")
	if code != 0 {
		t.Fatalf("task add: code %d, out %q", code, out)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == 0 {
		t.Fatalf("task add --json: %v (%q)", err, out)
	}
	id := strconv.FormatInt(created.ID, 10)

	// Wait for the run to settle, then read the row the step wrote to.
	var detail struct {
		State string `json:"state"`
		Steps []struct {
			StepID        string  `json:"step_id"`
			State         string  `json:"state"`
			StatusMessage *string `json:"status_message"`
		} `json:"steps"`
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		out, code = runVincent(t, dataDir, cfgDir, "task", "show", id, "--json")
		if code != 0 {
			t.Fatalf("task show --json: code %d, out %q", code, out)
		}
		if err := json.Unmarshal([]byte(out), &detail); err != nil {
			t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
		}
		if detail.State == "done" || detail.State == "blocked" || detail.State == "aborted" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if detail.State != "done" {
		t.Fatalf("task state = %q, want done (steps %+v)", detail.State, detail.Steps)
	}
	if len(detail.Steps) != 1 {
		t.Fatalf("step runs = %d, want 1", len(detail.Steps))
	}
	got := detail.Steps[0].StatusMessage
	if got == nil {
		t.Fatalf("status_message is null: `vincent status` never reached the row (%q)", out)
	}
	// The *last* value set before the attempt ended is what stays on the
	// finished row — the terminal half of the feature.
	if *got != "3 tests red in internal/store" {
		t.Errorf("status_message = %q, want the last value the step set", *got)
	}

	// And the human-readable table carries it under its own column, distinct
	// from REASON.
	out, code = runVincent(t, dataDir, cfgDir, "task", "show", id)
	if code != 0 {
		t.Fatalf("task show: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "STATUS") || !strings.Contains(out, "3 tests red in internal/store") {
		t.Errorf("`vincent task show` does not render the status:\n%s", out)
	}
}

// Outside a step there is nothing to address, and the error has to say so
// rather than reporting a daemon problem: the overwhelmingly likely cause is
// that someone typed the command at a shell.
func TestStatusOutsideAStep(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	// Pin the hostile environment rather than hoping for a clean one. This
	// suite really is run from inside a vincent step, and inheriting §8.5's
	// block made the child address the *outer* task and report a daemon
	// problem instead. `runVincent` strips it; setting it here is what asserts
	// that, and what makes the regression reproduce off a dev machine.
	t.Setenv(envTaskID, "4242")
	t.Setenv(envStepID, "outer-step")
	out, code := runVincent(t, dataDir, cfgDir, "status", "hello")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out, "VINCENT_TASK_ID") || !strings.Contains(out, "from inside a step") {
		t.Errorf("error does not name the missing variables:\n%s", out)
	}
}
